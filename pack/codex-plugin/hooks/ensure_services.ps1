# SessionStart hook for the codex-lang-ensure plugin (Windows).
#
# Codex talks only to the Front Door. This hook starts that door and the Local
# Gateway (via the Watchdog) so a session never hits a refused connection. If
# the Local Gateway is still down when the hook exits, turns fail closed with
# HTTP 403 instead of leaking untranslated Han.
#
# Always tries to start both. A Control Page "front off" does not survive the
# next session: the 403 contract is the point of the plugin.
#
# Fast, silent on success, idempotent under concurrent sessions: a lost bind
# race just means the other session already started the process. Never prints
# prompts, bodies, headers, or keys. Exit code is always 0 so a broken local
# service never blocks session start; the 403s speak then.
#
# This is the Windows twin of hooks/ensure_services.sh (Linux/macOS); keep the
# two in step when changing behaviour.

$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'

$FrontPort = if ($env:CODEX_FRONT_PORT) { $env:CODEX_FRONT_PORT } else { '18787' }
$UiPort = if ($env:CODEX_UI_PORT) { $env:CODEX_UI_PORT } else { '18786' }
$Backend = 'http://127.0.0.1:18788'
$LogDir = Join-Path $env:USERPROFILE '.codex\logs'
$LogFile = Join-Path $LogDir 'ensure.log'

function Write-EnsureLog {
    param([string]$Message)
    try {
        New-Item -ItemType Directory -Force -Path $LogDir | Out-Null
        $stamp = (Get-Date).ToString('yyyy-MM-dd HH:mm:ss')
        Add-Content -Path $LogFile -Value "$stamp $Message" -Encoding UTF8
    } catch {
        # Logging must never break the hook.
    }
}

# -UseBasicParsing skips the IE engine on Windows PowerShell 5.1 (the parsing
# engine can block on a first run with IE unconfigured). It is a no-op that
# PowerShell 6+ still accepts, but gate it so a future removal cannot break the
# hook either.
function New-WebParams {
    param([string]$Url)
    $params = @{ Uri = $Url; TimeoutSec = 2; ErrorAction = 'Stop' }
    if ($PSVersionTable.PSVersion.Major -lt 6) { $params['UseBasicParsing'] = $true }
    return $params
}

function Test-Up {
    param([string]$Url)
    $web = New-WebParams -Url $Url
    try {
        $response = Invoke-WebRequest @web
        return ($response.StatusCode -ge 200 -and $response.StatusCode -lt 300)
    } catch {
        return $false
    }
}

function Get-State {
    param([string]$Url)
    $web = New-WebParams -Url $Url
    try {
        return Invoke-RestMethod @web
    } catch {
        return $null
    }
}

function Find-Bin {
    param([string]$Name)
    $command = Get-Command "$Name.exe" -ErrorAction SilentlyContinue
    if ($command) { return $command.Source }
    foreach ($dir in @((Join-Path $env:USERPROFILE '.codex\bin'), (Join-Path $env:USERPROFILE '.local\bin'))) {
        $candidate = Join-Path $dir "$Name.exe"
        if (Test-Path $candidate) { return $candidate }
    }
    return $null
}

function Start-Hidden {
    param([string]$Exe, [string[]]$Arguments)
    # Start-Process gives the child its own hidden console, so it outlives this
    # hook and never flashes a window in front of the Codex session.
    Start-Process -FilePath $Exe -ArgumentList $Arguments -WindowStyle Hidden | Out-Null
}

function Wait-Up {
    param([string]$Url)
    for ($i = 0; $i -lt 20; $i++) {
        if (Test-Up $Url) { return $true }
        Start-Sleep -Milliseconds 200
    }
    return (Test-Up $Url)
}

$StateUrl = "http://127.0.0.1:$UiPort/api/state"

try {

if (-not (Test-Up $StateUrl)) {
    $watchdog = Find-Bin 'codex-watchdog'
    if ($watchdog) {
        Write-EnsureLog "watchdog $UiPort down, starting"
        Start-Hidden $watchdog @('--no-tray')
    } else {
        Write-EnsureLog "watchdog $UiPort down, codex-watchdog not on PATH nor ~\.codex\bin"
    }
}

if (Wait-Up $StateUrl) {
    $state = Get-State $StateUrl
    if ($state) {
        if ($state.gatewayPort) { $Backend = "http://127.0.0.1:$($state.gatewayPort)" }
        if ($state.frontPort) { $FrontPort = $state.frontPort }
    }
    try {
        Invoke-RestMethod -Uri "http://127.0.0.1:$UiPort/api/front" -Method Post `
            -ContentType 'application/json' -Body '{"enabled":true}' -TimeoutSec 3 -ErrorAction Stop | Out-Null
    } catch {
        # The front door may already be on; nothing to do.
    }
    $state = Get-State $StateUrl
    if ($state) {
        if ($state.gatewayPort) { $Backend = "http://127.0.0.1:$($state.gatewayPort)" }
        if ($state.frontPort) { $FrontPort = $state.frontPort }
    }
}

$FrontHealth = "http://127.0.0.1:$FrontPort/healthz"

if (-not (Test-Up $FrontHealth)) {
    $front = Find-Bin 'codex-fronthost'
    if ($front) {
        Write-EnsureLog "front $FrontPort down, starting (backend $Backend)"
        Start-Hidden $front @('-port', "$FrontPort", '-backend', $Backend)
    } else {
        Write-EnsureLog "front $FrontPort down, codex-fronthost not on PATH nor ~\.codex\bin"
    }
}

if (-not (Wait-Up $FrontHealth)) {
    Write-EnsureLog "front $FrontPort still down after ~4s; turns will 403 until it is up"
}

} catch {
    # The contract is "never block session start": log the surprise and exit 0.
    Write-EnsureLog "hook error: $($_.Exception.Message)"
}

exit 0
