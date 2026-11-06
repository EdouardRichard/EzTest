#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W2-15 GC domain script.

.DESCRIPTION
    Reuses the W2 base data created by scripts/v022-w2-data.ps1 (database
    eztest_v022_w2) and exercises the two-phase GC flow through the product API
    and read-only SQL assertions:

      1. start an isolated API on port 13452 in a private working directory;
         the database is never dropped or recreated and the shared storage of
         other W2 domain scripts is not touched (paths.data_root is relative and
         therefore resolves inside .tmp/w2-gc);
      2. read run/data-verdict.json for the version id / base artifacts;
      3. make retention.* the effective config through the settings API and
         assert the four legacy gc.* aliases are absent from the editable
         catalog (the catalog check lives in the Go tests);
      4. create one unreferenced temp object: a valid-SHA file under
         objects/aa/bb/<sha> with an old mtime and no content_object row;
      5. run a dry-run and assert the report only records candidates (no rows,
         files or tombstones deleted);
      6. capture an active case's evidence count, run apply and assert >= 1
         deletion, >= 1 tombstone and the evidence count unchanged;
      7. verify the gc.started / gc.completed events on the system channel and
         their EV-21 payload keys in PostgreSQL;
      8. write docs/version-V0.22/acceptance/evidence/w2/run/gc-verdict.json.

    PASS requires: dryDeleted = 0, dryTombstones = 0, applyDeleted >= 1,
    applyTombstones >= 1, active case evidence unchanged, gc.started >= 2 and
    gc.completed >= 2.

.PARAMETER SkipBuild
    Reuse .tmp/w2-gc/eztest.exe instead of rebuilding the current HEAD binary.
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
$apiPort = 13452
$apiBase = "http://127.0.0.1:$apiPort"
$work = Join-Path $repoRoot '.tmp\w2-gc'
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2\run'
$dataVerdictPath = Join-Path $evidenceRoot 'data-verdict.json'
$gcVerdictPath = Join-Path $evidenceRoot 'gc-verdict.json'
$apiExe = Join-Path $work 'eztest.exe'
$apiLog = Join-Path $work 'api.log'
$psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
$goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'

New-Item -ItemType Directory -Force -Path $work | Out-Null

$script:token = ''
$script:apiStarted = $false
$script:errors = New-Object System.Collections.Generic.List[string]

