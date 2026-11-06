#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 FA-6/FA-7 independent AC probes: R01-R06 (22 ACs) and
    R07-R12 (19 ACs).

.DESCRIPTION
    Each probe recomputes the SUT ground truth from independent sources:
      * SUT HTTP API (http://localhost:8080);
      * SUT PostgreSQL database (read-only SELECT via psql);
      * real Playwright UI operations through scripts/v022-fa-ui-check.mjs.

    Outputs:
      <EvidenceRoot>/<RunDir>/probes/<AcId>.json       per-AC probe ledger
      <EvidenceRoot>/<RunDir>/probes/<AcId>.raw.json   raw API/DB/UI evidence
      <EvidenceRoot>/<RunDir>/typical/screenshots/*.png
      <EvidenceRoot>/<RunDir>/acs-r01-r06.json         R01-R06 summary
      <EvidenceRoot>/<RunDir>/acs-r07-r12.json         R07-R12 summary

    Exit code:
      0  probe(s) executed and their results recorded (pass/fail/gap/blocked);
      1  the probe harness itself could not execute (SUT down, no admin account,
         missing node/Playwright, invalid evidence schema in self-check).

    Encoding rules: read .env/JSON as UTF-8; write JSON as UTF-8 without BOM.
    Secrets (SUT_PASSWORD, SUT_DB_DSN, bearer tokens) are never printed or
    written to evidence files.

.NOTES
    Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass `
            -File scripts\v022-fa-ac-probe.ps1 -AcId AC-R07-01 -RunDir RUN-1
        powershell.exe -NoProfile -ExecutionPolicy Bypass `
            -File scripts\v022-fa-ac-probe.ps1 -Range R01-R06 -RunDir RUN-1
        powershell.exe -NoProfile -ExecutionPolicy Bypass `
            -File scripts\v022-fa-ac-probe.ps1 -Range R07-R12 -RunDir RUN-1
#>

param(
    [string]$AcId,

    [ValidateSet('RUN-1', 'RUN-2')]
    [string]$RunDir = 'RUN-1',

    [ValidateSet('R01-R06', 'R07-R12')]
    [string]$Range = 'R01-R06',

    [string]$EvidenceRoot,

    [string]$SutApiBase = 'http://localhost:8080',

    [string]$SutWebBase = 'http://localhost:5173',

    [switch]$SelfCheck,

    [switch]$SummaryOnly
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

. (Join-Path $PSScriptRoot 'v022-fa-common.ps1')

$script:RepoRoot = $repoRoot
$script:Utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$script:SutApiBase = $SutApiBase.TrimEnd('/')
$script:SutWebBase = $SutWebBase.TrimEnd('/')
$script:RunDir = $RunDir

if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $script:EvidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final'
}
elseif ([System.IO.Path]::IsPathRooted($EvidenceRoot)) {
    $script:EvidenceRoot = [System.IO.Path]::GetFullPath($EvidenceRoot)
}
else {
    $script:EvidenceRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot $EvidenceRoot))
}

$script:ProbesDir = Join-Path $script:EvidenceRoot "$RunDir\probes"
$script:ScreenshotsDir = Join-Path $script:EvidenceRoot "$RunDir\typical\screenshots"
$script:LogsDir = Join-Path $script:EvidenceRoot 'logs'
$script:FixturesDir = Join-Path $repoRoot '.tmp\fa6\fixtures'
$script:SutWritesLog = Join-Path $repoRoot '.tmp\fa6\sut-writes.log'

foreach ($dir in @($script:ProbesDir, $script:ScreenshotsDir, $script:LogsDir, $script:FixturesDir)) {
    if (-not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
}

function Set-ProbeScope {
    param([Parameter(Mandatory = $true)][string]$RangeName)
    if ($RangeName -eq 'R07-R12') {
        $script:ActiveRange = 'R07-R12'
        $script:AcOrder = $script:R07R12AcOrder
        $script:ProbeFamily = 'FA7'
        $script:FixturesDir = Join-Path $script:RepoRoot '.tmp\fa7\fixtures'
        $script:SutWritesLog = Join-Path $script:RepoRoot '.tmp\fa7\sut-writes.log'
    }
    else {
        $script:ActiveRange = 'R01-R06'
        $script:AcOrder = $script:R01R06AcOrder
        $script:ProbeFamily = 'FA6'
        $script:FixturesDir = Join-Path $script:RepoRoot '.tmp\fa6\fixtures'
        $script:SutWritesLog = Join-Path $script:RepoRoot '.tmp\fa6\sut-writes.log'
    }
    foreach ($dir in @($script:FixturesDir, (Split-Path -Parent $script:SutWritesLog))) {
        if (-not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
    }
}

$script:R01R06AcOrder = @(
    'AC-R01-01', 'AC-R01-02', 'AC-R01-03',
    'AC-R02-01', 'AC-R02-02', 'AC-R02-03', 'AC-R02-04',
    'AC-R03-01', 'AC-R03-02', 'AC-R03-03', 'AC-R03-04',
    'AC-R04-01', 'AC-R04-02', 'AC-R04-03', 'AC-R04-04',
    'AC-R05-01', 'AC-R05-02', 'AC-R05-03', 'AC-R05-04',
    'AC-R06-01', 'AC-R06-02', 'AC-R06-03'
)

$script:R07R12AcOrder = @(
    'AC-R07-01', 'AC-R07-02', 'AC-R07-03',
    'AC-R08-01', 'AC-R08-02', 'AC-R08-03', 'AC-R08-04',
    'AC-R09-01', 'AC-R09-02', 'AC-R09-03',
    'AC-R10-01', 'AC-R10-02', 'AC-R10-03',
    'AC-R11-01', 'AC-R11-02', 'AC-R11-03',
    'AC-R12-01', 'AC-R12-02', 'AC-R12-03'
)

$script:AcOrder = $script:R01R06AcOrder
$script:ActiveRange = 'R01-R06'
$script:ProbeFamily = 'FA6'
$script:Fa7CreatedPosts = New-Object 'System.Collections.Generic.List[string]'
$script:Fa7CreatedTags = New-Object 'System.Collections.Generic.List[string]'

$script:AdminUser = $null
$script:AuthorUser = $null
$script:AdminToken = $null
$script:AuthorToken = $null
$script:SutPassword = $null
$script:R05PostId = $null
$script:R05PostTitle = $null
$script:R05TagNames = @()
$script:AcKinds = @{}
$script:InfraFailure = $false

# ---------------------------------------------------------------------------
# Probe state (reset for every AC).
# ---------------------------------------------------------------------------
function Reset-ProbeState {
    $script:Assertions = New-Object 'System.Collections.Generic.List[object]'
    $script:Evidence = New-Object 'System.Collections.Generic.List[object]'
    $script:Observed = New-Object 'System.Collections.Generic.List[string]'
    $script:Expected = New-Object 'System.Collections.Generic.List[string]'
    $script:UiScreenshots = New-Object 'System.Collections.Generic.List[string]'
    $script:GroundTruthOverride = $null
    $script:Blocked = $false
}

function Add-Assertion {
    param([string]$Kind, [bool]$Ok, [string]$Detail)
    $script:Assertions.Add([pscustomobject]@{ kind = $Kind; ok = $Ok; detail = $Detail })
}

function Add-EvidenceItem {
    param([string]$Kind, [string]$Label, $Data)
    $script:Evidence.Add([pscustomobject]@{ kind = $Kind; label = $Label; data = $Data })
}

function Add-Observed([string]$Text) {
    if (-not [string]::IsNullOrWhiteSpace($Text)) { $script:Observed.Add($Text) }
}

function Add-Expected([string]$Text) {
    if (-not [string]::IsNullOrWhiteSpace($Text)) { $script:Expected.Add($Text) }
}

function Add-SutWriteLog {
    param([string]$Action, [string]$Detail)
    $line = ('{0} | {1} | {2}' -f (Get-Date).ToString('o'), $Action, $Detail)
    [System.IO.File]::AppendAllText($script:SutWritesLog, $line + "`r`n", $script:Utf8NoBom)
}

# ---------------------------------------------------------------------------
# SUT API / DB / upload helpers. Secrets stay in memory only.
# ---------------------------------------------------------------------------
function Invoke-SutJson {
    param(
        [string]$Method = 'GET',
        [Parameter(Mandatory = $true)]
        [string]$Path,
        $Body = $null,
        [string]$Token = '',
        [int]$TimeoutSec = 60
    )

    Add-Type -AssemblyName System.Net.Http -ErrorAction SilentlyContinue
    $uri = $script:SutApiBase + $Path
    $status = 0
    $raw = ''
    $errorMessage = $null
    $client = $null
    $request = $null
    try {
        $client = New-Object System.Net.Http.HttpClient
        $client.Timeout = [TimeSpan]::FromSeconds($TimeoutSec)
        $request = New-Object System.Net.Http.HttpRequestMessage
        $request.Method = New-Object System.Net.Http.HttpMethod($Method.ToUpperInvariant())
        $request.RequestUri = [Uri]$uri
        if (-not [string]::IsNullOrWhiteSpace($Token)) {
            $request.Headers.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue('Bearer', $Token)
        }
        if ($null -ne $Body) {
            $json = if ($Body -is [string]) { $Body } else { $Body | ConvertTo-Json -Depth 20 -Compress }
            $request.Content = New-Object System.Net.Http.StringContent($json, [System.Text.Encoding]::UTF8, 'application/json')
        }
        $response = $client.SendAsync($request).Result
        try {
            $status = [int]$response.StatusCode
            $raw = [string]$response.Content.ReadAsStringAsync().Result
        }
        finally { $response.Dispose() }
    }
    catch {
        $errorMessage = $_.Exception.Message
        if ($null -ne $_.Exception.InnerException) { $errorMessage = $_.Exception.InnerException.Message }
        $status = 0
        $raw = ''
    }
    finally {
        if ($null -ne $request) { $request.Dispose() }
        if ($null -ne $client) { $client.Dispose() }
    }

    $parsed = $null
    if (-not [string]::IsNullOrWhiteSpace($raw)) {
        try { $parsed = $raw | ConvertFrom-Json } catch { $parsed = $null }
    }

    return [pscustomobject]@{
        Status = $status
        Raw    = $raw
        Body   = $parsed
        Error  = $errorMessage
        Ok     = ($status -ge 200 -and $status -lt 300)
    }
}

function Invoke-SutDb {
    param([Parameter(Mandatory = $true)][string]$Sql)

    $dsn = Get-EnvValue 'SUT_DB_DSN'
    $psql = Join-Path $script:RepoRoot '.tools\pgsql\bin\psql.exe'
    if (-not (Test-Path -LiteralPath $psql)) { throw 'psql.exe not found under .tools\pgsql\bin' }

    $output = & $psql $dsn -X -q -A -t -F '|' -v ON_ERROR_STOP=1 -c $Sql 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw 'SUT DB read-only query failed'
    }
    return @($output | ForEach-Object { [string]$_ })
}

function Invoke-SutUpload {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string]$Usage
    )

    if (-not (Test-Path -LiteralPath $FilePath)) { throw "missing upload fixture: $FilePath" }
    $uri = $script:SutApiBase + '/api/v1/uploads'
    $raw = & curl.exe -s -S -w "`n%{http_code}" -F "file=@$FilePath" -F "usage=$Usage" -H "Authorization: Bearer $($script:AdminToken)" $uri 2>$null
    $exitCode = $LASTEXITCODE
    $lines = @($raw)
    $status = 0
    $bodyText = ''
    if ($lines.Count -gt 0) {
        $last = [string]$lines[$lines.Count - 1]
        [void][int]::TryParse($last.Trim(), [ref]$status)
        if ($lines.Count -gt 1) { $bodyText = ($lines[0..($lines.Count - 2)] -join "`n") }
    }
    $parsed = $null
    if (-not [string]::IsNullOrWhiteSpace($bodyText)) { try { $parsed = $bodyText | ConvertFrom-Json } catch { } }
    return [pscustomobject]@{ Status = $status; Raw = $bodyText; Body = $parsed; CurlExit = $exitCode }
}

function Get-SutLoginToken {
    param([Parameter(Mandatory = $true)][string]$Account)
    $response = $null
    for ($attempt = 0; $attempt -lt 3; $attempt++) {
        $response = Invoke-SutJson -Method Post -Path '/api/v1/auth/login' -Body @{ account = $Account; password = $script:SutPassword }
        if ($response.Status -ne 429) { break }
        Start-Sleep -Seconds 61
    }
    if (-not $response.Ok -or $null -eq $response.Body -or $null -eq $response.Body.data -or
        [string]::IsNullOrWhiteSpace([string]$response.Body.data.accessToken)) {
        throw "SUT login failed for $Account (http=$($response.Status))"
    }
    Add-SutWriteLog 'login' "account=$Account http=$($response.Status) refresh-token-row-created"
    return [string]$response.Body.data.accessToken
}

function Invoke-SutLoginAttempt {
    param(
        [Parameter(Mandatory = $true)][string]$Account,
        [Parameter(Mandatory = $true)][string]$Password
    )
    $response = $null
    for ($attempt = 0; $attempt -lt 3; $attempt++) {
        $response = Invoke-SutJson -Method Post -Path '/api/v1/auth/login' -Body @{ account = $Account; password = $Password }
        if ($response.Status -ne 429) { break }
        Start-Sleep -Seconds 61
    }
    return $response
}

function Get-ObjValue {
    param($Object, [string[]]$Names)
    if ($null -eq $Object) { return $null }
    foreach ($name in $Names) {
        if ($Object -is [System.Collections.IDictionary]) {
            if ($Object.Contains($name)) { return $Object[$name] }
            continue
        }
        $property = $Object.PSObject.Properties[$name]
        if ($null -ne $property -and $null -ne $property.Value) { return $property.Value }
    }
    return $null
}

function Get-EvidenceRelativePath {
    param([string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path)) { return $null }
    try { $full = [System.IO.Path]::GetFullPath($Path) } catch { return $Path }
    $root = [System.IO.Path]::GetFullPath($script:EvidenceRoot)
    if ($full.StartsWith($root, [StringComparison]::OrdinalIgnoreCase)) {
        return $full.Substring($root.Length).TrimStart('\')
    }
    return $full
}

function Resolve-EvidencePath {
    param([string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path)) { return $null }
    $normalized = $Path -replace '/', '\'
    $candidates = New-Object System.Collections.Generic.List[string]
    if ([System.IO.Path]::IsPathRooted($normalized)) { $candidates.Add($normalized) }
    else {
        $candidates.Add((Join-Path $script:EvidenceRoot $normalized))
        $candidates.Add((Join-Path (Join-Path $script:EvidenceRoot $script:RunDir) $normalized))
        $candidates.Add((Join-Path $script:RepoRoot $normalized))
    }
    foreach ($candidate in $candidates) {
        if (Test-Path -LiteralPath $candidate) { return (Resolve-Path -LiteralPath $candidate).Path }
    }
    return $null
}

# ---------------------------------------------------------------------------
# Fixtures, preflight and accounts.
# ---------------------------------------------------------------------------
function Ensure-Fixtures {
    $png = Join-Path $script:FixturesDir 'fa6-upload-200k.png'
    if (-not (Test-Path -LiteralPath $png)) {
        Add-Type -AssemblyName System.Drawing
        $bitmap = New-Object System.Drawing.Bitmap(320, 240)
        try {
            $random = New-Object System.Random(7)
            for ($y = 0; $y -lt $bitmap.Height; $y++) {
                for ($x = 0; $x -lt $bitmap.Width; $x++) {
                    $bitmap.SetPixel($x, $y, [System.Drawing.Color]::FromArgb(255, $random.Next(256), $random.Next(256), $random.Next(256)))
                }
            }
            $bitmap.Save($png, [System.Drawing.Imaging.ImageFormat]::Png)
        }
        finally { $bitmap.Dispose() }
    }

    $zip = Join-Path $script:FixturesDir 'fa6-oversize.zip'
    if (-not (Test-Path -LiteralPath $zip)) {
        $stream = [System.IO.File]::Create($zip)
        try { $stream.SetLength(21MB) } finally { $stream.Close() }
    }

    $exe = Join-Path $script:FixturesDir 'fa6-unsupported.exe'
    if (-not (Test-Path -LiteralPath $exe)) {
        [System.IO.File]::WriteAllBytes($exe, [byte[]](1..64))
    }
}

function Initialize-SutContext {
    $script:SutPassword = Get-EnvValue 'SUT_PASSWORD'

    $health = Invoke-SutJson -Path '/api/v1/health'
    if (-not $health.Ok) { throw "SUT API is not reachable at $($script:SutApiBase) (http=$($health.Status))" }

    $web = $null
    try { $web = Invoke-WebRequest -Uri $script:SutWebBase -UseBasicParsing -TimeoutSec 15 }
    catch { throw "SUT web is not reachable at $($script:SutWebBase)" }
    if ([int]$web.StatusCode -ne 200) { throw "SUT web returned http=$($web.StatusCode)" }

    $selectOne = @(Invoke-SutDb 'select 1')
    if ($selectOne.Count -eq 0 -or $selectOne[0].Trim() -ne '1') { throw 'SUT DB is not reachable (select 1 failed)' }

    $adminRows = @(Invoke-SutDb "select username from blog.users where role in ('admin','super_admin') and is_active = true order by created_at limit 1")
    if ($adminRows.Count -eq 0 -or [string]::IsNullOrWhiteSpace($adminRows[0])) { throw 'SUT DB has no active admin account' }
    $script:AdminUser = $adminRows[0].Trim()

    $authorRows = @(Invoke-SutDb "select username from blog.users where role = 'author' and is_active = true order by created_at limit 1")
    if ($authorRows.Count -eq 0 -or [string]::IsNullOrWhiteSpace($authorRows[0])) { throw 'SUT DB has no active author account' }
    $script:AuthorUser = $authorRows[0].Trim()

    $script:AdminToken = Get-SutLoginToken -Account $script:AdminUser
    $script:AuthorToken = Get-SutLoginToken -Account $script:AuthorUser

    if ($script:ProbeFamily -eq 'FA6') { Ensure-Fixtures }

    if (-not (Get-Command node -ErrorAction SilentlyContinue)) { throw 'node is not available on PATH' }
}
function Get-FirstCategory {
    $response = Invoke-SutJson -Path '/api/v1/categories'
    if (-not $response.Ok -or $null -eq $response.Body -or @($response.Body.data).Count -eq 0) {
        throw 'SUT categories API returned no categories'
    }
    return @($response.Body.data)[0]
}

function Get-AllTags {
    $response = Invoke-SutJson -Path '/api/v1/tags'
    if (-not $response.Ok -or $null -eq $response.Body) { throw 'SUT tags API failed' }
    return @($response.Body.data)
}

# ---------------------------------------------------------------------------
# Fixture data created through the public product API (writes are logged).
# ---------------------------------------------------------------------------
function New-R04FixturePost {
    $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
    $category = Get-FirstCategory
    $title = "FA6 AC-R04-01 Markdown title hit $stamp"
    $slug = "fa6-probe-r04-markdown-$stamp"
    $body = @{
        title      = $title
        slug       = $slug
        summary    = 'FA6 R04 title-only search fixture'
        content    = "# FA6 R04-01`n`nThis body intentionally avoids the English keyword being searched."
        status     = 'published'
        categoryId = [string]$category.id
    }
    $response = Invoke-SutJson -Method Post -Path '/api/v1/posts' -Body $body -Token $script:AdminToken
    if (-not $response.Ok -or $null -eq $response.Body -or $null -eq $response.Body.data -or
        [string]::IsNullOrWhiteSpace([string]$response.Body.data.id)) {
        throw "failed to create the R04 fixture post (http=$($response.Status))"
    }
    $id = [string]$response.Body.data.id
    Add-SutWriteLog 'create-post' "AC-R04-01 id=$id slug=$slug status=published"
    return [pscustomobject]@{ Id = $id; Title = $title; Slug = $slug; Category = $category }
}

function New-R05FixturePost {
    $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
    $category = Get-FirstCategory
    $tags = @(Get-AllTags | Select-Object -First 2)
    if ($tags.Count -eq 0) { throw 'SUT has no tags for the R05 fixture' }

    $title = "FA6 AC-R05 article detail and TOC $stamp"
    $slug = "fa6-probe-r05-toc-$stamp"
    $content = @'
## 第一章 背景

FA6 R05 探针正文第一段。

## 第二章 数据表

| 项目 | 值 |
| --- | --- |
| AC | R05-01 |
| 结果 | 通过 |

## 第三章 示例代码

```js
console.log('FA6-R05')
```
'@
    $tagIds = @($tags | ForEach-Object { [string]$_.id })
    $tagNames = @($tags | ForEach-Object { [string]$_.name })
    $body = @{
        title      = $title
        slug       = $slug
        summary    = 'FA6 R05 detail/TOC fixture'
        content    = $content
        status     = 'published'
        categoryId = [string]$category.id
        tagIds     = $tagIds
    }
    $response = Invoke-SutJson -Method Post -Path '/api/v1/posts' -Body $body -Token $script:AdminToken
    if (-not $response.Ok -or $null -eq $response.Body -or $null -eq $response.Body.data -or
        [string]::IsNullOrWhiteSpace([string]$response.Body.data.id)) {
        throw "failed to create the R05 fixture post (http=$($response.Status))"
    }
    $id = [string]$response.Body.data.id
    Add-SutWriteLog 'create-post' "AC-R05 id=$id slug=$slug status=published tags=$($tagNames -join ',')"

    $script:R05PostId = $id
    $script:R05PostTitle = $title
    $script:R05TagNames = $tagNames
    return [pscustomobject]@{ Id = $id; Title = $title; Slug = $slug; TagNames = $tagNames; Category = $category }
}

function Get-OrCreateR05FixturePost {
    if (-not [string]::IsNullOrWhiteSpace($script:R05PostId)) {
        return [pscustomobject]@{ Id = $script:R05PostId; Title = $script:R05PostTitle; TagNames = $script:R05TagNames }
    }
    return New-R05FixturePost
}

function Get-R04ContentOnlyPost {
    $rows = @(Invoke-SutDb "select id from blog.posts where status = 'published' and review_status = 'approved' and content ilike U&'%\8349\7A3F%' and title not ilike U&'%\8349\7A3F%' and summary not ilike U&'%\8349\7A3F%' order by created_at desc limit 1")
    if ($rows.Count -eq 0 -or [string]::IsNullOrWhiteSpace([string]$rows[0])) { throw 'SUT has no content-only body fixture post' }
    return [pscustomobject]@{ Id = ([string]$rows[0]).Trim() }
}

function Get-NonPublicArticleFixture {
    $items = New-Object System.Collections.Generic.List[object]
    $draftId = @(Invoke-SutDb "select id from blog.posts where status = 'draft' and review_status <> 'rejected' order by created_at desc limit 1")
    if ($draftId.Count -gt 0 -and -not [string]::IsNullOrWhiteSpace([string]$draftId[0])) {
        $items.Add([pscustomobject]@{ Id = ([string]$draftId[0]).Trim(); Status = 'draft'; Title = '' })
    }
    $otherId = @(Invoke-SutDb "select id from blog.posts where status in ('scheduled','offline') order by created_at desc limit 1")
    if ($otherId.Count -gt 0 -and -not [string]::IsNullOrWhiteSpace([string]$otherId[0])) {
        $otherStatus = @(Invoke-SutDb "select status from blog.posts where id = '$(([string]$otherId[0]).Trim())'")
        $items.Add([pscustomobject]@{ Id = ([string]$otherId[0]).Trim(); Status = ([string]$otherStatus[0]).Trim(); Title = '' })
    }
    if ($items.Count -eq 0) { throw 'SUT has no non-public article fixture' }

    $adminPosts = Invoke-SutJson -Path '/api/v1/admin/posts?page=1&pageSize=100' -Token $script:AdminToken
    if ($adminPosts.Ok -and $null -ne $adminPosts.Body -and $null -ne $adminPosts.Body.data) {
        $all = @($adminPosts.Body.data.items)
        foreach ($item in $items) {
            $match = @($all | Where-Object { [string]$_.id -eq [string]$item.Id })
            if ($match.Count -gt 0) { $item.Title = [string]$match[0].title }
        }
    }
    return $items.ToArray()
}

function Get-PublishedArticleFixture {
    $rows = @(Invoke-SutDb "select id from blog.posts where status = 'published' and review_status = 'approved' order by created_at desc limit 1")
    if ($rows.Count -eq 0 -or [string]::IsNullOrWhiteSpace($rows[0])) { throw 'SUT has no published article fixture' }
    return [pscustomobject]@{ Id = $rows[0].Trim() }
}

# ---------------------------------------------------------------------------
# FA-7 fixtures (R07-R12). All writes go through the public product API and
# are recorded in .tmp/fa7/sut-writes.log; everything created here is removed
# again by Remove-Fa7Assets.
# ---------------------------------------------------------------------------
function New-Fa7Tag {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Slug
    )
    $response = Invoke-SutJson -Method Post -Path '/api/v1/admin/tags' -Body ([ordered]@{ name = $Name; slug = $Slug }) -Token $script:AdminToken
    if (-not $response.Ok -or $null -eq $response.Body -or $null -eq $response.Body.data -or
        [string]::IsNullOrWhiteSpace([string]$response.Body.data.id)) {
        throw "failed to create FA-7 tag $Slug (http=$($response.Status))"
    }
    $id = [string]$response.Body.data.id
    $script:Fa7CreatedTags.Add($id)
    Add-SutWriteLog 'create-tag' "id=$id name=$Name slug=$Slug"
    return [pscustomobject]@{ Id = $id; Name = $Name; Slug = $Slug }
}

function New-Fa7FixturePost {
    param(
        [Parameter(Mandatory = $true)][string]$Title,
        [Parameter(Mandatory = $true)][string]$Slug,
        [string]$Content = 'FA-7 probe fixture content.',
        [string]$Status = 'draft',
        [string[]]$TagIds = @(),
        [string]$CategoryId = '',
        [string]$Token = '',
        [string]$Summary = 'FA-7 probe fixture summary'
    )
    if ([string]::IsNullOrWhiteSpace($Token)) { $Token = $script:AdminToken }
    if ([string]::IsNullOrWhiteSpace($CategoryId)) {
        $category = Get-FirstCategory
        $CategoryId = [string]$category.id
    }
    $body = [ordered]@{
        title      = $Title
        slug       = $Slug
        summary    = $Summary
        content    = $Content
        status     = $Status
        categoryId = $CategoryId
        tagIds     = @($TagIds)
    }
    $response = Invoke-SutJson -Method Post -Path '/api/v1/posts' -Body $body -Token $Token
    if (-not $response.Ok -or $null -eq $response.Body -or $null -eq $response.Body.data -or
        [string]::IsNullOrWhiteSpace([string]$response.Body.data.id)) {
        throw "failed to create FA-7 fixture post $Slug (http=$($response.Status))"
    }
    $id = [string]$response.Body.data.id
    $script:Fa7CreatedPosts.Add($id)
    Add-SutWriteLog 'create-post' "id=$id slug=$Slug status=$Status tags=$(@($TagIds) -join ',')"
    return [pscustomobject]@{
        Id       = $id
        Title    = $Title
        Slug     = $Slug
        Status   = $Status
        Content  = $Content
        Response = $response.Body.data
    }
}

function Remove-Fa7Assets {
    try {
        $postIds = New-Object System.Collections.Generic.List[string]
        foreach ($id in @($script:Fa7CreatedPosts)) { if (-not [string]::IsNullOrWhiteSpace($id)) { $postIds.Add($id) } }
        $discoveredPosts = @(Invoke-SutDb "select id from blog.posts where slug like 'fa7-%' or title like 'FA7 %'")
        foreach ($row in $discoveredPosts) {
            $id = ([string]$row).Trim()
            if (-not [string]::IsNullOrWhiteSpace($id) -and -not $postIds.Contains($id)) { $postIds.Add($id) }
        }
        foreach ($id in @($postIds)) {
            $postRows = @(Invoke-SutDb "select json_build_object('title', title, 'slug', slug, 'summary', summary, 'content', content, 'categoryId', category_id, 'status', status, 'tagIds', '[]'::json)::text from blog.posts where id = '$id'")
            if ($postRows.Count -eq 1 -and -not [string]::IsNullOrWhiteSpace([string]$postRows[0])) {
                try {
                    $post = [string]$postRows[0] | ConvertFrom-Json
                    $categoryId = if ($null -eq $post.categoryId) { $null } else { [string]$post.categoryId }
                    $body = [ordered]@{
                        title      = [string]$post.title
                        slug       = [string]$post.slug
                        summary    = [string]$post.summary
                        content    = [string]$post.content
                        status     = [string]$post.status
                        categoryId = $categoryId
                        tagIds     = @()
                    }
                    $clear = Invoke-SutJson -Method Put -Path "/api/v1/posts/$id" -Body $body -Token $script:AdminToken
                    Add-SutWriteLog 'clear-post-tags' "id=$id http=$($clear.Status)"
                }
                catch {
                    Add-SutWriteLog 'clear-post-tags' "id=$id error=$($_.Exception.Message)"
                }
            }
            $response = Invoke-SutJson -Method Delete -Path "/api/v1/posts/$id" -Token $script:AdminToken
            Add-SutWriteLog 'delete-post' "id=$id http=$($response.Status)"
        }
        $script:Fa7CreatedPosts.Clear()

        $tagIds = New-Object System.Collections.Generic.List[string]
        foreach ($id in @($script:Fa7CreatedTags)) { if (-not [string]::IsNullOrWhiteSpace($id)) { $tagIds.Add($id) } }
        $discoveredTags = @(Invoke-SutDb "select id from blog.tags where name like 'FA7-%' or slug like 'fa7-%'")
        foreach ($row in $discoveredTags) {
            $id = ([string]$row).Trim()
            if (-not [string]::IsNullOrWhiteSpace($id) -and -not $tagIds.Contains($id)) { $tagIds.Add($id) }
        }
        foreach ($id in @($tagIds)) {
            $response = Invoke-SutJson -Method Delete -Path "/api/v1/admin/tags/$id" -Token $script:AdminToken
            Add-SutWriteLog 'delete-tag' "id=$id http=$($response.Status)"
        }
        $script:Fa7CreatedTags.Clear()
    }
    catch {
        Write-Host ("WARN: FA7 asset cleanup failed: " + $_.Exception.Message)
    }
}

function Get-Fa7DbCount {
    param([Parameter(Mandatory = $true)][string]$Sql)
    $rows = @(Invoke-SutDb $Sql)
    if ($rows.Count -eq 0 -or [string]::IsNullOrWhiteSpace([string]$rows[0])) { return -1 }
    $value = 0
    if ([int]::TryParse(([string]$rows[0]).Trim(), [ref]$value)) { return $value }
    return -1
}

function Test-RouteAbsent {
    param(
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Path,
        [string]$Token = '',
        $Body = $null
    )
    $response = Invoke-SutJson -Method $Method -Path $Path -Token $Token -Body $Body
    return [pscustomobject]@{
        Method   = $Method.ToUpperInvariant()
        Path     = $Path
        Status   = $response.Status
        Absent   = ($response.Status -eq 404 -or $response.Status -eq 405)
        Response = $response
    }
}

# ---------------------------------------------------------------------------
# Probe result writing.
# ---------------------------------------------------------------------------
function Write-ProbeResult {
    param(
        [Parameter(Mandatory = $true)][string]$AcId,
        [Parameter(Mandatory = $true)][string]$Kind,
        [Parameter(Mandatory = $true)][string]$Observed,
        [Parameter(Mandatory = $true)][string]$Expected
    )

    if ($script:Assertions.Count -lt 2) {
        Add-Assertion 'internal' $false "probe must provide at least 2 independent assertions (got $($script:Assertions.Count))"
    }

    $failed = @($script:Assertions | Where-Object { -not $_.ok })
    $groundTruth = 'present'
    $recomputed = 'pass'
    if ($script:Blocked) { $groundTruth = 'partial'; $recomputed = 'blocked' }
    elseif ($failed.Count -gt 0) { $groundTruth = 'partial'; $recomputed = 'fail' }
    if (-not [string]::IsNullOrWhiteSpace($script:GroundTruthOverride)) {
        $groundTruth = [string]$script:GroundTruthOverride
        if ($groundTruth -eq 'absent' -and $failed.Count -eq 0) { $recomputed = 'gap' }
    }

    $requirementId = [regex]::Match($AcId, '^AC-(R\d{2})-').Groups[1].Value
    $screenshotRel = @($script:UiScreenshots | ForEach-Object { Get-EvidenceRelativePath $_ } | Where-Object { $_ })

    $rawPath = Join-Path $script:ProbesDir "$AcId.raw.json"
    $rawObject = [ordered]@{
        acId        = $AcId
        generatedAt = (Get-Date).ToString('o')
        api         = @($script:Evidence | Where-Object { $_.kind -eq 'api' })
        db          = @($script:Evidence | Where-Object { $_.kind -eq 'db' })
        ui          = @($script:Evidence | Where-Object { $_.kind -eq 'ui' })
    }
    Write-JsonUtf8 -Path $rawPath -Object $rawObject

    $evidenceRel = New-Object System.Collections.Generic.List[string]
    $evidenceRel.Add((Get-EvidenceRelativePath $rawPath))
    foreach ($shot in $screenshotRel) { if ($shot) { $evidenceRel.Add([string]$shot) } }

    $assertionsArray = $script:Assertions.ToArray()
    $probeObject = [ordered]@{
        acId          = $AcId
        requirementId = $requirementId
        kind          = $Kind
        groundTruth   = $groundTruth
        recomputed    = $recomputed
        status        = $recomputed
        assertions    = $assertionsArray
        observed      = $Observed
        expected      = $Expected
        evidence      = @($evidenceRel | Select-Object -Unique)
        screenshots   = @($screenshotRel)
        generatedAt   = (Get-Date).ToString('o')
    }
    $probePath = Join-Path $script:ProbesDir "$AcId.json"
    Write-JsonUtf8 -Path $probePath -Object $probeObject

    Write-Host ("PROBE {0} groundTruth={1} recomputed={2}" -f $AcId, $groundTruth, $recomputed)
    return [pscustomobject]@{ Probe = $probeObject; Path = $probePath }
}

# ---------------------------------------------------------------------------
# Playwright UI check bridge.
# ---------------------------------------------------------------------------
function Invoke-UiCheck {
    param(
        [Parameter(Mandatory = $true)][string]$AcId,
        [hashtable]$Environment = @{}
    )

    $node = (Get-Command node -ErrorAction SilentlyContinue)
    if ($null -eq $node) { throw 'node is not available on PATH' }
    $Environment['FA6_ADMIN_TOKEN'] = $script:AdminToken
    $Environment['FA6_AUTHOR_TOKEN'] = $script:AuthorToken
    $Environment['FA7_ADMIN_TOKEN'] = $script:AdminToken
    $Environment['FA7_AUTHOR_TOKEN'] = $script:AuthorToken
    $Environment['FA7_ADMIN_USERNAME'] = $script:AdminUser
    $Environment['FA7_AUTHOR_USERNAME'] = $script:AuthorUser
    foreach ($key in $Environment.Keys) {
        Set-Item -Path ("env:" + $key) -Value ([string]$Environment[$key])
    }

    $scriptPath = Join-Path $script:RepoRoot 'scripts\v022-fa-ui-check.mjs'
    $stdout = & $node.Source $scriptPath $AcId $script:ScreenshotsDir 2>$null
    $exitCode = $LASTEXITCODE
    $jsonLine = @($stdout | Where-Object { $_ -and ([string]$_).TrimStart().StartsWith('{') }) | Select-Object -Last 1
    if (-not $jsonLine) {
        $script:InfraFailure = $true
        throw "UI check could not execute (exit=$exitCode, no JSON output)"
    }
    try { $result = $jsonLine | ConvertFrom-Json }
    catch {
        $script:InfraFailure = $true
        throw "UI check returned invalid JSON (exit=$exitCode)"
    }
    return [pscustomobject]@{ Result = $result; ExitCode = $exitCode }
}

function Use-UiCheck {
    param(
        [Parameter(Mandatory = $true)][string]$AcId,
        [hashtable]$Environment = @{}
    )

    $ui = Invoke-UiCheck -AcId $AcId -Environment $Environment
    $result = $ui.Result
    if ([bool](Get-ObjValue $result @('fatal'))) {
        $script:Blocked = $true
        $script:InfraFailure = $true
    }
    foreach ($item in @($result.assertions)) {
        if ($null -eq $item) { continue }
        Add-Assertion 'ui' ([bool](Get-ObjValue $item @('ok'))) ([string](Get-ObjValue $item @('detail')))
    }
    if (@($result.assertions).Count -eq 0) {
        Add-Assertion 'ui' ([bool](Get-ObjValue $result @('ok'))) ([string](Get-ObjValue $result @('observed')))
    }
    $shots = @()
    if ($null -ne (Get-ObjValue $result @('screenshots'))) { $shots = @(Get-ObjValue $result @('screenshots')) }
    elseif ($null -ne (Get-ObjValue $result @('screenshot'))) { $shots = @(Get-ObjValue $result @('screenshot')) }
    foreach ($shot in $shots) { if ($shot) { $script:UiScreenshots.Add([string]$shot) } }
    Add-EvidenceItem 'ui' 'playwright' $result
    return $result
}

# ---------------------------------------------------------------------------
# Small evidence helpers.
# ---------------------------------------------------------------------------
function Add-ApiEvidence {
    param([string]$Label, $Response, $Excerpt)
    Add-EvidenceItem 'api' $Label ([pscustomobject]@{ status = $Response.Status; excerpt = $Excerpt })
}

function Add-DbEvidence {
    param([string]$Label, [string]$Sql, $Rows)
    Add-EvidenceItem 'db' $Label ([pscustomobject]@{ sql = $Sql; rows = @($Rows) })
}

function Get-PostListExcerpt {
    param($Response)
    $items = @($Response.Body.data.items)
    return [pscustomobject]@{
        page       = $Response.Body.data.page
        pageSize   = $Response.Body.data.pageSize
        total      = $Response.Body.data.total
        totalPages = $Response.Body.data.totalPages
        count      = $items.Count
        ids        = @($items | ForEach-Object { [string]$_.id })
        titles     = @($items | ForEach-Object { [string]$_.title })
    }
}

# ---------------------------------------------------------------------------
# AC probe implementations.
# ---------------------------------------------------------------------------
function Invoke-AcLogic {
    param([Parameter(Mandatory = $true)][string]$AcId)

    switch ($AcId) {
        'AC-R01-01' {
            $me = Invoke-SutJson -Path '/api/v1/users/me' -Token $script:AdminToken
            Add-Assertion 'api' ($me.Ok -and [string]$me.Body.data.username -eq $script:AdminUser) "GET /users/me returns the authenticated admin account (http=$($me.Status))"
            Add-ApiEvidence 'users-me' $me ([pscustomobject]@{ username = $(if ($me.Ok) { [string]$me.Body.data.username } else { '' }); role = $(if ($me.Ok) { [string]$me.Body.data.role } else { '' }) })

            $sql = "select username, role, is_active from blog.users where username = '$($script:AdminUser)'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'active-admin-row' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0] -match '\|t$') 'SUT users row is active for the login account'

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "preflight admin session valid; UI login as $($script:AdminUser); users/me returns $($script:AdminUser)"
            Add-Expected 'modal closes, nav shows logged-in state, reload keeps the session'
        }

        'AC-R01-02' {
            $wrong = Invoke-SutLoginAttempt -Account $script:AdminUser -Password 'fa6-definitely-wrong-password'
            $missingAccount = 'fa6-no-such-user-' + (Get-Date -Format 'HHmmssfff')
            $missing = Invoke-SutLoginAttempt -Account $missingAccount -Password 'fa6-definitely-wrong-password'
            Add-Assertion 'api' ($wrong.Status -eq 401 -and $missing.Status -eq 401) "wrong password and unknown account both return HTTP 401 (wrong=$($wrong.Status), missing=$($missing.Status))"
            $message1 = [string](Get-ObjValue $wrong.Body @('message'))
            $message2 = [string](Get-ObjValue $missing.Body @('message'))
            Add-Assertion 'api' ($message1 -eq $message2 -and -not [string]::IsNullOrWhiteSpace($message1)) "failure message is uniform: '$message1'"
            Add-ApiEvidence 'wrong-password' $wrong ([pscustomobject]@{ message = $message1 })
            Add-ApiEvidence 'unknown-account' $missing ([pscustomobject]@{ account = $missingAccount; message = $message2 })

            $countSql = "select count(*) from blog.users where username = '$missingAccount'"
            $countRows = @(Invoke-SutDb $countSql)
            Add-DbEvidence 'unknown-account-absent' $countSql $countRows
            Add-Assertion 'db' ($countRows.Count -eq 1 -and $countRows[0].Trim() -eq '0') 'the unknown account does not exist in the SUT users table'

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "api wrong=$($wrong.Status) missing=$($missing.Status); uniform message='$message1'"
            Add-Expected 'both attempts show the same failure text and keep the account input'
        }

        'AC-R01-03' {
            $email = 'admin@example.com'
            $login = Invoke-SutLoginAttempt -Account $email -Password $script:SutPassword
            $ok = $login.Ok -and $null -ne $login.Body -and [string]$login.Body.data.user.email -eq $email
            Add-Assertion 'api' $ok "login with the bound email returns the admin user (http=$($login.Status))"
            Add-ApiEvidence 'email-login' $login ([pscustomobject]@{ username = $(if ($ok) { [string]$login.Body.data.user.username } else { '' }); email = $(if ($ok) { [string]$login.Body.data.user.email } else { '' }) })
            if ($ok) { Add-SutWriteLog 'login' "AC-R01-03 account=$email http=$($login.Status) refresh-token-row-created" }

            $sql = "select email, is_active from blog.users where username = '$($script:AdminUser)'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'bound-email-row' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0] -match [regex]::Escape($email) -and $rows[0] -match '\|t$') 'SUT users row binds that email to an active account'

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "email login http=$($login.Status); email=$email"
            Add-Expected 'email login succeeds and the account page shows the username and email'
        }

        'AC-R02-01' {
            $anonymous = Invoke-SutJson -Path '/api/v1/admin/posts?page=1&pageSize=1'
            Add-Assertion 'api' ($anonymous.Status -eq 401) "anonymous admin API request is rejected with HTTP 401 (got $($anonymous.Status))"
            Add-ApiEvidence 'anonymous-admin-api' $anonymous ([pscustomobject]@{ message = [string](Get-ObjValue $anonymous.Body @('message')) })
            $sql = "select count(*) from blog.users where role in ('admin','super_admin') and is_active = true"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'active-admin-count' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and [int]$rows[0].Trim() -ge 1) 'the protected admin area has at least one active admin account'

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "anonymous admin API=$($anonymous.Status); modal + home redirect"
            Add-Expected 'login modal opens and the visitor stays on the home page'
        }

        'AC-R02-02' {
            $adminPosts = Invoke-SutJson -Path '/api/v1/admin/posts?page=1&pageSize=1' -Token $script:AdminToken
            $count = if ($adminPosts.Ok) { @($adminPosts.Body.data.items).Count } else { 0 }
            Add-Assertion 'api' ($adminPosts.Ok -and $count -ge 1) "admin token can read the admin post list (http=$($adminPosts.Status))"
            Add-ApiEvidence 'admin-posts' $adminPosts ([pscustomobject]@{ page = $(if ($adminPosts.Ok) { $adminPosts.Body.data.page } else { $null }); count = $count })
            $sql = "select count(*) from blog.users where role in ('admin','super_admin') and is_active = true"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'active-admin-count' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and [int]$rows[0].Trim() -ge 1) 'the SUT still has an active admin identity for the redirect'

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "admin API=$($adminPosts.Status); UI login restores /admin"
            Add-Expected 'after admin login the browser automatically enters the admin area'
        }

        'AC-R02-03' {
            $post = Get-PublishedArticleFixture
            $beforeRows = @(Invoke-SutDb "select status from blog.posts where id = '$($post.Id)'")
            $before = [string]$beforeRows[0].Trim()
            $list = Invoke-SutJson -Path '/api/v1/admin/posts?page=1&pageSize=1' -Token $script:AuthorToken
            Add-Assertion 'api' ($list.Status -eq 403) "author token is rejected from the admin list with HTTP 403 (got $($list.Status))"
            $patch = Invoke-SutJson -Method Patch -Path "/api/v1/admin/posts/$($post.Id)/status" -Body @{ status = 'offline' } -Token $script:AuthorToken
            Add-Assertion 'api' ($patch.Status -eq 403) "author token is rejected from an admin write with HTTP 403 (got $($patch.Status))"
            Add-ApiEvidence 'author-admin-list' $list ([pscustomobject]@{ message = [string](Get-ObjValue $list.Body @('message')) })
            Add-ApiEvidence 'author-admin-write' $patch ([pscustomobject]@{ message = [string](Get-ObjValue $patch.Body @('message')); targetPost = $post.Id })
            $afterRows = @(Invoke-SutDb "select status from blog.posts where id = '$($post.Id)'")
            $after = [string]$afterRows[0].Trim()
            Add-DbEvidence 'post-status-before-after' "select status from blog.posts where id = '$($post.Id)'" @("before=$before", "after=$after")
            Add-Assertion 'db' ($before -eq $after -and $before -ne '') "the admin write was rejected and post status is unchanged ($before -> $after)"

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "author admin list=$($list.Status), write=$($patch.Status); status $before -> $after"
            Add-Expected 'author is redirected home and cannot change admin data'
        }

        'AC-R02-04' {
            $missing = Invoke-SutJson -Path ("/api/v1/fa6-nonexistent-" + (Get-Date -Format 'HHmmssfff'))
            Add-Assertion 'api' ($missing.Status -eq 404) "an unknown API route returns HTTP 404 (got $($missing.Status))"
            Add-ApiEvidence 'unknown-api-route' $missing ([pscustomobject]@{ message = [string](Get-ObjValue $missing.Body @('message')) })
            $sql = "select count(*) from blog.posts where status = 'published' and review_status = 'approved'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'home-has-public-posts' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and [int]$rows[0].Trim() -ge 1) 'the home page has at least one readable public post'

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "unknown API route=$($missing.Status); UI redirects unknown page to /"
            Add-Expected 'unknown UI route redirects to the home page'
        }

        'AC-R03-01' {
            $api = Invoke-SutJson -Path '/api/v1/posts?page=1&pageSize=5'
            $excerpt = if ($api.Ok) { Get-PostListExcerpt $api } else { $null }
            $total = if ($api.Ok) { [int]$excerpt.total } else { 0 }
            $pages = if ($api.Ok) { [int]$excerpt.totalPages } else { 0 }
            Add-Assertion 'api' ($api.Ok -and $excerpt.count -eq 5 -and [int]$excerpt.page -eq 1 -and $total -gt 5) "page 1 returns exactly 5 items, page=1, total=$total (http=$($api.Status))"
            Add-ApiEvidence 'posts-page-1' $api $excerpt
            $sql = "select count(*) from blog.posts where status = 'published' and review_status = 'approved'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'published-post-count' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and [int]$rows[0].Trim() -eq $total) "DB published/approved count equals the API total ($total)"

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "page1 items=5 total=$total pages=$pages"
            Add-Expected 'five cards, current page 1, and the total article count on the pagination'
        }

        'AC-R03-02' {
            $page1 = Invoke-SutJson -Path '/api/v1/posts?page=1&pageSize=5'
            $page2 = Invoke-SutJson -Path '/api/v1/posts?page=2&pageSize=5'
            $e1 = if ($page1.Ok) { Get-PostListExcerpt $page1 } else { $null }
            $e2 = if ($page2.Ok) { Get-PostListExcerpt $page2 } else { $null }
            $first1 = if ($e1) { [string]$e1.ids[0] } else { '' }
            $first2 = if ($e2) { [string]$e2.ids[0] } else { '' }
            Add-Assertion 'api' ($page1.Ok -and $page2.Ok -and $e1.count -eq 5 -and $e2.count -eq 5 -and $first1 -ne $first2) "page 2 returns the next 5 items (first $first1 -> $first2)"
            Add-ApiEvidence 'posts-page-1' $page1 $e1
            Add-ApiEvidence 'posts-page-2' $page2 $e2
            $sql = "select count(*) from blog.posts where status = 'published' and review_status = 'approved'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'published-post-count' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and [int]$rows[0].Trim() -ge 10) 'the SUT has enough public articles for a second page'

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "page1 first=$first1; page2 first=$first2"
            Add-Expected 'page 2 in the URL, items 6-10 shown, scroll back to the list top'
        }

        'AC-R03-03' {
            $api = Invoke-SutJson -Path '/api/v1/posts?page=99&pageSize=5'
            $lastPage = if ($api.Ok) { [int]$api.Body.data.page } else { 0 }
            $totalPages = if ($api.Ok) { [int]$api.Body.data.totalPages } else { 0 }
            Add-Assertion 'api' ($api.Ok -and $lastPage -eq $totalPages -and $lastPage -ge 1) "API clamps page=99 to the last page $totalPages (got $lastPage)"
            Add-ApiEvidence 'posts-page-99' $api ([pscustomobject]@{ requested = 99; page = $lastPage; totalPages = $totalPages; count = $(if ($api.Ok) { @($api.Body.data.items).Count } else { 0 }) })
            $sql = "select count(*) from blog.posts where status = 'published' and review_status = 'approved'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'published-post-count' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and [int]$rows[0].Trim() -gt 0) 'the SUT has public articles to clamp against'

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "API 99 -> $lastPage/$totalPages"
            Add-Expected 'input 99 is clamped to the last valid page and the list stays non-empty'
        }

        'AC-R03-04' {
            $category = Get-FirstCategory
            $api = Invoke-SutJson -Path ("/api/v1/posts?page=2&pageSize=5&category=" + [uri]::EscapeDataString([string]$category.slug))
            $items = if ($api.Ok) { @($api.Body.data.items) } else { @() }
            $allCategory = @($items | Where-Object {
                $c = $_.category
                $null -ne $c -and ([string]$c.slug -eq [string]$category.slug -or [string]$c.name -eq [string]$category.name -or [string]$c.id -eq [string]$category.id)
            }).Count -eq $items.Count
            Add-Assertion 'api' ($api.Ok -and $items.Count -eq 5 -and $allCategory) "category page 2 returns 5 items, all in $($category.name) (http=$($api.Status))"
            Add-ApiEvidence 'category-page-2' $api ([pscustomobject]@{ category = $category.slug; page = $(if ($api.Ok) { $api.Body.data.page } else { $null }); total = $(if ($api.Ok) { $api.Body.data.total } else { $null }); count = $items.Count; ids = @($items | ForEach-Object { [string]$_.id }) })
            $sql = "select count(*) from blog.posts p left join blog.categories c on c.id = p.category_id where p.status = 'published' and p.review_status = 'approved' and (c.slug = '$($category.slug)' or c.id::text = '$($category.id)')"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'category-published-count' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and [int]$rows[0].Trim() -eq [int]$api.Body.data.total) "DB category count equals the API total ($($api.Body.data.total))"

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "category=$($category.slug); page=2; items=$($items.Count)"
            Add-Expected 'URL keeps category and page; list only shows that category page'
        }

        'AC-R04-01' {
            $fixture = New-R04FixturePost
            $api = Invoke-SutJson -Path '/api/v1/posts?page=1&pageSize=20&q=markdown'
            $items = if ($api.Ok) { @($api.Body.data.items) } else { @() }
            $hit = @($items | Where-Object { [string]$_.id -eq $fixture.Id })
            $titleHit = $hit.Count -eq 1 -and ([string]$hit[0].title -match 'Markdown')
            Add-Assertion 'api' ($api.Ok -and $titleHit) "q=markdown returns the title-only fixture post (http=$($api.Status))"
            Add-ApiEvidence 'title-search' $api ([pscustomobject]@{ q = 'markdown'; total = $(if ($api.Ok) { $api.Body.data.total } else { $null }); fixtureId = $fixture.Id; hitTitles = @($hit | ForEach-Object { [string]$_.title }) })
            $sql = "select count(*) from blog.posts where id = '$($fixture.Id)' and status = 'published' and review_status = 'approved' and title ilike '%markdown%' and content not ilike '%markdown%' and summary not ilike '%markdown%'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'title-only-match' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0].Trim() -eq '1') 'the fixture matches on title only (content/summary exclude the keyword)'

            Use-UiCheck -AcId $AcId -Environment @{ FA6_R04_POST_ID = $fixture.Id } | Out-Null
            Add-Observed "fixture=$($fixture.Id); q=markdown total=$(if ($api.Ok) { $api.Body.data.total } else { 'n/a' })"
            Add-Expected 'lowercase search matches the Markdown title and the URL keeps q'
        }

        'AC-R04-02' {
            $fixture = Get-R04ContentOnlyPost
            $keyword = '草稿'
            $api = Invoke-SutJson -Path ("/api/v1/posts?page=1&pageSize=20&q=" + [uri]::EscapeDataString($keyword))
            $items = if ($api.Ok) { @($api.Body.data.items) } else { @() }
            $hit = @($items | Where-Object { [string]$_.id -eq $fixture.Id })
            Add-Assertion 'api' ($api.Ok -and $hit.Count -eq 1) "q=$keyword returns the content-only fixture post (http=$($api.Status))"
            Add-ApiEvidence 'body-search' $api ([pscustomobject]@{ q = $keyword; total = $(if ($api.Ok) { $api.Body.data.total } else { $null }); fixtureId = $fixture.Id; hitTitles = @($hit | ForEach-Object { [string]$_.title }) })
            $sql = "select count(*) from blog.posts where id = '$($fixture.Id)' and content ilike U&'%\8349\7A3F%' and title not ilike U&'%\8349\7A3F%' and summary not ilike U&'%\8349\7A3F%'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'body-only-match' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0].Trim() -eq '1') 'the fixture matches on body only (title/summary exclude the keyword)'

            Use-UiCheck -AcId $AcId -Environment @{ FA6_R04_CONTENT_POST_ID = $fixture.Id } | Out-Null
            Add-Observed "fixture=$($fixture.Id); q=$keyword total=$(if ($api.Ok) { $api.Body.data.total } else { 'n/a' })"
            Add-Expected 'content-only keyword appears in the result list'
        }

        'AC-R04-03' {
            $keyword = 'fa6-no-match-' + (Get-Date -Format 'HHmmssfff')
            $api = Invoke-SutJson -Path ("/api/v1/posts?page=1&pageSize=20&q=" + [uri]::EscapeDataString($keyword))
            Add-Assertion 'api' ($api.Ok -and [int]$api.Body.data.total -eq 0 -and @($api.Body.data.items).Count -eq 0) "unknown keyword returns an empty result set (http=$($api.Status), total=$(if ($api.Ok) { $api.Body.data.total } else { 'n/a' }))"
            Add-ApiEvidence 'empty-search' $api ([pscustomobject]@{ q = $keyword; total = $(if ($api.Ok) { $api.Body.data.total } else { $null }); count = $(if ($api.Ok) { @($api.Body.data.items).Count } else { 0 }) })
            $sql = "select count(*) from blog.posts where title ilike '%$keyword%' or summary ilike '%$keyword%' or content ilike '%$keyword%'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'no-match-count' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0].Trim() -eq '0') 'the DB has no article matching the unknown keyword'

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "q=$keyword total=0"
            Add-Expected 'empty state text, retained keyword, and a clear-filters action'
        }

        'AC-R04-04' {
            $category = Get-FirstCategory
            $api = Invoke-SutJson -Path ("/api/v1/posts?page=1&pageSize=20&q=markdown&category=" + [uri]::EscapeDataString([string]$category.slug))
            $items = if ($api.Ok) { @($api.Body.data.items) } else { @() }
            $allCategory = @($items | Where-Object {
                $c = $_.category
                $null -ne $c -and ([string]$c.slug -eq [string]$category.slug -or [string]$c.name -eq [string]$category.name -or [string]$c.id -eq [string]$category.id)
            }).Count -eq $items.Count
            Add-Assertion 'api' ($api.Ok -and $items.Count -gt 0 -and $allCategory) "combined q+category returns $($items.Count) items, all in $($category.name) (http=$($api.Status))"
            Add-ApiEvidence 'combined-search' $api ([pscustomobject]@{ q = 'markdown'; category = $category.slug; total = $(if ($api.Ok) { $api.Body.data.total } else { $null }); count = $items.Count; ids = @($items | ForEach-Object { [string]$_.id }) })
            $sql = "select count(*) from blog.posts p left join blog.categories c on c.id = p.category_id where p.status = 'published' and p.review_status = 'approved' and (p.title ilike '%markdown%' or p.summary ilike '%markdown%' or p.content ilike '%markdown%') and (c.slug = '$($category.slug)' or c.id::text = '$($category.id)')"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'combined-count' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and [int]$rows[0].Trim() -eq [int]$api.Body.data.total) "DB combined count equals the API total ($($api.Body.data.total))"

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "q=markdown category=$($category.slug); items=$($items.Count)"
            Add-Expected 'URL keeps both filters and every result satisfies both conditions'
        }

        'AC-R05-01' {
            $fixture = New-R05FixturePost
            $api = Invoke-SutJson -Path "/api/v1/posts/$($fixture.Id)"
            $content = if ($api.Ok) { [string]$api.Body.data.content } else { '' }
            $tagCount = if ($api.Ok) { @($api.Body.data.tags).Count } else { 0 }
            $h2Content = ([regex]::Matches($content, '(?m)^##\s')).Count
            $hasTable = $content.Contains('|')
            $hasCode = $content.Contains('```')
            Add-Assertion 'api' ($api.Ok -and [string]$api.Body.data.title -eq $fixture.Title -and $h2Content -ge 3 -and $hasTable -and $hasCode) "public detail returns title + 3 level-2 headings + table + code block (http=$($api.Status))"
            Add-ApiEvidence 'article-detail' $api ([pscustomobject]@{ id = $fixture.Id; title = $(if ($api.Ok) { [string]$api.Body.data.title } else { '' }); status = $(if ($api.Ok) { [string]$api.Body.data.status } else { '' }); reviewStatus = $(if ($api.Ok) { [string]$api.Body.data.reviewStatus } else { '' }); h2 = $h2Content; tags = $tagCount })
            $sql = "select status, review_status, author_id, regexp_count(content, '^## ', 1, 'n'), (content like '%|%'), (content like '%```%') from blog.posts where id = '$($fixture.Id)'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'article-row' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0] -match '^published\|approved\|' -and $rows[0] -match '\|3\|t\|t$') 'DB row is published/approved with 3 level-2 headings, a table and a code block'
            $tagSql = "select count(*) from blog.post_tags where post_id = '$($fixture.Id)'"
            $tagRows = @(Invoke-SutDb $tagSql)
            Add-DbEvidence 'article-tags' $tagSql $tagRows
            Add-Assertion 'db' ($tagRows.Count -eq 1 -and [int]$tagRows[0].Trim() -ge 1) 'DB row has at least one tag association'

            Use-UiCheck -AcId $AcId -Environment @{
                FA6_R05_POST_ID = $fixture.Id
                FA6_R05_TITLE = $fixture.Title
                FA6_R05_TAG_NAMES_JSON = ('[' + ((@($fixture.TagNames) | ForEach-Object { ([string]$_ | ConvertTo-Json -Compress) }) -join ',') + ']')
            } | Out-Null
            Add-Observed "R05 fixture=$($fixture.Id); h2=$h2Content; tags=$tagCount"
            Add-Expected 'title/author/category/tags/views/body, 3 TOC entries, table+code, no timestamps'
        }

        'AC-R05-02' {
            $fixture = Get-OrCreateR05FixturePost
            $api = Invoke-SutJson -Path "/api/v1/posts/$($fixture.Id)"
            $content = if ($api.Ok) { [string]$api.Body.data.content } else { '' }
            $h2 = ([regex]::Matches($content, '(?m)^##\s')).Count
            Add-Assertion 'api' ($api.Ok -and $h2 -ge 2) "article detail exposes at least 2 level-2 headings (h2=$h2, http=$($api.Status))"
            Add-ApiEvidence 'article-headings' $api ([pscustomobject]@{ id = $fixture.Id; h2 = $h2 })
            $sql = "select regexp_count(content, '^## ', 1, 'n') from blog.posts where id = '$($fixture.Id)'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'article-h2-count' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and [int]$rows[0].Trim() -eq $h2 -and [int]$rows[0].Trim() -ge 2) "DB heading count matches the API ($h2)"

            Use-UiCheck -AcId $AcId -Environment @{ FA6_R05_POST_ID = $fixture.Id } | Out-Null
            Add-Observed "fixture=$($fixture.Id); h2=$h2"
            Add-Expected 'clicking TOC item 2 scrolls to the corresponding heading'
        }

        'AC-R05-03' {
            $fixtures = @(Get-NonPublicArticleFixture)
            foreach ($fixture in $fixtures) {
                $api = Invoke-SutJson -Path "/api/v1/posts/$($fixture.Id)"
                Add-Assertion 'api' ($api.Status -eq 404) "anonymous detail for $($fixture.Status) article returns HTTP 404 (got $($api.Status))"
                Add-ApiEvidence ("nonpublic-$($fixture.Status)") $api ([pscustomobject]@{ id = $fixture.Id; status = $fixture.Status; message = [string](Get-ObjValue $api.Body @('message')) })
            }
            $sql = "select status, review_status from blog.posts where id = '$($fixtures[0].Id)'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'nonpublic-row' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0] -notmatch '^published\|approved') 'DB row is genuinely non-public'

            $envJson = '[' + ((@($fixtures) | ForEach-Object { ([pscustomobject]@{ id = $_.Id; title = $_.Title; status = $_.Status } | ConvertTo-Json -Compress -Depth 5) }) -join ',') + ']'
            Use-UiCheck -AcId $AcId -Environment @{ FA6_R05_NONPUBLIC_JSON = $envJson } | Out-Null
            Add-Observed ("non-public fixtures=" + (($fixtures | ForEach-Object { "$($_.Status):$($_.Id)" }) -join ','))
            Add-Expected 'article-not-found message shown; title/body not leaked'
        }

        'AC-R05-04' {
            $fixture = Get-OrCreateR05FixturePost
            $me = Invoke-SutJson -Path '/api/v1/users/me' -Token $script:AdminToken
            Add-Assertion 'api' ($me.Ok -and [string]$me.Body.data.role -eq 'admin') "admin session can read /users/me (http=$($me.Status), role=$(if ($me.Ok) { $me.Body.data.role } else { 'n/a' }))"
            Add-ApiEvidence 'admin-me' $me ([pscustomobject]@{ username = $(if ($me.Ok) { [string]$me.Body.data.username } else { '' }); role = $(if ($me.Ok) { [string]$me.Body.data.role } else { '' }) })
            $sql = "select p.author_id, u.role from blog.posts p join blog.users u on u.id = p.author_id where p.id = '$($fixture.Id)'"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'article-author-role' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0] -match '\|admin$') 'the article author is an admin'

            Use-UiCheck -AcId $AcId -Environment @{ FA6_R05_POST_ID = $fixture.Id } | Out-Null
            Add-Observed "fixture=$($fixture.Id); admin /users/me role=admin"
            Add-Expected 'admin sees the edit entry; anonymous visitors do not'
        }

        'AC-R06-01' {
            $png = Join-Path $script:FixturesDir 'fa6-upload-200k.png'
            $size = (Get-Item -LiteralPath $png).Length
            $upload = Invoke-SutUpload -FilePath $png -Usage 'post-content'
            $uploadOk = $upload.Status -eq 201 -and $null -ne $upload.Body -and $null -ne $upload.Body.data -and ([string]$upload.Body.data.url -match '^/uploads/.+\.png$')
            Add-Assertion 'api' $uploadOk "PNG upload to /uploads returns 201 with a site URL (http=$($upload.Status))"
            Add-ApiEvidence 'upload-png' $upload ([pscustomobject]@{ url = $(if ($uploadOk) { [string]$upload.Body.data.url } else { '' }); size = $(if ($uploadOk) { [int64]$upload.Body.data.size } else { 0 }); contentType = $(if ($uploadOk) { [string]$upload.Body.data.contentType } else { '' }) })
            if ($uploadOk) {
                $url = [string]$upload.Body.data.url
                Add-SutWriteLog 'upload' "AC-R06-01 usage=post-content url=$url bytes=$size"
                $sql = "select url, usage, size, content_type, path from blog.upload_assets where url = '$url'"
                $rows = @(Invoke-SutDb $sql)
                Add-DbEvidence 'upload-asset-row' $sql $rows
                Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0] -match [regex]::Escape($url) -and $rows[0] -match '\|post-content\|') 'DB upload_assets row records the post-content asset'
                $assetPath = if ($rows.Count -eq 1) { ($rows[0] -split '\|')[4] } else { '' }
                $assetCandidates = @($assetPath)
                if (-not [string]::IsNullOrWhiteSpace($assetPath)) {
                    $assetCandidates += (Join-Path 'D:\project\blog-backend' $assetPath)
                    $assetCandidates += (Join-Path 'D:\project\blog-backend\uploads' (Split-Path -Leaf $assetPath))
                }
                $assetExists = @($assetCandidates | Where-Object { $_ -and (Test-Path -LiteralPath $_) }).Count -gt 0
                Add-DbEvidence 'upload-asset-path' "select path from blog.upload_assets where url = '$url'" @([pscustomobject]@{ path = $assetPath; candidates = @($assetCandidates); exists = $assetExists })
                Add-Assertion 'db' ($assetExists) 'the uploaded file exists on the SUT filesystem'
            }
            else { Add-Assertion 'api' $false 'cannot verify the upload DB row because the API upload failed' }

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "upload http=$($upload.Status); bytes=$size"
            Add-Expected 'image upload inserts a markdown image reference at the cursor and previews it'
        }

        'AC-R06-02' {
            $zip = Join-Path $script:FixturesDir 'fa6-oversize.zip'
            $exe = Join-Path $script:FixturesDir 'fa6-unsupported.exe'
            $oversize = Invoke-SutUpload -FilePath $zip -Usage 'post-attachment'
            Add-Assertion 'api' ($oversize.Status -eq 400) "an oversized attachment is rejected with HTTP 400 (got $($oversize.Status))"
            Add-ApiEvidence 'oversize-upload' $oversize ([pscustomobject]@{ bytes = (Get-Item -LiteralPath $zip).Length; message = [string](Get-ObjValue $oversize.Body @('message')) })
            $unsupported = Invoke-SutUpload -FilePath $exe -Usage 'post-content'
            Add-Assertion 'api' ($unsupported.Status -eq 400) "an unsupported file type is rejected with HTTP 400 (got $($unsupported.Status))"
            Add-ApiEvidence 'unsupported-upload' $unsupported ([pscustomobject]@{ bytes = (Get-Item -LiteralPath $exe).Length; message = [string](Get-ObjValue $unsupported.Body @('message')) })
            $sql = "select count(*) from blog.upload_assets where original_name in ('fa6-oversize.zip','fa6-unsupported.exe')"
            $rows = @(Invoke-SutDb $sql)
            Add-DbEvidence 'rejected-upload-rows' $sql $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0].Trim() -eq '0') 'no DB upload_assets row was created for the rejected files'

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "oversize=$($oversize.Status); unsupported=$($unsupported.Status)"
            Add-Expected 'failure feedback shown; no invalid reference inserted'
        }

        'AC-R06-03' {
            $png = Join-Path $script:FixturesDir 'fa6-upload-200k.png'
            $upload = Invoke-SutUpload -FilePath $png -Usage 'post-cover'
            $uploadOk = $upload.Status -eq 201 -and $null -ne $upload.Body -and $null -ne $upload.Body.data -and ([string]$upload.Body.data.url -match '^/uploads/.+\.png$')
            Add-Assertion 'api' $uploadOk "cover upload to /uploads returns 201 with a site URL (http=$($upload.Status))"
            Add-ApiEvidence 'upload-cover' $upload ([pscustomobject]@{ url = $(if ($uploadOk) { [string]$upload.Body.data.url } else { '' }); usage = $(if ($uploadOk) { [string]$upload.Body.data.usage } else { '' }) })
            if ($uploadOk) {
                $url = [string]$upload.Body.data.url
                Add-SutWriteLog 'upload' "AC-R06-03 usage=post-cover url=$url"
                $sql = "select url, usage from blog.upload_assets where url = '$url'"
                $rows = @(Invoke-SutDb $sql)
                Add-DbEvidence 'cover-asset-row' $sql $rows
                Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0] -match '\|post-cover$') 'DB upload_assets row records the cover asset'
            }
            else { Add-Assertion 'api' $false 'cannot verify the cover DB row because the API upload failed' }

            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "cover upload http=$($upload.Status)"
            Add-Expected 'cover preview appears after upload and disappears after 清空封面'
        }

        'AC-R07-01' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $category = Get-FirstCategory
            $title = "FA7 AC-R07-01 publish $stamp"
            $slug = "fa7-r07-01-publish-$stamp"
            $ui = Use-UiCheck -AcId $AcId -Environment @{
                FA7_R07_TITLE       = $title
                FA7_R07_SLUG        = $slug
                FA7_R07_CATEGORY_ID = [string]$category.id
            }
            $rows = @(Invoke-SutDb "select id, status, review_status, (published_at is not null) from blog.posts where slug = '$slug'")
            Add-DbEvidence 'published-post-row' "select id, status, review_status, (published_at is not null) from blog.posts where slug = '$slug'" $rows
            $createdId = ''
            if ($rows.Count -eq 1) { $createdId = ([string]$rows[0] -split '\|')[0] }
            Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0] -match '^[0-9a-f-]+\|published\|approved\|t$') 'the UI publish created exactly one published/approved row with a publish time'
            if (-not [string]::IsNullOrWhiteSpace($createdId)) {
                $script:Fa7CreatedPosts.Add($createdId)
                $detail = Invoke-SutJson -Path "/api/v1/posts/$createdId"
                Add-Assertion 'api' ($detail.Ok -and [string]$detail.Body.data.status -eq 'published' -and $null -ne $detail.Body.data.publishedAt) "anonymous GET /posts/$createdId returns the published article (http=$($detail.Status))"
                Add-ApiEvidence 'published-detail' $detail ([pscustomobject]@{ id = $createdId; title = $(if ($detail.Ok) { [string]$detail.Body.data.title } else { '' }); status = $(if ($detail.Ok) { [string]$detail.Body.data.status } else { '' }); publishedAt = $(if ($detail.Ok) { [string]$detail.Body.data.publishedAt } else { '' }) })
            }
            else { Add-Assertion 'api' $false 'cannot verify the published article because the UI publish did not create a row' }
            Add-Observed "UI published '$title' as $slug; dbRows=$($rows.Count)"
            Add-Expected 'the page opens the public reading view without preview mark and anonymous visitors can read it'
        }

        'AC-R07-02' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $title = "FA7 AC-R07-02 published-at $stamp"
            $slug = "fa7-r07-02-published-at-$stamp"
            $bodyText = 'FA7 R07-02 original content.'
            $fixture = New-Fa7FixturePost -Title $title -Slug $slug -Content $bodyText -Status 'published' -Token $script:AuthorToken
            $beforeApi = [string]$fixture.Response.publishedAt
            $beforeDbRows = @(Invoke-SutDb "select published_at from blog.posts where id = '$($fixture.Id)'")
            $beforeDb = if ($beforeDbRows.Count -eq 1) { ([string]$beforeDbRows[0]).Trim() } else { '' }
            $updatedMarker = "FA7-R07-02-UPDATED-$stamp"
            $updateBody = [ordered]@{
                title      = $title
                slug       = $slug
                summary    = 'FA7 R07-02 updated summary'
                content    = "$bodyText`n`n$updatedMarker"
                status     = 'published'
                categoryId = [string]$fixture.Response.categoryId
                tagIds     = @()
            }
            $updated = Invoke-SutJson -Method Put -Path "/api/v1/posts/$($fixture.Id)" -Body $updateBody -Token $script:AuthorToken
            $afterApi = if ($updated.Ok) { [string]$updated.Body.data.publishedAt } else { '' }
            $afterDbRows = @(Invoke-SutDb "select published_at from blog.posts where id = '$($fixture.Id)'")
            $afterDb = if ($afterDbRows.Count -eq 1) { ([string]$afterDbRows[0]).Trim() } else { '' }
            Add-Assertion 'api' ($updated.Ok -and -not [string]::IsNullOrWhiteSpace($beforeApi) -and $beforeApi -eq $afterApi) "PUT /posts keeps first publishedAt unchanged ($beforeApi -> $afterApi, http=$($updated.Status))"
            Add-ApiEvidence 'edit-published' $updated ([pscustomobject]@{ id = $fixture.Id; status = $(if ($updated.Ok) { [string]$updated.Body.data.status } else { '' }); publishedAt = $afterApi; contentUpdated = ($updated.Ok -and ([string]$updated.Body.data.content).Contains($updatedMarker)) })
            Add-DbEvidence 'published-at-before-after' "select published_at from blog.posts where id = '$($fixture.Id)'" @("before=$beforeDb", "after=$afterDb")
            Add-Assertion 'db' (-not [string]::IsNullOrWhiteSpace($beforeDb) -and $beforeDb -eq $afterDb) "DB published_at is unchanged by the edit ($beforeDb -> $afterDb)"
            Add-Assertion 'api' ($updated.Ok -and ([string]$updated.Body.data.content).Contains($updatedMarker)) 'the edited content is persisted'
            Use-UiCheck -AcId $AcId -Environment @{
                FA7_R07_POST_ID       = $fixture.Id
                FA7_R07_UPDATED_MARKER = $updatedMarker
            } | Out-Null
            Add-Observed "fixture=$($fixture.Id); publishedAt=$beforeApi; updated=$($updated.Status)"
            Add-Expected 'first publish time stays T1 while the article content becomes the new content'
        }

        'AC-R07-03' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $category = Get-FirstCategory
            $title = "FA7 AC-R07-03 duplicate submit $stamp"
            $slug = "fa7-r07-03-duplicate-$stamp"
            $ui = Use-UiCheck -AcId $AcId -Environment @{
                FA7_R07_TITLE       = $title
                FA7_R07_SLUG        = $slug
                FA7_R07_CATEGORY_ID = [string]$category.id
            }
            $rows = @(Invoke-SutDb "select count(*) from blog.posts where slug = '$slug'")
            Add-DbEvidence 'single-row-per-slug' "select count(*) from blog.posts where slug = '$slug'" $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and [string]$rows[0].Trim() -eq '1') 'only one row exists for the publish attempt (no duplicate submit)'
            $idRows = @(Invoke-SutDb "select id from blog.posts where slug = '$slug'")
            if ($idRows.Count -eq 1 -and -not [string]::IsNullOrWhiteSpace([string]$idRows[0])) {
                $script:Fa7CreatedPosts.Add(([string]$idRows[0]).Trim())
            }
            else { Add-Assertion 'api' $false 'cannot verify the published row because the UI publish failed' }
            Add-Observed "UI publish with delayed save; slug=$slug rows=$($rows.Count)"
            Add-Expected 'save and publish buttons stay disabled while saving and only one save request is sent'
        }

        'AC-R08-01' {
            $draftApi = Invoke-SutJson -Path '/api/v1/admin/posts?page=1&pageSize=20&status=draft' -Token $script:AdminToken
            $draftDb = Get-Fa7DbCount "select count(*) from blog.posts where status = 'draft'"
            $draftTotal = if ($draftApi.Ok) { [int]$draftApi.Body.data.total } else { -1 }
            Add-Assertion 'api' ($draftApi.Ok -and $draftTotal -ge 0 -and $draftTotal -eq $draftDb) "admin status=draft total=$draftTotal equals DB draft count=$draftDb (http=$($draftApi.Status))"
            Add-ApiEvidence 'admin-status-draft' $draftApi ([pscustomobject]@{ status = 'draft'; page = $(if ($draftApi.Ok) { $draftApi.Body.data.page } else { $null }); total = $draftTotal; pageSize = $(if ($draftApi.Ok) { $draftApi.Body.data.pageSize } else { $null }) })
            Add-DbEvidence 'draft-count' "select count(*) from blog.posts where status = 'draft'" @($draftDb)

            $reviewApi = Invoke-SutJson -Path '/api/v1/admin/posts?page=1&pageSize=20&status=draft&reviewStatus=approved' -Token $script:AdminToken
            $reviewDb = Get-Fa7DbCount "select count(*) from blog.posts where status = 'draft' and review_status = 'approved'"
            $reviewTotal = if ($reviewApi.Ok) { [int]$reviewApi.Body.data.total } else { -1 }
            Add-Assertion 'api' ($reviewApi.Ok -and $reviewTotal -eq $reviewDb) "combined draft+approved total=$reviewTotal equals DB count=$reviewDb (http=$($reviewApi.Status))"
            Add-ApiEvidence 'admin-draft-approved' $reviewApi ([pscustomobject]@{ status = 'draft'; reviewStatus = 'approved'; page = $(if ($reviewApi.Ok) { $reviewApi.Body.data.page } else { $null }); total = $reviewTotal })
            Add-DbEvidence 'draft-approved-count' "select count(*) from blog.posts where status = 'draft' and review_status = 'approved'" @($reviewDb)

            Use-UiCheck -AcId $AcId -Environment @{
                FA7_R08_DRAFT_TOTAL  = [string]$draftTotal
                FA7_R08_REVIEW_TOTAL = [string]$reviewTotal
            } | Out-Null
            Add-Observed "draft total=$draftTotal; draft+approved total=$reviewTotal"
            Add-Expected 'choosing a status/review filter resets to page 1, narrows rows and keeps the pagination total in sync'
        }

        'AC-R08-02' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $keyword = "fa7r08kw$stamp"
            $title = "FA7 R08 auto filter $keyword"
            $slug = "fa7-r08-02-$keyword"
            $fixture = New-Fa7FixturePost -Title $title -Slug $slug -Content 'FA7 R08-02 debounce fixture.' -Status 'published'
            $api = Invoke-SutJson -Path ("/api/v1/admin/posts?page=1&pageSize=20&q=" + [uri]::EscapeDataString($keyword)) -Token $script:AdminToken
            $items = if ($api.Ok) { @($api.Body.data.items) } else { @() }
            $hit = @($items | Where-Object { [string]$_.id -eq $fixture.Id })
            Add-Assertion 'api' ($api.Ok -and $hit.Count -eq 1 -and [int]$api.Body.data.total -eq 1) "admin q=$keyword returns the fixture automatically (http=$($api.Status), total=$(if ($api.Ok) { $api.Body.data.total } else { 'n/a' }))"
            Add-ApiEvidence 'admin-keyword' $api ([pscustomobject]@{ q = $keyword; total = $(if ($api.Ok) { $api.Body.data.total } else { $null }); fixtureId = $fixture.Id })
            $rows = @(Invoke-SutDb "select count(*) from blog.posts where title ilike '%$keyword%' and status = 'published'")
            Add-DbEvidence 'keyword-row' "select count(*) from blog.posts where title ilike '%$keyword%' and status = 'published'" $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and [string]$rows[0].Trim() -eq '1') 'exactly one published fixture row matches the keyword'
            Use-UiCheck -AcId $AcId -Environment @{
                FA7_R08_KEYWORD = $keyword
                FA7_R08_POST_ID = $fixture.Id
            } | Out-Null
            Add-Observed "keyword=$keyword; fixture=$($fixture.Id)"
            Add-Expected 'the list refreshes automatically about 250ms after typing, without a manual search click'
        }

        'AC-R08-03' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $keyword = "fa7r08pv$stamp"
            $title = "FA7 R08 preview draft $keyword"
            $slug = "fa7-r08-03-$keyword"
            $marker = "FA7-R08-03-PREVIEW-$stamp"
            $fixture = New-Fa7FixturePost -Title $title -Slug $slug -Content "Draft body. $marker" -Status 'draft' -Token $script:AuthorToken
            $adminPreview = Invoke-SutJson -Path "/api/v1/posts/$($fixture.Id)?preview=1" -Token $script:AdminToken
            Add-Assertion 'api' ($adminPreview.Ok -and [string]$adminPreview.Body.data.title -eq $title) "admin preview GET returns the draft article (http=$($adminPreview.Status))"
            Add-ApiEvidence 'admin-preview' $adminPreview ([pscustomobject]@{ id = $fixture.Id; title = $(if ($adminPreview.Ok) { [string]$adminPreview.Body.data.title } else { '' }); status = $(if ($adminPreview.Ok) { [string]$adminPreview.Body.data.status } else { '' }) })
            $anonymousPreview = Invoke-SutJson -Path "/api/v1/posts/$($fixture.Id)?preview=1"
            Add-Assertion 'api' ($anonymousPreview.Status -eq 404 -or $anonymousPreview.Status -eq 403) "anonymous preview of the draft is refused with HTTP 403/404 (got $($anonymousPreview.Status))"
            $anonymousLeaksTitle = $anonymousPreview.Ok -and $null -ne $anonymousPreview.Body -and [string]$anonymousPreview.Body.data.title -eq $title
            Add-Assertion 'api' (-not $anonymousLeaksTitle) 'anonymous preview does not return the draft article content'
            Add-ApiEvidence 'anonymous-preview' $anonymousPreview ([pscustomobject]@{ id = $fixture.Id; message = [string](Get-ObjValue $anonymousPreview.Body @('message')) })
            $rows = @(Invoke-SutDb "select status, review_status from blog.posts where id = '$($fixture.Id)'")
            Add-DbEvidence 'preview-draft-row' "select status, review_status from blog.posts where id = '$($fixture.Id)'" $rows
            Add-Assertion 'db' ($rows.Count -eq 1 -and $rows[0] -match '^draft\|') 'the previewed article is genuinely a draft'
            Use-UiCheck -AcId $AcId -Environment @{
                FA7_R08_KEYWORD  = $keyword
                FA7_R08_DRAFT_ID = $fixture.Id
                FA7_R08_MARKER   = $marker
            } | Out-Null
            Add-Observed "draft=$($fixture.Id); adminPreview=$($adminPreview.Status); anonymousPreview=$($anonymousPreview.Status)"
            Add-Expected 'clicking 预览 opens the draft in preview mode and shows its unpublished content'
        }

        'AC-R08-04' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $title = "FA7 AC-R08-04 admin denial $stamp"
            $slug = "fa7-r08-04-$stamp"
            $fixture = New-Fa7FixturePost -Title $title -Slug $slug -Content 'FA7 R08-04 guard fixture.' -Status 'published'
            $list = Invoke-SutJson -Path '/api/v1/admin/posts?page=1&pageSize=1' -Token $script:AuthorToken
            Add-Assertion 'api' ($list.Status -eq 403) "author token is rejected from GET /admin/posts with HTTP 403 (got $($list.Status))"
            Add-ApiEvidence 'author-admin-list' $list ([pscustomobject]@{ message = [string](Get-ObjValue $list.Body @('message')) })
            $beforeRows = @(Invoke-SutDb "select status from blog.posts where id = '$($fixture.Id)'")
            $customStatus = @('offline', 'draft') | Where-Object { $_ -ne [string]$fixture.Status } | Select-Object -First 1
            $patch = Invoke-SutJson -Method Patch -Path "/api/v1/admin/posts/$($fixture.Id)/status" -Body @{ status = $customStatus } -Token $script:AuthorToken
            Add-Assertion 'api' ($patch.Status -eq 403) "author token is rejected from PATCH /admin/posts/{id}/status with HTTP 403 (got $($patch.Status))"
            Add-ApiEvidence 'author-admin-write' $patch ([pscustomobject]@{ targetPost = $fixture.Id; targetStatus = $customStatus; message = [string](Get-ObjValue $patch.Body @('message')) })
            $afterRows = @(Invoke-SutDb "select status from blog.posts where id = '$($fixture.Id)'")
            $before = if ($beforeRows.Count -eq 1) { ([string]$beforeRows[0]).Trim() } else { '' }
            $after = if ($afterRows.Count -eq 1) { ([string]$afterRows[0]).Trim() } else { '' }
            Add-DbEvidence 'guarded-post-status' "select status from blog.posts where id = '$($fixture.Id)'" @("before=$before", "after=$after")
            Add-Assertion 'db' ($before -eq 'published' -and $after -eq 'published') "the rejected admin write leaves the post status unchanged ($before -> $after)"
            Add-Assertion 'db' ((Get-Fa7DbCount "select count(*) from blog.users where role in ('admin','super_admin') and is_active = true") -ge 1) 'an active admin identity exists for the legitimate admin path'
            Use-UiCheck -AcId $AcId | Out-Null
            Add-Observed "author admin list=$($list.Status), write=$($patch.Status); status $before -> $after"
            Add-Expected 'non-admin users are denied and the admin article data stays hidden'
        }

        'AC-R09-01' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $tagA = New-Fa7Tag -Name "FA7-R09-A-$stamp" -Slug "fa7-r09-a-$stamp"
            $tagB = New-Fa7Tag -Name "FA7-R09-B-$stamp" -Slug "fa7-r09-b-$stamp"
            $post1 = New-Fa7FixturePost -Title "FA7 R09-01 tag A only $stamp" -Slug "fa7-r09-01-a-$stamp" -Content 'FA7 R09-01 A.' -Status 'published' -TagIds @($tagA.Id)
            $post2 = New-Fa7FixturePost -Title "FA7 R09-01 tag A and B $stamp" -Slug "fa7-r09-01-ab-$stamp" -Content 'FA7 R09-01 A+B.' -Status 'published' -TagIds @($tagA.Id, $tagB.Id)
            $apiPath = "/api/v1/posts?page=1&pageSize=20&tag=$([uri]::EscapeDataString($tagA.Slug))&tag=$([uri]::EscapeDataString($tagB.Slug))"
            $api = Invoke-SutJson -Path $apiPath
            $items = if ($api.Ok) { @($api.Body.data.items) } else { @() }
            $ids = @($items | ForEach-Object { [string]$_.id })
            $uniqueIds = @($ids | Select-Object -Unique)
            Add-Assertion 'api' ($api.Ok -and [int]$api.Body.data.total -eq 2 -and $ids.Count -eq 2 -and $uniqueIds.Count -eq 2 -and $ids -contains $post1.Id -and $ids -contains $post2.Id) "tag A ∪ B returns exactly the 2 unique fixture articles (http=$($api.Status), total=$(if ($api.Ok) { $api.Body.data.total } else { 'n/a' }))"
            Add-ApiEvidence 'tag-union' $api ([pscustomobject]@{ tagA = $tagA.Slug; tagB = $tagB.Slug; total = $(if ($api.Ok) { $api.Body.data.total } else { $null }); ids = $ids })
            $dbCount = Get-Fa7DbCount "select count(distinct p.id) from blog.posts p join blog.post_tags pt on pt.post_id = p.id join blog.tags t on t.id = pt.tag_id where p.status = 'published' and p.review_status = 'approved' and (t.slug in ('$($tagA.Slug)','$($tagB.Slug)') or t.name in ('$($tagA.Name)','$($tagB.Name)'))"
            Add-DbEvidence 'tag-union-count' "distinct published posts tagged A or B" @($dbCount)
            Add-Assertion 'db' ($dbCount -eq 2) "DB union of tags A and B has 2 distinct published articles (got $dbCount)"
            Use-UiCheck -AcId $AcId -Environment @{
                FA7_R09_TAG_A_SLUG = $tagA.Slug
                FA7_R09_TAG_B_SLUG = $tagB.Slug
                FA7_R09_TAG_A_NAME = $tagA.Name
                FA7_R09_TAG_B_NAME = $tagB.Name
                FA7_R09_POST_IDS   = ('[' + ((@($post1.Id, $post2.Id) | ForEach-Object { '"{0}"' -f $_ }) -join ',') + ']')
            } | Out-Null
            Add-Observed "tagA=$($tagA.Slug) tagB=$($tagB.Slug); union ids=$($ids -join ',')"
            Add-Expected 'mobile users can select A and B together and see the deduplicated union of 2 articles'
        }

        'AC-R09-02' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $tagC = New-Fa7Tag -Name "FA7-R09-C-$stamp" -Slug "fa7-r09-c-$stamp"
            $tagD = New-Fa7Tag -Name "FA7-R09-D-$stamp" -Slug "fa7-r09-d-$stamp"
            $post1 = New-Fa7FixturePost -Title "FA7 R09-02 tag C $stamp" -Slug "fa7-r09-02-c-$stamp" -Content 'FA7 R09-02 C.' -Status 'published' -TagIds @($tagC.Id)
            $post2 = New-Fa7FixturePost -Title "FA7 R09-02 tag D $stamp" -Slug "fa7-r09-02-d-$stamp" -Content 'FA7 R09-02 D.' -Status 'published' -TagIds @($tagD.Id)
            $allApi = Invoke-SutJson -Path '/api/v1/posts?page=1&pageSize=5'
            $allTotal = if ($allApi.Ok) { [int]$allApi.Body.data.total } else { -1 }
            $allDb = Get-Fa7DbCount "select count(*) from blog.posts where status = 'published' and review_status = 'approved'"
            Add-Assertion 'api' ($allApi.Ok -and $allTotal -eq $allDb) "unfiltered page-1 total=$allTotal equals DB public count=$allDb (http=$($allApi.Status))"
            Add-ApiEvidence 'all-public' $allApi ([pscustomobject]@{ total = $allTotal; page = $(if ($allApi.Ok) { $allApi.Body.data.page } else { $null }); count = $(if ($allApi.Ok) { @($allApi.Body.data.items).Count } else { 0 }) })
            Add-DbEvidence 'all-public-count' "select count(*) from blog.posts where status = 'published' and review_status = 'approved'" @($allDb)
            Use-UiCheck -AcId $AcId -Environment @{
                FA7_R09_TAG_C_SLUG = $tagC.Slug
                FA7_R09_TAG_D_SLUG = $tagD.Slug
                FA7_R09_ALL_TOTAL  = [string]$allTotal
                FA7_R09_POST_IDS   = ('[' + ((@($post1.Id, $post2.Id) | ForEach-Object { '"{0}"' -f $_ }) -join ',') + ']')
            } | Out-Null
            Add-Observed "selected tags C+D then cleared; allTotal=$allTotal"
            Add-Expected 'clearing all filters removes the selected tags and restores the full public article list'
        }

        'AC-R09-03' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $tagE = New-Fa7Tag -Name "FA7-R09-E-$stamp" -Slug "fa7-r09-e-$stamp"
            $post1 = New-Fa7FixturePost -Title "FA7 R09-03 duplicate one $stamp" -Slug "fa7-r09-03-one-$stamp" -Content 'FA7 R09-03 one.' -Status 'published' -TagIds @($tagE.Id)
            $post2 = New-Fa7FixturePost -Title "FA7 R09-03 duplicate two $stamp" -Slug "fa7-r09-03-two-$stamp" -Content 'FA7 R09-03 two.' -Status 'published' -TagIds @($tagE.Id)
            $path = "/api/v1/posts?page=1&pageSize=20&tag=$([uri]::EscapeDataString($tagE.Slug))&tags=$([uri]::EscapeDataString($tagE.Name)),$([uri]::EscapeDataString($tagE.Slug))"
            $api = Invoke-SutJson -Path $path
            $items = if ($api.Ok) { @($api.Body.data.items) } else { @() }
            $ids = @($items | ForEach-Object { [string]$_.id })
            $unique = @($ids | Select-Object -Unique)
            Add-Assertion 'api' ($api.Ok -and [int]$api.Body.data.total -eq 2 -and $ids.Count -eq 2 -and $unique.Count -eq 2) "duplicate tag conditions in different forms still return 2 unique articles (http=$($api.Status), total=$(if ($api.Ok) { $api.Body.data.total } else { 'n/a' }))"
            Add-ApiEvidence 'duplicate-tag-conditions' $api ([pscustomobject]@{ tag = $tagE.Slug; tags = "$($tagE.Name),$($tagE.Slug)"; total = $(if ($api.Ok) { $api.Body.data.total } else { $null }); ids = $ids })
            $dbCount = Get-Fa7DbCount "select count(*) from blog.posts p join blog.post_tags pt on pt.post_id = p.id join blog.tags t on t.id = pt.tag_id where p.status = 'published' and p.review_status = 'approved' and t.slug = '$($tagE.Slug)'"
            Add-DbEvidence 'single-tag-post-count' "select count(*) from blog.posts p join blog.post_tags pt on pt.post_id = p.id join blog.tags t on t.id = pt.tag_id where p.status = 'published' and p.review_status = 'approved' and t.slug = '$($tagE.Slug)'" @($dbCount)
            Add-Assertion 'db' ($dbCount -eq 2) "DB shows 2 published posts for tag E (got $dbCount)"
            Use-UiCheck -AcId $AcId -Environment @{
                FA7_R09_DUP_PATH  = ($path -replace '^/api/v1/posts', '/blog')
                FA7_R09_POST_IDS  = ('[' + ((@($post1.Id, $post2.Id) | ForEach-Object { '"{0}"' -f $_ }) -join ',') + ']')
                FA7_R09_EXPECTED  = '2'
            } | Out-Null
            Add-Observed "duplicate tag URL=$path; ids=$($ids -join ',')"
            Add-Expected 'repeated/equivalent tag conditions are merged and no article appears twice'
        }

        'AC-R10-01' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $title = "FA7 AC-R10-01 schedule probe $stamp"
            $slug = "fa7-r10-01-$stamp"
            $fixture = New-Fa7FixturePost -Title $title -Slug $slug -Content 'FA7 R10-01 scheduling probe.' -Status 'draft' -Token $script:AuthorToken
            $scheduledAt = (Get-Date).AddMinutes(2).ToString('o')
            $updateBody = [ordered]@{
                title       = $title
                slug        = $slug
                summary     = 'FA7 R10-01 schedule attempt'
                content     = 'FA7 R10-01 scheduling probe.'
                status      = 'scheduled'
                categoryId  = [string]$fixture.Response.categoryId
                tagIds      = @()
                scheduledAt = $scheduledAt
            }
            $update = Invoke-SutJson -Method Put -Path "/api/v1/posts/$($fixture.Id)" -Body $updateBody -Token $script:AuthorToken
            $hasScheduleField = $null -ne (Get-ObjValue $update.Body.data @('scheduledAt', 'scheduled_at', 'scheduleAt', 'publishAt', 'publish_at'))
            Add-Assertion 'api' (-not $hasScheduleField) "the post API response exposes no scheduled publish field (http=$($update.Status))"
            Add-ApiEvidence 'schedule-attempt' $update ([pscustomobject]@{ id = $fixture.Id; requestedStatus = 'scheduled'; requestedAt = $scheduledAt; responseStatus = $(if ($update.Ok) { [string]$update.Body.data.status } else { '' }); hasScheduleField = $hasScheduleField })
            $columnCount = Get-Fa7DbCount "select count(*) from information_schema.columns where table_schema = 'blog' and table_name = 'posts' and column_name in ('scheduled_at','scheduled_for','publish_at','schedule_at')"
            Add-DbEvidence 'schedule-columns' "select count(*) from information_schema.columns where table_schema='blog' and table_name='posts' and column_name in ('scheduled_at','scheduled_for','publish_at','schedule_at')" @($columnCount)
            Add-Assertion 'db' ($columnCount -eq 0) "the SUT posts table has no scheduled-publish timestamp column (got $columnCount)"
            $calendar1 = Test-RouteAbsent -Method Get -Path '/api/v1/admin/calendar' -Token $script:AdminToken
            $calendar2 = Test-RouteAbsent -Method Get -Path '/api/v1/admin/publish-calendar' -Token $script:AdminToken
            $calendar3 = Test-RouteAbsent -Method Get -Path '/api/v1/admin/posts/calendar' -Token $script:AdminToken
            Add-Assertion 'api' ($calendar1.Absent -and $calendar2.Absent -and $calendar3.Absent) "no publish-calendar route exists (calendar=$($calendar1.Status), publish-calendar=$($calendar2.Status), posts/calendar=$($calendar3.Status))"
            Add-ApiEvidence 'calendar-routes' $null ([pscustomobject]@{ calendar = $calendar1.Status; publishCalendar = $calendar2.Status; postsCalendar = $calendar3.Status })
            $ui = Use-UiCheck -AcId $AcId
            $uiOk = (@($ui.assertions | Where-Object { -not $_.ok }).Count -eq 0)
            if ($uiOk -and -not $hasScheduleField -and $columnCount -eq 0 -and $calendar1.Absent -and $calendar2.Absent -and $calendar3.Absent) {
                $script:GroundTruthOverride = 'absent'
            }
            Add-Observed "scheduledAt field present=$hasScheduleField; schedule columns=$columnCount; calendar routes=$($calendar1.Status)/$($calendar2.Status)/$($calendar3.Status)"
            Add-Expected 'author sets a future publish time, sees 预约发布 + the time, and visitors cannot see the scheduled article'
        }

        'AC-R10-02' {
            $columnCount = Get-Fa7DbCount "select count(*) from information_schema.columns where table_schema = 'blog' and table_name = 'posts' and column_name in ('scheduled_at','scheduled_for','publish_at','schedule_at')"
            Add-DbEvidence 'schedule-columns' "select count(*) from information_schema.columns where table_schema='blog' and table_name='posts' and column_name in ('scheduled_at','scheduled_for','publish_at','schedule_at')" @($columnCount)
            Add-Assertion 'db' ($columnCount -eq 0) "no scheduled-publish timestamp column exists to drive automatic publishing (got $columnCount)"
            $autoPublished = Get-Fa7DbCount "select count(*) from blog.posts where status = 'scheduled' and published_at is not null"
            Add-DbEvidence 'scheduled-auto-published' "select count(*) from blog.posts where status = 'scheduled' and published_at is not null" @($autoPublished)
            Add-Assertion 'db' ($autoPublished -eq 0) "no scheduled-status post has been automatically published (got $autoPublished)"
            $schedule1 = Test-RouteAbsent -Method Get -Path '/api/v1/admin/schedules' -Token $script:AdminToken
            $schedule2 = Test-RouteAbsent -Method Get -Path '/api/v1/admin/posts/scheduled' -Token $script:AdminToken
            $schedule3 = Test-RouteAbsent -Method Post -Path '/api/v1/admin/publish-check' -Token $script:AdminToken
            Add-Assertion 'api' ($schedule1.Absent -and $schedule2.Absent -and $schedule3.Absent) "no scheduler/publish-check route exists (schedules=$($schedule1.Status), scheduled=$($schedule2.Status), publish-check=$($schedule3.Status))"
            Add-ApiEvidence 'scheduler-routes' $null ([pscustomobject]@{ schedules = $schedule1.Status; scheduledPosts = $schedule2.Status; publishCheck = $schedule3.Status })
            $ui = Use-UiCheck -AcId $AcId
            $uiOk = (@($ui.assertions | Where-Object { -not $_.ok }).Count -eq 0)
            if ($uiOk -and $columnCount -eq 0 -and $autoPublished -eq 0 -and $schedule1.Absent -and $schedule2.Absent -and $schedule3.Absent) {
                $script:GroundTruthOverride = 'absent'
            }
            Add-Observed "schedule columns=$columnCount; auto-published scheduled rows=$autoPublished; routes=$($schedule1.Status)/$($schedule2.Status)/$($schedule3.Status)"
            Add-Expected 'at time T the article becomes published with first publish time T and becomes publicly readable'
        }

        'AC-R10-03' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $fixture = New-Fa7FixturePost -Title "FA7 AC-R10-03 cancel probe $stamp" -Slug "fa7-r10-03-$stamp" -Content 'FA7 R10-03 cancel probe.' -Status 'draft' -Token $script:AuthorToken
            $calendar = Test-RouteAbsent -Method Get -Path '/api/v1/admin/calendar' -Token $script:AdminToken
            $cancel1 = Test-RouteAbsent -Method Delete -Path "/api/v1/posts/$($fixture.Id)/schedule" -Token $script:AuthorToken
            $cancel2 = Test-RouteAbsent -Method Delete -Path "/api/v1/admin/posts/$($fixture.Id)/schedule" -Token $script:AdminToken
            Add-Assertion 'api' ($calendar.Absent -and $cancel1.Absent -and $cancel2.Absent) "no calendar or cancel-schedule route exists (calendar=$($calendar.Status), cancel=$($cancel1.Status), adminCancel=$($cancel2.Status))"
            Add-ApiEvidence 'calendar-cancel-routes' $null ([pscustomobject]@{ calendar = $calendar.Status; cancel = $cancel1.Status; adminCancel = $cancel2.Status })
            $tableCount = Get-Fa7DbCount "select count(*) from information_schema.tables where table_schema = 'blog' and table_name in ('schedules','schedule','publish_calendar','publishing_schedule','scheduled_posts')"
            Add-DbEvidence 'schedule-tables' "select count(*) from information_schema.tables where table_schema='blog' and table_name in ('schedules','schedule','publish_calendar','publishing_schedule','scheduled_posts')" @($tableCount)
            Add-Assertion 'db' ($tableCount -eq 0) "the SUT DB has no publish-calendar/schedule table (got $tableCount)"
            $ui = Use-UiCheck -AcId $AcId
            $uiOk = (@($ui.assertions | Where-Object { -not $_.ok }).Count -eq 0)
            if ($uiOk -and $calendar.Absent -and $cancel1.Absent -and $cancel2.Absent -and $tableCount -eq 0) {
                $script:GroundTruthOverride = 'absent'
            }
            Add-Observed "calendar=$($calendar.Status); cancel routes=$($cancel1.Status)/$($cancel2.Status); schedule tables=$tableCount"
            Add-Expected 'admin sees a 30-day publish calendar including the article; cancelling returns it to its pre-schedule state'
        }

        'AC-R11-01' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $fixture = New-Fa7FixturePost -Title "FA7 AC-R11-01 like probe $stamp" -Slug "fa7-r11-01-$stamp" -Content 'FA7 R11-01 like probe.' -Status 'published'
            $detail = Invoke-SutJson -Path "/api/v1/posts/$($fixture.Id)"
            $detailProps = if ($detail.Ok) { @($detail.Body.data.PSObject.Properties.Name) } else { @() }
            $hasLikeField = @($detailProps | Where-Object { $_ -match '^(likeCount|liked|like_count|likes)$' }).Count -gt 0
            Add-Assertion 'api' ($detail.Ok -and -not $hasLikeField) "the article detail exposes no like/like-count field (http=$($detail.Status))"
            Add-ApiEvidence 'article-detail-like-fields' $detail ([pscustomobject]@{ id = $fixture.Id; properties = $detailProps; hasLikeField = $hasLikeField })
            $like1 = Test-RouteAbsent -Method Post -Path "/api/v1/posts/$($fixture.Id)/like" -Token $script:AuthorToken
            $like2 = Test-RouteAbsent -Method Delete -Path "/api/v1/posts/$($fixture.Id)/like" -Token $script:AuthorToken
            Add-Assertion 'api' ($like1.Absent -and $like2.Absent) "no like/unlike route exists (like=$($like1.Status), unlike=$($like2.Status))"
            Add-ApiEvidence 'like-routes' $null ([pscustomobject]@{ like = $like1.Status; unlike = $like2.Status })
            $tableCount = Get-Fa7DbCount "select count(*) from information_schema.tables where table_schema = 'blog' and table_name in ('post_likes','likes','article_likes')"
            $columnCount = Get-Fa7DbCount "select count(*) from information_schema.columns where table_schema = 'blog' and table_name = 'posts' and column_name in ('like_count','likes_count')"
            Add-DbEvidence 'like-schema' "tables/columns for likes" @([pscustomobject]@{ tables = $tableCount; columns = $columnCount })
            Add-Assertion 'db' ($tableCount -eq 0 -and $columnCount -eq 0) "the DB has no like table or like-count column (tables=$tableCount, columns=$columnCount)"
            $ui = Use-UiCheck -AcId $AcId -Environment @{ FA7_R11_POST_ID = $fixture.Id }
            $uiOk = (@($ui.assertions | Where-Object { -not $_.ok }).Count -eq 0)
            if ($uiOk -and -not $hasLikeField -and $like1.Absent -and $like2.Absent -and $tableCount -eq 0 -and $columnCount -eq 0) {
                $script:GroundTruthOverride = 'absent'
            }
            Add-Observed "like fields=$hasLikeField; routes=$($like1.Status)/$($like2.Status); like tables=$tableCount"
            Add-Expected 'one click adds exactly one like, a second click does not increase the count, and the UI marks the user as having liked'
        }

        'AC-R11-02' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $fixture = New-Fa7FixturePost -Title "FA7 AC-R11-02 favorite probe $stamp" -Slug "fa7-r11-02-$stamp" -Content 'FA7 R11-02 favorite probe.' -Status 'published'
            $favorites = Test-RouteAbsent -Method Get -Path '/api/v1/users/me/favorites' -Token $script:AuthorToken
            $favorite1 = Test-RouteAbsent -Method Post -Path "/api/v1/posts/$($fixture.Id)/favorite" -Token $script:AuthorToken
            $favorite2 = Test-RouteAbsent -Method Delete -Path "/api/v1/posts/$($fixture.Id)/favorite" -Token $script:AuthorToken
            Add-Assertion 'api' ($favorites.Absent -and $favorite1.Absent -and $favorite2.Absent) "no favorites list/favorite/unfavorite route exists (list=$($favorites.Status), favorite=$($favorite1.Status), unfavorite=$($favorite2.Status))"
            Add-ApiEvidence 'favorite-routes' $null ([pscustomobject]@{ list = $favorites.Status; favorite = $favorite1.Status; unfavorite = $favorite2.Status })
            $tableCount = Get-Fa7DbCount "select count(*) from information_schema.tables where table_schema = 'blog' and table_name in ('favorites','post_favorites','user_favorites','favorite_posts')"
            Add-DbEvidence 'favorite-tables' "tables for favorites" @($tableCount)
            Add-Assertion 'db' ($tableCount -eq 0) "the DB has no favorites table (got $tableCount)"
            $ui = Use-UiCheck -AcId $AcId -Environment @{ FA7_R11_POST_ID = $fixture.Id }
            $uiOk = (@($ui.assertions | Where-Object { -not $_.ok }).Count -eq 0)
            if ($uiOk -and $favorites.Absent -and $favorite1.Absent -and $favorite2.Absent -and $tableCount -eq 0) {
                $script:GroundTruthOverride = 'absent'
            }
            Add-Observed "favorite routes=$($favorites.Status)/$($favorite1.Status)/$($favorite2.Status); favorite tables=$tableCount"
            Add-Expected '我的收藏 lists the 3 favorited articles newest-first'
        }

        'AC-R11-03' {
            $stamp = Get-Date -Format 'yyyyMMddHHmmssfff'
            $fixture = New-Fa7FixturePost -Title "FA7 AC-R11-03 progress probe $stamp" -Slug "fa7-r11-03-$stamp" -Content ("FA7 R11-03 reading progress probe.`n`n## Section`n`n" + ('Progress body. ' * 200)) -Status 'published'
            $progress1 = Test-RouteAbsent -Method Put -Path "/api/v1/posts/$($fixture.Id)/reading-progress" -Token $script:AuthorToken -Body @{ percent = 50 }
            $progress2 = Test-RouteAbsent -Method Get -Path '/api/v1/users/me/reading-progress' -Token $script:AuthorToken
            $progress3 = Test-RouteAbsent -Method Get -Path "/api/v1/posts/$($fixture.Id)/reading-progress" -Token $script:AuthorToken
            Add-Assertion 'api' ($progress1.Absent -and $progress2.Absent -and $progress3.Absent) "no reading-progress route exists (save=$($progress1.Status), list=$($progress2.Status), get=$($progress3.Status))"
            Add-ApiEvidence 'reading-progress-routes' $null ([pscustomobject]@{ save = $progress1.Status; list = $progress2.Status; get = $progress3.Status })
            $tableCount = Get-Fa7DbCount "select count(*) from information_schema.tables where table_schema = 'blog' and table_name in ('reading_progress','reading_positions','user_reading_progress','article_reading_progress')"
            Add-DbEvidence 'reading-progress-tables' "tables for reading progress" @($tableCount)
            Add-Assertion 'db' ($tableCount -eq 0) "the DB has no reading-progress table (got $tableCount)"
            $ui = Use-UiCheck -AcId $AcId -Environment @{ FA7_R11_POST_ID = $fixture.Id }
            $uiOk = (@($ui.assertions | Where-Object { -not $_.ok }).Count -eq 0)
            if ($uiOk -and $progress1.Absent -and $progress2.Absent -and $progress3.Absent -and $tableCount -eq 0) {
                $script:GroundTruthOverride = 'absent'
            }
            Add-Observed "progress routes=$($progress1.Status)/$($progress2.Status)/$($progress3.Status); progress tables=$tableCount"
            Add-Expected 'reopening the article offers to resume from the last saved reading position'
        }

        'AC-R12-01' {
            $suggest1 = Test-RouteAbsent -Method Get -Path '/api/v1/search/suggest?q=%E9%83%A8'
            $suggest2 = Test-RouteAbsent -Method Get -Path '/api/v1/posts/suggest?q=%E9%83%A8'
            $suggest3 = Test-RouteAbsent -Method Get -Path '/api/v1/suggest?q=%E9%83%A8'
            Add-Assertion 'api' ($suggest1.Absent -and $suggest2.Absent -and $suggest3.Absent) "no search-suggestion route exists (search/suggest=$($suggest1.Status), posts/suggest=$($suggest2.Status), suggest=$($suggest3.Status))"
            Add-ApiEvidence 'suggest-routes' $null ([pscustomobject]@{ searchSuggest = $suggest1.Status; postsSuggest = $suggest2.Status; suggest = $suggest3.Status })
            $tableCount = Get-Fa7DbCount "select count(*) from information_schema.tables where table_schema = 'blog' and table_name in ('search_suggestions','suggestions','search_keywords')"
            Add-DbEvidence 'suggestion-tables' "tables for search suggestions" @($tableCount)
            Add-Assertion 'db' ($tableCount -eq 0) "the DB has no search-suggestion table (got $tableCount)"
            $ui = Use-UiCheck -AcId $AcId
            $uiOk = (@($ui.assertions | Where-Object { -not $_.ok }).Count -eq 0)
            if ($uiOk -and $suggest1.Absent -and $suggest2.Absent -and $suggest3.Absent -and $tableCount -eq 0) {
                $script:GroundTruthOverride = 'absent'
            }
            Add-Observed "suggest routes=$($suggest1.Status)/$($suggest2.Status)/$($suggest3.Status); suggestion tables=$tableCount"
            Add-Expected 'typing 部 shows a suggestion containing 部署 and clicking it fills the box and runs the search'
        }

        'AC-R12-02' {
            $list = Invoke-SutJson -Path '/api/v1/posts?page=1&pageSize=10&q=Vue'
            $items = if ($list.Ok) { @($list.Body.data.items) } else { @() }
            $highlightFields = @()
            foreach ($item in $items) {
                foreach ($prop in @($item.PSObject.Properties.Name)) {
                    if ($prop -match 'highlight|matchedField|matchRanges') { $highlightFields += $prop }
                }
            }
            Add-Assertion 'api' ($list.Ok -and $highlightFields.Count -eq 0) "search results expose no highlight/match metadata (http=$($list.Status))"
            Add-ApiEvidence 'search-highlight-metadata' $list ([pscustomobject]@{ q = 'Vue'; total = $(if ($list.Ok) { $list.Body.data.total } else { $null }); highlightFields = @($highlightFields | Select-Object -Unique) })
            $columnCount = Get-Fa7DbCount "select count(*) from information_schema.columns where table_schema = 'blog' and table_name = 'posts' and column_name in ('highlight','search_highlight')"
            Add-DbEvidence 'highlight-columns' "select count(*) from information_schema.columns where table_schema='blog' and table_name='posts' and column_name in ('highlight','search_highlight')" @($columnCount)
            Add-Assertion 'db' ($columnCount -eq 0) "the SUT has no stored highlight column (got $columnCount)"
            $ui = Use-UiCheck -AcId $AcId
            $uiOk = (@($ui.assertions | Where-Object { -not $_.ok }).Count -eq 0)
            if ($uiOk -and $highlightFields.Count -eq 0 -and $columnCount -eq 0) {
                $script:GroundTruthOverride = 'absent'
            }
            Add-Observed "q=Vue results=$($items.Count); highlight fields=$(@($highlightFields | Select-Object -Unique).Count)"
            Add-Expected 'the matched keyword is visually highlighted in result titles and summaries'
        }

        'AC-R12-03' {
            $history1 = Test-RouteAbsent -Method Get -Path '/api/v1/search/history' -Token $script:AuthorToken
            $history2 = Test-RouteAbsent -Method Get -Path '/api/v1/users/me/search-history' -Token $script:AuthorToken
            $history3 = Test-RouteAbsent -Method Delete -Path '/api/v1/search/history' -Token $script:AuthorToken
            Add-Assertion 'api' ($history1.Absent -and $history2.Absent -and $history3.Absent) "no search-history route exists (get=$($history1.Status), me=$($history2.Status), clear=$($history3.Status))"
            Add-ApiEvidence 'search-history-routes' $null ([pscustomobject]@{ get = $history1.Status; me = $history2.Status; clear = $history3.Status })
            $tableCount = Get-Fa7DbCount "select count(*) from information_schema.tables where table_schema = 'blog' and table_name in ('search_history','search_histories','user_search_history','search_records')"
            Add-DbEvidence 'search-history-tables' "tables for search history" @($tableCount)
            Add-Assertion 'db' ($tableCount -eq 0) "the DB has no search-history table (got $tableCount)"
            $ui = Use-UiCheck -AcId $AcId
            $uiOk = (@($ui.assertions | Where-Object { -not $_.ok }).Count -eq 0)
            if ($uiOk -and $history1.Absent -and $history2.Absent -and $history3.Absent -and $tableCount -eq 0) {
                $script:GroundTruthOverride = 'absent'
            }
            Add-Observed "history routes=$($history1.Status)/$($history2.Status)/$($history3.Status); history tables=$tableCount"
            Add-Expected 'reopening the search box shows the last 10 searches and clearing removes them'
        }

        default { throw "unsupported AC: $AcId" }
    }
}

function Get-AcKind {
    param([string]$AcId)
    switch -Regex ($AcId) {
        '^AC-R04-0[124]$' { return 'api' }
        default { return 'ui' }
    }
}

function Get-AcExpected {
    param([string]$AcId)
    switch ($AcId) {
        'AC-R01-01' { return 'login succeeds, modal closes, session survives reload' }
        'AC-R01-02' { return 'wrong password and unknown account produce one uniform failure message' }
        'AC-R01-03' { return 'login with the bound email succeeds and shows the account' }
        'AC-R02-01' { return 'anonymous admin visit opens login modal and stays on home' }
        'AC-R02-02' { return 'after login the original admin page is restored' }
        'AC-R02-03' { return 'author is rejected from admin and data is unchanged' }
        'AC-R02-04' { return 'unknown route returns to home' }
        'AC-R03-01' { return '5 cards, page 1, total count visible' }
        'AC-R03-02' { return 'page 2 URL, items 6-10, scroll to list top' }
        'AC-R03-03' { return 'out-of-range page is clamped and list is valid' }
        'AC-R03-04' { return 'category + page coexist and results stay in category' }
        'AC-R04-01' { return 'case-insensitive title hit and q kept in URL' }
        'AC-R04-02' { return 'body-only keyword hit' }
        'AC-R04-03' { return 'empty state, retained keyword, clear action' }
        'AC-R04-04' { return 'search and category filters both apply' }
        'AC-R05-01' { return 'detail elements, 3 TOC entries, table/code, no timestamps' }
        'AC-R05-02' { return 'TOC item 2 scrolls to its heading' }
        'AC-R05-03' { return 'non-public article shows not-found and leaks nothing' }
        'AC-R05-04' { return 'admin sees edit entry; anonymous does not' }
        'AC-R06-01' { return 'image upload inserts reference at cursor' }
        'AC-R06-02' { return 'oversized/unsupported upload fails with feedback and no insertion' }
        'AC-R06-03' { return 'cover upload previews and clears' }
        'AC-R07-01' { return 'publishing opens the public reading page and visitors can read it' }
        'AC-R07-02' { return 'editing a published article keeps the first publish time and updates the content' }
        'AC-R07-03' { return 'save/publish buttons are disabled while saving and only one save is sent' }
        'AC-R08-01' { return 'status/review filters reset to page 1, narrow rows and keep the total in sync' }
        'AC-R08-02' { return 'typing a keyword auto-refreshes the admin list after about 250ms' }
        'AC-R08-03' { return 'admin preview opens an unpublished article and shows its content' }
        'AC-R08-04' { return 'non-admin users are denied the admin article area and data' }
        'AC-R09-01' { return 'mobile multi-tag selection shows the deduplicated union' }
        'AC-R09-02' { return 'clear filters removes all selected tags and restores the public list' }
        'AC-R09-03' { return 'duplicate tag conditions are merged without duplicate articles' }
        'AC-R10-01' { return 'future publish stores a scheduled time and hides the article while scheduled' }
        'AC-R10-02' { return 'the scheduled article is automatically published at T with first publish time T' }
        'AC-R10-03' { return 'admin publish calendar shows future schedules and cancellation restores the draft' }
        'AC-R11-01' { return 'repeated likes count once and the UI marks the user as having liked' }
        'AC-R11-02' { return '我的收藏 lists favorited articles newest-first' }
        'AC-R11-03' { return 'reopening an article offers to resume from the last reading position' }
        'AC-R12-01' { return 'typing shows up to 8 suggestions from public article titles/tags' }
        'AC-R12-02' { return 'matched keywords are highlighted in result titles and summaries' }
        'AC-R12-03' { return 'the last 10 searches are shown and can be cleared' }
        default { return '' }
    }
}

function Invoke-ForAc {
    param([Parameter(Mandatory = $true)][string]$AcId)
    Reset-ProbeState
    $kind = Get-AcKind $AcId
    $expected = Get-AcExpected $AcId
    try {
        Invoke-AcLogic -AcId $AcId
    }
    catch {
        $script:Blocked = $true
        Add-Assertion 'internal' $false ("probe could not complete: " + $_.Exception.Message)
    }
    $observed = ($script:Observed -join '; ')
    if ([string]::IsNullOrWhiteSpace($observed)) { $observed = 'see assertions' }
    if ([string]::IsNullOrWhiteSpace($expected)) { $expected = $AcId }
    return (Write-ProbeResult -AcId $AcId -Kind $kind -Observed $observed -Expected $expected)
}

# ---------------------------------------------------------------------------
# eztestVerdict join (case-runs.json; FA-5/FA-14 supplies it).
# ---------------------------------------------------------------------------
function Get-EztestVerdicts {
    param([string[]]$RequirementIds = $null)
    if ($null -eq $RequirementIds -or $RequirementIds.Count -eq 0) {
        $RequirementIds = @('R01', 'R02', 'R03', 'R04', 'R05', 'R06')
    }
    $verdicts = @{}
    foreach ($rid in $RequirementIds) { $verdicts[$rid] = 'missing' }

    $path = Join-Path $script:EvidenceRoot "$RunDir\typical\case-runs.json"
    if (-not (Test-Path -LiteralPath $path)) { return $verdicts }

    try { $json = Read-JsonUtf8 -Path $path } catch { return $verdicts }
    $runs = @()
    if ($json -is [System.Array]) { $runs = @($json) }
    else {
        foreach ($name in @('runs', 'items', 'caseRuns', 'case_runs', 'records')) {
            $value = Get-ObjValue $json @($name)
            if ($null -ne $value) { $runs = @($value); break }
        }
    }
    if ($runs.Count -eq 0) { return $verdicts }

    $groups = @{}
    foreach ($run in $runs) {
        if ($null -eq $run) { continue }
        $status = [string](Get-ObjValue $run @('status'))
        if ([string]::IsNullOrWhiteSpace($status)) { continue }
        $rid = [string](Get-ObjValue $run @('requirementId', 'requirement_id', 'requirementExternalId', 'requirement_external_id'))
        if ([string]::IsNullOrWhiteSpace($rid)) {
            $requirement = Get-ObjValue $run @('requirement')
            if ($null -ne $requirement) { $rid = [string](Get-ObjValue $requirement @('id', 'externalId', 'external_id')) }
        }
        if ([string]::IsNullOrWhiteSpace($rid)) {
            $case = Get-ObjValue $run @('case')
            if ($null -ne $case) {
                $requirement = Get-ObjValue $case @('requirement')
                if ($null -ne $requirement) { $rid = [string](Get-ObjValue $requirement @('id', 'externalId', 'external_id')) }
                if ([string]::IsNullOrWhiteSpace($rid)) { $rid = [string](Get-ObjValue $case @('requirementId', 'requirement_id')) }
            }
        }
        if ([string]::IsNullOrWhiteSpace($rid)) {
            $caseVersion = Get-ObjValue $run @('caseVersion', 'case_version')
            if ($null -ne $caseVersion) {
                $case = Get-ObjValue $caseVersion @('case')
                if ($null -ne $case) {
                    $requirement = Get-ObjValue $case @('requirement')
                    if ($null -ne $requirement) { $rid = [string](Get-ObjValue $requirement @('id', 'externalId', 'external_id')) }
                }
            }
        }
        if ($rid -match '(R\d{2})') { $rid = $Matches[1] }
        if ([string]::IsNullOrWhiteSpace($rid)) { continue }
        if (-not $groups.ContainsKey($rid)) { $groups[$rid] = New-Object 'System.Collections.Generic.List[string]' }
        $groups[$rid].Add($status.ToLowerInvariant())
    }

    foreach ($rid in @($groups.Keys)) {
        $statuses = @($groups[$rid])
        if ($statuses.Count -eq 0) { continue }
        if (@($statuses | Where-Object { $_ -in @('failed', 'fail', 'error') }).Count -gt 0) { $verdicts[$rid] = 'fail' }
        elseif (@($statuses | Where-Object { $_ -eq 'blocked' }).Count -gt 0) { $verdicts[$rid] = 'blocked' }
        elseif (@($statuses | Where-Object { $_ -in @('passed', 'pass', 'succeeded', 'success') }).Count -eq $statuses.Count) { $verdicts[$rid] = 'pass' }
        else { $verdicts[$rid] = 'missing' }
    }
    return $verdicts
}

# ---------------------------------------------------------------------------
# R01-R06 summary + self-check.
# ---------------------------------------------------------------------------
function Get-DerivedRequirementStatus {
    param($Statuses)
    $list = @($Statuses)
    if ($list -contains 'fail') { return 'fail' }
    if ($list -contains 'blocked') { return 'blocked' }
    if ($list -contains 'gap') { return 'gap' }
    if ($list -contains 'partial') { return 'partial' }
    return 'pass'
}

function Write-AcsSummary {
    $requirementIds = @($script:AcOrder | ForEach-Object { [regex]::Match($_, '^AC-(R\d{2})-').Groups[1].Value } | Select-Object -Unique)
    $verdicts = Get-EztestVerdicts -RequirementIds $requirementIds
    $acs = New-Object System.Collections.Generic.List[object]
    $requirements = New-Object System.Collections.Generic.List[object]
    $statusByRequirement = @{}

    foreach ($acId in $script:AcOrder) {
        $probePath = Join-Path $script:ProbesDir "$acId.json"
        if (-not (Test-Path -LiteralPath $probePath)) { throw "missing probe result: $probePath" }
        $probe = Read-JsonUtf8 -Path $probePath
        $rid = [string]$probe.requirementId
        if ([string]::IsNullOrWhiteSpace($rid)) { $rid = [regex]::Match($acId, '^AC-(R\d{2})-').Groups[1].Value }
        if (-not $statusByRequirement.ContainsKey($rid)) { $statusByRequirement[$rid] = New-Object 'System.Collections.Generic.List[string]' }
        $statusByRequirement[$rid].Add([string]$probe.recomputed)
        $acs.Add([ordered]@{
            id            = $acId
            requirementId = $rid
            kind          = [string]$probe.kind
            status        = [string]$probe.recomputed
            groundTruth   = [string]$probe.groundTruth
            eztestVerdict = $verdicts[$rid]
            observed      = [string]$probe.observed
            expected      = [string]$probe.expected
            probe         = [ordered]@{
                kind       = [string]$probe.kind
                recomputed = [string]$probe.recomputed
                file       = (Get-EvidenceRelativePath $probePath)
            }
            evidence      = @($probe.evidence)
            screenshots   = @($probe.screenshots)
            assertions    = @($probe.assertions)
        })
    }

    foreach ($rid in $requirementIds) {
        $statuses = @()
        if ($statusByRequirement.ContainsKey($rid)) { $statuses = @($statusByRequirement[$rid]) }
        $status = Get-DerivedRequirementStatus $statuses
        $acIds = @($acs | Where-Object { $_.requirementId -eq $rid } | ForEach-Object { $_.id })
        $evidence = @($acs | Where-Object { $_.requirementId -eq $rid } | ForEach-Object { $_.evidence } | Where-Object { $_ } | Select-Object -Unique)
        $requirements.Add([ordered]@{
            id            = $rid
            status        = $status
            acIds         = $acIds
            eztestVerdict = $verdicts[$rid]
            evidence      = @($evidence)
        })
    }

    $summary = [ordered]@{
        schemaVersion = 1
        range         = $script:ActiveRange
        runDir        = $RunDir
        gitHead       = (Get-Head)
        generatedAt   = (Get-Date).ToString('o')
        requirements  = $requirements.ToArray()
        acs           = $acs.ToArray()
    }
    $summaryName = if ($script:ActiveRange -eq 'R07-R12') { 'acs-r07-r12.json' } else { 'acs-r01-r06.json' }
    $summaryPath = Join-Path $script:EvidenceRoot "$RunDir\$summaryName"
    Write-JsonUtf8 -Path $summaryPath -Object $summary
    return $summaryPath
}

function Invoke-SelfCheck {
    $red = New-Object System.Collections.Generic.List[string]
    $expectedAcIds = @($script:AcOrder)
    $seen = New-Object System.Collections.Generic.List[string]

    foreach ($acId in $expectedAcIds) {
        $probePath = Join-Path $script:ProbesDir "$acId.json"
        if (-not (Test-Path -LiteralPath $probePath)) { $red.Add("missing probe $acId"); continue }
        try { $probe = Read-JsonUtf8 -Path $probePath } catch { $red.Add("unreadable probe $acId"); continue }
        $seen.Add([string]$probe.acId)
        if ([string]$probe.acId -ne $acId) { $red.Add("$acId acId mismatch: $($probe.acId)") }
        if (@('present', 'absent', 'partial') -notcontains [string]$probe.groundTruth) { $red.Add("$acId invalid groundTruth=$($probe.groundTruth)") }
        if (@('pass', 'fail', 'gap', 'blocked') -notcontains [string]$probe.recomputed) { $red.Add("$acId invalid recomputed=$($probe.recomputed)") }
        if (@($probe.assertions).Count -lt 2) { $red.Add("$acId assertions=$(@($probe.assertions).Count) (<2)") }
        if (@($probe.evidence).Count -lt 1) { $red.Add("$acId evidence is empty") }
        if (@($probe.screenshots).Count -lt 1) { $red.Add("$acId screenshots is empty") }

        foreach ($evidencePath in @($probe.evidence)) {
            if ([string]::IsNullOrWhiteSpace([string]$evidencePath)) { continue }
            $resolved = Resolve-EvidencePath ([string]$evidencePath)
            if ($null -eq $resolved) { $red.Add("$acId evidence missing: $evidencePath") }
        }
        foreach ($screenshotPath in @($probe.screenshots)) {
            if ([string]::IsNullOrWhiteSpace([string]$screenshotPath)) { continue }
            $resolved = Resolve-EvidencePath ([string]$screenshotPath)
            if ($null -eq $resolved) { $red.Add("$acId screenshot missing: $screenshotPath"); continue }
            foreach ($problem in @(Assert-Png -Path $resolved)) { $red.Add("$acId $problem") }
        }
    }
    foreach ($problem in @(Assert-ExactSetStrings -Actual @($seen) -Expected $expectedAcIds -Label 'probes')) { $red.Add($problem) }

    $expectedRequirementIds = @($expectedAcIds | ForEach-Object { [regex]::Match($_, '^AC-(R\d{2})-').Groups[1].Value } | Select-Object -Unique)
    $summaryName = if ($script:ActiveRange -eq 'R07-R12') { 'acs-r07-r12.json' } else { 'acs-r01-r06.json' }
    $summaryPath = Join-Path $script:EvidenceRoot "$RunDir\$summaryName"
    if (-not (Test-Path -LiteralPath $summaryPath)) { $red.Add("$summaryName missing") }
    else {
        try { $summary = Read-JsonUtf8 -Path $summaryPath } catch { $red.Add("$summaryName unreadable"); $summary = $null }
        if ($null -ne $summary) {
            $acs = @($summary.acs)
            $requirements = @($summary.requirements)
            foreach ($problem in @(Assert-ExactSetStrings -Actual @($acs | ForEach-Object { [string]$_.id }) -Expected $expectedAcIds -Label 'acs')) { $red.Add($problem) }
            foreach ($problem in @(Assert-ExactSetStrings -Actual @($requirements | ForEach-Object { [string]$_.id }) -Expected $expectedRequirementIds -Label 'requirements')) { $red.Add($problem) }
            foreach ($ac in $acs) {
                $acId = [string]$ac.id
                if ([string]::IsNullOrWhiteSpace([string]$ac.observed)) { $red.Add("$acId observed empty") }
                if ([string]::IsNullOrWhiteSpace([string]$ac.expected)) { $red.Add("$acId expected empty") }
                if ($null -eq $ac.probe -or [string]$ac.probe.recomputed -ne [string]$ac.status) { $red.Add("$acId probe.recomputed mismatch") }
                $probeFile = if ($null -ne $ac.probe) { [string]$ac.probe.file } else { '' }
                if ([string]::IsNullOrWhiteSpace($probeFile) -or $null -eq (Resolve-EvidencePath $probeFile)) { $red.Add("$acId probe.file missing") }
                foreach ($screenshotPath in @($ac.screenshots)) {
                    $resolved = Resolve-EvidencePath ([string]$screenshotPath)
                    if ($null -eq $resolved) { $red.Add("$acId summary screenshot missing: $screenshotPath") }
                }
            }
            foreach ($requirement in $requirements) {
                $rid = [string]$requirement.id
                $acStatuses = @($acs | Where-Object { $_.requirementId -eq $rid } | ForEach-Object { [string]$_.status })
                $derived = Get-DerivedRequirementStatus $acStatuses
                if ([string]$requirement.status -ne $derived) { $red.Add("requirement $rid status=$($requirement.status) != derived=$derived") }
            }
        }
    }

    if ($red.Count -gt 0) {
        foreach ($item in $red) { Write-Host ("SELFCHECK RED: " + $item) }
        Write-Host ("SELFCHECK FAIL: {0} red" -f $red.Count)
        return $false
    }
    Write-Host ("SELFCHECK PASS: {0} probes / {1} requirements / {2} schema+PNG+evidence" -f $expectedAcIds.Count, $expectedRequirementIds.Count, $summaryName)
    return $true
}

function Write-GreenLog {
    param([bool]$SelfCheckPass, [string]$SummaryPath)
    $lines = New-Object System.Collections.Generic.List[string]
    $familyLabel = if ($script:ProbeFamily -eq 'FA7') { 'FA-7' } else { 'FA-6' }
    $lines.Add("$familyLabel AC probe $($script:ActiveRange) - generated $(Get-Date -Format o)")
    $lines.Add("gitHead=$((Get-Head))")
    $lines.Add("runDir=$RunDir")
    $lines.Add("summary=$SummaryPath")
    $verdicts = @{}
    if ($script:ProbeFamily -eq 'FA7') {
        $requirementIds = @($script:AcOrder | ForEach-Object { [regex]::Match($_, '^AC-(R\d{2})-').Groups[1].Value } | Select-Object -Unique)
        $verdicts = Get-EztestVerdicts -RequirementIds $requirementIds
    }
    foreach ($acId in $script:AcOrder) {
        $probePath = Join-Path $script:ProbesDir "$acId.json"
        if (Test-Path -LiteralPath $probePath) {
            $probe = Read-JsonUtf8 -Path $probePath
            $line = ("PROBE {0} groundTruth={1} recomputed={2}" -f $acId, $probe.groundTruth, $probe.recomputed)
            if ($script:ProbeFamily -eq 'FA7') {
                $rid = [regex]::Match($acId, '^AC-(R\d{2})-').Groups[1].Value
                $line += " eztestVerdict=$($verdicts[$rid])"
            }
            $lines.Add($line)
        }
        else { $lines.Add("PROBE $acId MISSING") }
    }
    $lines.Add(("SELFCHECK " + $(if ($SelfCheckPass) { 'PASS' } else { 'FAIL' })))
    $logName = if ($script:ProbeFamily -eq 'FA7') { 'fa7-green.log' } else { 'fa6-green.log' }
    $logPath = Join-Path $script:LogsDir $logName
    [System.IO.File]::WriteAllText($logPath, ($lines -join "`r`n") + "`r`n", $script:Utf8NoBom)
    return $logPath
}

function Cleanup-Fa6Assets {
    try {
        $rows = @(Invoke-SutDb "select id from blog.upload_assets where original_name like 'fa6-%' and created_at > now() - interval '2 hours'")
        foreach ($row in $rows) {
            if ([string]::IsNullOrWhiteSpace($row)) { continue }
            $id = ([string]$row).Trim()
            $response = Invoke-SutJson -Method Delete -Path "/api/v1/uploads/$id" -Token $script:AdminToken
            Add-SutWriteLog 'delete-upload' "id=$id http=$($response.Status)"
        }
    }
    catch {
        Write-Host ("WARN: FA6 asset cleanup failed: " + $_.Exception.Message)
    }
}

# ---------------------------------------------------------------------------
# Entry point.
# ---------------------------------------------------------------------------
try {
    $effectiveRange = $Range
    if (-not [string]::IsNullOrWhiteSpace($AcId)) {
        if ($AcId -match '^AC-R(07|08|09|10|11|12)-') { $effectiveRange = 'R07-R12' }
        elseif ($AcId -match '^AC-R0[1-6]-') { $effectiveRange = 'R01-R06' }
    }
    Set-ProbeScope -RangeName $effectiveRange

    if ($SummaryOnly) {
        $summaryPath = Write-AcsSummary
        $selfCheckPass = Invoke-SelfCheck
        $greenLog = Write-GreenLog -SelfCheckPass $selfCheckPass -SummaryPath $summaryPath
        Write-Host ("SUMMARY " + $summaryPath)
        Write-Host ("LOG " + $greenLog)
        if (-not $selfCheckPass) { exit 1 }
        exit 0
    }

    if ($SelfCheck) {
        $ok = Invoke-SelfCheck
        if (-not $ok) { exit 1 }
        exit 0
    }

    if ([string]::IsNullOrWhiteSpace($AcId)) {
        Initialize-SutContext
        $family = if ($script:ProbeFamily -eq 'FA7') { 'FA7' } else { 'FA6' }
        Write-Host ("{0} range {1}: {2} probes against {3}" -f $family, $script:ActiveRange, $script:AcOrder.Count, $script:SutApiBase)
        foreach ($id in $script:AcOrder) {
            Invoke-ForAc -AcId $id | Out-Null
        }
        $summaryPath = Write-AcsSummary
        if ($script:ProbeFamily -eq 'FA7') { Remove-Fa7Assets } else { Cleanup-Fa6Assets }
        $selfCheckPass = Invoke-SelfCheck
        $greenLog = Write-GreenLog -SelfCheckPass $selfCheckPass -SummaryPath $summaryPath
        Write-Host ("SUMMARY " + $summaryPath)
        Write-Host ("LOG " + $greenLog)
        if (-not $selfCheckPass) { exit 1 }
        if ($script:InfraFailure) { exit 1 }
        exit 0
    }

    if ($script:AcOrder -notcontains $AcId) { throw "unsupported AC: $AcId" }
    Initialize-SutContext
    Invoke-ForAc -AcId $AcId | Out-Null
    if ($AcId -like 'AC-R06-*') { Cleanup-Fa6Assets }
    if ($script:ProbeFamily -eq 'FA7') { Remove-Fa7Assets }
    if ($script:InfraFailure) { exit 1 }
    exit 0
}
catch {
    [Console]::Error.WriteLine("PROBE FATAL: " + $_.Exception.Message)
    [Console]::Error.WriteLine($_.ScriptStackTrace)
    exit 1
}
