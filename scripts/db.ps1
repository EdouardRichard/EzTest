#requires -Version 5.1
<#
.SYNOPSIS
    Manages the portable PostgreSQL 17 cluster used by EzTest local development.

.DESCRIPTION
    Actions:
      init   - initialise .tools/pgdata when missing, start the server and create
               the eztest and eztest_test databases (idempotent).
      start  - start the server on port 5433.
      stop   - stop the server (fast shutdown).
      status - report the data directory, port and pg_isready result.

    The database password is read from EZTEST_DATABASE_URL in the gitignored
    .env and is never printed. The cluster uses the portable PostgreSQL under
    .tools/pgsql (created by .tools/setup-toolchain.ps1).

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet("init", "start", "stop", "status")]
    [string]$Action = "status"
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$pgBin = Join-Path $repoRoot ".tools\pgsql\bin"
$pgData = Join-Path $repoRoot ".tools\pgdata"
$pgLog = Join-Path $repoRoot ".tools\pg.log"
$pgCtl = Join-Path $pgBin "pg_ctl.exe"
$pgIsReady = Join-Path $pgBin "pg_isready.exe"
$initdb = Join-Path $pgBin "initdb.exe"
$psql = Join-Path $pgBin "psql.exe"
$createdb = Join-Path $pgBin "createdb.exe"
$dbUser = "eztest"
$dbPort = 5433
$databases = @("eztest", "eztest_test")

function Assert-Toolchain {
    if (-not (Test-Path -LiteralPath $pgCtl)) {
        throw "PostgreSQL portable binaries are missing at $pgBin. Run: powershell.exe -NoProfile -ExecutionPolicy Bypass -File .tools/setup-toolchain.ps1"
    }
}

function Get-DatabasePassword {
    $envPath = Join-Path $repoRoot ".env"
    if (-not (Test-Path -LiteralPath $envPath)) {
        throw "Missing .env at $envPath (needed to read the database password)"
    }
    foreach ($line in Get-Content -LiteralPath $envPath) {
        if ($line -match '^\s*EZTEST_DATABASE_URL\s*=\s*(.+)$') {
            $url = $matches[1].Trim().Trim('"').Trim("'")
            if ($url -match '^postgres://[^:/@]+:([^@]+)@') {
                return [System.Uri]::UnescapeDataString($matches[1])
            }
        }
    }
    throw "Cannot read the database password from EZTEST_DATABASE_URL in .env"
}

function Test-ServerRunning {
    if (-not (Test-Path -LiteralPath (Join-Path $pgData "PG_VERSION"))) {
        return $false
    }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $pgCtl status -D $pgData *> $null
        return ($LASTEXITCODE -eq 0)
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Start-Database {
    Assert-Toolchain
    if (-not (Test-Path -LiteralPath (Join-Path $pgData "PG_VERSION"))) {
        throw "PostgreSQL cluster is not initialised at $pgData. Run: scripts\db.ps1 init"
    }
    if (Test-ServerRunning) {
        Write-Host "PostgreSQL is already running on port $dbPort."
        return
    }
    & $pgCtl -D $pgData -l $pgLog -o "-p $dbPort" -w start
    if ($LASTEXITCODE -ne 0) {
        throw "pg_ctl start failed with exit code $LASTEXITCODE; see $pgLog"
    }
    Write-Host "PostgreSQL started on port $dbPort (data: $pgData)."
}

function Stop-Database {
    Assert-Toolchain
    if (-not (Test-ServerRunning)) {
        Write-Host "PostgreSQL is not running."
        return
    }
    & $pgCtl -D $pgData -m fast -w stop
    if ($LASTEXITCODE -ne 0) {
        throw "pg_ctl stop failed with exit code $LASTEXITCODE; see $pgLog"
    }
    Write-Host "PostgreSQL stopped."
}

function Initialize-Database {
    Assert-Toolchain
    $password = Get-DatabasePassword

    if (-not (Test-Path -LiteralPath (Join-Path $pgData "PG_VERSION"))) {
        $pwFile = Join-Path $repoRoot ".tools\pgpass.init"
        try {
            [System.IO.File]::WriteAllText($pwFile, $password)
            Write-Host "Initialising PostgreSQL cluster at $pgData..."
            & $initdb -D $pgData -U $dbUser --pwfile=$pwFile -A md5 -E UTF8
            if ($LASTEXITCODE -ne 0) {
                throw "initdb failed with exit code $LASTEXITCODE"
            }
        } finally {
            Remove-Item -LiteralPath $pwFile -Force -ErrorAction SilentlyContinue
        }
    } else {
        Write-Host "PostgreSQL cluster already initialised at $pgData."
    }

    Start-Database

    $previous = $ErrorActionPreference
    $env:PGPASSWORD = $password
    $ErrorActionPreference = "SilentlyContinue"
    try {
        foreach ($database in $databases) {
            $query = "SELECT 1 FROM pg_database WHERE datname = '$database'"
            $exists = (& $psql -h localhost -p $dbPort -U $dbUser -d postgres -tA -c $query 2>$null | Out-String).Trim()
            if ($LASTEXITCODE -ne 0) {
                throw "psql failed while checking database $database"
            }
            if ($exists -ne "1") {
                & $createdb -h localhost -p $dbPort -U $dbUser $database
                if ($LASTEXITCODE -ne 0) {
                    throw "createdb $database failed with exit code $LASTEXITCODE"
                }
                Write-Host "Created database $database."
            } else {
                Write-Host "Database $database already exists."
            }
        }
    } finally {
        $ErrorActionPreference = $previous
        Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    }

    Write-Host "PostgreSQL is ready on port $dbPort."
}

function Show-DatabaseStatus {
    Assert-Toolchain
    Write-Host "Data directory: $pgData"
    Write-Host "Port:           $dbPort"
    if (-not (Test-Path -LiteralPath (Join-Path $pgData "PG_VERSION"))) {
        Write-Host "Cluster:        not initialised (run scripts\db.ps1 init)"
        return
    }
    if (-not (Test-ServerRunning)) {
        Write-Host "Server:         stopped"
        return
    }
    Write-Host "Server:         running"
    & $pgIsReady -h localhost -p $dbPort
    if ($LASTEXITCODE -ne 0) {
        Write-Host "Readiness:      pg_isready reported not ready" -ForegroundColor Yellow
    }
}

switch ($Action) {
    "init" { Initialize-Database }
    "start" { Start-Database }
    "stop" { Stop-Database }
    "status" { Show-DatabaseStatus }
}
