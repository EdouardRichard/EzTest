#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 FA-8: the fourteen user-problem regression matrix.

.DESCRIPTION
    Produces docs/version-V0.22/acceptance/evidence/final/RUN-x/issues14.json:

      14 rows (id 1..14) with
        { id, title, status, w0CheckIds[], independent, assertions[], evidence[], detail }

    The eight items that have no legacy hard-coded check are mapped to the fresh
    W0 regression result (RUN-x/w0flow/result.json, item ids issue-*). The six
    legacy hard-coded items (1 model params, 2 multi-repo, 4 concurrent
    conversation, 5 extraction count, 11 attachment vision, 12 binding edit)
    carry independent real assertions computed from the FA stack API, the FA
    PostgreSQL database, and (for item 1) a real Playwright screenshot.

    Two modes:
      -VerifyOnly   validate an existing issues14.json and its W0 evidence;
                    this is the TDD validator and never touches the stack.
      (default)     collect the evidence, write issues14.json, validate it and
                    run scripts/v022-fa-gate.ps1 -Gate issues14.

    W0 gate compatibility: the FA-3 gate reads pass states from
    items[].ok/pass/passed or state.checks.<id>.ok, while the fresh W0 producer
    writes items[].status = "PASS". This script therefore adds a deterministic
    state.checks projection derived from items[].status and records the
    pre-projection sha256 in w0flow/checks-projection.json. The W0 items/state
    truth is never changed. No gate/verdict script is modified.

    Encoding: Windows PowerShell 5.1; read JSON as UTF-8; write evidence as
    UTF-8 without BOM; this script itself is saved with a UTF-8 BOM because it
    contains CJK literal strings. Secrets are never printed or written.

.NOTES
    Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\v022-fa-issues14.ps1
        powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\v022-fa-issues14.ps1 -VerifyOnly
#>
param(
    [ValidateSet('RUN-1', 'RUN-2')]
    [string]$RunDir = 'RUN-1',

    [switch]$VerifyOnly,

    # Optional override for negative-fixture tests; the default is the final
    # evidence root. Relative paths resolve against the repository root.
    [string]$EvidenceRoot = '',

    # The FA database name; defaults to the Run-1/Run-2 FA database.
    [string]$DatabaseName = '',

    # FA stack ports; Run 2 uses 13461/6881 and passes them explicitly.
    [int]$ApiPort = 13460,
    [int]$WebPort = 6880
)

$ErrorActionPreference = 'Stop'
$script:RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $script:RepoRoot
. (Join-Path $PSScriptRoot 'v022-fa-common.ps1')

if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $script:EvidenceRoot = Join-Path $script:RepoRoot 'docs\version-V0.22\acceptance\evidence\final'
}
elseif ([System.IO.Path]::IsPathRooted($EvidenceRoot)) {
    $script:EvidenceRoot = [System.IO.Path]::GetFullPath($EvidenceRoot)
}
else {
    $script:EvidenceRoot = [System.IO.Path]::GetFullPath((Join-Path $script:RepoRoot $EvidenceRoot))
}

$script:RunRoot = Join-Path $script:EvidenceRoot $RunDir
$script:W0Dir = Join-Path $script:RunRoot 'w0flow'
$script:W0Path = Join-Path $script:W0Dir 'result.json'
$script:IssuesPath = Join-Path $script:RunRoot 'issues14.json'
$script:IssuesDir = Join-Path $script:RunRoot 'issues14'
$script:ShotDir = Join-Path $script:IssuesDir 'screenshots'
$script:LogDir = Join-Path $script:RunRoot 'logs'
$script:TmpDir = Join-Path $script:RepoRoot '.tmp\fa8'
$script:ApiBase = "http://127.0.0.1:$ApiPort"
$script:WebBase = "http://127.0.0.1:$WebPort"
$script:Database = if (-not [string]::IsNullOrWhiteSpace($DatabaseName)) { $DatabaseName }
                   elseif ($RunDir -eq 'RUN-2') { 'eztest_v022_fa_r2' }
                   else { 'eztest_v022_fa' }
$script:Utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$script:Stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$script:LogPath = Join-Path $script:LogDir ("fa8-run-$($script:Stamp).log")
$script:Token = ''
$script:W0Items = @{}

New-Item -ItemType Directory -Force -Path $script:IssuesDir, $script:ShotDir, $script:LogDir, $script:TmpDir | Out-Null

function Write-Log {
    param([string]$Message)
    $line = "[{0}] {1}" -f (Get-Date -Format 'HH:mm:ss'), $Message
    Write-Host $line
    [System.IO.File]::AppendAllText($script:LogPath, $line + [Environment]::NewLine, $script:Utf8NoBom)
}

function Get-ArrayValue {
    param($Object, [string]$Name)
    if ($null -eq $Object) { return @() }
    $value = $Object.$Name
    if ($null -eq $value) { return @() }
    return @($value)
}

function Get-JsonValue {
    param($Object, [string]$Name, $Default = $null)
    if ($null -eq $Object) { return $Default }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property -or $null -eq $property.Value) { return $Default }
    return $property.Value
}

function Protect-EvidenceText {
    param([string]$Text)
    if ([string]::IsNullOrEmpty($Text)) { return $Text }
    $safe = [regex]::Replace($Text, '(?i)(postgres(?:ql)?://[^:/]+:)[^@]+@', '$1***@')
    $safe = [regex]::Replace($safe, '(?i)("api_key"\s*:\s*")[^"]+(")', '$1***$2')
    $safe = [regex]::Replace($safe, '(?i)("password"\s*:\s*")[^"]*(")', '$1***$2')
    $safe = [regex]::Replace($safe, '(?i)(authorization\s*:\s*bearer\s+)[A-Za-z0-9._\-]+', '$1***')
    return $safe
}

function Get-TextSha256 {
    param([string]$Text)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { $bytes = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($Text)) }
    finally { $sha.Dispose() }
    return (-join ($bytes | ForEach-Object { $_.ToString('x2') }))
}

function Save-EvidenceJson {
    param([string]$Name, $Object)
    $path = Join-Path $script:IssuesDir $Name
    $json = $Object | ConvertTo-Json -Depth 40
    $json = Protect-EvidenceText -Text $json
    [System.IO.File]::WriteAllText($path, $json, $script:Utf8NoBom)
    return "$RunDir/issues14/$Name"
}

function New-Assertion {
    param(
        [string]$Kind,
        [bool]$Ok,
        [string]$Detail,
        [bool]$Independent = $false
    )
    return [ordered]@{
        kind        = $Kind
        ok          = $Ok
        detail      = $Detail
        independent = $Independent
    }
}

function New-Problem {
    param(
        [int]$Id,
        [string]$Title,
        [string[]]$W0CheckIds,
        [bool]$Independent,
        $Assertions,
        [string[]]$Evidence,
        [string]$Detail
    )
    $list = $Assertions
    $failed = @($list | Where-Object { -not [bool]$_.ok })
    $status = if ($failed.Count -eq 0) { 'PASS' } else { 'RED' }
    $detailText = $Detail
    if ($failed.Count -gt 0) {
        $detailText = ($Detail + ' | FAILED: ' + (($failed | ForEach-Object { [string]$_.detail }) -join '; ')).Trim()
    }
    return [ordered]@{
        id          = $Id
        title       = $Title
        status      = $status
        w0CheckIds  = @($W0CheckIds)
        independent = $Independent
        assertions  = @($list)
        evidence    = @($Evidence | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) } | Select-Object -Unique)
        detail      = $detailText
    }
}

# ---------------------------------------------------------------------------
# Path resolution used by both the validator and the producer.
# ---------------------------------------------------------------------------
function Resolve-EvidencePath {
    param([string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path)) { return $null }
    $normalized = $Path -replace '/', '\'
    $candidates = @(
        (Join-Path $script:EvidenceRoot $normalized),
        (Join-Path $script:RunRoot $normalized),
        (Join-Path $script:RepoRoot $normalized)
    )
    foreach ($candidate in $candidates) {
        if (Test-Path -LiteralPath $candidate) {
            try { return (Resolve-Path -LiteralPath $candidate).Path } catch { return $candidate }
        }
    }
    return $null
}

function Resolve-W0EvidencePath {
    param([string]$EvidenceText)
    $files = @()
    foreach ($part in ([string]$EvidenceText -split ',')) {
        $item = $part.Trim()
        if ([string]::IsNullOrWhiteSpace($item)) { continue }
        $normalized = $item -replace '/', '\'
        $candidates = @(
            (Join-Path $script:W0Dir $normalized),
            (Join-Path $script:W0Dir ([System.IO.Path]::GetFileName($normalized))),
            (Join-Path $script:EvidenceRoot $normalized),
            (Join-Path $script:RunRoot $normalized)
        )
        foreach ($candidate in $candidates) {
            if (Test-Path -LiteralPath $candidate) {
                $files += (Resolve-Path -LiteralPath $candidate).Path
                break
            }
        }
    }
    return @($files | Select-Object -Unique)
}

function Get-W0EvidenceRelativePaths {
    param([string]$EvidenceText)
    $paths = New-Object System.Collections.Generic.List[string]
    foreach ($file in @(Resolve-W0EvidencePath $EvidenceText)) {
        $paths.Add("$RunDir/w0flow/$([System.IO.Path]::GetFileName($file))")
    }
    return @($paths)
}

# ---------------------------------------------------------------------------
# W0 pass map: items[].status is the W0 producer truth. The FA-3 gate only
# understands items[].ok or state.checks.<id>.ok, so the producer adds the
# deterministic projection below; the validator accepts both shapes.
# ---------------------------------------------------------------------------
function Initialize-W0State {
    if (-not (Test-Path -LiteralPath $script:W0Path)) {
        throw "missing fresh W0 result: $($script:W0Path)"
    }
    $w0 = Read-JsonUtf8 -Path $script:W0Path
    $items = @(Get-ArrayValue $w0 'items')
    if ($items.Count -eq 0) {
        throw "fresh W0 result has no items: $($script:W0Path)"
    }
    $script:W0Items = @{}
    foreach ($item in $items) {
        $id = [string]$item.id
        if ([string]::IsNullOrWhiteSpace($id)) { continue }
        $script:W0Items[$id] = $item
    }
    return $w0
}

function Get-W0PassMap {
    param($W0)
    $pass = @{}
    foreach ($item in @(Get-ArrayValue $W0 'items')) {
        $id = [string]$item.id
        if ([string]::IsNullOrWhiteSpace($id)) { continue }
        if (([string]$item.status).ToUpperInvariant() -eq 'PASS') { $pass[$id] = $true }
    }
    $state = Get-JsonValue $W0 'state'
    if ($null -ne $state) {
        $checks = Get-JsonValue $state 'checks'
        if ($null -ne $checks) {
            foreach ($property in $checks.PSObject.Properties) {
                $ok = Get-JsonValue $property.Value 'ok'
                if ($null -ne $ok -and [bool]$ok) { $pass[$property.Name] = $true }
            }
        }
    }
    return $pass
}

function Get-W0Assertions {
    param([string[]]$CheckIds)
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]
    foreach ($checkId in $CheckIds) {
        $item = $null
        if ($script:W0Items.ContainsKey([string]$checkId)) { $item = $script:W0Items[[string]$checkId] }
        $ok = $null -ne $item -and ([string]$item.status).ToUpperInvariant() -eq 'PASS'
        $files = @()
        if ($null -ne $item) { $files = @(Resolve-W0EvidencePath ([string]$item.evidence)) }
        $detail = if ($null -eq $item) {
            "W0 check $checkId is missing from the fresh result"
        }
        else {
            "W0 check $checkId status=$([string]$item.status); evidenceFiles=$($files.Count)"
        }
        if ($ok -and $files.Count -eq 0) {
            $ok = $false
            $detail = "W0 check $checkId is PASS but its evidence files are missing"
        }
        if (-not $ok -and $null -ne $item -and ([string]$item.status).ToUpperInvariant() -ne 'PASS') {
            $detail = "W0 check $checkId status=$([string]$item.status) ($([string]$item.detail))"
        }
        $assertions.Add((New-Assertion 'w0' $ok $detail $false))
        foreach ($file in $files) {
            $evidence.Add("$RunDir/w0flow/$([System.IO.Path]::GetFileName($file))")
        }
    }
    return [ordered]@{
        Assertions = @($assertions.ToArray())
        Evidence   = @($evidence.ToArray())
    }
}

