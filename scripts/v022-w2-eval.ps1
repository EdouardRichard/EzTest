#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W2-14 eval/quality domain script.

.DESCRIPTION
    Reuses the W2 base data created by scripts/v022-w2-data.ps1 (fresh database
    eztest_v022_w2) and exercises the snapshot eval + trends flow through the
    product API only:

      1. start an isolated API on port 13451 (the database is never dropped or
         recreated, so the parallel W2-13 exploration run is unaffected);
      2. read the version id from run/data-verdict.json (and
         run/exploration-verdict.json when the W2-13 run finished);
      3. trigger two mode=snapshot eval runs for that version through
         POST /api/quality/eval-runs {mode:"snapshot",version_id};
      4. verify eval_run (mode/status/version_id) and the four eval_metric rows
         (design_coverage, execution_pass_rate, case_coverage,
         visual_classification_accuracy) in PostgreSQL;
      5. read GET /api/quality/trends for each metric and require >= 2 points;
      6. verify the eval.started / eval.completed events on the version channel;
      7. write docs/version-V0.22/acceptance/evidence/w2/run/eval-verdict.json.

    PASS requires: evalRun >= 1, 4 metrics, trend points >= 2, eval.* events >= 1.

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
$work = Join-Path $repoRoot '.tmp\w2-eval'
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2\run'
$dataVerdictPath = Join-Path $evidenceRoot 'data-verdict.json'
$explorationVerdictPath = Join-Path $evidenceRoot 'exploration-verdict.json'
$evalVerdictPath = Join-Path $evidenceRoot 'eval-verdict.json'
$apiExe = Join-Path $work 'eztest.exe'
$apiLog = Join-Path $work 'api.log'
$psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
$goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'
$metricNames = @('design_coverage', 'execution_pass_rate', 'case_coverage', 'visual_classification_accuracy')

New-Item -ItemType Directory -Force -Path $work | Out-Null

$script:token = ''
$script:apiStarted = $false
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

function Wait-ApiReady([int]$timeoutSeconds = 120) {
    $deadline = (Get-Date).AddSeconds($timeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ((Get-HttpCode "$apiBase/api/health") -eq '200') { return $true }
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
    if (-not $SkipBuild -or -not (Test-Path -LiteralPath $apiExe)) {
        Write-Host '== building backend'
        Push-Location (Join-Path $repoRoot 'backend')
        try {
            & $goExe build -o $apiExe ./cmd/eztest 2>&1 | Tee-Object -FilePath (Join-Path $work 'build.log')
            if ($LASTEXITCODE -ne 0) { throw 'go build ./cmd/eztest failed' }
        } finally { Pop-Location }
    }
    if (-not (Test-Path -LiteralPath $apiExe)) { throw "backend binary missing: $apiExe" }
    $runApi = Join-Path $work 'run-api-w214.cmd'
    $apiLines = @(
        '@echo off',
        "cd /d `"$repoRoot\backend`"",
        "set EZTEST_DATABASE_URL=$script:apiDbUrl",
        "set EZTEST_API_PORT=$apiPort",
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
    if (-not (Wait-ApiReady 120)) { throw "API did not become healthy on port $apiPort" }
}

function Stop-Api {
    $conns = Get-NetTCPConnection -State Listen -LocalPort $apiPort -ErrorAction SilentlyContinue
    foreach ($conn in $conns) {
        Stop-Process -Id $conn.OwningProcess -Force -ErrorAction SilentlyContinue
    }
    Start-Sleep -Seconds 1
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

function Write-Verdict {
    $verdict.errors = @($script:errors)
    $verdict.generatedAt = (Get-Date -Format o)
    Write-Utf8 $evalVerdictPath (($verdict | ConvertTo-Json -Depth 12))
    Write-Host "== wrote $evalVerdictPath"
}

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

    Start-Api
    $session = Invoke-Api -Method 'POST' -Path '/api/auth/login' -Body @{ username = 'admin'; password = $script:adminPassword }
    if ($null -eq $session -or [string]::IsNullOrWhiteSpace([string]$session.data.token)) { throw 'login returned no token' }
    $script:token = [string]$session.data.token

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

    # Two snapshot triggers: the report needs a trend series (>= 2 points) and
    # the second run also exercises the warn-only previous-snapshot baseline.
    $triggered = @()
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
        $triggered += [string]$run.id
        Write-Host "== snapshot eval run $($run.id) status=$($run.status) metrics=$(@($run.metrics).Count)"
        Start-Sleep -Milliseconds 150
    }
    $verdict.evalRunIds = $triggered
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
    # Trends: each of the four metrics must have >= 2 version-scoped points.
    # ------------------------------------------------------------------
    $minPoints = [int]::MaxValue
    foreach ($name in $metricNames) {
        $series = Invoke-Api -Method 'GET' -Path "/api/quality/trends?version_id=$($script:versionId)&metric=$name"
        $points = @($series.data)
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
    }
    $verdict.trendPoints = $minPoints
    if ($verdict.trendPoints -lt 2) { throw "trend points = $($verdict.trendPoints), want >= 2" }

    # ------------------------------------------------------------------
    # Events: eval.started + eval.completed on the version channel.
    # ------------------------------------------------------------------
    $counts = Get-EventCounts $script:versionId
    $verdict.events = $counts
    if ($counts.started -lt 1) { throw 'missing eval.started event' }
    if ($counts.completed -lt 1) { throw 'missing eval.completed event' }

    $verdict.status = 'PASS'
    Write-Host "== PASS eval runs=$($triggered.Count) metrics=4 trendPoints=$($verdict.trendPoints) events=$($counts.total) (before=$runsBefore)"
} catch {
    $script:errors.Add($_.Exception.Message)
    Write-Host "== FAIL: $($_.Exception.Message)"
} finally {
    Write-Verdict
    if ($script:apiStarted) { Stop-Api }
}

if ($verdict.status -ne 'PASS') { exit 1 }
exit 0
