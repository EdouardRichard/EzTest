#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W2-14 eval/quality domain script.

.DESCRIPTION
    Reuses the W2 base data created by scripts/v022-w2-data.ps1 (fresh database
    eztest_v022_w2) and exercises the snapshot eval + trends flow through the
    product API/UI only:

      1. start an isolated API on port 13451 (the database is never dropped or
         recreated, so the parallel W2-13 exploration run is unaffected);
      2. read the version id from run/data-verdict.json (and
         run/exploration-verdict.json when the W2-13 run finished);
      3. trigger two mode=snapshot eval runs for that version through
         POST /api/quality/eval-runs {mode:"snapshot",version_id};
      4. capture the QualityPage in a headless Playwright browser (selected
         version, trend chart and the UI trigger result) under run/eval/*.png
         and trigger one more snapshot run through the real UI;
      5. verify eval_run (mode/status/version_id) and the four eval_metric rows
         (design_coverage, execution_pass_rate, case_coverage,
         visual_classification_accuracy) in PostgreSQL;
      6. verify the run-scoped gates: runsAfter == runsBefore + triggered runs,
         every triggered run_id appears exactly once in every metric trend
         series, and each eval.started/completed event payload carries the new
         eval_run_id (deltas, not version-level stale totals);
      7. stop and restart the API on the same database, then re-read the eval
         run/trend/DB counters and require byte-identical persistence;
      8. write docs/version-V0.22/acceptance/evidence/w2/run/eval-verdict.json.

    PASS requires: evalRun >= 1, 4 metrics, trend points >= 2, run-scoped gates,
    eval.* event payloads, restart.equal and non-empty screenshots.

.PARAMETER SkipBuild
    Reuse .tmp/w2-eval/eztest.exe instead of rebuilding the current HEAD binary.
#>
param(
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

$dbName = 'eztest_v022_w2'
$apiPort = 13451
$apiBase = "http://127.0.0.1:$apiPort"
$webPort = 13452
$webBase = "http://127.0.0.1:$webPort"
$work = Join-Path $repoRoot '.tmp\w2-eval'
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2\run'
$evalDir = Join-Path $evidenceRoot 'eval'
$dataVerdictPath = Join-Path $evidenceRoot 'data-verdict.json'
$explorationVerdictPath = Join-Path $evidenceRoot 'exploration-verdict.json'
$evalVerdictPath = Join-Path $evidenceRoot 'eval-verdict.json'
$uiFlowPath = Join-Path $work 'eval-ui.mjs'
$uiVerdictPath = Join-Path $evalDir 'ui.json'
$webStamp = Get-Date -Format 'yyyyMMddHHmmss'
$webRoot = Join-Path $work "frontend-$webStamp"
$apiExe = Join-Path $work 'eztest.exe'
$apiLog = Join-Path $work 'api.log'
$webLog = Join-Path $work 'web.log'
$psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
$goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'
$metricNames = @('design_coverage', 'execution_pass_rate', 'case_coverage', 'visual_classification_accuracy')

New-Item -ItemType Directory -Force -Path $work | Out-Null
New-Item -ItemType Directory -Force -Path $evalDir | Out-Null

$script:token = ''
$script:apiStarted = $false
$script:webStarted = $false
$script:built = $false
$script:errors = New-Object System.Collections.Generic.List[string]

$verdict = [ordered]@{
    status      = 'FAIL'
    db          = $dbName
    versionId   = $null
    dataSource  = 'base'
    evalRunId   = $null
    evalRunIds  = @()
    statusValue = $null
    metrics     = [ordered]@{}
    trendPoints = 0
    trendSeries = [ordered]@{}
    events      = [ordered]@{ started = 0; completed = 0; failed = 0; total = 0 }
    runScoped   = [ordered]@{}
    screenshots = @()
    route       = ''
    webUrl      = $webBase
    testid      = 'quality-trends'
    dbCounters  = [ordered]@{}
    restart     = [ordered]@{ equal = $false }
    apiBase     = $apiBase
    generatedAt = (Get-Date -Format o)
    errors      = @()
}

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

function Wait-HttpReady([string]$url, [int]$timeoutSeconds = 120) {
    $deadline = (Get-Date).AddSeconds($timeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ((Get-HttpCode $url) -eq '200') { return $true }
        Start-Sleep -Seconds 1
    }
    return $false
}

function Get-PortOwner([int]$port) {
    $conn = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $conn) { return $null }
    return [int]$conn.OwningProcess
}

function Stop-Port([int]$port) {
    $conns = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue
    foreach ($conn in $conns) {
        Stop-Process -Id $conn.OwningProcess -Force -ErrorAction SilentlyContinue
    }
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        if ($null -eq (Get-PortOwner $port)) { return }
        Start-Sleep -Milliseconds 250
    }
    throw "port $port still has a listener after stop"
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
    if ($curlExit -ne 0 -or $status -lt 200 -or $status -ge 300) {
        throw "HTTP $Method $Path -> $status $($text.Substring(0, [Math]::Min(600, $text.Length)))"
    }
    if ([string]::IsNullOrWhiteSpace($text)) { return $null }
    return ($text | ConvertFrom-Json)
}

function Start-Api {
    if ((Get-HttpCode "$apiBase/api/health") -eq '200') { return }
    if (-not $SkipBuild -and -not $script:built) {
        Write-Host '== building backend'
        Push-Location (Join-Path $repoRoot 'backend')
        try {
            & $goExe build -o $apiExe ./cmd/eztest 2>&1 | Tee-Object -FilePath (Join-Path $work 'build.log')
            if ($LASTEXITCODE -ne 0) { throw 'go build ./cmd/eztest failed' }
            $script:built = $true
        } finally { Pop-Location }
    }
    if (-not (Test-Path -LiteralPath $apiExe)) { throw "backend binary missing: $apiExe" }
    $runApi = Join-Path $work 'run-api-w214.cmd'
    $apiLines = @(
        '@echo off',
        "cd /d `"$repoRoot\backend`"",
        "set EZTEST_DATABASE_URL=$script:apiDbUrl",
        "set EZTEST_API_PORT=$apiPort",
        "set EZTEST_WEB_PORT=$webPort",
        "set EZTEST_STORAGE_DIR=$work\storage",
        "set EZTEST_INITIAL_ADMIN_PASSWORD=$script:adminPassword",
        "set EZTEST_ADMIN_USERNAME=admin",
        "set EZTEST_SNOWFLAKE_NODE_ID=11",
        "`"$apiExe`" >> `"$apiLog`" 2>&1"
    )
    [System.IO.File]::WriteAllLines($runApi, $apiLines, (New-Object System.Text.UTF8Encoding($false)))
    Write-Host "== starting API on port $apiPort"
    $script:apiStarted = $true
    $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
        CommandLine      = "cmd.exe /c `"$runApi`""
        CurrentDirectory = $work
    }
    if (-not (Wait-HttpReady "$apiBase/api/health" 120)) { throw "API did not become healthy on port $apiPort" }
}

function Stop-Api {
    Stop-Port $apiPort
    Start-Sleep -Milliseconds 500
}

function Initialize-WebTree {
    # Next 16 refuses to start a second `next dev` in the same project directory
    # (the parallel W2-13 exploration run owns frontend/.next/dev). Run a
    # throwaway copy of the app sources with its own .next directory and a
    # junction to the installed node_modules, so the two acceptance lanes never
    # share the dev lock.
    if (Test-Path -LiteralPath $webRoot) { throw "web tree already exists: $webRoot" }
    New-Item -ItemType Directory -Force -Path $webRoot | Out-Null
    Copy-Item -LiteralPath (Join-Path $repoRoot 'frontend\src') -Destination (Join-Path $webRoot 'src') -Recurse
    foreach ($name in @('package.json', 'next.config.ts', 'postcss.config.mjs', 'tsconfig.json', 'next-env.d.ts')) {
        Copy-Item -LiteralPath (Join-Path $repoRoot (Join-Path 'frontend' $name)) -Destination (Join-Path $webRoot $name)
    }
    # Turbopack refuses to compile packages resolved outside its root; the
    # node_modules junction points back into the repository, so pin the root to
    # the repository instead of the throwaway copy.
    $configPath = Join-Path $webRoot 'next.config.ts'
    $config = [System.IO.File]::ReadAllText($configPath, (New-Object System.Text.UTF8Encoding($false)))
    $anchor = 'const nextConfig: NextConfig = {'
    if (-not $config.Contains($anchor)) { throw "unexpected next.config.ts shape: missing '$anchor'" }
    $config = $config.Replace($anchor, $anchor + "`n  turbopack: { root: process.env.W214_TURBOPACK_ROOT || process.cwd() },")
    Write-Utf8 $configPath $config
    $nodeModules = Join-Path $repoRoot 'frontend\node_modules'
    if (-not (Test-Path -LiteralPath $nodeModules)) { throw "frontend node_modules missing: $nodeModules" }
    New-Item -ItemType Junction -Path (Join-Path $webRoot 'node_modules') -Target $nodeModules | Out-Null
}

function Start-Web {
    if ((Get-HttpCode "$webBase/login") -eq '200') { return }
    $nodeExe = (Get-Command node -ErrorAction SilentlyContinue).Source
    if (-not $nodeExe) { throw 'node is not on PATH (required for the Playwright UI evidence)' }
    $nextBin = Join-Path $webRoot 'node_modules\next\dist\bin\next'
    if (-not (Test-Path -LiteralPath $nextBin)) { throw "next binary missing: $nextBin" }
    $runWeb = Join-Path $work 'run-web-w214.cmd'
    $webLines = @(
        '@echo off',
        "cd /d `"$webRoot`"",
        "set NEXT_PUBLIC_API_BASE_URL=$apiBase",
        'set NEXT_TELEMETRY_DISABLED=1',
        "set W214_TURBOPACK_ROOT=$repoRoot",
        "`"$nodeExe`" `"$nextBin`" dev -p $webPort >> `"$webLog`" 2>&1"
    )
    [System.IO.File]::WriteAllLines($runWeb, $webLines, (New-Object System.Text.UTF8Encoding($false)))
    Write-Host "== starting web on port $webPort from $webRoot"
    $script:webStarted = $true
    $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
        CommandLine      = "cmd.exe /c `"$runWeb`""
        CurrentDirectory = $work
    }
    if (-not (Wait-HttpReady "$webBase/login" 300)) { throw "web did not become ready on port $webPort (see $webLog)" }
}

function Stop-Web {
    Stop-Port $webPort
    Start-Sleep -Milliseconds 500
}

function Read-JsonVerdict([string]$path) {
    if (-not (Test-Path -LiteralPath $path)) { return $null }
    try {
        $text = [System.IO.File]::ReadAllText($path, (New-Object System.Text.UTF8Encoding($false)))
        if ([string]::IsNullOrWhiteSpace($text)) { return $null }
        return ($text | ConvertFrom-Json)
    } catch { return $null }
}

function Get-EventCounts([string]$versionId) {
    $rows = Invoke-Db "select type || '=' || count(*) from event where channel = 'version:$versionId' and type like 'eval.%' group by type order by type"
    $counts = [ordered]@{ started = 0; completed = 0; failed = 0; total = 0 }
    foreach ($row in @($rows -split "`n")) { $row = $row.Trim();
        if ($row -match '^eval\.([a-z]+)=([0-9]+)$') {
            $counts[$Matches[1]] = [int]$Matches[2]
            $counts.total += [int]$Matches[2]
        }
    }
    return $counts
}

function Get-EventRunPayloadCount([string]$versionId, [string]$type, [string]$runId) {
    return [int](Invoke-Db "select count(*) from event where channel = 'version:$versionId' and type = '$type' and envelope->'payload'->>'eval_run_id' = '$runId'")
}

function Read-Trends([string]$versionId) {
    $series = [ordered]@{}
    foreach ($name in $metricNames) {
        $response = Invoke-Api -Method 'GET' -Path "/api/quality/trends?version_id=$versionId&metric=$name"
        $series[$name] = @($response.data)
    }
    return $series
}

function Get-DbCounters([string]$versionId) {
    $runIdsRaw = Invoke-Db "select id from eval_run where version_id = $versionId order by id"
    $runIds = @($runIdsRaw -split "`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne '' })
    return [ordered]@{
        evalRunTotal      = [int](Invoke-Db "select count(*) from eval_run")
        evalRunVersion    = [int](Invoke-Db "select count(*) from eval_run where version_id = $versionId")
        evalMetricVersion = [int](Invoke-Db "select count(*) from eval_metric m join eval_run r on r.id = m.run_id where r.version_id = $versionId")
        evalRunIds        = $runIds
        eventStarted      = [int](Invoke-Db "select count(*) from event where channel = 'version:$versionId' and type = 'eval.started'")
        eventCompleted    = [int](Invoke-Db "select count(*) from event where channel = 'version:$versionId' and type = 'eval.completed'")
        eventEvalTotal    = [int](Invoke-Db "select count(*) from event where channel = 'version:$versionId' and type like 'eval.%'")
    }
}

function Login([string]$password) {
    $session = Invoke-Api -Method 'POST' -Path '/api/auth/login' -Body @{ username = 'admin'; password = $password }
    if ($null -eq $session -or [string]::IsNullOrWhiteSpace([string]$session.data.token)) { throw 'login returned no token' }
    $script:token = [string]$session.data.token
}

function Get-TrendsSignature($trends) {
    return ($trends | ConvertTo-Json -Depth 12 -Compress)
}

function Write-Verdict {
    $verdict.errors = @($script:errors)
    $verdict.generatedAt = (Get-Date -Format o)
    Write-Utf8 $evalVerdictPath (($verdict | ConvertTo-Json -Depth 14))
    Write-Host "== wrote $evalVerdictPath"
}

# ---------------------------------------------------------------------------
# Playwright UI evidence flow. The selected version, the trend chart and the UI
# trigger result are captured as real screenshots; the trigger also creates one
# more snapshot run, which the run-scoped gates below count (number triggered).
# ---------------------------------------------------------------------------
$uiFlowScript = @'
import fs from "node:fs"
import path from "node:path"
import { createRequire } from "node:module"

const API = process.env.W214_API
const WEB = process.env.W214_WEB
const VERSION = process.env.W214_VERSION
const USER = process.env.W214_USER || "admin"
const PASSWORD = process.env.W214_PASSWORD
const SHOTS = process.env.W214_SHOTS
const ROUTE = process.env.W214_ROUTE
const REPO_ROOT = process.env.W214_REPO_ROOT
const OUT = process.env.W214_UI_JSON

const requireFromExecutor = createRequire(path.join(REPO_ROOT, "executor", "package.json"))
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

function write(value) {
  fs.writeFileSync(OUT, JSON.stringify(value, null, 2), "utf8")
}
function log(message) {
  console.log(new Date().toISOString(), message)
}

async function main() {
  fs.mkdirSync(SHOTS, { recursive: true })
  const { chromium } = requireFromExecutor("playwright")
  let browser
  try {
    browser = await chromium.launch({ headless: true, channel: "chrome" })
  } catch (err) {
    log("chrome channel unavailable, falling back to msedge: " + String(err.message || err).split("\n")[0])
    browser = await chromium.launch({ headless: true, channel: "msedge" })
  }
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
    page.setDefaultTimeout(180000)
    await page.goto(WEB + "/login", { waitUntil: "domcontentloaded" })
    await sleep(1200)
    if (page.url().includes("/login")) {
      await page.fill("#username", USER)
      await page.fill("#password", PASSWORD)
      await page.locator("button[type=submit]").click()
      await page.waitForURL(/\/versions/, { timeout: 120000 })
    }
    log("logged in as " + USER)

    await page.goto(WEB + ROUTE, { waitUntil: "domcontentloaded" })
    await page.waitForSelector('[data-testid="quality-page"]', { timeout: 180000 })
    // The version select is the first <select> in the header; the trend metric
    // select renders later, so the first one is the version scope.
    const select = page.locator("select").first()
    await select.waitFor({ state: "visible", timeout: 120000 })
    const selected = await select.inputValue()
    if (selected !== VERSION) {
      throw new Error("selected version = " + selected + ", want " + VERSION)
    }

    const trigger = page.locator('[data-testid="quality-eval-trigger"]').first()
    await trigger.waitFor({ state: "visible", timeout: 120000 })
    await trigger.click()
    const result = page.locator('[data-testid="quality-eval-trigger-result"]').first()
    await result.waitFor({ state: "visible", timeout: 180000 })
    const triggerText = (await result.innerText()).trim()
    const matched = triggerText.match(/#(\d+)/)
    if (!matched) throw new Error("trigger result has no run id: " + triggerText)
    const runId = matched[1]
    log("UI snapshot run " + runId + ": " + triggerText)

    // The chart must show the new run, so the screenshot is not a stale series.
    await page.locator('[data-testid="quality-trend-point-' + runId + '"]').first()
      .waitFor({ state: "visible", timeout: 180000 })
    await page.locator('[data-testid="quality-trend-chart"]').first()
      .waitFor({ state: "visible", timeout: 120000 })
    const bars = await page.locator('[data-testid="quality-trend-chart"] [data-trend-bar]').count()
    if (bars < 1) throw new Error("trend chart has no bars")

    const trends = page.locator('[data-testid="quality-trends"]').first()
    await trends.scrollIntoViewIfNeeded()
    await sleep(700)

    const shots = []
    const pageFile = path.join(SHOTS, "01-quality-page.png")
    await page.screenshot({ path: pageFile, fullPage: true })
    shots.push("eval/" + path.basename(pageFile))
    const trendFile = path.join(SHOTS, "02-quality-trends.png")
    await trends.screenshot({ path: trendFile })
    shots.push("eval/" + path.basename(trendFile))
    const resultFile = path.join(SHOTS, "03-quality-trigger-result.png")
    await result.screenshot({ path: resultFile })
    shots.push("eval/" + path.basename(resultFile))

    for (const shot of shots) {
      const full = path.join(SHOTS, path.basename(shot))
      if (!fs.existsSync(full) || fs.statSync(full).size <= 0) {
        throw new Error("empty screenshot " + shot)
      }
    }

    write({
      ok: true,
      runId,
      route: ROUTE,
      webUrl: WEB,
      testid: "quality-trends",
      selectedVersion: selected,
      trendBars: bars,
      triggerText,
      screenshots: shots,
      apiBase: API,
      generatedAt: new Date().toISOString(),
    })
    log("captured " + shots.join(", "))
  } finally {
    await browser.close()
  }
}

main().then(
  () => process.exit(0),
  (err) => {
    try {
      write({ ok: false, error: String((err && err.message) || err) })
    } catch (_) {
      // The verdict file is best effort on failure.
    }
    console.error(err)
    process.exit(1)
  },
)
'@

try {
    if (-not (Test-Path -LiteralPath $dataVerdictPath)) { throw "missing W2 data verdict: $dataVerdictPath" }
    $data = Read-JsonVerdict $dataVerdictPath
    if ($null -eq $data -or [string]::IsNullOrWhiteSpace([string]$data.versionId)) {
        throw 'data-verdict.json has no versionId'
    }
    $baseVersionId = [string]$data.versionId
    $script:versionId = $baseVersionId
    $verdict.versionId = $baseVersionId
    $verdict.dataSource = 'base'

    if (-not (Test-Path -LiteralPath $psql)) { throw "psql missing: $psql" }

    $dbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
    $parsed = [uri]$dbUrl
    $dbUser = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[0])
    $dbPassword = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[1])
    $env:PGPASSWORD = $dbPassword
    $script:apiDbUrl = "postgres://$dbUser`:$dbPassword@localhost:5433/${dbName}?sslmode=disable"
    $script:adminPassword = Get-EnvValue 'EZTEST_ADMIN_PASSWORD'

    # This lane owns ports 13451/13452: a listener left by an aborted run must not
    # be reused (it may predate the current binary/source).
    foreach ($port in @($apiPort, $webPort)) {
        if ($null -ne (Get-PortOwner $port)) {
            Write-Host "== stopping stale listener on lane port $port"
            Stop-Port $port
        }
    }

    Start-Api
    Login $script:adminPassword

    # W2-13 exploration may add cases/observations to the same version. Prefer
    # its version only when the file is complete, readable through the API and
    # the exploration verdict passed; otherwise record that the base data was
    # used instead of blocking.
    $exploration = Read-JsonVerdict $explorationVerdictPath
    if ($null -ne $exploration -and [string]::IsNullOrWhiteSpace([string]$exploration.versionId) -eq $false) {
        $candidate = [string]$exploration.versionId
        try {
            $explored = Invoke-Api -Method 'GET' -Path "/api/versions/$candidate"
            if ($null -ne $explored -and [string]$explored.data.id -eq $candidate) {
                $script:versionId = $candidate
                $verdict.versionId = $candidate
                $verdict.dataSource = 'exploration'
                Write-Host "== using W2-13 exploration version $candidate"
            }
        } catch {
            $script:errors.Add("exploration version $candidate is not readable; falling back to base data: $($_.Exception.Message)")
        }
    }
    if ($verdict.dataSource -ne 'exploration') {
        Write-Host "== W2-13 exploration verdict not ready; using base version $baseVersionId"
    }

    $version = Invoke-Api -Method 'GET' -Path "/api/versions/$($script:versionId)"
    if ($null -eq $version -or [string]$version.data.id -ne $script:versionId) {
        throw "base version $($script:versionId) not readable"
    }

    $runsBefore = [int](Invoke-Db "select count(*) from eval_run where version_id = $($script:versionId)")
    $eventsBefore = Get-EventCounts $script:versionId

    # Two API snapshot triggers: the trend series needs several points and the
    # second run exercises the warn-only previous-snapshot baseline.
    $triggered = New-Object System.Collections.Generic.List[string]
    foreach ($round in 1, 2) {
        $created = Invoke-Api -Method 'POST' -Path '/api/quality/eval-runs' -Body @{
            mode = 'snapshot'
            version_id = $script:versionId
        }
        $run = $created.data
        if ($null -eq $run -or [string]::IsNullOrWhiteSpace([string]$run.id)) {
            throw "snapshot trigger $round returned no run"
        }
        if ([string]$run.mode -ne 'snapshot' -or [string]$run.version_id -ne $script:versionId) {
            throw "snapshot trigger $round scope = $($run.mode)/$($run.version_id)"
        }
        $triggered.Add([string]$run.id)
        Write-Host "== snapshot eval run $($run.id) status=$($run.status) metrics=$(@($run.metrics).Count)"
        Start-Sleep -Milliseconds 150
    }

    # UI screenshot evidence: real Playwright capture of the selected version,
    # the trend chart and the trigger result. Its click is a third snapshot run.
    Write-Host '== capturing QualityPage screenshots through Playwright'
    Write-Utf8 $uiFlowPath $uiFlowScript
    $env:W214_API = $apiBase
    $env:W214_WEB = $webBase
    $env:W214_VERSION = $script:versionId
    $env:W214_USER = 'admin'
    $env:W214_PASSWORD = $script:adminPassword
    $env:W214_SHOTS = $evalDir
    $env:W214_ROUTE = "/quality?version=$($script:versionId)"
    $env:W214_REPO_ROOT = $repoRoot
    $env:W214_UI_JSON = $uiVerdictPath
    Initialize-WebTree
    Start-Web
    $nodeExe = (Get-Command node -ErrorAction SilentlyContinue).Source
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        & $nodeExe $uiFlowPath 2>&1 | Tee-Object -FilePath (Join-Path $evalDir 'ui.log')
        $uiExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    $ui = Read-JsonVerdict $uiVerdictPath
    if ($uiExit -ne 0) {
        $uiReason = 'no ui.json verdict'
        if ($null -ne $ui -and -not [string]::IsNullOrWhiteSpace([string]$ui.error)) { $uiReason = [string]$ui.error }
        throw "Playwright UI evidence failed: $uiReason"
    }
    if ($null -eq $ui -or -not [bool]$ui.ok) { throw 'Playwright UI evidence wrote no ok verdict' }
    if ([string]$ui.selectedVersion -ne $script:versionId) {
        throw "UI selected version = $($ui.selectedVersion), want $($script:versionId)"
    }
    $uiRunId = [string]$ui.runId
    if ([string]::IsNullOrWhiteSpace($uiRunId)) { throw 'UI trigger produced no run id' }
    if (-not $triggered.Contains($uiRunId)) { $triggered.Add($uiRunId) }
    $verdict.screenshots = @($ui.screenshots)
    $verdict.route = [string]$ui.route
    $verdict.webUrl = [string]$ui.webUrl
    $verdict.testid = [string]$ui.testid
    if (@($verdict.screenshots).Count -lt 1) { throw 'UI verification produced no screenshots' }
    foreach ($shot in @($verdict.screenshots)) {
        $shotPath = Join-Path $evidenceRoot ([string]$shot)
        if (-not (Test-Path -LiteralPath $shotPath)) { throw "missing screenshot $shot" }
        if ((Get-Item -LiteralPath $shotPath).Length -le 0) { throw "empty screenshot $shot" }
    }
    if ($triggered.Count -lt 1) { throw 'no snapshot run was triggered' }

    $verdict.evalRunIds = @($triggered)
    $verdict.evalRunId = $triggered[$triggered.Count - 1]

    # ------------------------------------------------------------------
    # DB verification: eval_run row (mode/status/version_id) + 4 metrics.
    # ------------------------------------------------------------------
    $latestRunId = [string]$verdict.evalRunId
    $dbRun = Invoke-Db "select mode || '|' || status || '|' || coalesce(version_id::text,'') from eval_run where id = $latestRunId"
    $parts = $dbRun -split '\|'
    if ($parts.Count -lt 3) { throw "eval_run $latestRunId not found" }
    $verdict.statusValue = $parts[1]
    if ($parts[0] -ne 'snapshot') { throw "eval_run mode = $($parts[0])" }
    if ($parts[2] -ne $script:versionId) { throw "eval_run version_id = $($parts[2])" }
    if ($parts[1] -ne 'passed' -and $parts[1] -ne 'failed') { throw "eval_run status = $($parts[1])" }

    $metricRows = Invoke-Db "select name || '|' || value::float8 from eval_metric where run_id = $latestRunId order by name"
    $metricMap = [ordered]@{}
    foreach ($row in @($metricRows -split "`n")) { $row = $row.Trim();
        if ($row -match '^([a-z_]+)\|(.+)$') { $metricMap[$Matches[1]] = [double]$Matches[2] }
    }
    $verdict.metrics = $metricMap
    foreach ($name in $metricNames) {
        if (-not $metricMap.Contains($name)) { throw "missing eval metric: $name" }
    }
    if ($metricMap.Count -ne 4) { throw "eval metrics = $($metricMap.Count), want 4" }

    # ------------------------------------------------------------------
    # Run-scoped gates (review P2): counts are deltas, every triggered run is
    # present in every metric series, and each event payload carries its run id.
    # ------------------------------------------------------------------
    $runsAfter = [int](Invoke-Db "select count(*) from eval_run where version_id = $($script:versionId)")
    $expectedRuns = $runsBefore + $triggered.Count
    if ($runsAfter -ne $expectedRuns) {
        throw "eval runs after = $runsAfter, want runsBefore $runsBefore + triggered $($triggered.Count) = $expectedRuns"
    }
    $eventsAfter = Get-EventCounts $script:versionId
    $verdict.events = $eventsAfter
    $startedDelta = $eventsAfter.started - $eventsBefore.started
    $completedDelta = $eventsAfter.completed - $eventsBefore.completed
    $failedDelta = $eventsAfter.failed - $eventsBefore.failed
    if ($startedDelta -ne $triggered.Count) { throw "eval.started delta = $startedDelta, want $($triggered.Count)" }
    if ($completedDelta -ne $triggered.Count) { throw "eval.completed delta = $completedDelta, want $($triggered.Count)" }
    if ($failedDelta -ne 0) { throw "eval.failed delta = $failedDelta, want 0" }

    $trendMap = Read-Trends $script:versionId
    $minPoints = [int]::MaxValue
    $payloadCounts = [ordered]@{}
    foreach ($name in $metricNames) {
        $points = @($trendMap[$name])
        $verdict.trendSeries[$name] = $points.Count
        if ($points.Count -lt $minPoints) { $minPoints = $points.Count }
        foreach ($point in $points) {
            if ([string]$point.version_id -ne $script:versionId) {
                throw "trend point $($point.run_id) version_id = $($point.version_id)"
            }
            if ([string]$point.metric -ne $name) {
                throw "trend point $($point.run_id) metric = $($point.metric), want $name"
            }
        }
        foreach ($runId in $triggered) {
            $matches = @($points | Where-Object { [string]$_.run_id -eq $runId })
            if ($matches.Count -ne 1) {
                throw "triggered run $runId appears $($matches.Count) time(s) in $name trend, want exactly 1"
            }
        }
    }
    foreach ($runId in $triggered) {
        $payloadCounts[$runId] = [ordered]@{
            started   = Get-EventRunPayloadCount $script:versionId 'eval.started' $runId
            completed = Get-EventRunPayloadCount $script:versionId 'eval.completed' $runId
        }
        if ($payloadCounts[$runId].started -lt 1) { throw "eval.started payload missing eval_run_id $runId" }
        if ($payloadCounts[$runId].completed -lt 1) { throw "eval.completed payload missing eval_run_id $runId" }
    }
    $verdict.trendPoints = $minPoints
    if ($verdict.trendPoints -lt 2) { throw "trend points = $($verdict.trendPoints), want >= 2" }
    $verdict.runScoped = [ordered]@{
        runsBefore   = $runsBefore
        runsAfter    = $runsAfter
        expectedRuns = $expectedRuns
        triggered    = @($triggered)
        eventsBefore = $eventsBefore
        eventsAfter  = $eventsAfter
        eventDeltas  = [ordered]@{ started = $startedDelta; completed = $completedDelta; failed = $failedDelta }
        payloads     = $payloadCounts
    }

    # ------------------------------------------------------------------
    # Restart persistence (review P1): restart the API on the same database and
    # require the persisted runs, trend series and DB counters to be identical.
    # ------------------------------------------------------------------
    $dbBefore = Get-DbCounters $script:versionId
    $trendsBefore = Read-Trends $script:versionId
    $signatureBefore = (Get-DbCounters $script:versionId | ConvertTo-Json -Depth 12 -Compress) + '|' + (Get-TrendsSignature $trendsBefore)
    $pidBefore = Get-PortOwner $apiPort
    Write-Host "== restarting API (pid $pidBefore) on the same database $dbName"
    Stop-Api
    Start-Api
    Login $script:adminPassword
    $pidAfter = Get-PortOwner $apiPort
    $apiRestarted = ($null -ne $pidBefore -and $null -ne $pidAfter -and $pidBefore -ne $pidAfter)
    $afterVersion = Invoke-Api -Method 'GET' -Path "/api/versions/$($script:versionId)"
    if ($null -eq $afterVersion -or [string]$afterVersion.data.id -ne $script:versionId) {
        throw 'version not readable after API restart'
    }
    $dbAfter = Get-DbCounters $script:versionId
    $trendsAfter = Read-Trends $script:versionId
    $signatureAfter = (Get-DbCounters $script:versionId | ConvertTo-Json -Depth 12 -Compress) + '|' + (Get-TrendsSignature $trendsAfter)
    $persistedEqual = ($signatureBefore -eq $signatureAfter)
    $restartEqual = ($persistedEqual -and $apiRestarted)
    $verdict.dbCounters = $dbAfter
    $verdict.restart = [ordered]@{
        equal       = $restartEqual
        apiRestarted = $apiRestarted
        dbBefore    = $dbBefore
        dbAfter     = $dbAfter
        trendsEqual = $persistedEqual
        trendSeriesBefore = [ordered]@{}
        trendSeriesAfter  = [ordered]@{}
    }
    foreach ($name in $metricNames) {
        $verdict.restart.trendSeriesBefore[$name] = @($trendsBefore[$name]).Count
        $verdict.restart.trendSeriesAfter[$name] = @($trendsAfter[$name]).Count
    }
    if (-not $apiRestarted) { throw 'API process was not restarted on the same port' }
    if (-not $persistedEqual) { throw "restart persistence mismatch: before=$signatureBefore after=$signatureAfter" }

    if ($verdict.trendPoints -lt 2) { throw "trend points = $($verdict.trendPoints), want >= 2" }
    if ($eventsAfter.total -lt 1) { throw 'missing eval.* events' }
    if ($eventsAfter.started -lt 1) { throw 'missing eval.started event' }
    if ($eventsAfter.completed -lt 1) { throw 'missing eval.completed event' }
    if (@($verdict.screenshots).Count -lt 1) { throw 'missing screenshots' }
    if (-not $verdict.restart.equal) { throw 'restart.equal is false' }

    $verdict.status = 'PASS'
    Write-Host "== PASS eval runs=$($triggered.Count) (+$($runsBefore) before) metrics=4 trendPoints=$($verdict.trendPoints) eventsDelta=$startedDelta/$completedDelta restart=$($verdict.restart.equal) screenshots=$(@($verdict.screenshots).Count)"
} catch {
    $script:errors.Add($_.Exception.Message)
    Write-Host "== FAIL: $($_.Exception.Message)"
} finally {
    Write-Verdict
    if ($script:webStarted) { try { Stop-Web } catch { Write-Host "== web stop warning: $($_.Exception.Message)" } }
    if ($script:apiStarted) { try { Stop-Api } catch { Write-Host "== api stop warning: $($_.Exception.Message)" } }
}

if ($verdict.status -ne 'PASS') { exit 1 }
exit 0