# ---------------------------------------------------------------------------
# Read-only validator (TDD): exact 14 rows, PASS status, W0 ids PASS, six
# independent assertions, non-empty existing evidence.
# ---------------------------------------------------------------------------
function Invoke-Issues14Verification {
    param([string]$Root = $script:EvidenceRoot)

    $red = New-Object System.Collections.Generic.List[string]
    $runRoot = Join-Path $Root $RunDir
    $issuesPath = Join-Path $runRoot 'issues14.json'
    $w0Path = Join-Path $runRoot 'w0flow\result.json'

    if (-not (Test-Path -LiteralPath $issuesPath)) {
        $red.Add("missing issues14.json: $issuesPath")
        return @($red)
    }
    if (-not (Test-Path -LiteralPath $w0Path)) {
        $red.Add("missing fresh W0 result.json: $w0Path")
        return @($red)
    }

    $doc = $null
    try { $doc = Read-JsonUtf8 -Path $issuesPath }
    catch { $red.Add("issues14.json unreadable: $($_.Exception.Message)"); return @($red) }

    $problems = @(Get-ArrayValue $doc 'problems')
    $actualIds = @($problems | ForEach-Object { [string]$_.id })
    $expectedIds = @(1..14 | ForEach-Object { [string]$_ })
    foreach ($msg in @(Assert-ExactSetStrings -Actual $actualIds -Expected $expectedIds -Label 'problems')) {
        $red.Add($msg)
    }

    $w0 = $null
    try { $w0 = Read-JsonUtf8 -Path $w0Path }
    catch { $red.Add("W0 result.json unreadable: $($_.Exception.Message)"); return @($red) }
    $passMap = Get-W0PassMap $w0
    $independentIds = @('1', '2', '4', '5', '11', '12')

    foreach ($problem in $problems) {
        $id = [string]$problem.id
        $status = ([string]$problem.status).ToUpperInvariant()
        if ($status -ne 'PASS') { $red.Add("problem $id status='$status' (expected PASS)") }

        $checkIds = @(Get-ArrayValue $problem 'w0CheckIds')
        if ($checkIds.Count -eq 0) { $red.Add("problem $id has no w0CheckIds") }
        foreach ($checkId in $checkIds) {
            if (-not $passMap.ContainsKey([string]$checkId)) {
                $red.Add("problem $id w0 check not PASS: $checkId")
            }
        }

        $assertions = @(Get-ArrayValue $problem 'assertions')
        if ($assertions.Count -eq 0) { $red.Add("problem $id assertions is empty") }
        foreach ($assertion in $assertions) {
            $ok = Get-JsonValue $assertion 'ok'
            if ($null -ne $ok -and -not [bool]$ok) {
                $red.Add("problem $id assertion not ok: $([string](Get-JsonValue $assertion 'detail' ''))")
            }
        }
        if ($independentIds -contains $id) {
            $hasIndependent = $false
            foreach ($assertion in $assertions) {
                if ([bool](Get-JsonValue $assertion 'independent' $false)) { $hasIndependent = $true }
            }
            if (-not $hasIndependent) { $red.Add("problem $id (legacy hard-coded item) has no independent assertion") }
        }

        $evidence = @(Get-ArrayValue $problem 'evidence')
        if ($evidence.Count -eq 0) { $red.Add("problem $id has no evidence files") }
        foreach ($path in $evidence) {
            $resolved = Resolve-EvidencePath ([string]$path)
            if ($null -eq $resolved) { $red.Add("problem $id evidence missing: $path") }
        }
    }

    return @($red)
}

# ---------------------------------------------------------------------------
# Verify-only entry point.
# ---------------------------------------------------------------------------
if ($VerifyOnly) {
    Write-Log "verify-only runDir=$RunDir root=$($script:EvidenceRoot)"
    $red = @(Invoke-Issues14Verification -Root $script:EvidenceRoot)
    if ($red.Count -eq 0) {
        Write-Log 'VERIFY PASS: 14 rows, fresh W0 PASS, 6 independent assertions, evidence exists'
        exit 0
    }
    foreach ($problem in $red) { Write-Log "VERIFY RED: $problem" }
    Write-Log "VERIFY FAIL: $($red.Count) RED item(s)"
    exit 1
}

# ---------------------------------------------------------------------------
# Producer helpers: PostgreSQL, FA API, Node helper processes.
# ---------------------------------------------------------------------------
function Get-PostgresParts {
    param([string]$Url)
    $match = [regex]::Match($Url, 'postgres://(?<u>[^:]+):(?<p>[^@]+)@(?<h>[^:]+):(?<port>\d+)/(?<db>[^?]+)')
    if (-not $match.Success) { throw 'cannot parse EZTEST_DATABASE_URL' }
    return [pscustomobject]@{
        User     = $match.Groups['u'].Value
        Password = $match.Groups['p'].Value
        Host     = $match.Groups['h'].Value
        Port     = $match.Groups['port'].Value
        Db       = $match.Groups['db'].Value
    }
}

function Invoke-PsqlRows {
    param([string]$Sql)
    $parts = Get-PostgresParts (Get-EnvValue 'EZTEST_DATABASE_URL')
    $psql = Join-Path $script:RepoRoot '.tools\pgsql\bin\psql.exe'
    if (-not (Test-Path -LiteralPath $psql)) { throw "psql.exe not found: $psql" }
    $previousPassword = $env:PGPASSWORD
    $previousEncoding = $env:PGCLIENTENCODING
    try {
        $env:PGPASSWORD = $parts.Password
        $env:PGCLIENTENCODING = 'UTF8'
        $output = & $psql -h $parts.Host -p $parts.Port -U $parts.User -d $script:Database -X -q -A -t -F '|' -c $Sql 2>&1
        $exitCode = $LASTEXITCODE
    }
    finally {
        $env:PGPASSWORD = $previousPassword
        $env:PGCLIENTENCODING = $previousEncoding
    }
    if ($exitCode -ne 0) { throw "psql query failed (exit=$exitCode) against database $($script:Database)" }
    return @($output | ForEach-Object { [string]$_ })
}

function Invoke-PsqlJson {
    param([string]$Sql)
    $rows = @(Invoke-PsqlRows -Sql $Sql)
    $raw = (@($rows) -join "`n").Trim()
    if ([string]::IsNullOrWhiteSpace($raw)) { return @() }
    $parsed = $raw | ConvertFrom-Json
    if ($null -eq $parsed) { return @() }
    return @($parsed)
}

function Invoke-PsqlScalar {
    param([string]$Sql)
    $rows = @(Invoke-PsqlRows -Sql $Sql)
    return ((@($rows) -join '').Trim())
}

function Invoke-FaApi {
    param(
        [string]$Method,
        [string]$Path,
        $Body = $null,
        [int]$TimeoutSec = 120
    )
    return Invoke-Api -Method $Method -Uri ($script:ApiBase + $Path) `
        -Headers @{ Authorization = "Bearer $($script:Token)" } -Body $Body -TimeoutSec $TimeoutSec
}

function Save-ApiEvidence {
    param([string]$Name, $Response)
    $raw = [string]$Response.Raw
    if ($raw.Length -gt 20000) { $raw = $raw.Substring(0, 20000) + '...<truncated>' }
    $payload = [ordered]@{
        status = [int]$Response.Status
        ok     = [bool]$Response.Ok
        body   = $Response.Body
        raw    = $raw
    }
    return (Save-EvidenceJson -Name $Name -Object $payload)
}

function Invoke-NodeHelper {
    param(
        [string]$ScriptPath,
        [hashtable]$EnvValues,
        [string]$StdoutPath,
        [string]$StderrPath
    )
    $previous = @{}
    foreach ($key in $EnvValues.Keys) {
        $previous[$key] = [System.Environment]::GetEnvironmentVariable($key, 'Process')
        [System.Environment]::SetEnvironmentVariable($key, [string]$EnvValues[$key], 'Process')
    }
    try {
        $node = (Get-Command node -ErrorAction Stop).Source
        $process = Start-Process -FilePath $node -ArgumentList @($ScriptPath) -WorkingDirectory $script:RepoRoot `
            -RedirectStandardOutput $StdoutPath -RedirectStandardError $StderrPath -PassThru -NoNewWindow -Wait
        return [int]$process.ExitCode
    }
    finally {
        foreach ($key in $previous.Keys) {
            [System.Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process')
        }
    }
}

