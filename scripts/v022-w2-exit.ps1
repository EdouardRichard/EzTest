#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W2 exit gate (W2-16) - mechanical exit certification.

.DESCRIPTION
    Re-runs every W2 exit gate fresh and derives a machine verdict. Nothing is
    certified from prose or from an older verdict file.

    Run order (one serial pass, no parallel domain scripts):
      1. Fresh base data: rebuild eztest_v022_w2 through scripts/v022-w2-data.ps1
         (that script drops/creates only its own database), then run the seven
         domain scripts plus the exploration e2e one at a time. The known W2
         ports are released between every script, so no script can silently
         reuse a stale API/executor/web/proxy process.
      2. Static gates: constructor refs, API-only --strict, event consumers
         (unregistered=0 unknown=0), explore residue.
      3. Contract gates: v0.2.1 and v0.22, both must report exactly
         214 paths / 264 operations / 322 schemas.
      4. Backend unit: go test ./... -count=1.
      5. Backend integration: scripts/test-integration.ps1 ./... -p 1, serialized;
         the suite must actually run (no SKIP, at least five ok packages).
      6. Frontend unit + typecheck.
      7. Restart persistence: build the current HEAD backend, start the API on a
         dedicated W2-only port against the same eztest_v022_w2 database, capture
         the read API snapshots of every domain plus the DB/event counters, stop
         the API, start it again on the same database, capture the same snapshots
         and require byte-identical canonical values (restart.equal=true).
      8. Verdict: docs/version-V0.22/acceptance/evidence/w2/run/w2-exit-verdict.json
         with the contract {status,gitHead,generatedAt,gates,dbCounters,
         eventCounters,restart,red}; EXIT.md is generated from that verdict.

    PASS requires: every gate exit 0, red=[], all domain verdicts PASS with the
    required counters (export formats >= 2, indexed repos >= 2, visual states
    valid with at least one diff, trend points >= 2), exploration PASS, event
    unregistered=0, contract 214/264/322, migrations 64 and restart.equal=true.
    A missing external dependency (SUT, LLM, PostgreSQL) stops the run as
    EXTERNAL-BLOCKED (exit 2) and is never recorded as PASS.

    -RestartOnly is a development switch: it exercises only the DB/restart
    section, records an explicit debug red item and can never produce PASS.

.NOTES
    Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\v022-w2-exit.ps1
    Exit codes: 0 = PASS, 1 = FAIL, 2 = EXTERNAL-BLOCKED.
#>

param(
    [switch]$RestartOnly
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

# Native tools (curl/psql) emit UTF-8; decode it explicitly on a zh-CN host.
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\w2'
$runDir = Join-Path $evidenceRoot 'run'
$exitDir = Join-Path $runDir 'exit'
$exitVerdictPath = Join-Path $runDir 'w2-exit-verdict.json'
$exitMarkdownPath = Join-Path $evidenceRoot 'EXIT.md'
$work = Join-Path $repoRoot '.tmp\w2-exit'
$scriptsDir = Join-Path $repoRoot 'scripts'

$dataVerdictPath = Join-Path $runDir 'data-verdict.json'
$humanVerdictPath = Join-Path $runDir 'human-verdict.json'
$issueVerdictPath = Join-Path $runDir 'issue\issue-verdict.json'
$exportVerdictPath = Join-Path $runDir 'export\export-verdict.json'
$codeVerdictPath = Join-Path $runDir 'code-verdict.json'
$visualVerdictPath = Join-Path $runDir 'visual-verdict.json'
$explorationVerdictPath = Join-Path $runDir 'exploration-verdict.json'
$evalVerdictPath = Join-Path $runDir 'eval-verdict.json'
$gcVerdictPath = Join-Path $runDir 'gc-verdict.json'

$dbName = 'eztest_v022_w2'
$dbHost = 'localhost'
$dbPort = 5433
$dbUser = 'eztest'
$exitApiPort = 13453
$w2Ports = @(13445, 13446, 13447, 13448, 13449, 13450, 13451, 13452, 13453,
    6871, 6872, 6873, 6874, 13349, 13350, 13351, 13352, 13353, 13999)

New-Item -ItemType Directory -Force -Path $runDir, $exitDir, $work | Out-Null

# ---------------------------------------------------------------------------
# Helpers. Evidence JSON/logs are written as UTF-8 without BOM: Windows
# PowerShell 5.1 text cmdlets would otherwise rewrite CJK payloads in ANSI.
# ---------------------------------------------------------------------------
function Write-Utf8([string]$Path, [string]$Text) {
    $parent = Split-Path -Parent $Path
    if ($parent -and -not (Test-Path -LiteralPath $parent)) {
        New-Item -ItemType Directory -Force -Path $parent | Out-Null
    }
    [System.IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding($false)))
}

