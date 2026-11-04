#requires -Version 5.1
<#
.SYNOPSIS
    S7 fault-injection acceptance for EzTest 0.2.1.

.DESCRIPTION
    Exercises the 0.2.1 fault semantics required by spec 13.2 S7:
      * a model rejection (HTTP 4xx from the real gateway) is classified as
        failure_kind=model_error, non-retryable, user-visible, with an
        actionable next step, and the actual model is recorded (no auto-switch);
      * a Playwright executor that is killed becomes offline in the admin
        registry, the API stays healthy, and the executor recovers when it is
        restarted (detached, so the acceptance parent process can exit);
      * an unreachable LLM gateway surfaces a typed connection-test failure
        without leaking the API key;
      * a 30+ model gateway is catalogued with an explicit default model and
        the public default model does not change automatically.

    Faults are injected against the live stack. The executor is restored in
    all paths (including Ctrl-C style failures via finally); temporary LLM
    configs and models are deleted again. If the shared stack is already down,
    the scenario exits 2 (blocked) with the exact URL/error.

    The job-level "network down" retry path is not injected here because the
    default LLM configuration is shared with other acceptance lanes and a
    broken default would corrupt their runs. That residual is reported as a
    warning, not hidden.

.PARAMETER EvidenceRoot
    Evidence root. Defaults to docs/version-V0.2.1/acceptance/evidence/s7-s10.

.PARAMETER ExecutorOfflineTimeoutSeconds
    How long to wait for the killed executor to become offline. Default 60.

.PARAMETER ModelFailureTimeoutSeconds
    How long to wait for the model-rejection run/job events. Default 120.

.PARAMETER SkipExecutorKill
    Do not kill the executor (diagnostic only; the kill check is recorded as a
    warning instead of a pass).

.NOTES
    Exit codes: 0 = passed, 1 = failed acceptance assertion, 2 = blocked
    environment. No git writes, no product code changes.
#>
[CmdletBinding()]
param(
    [string] $ApiBaseUrl = "",
    [string] $WebBaseUrl = "",
    [string] $ExecutorUrl = "",
    [string] $SutWebBaseUrl = "",
    [string] $SutApiBaseUrl = "",
    [string] $AdminUsername = "",
    [string] $AdminPassword = "",
    [string] $SutPassword = "",
    [string] $EvidenceRoot = "",
    [int] $ExecutorOfflineTimeoutSeconds = 60,
    [int] $ModelFailureTimeoutSeconds = 120,
    [switch] $SkipExecutorKill
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib.ps1")

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $EvidenceRoot = Join-Path $repoRoot "docs/version-V0.2.1/acceptance/evidence/s7-s10"
}
$context = Initialize-AcceptanceContext -RepoRoot $repoRoot -EvidenceRoot $EvidenceRoot `
    -ApiBaseUrl $ApiBaseUrl -WebBaseUrl $WebBaseUrl -ExecutorUrl $ExecutorUrl `
    -SutWebBaseUrl $SutWebBaseUrl -SutApiBaseUrl $SutApiBaseUrl `
    -AdminUsername $AdminUsername -AdminPassword $AdminPassword -SutPassword $SutPassword
$scenario = New-AcceptanceScenario -Context $context -Name "s7" -Title "Abnormal injection: executor, gateway, model 4xx (S7)"

$exitCode = 2
$tempConfigIds = [System.Collections.Generic.List[string]]::new()
$tempDefaultModelId = ""
$defaultConfigId = ""
$settingsRestored = $false
$executorNeedsRestart = $false
$gatewayPort = 59997
$networkPort = 59996
$runSuffix = (Get-Date).ToString("yyyyMMdd-HHmmss")
$versionId = ""
$conversationId = ""

function Get-DefaultLlmConfig {
    param($Context)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/admin/llm/configs" -Token $Context.Token -Label "list LLM configs"
    return @($response.Data) | Where-Object { [bool](Get-JsonProp -Object $_ -Name "is_default" -Default $false) } | Select-Object -First 1
}

function Add-ModelToConfig {
    param($Context, [string] $ConfigId, [string] $Model, [bool] $SupportsTools, [int] $ContextWindow = 8192)
    $response = Invoke-Api -Context $Context -Method "POST" -Path "/api/admin/llm/configs/$ConfigId/models" -Token $Context.Token -Body @{
        model                 = $Model
        context_window        = $ContextWindow
        max_output            = 2048
        supports_tools        = $SupportsTools
        supports_vision       = $false
        supports_reasoning    = $false
        supports_prompt_cache = $false
    }
    if ($response.Status -notin @(200, 201)) {
        throw "add model $Model failed: HTTP $($response.Status) $($response.Text)"
    }
    return $response.Data
}

function Remove-ModelFromConfig {
    param($Context, [string] $ConfigId, [string] $ModelId)
    if ([string]::IsNullOrWhiteSpace($ConfigId) -or [string]::IsNullOrWhiteSpace($ModelId)) { return }
    Invoke-Api -Context $Context -Method "DELETE" -Path "/api/admin/llm/configs/$ConfigId/models/$ModelId" -Token $Context.Token | Out-Null
}

function Remove-LlmConfig {
    param($Context, [string] $ConfigId)
    if ([string]::IsNullOrWhiteSpace($ConfigId)) { return }
    Invoke-Api -Context $Context -Method "DELETE" -Path "/api/admin/llm/configs/$ConfigId" -Token $Context.Token | Out-Null
}

function Write-RestartExecutorWrapper {
    param($Context, [string] $Token)
    $tempRoot = Join-Path $Context.RepoRoot ".tmp/acceptance-021"
    New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null
    $wrapper = Join-Path $tempRoot "s7-executor-restart.cmd"
    $logPath = Join-Path $Context.RepoRoot ".tmp/acceptance-021/s7-executor-restart.log"
    $errPath = Join-Path $Context.RepoRoot ".tmp/acceptance-021/s7-executor-restart.err.log"
    $executorDir = Join-Path $Context.RepoRoot "executor"
    $lines = @(
        "@echo off",
        "set EZTEST_EXECUTOR_TOKEN=$Token",
        "set EZTEST_EXECUTOR_ADDR=127.0.0.1:13336",
        "set EZTEST_API_URL=http://localhost:13333",
        "set EZTEST_EXECUTOR_HEARTBEAT_SECONDS=5",
        "set EZTEST_EXECUTOR_MAX_CONTEXTS=8",
        "cd /d `"$executorDir`"",
        "node src/server.mjs >> `"$logPath`" 2>> `"$errPath`""
    )
    [System.IO.File]::WriteAllText($wrapper, ($lines -join "`r`n") + "`r`n", [System.Text.UTF8Encoding]::new($false))
    return $wrapper
}