function Read-LastJsonLine {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    $lines = @([System.IO.File]::ReadAllLines($Path, [System.Text.Encoding]::UTF8) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($lines.Count -eq 0) { return $null }
    for ($i = $lines.Count - 1; $i -ge 0; $i--) {
        try { return ($lines[$i] | ConvertFrom-Json) } catch { }
    }
    return $null
}

function Write-Utf8Text {
    param([string]$Path, [string]$Text)
    $parent = Split-Path -Parent $Path
    if ($parent -and -not (Test-Path -LiteralPath $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    [System.IO.File]::WriteAllText($Path, $Text, $script:Utf8NoBom)
}

# ---------------------------------------------------------------------------
# W0 gate-compatibility projection. items[].status stays the truth; the
# FA-3 gate reads state.checks.<id>.ok, so add a deterministic projection and
# record the pre-projection sha256 for audit.
# ---------------------------------------------------------------------------
function Add-W0ChecksProjection {
    $w0 = Read-JsonUtf8 -Path $script:W0Path
    $items = @(Get-ArrayValue $w0 'items')
    $checks = [ordered]@{}
    foreach ($item in $items) {
        $id = [string]$item.id
        if ([string]::IsNullOrWhiteSpace($id)) { continue }
        $checks[$id] = [ordered]@{
            ok          = (([string]$item.status).ToUpperInvariant() -eq 'PASS')
            status      = [string]$item.status
            detail      = [string]$item.detail
            evidence    = [string]$item.evidence
            derivedFrom = 'items[].status'
        }
    }
    $state = Get-JsonValue $w0 'state'
    $alreadyProjected = $false
    if ($null -ne $state) {
        if ($null -ne (Get-JsonValue $state 'checks')) { $alreadyProjected = $true }
    }
    else {
        $w0 | Add-Member -NotePropertyName state -NotePropertyValue ([pscustomobject]@{}) -Force
        $state = $w0.state
    }
    $state | Add-Member -NotePropertyName checks -NotePropertyValue $checks -Force
    $itemsText = ($items | ConvertTo-Json -Depth 100)
    $itemsSha = Get-TextSha256 -Text $itemsText
    $originalSha = Get-Sha -Path $script:W0Path
    Write-JsonUtf8 -Path $script:W0Path -Object $w0 -Depth 100
    [void](Save-EvidenceJson -Name 'w0-checks-projection.json' -Object ([ordered]@{
        generatedAt      = (Get-Date).ToString('o')
        source           = $script:W0Path
        derivedFrom      = 'items[].status'
        alreadyProjected = $alreadyProjected
        itemsSha256      = $itemsSha
        originalSha256   = $originalSha
        projectedSha256  = (Get-Sha -Path $script:W0Path)
        projectedChecks  = @($checks.Keys)
    }))
    Write-Log "W0 projection: $($checks.Count) checks derived from items[].status (items sha=$itemsSha alreadyProjected=$alreadyProjected)"
}

# ---------------------------------------------------------------------------
# Probe version used only for FA-8 independent checks (API/DB/UI). It is not
# the FA-5 version, so the typical evidence stays untouched.
# ---------------------------------------------------------------------------
function Get-ProbeVersion {
    param([string]$EnvironmentId)
    $list = Invoke-FaApi -Method 'GET' -Path '/api/versions?limit=100'
    if ($list.Status -eq 200) {
        foreach ($candidate in @($list.Body.data)) {
            if ([string]$candidate.title -ne 'FA-8 issues14 probe') { continue }
            $id = [string]$candidate.id
            $binding = Invoke-FaApi -Method 'GET' -Path "/api/versions/$id/binding"
            if ($binding.Status -eq 200 -and [string]$binding.Body.data.stage -eq 'clarifying' -and
                [bool]$binding.Body.data.editable -and @($binding.Body.data.repositories).Count -eq 2) {
                Write-Log "probe version reused: $id"
                return $id
            }
        }
    }
    $created = Invoke-FaApi -Method 'POST' -Path '/api/versions' -Body ([ordered]@{
        title          = 'FA-8 issues14 probe'
        description    = 'FA-8 independent assertions probe (binding, concurrency, vision)'
        goal           = 'Verify FA-8 independent assertions without touching FA-5 evidence'
        environment_id = $EnvironmentId
    })
    if ($created.Status -ne 200 -and $created.Status -ne 201) {
        throw "could not create FA-8 probe version (http=$($created.Status))"
    }
    $id = [string]$created.Body.data.id
    Write-Log "probe version created: $id"
    return $id
}

# ---------------------------------------------------------------------------
# Problem 1: model token parameters (context window / max output) are manual
# and persist across a re-read. The FA-5 restart path is covered by W0 01b.
# ---------------------------------------------------------------------------
function Invoke-Problem01 {
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]

    $configsResp = Invoke-FaApi -Method 'GET' -Path '/api/admin/llm/configs'
    $evidence.Add((Save-ApiEvidence -Name '01-llm-configs-before.json' -Response $configsResp))
    $config = $null
    if ($configsResp.Status -eq 200) {
        $config = @(@($configsResp.Body.data) | Where-Object { [bool]$_.is_default } | Select-Object -First 1)[0]
    }
    if ($null -eq $config) {
        $assertions.Add((New-Assertion 'api' $false 'no default LLM config in the FA stack' $true))
        $w0 = Get-W0Assertions @('issue-01a-model-manual', 'issue-01b-model-restart', 'issue-01c-model-ui')
        foreach ($a in $w0.Assertions) { $assertions.Add($a) }
        foreach ($e in $w0.Evidence) { $evidence.Add($e) }
        return (New-Problem -Id 1 -Title '模型 token 参数（窗口/输出）手工修复并持久化' `
            -W0CheckIds @('issue-01a-model-manual', 'issue-01b-model-restart', 'issue-01c-model-ui') `
            -Independent $true -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
            -Detail 'No default LLM config was readable; item recorded RED.')
    }

    $configId = [string]$config.id
    $model = @(@($config.models) | Where-Object { [string]$_.id -eq [string]$config.default_model_id } | Select-Object -First 1)[0]
    if ($null -eq $model) {
        $assertions.Add((New-Assertion 'api' $false 'default config exposes no model row' $true))
        $w0 = Get-W0Assertions @('issue-01a-model-manual', 'issue-01b-model-restart', 'issue-01c-model-ui')
        foreach ($a in $w0.Assertions) { $assertions.Add($a) }
        foreach ($e in $w0.Evidence) { $evidence.Add($e) }
        return (New-Problem -Id 1 -Title '模型 token 参数（窗口/输出）手工修复并持久化' `
            -W0CheckIds @('issue-01a-model-manual', 'issue-01b-model-restart', 'issue-01c-model-ui') `
            -Independent $true -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
            -Detail 'Default LLM config has no model row; item recorded RED.')
    }

    $modelId = [string]$model.id
    $beforeContext = [int](Get-JsonValue $model 'context_window' 0)
    $beforeOutput = [int](Get-JsonValue $model 'max_output' 0)

    $patchResp = Invoke-FaApi -Method 'PATCH' -Path "/api/admin/llm/configs/$configId/models/$modelId" `
        -Body ([ordered]@{ context_window = 131072; max_output = 8192 })
    $evidence.Add((Save-ApiEvidence -Name '01-llm-patch-131072.json' -Response $patchResp))
    $patched = if ($patchResp.Status -eq 200) { $patchResp.Body.data } else { $null }
    $patchOk = $null -ne $patched -and [int](Get-JsonValue $patched 'context_window' 0) -eq 131072 -and
        [int](Get-JsonValue $patched 'max_output' 0) -eq 8192 -and
        [string](Get-JsonValue $patched 'window_source' '') -eq 'manual' -and
        [string](Get-JsonValue $patched 'output_source' '') -eq 'manual'
    $assertions.Add((New-Assertion 'api' $patchOk `
        "PATCH model $modelId context_window=131072 max_output=8192 -> http=$($patchResp.Status) window_source=$([string](Get-JsonValue $patched 'window_source' '')) output_source=$([string](Get-JsonValue $patched 'output_source' ''))" $true))

    $reReadResp = Invoke-FaApi -Method 'GET' -Path "/api/admin/llm/configs/$configId"
    $evidence.Add((Save-ApiEvidence -Name '01-llm-config-reread.json' -Response $reReadResp))
    $reReadModel = $null
    if ($reReadResp.Status -eq 200) {
        $reReadModel = @(@($reReadResp.Body.data.models) | Where-Object { [string]$_.id -eq $modelId } | Select-Object -First 1)[0]
    }
    $reReadOk = $null -ne $reReadModel -and [int](Get-JsonValue $reReadModel 'context_window' 0) -eq 131072 -and
        [int](Get-JsonValue $reReadModel 'max_output' 0) -eq 8192 -and
        [string](Get-JsonValue $reReadModel 'window_source' '') -eq 'manual' -and
        [string](Get-JsonValue $reReadModel 'output_source' '') -eq 'manual'
    $assertions.Add((New-Assertion 'api' $reReadOk `
        "GET config re-read after PATCH -> context_window=$([string](Get-JsonValue $reReadModel 'context_window' '')) max_output=$([string](Get-JsonValue $reReadModel 'max_output' '')) source=$([string](Get-JsonValue $reReadModel 'window_source' ''))/$([string](Get-JsonValue $reReadModel 'output_source' ''))" $true))

    $dbDuring = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('id',id::text,'model',model,'context_window',context_window,'max_output',max_output,'window_source',window_source,'output_source',output_source) order by id),'[]'::json)::text from llm_model where id = $modelId")
    $evidence.Add((Save-EvidenceJson -Name '01-llm-model-db-during.json' -Object ([ordered]@{ query = 'llm_model by id'; rows = @($dbDuring) })))
    $dbDuringOk = $dbDuring.Count -eq 1 -and [int](Get-JsonValue $dbDuring[0] 'context_window' 0) -eq 131072 -and
        [int](Get-JsonValue $dbDuring[0] 'max_output' 0) -eq 8192 -and
        [string](Get-JsonValue $dbDuring[0] 'window_source' '') -eq 'manual' -and
        [string](Get-JsonValue $dbDuring[0] 'output_source' '') -eq 'manual'
    $assertions.Add((New-Assertion 'db' $dbDuringOk "llm_model window_source/output_source=manual and 131072/8192 in $($script:Database)" $true))

    $restoreResp = Invoke-FaApi -Method 'PATCH' -Path "/api/admin/llm/configs/$configId/models/$modelId" `
        -Body ([ordered]@{ context_window = 128000; max_output = 8192 })
    $evidence.Add((Save-ApiEvidence -Name '01-llm-restore-128000.json' -Response $restoreResp))
    $assertions.Add((New-Assertion 'api' ($restoreResp.Status -eq 200) "PATCH restore context_window=128000 max_output=8192 -> http=$($restoreResp.Status)" $true))

    $finalResp = Invoke-FaApi -Method 'GET' -Path "/api/admin/llm/configs/$configId"
    $evidence.Add((Save-ApiEvidence -Name '01-llm-config-final.json' -Response $finalResp))
    $finalModel = $null
    if ($finalResp.Status -eq 200) {
        $finalModel = @(@($finalResp.Body.data.models) | Where-Object { [string]$_.id -eq $modelId } | Select-Object -First 1)[0]
    }
    $finalOk = $null -ne $finalModel -and [int](Get-JsonValue $finalModel 'context_window' 0) -eq 128000 -and
        [int](Get-JsonValue $finalModel 'max_output' 0) -eq 8192 -and
        [string](Get-JsonValue $finalModel 'window_source' '') -eq 'manual' -and
        [string](Get-JsonValue $finalModel 'output_source' '') -eq 'manual'
    $assertions.Add((New-Assertion 'api' $finalOk `
        "final GET -> context_window=$([string](Get-JsonValue $finalModel 'context_window' '')) max_output=$([string](Get-JsonValue $finalModel 'max_output' '')) source=$([string](Get-JsonValue $finalModel 'window_source' ''))/$([string](Get-JsonValue $finalModel 'output_source' ''))" $true))

    $dbFinal = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('id',id::text,'model',model,'context_window',context_window,'max_output',max_output,'window_source',window_source,'output_source',output_source) order by id),'[]'::json)::text from llm_model where id = $modelId")
    $evidence.Add((Save-EvidenceJson -Name '01-llm-model-db-final.json' -Object ([ordered]@{ query = 'llm_model by id'; rows = @($dbFinal) })))
    $dbFinalOk = $dbFinal.Count -eq 1 -and [int](Get-JsonValue $dbFinal[0] 'context_window' 0) -eq 128000 -and
        [int](Get-JsonValue $dbFinal[0] 'max_output' 0) -eq 8192 -and
        [string](Get-JsonValue $dbFinal[0] 'output_source' '') -eq 'manual'
    $assertions.Add((New-Assertion 'db' $dbFinalOk "llm_model final manual 128000/8192 persisted in $($script:Database)" $true))

    $shotName = '01-settings-model-manual.png'
    $shotPath = Join-Path $script:ShotDir $shotName
    $mjsPath = Join-Path $script:TmpDir 'screenshot-settings.mjs'
    $nodeSource = @'
import path from 'node:path'
import { createRequire } from 'node:module'
const requireFromExecutor = createRequire(path.join(process.env.FA8_REPO_ROOT, 'executor', 'package.json'))
const { chromium } = requireFromExecutor('playwright')
const WEB = process.env.FA8_WEB
const PW = process.env.FA8_ADMIN_PW
const OUT = process.env.FA8_OUT
const browser = await chromium.launch({ headless: true, channel: 'chrome' })
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
page.setDefaultTimeout(60000)
await page.goto(`${WEB}/login`, { waitUntil: 'domcontentloaded' })
await page.waitForTimeout(1000)
if (page.url().includes('/login')) {
  await page.fill('#username', 'admin')
  await page.fill('#password', PW)
  await page.locator('button[type=submit]').click()
  await page.waitForURL(/\/versions/, { timeout: 90000 })
}
await page.goto(`${WEB}/settings?tab=llm`, { waitUntil: 'domcontentloaded' })
await page.waitForTimeout(3000)
const manage = page.getByRole('button', { name: '管理模型' }).first()
if (await manage.count()) { await manage.click(); await page.waitForTimeout(1500) }
const text = await page.locator('body').innerText()
await page.screenshot({ path: OUT, fullPage: true })
console.log(JSON.stringify({
  url: page.url(),
  hasModel: /deepseek-flash/.test(text),
  hasContext128: /上下文\s*128K/.test(text),
  hasOutput8K: /输出\s*8(?:\.192)?K/.test(text),
  hasManual: /手工/.test(text),
  hasVision: /视觉/.test(text),
  excerpt: text.slice(0, 500)
}))
await browser.close()
'@
    Write-Utf8Text -Path $mjsPath -Text $nodeSource
    $stdoutPath = Join-Path $script:TmpDir 'settings-shot.out.json'
    $stderrPath = Join-Path $script:TmpDir 'settings-shot.err.log'
    $nodeExit = Invoke-NodeHelper -ScriptPath $mjsPath -EnvValues @{
        FA8_REPO_ROOT = $script:RepoRoot
        FA8_WEB       = $script:WebBase
        FA8_ADMIN_PW  = (Get-EnvValue 'EZTEST_ADMIN_PASSWORD')
        FA8_OUT       = $shotPath
    } -StdoutPath $stdoutPath -StderrPath $stderrPath
    $uiJson = Read-LastJsonLine -Path $stdoutPath
    $evidence.Add((Save-EvidenceJson -Name '01-settings-ui.json' -Object ([ordered]@{
        nodeExit = $nodeExit
        ui       = $uiJson
        stderr   = if (Test-Path -LiteralPath $stderrPath) { [System.IO.File]::ReadAllText($stderrPath, [System.Text.Encoding]::UTF8) } else { '' }
    })))
    $pngRed = @()
    if (Test-Path -LiteralPath $shotPath) { $pngRed = @(Assert-Png -Path $shotPath) }
    else { $pngRed = @('screenshot missing') }
    $uiOk = $nodeExit -eq 0 -and $null -ne $uiJson -and [bool]$uiJson.hasModel -and
        [bool]$uiJson.hasContext128 -and [bool]$uiJson.hasOutput8K -and [bool]$uiJson.hasManual -and
        [bool]$uiJson.hasVision -and $pngRed.Count -eq 0
    $assertions.Add((New-Assertion 'ui' $uiOk `
        "Playwright /settings?tab=llm screenshot: model=$([bool]$uiJson.hasModel) context128=$([bool]$uiJson.hasContext128) output8K=$([bool]$uiJson.hasOutput8K) manual=$([bool]$uiJson.hasManual) vision=$([bool]$uiJson.hasVision) pngRed=$($pngRed.Count)" $true))
    $evidence.Add("$RunDir/issues14/screenshots/$shotName")

    $w0 = Get-W0Assertions @('issue-01a-model-manual', 'issue-01b-model-restart', 'issue-01c-model-ui')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }

    return (New-Problem -Id 1 -Title '模型 token 参数（窗口/输出）手工修复并持久化' `
        -W0CheckIds @('issue-01a-model-manual', 'issue-01b-model-restart', 'issue-01c-model-ui') `
        -Independent $true -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail "PATCH 131072/8192 then restore 128000/8192: DB window_source/output_source=manual and a second GET keeps the values; UI shows 128K/8.192K/manual. No API restart is performed by FA-8 (the restart path is W0 issue-01b-model-restart, PASS). before=$beforeContext/$beforeOutput.")
}

# ---------------------------------------------------------------------------
# Problem 2: one SUT binds two code repositories and codeintel indexes both.
# ---------------------------------------------------------------------------
function Invoke-Problem02 {
    param([string]$ProbeVersionId, [string]$Fa5VersionId, [string]$Fa5SutId)
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]

    $sutRepos = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('id',r.id::text,'sut_id',r.sut_id::text,'repo_id',r.repo_id::text,'role',r.role,'name',c.name,'path',c.path,'status',c.status) order by r.role),'[]'::json)::text from sut_repository r join code_repository c on c.id = r.repo_id where r.sut_id = $Fa5SutId and r.enabled")
    $evidence.Add((Save-EvidenceJson -Name '02-sut-repositories.json' -Object ([ordered]@{ database = $script:Database; sutId = $Fa5SutId; rows = @($sutRepos) })))
    $roles = @($sutRepos | ForEach-Object { [string]$_.role })
    $sutReposOk = $sutRepos.Count -ge 2 -and ($roles -contains 'backend') -and ($roles -contains 'frontend')
    $assertions.Add((New-Assertion 'db' $sutReposOk "sut_repository rows=$($sutRepos.Count) roles=$($roles -join ',') (need >=2 with backend+frontend)" $true))

    $fa5BindingResp = Invoke-FaApi -Method 'GET' -Path "/api/versions/$Fa5VersionId/binding"
    $evidence.Add((Save-ApiEvidence -Name '02-binding-fa5.json' -Response $fa5BindingResp))
    $fa5Repos = @(if ($fa5BindingResp.Status -eq 200) { $fa5BindingResp.Body.data.repositories } else { @() })
    $fa5Roles = @($fa5Repos | ForEach-Object { [string]$_.role })
    $fa5BindingOk = $fa5Repos.Count -eq 2 -and ($fa5Roles -contains 'backend') -and ($fa5Roles -contains 'frontend')
    $assertions.Add((New-Assertion 'api' $fa5BindingOk "FA-5 version binding repositories=$($fa5Repos.Count) roles=$($fa5Roles -join ',')" $true))

    $probeBindingResp = Invoke-FaApi -Method 'GET' -Path "/api/versions/$ProbeVersionId/binding"
    $evidence.Add((Save-ApiEvidence -Name '02-binding-probe.json' -Response $probeBindingResp))
    $probeRepos = @(if ($probeBindingResp.Status -eq 200) { $probeBindingResp.Body.data.repositories } else { @() })
    $assertions.Add((New-Assertion 'api' ($probeRepos.Count -eq 2) "probe version inherits $($probeRepos.Count) repositories from the environment-only SUT binding" $true))

    $codeReposResp = Invoke-FaApi -Method 'GET' -Path "/api/versions/$ProbeVersionId/code/repos"
    $evidence.Add((Save-ApiEvidence -Name '02-code-repos.json' -Response $codeReposResp))
    $codeRepos = if ($codeReposResp.Status -eq 200) { @($codeReposResp.Body.data) } else { @() }
    $indexRuns = New-Object System.Collections.Generic.List[object]
    foreach ($repo in $codeRepos) {
        $repoId = [string]$repo.repo_id
        $path = [string]$repo.repository.path
        $branch = [string]$repo.repository.branch
        $actual = ''
        if (Test-Path -LiteralPath $path) {
            $actual = (& git -C $path symbolic-ref --short HEAD 2>$null | Out-String).Trim()
        }
        if ($LASTEXITCODE -eq 0 -and -not [string]::IsNullOrWhiteSpace($actual) -and $actual -ne $branch) {
            $branchPatch = Invoke-FaApi -Method 'PATCH' -Path "/api/resources/code-repos/$repoId" -Body ([ordered]@{ branch = $actual })
            $evidence.Add((Save-ApiEvidence -Name ("02-code-repo-branch-$repoId.json") -Response $branchPatch))
        }
        $indexResp = Invoke-FaApi -Method 'POST' -Path "/api/versions/$ProbeVersionId/code/index" -Body ([ordered]@{ repoId = $repoId })
        $evidence.Add((Save-ApiEvidence -Name ("02-code-index-start-$repoId.json") -Response $indexResp))
        $runId = if ($indexResp.Status -eq 200 -or $indexResp.Status -eq 201) { [string]$indexResp.Body.data.id } else { '' }
        $status = ''
        if (-not [string]::IsNullOrWhiteSpace($runId)) {
            $deadline = (Get-Date).AddSeconds(180)
            while ((Get-Date) -lt $deadline) {
                $statusResp = Invoke-FaApi -Method 'GET' -Path "/api/code/repos/$repoId/index-runs/$runId"
                $status = [string]$statusResp.Body.data.status
                if ($status -in @('done', 'failed', 'skipped')) { break }
                Start-Sleep -Seconds 2
            }
            $statusResp = Invoke-FaApi -Method 'GET' -Path "/api/code/repos/$repoId/index-runs/$runId"
            $evidence.Add((Save-ApiEvidence -Name ("02-code-index-run-$repoId.json") -Response $statusResp))
        }
        $indexRuns.Add([ordered]@{ repoId = $repoId; name = [string]$repo.repository.name; path = $path; runId = $runId; status = $status })
    }
    $doneRuns = @($indexRuns | Where-Object { [string]$_.status -eq 'done' })
    $assertions.Add((New-Assertion 'api' ($doneRuns.Count -ge 2) "codeintel index runs done=$($doneRuns.Count)/$($indexRuns.Count) (need >=2 repositories)" $true))

    $runIds = @($indexRuns | ForEach-Object { [string]$_.runId } | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    $dbIndex = @()
    if ($runIds.Count -gt 0) {
        $inClause = ($runIds | ForEach-Object { "'$_'" }) -join ','
        $dbIndex = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('id',id::text,'repo_id',repo_id::text,'status',status,'files_indexed',files_indexed,'files_total',files_total,'commit_sha',commit_sha) order by id),'[]'::json)::text from code_index_run where id in ($inClause) and status = 'done'")
    }
    $evidence.Add((Save-EvidenceJson -Name '02-code-index-db.json' -Object ([ordered]@{ database = $script:Database; runIds = @($runIds); rows = @($dbIndex) })))
    $dbRepoCount = @($dbIndex | ForEach-Object { [string]$_.repo_id } | Select-Object -Unique).Count
    $assertions.Add((New-Assertion 'db' ($dbRepoCount -ge 2) "code_index_run done rows=$($dbIndex.Count) distinct repos=$dbRepoCount (need >=2)" $true))

    $w0 = Get-W0Assertions @('issue-02-multi-repo')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 2 -Title '一个 SUT 绑定多个代码仓（版本继承 + 双仓 codeintel）' `
        -W0CheckIds @('issue-02-multi-repo') -Independent $true -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail "sut_repository >=2; FA-5 and probe version bindings expose 2 repositories; codeintel indexed $dbRepoCount repositories (status=done in $($script:Database)).")
}

# ---------------------------------------------------------------------------
# Problem 3: single writer / no requirement_curator double job.
# ---------------------------------------------------------------------------
function Invoke-Problem03 {
    param([string]$Fa5ConversationId)
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]

    $profileRows = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('profile_key',profile_key,'status',status,'count',cnt) order by profile_key,status),'[]'::json)::text from (select profile_key, status, count(*) as cnt from agent_run where conversation_id = $Fa5ConversationId group by profile_key, status) t")
    $evidence.Add((Save-EvidenceJson -Name '03-agent-run-profiles.json' -Object ([ordered]@{ conversationId = $Fa5ConversationId; rows = @($profileRows) })))
    $forbidden = @($profileRows | Where-Object { [string]$_.profile_key -match 'requirement_curator|scout|planner' })
    $assertions.Add((New-Assertion 'db' ($forbidden.Count -eq 0) "agent_run conversation profiles=$((@($profileRows | ForEach-Object { [string]$_.profile_key }) -join ',')) forbidden=$($forbidden.Count)" $false))

    $authorRows = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('author_agent',author_agent,'count',cnt) order by author_agent),'[]'::json)::text from (select author_agent, count(*) as cnt from conversation_message where conversation_id = $Fa5ConversationId and role = 'assistant' and coalesce(author_agent, '') <> '' group by author_agent) t")
    $evidence.Add((Save-EvidenceJson -Name '03-assistant-authors.json' -Object ([ordered]@{ conversationId = $Fa5ConversationId; rows = @($authorRows) })))
    $forbiddenAuthors = @($authorRows | Where-Object { [string]$_.author_agent -match 'requirement_curator|scout|planner' })
    $assertions.Add((New-Assertion 'db' ($forbiddenAuthors.Count -eq 0) "assistant author_agent rows=$($authorRows.Count) forbidden=$($forbiddenAuthors.Count)" $false))

    $w0 = Get-W0Assertions @('issue-03-single-writer')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 3 -Title '澄清只产生单写者回复，无 requirement_curator 双作业' `
        -W0CheckIds @('issue-03-single-writer') -Independent $false -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail 'Fresh W0 check issue-03-single-writer is PASS; the FA-5 conversation has no forbidden agent profile and no forbidden assistant author.')
}

