# Windows installer for codex-lang: build the binaries, install the
# codex-lang-ensure SessionStart plugin, and add a startup shortcut.
#
# Mirrors scripts/install-plugin.sh (Linux/macOS). Idempotent: re-running
# rebuilds, recopies, rewrites the plugin's hooks.json for PowerShell, and
# merges the marketplace entry without duplicating it. Never writes secrets.
#
# Run from an ordinary PowerShell window (no admin needed):
#   powershell -ExecutionPolicy Bypass -File .\scripts\install-plugin.ps1

$ErrorActionPreference = 'Stop'
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { }

$Root = Split-Path -Parent $PSScriptRoot
$BinDir = Join-Path $env:USERPROFILE '.codex\bin'
$PluginSrc = Join-Path $Root 'pack\codex-plugin'
$PluginDst = Join-Path $env:USERPROFILE '.codex\plugins\codex-lang-ensure'
$Marketplace = Join-Path $env:USERPROFILE '.agents\plugins\marketplace.json'
$Template = Join-Path $PluginSrc 'marketplace.template.json'

function Write-Utf8NoBom {
    param([string]$Path, [string]$Text)
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($Path, $Text, $utf8)
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host '需要 Go 1.22+（PATH 里找不到 go）' -ForegroundColor Red
    exit 1
}

function Invoke-GoBuild {
    param([string]$Output, [string]$Package)
    go build -o $Output $Package
    if ($LASTEXITCODE -ne 0) { throw "go build $Package 失败（exit $LASTEXITCODE）" }
}

Push-Location $Root
try {
    Write-Host "== go build =="
    $out = Join-Path $env:TEMP 'codex-lang-build'
    New-Item -ItemType Directory -Force -Path $out | Out-Null
    Invoke-GoBuild (Join-Path $out 'codex-fronthost.exe') './cmd/codex-fronthost'
    Invoke-GoBuild (Join-Path $out 'codex-watchdog.exe') './cmd/codex-watchdog'
    Invoke-GoBuild (Join-Path $out 'codex-translate.exe') './cmd/codex-translate'

    Write-Host "== install bins to $BinDir =="
    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    Copy-Item (Join-Path $out '*.exe') $BinDir -Force

    Write-Host "== install plugin to $PluginDst =="
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $PluginDst) | Out-Null
    if (Test-Path $PluginDst) { Remove-Item -Recurse -Force $PluginDst }
    Copy-Item -Recurse -Force $PluginSrc $PluginDst

    # Codex runs the hook command in a shell of its own choosing; on Windows
    # the reliable entry point is PowerShell, so the plugin ships a second
    # hooks.json and the installer selects it. (ensure_services.cmd is the
    # fallback for setups that spawn hooks through cmd.exe.)
    Copy-Item (Join-Path $PluginDst 'hooks\hooks.windows.json') (Join-Path $PluginDst 'hooks\hooks.json') -Force

    Write-Host "== autostart $(Join-Path ([Environment]::GetFolderPath('Startup')) 'codex-lang.lnk') =="
    $startup = [Environment]::GetFolderPath('Startup')
    $shell = New-Object -ComObject WScript.Shell
    $link = $shell.CreateShortcut((Join-Path $startup 'codex-lang.lnk'))
    $link.TargetPath = Join-Path $BinDir 'codex-watchdog.exe'
    $link.WorkingDirectory = $BinDir
    $link.WindowStyle = 7  # minimised; build with -ldflags "-H windowsgui" for no window at all
    $link.Description = 'codex-lang watchdog: Control Page + tray icon'
    $link.Save()

    Write-Host "== personal marketplace $Marketplace =="
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Marketplace) | Out-Null
    if (Test-Path $Marketplace) {
        $data = $null
        try { $data = Get-Content -Raw $Marketplace | ConvertFrom-Json } catch { $data = $null }
        if (-not $data) { $data = [pscustomobject]@{} }
        if (-not $data.name) {
            $data | Add-Member -NotePropertyName 'name' -NotePropertyValue 'codex-lang-local' -Force
        }
        if (-not $data.interface) {
            $data | Add-Member -NotePropertyName 'interface' -NotePropertyValue ([pscustomobject]@{ displayName = 'Codex Lang Local' }) -Force
        }
        $entry = [pscustomobject]@{
            name = 'codex-lang-ensure'
            source = [pscustomobject]@{ source = 'local'; path = './.codex/plugins/codex-lang-ensure' }
            policy = [pscustomobject]@{ installation = 'AVAILABLE'; authentication = 'ON_INSTALL' }
            category = 'Developer Tools'
        }
        $kept = @()
        if ($data.plugins) { $kept = @($data.plugins | Where-Object { $_.name -ne 'codex-lang-ensure' }) }
        $data | Add-Member -NotePropertyName 'plugins' -NotePropertyValue (@($kept) + $entry) -Force
        Write-Utf8NoBom $Marketplace ($data | ConvertTo-Json -Depth 10)
        Write-Host 'marketplace entry ok'
    } else {
        Copy-Item $Template $Marketplace -Force
        Write-Host 'marketplace 按模板新建'
    }
} finally {
    Pop-Location
}

$pathDirs = $env:PATH -split ';'
if ($pathDirs -notcontains $BinDir) {
    # The hook looks in %USERPROFILE%\.codex\bin anyway, so this only affects
    # typing the commands by hand.
    Write-Host "提示：$BinDir 不在 PATH 里（手动敲命令时不方便，插件 hook 不受影响）。" -ForegroundColor Yellow
    Write-Host "      想加的话：设置 → 系统 → 系统信息 → 高级系统设置 → 环境变量 → 用户变量 Path 里加一行。"
}

Write-Host ''
Write-Host '完成。接下来（只需一次）：'
Write-Host '  1. 重启 ChatGPT/Codex 桌面（只用 CLI 可跳过）；'
Write-Host '  2. codex plugin add codex-lang-ensure@codex-lang-local'
Write-Host '  3. 开一轮 CLI 跑 /hooks，把 SessionStart 那条 trust；'
Write-Host '  4. curl.exe -s http://127.0.0.1:18787/healthz   # {"ok":true}'
Write-Host '  5. Control Page 打开前置开关，真网关端口填 18788，TOML 贴进 %USERPROFILE%\.codex\config.toml。'
