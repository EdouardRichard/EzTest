#requires -Version 5.1
<#
.SYNOPSIS
    P1 real-LLM acceptance (DeepSeek official endpoint).

.DESCRIPTION
    Starts an isolated API (no fake LLM), reads the acceptance-only provider
    config from .env (`llm_api_key`, `llm_base_url`, `llm_model_name`), writes a
    default llm_config through the admin API, refreshes the model catalog, and
    runs the clarification flow against the real model. The API process still
    reads only the four EZTEST_* variables.

    Evidence: .tmp/p1-acceptance/llm.log
#>
[CmdletBinding()]
param(
    [int]$ApiPort = 14343,
    [int]$PollTimeoutSeconds = 180,
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$logDir = Join-Path $repoRoot ".tmp\p1-acceptance"
$logFile = Join-Path $logDir "llm.log"
$exePath = Join-Path $repoRoot ".tmp\p1\eztest.exe"
$testDatabase = "eztest_v02_llm_e2e"
$apiBase = "http://127.0.0.1:$ApiPort"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $exePath) | Out-Null
Set-Content -LiteralPath $logFile -Value "" -Encoding UTF8
$apiProcess = $null
$token = ""

function Log([string]$message, [string]$color = "Gray") {
    $line = "[{0}] {1}" -f (Get-Date -Format "HH:mm:ss"), $message
    Write-Host $line -ForegroundColor $color
    Add-Content -LiteralPath $logFile -Value $line -Encoding UTF8
}

function Read-Env {
    $values = @{}
    Get-Content (Join-Path $repoRoot ".env") | ForEach-Object {
        $line = $_.Trim()
        if ($line -eq "" -or $line.StartsWith("#")) { return }
        $eq = $line.IndexOf("=")
        if ($eq -lt 1) { return }
        $values[$line.Substring(0, $eq).Trim()] = $line.Substring($eq + 1).Trim()
    }
    return $values
}

function Api {
    param([string]$Method, [string]$Path, $Body)
    $headers = @{}
    if (-not [string]::IsNullOrEmpty($token)) { $headers.Authorization = "Bearer $token" }
    $params = @{ Method = $Method; Uri = $apiBase + $Path; Headers = $headers; TimeoutSec = 120 }
    if ($null -ne $Body) {
        $params.ContentType = "application/json; charset=utf-8"
        $json = ConvertTo-Json -InputObject $Body -Depth 30 -Compress
        $params.Body = [Text.Encoding]::UTF8.GetBytes($json)
    }
    return Invoke-RestMethod @params
}

function Wait-Api([int]$seconds) {
    for ($i = 0; $i -lt ($seconds * 2); $i++) {
        try {
            $health = Invoke-RestMethod -Uri "$apiBase/api/health" -TimeoutSec 3
            if ($health.data.db -eq "ok") { return $true }
        } catch { }
        Start-Sleep -Milliseconds 500
    }
    return $false
}

