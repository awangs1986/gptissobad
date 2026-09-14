#!/bin/bash
# Build codex-lang binaries, install them to ~/.local/bin, and install the
# codex-lang-ensure SessionStart plugin into the personal marketplace.
#
# Idempotent: re-running rebuilds, recopies, and merges the marketplace
# entry without duplicating it. Never writes secrets.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="$HOME/.local/bin"
PLUGIN_SRC="$ROOT/pack/codex-plugin"
PLUGIN_DST="$HOME/.codex/plugins/codex-lang-ensure"
MARKETPLACE="$HOME/.agents/plugins/marketplace.json"

cd "$ROOT"

if ! command -v go >/dev/null 2>&1; then
  echo "需要 Go 1.22+（ PATH 里找不到 go ）" >&2
  exit 1
fi

echo "== go build =="
go build -o codex-fronthost ./cmd/codex-fronthost
go build -o codex-watchdog ./cmd/codex-watchdog
go build -o codex-translate ./cmd/codex-translate

echo "== install bins to $BIN_DIR =="
mkdir -p "$BIN_DIR"
install -m755 codex-fronthost codex-watchdog codex-translate "$BIN_DIR/"

echo "== install plugin to $PLUGIN_DST =="
mkdir -p "$(dirname "$PLUGIN_DST")"
rm -rf "$PLUGIN_DST"
cp -a "$PLUGIN_SRC" "$PLUGIN_DST"
chmod +x "$PLUGIN_DST/hooks/ensure_services.sh"

echo "== autostart $HOME/.config/autostart/codex-lang.desktop =="
mkdir -p "$HOME/.config/autostart"
# Reuse the packaged desktop entry, pointed at the installed binary in
# headless mode: at login there may be no panel yet, and a second manual
# launch simply adopts the running instance.
sed -e "s|^Exec=.*|Exec=$BIN_DIR/codex-watchdog|" \
  "$ROOT/pack/appimage/codex-lang.desktop" > "$HOME/.config/autostart/codex-lang.desktop"

echo "== personal marketplace $MARKETPLACE =="
mkdir -p "$(dirname "$MARKETPLACE")"
python3 - "$MARKETPLACE" <<'PY'
import json, sys
path = sys.argv[1]
entry = {
    "name": "codex-lang-ensure",
    "source": {"source": "local", "path": "./.codex/plugins/codex-lang-ensure"},
    "policy": {"installation": "AVAILABLE", "authentication": "ON_INSTALL"},
    "category": "Developer Tools",
}
try:
    with open(path, encoding="utf-8") as f:
        data = json.load(f)
except (FileNotFoundError, ValueError):
    data = {}
if not isinstance(data, dict):
    data = {}
data.setdefault("name", "codex-lang-local")
data.setdefault("interface", {"displayName": "Codex Lang Local"})
plugins = data.setdefault("plugins", [])
for i, p in enumerate(plugins):
    if isinstance(p, dict) and p.get("name") == "codex-lang-ensure":
        plugins[i] = entry
        break
else:
    plugins.append(entry)
with open(path, "w", encoding="utf-8") as f:
    json.dump(data, f, ensure_ascii=False, indent=2)
    f.write("\n")
print("marketplace entry ok")
PY

cat <<'NEXT'

完成。接下来（只需一次）：
  1. 重启 ChatGPT/Codex 桌面（只用 CLI 可跳过）；
  2. codex plugin add codex-lang-ensure@codex-lang-local
  3. 开一轮 CLI 跑 /hooks，把 SessionStart 那条 trust；
  4. curl -s http://127.0.0.1:18787/healthz   # {"ok":true}
  5. Control Page 打开前置开关，真网关端口填 18788，TOML 贴进 ~/.codex/config.toml。
NEXT