# ---------------------------------------------------------------------------
# Problem 4: two concurrent Host runs on one conversation -> the second gets
# the typed 409 conversation_busy and writes no assistant message.
# ---------------------------------------------------------------------------
function Get-ConversationRuns {
    param([string]$ConversationId)
    $resp = Invoke-FaApi -Method 'GET' -Path '/api/agent-center/runs?limit=100'
    if ($resp.Status -ne 200) { return @() }
    return @(@($resp.Body.data) | Where-Object { [string]$_.conversation_id -eq [string]$ConversationId })
}

function Wait-ConversationSettled {
    param([string]$ConversationId, [int]$TimeoutSec = 180)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        $running = @(Get-ConversationRuns $ConversationId | Where-Object { [string]$_.status -eq 'running' })
        if ($running.Count -eq 0) { return $true }
        Start-Sleep -Seconds 2
    }
    return $false
}

function Invoke-Problem04 {
    param([string]$ProbeVersionId)
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]
    $stamp = $script:Stamp

    $convResp = Invoke-FaApi -Method 'POST' -Path "/api/versions/$ProbeVersionId/conversations" -Body ([ordered]@{
        kind = 'clarification'; title = "FA-8 concurrency probe $stamp"
    })
    $evidence.Add((Save-ApiEvidence -Name '04-conversation.json' -Response $convResp))
    if ($convResp.Status -ne 201 -and $convResp.Status -ne 200) {
        $assertions.Add((New-Assertion 'api' $false "could not create concurrency conversation (http=$($convResp.Status))" $true))
        $w0 = Get-W0Assertions @('issue-04-no-duplicate-assistant')
        foreach ($a in $w0.Assertions) { $assertions.Add($a) }
        foreach ($e in $w0.Evidence) { $evidence.Add($e) }
        return (New-Problem -Id 4 -Title '澄清/设计对话并发不串台、不重复 assistant' `
            -W0CheckIds @('issue-04-no-duplicate-assistant') -Independent $true -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
            -Detail 'Could not create the concurrency probe conversation; item recorded RED.')
    }
    $conversationId = [string]$convResp.Body.data.id

    Start-Sleep -Seconds 3
    $openingSeen = $false
    $openDeadline = (Get-Date).AddSeconds(60)
    while ((Get-Date) -lt $openDeadline) {
        if (@(Get-ConversationRuns $conversationId).Count -gt 0) { $openingSeen = $true; break }
        Start-Sleep -Seconds 1
    }
    [void](Wait-ConversationSettled -ConversationId $conversationId -TimeoutSec 120)

    $msgAResp = Invoke-FaApi -Method 'POST' -Path "/api/versions/$ProbeVersionId/conversations/$conversationId/messages" -Body ([ordered]@{
        client_message_id = "fa8-conc-a-$stamp"
        parts             = @(@{ type = 'text'; text = 'In three short paragraphs, describe what this test version is for and what the environment contains.' })
    })
    $evidence.Add((Save-ApiEvidence -Name '04-message-a.json' -Response $msgAResp))
    $msgAId = if ($msgAResp.Status -eq 201) { [string]$msgAResp.Body.data.id } else { '' }
    $assertions.Add((New-Assertion 'api' ($msgAResp.Status -eq 201) "concurrency message A -> http=$($msgAResp.Status) id=$msgAId" $true))

    $runningRun = $null
    $runningDeadline = (Get-Date).AddSeconds(60)
    while ((Get-Date) -lt $runningDeadline) {
        $candidate = @(Get-ConversationRuns $conversationId | Where-Object { [string]$_.status -eq 'running' } | Select-Object -First 1)
        if ($candidate.Count -gt 0) { $runningRun = $candidate[0]; break }
        Start-Sleep -Seconds 1
    }
    $runningRunId = if ($null -ne $runningRun) { [string]$runningRun.id } else { '' }
    $assertions.Add((New-Assertion 'api' ($null -ne $runningRun) "first Host run observed running before the second post: id=$runningRunId" $true))

    $msgBResp = Invoke-FaApi -Method 'POST' -Path "/api/versions/$ProbeVersionId/conversations/$conversationId/messages" -Body ([ordered]@{
        client_message_id = "fa8-conc-b-$stamp"
        parts             = @(@{ type = 'text'; text = 'Also add one sentence about the environment that is bound to this version.' })
    })
    $evidence.Add((Save-ApiEvidence -Name '04-message-b.json' -Response $msgBResp))
    $msgBId = if ($msgBResp.Status -eq 201) { [string]$msgBResp.Body.data.id } else { '' }
    $assertions.Add((New-Assertion 'api' ($msgBResp.Status -eq 201) "concurrency message B -> http=$($msgBResp.Status) id=$msgBId" $true))

    $busyRun = $null
    $busyDeadline = (Get-Date).AddSeconds(120)
    while ((Get-Date) -lt $busyDeadline) {
        $candidate = @(Get-ConversationRuns $conversationId | Where-Object { [string]$_.terminal_reason -eq 'conversation_busy' } | Select-Object -First 1)
        if ($candidate.Count -gt 0) { $busyRun = $candidate[0]; break }
        Start-Sleep -Seconds 1
    }
    $busyRunId = if ($null -ne $busyRun) { [string]$busyRun.id } else { '' }
    $busyOk = $null -ne $busyRun -and [string]$busyRun.profile_key -eq 'clarification_host' -and
        [string]$busyRun.model -eq 'deepseek-flash' -and [string]$busyRun.status -eq 'failed'
    $assertions.Add((New-Assertion 'api' $busyOk `
        "second concurrent Host run -> terminal_reason=$([string](Get-JsonValue $busyRun 'terminal_reason' '')) status=$([string](Get-JsonValue $busyRun 'status' '')) profile=$([string](Get-JsonValue $busyRun 'profile_key' '')) model=$([string](Get-JsonValue $busyRun 'model' '')) id=$busyRunId" $true))

    [void](Wait-ConversationSettled -ConversationId $conversationId -TimeoutSec 180)
    $runsAfter = @(Get-ConversationRuns $conversationId)
    $evidence.Add((Save-EvidenceJson -Name '04-concurrency-runs.json' -Object ([ordered]@{ conversationId = $conversationId; runs = @($runsAfter) })))
    $messagesResp = Invoke-FaApi -Method 'GET' -Path "/api/versions/$ProbeVersionId/conversations/$conversationId/messages?limit=100"
    $evidence.Add((Save-ApiEvidence -Name '04-concurrency-messages.json' -Response $messagesResp))

    $busyRows = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('id',id::text,'profile_key',profile_key,'status',status,'terminal_reason',terminal_reason,'model',model,'conversation_id',conversation_id::text) order by id),'[]'::json)::text from agent_run where conversation_id = $conversationId and terminal_reason = 'conversation_busy'")
    $evidence.Add((Save-EvidenceJson -Name '04-busy-runs-db.json' -Object ([ordered]@{ conversationId = $conversationId; rows = @($busyRows) })))
    $busyDbOk = $busyRows.Count -ge 1
    $assertions.Add((New-Assertion 'db' $busyDbOk "agent_run conversation_busy rows=$($busyRows.Count)" $true))

    $assistantByBusy = 0
    if ($busyRows.Count -gt 0) {
        $busyIds = @($busyRows | ForEach-Object { "'$([string]$_.id)'" }) -join ','
        $assistantByBusy = [int](Invoke-PsqlScalar "select count(*) from conversation_message where conversation_id = $conversationId and role = 'assistant' and agent_run_id in ($busyIds)")
    }
    $evidence.Add((Save-EvidenceJson -Name '04-assistant-by-busy-run.json' -Object ([ordered]@{ conversationId = $conversationId; busyRunIds = @($busyRows | ForEach-Object { [string]$_.id }); assistantMessages = $assistantByBusy })))
    $assertions.Add((New-Assertion 'db' ($assistantByBusy -eq 0) "assistant messages written by the busy run(s) = $assistantByBusy (must be 0)" $true))

    $duplicateReplies = [int](Invoke-PsqlScalar "select count(*) from (select reply_to_message_id from conversation_message where conversation_id = $conversationId and role = 'assistant' and reply_to_message_id is not null group by reply_to_message_id having count(*) > 1) t")
    $evidence.Add((Save-EvidenceJson -Name '04-duplicate-replies.json' -Object ([ordered]@{ conversationId = $conversationId; duplicateReplyGroups = $duplicateReplies })))
    $assertions.Add((New-Assertion 'db' ($duplicateReplies -eq 0) "duplicate assistant reply groups per user message = $duplicateReplies (must be 0)" $true))

    $repliesToB = 0
    if (-not [string]::IsNullOrWhiteSpace($msgBId)) {
        $repliesToB = [int](Invoke-PsqlScalar "select count(*) from conversation_message where conversation_id = $conversationId and role = 'assistant' and reply_to_message_id = $msgBId")
    }
    $evidence.Add((Save-EvidenceJson -Name '04-replies-to-b.json' -Object ([ordered]@{ conversationId = $conversationId; messageB = $msgBId; replies = $repliesToB })))
    $assertions.Add((New-Assertion 'db' ($repliesToB -le 1) "assistant replies linked to message B = $repliesToB (must be <=1)" $true))

    $w0 = Get-W0Assertions @('issue-04-no-duplicate-assistant')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 4 -Title '澄清/设计对话并发不串台、不重复 assistant' `
        -W0CheckIds @('issue-04-no-duplicate-assistant') -Independent $true -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail "Conversation ${conversationId}: first Host run $runningRunId was running when message B arrived; the second run $busyRunId ended with terminal_reason=conversation_busy and wrote 0 assistant messages; no duplicate assistant replies per user message; openingSeen=$openingSeen.")
}

# ---------------------------------------------------------------------------
# Problem 5: extraction count equals the 12 FR sections.
# ---------------------------------------------------------------------------
function Invoke-Problem05 {
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]
    $reqPath = Join-Path $script:RunRoot 'typical\requirements.json'
    $extPath = Join-Path $script:RunRoot 'typical\_extraction.json'
    $reqCount = -1
    $runExpected = -1
    $runExtracted = -1
    $runMissing = -1
    $runStatus = ''
    $failedSegments = -1
    if (Test-Path -LiteralPath $reqPath) {
        $reqDoc = Read-JsonUtf8 -Path $reqPath
        $reqCount = @(Get-ArrayValue $reqDoc 'requirements').Count
    }
    if (Test-Path -LiteralPath $extPath) {
        $extDoc = Read-JsonUtf8 -Path $extPath
        $run = Get-JsonValue $extDoc 'run'
        $runExpected = [int](Get-JsonValue $run 'expected' -1)
        $runExtracted = [int](Get-JsonValue $run 'extracted' -1)
        $runMissing = [int](Get-JsonValue $run 'missing' -1)
        $failedSegments = [int](Get-JsonValue $run 'failed_segments' -1)
        $runStatus = [string](Get-JsonValue $run 'status' '')
    }
    $assertions.Add((New-Assertion 'file' ($reqCount -eq 12) "typical/requirements.json requirements=$reqCount (expected 12)" $true))
    $extractionOk = $runExpected -eq 12 -and $runExtracted -eq 12 -and $runMissing -eq 0 -and $runStatus -eq 'done' -and $failedSegments -eq 0
    $assertions.Add((New-Assertion 'file' $extractionOk "typical/_extraction.json status=$runStatus expected=$runExpected extracted=$runExtracted missing=$runMissing failedSegments=$failedSegments" $true))
    $evidence.Add("$RunDir/typical/requirements.json")
    $evidence.Add("$RunDir/typical/_extraction.json")
    $w0 = Get-W0Assertions @('issue-05-extraction-completeness')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 5 -Title '抽取条数与 FR 清点一致（12 条）且无 token 终止' `
        -W0CheckIds @('issue-05-extraction-completeness') -Independent $true -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail "The FA-5 extraction run reports expected=$runExpected extracted=$runExtracted missing=$runMissing failedSegments=$failedSegments and requirements.json has $reqCount rows.")
}