try {
    $envValues = Read-Env
    $llmKey = [string]$envValues["llm_api_key"]
    $llmBase = [string]$envValues["llm_base_url"]
    $llmModel = [string]$envValues["llm_model_name"]
    if ([string]::IsNullOrWhiteSpace($llmKey)) { throw "llm_api_key missing from .env" }
    if ([string]::IsNullOrWhiteSpace($llmBase)) { $llmBase = "https://api.deepseek.com" }
    if ([string]::IsNullOrWhiteSpace($llmModel)) { throw "llm_model_name missing from .env" }
    Log "real LLM target: $llmBase model=$llmModel (key redacted)"

    $devDb = [string]$envValues["EZTEST_DATABASE_URL"]
    if ([string]::IsNullOrWhiteSpace($devDb)) { throw "EZTEST_DATABASE_URL missing from .env" }
    $llmDb = $devDb -replace '/eztest_v02(\?|$)', "/$testDatabase`$1"
    if ($llmDb -eq $devDb) { throw "could not derive the real-LLM e2e database URL" }

    $psql = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    $dbUri = [uri]$llmDb
    $userInfo = $dbUri.UserInfo.Split(":", 2)
    $previousPassword = $env:PGPASSWORD
    $env:PGPASSWORD = $userInfo[1]
    try {
        & $psql -h $dbUri.Host -p $dbUri.Port -U $userInfo[0] -d postgres -v ON_ERROR_STOP=1 `
            -c "DROP DATABASE IF EXISTS $testDatabase WITH (FORCE);" `
            -c "CREATE DATABASE $testDatabase;" *> (Join-Path $logDir "llm-db.log")
        if ($LASTEXITCODE -ne 0) { throw "database reset failed (see llm-db.log)" }
    } finally { $env:PGPASSWORD = $previousPassword }
    Log "isolated database $testDatabase ready"

    if (-not $SkipBuild) {
        Push-Location (Join-Path $repoRoot "backend")
        try {
            $go = Join-Path $repoRoot ".tools\go\bin\go.exe"
            if (-not (Test-Path $go)) { $go = "go" }
            & $go build -o $exePath ./cmd/eztest *> (Join-Path $logDir "llm-build.log")
            if ($LASTEXITCODE -ne 0) { throw "go build failed (see llm-build.log)" }
        } finally { Pop-Location }
    }

    $previousDb = $env:EZTEST_DATABASE_URL
    $previousPort = $env:EZTEST_API_PORT
    try {
        $env:EZTEST_DATABASE_URL = $llmDb
        $env:EZTEST_API_PORT = "$ApiPort"
        $env:EZTEST_WEB_PORT = "16868"
        $env:EZTEST_EXECUTOR_PORT = "13336"
        $apiProcess = Start-Process -FilePath $exePath -WorkingDirectory (Join-Path $repoRoot "backend") `
            -RedirectStandardOutput (Join-Path $logDir "llm-api.out.log") `
            -RedirectStandardError (Join-Path $logDir "llm-api.err.log") -PassThru
    } finally {
        $env:EZTEST_DATABASE_URL = $previousDb
        $env:EZTEST_API_PORT = $previousPort
    }
    if (-not (Wait-Api 60)) { throw "API did not become ready" }
    Log "API ready at $apiBase"

    $login = Api "POST" "/api/auth/login" @{ username = "admin"; password = "admin123" }
    $token = [string]$login.data.token

    $config = Api "POST" "/api/admin/llm/configs" @{
        name           = "real-llm-acceptance"
        provider       = "openai"
        protocol       = "openai"
        base_url       = $llmBase
        api_key        = $llmKey
        timeout_seconds = 120
        max_retries    = 0
        is_default     = $true
    }
    $configID = [string]$config.data.id
    Log "llm_config created id=$configID"

    $refreshed = Api "POST" "/api/admin/llm/configs/$configID/refresh-models" $null
    $models = @($refreshed.data)
    $modelNames = @($models | ForEach-Object { [string]$_.model })
    $modelInfo = $models | Where-Object { [string]$_.model -eq $llmModel } | Select-Object -First 1
    if ($null -eq $modelInfo) {
        Log "catalog did not expose $llmModel; adding a manual model with conservative metadata"
        $added = Api "POST" "/api/admin/llm/configs/$configID/models" @{
            model = $llmModel; context_window = 64000; max_output = 8192
            supports_tools = $true; supports_vision = $false
        }
        $modelInfo = $added.data
        $modelNames = @($modelNames) + @($llmModel)
    }
    $contextWindow = [int]$modelInfo.context_window
    $maxOutput = [int]$modelInfo.max_output
    Log ("catalog models: {0}; selected={1} context_window={2} max_output={3}" -f ($modelNames -join ", "), $llmModel, $contextWindow, $maxOutput)

    $suffix = Get-Date -Format "yyyyMMddHHmmss"
    $sut = Api "POST" "/api/resources/suts" @{ name = "Real LLM SUT $suffix"; intro = "real LLM acceptance" }
    $envt = Api "POST" "/api/resources/suts/$($sut.data.id)/environments" @{
        name = "real-llm"; base_url = "https://real-llm.example.test"; is_default = $true
    }
    $page = Api "POST" "/api/resources/environments/$($envt.data.id)/pages" @{
        name = "登录页"; url = "https://real-llm.example.test/login"; route_template = "/login"
        page_type = "page"; auth_role = "guest"; module = "auth"
    }
    $version = Api "POST" "/api/versions" @{
        title = "Real LLM acceptance $suffix"
        bindings = @(@{
            sut_id = $sut.data.id; environment_id = $envt.data.id
            page_ids = @($page.data.id); api_ids = @(); database_ids = @(); accounts = @()
        })
    }
    $versionID = [string]$version.data.id
    Log "version created id=$versionID status=$($version.data.status)"

    $conversation = Api "POST" "/api/versions/$versionID/conversations" @{
        kind = "clarification"; title = "Real LLM clarification"
    }
    $conversationID = [string]$conversation.data.id
    Api "POST" "/api/versions/$versionID/conversations/$conversationID/messages" @{
        parts = @(@{ type = "text"; text = "请澄清登录需求：支持密码登录和短信登录；记住我为可选；登录失败提示不能泄露账号是否存在；密码至少 8 位。" })
    } | Out-Null
    Log "user message posted; waiting for real model (timeout ${PollTimeoutSeconds}s)"

    $assistant = $null
    $requirement = $null
    $deadline = (Get-Date).AddSeconds($PollTimeoutSeconds)
    while ((Get-Date) -lt $deadline -and ($null -eq $assistant -or $null -eq $requirement)) {
        Start-Sleep -Seconds 2
        try {
            $messages = Api "GET" "/api/versions/$versionID/conversations/$conversationID/messages?limit=100" $null
            foreach ($item in @($messages.data)) {
                if ($item.role -eq "assistant") { $assistant = $item }
            }
            $requirements = Api "GET" "/api/versions/$versionID/requirements?limit=100" $null
            $requirements = @($requirements.data)
            if ($requirements.Count -gt 0) { $requirement = $requirements[0] }
        } catch {
            Log "poll warning: $($_.Exception.Message)" "Yellow"
        }
    }
    if ($null -eq $assistant) { throw "real model produced no assistant message within ${PollTimeoutSeconds}s" }
    if ($null -eq $requirement) { throw "real model produced no requirement draft within ${PollTimeoutSeconds}s (tool call missing)" }

    $assistantText = (@($assistant.parts) | ForEach-Object { [string]$_.text }) -join ""
    Log ("assistant chars={0} preview={1}" -f $assistantText.Length, $assistantText.Substring(0, [Math]::Min(160, $assistantText.Length)))

    $detail = Api "GET" "/api/versions/$versionID/requirements/$($requirement.id)" $null
    $versionIDForConfirm = [string]$requirement.id
    $confirm = Api "POST" "/api/versions/$versionID/requirements/$versionIDForConfirm/confirmations" $null
    if ($confirm.data.requirement.status -ne "clarified") {
        throw ("real model draft confirmation failed: status={0}" -f $confirm.data.requirement.status)
    }
    Log ("confirmed requirement id={0} revision={1} title={2}" -f $confirm.data.requirement.id, $confirm.data.requirement.current_version.revision, $confirm.data.requirement.current_version.title)
    Log "RESULT: PASS (real LLM clarification + draft + confirm)"
    exit 0
} catch {
    Log "RESULT: FAIL: $($_.Exception.Message)" "Red"
    if ($_.ErrorDetails) { Log ("detail: " + $_.ErrorDetails.Message) "DarkYellow" }
    exit 1
} finally {
    if ($null -ne $apiProcess) { try { Stop-Process -Id $apiProcess.Id -Force -ErrorAction SilentlyContinue } catch { } }
}
