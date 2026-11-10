package main

import (
	"os"
	"path/filepath"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

const (
	windowWidth  = 1320
	windowHeight = 840
	// Matches the RT_GROUP_ICON "#1" entry in winres/winres.json.
	iconResourceID = 1
)

// Shows the UI in a native window using the WebView2 runtime built into Windows 10/11.
// Returns false if WebView2 isn't available so the caller can fall back to the browser.
func runWindow(url string) bool {
	view := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		// Portable keeps the window's cache on the USB; the installed app keeps it in the
		// user's local app data (layout preference only, never files or passwords).
		DataPath: webViewDataPath(),
		WindowOptions: webview2.WindowOptions{
			Title:  "DPLocker",
			Width:  windowWidth,
			Height: windowHeight,
			IconId: iconResourceID,
			Center: true,
		},
	})
	if view == nil {
		return false
	}
	view.SetSize(960, 620, webview2.HintMin)
	view.Navigate(url)
	view.Run()
	view.Destroy()
	// The window is closed, so the WebView process has released its cache; wipe it so no
	// browsing data (page cache, history) is left on the drive.
	wipeWebViewCache()
	return true
}

// Removes the WebView cache folder. Called once the WebView has shut down.
func wipeWebViewCache() {
	os.RemoveAll(webViewDataPath())
}

// Opens a stored file in its default Windows app (Word, Excel, PDF reader...).
func launchFile(path string) error {
	operation, _ := windows.UTF16PtrFromString("open")
	target, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, operation, target, nil, nil, windows.SW_SHOWNORMAL)
}

func webViewDataPath() string {
	if portable {
		return filepath.Join(rootDirectory, "WebView")
	}
	return filepath.Join(localCacheDirectory(), "WebView")
}
