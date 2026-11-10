// Update checks against the project's GitHub Releases. Nothing installs without the user
// clicking Update; the downloaded file must match the release's SHA256SUMS.txt.
package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	updateRepository  = "nathmadgam/DPLocker"
	updateCheckURL    = "https://api.github.com/repos/" + updateRepository + "/releases/latest"
	checksumAssetName = "SHA256SUMS.txt"
	maxDownloadBytes  = 200 * 1024 * 1024
	afterUpdateFlag   = "--after-update"
	oldExecutableName = "DPLocker.old.exe"
)

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type releaseInfo struct {
	TagName string         `json:"tag_name"`
	Name    string         `json:"name"`
	Body    string         `json:"body"`
	HTMLURL string         `json:"html_url"`
	Draft   bool           `json:"draft"`
	Assets  []releaseAsset `json:"assets"`
}

var (
	updateMutex  sync.Mutex
	latestCached *releaseInfo
	updating     bool
)

// "v1.2.3" → [1 2 3]; anything unparsable compares as 0.
func parseVersion(value string) [3]int {
	var parts [3]int
	for index, piece := range strings.SplitN(strings.TrimPrefix(strings.TrimSpace(value), "v"), ".", 3) {
		number, _ := strconv.Atoi(strings.SplitN(piece, "-", 2)[0])
		parts[index] = number
	}
	return parts
}

func isNewer(candidate, current string) bool {
	a, b := parseVersion(candidate), parseVersion(current)
	for index := range a {
		if a[index] != b[index] {
			return a[index] > b[index]
		}
	}
	return false
}

var updateClient = &http.Client{Timeout: 30 * time.Second}

func fetchLatestRelease() (*releaseInfo, error) {
	request, _ := http.NewRequest(http.MethodGet, updateCheckURL, nil)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "DPLocker/"+appVersion)
	response, err := updateClient.Do(request)
	if err != nil {
		return nil, errors.New("couldn’t reach GitHub (offline?)")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, errors.New("no releases published yet")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %d", response.StatusCode)
	}
	var release releaseInfo
	if err := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024)).Decode(&release); err != nil {
		return nil, errors.New("couldn’t read the release information")
	}
	return &release, nil
}

func expectedAssetName(version string) string {
	if portable {
		return "DPLocker-" + version + "-portable.zip"
	}
	return "DPLocker-" + version + "-setup.exe"
}

func findAsset(release *releaseInfo, name string) *releaseAsset {
	for index := range release.Assets {
		if release.Assets[index].Name == name {
			return &release.Assets[index]
		}
	}
	return nil
}