$verdict = [ordered]@{
    status                     = 'FAIL'
    db                         = $dbName
    dryRunId                   = $null
    applyRunId                 = $null
    dryDeleted                 = $null
    dryTombstones              = $null
    applyDeleted               = $null
    applyTombstones            = $null
    runningCaseEvidenceBefore  = $null
    runningCaseEvidenceAfter   = $null
    events                     = [ordered]@{ started = 0; completed = 0; failed = 0; total = 0 }
    apiBase                    = $apiBase
    generatedAt                = (Get-Date -Format o)
    versionId                  = $null
    dataSource                 = 'base'
    dataRoot                   = $null
    candidateClass             = 'temp'
    candidateSha               = $null
    dryCandidates              = $null
    applyDeletedObjects        = $null
    applyDeletedRefs           = $null
    applyDeletedFiles          = $null
    runningCaseId              = $null
    runningCaseStatus          = $null
    eventsDetail               = [ordered]@{}
    screenshots                = @()
    errors                     = @()
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
    if ((Get-HttpCode "$apiBase/api/health") -eq '200') {
        Write-Host "== reusing the API already listening on $apiPort"
        return
    }
    if (-not $SkipBuild -or -not (Test-Path -LiteralPath $apiExe)) {
        Write-Host '== building backend'
        Push-Location (Join-Path $repoRoot 'backend')
        try {
            & $goExe build -o $apiExe ./cmd/eztest 2>&1 | Tee-Object -FilePath (Join-Path $work 'build.log')
            if ($LASTEXITCODE -ne 0) { throw 'go build ./cmd/eztest failed' }
        } finally { Pop-Location }
    }
    if (-not (Test-Path -LiteralPath $apiExe)) { throw "backend binary missing: $apiExe" }
    $runApi = Join-Path $work 'run-api-w215.cmd'
    $apiLines = @(
        '@echo off',
        "cd /d `"$work`"",
        "set EZTEST_DATABASE_URL=$script:apiDbUrl",
        "set EZTEST_API_PORT=$apiPort",
        "set EZTEST_INITIAL_ADMIN_PASSWORD=$script:adminPassword",
        "set EZTEST_ADMIN_USERNAME=admin",
        "set EZTEST_SNOWFLAKE_NODE_ID=15",
        "`"$apiExe`" >> `"$apiLog`" 2>&1"
    )
    [System.IO.File]::WriteAllLines($runApi, $apiLines, (New-Object System.Text.UTF8Encoding($false)))
    Write-Host "== starting API on port $apiPort (cwd $work)"
    $script:apiStarted = $true
    $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
        CommandLine      = "cmd.exe /c `"$runApi`""
        CurrentDirectory = $work
    }
    if (-not (Wait-ApiReady 120)) { throw "API did not become healthy on port $apiPort (log: $apiLog)" }
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

function Resolve-DataRoot {
    $rows = Invoke-Api -Method 'GET' -Path '/api/admin/settings'
    $setting = @($rows.data | Where-Object { $_.key -eq 'paths.data_root' })
    if ($setting.Count -lt 1) { throw 'paths.data_root is not in the settings API' }
    $raw = [string]$setting[0].value
    if ([string]::IsNullOrWhiteSpace($raw)) { throw 'paths.data_root is empty' }
    if ([System.IO.Path]::IsPathRooted($raw)) {
        return [System.IO.Path]::GetFullPath($raw)
    }
    return [System.IO.Path]::GetFullPath((Join-Path $work $raw))
}

function Set-RetentionSetting([string]$key, $value) {
    $updated = Invoke-Api -Method 'PATCH' -Path "/api/admin/settings/$key" -Body @{ value = $value }
    if ($null -eq $updated -or $updated.data.key -ne $key) { throw "settings PATCH $key returned no row" }
    return $updated.data
}

function New-UnreferencedTempCandidate([string]$dataRoot) {
    $seed = [System.Guid]::NewGuid().ToString() + [System.Guid]::NewGuid().ToString() + (Get-Date -Format o)
    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($seed)
    $sha = ([System.BitConverter]::ToString($sha256.ComputeHash($bytes))).Replace('-', '').ToLowerInvariant()
    if ((Invoke-Db "select count(*) from content_object where sha256 = '$sha'") -ne '0') {
        throw "generated temp candidate sha collides with a content_object: $sha"
    }
    $dir = Join-Path $dataRoot ("objects\{0}\{1}" -f $sha.Substring(0, 2), $sha.Substring(2, 2))
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $path = Join-Path $dir $sha
    $payload = [System.Text.Encoding]::UTF8.GetBytes("eztest W2-15 GC temp candidate " + $seed)
    [System.IO.File]::WriteAllBytes($path, $payload)
    $old = (Get-Date).ToUniversalTime().AddHours(-200)
    [System.IO.File]::SetLastWriteTimeUtc($path, $old)
    [System.IO.File]::SetCreationTimeUtc($path, $old)
    return [pscustomobject]@{ SHA = $sha; Path = $path; Size = $payload.Length }
}

function Get-Events([string]$afterSeq) {
    $rows = Invoke-Db "select type || '=' || count(*) from event where channel = 'system' and seq > $afterSeq and type like 'gc.%' group by type order by type"
    $counts = [ordered]@{ started = 0; completed = 0; failed = 0; total = 0 }
    $detail = [ordered]@{}
    foreach ($row in @($rows -split "`n")) {
        $row = $row.Trim()
        if ($row -match '^gc\.([a-z]+)=([0-9]+)$') {
            $counts[$Matches[1]] = [int]$Matches[2]
            $counts.total += [int]$Matches[2]
            $detail['gc.' + $Matches[1]] = [int]$Matches[2]
        }
    }
    return [pscustomobject]@{ Counts = $counts; Detail = $detail }
}

function Write-Verdict {
    $verdict.errors = @($script:errors)
    $verdict.generatedAt = (Get-Date -Format o)
    Write-Utf8 $gcVerdictPath (($verdict | ConvertTo-Json -Depth 12))
    Write-Host "== wrote $gcVerdictPath"
}

