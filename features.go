// Metadata, rename, settings and storage endpoints.
package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const settingsFileName = "settings.json"

type appSettings struct {
	AutoClean           bool   `json:"autoClean"`
	WarningDismissed    bool   `json:"warningDismissed"`
	UpdateCheckDisabled bool   `json:"updateCheckDisabled"`
	SkippedVersion      string `json:"skippedVersion"`
}

func settingsPath() string {
	return filepath.Join(rootDirectory, settingsFileName)
}

func loadSettings() appSettings {
	settings := appSettings{}
	if bytes, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(bytes, &settings)
	}
	return settings
}

func saveSettings(settings appSettings) error {
	bytes, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	temporary := settingsPath() + ".tmp"
	if err := os.WriteFile(temporary, bytes, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, settingsPath())
}

func handleSettings(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPost {
		var settings appSettings
		if !readJSON(request, &settings) {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		writeMutex.Lock()
		err := saveSettings(settings)
		writeMutex.Unlock()
		if err != nil {
			writeJSON(writer, map[string]any{"ok": false, "message": err.Error()})
			return
		}
	}
	writeJSON(writer, loadSettings())
}

func handleStorage(writer http.ResponseWriter, request *http.Request) {
	total, free := diskSpace(storageBase)
	writeJSON(writer, map[string]any{"total": total, "free": free, "used": vaultTotalSize()})
}

func handleMetadata(writer http.ResponseWriter, request *http.Request) {
	category, month, name, trashed, ok := resolveFile(request)
	if !ok {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	switch request.Method {
	case http.MethodGet:
		var result *metadataResult
		err := withDecryptedTemp(category, month, name, trashed, false, func(path string) error {
			parsed, parseErr := readMetadata(path)
			result = parsed
			return parseErr
		})
		if err != nil {
			writeJSON(writer, map[string]any{"supported": false, "notes": []string{err.Error()}})
			return
		}
		writeJSON(writer, result)
	case http.MethodPost:
		var body struct {
			Fields map[string]string `json:"fields"`
			Clean  bool              `json:"clean"`
		}
		if !readJSON(request, &body) {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		// Decrypt to a temp file, edit it, re-encrypt the result back into the vault.
		err := withDecryptedTemp(category, month, name, false, true, func(path string) error {
			return writeMetadata(path, body.Fields, body.Clean)
		})
		if err != nil {
			writeJSON(writer, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		writeJSON(writer, map[string]any{"ok": true})
	default:
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Renames an entry. The extension is kept so a file can't be renamed into a blocked
// type or out of its category.
func handleRename(writer http.ResponseWriter, request *http.Request) {
	category, month, name, _, ok := resolveFile(request)
	var body struct {
		NewName string `json:"newName"`
	}
	if !ok || request.Method != http.MethodPost || !readJSON(request, &body) {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(cleanFileName(body.NewName), filepath.Ext(body.NewName))
	if !strings.EqualFold(filepath.Ext(body.NewName), extension) {
		base = cleanFileName(body.NewName)
	}
	newName := strings.TrimSpace(base) + extension
	if !safeComponent(newName) || strings.TrimSpace(base) == "" {
		writeJSON(writer, map[string]any{"ok": false, "message": "That name isn’t allowed."})
		return
	}
	if newName == name {
		writeJSON(writer, map[string]any{"ok": true, "name": name})
		return
	}
	final, err := renameEntry(category, month, name, newName)
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(writer, map[string]any{"ok": true, "name": final})
}

// Decrypts the file to a temporary copy and opens it in its default app. The temp copy
// is shredded a few minutes later. Edits are NOT saved back into the locker — the user
// re-adds an edited file.
func handleLaunch(writer http.ResponseWriter, request *http.Request) {
	category, month, name, trashed, ok := resolveFile(request)
	if !ok || request.Method != http.MethodPost {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	content, err := readEntryContent(category, month, name, trashed)
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "file not found"})
		return
	}
	temporary, err := os.CreateTemp("", "dpl-open-*"+filepath.Ext(name))
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "could not open a temporary copy"})
		return
	}
	path := temporary.Name()
	temporary.Write(content)
	temporary.Close()
	if err := launchFile(path); err != nil {
		shredFile(path)
		writeJSON(writer, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	// The app needs the file to exist while it loads; shred it after a grace period.
	go func() {
		time.Sleep(5 * time.Minute)
		shredFile(path)
	}()
	writeJSON(writer, map[string]any{"ok": true})
}

// Only the company site may be opened, so the page can't be used to launch arbitrary URLs.
func handleExternal(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	target := request.URL.Query().Get("url")
	if target != "https://datalprojects.com" && !strings.HasPrefix(target, "https://datalprojects.com/") {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}
	if err := launchFile(target); err != nil {
		openBrowser(target)
	}
	writer.WriteHeader(http.StatusNoContent)
}

// Lists USB drives for the installed edition, and switches to the one picked.
// Switching is only allowed before anyone has unlocked, so a session can't be
// moved onto a different locker underneath it.
func handleDrives(writer http.ResponseWriter, request *http.Request) {
	drives := removableDrives()
	for index := range drives {
		drives[index].HasLocker = hasLocker(drives[index].Path)
	}
	if request.Method != http.MethodPost {
		writeJSON(writer, map[string]any{"drives": drives, "current": storageBase, "portable": portable})
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	authMutex.Lock()
	unlocked := sessionToken != ""
	authMutex.Unlock()
	if portable || unlocked || !readJSON(request, &body) {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}
	for _, drive := range drives {
		if drive.Path == body.Path {
			if err := useStorage(drive.Path); err != nil {
				writeJSON(writer, map[string]any{"ok": false, "message": err.Error()})
				return
			}
			writeJSON(writer, map[string]any{"ok": true})
			return
		}
	}
	writeJSON(writer, map[string]any{"ok": false, "message": "That drive isn’t plugged in anymore."})
}

//go:embed LICENSE
var licenseText string

//go:embed THIRD_PARTY_NOTICES.txt
var noticesText string

// Serves the MIT license and the bundled libraries' licenses, which their terms
// require to ship with the binary.
func handleLicense(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if request.URL.Query().Get("doc") == "notices" {
		writer.Write([]byte(noticesText))
		return
	}
	writer.Write([]byte(licenseText))
}
