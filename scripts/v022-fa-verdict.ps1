#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 final-acceptance (FA) verdict joiner.

.DESCRIPTION
    Reads the FA evidence tree, runs the read-only gate for every evidence
    family, and joins the gate exits plus the evidence JSON into
    run/fa-verdict.json (schema v1). It never invents a status: the status is
    derived only from gate exits, signoff and the P1 ledger.

      any gate EXTERNAL-BLOCKED (exit 2)  -> EXTERNAL-BLOCKED
      any gate FAIL (exit 1)             -> FAIL
      all gates green + unsigned/deferred -> PENDING-SIGNOFF (FA-C7)
      all gates green + signed            -> PASS

    The joiner writes only run/fa-verdict.json and the per-gate logs under
    RUN-x/logs/. It does not modify product code or git history.

.NOTES
    Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass `
            -File scripts\v022-fa-verdict.ps1 -RunDir RUN-2
    Exit codes: 0 = verdict written, 1 = could not write the verdict.
#>

param(
    [ValidatePattern('^RUN-[0-9]+$')]
    [string]$RunDir = 'RUN-2',

    [string]$EvidenceRoot,

    [string]$IssuesPath,

    [switch]$InitRunMeta,

    [switch]$Force
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

. (Join-Path $PSScriptRoot 'v022-fa-common.ps1')

$script:Utf8NoBom = New-Object System.Text.UTF8Encoding($false)
if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $script:EvidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final'
}
elseif ([System.IO.Path]::IsPathRooted($EvidenceRoot)) {
    $script:EvidenceRoot = [System.IO.Path]::GetFullPath($EvidenceRoot)
}
else {
    $script:EvidenceRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot $EvidenceRoot))
}
$script:RunRoot = Join-Path $script:EvidenceRoot $RunDir
$script:LogDir = Join-Path $script:RunRoot 'logs'
$gateScript = Join-Path $PSScriptRoot 'v022-fa-gate.ps1'
$script:Head = ''
try { $script:Head = Get-Head } catch { $script:Head = '' }

# ---------------------------------------------------------------------------
# -InitRunMeta: producer helper for a fresh RUN-x (FA-4/14). This is the only
# write path besides the verdict itself. It refuses to overwrite an existing
# run-meta unless -Force is explicit, so a stale run cannot be relabelled.
# ---------------------------------------------------------------------------
if ($InitRunMeta) {
    if ([string]::IsNullOrWhiteSpace($script:Head)) {
        Write-Host 'FAIL: cannot resolve git HEAD for run-meta'
        exit 1
    }
    $metaPath = Join-Path $script:RunRoot 'run-meta.json'
    if ((Test-Path -LiteralPath $metaPath) -and -not $Force) {
        Write-Host "FAIL: $metaPath already exists (use -Force to reinitialise a fresh run)"
        exit 1
    }
    $phase = if ($RunDir -eq 'RUN-1') { 'run1' } else { 'run2' }
    $shortHead = $script:Head.Substring(0, 8)
    $runId = 'FA-' + (Get-Date -Format 'yyyyMMddTHHmm') + '-' + $shortHead + '-' + ($RunDir -replace 'RUN-', 'R')
    $meta = [ordered]@{
        runId = $runId
        phase = $phase
        gitHead = $script:Head
        startedAt = (Get-Date).ToString('o')
        budgets = [ordered]@{ wallHours = 8; maxTokens = 10000000 }
        reuse = [ordered]@{ reused = $false }
    }
    try {
        Write-JsonUtf8 -Path $metaPath -Object $meta -Depth 10
    }
    catch {
        Write-Host "FAIL: could not write run-meta.json: $($_.Exception.Message)"
        exit 1
    }
    Write-Host "RUN-META: $metaPath (runId=$runId phase=$phase)"
    exit 0
}

function Get-Value {
    param($Object, [string[]]$Names, $Default = $null)
    if ($null -eq $Object) { return $Default }
    foreach ($name in $Names) {
        if ($Object -is [System.Collections.IDictionary]) {
            if ($Object.Contains($name)) { return $Object[$name] }
            continue
        }
        $property = $Object.PSObject.Properties[$name]
        if ($null -ne $property -and $null -ne $property.Value) { return $property.Value }
    }
    return $Default
}
function Get-Iarray($Value) {
    if ($null -eq $Value) { return @() }
    if ($Value -is [System.Array]) { return $Value }
    return @($Value)
}
function Get-StringValue($Object, [string[]]$Names, [string]$Default = '') {
    $value = Get-Value $Object $Names $null
    if ($null -eq $value) { return $Default }
    return [string]$value
}
function Get-IntValue($Object, [string[]]$Names, [int]$Default = -1) {
    $value = Get-Value $Object $Names $null
    if ($null -eq $value) { return $Default }
    $parsed = 0
    if ([int]::TryParse([string]$value, [ref]$parsed)) { return $parsed }
    return $Default
}
function Read-JsonIfExists([string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path)) { return $null }
    try { return (Read-JsonUtf8 -Path $Path) } catch { return $null }
}
function Get-RelativeEvidencePath([string]$Path) {
    return ($Path.Substring($script:EvidenceRoot.Length).TrimStart('\') -replace '\\', '/')
}
function Resolve-EvidencePath([string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) { return $null }
    $normalized = $Path -replace '/', '\'
    $candidates = @()
    if ([System.IO.Path]::IsPathRooted($normalized)) { $candidates += $normalized }
    else {
        $candidates += (Join-Path $script:EvidenceRoot $normalized)
        $candidates += (Join-Path $script:RunRoot $normalized)
        $candidates += (Join-Path $repoRoot $normalized)
    }
    foreach ($candidate in $candidates) {
        if (Test-Path -LiteralPath $candidate) { return (Resolve-Path -LiteralPath $candidate).Path }
    }
    return $null
}
function Quote-Arg([string]$Value) {
    return '"' + ($Value -replace '"', '\"') + '"'
}

New-Item -ItemType Directory -Force -Path $script:LogDir | Out-Null

$runMeta = Read-JsonIfExists (Join-Path $script:RunRoot 'run-meta.json')
$signoffPath = Join-Path $script:EvidenceRoot 'run\signoff.json'
$signoffJson = Read-JsonIfExists $signoffPath
$signoff = [ordered]@{
    userConfirmed = if ($null -ne $signoffJson) { [bool](Get-Value $signoffJson @('userConfirmed') $false) } else { $false }
    date = if ($null -ne $signoffJson) { Get-Value $signoffJson @('date') $null } else { $null }
    residuals = if ($null -ne $signoffJson) { @(Get-Iarray (Get-Value $signoffJson @('residuals'))) } else { @() }
}

# ---- read the fix records first so their per-issue gates can run -------------
$fixRecords = @()
$fixesDir = Join-Path $script:RunRoot 'fixes'
if (Test-Path -LiteralPath $fixesDir) {
    foreach ($directory in @(Get-ChildItem -LiteralPath $fixesDir -Directory -ErrorAction SilentlyContinue | Sort-Object Name)) {
        foreach ($candidate in @((Join-Path $directory.FullName 'fix.json'), (Join-Path $directory.FullName 'record.json'))) {
            $record = Read-JsonIfExists $candidate
            if ($null -ne $record) { $fixRecords += $record; break }
        }
    }
    foreach ($file in @(Get-ChildItem -LiteralPath $fixesDir -File -Filter '*.json' -ErrorAction SilentlyContinue | Sort-Object Name)) {
        $record = Read-JsonIfExists $file.FullName
        if ($null -ne $record) { $fixRecords += $record }
    }
}

# ---- run every evidence gate and keep its raw log ---------------------------
function Invoke-FaGate {
    param([string]$Name, [string[]]$ExtraArgs = @())

    $argumentLine = '-NoProfile -ExecutionPolicy Bypass -File ' + (Quote-Arg $gateScript) +
        ' -Gate ' + $Name + ' -RunDir ' + $RunDir + ' -EvidenceRoot ' + (Quote-Arg $script:EvidenceRoot)
    foreach ($extra in $ExtraArgs) { $argumentLine += ' ' + $extra }

    $startInfo = New-Object System.Diagnostics.ProcessStartInfo
    $startInfo.FileName = 'powershell.exe'
    $startInfo.Arguments = $argumentLine
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.CreateNoWindow = $true
    $startInfo.StandardOutputEncoding = [System.Text.Encoding]::UTF8
    $startInfo.StandardErrorEncoding = [System.Text.Encoding]::UTF8

    $watch = [System.Diagnostics.Stopwatch]::StartNew()
    $output = ''
    $exit = 1
    try {
        $process = [System.Diagnostics.Process]::Start($startInfo)
        $stdout = $process.StandardOutput.ReadToEnd()
        $stderr = $process.StandardError.ReadToEnd()
        $process.WaitForExit()
        $exit = $process.ExitCode
        $output = $stdout + $stderr
    }
    catch {
        $output = "gate process failed: $($_.Exception.Message)"
        $exit = 1
    }
    $watch.Stop()

    $safeName = ($Name -replace '[^A-Za-z0-9_.-]', '-')
    if ($ExtraArgs.Count -gt 0) {
        $issueArg = $ExtraArgs -join '-'
        $safeName = $safeName + '-' + ($issueArg -replace '[^A-Za-z0-9_.-]', '-')
    }
    $logName = "gate-$safeName.log"
    $logPath = Join-Path $script:LogDir $logName
    try { [System.IO.File]::WriteAllText($logPath, $output, $script:Utf8NoBom) }
    catch { $output += "`r`nlog write failed: $($_.Exception.Message)" }

    $sha = ''
    try { if (Test-Path -LiteralPath $logPath) { $sha = Get-Sha -Path $logPath } } catch { $sha = '' }

    return [pscustomobject]@{
        name = $Name
        cmd = "powershell.exe $argumentLine"
        exit = $exit
        durationSec = [math]::Round($watch.Elapsed.TotalSeconds, 2)
        log = (Get-RelativeEvidencePath $logPath)
        sha256 = $sha
        output = $output
    }
}

$gateResults = @()
$gateNames = @('inputs', 'prep', 'harness', 'flow', 'typical', 'acs', 'issues14', 'domains', 'exploration', 'visual', 'run1')
foreach ($gateName in $gateNames) {
    $extra = @()
    if ($gateName -eq 'acs') { $extra += @('-Range', 'all') }
    if ($gateName -eq 'run1' -and -not [string]::IsNullOrWhiteSpace($IssuesPath)) {
        $extra += @('-IssuesPath', (Quote-Arg $IssuesPath))
    }
    $gateResults += Invoke-FaGate -Name $gateName -ExtraArgs $extra
}
foreach ($record in $fixRecords) {
    $issueId = Get-StringValue $record @('issueId', 'id')
    if ([string]::IsNullOrWhiteSpace($issueId)) { continue }
    $gateResults += Invoke-FaGate -Name 'fix' -ExtraArgs @('-IssueId', (Quote-Arg $issueId))
}

$external = @($gateResults | Where-Object { $_.exit -eq 2 })
$failed = @($gateResults | Where-Object { $_.exit -ne 0 -and $_.exit -ne 2 })
$red = New-Object System.Collections.Generic.List[string]
foreach ($gate in $failed) {
    $lines = @($gate.output -split "`r?`n" | Where-Object { $_ -like 'RED: *' } | ForEach-Object { $_ -replace '^RED:\s*', '' })
    if ($lines.Count -eq 0) { $red.Add("gate $($gate.name) exited $($gate.exit)") }
    else { foreach ($line in $lines) { $red.Add($line) } }
}
foreach ($gate in $external) {
    $lines = @($gate.output -split "`r?`n" | Where-Object { $_ -like 'EXTERNAL-BLOCKED: *' } | ForEach-Object { $_ -replace '^EXTERNAL-BLOCKED:\s*', '' })
    if ($lines.Count -eq 0) { $red.Add("gate $($gate.name) is EXTERNAL-BLOCKED") }
    else { foreach ($line in $lines) { $red.Add($line) } }
}

# ---- read the evidence payloads --------------------------------------------
$flowResult = Read-JsonIfExists (Join-Path $script:RunRoot 'w0flow\result.json')
if ($null -eq $flowResult) { $flowResult = Read-JsonIfExists (Join-Path $script:RunRoot 'w0flow\flow.json') }
$typical = Read-JsonIfExists (Join-Path $script:RunRoot 'typical\eztest.json')
$issues14 = Read-JsonIfExists (Join-Path $script:RunRoot 'issues14.json')
$w2 = Read-JsonIfExists (Join-Path $script:RunRoot 'w2\w2-exit-verdict.json')
$explorationRef = Read-JsonIfExists (Join-Path $script:RunRoot 'exploration\ref.json')
if ($null -eq $explorationRef) { $explorationRef = Read-JsonIfExists (Join-Path $script:RunRoot 'w2\exploration-verdict.json') }
$visualReview = Read-JsonIfExists (Join-Path $script:RunRoot 'visual\review.json')
$stageEvidenceJson = Read-JsonIfExists (Join-Path $script:RunRoot 'stageEvidence.json')
$p0 = Read-JsonIfExists (Join-Path $script:EvidenceRoot 'run\p0.json')
$inputManifest = Read-JsonIfExists (Join-Path $script:EvidenceRoot 'inputs\manifest.json')

$allAcs = @()
$acsRequirements = @{}
foreach ($file in @('acs-r01-r06.json', 'acs-r07-r12.json')) {
    $json = Read-JsonIfExists (Join-Path $script:RunRoot $file)
    if ($null -eq $json) { continue }
    $acs = @(Get-Iarray (Get-Value $json @('acs', 'acsList')))
    $allAcs += $acs
    foreach ($requirement in @(Get-Iarray (Get-Value $json @('requirements')))) {
        $rid = Get-StringValue $requirement @('id', 'requirementId')
        if ($rid) { $acsRequirements[$rid] = $requirement }
    }
}

$ezRequirements = @{}
if ($null -ne $typical) {
    foreach ($requirement in @(Get-Iarray (Get-Value $typical @('requirements')))) {
        if ($requirement -is [string]) { continue }
        $rid = Get-StringValue $requirement @('id', 'requirementId')
        if ($rid) { $ezRequirements[$rid] = $requirement }
    }
}

$requirements = @()
foreach ($rid in @('R01', 'R02', 'R03', 'R04', 'R05', 'R06', 'R07', 'R08', 'R09', 'R10', 'R11', 'R12')) {
    $acsForRequirement = @($allAcs | Where-Object { (Get-StringValue $_ @('requirementId', 'requirement_id')) -eq $rid })
    $acIds = @($acsForRequirement | ForEach-Object { Get-StringValue $_ @('id', 'acId') })
    $truth = if ($acsRequirements.ContainsKey($rid)) { Get-StringValue $acsRequirements[$rid] @('status') } else { 'missing' }
    $eztestVerdict = if ($ezRequirements.ContainsKey($rid)) { Get-StringValue $ezRequirements[$rid] @('eztestVerdict') } else { 'missing' }
    $evidencePaths = @()
    if ($ezRequirements.ContainsKey($rid)) { $evidencePaths = @(Get-Iarray (Get-Value $ezRequirements[$rid] @('evidence'))) }
    $requirements += [ordered]@{
        id = $rid
        status = $truth
        acIds = @($acIds)
        eztestVerdict = $eztestVerdict
        evidence = @($evidencePaths)
    }
}

$fourteenProblems = @()
if ($null -ne $issues14) {
    $problemSource = if ($issues14 -is [System.Array]) { @($issues14) } else { @(Get-Iarray (Get-Value $issues14 @('problems', 'issues'))) }
    foreach ($problem in $problemSource) {
        $fourteenProblems += [ordered]@{
            id = (Get-Value $problem @('id'))
            status = Get-StringValue $problem @('status')
            w0CheckIds = @(Get-Iarray (Get-Value $problem @('w0CheckIds')))
            assertions = @(Get-Iarray (Get-Value $problem @('assertions')))
            evidence = @(Get-Iarray (Get-Value $problem @('evidence')))
        }
    }
}

$domainFiles = [ordered]@{
    human = 'w2/human-verdict.json'
    issue = 'w2/issue/issue-verdict.json'
    export = 'w2/export/export-verdict.json'
    code = 'w2/code-verdict.json'
    visual = 'w2/visual-verdict.json'
    eval = 'w2/eval-verdict.json'
    gc = 'w2/gc-verdict.json'
}
$sevenDomains = @()
foreach ($domain in $domainFiles.Keys) {
    $relative = $domainFiles[$domain]
    $absolute = Join-Path $script:RunRoot $relative
    if (-not (Test-Path -LiteralPath $absolute) -and ($domain -eq 'issue' -or $domain -eq 'export')) {
        $flat = if ($domain -eq 'issue') { 'w2/issue-verdict.json' } else { 'w2/export-verdict.json' }
        $candidate = Join-Path $script:RunRoot $flat
        if (Test-Path -LiteralPath $candidate) { $absolute = $candidate; $relative = $flat }
    }
    $verdict = Read-JsonIfExists $absolute
    $sevenDomains += [ordered]@{
        domain = $domain
        verdict = if ($null -ne $verdict) { Get-StringValue $verdict @('status') } else { 'MISSING' }
        verdictPath = if (Test-Path -LiteralPath $absolute) { Get-RelativeEvidencePath $absolute } else { $relative }
        verdictSha256 = if (Test-Path -LiteralPath $absolute) { Get-Sha -Path $absolute } else { '' }
        counters = if ($null -ne $verdict) { Get-Value $verdict @('counters') $null } else { $null }
    }
}

$stageEvidence = @()
if ($null -ne $stageEvidenceJson) { $stageEvidence = @(Get-Iarray (Get-Value $stageEvidenceJson @('stages', 'stageEvidence'))) }
elseif ($null -ne $runMeta) { $stageEvidence = @(Get-Iarray (Get-Value $runMeta @('stageEvidence'))) }

$exploration = $null
if ($null -ne $explorationRef) {
    $idempotency = Get-Value $explorationRef @('idempotency')
    $exploration = [ordered]@{
        status = Get-StringValue $explorationRef @('status')
        reuse = Get-Value $explorationRef @('reuse') ([ordered]@{ reused = $false })
        idempotency = if ($null -ne $idempotency) { [ordered]@{ replayed = [bool](Get-Value $idempotency @('replayed') $false) } } else { [ordered]@{ replayed = [bool](Get-Value $explorationRef @('replayed') $false) } }
        currentWaveCaseRunIdsBefore = @(Get-Iarray (Get-Value $explorationRef @('currentWaveCaseRunIdsBefore', 'currentWaveBefore')))
        currentWaveCaseRunIdsAfter = @(Get-Iarray (Get-Value $explorationRef @('currentWaveCaseRunIdsAfter', 'currentWaveAfter')))
        nextWaveSnapshotContainsU = [bool](Get-Value $explorationRef @('nextWaveSnapshotContainsU') $false)
        nextWaveSnapshotContainsA = [bool](Get-Value $explorationRef @('nextWaveSnapshotContainsA') $false)
        coverageBeforeUncovered = Get-IntValue $explorationRef @('coverageBeforeUncovered') 0
        coverageAfterCovered = Get-IntValue $explorationRef @('coverageAfterCovered') 0
        screenshots = @(Get-Iarray (Get-Value $explorationRef @('screenshots')))
    }
}

$visual = $null
if ($null -ne $visualReview) {
    $visual = [ordered]@{
        reviewer = Get-StringValue $visualReview @('reviewer')
        reviewedScreenshots = Get-IntValue $visualReview @('reviewedScreenshots') 0
        vision = Get-Value $visualReview @('vision') ([ordered]@{})
        stages = @(Get-Iarray (Get-Value $visualReview @('stages')))
        screenshots = @(Get-Iarray (Get-Value $visualReview @('screenshots')))
        defects = @(Get-Iarray (Get-Value $visualReview @('defects')))
    }
}

$flow = $null
if ($null -ne $flowResult) {
    $agentRun = Get-Value $flowResult @('agentRun')
    $agentRuns = @(Get-Iarray (Get-Value $flowResult @('agentRuns')))
    $model = ''
    if ($null -ne $agentRun) { $model = Get-StringValue $agentRun @('model') }
    if ([string]::IsNullOrWhiteSpace($model) -and $agentRuns.Count -gt 0) { $model = Get-StringValue $agentRuns[0] @('model') }
    $screenshots = @()
    foreach ($stage in @(Get-Iarray (Get-Value $flowResult @('stages')))) {
        if ($stage -is [string]) { continue }
        $screenshots += @(Get-Iarray (Get-Value $stage @('screenshots')))
    }
    if ($screenshots.Count -eq 0) { $screenshots = @(Get-Iarray (Get-Value $flowResult @('screenshots'))) }
    $flow = [ordered]@{
        flowExit = Get-IntValue $flowResult @('flowExit') -1
        stage = Get-StringValue $flowResult @('stage')
        agentRuns = $(if ($agentRuns.Count -gt 0) { $agentRuns.Count } elseif ($null -ne $agentRun) { 1 } else { 0 })
        model = $model
        screenshots = @($screenshots)
    }
}

$restart = $null
if ($null -ne $w2) {
    $restartJson = Get-Value $w2 @('restart')
    $restart = [ordered]@{
        equal = if ($null -ne $restartJson) { [bool](Get-Value $restartJson @('equal') $false) } else { [bool](Get-Value $w2 @('restartEqual') $false) }
        db = if ($null -ne $restartJson) { Get-StringValue $restartJson @('db') } else { '' }
    }
}

$multiRepo = $null
foreach ($source in @($flowResult, $typical)) {
    if ($null -eq $source) { continue }
    $value = Get-Value $source @('multiRepo')
    if ($null -ne $value) { $multiRepo = $value; break }
}
if ($null -eq $multiRepo) { $multiRepo = [ordered]@{ sutRepositories = 0; briefingRepos = @(); referenced = $false } }

$questions = $null
foreach ($source in @($flowResult, $typical)) {
    if ($null -eq $source) { continue }
    $value = Get-Value $source @('questions')
    if ($null -ne $value) { $questions = $value; break }
}
if ($null -eq $questions) { $questions = [ordered]@{ answered = 0; expired = 0; screenshots = @() } }

$stageMachine = $null
foreach ($source in @($flowResult, $typical, $w2)) {
    if ($null -eq $source) { continue }
    $value = Get-Value $source @('stageMachine')
    if ($null -ne $value) { $stageMachine = $value; break }
}
if ($null -eq $stageMachine) { $stageMachine = [ordered]@{ stageEvents = 0; planItems = 0; planEvents = 0; facts = 0; unregistered = 0; unknown = 0 } }

# ---- P1 ledger counts -------------------------------------------------------
$p1Total = 0
$p1Concluded = 0
$p1DeferredApproved = 0
$p1DeferredWithoutApproval = @()
$issuesFile = if (-not [string]::IsNullOrWhiteSpace($IssuesPath)) { $IssuesPath } else { Join-Path $repoRoot 'docs\version-V0.22\acceptance\issues.md' }
if (-not [System.IO.Path]::IsPathRooted($issuesFile)) { $issuesFile = Join-Path $repoRoot $issuesFile }
if (Test-Path -LiteralPath $issuesFile) {
    $text = [System.IO.File]::ReadAllText($issuesFile, [System.Text.Encoding]::UTF8)
    foreach ($line in ($text -split "`r?`n")) {
        $trimmed = $line.Trim()
        if (-not $trimmed.StartsWith('|')) { continue }
        if ($trimmed -match '^\|[\s\-:|]+\|$') { continue }
        $cells = @($trimmed.Trim('|') -split '\|' | ForEach-Object { $_.Trim() })
        if ($cells.Count -lt 9) { continue }
        if ($cells[0] -match '^(?i)ID$') { continue }
        if ($cells[1].ToUpperInvariant() -ne 'P1') { continue }
        $p1Total++
        $status = $cells[7].ToLowerInvariant()
        if ($status -in @('fixed', 'deferred')) { $p1Concluded++ }
        if ($status -eq 'deferred') {
            if ($trimmed -match 'approved_by\s*[=:]\s*user') { $p1DeferredApproved++ }
            else { $p1DeferredWithoutApproval += $cells[0] }
        }
    }
}

$p0Open = if ($null -ne $p0) { Get-IntValue $p0 @('open') 0 } else { 0 }
$defects = [ordered]@{
    p0Open = $p0Open
    p1Total = $p1Total
    p1Concluded = $p1Concluded
    p1DeferredApproved = $p1DeferredApproved
}

# ---- status (never invented) -----------------------------------------------
$status = 'PASS'
if ($external.Count -gt 0) {
    $status = 'EXTERNAL-BLOCKED'
}
elseif ($failed.Count -gt 0) {
    $status = 'FAIL'
}
else {
    $pendingReasons = @()
    if (-not $signoff.userConfirmed) { $pendingReasons += 'signoff.userConfirmed=false' }
    foreach ($id in $p1DeferredWithoutApproval) { $pendingReasons += "$id deferred P1 awaiting user approval" }
    if ($pendingReasons.Count -gt 0) {
        $status = 'PENDING-SIGNOFF'
        foreach ($reason in $pendingReasons) { $red.Add($reason) }
    }
}

# ---- input set / evidence tree ---------------------------------------------
$inputFile = Join-Path $script:EvidenceRoot 'inputs\typical-section1.md'
$inputSha = if (Test-Path -LiteralPath $inputFile) { Get-Sha -Path $inputFile } else { '' }
$inputSet = [ordered]@{
    file = 'inputs/typical-section1.md'
    sha256 = $inputSha
    requirements = if ($null -ne $inputManifest) { Get-IntValue $inputManifest @('requirements') 0 } else { 0 }
    acs = if ($null -ne $inputManifest) { Get-IntValue $inputManifest @('acs') 0 } else { 0 }
}

$evidenceFiles = @()
if (Test-Path -LiteralPath $script:EvidenceRoot) {
    $faVerdictAbs = [System.IO.Path]::GetFullPath((Join-Path $script:EvidenceRoot 'run\fa-verdict.json'))
    $indexAbs = [System.IO.Path]::GetFullPath((Join-Path $script:EvidenceRoot 'INDEX.md'))
    $reportAbs = [System.IO.Path]::GetFullPath((Join-Path $script:EvidenceRoot 'FINAL-REPORT.md'))
    foreach ($file in @(Get-ChildItem -LiteralPath $script:EvidenceRoot -Recurse -File -ErrorAction SilentlyContinue | Sort-Object FullName)) {
        $absolute = [System.IO.Path]::GetFullPath($file.FullName)
        if ($absolute -eq $faVerdictAbs -or $absolute -eq $indexAbs -or $absolute -eq $reportAbs) { continue }
        $evidenceFiles += [ordered]@{
            path = (Get-RelativeEvidencePath $absolute)
            bytes = $file.Length
            sha256 = (Get-Sha -Path $absolute)
        }
    }
}

$claims = @()
foreach ($ac in $allAcs) {
    $acId = Get-StringValue $ac @('id', 'acId')
    $evidencePaths = @(Get-Iarray (Get-Value $ac @('evidence')))
    $sha = ''
    if ($evidencePaths.Count -gt 0) {
        $resolved = Resolve-EvidencePath ([string]$evidencePaths[0])
        if ($null -ne $resolved) { $sha = Get-Sha -Path $resolved }
    }
    $screenshots = @(Get-Iarray (Get-Value $ac @('screenshots')))
    $claims += [ordered]@{
        claimId = $acId
        evidencePaths = @($evidencePaths)
        sha256 = $sha
        dbQuery = ''
        api = (Get-StringValue $ac @('observed'))
        event = ''
        screenshot = if ($screenshots.Count -gt 0) { [string]$screenshots[0] } else { '' }
    }
}

$runId = if ($null -ne $runMeta) { Get-StringValue $runMeta @('runId') } else { '' }
if ([string]::IsNullOrWhiteSpace($runId)) {
    $shortHead = if ($script:Head.Length -ge 8) { $script:Head.Substring(0, 8) } else { 'unknown' }
    $runId = 'FA-' + (Get-Date -Format 'yyyyMMddTHHmm') + '-' + $shortHead + '-' + ($RunDir -replace 'RUN-', 'R')
}

$faVerdict = [ordered]@{
    schemaVersion = 1
    status = $status
    runId = $runId
    gitHead = $script:Head
    generatedAt = (Get-Date).ToString('o')
    reuse = [ordered]@{ reused = if ($null -ne $runMeta) { [bool](Get-Value (Get-Value $runMeta @('reuse')) @('reused') $false) } else { $false } }
    red = @($red)
    gates = @($gateResults | ForEach-Object {
            [ordered]@{ name = $_.name; cmd = $_.cmd; exit = $_.exit; durationSec = $_.durationSec; log = $_.log; sha256 = $_.sha256 }
        })
    inputSet = $inputSet
    stageEvidence = @($stageEvidence)
    flow = $flow
    requirements = @($requirements)
    acs = @($allAcs)
    fourteenProblems = @($fourteenProblems)
    sevenDomains = @($sevenDomains)
    exploration = $exploration
    visual = $visual
    restart = $restart
    multiRepo = $multiRepo
    questions = $questions
    stageMachine = $stageMachine
    evidence = [ordered]@{
        root = $(if ($script:EvidenceRoot.StartsWith($repoRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
                ($script:EvidenceRoot.Substring($repoRoot.Length).TrimStart('\') -replace '\\', '/')
            }
            else { ($script:EvidenceRoot -replace '\\', '/') })
        files = @($evidenceFiles)
        claims = @($claims)
    }
    defects = $defects
    fixes = @($fixRecords)
    signoff = $signoff
}

$outPath = Join-Path $script:EvidenceRoot 'run\fa-verdict.json'
try {
    Write-JsonUtf8 -Path $outPath -Object $faVerdict -Depth 30
}
catch {
    Write-Host "FAIL: could not write fa-verdict.json: $($_.Exception.Message)"
    exit 1
}

Write-Host ("VERDICT: status={0} gates={1} red={2} runId={3}" -f $status, $gateResults.Count, $red.Count, $runId)
foreach ($gate in $gateResults) {
    Write-Host ("  gate {0,-12} exit={1} log={2}" -f $gate.name, $gate.exit, $gate.log)
}
exit 0