function Get-RepoRelative([string]$Path) {
    return ($Path.Substring($repoRoot.Length).TrimStart('\') -replace '\\', '/')
}

function Strip-Ansi([string]$Text) {
    if (-not $Text) { return '' }
    return ($Text -replace ([char]27 + '\[[0-9;]*[A-Za-z]'), '')
}

function Get-EnvValue([string]$Key) {
    $line = Select-String -Path (Join-Path $repoRoot '.env') -Pattern ('^' + [regex]::Escape($Key) + '=(.*)$')
    if (-not $line) { throw "missing $Key in .env" }
    return $line.Matches[0].Groups[1].Value
}

function Read-JsonFile([string]$Path) {
    return (Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json)
}

function Read-JsonFileSafe([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    try { return (Read-JsonFile $Path) } catch { return $null }
}

function Get-ItemCount($Value) {
    if ($null -eq $Value) { return 0 }
    return @($Value).Count
}

$goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'
if (-not (Test-Path -LiteralPath $goExe)) {
    $goCommand = Get-Command go.exe -ErrorAction SilentlyContinue
    if (-not $goCommand) { throw 'Go is not available (.tools/go/bin/go.exe or go.exe on PATH).' }
    $goExe = $goCommand.Source
}
$nodeExe = (Get-Command node.exe -ErrorAction Stop).Source
$pnpmExe = (Get-Command pnpm.cmd -ErrorAction SilentlyContinue).Source
if (-not $pnpmExe) { $pnpmExe = (Get-Command pnpm -ErrorAction Stop).Source }
$powershellExe = (Get-Command powershell.exe -ErrorAction Stop).Source
$curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
if (-not $curlExe) { throw 'curl.exe is required for the exit read-API checks.' }
$psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
if (-not (Test-Path -LiteralPath $psql)) {
    $psqlCommand = Get-Command psql.exe -ErrorAction SilentlyContinue
    if (-not $psqlCommand) { throw 'PostgreSQL client is not available (.tools/pgsql/bin/psql.exe or psql.exe on PATH).' }
    $psql = $psqlCommand.Source
}

try {
    # .env points at the release database (v0.2.1); the W2 gates must use the
    # dedicated W2 database, exactly like the domain scripts do. Only the
    # password is taken from .env.
    $envDbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
    $dbPassword = [regex]::Match($envDbUrl, 'postgres://[^:]+:([^@]+)@').Groups[1].Value
    if (-not $dbPassword) { throw 'cannot extract the password from EZTEST_DATABASE_URL' }
    $adminPassword = Get-EnvValue 'EZTEST_ADMIN_PASSWORD'
    $adminUsername = Get-EnvValue 'EZTEST_ADMIN_USERNAME'
    if ([string]::IsNullOrWhiteSpace($adminUsername)) { $adminUsername = 'admin' }
} catch {
    Write-Host "W2 EXIT: EXTERNAL-BLOCKED (cannot read .env: $($_.Exception.Message))"
    exit 2
}
$env:PGPASSWORD = $dbPassword
$w2DbUrl = "postgres://${dbUser}:${dbPassword}@${dbHost}:${dbPort}/${dbName}?sslmode=disable"

$gates = New-Object System.Collections.Generic.List[object]
$red = New-Object System.Collections.Generic.List[string]
$script:externalBlocked = $false
$script:abortDomains = $false
$script:restartToken = ''
$script:restartApiBase = "http://127.0.0.1:$exitApiPort"

# ---------------------------------------------------------------------------
# Port hygiene: each domain script may start an API/executor/web/proxy. The
# exit gate runs them serially and must never let the next script reuse a stale
# listener. Only the known W2 ports are touched; the SUT (8080/5173) is never
# killed.
# ---------------------------------------------------------------------------
function Stop-W2Ports {
    $problems = New-Object System.Collections.Generic.List[string]
    foreach ($port in $w2Ports) {
        $listeners = @(Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue)
        foreach ($listener in $listeners) {
            $ownerId = [int]$listener.OwningProcess
            if ($ownerId -gt 0) {
                Stop-Process -Id $ownerId -Force -ErrorAction SilentlyContinue
            }
        }
    }
    $pending = New-Object System.Collections.Generic.List[int]
    foreach ($port in $w2Ports) { $pending.Add($port) }
    $deadline = (Get-Date).AddSeconds(30)
    while ($pending.Count -gt 0 -and (Get-Date) -lt $deadline) {
        foreach ($port in @($pending)) {
            if (@(Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue).Count -eq 0) {
                $null = $pending.Remove($port)
            }
        }
        if ($pending.Count -gt 0) { Start-Sleep -Milliseconds 250 }
    }
    foreach ($port in $pending) {
        $owner = (@(Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue |
                    Select-Object -ExpandProperty OwningProcess -Unique) -join ',')
        $problems.Add("W2 port $port still has a listener after 30s (pid=$owner)")
    }

    # Next 16 writes frontend/.next/dev/lock. Remove it only when its recorded
    # pid is gone; a live holder is a genuine port/lock conflict and is red.
    $devLock = Join-Path $repoRoot 'frontend\.next\dev\lock'
    if (Test-Path -LiteralPath $devLock) {
        $lockOwnerId = 0
        try {
            $lockInfo = Get-Content -LiteralPath $devLock -Raw -Encoding UTF8 | ConvertFrom-Json
            if ($null -ne $lockInfo.pid) { $lockOwnerId = [int]$lockInfo.pid }
        } catch { }
        if ($lockOwnerId -le 0) {
            Remove-Item -LiteralPath $devLock -Force -ErrorAction SilentlyContinue
        } elseif (Get-Process -Id $lockOwnerId -ErrorAction SilentlyContinue) {
            $problems.Add("frontend/.next/dev/lock is held by live pid $lockOwnerId")
        } else {
            Remove-Item -LiteralPath $devLock -Force -ErrorAction SilentlyContinue
        }
    }
    return @($problems)
}

# ---------------------------------------------------------------------------
# Gate runner: one log per gate, exit code + duration + repo-relative log path.
# ---------------------------------------------------------------------------
function Invoke-ExitGate {
    param(
        [string]$Name,
        [string]$Command,
        [string]$File,
        [string[]]$Arguments,
        [string]$WorkDir,
        [scriptblock]$Validate
    )
    Write-Host ''
    Write-Host "== [$Name] $Command"
    $started = Get-Date
    $safeName = $Name -replace '[^A-Za-z0-9_.-]', '-'
    New-Item -ItemType Directory -Force -Path $exitDir | Out-Null
    $logPath = Join-Path $exitDir ($safeName + '.log')
    $exitCode = -1
    $output = $null
    Push-Location -LiteralPath $WorkDir
    try {
        $previousEAP = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        $global:LASTEXITCODE = 0
        try {
            $output = & $File @Arguments 2>&1
            $exitCode = $LASTEXITCODE
        } catch {
            $exitCode = 1
            $output = @("gate invocation error: $($_.Exception.Message)")
        } finally {
            $ErrorActionPreference = $previousEAP
        }
    } finally {
        Pop-Location
    }
    $text = (@($output) | ForEach-Object { [string]$_ }) -join "`r`n"
    Write-Utf8 $logPath ($text + "`r`n")
    $duration = [math]::Round(((Get-Date) - $started).TotalSeconds, 1)
    $problems = New-Object System.Collections.Generic.List[string]
    if ($exitCode -ne 0) {
        $problems.Add("exited $exitCode")
        if ($exitCode -eq 2) { $script:externalBlocked = $true }
    } elseif ($Validate) {
        foreach ($problem in @(& $Validate $text $started)) {
            if ($problem) { $problems.Add([string]$problem) }
        }
    }
    $gate = [ordered]@{
        name        = $Name
        cmd         = $Command
        exit        = $exitCode
        durationSec = $duration
        log         = (Get-RepoRelative $logPath)
    }
    if ($problems.Count -gt 0) { $gate['problems'] = @($problems) }
    $gates.Add([pscustomobject]$gate)
    foreach ($problem in $problems) { $red.Add("${Name}: $problem") }
    if ($exitCode -eq 0 -and $problems.Count -eq 0) {
        Write-Host "   -> exit 0 ($duration s) log=$(Get-RepoRelative $logPath)"
    } else {
        Write-Host "   -> exit $exitCode ($duration s) RED: $($problems -join '; ')"
        Write-Host "      log=$(Get-RepoRelative $logPath)"
    }
}

function Invoke-DomainGate {
    param(
        [string]$Name,
        [string]$ScriptName,
        [string]$VerdictPath,
        [scriptblock]$Validate
    )
    if ($script:abortDomains) { return }
    $portProblems = @(Stop-W2Ports)
    if ($portProblems.Count -gt 0) {
        foreach ($problem in $portProblems) { $red.Add("harness.ports: $problem") }
        $red.Add("${Name}: skipped because the W2 ports could not be released")
        $script:abortDomains = $true
        return
    }
    Invoke-ExitGate -Name $Name `
        -Command "powershell.exe -File scripts/$ScriptName" `
        -File $powershellExe -Arguments @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Join-Path $scriptsDir $ScriptName)) `
        -WorkDir $repoRoot -Validate $Validate
}

# ---------------------------------------------------------------------------
# Validators.
# ---------------------------------------------------------------------------
$validateExitZeroOnly = { param($text, $started) return @() }

$validateConstructor = {
    param($text, $started)
    if ($text -notmatch 'constructor guard: OK') { return @('missing "constructor guard: OK"') }
    return @()
}
$validateApiOnly = {
    param($text, $started)
    if ($text -notmatch 'registry holds 16 business routes') { return @('strict registry summary missing (expected 16 business routes)') }
    return @()
}
$validateEventConsumers = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    $unregistered = [regex]::Match($text, 'unregistered=(\d+)')
    $unknown = [regex]::Match($text, 'unknown=(\d+)')
    if (-not $unregistered.Success) {
        $problems.Add('missing unregistered=<n> in the gate output')
    } elseif ([int]$unregistered.Groups[1].Value -ne 0) {
        $problems.Add("unregistered=$($unregistered.Groups[1].Value) (expected 0)")
    }
    if (-not $unknown.Success) {
        $problems.Add('missing unknown=<n> in the gate output')
    } elseif ([int]$unknown.Groups[1].Value -ne 0) {
        $problems.Add("unknown=$($unknown.Groups[1].Value) (expected 0)")
    }
    return @($problems)
}
$validateExploreResidue = {
    param($text, $started)
    if ($text -notmatch 'clean \(') { return @('missing "clean (" result') }
    return @()
}
$validateContract = {
    param($text, $started)
    $countMatches = [regex]::Matches($text, '(\d+) paths / (\d+) operations / (\d+) schemas')
    $found = $false
    foreach ($match in $countMatches) {
        if ($match.Groups[1].Value -eq '214' -and $match.Groups[2].Value -eq '264' -and $match.Groups[3].Value -eq '322') {
            $found = $true
            break
        }
    }
    if (-not $found) { return @('contract counts are not exactly 214 paths / 264 operations / 322 schemas') }
    return @()
}
$validateUnit = {
    param($text, $started)
    $clean = Strip-Ansi $text
    $problems = New-Object System.Collections.Generic.List[string]
    if ($clean -match '(?m)^\s*FAIL\s') { $problems.Add('go test output contains FAIL') }
    if ($clean -match '(?m)^panic:') { $problems.Add('go test output contains panic') }
    return @($problems)
}
$validateIntegration = {
    param($text, $started)
    $clean = Strip-Ansi $text
    $problems = New-Object System.Collections.Generic.List[string]
    $okPackages = [regex]::Matches($clean, '(?m)^ok\s+github\.com/\S+').Count
    if ($okPackages -lt 5) {
        $problems.Add("only $okPackages integration package(s) reported ok (expected >= 5: the -tags=integration suite must actually run)")
    }
    if ($clean -match '(?m)^\s*--- SKIP') {
        $problems.Add('integration output contains a --- SKIP test (the suite must run, not skip)')
    }
    if ($clean -match '(?m)^\s*FAIL\s') {
        $problems.Add('integration output contains FAIL')
    }
    return @($problems)
}
$validateFrontendTest = {
    param($text, $started)
    $clean = Strip-Ansi $text
    $problems = New-Object System.Collections.Generic.List[string]
    if ($clean -notmatch 'Tests\s+\d+\s+passed') { $problems.Add('missing "Tests <n> passed" in the vitest output') }
    if ($clean -match 'Tests\s+\d+\s+failed') { $problems.Add('vitest output reports failed tests') }
    return @($problems)
}

function Test-VerdictBasics {
    param($Verdict, [string]$Name, [string[]]$Required)
    $problems = New-Object System.Collections.Generic.List[string]
    if ($null -eq $Verdict) { $problems.Add("$Name verdict missing or unreadable"); return @($problems) }
    if ([string]$Verdict.status -ne 'PASS') { $problems.Add("status=$($Verdict.status)") }
    if ((Get-ItemCount $Verdict.errors) -ne 0) { $problems.Add("errors: $(@($Verdict.errors) -join '; ')") }
    if ((Get-ItemCount $Verdict.red) -ne 0) { $problems.Add("red: $(@($Verdict.red) -join '; ')") }
    if ($Required) {
        foreach ($property in $Required) {
            if ([string]::IsNullOrWhiteSpace([string]$Verdict.$property)) {
                $problems.Add("$property is empty")
            }
        }
    }
    return @($problems)
}

function Test-VerdictFresh([string]$Path, $Started) {
    if (-not (Test-Path -LiteralPath $Path)) { return "missing verdict $Path" }
    if ((Get-Item -LiteralPath $Path).LastWriteTime -lt $Started.AddMinutes(-1)) {
        return "verdict is older than the gate start (not a fresh run): $Path"
    }
    return $null
}

$validateData = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    $fresh = Test-VerdictFresh $dataVerdictPath $started
    if ($fresh) { $problems.Add($fresh) }
    $v = Read-JsonFileSafe $dataVerdictPath
    foreach ($problem in @(Test-VerdictBasics $v 'data')) { $problems.Add($problem) }
    if ($null -eq $v) { return @($problems) }
    foreach ($key in 'requirements', 'cases', 'waves', 'caseRuns', 'evidenceRows', 'events') {
        if ([int64]$v.dbCounters.$key -lt 1) { $problems.Add("dbCounters.$key=$($v.dbCounters.$key)") }
    }
    if ([string]::IsNullOrWhiteSpace([string]$v.versionId)) { $problems.Add('versionId is empty') }
    if ($v.assertions) {
        foreach ($property in $v.assertions.PSObject.Properties) {
            if (-not $property.Value) { $problems.Add("assertion failed: $($property.Name)") }
        }
    }
    return @($problems)
}

$validateHuman = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    $fresh = Test-VerdictFresh $humanVerdictPath $started
    if ($fresh) { $problems.Add($fresh) }
    $v = Read-JsonFileSafe $humanVerdictPath
    foreach ($problem in @(Test-VerdictBasics $v 'human' @('versionId', 'approvalId', 'blockedId', 'conflictId', 'expiredId'))) { $problems.Add($problem) }
    if ($null -eq $v) { return @($problems) }
    if ([int]$v.events.requested -lt 1) { $problems.Add("events.requested=$($v.events.requested)") }
    if ([int]$v.events.decided -lt 1) { $problems.Add("events.decided=$($v.events.decided)") }
    if ([int]$v.expiredCount -lt 1) { $problems.Add("expiredCount=$($v.expiredCount)") }
    if ($v.timeoutSetting.restored -ne $true) { $problems.Add('timeoutSetting.restored is not true') }
    if ([string]$v.stage.after -ne 'executing') { $problems.Add("stage.after=$($v.stage.after) (expected executing)") }
    foreach ($kind in 'approval', 'blocked', 'conflict') {
        if ([string]$v.decided.$kind.status -ne 'answered') {
            $problems.Add("decided.$kind.status=$($v.decided.$kind.status)")
        }
    }
    return @($problems)
}

$validateIssue = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    $fresh = Test-VerdictFresh $issueVerdictPath $started
    if ($fresh) { $problems.Add($fresh) }
    $v = Read-JsonFileSafe $issueVerdictPath
    foreach ($problem in @(Test-VerdictBasics $v 'issue' @('versionId', 'issueId', 'retestWaveId'))) { $problems.Add($problem) }
    if ($null -eq $v) { return @($problems) }
    if (@('passed', 'failed') -notcontains [string]$v.retestStatus) { $problems.Add("retestStatus=$($v.retestStatus)") }
    if ([string]$v.retestWaveStatus -ne 'done') { $problems.Add("retestWaveStatus=$($v.retestWaveStatus)") }
    if ([string]$v.db.retestStatus -ne [string]$v.retestStatus) { $problems.Add("db.retestStatus=$($v.db.retestStatus) != retestStatus=$($v.retestStatus)") }
    if ([int]$v.events.terminal -lt 1) { $problems.Add("events.terminal=$($v.events.terminal)") }
    return @($problems)
}

$validateExport = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    $fresh = Test-VerdictFresh $exportVerdictPath $started
    if ($fresh) { $problems.Add($fresh) }
    $v = Read-JsonFileSafe $exportVerdictPath
    foreach ($problem in @(Test-VerdictBasics $v 'export' @('versionId', 'reportId'))) { $problems.Add($problem) }
    if ($null -eq $v) { return @($problems) }
    if ((Get-ItemCount $v.formats) -lt 2) { $problems.Add("formats=$(@($v.formats) -join ',') (expected >= 2)") }
    if ((Get-ItemCount $v.artifacts) -lt 2) { $problems.Add("artifacts=$(Get-ItemCount $v.artifacts) (expected >= 2)") }
    foreach ($artifact in @($v.artifacts)) {
        if ([string]$artifact.status -ne 'ready') { $problems.Add("artifact $($artifact.id) status=$($artifact.status)") }
        if (([string]$artifact.sha256).Length -ne 64) { $problems.Add("artifact $($artifact.id) sha256 is not 64 hex chars") }
    }
    if ($v.downloadOk -ne $true) { $problems.Add('downloadOk is not true') }
    if ([int]$v.events.completed -lt 1) { $problems.Add("events.completed=$($v.events.completed)") }
    return @($problems)
}

$validateCode = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    $fresh = Test-VerdictFresh $codeVerdictPath $started
    if ($fresh) { $problems.Add($fresh) }
    $v = Read-JsonFileSafe $codeVerdictPath
    foreach ($problem in @(Test-VerdictBasics $v 'code' @('versionId'))) { $problems.Add($problem) }
    if ($null -eq $v) { return @($problems) }
    if ([int]$v.indexRepos -lt 2) { $problems.Add("indexRepos=$($v.indexRepos) (expected >= 2)") }
    foreach ($key in 'repos', 'analysis', 'impact', 'anchors') {
        if ([int]$v.api.$key.status -ne 200) { $problems.Add("api.$key.status=$($v.api.$key.status)") }
    }
    return @($problems)
}

$validateVisual = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    $fresh = Test-VerdictFresh $visualVerdictPath $started
    if ($fresh) { $problems.Add($fresh) }
    $v = Read-JsonFileSafe $visualVerdictPath
    foreach ($problem in @(Test-VerdictBasics $v 'visual' @('versionId', 'assertionId'))) { $problems.Add($problem) }
    if ($null -eq $v) { return @($problems) }
    $states = @()
    if ($null -ne $v.visualStates) { $states = @($v.visualStates) }
    if ($states.Count -lt 1) { $problems.Add('visualStates is empty') }
    foreach ($state in $states) {
        if (@('passed', 'failed', 'blocked', 'inconclusive') -notcontains [string]$state) {
            $problems.Add("visualStates contains invalid state '$state'")
        }
    }
    if ([int]$v.dbCounters.diffs -lt 1) { $problems.Add("dbCounters.diffs=$($v.dbCounters.diffs) (expected >= 1)") }
    if ([int]$v.dbCounters.approvedBaselines -lt 1) { $problems.Add("dbCounters.approvedBaselines=$($v.dbCounters.approvedBaselines)") }
    return @($problems)
}

$validateEval = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    $fresh = Test-VerdictFresh $evalVerdictPath $started
    if ($fresh) { $problems.Add($fresh) }
    $v = Read-JsonFileSafe $evalVerdictPath
    foreach ($problem in @(Test-VerdictBasics $v 'eval' @('versionId', 'evalRunId'))) { $problems.Add($problem) }
    if ($null -eq $v) { return @($problems) }
    if ([int]$v.trendPoints -lt 2) { $problems.Add("trendPoints=$($v.trendPoints) (expected >= 2)") }
    $metricCount = 0
    if ($null -ne $v.metrics) { $metricCount = @($v.metrics.PSObject.Properties).Count }
    if ($metricCount -lt 4) { $problems.Add("metrics count=$metricCount (expected 4)") }
    if ([int]$v.events.completed -lt 1) { $problems.Add("events.completed=$($v.events.completed)") }
    foreach ($name in 'design_coverage', 'execution_pass_rate', 'case_coverage', 'visual_classification_accuracy') {
        if ([int]$v.trendSeries.$name -lt 2) { $problems.Add("trendSeries.$name=$($v.trendSeries.$name) (expected >= 2)") }
    }
    return @($problems)
}

$validateGc = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    $fresh = Test-VerdictFresh $gcVerdictPath $started
    if ($fresh) { $problems.Add($fresh) }
    $v = Read-JsonFileSafe $gcVerdictPath
    foreach ($problem in @(Test-VerdictBasics $v 'gc' @('dryRunId', 'applyRunId'))) { $problems.Add($problem) }
    if ($null -eq $v) { return @($problems) }
    if ([int]$v.events.completed -lt 1) { $problems.Add("events.completed=$($v.events.completed)") }
    if ([int]$v.applyTombstones -lt 1) { $problems.Add("applyTombstones=$($v.applyTombstones)") }
    return @($problems)
}

$validateExploration = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    $fresh = Test-VerdictFresh $explorationVerdictPath $started
    if ($fresh) { $problems.Add($fresh) }
    $v = Read-JsonFileSafe $explorationVerdictPath
    foreach ($problem in @(Test-VerdictBasics $v 'exploration' @('versionId', 'currentWaveId', 'nextWaveId'))) { $problems.Add($problem) }
    if ($null -eq $v) { return @($problems) }
    if ($v.replayed -ne $true) { $problems.Add('replayed is not true') }
    if ($v.idempotency.status -ne 'PASS') { $problems.Add("idempotency.status=$($v.idempotency.status)") }
    if ([int]$v.psPhase.flowExit -ne 0) { $problems.Add("psPhase.flowExit=$($v.psPhase.flowExit)") }
    foreach ($property in $v.psPhase.checks.PSObject.Properties) {
        if (-not $property.Value) { $problems.Add("psPhase.checks.$($property.Name) is false") }
    }
    if ([int]$v.observationPages -lt 1) { $problems.Add("observationPages=$($v.observationPages)") }
    if ([int]$v.observationApis -lt 1) { $problems.Add("observationApis=$($v.observationApis)") }
    if ([int]$v.resourceDeltaEvents -lt 1) { $problems.Add("resourceDeltaEvents=$($v.resourceDeltaEvents)") }
    if ((Get-ItemCount $v.newCaseVersionIds) -lt 2) { $problems.Add("newCaseVersionIds=$(Get-ItemCount $v.newCaseVersionIds) (expected >= 2)") }
    if ((Get-ItemCount $v.wave2MissingNewCaseIds) -ne 0) { $problems.Add("wave2MissingNewCaseIds=$(@($v.wave2MissingNewCaseIds) -join ',')") }
    return @($problems)
}

# ---------------------------------------------------------------------------
# 1. Fresh base data and the domain scripts, strictly serial. Each script is
#    expected to exit 0 and to leave a fresh PASS verdict. Any red stays red.
# ---------------------------------------------------------------------------
if (-not $RestartOnly) {
Write-Host ''
Write-Host '################ W2 fresh base data + domains ################'
Invoke-DomainGate -Name 'data.base' -ScriptName 'v022-w2-data.ps1' -VerdictPath $dataVerdictPath -Validate $validateData

if (-not $script:externalBlocked -and -not $script:abortDomains) {
    Invoke-DomainGate -Name 'domain.human' -ScriptName 'v022-w2-human.ps1' -VerdictPath $humanVerdictPath -Validate $validateHuman
}
if (-not $script:externalBlocked -and -not $script:abortDomains) {
    Invoke-DomainGate -Name 'domain.issue' -ScriptName 'v022-w2-issue.ps1' -VerdictPath $issueVerdictPath -Validate $validateIssue
}
if (-not $script:externalBlocked -and -not $script:abortDomains) {
    Invoke-DomainGate -Name 'domain.export' -ScriptName 'v022-w2-export.ps1' -VerdictPath $exportVerdictPath -Validate $validateExport
}
if (-not $script:externalBlocked -and -not $script:abortDomains) {
    Invoke-DomainGate -Name 'domain.code' -ScriptName 'v022-w2-code.ps1' -VerdictPath $codeVerdictPath -Validate $validateCode
}
if (-not $script:externalBlocked -and -not $script:abortDomains) {
    Invoke-DomainGate -Name 'domain.visual' -ScriptName 'v022-w2-visual.ps1' -VerdictPath $visualVerdictPath -Validate $validateVisual
}
if (-not $script:externalBlocked -and -not $script:abortDomains) {
    Invoke-DomainGate -Name 'domain.exploration' -ScriptName 'v022-exploration-e2e.ps1' -VerdictPath $explorationVerdictPath -Validate $validateExploration
}
if (-not $script:externalBlocked -and -not $script:abortDomains) {
    Invoke-DomainGate -Name 'domain.eval' -ScriptName 'v022-w2-eval.ps1' -VerdictPath $evalVerdictPath -Validate $validateEval
}
if (-not $script:externalBlocked -and -not $script:abortDomains) {
    Invoke-DomainGate -Name 'domain.gc' -ScriptName 'v022-w2-gc.ps1' -VerdictPath $gcVerdictPath -Validate $validateGc
}

# ---------------------------------------------------------------------------
# 2. Static and contract gates (fast; still run when a domain is red, so the
#    verdict records the complete gate matrix).
# ---------------------------------------------------------------------------
if (-not $script:externalBlocked) {
    Write-Host ''
    Write-Host '################ static + contract + suites ################'
    Invoke-ExitGate -Name 'static.constructor-refs' `
        -Command 'node scripts/check-constructor-refs.cjs' `
        -File $nodeExe -Arguments @('scripts\check-constructor-refs.cjs') `
        -WorkDir $repoRoot -Validate $validateConstructor

    Invoke-ExitGate -Name 'static.api-only' `
        -Command 'node scripts/v022-api-only-check.cjs --strict' `
        -File $nodeExe -Arguments @('scripts\v022-api-only-check.cjs', '--strict') `
        -WorkDir $repoRoot -Validate $validateApiOnly

    Invoke-ExitGate -Name 'static.event-consumers' `
        -Command 'node scripts/v022-event-consumers.cjs' `
        -File $nodeExe -Arguments @('scripts\v022-event-consumers.cjs') `
        -WorkDir $repoRoot -Validate $validateEventConsumers

    Invoke-ExitGate -Name 'static.explore-residue' `
        -Command 'node scripts/v022-explore-residue-check.cjs' `
        -File $nodeExe -Arguments @('scripts\v022-explore-residue-check.cjs') `
        -WorkDir $repoRoot -Validate $validateExploreResidue

    Invoke-ExitGate -Name 'contract.v021' `
        -Command 'node scripts/v021-contract-check.cjs' `
        -File $nodeExe -Arguments @('scripts\v021-contract-check.cjs') `
        -WorkDir $repoRoot -Validate $validateContract

    Invoke-ExitGate -Name 'contract.v022' `
        -Command 'node scripts/v022-contract-check.cjs' `
        -File $nodeExe -Arguments @('scripts\v022-contract-check.cjs') `
        -WorkDir $repoRoot -Validate $validateContract

    Invoke-ExitGate -Name 'backend.unit' `
        -Command 'go test ./... -count=1' `
        -File $goExe -Arguments @('test', './...', '-count=1') `
        -WorkDir (Join-Path $repoRoot 'backend') -Validate $validateUnit

    Invoke-ExitGate -Name 'backend.integration' `
        -Command 'powershell.exe -File scripts/test-integration.ps1 ./... -p 1' `
        -File $powershellExe -Arguments @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', 'scripts\test-integration.ps1', './...', '-p', '1') `
        -WorkDir $repoRoot -Validate $validateIntegration

    Invoke-ExitGate -Name 'frontend.test' `
        -Command 'pnpm --dir frontend test' `
        -File $pnpmExe -Arguments @('--dir', 'frontend', 'test') `
        -WorkDir $repoRoot -Validate $validateFrontendTest

    Invoke-ExitGate -Name 'frontend.typecheck' `
        -Command 'pnpm --dir frontend typecheck' `
        -File $pnpmExe -Arguments @('--dir', 'frontend', 'typecheck') `
        -WorkDir $repoRoot -Validate $validateExitZeroOnly
}
}

