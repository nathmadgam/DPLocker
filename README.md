<div align="center">

<img src="docs/logo.svg" alt="DPLocker" width="96" height="96">

# DPLocker

**A file locker for USB drives.**
Keep documents on a USB stick, encrypted behind a password and an authenticator code.

[![License: MIT](https://img.shields.io/badge/License-MIT-2563eb.svg)](LICENSE)
[![Platform: Windows](https://img.shields.io/badge/Platform-Windows%2010%20%2F%2011-2563eb.svg)](#download)
[![Built with Go](https://img.shields.io/badge/Built%20with-Go-00ADD8.svg)](https://go.dev)
[![Offline](https://img.shields.io/badge/Runs-Offline-16a34a.svg)](#privacy)

A product by [DATAL Projects](https://datalprojects.com)

<img src="docs/screenshot-light.png#gh-light-mode-only" alt="DPLocker" width="820">
<img src="docs/screenshot-dark.png#gh-dark-mode-only" alt="DPLocker" width="820">

</div>

## What it is

DPLocker turns an ordinary USB stick into a private file locker. Your documents are
encrypted on the drive and only open after you enter a password and a 6-digit code from an
authenticator app. It runs in its own window, works completely offline, and leaves nothing
on the computer you plug into.

It is meant for the documents a small business or a person keeps on a USB stick — invoices,
contracts, reports, spreadsheets — and wants to keep private if the stick is lost or shared.

## Features

- **Encrypted at rest.** Every file's contents and its name are encrypted with AES-256-GCM
  under a key derived from your password (scrypt). An imaged drive shows only random data.
- **Password + authenticator lock.** Works with any TOTP app or browser extension
  (Google Authenticator, Microsoft Authenticator, the Authenticator extension, and others),
  with lockout after repeated wrong attempts and one-time codes that cannot be reused.
- **A clean, Drive-style window.** Drag-and-drop uploads, list and grid views, search,
  a details panel, Trash with restore, and light and dark themes.
- **Metadata cleaning.** View, edit, or strip the author, title, company, comments and edit
  history inside Word, Excel, PowerPoint, OpenDocument and PDF files — one file at a time or
  automatically on every upload.
- **USB only.** Refuses to run from a computer's internal drive, so files never land on a PC.
- **Optional auto-update.** Checks GitHub for a newer release and installs it only when you
  choose, verifying the download against the release checksum first.
- **Closes clean.** Quits as soon as its window closes and wipes its browsing cache from the
  drive.

## Download

Grab the latest files from the [**Releases**](../../releases/latest) page.

| File | Use it when |
|------|-------------|
| `DPLocker-setup.exe` | You want to install DPLocker on a PC (Start-menu shortcut, uninstaller). Files still live on a USB drive you pick. |
| `DPLocker-portable.zip` | You want no install — unzip and copy `DPLocker.exe` onto any USB stick. |
| `DPLocker-obfuscated.exe` | Optional. Same app with internal names hidden; cleaning a PDF leaves a generic "pdfcpu" producer tag (author, title and subject are still removed). |

Every release includes `SHA256SUMS.txt` so you can verify your download.

> **Windows will warn you on first run.** The app is not code-signed yet, so SmartScreen
> shows "Windows protected your PC." Click **More info → Run anyway**. The source is here for
> anyone who wants to read it or build it themselves.

## How it works

1. Copy `DPLocker.exe` onto your USB stick (or install it and plug a stick in).
2. On first run, set a password and scan the QR code with your authenticator app.
3. Drop files in. They are sorted into folders by type and month, and encrypted.
4. Every time you open DPLocker, you enter your password and the current code.

Files are kept in a hidden `DPLocker Data` folder next to `DPLocker.exe`. Opening a Word or
Excel file writes a temporary decrypted copy to the computer's temp area (shredded
afterwards, never the USB); edits are not saved back automatically, so you re-add an edited
file. PDFs and text preview inside the app from memory.

## Privacy

DPLocker never connects to the internet except for the optional update check, which sends
nothing about you. Nothing is stored on the computer you plug into. The source is open so
anyone can confirm this.

## Important limits

- **There is no password recovery.** Files are encrypted with a key derived from your
  password, so if you lose both your password and your authenticator, the files cannot be
  recovered — by anyone, including you. Write your password down somewhere safe.
- **Keep a second copy.** A USB stick can be lost or fail. DPLocker is a convenient locker,
  not a backup.
- **Some USB SSDs report as fixed disks** and the portable edition will not run from those.
- **Metadata cleaning does not cover** names inside Word comments or tracked changes, the old
  `.doc` / `.xls` / `.ppt` formats, or password-protected PDFs.

## Build from source

Needs [Go](https://go.dev) 1.26+, Python 3, [go-winres](https://github.com/tc-hib/go-winres),
[NSIS](https://nsis.sourceforge.io) (`makensis`) and `zip`.

```sh
./build.sh
```

Outputs land in `dist/`, with SHA-256 checksums in `dist/SHA256SUMS.txt`. The page
(`index.html`) is generated from `index.template.html` and the images in `assets/` by
`tools/build_page.py` — edit the template, not the generated file.

## Tech

Go, with a [WebView2](https://github.com/jchv/go-webview2) window, [pdfcpu](https://github.com/pdfcpu/pdfcpu)
for PDF metadata, [go-qrcode](https://github.com/skip2/go-qrcode) for the authenticator QR,
and the Go standard library for AES-256-GCM and scrypt. Third-party licenses are in
[THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt).

## License

[MIT](LICENSE) © 2026 DATAL Projects. The DPLocker name and logo belong to DATAL Projects.
