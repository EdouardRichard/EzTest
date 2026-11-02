#requires -Version 5.1
<#
.SYNOPSIS
    Optional real-LLM acceptance for the EzTest Agent runtime (gated by
    EZTEST_RUN_REAL_LLM=1).

.DESCRIPTION
    Boots the API against a freshly reset eztest_e2e database (which seeds the
    builtin runtime-diagnostic profile and the default LLM configuration from
    .env), logs in as the admin, and drives one real diagnostic run:
      * GET /api/admin/llm/configs and PATCH the default configuration's
        timeout_ms to 180000 (the 60 s seed default is too short for the
        thinking=max profile);
      * GET /api/agents/profiles and pick key=runtime-diagnostic;
      * POST /api/agents/sessions;
      * POST .../messages with the diagnostic prompt (http.get + README);
      * poll the session until the run reaches a terminal state (300 s cap),
        then retry the event snapshot briefly until run_finished is visible
        (the API writes the terminal row before publishing the event);
      * read the agent channel's event snapshot and assert the activity
        contract: run_started/thinking/text/tool_use/tool_result/usage/
        run_finished, at least one successful tool result, and non-zero
        reasoning tokens.
    It prints PASS only after every assertion holds. Secrets are read from .env
    and are never printed: only statuses, error codes and event kinds are
    reported.

.PARAMETER DryRun
    Validate the script's paths and the .env key presence (including the
    EZTEST_SECRET_KEYS/EZTEST_SECRET_KEY_ACTIVE variables) without resetting
    the database, building or starting the API, or calling the LLM. Exits 0
    when the preflight passes.

.NOTES
    This script makes paid upstream calls. It is deliberately not part of
    scripts/acceptance.ps1, which reports it as gated. When the gate is open
    (EZTEST_RUN_REAL_LLM=1), missing .env LLM keys are a failure, never a skip.
    PostgreSQL must already be running; the script resets the eztest_e2e database
    and leaves PostgreSQL running. Compatible with Windows PowerShell 5.1 and
    PowerShell 7+.
#>
[CmdletBinding()]
param(
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$tmpDir = Join-Path $repoRoot ".tmp"
$envPath = Join-Path $repoRoot ".env"
$e2eDbName = "eztest_e2e"
$apiPort = 13333
$apiBaseUrl = "http://localhost:$apiPort"
# The snapshot endpoint caps limit at 100 (platform.MaxLimit), so the plan's
# limit=200 would be rejected with HTTP 400. Fetch up to two pages instead so
# the newest <=200 events are inspected, as the plan intended.
$eventsLimit = 100
$maxEventPages = 2
$pollIntervalSeconds = 2
$pollTimeoutSeconds = 300
# The default LLM configuration is seeded with timeout_ms=60000, which is too
# short for a thinking=max diagnostic run; the script raises it before the run.
$llmConfigsPath = "/api/admin/llm/configs"
$llmTimeoutMs = 180000
# The API writes the terminal run row before publishing run_finished, so a
# running=false poll can race the event; retry the snapshot briefly first.
$runFinishedWaitAttempts = 5
$runFinishedWaitIntervalSeconds = 1
$diagnosticPrompt = "请检查 EzTest 运行时健康（调用 http.get 访问 http://localhost:13333/api/health），并读取 README 的构建说明。"
$requiredKinds = @("run_started", "thinking", "text", "tool_use", "tool_result", "usage", "run_finished")

# Secrets parsed from .env live here only so error text can be redacted. They
# are never printed, logged, or written anywhere.
$script:secretValues = @()
$script:sessionId = $null
$script:runId = $null
$script:token = $null
$script:eventItems = $null

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

function Resolve-ToolPath {
    param([string]$LocalPath, [string]$CommandName)
    if (Test-Path -LiteralPath $LocalPath -PathType Leaf) { return $LocalPath }
    $command = Get-Command $CommandName -ErrorAction SilentlyContinue
    if ($null -ne $command -and -not [string]::IsNullOrWhiteSpace([string]$command.Source)) {
        return [string]$command.Source
    }
    return ""
}

function Get-RedactedText {
    param([string]$Text)
    if ([string]::IsNullOrEmpty($Text)) { return "" }
    foreach ($secret in $script:secretValues) {
        if (-not [string]::IsNullOrEmpty($secret)) {
            $Text = $Text.Replace($secret, "[redacted]")
        }
    }
    $Text = $Text -replace "[\r\n]+", " "
    if ($Text.Length -gt 400) { $Text = $Text.Substring(0, 400) + "..." }
    return $Text
}

function ConvertTo-Int64OrZero {
    param($Value)
    if ($null -eq $Value) { return [int64]0 }
    $parsed = [int64]0
    if ([int64]::TryParse([string]$Value, [ref]$parsed)) { return $parsed }
    return [int64]0
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

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Uri,
        $Body = $null,
        $Headers = $null,
        [int]$TimeoutSec = 30
    )
    $arguments = @{
        Method     = $Method
        Uri        = $Uri
        TimeoutSec = $TimeoutSec
    }
    if ($null -ne $Headers) { $arguments["Headers"] = $Headers }
    if ($null -ne $Body) {
        $json = $Body | ConvertTo-Json -Compress -Depth 8
        $arguments["ContentType"] = "application/json; charset=utf-8"
        # Windows PowerShell 5.1 would otherwise encode the Chinese prompt with
        # the ANSI code page, so always send explicit UTF-8 bytes.
        $arguments["Body"] = [System.Text.Encoding]::UTF8.GetBytes($json)
    }
    return Invoke-RestMethod @arguments
}