# ---------------------------------------------------------------------------
# 3. Restart persistence. Build the HEAD backend once, then start it on the
#    same W2 database twice and compare canonical read-API snapshots plus DB and
#    event counters.
# ---------------------------------------------------------------------------
function Get-DbJson([string]$Sql) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $raw = (& $psql -h $dbHost -p $dbPort -U $dbUser -d $dbName -tAc $Sql 2>&1 | Out-String).Trim()
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "psql failed (exit=$code): $($raw.Substring(0, [Math]::Min(400, $raw.Length)))" }
    return ($raw | ConvertFrom-Json)
}

function Get-DbCounterSnapshot {
    $sql = "select json_build_object(" +
        "'versions',(select count(*) from version)," +
        "'requirements',(select count(*) from requirement)," +
        "'caseVersions',(select count(*) from case_version)," +
        "'planItems',(select count(*) from version_plan_item)," +
        "'facts',(select count(*) from version_fact)," +
        "'waves',(select count(*) from run_wave)," +
        "'caseRuns',(select count(*) from case_run)," +
        "'evidenceRows',(select count(*) from evidence)," +
        "'humanRequests',(select count(*) from human_request)," +
        "'issues',(select count(*) from delivery_issue)," +
        "'exportArtifacts',(select count(*) from export_artifact)," +
        "'codeIndexRuns',(select count(*) from code_index_run)," +
        "'visualAssertions',(select count(*) from visual_assertion)," +
        "'visualBaselines',(select count(*) from visual_baseline)," +
        "'visualDiffs',(select count(*) from visual_diff)," +
        "'evalRuns',(select count(*) from eval_run)," +
        "'gcRuns',(select count(*) from gc_run)," +
        "'observations',(select count(*) from execution_discovery_observation)" +
        ")::text"
    return (& $psql -h $dbHost -p $dbPort -U $dbUser -d $dbName -tAc $sql 2>&1 | Out-String).Trim()
}

