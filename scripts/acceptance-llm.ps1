#requires -Version 5.1
<#
.SYNOPSIS
    Optional real-LLM acceptance for EzTest (gated by EZTEST_RUN_REAL_LLM=1).

.DESCRIPTION
    Boots the API against a freshly migrated eztest_e2e database (which seeds the
    default LLM configuration from .env), logs in as the admin, and calls the
    saved configuration's /test endpoint twice:
      * Chat Completions with the built-in 1x1 PNG image;
      * Responses with a text prompt.
    Both calls must return HTTP 200 and a non-empty text result. The script is a
    no-op that prints SKIPPED unless EZTEST_RUN_REAL_LLM=1. Secrets are read from
    .env and are never printed; only lengths/statuses are reported.

.NOTES
    This script makes paid upstream calls. It is deliberately not part of
    scripts/acceptance.ps1, which reports it as SKIPPED.
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$tmpDir = Join-Path $repoRoot ".tmp"
$e2eDbName = "eztest_e2e"
$apiPort = 13333
$apiBaseUrl = "http://localhost:$apiPort"

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) { throw "Missing .env at $Path" }
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
        # Best-effort cleanup.
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Assert-PortFree {
    param([int]$Port)
    $listeners = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue
    if ($listeners) { throw "Port $Port is already in use; stop the other process first" }
}

function Wait-ForHttp {
    param([string]$Url, [int]$TimeoutSeconds, $Process, [string]$Label)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($null -ne $Process) {
            $Process.Refresh()
            if ($Process.HasExited) { throw "$Label exited early with code $($Process.ExitCode)" }
        }
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 10
            if ($response.StatusCode -eq 200) { return }
        } catch {
            # Not ready yet.
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Timed out waiting for $Label at $Url"
}

function Get-PsqlScalar {
    param([string]$Database, [string]$Sql)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        $output = & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d $Database -tA -c $Sql 2>$null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "psql scalar query failed (exit $code)" }
    return (($output | Out-String).Trim())
}

function Invoke-PsqlQuiet {
    param([string]$Database, [string]$Sql, [string]$Operation)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d $Database -v ON_ERROR_STOP=1 -q -c $Sql *> $null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "$Operation failed (psql exit $code)" }
}

function ConvertTo-ApiBody {
    param($Value)
    return ($Value | ConvertTo-Json -Compress -Depth 8)
}

function Get-ApiErrorSummary {
    param($ErrorRecord)
    $message = $ErrorRecord.Exception.Message
    try {
        $response = $ErrorRecord.Exception.Response
        if ($null -eq $response) { return $message }
        $status = [int]$response.StatusCode
        $body = $null
        if ($response -is [System.Net.HttpWebResponse]) {
            $stream = $response.GetResponseStream()
            $reader = New-Object System.IO.StreamReader($stream)
            $body = $reader.ReadToEnd()
        } elseif ($null -ne $response.Content) {
            $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        }
        if (-not [string]::IsNullOrWhiteSpace($body)) {
            $parsed = $body | ConvertFrom-Json
            $code = [string]$parsed.error.code
            $detail = [string]$parsed.error.message
            if ($code -or $detail) {
                return "HTTP $status ${code}: $detail"
            }
        }
        return "HTTP $status"
    } catch {
        return $message
    }
}

$exitCode = 0
$apiProcess = $null

