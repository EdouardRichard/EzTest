#requires -Version 5.1
<#
.SYNOPSIS
    Runs the EzTest Playwright E2E suite against the dedicated eztest_e2e database.

.DESCRIPTION
    Initialises/resets the eztest_e2e database from EZTEST_DATABASE_URL, builds and
    starts the API on :13333 with EZTEST_HTTP_ADDR=:13333, starts the Next.js web app
    on :6868, runs the Playwright suite with the installed Chrome/Edge channel, and
    stops both processes on exit. Secrets are read from the gitignored .env and are
    never printed.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$frontendDir = Join-Path $repoRoot "frontend"
$backendDir = Join-Path $repoRoot "backend"
$tmpDir = Join-Path $repoRoot ".tmp"
$e2eDbName = "eztest_e2e"
$apiPort = 13333
$webPort = 6868
$apiBaseUrl = "http://localhost:$apiPort"
$webBaseUrl = "http://localhost:$webPort"

New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Write-LogTail {
    param(
        [string]$Path,
        [string]$Label,
        [int]$Lines = 60
    )
    if (-not (Test-Path -LiteralPath $Path)) { return }
    Write-Host ""
    Write-Host "--- $Label (last $Lines lines) ---" -ForegroundColor Yellow
    Get-Content -LiteralPath $Path -Tail $Lines -ErrorAction SilentlyContinue | ForEach-Object {
        Write-Host $_
    }
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Missing .env at $Path (required for the e2e database, admin and secret keys)"
    }
    foreach ($line in Get-Content -LiteralPath $Path) {
        if ($line -match '^\s*#' -or $line -match '^\s*$') { continue }
        if ($line -match '^\s*([^=]+)=(.*)$') {
            $key = $matches[1].Trim()
            $value = $matches[2].Trim()
            if ($value.Length -ge 2) {
                $first = $value.Substring(0, 1)
                $last = $value.Substring($value.Length - 1, 1)
                if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                    $value = $value.Substring(1, $value.Length - 2)
                }
            }
            $values[$key] = $value
        }
    }
    return $values
}

function Get-RequiredValue {
    param($Values, [string]$Key)
    if (-not $Values.ContainsKey($Key) -or [string]::IsNullOrWhiteSpace([string]$Values[$Key])) {
        throw "Missing required value $Key in .env"
    }
    return [string]$Values[$Key]
}

function Stop-ProcessTree {
    param($Process)
    if ($null -eq $Process) { return }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $Process.Refresh()
        if (-not $Process.HasExited) {
            & taskkill.exe /PID $Process.Id /T /F 2>$null | Out-Null
        }
    } catch {
        # The process may have already exited; cleanup is best effort.
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Invoke-PsqlQuiet {
    param(
        [string]$Database,
        [string]$Sql,
        [string]$Operation
    )
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d $Database -v ON_ERROR_STOP=1 -q -c $Sql *> $null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) {
        throw "$Operation failed (psql exit $code)"
    }
}

function Get-PsqlScalar {
    param(
        [string]$Database,
        [string]$Sql
    )
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $output = & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d $Database -tA -c $Sql 2>$null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) {
        throw "psql scalar query failed (exit $code)"
    }
    return (($output | Out-String).Trim())
}