function Get-EventCounterSnapshot {
    $sql = "select coalesce(json_object_agg(type, cnt), '{}')::text from (select type, count(*) as cnt from event group by type) t"
    return (& $psql -h $dbHost -p $dbPort -U $dbUser -d $dbName -tAc $sql 2>&1 | Out-String).Trim()
}

function Get-DomainEventCounterSnapshot {
    # Restart persistence compares only the event families owned by the W2
    # domains. The composition root legitimately drains queued agent/job/work
    # items on startup, so the global event table is not a stable restart
    # invariant (agent.*, job.*, work_item.*, instance.*, wave.finished, ...).
    $sql = "select coalesce(json_object_agg(type, cnt), '{}')::text from (" +
        "select type, count(*) as cnt from event where " +
        "type like 'human_request.%' or type like 'approval.%' or type like 'issue.%' " +
        "or type like 'export.%' or type like 'report.%' or type like 'code.%' " +
        "or type like 'visual.%' or type like 'eval.%' or type like 'gc.%' " +
        "or type like 'case.revision.%' or type like 'case.version.%' " +
        "or type = 'resource.delta' or type = 'version.scope.snapshot_created' " +
        "group by type) t"
    return (& $psql -h $dbHost -p $dbPort -U $dbUser -d $dbName -tAc $sql 2>&1 | Out-String).Trim()
}

