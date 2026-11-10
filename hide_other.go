//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Non-Windows builds exist only for local testing.
func hideFolder(path string) {}

func isRemovableDrive(path string) bool {
	return os.Getenv("DPLOCKER_FAKE_FIXED") == "" && os.Getenv("DPLOCKER_FAKE_DRIVES") == ""
}

func diskSpace(path string) (total, free uint64) {
	var stat syscall.Statfs_t
	if syscall.Statfs(path, &stat) != nil {
		return 0, 0
	}
	return stat.Blocks * uint64(stat.Bsize), stat.Bavail * uint64(stat.Bsize)
}

func showError(title, message string) {
	fmt.Fprintln(os.Stderr, title+": "+message)
}

// DPLOCKER_FAKE_DRIVES (colon-separated folders) stands in for USB drives in tests.
func removableDrives() []driveInfo {
	drives := []driveInfo{}
	for _, path := range strings.Split(os.Getenv("DPLOCKER_FAKE_DRIVES"), ":") {
		if path == "" {
			continue
		}
		total, free := diskSpace(path)
		drives = append(drives, driveInfo{Path: path, Label: filepath.Base(path), Total: total, Free: free})
	}
	return drives
}

func localCacheDirectory() string {
	return filepath.Join(os.TempDir(), "DPLocker")
}