# ---------------------------------------------------------------------------
# Problem 6: long runs complete without a token-budget kill.
# ---------------------------------------------------------------------------
function Invoke-Problem06 {
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]
    $longRuns = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('id',id::text,'tokens',coalesce((usage->>'total_tokens')::bigint,0),'terminal_reason',terminal_reason,'status',status) order by id),'[]'::json)::text from agent_run where status = 'done' and coalesce((usage->>'total_tokens')::bigint, 0) > 20000")
    $evidence.Add((Save-EvidenceJson -Name '06-long-runs.json' -Object ([ordered]@{ database = $script:Database; longRuns = @($longRuns) })))
    $tokenKills = @($longRuns | Where-Object { [string]$_.terminal_reason -match 'token' })
    $assertions.Add((New-Assertion 'db' ($longRuns.Count -ge 1) "runs with >20k tokens and status=done: $($longRuns.Count) (need >=1)" $false))
    $assertions.Add((New-Assertion 'db' ($tokenKills.Count -eq 0) "token-budget terminal_reason among long runs: $($tokenKills.Count) (must be 0)" $false))
    $w0 = Get-W0Assertions @('issue-06-budget-no-token-kill')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 6 -Title '长 run 不再因 token 预算耗尽终止' `
        -W0CheckIds @('issue-06-budget-no-token-kill') -Independent $false -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail "No long run carries a token terminal_reason; fresh W0 issue-06-budget-no-token-kill is PASS.")
}

function Add-PngAssertion {
    param($Assertions, $Evidence, [string]$W0RelativePath, [string]$Label)
    $resolved = Resolve-EvidencePath $W0RelativePath
    $pngRed = @()
    if ($null -eq $resolved) { $pngRed = @('evidence file missing') }
    else { $pngRed = @(Assert-Png -Path $resolved) }
    $Assertions.Add((New-Assertion 'ui' ($pngRed.Count -eq 0) "${Label}: $W0RelativePath pngRed=$($pngRed.Count)" $false))
    if ($null -ne $resolved) { $Evidence.Add($W0RelativePath) }
}

# ---------------------------------------------------------------------------
# Problem 7: chat auto-scroll (W0 evidence + real PNG).
# ---------------------------------------------------------------------------
function Invoke-Problem07 {
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]
    Add-PngAssertion -Assertions $assertions -Evidence $evidence -W0RelativePath "$RunDir/w0flow/ui-chat-autoscroll.png" -Label 'chat auto-scroll screenshot'
    $w0 = Get-W0Assertions @('issue-07-chat-autoscroll')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 7 -Title '对话框自动滚动上下文' `
        -W0CheckIds @('issue-07-chat-autoscroll') -Independent $false -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail 'Fresh W0 issue-07-chat-autoscroll is PASS and the captured browser screenshot is a real non-blank PNG.')
}