function Join-Sorted([object[]]$Items) {
    if ($null -eq $Items) { return '' }
    return ((@($Items) | ForEach-Object { [string]$_ } | Sort-Object) -join "`n")
}

function Get-ApiJson([string]$Path, [switch]$AllowNotFound) {
    $responseFile = Join-Path $work 'api-response.json'
    if (Test-Path -LiteralPath $responseFile) { Remove-Item -LiteralPath $responseFile -Force }
    $curlArgs = @('-s', '-S', '-X', 'GET', '-o', $responseFile, '-w', '%{http_code}', '--max-time', '60',
        '-H', "Authorization: Bearer $($script:restartToken)", ($script:restartApiBase + $Path))
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $statusText = (& $curlExe @curlArgs 2>&1 | Out-String).Trim()
        $curlExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($curlExit -ne 0) { throw "curl failed (exit=$curlExit) for $Path" }
    if ($statusText -eq '404' -and $AllowNotFound) { return $null }
    if ($statusText -notmatch '^2\d\d$') {
        $body = ''
        if (Test-Path -LiteralPath $responseFile) { $body = [System.IO.File]::ReadAllText($responseFile, [System.Text.Encoding]::UTF8) }
        throw "GET $Path returned HTTP ${statusText}: $($body.Substring(0, [Math]::Min(240, $body.Length)))"
    }
    $text = [System.IO.File]::ReadAllText($responseFile, [System.Text.Encoding]::UTF8)
    if ([string]::IsNullOrWhiteSpace($text)) { return $null }
    return ($text | ConvertFrom-Json)
}

function Start-ExitApi([string]$ApiExe) {
    $stdoutLog = Join-Path $work 'restart-api.out.log'
    $stderrLog = Join-Path $work 'restart-api.err.log'
    Remove-Item -LiteralPath $stdoutLog, $stderrLog -Force -ErrorAction SilentlyContinue
    $env:EZTEST_DATABASE_URL = $w2DbUrl
    $env:EZTEST_API_PORT = [string]$exitApiPort
    $env:EZTEST_STORAGE_DIR = (Join-Path $repoRoot 'backend\.data')
    $env:EZTEST_INITIAL_ADMIN_PASSWORD = $adminPassword
    $env:EZTEST_ADMIN_USERNAME = $adminUsername
    $env:EZTEST_SNOWFLAKE_NODE_ID = '7'
    $process = Start-Process -FilePath $ApiExe -WorkingDirectory (Join-Path $repoRoot 'backend') `
        -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog -PassThru
    $ready = $false
    $deadline = (Get-Date).AddSeconds(120)
    while ((Get-Date) -lt $deadline) {
        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            $code = (& $curlExe -s -o NUL -w '%{http_code}' --max-time 3 "$($script:restartApiBase)/api/health" 2>&1 | Out-String).Trim()
        } finally {
            $ErrorActionPreference = $previous
        }
        if ($code -eq '200') { $ready = $true; break }
        Start-Sleep -Milliseconds 500
    }
    if (-not $ready) {
        Write-Host '   restart API did not become healthy; stderr tail:'
        Get-Content -LiteralPath $stderrLog -Tail 20 -ErrorAction SilentlyContinue | ForEach-Object { Write-Host "      $_" }
        return $false
    }
    $loginBody = @{ username = $adminUsername; password = $adminPassword } | ConvertTo-Json -Compress
    $loginFile = Join-Path $work 'restart-login.json'
    Write-Utf8 $loginFile $loginBody
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $loginText = (& $curlExe -s -S -X POST -H 'Content-Type: application/json' --data-binary "@$loginFile" "$($script:restartApiBase)/api/auth/login" 2>&1 | Out-String).Trim()
    } finally {
        $ErrorActionPreference = $previous
    }
    try {
        $login = $loginText | ConvertFrom-Json
        $script:restartToken = [string]$login.data.token
    } catch {
        $script:restartToken = ''
    }
    if ([string]::IsNullOrWhiteSpace($script:restartToken)) {
        Write-Host '   restart API login failed (no token)'
        return $false
    }
    return $true
}

function Stop-ExitApi {
    foreach ($listener in @(Get-NetTCPConnection -State Listen -LocalPort $exitApiPort -ErrorAction SilentlyContinue)) {
        Stop-Process -Id ([int]$listener.OwningProcess) -Force -ErrorAction SilentlyContinue
    }
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline -and @(Get-NetTCPConnection -State Listen -LocalPort $exitApiPort -ErrorAction SilentlyContinue).Count -gt 0) {
        Start-Sleep -Milliseconds 250
    }
}

function Get-RestartApiSnapshot {
    $snapshot = [ordered]@{}
    $data = Read-JsonFileSafe $dataVerdictPath
    $human = Read-JsonFileSafe $humanVerdictPath
    $issue = Read-JsonFileSafe $issueVerdictPath
    $export = Read-JsonFileSafe $exportVerdictPath
    $code = Read-JsonFileSafe $codeVerdictPath
    $visual = Read-JsonFileSafe $visualVerdictPath
    $exploration = Read-JsonFileSafe $explorationVerdictPath
    $evalVerdict = Read-JsonFileSafe $evalVerdictPath
    $gc = Read-JsonFileSafe $gcVerdictPath

    if ($data) {
        $baseId = [string]$data.versionId
        $version = Get-ApiJson "/api/versions/$baseId"
        $stage = Get-ApiJson "/api/versions/$baseId/stage"
        $requirements = Get-ApiJson "/api/versions/$baseId/requirements?limit=100"
        $waves = Get-ApiJson "/api/versions/$baseId/waves?limit=50"
        $snapshot['data'] = @(
            "version=$($version.data.id):$($version.data.status):$($stage.data.stage)",
            'requirements=' + (Join-Sorted @($requirements.data | ForEach-Object { "$($_.id):$($_.status):$($_.current_version_id)" })),
            'waves=' + (Join-Sorted @($waves.data | ForEach-Object { "$($_.id):$($_.status)" }))
        ) -join "`n"
    }
    if ($human) {
        $rows = Get-ApiJson "/api/versions/$($human.versionId)/human-requests?limit=100"
        $snapshot['human'] = Join-Sorted @($rows.data | ForEach-Object { "$($_.id):$($_.kind):$($_.status)" })
    }
    if ($issue) {
        $rows = Get-ApiJson "/api/versions/$($issue.versionId)/issues?limit=100"
        $snapshot['issue'] = Join-Sorted @($rows.data | ForEach-Object { "$($_.id):$($_.status):$($_.retest_status):$($_.severity)" })
    }
    if ($export) {
        $rows = Get-ApiJson "/api/versions/$($export.versionId)/exports?limit=100"
        $snapshot['export'] = Join-Sorted @($rows.data | ForEach-Object { "$($_.id):$($_.format):$($_.status):$($_.sha256):$($_.size):$($_.revision)" })
    }
    if ($code) {
        $repos = Get-ApiJson "/api/versions/$($code.versionId)/code/repos"
        $impact = Get-ApiJson "/api/versions/$($code.versionId)/code/impact"
        $anchors = Get-ApiJson "/api/versions/$($code.versionId)/code/anchors"
        $analysisLines = New-Object System.Collections.Generic.List[string]
        foreach ($repo in @($repos.data)) {
            $analysis = Get-ApiJson "/api/versions/$($code.versionId)/code/analyses/latest?repoId=$($repo.repo_id)" -AllowNotFound
            if ($null -ne $analysis -and $null -ne $analysis.data.analysis) {
                $analysisLines.Add("$($repo.repo_id):$($analysis.data.analysis.id):$($analysis.data.analysis.status)")
            }
        }
        $snapshot['code'] = @(
            'repos=' + (Join-Sorted @($repos.data | ForEach-Object { "$($_.repo_id):$($_.latest_index.id):$($_.latest_index.status):$($_.latest_index.commit_sha)" })),
            'analysis=' + (Join-Sorted @($analysisLines)),
            'impact=' + (Join-Sorted @($impact.data | ForEach-Object { "$($_.id):$($_.subject_kind):$($_.subject_id):$($_.repo_id)" })),
            'anchors=' + (Join-Sorted @($anchors.data | ForEach-Object { "$($_.id):$($_.status):$($_.file_path):$($_.commit_sha)" }))
        ) -join "`n"
    }
    if ($visual) {
        $visualVersion = [string]$visual.versionId
        $overview = Get-ApiJson "/api/versions/$visualVersion/visual/overview"
        $diffs = Get-ApiJson "/api/versions/$visualVersion/visual/diffs?limit=100"
        $baselines = Get-ApiJson "/api/versions/$visualVersion/visual/assertions/$($visual.assertionId)/baselines"
        $snapshot['visual'] = @(
            "overview=$($overview.data.assertion_total):$($overview.data.baseline_covered):$($overview.data.passed):$($overview.data.failed):$($overview.data.inconclusive):$($overview.data.blocked)",
            'diffs=' + (Join-Sorted @($diffs.data | ForEach-Object { "$($_.id):$($_.classification):$($_.diff_ratio):$($_.baseline_id)" })),
            'baselines=' + (Join-Sorted @($baselines.data | ForEach-Object { "$($_.id):$($_.status):$($_.sha256)" }))
        ) -join "`n"
    }
    if ($evalVerdict) {
        $evalVersion = [string]$evalVerdict.versionId
        $trendLines = New-Object System.Collections.Generic.List[string]
        foreach ($metric in 'design_coverage', 'execution_pass_rate', 'case_coverage', 'visual_classification_accuracy') {
            $trend = Get-ApiJson "/api/quality/trends?version_id=$evalVersion&metric=$metric"
            foreach ($point in @($trend.data)) {
                $trendLines.Add("$($point.run_id):$($point.metric):$($point.value):$($point.status)")
            }
        }
        $runs = Get-ApiJson "/api/quality/eval-runs?version_id=$evalVersion"
        $snapshot['eval'] = @(
            'trends=' + (Join-Sorted @($trendLines)),
            'runs=' + (Join-Sorted @($runs.data | ForEach-Object { "$($_.id):$($_.status):$($_.version_id)" }))
        ) -join "`n"
    }
    if ($gc) {
        $runs = Get-ApiJson "/api/admin/gc/runs?limit=100"
        $snapshot['gc'] = Join-Sorted @($runs.data | ForEach-Object {
                "$($_.id):$($_.mode):$($_.status):$($_.scanned_objects):$($_.deleted_objects):$($_.deleted_refs)"
            })
    }
    if ($exploration) {
        $explorationVersion = [string]$exploration.versionId
        $version = Get-ApiJson "/api/versions/$explorationVersion"
        $coverage = Get-ApiJson "/api/versions/$explorationVersion/coverage"
        $observations = Get-ApiJson "/api/versions/$explorationVersion/discovery/observations?limit=100"
        $deltas = Get-ApiJson "/api/versions/$explorationVersion/discovery/deltas"
        $snapshot['exploration'] = @(
            "version=$($version.data.id):$($version.data.status)",
            "coverage=pages:$($coverage.data.pages.observed):$($coverage.data.pages.proposed):$($coverage.data.pages.approved):$($coverage.data.pages.rejected):$($coverage.data.pages.out_of_scope);apis:$($coverage.data.apis.observed):$($coverage.data.apis.proposed):$($coverage.data.apis.approved):$($coverage.data.apis.rejected):$($coverage.data.apis.out_of_scope)",
            'observations=' + (Join-Sorted @($observations.data.items | ForEach-Object { "$($_.observation_id):$($_.kind):$($_.method):$($_.resource_template):$($_.decision):$($_.resource_id):$($_.fact_id):$($_.occurrences)" })),
            'deltas=' + (Join-Sorted @($deltas.data.items | ForEach-Object { "$($_.observation_id):$($_.decision):$($_.resource_id)" }))
        ) -join "`n"
    }
    return $snapshot
}