function Get-AgentEventItems {
    param([hashtable]$Headers, [int64]$SessionId)
    $channel = [System.Uri]::EscapeDataString("agent:$SessionId")
    $items = @()
    $beforeSeq = [int64]0
    for ($page = 0; $page -lt $maxEventPages; $page++) {
        $url = "$apiBaseUrl/api/stream/events?channel=$channel&limit=$eventsLimit"
        if ($beforeSeq -gt 0) { $url = $url + "&before_seq=$beforeSeq" }
        $response = Invoke-Api -Method Get -Uri $url -Headers $Headers -TimeoutSec 20
        $pageItems = @()
        if ($null -ne $response.data.items) { $pageItems = @($response.data.items) }
        if ($pageItems.Count -eq 0) { break }
        $items = $pageItems + $items
        if (-not [bool]$response.data.has_more) { break }
        # snapshot items are ascending, so the oldest seq is the backward cursor.
        $beforeSeq = ConvertTo-Int64OrZero -Value $pageItems[0].seq
        if ($beforeSeq -le 0) { break }
    }
    return $items
}

function Wait-AgentEventKind {
    param(
        [hashtable]$Headers,
        [int64]$SessionId,
        [string]$Kind,
        [int]$Attempts = 5,
        [int]$IntervalSeconds = 1
    )
    # The API writes the terminal run row before publishing run_finished, so
    # running=false does not guarantee the event is in the snapshot yet.
    $items = @()
    for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
        $items = @(Get-AgentEventItems -Headers $Headers -SessionId $SessionId)
        foreach ($item in $items) {
            if ($null -eq $item -or $null -eq $item.detail) { continue }
            if ([string]$item.detail.kind -eq $Kind) { return $items }
        }
        if ($attempt -lt $Attempts) { Start-Sleep -Seconds $IntervalSeconds }
    }
    return $items
}

function Get-DefaultLLMConfig {
    param([hashtable]$Headers)
    $response = Invoke-Api -Method Get -Uri "${apiBaseUrl}${llmConfigsPath}" -Headers $Headers -TimeoutSec 20
    $defaults = @()
    if ($null -ne $response.data) {
        $defaults = @($response.data | Where-Object { $null -ne $_ -and [bool]$_.is_default })
    }
    if ($defaults.Count -eq 0) {
        throw "no default LLM configuration was returned by GET ${llmConfigsPath}"
    }
    return $defaults[0]
}