function Restart-ExecutorDetached {
    param($Context, [string] $Token, [int] $TimeoutSeconds = 90)
    $wrapper = Write-RestartExecutorWrapper -Context $Context -Token $Token
    $command = 'cmd.exe /c "' + $wrapper + '"'
    $created = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
        CommandLine      = $command
        CurrentDirectory = (Join-Path $Context.RepoRoot "executor")
    }
    if ($null -eq $created -or [int]$created.ReturnValue -ne 0) {
        throw "Win32_Process.Create failed: $($created | ConvertTo-Json -Compress)"
    }
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $health = Invoke-Api -Context $Context -Method "GET" -Path "$($Context.ExecutorUrl)/healthz" -TimeoutSeconds 5
            if ($health.Status -eq 200 -and [string](Get-JsonProp -Object $health.Json -Name "status" -Default "") -eq "ok") {
                return [pscustomobject]@{ Ok = $true; Wrapper = $wrapper; LauncherPid = $created.ProcessId; Browser = [string](Get-JsonProp -Object $health.Json -Name "browserVersion" -Default "") }
            }
        } catch { }
        Start-Sleep -Seconds 2
    }
    return [pscustomobject]@{ Ok = $false; Wrapper = $wrapper; LauncherPid = $created.ProcessId; Browser = "" }
}

function Get-ExecutorAdminRow {
    param($Context)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/admin/executors" -Token $Context.Token -Label "list executors"
    $rows = @($response.Data)
    $match = $rows | Where-Object {
        ([string](Get-JsonProp -Object $_ -Name "endpoint" -Default "")).TrimEnd('/') -eq $Context.ExecutorUrl.TrimEnd('/')
    } | Select-Object -First 1
    if ($null -eq $match -and $rows.Count -gt 0) { $match = $rows[0] }
    return $match
}

function Set-AdminSetting {
    param($Context, [string] $Key, $Value)
    $response = Invoke-Api -Context $Context -Method "PATCH" -Path "/api/admin/settings/$Key" -Token $Context.Token -Body @{ value = $Value }
    if ($response.Status -ne 200) { throw "set setting $Key failed: HTTP $($response.Status) $($response.Text)" }
    return $response
}

function Get-AdminSetting {
    param($Context, [string] $Key)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/admin/settings/$Key" -Token $Context.Token -Label "get setting $Key"
    return Get-JsonProp -Object $response.Data -Name "value" -Default $null
}

function Stop-ListenerOnPort {
    param([int] $Port)
    $connection = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $connection) { return [pscustomobject]@{ Stopped = $false; Pid = 0; CommandLine = "" } }
    $pidValue = [int]$connection.OwningProcess
    $process = Get-CimInstance Win32_Process -Filter "ProcessId=$pidValue" -ErrorAction SilentlyContinue
    $commandLine = if ($null -ne $process) { [string]$process.CommandLine } else { "" }
    # taskkill may report an already-exiting child (Chrome) as an error; the
    # listener is what matters. Suppress the native error and verify the port.
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & taskkill.exe /PID $pidValue /T /F 2>$null | Out-Null
        Start-Sleep -Seconds 1
        if ($null -ne (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1)) {
            try { Stop-Process -Id $pidValue -Force -ErrorAction SilentlyContinue } catch { }
        }
    } finally {
        $ErrorActionPreference = $previousErrorAction
    }
    Start-Sleep -Seconds 1
    $stillListening = $null -ne (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1)
    return [pscustomobject]@{ Stopped = (-not $stillListening); Pid = $pidValue; CommandLine = $commandLine }
}

