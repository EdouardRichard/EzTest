#requires -Version 5.1
<#
.SYNOPSIS
    Starts the EzTest Next.js development server on http://localhost:6868.

.DESCRIPTION
    Runs `pnpm dev` from frontend/ (the package script binds port 6868). Install
    dependencies first with `pnpm install` when node_modules is missing. The API
    is expected to be running separately via scripts/dev-api.ps1.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$frontendDir = Join-Path $repoRoot "frontend"

if (-not (Test-Path -LiteralPath (Join-Path $frontendDir "node_modules"))) {
    throw "frontend/node_modules is missing. Run 'pnpm install' in frontend/ first."
}

$pnpmCommand = Get-Command pnpm.cmd -ErrorAction SilentlyContinue
if (-not $pnpmCommand) {
    $pnpmCommand = Get-Command pnpm -ErrorAction SilentlyContinue
}
if (-not $pnpmCommand) {
    throw "pnpm is not on PATH. Install pnpm (or enable Corepack) first."
}

Write-Host "Starting EzTest web on http://localhost:6868 ..." -ForegroundColor Cyan
Write-Host "The API must be running (scripts\dev-api.ps1). Press Ctrl+C to stop." -ForegroundColor Cyan

Push-Location $frontendDir
try {
    & $pnpmCommand.Source dev
    if ($LASTEXITCODE -ne 0) {
        throw "pnpm dev exited with code $LASTEXITCODE"
    }
} finally {
    Pop-Location
}