# ---------------------------------------------------------------------------
# Problem 8: design UI layout (W0 evidence + real PNG).
# ---------------------------------------------------------------------------
function Invoke-Problem08 {
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]
    Add-PngAssertion -Assertions $assertions -Evidence $evidence -W0RelativePath "$RunDir/w0flow/ui-design.png" -Label 'design layout screenshot'
    $w0 = Get-W0Assertions @('issue-08-design-layout')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 8 -Title '用例设计 UI 三层布局' `
        -W0CheckIds @('issue-08-design-layout') -Independent $false -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail 'Fresh W0 issue-08-design-layout is PASS and the captured design screenshot is a real non-blank PNG.')
}

# ---------------------------------------------------------------------------
# Problem 9: case.graph_propose contract (W0 evidence + design graph content).
# ---------------------------------------------------------------------------
function Invoke-Problem09 {
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]
    $graphPath = Join-Path $script:W0Dir 'design-graph.json'
    $graphOk = $false
    $graphDetail = 'design-graph.json missing'
    if (Test-Path -LiteralPath $graphPath) {
        $graph = Read-JsonUtf8 -Path $graphPath
        $data = Get-JsonValue $graph 'data'
        $stats = Get-JsonValue $data 'stats'
        $versionId = [string](Get-JsonValue $data 'version_id' '')
        $cases = [int](Get-JsonValue $stats 'cases' 0)
        $graphOk = -not [string]::IsNullOrWhiteSpace($versionId) -and $cases -gt 0
        $graphDetail = "design-graph version_id=$versionId cases=$cases"
        $evidence.Add("$RunDir/w0flow/design-graph.json")
    }
    $assertions.Add((New-Assertion 'file' $graphOk $graphDetail $false))
    $w0 = Get-W0Assertions @('issue-09-graph-contract')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 9 -Title 'case.graph_propose 字段/断言契约' `
        -W0CheckIds @('issue-09-graph-contract') -Independent $false -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail 'Fresh W0 issue-09-graph-contract is PASS; the captured design graph has a version id and non-zero cases.')
}

