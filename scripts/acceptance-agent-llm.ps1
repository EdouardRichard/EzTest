#requires -Version 5.1
<#
.SYNOPSIS
    Optional real-LLM acceptance for the EzTest Agent runtime (gated by
    EZTEST_RUN_REAL_LLM=1).

.DESCRIPTION
    Boots the API against a freshly reset eztest_e2e database, configures the
    real LLM from .env through the admin HTTP API (the settings/LLM config
    surface the product actually reads; no migration seeds a default config),
    logs in as the admin, and drives ONE bounded agent turn through the real
    0.2.4 conversation pipeline:
      * GET /api/admin/llm/configs, creating and making default the config that
        matches llm_base_url when none exists, adding llm_model_name as a
        tool-capable, vision-capable model and failing it as the default model;
      * POST /api/admin/llm/configs/{id}/test as the reachability signal (the
        /api/health payload has no llm_configured field);
      * POST /api/resources/suts and .../environments (idempotent) to bind a
        version to an environment;
      * POST /api/versions and POST /api/versions/{id}/conversations (kind
        clarification); creating the conversation makes the product open it
        with one bounded Host reply (no user message is posted, so the run is a
        single paid model round);
      * poll GET .../messages until the opening reply is terminal and carries
        text, capturing one mid-reply streaming snapshot (the reload-mid-reply
        observation);
      * read pg_stat_user_tables before/after to report the per-delta DB write
        counts the streaming pipeline performed (the bounded-flush evidence).
    It prints PASS only after the assistant reply is non-empty and reachable.
    Secrets are read from .env and never printed: only statuses, counts and
    error codes are reported.

.PARAMETER DryRun
    Validate the script's paths and the .env key presence (database, admin and
    LLM variables) without resetting the database, building or starting the API,
    or calling the LLM. Exits 0 when the preflight passes.

.PARAMETER PollTimeoutSeconds
    Hard cap on how long to wait for the assistant reply (default 300).

.NOTES
    This script makes paid upstream calls. It is deliberately not part of
    scripts/acceptance.ps1, which reports it as gated. When the gate is open
    (EZTEST_RUN_REAL_LLM=1), missing .env LLM keys are a failure, never a skip.
    PostgreSQL must already be running; the script resets the eztest_e2e database
    and leaves PostgreSQL running. Compatible with Windows PowerShell 5.1 and
    PowerShell 7+. The file is ASCII-only so Windows PowerShell 5.1 parses it
    without a BOM.
#>
[CmdletBinding()]
param(
    [switch]$DryRun,
    [int]$PollTimeoutSeconds = 300,
    [double]$PollIntervalSec = 2
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$tmpDir = Join-Path $repoRoot ".tmp"
$envPath = Join-Path $repoRoot ".env"
$e2eDbName = "eztest_e2e"
$apiPort = 13333
$apiBaseUrl = "http://localhost:$apiPort"
$pollIntervalSeconds = $PollIntervalSec
$llmTimeoutMs = 180000
$llmConfigsPath = "/api/admin/llm/configs"
# The tables the per-delta streaming pipeline writes on every delta: the Host
# head message, its per-seq part rows, and the agent transcript row.
$streamWriteTables = @("conversation_message", "conversation_message_part", "agent_message")

$script:secretValues = @()
$script:token = $null

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
        # Windows PowerShell 5.1 would otherwise encode non-ASCII with the ANSI
        # code page, so always send explicit UTF-8 bytes.
        $arguments["Body"] = [System.Text.Encoding]::UTF8.GetBytes($json)
    }
    return Invoke-RestMethod @arguments
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

