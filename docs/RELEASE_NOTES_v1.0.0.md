## DPLocker 1.0.0

First public release.

### What it does

A file locker for USB drives. Your documents are encrypted on the stick with AES-256-GCM, using a key derived from your password with scrypt. They open only after a password and an authenticator code. It runs offline in its own window and leaves nothing on the computer.

### Highlights

Files and file names are encrypted at rest. An imaged drive shows only random data.

A password plus a TOTP authenticator lock the app. It locks out after wrong attempts. Each code works once.

The window works like a simple Drive. Drag-and-drop, list and grid, search, Trash, light and dark.

You view, edit, or strip metadata in Word, Excel, PowerPoint, OpenDocument, and PDF files.

It runs from USB only. Nothing is written to the PC. The cache is wiped on close.

Updates are optional and verified against a checksum.

### Downloads

DPLocker-setup.exe is the installer. It adds a Start-menu shortcut and an uninstaller.

DPLocker-portable.zip is the no-install version. Unzip and copy onto any USB stick.

DPLocker-obfuscated.exe is an optional build with internal names hidden. See the note below.

SHA256SUMS.txt lets you verify your download.

### Please note

There is no password recovery. If you lose your password and authenticator, the files are gone. Keep a second copy of anything important.

Windows SmartScreen warns on first run until the app is code-signed. Click More info, then Run anyway.

The obfuscated build uses an older PDF library. Cleaning a PDF leaves a generic "pdfcpu" producer tag. Author, title, and subject are still removed. The regular builds do not have this.

Needs 64-bit Windows 10 or 11. MIT licensed. Built by [DATAL Projects](https://datalprojects.com).
