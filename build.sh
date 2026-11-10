#!/usr/bin/env sh
# Builds dist/DPLocker.exe, the portable zip and the Windows installer.
# Needs: Go 1.23+, Python 3, go-winres (go install github.com/tc-hib/go-winres@latest),
# NSIS (makensis) and zip. Works on Linux, macOS or Windows (Git Bash).
set -eu
cd "$(dirname "$0")"
VERSION="$(sed -n 's/.*appVersion *= *"\(.*\)".*/\1/p' main.go)"

python3 tools/build_page.py
go-winres make --arch amd64 --in winres/winres.json --out rsrc
mkdir -p dist
GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-s -w -H windowsgui -buildid=" -o dist/DPLocker.exe .

rm -rf dist/portable && mkdir -p dist/portable
cp dist/DPLocker.exe dist/portable/
cp LICENSE dist/portable/LICENSE.txt
cp THIRD_PARTY_NOTICES.txt dist/portable/
cp installer/PORTABLE-README.txt dist/portable/README.txt
(cd dist/portable && zip -q -9 "../DPLocker-$VERSION-portable.zip" ./*)

(cd installer && makensis -V2 -DVERSION="$VERSION" dplocker.nsi)
(cd dist && sha256sum "DPLocker-$VERSION-portable.zip" "DPLocker-$VERSION-setup.exe" > SHA256SUMS.txt)
echo "Built dist/DPLocker-$VERSION-portable.zip and dist/DPLocker-$VERSION-setup.exe"
