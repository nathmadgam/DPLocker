//go:build !windows

package main

import "errors"

// Non-Windows test builds always use the browser.
func runWindow(url string) bool {
	return false
}

func launchFile(path string) error {
	return errors.New("opening files in apps is only supported on Windows")
}

func wipeWebViewCache() {}

func webViewDataPath() string {
	return ""
}