function Get-TableWriteCounts {
    param([string]$Database)
    $inList = ($streamWriteTables | ForEach-Object { "'$_'" }) -join ","
    $sql = "SELECT relname || ':' || n_tup_ins || '/' || n_tup_upd FROM pg_stat_user_tables WHERE relname IN ($inList) ORDER BY relname;"
    # psql needs the password here too: this runs outside the DB-reset block, and
    # without PGPASSWORD psql would block on an interactive password prompt.
    $previousPassword = $env:PGPASSWORD
    $env:PGPASSWORD = $dbPassword
    try {
        $output = Get-PsqlScalar -Database $Database -Sql $sql
    } finally {
        if ([string]::IsNullOrEmpty($previousPassword)) {
            Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
        } else {
            $env:PGPASSWORD = $previousPassword
        }
    }
    $map = @{}
    foreach ($line in ($output -split "`n")) {
        $trimmed = $line.Trim()
        if ($trimmed -eq "") { continue }
        if ($trimmed -match '^([^:]+):(\d+)/(\d+)$') {
            $map[$matches[1]] = [pscustomobject]@{ Ins = [int64]$matches[2]; Upd = [int64]$matches[3] }
        }
    }
    return $map
}

function Initialize-LlmConfiguration {
    param([hashtable]$Headers, [string]$BaseUrl, [string]$ApiKey, [string]$ModelName, [int]$TimeoutMs)
    $base = $BaseUrl.TrimEnd('/')
    $configs = @((Invoke-Api -Method Get -Uri "$apiBaseUrl$llmConfigsPath" -Headers $Headers -TimeoutSec 20).data)
    $config = $configs | Where-Object { $null -ne $_ -and ([string]$_.base_url).TrimEnd('/') -eq $base } | Select-Object -First 1
    if ($null -eq $config) {
        $createBody = @{
            name             = "acceptance-$ModelName"
            provider         = "acceptance"
            protocol         = "openai_chat_completions"
            base_url         = $base
            api_key          = $ApiKey
            is_default       = $true
            status           = "active"
            timeout_seconds  = [int]([Math]::Max(1, [Math]::Floor($TimeoutMs / 1000)))
            max_retries      = 1
            thinking_type    = "auto"
            reasoning_effort = "none"
        }
        $config = (Invoke-Api -Method Post -Uri "$apiBaseUrl$llmConfigsPath" -Headers $Headers -Body $createBody -TimeoutSec 20).data
    }
    $configId = ConvertTo-Int64OrZero -Value $config.id
    if ($configId -le 0) { throw "the LLM configuration has no id" }
    if (-not [bool]$config.is_default) {
        $config = (Invoke-Api -Method Patch -Uri "$apiBaseUrl$llmConfigsPath/$configId" -Headers $Headers -Body @{ is_default = $true } -TimeoutSec 20).data
    }
    if (-not [bool]$config.api_key_set) {
        $config = (Invoke-Api -Method Patch -Uri "$apiBaseUrl$llmConfigsPath/$configId" -Headers $Headers -Body @{ api_key = $ApiKey } -TimeoutSec 20).data
    }

    $models = @($config.models)
    $model = $models | Where-Object { $null -ne $_ -and [string]$_.model -eq $ModelName } | Select-Object -First 1
    if ($null -eq $model) {
        $modelBody = @{
            model                 = $ModelName
            context_window        = 131072
            max_output            = 8192
            supports_tools        = $true
            supports_vision       = $true
            supports_reasoning    = $false
            supports_prompt_cache = $true
        }
        $model = (Invoke-Api -Method Post -Uri "$apiBaseUrl$llmConfigsPath/$configId/models" -Headers $Headers -Body $modelBody -TimeoutSec 20).data
    }
    $modelId = ConvertTo-Int64OrZero -Value $model.id
    if ($modelId -le 0) { throw "the LLM model row has no id" }
    if (-not [bool]$model.supports_tools) {
        $model = (Invoke-Api -Method Patch -Uri "$apiBaseUrl$llmConfigsPath/$configId/models/$modelId" -Headers $Headers -Body @{ supports_tools = $true } -TimeoutSec 20).data
    }
    $defaultModelId = ConvertTo-Int64OrZero -Value $config.default_model_id
    if ($defaultModelId -ne $modelId) {
        $config = (Invoke-Api -Method Patch -Uri "$apiBaseUrl$llmConfigsPath/$configId" -Headers $Headers -Body @{ default_model_id = [string]$modelId } -TimeoutSec 20).data
    }

    # The reachability signal that replaced the never-present health.llm_configured.
    $test = (Invoke-Api -Method Post -Uri "$apiBaseUrl$llmConfigsPath/$configId/test" -Headers $Headers -TimeoutSec 120).data
    $ok = [bool]$test.ok
    if (-not $ok) {
        throw ("LLM connectivity test failed: " + [string]$test.error)
    }
    return [pscustomobject]@{
        ConfigId  = $configId
        ModelId   = $modelId
        Model     = $ModelName
        LatencyMS = (ConvertTo-Int64OrZero -Value $test.latency_ms)
    }
}

