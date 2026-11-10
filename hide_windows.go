package main

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// Hidden + system keeps the folder out of Explorer even when "show hidden items" is on,
// unless "hide protected operating system files" is also turned off.
func hideFolder(path string) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	syscall.SetFileAttributes(pointer, syscall.FILE_ATTRIBUTE_HIDDEN|syscall.FILE_ATTRIBUTE_SYSTEM)
}

func driveRoot(path string) string {
	return filepath.VolumeName(path) + `\`
}

// Windows reports USB flash drives and SD cards as DRIVE_REMOVABLE. Some USB SSDs
// and enclosures report DRIVE_FIXED and are refused too.
func isRemovableDrive(path string) bool {
	pointer, err := windows.UTF16PtrFromString(driveRoot(path))
	if err != nil {
		return false
	}
	return windows.GetDriveType(pointer) == windows.DRIVE_REMOVABLE
}

func diskSpace(path string) (total, free uint64) {
	pointer, err := windows.UTF16PtrFromString(driveRoot(path))
	if err != nil {
		return 0, 0
	}
	var available, totalBytes, totalFree uint64
	if windows.GetDiskFreeSpaceEx(pointer, &available, &totalBytes, &totalFree) != nil {
		return 0, 0
	}
	return totalBytes, available
}

func showError(title, message string) {
	titlePointer, _ := windows.UTF16PtrFromString(title)
	messagePointer, _ := windows.UTF16PtrFromString(message)
	windows.MessageBox(0, messagePointer, titlePointer, windows.MB_OK|windows.MB_ICONERROR)
}

// Lists ready removable drives (USB sticks, SD cards) as root paths like `E:\`.
func removableDrives() []driveInfo {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	drives := []driveInfo{}
	for index := 0; index < 26; index++ {
		if mask&(1<<uint(index)) == 0 {
			continue
		}
		root := string(rune('A'+index)) + `:\`
		if !isRemovableDrive(root) {
			continue
		}
		total, free := diskSpace(root)
		if total == 0 {
			// An empty card reader slot reports as removable but isn't ready.
			continue
		}
		drives = append(drives, driveInfo{Path: root, Label: volumeLabel(root), Total: total, Free: free})
	}
	return drives
}

func volumeLabel(root string) string {
	pointer, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return ""
	}
	name := make([]uint16, windows.MAX_PATH+1)
	if windows.GetVolumeInformation(pointer, &name[0], uint32(len(name)), nil, nil, nil, nil, 0) != nil {
		return ""
	}
	return windows.UTF16ToString(name)
}

func localCacheDirectory() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(base, "DPLocker")
}