$restartResult = [ordered]@{
    equal = $false
    apiPort = $exitApiPort
    db = $dbName
    attempts = 0
    before = [ordered]@{}
    after = [ordered]@{}
    dbBefore = [ordered]@{}
    dbAfter = [ordered]@{}
    eventBefore = [ordered]@{}
    eventAfter = [ordered]@{}
}
$restartProblems = New-Object System.Collections.Generic.List[string]

if (-not $script:externalBlocked) {
    Write-Host ''
    Write-Host '################ restart persistence ################'
    $restartStarted = Get-Date
    $restartLog = Join-Path $exitDir 'restart.persistence.log'
    New-Item -ItemType Directory -Force -Path $exitDir | Out-Null
    $restartLines = New-Object System.Collections.Generic.List[string]
    $apiExe = Join-Path $work 'eztest.exe'
    $buildLog = Join-Path $work 'restart-build.log'
    $buildExit = 1
    Push-Location (Join-Path $repoRoot 'backend')
    try {
        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            & $goExe build -o $apiExe ./cmd/eztest 2>&1 | Tee-Object -FilePath $buildLog | Out-Null
            $buildExit = $LASTEXITCODE
        } finally { $ErrorActionPreference = $previous }
    } finally { Pop-Location }
    $restartLines.Add("build: go build ./cmd/eztest exit=$buildExit")
    if ($buildExit -ne 0) {
        $restartProblems.Add("backend build failed (exit=$buildExit)")
    } else {
        $portProblems = @(Stop-W2Ports)
        foreach ($problem in $portProblems) { $restartProblems.Add("harness.ports: $problem") }
        $startedOnce = $false
        for ($attempt = 1; $attempt -le 2 -and -not $startedOnce; $attempt++) {
            $restartResult.attempts = $attempt
            if ($attempt -gt 1) { Start-Sleep -Seconds 2 }
            $startedOnce = Start-ExitApi $apiExe
            if (-not $startedOnce) { $restartLines.Add("start attempt $attempt failed") }
        }
        if (-not $startedOnce) {
            $restartProblems.Add('API did not become healthy on the same W2 database (2 attempts)')
        } else {
            try {
                $dbBeforeRaw = Get-DbCounterSnapshot
                $eventBeforeRaw = Get-DomainEventCounterSnapshot
                $restartResult.dbBefore = ($dbBeforeRaw | ConvertFrom-Json)
                $restartResult.eventBefore = ($eventBeforeRaw | ConvertFrom-Json)
                $restartResult.before = Get-RestartApiSnapshot
                Stop-ExitApi
            } catch {
                $restartProblems.Add("before-restart snapshot failed: $($_.Exception.Message)")
                Stop-ExitApi
            }
            if ($restartProblems.Count -eq 0) {
                $restarted = Start-ExitApi $apiExe
                if (-not $restarted) {
                    $restartProblems.Add('API did not become healthy after the restart')
                } else {
                    try {
                        $dbAfterRaw = Get-DbCounterSnapshot
                        $eventAfterRaw = Get-DomainEventCounterSnapshot
                        $restartResult.dbAfter = ($dbAfterRaw | ConvertFrom-Json)
                        $restartResult.eventAfter = ($eventAfterRaw | ConvertFrom-Json)
                        $restartResult.after = Get-RestartApiSnapshot
                    } catch {
                        $restartProblems.Add("after-restart snapshot failed: $($_.Exception.Message)")
                    } finally {
                        Stop-ExitApi
                    }
                    if ($dbBeforeRaw -ne $dbAfterRaw) {
                        $restartProblems.Add('DB counters changed across the restart')
                    }
                    if ($eventBeforeRaw -ne $eventAfterRaw) {
                        $restartProblems.Add('event counters changed across the restart')
                    }
                    foreach ($domain in @('data', 'human', 'issue', 'export', 'code', 'visual', 'exploration', 'eval', 'gc')) {
                        if (-not $restartResult.before.Contains($domain)) { continue }
                        $beforeText = ($restartResult.before[$domain] | Out-String).TrimEnd()
                        $afterText = ($restartResult.after[$domain] | Out-String).TrimEnd()
                        if ($beforeText -ne $afterText) {
                            $restartProblems.Add("domain $domain read API snapshot changed across the restart")
                        }
                    }
                }
            }
        }
    }
    $restartResult.equal = ($restartProblems.Count -eq 0)
    $restartLines.Add("equal=$($restartResult.equal)")
    foreach ($problem in $restartProblems) { $restartLines.Add("RED: $problem") }
    if ($restartResult.before.Count -gt 0) { $restartLines.Add('domains captured: ' + (@($restartResult.before.Keys) -join ',')) }
    $restartDuration = [math]::Round(((Get-Date) - $restartStarted).TotalSeconds, 1)
    Write-Utf8 $restartLog (($restartLines -join "`r`n") + "`r`n")
    $restartGate = [pscustomobject][ordered]@{
        name        = 'restart.persistence'
        cmd         = 'stop/start API on eztest_v022_w2; compare 9 domain read API snapshots + DB/event counters'
        exit        = $(if ($restartResult.equal) { 0 } else { 1 })
        durationSec = $restartDuration
        log         = (Get-RepoRelative $restartLog)
    }
    if ($restartProblems.Count -gt 0) { $restartGate | Add-Member -NotePropertyName problems -NotePropertyValue @($restartProblems) }
    $gates.Add($restartGate)
    foreach ($problem in $restartProblems) { $red.Add("restart.persistence: $problem") }
    Write-Host "   -> equal=$($restartResult.equal) ($restartDuration s) log=$(Get-RepoRelative $restartLog)"
}

