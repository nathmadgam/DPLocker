// DPLocker.exe: a portable, offline file locker for USB drives. It serves a Drive-style page on
// 127.0.0.1, saves uploads into folders next to the exe, and quits a short
// while after the browser tab is closed.
package main

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

const (
	maxFileSizeMB    = 25
	idleTimeout      = 90 * time.Second
	quitGracePeriod  = 3 * time.Second
	startupTimeout   = 2 * time.Minute
	preferredAddress = "127.0.0.1:47219"
	trashFolder      = "Trash"
	appVersion       = "1.0.0"
	dataFolder       = "DPLocker Data"
	legacyDataFolder = "Backup Data"
)

type category struct {
	Folder     string   `json:"folder"`
	Extensions []string `json:"extensions"`
	Color      string   `json:"color"`
}

// Allow-list on purpose: blocking known-bad extensions misses new ones.
var categories = []category{
	{"PDFs", []string{"pdf"}, "var(--type-pdf)"},
	{"Word Documents", []string{"doc", "docx", "odt", "rtf"}, "var(--type-doc)"},
	{"Spreadsheets", []string{"xls", "xlsx", "ods", "csv"}, "var(--type-sheet)"},
	{"Presentations", []string{"ppt", "pptx", "odp"}, "var(--type-slides)"},
	{"Text Files", []string{"txt", "md"}, "var(--type-text)"},
}

type fileEntry struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Month    string `json:"month"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

type driveInfo struct {
	Path      string `json:"path"`
	Label     string `json:"label"`
	Total     uint64 `json:"total"`
	Free      uint64 `json:"free"`
	HasLocker bool   `json:"hasLocker"`
}

var (
	exeDirectory    string
	storageBase     string
	portable        bool
	storageMutex    sync.Mutex
	rootDirectory   string
	token           string
	lastPing        time.Time
	quitRequestedAt time.Time
	pingMutex       sync.Mutex
	writeMutex      sync.Mutex
)

func categoryForName(name string) *category {
	extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	for index := range categories {
		for _, allowed := range categories[index].Extensions {
			if allowed == extension {
				return &categories[index]
			}
		}
	}
	return nil
}

// Rejects anything that could escape the backup folders (separators, "..").
func safeComponent(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.ContainsAny(value, `/\:*?"<>|`)
}

func cleanFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	replacer := strings.NewReplacer(":", "_", "*", "_", "?", "_", `"`, "_", "<", "_", ">", "_", "|", "_")
	return strings.TrimSpace(replacer.Replace(name))
}

func uniquePath(directory, name string) string {
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	candidate := filepath.Join(directory, name)
	for counter := 2; ; counter++ {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
		candidate = filepath.Join(directory, fmt.Sprintf("%s (%d)%s", base, counter, extension))
	}
}

func requireToken(next http.HandlerFunc) http.HandlerFunc {
	// The token stops other websites open in the browser from calling this local API.
	return func(writer http.ResponseWriter, request *http.Request) {
		provided := request.Header.Get("X-Token")
		if provided == "" {
			provided = request.URL.Query().Get("t")
		}
		if provided != token {
			http.Error(writer, "forbidden", http.StatusForbidden)
			return
		}
		pingMutex.Lock()
		lastPing = time.Now()
		pingMutex.Unlock()
		next(writer, request)
	}
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	json.NewEncoder(writer).Encode(value)
}

func handleConfig(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, map[string]any{
		"categories":    categories,
		"maxFileSizeMB": maxFileSizeMB,
		"location":      storageBase,
		"portable":      portable,
		"version":       appVersion,
	})
}

// Validates a request's category/month/name against the allow-list; the vault, not the
// filesystem, resolves them to a stored file.
func resolveFile(request *http.Request) (category, month, name string, trashed, ok bool) {
	query := request.URL.Query()
	category, month, name = query.Get("category"), query.Get("month"), query.Get("name")
	trashed = query.Get("trash") == "1"
	valid := false
	for _, item := range categories {
		if item.Folder == category {
			valid = true
		}
	}
	if !valid || !safeComponent(month) || !safeComponent(name) {
		return "", "", "", false, false
	}
	return category, month, name, trashed, true
}

func handleList(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, vaultList(request.URL.Query().Get("trash") == "1"))
}