function Set-DefaultLLMTimeout {
    param([hashtable]$Headers, [int64]$ConfigId, [int]$TimeoutMs)
    # PATCH is a partial update: only timeout_ms is sent, so the server keeps
    # every other stored field (name, protocol, model, api_key, headers, ...).
    $response = Invoke-Api -Method Patch -Uri "${apiBaseUrl}${llmConfigsPath}/${ConfigId}" -Headers $Headers -Body @{ timeout_ms = $TimeoutMs } -TimeoutSec 20
    $applied = [int64]-1
    if ($null -ne $response.data -and $null -ne $response.data.timeout_ms) {
        $applied = ConvertTo-Int64OrZero -Value $response.data.timeout_ms
    }
    if ($applied -ne [int64]$TimeoutMs) {
        throw "the LLM configuration update did not apply timeout_ms=$TimeoutMs"
    }
    return $response
}

function Get-ApiErrorSummary {
    param($ErrorRecord)
    $status = 0
    $code = ""
    $reason = ""
    $rawBody = $null
    try {
        $response = $ErrorRecord.Exception.Response
        if ($null -ne $response) {
            $status = [int]$response.StatusCode
            if ($response -is [System.Net.HttpWebResponse]) {
                $stream = $response.GetResponseStream()
                if ($null -ne $stream) {
                    $reader = New-Object System.IO.StreamReader($stream)
                    try { $rawBody = $reader.ReadToEnd() } finally { $reader.Close() }
                }
            } elseif ($null -ne $response.Content) {
                $rawBody = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            }
        }
    } catch {
        # Keep only what was extracted safely.
    }
    if ([string]::IsNullOrWhiteSpace($rawBody)) {
        try {
            if ($null -ne $ErrorRecord.ErrorDetails -and -not [string]::IsNullOrWhiteSpace($ErrorRecord.ErrorDetails.Message)) {
                $rawBody = $ErrorRecord.ErrorDetails.Message
            }
        } catch {
            # No body available.
        }
    }
    if (-not [string]::IsNullOrWhiteSpace($rawBody)) {
        # Only the shared error envelope's code and details.reason are reported;
        # the body, headers and any echoed request data are deliberately
        # discarded.
        try {
            $parsed = $rawBody | ConvertFrom-Json
            $code = [string]$parsed.error.code
            if ($null -ne $parsed.error.details -and $null -ne $parsed.error.details.reason) {
                $reason = [string]$parsed.error.details.reason
            }
        } catch {
            $code = ""
            $reason = ""
        }
    }
    if ($status -gt 0) {
        $parts = @("HTTP $status")
        if (-not [string]::IsNullOrWhiteSpace($code)) { $parts += "error_code=$code" }
        if (-not [string]::IsNullOrWhiteSpace($reason)) { $parts += "reason=$reason" }
        return ($parts -join " ")
    }
    return (Get-RedactedText -Text ([string]$ErrorRecord.Exception.Message))
}

function Show-AgentFailureDiagnostics {
    if ($null -eq $script:sessionId -or [string]::IsNullOrWhiteSpace([string]$script:token)) { return }
    try {
        $headers = @{ Authorization = "Bearer $($script:token)" }
        $session = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/agents/sessions/$($script:sessionId)" -Headers $headers -TimeoutSec 20
        $run = $session.data.last_run
        if ($null -ne $run) {
            $status = [string]$run.status
            $reason = [string]$run.terminal_reason
            $errorCode = ""
            if ($null -ne $run.error_code) { $errorCode = [string]$run.error_code }
            Write-Host ("last_run status={0} terminal_reason={1} error_code={2}" -f $status, $reason, $errorCode) -ForegroundColor Yellow
        } else {
            Write-Host ("last_run=<none> running={0}" -f [string]$session.data.running) -ForegroundColor Yellow
        }
        $items = @()
        if ($null -ne $script:eventItems) {
            $items = @($script:eventItems)
        } else {
            $items = @(Get-AgentEventItems -Headers $headers -SessionId $script:sessionId)
        }
        $kinds = @()
        foreach ($item in $items) {
            if ($null -eq $item -or $null -eq $item.detail) { continue }
            $kind = [string]$item.detail.kind
            if (-not [string]::IsNullOrWhiteSpace($kind)) { $kinds += $kind }
        }
        $uniqueKinds = @($kinds | Select-Object -Unique)
        Write-Host ("event_kinds=" + ($uniqueKinds -join ",")) -ForegroundColor Yellow
    } catch {
        Write-Host "diagnostics unavailable (the API may already be stopped)." -ForegroundColor Yellow
    }
}