# ---------------------------------------------------------------------------
# Problem 10: exactly two Hosts, no Scout/Curator/Planner.
# ---------------------------------------------------------------------------
function Invoke-Problem10 {
    param([string]$Fa5ConversationId)
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]
    $profileRows = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('profile_key',profile_key,'count',cnt) order by profile_key),'[]'::json)::text from (select profile_key, count(*) as cnt from agent_run where conversation_id = $Fa5ConversationId group by profile_key) t")
    $evidence.Add((Save-EvidenceJson -Name '10-host-profiles.json' -Object ([ordered]@{ conversationId = $Fa5ConversationId; rows = @($profileRows) })))
    $forbidden = @($profileRows | Where-Object { [string]$_.profile_key -match 'scout|requirement_curator|planner' })
    $assertions.Add((New-Assertion 'db' ($forbidden.Count -eq 0) "conversation profiles=$((@($profileRows | ForEach-Object { [string]$_.profile_key }) -join ',')) forbidden=$($forbidden.Count)" $false))
    $w0 = Get-W0Assertions @('issue-10-two-hosts')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 10 -Title '仅两个 Host，无 Scout/Curator/Planner' `
        -W0CheckIds @('issue-10-two-hosts') -Independent $false -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail 'Fresh W0 issue-10-two-hosts is PASS (clarification_host + design_host; no forbidden profile).')
}

# ---------------------------------------------------------------------------
# Problem 11: attachment image vision. The FA-5 input image is a direct message
# image part (not an embedded-document attachment_image row), so this item
# verifies both paths:
#   (a) FA-5 direct image part + a done clarification run using deepseek-flash
#       whose reply references the screenshot content;
#   (b) a real DOCX with an embedded PNG uploaded to the probe version, which
#       produces attachment_image.status=done plus a caption fact whose
#       payload.model is deepseek-flash.
# ---------------------------------------------------------------------------
function New-VisionProbeDocx {
    param([string]$Path)
    Add-Type -AssemblyName System.IO.Compression | Out-Null
    Add-Type -AssemblyName System.IO.Compression.FileSystem | Out-Null
    $imagePath = Join-Path $script:RunRoot 'typical\screenshots\00-sut-blog-list.png'
    if (-not (Test-Path -LiteralPath $imagePath)) {
        $imagePath = Join-Path $script:RepoRoot '.tmp\fa\vision-probe.png'
    }
    if (-not (Test-Path -LiteralPath $imagePath)) { throw 'no real PNG available for the embedded-image probe' }
    if (Test-Path -LiteralPath $Path) { Remove-Item -LiteralPath $Path -Force }
    $zip = [System.IO.Compression.ZipFile]::Open($Path, [System.IO.Compression.ZipArchiveMode]::Create)
    try {
        $contentTypes = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="png" ContentType="image/png"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>'
        $rootRels = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>'
        $document = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><w:body><w:p><w:r><w:t>FA-8 embedded image vision probe. The screenshot below is the real SUT blog list.</w:t></w:r></w:p><w:p><w:r><w:t>Embedded screenshot:</w:t></w:r><w:r><w:drawing><a:blip r:embed="rId10"/></w:drawing></w:r></w:p></w:body></w:document>'
        $documentRels = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId10" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.png"/></Relationships>'
        foreach ($entry in @(
                @{ Name = '[Content_Types].xml'; Text = $contentTypes },
                @{ Name = '_rels/.rels'; Text = $rootRels },
                @{ Name = 'word/document.xml'; Text = $document },
                @{ Name = 'word/_rels/document.xml.rels'; Text = $documentRels })) {
            $stream = $zip.CreateEntry($entry.Name, [System.IO.Compression.CompressionLevel]::Optimal).Open()
            $bytes = [System.Text.Encoding]::UTF8.GetBytes($entry.Text)
            $stream.Write($bytes, 0, $bytes.Length)
            $stream.Dispose()
        }
        $imageBytes = [System.IO.File]::ReadAllBytes($imagePath)
        $imageStream = $zip.CreateEntry('word/media/image1.png', [System.IO.Compression.CompressionLevel]::Optimal).Open()
        $imageStream.Write($imageBytes, 0, $imageBytes.Length)
        $imageStream.Dispose()
    }
    finally { $zip.Dispose() }
}

function Invoke-VisionProbeUpload {
    param([string]$ProbeVersionId, [string]$DocxPath)
    $mjsPath = Join-Path $script:TmpDir 'upload-vision-probe.mjs'
    $nodeSource = @'
import fs from 'node:fs'
import path from 'node:path'
const BASE = process.env.FA8_BASE
const VID = process.env.FA8_VID
const DOCX = process.env.FA8_DOCX
const TOKEN = process.env.FA8_TOKEN
function api(method, p, body) {
  const headers = { authorization: `Bearer ${TOKEN}` }
  if (body !== undefined) headers['content-type'] = 'application/json'
  return fetch(`${BASE}${p}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
    .then(async (res) => ({ status: res.status, text: await res.text() }))
    .then((out) => ({ status: out.status, json: (() => { try { return out.text ? JSON.parse(out.text) : null } catch { return null } })(), text: out.text }))
}
const conv = await api('POST', `/api/versions/${VID}/conversations`, { kind: 'clarification', title: `FA-8 vision probe ${Date.now()}` })
if (conv.status !== 201 && conv.status !== 200) { console.log(JSON.stringify({ error: `conversation ${conv.status}`, body: conv.text.slice(0, 300) })); process.exit(1) }
const cid = conv.json.data.id
const data = fs.readFileSync(DOCX)
const form = new FormData()
form.append('file', new Blob([data], { type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' }), path.basename(DOCX))
const up = await fetch(`${BASE}/api/content`, { method: 'POST', headers: { authorization: `Bearer ${TOKEN}` }, body: form })
const upText = await up.text()
if (up.status !== 201) { console.log(JSON.stringify({ error: `upload ${up.status}`, body: upText.slice(0, 300) })); process.exit(1) }
const content = JSON.parse(upText).data
const post = await api('POST', `/api/versions/${VID}/conversations/${cid}/messages`, {
  client_message_id: `fa8-vision-${Date.now()}`,
  parts: [{ type: 'attachment', sha256: content.sha256, name: path.basename(DOCX), mime: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', size: content.size }],
})
let attachmentId = ''
if (post.json?.error?.details?.attachment_id) attachmentId = String(post.json.error.details.attachment_id)
if (!attachmentId && content.attachment_id) attachmentId = String(content.attachment_id)
if (!attachmentId) { console.log(JSON.stringify({ error: 'upload did not return an attachment id', postStatus: post.status, postBody: post.json })); process.exit(1) }
let extraction = null
let images = []
for (let i = 0; i < 120; i++) {
  const ex = await api('GET', `/api/versions/${VID}/requirements/extractions`)
  const list = Array.isArray(ex.json?.data) ? ex.json.data : (ex.json?.data ? [ex.json.data] : [])
  extraction = list.find((e) => String(e.attachment_id) === attachmentId) || null
  const im = await api('GET', `/api/versions/${VID}/attachments/${attachmentId}/images`)
  images = im.json?.data || []
  const terminalImage = images.some((row) => ['done', 'failed', 'skipped'].includes(String(row.status)))
  const terminalExtraction = extraction && ['done', 'failed', 'skipped'].includes(String(extraction.status))
  if (terminalImage || (terminalExtraction && images.length > 0)) break
  await new Promise((r) => setTimeout(r, 2000))
}
console.log(JSON.stringify({ conversationId: String(cid), sha256: content.sha256, size: content.size, postStatus: post.status, postBody: post.json, attachmentId, extraction, images }))
'@
    Write-Utf8Text -Path $mjsPath -Text $nodeSource
    $stdoutPath = Join-Path $script:TmpDir 'vision-probe.out.json'
    $stderrPath = Join-Path $script:TmpDir 'vision-probe.err.log'
    $exit = Invoke-NodeHelper -ScriptPath $mjsPath -EnvValues @{
        FA8_BASE  = $script:ApiBase
        FA8_VID   = $ProbeVersionId
        FA8_DOCX  = $DocxPath
        FA8_TOKEN = $script:Token
    } -StdoutPath $stdoutPath -StderrPath $stderrPath
    return [pscustomobject]@{
        ExitCode = $exit
        Result   = (Read-LastJsonLine -Path $stdoutPath)
        Stdout   = $stdoutPath
        Stderr   = $stderrPath
    }
}

function Invoke-Problem11 {
    param([string]$ProbeVersionId, [string]$Fa5VersionId, [string]$Fa5ConversationId)
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]

    $imageMessages = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('message_id',id::text,'parts',parts,'created_at',created_at) order by id),'[]'::json)::text from conversation_message where conversation_id = $Fa5ConversationId and role = 'user' and parts @> jsonb_build_array(jsonb_build_object('type', 'image'))")
    $evidence.Add((Save-EvidenceJson -Name '11-fa5-image-messages.json' -Object ([ordered]@{ conversationId = $Fa5ConversationId; messages = @($imageMessages) })))
    $imagePart = $null
    $imageMessageId = ''
    foreach ($message in $imageMessages) {
        foreach ($part in @(Get-JsonValue $message 'parts')) {
            if ([string](Get-JsonValue $part 'type' '') -eq 'image') {
                $imagePart = $part
                $imageMessageId = [string](Get-JsonValue $message 'message_id' '')
                break
            }
        }
        if ($null -ne $imagePart) { break }
    }
    $imagePartOk = $null -ne $imagePart -and [string](Get-JsonValue $imagePart 'mime' '') -eq 'image/png' -and
        [int](Get-JsonValue $imagePart 'width' 0) -gt 0 -and [int](Get-JsonValue $imagePart 'height' 0) -gt 0 -and
        -not [string]::IsNullOrWhiteSpace([string](Get-JsonValue $imagePart 'data_ref' ''))
    $assertions.Add((New-Assertion 'db' $imagePartOk `
        "FA-5 direct image part message=$imageMessageId mime=$([string](Get-JsonValue $imagePart 'mime' '')) size=$([string](Get-JsonValue $imagePart 'width' ''))x$([string](Get-JsonValue $imagePart 'height' '')) data_ref=$([string](Get-JsonValue $imagePart 'data_ref' ''))" $true))

    $runsAfterImage = @()
    if (-not [string]::IsNullOrWhiteSpace($imageMessageId)) {
        $runsAfterImage = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('id',id::text,'profile_key',profile_key,'model',model,'status',status,'started_at',started_at,'finished_at',finished_at) order by started_at),'[]'::json)::text from (select id, profile_key, model, status, started_at, finished_at from agent_run where conversation_id = $Fa5ConversationId and profile_key = 'clarification_host' and status = 'done' and started_at > (select created_at from conversation_message where id = $imageMessageId) order by started_at limit 5) t")
    }
    $evidence.Add((Save-EvidenceJson -Name '11-fa5-vision-runs.json' -Object ([ordered]@{ messageId = $imageMessageId; runs = @($runsAfterImage) })))
    $visionRun = @($runsAfterImage | Where-Object { [string]$_.model -eq 'deepseek-flash' } | Select-Object -First 1)
    $assertions.Add((New-Assertion 'db' ($visionRun.Count -eq 1) `
        "done clarification_host run(s) after the image: $($runsAfterImage.Count); deepseek-flash=$($visionRun.Count)" $true))

    $assistantAfterImage = @()
    if (-not [string]::IsNullOrWhiteSpace($imageMessageId)) {
        $assistantAfterImage = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('id',id::text,'parts',parts,'created_at',created_at) order by id),'[]'::json)::text from (select id, parts, created_at from conversation_message where conversation_id = $Fa5ConversationId and role = 'assistant' and kind = 'text' and id > $imageMessageId order by id limit 5) t")
    }
    $evidence.Add((Save-EvidenceJson -Name '11-fa5-assistant-after-image.json' -Object ([ordered]@{ messageId = $imageMessageId; messages = @($assistantAfterImage) })))
    $visionReferenced = $false
    foreach ($message in $assistantAfterImage) {
        $text = (@(Get-JsonValue $message 'parts') | ForEach-Object { [string](Get-JsonValue $_ 'text' '') }) -join "`n"
        if ($text -match '最新发布|截图|screenshot') { $visionReferenced = $true; break }
    }
    $assertions.Add((New-Assertion 'db' $visionReferenced 'an assistant reply after the image references screenshot-specific content (最新发布/截图/screenshot)' $true))

    $docxPath = Join-Path $script:TmpDir 'fa8-vision-probe.docx'
    New-VisionProbeDocx -Path $docxPath
    $probe = Invoke-VisionProbeUpload -ProbeVersionId $ProbeVersionId -DocxPath $docxPath
    $probeResult = $probe.Result
    $evidence.Add((Save-EvidenceJson -Name '11-embedded-image-api.json' -Object ([ordered]@{
        nodeExit = $probe.ExitCode
        result   = $probeResult
        stderr   = if (Test-Path -LiteralPath $probe.Stderr) { [System.IO.File]::ReadAllText($probe.Stderr, [System.Text.Encoding]::UTF8) } else { '' }
    })))
    $probeAttachmentId = if ($null -ne $probeResult) { [string](Get-JsonValue $probeResult 'attachmentId' '') } else { '' }
    $imageRows = @()
    $factRows = @()
    if (-not [string]::IsNullOrWhiteSpace($probeAttachmentId)) {
        $imageRows = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('id',id::text,'attachment_id',attachment_id::text,'seq',seq,'mime',mime,'width',width,'height',height,'status',status,'caption',caption,'error',error) order by id),'[]'::json)::text from attachment_image where attachment_id = $probeAttachmentId")
        $factRows = @(Invoke-PsqlJson "select coalesce(json_agg(json_build_object('id',id::text,'kind',kind,'key',key,'payload',payload) order by id),'[]'::json)::text from version_fact where kind = 'attachment_image_caption' and version_id = $ProbeVersionId and key like 'attachment:${probeAttachmentId}:image:%'")
    }
    $evidence.Add((Save-EvidenceJson -Name '11-embedded-image-db.json' -Object ([ordered]@{
        database = $script:Database; versionId = $ProbeVersionId; attachmentId = $probeAttachmentId
        images = @($imageRows); facts = @($factRows)
    })))
    $doneImage = @($imageRows | Where-Object { [string]$_.status -eq 'done' -and -not [string]::IsNullOrWhiteSpace([string]$_.caption) } | Select-Object -First 1)
    $imageCaptionOk = $doneImage.Count -eq 1 -and ([string]$doneImage[0].caption).Length -ge 20 -and
        [string]$doneImage[0].mime -eq 'image/png' -and [int]$doneImage[0].width -gt 0 -and [int]$doneImage[0].height -gt 0
    $assertions.Add((New-Assertion 'db' $imageCaptionOk `
        "embedded attachment_image rows=$($imageRows.Count) done-with-caption=$($doneImage.Count) captionLength=$(if ($doneImage.Count -eq 1) { ([string]$doneImage[0].caption).Length } else { 0 })" $true))

    $factOk = $false
    $factDetail = 'no attachment_image_caption fact'
    if ($factRows.Count -ge 1) {
        $payload = Get-JsonValue $factRows[0] 'payload'
        $model = [string](Get-JsonValue $payload 'model' '')
        $caption = [string](Get-JsonValue $payload 'caption' '')
        $factOk = $model -eq 'deepseek-flash' -and -not [string]::IsNullOrWhiteSpace($caption)
        $factDetail = "fact model=$model captionLength=$($caption.Length) key=$([string](Get-JsonValue $factRows[0] 'key' ''))"
    }
    $assertions.Add((New-Assertion 'db' $factOk $factDetail $true))

    $w0 = Get-W0Assertions @('issue-11a-doc-images', 'issue-11b-vision-gate')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 11 -Title '附件图片视觉模型可读（真实 caption）' `
        -W0CheckIds @('issue-11a-doc-images', 'issue-11b-vision-gate') -Independent $true -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail "FA-5 input PNG is a direct message image part (no attachment_image row by design), read in-context by a done deepseek-flash clarification run whose reply references the screenshot. The embedded-image path is independently verified on the probe version: attachment $probeAttachmentId -> attachment_image.status=done + caption + version_fact.payload.model=deepseek-flash.")
}