# ---------------------------------------------------------------------------
# 4. Explicit thresholds and contract counters for the verdict.
# ---------------------------------------------------------------------------
$migrationCount = @(Get-ChildItem -Path (Join-Path $repoRoot 'backend\internal\db\migrations') -Filter '*.sql' -File -ErrorAction SilentlyContinue).Count
if ($migrationCount -ne 64) { $red.Add("migrations: found $migrationCount .sql files (expected 64)") }

$contractMatch = $null
$contractText = ''
$eventUnregistered = -1
$eventUnknown = -1
$contractLogPath = Join-Path $exitDir 'contract.v022.log'
if (Test-Path -LiteralPath $contractLogPath) {
    $contractText = Strip-Ansi ([System.IO.File]::ReadAllText($contractLogPath, [System.Text.Encoding]::UTF8))
    $contractMatches = [regex]::Matches($contractText, '(\d+) paths / (\d+) operations / (\d+) schemas')
    foreach ($match in $contractMatches) {
        if ($match.Groups[1].Value -eq '214' -and $match.Groups[2].Value -eq '264' -and $match.Groups[3].Value -eq '322') {
            $contractMatch = $match
            break
        }
    }
}
if ($null -eq $contractMatch) { $red.Add('contract: v0.22 output does not prove 214 paths / 264 operations / 322 schemas') }
$eventLogPath = Join-Path $exitDir 'static.event-consumers.log'
if (Test-Path -LiteralPath $eventLogPath) {
    $eventText = Strip-Ansi ([System.IO.File]::ReadAllText($eventLogPath, [System.Text.Encoding]::UTF8))
    $unregisteredMatch = [regex]::Match($eventText, 'unregistered=(\d+)')
    $unknownMatch = [regex]::Match($eventText, 'unknown=(\d+)')
    if ($unregisteredMatch.Success) { $eventUnregistered = [int]$unregisteredMatch.Groups[1].Value }
    if ($unknownMatch.Success) { $eventUnknown = [int]$unknownMatch.Groups[1].Value }
}
if ($eventUnregistered -ne 0) { $red.Add("events: unregistered=$eventUnregistered (expected 0)") }
if ($eventUnknown -ne 0) { $red.Add("events: unknown=$eventUnknown (expected 0)") }

$domainThresholds = [ordered]@{
    exportFormats     = $null
    indexRepos        = $null
    visualStatesValid = $false
    visualDiffs       = $null
    trendPoints       = $null
    migrations        = $migrationCount
}
$exportVerdict = Read-JsonFileSafe $exportVerdictPath
if ($exportVerdict) { $domainThresholds.exportFormats = Get-ItemCount $exportVerdict.formats }
$codeVerdict = Read-JsonFileSafe $codeVerdictPath
if ($codeVerdict) { $domainThresholds.indexRepos = [int]$codeVerdict.indexRepos }
$visualVerdict = Read-JsonFileSafe $visualVerdictPath
if ($visualVerdict) {
    $states = @()
    if ($null -ne $visualVerdict.visualStates) { $states = @($visualVerdict.visualStates) }
    $valid = ($states.Count -ge 1)
    foreach ($state in $states) {
        if (@('passed', 'failed', 'blocked', 'inconclusive') -notcontains [string]$state) { $valid = $false }
    }
    $domainThresholds.visualStatesValid = $valid
    $domainThresholds.visualDiffs = [int]$visualVerdict.dbCounters.diffs
}
$evalVerdict = Read-JsonFileSafe $evalVerdictPath
if ($evalVerdict) { $domainThresholds.trendPoints = [int]$evalVerdict.trendPoints }
if ($null -ne $domainThresholds.exportFormats -and [int]$domainThresholds.exportFormats -lt 2) { $red.Add("threshold: exportFormats=$($domainThresholds.exportFormats) (expected >= 2)") }
if ($null -eq $domainThresholds.exportFormats) { $red.Add('threshold: exportFormats is missing') }
if ($null -eq $domainThresholds.indexRepos -or [int]$domainThresholds.indexRepos -lt 2) { $red.Add("threshold: indexRepos=$($domainThresholds.indexRepos) (expected >= 2)") }
if ($domainThresholds.visualStatesValid -ne $true) { $red.Add('threshold: visualStatesValid is not true') }
if ($null -eq $domainThresholds.visualDiffs -or [int]$domainThresholds.visualDiffs -lt 1) { $red.Add("threshold: visual diffs=$($domainThresholds.visualDiffs) (expected >= 1)") }
if ($null -eq $domainThresholds.trendPoints -or [int]$domainThresholds.trendPoints -lt 2) { $red.Add("threshold: trendPoints=$($domainThresholds.trendPoints) (expected >= 2)") }

if ($RestartOnly) {
    $red.Add('debug skip: -RestartOnly was used; the W2 exit gate is not valid evidence')
}

# ---------------------------------------------------------------------------
# 5. Final DB/event counters (read-only) and machine verdict.
# ---------------------------------------------------------------------------
$dbSnapshot = [ordered]@{}
$eventSnapshot = [ordered]@{}
$domainEventSnapshot = [ordered]@{}
if (-not $script:externalBlocked) {
    try { $dbSnapshot = (Get-DbCounterSnapshot | ConvertFrom-Json) } catch { $red.Add("dbCounters query failed: $($_.Exception.Message)") }
    try { $eventSnapshot = (Get-EventCounterSnapshot | ConvertFrom-Json) } catch { $red.Add("eventCounters query failed: $($_.Exception.Message)") }
    try { $domainEventSnapshot = (Get-DomainEventCounterSnapshot | ConvertFrom-Json) } catch { $red.Add("domain eventCounters query failed: $($_.Exception.Message)") }
}

# Leave the shared W2 ports free. This is cleanup only; it cannot turn a red
# result green.
$cleanupProblems = @(Stop-W2Ports)
foreach ($problem in $cleanupProblems) { Write-Host "   cleanup: $problem" }

$gitHead = ''
try { $gitHead = (& git rev-parse HEAD 2>$null | Out-String).Trim() } catch { }

$status = 'FAIL'
if ($script:externalBlocked) {
    $status = 'EXTERNAL-BLOCKED'
} elseif ($red.Count -eq 0 -and $gates.Count -gt 0 -and -not $RestartOnly) {
    $status = 'PASS'
}

$eventTotal = 0
if ($eventSnapshot) {
    foreach ($property in $eventSnapshot.PSObject.Properties) { $eventTotal += [int64]$property.Value }
}

$verdict = [ordered]@{
    status            = $status
    gitHead           = [string]$gitHead
    generatedAt       = (Get-Date -Format o)
    gates             = @($gates | ForEach-Object { $_ })
    dbCounters        = $dbSnapshot
    eventCounters     = [ordered]@{
        total        = $eventTotal
        unregistered = $eventUnregistered
        unknown      = $eventUnknown
        byType       = $eventSnapshot
        domainByType = $domainEventSnapshot
    }
    restart           = $restartResult
    domainThresholds  = $domainThresholds
    red               = @($red | ForEach-Object { [string]$_ })
}
Write-Utf8 $exitVerdictPath ($verdict | ConvertTo-Json -Depth 12)