function Invoke-DryRun {
    Write-Step "DryRun: validating paths and environment (no database reset, no API, no LLM call)"
    $problems = @()

    if (-not (Test-Path -LiteralPath $repoRoot -PathType Container)) { $problems += "repository root not found" }
    if (-not (Test-Path -LiteralPath $backendDir -PathType Container)) { $problems += "backend directory not found" }

    $envValues = $null
    $requiredKeys = @("EZTEST_DATABASE_URL", "EZTEST_ADMIN_USERNAME", "EZTEST_ADMIN_PASSWORD", "EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "llm_base_url", "llm_key", "llm_model_name")
    $presentKeys = @()
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
        $problems += ".env not found at $envPath"
    } else {
        $envValues = Read-DotEnv -Path $envPath
        foreach ($key in $requiredKeys) {
            if (-not $envValues.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$envValues[$key])) {
                $problems += "missing .env key: $key"
            } else {
                $presentKeys += $key
            }
        }
    }

    $goExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\go\bin\go.exe") -CommandName "go"
    $psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql"
    if ([string]::IsNullOrWhiteSpace($goExe)) { $problems += "go executable not found (.tools or PATH)" }
    if ([string]::IsNullOrWhiteSpace($psqlExe)) { $problems += "psql executable not found (.tools or PATH)" }

    if ($problems.Count -gt 0) {
        Write-Host "DRYRUN FAIL:" -ForegroundColor Red
        foreach ($problem in $problems) { Write-Host "  - $problem" -ForegroundColor Red }
        return 1
    }

    Write-Host "DryRun: repository root: $repoRoot"
    Write-Host "DryRun: backend directory: $backendDir"
    Write-Host ("DryRun: .env found; required keys present ({0}/{1}): {2}" -f $presentKeys.Count, $requiredKeys.Count, ($presentKeys -join ", "))
    Write-Host "DryRun: go executable found: $goExe"
    Write-Host "DryRun: psql executable found: $psqlExe"
    if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
        Write-Host "DryRun: gate is closed (EZTEST_RUN_REAL_LLM != 1); the real run would print SKIPPED and exit 0."
    } else {
        Write-Host "DryRun: gate is open; the real run would reset $e2eDbName, build/start the API on :$apiPort, and spend one paid LLM run."
    }
    Write-Host "DryRun: the real run will PATCH the default LLM configuration timeout_ms to $llmTimeoutMs via the admin API."
    Write-Host "DryRun: port :$apiPort is not probed (another process may own it); the real run asserts it is free."
    Write-Host "DRYRUN OK" -ForegroundColor Green
    return 0
}

if ($DryRun) {
    exit (Invoke-DryRun)
}

if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
    Write-Host "SKIPPED: real Agent LLM acceptance is disabled (set EZTEST_RUN_REAL_LLM=1 to run it)."
    exit 0
}

$exitCode = 0
$apiProcess = $null

