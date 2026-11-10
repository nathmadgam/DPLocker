## DPLocker 1.0.0

First public release.

**What it does**

A file locker for USB drives. Your documents are encrypted on the stick (AES-256-GCM, key
derived from your password with scrypt) and open only after a password and an authenticator
code. It runs offline in its own window and leaves nothing on the computer.

**Highlights**

- Files and file names encrypted at rest — an imaged drive shows only random data
- Password + TOTP authenticator lock, with lockout and single-use codes
- Drive-style window: drag-and-drop, list/grid, search, Trash, light & dark
- View, edit or strip metadata in Word, Excel, PowerPoint, OpenDocument and PDF files
- Runs from USB only; nothing is written to the PC; cache wiped on close
- Optional, checksum-verified auto-update

**Downloads**

- `DPLocker-setup.exe` — installer (Start-menu shortcut + uninstaller)
- `DPLocker-portable.zip` — unzip and copy onto any USB stick
- `DPLocker-obfuscated.exe` — optional build with internal names hidden (see note below)
- `SHA256SUMS.txt` — verify your download

**Please note**

- There is no password recovery. Lose your password and authenticator, and the files are
  gone for good. Keep a second copy of anything important.
- Windows SmartScreen will warn on first run until the app is code-signed — More info → Run anyway.
- The obfuscated build uses an older PDF library; cleaning a PDF leaves a generic "pdfcpu"
  producer tag (author, title and subject are still removed). The regular builds don't have this.

Needs 64-bit Windows 10 or 11. MIT licensed. Built by [DATAL Projects](https://datalprojects.com).