func handleDelete(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	category, month, name, trashed, ok := resolveFile(request)
	if !ok {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	if trashed {
		err = purgeEntry(category, month, name)
	} else {
		err = setTrashed(category, month, name, false, true)
	}
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(writer, map[string]any{"ok": true})
}

func handleRestore(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Query().Get("trash") != "1" {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	category, month, name, _, ok := resolveFile(request)
	if !ok {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	if err := setTrashed(category, month, name, true, false); err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(writer, map[string]any{"ok": true})
}

func handleUpload(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxFileSizeMB*1024*1024+1024*1024)
	file, header, err := request.FormFile("file")
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": fmt.Sprintf("larger than %d MB", maxFileSizeMB)})
		return
	}
	defer file.Close()

	name := cleanFileName(header.Filename)
	item := categoryForName(name)
	if item == nil || !safeComponent(name) {
		writeJSON(writer, map[string]any{"ok": false, "message": "file type not allowed"})
		return
	}
	content, err := io.ReadAll(io.LimitReader(file, maxFileSizeMB*1024*1024+1))
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "upload interrupted"})
		return
	}
	if int64(len(content)) > maxFileSizeMB*1024*1024 {
		writeJSON(writer, map[string]any{"ok": false, "message": fmt.Sprintf("larger than %d MB", maxFileSizeMB)})
		return
	}

	// Metadata is cleaned on a throwaway temp copy before anything is encrypted or stored.
	cleaned := false
	if loadSettings().AutoClean && metadataFormat(name) != "" {
		if temporary, err := os.CreateTemp("", "dpl-*"+filepath.Ext(name)); err == nil {
			path := temporary.Name()
			temporary.Write(content)
			temporary.Close()
			if writeMetadata(path, nil, true) == nil {
				if scrubbed, err := os.ReadFile(path); err == nil {
					content = scrubbed
					cleaned = true
				}
			}
			shredFile(path)
		}
	}

	month := time.Now().Format("2006-01")
	entry, err := vaultAdd(name, item.Folder, month, content)
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "could not save (drive full or removed?)"})
		return
	}
	writeJSON(writer, map[string]any{"ok": true, "saved": entry.Name, "cleaned": cleaned})
}