try {
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null

    $envValues = Read-DotEnv -Path $envPath

    $llmKey = ""
    if ($envValues.ContainsKey("llm_key")) { $llmKey = [string]$envValues["llm_key"] }
    $llmBaseURL = ""
    if ($envValues.ContainsKey("llm_base_url")) { $llmBaseURL = [string]$envValues["llm_base_url"] }
    $llmModel = ""
    if ($envValues.ContainsKey("llm_model_name")) { $llmModel = [string]$envValues["llm_model_name"] }

    # The gate was explicitly opened, so a missing LLM key is a failure rather
    # than a skip. Only the key names are reported; values are never printed.
    $missingLlmKeys = @()
    if ([string]::IsNullOrWhiteSpace($llmKey)) { $missingLlmKeys += "llm_key" }
    if ([string]::IsNullOrWhiteSpace($llmBaseURL)) { $missingLlmKeys += "llm_base_url" }
    if ([string]::IsNullOrWhiteSpace($llmModel)) { $missingLlmKeys += "llm_model_name" }
    if ($missingLlmKeys.Count -gt 0) {
        Write-Host ("REAL AGENT LLM ACCEPTANCE FAILED: EZTEST_RUN_REAL_LLM=1 but .env is missing required key(s): " + ($missingLlmKeys -join ", ")) -ForegroundColor Red
        Write-Host "Refusing to skip the gated run; add the missing key(s) to .env or unset EZTEST_RUN_REAL_LLM." -ForegroundColor Red
        exit 1
    }

    $adminUsername = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_USERNAME"
    $adminPassword = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_PASSWORD"

    $script:secretValues = @($adminPassword, $llmKey)
    if ($envValues.ContainsKey("EZTEST_SECRET_KEYS")) {
        $rawSecretKeys = [string]$envValues["EZTEST_SECRET_KEYS"]
        if (-not [string]::IsNullOrWhiteSpace($rawSecretKeys)) {
            $script:secretValues += $rawSecretKeys
            try {
                $secretObject = $rawSecretKeys | ConvertFrom-Json
                foreach ($property in $secretObject.PSObject.Properties) {
                    if ($null -ne $property.Value -and -not [string]::IsNullOrWhiteSpace([string]$property.Value)) {
                        $script:secretValues += [string]$property.Value
                    }
                }
            } catch {
                # A malformed keyring is reported by the API at startup.
            }
        }
    }

    $databaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_DATABASE_URL"
    $script:secretValues += $databaseUrl
    $databaseUri = [System.Uri]$databaseUrl
    if (-not $databaseUri.IsAbsoluteUri) { throw "EZTEST_DATABASE_URL is not an absolute URL" }
    $userInfo = $databaseUri.UserInfo
    $separator = $userInfo.IndexOf(":")
    if ($separator -lt 0) { throw "EZTEST_DATABASE_URL must contain a user and password" }
    $dbUser = [System.Uri]::UnescapeDataString($userInfo.Substring(0, $separator))
    $dbPassword = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
    $dbHost = $databaseUri.Host
    $dbPort = $databaseUri.Port
    $script:secretValues += $dbPassword
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

    $goExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\go\bin\go.exe") -CommandName "go"
    if ([string]::IsNullOrWhiteSpace($goExe)) { throw "go executable not found (.tools or PATH)" }
    $psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql"
    if ([string]::IsNullOrWhiteSpace($psqlExe)) { throw "psql executable not found (.tools or PATH)" }

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

    $apiExe = Join-Path $tmpDir "acceptance-agent-llm-api.exe"
    $buildLog = Join-Path $tmpDir "acceptance-agent-llm-build.log"
    Push-Location $backendDir
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $goExe build -o $apiExe ./cmd/eztest *> $buildLog
        $buildExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        Pop-Location
    }
    if ($buildExit -ne 0) { throw "go build ./cmd/eztest failed (see .tmp/acceptance-agent-llm-build.log)" }

    Write-Step "Starting API on :$apiPort with the real LLM configuration from .env"
    $env:EZTEST_ENV = "dev"
    $env:EZTEST_DATABASE_URL = $e2eDatabaseUrl
    $env:EZTEST_HTTP_ADDR = ":$apiPort"
    $env:EZTEST_WEB_ORIGIN = "http://localhost:6868"
    $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
    $env:EZTEST_STORAGE_DIR = (Join-Path $tmpDir "e2e-objects")
    $env:EZTEST_ADMIN_USERNAME = $adminUsername
    $env:EZTEST_ADMIN_PASSWORD = $adminPassword
    # Read-only file tools are rooted at the repository so the model can read
    # README.md; http.get is pinned to the local API host (spec 4.2).
    $env:EZTEST_AGENT_FILE_ROOT = $repoRoot
    $env:EZTEST_AGENT_HTTP_ALLOWLIST = "localhost,127.0.0.1"
    foreach ($key in @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "llm_base_url", "llm_key", "llm_model_name")) {
        if ($envValues.ContainsKey($key)) { Set-Item -Path ("Env:" + $key) -Value $envValues[$key] }
    }
    Remove-Item Env:EZTEST_TEST_DATABASE_URL -ErrorAction SilentlyContinue

    $apiOut = Join-Path $tmpDir "acceptance-agent-llm-api.out.log"
    $apiErr = Join-Path $tmpDir "acceptance-agent-llm-api.err.log"
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

    $health = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/health" -TimeoutSec 20
    if ($health.data.llm_configured -ne $true) {
        throw "health reports llm_configured=false after seeding the default configuration"
    }

    Write-Step "Logging in as admin"
    $login = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/auth/login" -Body @{
        username = $adminUsername
        password = $adminPassword
    } -TimeoutSec 20
    $script:token = [string]$login.data.token
    if ([string]::IsNullOrWhiteSpace([string]$script:token)) { throw "admin login did not return a token" }
    $headers = @{ Authorization = "Bearer $($script:token)" }

    Write-Step "Raising the default LLM timeout to ${llmTimeoutMs}ms for the diagnostic run"
    try {
        $llmConfig = Get-DefaultLLMConfig -Headers $headers
        $llmConfigId = ConvertTo-Int64OrZero -Value $llmConfig.id
        if ($llmConfigId -le 0) { throw "the default LLM configuration has no id" }
    } catch {
        $summary = Get-ApiErrorSummary -ErrorRecord $_
        Write-Host "LLM configuration lookup failed: $summary" -ForegroundColor Red
        throw "could not load the default LLM configuration"
    }
    try {
        # PATCH is partial: only timeout_ms changes, so the server preserves the
        # seeded base_url/model/api_key and every other stored field.
        Set-DefaultLLMTimeout -Headers $headers -ConfigId $llmConfigId -TimeoutMs $llmTimeoutMs | Out-Null
    } catch {
        $summary = Get-ApiErrorSummary -ErrorRecord $_
        Write-Host "LLM timeout update failed: $summary" -ForegroundColor Red
        throw "could not set the default LLM timeout_ms=$llmTimeoutMs"
    }
    Write-Host ("llm_timeout_ms={0}" -f $llmTimeoutMs)

    Write-Step "Loading the runtime-diagnostic agent profile"
    $profilesResponse = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/agents/profiles" -Headers $headers -TimeoutSec 20
    $profile = $null
    $profiles = @()
    if ($null -ne $profilesResponse.data) { $profiles = @($profilesResponse.data) }
    foreach ($candidate in $profiles) {
        if ($null -ne $candidate -and [string]$candidate.key -eq "runtime-diagnostic") {
            $profile = $candidate
            break
        }
    }
    if ($null -eq $profile) { throw "the runtime-diagnostic agent profile was not seeded" }
    if (-not [bool]$profile.enabled) { throw "the runtime-diagnostic agent profile is disabled" }
    $profileId = ConvertTo-Int64OrZero -Value $profile.id

    Write-Step "Creating a session and starting the diagnostic run"
    $sessionResponse = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/agents/sessions" -Headers $headers -Body @{
        profile_id = $profileId
        title      = "gated real-llm agent acceptance"
    } -TimeoutSec 20
    $script:sessionId = ConvertTo-Int64OrZero -Value $sessionResponse.data.id
    if ($script:sessionId -le 0) { throw "session creation did not return an id" }

    $messageResponse = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/agents/sessions/$($script:sessionId)/messages" -Headers $headers -Body @{
        message = $diagnosticPrompt
    } -TimeoutSec 30
    $script:runId = ConvertTo-Int64OrZero -Value $messageResponse.data.run_id
    if ($script:runId -le 0) { throw "sending the diagnostic message did not return a run id" }

    Write-Step "Polling session $($script:sessionId) until run $($script:runId) finishes (max ${pollTimeoutSeconds}s)"
    $deadline = (Get-Date).AddSeconds($pollTimeoutSeconds)
    $lastSession = $null
    $lastPollError = ""
    $finished = $false
    while ((Get-Date) -lt $deadline) {
        try {
            $lastSession = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/agents/sessions/$($script:sessionId)" -Headers $headers -TimeoutSec 20
        } catch {
            $lastPollError = Get-ApiErrorSummary -ErrorRecord $_
            Start-Sleep -Seconds $pollIntervalSeconds
            continue
        }
        $running = $false
        if ($null -ne $lastSession.data.running) { $running = [bool]$lastSession.data.running }
        $seenRunId = [int64]0
        if ($null -ne $lastSession.data.last_run) {
            $seenRunId = ConvertTo-Int64OrZero -Value $lastSession.data.last_run.id
        }
        if (-not $running -and $seenRunId -eq $script:runId) {
            $finished = $true
            break
        }
        Start-Sleep -Seconds $pollIntervalSeconds
    }
    if (-not $finished) {
        if (-not [string]::IsNullOrWhiteSpace($lastPollError)) {
            throw "timed out after ${pollTimeoutSeconds}s waiting for run $($script:runId) (last poll error: $lastPollError)"
        }
        throw "timed out after ${pollTimeoutSeconds}s waiting for run $($script:runId) to finish"
    }

    $lastRun = $lastSession.data.last_run
    $runStatus = [string]$lastRun.status
    $terminalReason = [string]$lastRun.terminal_reason
    $errorCode = ""
    if ($null -ne $lastRun.error_code) { $errorCode = [string]$lastRun.error_code }
    if ($runStatus -ne "done" -or $terminalReason -ne "completed") {
        throw "run status=$runStatus terminal_reason=$terminalReason error_code=$errorCode"
    }
    Write-Host "Run finished: status=$runStatus terminal_reason=$terminalReason"

    Write-Step "Reading the agent channel event snapshot (retrying for run_finished)"
    $eventItems = @(Wait-AgentEventKind -Headers $headers -SessionId $script:sessionId -Kind "run_finished" -Attempts $runFinishedWaitAttempts -IntervalSeconds $runFinishedWaitIntervalSeconds)
    $script:eventItems = $eventItems

    $kinds = @()
    $toolUseCount = 0
    $okToolResults = 0
    $usageReasonTokens = [int64]0
    foreach ($eventItem in $eventItems) {
        if ($null -eq $eventItem -or $null -eq $eventItem.detail) { continue }
        $kind = [string]$eventItem.detail.kind
        if ([string]::IsNullOrWhiteSpace($kind)) { continue }
        $kinds += $kind
        switch ($kind) {
            "tool_use" {
                $toolUseCount++
            }
            "tool_result" {
                if ([string]$eventItem.detail.status -eq "ok") { $okToolResults++ }
            }
            "usage" {
                $usageReasonTokens += (ConvertTo-Int64OrZero -Value $eventItem.detail.reasoning_tokens)
            }
        }
    }

    $missingKinds = @()
    foreach ($requiredKind in $requiredKinds) {
        if ($kinds -notcontains $requiredKind) { $missingKinds += $requiredKind }
    }
    if ($missingKinds.Count -gt 0) {
        throw ("agent event stream is missing required kinds: " + ($missingKinds -join ", "))
    }
    if ($okToolResults -lt 1) { throw "no tool_result event reports status=ok" }
    if ($usageReasonTokens -le 0) { throw "usage events report no reasoning tokens (reasoning_tokens=0)" }

    $turns = ConvertTo-Int64OrZero -Value $lastRun.turns
    $reasonTokens = ConvertTo-Int64OrZero -Value $lastRun.reasoning_tokens
    if ($reasonTokens -le 0) { $reasonTokens = $usageReasonTokens }

    $uniqueKinds = @($kinds | Select-Object -Unique)
    Write-Host ("Event kinds: " + ($uniqueKinds -join ","))
    Write-Host ""
    Write-Host ("PASS run={0} turns={1} tools={2} reason_tokens={3}" -f $script:runId, $turns, $toolUseCount, $reasonTokens) -ForegroundColor Green
} catch {
    $summary = Get-ApiErrorSummary -ErrorRecord $_
    Write-Host ""
    Write-Host "REAL AGENT LLM ACCEPTANCE FAILED: $summary" -ForegroundColor Red
    Show-AgentFailureDiagnostics
    $exitCode = 1
} finally {
    Stop-ProcessTree -Process $apiProcess
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
}

exit $exitCode
