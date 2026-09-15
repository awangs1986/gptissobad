@echo off
rem SessionStart hook shim for the codex-lang-ensure plugin on Windows.
rem
rem Codex may spawn hook commands through cmd.exe; this shim keeps the hook's
rem command line short and finds the PowerShell implementation next to itself,
rem so a plugin path with spaces needs no extra quoting. It bypasses the
rem execution policy for this single script only, and always exits 0.
setlocal
set "PS=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"
if not exist "%PS%" set "PS=powershell.exe"
"%PS%" -NoProfile -ExecutionPolicy Bypass -File "%~dp0ensure_services.ps1"
exit /b 0
