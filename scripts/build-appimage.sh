#!/usr/bin/env bash
# Build CodexLang-x86_64.AppImage for Linux Mint (Cinnamon panel / xdg-open).
# Run this on the Mint machine (needs Go 1.22+, curl). This Windows box cannot run appimagetool.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/dist"
appdir="$out/CodexLang.AppDir"
mkdir -p "$out" "$appdir/usr/bin"

if ! command -v go >/dev/null 2>&1; then
  echo "go is required to build the AppImage" >&2
  exit 1
fi

export CGO_ENABLED=0
export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
go -C "$root" run ./cmd/codex-watchdog -write-icon "$appdir/codex-lang.png"

export GOOS=linux
export GOARCH="${GOARCH:-amd64}"
go -C "$root" build -trimpath -ldflags="-s -w" -o "$appdir/usr/bin/codex-watchdog" ./cmd/codex-watchdog

cp "$root/pack/appimage/AppRun" "$appdir/AppRun"
cp "$root/pack/appimage/codex-lang.desktop" "$appdir/codex-lang.desktop"
chmod +x "$appdir/AppRun" "$appdir/usr/bin/codex-watchdog"

tool="$out/appimagetool-x86_64.AppImage"
if [[ ! -x "$tool" ]]; then
  curl -fsSL -o "$tool" \
    "https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-x86_64.AppImage"
  chmod +x "$tool"
fi

ARCH=x86_64
if ! "$tool" "$appdir" "$out/CodexLang-x86_64.AppImage"; then
  "$tool" --appimage-extract-and-run "$appdir" "$out/CodexLang-x86_64.AppImage"
fi

echo "wrote $out/CodexLang-x86_64.AppImage"