function Get-OrCreateEnvironment {
    param([hashtable]$Headers, [string]$BaseUrl)
    # 0.2.4-B resource model: environments are top-level and a version binds an
    # environment; SUTs are catalogued under an environment (the old
    # POST /api/resources/suts and .../suts/{id}/environments shapes are gone).
    $environmentName = "acceptance-local"
    $environments = @((Invoke-Api -Method Get -Uri "$apiBaseUrl/api/resources/environments?limit=200" -Headers $Headers -TimeoutSec 20).data)
    $environment = $environments | Where-Object { $null -ne $_ -and [string]$_.name -eq $environmentName } | Select-Object -First 1
    if ($null -eq $environment) {
        $environment = (Invoke-Api -Method Post -Uri "$apiBaseUrl/api/resources/environments" -Headers $Headers -Body @{ name = $environmentName; description = "EzTest acceptance environment"; status = "active" } -TimeoutSec 20).data
    }
    $environmentId = ConvertTo-Int64OrZero -Value $environment.id
    if ($environmentId -le 0) { throw "the acceptance environment has no id" }

    $sutName = "EzTest Acceptance SUT"
    $suts = @((Invoke-Api -Method Get -Uri "$apiBaseUrl/api/resources/environments/$environmentId/suts" -Headers $Headers -TimeoutSec 20).data)
    $sut = $suts | Where-Object { $null -ne $_ -and [string]$_.name -eq $sutName } | Select-Object -First 1
    if ($null -eq $sut) {
        $sut = (Invoke-Api -Method Post -Uri "$apiBaseUrl/api/resources/environments/$environmentId/suts" -Headers $Headers -Body @{ name = $sutName; intro = "EzTest acceptance SUT (local API)"; base_url = $BaseUrl; family = "web" } -TimeoutSec 20).data
    }
    $sutId = ConvertTo-Int64OrZero -Value $sut.id
    if ($sutId -le 0) { throw "the acceptance SUT has no id" }
    return [pscustomobject]@{ SutId = $sutId; EnvironmentId = $environmentId }
}

function Get-AssistantReplyState {
    param($Message)
    $length = 0
    $errorText = ""
    foreach ($part in @($Message.parts)) {
        if ($null -eq $part) { continue }
        if ([string]$part.type -eq "text") {
            $length += ([string]$part.text).Length
        } elseif ([string]$part.type -eq "job_card" -and [string]$part.status -eq "failed") {
            $errorText = [string]$part.reason
        } elseif ([string]$part.type -eq "error") {
            $errorText = [string]$part.error
        }
    }
    return [pscustomobject]@{
        Length     = $length
        StreamState = [string]$Message.stream_state
        ErrorText  = $errorText
        ActualModel = [string]$Message.actual_model
    }
}

