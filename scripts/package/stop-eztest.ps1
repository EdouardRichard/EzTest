#requires -Version 5.1
<#
.SYNOPSIS
    Stops the EzTest stack started by start-eztest.ps1.

.DESCRIPTION
    Stops, in order: web, the Playwright executor (whole process tree, so
    Chromium children die too) and the Go API. Pid files under run\ are removed.
    PostgreSQL and the data\ directories are left untouched; a missing or stale
    pid file is reported without failing.

    Windows PowerShell 5.1 compatible. On Linux use stop-eztest.sh.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

if ([System.IO.Path]::DirectorySeparatorChar -ne '\') {
    Write-Host "This is the Windows stop script." -ForegroundColor Red
    Write-Host "On Linux run: bash stop-eztest.sh" -ForegroundColor Yellow
    exit 2
}

$PackageRoot = $PSScriptRoot
$RunDir = Join-Path $PackageRoot "run"

function Get-RecordedPid {
    param([string]$Name)
    $pidPath = Join-Path $RunDir ($Name + ".pid")
    if (-not (Test-Path -LiteralPath $pidPath -PathType Leaf)) { return 0 }
    $text = Get-Content -LiteralPath $pidPath -TotalCount 1 -ErrorAction SilentlyContinue
    if ($null -eq $text) { return 0 }
    if ("$text".Trim() -match '^\d+$') { return [int]"$text".Trim() }
    return 0
}

function Test-PidAlive {
    param([int]$ProcessId)
    if ($ProcessId -le 0) { return $false }
    try {
        return ($null -ne (Get-Process -Id $ProcessId -ErrorAction Stop))
    } catch {
        return $false
    }
}

function Invoke-TaskKill {
    # taskkill writes "ERROR: ... could not be terminated" to stderr when a child
    # disappears between the parent kill and the tree walk; that is expected while
    # stopping a process tree, so capture it instead of surfacing a scary error.
    param([int]$ProcessId, [switch]$Force)
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $arguments = @("/PID", "$ProcessId", "/T")
        if ($Force) { $arguments += "/F" }
        & taskkill.exe @arguments 2>&1 | Out-Null
    } finally {
        $ErrorActionPreference = $previousPreference
    }
}

function Stop-ProcessTree {
    param([int]$ProcessId, [int]$GraceSeconds = 8)
    # First ask politely (windowed processes get WM_CLOSE); console processes
    # usually ignore it, so fall back to a forced tree kill.
    Invoke-TaskKill -ProcessId $ProcessId
    $deadline = (Get-Date).AddSeconds($GraceSeconds)
    while ((Get-Date) -lt $deadline) {
        if (-not (Test-PidAlive -ProcessId $ProcessId)) { return $true }
        Start-Sleep -Milliseconds 250
    }
    Invoke-TaskKill -ProcessId $ProcessId -Force
    $deadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $deadline) {
        if (-not (Test-PidAlive -ProcessId $ProcessId)) { return $true }
        Start-Sleep -Milliseconds 250
    }
    return $false
}

if (-not (Test-Path -LiteralPath $RunDir -PathType Container)) {
    Write-Host "No run\ directory found; EzTest was not started from this package."
    exit 0
}

$stopped = 0
foreach ($component in @("web", "executor", "api")) {
    $pidPath = Join-Path $RunDir ($component + ".pid")
    if (-not (Test-Path -LiteralPath $pidPath -PathType Leaf)) {
        Write-Host ("{0,-10} not running (no pid file)" -f $component)
        continue
    }
    $processId = Get-RecordedPid -Name $component
    if (-not (Test-PidAlive -ProcessId $processId)) {
        Write-Host ("{0,-10} not running (stale pid file removed)" -f $component)
        Remove-Item -LiteralPath $pidPath -Force -ErrorAction SilentlyContinue
        continue
    }
    Write-Host ("{0,-10} stopping pid {1} ..." -f $component, $processId) -NoNewline
    if (Stop-ProcessTree -ProcessId $processId) {
        Write-Host " stopped" -ForegroundColor Green
        $stopped++
    } else {
        Write-Host ""
        Write-Host ("WARNING: {0} (pid {1}) is still running; kill it manually." -f $component, $processId) -ForegroundColor Yellow
    }
    Remove-Item -LiteralPath $pidPath -Force -ErrorAction SilentlyContinue
}

Write-Host ""
if ($stopped -gt 0) {
    Write-Host "EzTest stopped. PostgreSQL and the data\ directories were left untouched." -ForegroundColor Green
} else {
    Write-Host "EzTest was not running. PostgreSQL and the data\ directories were left untouched."
}
exit 0