// Only GitHub's own download hosts are trusted for release files.
func trustedDownload(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	host := parsed.Hostname()
	return host == "github.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

func download(raw string, limit int64) ([]byte, error) {
	if !trustedDownload(raw) {
		return nil, errors.New("refused a download from outside GitHub")
	}
	request, _ := http.NewRequest(http.MethodGet, raw, nil)
	request.Header.Set("User-Agent", "DPLocker/"+appVersion)
	client := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(next *http.Request, _ []*http.Request) error {
		if !trustedDownload(next.URL.String()) {
			return errors.New("redirected outside GitHub")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("download failed: " + err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed (%d)", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, errors.New("download interrupted")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("download is larger than expected")
	}
	return data, nil
}

func checksumFor(sums []byte, name string) string {
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}

func handleUpdateCheck(writer http.ResponseWriter, request *http.Request) {
	settings := loadSettings()
	if settings.UpdateCheckDisabled && request.URL.Query().Get("force") != "1" {
		writeJSON(writer, map[string]any{"available": false, "disabled": true})
		return
	}
	release, err := fetchLatestRelease()
	if err != nil {
		writeJSON(writer, map[string]any{"available": false, "message": err.Error()})
		return
	}
	version := strings.TrimPrefix(release.TagName, "v")
	updateMutex.Lock()
	latestCached = release
	updateMutex.Unlock()
	available := !release.Draft && isNewer(version, appVersion) && findAsset(release, expectedAssetName(version)) != nil
	writeJSON(writer, map[string]any{
		"available": available, "version": version, "current": appVersion, "notes": release.Body,
		"skipped": settings.SkippedVersion == version, "page": release.HTMLURL,
	})
}

// Downloads, verifies and applies the update the user asked for.
func handleUpdateInstall(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	updateMutex.Lock()
	release := latestCached
	if updating {
		updateMutex.Unlock()
		writeJSON(writer, map[string]any{"ok": false, "message": "An update is already running."})
		return
	}
	updating = true
	updateMutex.Unlock()
	fail := func(message string) {
		updateMutex.Lock()
		updating = false
		updateMutex.Unlock()
		writeJSON(writer, map[string]any{"ok": false, "message": message})
	}
	if release == nil {
		fail("Check for updates first.")
		return
	}
	version := strings.TrimPrefix(release.TagName, "v")
	asset := findAsset(release, expectedAssetName(version))
	sumsAsset := findAsset(release, checksumAssetName)
	if asset == nil || sumsAsset == nil {
		fail("This release is missing its download or checksum file.")
		return
	}
	sums, err := download(sumsAsset.URL, 64*1024)
	if err != nil {
		fail(err.Error())
		return
	}
	expected := checksumFor(sums, asset.Name)
	if expected == "" {
		fail("The checksum list doesn’t include " + asset.Name + ".")
		return
	}
	data, err := download(asset.URL, maxDownloadBytes)
	if err != nil {
		fail(err.Error())
		return
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != expected {
		fail("The download didn’t match its checksum, so it wasn’t installed.")
		return
	}
	if portable {
		err = applyPortableUpdate(data)
	} else {
		err = runInstaller(data, version)
	}
	if err != nil {
		fail(err.Error())
		return
	}
	writeJSON(writer, map[string]any{"ok": true, "version": version})
	// Give the response time to reach the window before this copy exits.
	go func() {
		time.Sleep(700 * time.Millisecond)
		os.Exit(0)
	}()
}

// Windows lets a running exe be renamed but not overwritten, so the current exe steps
// aside as DPLocker.old.exe and the new one takes its name; the next launch removes the old one.
func applyPortableUpdate(zipData []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return errors.New("the update package is damaged")
	}
	var newExecutable []byte
	for _, file := range reader.File {
		if strings.EqualFold(filepath.Base(file.Name), "DPLocker.exe") {
			handle, err := file.Open()
			if err != nil {
				return errors.New("the update package is damaged")
			}
			newExecutable, err = io.ReadAll(io.LimitReader(handle, maxDownloadBytes))
			handle.Close()
			if err != nil {
				return errors.New("the update package is damaged")
			}
		}
	}
	if len(newExecutable) == 0 {
		return errors.New("the update package has no DPLocker.exe")
	}
	current, err := os.Executable()
	if err != nil {
		return err
	}
	directory := filepath.Dir(current)
	staged := filepath.Join(directory, "DPLocker.new.exe")
	if err := os.WriteFile(staged, newExecutable, 0o755); err != nil {
		return errors.New("couldn’t write the update to the USB drive (full or read-only?)")
	}
	oldPath := filepath.Join(directory, oldExecutableName)
	os.Remove(oldPath)
	if err := os.Rename(current, oldPath); err != nil {
		os.Remove(staged)
		return errors.New("couldn’t replace DPLocker.exe: " + err.Error())
	}
	if err := os.Rename(staged, current); err != nil {
		// Put the running version back so the drive still has a working DPLocker.exe.
		os.Rename(oldPath, current)
		return errors.New("couldn’t install the update: " + err.Error())
	}
	return exec.Command(current, afterUpdateFlag).Start()
}

// The installed edition updates by running the new setup, which replaces the files in
// Program Files once this copy has closed.
func runInstaller(setupData []byte, version string) error {
	path := filepath.Join(os.TempDir(), "DPLocker-"+version+"-setup.exe")
	if err := os.WriteFile(path, setupData, 0o755); err != nil {
		return errors.New("couldn’t save the installer: " + err.Error())
	}
	return launchFile(path)
}

// Runs at startup: waits for the previous copy to release the window and drive,
// then removes the exe it replaced.
func finishPendingUpdate() {
	isAfterUpdate := false
	for _, argument := range os.Args[1:] {
		if argument == afterUpdateFlag {
			isAfterUpdate = true
		}
	}
	if isAfterUpdate {
		time.Sleep(1500 * time.Millisecond)
	}
	if current, err := os.Executable(); err == nil {
		oldPath := filepath.Join(filepath.Dir(current), oldExecutableName)
		for attempt := 0; attempt < 5; attempt++ {
			if err := os.Remove(oldPath); err == nil || os.IsNotExist(err) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
}