func handleOpen(writer http.ResponseWriter, request *http.Request) {
	category, month, name, trashed, ok := resolveFile(request)
	if !ok {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	content, err := readEntryContent(category, month, name, trashed)
	if err != nil {
		http.Error(writer, "not found", http.StatusNotFound)
		return
	}
	if request.URL.Query().Get("download") == "1" {
		writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	}
	http.ServeContent(writer, request, name, time.Now(), bytes.NewReader(content))
}

// Points DPLocker at the locker on base (a drive root or the portable exe's folder).
func useStorage(base string) error {
	storageMutex.Lock()
	defer storageMutex.Unlock()
	directory := filepath.Join(base, dataFolder)
	// Earlier builds used "Backup Data"; rename it so existing files and login carry over.
	if _, err := os.Stat(directory); os.IsNotExist(err) {
		os.Rename(filepath.Join(base, legacyDataFolder), directory)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	hideFolder(directory)
	storageBase = base
	rootDirectory = directory
	adoptOldFolders()
	return nil
}

// One drive, or exactly one drive that already holds a locker, needs no question.
func preferredDrive(drives []driveInfo) string {
	if len(drives) == 1 {
		return drives[0].Path
	}
	withLocker := []string{}
	for _, drive := range drives {
		if hasLocker(drive.Path) {
			withLocker = append(withLocker, drive.Path)
		}
	}
	if len(withLocker) == 1 {
		return withLocker[0]
	}
	return ""
}

func hasLocker(base string) bool {
	for _, folder := range []string{dataFolder, legacyDataFolder} {
		if info, err := os.Stat(filepath.Join(base, folder)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// Earlier versions saved category folders straight next to the exe; move them
// into the hidden folder so existing backups keep showing up.
func adoptOldFolders() {
	folders := []string{trashFolder}
	for _, item := range categories {
		folders = append(folders, item.Folder)
	}
	for _, folder := range folders {
		oldPath := filepath.Join(storageBase, folder)
		newPath := filepath.Join(rootDirectory, folder)
		if info, err := os.Stat(oldPath); err != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Stat(newPath); os.IsNotExist(err) {
			os.Rename(oldPath, newPath)
		}
	}
}

func openBrowser(url string) {
	// rundll32 opens the default browser without flashing a console window.
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func randomToken() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func main() {
	executable, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	exeDirectory = filepath.Dir(executable)
	finishPendingUpdate()
	// Locked files only ever live on a removable drive. Portable: the drive the exe is on.
	// Installed (Program Files): a plugged-in USB drive, chosen in the window if there are several.
	portable = isRemovableDrive(exeDirectory)
	if portable {
		if err := useStorage(exeDirectory); err != nil {
			showError("DPLocker can’t start", "DPLocker couldn’t create its folder on this drive.\n\n"+err.Error())
			os.Exit(1)
		}
	} else {
		drives := removableDrives()
		if len(drives) == 0 {
			showError("Plug in your USB drive",
				"DPLocker keeps your files only on a USB drive, never on this PC.\n\n"+
					"Plug in your USB flash drive, then open DPLocker again.")
			os.Exit(1)
		}
		if choice := preferredDrive(drives); choice != "" {
			if err := useStorage(choice); err != nil {
				showError("DPLocker can’t start", "DPLocker couldn’t open "+choice+"\n\n"+err.Error())
				os.Exit(1)
			}
		}
	}
	token = randomToken()

	// A fixed port keeps the page's origin stable so the browser remembers the chosen layout;
	// fall back to any free port if another copy or program is using it.
	listener, err := net.Listen("tcp", preferredAddress)
	if err != nil {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Write(indexHTML)
	})
	mux.HandleFunc("/api/drives", requireToken(handleDrives))
	mux.HandleFunc("/api/license", requireToken(handleLicense))
	mux.HandleFunc("/api/auth/status", requireToken(handleAuthStatus))
	mux.HandleFunc("/api/auth/setup-begin", requireToken(handleSetupBegin))
	mux.HandleFunc("/api/auth/setup-finish", requireToken(handleSetupFinish))
	mux.HandleFunc("/api/auth/login", requireToken(handleLogin))
	mux.HandleFunc("/api/auth/logout", requireToken(handleLogout))
	mux.HandleFunc("/api/auth/change-password", requireSession(handleChangePassword))
	mux.HandleFunc("/api/auth/relink-begin", requireSession(handleSetupBegin))
	mux.HandleFunc("/api/auth/relink-finish", requireSession(handleRelinkFinish))
	mux.HandleFunc("/api/config", requireSession(handleConfig))
	mux.HandleFunc("/api/settings", requireSession(handleSettings))
	mux.HandleFunc("/api/storage", requireSession(handleStorage))
	mux.HandleFunc("/api/metadata", requireSession(handleMetadata))
	mux.HandleFunc("/api/rename", requireSession(handleRename))
	mux.HandleFunc("/api/launch", requireSession(handleLaunch))
	mux.HandleFunc("/api/update/check", requireSession(handleUpdateCheck))
	mux.HandleFunc("/api/update/install", requireSession(handleUpdateInstall))
	mux.HandleFunc("/api/external", requireToken(handleExternal))
	mux.HandleFunc("/api/files", requireSession(handleList))
	mux.HandleFunc("/api/upload", requireSession(handleUpload))
	mux.HandleFunc("/api/open", requireSession(handleOpen))
	mux.HandleFunc("/api/delete", requireSession(handleDelete))
	mux.HandleFunc("/api/restore", requireSession(handleRestore))
	mux.HandleFunc("/api/ping", requireToken(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("/api/quit", requireToken(func(writer http.ResponseWriter, request *http.Request) {
		pingMutex.Lock()
		quitRequestedAt = time.Now()
		pingMutex.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	}))

	// The page beacons /api/quit when its tab closes. A short grace period lets a
	// page refresh ping again and cancel it. The idle timeout is only a fallback
	// for a browser crash, and is long because Chrome throttles timers in
	// background tabs to about once a minute.
	started := time.Now()
	go func() {
		for range time.Tick(time.Second) {
			pingMutex.Lock()
			quitting := !quitRequestedAt.IsZero() && !lastPing.After(quitRequestedAt) &&
				time.Since(quitRequestedAt) > quitGracePeriod
			idle := lastPing.IsZero() && time.Since(started) > startupTimeout ||
				!lastPing.IsZero() && time.Since(lastPing) > idleTimeout
			pingMutex.Unlock()
			if quitting || idle {
				os.Exit(0)
			}
		}
	}()

	url := fmt.Sprintf("http://%s/?t=%s", listener.Addr().String(), token)
	if os.Getenv("DPLOCKER_DEBUG") != "" {
		fmt.Println(url)
	}
	go http.Serve(listener, mux)
	if os.Getenv("DPLOCKER_DEBUG") == "" && runWindow(url) {
		// Closing the window ends the app immediately.
		os.Exit(0)
	}
	// No WebView2 runtime (or a test build): fall back to the default browser.
	openBrowser(url)
	select {}
}