try {
    if (-not (Test-Path -LiteralPath $dataVerdictPath)) { throw "missing W2 data verdict: $dataVerdictPath" }
    $data = Read-JsonVerdict $dataVerdictPath
    if ($null -eq $data -or [string]::IsNullOrWhiteSpace([string]$data.versionId)) {
        throw 'data-verdict.json has no versionId'
    }
    $verdict.versionId = [string]$data.versionId
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

    # retention.* is the effective GC settings family; the old gc.* aliases are
    # not accepted by the catalog and are filtered from the UI.
    $null = Set-RetentionSetting 'retention.unreferenced_content_days' 30
    $null = Set-RetentionSetting 'retention.gc_grace_hours' 168
    $null = Set-RetentionSetting 'retention.temp_hours' 24
    $null = Set-RetentionSetting 'retention.gc_dry_run' $true

    $dataRoot = Resolve-DataRoot
    $verdict.dataRoot = $dataRoot
    New-Item -ItemType Directory -Force -Path (Join-Path $dataRoot 'objects') | Out-Null
    $candidate = New-UnreferencedTempCandidate $dataRoot
    $verdict.candidateSha = $candidate.SHA
    Write-Host "== temp candidate $($candidate.SHA) at $($candidate.Path)"

    # Active case evidence: prefer a running/paused case; otherwise use the
    # case_run with the most evidence (it is still the protected evidence set).
    $caseRow = Invoke-Db "select cr.id || '|' || cr.status || '|' || count(e.id) from case_run cr join evidence e on e.case_run_id = cr.id where cr.status in ('running','paused') group by cr.id, cr.status order by count(e.id) desc, cr.id desc limit 1"
    if ([string]::IsNullOrWhiteSpace($caseRow)) {
        $caseRow = Invoke-Db "select cr.id || '|' || cr.status || '|' || count(e.id) from case_run cr join evidence e on e.case_run_id = cr.id group by cr.id, cr.status order by count(e.id) desc, cr.id desc limit 1"
    }
    if ([string]::IsNullOrWhiteSpace($caseRow)) { throw 'no case_run with evidence exists in the base data' }
    $caseParts = $caseRow -split '\|'
    $script:caseRunId = [string]$caseParts[0]
    $verdict.runningCaseId = $script:caseRunId
    $verdict.runningCaseStatus = [string]$caseParts[1]
    $script:evidenceBefore = [int]$caseParts[2]
    $verdict.runningCaseEvidenceBefore = $script:evidenceBefore

    $tombstonesBefore = [int](Invoke-Db 'select count(*) from gc_tombstone')
    $systemSeqBefore = [string](Invoke-Db "select coalesce(max(seq), 0) from event where channel = 'system'")

    # --- dry run -------------------------------------------------------------
    $dry = Invoke-Api -Method 'POST' -Path '/api/admin/gc/runs' -Body @{ mode = 'dry_run' }
    $dryRun = $dry.data
    if ($null -eq $dryRun -or [string]::IsNullOrWhiteSpace([string]$dryRun.id)) { throw 'dry-run returned no run' }
    $verdict.dryRunId = [string]$dryRun.id
    if ([string]$dryRun.status -ne 'completed' -or [string]$dryRun.mode -ne 'dry_run') {
        throw "dry-run status/mode = $($dryRun.status)/$($dryRun.mode)"
    }
    $dryReport = $dryRun.report
    $dryFiles = 0
    if ($null -ne $dryReport.files) { $dryFiles = [int]$dryReport.files.deleted }
    $verdict.dryTombstones = [int]$dryReport.tombstones
    $verdict.dryDeleted = [int]$dryRun.deleted_objects + [int]$dryRun.deleted_refs + $dryFiles
    $dryCandidates = $dryReport.candidates
    if ($null -ne $dryCandidates) {
        $verdict.dryCandidates = [ordered]@{
            temp             = [int]$dryCandidates.temp
            unreferenced     = [int]$dryCandidates.unreferenced
            superseded_export = [int]$dryCandidates.superseded_export
            orphan_file      = [int]$dryCandidates.orphan_file
        }
    }
    if ($verdict.dryDeleted -ne 0) { throw "dry-run deleted $($verdict.dryDeleted) rows/files" }
    if ($verdict.dryTombstones -ne 0) { throw "dry-run wrote $($verdict.dryTombstones) tombstones" }
    if (-not (Test-Path -LiteralPath $candidate.Path)) { throw 'dry-run removed the temp candidate file' }
    if ((Invoke-Db 'select count(*) from gc_tombstone') -ne [string]$tombstonesBefore) {
        throw 'dry-run changed the gc_tombstone row count'
    }
    if ($null -ne $dryCandidates -and [int]$dryCandidates.temp -lt 1) {
        throw "dry-run did not report the temp candidate (candidates=$($dryReport.candidates | ConvertTo-Json -Compress))"
    }
    Write-Host "== dry-run $($dryRun.id): candidates temp=$($dryCandidates.temp) deleted=$($verdict.dryDeleted) tombstones=$($verdict.dryTombstones)"

    # --- apply ---------------------------------------------------------------
    $apply = Invoke-Api -Method 'POST' -Path '/api/admin/gc/runs' -Body @{ mode = 'apply'; confirm = $true }
    $applyRun = $apply.data
    if ($null -eq $applyRun -or [string]::IsNullOrWhiteSpace([string]$applyRun.id)) { throw 'apply returned no run' }
    $verdict.applyRunId = [string]$applyRun.id
    if ([string]$applyRun.status -ne 'completed' -or [string]$applyRun.mode -ne 'apply') {
        throw "apply status/mode = $($applyRun.status)/$($applyRun.mode)"
    }
    $applyReport = $applyRun.report
    $applyFiles = 0
    if ($null -ne $applyReport.files) { $applyFiles = [int]$applyReport.files.deleted }
    $verdict.applyDeletedObjects = [int]$applyRun.deleted_objects
    $verdict.applyDeletedRefs = [int]$applyRun.deleted_refs
    $verdict.applyDeletedFiles = $applyFiles
    $verdict.applyDeleted = [int]$applyRun.deleted_objects + [int]$applyRun.deleted_refs + $applyFiles
    $verdict.applyTombstones = [int]$applyReport.tombstones
    if ($verdict.applyDeleted -lt 1) { throw "apply deleted $($verdict.applyDeleted) rows/files, want >= 1" }
    if ($verdict.applyTombstones -lt 1) { throw "apply wrote $($verdict.applyTombstones) tombstones, want >= 1" }
    if (Test-Path -LiteralPath $candidate.Path) { throw 'apply did not delete the temp candidate file' }
    $tombstonesAfter = [int](Invoke-Db 'select count(*) from gc_tombstone')
    if ($tombstonesAfter -lt ($tombstonesBefore + 1)) {
        throw "gc_tombstone count $tombstonesAfter did not grow from $tombstonesBefore"
    }
    $candidateTombstone = Invoke-Db "select count(*) from gc_tombstone where run_id = $($applyRun.id) and object_sha = '$($candidate.SHA)'"
    if ($candidateTombstone -ne '1') { throw "no tombstone row for the apply run's temp candidate" }

    # Running/active case evidence must survive the apply byte-for-byte.
    $evidenceAfter = [int](Invoke-Db "select count(*) from evidence where case_run_id = $($script:caseRunId)")
    $verdict.runningCaseEvidenceAfter = $evidenceAfter
    if ($evidenceAfter -ne $script:evidenceBefore) {
        throw "active case $($script:caseRunId) evidence count $evidenceAfter != $($script:evidenceBefore)"
    }
    $liveEvidenceRefs = [int](Invoke-Db "select count(*) from evidence e join content_ref r on r.id = e.content_ref_id where e.case_run_id = $($script:caseRunId) and r.deleted_at is null")
    if ($liveEvidenceRefs -lt $evidenceAfter) {
        throw "active case evidence content refs were soft-deleted ($liveEvidenceRefs < $evidenceAfter)"
    }
    Write-Host "== apply $($applyRun.id): deleted objects=$($verdict.applyDeletedObjects) refs=$($verdict.applyDeletedRefs) files=$applyFiles tombstones=$($verdict.applyTombstones); case $($script:caseRunId) evidence=$evidenceAfter"

    # --- events --------------------------------------------------------------
    $events = Get-Events $systemSeqBefore
    $verdict.events = $events.Counts
    $verdict.eventsDetail = $events.Detail
    if ($events.Counts.started -lt 2) { throw "gc.started events = $($events.Counts.started), want >= 2" }
    if ($events.Counts.completed -lt 2) { throw "gc.completed events = $($events.Counts.completed), want >= 2" }
    if ($events.Counts.total -lt 2) { throw "gc.* events = $($events.Counts.total), want >= 2" }
    $payloadRows = Invoke-Db "select count(*) from event where channel = 'system' and seq > $systemSeqBefore and type = 'gc.completed' and envelope->'payload' ? 'gc_run_id' and envelope->'payload' ? 'mode' and envelope->'payload' ? 'deleted_objects' and envelope->'payload' ? 'deleted_refs' and envelope->'payload' ? 'error'"
    if ([int]$payloadRows -lt 2) { throw "gc.completed payload keys missing (rows=$payloadRows)" }

    $verdict.status = 'PASS'
    Write-Host "== PASS dry=$($verdict.dryRunId) apply=$($verdict.applyRunId) events started=$($events.Counts.started) completed=$($events.Counts.completed)"
} catch {
    $script:errors.Add($_.Exception.Message)
    Write-Host "== FAIL: $($_.Exception.Message)"
} finally {
    Write-Verdict
    if ($script:apiStarted) { Stop-Api }
}

if ($verdict.status -ne 'PASS') { exit 1 }
exit 0
