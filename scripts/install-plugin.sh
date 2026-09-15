#!/bin/bash
# Build codex-lang binaries, install them to ~/.local/bin, and install the
# codex-lang-ensure SessionStart plugin into the personal marketplace.
#
# Works on Linux and macOS (bash 3.2+): the binaries are pure Go, the hook is
# /bin/sh, and only the autostart step differs (XDG .desktop vs launchd
# LaunchAgent). Windows uses scripts/install-plugin.ps1 with the PowerShell
# hook instead.
#
# Idempotent: re-running rebuilds, recopies, and merges the marketplace entry
# without duplicating it. Never writes secrets.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="$HOME/.local/bin"
PLUGIN_SRC="$ROOT/pack/codex-plugin"
PLUGIN_DST="$HOME/.codex/plugins/codex-lang-ensure"
MARKETPLACE="$HOME/.agents/plugins/marketplace.json"
TEMPLATE="$PLUGIN_SRC/marketplace.template.json"
PLATFORM="$(uname -s)"

cd "$ROOT"

if ! command -v go >/dev/null 2>&1; then
  echo "需要 Go 1.22+（ PATH 里找不到 go ）" >&2
  exit 1
fi

echo "== go build == ($PLATFORM)"
go build -o codex-fronthost ./cmd/codex-fronthost
go build -o codex-watchdog ./cmd/codex-watchdog
go build -o codex-translate ./cmd/codex-translate

echo "== install bins to $BIN_DIR =="
mkdir -p "$BIN_DIR"
install -m755 codex-fronthost codex-watchdog codex-translate "$BIN_DIR/"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "提示：$BIN_DIR 不在 PATH 里，插件 hook 会找不到二进制；建议把 export PATH=\"\$HOME/.local/bin:\$PATH\" 写进 shell 配置。" >&2 ;;
esac

echo "== install plugin to $PLUGIN_DST =="
mkdir -p "$(dirname "$PLUGIN_DST")"
rm -rf "$PLUGIN_DST"
cp -R -p "$PLUGIN_SRC" "$PLUGIN_DST"
chmod +x "$PLUGIN_DST/hooks/ensure_services.sh"

case "$PLATFORM" in
  Linux)
    echo "== autostart $HOME/.config/autostart/codex-lang.desktop =="
    mkdir -p "$HOME/.config/autostart"
    # Reuse the packaged desktop entry, pointed at the installed binary in
    # headless mode: at login there may be no panel yet, and a second manual
    # launch simply adopts the running instance.
    sed -e "s|^Exec=.*|Exec=$BIN_DIR/codex-watchdog|" \
      "$ROOT/pack/appimage/codex-lang.desktop" > "$HOME/.config/autostart/codex-lang.desktop"
    ;;
  Darwin)
    AGENT="$HOME/Library/LaunchAgents/local.codex-lang.watchdog.plist"
    echo "== autostart $AGENT =="
    mkdir -p "$(dirname "$AGENT")" "$HOME/.codex/logs"
    cat > "$AGENT" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>local.codex-lang.watchdog</string>
  <key>ProgramArguments</key>
  <array>
    <string>$BIN_DIR/codex-watchdog</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardOutPath</key>
  <string>$HOME/.codex/logs/watchdog.log</string>
  <key>StandardErrorPath</key>
  <string>$HOME/.codex/logs/watchdog.log</string>
</dict>
</plist>
PLIST
    launchctl bootstrap "gui/$(id -u)" "$AGENT" 2>/dev/null \
      || launchctl load -w "$AGENT" 2>/dev/null \
      || echo "（launchctl 没能自动载入，手动跑：launchctl load -w $AGENT）" >&2
    echo "注意：macOS 上没有菜单栏图标（见 README），Control Page 照常工作。"
    ;;
  *)
    echo "== 跳过自启：$PLATFORM 不在支持列表（二进制和插件已装好）==" >&2
    ;;
esac

echo "== personal marketplace $MARKETPLACE =="
mkdir -p "$(dirname "$MARKETPLACE")"
if command -v python3 >/dev/null 2>&1; then
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
elif [ ! -f "$MARKETPLACE" ]; then
  cp "$TEMPLATE" "$MARKETPLACE"
  echo "marketplace 按模板新建（本机没有 python3，只处理还没有 marketplace 的情况）"
else
  printf '%s\n' \
    "注意：$MARKETPLACE 已存在，而本机没有 python3，脚本不敢替你改 JSON。" \
    "请手动把下面这段插件条目加进它的 \"plugins\" 数组：" >&2
  sed -n '/"plugins"/,$p' "$TEMPLATE" >&2
fi

cat <<'NEXT'

完成。接下来（只需一次）：
  1. 重启 ChatGPT/Codex 桌面（只用 CLI 可跳过）；
  2. codex plugin add codex-lang-ensure@codex-lang-local
  3. 开一轮 CLI 跑 /hooks，把 SessionStart 那条 trust；
  4. curl -s http://127.0.0.1:18787/healthz   # {"ok":true}
  5. Control Page 打开前置开关，真网关端口填 18788，TOML 贴进 ~/.codex/config.toml。
NEXT