# ---------------------------------------------------------------------------
# 6. EXIT.md derived from the verdict (RED is always rendered as FAIL).
# ---------------------------------------------------------------------------
function Write-ExitMarkdown {
    param($Verdict)
    $lines = New-Object System.Collections.Generic.List[string]
    $isPass = ($Verdict.status -eq 'PASS' -and (Get-ItemCount $Verdict.red) -eq 0)
    $lines.Add('# EzTest v0.22 W2 - Exit verification (W2-16)')
    $lines.Add('')
    if ($isPass) {
        $lines.Add('- **Status: PASS - W2 is signed off.**')
    } elseif ($Verdict.status -eq 'EXTERNAL-BLOCKED') {
        $lines.Add('- **Status: EXTERNAL-BLOCKED - not PASS.**')
    } else {
        $lines.Add('- **Status: FAIL - not PASS.**')
    }
    $lines.Add("- **gitHead:** ``$($Verdict.gitHead)``")
    $lines.Add("- **Generated:** $($Verdict.generatedAt) (Asia/Shanghai), single run of ``scripts/v022-w2-exit.ps1``")
    $lines.Add("- **Machine verdict:** [``run/w2-exit-verdict.json``](run/w2-exit-verdict.json) (``status: $($Verdict.status)``, ``gates: $(Get-ItemCount $Verdict.gates)``, ``red: $(Get-ItemCount $Verdict.red)``)")
    $lines.Add('')
    $lines.Add('## 1. Gates')
    $lines.Add('')
    $lines.Add('| # | Gate | Command | Exit | Duration | Log |')
    $lines.Add('|---|---|---|---|---|---|')
    $index = 0
    foreach ($gate in @($Verdict.gates)) {
        $index++
        $lines.Add("| $index | $($gate.name) | ``$($gate.cmd)`` | $($gate.exit) | $($gate.durationSec)s | ``$($gate.log)`` |")
    }
    $lines.Add('')
    $contract = 'missing'
    if ($domainThresholds -and $domainThresholds.migrations -eq 64) { $contract = '214 paths / 264 operations / 322 schemas; migrations 64' }
    $lines.Add("Contract: **$contract**. Events: **unregistered=$($Verdict.eventCounters.unregistered) unknown=$($Verdict.eventCounters.unknown)**.")
    $lines.Add('')
    $lines.Add('## 2. Domain verdicts')
    $lines.Add('')
    $lines.Add('| Domain | Verdict | Evidence | Key counters |')
    $lines.Add('|---|---|---|---|')
    $domainRows = @(
        @{ name = 'data'; path = 'run/data-verdict.json'; detail = { param($v) "version=$($v.versionId) stage=$($v.stage) cases=$($v.dbCounters.cases) waves=$($v.dbCounters.waves)" } },
        @{ name = 'human'; path = 'run/human-verdict.json'; detail = { param($v) "requests=$($v.events.requested) decided=$($v.events.decided) expired=$($v.expiredCount)" } },
        @{ name = 'issue'; path = 'run/issue/issue-verdict.json'; detail = { param($v) "issue=$($v.issueId) retest=$($v.retestStatus) wave=$($v.retestWaveStatus)" } },
        @{ name = 'export'; path = 'run/export/export-verdict.json'; detail = { param($v) "formats=$(@($v.formats) -join ',') artifacts=$(@($v.artifacts).Count) downloads=$($v.downloadOk)" } },
        @{ name = 'code'; path = 'run/code-verdict.json'; detail = { param($v) "repos=$($v.indexRepos) impact=$($v.impactCandidates) anchors=$($v.anchors)" } },
        @{ name = 'visual'; path = 'run/visual-verdict.json'; detail = { param($v) "states=$(@($v.visualStates) -join ',') diffs=$($v.dbCounters.diffs) baselines=$($v.dbCounters.approvedBaselines)" } },
        @{ name = 'exploration'; path = 'run/exploration-verdict.json'; detail = { param($v) "pages=$($v.observationPages) apis=$($v.observationApis) delta=$($v.resourceDeltaEvents) replay=$($v.replayed)" } },
        @{ name = 'eval'; path = 'run/eval-verdict.json'; detail = { param($v) $metricCount = 0; if ($null -ne $v.metrics) { $metricCount = @($v.metrics.PSObject.Properties).Count }; "trendPoints=$($v.trendPoints) metrics=$metricCount runs=$(Get-ItemCount $v.evalRunIds)" } },
        @{ name = 'gc'; path = 'run/gc-verdict.json'; detail = { param($v) "dry=$($v.dryRunId) apply=$($v.applyRunId) tombstones=$($v.applyTombstones)" } }
    )
    foreach ($row in $domainRows) {
        $path = Join-Path $evidenceRoot $row.path
        $detail = 'missing'
        $verdictValue = 'MISSING'
        if (Test-Path -LiteralPath $path) {
            $parsed = Read-JsonFileSafe $path
            if ($parsed) {
                $verdictValue = [string]$parsed.status
                try { $detail = (& $row.detail $parsed) } catch { $detail = 'unreadable detail' }
            }
        }
        $lines.Add("| $($row.name) | $verdictValue | ``$($row.path)`` | $detail |")
    }
    $lines.Add('')
    $lines.Add('## 3. Thresholds and restart')
    $lines.Add('')
    $lines.Add("- exportFormats: **$($Verdict.domainThresholds.exportFormats)** (>= 2)")
    $lines.Add("- indexRepos: **$($Verdict.domainThresholds.indexRepos)** (>= 2)")
    $lines.Add("- visualStatesValid: **$($Verdict.domainThresholds.visualStatesValid)**; visual diffs: **$($Verdict.domainThresholds.visualDiffs)** (>= 1)")
    $lines.Add("- trendPoints: **$($Verdict.domainThresholds.trendPoints)** (>= 2)")
    $lines.Add("- migrations: **$($Verdict.domainThresholds.migrations)** (== 64)")
    $lines.Add("- restart.equal: **$($Verdict.restart.equal)**; API port $($Verdict.restart.apiPort); attempts $($Verdict.restart.attempts)")
    if ($Verdict.restart.before) {
        $lines.Add("- restart domains compared: $(@($Verdict.restart.before.Keys) -join ', ')")
    }
    $lines.Add('')
    $lines.Add('## 4. Red items')
    $lines.Add('')
    if ((Get-ItemCount $Verdict.red) -eq 0) {
        $lines.Add('- none')
    } else {
        foreach ($entry in @($Verdict.red)) { $lines.Add("- $entry") }
        $lines.Add('')
        $lines.Add('**RED IS NOT PASS. W2 must not be signed off from this run.**')
    }
    $lines.Add('')
    $lines.Add('## 5. Residual risks (do not block a correctly green W2 exit unless they become red above)')
    $lines.Add('')
    $lines.Add('1. W2-12: the rollback entry point after a reload is still limited by the case-detail DTO; the product API and tests cover the rollback mutation itself.')
    $lines.Add('2. W2-10 exercised two real visual states (`passed`, `inconclusive`); `failed`/`blocked` are covered by unit tests rather than this acceptance flow.')
    $lines.Add('3. The LLM visual path is a caption proxy (`visionExercised=false`); text prompts still go to the real upstream. This is unchanged from W1 and explicitly disclosed.')
    $lines.Add('4. The acceptance environment needs the real SUT (localhost:8080/5173) plus PostgreSQL 5433 and the real LLM key; a missing dependency is EXTERNAL-BLOCKED, never PASS.')
    $lines.Add('5. Domain scripts share the W2 database and are serialized; the exit gate frees the known W2 ports between scripts and never touches the SUT ports.')
    $lines.Add('6. This EXIT.md is derived from the machine verdict; old run artifacts from earlier attempts are not certification.')
    $lines.Add('')
    $lines.Add('## 6. Declaration')
    $lines.Add('')
    if ($isPass) {
        $lines.Add("W2 (W2-1..W2-15) is mechanically certified at ``$($Verdict.gitHead)``: all $(Get-ItemCount $Verdict.gates) gates exit 0, every domain verdict is PASS, the required counters meet their thresholds, event registration has no gaps, and the restart persistence comparison is identical.")
    } else {
        $lines.Add("W2 is **not** certified at ``$($Verdict.gitHead)``. The gate/domain/red rows above are the complete reason.")
    }
    return ($lines -join "`r`n")
}
Write-Utf8 $exitMarkdownPath ((Write-ExitMarkdown $verdict) + "`r`n")

# ---------------------------------------------------------------------------
# 7. Console summary and exit code.
# ---------------------------------------------------------------------------
Write-Host ''
Write-Host "== W2 exit gates ($($gates.Count)):"
foreach ($gate in $gates) {
    Write-Host ("   {0,-28} exit={1,-3} {2,8}s  {3}" -f $gate.name, $gate.exit, $gate.durationSec, $gate.log)
}
Write-Host ''
Write-Host "== W2 exit red items ($($red.Count)):"
foreach ($entry in $red) { Write-Host "   - $entry" }
Write-Host ''
Write-Host "verdict: $(Get-RepoRelative $exitVerdictPath)"
Write-Host "exit doc: $(Get-RepoRelative $exitMarkdownPath)"
Write-Host "status: $status"
Write-Host ''

if ($status -eq 'PASS') {
    Write-Host 'W2 EXIT: PASS'
    exit 0
}
if ($status -eq 'EXTERNAL-BLOCKED') {
    Write-Host 'W2 EXIT: EXTERNAL-BLOCKED (environment missing; not PASS)'
    exit 2
}
Write-Host 'W2 EXIT: FAIL'
exit 1
