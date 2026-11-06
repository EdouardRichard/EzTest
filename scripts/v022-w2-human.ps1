#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W2-3 human_request domain script.

.DESCRIPTION
    Reuses the W2 base data created by scripts/v022-w2-data.ps1 (fresh database
    eztest_v022_w2, version from run/data-verdict.json) and exercises the
    approval / blocked_recovery / conflict decision flow through the product
    API only:

      1. start the real API on port 13445 when it is not already listening
         (the database is never dropped or recreated);
      2. block the version with an "account" blocker, create one
         blocked_recovery request and approve it (the approval unblocks the
         version through the stage gate);
      3. create one approval request and reject it with a reason;
      4. create one conflict request and approve it;
      5. set question.timeout_seconds to its minimum (60s), create one approval
         and wait for the leader-gated expiry worker to set it 'expired';
      6. verify the human_request rows / response JSONB and the
         approval.requested / approval.decided events (read-only SQL + API);
      7. capture 2-3 screenshots of the approval card / todo surface.

    Evidence (untracked) is written next to the W2 base evidence:
      docs/version-V0.22/acceptance/evidence/w2/run/human-verdict.json
      docs/version-V0.22/acceptance/evidence/w2/run/human/*.png
      docs/version-V0.22/acceptance/evidence/w2/run/human/db-evidence.json

    PASS requires: all three decision kinds answered, at least one request
    expired, at least three approval.requested and three approval.decided
    events, and at least two non-empty screenshots.

    Everything this script starts is stopped at the end. A pre-existing API or
    web dev server is reused and left running.

.PARAMETER SkipBuild
    Reuse .tmp/w2-e2e/eztest.exe instead of rebuilding the current HEAD binary.

.PARAMETER ExpiryTimeoutSeconds
    Upper bound for waiting on the leader-gated expiry sweep (default 200s).
#>
param(
    [switch]$SkipBuild,
    [int]$ExpiryTimeoutSeconds = 200
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

# Native tools (psql/curl) write UTF-8; PowerShell 5.1 decodes native stdout with
# the console code page by default, which mangles CJK reasons on a zh-CN host.
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

$dbName = 'eztest_v022_w2'
$apiPort = 13445
$webPort = 6871
$execPort = 13349
$apiBase = "http://127.0.0.1:$apiPort"
$webUrl = "http://127.0.0.1:$webPort"
$work = Join-Path $repoRoot '.tmp\w2-e2e'
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2\run'
$humanDir = Join-Path $evidenceRoot 'human'
$dataVerdictPath = Join-Path $evidenceRoot 'data-verdict.json'
$humanVerdictPath = Join-Path $evidenceRoot 'human-verdict.json'
$dbEvidencePath = Join-Path $humanDir 'db-evidence.json'
$uiScriptPath = Join-Path $work 'w2-human-ui.mjs'
$apiExe = Join-Path $work 'eztest.exe'
$apiLog = Join-Path $work 'api.log'
$webLog = Join-Path $work 'web.log'
$psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
$goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'

New-Item -ItemType Directory -Force -Path $work, $humanDir | Out-Null

$script:token = ''
$script:apiStarted = $false
$script:webStarted = $false
$script:timeoutRestored = $false
$script:originalTimeout = $null
$script:errors = New-Object System.Collections.Generic.List[string]
$script:startedAt = Get-Date

# The verdict is written even when the flow fails, so a later reviewer can see
# exactly which request/verification failed.
$verdict = [ordered]@{
    status        = 'FAIL'
    db            = $dbName
    versionId     = $null
    approvalId    = $null
    blockedId     = $null
    conflictId    = $null
    expiredId     = $null
    decided       = [ordered]@{}
    events        = [ordered]@{ requested = 0; decided = 0 }
    expiredCount  = 0
    screenshots   = @()
    stage         = [ordered]@{ before = $null; blocked = $null; after = $null }
    timeoutSetting = [ordered]@{ key = 'question.timeout_seconds'; configured = $null; used = $null; restored = $false }
    apiBase       = $apiBase
    webUrl        = $webUrl
    generatedAt   = (Get-Date -Format o)
    errors        = @()
}

# UTF-8 without BOM: the verdict carries CJK reasons from the API.
function Write-Utf8([string]$path, [string]$text) {
    [System.IO.File]::WriteAllText($path, $text, (New-Object System.Text.UTF8Encoding($false)))
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

function Invoke-Db([string]$sql) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = (& $psql -h localhost -p 5433 -U eztest -d $dbName -tAc $sql 2>&1 | Out-String).Trim()
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "psql failed (exit=$code): $($output.Substring(0, [Math]::Min(400, $output.Length)))" }
    return $output
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body = $null
    )
    # curl + explicit UTF-8 file I/O: Invoke-WebRequest in Windows PowerShell 5.1
    # decodes a JSON response without a charset as ISO-8859-1 and corrupts CJK.
    $curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
    if (-not $curlExe) { throw 'curl.exe is required' }
    $bodyFile = Join-Path $work 'api-body.json'
    $responseFile = Join-Path $work 'api-response.json'
    if (Test-Path -LiteralPath $responseFile) { Remove-Item -LiteralPath $responseFile -Force }
    $curlArgs = @('-s', '-S', '-X', $Method, '-o', $responseFile, '-w', '%{http_code}', '--max-time', '60')
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
    if ($curlExit -ne 0 -or $status -lt 200 -or $status -ge 300) {
        throw "HTTP $Method $Path -> $status $($text.Substring(0, [Math]::Min(600, $text.Length)))"
    }
    if ([string]::IsNullOrWhiteSpace($text)) { return $null }
    return ($text | ConvertFrom-Json)
}

function Stop-StartedServices {
    foreach ($item in @(
            @{ started = $script:apiStarted; port = $apiPort },
            @{ started = $script:webStarted; port = $webPort }
        )) {
        if (-not $item.started) { continue }
        $conns = Get-NetTCPConnection -State Listen -LocalPort $item.port -ErrorAction SilentlyContinue
        foreach ($conn in $conns) {
            Stop-Process -Id $conn.OwningProcess -Force -ErrorAction SilentlyContinue
        }
    }
}

function Restore-TimeoutSetting {
    if ($script:timeoutRestored) { return }
    $script:timeoutRestored = $true
    if ($null -eq $script:originalTimeout) { return }
    try {
        $current = Invoke-Api -Method 'GET' -Path '/api/admin/settings/question.timeout_seconds'
        if ($null -ne $current -and $current.data.value -ne $script:originalTimeout) {
            Invoke-Api -Method 'PATCH' -Path '/api/admin/settings/question.timeout_seconds' -Body @{ value = $script:originalTimeout } | Out-Null
        }
        $verdict.timeoutSetting.restored = $true
    } catch {
        $script:errors.Add("restore question.timeout_seconds: $($_.Exception.Message)")
    }
}

function Get-ScreenshotFiles {
    $files = @()
    if (Test-Path -LiteralPath $humanDir) {
        $files = @(Get-ChildItem -LiteralPath $humanDir -Filter '*.png' | Where-Object { $_.Length -gt 0 })
    }
    return $files
}

function Write-Verdict {
    $verdict.errors = @($script:errors)
    $verdict.generatedAt = (Get-Date -Format o)
    Write-Utf8 $humanVerdictPath (($verdict | ConvertTo-Json -Depth 10))
}

try {
    if (-not (Test-Path -LiteralPath $dataVerdictPath)) { throw "missing W2 data verdict: $dataVerdictPath" }
    $dataText = [System.IO.File]::ReadAllText($dataVerdictPath, (New-Object System.Text.UTF8Encoding($false)))
    $data = $dataText | ConvertFrom-Json
    if ($null -eq $data.versionId -or [string]::IsNullOrWhiteSpace([string]$data.versionId)) {
        throw 'data-verdict.json has no versionId'
    }
    $versionId = [string]$data.versionId
    $verdict.versionId = $versionId
    Write-Host "== W2-3 human domain: version=$versionId db=$dbName"

    if (-not (Test-Path -LiteralPath $psql)) { throw "psql missing: $psql" }

    # Credentials come from .env; the W2 database name is the only override.
    $dbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
    $parsed = [uri]$dbUrl
    $dbUser = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[0])
    $dbPassword = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[1])
    $env:PGPASSWORD = $dbPassword
    $apiDbUrl = "postgres://$dbUser`:$dbPassword@localhost:5433/${dbName}?sslmode=disable"
    $adminPassword = Get-EnvValue 'EZTEST_ADMIN_PASSWORD'

    # ---------------------------------------------------------------------
    # 1. API: reuse a listening instance, otherwise build and start one.
    # ---------------------------------------------------------------------
    if (-not (Test-ApiReady)) {
        if (-not $SkipBuild) {
            Write-Host '== building backend'
            Push-Location (Join-Path $repoRoot 'backend')
            try {
                & $goExe build -o $apiExe ./cmd/eztest 2>&1 | Tee-Object -FilePath (Join-Path $humanDir 'build.log')
                if ($LASTEXITCODE -ne 0) { throw 'go build ./cmd/eztest failed' }
            } finally { Pop-Location }
        }
        if (-not (Test-Path -LiteralPath $apiExe)) { throw "backend binary missing: $apiExe" }
        $runApi = Join-Path $work 'run-api-w23.cmd'
        $apiLines = @(
            '@echo off',
            "cd /d `"$repoRoot\backend`"",
            "set EZTEST_DATABASE_URL=$apiDbUrl",
            "set EZTEST_API_PORT=$apiPort",
            "set EZTEST_WEB_PORT=$webPort",
            "set EZTEST_EXECUTOR_PORT=$execPort",
            "set EZTEST_STORAGE_DIR=$work\storage",
            "set EZTEST_INITIAL_ADMIN_PASSWORD=$adminPassword",
            "set EZTEST_ADMIN_USERNAME=admin",
            "set EZTEST_SNOWFLAKE_NODE_ID=7",
            "`"$apiExe`" >> `"$apiLog`" 2>&1"
        )
        [System.IO.File]::WriteAllLines($runApi, $apiLines, (New-Object System.Text.UTF8Encoding($false)))
        Write-Host '== starting API on port 13445'
        $script:apiStarted = $true
        $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
            CommandLine      = "cmd.exe /c `"$runApi`""
            CurrentDirectory = $work
        }
        if (-not (Wait-ApiReady 120)) { throw 'API did not become healthy on port 13445' }
    } else {
        Write-Host '== reusing the API already listening on port 13445'
    }

    # ---------------------------------------------------------------------
    # 2. Login and confirm the base version/stage.
    # ---------------------------------------------------------------------
    $session = Invoke-Api -Method 'POST' -Path '/api/auth/login' -Body @{ username = 'admin'; password = $adminPassword }
    if ($null -eq $session -or [string]::IsNullOrWhiteSpace([string]$session.data.token)) { throw 'login returned no token' }
    $script:token = [string]$session.data.token

    $version = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId"
    if ($null -eq $version -or [string]$version.data.id -ne $versionId) { throw "base version $versionId not readable" }
    $stage = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/stage"
    $originalStage = [string]$stage.data.stage
    $verdict.stage.before = $originalStage
    Write-Host "== base stage=$originalStage"

    # Start the web UI early so it can compile while the API flow runs. The SUT
    # frontend (5173) is not touched; this is the EzTest UI on port 6871.
    if ((Get-HttpCode "$webUrl/login") -ne '200') {
        $runWeb = Join-Path $work 'run-web.cmd'
        if (-not (Test-Path -LiteralPath $runWeb)) {
            $nodeExe = (Get-Command node -ErrorAction SilentlyContinue).Source
            if (-not $nodeExe) { throw 'node is missing (required to start the web UI)' }
            $webLines = @(
                '@echo off',
                "cd /d `"$repoRoot\frontend`"",
                "set NEXT_PUBLIC_API_BASE_URL=$apiBase",
                "`"$nodeExe`" `"$repoRoot\frontend\node_modules\next\dist\bin\next`" dev -p $webPort >> `"$webLog`" 2>&1"
            )
            [System.IO.File]::WriteAllLines($runWeb, $webLines, (New-Object System.Text.UTF8Encoding($false)))
        }
        Write-Host '== starting EzTest web on port 6871'
        $script:webStarted = $true
        $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
            CommandLine      = "cmd.exe /c `"$runWeb`""
            CurrentDirectory = $work
        }
    }

    # ---------------------------------------------------------------------
    # 3. Block the version (account blocker) and create the three requests.
    # ---------------------------------------------------------------------
    $blocker = @{
        kind   = 'account'
        reason = 'W2-3 blocked recovery drill: verify the test account before resuming'
        action = 'Approve the blocked recovery request after the account is restored'
        next   = 'Resume the recorded executing stage'
    }
    $blockedStage = Invoke-Api -Method 'POST' -Path "/api/versions/$versionId/stage/block" -Body $blocker
    $verdict.stage.blocked = [string]$blockedStage.data.stage
    if ($verdict.stage.blocked -ne 'blocked') { throw "stage/block returned stage=$($verdict.stage.blocked)" }
    Write-Host '== version blocked'

    $approvalReason = 'W2-3 approval: confirm the release can proceed after the acceptance review'
    $blockedReason = 'W2-3 blocked recovery: the account was restored; approve to unblock the version'
    $conflictReason = 'W2-3 conflict: two persisted facts disagree about the refund window'

    $approval = Invoke-Api -Method 'POST' -Path "/api/versions/$versionId/human-requests" -Body @{
        kind = 'approval'; reason = $approvalReason
    }
    $verdict.approvalId = [string]$approval.data.id

    $blockedRequest = Invoke-Api -Method 'POST' -Path "/api/versions/$versionId/human-requests" -Body @{
        kind = 'blocked_recovery'; reason = $blockedReason
    }
    $verdict.blockedId = [string]$blockedRequest.data.id

    $conflict = Invoke-Api -Method 'POST' -Path "/api/versions/$versionId/human-requests" -Body @{
        kind = 'conflict'; reason = $conflictReason
    }
    $verdict.conflictId = [string]$conflict.data.id
    Write-Host "== created approval=$($verdict.approvalId) blocked=$($verdict.blockedId) conflict=$($verdict.conflictId)"

    $pending = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/human-requests?status=pending"
    $pendingIds = @($pending.data | ForEach-Object { [string]$_.id })
    foreach ($id in @($verdict.approvalId, $verdict.blockedId, $verdict.conflictId)) {
        if (-not ($pendingIds -contains $id)) { throw "created request $id is missing from the pending list" }
    }

    # ---------------------------------------------------------------------
    # 4. Screenshots of the pending approval card / todo (before decisions).
    # ---------------------------------------------------------------------
    $nodeExe = (Get-Command node -ErrorAction SilentlyContinue).Source
    if (-not $nodeExe) { throw 'node is missing (required for the screenshot helper)' }
    $uiScript = @'
import path from "node:path"
import { createRequire } from "node:module"

const repo = process.env.W23_REPO
const web = process.env.W23_WEB
const versionId = process.env.W23_VERSION_ID
const out = process.env.W23_OUT
const adminPassword = process.env.W23_ADMIN_PW
const requireFromExecutor = createRequire(path.join(repo, "executor", "package.json"))
const { chromium } = requireFromExecutor("playwright")

let browser
try {
  browser = await chromium.launch({ headless: true, channel: "chrome" })
} catch {
  browser = await chromium.launch({ headless: true, channel: "msedge" })
}
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
page.setDefaultTimeout(45000)

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
    await page.waitForURL(/\/versions/, { timeout: 60000 })
  }
  await page.goto(`${web}/versions/${versionId}?tab=overview`, { waitUntil: "domcontentloaded" })
  await page.waitForSelector('[data-testid="pending-human-decisions"]', { timeout: 90000 })
  await page.waitForSelector('[data-testid^="human-decision-card-"]', { timeout: 30000 })
  await page.waitForTimeout(1000)
  await shot("01-version-overview-decision-cards.png")

  await page.goto(`${web}/versions/${versionId}?tab=execution`, { waitUntil: "domcontentloaded" })
  await page.waitForSelector('[data-testid^="human-decision-card-"]', { timeout: 90000 })
  await page.waitForTimeout(1000)
  await shot("02-execution-decision-cards.png")

  const todo = page.locator('[data-testid="todo-drawer-trigger"]')
  if (await todo.count()) {
    await todo.click()
    await page.waitForSelector('[data-testid="todo-drawer"]', { timeout: 30000 })
    await page.waitForTimeout(1000)
    await shot("03-todo-human-requests.png")
  } else {
    console.log("todo trigger not found; skipping todo screenshot")
  }
} finally {
  await browser.close()
}
'@
    Write-Utf8 $uiScriptPath $uiScript
    $env:W23_REPO = $repoRoot
    $env:W23_WEB = $webUrl
    $env:W23_VERSION_ID = $versionId
    $env:W23_OUT = $humanDir
    $env:W23_ADMIN_PW = $adminPassword
    $webDeadline = (Get-Date).AddSeconds(180)
    while ((Get-Date) -lt $webDeadline -and (Get-HttpCode "$webUrl/login") -ne '200') {
        Start-Sleep -Seconds 2
    }
    Write-Host '== capturing UI screenshots'
    & $nodeExe $uiScriptPath 2>&1 | Tee-Object -FilePath (Join-Path $humanDir 'ui.log')
    if ($LASTEXITCODE -ne 0) { throw 'screenshot helper failed' }
    $verdict.screenshots = @(Get-ScreenshotFiles | ForEach-Object { "human/$($_.Name)" })
    Write-Host "== screenshots: $($verdict.screenshots -join ', ')"

    # ---------------------------------------------------------------------
    # 5. Decisions: reject approval, approve blocked_recovery, approve conflict.
    # ---------------------------------------------------------------------
    $decided = Invoke-Api -Method 'POST' -Path "/api/human-requests/$($verdict.approvalId)/resolve" -Body @{
        response = @{ decision = 'rejected'; reason = 'W2-3 acceptance: rejected with a recorded reason' }
    }
    $verdict.decided.approval = [ordered]@{
        id = $verdict.approvalId; status = [string]$decided.data.status
        decision = [string]$decided.data.response.decision; reason = [string]$decided.data.response.reason
    }

    $unblocked = Invoke-Api -Method 'POST' -Path "/api/human-requests/$($verdict.blockedId)/resolve" -Body @{
        response = @{ decision = 'approved'; reason = 'W2-3 acceptance: account verified, resume the version' }
    }
    $verdict.decided.blocked = [ordered]@{
        id = $verdict.blockedId; status = [string]$unblocked.data.status
        decision = [string]$unblocked.data.response.decision; reason = [string]$unblocked.data.response.reason
    }

    $resolvedConflict = Invoke-Api -Method 'POST' -Path "/api/human-requests/$($verdict.conflictId)/resolve" -Body @{
        response = @{ decision = 'approved'; reason = 'W2-3 acceptance: conflict decision recorded' }
    }
    $verdict.decided.conflict = [ordered]@{
        id = $verdict.conflictId; status = [string]$resolvedConflict.data.status
        decision = [string]$resolvedConflict.data.response.decision; reason = [string]$resolvedConflict.data.response.reason
    }
    Write-Host '== three decision requests resolved'

    $afterStage = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/stage"
    $verdict.stage.after = [string]$afterStage.data.stage
    if ($verdict.stage.after -ne $originalStage) {
        throw "blocked_recovery approval did not restore the stage (want $originalStage, got $($verdict.stage.after))"
    }

    # ---------------------------------------------------------------------
    # 6. Timeout expiry: question.timeout_seconds minimum, wait for the sweep.
    # ---------------------------------------------------------------------
    $setting = Invoke-Api -Method 'GET' -Path '/api/admin/settings/question.timeout_seconds'
    $script:originalTimeout = $setting.data.value
    $verdict.timeoutSetting.configured = $script:originalTimeout
    $usedTimeout = 60
    if ([int]$script:originalTimeout -lt $usedTimeout) { $usedTimeout = [int]$script:originalTimeout }
    $patched = Invoke-Api -Method 'PATCH' -Path '/api/admin/settings/question.timeout_seconds' -Body @{ value = $usedTimeout }
    if ([int]$patched.data.value -ne $usedTimeout) { throw "question.timeout_seconds did not accept $usedTimeout (got $($patched.data.value))" }
    $verdict.timeoutSetting.used = $usedTimeout
    Write-Host "== question.timeout_seconds=$usedTimeout"

    $expiry = Invoke-Api -Method 'POST' -Path "/api/versions/$versionId/human-requests" -Body @{
        kind = 'approval'; reason = 'W2-3 expiry: this approval must expire through the leader-gated sweep'
    }
    $verdict.expiredId = [string]$expiry.data.id
    Write-Host "== created expiry candidate $($verdict.expiredId), waiting for the expiry worker"

    $expiryDeadline = (Get-Date).AddSeconds($ExpiryTimeoutSeconds)
    $expired = $false
    while ((Get-Date) -lt $expiryDeadline) {
        $status = Invoke-Db "select status from human_request where id = $($verdict.expiredId)"
        if ($status -eq 'expired') { $expired = $true; break }
        Start-Sleep -Seconds 5
    }
    if (-not $expired) {
        throw "expiry candidate $($verdict.expiredId) did not reach status=expired within ${ExpiryTimeoutSeconds}s"
    }
    Write-Host '== expiry candidate expired'

    # ---------------------------------------------------------------------
    # 7. DB / API / event verification (read-only SQL).
    # ---------------------------------------------------------------------
    $idList = @($verdict.approvalId, $verdict.blockedId, $verdict.conflictId, $verdict.expiredId) |
        Where-Object { -not [string]::IsNullOrWhiteSpace($_) } |
        ForEach-Object { "'$($_)'" }
    $idCsv = $idList -join ','

    $rows = @()
    $rawRows = Invoke-Db "select id, kind, status, coalesce(response->>'decision',''), coalesce(response->>'reason','') from human_request where id in ($idCsv) order by id"
    foreach ($line in @($rawRows -split "\r?\n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line.Split('|')
        $rows += [ordered]@{
            id = $parts[0]; kind = $parts[1]; status = $parts[2]
            decision = $(if ($parts.Count -gt 3) { $parts[3] } else { '' })
            reason = $(if ($parts.Count -gt 4) { $parts[4] } else { '' })
        }
    }

    $requestedCount = [int](Invoke-Db "select count(*) from event where channel = 'version:$versionId' and type = 'approval.requested' and (envelope->'scope'->>'human_request_id') in ($idCsv)")
    $decidedCount = [int](Invoke-Db "select count(*) from event where channel = 'version:$versionId' and type = 'approval.decided' and (envelope->'scope'->>'human_request_id') in ($idCsv)")
    $verdict.events.requested = $requestedCount
    $verdict.events.decided = $decidedCount

    $expiredCount = [int](Invoke-Db "select count(*) from human_request where id in ($idCsv) and status = 'expired'")
    $verdict.expiredCount = $expiredCount

    # API response JSONB verification: the three answered requests must expose
    # the recorded decision/reason through the list projection.
    $answered = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/human-requests?status=answered"
    $answeredById = @{}
    foreach ($item in @($answered.data)) { $answeredById[[string]$item.id] = $item }

    $dbEvidence = [ordered]@{
        generatedAt = (Get-Date -Format o)
        versionId   = $versionId
        rows        = $rows
        events      = [ordered]@{ requested = $requestedCount; decided = $decidedCount }
        expiredCount = $expiredCount
    }
    Write-Utf8 $dbEvidencePath (($dbEvidence | ConvertTo-Json -Depth 8))

    $decisionKeyById = @{}
    $decisionKeyById[[string]$verdict.approvalId] = 'approval'
    $decisionKeyById[[string]$verdict.blockedId] = 'blocked'
    $decisionKeyById[[string]$verdict.conflictId] = 'conflict'
    foreach ($id in @($verdict.approvalId, $verdict.blockedId, $verdict.conflictId)) {
        $row = $rows | Where-Object { $_.id -eq $id } | Select-Object -First 1
        if ($null -eq $row -or $row.status -ne 'answered') { throw "human_request $id is not answered in the DB" }
        $expectedDecision = [string]$verdict.decided[$decisionKeyById[[string]$id]].decision
        if ($row.decision -ne $expectedDecision) {
            throw "human_request $id decision mismatch (db=$($row.decision) expected=$expectedDecision)"
        }
        if (-not $answeredById.ContainsKey($id)) { throw "human_request $id is missing from the answered API list" }
        $apiRow = $answeredById[$id]
        if ([string]$apiRow.response.decision -ne $row.decision) { throw "human_request $id API decision mismatch" }
        if ([string]$apiRow.response.reason -ne $row.reason) { throw "human_request $id API reason mismatch" }
    }

    $expiredRow = $rows | Where-Object { $_.id -eq $verdict.expiredId } | Select-Object -First 1
    if ($null -eq $expiredRow -or $expiredRow.status -ne 'expired') { throw "human_request $($verdict.expiredId) is not expired" }
    if ($expiredCount -lt 1) { throw 'no decision request expired' }
    if ($requestedCount -lt 3) { throw "approval.requested events=$requestedCount, want >= 3" }
    if ($decidedCount -lt 3) { throw "approval.decided events=$decidedCount, want >= 3" }
    if (@($verdict.screenshots).Count -lt 2) { throw "screenshots=$(@($verdict.screenshots).Count), want >= 2" }

    # All three v0.22 decision kinds were answered.
    $kinds = @($rows | ForEach-Object { $_.kind } | Sort-Object -Unique)
    foreach ($kind in @('approval', 'blocked_recovery', 'conflict')) {
        if (-not ($kinds -contains $kind)) { throw "decision kind $kind was not answered" }
    }

    $verdict.status = 'PASS'
    Write-Host '== W2-3 human domain PASS'
    Write-Host "== approval=$($verdict.approvalId) blocked=$($verdict.blockedId) conflict=$($verdict.conflictId) expired=$($verdict.expiredId)"
    Write-Host "== events requested=$requestedCount decided=$decidedCount expiredCount=$expiredCount screenshots=$(@($verdict.screenshots).Count)"
} catch {
    $script:errors.Add($_.Exception.Message)
    Write-Host "ERROR: $($_.Exception.Message)"
} finally {
    Restore-TimeoutSetting
    Stop-StartedServices
    Write-Verdict
    Write-Host "== verdict: $humanVerdictPath"
}

if ($verdict.status -ne 'PASS') {
    Write-Host "== W2-3 human domain FAIL"
    Write-Host ($verdict.errors -join ' | ')
    exit 1
}
exit 0