try {
    if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
        Write-Host "SKIPPED: real LLM acceptance is disabled (set EZTEST_RUN_REAL_LLM=1 to run it)."
        exit 0
    }

    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
    $envValues = Read-DotEnv -Path (Join-Path $repoRoot ".env")
    $adminUsername = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_USERNAME"
    $adminPassword = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_PASSWORD"

    $llmKey = ""
    if ($envValues.ContainsKey("llm_key")) { $llmKey = [string]$envValues["llm_key"] }
    $llmBaseURL = ""
    if ($envValues.ContainsKey("llm_base_url")) { $llmBaseURL = [string]$envValues["llm_base_url"] }
    $llmModel = ""
    if ($envValues.ContainsKey("llm_model_name")) { $llmModel = [string]$envValues["llm_model_name"] }
    if ([string]::IsNullOrWhiteSpace($llmKey) -or [string]::IsNullOrWhiteSpace($llmBaseURL) -or [string]::IsNullOrWhiteSpace($llmModel)) {
        Write-Host "SKIPPED: .env does not provide llm_base_url/llm_key/llm_model_name, so no real LLM call can be made."
        exit 0
    }

    $databaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_DATABASE_URL"
    $databaseUri = [System.Uri]$databaseUrl
    if (-not $databaseUri.IsAbsoluteUri) { throw "EZTEST_DATABASE_URL is not an absolute URL" }
    $userInfo = $databaseUri.UserInfo
    $separator = $userInfo.IndexOf(":")
    if ($separator -lt 0) { throw "EZTEST_DATABASE_URL must contain a user and password" }
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
    if ($slashIndex -lt 0) { throw "EZTEST_DATABASE_URL does not contain a database name" }
    $e2eDatabaseUrl = $urlBase.Substring(0, $slashIndex + 1) + $e2eDbName + $urlQuery

    $goExe = Join-Path $repoRoot ".tools\go\bin\go.exe"
    if (-not (Test-Path -LiteralPath $goExe)) { $goExe = "go" }
    $psqlExe = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    if (-not (Test-Path -LiteralPath $psqlExe)) { $psqlExe = "psql" }

    Assert-PortFree -Port $apiPort

    Write-Step "Resetting database $e2eDbName and building the API"
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

    $apiExe = Join-Path $tmpDir "acceptance-llm-api.exe"
    Push-Location $backendDir
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $goExe build -o $apiExe ./cmd/eztest *> (Join-Path $tmpDir "acceptance-llm-build.log")
        $buildExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        Pop-Location
    }
    if ($buildExit -ne 0) { throw "go build ./cmd/eztest failed (see .tmp/acceptance-llm-build.log)" }

    Write-Step "Starting API on :$apiPort"
    $env:EZTEST_ENV = "dev"
    $env:EZTEST_DATABASE_URL = $e2eDatabaseUrl
    $env:EZTEST_HTTP_ADDR = ":$apiPort"
    $env:EZTEST_WEB_ORIGIN = "http://localhost:6868"
    $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
    $env:EZTEST_STORAGE_DIR = (Join-Path $tmpDir "e2e-objects")
    $env:EZTEST_ADMIN_USERNAME = $adminUsername
    $env:EZTEST_ADMIN_PASSWORD = $adminPassword
    foreach ($key in @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "llm_base_url", "llm_key", "llm_model_name")) {
        if ($envValues.ContainsKey($key)) { Set-Item -Path ("Env:" + $key) -Value $envValues[$key] }
    }
    Remove-Item Env:EZTEST_TEST_DATABASE_URL -ErrorAction SilentlyContinue

    $apiOut = Join-Path $tmpDir "acceptance-llm-api.out.log"
    $apiErr = Join-Path $tmpDir "acceptance-llm-api.err.log"
    Remove-Item -LiteralPath $apiOut, $apiErr -ErrorAction SilentlyContinue
    $apiProcess = Start-Process -FilePath $apiExe `
        -WorkingDirectory $backendDir `
        -RedirectStandardOutput $apiOut `
        -RedirectStandardError $apiErr `
        -WindowStyle Hidden `
        -PassThru
    try {
        Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 90 -Process $apiProcess -Label "API"
    } catch {
        Stop-ProcessTree -Process $apiProcess
        $apiProcess = $null
        throw
    }

    $health = Invoke-RestMethod -Uri "$apiBaseUrl/api/health" -TimeoutSec 20
    if ($health.data.llm_configured -ne $true) {
        throw "health reports llm_configured=false after seeding the default configuration"
    }

    Write-Step "Logging in as admin and loading the default LLM configuration"
    $loginPayload = ConvertTo-ApiBody @{ username = $adminUsername; password = $adminPassword }
    $login = Invoke-RestMethod -Method Post -Uri "$apiBaseUrl/api/auth/login" `
        -ContentType "application/json" -Body $loginPayload -TimeoutSec 20
    $token = [string]$login.data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "admin login did not return a token" }
    $headers = @{ Authorization = "Bearer $token" }

    $configs = Invoke-RestMethod -Uri "$apiBaseUrl/api/admin/llm/configs" -Headers $headers -TimeoutSec 20
    $default = @($configs.data | Where-Object { $_.is_default } | Select-Object -First 1)
    if ($default.Count -eq 0) { throw "no default LLM configuration exists after seeding" }
    $configId = [int64]$default[0].id
    $originalProtocol = [string]$default[0].protocol
    if ([string]::IsNullOrWhiteSpace($originalProtocol)) { $originalProtocol = "chat_completions" }
    $restoreProtocol = $originalProtocol

    try {
        if ($originalProtocol -ne "chat_completions") {
            $patchBody = ConvertTo-ApiBody @{ protocol = "chat_completions" }
            Invoke-RestMethod -Method Patch -Uri "$apiBaseUrl/api/admin/llm/configs/$configId" `
                -Headers $headers -ContentType "application/json" -Body $patchBody -TimeoutSec 20 | Out-Null
        }

        Write-Step "Real LLM test: Chat Completions with image"
        $chatBody = ConvertTo-ApiBody @{
            image  = $true
            stream = $false
        }
        $chatResult = Invoke-RestMethod -Method Post -Uri "$apiBaseUrl/api/admin/llm/configs/$configId/test" `
            -Headers $headers -ContentType "application/json" -Body $chatBody -TimeoutSec 180
        $chatText = [string]$chatResult.data.text
        if ([string]::IsNullOrWhiteSpace($chatText)) {
            throw "Chat Completions image test returned an empty text"
        }
        Write-Host "PASS Chat Completions image test: non-empty text ($($chatText.Length) chars)." -ForegroundColor Green

        Write-Step "Real LLM test: Responses with text"
        $responsesBody = ConvertTo-ApiBody @{
            image  = $false
            stream = $false
            prompt = "Reply with: pong"
        }
        $patchBody = ConvertTo-ApiBody @{ protocol = "responses" }
        Invoke-RestMethod -Method Patch -Uri "$apiBaseUrl/api/admin/llm/configs/$configId" `
            -Headers $headers -ContentType "application/json" -Body $patchBody -TimeoutSec 20 | Out-Null
        $responsesResult = Invoke-RestMethod -Method Post -Uri "$apiBaseUrl/api/admin/llm/configs/$configId/test" `
            -Headers $headers -ContentType "application/json" -Body $responsesBody -TimeoutSec 180
        $responsesText = [string]$responsesResult.data.text
        if ([string]::IsNullOrWhiteSpace($responsesText)) {
            throw "Responses text test returned an empty text"
        }
        Write-Host "PASS Responses text test: non-empty text ($($responsesText.Length) chars)." -ForegroundColor Green

        Write-Host ""
        Write-Host "REAL LLM ACCEPTANCE PASSED" -ForegroundColor Green
    } catch {
        $summary = Get-ApiErrorSummary -ErrorRecord $_
        throw "real LLM acceptance failed: $summary"
    } finally {
        if ($null -ne $apiProcess -and -not $apiProcess.HasExited) {
            try {
                $restoreBody = ConvertTo-ApiBody @{ protocol = $restoreProtocol }
                Invoke-RestMethod -Method Patch -Uri "$apiBaseUrl/api/admin/llm/configs/$configId" `
                    -Headers $headers -ContentType "application/json" -Body $restoreBody -TimeoutSec 20 | Out-Null
                Write-Host "Restored the default configuration protocol to $restoreProtocol."
            } catch {
                Write-Host "WARNING: could not restore the default configuration protocol." -ForegroundColor Yellow
            }
        }
    }
} catch {
    Write-Host ""
    Write-Host "REAL LLM ACCEPTANCE FAILED: $($_.Exception.Message)" -ForegroundColor Red
    $exitCode = 1
} finally {
    Stop-ProcessTree -Process $apiProcess
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
}

exit $exitCode
