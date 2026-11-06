#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W2-8 report export domain script.

.DESCRIPTION
    Reuses the W2 base data created by scripts/v022-w2-data.ps1 (fresh database
    eztest_v022_w2, version/report/revision from run/data-verdict.json) and
    exercises the report artifact export through the product API only:

      1. start an isolated API on port 13448 when nothing is listening there
         (the database is never dropped or recreated, and the shared W2 content
         storage directory is reused so base report bytes stay readable);
      2. log in as the configured admin;
      3. create HTML and Markdown artifacts for the base report revision and
         attempt PDF with a bounded pdf_not_ready poll (the base revision may
         have no published PDF; the 409 path is recorded);
      4. verify export_artifact rows, content_object/content_ref ledger rows,
         no binary columns, export.started/completed/failed events, the report
         artifact list API and authenticated download 200 + sha256;
      5. optionally capture 2-3 screenshots of the report export/artifact and
         evidence surfaces (screenshots never gate the verdict).

    Evidence (untracked) is written next to the W2 base evidence:
      docs/version-V0.22/acceptance/evidence/w2/run/export-verdict.json
      docs/version-V0.22/acceptance/evidence/w2/run/export/*.png
      docs/version-V0.22/acceptance/evidence/w2/run/export/artifacts/*

    PASS requires at least two ready formats (HTML and Markdown are mandatory,
    PDF is preferred) and every sha/event/download/list assertion above.

    Everything this script starts is stopped at the end. A pre-existing API or
    web dev server is reused and left running.

.PARAMETER SkipBuild
    Reuse .tmp/w2-export/eztest.exe instead of rebuilding the current HEAD.

.PARAMETER PdfWaitSeconds
    Upper bound for polling pdf_not_ready before recording the 409 path (60s).

.PARAMETER SkipScreenshots
    Do not start the web UI or capture screenshots.
#>
param(
    [switch]$SkipBuild,
    [int]$PdfWaitSeconds = 60,
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
$apiPort = 13448
$webPort = 6872
$execPort = 13349
$apiBase = "http://127.0.0.1:$apiPort"
$work = Join-Path $repoRoot '.tmp\w2-export'
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2\run'
$exportDir = Join-Path $evidenceRoot 'export'
$artifactDir = Join-Path $exportDir 'artifacts'
$dataVerdictPath = Join-Path $evidenceRoot 'data-verdict.json'
$exportVerdictPath = Join-Path $exportDir 'export-verdict.json'
$uiScriptPath = Join-Path $work 'w2-export-ui.mjs'
$apiExe = Join-Path $work 'eztest.exe'
$apiLog = Join-Path $work 'api.log'
$webLog = Join-Path $work 'web.log'
$psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
$goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'
$backendDir = Join-Path $repoRoot 'backend'
# The API resolves paths.data_root relative to its process CWD (backend). The
# real root is read from the settings API after login; this is only the fallback.
$storageDir = Join-Path $backendDir '.data'

New-Item -ItemType Directory -Force -Path $work, $exportDir, $artifactDir | Out-Null

$script:token = ''
$script:apiStarted = $false
$script:webStarted = $false
$script:startedAt = Get-Date
$script:errors = New-Object System.Collections.Generic.List[string]
$script:createdArtifacts = New-Object System.Collections.Generic.List[object]
$script:eventBaseline = [long]0
$script:downloadOk = $false
$script:dbUser = $null
$script:report = $null

# The verdict is written even when the flow fails so a reviewer can see exactly
# which assertion failed.
$verdict = [ordered]@{
    status          = 'FAIL'
    versionId       = $null
    reportId        = $null
    reportRevision  = $null
    artifacts       = @()
    formats         = @()
    events          = [ordered]@{ started = 0; completed = 0; failed = 0 }
    downloadOk      = $false
    apiBase         = $apiBase
    generatedAt     = (Get-Date -Format o)
    storageDir      = $storageDir
    pdf             = [ordered]@{
        available    = $false
        status       = $null
        attempts     = 0
        lastError    = ''
        documented409 = $false
        path         = 'pdf_not_ready'
    }
    downloads       = @()
    screenshots     = @()
    errors          = @()
}

# UTF-8 without BOM: verdicts may carry CJK API messages/reasons.
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

function Test-ApiReadyOn([int]$port) {
    return ((Get-HttpCode "http://127.0.0.1:$port/api/health") -eq '200')
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

function Add-CreatedArtifact($data) {
    $entry = [ordered]@{
        id     = [string]$data.id
        format = [string]$data.format
        sha256 = [string]$data.sha256
        size   = [long]$data.size
        status = [string]$data.status
    }
    $script:createdArtifacts.Add($entry)
    return $entry
}

function Get-ArtifactExtension([string]$format) {
    switch ($format) {
        'html' { return '.html' }
        'pdf' { return '.pdf' }
        default { return '.md' }
    }
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

function Get-ScreenshotFiles {
    if (-not (Test-Path -LiteralPath $exportDir)) { return @() }
    return @(Get-ChildItem -LiteralPath $exportDir -Filter '*.png' | Where-Object { $_.Length -gt 0 } | Sort-Object Name)
}

function Write-Verdict {
    $verdict.artifacts = @($script:createdArtifacts | ForEach-Object { $_ })
    $verdict.downloadOk = [bool]$script:downloadOk
    $verdict.errors = @($script:errors)
    $verdict.generatedAt = (Get-Date -Format o)
    Write-Utf8 $exportVerdictPath (($verdict | ConvertTo-Json -Depth 12))
}

try {
    if (-not (Test-Path -LiteralPath $dataVerdictPath)) { throw "missing W2 data verdict: $dataVerdictPath" }
    $data = Read-JsonFile $dataVerdictPath
    if ([string]::IsNullOrWhiteSpace([string]$data.versionId)) { throw 'data-verdict.json has no versionId' }
    if ([string]::IsNullOrWhiteSpace([string]$data.reportId)) { throw 'data-verdict.json has no reportId' }
    $versionId = [string]$data.versionId
    $reportId = [string]$data.reportId
    $expectedRevision = 1
    if ($null -ne $data.reportRevision) { $expectedRevision = [int]$data.reportRevision }
    $verdict.versionId = $versionId
    $verdict.reportId = $reportId
    Write-Host "== W2-8 export domain: version=$versionId report=$reportId revision=$expectedRevision db=$dbName"

    if (-not (Test-Path -LiteralPath $psql)) { throw "psql missing: $psql" }

    $dbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
    $parsed = [uri]$dbUrl
    $script:dbUser = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[0])
    $dbPassword = [uri]::UnescapeDataString($parsed.UserInfo.Split(':')[1])
    $env:PGPASSWORD = $dbPassword
    $apiDbUrl = "postgres://$($script:dbUser)`:$dbPassword@localhost:5433/${dbName}?sslmode=disable"
    $adminPassword = Get-EnvValue 'EZTEST_ADMIN_PASSWORD'

    # ---------------------------------------------------------------------
    # 1. API: reuse a listening instance, otherwise build and start one.
    # ---------------------------------------------------------------------
    if (-not (Test-ApiReady)) {
        if (-not $SkipBuild) {
            Write-Host '== building backend'
            Push-Location (Join-Path $repoRoot 'backend')
            try {
                & $goExe build -o $apiExe ./cmd/eztest 2>&1 | Tee-Object -FilePath (Join-Path $exportDir 'build.log')
                if ($LASTEXITCODE -ne 0) { throw 'go build ./cmd/eztest failed' }
            } finally { Pop-Location }
        }
        if (-not (Test-Path -LiteralPath $apiExe)) { throw "backend binary missing: $apiExe" }
        $runApi = Join-Path $work 'run-api-w28.cmd'
        $apiLines = @(
            '@echo off',
            "cd /d `"$repoRoot\backend`"",
            "set EZTEST_DATABASE_URL=$apiDbUrl",
            "set EZTEST_API_PORT=$apiPort",
            "set EZTEST_WEB_PORT=$webPort",
            "set EZTEST_EXECUTOR_PORT=$execPort",
            "set EZTEST_INITIAL_ADMIN_PASSWORD=$adminPassword",
            "set EZTEST_ADMIN_USERNAME=admin",
            "set EZTEST_SNOWFLAKE_NODE_ID=8",
            "`"$apiExe`" >> `"$apiLog`" 2>&1"
        )
        [System.IO.File]::WriteAllLines($runApi, $apiLines, (New-Object System.Text.UTF8Encoding($false)))
        Write-Host '== starting isolated API on port 13448'
        $script:apiStarted = $true
        $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
            CommandLine      = "cmd.exe /c `"$runApi`""
            CurrentDirectory = $work
        }
        if (-not (Wait-ApiReady 120)) { throw 'API did not become healthy on port 13448' }
    } else {
        Write-Host '== reusing the API already listening on port 13448'
    }

    # ---------------------------------------------------------------------
    # 2. Login and read the base report revision.
    # ---------------------------------------------------------------------
    $session = Invoke-Api -Method 'POST' -Path '/api/auth/login' -Body @{ username = 'admin'; password = $adminPassword }
    if ($null -eq $session -or [string]::IsNullOrWhiteSpace([string]$session.data.token)) { throw 'login returned no token' }
    $script:token = [string]$session.data.token

    # Resolve the content ledger root the API actually uses. paths.data_root is
    # relative to the API process CWD (backend); the base report/evidence bytes
    # live there, so downloads and file existence must use the same root.
    $dataRoot = './.data'
    try {
        $rootSetting = Invoke-Api -Method 'GET' -Path '/api/admin/settings/paths.data_root'
        if ($null -ne $rootSetting -and -not [string]::IsNullOrWhiteSpace([string]$rootSetting.data.value)) {
            $dataRoot = [string]$rootSetting.data.value
        }
    } catch {
        $script:errors.Add("read paths.data_root: $($_.Exception.Message)")
    }
    if ([System.IO.Path]::IsPathRooted($dataRoot)) {
        $storageDir = [System.IO.Path]::GetFullPath($dataRoot)
    } else {
        $storageDir = [System.IO.Path]::GetFullPath((Join-Path $backendDir $dataRoot))
    }
    $verdict.storageDir = $storageDir
    if (-not (Test-Path -LiteralPath $storageDir)) { throw "content data root missing: $storageDir (run v022-w2-data.ps1 first)" }
    Write-Host "== content data root=$storageDir"

    $report = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/reports/$reportId"
    $script:report = $report
    if ([string]$report.data.id -ne $reportId) { throw "report $reportId is not readable" }
    $current = $report.data.current_revision
    if ($null -eq $current) { throw "report $reportId has no current revision" }
    $currentRevision = [int]$current.revision
    $verdict.reportRevision = $currentRevision
    $verdict.pdf.available = [bool]$current.pdf_available
    $verdict.pdf.status = [string]$current.pdf_status
    if ($currentRevision -ne $expectedRevision) {
        throw "current report revision is $currentRevision, data-verdict expects $expectedRevision"
    }
    if ([string]$current.status -ne 'published' -or [string]$current.render_status -ne 'done') {
        throw "report revision $currentRevision is not published/rendered (status=$($current.status) render=$($current.render_status))"
    }
    Write-Host "== report revision $currentRevision published; pdf_available=$($current.pdf_available) pdf_status=$($current.pdf_status)"

    $script:eventBaseline = [long](Invoke-Db "select coalesce(max(seq),0) from event where channel = 'version:$versionId'")
    Write-Host "== export event baseline seq=$($script:eventBaseline)"

    # ---------------------------------------------------------------------
    # 3. Create the artifacts (HTML + Markdown first, PDF last).
    # ---------------------------------------------------------------------
    foreach ($format in @('html', 'markdown')) {
        $raw = Invoke-ApiRaw -Method 'POST' -Path "/api/versions/$versionId/reports/$reportId/artifacts" -Body @{ format = $format }
        if ($raw.CurlExit -ne 0 -or $raw.Status -ne 200) {
            $info = Get-ApiErrorInfo $raw
            throw "create $format artifact failed: HTTP $($raw.Status) $($info.code) $($info.message)"
        }
        $parsedBody = $raw.Text | ConvertFrom-Json
        $created = Add-CreatedArtifact $parsedBody.data
        Write-Host "== created $format artifact id=$($created.id) sha256=$($created.sha256) size=$($created.size) status=$($created.status)"
    }

    # PDF: the base revision may not have a published PDF. Attempt it anyway,
    # record the documented 409 path, and poll bounded for the render worker.
    $pdfDeadline = (Get-Date).AddSeconds($PdfWaitSeconds)
    $pdfDone = $false
    while ($true) {
        $verdict.pdf.attempts = [int]$verdict.pdf.attempts + 1
        $pdfRaw = Invoke-ApiRaw -Method 'POST' -Path "/api/versions/$versionId/reports/$reportId/artifacts" -Body @{ format = 'pdf' }
        if ($pdfRaw.CurlExit -eq 0 -and $pdfRaw.Status -eq 200) {
            $pdfBody = $pdfRaw.Text | ConvertFrom-Json
            $created = Add-CreatedArtifact $pdfBody.data
            Write-Host "== created pdf artifact id=$($created.id) sha256=$($created.sha256) size=$($created.size)"
            $pdfDone = $true
            $verdict.pdf.path = 'pdf_ready'
            break
        }
        $info = Get-ApiErrorInfo $pdfRaw
        $verdict.pdf.documented409 = ($pdfRaw.Status -eq 409)
        $verdict.pdf.path = 'pdf_not_ready'
        if ([string]::IsNullOrWhiteSpace($info.reason)) {
            $verdict.pdf.lastError = "HTTP $($pdfRaw.Status) $($info.code): $($info.message)"
        } else {
            $verdict.pdf.lastError = "HTTP $($pdfRaw.Status) $($info.code)/$($info.reason): $($info.message)"
        }
        if ($info.reason -eq 'pdf_failed') { $verdict.pdf.path = 'pdf_failed' }
        Write-Host "== pdf artifact attempt $($verdict.pdf.attempts): $($verdict.pdf.lastError)"
        if ([string]$info.code -ne 'pdf_not_ready' -and $info.reason -ne 'pdf_not_ready') { break }
        if ((Get-Date) -ge $pdfDeadline) { break }
        Start-Sleep -Seconds 5
        try {
            $latest = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/reports/$reportId/revisions/$currentRevision"
            $verdict.pdf.available = [bool]$latest.data.pdf_available
            $verdict.pdf.status = [string]$latest.data.pdf_status
        } catch {
            $script:errors.Add("refresh pdf state: $($_.Exception.Message)")
        }
    }

    $ready = @($script:createdArtifacts | Where-Object { $_.status -eq 'ready' })
    $formats = @($ready | ForEach-Object { $_.format })
    $verdict.formats = $formats
    Write-Host "== ready artifacts=$($ready.Count) formats=$($formats -join ',')"
    if (-not ($formats -contains 'html')) { throw 'html artifact is not ready' }
    if (-not ($formats -contains 'markdown')) { throw 'markdown artifact is not ready' }
    if ($ready.Count -lt 2) { throw "ready artifact formats=$($ready.Count), want at least 2" }
    if ($ready.Count -eq 0) { throw 'no ready artifacts were created' }

    $idCsv = (@($ready) | ForEach-Object { $_.id }) -join ','
    $shaCsv = (@($ready) | ForEach-Object { "'$($_.sha256)'" }) -join ','

    # ---------------------------------------------------------------------
    # 4. DB verification: export_artifact + content ledger + no binary cols.
    # ---------------------------------------------------------------------
    $rowRaw = Invoke-Db "select id, owner_kind, owner_id, revision, file_format, content_object_id, size_bytes, status, filename from export_artifact where id in ($idCsv) order by id"
    $rowsById = @{}
    foreach ($line in @($rowRaw -split "`r?`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line.Split('|')
        if ($parts.Count -lt 9) { throw "unexpected export_artifact row: $line" }
        $rowsById[$parts[0]] = [ordered]@{
            id = $parts[0]; owner_kind = $parts[1]; owner_id = $parts[2]; revision = $parts[3]
            format = $parts[4]; sha256 = $parts[5]; size = $parts[6]; status = $parts[7]; filename = $parts[8]
        }
    }
    foreach ($artifact in $ready) {
        if (-not $rowsById.ContainsKey($artifact.id)) { throw "export_artifact row $($artifact.id) is missing" }
        $row = $rowsById[$artifact.id]
        if ($row.owner_kind -ne 'report' -or $row.owner_id -ne $reportId) {
            throw "artifact $($artifact.id) owner=$($row.owner_kind)/$($row.owner_id), want report/$reportId"
        }
        if ($row.format -ne $artifact.format) { throw "artifact $($artifact.id) format=$($row.format), want $($artifact.format)" }
        if ($row.sha256 -ne $artifact.sha256) { throw "artifact $($artifact.id) sha256 mismatch" }
        if ([long]$row.size -ne [long]$artifact.size) { throw "artifact $($artifact.id) size=$($row.size), want $($artifact.size)" }
        if ($row.status -ne 'ready') { throw "artifact $($artifact.id) status=$($row.status), want ready" }
        Write-Host "== db export_artifact id=$($row.id) owner=$($row.owner_kind)/$($row.owner_id) revision=$($row.revision) format=$($row.format) sha256=$($row.sha256) size=$($row.size) status=$($row.status)"
    }

    $objectRaw = Invoke-Db "select sha256, size, mime, relative_path from content_object where sha256 in ($shaCsv)"
    $objectsBySha = @{}
    foreach ($line in @($objectRaw -split "`r?`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line.Split('|')
        if ($parts.Count -lt 4) { throw "unexpected content_object row: $line" }
        $objectsBySha[$parts[0]] = [ordered]@{ sha256 = $parts[0]; size = $parts[1]; mime = $parts[2]; relative_path = $parts[3] }
    }
    foreach ($artifact in $ready) {
        if (-not $objectsBySha.ContainsKey($artifact.sha256)) { throw "content_object $($artifact.sha256) is missing" }
        $object = $objectsBySha[$artifact.sha256]
        if ([long]$object.size -ne [long]$artifact.size) { throw "content_object $($artifact.sha256) size=$($object.size), want $($artifact.size)" }
        if ([string]::IsNullOrWhiteSpace($object.relative_path)) { throw "content_object $($artifact.sha256) has no relative_path" }
        $relative = $object.relative_path.Replace('/', [System.IO.Path]::DirectorySeparatorChar)
        $absolute = Join-Path $storageDir $relative
        if (-not (Test-Path -LiteralPath $absolute -PathType Leaf)) { throw "content_object file is missing on disk: $absolute" }
        Write-Host "== content_object sha256=$($object.sha256) size=$($object.size) mime=$($object.mime) path=$($object.relative_path)"
    }

    $refRaw = Invoke-Db "select owner_id, count(*) from content_ref where owner_kind = 'export' and owner_id in ($idCsv) and role = 'content' and deleted_at is null group by owner_id"
    $refsByOwner = @{}
    foreach ($line in @($refRaw -split "`r?`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line.Split('|')
        $refsByOwner[$parts[0]] = [int]$parts[1]
    }
    foreach ($artifact in $ready) {
        if (-not $refsByOwner.ContainsKey($artifact.id) -or $refsByOwner[$artifact.id] -ne 1) {
            throw "artifact $($artifact.id) has no active export content ref"
        }
    }
    Write-Host '== content ledger refs verified for every artifact'

    $binaryColumns = [int](Invoke-Db "select count(*) from information_schema.columns where table_schema = current_schema() and table_name in ('export_artifact', 'content_object') and data_type in ('bytea', 'blob')")
    if ($binaryColumns -ne 0) { throw "binary columns in export tables=$binaryColumns, want 0" }
    Write-Host '== no binary columns in export_artifact/content_object'

    # ---------------------------------------------------------------------
    # 5. Events: export.started -> export.completed/export.failed.
    # ---------------------------------------------------------------------
    $eventRaw = Invoke-Db "select seq, type, coalesce(envelope->'payload'->>'export_id',''), coalesce(envelope->'payload'->>'report_id',''), coalesce(envelope->'payload'->>'format',''), coalesce(envelope->'payload'->>'sha256',''), coalesce(envelope->'payload'->>'status','') from event where channel = 'version:$versionId' and seq > $($script:eventBaseline) and type like 'export.%' order by seq"
    $events = @()
    foreach ($line in @($eventRaw -split "`r?`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line.Split('|')
        if ($parts.Count -lt 7) { continue }
        $events += [ordered]@{
            seq = $parts[0]; type = $parts[1]; export_id = $parts[2]; report_id = $parts[3]
            format = $parts[4]; sha256 = $parts[5]; status = $parts[6]
        }
    }
    $started = @($events | Where-Object { $_.type -eq 'export.started' })
    $completed = @($events | Where-Object { $_.type -eq 'export.completed' })
    $failed = @($events | Where-Object { $_.type -eq 'export.failed' })
    $verdict.events = [ordered]@{ started = $started.Count; completed = $completed.Count; failed = $failed.Count }
    foreach ($artifact in $ready) {
        if (-not (@($started | Where-Object { $_.export_id -eq $artifact.id }).Count -ge 1)) {
            throw "export.started event for artifact $($artifact.id) is missing"
        }
        $done = @($completed | Where-Object { $_.export_id -eq $artifact.id })
        if ($done.Count -lt 1) { throw "export.completed event for artifact $($artifact.id) is missing" }
        if ($done[0].sha256 -ne $artifact.sha256) { throw "export.completed sha256 for artifact $($artifact.id) mismatch" }
        if ($done[0].report_id -ne $reportId) { throw "export.completed report_id for artifact $($artifact.id) mismatch" }
    }
    if ($started.Count -lt $ready.Count) { throw "export.started events=$($started.Count), want >= $($ready.Count)" }
    Write-Host "== export events started=$($started.Count) completed=$($completed.Count) failed=$($failed.Count)"

    # ---------------------------------------------------------------------
    # 6. API list + authenticated download 200 + sha256.
    # ---------------------------------------------------------------------
    $exportList = Invoke-Api -Method 'GET' -Path "/api/versions/$versionId/exports?limit=100"
    $listById = @{}
    foreach ($item in @($exportList.data)) { $listById[[string]$item.id] = $item }

    $downloads = @()
    $downloadAllOk = $true
    $curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
    if (-not $curlExe) { throw 'curl.exe is required for artifact downloads' }
    foreach ($artifact in $ready) {
        $view = Invoke-Api -Method 'GET' -Path "/api/exports/$($artifact.id)"
        if ([string]$view.data.sha256 -ne $artifact.sha256) { throw "export metadata sha256 mismatch for artifact $($artifact.id)" }
        if ([string]$view.data.format -ne $artifact.format) { throw "export metadata format mismatch for artifact $($artifact.id)" }
        if ([string]$view.data.owner_kind -ne 'report' -or [string]$view.data.owner_id -ne $reportId) { throw "export metadata owner mismatch for artifact $($artifact.id)" }

        if (-not $listById.ContainsKey($artifact.id)) { throw "artifact $($artifact.id) is missing from the version export list" }
        $listed = $listById[$artifact.id]
        if ([string]$listed.owner_kind -ne 'report' -or [string]$listed.owner_id -ne $reportId) { throw "version export list owner mismatch for artifact $($artifact.id)" }

        $extension = Get-ArtifactExtension $artifact.format
        $downloadPath = Join-Path $artifactDir "$($artifact.id)$extension"
        $headersPath = Join-Path $artifactDir "$($artifact.id).headers"
        $statusText = (& $curlExe -s -S -D $headersPath -o $downloadPath -w "%{http_code}" --max-time 120 -H "Authorization: Bearer $($script:token)" "$apiBase/api/exports/$($artifact.id)/download" 2>&1 | Out-String).Trim()
        $status = 0
        if ($statusText -match '([0-9]{3})\s*$') { $status = [int]$Matches[1] }
        $downloadSha = ''
        if (Test-Path -LiteralPath $downloadPath) {
            $downloadSha = (Get-FileHash -LiteralPath $downloadPath -Algorithm SHA256).Hash.ToLowerInvariant()
        }
        $headerSha = ''
        if (Test-Path -LiteralPath $headersPath) {
            $headerLine = Select-String -Path $headersPath -Pattern '^X-Content-SHA256:\s*(\S+)' | Select-Object -First 1
            if ($headerLine) { $headerSha = $headerLine.Matches[0].Groups[1].Value.Trim() }
        }
        $ok = ($status -eq 200 -and $downloadSha -eq $artifact.sha256)
        if (-not $ok) { $downloadAllOk = $false }
        $downloads += [ordered]@{
            id = $artifact.id; format = $artifact.format; status = $status
            sha256 = $downloadSha; headerSha256 = $headerSha; ok = $ok; path = $downloadPath
        }
        if (-not $ok) { throw "download artifact $($artifact.id) failed: HTTP $status sha=$downloadSha want=$($artifact.sha256)" }
        if ($headerSha -ne '' -and $headerSha -ne $artifact.sha256) { throw "download X-Content-SHA256 mismatch for artifact $($artifact.id)" }
        Write-Host "== download artifact id=$($artifact.id) HTTP=$status sha256=$downloadSha ok=$ok"
    }
    $verdict.downloads = $downloads
    $script:downloadOk = $downloadAllOk

    # ---------------------------------------------------------------------
    # 7. Optional screenshots of the report export/artifact/evidence UI.
    # ---------------------------------------------------------------------
    if (-not $SkipScreenshots) {
        try {
            $nodeExe = (Get-Command node -ErrorAction SilentlyContinue).Source
            if (-not $nodeExe) { throw 'node is missing' }
            $webUrl = $null
            $existingWeb = 'http://127.0.0.1:6871'
            if ((Get-HttpCode "$existingWeb/login") -eq '200' -and (Test-ApiReadyOn 13445)) {
                $webUrl = $existingWeb
                Write-Host '== reusing the W2 web UI on port 6871 for screenshots'
            } else {
                $webUrl = "http://127.0.0.1:$webPort"
                if ((Get-HttpCode "$webUrl/login") -ne '200') {
                    $runWeb = Join-Path $work 'run-web-w28.cmd'
                    $webLines = @(
                        '@echo off',
                        "cd /d `"$repoRoot\frontend`"",
                        "set NEXT_PUBLIC_API_BASE_URL=$apiBase",
                        "`"$nodeExe`" `"$repoRoot\frontend\node_modules\next\dist\bin\next`" dev -p $webPort >> `"$webLog`" 2>&1"
                    )
                    [System.IO.File]::WriteAllLines($runWeb, $webLines, (New-Object System.Text.UTF8Encoding($false)))
                    Write-Host '== starting EzTest web on port 6872 for screenshots'
                    $script:webStarted = $true
                    $null = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
                        CommandLine      = "cmd.exe /c `"$runWeb`""
                        CurrentDirectory = $work
                    }
                }
                $webDeadline = (Get-Date).AddSeconds(240)
                while ((Get-Date) -lt $webDeadline -and (Get-HttpCode "$webUrl/login") -ne '200') {
                    Start-Sleep -Seconds 2
                }
            }
            if ((Get-HttpCode "$webUrl/login") -ne '200') { throw 'web UI did not become ready' }

            $uiScript = @'
import path from "node:path"
import { createRequire } from "node:module"

const repo = process.env.W28_REPO
const web = process.env.W28_WEB
const versionId = process.env.W28_VERSION_ID
const reportId = process.env.W28_REPORT_ID
const out = process.env.W28_OUT
const adminPassword = process.env.W28_ADMIN_PW
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
  await page.goto(`${web}/versions/${versionId}?tab=reports&report=${reportId}`, { waitUntil: "domcontentloaded" })
  await page.waitForSelector('[data-testid="report-detail"]', { timeout: 120000 })
  try {
    await page.waitForSelector('[data-testid^="report-artifact-"]', { timeout: 90000 })
  } catch {
    console.log("artifact rows not found; screenshotting the report detail anyway")
  }
  await page.waitForTimeout(1200)
  await shot("01-report-artifacts.png")

  try {
    const evidence = page.locator('[data-testid="report-evidence"]')
    if (await evidence.count()) {
      await evidence.scrollIntoViewIfNeeded()
      await page.waitForSelector('[data-testid^="evidence-thumb-"]', { timeout: 90000 })
      await page.waitForTimeout(1200)
      await shot("02-report-evidence.png")
      await page.locator('[data-testid^="evidence-thumb-"]').first().click()
      await page.waitForSelector('[role="dialog"]', { timeout: 30000 })
      await page.waitForTimeout(1000)
      await shot("03-evidence-viewer.png")
    } else {
      console.log("report evidence section not found")
    }
  } catch (cause) {
    console.log(`evidence screenshots skipped: ${cause.message}`)
  }
} finally {
  await browser.close()
}
'@
            Write-Utf8 $uiScriptPath $uiScript
            $env:W28_REPO = $repoRoot
            $env:W28_WEB = $webUrl
            $env:W28_VERSION_ID = $versionId
            $env:W28_REPORT_ID = $reportId
            $env:W28_OUT = $exportDir
            $env:W28_ADMIN_PW = $adminPassword
            Write-Host '== capturing report export screenshots'
            & $nodeExe $uiScriptPath 2>&1 | Tee-Object -FilePath (Join-Path $exportDir 'ui.log')
            if ($LASTEXITCODE -ne 0) { $script:errors.Add('screenshot helper failed (optional)') }
        } catch {
            # Screenshots are optional evidence; never fail the export verdict.
            $script:errors.Add("screenshots skipped: $($_.Exception.Message)")
            Write-Host "== screenshots skipped: $($_.Exception.Message)"
        }
    }

    $verdict.screenshots = @(Get-ScreenshotFiles | ForEach-Object { "export/$($_.Name)" })
    Write-Host "== screenshots: $($verdict.screenshots -join ', ')"

    $preferred = @('html', 'markdown', 'pdf') | Where-Object { $formats -contains $_ }
    $verdict.formats = @($preferred)
    $verdict.status = 'PASS'
    Write-Host "== W2-8 export domain PASS formats=$($verdict.formats -join ',') artifacts=$($ready.Count)"
} catch {
    $script:errors.Add($_.Exception.Message)
    Write-Host "ERROR: $($_.Exception.Message)"
} finally {
    Stop-StartedServices
    Write-Verdict
    Write-Host "== verdict: $exportVerdictPath"
}

if ($verdict.status -ne 'PASS') {
    Write-Host '== W2-8 export domain FAIL'
    Write-Host ($verdict.errors -join ' | ')
    exit 1
}
exit 0
