#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W2-6 issue/retest domain script.

.DESCRIPTION
    Reuses the W2 base data created by scripts/v022-w2-data.ps1 (database
    eztest_v022_w2; version and failed base case_run from run/data-verdict.json)
    and exercises the issue retest flow through the product API only:

      1. start an isolated API on 13449, a real executor on 13352 and the
         vision-caption-style LLM forward proxy on 13999 when they are not
         already listening (the database is never dropped or recreated);
      2. log in as the configured admin and point execution.executor_url at the
         isolated executor (the previous value is restored at the end);
      3. select the auto-created issue of the failed base run, or create a
         manual issue linked to that run when no retestable issue exists;
      4. POST /api/versions/{id}/issues/{iid}/retest with an empty selection,
         wait (bounded) for the retest wave to reach its terminal state and for
         the issue retest writeback;
      5. verify delivery_issue.retest_status / last_retest_wave_id /
         last_retest_case_run_id, role='retest' refs, issue history, the
         issue.retest_started/completed/failed event payloads (including error)
         and that the response case_run_ids are the linked retest runs;
      6. optionally capture 1-3 screenshots of the issue center, the detail
         sheet and the retest run (screenshots never gate the verdict).

    Evidence (untracked) is written next to the W2 base evidence:
      docs/version-V0.22/acceptance/evidence/w2/run/issue/issue-verdict.json
      docs/version-V0.22/acceptance/evidence/w2/run/issue/*.png

    PASS requires: issue >= 1 (linked to the failed run), retest wave done,
    retest_status passed|failed, at least two role='retest' refs (wave + run),
    at least one history row, at least one retest event, and every response
    case_run_id present in the DB refs / wave runs with last_retest_case_run_id
    matching the response.

    Everything this script starts is stopped at the end. A pre-existing API,
    executor, proxy or web dev server is reused and left running. The SUT
    frontend on localhost:5173 is left running when this script starts it.

.PARAMETER SkipBuild
    Reuse .tmp/w2-issue/eztest.exe instead of rebuilding the current HEAD binary.

.PARAMETER WaveWaitSeconds
    Upper bound for the retest wave to reach a terminal status (600s).

.PARAMETER SkipScreenshots
    Do not start the web UI or capture screenshots.
#>
param(
    [switch]$SkipBuild,
    [int]$WaveWaitSeconds = 600,
    [switch]$SkipScreenshots
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

# Native tools (psql/curl) write UTF-8; PowerShell 5.1 otherwise decodes their
# stdout with the console code page.
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

$dbName = 'eztest_v022_w2'
$apiPort = 13449
$webPort = 6873
$execPort = 13352
$proxyPort = 13999
$sutUrl = 'http://localhost:5173/'
$sutDir = if ($env:EZTEST_SUT_DIR) { $env:EZTEST_SUT_DIR } else { 'D:\project\blog-frontend' }
$apiBase = "http://127.0.0.1:$apiPort"
$webUrl = "http://127.0.0.1:$webPort"
$executorUrl = "http://127.0.0.1:$execPort"
$proxyBase = "http://127.0.0.1:$proxyPort"
$work = Join-Path $repoRoot '.tmp\w2-issue'
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2\run'
$issueDir = Join-Path $evidenceRoot 'issue'
$dataVerdictPath = Join-Path $evidenceRoot 'data-verdict.json'
$issueVerdictPath = Join-Path $issueDir 'issue-verdict.json'
$uiScriptPath = Join-Path $work 'w2-issue-ui.mjs'
$apiExe = Join-Path $work 'eztest.exe'
$apiLog = Join-Path $work 'api.log'
$webLog = Join-Path $work 'web.log'
$execLog = Join-Path $work 'executor.log'
$proxyLog = Join-Path $work 'proxy.log'
$psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
$goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'
$backendDir = Join-Path $repoRoot 'backend'

New-Item -ItemType Directory -Force -Path $work, $issueDir | Out-Null

$script:token = ''
$script:apiStarted = $false
$script:webStarted = $false
$script:execStarted = $false
$script:proxyStarted = $false
$script:originalExecutorUrl = $null
$script:executorUrlRestored = $false
$script:errors = New-Object System.Collections.Generic.List[string]
$script:startedAt = Get-Date
$script:eventBaseline = [long]0
$script:issueSource = ''
$script:issueStatus = ''
$script:waveStatus = ''
$script:historyRows = 0
$script:retestRefRows = @()
$script:responseCaseRunIds = @()
$script:dbLastWaveId = ''
$script:dbLastCaseRunId = ''
$script:dbRetestStatus = ''

# The verdict is written even when the flow fails so a reviewer can see exactly
# which assertion failed.
$verdict = [ordered]@{
    status             = 'FAIL'
    versionId          = $null
    issueId            = $null
    retestWaveId       = $null
    retestStatus       = $null
    retestCaseRunIds   = @()
    historyRows        = 0
    events             = [ordered]@{ started = 0; completed = 0; failed = 0; terminal = 0 }
    apiBase            = $apiBase
    generatedAt        = (Get-Date -Format o)
    failedCaseRunId    = $null
    issueSource        = ''
    issueStatus        = ''
    retestWaveStatus   = ''
    responseCaseRunIds = @()
    db                 = [ordered]@{
        retestStatus       = $null
        lastRetestWaveId   = $null
        lastRetestCaseRunId = $null
    }
    refs               = [ordered]@{
        total          = 0
        retest         = 0
        retestWave     = 0
        retestCaseRun  = 0
        rows           = @()
    }
    history            = @()
    eventRows          = @()
    waveRuns           = @()
    executableUrl      = $executorUrl
    screenshots        = @()
    sutStarted         = $false
    errors             = @()
}

# UTF-8 without BOM: the verdict may carry CJK API messages/reasons.
function Write-Utf8([string]$path, [string]$text) {
    [System.IO.File]::WriteAllText($path, $text, (New-Object System.Text.UTF8Encoding($false)))
}

function Read-JsonFile([string]$path) {
    $text = [System.IO.File]::ReadAllText($path, (New-Object System.Text.UTF8Encoding($false)))
    return ($text | ConvertFrom-Json)
}

function Get-EnvValue([string]$key) {
    $line = Select-String -Path (Join-Path $repoRoot '.env') -Pattern ("^" + [regex]::Escape($key) + "=(.*)$")
    if (-not $line) { throw "missing $key in .env" }
    return $line.Matches[0].Groups[1].Value
}

function Get-HttpCode([string]$url) {
    $curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
    if (-not $curlExe) { return 'no-curl' }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $code = (& $curlExe -s -o NUL -w "%{http_code}" --max-time 5 $url 2>&1 | Out-String).Trim()
        return $code
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Test-ApiReady {
    return ((Get-HttpCode "$apiBase/api/health") -eq '200')
}

function Wait-ApiReady([int]$timeoutSeconds = 120) {
    $deadline = (Get-Date).AddSeconds($timeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (Test-ApiReady) { return $true }
        Start-Sleep -Seconds 1
    }
    return $false
}

function Wait-Http200([string]$url, [int]$timeoutSeconds = 90) {
    $deadline = (Get-Date).AddSeconds($timeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ((Get-HttpCode $url) -eq '200') { return $true }
        Start-Sleep -Seconds 2
    }
    return $false
}

function Invoke-Db([string]$sql) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = (& $psql -h localhost -p 5433 -U $script:dbUser -d $dbName -tA -F '|' -c $sql 2>&1 | Out-String).Trim()
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "psql failed (exit=$code): $($output.Substring(0, [Math]::Min(400, $output.Length)))" }
    return $output
}

function Invoke-ApiRaw {
    param(
        [string]$Method,
        [string]$Path,
        $Body = $null
    )
    $curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
    if (-not $curlExe) { throw 'curl.exe is required' }
    $bodyFile = Join-Path $work 'api-body.json'
    $responseFile = Join-Path $work 'api-response.json'
    if (Test-Path -LiteralPath $responseFile) { Remove-Item -LiteralPath $responseFile -Force }
    $curlArgs = @('-s', '-S', '-X', $Method, '-o', $responseFile, '-w', '%{http_code}', '--max-time', '120')
    if ($script:token -ne '') { $curlArgs += @('-H', "Authorization: Bearer $($script:token)") }
    if ($null -ne $Body) {
        $json = $Body | ConvertTo-Json -Depth 12 -Compress
        [System.IO.File]::WriteAllText($bodyFile, $json, (New-Object System.Text.UTF8Encoding($false)))
        $curlArgs += @('-H', 'Content-Type: application/json; charset=utf-8')
        $curlArgs += @('--data-binary', "@$bodyFile")
    }
    $curlArgs += "$apiBase$Path"
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $statusText = (& $curlExe @curlArgs 2>&1 | Out-String).Trim()
        $curlExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    $status = 0
    if ($statusText -match '([0-9]{3})\s*$') { $status = [int]$Matches[1] }
    $text = ''
    if (Test-Path -LiteralPath $responseFile) {
        $text = [System.IO.File]::ReadAllText($responseFile, (New-Object System.Text.UTF8Encoding($false)))
    }
    return [ordered]@{ Status = $status; Text = $text; CurlExit = $curlExit }
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body = $null
    )
    $raw = Invoke-ApiRaw -Method $Method -Path $Path -Body $Body
    if ($raw.CurlExit -ne 0 -or $raw.Status -lt 200 -or $raw.Status -ge 300) {
        $snippet = ''
        if (-not [string]::IsNullOrWhiteSpace($raw.Text)) {
            $snippet = $raw.Text.Substring(0, [Math]::Min(600, $raw.Text.Length))
        }
        throw "HTTP $Method $Path -> $($raw.Status) $snippet"
    }
    if ([string]::IsNullOrWhiteSpace($raw.Text)) { return $null }
    return ($raw.Text | ConvertFrom-Json)
}

function Get-ApiErrorInfo($raw) {
    $info = [ordered]@{ code = ''; message = ''; reason = '' }
    if ([string]::IsNullOrWhiteSpace($raw.Text)) { return $info }
    try {
        $parsed = $raw.Text | ConvertFrom-Json
        if ($null -ne $parsed.error) {
            $info.code = [string]$parsed.error.code
            $info.message = [string]$parsed.error.message
            if ($null -ne $parsed.error.details) {
                $info.reason = [string]$parsed.error.details.reason
            }
        }
    } catch {
        $info.message = $raw.Text.Substring(0, [Math]::Min(300, $raw.Text.Length))
    }
    return $info
}

function Save-ExecutorUrl {
    try {
        $current = Invoke-Api -Method 'GET' -Path '/api/admin/settings/execution.executor_url'
        $script:originalExecutorUrl = [string]$current.data.value
    } catch {
        $script:errors.Add("read execution.executor_url: $($_.Exception.Message)")
    }
}

function Restore-ExecutorUrl {
    if ($script:executorUrlRestored -or $null -eq $script:originalExecutorUrl) { return }
    $script:executorUrlRestored = $true
    try {
        $current = Invoke-Api -Method 'GET' -Path '/api/admin/settings/execution.executor_url'
        if ([string]$current.data.value -ne $script:originalExecutorUrl) {
            Invoke-Api -Method 'PATCH' -Path '/api/admin/settings/execution.executor_url' -Body @{ value = $script:originalExecutorUrl } | Out-Null
        }
    } catch {
        $script:errors.Add("restore execution.executor_url: $($_.Exception.Message)")
    }
}

function Stop-StartedServices {
    foreach ($item in @(
            @{ started = $script:apiStarted; port = $apiPort },
            @{ started = $script:webStarted; port = $webPort },
            @{ started = $script:execStarted; port = $execPort },
            @{ started = $script:proxyStarted; port = $proxyPort }
        )) {
        if (-not $item.started) { continue }
        $conns = Get-NetTCPConnection -State Listen -LocalPort $item.port -ErrorAction SilentlyContinue
        foreach ($conn in $conns) {
            Stop-Process -Id $conn.OwningProcess -Force -ErrorAction SilentlyContinue
        }
    }
}

function Get-ScreenshotFiles {
    if (-not (Test-Path -LiteralPath $issueDir)) { return @() }
    return @(Get-ChildItem -LiteralPath $issueDir -Filter '*.png' | Where-Object { $_.Length -gt 0 } | Sort-Object Name)
}

function Write-Verdict {
    $verdict.retestCaseRunIds = @($script:responseCaseRunIds)
    $verdict.responseCaseRunIds = @($script:responseCaseRunIds)
    $verdict.historyRows = [int]$script:historyRows
    $verdict.issueSource = $script:issueSource
    $verdict.issueStatus = $script:issueStatus
    $verdict.retestWaveStatus = $script:waveStatus
    $verdict.db.retestStatus = $script:dbRetestStatus
    $verdict.db.lastRetestWaveId = $script:dbLastWaveId
    $verdict.db.lastRetestCaseRunId = $script:dbLastCaseRunId
    $verdict.refs.rows = @($script:retestRefRows)
    $verdict.errors = @($script:errors)
    $verdict.startedAt = $script:startedAt.ToString('o')
    $verdict.generatedAt = (Get-Date -Format o)
    Write-Utf8 $issueVerdictPath (($verdict | ConvertTo-Json -Depth 12))
}

function Wait-WaveTerminal([string]$versionId, [string]$waveId) {
    $deadline = (Get-Date).AddSeconds($WaveWaitSeconds)
    $last = ''
    while ((Get-Date) -lt $deadline) {
        $wave = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/waves/$waveId"
        $last = [string]$wave.data.status
        Write-Host "== retest wave $waveId status=$last"
        if ($last -eq 'done' -or $last -eq 'failed' -or $last -eq 'cancelled') {
            return $last
        }
        Start-Sleep -Seconds 5
    }
    throw "retest wave $waveId did not reach a terminal status within $WaveWaitSeconds s (last=$last)"
}

function Wait-NoActiveWave([string]$versionId, [int]$timeoutSeconds = 180) {
    $deadline = (Get-Date).AddSeconds($timeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $overview = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/execution/overview"
        if ($null -eq $overview.data.active_wave) { return $true }
        Write-Host "== another wave is still active; waiting before the retest"
        Start-Sleep -Seconds 5
    }
    return $false
}

try {
    if (-not (Test-Path -LiteralPath $dataVerdictPath)) { throw "missing W2 data verdict: $dataVerdictPath" }
    $data = Read-JsonFile $dataVerdictPath
    if ([string]::IsNullOrWhiteSpace([string]$data.versionId)) { throw 'data-verdict.json has no versionId' }
    $versionId = [string]$data.versionId
    $verdict.versionId = $versionId

    $failedRunId = ''
    $failedIds = @($data.caseRunIds.failed)
    if ($failedIds.Count -gt 0) { $failedRunId = [string]$failedIds[0] }
    if ([string]::IsNullOrWhiteSpace($failedRunId)) { throw 'data-verdict.json has no failed case_run id' }
    $verdict.failedCaseRunId = $failedRunId

    Write-Host "== W2-6 issue/retest: version=$versionId failed_run=$failedRunId db=$dbName"

    if (-not (Test-Path -LiteralPath $psql)) { throw "psql missing: $psql" }

    $dbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
    $parsed = [uri]$dbUrl
    $script:dbUser = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[0])
    $dbPassword = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[1])
    $env:PGPASSWORD = $dbPassword
    $apiDbUrl = "postgres://$($script:dbUser)`:$dbPassword@localhost:5433/${dbName}?sslmode=disable"
    $adminPassword = Get-EnvValue 'EZTEST_ADMIN_PASSWORD'
    $execToken = Get-EnvValue 'EZTEST_EXECUTOR_TOKEN'
    $llmKey = Get-EnvValue 'llm_api_key'
    $llmBase = Get-EnvValue 'llm_base_url'
    $nodeExe = (Get-Command node -ErrorAction SilentlyContinue).Source
    if (-not $nodeExe) { throw 'node is required' }
    $curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
    if (-not $curlExe) { throw 'curl.exe is required' }

    # ---------------------------------------------------------------------
    # 1. SUT frontend (left running; localhost keeps Vite on IPv6).
    # ---------------------------------------------------------------------
    if ((Get-HttpCode $sutUrl) -ne '200') {
        if (-not (Test-Path -LiteralPath $sutDir)) {
            throw "SUT frontend is down and $sutDir is missing"
        }
        Write-Host "== starting SUT frontend in $sutDir"
        $sutCmd = Join-Path $work 'run-sut.cmd'
        $sutLines = @('@echo off', "cd /d `"$sutDir`"", "npm run dev >> `"$work\sut.log`" 2>&1")
        [System.IO.File]::WriteAllLines($sutCmd, $sutLines, (New-Object System.Text.UTF8Encoding($false)))
        $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
            CommandLine      = "cmd.exe /c `"$sutCmd`""
            CurrentDirectory = $work
        }
        if (-not (Wait-Http200 $sutUrl 180)) { throw 'SUT frontend did not become ready on localhost:5173' }
        $verdict.sutStarted = $true
    }

    # ---------------------------------------------------------------------
    # 2. LLM forward proxy on 13999 (the configured base URL keeps working).
    # ---------------------------------------------------------------------
    if ((Get-HttpCode "$proxyBase/healthz") -ne '200') {
        $proxyScript = Join-Path $work 'w2-issue-llm-proxy.mjs'
        $proxySource = @'
import http from "node:http"
const port = Number(process.env.FAKE_LLM_PORT || 13999)
const upstream = (process.env.FAKE_LLM_UPSTREAM || "").replace(/\/+$/, "")
const key = process.env.FAKE_LLM_API_KEY || ""
if (!upstream || !key) { console.error("missing upstream or key"); process.exit(2) }
const server = http.createServer(async (req, res) => {
  if (req.url === "/healthz") {
    res.writeHead(200, { "content-type": "application/json" })
    res.end('{"status":"ok"}')
    return
  }
  try {
    const chunks = []
    for await (const chunk of req) chunks.push(chunk)
    const body = Buffer.concat(chunks)
    const headers = { ...req.headers }
    delete headers.host
    delete headers.authorization
    headers.authorization = `Bearer ${key}`
    const response = await fetch(upstream + req.url, {
      method: req.method,
      headers,
      body: req.method === "GET" || req.method === "HEAD" ? undefined : body,
    })
    const buffer = Buffer.from(await response.arrayBuffer())
    const responseHeaders = {}
    for (const [name, value] of response.headers.entries()) {
      if (name === "content-encoding" || name === "content-length" || name === "transfer-encoding") continue
      responseHeaders[name] = value
    }
    res.writeHead(response.status, responseHeaders)
    res.end(buffer)
  } catch (error) {
    res.writeHead(502, { "content-type": "text/plain" })
    res.end(String(error))
  }
})
server.listen(port, "127.0.0.1", () => console.log(`[issue-llm-proxy] 127.0.0.1:${port} upstream=${upstream}`))
'@
        Write-Utf8 $proxyScript $proxySource
        $runProxy = Join-Path $work 'run-issue-proxy.cmd'
        $proxyLines = @(
            '@echo off',
            "cd /d `"$work`"",
            "set FAKE_LLM_PORT=$proxyPort",
            "set FAKE_LLM_UPSTREAM=$llmBase",
            "set FAKE_LLM_API_KEY=$llmKey",
            "`"$nodeExe`" `"$proxyScript`" >> `"$proxyLog`" 2>&1"
        )
        [System.IO.File]::WriteAllLines($runProxy, $proxyLines, (New-Object System.Text.UTF8Encoding($false)))
        Write-Host '== starting LLM forward proxy on 13999'
        $script:proxyStarted = $true
        $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
            CommandLine      = "cmd.exe /c `"$runProxy`""
            CurrentDirectory = $work
        }
        if (-not (Wait-Http200 "$proxyBase/healthz" 60)) { throw 'LLM proxy did not become ready on 13999' }
    } else {
        Write-Host '== reusing the LLM proxy already listening on 13999'
    }

    # ---------------------------------------------------------------------
    # 3. API on 13449: reuse a listening instance, otherwise build and start.
    # ---------------------------------------------------------------------
    if (-not (Test-ApiReady)) {
        if (-not $SkipBuild) {
            Write-Host '== building backend'
            Push-Location $backendDir
            try {
                & $goExe build -o $apiExe ./cmd/eztest 2>&1 | Tee-Object -FilePath (Join-Path $issueDir 'build.log')
                if ($LASTEXITCODE -ne 0) { throw 'go build ./cmd/eztest failed' }
            } finally { Pop-Location }
        }
        if (-not (Test-Path -LiteralPath $apiExe)) { throw "backend binary missing: $apiExe" }
        $runApi = Join-Path $work 'run-api-w26.cmd'
        $apiLines = @(
            '@echo off',
            "cd /d `"$backendDir`"",
            "set EZTEST_DATABASE_URL=$apiDbUrl",
            "set EZTEST_API_PORT=$apiPort",
            "set EZTEST_WEB_PORT=$webPort",
            "set EZTEST_EXECUTOR_PORT=$execPort",
            "set EZTEST_INITIAL_ADMIN_PASSWORD=$adminPassword",
            "set EZTEST_ADMIN_USERNAME=admin",
            "set EZTEST_SNOWFLAKE_NODE_ID=13",
            "`"$apiExe`" >> `"$apiLog`" 2>&1"
        )
        [System.IO.File]::WriteAllLines($runApi, $apiLines, (New-Object System.Text.UTF8Encoding($false)))
        Write-Host '== starting isolated API on port 13449'
        $script:apiStarted = $true
        $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
            CommandLine      = "cmd.exe /c `"$runApi`""
            CurrentDirectory = $work
        }
        if (-not (Wait-ApiReady 120)) { throw 'API did not become healthy on port 13449' }
    } else {
        Write-Host '== reusing the API already listening on port 13449'
    }

    # ---------------------------------------------------------------------
    # 4. Executor on 13352 (registers against the isolated API).
    # ---------------------------------------------------------------------
    if ((Get-HttpCode "$executorUrl/healthz") -ne '200') {
        $runExec = Join-Path $work 'run-executor-w26.cmd'
        $execLines = @(
            '@echo off',
            "cd /d `"$repoRoot\executor`"",
            "set EZTEST_EXECUTOR_TOKEN=$execToken",
            "set EZTEST_EXECUTOR_ADDR=127.0.0.1:$execPort",
            "set EZTEST_API_URL=$apiBase",
            "set EZTEST_EXECUTOR_NAME=w2-issue",
            "`"$nodeExe`" src/server.mjs >> `"$execLog`" 2>&1"
        )
        [System.IO.File]::WriteAllLines($runExec, $execLines, (New-Object System.Text.UTF8Encoding($false)))
        Write-Host '== starting executor on 13352'
        $script:execStarted = $true
        $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
            CommandLine      = "cmd.exe /c `"$runExec`""
            CurrentDirectory = $work
        }
        if (-not (Wait-Http200 "$executorUrl/healthz" 90)) { throw 'executor did not become healthy on 13352' }
    } else {
        Write-Host '== reusing the executor already listening on 13352'
    }

    # ---------------------------------------------------------------------
    # 5. Login and point the shared executor URL at the isolated executor.
    # ---------------------------------------------------------------------
    $session = Invoke-Api -Method 'POST' -Path '/api/auth/login' -Body @{ username = 'admin'; password = $adminPassword }
    if ($null -eq $session -or [string]::IsNullOrWhiteSpace([string]$session.data.token)) { throw 'login returned no token' }
    $script:token = [string]$session.data.token

    Save-ExecutorUrl
    $patched = Invoke-Api -Method 'PATCH' -Path '/api/admin/settings/execution.executor_url' -Body @{ value = $executorUrl }
    if ([string]$patched.data.value -ne $executorUrl) { throw "execution.executor_url did not accept $executorUrl" }
    Write-Host "== execution.executor_url -> $executorUrl"

    $execDeadline = (Get-Date).AddSeconds(60)
    $execActive = $false
    while ((Get-Date) -lt $execDeadline) {
        try {
            $list = Invoke-Api -Method 'GET' -Path '/api/admin/executors'
            foreach ($item in @($list.data)) {
                if ([string]$item.endpoint -eq $executorUrl -and [string]$item.status -eq 'active') {
                    $execActive = $true
                }
            }
        } catch {
            $script:errors.Add("list executors: $($_.Exception.Message)")
        }
        if ($execActive) { break }
        Start-Sleep -Seconds 2
    }
    if (-not $execActive) { throw "executor $executorUrl did not register as active" }
    Write-Host '== executor registered active'

    # ---------------------------------------------------------------------
    # 6. Resolve the issue linked to the failed base run.
    # ---------------------------------------------------------------------
    $issuesResp = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/issues?limit=100"
    $issues = @($issuesResp.data)
    $target = $null
    foreach ($issue in $issues) {
        $issueStatus = [string]$issue.status
        if ($issueStatus -ne 'open' -and $issueStatus -ne 'confirmed') { continue }
        $hasSourceRun = $false
        foreach ($ref in @($issue.refs)) {
            if ([string]$ref.kind -eq 'case_run' -and [string]$ref.ref_id -eq $failedRunId -and [string]$ref.role -ne 'retest') {
                $hasSourceRun = $true
            }
        }
        if ($hasSourceRun) { $target = $issue; break }
    }

    if ($null -eq $target) {
        Write-Host "== no retestable issue links run $failedRunId; creating a manual issue"
        $manualBody = [ordered]@{
            title       = "W2-6 manual issue for failed case run $failedRunId"
            summary     = 'Created by scripts/v022-w2-issue.ps1 to exercise the issue retest API.'
            category    = 'ui'
            severity    = 'major'
            case_run_id = $failedRunId
            reason      = 'W2-6 issue/retest domain script'
        }
        $createdRaw = Invoke-ApiRaw -Method 'POST' -Path "/api/versions/$versionId/issues" -Body $manualBody
        if ($createdRaw.CurlExit -ne 0 -or $createdRaw.Status -ne 201) {
            $info = Get-ApiErrorInfo $createdRaw
            throw "create manual issue failed: HTTP $($createdRaw.Status) $($info.code) $($info.message)"
        }
        $target = ($createdRaw.Text | ConvertFrom-Json).data
        $script:issueSource = 'manual-created'
    } else {
        $script:issueSource = [string]$target.source
    }

    $issueId = [string]$target.id
    if ([string]::IsNullOrWhiteSpace($issueId)) { throw 'resolved issue has no id' }
    $verdict.issueId = $issueId
    $script:issueStatus = [string]$target.status
    $verdict.issueStatus = $script:issueStatus
    Write-Host "== issue=$issueId status=$($script:issueStatus) source=$($script:issueSource)"

    # Re-runs: if a previous retest wave is still active, wait it out first.
    $existingRetest = [string]$target.retest_status
    $existingWave = ''
    if ($null -ne $target.last_retest_wave_id) { $existingWave = [string]$target.last_retest_wave_id }
    if ($existingRetest -eq 'retesting' -and $existingWave -ne '') {
        Write-Host "== existing retest wave $existingWave is still running; waiting"
        $null = Wait-WaveTerminal $versionId $existingWave
    }

    $detailBefore = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/issues/$issueId"
    $historyBefore = @($detailBefore.data.history).Count
    $refsBefore = @($detailBefore.data.refs).Count
    Write-Host "== issue history rows before=$historyBefore refs before=$refsBefore"

    $script:eventBaseline = [long](Invoke-Db "select coalesce(max(seq),0) from event where channel = 'version:$versionId'")
    Write-Host "== retest event baseline seq=$($script:eventBaseline)"

    # ---------------------------------------------------------------------
    # 7. Start the retest (empty selection retests every linked source run).
    # ---------------------------------------------------------------------
    $retestRaw = $null
    $retestAttempts = 0
    while ($true) {
        $retestAttempts = $retestAttempts + 1
        $retestRaw = Invoke-ApiRaw -Method 'POST' -Path "/api/versions/$versionId/issues/$issueId/retest" -Body @{}
        if ($retestRaw.CurlExit -eq 0 -and $retestRaw.Status -eq 200) { break }
        $info = Get-ApiErrorInfo $retestRaw
        if ($info.reason -eq 'retest_active' -and $retestAttempts -lt 4) {
            Write-Host "== retest rejected (attempt $retestAttempts): another wave is active"
            if (-not (Wait-NoActiveWave $versionId 180)) {
                throw 'another wave stayed active; the retest could not start'
            }
            continue
        }
        throw "start retest failed: HTTP $($retestRaw.Status) $($info.code)/$($info.reason) $($info.message)"
    }
    $retest = ($retestRaw.Text | ConvertFrom-Json).data
    $retestWaveId = [string]$retest.wave_id
    $responseIds = @($retest.case_run_ids | ForEach-Object { [string]$_ })
    $script:responseCaseRunIds = $responseIds
    $verdict.retestWaveId = $retestWaveId
    $verdict.retestStatus = [string]$retest.retest_status
    Write-Host "== retest started wave=$retestWaveId status=$($retest.retest_status) response_case_run_ids=$($responseIds -join ',')"
    if ([string]::IsNullOrWhiteSpace($retestWaveId)) { throw 'retest response has no wave_id' }
    if ($responseIds.Count -eq 0) { throw 'retest response has no case_run_ids' }
    if ($responseIds -contains $failedRunId) { throw "retest response echoed the source run $failedRunId" }

    # ---------------------------------------------------------------------
    # 8. Wait for the retest wave and the issue writeback.
    # ---------------------------------------------------------------------
    $script:waveStatus = Wait-WaveTerminal $versionId $retestWaveId
    Write-Host "== retest wave $retestWaveId terminal status=$($script:waveStatus)"

    $writebackDeadline = (Get-Date).AddSeconds(90)
    $issueAfter = $null
    $retestStatus = ''
    while ((Get-Date) -lt $writebackDeadline) {
        $issueAfter = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/issues/$issueId"
        $retestStatus = [string]$issueAfter.data.retest_status
        if ($retestStatus -eq 'passed' -or $retestStatus -eq 'failed') { break }
        Start-Sleep -Seconds 3
    }
    $verdict.retestStatus = $retestStatus
    Write-Host "== issue retest_status=$retestStatus"
    if ($retestStatus -ne 'passed' -and $retestStatus -ne 'failed') {
        throw "issue retest_status=$retestStatus after the terminal wave, want passed|failed"
    }

    $waveRunsResp = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/waves/$retestWaveId/case-runs?limit=100"
    $waveRuns = @()
    foreach ($run in @($waveRunsResp.data)) {
        $waveRuns += [ordered]@{
            id     = [string]$run.id
            status = [string]$run.status
            intent = [string]$run.intent_id
        }
    }
    $verdict.waveRuns = @($waveRuns)
    foreach ($runId in $responseIds) {
        $match = @($waveRuns | Where-Object { $_.id -eq $runId })
        if ($match.Count -eq 0) { throw "response case_run $runId is missing from retest wave $retestWaveId" }
    }

    # ---------------------------------------------------------------------
    # 9. DB verification: issue projection, refs, history and events.
    # ---------------------------------------------------------------------
    $issueRow = Invoke-Db ("select retest_status, coalesce(last_retest_wave_id::text,''), coalesce(last_retest_case_run_id::text,''), status, source from delivery_issue where id = $issueId and version_id = $versionId")
    $issueParts = @($issueRow -split '\|', 5)
    if ($issueParts.Count -lt 5) { throw "unexpected delivery_issue row: $issueRow" }
    $script:dbRetestStatus = $issueParts[0]
    $script:dbLastWaveId = $issueParts[1]
    $script:dbLastCaseRunId = $issueParts[2]
    $verdict.db.retestStatus = $script:dbRetestStatus
    $verdict.db.lastRetestWaveId = $script:dbLastWaveId
    $verdict.db.lastRetestCaseRunId = $script:dbLastCaseRunId
    $verdict.issueStatus = $issueParts[3]
    $verdict.issueSource = $issueParts[4]
    Write-Host "== db delivery_issue retest_status=$($script:dbRetestStatus) last_wave=$($script:dbLastWaveId) last_case_run=$($script:dbLastCaseRunId) status=$($issueParts[3]) source=$($issueParts[4])"

    if ($script:dbRetestStatus -ne $retestStatus) {
        throw "db retest_status=$($script:dbRetestStatus), API retest_status=$retestStatus"
    }
    if ($script:dbLastWaveId -ne $retestWaveId) {
        throw "db last_retest_wave_id=$($script:dbLastWaveId), retest wave=$retestWaveId"
    }
    if (-not ($responseIds -contains $script:dbLastCaseRunId)) {
        throw "db last_retest_case_run_id=$($script:dbLastCaseRunId) is not in response ids $($responseIds -join ',')"
    }

    $refRaw = Invoke-Db ("select kind, ref_id, role, coalesce(version_sut_id::text,'') from delivery_issue_ref where issue_id = $issueId order by id")
    $allRefs = @()
    $retestRefs = @()
    foreach ($line in @($refRaw -split "`r?`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = @($line -split '\|', 4)
        if ($parts.Count -lt 4) { continue }
        $entry = [ordered]@{ kind = $parts[0]; ref_id = $parts[1]; role = $parts[2]; version_sut_id = $parts[3] }
        $allRefs += $entry
        if ($parts[2] -eq 'retest') { $retestRefs += $entry }
    }
    $script:retestRefRows = @($retestRefs)
    $retestWaveRefs = @($retestRefs | Where-Object { $_.kind -eq 'wave' })
    $retestCaseRunRefs = @($retestRefs | Where-Object { $_.kind -eq 'case_run' })
    $verdict.refs.total = $allRefs.Count
    $verdict.refs.retest = $retestRefs.Count
    $verdict.refs.retestWave = $retestWaveRefs.Count
    $verdict.refs.retestCaseRun = $retestCaseRunRefs.Count
    Write-Host "== db refs total=$($allRefs.Count) retest=$($retestRefs.Count) wave=$($retestWaveRefs.Count) case_run=$($retestCaseRunRefs.Count)"

    $waveRef = @($retestWaveRefs | Where-Object { $_.ref_id -eq $retestWaveId })
    if ($waveRef.Count -lt 1) { throw "role='retest' wave ref $retestWaveId is missing" }
    foreach ($runId in $responseIds) {
        $linked = @($retestCaseRunRefs | Where-Object { $_.ref_id -eq $runId })
        if ($linked.Count -lt 1) { throw "role='retest' case_run ref $runId is missing" }
    }
    if ($retestRefs.Count -lt 2) { throw "role='retest' refs=$($retestRefs.Count), want >= 2" }

    $historyRaw = Invoke-Db ("select coalesce(from_status,'null'), to_status, replace(coalesce(reason,''), '|', '/'), actor_kind from delivery_issue_status_history where issue_id = $issueId order by id")
    $history = @()
    foreach ($line in @($historyRaw -split "`r?`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = @($line -split '\|', 4)
        if ($parts.Count -lt 4) { continue }
        $history += [ordered]@{ from_status = $parts[0]; to_status = $parts[1]; reason = $parts[2]; actor_kind = $parts[3] }
    }
    $script:historyRows = $history.Count
    $verdict.historyRows = $script:historyRows
    $verdict.history = @($history)
    Write-Host "== db issue history rows=$($script:historyRows)"
    if ($script:historyRows -lt 1) { throw 'issue has no status history rows' }

    $eventRaw = Invoke-Db ("select seq, type, coalesce(envelope->'payload'->>'issue_id',''), coalesce(envelope->'payload'->>'version_id',''), coalesce(envelope->'payload'->>'wave_id',''), coalesce(envelope->'payload'->>'retest_status',''), coalesce(envelope->'payload'->>'case_run_ids',''), coalesce(envelope->'payload'->>'error','') from event where channel = 'version:$versionId' and seq > $($script:eventBaseline) and type like 'issue.retest_%' order by seq")
    $eventRows = @()
    foreach ($line in @($eventRaw -split "`r?`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = @($line -split '\|', 8)
        if ($parts.Count -lt 8) { continue }
        $eventRows += [ordered]@{
            seq           = $parts[0]
            type          = $parts[1]
            issue_id      = $parts[2]
            version_id    = $parts[3]
            wave_id       = $parts[4]
            retest_status = $parts[5]
            case_run_ids  = $parts[6]
            error         = $parts[7]
        }
    }
    $verdict.eventRows = @($eventRows)
    $startedEvents = @($eventRows | Where-Object { $_.type -eq 'issue.retest_started' })
    $completedEvents = @($eventRows | Where-Object { $_.type -eq 'issue.retest_completed' })
    $failedEvents = @($eventRows | Where-Object { $_.type -eq 'issue.retest_failed' })
    $terminalEvents = @($eventRows | Where-Object { $_.type -eq 'issue.retest_completed' -or $_.type -eq 'issue.retest_failed' })
    $verdict.events.started = $startedEvents.Count
    $verdict.events.completed = $completedEvents.Count
    $verdict.events.failed = $failedEvents.Count
    $verdict.events.terminal = $terminalEvents.Count
    Write-Host "== issue.retest events started=$($startedEvents.Count) completed=$($completedEvents.Count) failed=$($failedEvents.Count)"

    if ($startedEvents.Count -lt 1) { throw 'issue.retest_started event is missing' }
    if ($terminalEvents.Count -lt 1) { throw 'issue.retest_completed/issue.retest_failed event is missing' }
    foreach ($event in $startedEvents) {
        if ($event.issue_id -ne $issueId) { throw "retest_started issue_id=$($event.issue_id), want $issueId" }
        if ($event.version_id -ne $versionId) { throw "retest_started version_id=$($event.version_id), want $versionId" }
        if ($event.wave_id -ne $retestWaveId) { throw "retest_started wave_id=$($event.wave_id), want $retestWaveId" }
        if ($event.retest_status -ne 'retesting') { throw "retest_started retest_status=$($event.retest_status), want retesting" }
        if ($event.error -ne '') { throw "retest_started error='$($event.error)', want empty" }
        foreach ($runId in $responseIds) {
            if ($event.case_run_ids -notmatch [regex]::Escape($runId)) {
                throw "retest_started case_run_ids=$($event.case_run_ids) is missing response id $runId"
            }
        }
    }
    foreach ($event in $terminalEvents) {
        if ($event.issue_id -ne $issueId) { throw "terminal event issue_id=$($event.issue_id), want $issueId" }
        if ($event.wave_id -ne $retestWaveId) { throw "terminal event wave_id=$($event.wave_id), want $retestWaveId" }
        if ($event.retest_status -ne $retestStatus) { throw "terminal event retest_status=$($event.retest_status), want $retestStatus" }
        if ($retestStatus -eq 'failed' -and [string]::IsNullOrWhiteSpace($event.error)) {
            throw 'issue.retest_failed event carries no error text'
        }
    }

    # Response ids are the new retest runs and every one is linked in the DB.
    foreach ($runId in $responseIds) {
        $inRefs = @($retestCaseRunRefs | Where-Object { $_.ref_id -eq $runId })
        $inWave = @($waveRuns | Where-Object { $_.id -eq $runId })
        $inEvent = $false
        foreach ($event in $startedEvents) {
            if ($event.case_run_ids -match [regex]::Escape($runId)) { $inEvent = $true }
        }
        if ($inRefs.Count -lt 1 -or $inWave.Count -lt 1 -or -not $inEvent) {
            throw "response case_run $runId is not linked consistently (refs=$($inRefs.Count) wave=$($inWave.Count) event=$inEvent)"
        }
    }

    # ---------------------------------------------------------------------
    # 10. Optional screenshots of the issue center / detail / retest run.
    # ---------------------------------------------------------------------
    if (-not $SkipScreenshots) {
        try {
            $webUrlForShot = $webUrl
            if ((Get-HttpCode "$webUrl/login") -ne '200') {
                $runWeb = Join-Path $work 'run-web-w26.cmd'
                $webLines = @(
                    '@echo off',
                    "cd /d `"$repoRoot\frontend`"",
                    "set NEXT_PUBLIC_API_BASE_URL=$apiBase",
                    "`"$nodeExe`" `"$repoRoot\frontend\node_modules\next\dist\bin\next`" dev -p $webPort >> `"$webLog`" 2>&1"
                )
                [System.IO.File]::WriteAllLines($runWeb, $webLines, (New-Object System.Text.UTF8Encoding($false)))
                Write-Host '== starting EzTest web on 6873 for screenshots'
                $script:webStarted = $true
                $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
                    CommandLine      = "cmd.exe /c `"$runWeb`""
                    CurrentDirectory = $work
                }
            }
            $webDeadline = (Get-Date).AddSeconds(240)
            while ((Get-Date) -lt $webDeadline -and (Get-HttpCode "$webUrlForShot/login") -ne '200') {
                Start-Sleep -Seconds 2
            }
            if ((Get-HttpCode "$webUrlForShot/login") -eq '200') {
                $firstRunId = if ($responseIds.Count -gt 0) { [string]$responseIds[0] } else { '' }
                $uiScript = @'
import path from "node:path"
import { createRequire } from "node:module"

const repo = process.env.W26_REPO
const web = process.env.W26_WEB
const versionId = process.env.W26_VERSION_ID
const issueId = process.env.W26_ISSUE_ID
const waveId = process.env.W26_WAVE_ID
const caseRunId = process.env.W26_CASE_RUN_ID
const out = process.env.W26_OUT
const adminPassword = process.env.W26_ADMIN_PW
const requireFromExecutor = createRequire(path.join(repo, "executor", "package.json"))
const { chromium } = requireFromExecutor("playwright")

let browser
try {
  browser = await chromium.launch({ headless: true, channel: "chrome" })
} catch {
  browser = await chromium.launch({ headless: true, channel: "msedge" })
}
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
page.setDefaultTimeout(60000)

async function shot(name) {
  await page.screenshot({ path: path.join(out, name), fullPage: true })
  console.log(`screenshot ${name}`)
}

try {
  await page.goto(`${web}/login`, { waitUntil: "domcontentloaded" })
  await page.waitForTimeout(800)
  if (page.url().includes("/login")) {
    await page.fill("#username", "admin")
    await page.fill("#password", adminPassword)
    await page.locator("button[type=submit]").click()
    await page.waitForURL(/\/versions/, { timeout: 90000 })
  }
  await page.goto(`${web}/versions/${versionId}?tab=issues`, { waitUntil: "domcontentloaded" })
  await page.waitForSelector('[data-testid="issues-tab"]', { timeout: 120000 })
  await page.waitForSelector('[data-testid^="issue-retest-"]', { timeout: 120000 })
  await page.waitForTimeout(1200)
  await shot("01-issue-center.png")
  await page.goto(`${web}/versions/${versionId}?tab=issues&issue=${issueId}`, { waitUntil: "domcontentloaded" })
  await page.waitForSelector('[data-testid="issue-detail-sheet"]', { timeout: 120000 })
  await page.waitForSelector('[data-testid="issue-retest"]', { timeout: 120000 })
  await page.waitForTimeout(1200)
  await shot("02-issue-retest-detail.png")
  if (waveId && caseRunId) {
    await page.goto(`${web}/versions/${versionId}?tab=execution&wave=${waveId}&case_run=${caseRunId}`, { waitUntil: "domcontentloaded" })
    await page.waitForSelector('[data-testid="case-run-detail"]', { timeout: 120000 })
    await page.waitForTimeout(1200)
    await shot("03-retest-run.png")
  }
} finally {
  await browser.close()
}
'@
                Write-Utf8 $uiScriptPath $uiScript
                $env:W26_REPO = $repoRoot
                $env:W26_WEB = $webUrlForShot
                $env:W26_VERSION_ID = $versionId
                $env:W26_ISSUE_ID = $issueId
                $env:W26_WAVE_ID = $retestWaveId
                $env:W26_CASE_RUN_ID = $firstRunId
                $env:W26_OUT = $issueDir
                $env:W26_ADMIN_PW = $adminPassword
                Write-Host '== capturing issue/retest screenshots'
                & $nodeExe $uiScriptPath 2>&1 | Tee-Object -FilePath (Join-Path $issueDir 'ui.log')
                if ($LASTEXITCODE -ne 0) { $script:errors.Add('screenshot helper failed (optional)') }
            } else {
                $script:errors.Add('web UI did not become ready; screenshots skipped')
            }
        } catch {
            $script:errors.Add("screenshots skipped: $($_.Exception.Message)")
            Write-Host "== screenshots skipped: $($_.Exception.Message)"
        }
    }

    $verdict.screenshots = @(Get-ScreenshotFiles | ForEach-Object { "issue/$($_.Name)" })
    Write-Host "== screenshots: $($verdict.screenshots -join ', ')"

    # ---------------------------------------------------------------------
    # 11. Final PASS gate.
    # ---------------------------------------------------------------------
    $issueCount = @($issues | Where-Object { [string]$_.id -eq $issueId }).Count
    if ($issueCount -lt 1) { throw "issue $issueId is not in the version issue list" }
    if ($script:waveStatus -ne 'done') { throw "retest wave status=$($script:waveStatus), want done" }
    if ($retestStatus -ne 'passed' -and $retestStatus -ne 'failed') { throw "retest_status=$retestStatus, want passed|failed" }
    if ($retestRefs.Count -lt 2) { throw "retest refs=$($retestRefs.Count), want >= 2" }
    if ($script:historyRows -lt 1) { throw 'history rows=0, want >= 1' }
    if (($startedEvents.Count + $terminalEvents.Count) -lt 1) { throw 'retest events=0, want >= 1' }
    if ($responseIds.Count -eq 0) { throw 'response case_run_ids is empty' }

    $verdict.status = 'PASS'
    Write-Host "== W2-6 issue/retest PASS issue=$issueId wave=$retestWaveId retest_status=$retestStatus response_ids=$($responseIds -join ',') refs=$($retestRefs.Count)"
} catch {
    $script:errors.Add($_.Exception.Message)
    Write-Host "ERROR: $($_.Exception.Message)"
} finally {
    Restore-ExecutorUrl
    Stop-StartedServices
    Write-Verdict
    Write-Host "== verdict: $issueVerdictPath"
}

if ($verdict.status -ne 'PASS') {
    Write-Host '== W2-6 issue/retest FAIL'
    Write-Host ($verdict.errors -join ' | ')
    exit 1
}
exit 0
