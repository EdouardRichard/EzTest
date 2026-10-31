#requires -Version 5.1
<#
.SYNOPSIS
    P2 real-LLM acceptance (DeepSeek official endpoint).

.DESCRIPTION
    Starts an isolated API (no fake LLM), reads the acceptance-only provider
    config from .env (`llm_api_key`, `llm_base_url`, `llm_model_name`), writes a
    default llm_config through the admin API, refreshes the model catalog, and
    runs the design flow against the real model. The API process still reads
    only the four EZTEST_* variables.

    Evidence: .tmp/p2-acceptance/llm.log (full run) or llm-probe.log (-ProbeOnly).
#>
[CmdletBinding()]
param(
    [switch]$ProbeOnly,
    [int]$ApiPort = 14343,
    [int]$PollTimeoutSeconds = 300,
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$logDir = Join-Path $repoRoot ".tmp\p2-acceptance"
$logFile = Join-Path $logDir "llm.log"
if ($ProbeOnly) { $logFile = Join-Path $logDir "llm-probe.log" }
$exePath = Join-Path $repoRoot ".tmp\p2\eztest.exe"
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
        timeout_seconds = 300
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
    $sut = Api "POST" "/api/resources/suts" @{ name = "Real LLM design SUT $suffix"; intro = "real LLM P2 design acceptance" }
    $envt = Api "POST" "/api/resources/suts/$($sut.data.id)/environments" @{
        name = "real-llm"; base_url = "https://real-llm.example.test"; is_default = $true
    }
    $page = Api "POST" "/api/resources/environments/$($envt.data.id)/pages" @{
        name = "登录页"; url = "https://real-llm.example.test/login"; route_template = "/login"
        page_type = "page"; auth_role = "guest"; module = "auth"
    }
    $apiEndpoint = Api "POST" "/api/resources/environments/$($envt.data.id)/apis" @{
        name = "登录 API"; method = "POST"; path_template = "/api/login"; auth_role = "guest"
    }
    $version = Api "POST" "/api/versions" @{
        title = "Real LLM design acceptance $suffix"
        bindings = @(@{
            sut_id = $sut.data.id; environment_id = $envt.data.id
            page_ids = @($page.data.id); api_ids = @($apiEndpoint.data.id); database_ids = @(); accounts = @()
        })
    }
    $versionID = [string]$version.data.id
    $versionSUTID = [string]$version.data.bindings[0].id
    Log "version created id=$versionID status=$($version.data.status)"

    $requirementIDs = @()
    $titles = @("账号密码登录", "登录失败提示")
    for ($i = 0; $i -lt $titles.Count; $i++) {
        $requirement = Api "POST" "/api/versions/$versionID/requirements" @{
            title = $titles[$i]
            description = "$($titles[$i]) 的验收描述：用户可用账号密码登录；失败提示不泄露账号是否存在。"
            acceptance_criteria = "流程可执行；成功/失败提示正确；异常场景有断言。"
            priority = if ($i -eq 0) { "P1" } else { "P2" }
            constraints = @{}
            targets = @{ version_suts = @($versionSUTID); pages = @($page.data.id); apis = @($apiEndpoint.data.id) }
        }
        $rid = [string]$requirement.data.id
        Api "POST" "/api/versions/$versionID/requirements/$rid/confirmations" $null | Out-Null
        $requirementIDs += $rid
    }
    Log "clarified requirements: $($requirementIDs -join ', '); waiting for real design team"

    $designConversation = Api "POST" "/api/versions/$versionID/conversations" @{ kind = "design"; title = "Real LLM design round" }
    $conversationID = [string]$designConversation.data.id

    function Get-AllCases {
        $graph = Api "GET" "/api/versions/$versionID/design/graph" $null
        $cases = @()
        foreach ($req in @($graph.data.requirements)) {
            foreach ($scenario in @($req.scenarios)) {
                foreach ($case in @($scenario.cases)) { $cases += $case }
            }
        }
        return $cases
    }

    $cases = @()
    $deadline = (Get-Date).AddSeconds($PollTimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Seconds 2
        try { $cases = Get-AllCases } catch { Log "poll warning: $($_.Exception.Message)" "Yellow" }
        if ($cases.Count -ge 1) { break }
    }
    if ($cases.Count -lt 1) { throw "real model produced no design case within ${PollTimeoutSeconds}s" }
    $first = $cases[0]
    if ($first.status -ne "draft") { throw ("real model first case status is {0}, want draft" -f $first.status) }
    if ([int]$first.step_count -lt 1 -or [int]$first.assertion_count -lt 1) {
        throw "real model case has no steps/assertions"
    }
    $titlesSeen = @($cases | ForEach-Object { [string]$_.title })
    if (($titlesSeen | Sort-Object -Unique).Count -ne $titlesSeen.Count) { throw "real model produced duplicate case titles" }
    Log ("real design graph: cases={0}; first={1}; steps={2}; assertions={3}" -f $cases.Count, $first.title, $first.step_count, $first.assertion_count)

    if ($ProbeOnly) {
        Log "RESULT: PASS (real LLM design probe: conversation -> live graph)"
        exit 0
    }

    $beforeCount = $cases.Count
    $beforeRevision = @{}
    foreach ($case in $cases) { $beforeRevision[[string]$case.case_id] = [int]$case.revision }
    $message = Api "POST" "/api/versions/$versionID/conversations/$conversationID/messages" @{
        client_message_id = "real-llm-design-modify-" + [guid]::NewGuid().ToString("N")
        parts = @(@{ type = "text"; text = "请修改用例：补充异常提示，并新增一条边界用例。" })
    }
    Log "design modification message posted; waiting for real model graph change"

    $changed = $false
    $deadline = (Get-Date).AddSeconds($PollTimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Seconds 2
        try {
            $latest = Get-AllCases
            if ($latest.Count -gt $beforeCount) { $changed = $true; break }
            foreach ($case in $latest) {
                $id = [string]$case.case_id
                if ($beforeRevision.ContainsKey($id) -and [int]$case.revision -gt $beforeRevision[$id]) { $changed = $true; break }
            }
        } catch { Log "poll warning: $($_.Exception.Message)" "Yellow" }
        if ($changed) { break }
    }
    if (-not $changed) { throw "real model did not change the design graph after a modify request" }
    Log "real model graph changed via design conversation"

    $confirm = Api "POST" "/api/versions/$versionID/design/confirmations" @{ scope = "requirement"; target_id = $requirementIDs[0] }
    $confirmed = @($confirm.data.confirmed)
    if ($confirmed.Count -lt 1) { throw "requirement-scope confirmation confirmed no real-model case" }
    Log ("confirmed real-model case versions: " + $confirmed.Count)

    $export = Api "POST" "/api/versions/$versionID/exports" @{ owner_kind = "case"; owner_id = $versionID; format = "markdown" }
    $exportID = [string]$export.data.id
    $headers = @{ Authorization = "Bearer $token" }
    $outFile = Join-Path $logDir "real-llm-export.md"
    $response = Invoke-WebRequest -Uri ($apiBase + "/api/exports/$exportID/download") -Headers $headers -OutFile $outFile -PassThru -UseBasicParsing -TimeoutSec 120
    if ($response.StatusCode -ne 200) { throw "real-model export download failed" }
    $exportText = Get-Content -LiteralPath $outFile -Raw -Encoding UTF8
    if (-not $exportText.Contains($first.title)) { throw "real-model export does not contain the generated case title" }
    Log ("export ok: id={0} size={1} chars" -f $exportID, $exportText.Length)

    Log "RESULT: PASS (real LLM: design conversation -> live graph -> modification -> confirm -> export)"
    exit 0
} catch {
    Log "RESULT: FAIL: $($_.Exception.Message)" "Red"
    if ($_.ErrorDetails) { Log ("detail: " + $_.ErrorDetails.Message) "DarkYellow" }
    exit 1
} finally {
    if ($null -ne $apiProcess) { try { Stop-Process -Id $apiProcess.Id -Force -ErrorAction SilentlyContinue } catch { } }
}

