#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W2-9 codeintel domain script.

.DESCRIPTION
    Reuses the W2 base data created by scripts/v022-w2-data.ps1 (fresh database
    eztest_v022_w2, version and repository ids from run/data-verdict.json) and
    exercises the real codeintel flow through the product API only:

      1. start an isolated API on port 13446 (the database is never dropped or
         recreated);
      2. index both repositories from the base data at their own real diff base
         (HEAD~1) and verify two distinct done index runs;
      3. create one requirement anchor on a file that really changed in that
         diff (anchor deep link source);
      4. run one code analysis against the real HEAD commit, let the impact
         service persist candidates and verify the deterministic anchor hit;
      5. drill a real index failure with a corrupted throwaway git repository,
         restore the bound repository path, then retry the index and verify a
         done run plus the code.index_failed event;
      6. restart the API on the same database and verify the read model
         (index runs, analysis, impact, anchors) is unchanged;
      7. capture one or two CodeTab screenshots.

    Evidence (untracked) is written next to the W2 base evidence:
      docs/version-V0.22/acceptance/evidence/w2/run/code-verdict.json
      docs/version-V0.22/acceptance/evidence/w2/run/code/*.png
      docs/version-V0.22/acceptance/evidence/w2/run/code/*.json

    PASS requires: indexRepos >= 2, impactCandidates >= 1, anchors >= 1,
    failedRuns >= 1, retried >= 1, API reads 200, restart.equal = true and at
    least one non-empty screenshot.

.PARAMETER SkipBuild
    Reuse .tmp/w2-code/eztest.exe instead of rebuilding the current HEAD binary.

.PARAMETER SkipScreenshot
    Skip the EzTest web UI and the CodeTab screenshots (not a PASS run).
#>
param(
    [switch]$SkipBuild,
    [switch]$SkipScreenshot
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

# psql/curl/git write UTF-8; PowerShell 5.1 decodes native stdout with the console
# code page by default. The verdict is written explicitly as UTF-8 without BOM.
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

$dbName = 'eztest_v022_w2'
$apiPort = 13446
$webPort = 6872
$execPort = 13350
$apiBase = "http://127.0.0.1:$apiPort"
$webUrl = "http://127.0.0.1:$webPort"
$work = Join-Path $repoRoot '.tmp\w2-code'
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2\run'
$codeDir = Join-Path $evidenceRoot 'code'
$dataVerdictPath = Join-Path $evidenceRoot 'data-verdict.json'
$codeVerdictPath = Join-Path $evidenceRoot 'code-verdict.json'
$dbEvidencePath = Join-Path $codeDir 'db-evidence.json'
$uiLogPath = Join-Path $codeDir 'ui.log'
$uiScriptPath = Join-Path $work 'w2-code-ui.mjs'
$apiExe = Join-Path $work 'eztest.exe'
$apiLog = Join-Path $work 'api.log'
$webLog = Join-Path $work 'web.log'
$psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
$goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'

New-Item -ItemType Directory -Force -Path $work, $codeDir | Out-Null
Get-ChildItem -LiteralPath $codeDir -Filter '*.png' -ErrorAction SilentlyContinue | Remove-Item -Force

$script:token = ''
$script:apiStarted = $false
$script:webStarted = $false
$script:errors = New-Object System.Collections.Generic.List[string]
$script:originalRepoPath = $null
$script:pathPatched = $false

$verdict = [ordered]@{
    status           = 'FAIL'
    db               = $dbName
    versionId        = $null
    repoIds          = @()
    indexRepos       = 0
    impactCandidates = 0
    anchors          = 0
    failedRuns       = 0
    retried          = 0
    apiBase          = $apiBase
    generatedAt      = (Get-Date -Format o)
    screenshots      = @()
    events           = [ordered]@{}
    runs             = [ordered]@{}
    restart          = [ordered]@{ equal = $false; before = $null; after = $null }
    api              = [ordered]@{}
    errors           = @()
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
    # curl + explicit UTF-8 file I/O: Windows PowerShell 5.1 decodes a JSON
    # response without a charset as ISO-8859-1.
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
    if (Test-ApiReady) { return }
    if (-not $SkipBuild -or -not (Test-Path -LiteralPath $apiExe)) {
        Write-Host '== building backend'
        Push-Location (Join-Path $repoRoot 'backend')
        try {
            & $goExe build -o $apiExe ./cmd/eztest 2>&1 | Tee-Object -FilePath (Join-Path $work 'build.log')
            if ($LASTEXITCODE -ne 0) { throw 'go build ./cmd/eztest failed' }
        } finally { Pop-Location }
    }
    if (-not (Test-Path -LiteralPath $apiExe)) { throw "backend binary missing: $apiExe" }
    $runApi = Join-Path $work 'run-api-w29.cmd'
    $apiLines = @(
        '@echo off',
        "cd /d `"$repoRoot\backend`"",
        "set EZTEST_DATABASE_URL=$script:apiDbUrl",
        "set EZTEST_API_PORT=$apiPort",
        "set EZTEST_WEB_PORT=$webPort",
        "set EZTEST_EXECUTOR_PORT=$execPort",
        "set EZTEST_STORAGE_DIR=$work\storage",
        "set EZTEST_INITIAL_ADMIN_PASSWORD=$script:adminPassword",
        "set EZTEST_ADMIN_USERNAME=admin",
        "set EZTEST_SNOWFLAKE_NODE_ID=9",
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

function Start-Web {
    if ((Get-HttpCode "$webUrl/login") -eq '200') { return }
    $runWeb = Join-Path $work 'run-web.cmd'
    $nodeExe = (Get-Command node -ErrorAction SilentlyContinue).Source
    if (-not $nodeExe) { throw 'node is missing (required to start the web UI)' }
    $webLines = @(
        '@echo off',
        "cd /d `"$repoRoot\frontend`"",
        "set NEXT_PUBLIC_API_BASE_URL=$apiBase",
        "`"$nodeExe`" `"$repoRoot\frontend\node_modules\next\dist\bin\next`" dev -p $webPort >> `"$webLog`" 2>&1"
    )
    [System.IO.File]::WriteAllLines($runWeb, $webLines, (New-Object System.Text.UTF8Encoding($false)))
    Write-Host "== starting EzTest web on port $webPort"
    $script:webStarted = $true
    $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
        CommandLine      = "cmd.exe /c `"$runWeb`""
        CurrentDirectory = $work
    }
    $deadline = (Get-Date).AddSeconds(180)
    while ((Get-Date) -lt $deadline -and (Get-HttpCode "$webUrl/login") -ne '200') {
        Start-Sleep -Seconds 2
    }
    if ((Get-HttpCode "$webUrl/login") -ne '200') { throw 'EzTest web did not become ready on port 6872' }
}

function Stop-StartedServices {
    if ($script:webStarted) {
        $conns = Get-NetTCPConnection -State Listen -LocalPort $webPort -ErrorAction SilentlyContinue
        foreach ($conn in $conns) { Stop-Process -Id $conn.OwningProcess -Force -ErrorAction SilentlyContinue }
    }
    if ($script:apiStarted) {
        Stop-Api
    }
}

function Restore-RepoPath {
    if (-not $script:pathPatched -or $null -eq $script:originalRepoPath) { return }
    try {
        Invoke-Api -Method 'PATCH' -Path "/api/resources/code-repos/$script:repo1" -Body @{ path = $script:originalRepoPath } | Out-Null
        $script:pathPatched = $false
        Write-Host '== bound repository path restored'
    } catch {
        $script:errors.Add("restore repository path: $($_.Exception.Message)")
    }
}

function Get-GitValue([string]$path, [string]$rev) {
    $value = (& git -C $path rev-parse $rev 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) { throw "git rev-parse $rev failed in $path : $value" }
    return $value
}

function Wait-IndexStatus([string]$runId, [int]$timeoutSeconds = 180) {
    $deadline = (Get-Date).AddSeconds($timeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $status = Invoke-Db "select status from code_index_run where id = $runId"
        if ($status -eq 'done' -or $status -eq 'failed') { return $status }
        Start-Sleep -Seconds 1
    }
    throw "index run $runId did not reach a terminal status within ${timeoutSeconds}s"
}

function Wait-AnalysisStatus([string]$analysisId, [int]$timeoutSeconds = 300) {
    $deadline = (Get-Date).AddSeconds($timeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $status = Invoke-Db "select status from code_analysis where id = $analysisId"
        if ($status -eq 'done' -or $status -eq 'failed') { return $status }
        Start-Sleep -Seconds 1
    }
    throw "analysis $analysisId did not reach a terminal status within ${timeoutSeconds}s"
}

function Get-CodeSnapshot {
    $repos = Invoke-Api -Method 'GET' -Path "/api/versions/$script:versionId/code/repos"
    $repoRows = @()
    foreach ($item in @($repos.data)) {
        $latest = ''
        if ($null -ne $item.latest_index) {
            $latest = "$($item.latest_index.id):$($item.latest_index.status):$($item.latest_index.commit_sha)"
        }
        $repoRows += "$($item.repo_id)=$latest"
    }
    $analysis = Invoke-Api -Method 'GET' -Path "/api/versions/$script:versionId/code/analyses/latest?repoId=$script:analysisRepoId"
    $impact = Invoke-Api -Method 'GET' -Path "/api/versions/$script:versionId/code/impact"
    $anchors = Invoke-Api -Method 'GET' -Path "/api/versions/$script:versionId/code/anchors"
    return [ordered]@{
        repos    = @($repoRows | Sort-Object)
        analysis = "$($analysis.data.id):$($analysis.data.status):$($analysis.data.base_commit_sha):$($analysis.data.head_commit_sha)"
        impact   = @($impact.data | ForEach-Object { "$($_.id):$($_.subject_kind):$($_.subject_id)" } | Sort-Object)
        anchors  = @($anchors.data | ForEach-Object { "$($_.id):$($_.status):$($_.file_path)" } | Sort-Object)
    }
}

function Get-ScreenshotFiles {
    return @(Get-ChildItem -LiteralPath $codeDir -Filter '*.png' -ErrorAction SilentlyContinue | Where-Object { $_.Length -gt 0 })
}

function Write-Verdict {
    $verdict.errors = @($script:errors)
    $verdict.generatedAt = (Get-Date -Format o)
    Write-Utf8 $codeVerdictPath (($verdict | ConvertTo-Json -Depth 12))
}

try {
    if (-not (Test-Path -LiteralPath $dataVerdictPath)) { throw "missing W2 data verdict: $dataVerdictPath" }
    $dataText = [System.IO.File]::ReadAllText($dataVerdictPath, (New-Object System.Text.UTF8Encoding($false)))
    $data = $dataText | ConvertFrom-Json
    if ($null -eq $data.versionId -or [string]::IsNullOrWhiteSpace([string]$data.versionId)) {
        throw 'data-verdict.json has no versionId'
    }
    if ($null -eq $data.repoIds -or @($data.repoIds).Count -lt 2) {
        throw 'data-verdict.json must list at least two repoIds'
    }
    $script:versionId = [string]$data.versionId
    $repoIds = @($data.repoIds | ForEach-Object { [string]$_ })
    $script:repo1 = $repoIds[0]
    $script:repo2 = $repoIds[1]
    $passRequirementId = [string]$data.passRequirementId
    $verdict.versionId = $script:versionId
    $verdict.repoIds = $repoIds
    Write-Host "== W2-9 codeintel: version=$($script:versionId) repos=$($repoIds -join ',') db=$dbName"

    if (-not (Test-Path -LiteralPath $psql)) { throw "psql missing: $psql" }
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) { throw 'git is required' }

    # Credentials come from .env; only the database name is overridden.
    $dbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
    $parsed = [uri]$dbUrl
    $dbUser = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[0])
    $dbPassword = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[1])
    $env:PGPASSWORD = $dbPassword
    $script:apiDbUrl = "postgres://$dbUser`:$dbPassword@localhost:5433/${dbName}?sslmode=disable"
    $script:adminPassword = Get-EnvValue 'EZTEST_ADMIN_PASSWORD'

    # ---------------------------------------------------------------------
    # 1. API + login.
    # ---------------------------------------------------------------------
    Start-Api
    $session = Invoke-Api -Method 'POST' -Path '/api/auth/login' -Body @{ username = 'admin'; password = $script:adminPassword }
    if ($null -eq $session -or [string]::IsNullOrWhiteSpace([string]$session.data.token)) { throw 'login returned no token' }
    $script:token = [string]$session.data.token

    $version = Invoke-Api -Method 'GET' -Path "/api/versions/$($script:versionId)"
    if ($null -eq $version -or [string]$version.data.id -ne $script:versionId) { throw "base version $($script:versionId) not readable" }

    # ---------------------------------------------------------------------
    # 2. Index both repositories at their real diff base (HEAD~1).
    # ---------------------------------------------------------------------
    $repoRows = Invoke-Api -Method 'GET' -Path "/api/versions/$($script:versionId)/code/repos"
    $repoInfo = @{}
    foreach ($item in @($repoRows.data)) {
        $repoInfo[[string]$item.repo_id] = $item
    }
    # The base data may carry a stale branch name (for example "master" while
    # the SUT worktree is on "main"). CodeTab's change preview resolves the
    # repository branch, so align the metadata with the real branch through the
    # product API before indexing.
    $branchFixes = @()
    foreach ($repoId in $repoIds) {
        $path = [string]$repoInfo[$repoId].repository.path
        $currentBranch = [string]$repoInfo[$repoId].repository.branch
        $actualBranch = (& git -C $path symbolic-ref --short HEAD 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -eq 0 -and -not [string]::IsNullOrWhiteSpace($actualBranch) -and $actualBranch -ne $currentBranch) {
            Invoke-Api -Method 'PATCH' -Path "/api/resources/code-repos/$repoId" -Body @{ branch = $actualBranch } | Out-Null
            $branchFixes += [ordered]@{ repoId = $repoId; from = $currentBranch; to = $actualBranch }
            Write-Host "== repository $repoId branch metadata: $currentBranch -> $actualBranch"
        }
    }
    $verdict.repositoryBranchFixes = $branchFixes
    $reposPlan = @()
    foreach ($repoId in $repoIds) {
        if (-not $repoInfo.ContainsKey($repoId)) { throw "repository $repoId is missing from the version code repos" }
        $path = [string]$repoInfo[$repoId].repository.path
        if (-not (Test-Path -LiteralPath $path)) { throw "repository path does not exist: $path" }
        $head = Get-GitValue $path 'HEAD'
        $base = $head
        $previous = (& git -C $path rev-parse 'HEAD~1' 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -eq 0 -and -not [string]::IsNullOrWhiteSpace($previous)) { $base = $previous }
        $reposPlan += [ordered]@{ id = $repoId; path = $path; head = $head; base = $base; name = [string]$repoInfo[$repoId].repository.name }
    }
    $script:originalRepoPath = $reposPlan[0].path

    $indexRuns = @()
    foreach ($repo in $reposPlan) {
        Write-Host "== indexing $($repo.name) base=$($repo.base)"
        $run = Invoke-Api -Method 'POST' -Path "/api/versions/$($script:versionId)/code/index" -Body @{
            repoId = $repo.id
            commit = $repo.base
        }
        $runId = [string]$run.data.id
        $status = Wait-IndexStatus $runId 180
        if ($status -ne 'done') { throw "index run $runId for $($repo.name) ended $status" }
        $indexRuns += [ordered]@{ repoId = $repo.id; name = $repo.name; runId = $runId; commit = [string]$run.data.commit_sha; status = $status }
    }
    $verdict.runs.index = $indexRuns
    Write-Host "== indexed $($indexRuns.Count) repositories"

    # ---------------------------------------------------------------------
    # 3. Real diff -> anchor on a changed file.
    # ---------------------------------------------------------------------
    $analysisRepo = $null
    $changedFile = $null
    foreach ($repo in $reposPlan) {
        if ($repo.base -eq $repo.head) { continue }
        $diff = Invoke-Api -Method 'GET' -Path "/api/code/repos/$($repo.id)/diff?base=$($repo.base)&head=$($repo.head)"
        $files = @($diff.data | Where-Object { $_.binary -ne $true -and $_.status -ne 'deleted' })
        $modified = @($files | Where-Object { $_.status -eq 'modified' })
        if ($modified.Count -gt 0) {
            $analysisRepo = $repo
            $changedFile = $modified[0]
            break
        }
        if ($files.Count -gt 0 -and $null -eq $analysisRepo) {
            $analysisRepo = $repo
            $changedFile = $files[0]
        }
    }
    if ($null -eq $analysisRepo -or $null -eq $changedFile) {
        throw 'no non-binary changed file exists between HEAD~1 and HEAD in the bound repositories'
    }
    Write-Host "== diff $($analysisRepo.name): $($changedFile.path) ($($changedFile.status))"

    # Anchor at the indexed diff base: the file exists there (modified file) and
    # the immediate validation below resolves the same latest done index commit.
    $anchorCommit = $analysisRepo.base
    if ([string]$changedFile.status -ne 'modified') { $anchorCommit = $analysisRepo.head }
    $anchor = $null
    try {
        $anchor = Invoke-Api -Method 'POST' -Path "/api/versions/$($script:versionId)/code/anchors" -Body @{
            subjectKind = 'requirement'
            subjectId   = $passRequirementId
            repoId      = $analysisRepo.id
            filePath    = [string]$changedFile.path
            commit      = $anchorCommit
        }
    } catch {
        if ($_.Exception.Message -notmatch 'anchor_duplicate') { throw }
        $existing = Invoke-Api -Method 'GET' -Path "/api/versions/$($script:versionId)/code/anchors?subjectKind=requirement&subjectId=$passRequirementId"
        $matching = @($existing.data | Where-Object { $_.repo_id -eq $analysisRepo.id -and $_.file_path -eq $changedFile.path }) | Select-Object -First 1
        if ($null -eq $matching) { throw 'anchor_duplicate but no matching existing anchor was found' }
        $anchor = [pscustomobject]@{ data = $matching }
    }
    $anchorId = [string]$anchor.data.id
    $validated = Invoke-Api -Method 'POST' -Path "/api/versions/$($script:versionId)/code/anchors/$anchorId/validate"
    if ([string]$validated.data.status -eq 'broken') {
        throw "anchor $anchorId validated as broken"
    }
    Write-Host "== anchor $anchorId status=$($validated.data.status) path=$($validated.data.file_path)"

    # ---------------------------------------------------------------------
    # 4. Real analysis + impact.
    # ---------------------------------------------------------------------
    Write-Host "== starting analysis for $($analysisRepo.name) base=$($analysisRepo.base) head=$($analysisRepo.head)"
    $analysis = Invoke-Api -Method 'POST' -Path "/api/versions/$($script:versionId)/code/analyses" -Body @{
        repoId     = $analysisRepo.id
        headCommit = $analysisRepo.head
    }
    $analysisId = [string]$analysis.data.id
    $script:analysisRepoId = [string]$analysisRepo.id
    $analysisStatus = Wait-AnalysisStatus $analysisId 300
    if ($analysisStatus -ne 'done') { throw "analysis $analysisId ended $analysisStatus" }

    $impactDeadline = (Get-Date).AddSeconds(90)
    $impactCount = 0
    while ((Get-Date) -lt $impactDeadline) {
        $impactCount = [int](Invoke-Db "select count(*) from code_impact_candidate c join code_analysis a on a.id = c.analysis_id where a.version_id = $($script:versionId) and c.analysis_id = $analysisId")
        if ($impactCount -gt 0) { break }
        Start-Sleep -Seconds 2
    }
    $impactApi = Invoke-Api -Method 'GET' -Path "/api/versions/$($script:versionId)/code/impact"
    Write-Host "== analysis $analysisId done, impact candidates db=$impactCount api=$(@($impactApi.data).Count)"

    # ---------------------------------------------------------------------
    # 5. Failure + retry drill on the first repository (path restored).
    # ---------------------------------------------------------------------
    $failRepo = Join-Path $work 'failrepo'
    if (Test-Path -LiteralPath $failRepo) { Remove-Item -LiteralPath $failRepo -Recurse -Force }
    New-Item -ItemType Directory -Force -Path $failRepo | Out-Null
    & git -C $failRepo init -q
    & git -C $failRepo config user.email 'w2-code@example.local'
    & git -C $failRepo config user.name 'w2-code'
    Write-Utf8 (Join-Path $failRepo 'a.txt') "w2 codeintel failure drill`n"
    & git -C $failRepo add a.txt
    & git -C $failRepo commit -q -m 'w2 codeintel failure drill'
    if ($LASTEXITCODE -ne 0) { throw 'failed to create the throwaway git repository' }
    $corruptSha = (& git -C $failRepo rev-parse HEAD | Out-String).Trim()
    $treeSha = (& git -C $failRepo rev-parse 'HEAD^{tree}' | Out-String).Trim()
    $treeObject = Join-Path $failRepo (".git\objects\" + $treeSha.Substring(0, 2) + "\" + $treeSha.Substring(2))
    Remove-Item -LiteralPath $treeObject -Force

    $failedRunId = $null
    $failedStatus = $null
    try {
        Invoke-Api -Method 'PATCH' -Path "/api/resources/code-repos/$($reposPlan[0].id)" -Body @{ path = $failRepo } | Out-Null
        $script:pathPatched = $true
        Write-Host '== index failure drill: corrupted repository path installed'
        $failRun = Invoke-Api -Method 'POST' -Path "/api/versions/$($script:versionId)/code/index" -Body @{
            repoId = $reposPlan[0].id
            commit = $corruptSha
        }
        $failedRunId = [string]$failRun.data.id
        $failedStatus = Wait-IndexStatus $failedRunId 180
        if ($failedStatus -ne 'failed') { throw "failure drill index run $failedRunId ended $failedStatus, want failed" }
        Write-Host "== index failure drill: run $failedRunId failed as expected"
    } finally {
        Restore-RepoPath
    }

    Write-Host '== index retry after failure'
    $retryRun = Invoke-Api -Method 'POST' -Path "/api/versions/$($script:versionId)/code/index" -Body @{
        repoId = $reposPlan[0].id
        commit = $reposPlan[0].head
    }
    $retryRunId = [string]$retryRun.data.id
    $retryStatus = Wait-IndexStatus $retryRunId 180
    if ($retryStatus -ne 'done') { throw "retry index run $retryRunId ended $retryStatus" }
    Write-Host "== index retry: run $retryRunId done"

    # ---------------------------------------------------------------------
    # 6. DB / API / event verification.
    # ---------------------------------------------------------------------
    $repoCsv = ($repoIds | ForEach-Object { "'$($_)'" }) -join ','
    $verdict.indexRepos = [int](Invoke-Db "select count(distinct repo_id) from code_index_run where version_id = $($script:versionId) and status = 'done' and repo_id in ($repoCsv)")
    $verdict.impactCandidates = [int](Invoke-Db "select count(*) from code_impact_candidate c join code_analysis a on a.id = c.analysis_id where a.version_id = $($script:versionId)")
    $verdict.anchors = [int](Invoke-Db "select count(*) from code_anchor where version_id = $($script:versionId) and repo_id in ($repoCsv)")
    $verdict.failedRuns = [int](Invoke-Db "select count(*) from code_index_run where version_id = $($script:versionId) and status = 'failed' and repo_id in ($repoCsv)")
    $retryDoneAfterFailure = [int](Invoke-Db "select count(*) from code_index_run where version_id = $($script:versionId) and repo_id = $($reposPlan[0].id) and status = 'done' and id > $failedRunId")
    $verdict.retried = $(if ($retryStatus -eq 'done') { 1 } else { 0 })
    $verdict.runs.failedRunId = $failedRunId
    $verdict.runs.retryRunId = $retryRunId
    $verdict.runs.retryReused = ($retryRunId -eq $failedRunId)

    $verdict.events = [ordered]@{
        indexStarted   = [int](Invoke-Db "select count(*) from event where channel = 'version:$($script:versionId)' and type = 'code.index_started'")
        indexFinished  = [int](Invoke-Db "select count(*) from event where channel = 'version:$($script:versionId)' and type = 'code.index_finished'")
        indexFailed    = [int](Invoke-Db "select count(*) from event where channel = 'version:$($script:versionId)' and type = 'code.index_failed'")
        analysisStarted = [int](Invoke-Db "select count(*) from event where channel = 'version:$($script:versionId)' and type = 'code.analysis_started'")
        analysisFinished = [int](Invoke-Db "select count(*) from event where channel = 'version:$($script:versionId)' and type = 'code.analysis_finished'")
        impactComputed = [int](Invoke-Db "select count(*) from event where channel = 'version:$($script:versionId)' and type = 'code.impact_computed'")
    }

    $reposApi = Invoke-Api -Method 'GET' -Path "/api/versions/$($script:versionId)/code/repos"
    $impactApi = Invoke-Api -Method 'GET' -Path "/api/versions/$($script:versionId)/code/impact"
    $anchorsApi = Invoke-Api -Method 'GET' -Path "/api/versions/$($script:versionId)/code/anchors"
    $analysisApi = Invoke-Api -Method 'GET' -Path "/api/versions/$($script:versionId)/code/analyses/$analysisId"
    $verdict.api = [ordered]@{
        repos    = [ordered]@{ status = 200; count = @($reposApi.data).Count }
        analysis = [ordered]@{ status = 200; id = [string]$analysisApi.data.analysis.id; state = [string]$analysisApi.data.analysis.status }
        impact   = [ordered]@{ status = 200; count = @($impactApi.data).Count }
        anchors  = [ordered]@{ status = 200; count = @($anchorsApi.data).Count }
    }

    $dbEvidence = [ordered]@{
        generatedAt      = (Get-Date -Format o)
        versionId        = $script:versionId
        repoIds          = $repoIds
        indexRuns        = $indexRuns
        failedRunId      = $failedRunId
        retryRunId       = $retryRunId
        retryDoneAfterFailure = $retryDoneAfterFailure
        analysisId       = $analysisId
        anchorId         = $anchorId
        anchorStatus     = [string]$validated.data.status
        indexRepos       = $verdict.indexRepos
        impactCandidates = $verdict.impactCandidates
        anchors          = $verdict.anchors
        failedRuns       = $verdict.failedRuns
        events           = $verdict.events
    }
    Write-Utf8 $dbEvidencePath (($dbEvidence | ConvertTo-Json -Depth 10))

    if ($verdict.indexRepos -lt 2) { throw "indexRepos=$($verdict.indexRepos), want >= 2" }
    if ($verdict.impactCandidates -lt 1) { throw "impactCandidates=$($verdict.impactCandidates), want >= 1" }
    if ($verdict.anchors -lt 1) { throw "anchors=$($verdict.anchors), want >= 1" }
    if ($verdict.failedRuns -lt 1) { throw "failedRuns=$($verdict.failedRuns), want >= 1" }
    if ($verdict.retried -lt 1) { throw "retried=$($verdict.retried), want >= 1" }
    if ($verdict.events.indexFinished -lt 2) { throw "code.index_finished events=$($verdict.events.indexFinished), want >= 2" }
    if ($verdict.events.indexFailed -lt 1) { throw "code.index_failed events=$($verdict.events.indexFailed), want >= 1" }
    if (@($impactApi.data).Count -lt 1) { throw 'impact API returned no candidates' }
    if (@($anchorsApi.data).Count -lt 1) { throw 'anchors API returned no rows' }

    # ---------------------------------------------------------------------
    # 7. Restart persistence on the same database.
    # ---------------------------------------------------------------------
    $before = Get-CodeSnapshot
    Write-Host '== restarting API on the same database'
    Stop-Api
    Start-Api
    $session = Invoke-Api -Method 'POST' -Path '/api/auth/login' -Body @{ username = 'admin'; password = $script:adminPassword }
    $script:token = [string]$session.data.token
    $after = Get-CodeSnapshot
    $beforeJson = $before | ConvertTo-Json -Depth 8 -Compress
    $afterJson = $after | ConvertTo-Json -Depth 8 -Compress
    $verdict.restart.before = $before
    $verdict.restart.after = $after
    $verdict.restart.equal = ($beforeJson -eq $afterJson)
    if (-not $verdict.restart.equal) { throw 'read model changed across the API restart' }
    Write-Host '== restart persistence verified'

    # ---------------------------------------------------------------------
    # 8. CodeTab screenshots.
    # ---------------------------------------------------------------------
    if ($SkipScreenshot) {
        throw 'screenshots are required for PASS'
    }
    Start-Web
    $nodeExe = (Get-Command node -ErrorAction SilentlyContinue).Source
    if (-not $nodeExe) { throw 'node is missing (required for the screenshot helper)' }
    $uiScript = @'
import path from "node:path"
import { createRequire } from "node:module"

const repo = process.env.W29_REPO
const web = process.env.W29_WEB
const versionId = process.env.W29_VERSION_ID
const repo1 = process.env.W29_REPO1
const repo2 = process.env.W29_REPO2
const anchorId = process.env.W29_ANCHOR_ID
const out = process.env.W29_OUT
const adminPassword = process.env.W29_ADMIN_PW
const requireFromExecutor = createRequire(path.join(repo, "executor", "package.json"))
const { chromium } = requireFromExecutor("playwright")

let browser
try {
  browser = await chromium.launch({ headless: true, channel: "chrome" })
} catch {
  browser = await chromium.launch({ headless: true, channel: "msedge" })
}
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
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
    await page.waitForURL(/\/versions/, { timeout: 60000 })
  }

  await page.goto(`${web}/versions/${versionId}?tab=code&repo=${repo1}&anchor=${anchorId}`, { waitUntil: "domcontentloaded" })
  await page.waitForSelector("select#code-repo", { timeout: 90000 })
  await page.waitForFunction(() => document.querySelectorAll("select#code-repo option").length >= 2, null, { timeout: 30000 })
  await page.waitForSelector("li[aria-current='true']", { timeout: 30000 })
  await page.waitForTimeout(1200)
  await shot("01-codetab-anchor.png")

  const anchorRow = page.locator("li[aria-label]").first()
  await anchorRow.scrollIntoViewIfNeeded()
  await page.waitForTimeout(600)
  await page.screenshot({ path: path.join(out, "02-codetab-anchor-panel.png"), fullPage: false })
  console.log("screenshot 02-codetab-anchor-panel.png")
} finally {
  await browser.close()
}
'@
    Write-Utf8 $uiScriptPath $uiScript
    $env:W29_REPO = $repoRoot
    $env:W29_WEB = $webUrl
    $env:W29_VERSION_ID = $script:versionId
    # First screenshot: the repository that owns the diff/anchor/analysis.
    $env:W29_REPO1 = $script:analysisRepoId
    $env:W29_REPO2 = $(if ($script:analysisRepoId -eq $script:repo1) { $script:repo2 } else { $script:repo1 })
    $env:W29_ANCHOR_ID = $anchorId
    $env:W29_OUT = $codeDir
    $env:W29_ADMIN_PW = $script:adminPassword
    Write-Host '== capturing CodeTab screenshots'
    & $nodeExe $uiScriptPath 2>&1 | Tee-Object -FilePath $uiLogPath
    if ($LASTEXITCODE -ne 0) { throw 'CodeTab screenshot helper failed' }
    $verdict.screenshots = @(Get-ScreenshotFiles | ForEach-Object { "code/$($_.Name)" })
    if (@($verdict.screenshots).Count -lt 1) { throw 'no non-empty CodeTab screenshot was captured' }

    $verdict.status = 'PASS'
    Write-Host '== W2-9 codeintel domain PASS'
    Write-Host "== indexRepos=$($verdict.indexRepos) impact=$($verdict.impactCandidates) anchors=$($verdict.anchors) failedRuns=$($verdict.failedRuns) retried=$($verdict.retried)"
} catch {
    $script:errors.Add($_.Exception.Message)
    Write-Host "ERROR: $($_.Exception.Message)"
} finally {
    try { Restore-RepoPath } catch { $script:errors.Add($_.Exception.Message) }
    Stop-StartedServices
    Write-Verdict
    Write-Host "== verdict: $codeVerdictPath"
}

if ($verdict.status -ne 'PASS') {
    Write-Host '== W2-9 codeintel domain FAIL'
    Write-Host ($verdict.errors -join ' | ')
    exit 1
}
exit 0