# ---------------------------------------------------------------------------
# Problem 12: binding editable inside the gate, read-only after executing.
# ---------------------------------------------------------------------------
function Invoke-Problem12 {
    param([string]$ProbeVersionId, [string]$Fa5VersionId)
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]
    $probeBindingResp = Invoke-FaApi -Method 'GET' -Path "/api/versions/$ProbeVersionId/binding"
    $evidence.Add((Save-ApiEvidence -Name '12-binding-probe-before.json' -Response $probeBindingResp))
    $probeBinding = if ($probeBindingResp.Status -eq 200) { $probeBindingResp.Body.data } else { $null }
    $probeRepos = @(if ($null -ne $probeBinding) { $probeBinding.repositories } else { @() })
    $probePages = @(if ($null -ne $probeBinding) { $probeBinding.pages } else { @() })
    $pageId = if ($probePages.Count -gt 0) { [string]$probePages[0].id } else { '' }
    $assertions.Add((New-Assertion 'api' ($null -ne $probeBinding -and [bool]$probeBinding.editable -and [string]$probeBinding.stage -eq 'clarifying' -and $probeRepos.Count -eq 2) `
        "probe binding stage=$([string](Get-JsonValue $probeBinding 'stage' '')) editable=$([string](Get-JsonValue $probeBinding 'editable' '')) repositories=$($probeRepos.Count) pages=$($probePages.Count)" $true))

    $resetResp = Invoke-FaApi -Method 'PATCH' -Path "/api/versions/$ProbeVersionId/binding" -Body ([ordered]@{
        scope_exclude = [ordered]@{ pages = @(); apis = @(); databases = @() }
    })
    $evidence.Add((Save-ApiEvidence -Name '12-binding-probe-reset.json' -Response $resetResp))
    $beforeCount = $probePages.Count
    $patchResp = Invoke-FaApi -Method 'PATCH' -Path "/api/versions/$ProbeVersionId/binding" -Body ([ordered]@{
        scope_exclude = [ordered]@{ pages = @($pageId); apis = @(); databases = @() }
    })
    $evidence.Add((Save-ApiEvidence -Name '12-binding-probe-patch.json' -Response $patchResp))
    $afterResp = Invoke-FaApi -Method 'GET' -Path "/api/versions/$ProbeVersionId/binding"
    $evidence.Add((Save-ApiEvidence -Name '12-binding-probe-after.json' -Response $afterResp))
    $afterBinding = if ($afterResp.Status -eq 200) { $afterResp.Body.data } else { $null }
    $afterPages = @(if ($null -ne $afterBinding) { $afterBinding.pages } else { @() })
    $afterExclude = if ($null -ne $afterBinding) { Get-JsonValue $afterBinding 'scope_exclude' } else { $null }
    $excludedPages = @(if ($null -ne $afterExclude) { Get-JsonValue $afterExclude 'pages' } else { @() })
    $patchOk = $patchResp.Status -eq 200 -and $afterPages.Count -eq ($beforeCount - 1) -and
        @($excludedPages | ForEach-Object { [string]$_ }) -contains $pageId
    $assertions.Add((New-Assertion 'api' $patchOk `
        "PATCH binding at clarifying -> http=$($patchResp.Status); pages $beforeCount -> $($afterPages.Count); excluded=$($excludedPages -join ',')" $true))
    $restoreResp = Invoke-FaApi -Method 'PATCH' -Path "/api/versions/$ProbeVersionId/binding" -Body ([ordered]@{
        scope_exclude = [ordered]@{ pages = @(); apis = @(); databases = @() }
    })
    $evidence.Add((Save-ApiEvidence -Name '12-binding-probe-restore.json' -Response $restoreResp))
    $assertions.Add((New-Assertion 'api' ($restoreResp.Status -eq 200) "PATCH binding restore -> http=$($restoreResp.Status)" $true))

    $fa5BindingResp = Invoke-FaApi -Method 'GET' -Path "/api/versions/$Fa5VersionId/binding"
    $evidence.Add((Save-ApiEvidence -Name '12-binding-completed.json' -Response $fa5BindingResp))
    $fa5Binding = if ($fa5BindingResp.Status -eq 200) { $fa5BindingResp.Body.data } else { $null }
    $readOnlyOk = $null -ne $fa5Binding -and [string]$fa5Binding.stage -eq 'completed' -and -not [bool]$fa5Binding.editable
    $assertions.Add((New-Assertion 'api' $readOnlyOk `
        "FA-5 completed binding stage=$([string](Get-JsonValue $fa5Binding 'stage' '')) editable=$([string](Get-JsonValue $fa5Binding 'editable' ''))" $true))
    $readOnlyPatch = Invoke-FaApi -Method 'PATCH' -Path "/api/versions/$Fa5VersionId/binding" -Body ([ordered]@{
        scope_exclude = [ordered]@{ pages = @(); apis = @(); databases = @() }
    })
    $evidence.Add((Save-ApiEvidence -Name '12-binding-completed-patch.json' -Response $readOnlyPatch))
    $assertions.Add((New-Assertion 'api' ($readOnlyPatch.Status -eq 409 -or $readOnlyPatch.Status -eq 423) `
        "PATCH completed binding -> http=$($readOnlyPatch.Status) (read-only)" $true))

    $w0 = Get-W0Assertions @('issue-12a-binding-edit-in-gate', 'issue-12b-binding-readonly')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 12 -Title '新建版本后绑定可编辑（阶段门禁）' `
        -W0CheckIds @('issue-12a-binding-edit-in-gate', 'issue-12b-binding-readonly') -Independent $true -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail "Probe version at clarifying accepts a real PATCH (page $pageId excluded and restored); the FA-5 completed version is editable=false and PATCH returns 409/423. Fresh W0 issue-12a/12b are PASS.")
}

# ---------------------------------------------------------------------------
# Problem 13: environment-only version creation, default full binding.
# ---------------------------------------------------------------------------
function Invoke-Problem13 {
    param([string]$Fa5VersionId)
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]
    $bindingResp = Invoke-FaApi -Method 'GET' -Path "/api/versions/$Fa5VersionId/binding"
    $evidence.Add((Save-ApiEvidence -Name '13-binding.json' -Response $bindingResp))
    $binding = if ($bindingResp.Status -eq 200) { $bindingResp.Body.data } else { $null }
    $pages = @(if ($null -ne $binding) { $binding.pages } else { @() })
    $apis = @(if ($null -ne $binding) { $binding.apis } else { @() })
    $accounts = @(if ($null -ne $binding) { $binding.accounts } else { @() })
    $databases = @(if ($null -ne $binding) { $binding.databases } else { @() })
    $repos = @(if ($null -ne $binding) { $binding.repositories } else { @() })
    $ok = $pages.Count -eq 4 -and $apis.Count -eq 4 -and $accounts.Count -eq 1 -and $databases.Count -eq 1 -and $repos.Count -eq 2
    $assertions.Add((New-Assertion 'api' $ok `
        "binding pages=$($pages.Count) apis=$($apis.Count) accounts=$($accounts.Count) databases=$($databases.Count) repositories=$($repos.Count) (expected 4/4/1/1/2)" $false))
    $w0 = Get-W0Assertions @('issue-13-env-only-version')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 13 -Title '建版只关联环境，页面/API 默认全绑' `
        -W0CheckIds @('issue-13-env-only-version') -Independent $false -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail 'Fresh W0 issue-13-env-only-version is PASS; the FA-5 binding defaults to the environment approved resources (4 pages / 4 APIs / 1 account / 1 database / 2 repositories).')
}

# ---------------------------------------------------------------------------
# Problem 14: structured question card (W0 evidence + real PNGs).
# ---------------------------------------------------------------------------
function Invoke-Problem14 {
    $assertions = New-Object System.Collections.Generic.List[object]
    $evidence = New-Object System.Collections.Generic.List[string]
    Add-PngAssertion -Assertions $assertions -Evidence $evidence -W0RelativePath "$RunDir/w0flow/ui-question-card.png" -Label 'question card screenshot'
    Add-PngAssertion -Assertions $assertions -Evidence $evidence -W0RelativePath "$RunDir/w0flow/ui-question-answered.png" -Label 'answered question screenshot'
    $w0 = Get-W0Assertions @('issue-14-question-card')
    foreach ($a in $w0.Assertions) { $assertions.Add($a) }
    foreach ($e in $w0.Evidence) { $evidence.Add($e) }
    return (New-Problem -Id 14 -Title 'AI 提问以提问卡呈现，答案可回显' `
        -W0CheckIds @('issue-14-question-card') -Independent $false -Assertions $assertions.ToArray() -Evidence $evidence.ToArray() `
        -Detail 'Fresh W0 issue-14-question-card is PASS; the question-card and answered-card browser screenshots are real non-blank PNGs.')
}

# ---------------------------------------------------------------------------
# Producer entry point.
# ---------------------------------------------------------------------------
try {
    Write-Log "FA-8 producer start runDir=$RunDir database=$($script:Database)"
    [void](Initialize-W0State)
    Add-W0ChecksProjection

    $flowStatePath = Join-Path $script:RunRoot 'typical\_flow-state.json'
    if (-not (Test-Path -LiteralPath $flowStatePath)) { throw "missing FA-5 flow state: $flowStatePath" }
    $flowState = Read-JsonUtf8 -Path $flowStatePath
    $fa5VersionId = [string](Get-JsonValue $flowState 'versionId' '')
    $fa5SutId = [string](Get-JsonValue $flowState 'sutId' '')
    $fa5EnvironmentId = [string](Get-JsonValue $flowState 'environmentId' '')
    $fa5ConversationId = [string](Get-JsonValue $flowState 'conversationId' '')
    if ([string]::IsNullOrWhiteSpace($fa5VersionId) -or [string]::IsNullOrWhiteSpace($fa5SutId) -or
        [string]::IsNullOrWhiteSpace($fa5EnvironmentId) -or [string]::IsNullOrWhiteSpace($fa5ConversationId)) {
        throw 'FA-5 flow state is missing versionId/sutId/environmentId/conversationId'
    }

    $script:Token = Login-Admin -BaseUrl $script:ApiBase -Password (Get-EnvValue 'EZTEST_ADMIN_PASSWORD')
    Write-Log "admin login ok (token length=$($script:Token.Length))"
    $probeVersionId = Get-ProbeVersion -EnvironmentId $fa5EnvironmentId
    Write-Log "probe version id=$probeVersionId"

    $problems = New-Object System.Collections.Generic.List[object]
    $steps = @(
        @{ Name = '01 model parameters'; Action = { Invoke-Problem01 } },
        @{ Name = '02 multi repository'; Action = { Invoke-Problem02 -ProbeVersionId $probeVersionId -Fa5VersionId $fa5VersionId -Fa5SutId $fa5SutId } },
        @{ Name = '03 single writer'; Action = { Invoke-Problem03 -Fa5ConversationId $fa5ConversationId } },
        @{ Name = '04 concurrent conversation'; Action = { Invoke-Problem04 -ProbeVersionId $probeVersionId } },
        @{ Name = '05 extraction count'; Action = { Invoke-Problem05 } },
        @{ Name = '06 token budget'; Action = { Invoke-Problem06 } },
        @{ Name = '07 chat autoscroll'; Action = { Invoke-Problem07 } },
        @{ Name = '08 design layout'; Action = { Invoke-Problem08 } },
        @{ Name = '09 graph contract'; Action = { Invoke-Problem09 } },
        @{ Name = '10 two hosts'; Action = { Invoke-Problem10 -Fa5ConversationId $fa5ConversationId } },
        @{ Name = '11 attachment vision'; Action = { Invoke-Problem11 -ProbeVersionId $probeVersionId -Fa5VersionId $fa5VersionId -Fa5ConversationId $fa5ConversationId } },
        @{ Name = '12 binding edit'; Action = { Invoke-Problem12 -ProbeVersionId $probeVersionId -Fa5VersionId $fa5VersionId } },
        @{ Name = '13 environment-only version'; Action = { Invoke-Problem13 -Fa5VersionId $fa5VersionId } },
        @{ Name = '14 question card'; Action = { Invoke-Problem14 } }
    )
    foreach ($step in $steps) {
        Write-Log "collect: $($step.Name)"
        $problem = & $step.Action
        $problems.Add($problem)
        Write-Log "collected: id=$($problem.id) status=$($problem.status) assertions=$(@($problem.assertions).Count)"
    }

    $doc = [ordered]@{
        generatedAt = (Get-Date).ToString('o')
        problems    = @($problems | Sort-Object { [int]$_.id })
    }
    Write-JsonUtf8 -Path $script:IssuesPath -Object $doc -Depth 60
    Write-Log "wrote $($script:IssuesPath)"

    $red = @(Invoke-Issues14Verification -Root $script:EvidenceRoot)
    if ($red.Count -gt 0) {
        foreach ($item in $red) { Write-Log "VERIFY RED: $item" }
        Write-Log "VERIFY FAIL: $($red.Count) RED item(s) (evidence left in place)"
        exit 1
    }
    Write-Log 'VERIFY PASS: 14 rows, fresh W0 PASS, 6 independent assertions, evidence exists'

    $gatePath = Join-Path $script:RepoRoot 'scripts\v022-fa-gate.ps1'
    $gateLogPath = Join-Path $script:LogDir 'fa8-gate-issues14.log'
    $gateOutput = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $gatePath -Gate issues14 -RunDir $RunDir 2>&1
    $gateExit = $LASTEXITCODE
    Write-Utf8Text -Path $gateLogPath -Text (((@($gateOutput) -join "`r`n")) + "`r`n")
    foreach ($line in @($gateOutput)) { Write-Log "gate: $line" }
    if ($gateExit -ne 0) {
        Write-Log "GATE FAIL: issues14 exit=$gateExit"
        exit 1
    }

    $summaryLines = New-Object System.Collections.Generic.List[string]
    $summaryLines.Add("FA-8 GREEN at $(Get-Date -Format o)")
    $summaryLines.Add("runDir=$RunDir database=$($script:Database) probeVersion=$probeVersionId")
    foreach ($problem in @($doc.problems)) {
        $independent = if ([bool]$problem.independent) { 'independent' } else { 'w0-mapped' }
        $summaryLines.Add("problem $($problem.id) status=$($problem.status) ($independent) assertions=$(@($problem.assertions).Count) evidence=$(@($problem.evidence).Count)")
    }
    $summaryLines.Add("gate=scripts/v022-fa-gate.ps1 -Gate issues14 -RunDir $RunDir exit=$gateExit")
    $summaryLines.Add((@($gateOutput) -join [Environment]::NewLine))
    $greenText = (($summaryLines -join [Environment]::NewLine) + [Environment]::NewLine)
    Write-Utf8Text -Path (Join-Path $script:LogDir 'fa8-green.log') -Text $greenText
    Write-Utf8Text -Path (Join-Path (Join-Path $script:EvidenceRoot 'logs') 'fa8-green.log') -Text $greenText
    Write-Log 'FA-8 GREEN: issues14.json written and gate PASS'
    exit 0
}
catch {
    Write-Log "EXTERNAL-BLOCKED: $($_.Exception.Message)"
    if ($_.ScriptStackTrace) { Write-Log "STACK: $($_.ScriptStackTrace)" }
    exit 2
}