function Start-S7ModelGateway {
    param($Context, [int] $Port)
    $tempRoot = Join-Path $Context.RepoRoot ".tmp/acceptance-021"
    New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null
    $scriptPath = Join-Path $tempRoot "s7-model-gateway.cjs"
    $js = @'
const http = require("node:http");
const port = Number(process.argv[2] || "59997");
const models = [];
for (let i = 1; i <= 31; i += 1) {
  models.push({
    id: "s7-gateway-model-" + String(i).padStart(2, "0"),
    object: "model",
    created: 1700000000 + i,
    owned_by: "s7-acceptance",
    context_window: 8192,
    max_output: 1024,
    supports_tools: i === 31,
  });
}
const server = http.createServer((req, res) => {
  const pathname = (req.url || "").split("?")[0];
  if (req.method === "GET" && (pathname === "/models" || pathname === "/v1/models")) {
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ object: "list", data: models }));
    return;
  }
  if (req.method === "GET" && pathname === "/health") {
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ status: "ok", models: models.length }));
    return;
  }
  if (req.method === "POST" && (pathname === "/chat/completions" || pathname === "/v1/chat/completions")) {
    let body = "";
    req.on("data", (chunk) => { body += chunk; });
    req.on("end", () => {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify({
        id: "chatcmpl-s7-gateway",
        object: "chat.completion",
        created: 1700000000,
        model: "s7-gateway-model-01",
        choices: [{ index: 0, message: { role: "assistant", content: "ok" }, finish_reason: "stop" }],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
      }));
    });
    return;
  }
  res.writeHead(404, { "content-type": "application/json" });
  res.end(JSON.stringify({ error: { message: "not found" } }));
});
server.listen(port, "127.0.0.1", () => console.log("[s7-gateway] listening on " + port + " models=" + models.length));
'@
    [System.IO.File]::WriteAllText($scriptPath, $js, [System.Text.UTF8Encoding]::new($false))
    $node = (Get-Command node).Source
    $logPath = Join-Path $tempRoot "s7-model-gateway.log"
    $errPath = Join-Path $tempRoot "s7-model-gateway.err.log"
    $wrapper = Join-Path $tempRoot "s7-model-gateway.cmd"
    $lines = @(
        "@echo off",
        "`"$node`" `"$scriptPath`" $Port >> `"$logPath`" 2>> `"$errPath`""
    )
    [System.IO.File]::WriteAllText($wrapper, ($lines -join "`r`n") + "`r`n", [System.Text.UTF8Encoding]::new($false))
    $created = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
        CommandLine      = 'cmd.exe /c "' + $wrapper + '"'
        CurrentDirectory = $tempRoot
    }
    if ($null -eq $created -or [int]$created.ReturnValue -ne 0) {
        throw "could not start the 31-model gateway: $($created | ConvertTo-Json -Compress)"
    }
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        try {
            $health = Invoke-Api -Context $Context -Method "GET" -Path "http://127.0.0.1:$Port/v1/models" -TimeoutSeconds 3
            if ($health.Status -eq 200) {
                return [pscustomobject]@{ Ok = $true; Port = $Port; Pid = $created.ProcessId; Models = @($health.Data).Count }
            }
        } catch { }
        Start-Sleep -Seconds 1
    }
    return [pscustomobject]@{ Ok = $false; Port = $Port; Pid = $created.ProcessId; Models = 0 }
}

function Wait-ExecutorStatus {
    param($Context, [string] $Status, [int] $TimeoutSeconds)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = ""
    while ((Get-Date) -lt $deadline) {
        try {
            $row = Get-ExecutorAdminRow -Context $Context
            $last = [string](Get-JsonProp -Object $row -Name "status" -Default "")
            if ($last -eq $Status) { return [pscustomobject]@{ Ok = $true; Row = $row; Last = $last } }
        } catch { $last = "query-error" }
        Start-Sleep -Seconds 2
    }
    return [pscustomobject]@{ Ok = $false; Row = $null; Last = $last }
}

try {
    Write-AcceptanceLog -Scenario $scenario -Message "S7 fault injection; executor=$($context.ExecutorUrl) offlineTimeout=${ExecutorOfflineTimeoutSeconds}s modelTimeout=${ModelFailureTimeoutSeconds}s"

    $login = Connect-EzTest -Context $context
    Add-AcceptanceCheck -Scenario $scenario -Name "EzTest admin login" -Passed $login.Ok -Severity blocker `
        -Detail ($(if ($login.Ok) { "admin session established" } else { "admin credentials rejected" }))
    if (-not $login.Ok) {
        $scenario.ErrorMessage = "S7 blocked: EzTest admin login failed"
    } else {
        # ------------------------------------------------------------------
        # Preflight: the executor must be reachable before we fault it.
        # ------------------------------------------------------------------
        $executorBaseline = Test-ExecutorReachable -Context $context -TimeoutSeconds 15
        $baselineRow = $null
        $baselineActive = $false
        try {
            $baselineRow = Get-ExecutorAdminRow -Context $context
            $baselineActive = ([string](Get-JsonProp -Object $baselineRow -Name "status" -Default "")) -eq "active"
        } catch { }
        Add-AcceptanceCheck -Scenario $scenario -Name "executor healthy before fault injection" -Passed ($executorBaseline.Ok -and $baselineActive) -Severity blocker `
            -Detail ("healthz=$($executorBaseline.Ok) browser=$($executorBaseline.BrowserVersion) registry=" + [string](Get-JsonProp -Object $baselineRow -Name "status" -Default "missing"))

        $defaultConfig = Get-DefaultLlmConfig -Context $context
        $defaultConfigId = [string](Get-JsonProp -Object $defaultConfig -Name "id" -Default "")
        Add-AcceptanceCheck -Scenario $scenario -Name "default LLM configuration is explicit" -Passed ($defaultConfigId -ne "") -Severity blocker `
            -Detail ("config=" + [string](Get-JsonProp -Object $defaultConfig -Name "name" -Default "?") + " default_model_id=" + [string](Get-JsonProp -Object $defaultConfig -Name "default_model_id" -Default "?"))
        $publicModelsBefore = Invoke-ApiOrFail -Context $context -Method "GET" -Path "/api/llm/models" -Token $context.Token -Label "public models"
        $publicDefaultBefore = @($publicModelsBefore.Data) | Where-Object { [bool](Get-JsonProp -Object $_ -Name "default" -Default $false) } | Select-Object -First 1
        $publicDefaultModelBefore = [string](Get-JsonProp -Object $publicDefaultBefore -Name "model" -Default "")

        # ------------------------------------------------------------------
        # 1. 30+ model gateway: catalog refresh keeps the default explicit and
        #    does not switch the public default model.
        # ------------------------------------------------------------------
        if ($defaultConfigId -ne "") {
            $gateway = $null
            $gatewayConfigId = ""
            try {
                $gateway = Start-S7ModelGateway -Context $context -Port $gatewayPort
                Add-AcceptanceCheck -Scenario $scenario -Name "31-model gateway fixture reachable" -Passed $gateway.Ok -Severity blocker `
                    -Detail ("port=$gatewayPort pid=$($gateway.Pid) models=$($gateway.Models)")

                if ($gateway.Ok) {
                    $createConfig = Invoke-Api -Context $context -Method "POST" -Path "/api/admin/llm/configs" -Token $context.Token -Body @{
                        name             = "s7-31-model-gateway-$runSuffix"
                        provider         = "s7-acceptance"
                        protocol         = "openai_chat_completions"
                        base_url         = "http://127.0.0.1:$gatewayPort"
                        api_key          = "s7-gateway-not-a-secret"
                        is_default       = $false
                        status           = "active"
                        timeout_seconds  = 10
                        max_retries      = 0
                        thinking_type    = "auto"
                        reasoning_effort = "none"
                    }
                    if ($createConfig.Status -notin @(200, 201)) { throw "create 31-model config failed: HTTP $($createConfig.Status) $($createConfig.Text)" }
                    $gatewayConfigId = [string](Get-JsonProp -Object $createConfig.Data -Name "id" -Default "")
                    if ($gatewayConfigId -ne "") { [void]$tempConfigIds.Add($gatewayConfigId) }

                    $refresh = Invoke-Api -Context $context -Method "POST" -Path "/api/admin/llm/configs/$gatewayConfigId/refresh-models" -Token $context.Token
                    $refreshedModels = @($refresh.Data)
                    Add-AcceptanceCheck -Scenario $scenario -Name "model catalog refresh accepts 31 models" -Passed ($refresh.Status -eq 200 -and $refreshedModels.Count -ge 30) -Severity assertion `
                        -Detail ("HTTP $($refresh.Status) models=$($refreshedModels.Count)")

                    $gatewayConfig = Invoke-Api -Context $context -Method "GET" -Path "/api/admin/llm/configs/$gatewayConfigId" -Token $context.Token
                    $gatewayModels = @(Get-JsonProp -Object $gatewayConfig.Data -Name "models" -Default @())
                    $explicitDefault = Get-JsonProp -Object $gatewayConfig.Data -Name "default_model_id" -Default $null
                    Add-AcceptanceCheck -Scenario $scenario -Name "30+ model gateway keeps an explicit default (no models[0] fallback)" -Passed ($gatewayConfig.Status -eq 200 -and $gatewayModels.Count -ge 30 -and ($null -eq $explicitDefault -or [int64]$explicitDefault -eq 0)) -Severity assertion `
                        -Detail ("models=$($gatewayModels.Count) default_model_id=$(if ($null -eq $explicitDefault) { 'null' } else { [string]$explicitDefault })")

                    $publicAfter = Invoke-ApiOrFail -Context $context -Method "GET" -Path "/api/llm/models" -Token $context.Token -Label "public models after refresh"
                    $publicDefaultAfter = @($publicAfter.Data) | Where-Object { [bool](Get-JsonProp -Object $_ -Name "default" -Default $false) } | Select-Object -First 1
                    $publicDefaultModelAfter = [string](Get-JsonProp -Object $publicDefaultAfter -Name "model" -Default "")
                    Add-AcceptanceCheck -Scenario $scenario -Name "public default model does not auto-switch after 30+ catalog refresh" -Passed ($publicDefaultModelAfter -eq $publicDefaultModelBefore) -Severity assertion `
                        -Detail ("before=$publicDefaultModelBefore after=$publicDefaultModelAfter")

                    $gatewayEvidence = [pscustomobject]@{
                        fixture_models = $gateway.Models
                        config_id = $gatewayConfigId
                        refreshed_models = $refreshedModels.Count
                        default_model_id = $explicitDefault
                        public_default_before = $publicDefaultModelBefore
                        public_default_after = $publicDefaultModelAfter
                        supports_tools_sample = @($gatewayModels | Select-Object -First 3 | ForEach-Object { [pscustomobject]@{ model = [string](Get-JsonProp -Object $_ -Name "model" -Default ""); supports_tools = [bool](Get-JsonProp -Object $_ -Name "supports_tools" -Default $false) } })
                    }
                    Write-EvidenceJson -Path (Join-Path $scenario.Directory "s7-model-gateway.json") -Value $gatewayEvidence
                    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s7-model-gateway.json") -Kind "api" -Note "31-model gateway catalog refresh"
                }
            } catch {
                Add-AcceptanceCheck -Scenario $scenario -Name "30+ model gateway catalog refresh" -Passed $false -Severity assertion -Detail $_.Exception.Message
            } finally {
                Remove-LlmConfig -Context $context -ConfigId $gatewayConfigId
                if ($null -ne $gateway -and $gateway.Ok) { [void](Stop-ListenerOnPort -Port $gatewayPort) }
                if (-not [string]::IsNullOrWhiteSpace($gatewayConfigId)) { $tempConfigIds.Remove($gatewayConfigId) | Out-Null }
            }
        }

        # ------------------------------------------------------------------
        # 2. Model 4xx: real gateway rejects a temporary catalog model; the
        #    run must be classified model_error, non-retryable, actionable, and
        #    must not silently switch models.
        # ------------------------------------------------------------------
        $modelFaultEvidence = [ordered]@{}
        try {
            $binding = Resolve-AcceptanceSutBinding -Context $context
            $version = New-AcceptanceVersion -Context $context -Title ("S7 model 4xx acceptance " + $runSuffix) -Binding $binding.Binding
            $versionId = [string](Get-JsonProp -Object $version -Name "id" -Default "")
            $conversation = New-AcceptanceConversation -Context $context -VersionId $versionId -Kind "clarification" -Title "S7 model 4xx conversation"
            $conversationId = [string](Get-JsonProp -Object $conversation -Name "id" -Default "")
            Add-AcceptanceCheck -Scenario $scenario -Name "S7 version and conversation created" -Passed ($versionId -ne "" -and $conversationId -ne "") -Severity blocker `
                -Detail "version=$versionId conversation=$conversationId"

            $tempModelName = "s7-invalid-model-$runSuffix"
            $tempModel = Add-ModelToConfig -Context $context -ConfigId $defaultConfigId -Model $tempModelName -SupportsTools $true
            $tempDefaultModelId = [string](Get-JsonProp -Object $tempModel -Name "id" -Default "")
            $patch = Invoke-Api -Context $context -Method "PATCH" -Path "/api/versions/$versionId/conversations/$conversationId" -Token $context.Token -Body @{ model_override = $tempModelName }
            Add-AcceptanceCheck -Scenario $scenario -Name "conversation accepts the temporary model override" -Passed ($patch.Status -eq 200) -Severity assertion `
                -Detail ("HTTP $($patch.Status) effective=" + [string](Get-JsonProp -Object $patch.Data -Name "effective_model" -Default "?"))

            $cursor = Get-EventCursor -Context $context -Channel "version:$versionId"
            New-AcceptanceUserMessage -Context $context -VersionId $versionId -ConversationId $conversationId -Text "Reply with the single word ok." | Out-Null
            $deadline = (Get-Date).AddSeconds($ModelFailureTimeoutSeconds)
            $faultEvents = @()
            $wantedEventTypes = @("agent.run.failed", "work_item.dead", "job.finished")
            while ((Get-Date) -lt $deadline) {
                try {
                    $eventsResponse = Invoke-Api -Context $context -Method "GET" -Path ("/api/events?channel=" + [Uri]::EscapeDataString("version:$versionId") + "&after=$cursor&limit=200") -Token $context.Token
                    $allEvents = @($eventsResponse.Data)
                    $faultEvents = @($allEvents | Where-Object { $wantedEventTypes -contains [string](Get-JsonProp -Object $_ -Name "type" -Default "") })
                } catch {
                    $faultEvents = @()
                }
                $finished = @($faultEvents | Where-Object { [string](Get-JsonProp -Object $_ -Name "type" -Default "") -eq "job.finished" })
                if ($finished.Count -gt 0) { break }
                Start-Sleep -Seconds 1
            }
            Write-EvidenceJson -Path (Join-Path $scenario.Directory "s7-model-4xx-events.json") -Value $faultEvents
            Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s7-model-4xx-events.json") -Kind "event" -Note "model 4xx run/job events"

            $failedRun = $faultEvents | Where-Object { [string](Get-JsonProp -Object $_ -Name "type" -Default "") -eq "agent.run.failed" } | Select-Object -First 1
            $deadItem = $faultEvents | Where-Object { [string](Get-JsonProp -Object $_ -Name "type" -Default "") -eq "work_item.dead" } | Select-Object -First 1
            $finishedJob = $faultEvents | Where-Object { [string](Get-JsonProp -Object $_ -Name "type" -Default "") -eq "job.finished" } | Select-Object -First 1
            $failedPayload = Get-JsonProp -Object $failedRun -Name "payload" -Default $null
            $deadPayload = Get-JsonProp -Object $deadItem -Name "payload" -Default $null
            $jobPayload = Get-JsonProp -Object $finishedJob -Name "payload" -Default $null
            $failureKind = [string](Get-JsonProp -Object $failedPayload -Name "failure_kind" -Default "")
            $failureRetryable = Get-JsonProp -Object $failedPayload -Name "retryable" -Default $null
            $failureAction = [string](Get-JsonProp -Object $failedPayload -Name "failure_action" -Default "")
            $failureVisible = Get-JsonProp -Object $failedPayload -Name "failure_user_visible" -Default $null
            $terminalReason = [string](Get-JsonProp -Object $failedPayload -Name "terminal_reason" -Default "")
            $deadAttempt = Get-JsonProp -Object $deadPayload -Name "attempt" -Default $null
            $deadError = [string](Get-JsonProp -Object $deadPayload -Name "error" -Default "")
            $jobStatus = [string](Get-JsonProp -Object $jobPayload -Name "status" -Default "")
            $jobError = [string](Get-JsonProp -Object $jobPayload -Name "error" -Default "")

            Add-AcceptanceCheck -Scenario $scenario -Name "model 4xx classified as model_error" -Passed ($failureKind -eq "model_error" -and $terminalReason -eq "model_error") -Severity assertion `
                -Detail "failure_kind=$failureKind terminal_reason=$terminalReason"
            Add-AcceptanceCheck -Scenario $scenario -Name "model 4xx is not retried (non-retryable, one attempt)" -Passed ($failureRetryable -eq $false -and [int]$deadAttempt -eq 1) -Severity assertion `
                -Detail "retryable=$failureRetryable dead_attempt=$deadAttempt"
            Add-AcceptanceCheck -Scenario $scenario -Name "model 4xx carries a user-visible actionable next step" -Passed ($failureVisible -eq $true -and -not [string]::IsNullOrWhiteSpace($failureAction)) -Severity assertion `
                -Detail "user_visible=$failureVisible action=$failureAction"
            Add-AcceptanceCheck -Scenario $scenario -Name "model 4xx terminates the job as failed with the classified error" -Passed ($jobStatus -eq "failed" -and $jobError -match "model_error") -Severity assertion `
                -Detail "job_status=$jobStatus job_error_prefix=$($jobError.Substring(0, [Math]::Min(120, $jobError.Length)))"
            Add-AcceptanceCheck -Scenario $scenario -Name "model 4xx is not mislabeled as environment" -Passed ($failureKind -ne "environment" -and $jobError -notmatch "environment") -Severity assertion `
                -Detail "failure_kind=$failureKind"

            $runs = Invoke-Api -Context $context -Method "GET" -Path "/api/agent-center/runs?limit=30" -Token $context.Token
            $versionRuns = @($runs.Data) | Where-Object { [string](Get-JsonProp -Object $_ -Name "version_id" -Default "") -eq $versionId }
            $modelRun = $versionRuns | Where-Object {
                [string](Get-JsonProp -Object $_ -Name "status" -Default "") -eq "failed" -and
                [string](Get-JsonProp -Object $_ -Name "terminal_reason" -Default "") -eq "model_error"
            } | Select-Object -First 1
            $actualModel = [string](Get-JsonProp -Object $modelRun -Name "model" -Default "")
            $fallbackFrom = [string](Get-JsonProp -Object $modelRun -Name "fallback_from" -Default "")
            Add-AcceptanceCheck -Scenario $scenario -Name "actual model recorded and no automatic fallback" -Passed ($actualModel -eq $tempModelName -and [string]::IsNullOrWhiteSpace($fallbackFrom)) -Severity assertion `
                -Detail "actual_model=$actualModel fallback_from=$fallbackFrom"
            Write-EvidenceJson -Path (Join-Path $scenario.Directory "s7-model-4xx-runs.json") -Value ([pscustomobject]@{
                version_id = $versionId
                conversation_id = $conversationId
                temporary_model = $tempModelName
                failed_runs = @($versionRuns | Where-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") -eq "failed" } | ForEach-Object {
                    [pscustomobject]@{
                        run_id = [string](Get-JsonProp -Object $_ -Name "id" -Default "")
                        work_type = [string](Get-JsonProp -Object $_ -Name "work_type" -Default "")
                        status = [string](Get-JsonProp -Object $_ -Name "status" -Default "")
                        terminal_reason = [string](Get-JsonProp -Object $_ -Name "terminal_reason" -Default "")
                        model = [string](Get-JsonProp -Object $_ -Name "model" -Default "")
                        fallback_from = [string](Get-JsonProp -Object $_ -Name "fallback_from" -Default "")
                    }
                })
                sse_failure_kind = $failureKind
                sse_retryable = $failureRetryable
                sse_failure_action = $failureAction
                sse_job_status = $jobStatus
            })
            Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s7-model-4xx-runs.json") -Kind "api" -Note "model 4xx run classification"
        } catch {
            Add-AcceptanceCheck -Scenario $scenario -Name "model 4xx fault injection" -Passed $false -Severity assertion -Detail $_.Exception.Message
        } finally {
            if ($tempDefaultModelId -ne "" -and $defaultConfigId -ne "") {
                Remove-ModelFromConfig -Context $context -ConfigId $defaultConfigId -ModelId $tempDefaultModelId
                $tempDefaultModelId = ""
            }
            if ($versionId -ne "" -and $conversationId -ne "") {
                Invoke-Api -Context $context -Method "PATCH" -Path "/api/versions/$versionId/conversations/$conversationId" -Token $context.Token -Body @{ model_override = $null } | Out-Null
            }
        }

        # ------------------------------------------------------------------
        # 3. Network: an unreachable gateway must return a typed config-test
        #    failure without leaking the API key.
        # ------------------------------------------------------------------
        try {
            $networkConfigId = ""
            $createNetwork = Invoke-Api -Context $context -Method "POST" -Path "/api/admin/llm/configs" -Token $context.Token -Body @{
                name             = "s7-unreachable-gateway-$runSuffix"
                provider         = "s7-acceptance"
                protocol         = "openai_chat_completions"
                base_url         = "http://127.0.0.1:$networkPort"
                api_key          = "s7-network-not-a-secret"
                is_default       = $false
                status           = "active"
                timeout_seconds  = 5
                max_retries      = 0
                thinking_type    = "auto"
                reasoning_effort = "none"
            }
            if ($createNetwork.Status -in @(200, 201)) {
                $networkConfigId = [string](Get-JsonProp -Object $createNetwork.Data -Name "id" -Default "")
                if ($networkConfigId -ne "") { [void]$tempConfigIds.Add($networkConfigId) }
                $networkModel = Add-ModelToConfig -Context $context -ConfigId $networkConfigId -Model "s7-unreachable-model" -SupportsTools $false
                $networkModelId = [string](Get-JsonProp -Object $networkModel -Name "id" -Default "")
                Invoke-Api -Context $context -Method "PATCH" -Path "/api/admin/llm/configs/$networkConfigId" -Token $context.Token -Body @{ default_model_id = $networkModelId } | Out-Null
                $test = Invoke-Api -Context $context -Method "POST" -Path "/api/admin/llm/configs/$networkConfigId/test" -Token $context.Token -TimeoutSeconds 30
                $testOk = Get-JsonProp -Object $test.Data -Name "ok" -Default $null
                $testError = [string](Get-JsonProp -Object $test.Data -Name "error" -Default "")
                $testApiCode = [string](Get-JsonProp -Object $test.Error -Name "code" -Default "")
                $testApiMessage = [string](Get-JsonProp -Object $test.Error -Name "message" -Default "")
                $visibleError = if ($testError) { $testError } elseif ($testApiMessage) { $testApiMessage } else { $test.Text }
                $typedFailure = (($test.Status -eq 200 -and $testOk -eq $false) -or ($test.Status -ge 400 -and $test.Status -lt 600 -and -not [string]::IsNullOrWhiteSpace($visibleError)))
                Add-AcceptanceCheck -Scenario $scenario -Name "unreachable LLM gateway reports a typed, key-safe failure" -Passed ($typedFailure -and $visibleError -ne "" -and $visibleError -notmatch "s7-network-not-a-secret") -Severity assertion `
                    -Detail ("HTTP $($test.Status) ok=$testOk code=$testApiCode error=" + $visibleError.Substring(0, [Math]::Min(180, $visibleError.Length)))
                Write-EvidenceJson -Path (Join-Path $scenario.Directory "s7-network-config-test.json") -Value ([pscustomobject]@{
                    config_id = $networkConfigId
                    base_url = "http://127.0.0.1:$networkPort"
                    http_status = $test.Status
                    ok = $testOk
                    error_code = $testApiCode
                    error = $visibleError
                    api_key_leaked = ($visibleError -match "s7-network-not-a-secret")
                })
                Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s7-network-config-test.json") -Kind "api" -Note "unreachable gateway connection test"
                Add-AcceptanceCheck -Scenario $scenario -Name "job-level network retry semantics were not injected on the shared stack" -Passed $false -Severity warning `
                    -Detail "the default LLM config is shared with parallel acceptance lanes; breaking its base_url would corrupt their runs. The typed config-test failure is asserted; job-level retryability remains a residual for an isolated run."
            } else {
                Add-AcceptanceCheck -Scenario $scenario -Name "unreachable LLM gateway config created" -Passed $false -Severity assertion -Detail ("HTTP $($createNetwork.Status) $($createNetwork.Text)")
            }
            if (-not [string]::IsNullOrWhiteSpace($networkConfigId)) { Remove-LlmConfig -Context $context -ConfigId $networkConfigId }
        } catch {
            Add-AcceptanceCheck -Scenario $scenario -Name "unreachable LLM gateway fault injection" -Passed $false -Severity assertion -Detail $_.Exception.Message
        }

        # ------------------------------------------------------------------
        # 4. Executor kill: offline registry, healthy API, no environment mislabel,
        #    detached restart, recovery.
        # ------------------------------------------------------------------
        if ($SkipExecutorKill) {
            Add-AcceptanceCheck -Scenario $scenario -Name "executor kill/recovery" -Passed $false -Severity warning -Detail "skipped by -SkipExecutorKill"
        } else {
            $executorEvidence = [ordered]@{}
            $offlineBefore = $null
            $heartbeatBefore = $null
            try {
                $offlineBefore = Get-AdminSetting -Context $context -Key "execution.executor_offline_seconds"
                $heartbeatBefore = Get-AdminSetting -Context $context -Key "execution.executor_heartbeat_seconds"
                Set-AdminSetting -Context $context -Key "execution.executor_offline_seconds" -Value 5 | Out-Null
                Set-AdminSetting -Context $context -Key "execution.executor_heartbeat_seconds" -Value 1 | Out-Null
                $settingsRestored = $false

                $versionsBefore = Invoke-ApiOrFail -Context $context -Method "GET" -Path "/api/versions?limit=50" -Token $context.Token -Label "versions before kill"
                $failedBefore = @(@($versionsBefore.Data) | Where-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") -eq "failed" }).Count

                $executorNeedsRestart = $true
                $killed = Stop-ListenerOnPort -Port 13336
                Add-AcceptanceCheck -Scenario $scenario -Name "executor process killed" -Passed ($killed.Stopped -and $killed.CommandLine -match "server\.mjs") -Severity assertion `
                    -Detail ("pid=$($killed.Pid) command=" + $killed.CommandLine.Substring(0, [Math]::Min(160, $killed.CommandLine.Length)))

                $offline = Wait-ExecutorStatus -Context $context -Status "offline" -TimeoutSeconds $ExecutorOfflineTimeoutSeconds
                Add-AcceptanceCheck -Scenario $scenario -Name "killed executor becomes offline in the registry" -Passed $offline.Ok -Severity assertion `
                    -Detail ("status=$($offline.Last) timeout=${ExecutorOfflineTimeoutSeconds}s")

                $healthDuring = Invoke-Api -Context $context -Method "GET" -Path "/api/health" -TimeoutSeconds 10
                $healthStatus = [string](Get-JsonProp -Object $healthDuring.Data -Name "status" -Default "")
                $healthDb = [string](Get-JsonProp -Object $healthDuring.Data -Name "db" -Default "")
                Add-AcceptanceCheck -Scenario $scenario -Name "API stays healthy while the executor is down" -Passed ($healthDuring.Status -eq 200 -and $healthStatus -eq "ok" -and $healthDb -eq "ok") -Severity assertion `
                    -Detail "HTTP $($healthDuring.Status) status=$healthStatus db=$healthDb"

                $versionsDuring = Invoke-ApiOrFail -Context $context -Method "GET" -Path "/api/versions?limit=50" -Token $context.Token -Label "versions during kill"
                $failedDuring = @(@($versionsDuring.Data) | Where-Object { [string](Get-JsonProp -Object $_ -Name "status" -Default "") -eq "failed" }).Count
                Add-AcceptanceCheck -Scenario $scenario -Name "executor outage does not mark versions failed" -Passed ($failedDuring -le $failedBefore) -Severity assertion `
                    -Detail "failed_versions before=$failedBefore during=$failedDuring"

                if ($versionId -ne "") {
                    try {
                        $shot = Save-AcceptanceScreenshot -Context $context -Scenario $scenario -Route "/versions/${versionId}?tab=execution" -FileName "s7-executor-offline.png" `
                            -WaitForSelector '[data-testid="execution-toolbar"]' -Note "execution tab while the executor is offline"
                        $offlineText = ""
                        if ($null -ne $shot.Result) { $offlineText = [string](Get-JsonProp -Object $shot.Result -Name "textPrefix" -Default "") }
                        $semanticOk = ($offlineText -match "执行器离线") -and ($offlineText -match "等待执行器恢复") -and ($offlineText -notmatch "环境异常")
                        Add-AcceptanceCheck -Scenario $scenario -Name "offline UI says executor offline, not environment failure" -Passed $semanticOk -Severity assertion `
                            -Detail ("executor_offline_label=" + ($offlineText -match "执行器离线") + " wait_for_recovery=" + ($offlineText -match "等待执行器恢复") + " environment_label=" + ($offlineText -match "环境异常"))
                        Add-AcceptanceCheck -Scenario $scenario -Name "executor-offline UI evidence captured" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $shot.Path)
                    } catch {
                        Add-AcceptanceCheck -Scenario $scenario -Name "executor-offline UI evidence captured" -Passed $false -Severity warning -Detail $_.Exception.Message
                    }
                }

                $restartToken = [Environment]::GetEnvironmentVariable("EZTEST_EXECUTOR_TOKEN", "Process")
                if ([string]::IsNullOrWhiteSpace($restartToken)) { $restartToken = "eztest-dev-executor-token" }
                $restarted = Restart-ExecutorDetached -Context $context -Token $restartToken -TimeoutSeconds 90
                Add-AcceptanceCheck -Scenario $scenario -Name "executor restarts and answers /healthz" -Passed $restarted.Ok -Severity assertion `
                    -Detail ("launcher_pid=$($restarted.LauncherPid) browser=$($restarted.Browser)")
                if ($restarted.Ok) { $executorNeedsRestart = $false }
                $active = Wait-ExecutorStatus -Context $context -Status "active" -TimeoutSeconds 90
                Add-AcceptanceCheck -Scenario $scenario -Name "restarted executor is active again" -Passed $active.Ok -Severity assertion `
                    -Detail ("status=$($active.Last)")

                $executorEvidence = [ordered]@{
                    killed_pid = $killed.Pid
                    killed_command = $killed.CommandLine
                    offline_status = $offline.Last
                    api_health_during_outage = [pscustomobject]@{ http = $healthDuring.Status; status = $healthStatus; db = $healthDb }
                    failed_versions_before = $failedBefore
                    failed_versions_during = $failedDuring
                    restarted = $restarted.Ok
                    restarted_browser = $restarted.Browser
                    recovery_status = $active.Last
                }
                Write-EvidenceJson -Path (Join-Path $scenario.Directory "s7-executor-fault.json") -Value $executorEvidence
                Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s7-executor-fault.json") -Kind "api" -Note "executor kill/offline/recover"
            } catch {
                Add-AcceptanceCheck -Scenario $scenario -Name "executor kill/recovery fault injection" -Passed $false -Severity assertion -Detail $_.Exception.Message
            } finally {
                if ($executorNeedsRestart) {
                    try {
                        $restartToken = [Environment]::GetEnvironmentVariable("EZTEST_EXECUTOR_TOKEN", "Process")
                        if ([string]::IsNullOrWhiteSpace($restartToken)) { $restartToken = "eztest-dev-executor-token" }
                        $restarted = Restart-ExecutorDetached -Context $context -Token $restartToken -TimeoutSeconds 90
                        Add-AcceptanceCheck -Scenario $scenario -Name "executor restored after fault injection" -Passed $restarted.Ok -Severity blocker -Detail ("launcher_pid=$($restarted.LauncherPid)")
                        if ($restarted.Ok) { $executorNeedsRestart = $false }
                    } catch {
                        Add-AcceptanceCheck -Scenario $scenario -Name "executor restored after fault injection" -Passed $false -Severity blocker -Detail $_.Exception.Message
                    }
                }
                if ($null -ne $offlineBefore) {
                    try { Set-AdminSetting -Context $context -Key "execution.executor_offline_seconds" -Value $offlineBefore | Out-Null } catch { }
                }
                if ($null -ne $heartbeatBefore) {
                    try { Set-AdminSetting -Context $context -Key "execution.executor_heartbeat_seconds" -Value $heartbeatBefore | Out-Null } catch { }
                }
                $settingsRestored = $true
            }
        }

        if (-not $settingsRestored) {
            Add-AcceptanceCheck -Scenario $scenario -Name "executor settings restored" -Passed $false -Severity warning -Detail "settings restore was not confirmed"
        }
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S7 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    if ($tempDefaultModelId -ne "" -and $defaultConfigId -ne "") { Remove-ModelFromConfig -Context $context -ConfigId $defaultConfigId -ModelId $tempDefaultModelId }
    foreach ($configId in @($tempConfigIds)) { Remove-LlmConfig -Context $context -ConfigId $configId }
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S7 abnormal injection"
}

exit $exitCode