function Wait-ForHttp {
    param(
        [string]$Url,
        [int]$TimeoutSeconds,
        $Process,
        [string]$Label
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($null -ne $Process) {
            $Process.Refresh()
            if ($Process.HasExited) {
                throw "$Label exited early with code $($Process.ExitCode)"
            }
        }
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 10
            if ($response.StatusCode -eq 200) { return }
        } catch {
            # Not ready yet.
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Timed out after ${TimeoutSeconds}s waiting for $Label at $Url"
}

function Assert-PortFree {
    param([int]$Port)
    $listeners = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue
    if ($listeners) {
        throw "Port $Port is already in use; stop the other process before running the E2E suite"
    }
}

$exitCode = 0
$apiProcess = $null
$webProcess = $null
$apiOut = Join-Path $tmpDir "e2e-api.out.log"
$apiErr = Join-Path $tmpDir "e2e-api.err.log"
$webOut = Join-Path $tmpDir "e2e-web.out.log"
$webErr = Join-Path $tmpDir "e2e-web.err.log"
$playwrightLog = Join-Path $tmpDir "e2e-playwright.log"

try {
    Assert-PortFree -Port $apiPort
    Assert-PortFree -Port $webPort

    if (-not (Test-Path -LiteralPath (Join-Path $frontendDir "node_modules\next\dist\bin\next"))) {
        throw "frontend/node_modules is missing; run pnpm install in frontend first"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $frontendDir "node_modules\.bin\playwright.CMD"))) {
        throw "frontend/node_modules/.bin/playwright.CMD is missing; run pnpm install in frontend first"
    }

    $envValues = Read-DotEnv -Path (Join-Path $repoRoot ".env")
    $databaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_DATABASE_URL"
    $adminUsername = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_USERNAME"
    $adminPassword = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_PASSWORD"

    $databaseUri = [System.Uri]$databaseUrl
    if (-not $databaseUri.IsAbsoluteUri) {
        throw "EZTEST_DATABASE_URL is not an absolute URL"
    }
    $userInfo = $databaseUri.UserInfo
    $separator = $userInfo.IndexOf(":")
    if ($separator -lt 0) {
        throw "EZTEST_DATABASE_URL must contain a user and password"
    }
    $dbUser = [System.Uri]::UnescapeDataString($userInfo.Substring(0, $separator))
    $dbPassword = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
    $dbHost = $databaseUri.Host
    $dbPort = $databaseUri.Port

    $queryIndex = $databaseUrl.IndexOf("?")
    if ($queryIndex -ge 0) {
        $urlBase = $databaseUrl.Substring(0, $queryIndex)
        $urlQuery = $databaseUrl.Substring($queryIndex)
    } else {
        $urlBase = $databaseUrl
        $urlQuery = ""
    }
    $slashIndex = $urlBase.LastIndexOf("/")
    if ($slashIndex -lt 0) {
        throw "EZTEST_DATABASE_URL does not contain a database name"
    }
    $e2eDatabaseUrl = $urlBase.Substring(0, $slashIndex + 1) + $e2eDbName + $urlQuery

    $goExe = Join-Path $repoRoot ".tools\go\bin\go.exe"
    if (-not (Test-Path -LiteralPath $goExe)) { $goExe = "go" }
    $psqlExe = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    if (-not (Test-Path -LiteralPath $psqlExe)) { $psqlExe = "psql" }

    # The installed browser channel: Chrome first, Edge as fallback.
    $channel = $env:EZTEST_E2E_CHANNEL
    if ([string]::IsNullOrWhiteSpace($channel)) {
        $chromePaths = @()
        $edgePaths = @()
        if (-not [string]::IsNullOrWhiteSpace($env:ProgramFiles)) {
            $chromePaths += Join-Path $env:ProgramFiles "Google\Chrome\Application\chrome.exe"
            $edgePaths += Join-Path $env:ProgramFiles "Microsoft\Edge\Application\msedge.exe"
        }
        if (-not [string]::IsNullOrWhiteSpace(${env:ProgramFiles(x86)})) {
            $chromePaths += Join-Path ${env:ProgramFiles(x86)} "Google\Chrome\Application\chrome.exe"
            $edgePaths += Join-Path ${env:ProgramFiles(x86)} "Microsoft\Edge\Application\msedge.exe"
        }
        $chromeFound = $false
        foreach ($candidate in $chromePaths) {
            if (Test-Path -LiteralPath $candidate) { $chromeFound = $true }
        }
        if ($chromeFound) {
            $channel = "chrome"
        } else {
            $edgeFound = $false
            foreach ($candidate in $edgePaths) {
                if (Test-Path -LiteralPath $candidate) { $edgeFound = $true }
            }
            if (-not $edgeFound) {
                throw "Neither Chrome nor Edge was found; Playwright must use an installed browser channel"
            }
            $channel = "msedge"
        }
    }
    if ($channel -eq "edge") { $channel = "msedge" }

    Write-Step "Resetting database $e2eDbName"
    $env:PGPASSWORD = $dbPassword
    try {
        $exists = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$e2eDbName';"
        if ($exists -ne "0") {
            $terminateSql = "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$e2eDbName' AND pid <> pg_backend_pid();"
            Invoke-PsqlQuiet -Database "postgres" -Sql $terminateSql -Operation "terminating connections to $e2eDbName"
            Invoke-PsqlQuiet -Database "postgres" -Sql "DROP DATABASE $e2eDbName;" -Operation "dropping database $e2eDbName"
        }
        Invoke-PsqlQuiet -Database "postgres" -Sql "CREATE DATABASE $e2eDbName;" -Operation "creating database $e2eDbName"
    } finally {
        Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    }

    Write-Step "Building API"
    Push-Location $backendDir
    try {
        & $goExe build -o (Join-Path $tmpDir "e2e-api.exe") ./cmd/eztest
        if ($LASTEXITCODE -ne 0) { throw "go build ./cmd/eztest failed" }
    } finally {
        Pop-Location
    }

    Write-Step "Starting API on :$apiPort (database $e2eDbName)"
    $env:EZTEST_ENV = "dev"
    $env:EZTEST_DATABASE_URL = $e2eDatabaseUrl
    $env:EZTEST_HTTP_ADDR = ":$apiPort"
    $env:EZTEST_WEB_ORIGIN = $webBaseUrl
    $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
    $env:EZTEST_STORAGE_DIR = (Join-Path $tmpDir "e2e-objects")
    $env:EZTEST_ADMIN_USERNAME = $adminUsername
    $env:EZTEST_ADMIN_PASSWORD = $adminPassword
    foreach ($key in @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "llm_base_url", "llm_key", "llm_model_name")) {
        if ($envValues.ContainsKey($key)) {
            Set-Item -Path ("Env:" + $key) -Value $envValues[$key]
        }
    }
    Remove-Item Env:EZTEST_TEST_DATABASE_URL -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $apiOut, $apiErr -ErrorAction SilentlyContinue
    $apiProcess = Start-Process -FilePath (Join-Path $tmpDir "e2e-api.exe") `
        -WorkingDirectory $backendDir `
        -RedirectStandardOutput $apiOut `
        -RedirectStandardError $apiErr `
        -WindowStyle Hidden `
        -PassThru
    Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 90 -Process $apiProcess -Label "API"
    Write-Host "API is healthy at $apiBaseUrl"

    Write-Step "Starting web on :$webPort"
    $env:NEXT_PUBLIC_API_BASE_URL = $apiBaseUrl
    $env:NEXT_PUBLIC_APP_ENV = "test"
    $env:NEXT_TELEMETRY_DISABLED = "1"
    $nodeExe = (Get-Command node.exe -ErrorAction SilentlyContinue).Source
    if (-not $nodeExe) { $nodeExe = (Get-Command node).Source }
    $nextBin = Join-Path $frontendDir "node_modules\next\dist\bin\next"
    Remove-Item -LiteralPath $webOut, $webErr -ErrorAction SilentlyContinue
    $webProcess = Start-Process -FilePath $nodeExe `
        -ArgumentList @("`"$nextBin`"", "dev", "-p", "$webPort") `
        -WorkingDirectory $frontendDir `
        -RedirectStandardOutput $webOut `
        -RedirectStandardError $webErr `
        -WindowStyle Hidden `
        -PassThru
    Wait-ForHttp -Url "$webBaseUrl/login" -TimeoutSeconds 180 -Process $webProcess -Label "Web"
    Write-Host "Web is ready at $webBaseUrl"

    Write-Step "Running Playwright (channel: $channel, database: $e2eDbName)"
    $env:EZTEST_E2E_ADMIN_USERNAME = $adminUsername
    $env:EZTEST_E2E_ADMIN_PASSWORD = $adminPassword
    $env:EZTEST_E2E_USER_PASSWORD = "qa-e2e-pass-123"
    $env:EZTEST_E2E_API_URL = $apiBaseUrl
    $env:EZTEST_E2E_BASE_URL = $webBaseUrl
    $env:EZTEST_E2E_CHANNEL = $channel
    # The harness may set both NO_COLOR and FORCE_COLOR; Node then prints a
    # warning to stderr, which Windows PowerShell surfaces as a noisy error
    # record while the Playwright output is redirected to a file.
    Remove-Item Env:FORCE_COLOR -ErrorAction SilentlyContinue
    Remove-Item Env:NO_COLOR -ErrorAction SilentlyContinue

    $playwright = Join-Path $frontendDir "node_modules\.bin\playwright.CMD"
    Remove-Item -LiteralPath $playwrightLog -ErrorAction SilentlyContinue
    Push-Location $frontendDir
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & $playwright test *> $playwrightLog
        $playwrightExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        Pop-Location
    }
    if (Test-Path -LiteralPath $playwrightLog) {
        Get-Content -LiteralPath $playwrightLog | ForEach-Object { Write-Host $_ }
    }
    if ($playwrightExit -ne 0) {
        throw "Playwright exited with code $playwrightExit"
    }

    Write-Step "E2E PASSED"
} catch {
    Write-Host ""
    Write-Host "E2E FAILED: $($_.Exception.Message)" -ForegroundColor Red
    $exitCode = 1
    Write-LogTail -Path $apiOut -Label "API stdout" -Lines 80
    Write-LogTail -Path $apiErr -Label "API stderr" -Lines 80
    Write-LogTail -Path $webOut -Label "Web stdout" -Lines 80
    Write-LogTail -Path $webErr -Label "Web stderr" -Lines 80
    Write-LogTail -Path $playwrightLog -Label "Playwright" -Lines 120
} finally {
    Write-Step "Cleaning up"
    Stop-ProcessTree -Process $webProcess
    Stop-ProcessTree -Process $apiProcess
    Start-Sleep -Milliseconds 500
    foreach ($port in @($webPort, $apiPort)) {
        $listeners = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue
        if ($listeners) {
            Write-Host "WARNING: port $port still has a listener after cleanup" -ForegroundColor Yellow
        }
    }
    Remove-Item Env:EZTEST_E2E_ADMIN_PASSWORD -ErrorAction SilentlyContinue
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
}

exit $exitCode