function Invoke-DryRun {
    Write-Step "DryRun: validating paths and environment (no database reset, no API, no LLM call)"
    $problems = @()

    if (-not (Test-Path -LiteralPath $repoRoot -PathType Container)) { $problems += "repository root not found" }
    if (-not (Test-Path -LiteralPath $backendDir -PathType Container)) { $problems += "backend directory not found" }

    $envValues = $null
    $requiredKeys = @("EZTEST_DATABASE_URL", "EZTEST_ADMIN_USERNAME", "EZTEST_ADMIN_PASSWORD", "llm_base_url", "llm_model_name")
    $presentKeys = @()
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
        $problems += ".env not found at $envPath"
    } else {
        $envValues = Read-DotEnv -Path $envPath
        if (-not ($envValues.ContainsKey("llm_api_key") -and -not [string]::IsNullOrWhiteSpace([string]$envValues["llm_api_key"]))) {
            if (-not ($envValues.ContainsKey("llm_key") -and -not [string]::IsNullOrWhiteSpace([string]$envValues["llm_key"]))) {
                $problems += "missing .env key: llm_api_key (or legacy llm_key)"
            }
        }
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
    Write-Host ("DryRun: .env found; LLM key (llm_api_key or llm_key) present; required keys present ({0}/{1}): {2}" -f $presentKeys.Count, $requiredKeys.Count, ($presentKeys -join ", "))
    Write-Host "DryRun: go executable found: $goExe"
    Write-Host "DryRun: psql executable found: $psqlExe"
    if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
        Write-Host "DryRun: gate is closed (EZTEST_RUN_REAL_LLM != 1); the real run would print SKIPPED and exit 0."
    } else {
        Write-Host "DryRun: gate is open; the real run would reset $e2eDbName, build/start the API on :$apiPort, and spend one paid LLM turn."
    }
    Write-Host "DryRun: the real run creates the LLM config through the admin API and confirms it with POST ${llmConfigsPath}/{id}/test."
    Write-Host "DryRun: the real run drives one clarification conversation turn and reports per-delta DB write counts."
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
    if ($envValues.ContainsKey("llm_api_key")) { $llmKey = [string]$envValues["llm_api_key"] }
    if ([string]::IsNullOrWhiteSpace($llmKey) -and $envValues.ContainsKey("llm_key")) { $llmKey = [string]$envValues["llm_key"] }
    $llmBaseURL = ""
    if ($envValues.ContainsKey("llm_base_url")) { $llmBaseURL = [string]$envValues["llm_base_url"] }
    $llmModel = ""
    if ($envValues.ContainsKey("llm_model_name")) { $llmModel = [string]$envValues["llm_model_name"] }

    $missingLlmKeys = @()
    if ([string]::IsNullOrWhiteSpace($llmKey)) { $missingLlmKeys += "llm_api_key (or llm_key)" }
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

    Write-Step "Starting API on :$apiPort"
    $env:EZTEST_ENV = "dev"
    $env:EZTEST_DATABASE_URL = $e2eDatabaseUrl
    $env:EZTEST_HTTP_ADDR = ":$apiPort"
    $env:EZTEST_WEB_ORIGIN = "http://localhost:6868"
    $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
    $env:EZTEST_STORAGE_DIR = (Join-Path $tmpDir "e2e-objects")
    $env:EZTEST_ADMIN_USERNAME = $adminUsername
    $env:EZTEST_ADMIN_PASSWORD = $adminPassword
    # The first-start admin seed reads EZTEST_INITIAL_ADMIN_PASSWORD; the e2e
    # database is fresh, so this is what actually creates the admin row.
    $env:EZTEST_INITIAL_ADMIN_PASSWORD = $adminPassword
    $env:EZTEST_AGENT_FILE_ROOT = $repoRoot
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

    Write-Step "Logging in as admin"
    $login = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/auth/login" -Body @{
        username = $adminUsername
        password = $adminPassword
    } -TimeoutSec 20
    $script:token = [string]$login.data.token
    if ([string]::IsNullOrWhiteSpace([string]$script:token)) { throw "admin login did not return a token" }
    $headers = @{ Authorization = "Bearer $($script:token)" }

    Write-Step "Configuring the real LLM through the admin API and testing it"
    $llm = Initialize-LlmConfiguration -Headers $headers -BaseUrl $llmBaseURL -ApiKey $llmKey -ModelName $llmModel -TimeoutMs $llmTimeoutMs
    Write-Host ("llm_config_id={0} model={1} connectivity_test=ok latency_ms={2}" -f $llm.ConfigId, $llm.Model, $llm.LatencyMS) -ForegroundColor Green

    Write-Step "Binding an environment and creating the acceptance version"
    $binding = Get-OrCreateEnvironment -Headers $headers -BaseUrl $apiBaseUrl
    $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $version = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions" -Headers $headers -Body @{
        title          = "agent-llm-acceptance-$stamp"
        description    = "Gated real-LLM acceptance version"
        environment_id = [string]$binding.EnvironmentId
    } -TimeoutSec 20
    $versionId = ConvertTo-Int64OrZero -Value $version.data.id
    if ($versionId -le 0) { throw "version creation did not return an id" }
    Write-Host ("version_id={0} environment_id={1}" -f $versionId, $binding.EnvironmentId)

    # Sample the streaming tables before the opening round starts (the e2e DB is
    # fresh; creating the conversation enqueues the opening Host reply, see
    # conversation.go EnsureOpening / reconciler rule 2).
    $writeCountsBefore = Get-TableWriteCounts -Database $e2eDbName

    Write-Step "Creating the clarification conversation and waiting for the opening Host reply"
    $conversation = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$versionId/conversations" -Headers $headers -Body @{
        kind  = "clarification"
        title = "gated real-llm acceptance"
    } -TimeoutSec 20
    $conversationId = ConvertTo-Int64OrZero -Value $conversation.data.id
    if ($conversationId -le 0) { throw "conversation creation did not return an id" }
    Write-Host ("conversation_id={0}" -f $conversationId)
    $startedAt = Get-Date

    # The product opens a fresh clarification conversation with one bounded Host
    # reply (tool-free or tool-using; either streams). Poll until an assistant
    # message is terminal AND carries text. The opening round also emits an
    # artifact/job_card and a question card with no text, so a text-free
    # terminal message must not end the wait.
    $deadline = (Get-Date).AddSeconds($PollTimeoutSeconds)
    $assistantMessage = $null
    $lastState = "none"
    $midReplyStreamingSeen = $false
    $midReplyObserved = $false
    $midReplyChars = 0
    $lastErrorMessage = ""
    while ((Get-Date) -lt $deadline) {
        $messagesResponse = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$versionId/conversations/$conversationId/messages?limit=200" -Headers $headers -TimeoutSec 20
        foreach ($message in @($messagesResponse.data)) {
            if ($null -eq $message -or [string]$message.role -ne "assistant") { continue }
            $state = Get-AssistantReplyState -Message $message
            $lastState = $state.StreamState
            if ($state.StreamState -eq "streaming") {
                $midReplyStreamingSeen = $true
                if ($state.Length -gt 0) {
                    $midReplyObserved = $true
                    if ($state.Length -gt $midReplyChars) { $midReplyChars = $state.Length }
                }
            }
            if (($state.StreamState -eq "complete" -or $state.StreamState -eq "interrupted") -and $state.Length -gt 0) {
                $assistantMessage = $message
                if (-not [string]::IsNullOrWhiteSpace($state.ErrorText)) { $lastErrorMessage = $state.ErrorText }
            }
        }
        if ($null -ne $assistantMessage) { break }
        Start-Sleep -Seconds $pollIntervalSeconds
    }

    $elapsedSeconds = [Math]::Round(((Get-Date) - $startedAt).TotalSeconds, 1)
    if ($null -eq $assistantMessage) {
        throw "timed out after ${PollTimeoutSeconds}s waiting for a terminal assistant reply (last stream_state=$lastState)"
    }
    $assistantState = Get-AssistantReplyState -Message $assistantMessage
    if ($assistantState.Length -le 0) {
        $detail = if ([string]::IsNullOrWhiteSpace($lastErrorMessage)) { "no error part reported" } else { "error: $lastErrorMessage" }
        throw ("the assistant reply is empty (stream_state=" + $assistantState.StreamState + "; " + $detail + ")")
    }
    Write-Host ("run finished: stream_state={0} reply_chars={1} actual_model={2}" -f $assistantState.StreamState, $assistantState.Length, $assistantState.ActualModel)

    $writeCountsAfter = Get-TableWriteCounts -Database $e2eDbName
    $totalUpdates = [int64]0
    $totalInserts = [int64]0
    $tableReport = @()
    foreach ($table in $streamWriteTables) {
        $before = $writeCountsBefore[$table]
        $after = $writeCountsAfter[$table]
        if ($null -eq $after) { $after = [pscustomobject]@{ Ins = 0; Upd = 0 } }
        if ($null -eq $before) { $before = [pscustomobject]@{ Ins = 0; Upd = 0 } }
        $ins = $after.Ins - $before.Ins
        $upd = $after.Upd - $before.Upd
        $totalUpdates += $upd
        $totalInserts += $ins
        $tableReport += ("{0}: ins={1} upd={2}" -f $table, $ins, $upd)
    }

    # Per-delta DB write observation. Current state (v0.2.4-H, T28c): the AGENT
    # transcript path coalesces bounded flushes (transcript_stream.go), so the
    # agent message row is updated ~9 times per turn instead of once per delta
    # (49/70 -> 9). The HOST path (host_stream.go) still writes the FULL
    # accumulated snapshot to the part rows / head parts array per delta because
    # its write and publish are 1:1 coupled (~714 updates/turn); merging it needs
    # a version port split and is deferred to 0.3 (H-A44, SC-H1 written exception).
    # The counters below therefore observe BOTH paths; est_bytes_rewritten keeps
    # the legacy uniform-growth estimate (3 x N x L / 2) as a rough upper bound,
    # not a measured value. Do not read the update counts as "one write per delta".
    $deltasApprox = [int64]0
    $partAfter = $writeCountsAfter["conversation_message_part"]
    if ($null -ne $partAfter) { $deltasApprox = $partAfter.Upd }
    $throughput = 0.0
    if ($elapsedSeconds -gt 0) { $throughput = [Math]::Round([double]$totalUpdates / $elapsedSeconds, 2) }
    $amplification = [int64]0
    if ($deltasApprox -gt 0) { $amplification = [int64](3 * ($deltasApprox * $assistantState.Length) / 2) }

    Write-Host ""
    Write-Host ("PASS version={0} conversation={1} reply_chars={2} elapsed_s={3} deltas~={4}" -f $versionId, $conversationId, $assistantState.Length, $elapsedSeconds, $deltasApprox) -ForegroundColor Green
    Write-Host ("bounded-flush: total_ins={0} total_upd={1} upd_per_s={2} est_bytes_rewritten~={3} mid_reply_streaming_seen={4} mid_reply_text_observed={5} mid_reply_chars={6}" -f $totalInserts, $totalUpdates, $throughput, $amplification, $midReplyStreamingSeen, $midReplyObserved, $midReplyChars) -ForegroundColor Green
    foreach ($line in $tableReport) { Write-Host ("  writes " + $line) }
} catch {
    $summary = Get-ApiErrorSummary -ErrorRecord $_
    Write-Host ""
    Write-Host "REAL AGENT LLM ACCEPTANCE FAILED: $summary" -ForegroundColor Red
    $exitCode = 1
} finally {
    Stop-ProcessTree -Process $apiProcess
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
}

exit $exitCode
