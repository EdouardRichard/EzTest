#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 W1 exit gate (W1-15).

.DESCRIPTION
    Certifies the W1 exit by re-running every gate fresh and by querying the
    zero-data counters of the fresh W1 database directly. Nothing is certified
    from prose, an old verdict file or a previous run's text.

    Gates, in order (each writes one log under
    docs/version-V0.22/acceptance/evidence/w1/exit/ - deliberately outside
    run/, because a fresh W1 e2e clears run/ at its start):
      1. static:  constructor refs, API-only --strict, event consumers
                  (unregistered must be 0), explore residue.
      2. contract: v0.2.1 and v0.22 checks; both must report 210/257/315.
      3. backend unit: go test ./... -count=1 (cwd backend/).
      4. backend integration: scripts/test-integration.ps1 ./... -count=1 -p 1
         (serialized; must actually run the integration packages, not skip).
      5. frontend: pnpm --dir frontend test, pnpm --dir frontend typecheck.
      6. fresh W1 composition-root e2e: scripts/v022-w1-e2e.ps1 (never
         -ReuseFlow: the verdict must record replayed=false).
      7. fresh W0 regression: scripts/v022-w0-regression.ps1 without
         -ReuseFlow (rebuilds the W0 run/ evidence destroyed by the W1-9
         accidental run).
      8. W0 exit gate: scripts/v022-w0-exit.ps1 (pre-check + fresh commands).
      9. zero-data DB checks against eztest_v022_w1 (psql, direct SQL).

    The machine verdict is written to
    docs/version-V0.22/acceptance/evidence/w1/run/w1-exit-verdict.json with the
    schema { generated_at, gitHead, gates[], dbCounters{}, red[], status }.
    "W1 EXIT: PASS" is printed only when every gate exited 0 AND every required
    database counter is greater than zero.

    Exit codes: 0 = PASS, 1 = FAIL (a gate or a counter is red),
    2 = EXTERNAL-BLOCKED (a gate reported the environment is missing; the run
    stops and is never recorded as PASS).

.NOTES
    Windows PowerShell 5.1 compatible. Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\v022-w1-exit.ps1
    -SkipE2E / -SkipW0 exist for debugging only. They can never produce a PASS:
    the verdict is forced to FAIL with an explicit debug-skip red item.
#>
param(
    [switch]$SkipE2E,
    [switch]$SkipW0
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence'
$w1Run = Join-Path $evidenceRoot 'w1\run'
$w1VerdictPath = Join-Path $w1Run 'w1-verdict.json'
$w1ExitVerdictPath = Join-Path $w1Run 'w1-exit-verdict.json'
$w0Run = Join-Path $evidenceRoot 'w0\run'
$w0VerdictPath = Join-Path $w0Run 'verdict.json'
$w0ResultPath = Join-Path $w0Run 'result.json'
$w0DbChecksPath = Join-Path $w0Run 'db-checks.json'
$w0ExitChecksPath = Join-Path $w0Run 'exit\checks.json'
$logDir = Join-Path $evidenceRoot 'w1\exit'
New-Item -ItemType Directory -Force -Path $w1Run, $logDir | Out-Null

$dbName = 'eztest_v022_w1'
$dbHost = 'localhost'
$dbPort = 5433
$dbUser = 'eztest'

# ---------------------------------------------------------------------------
# Helpers. Evidence JSON and logs are written as UTF-8 without BOM: Windows
# PowerShell 5.1 text cmdlets would otherwise rewrite CJK payloads in the
# active ANSI code page.
# ---------------------------------------------------------------------------
function Write-Utf8([string]$Path, [string]$Text) {
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
$psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
if (-not (Test-Path -LiteralPath $psql)) {
    $psqlCommand = Get-Command psql.exe -ErrorAction SilentlyContinue
    if (-not $psqlCommand) { throw 'PostgreSQL client is not available (.tools/pgsql/bin/psql.exe or psql.exe on PATH).' }
    $psql = $psqlCommand.Source
}

try {
    $dbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
    $dbPassword = [regex]::Match($dbUrl, 'postgres://[^:]+:([^@]+)@').Groups[1].Value
    if (-not $dbPassword) { throw 'cannot extract the password from EZTEST_DATABASE_URL' }
} catch {
    Write-Host "W1 EXIT: EXTERNAL-BLOCKED (cannot read .env: $($_.Exception.Message))"
    exit 2
}
$env:PGPASSWORD = $dbPassword

$gates = New-Object System.Collections.Generic.List[object]
$red = New-Object System.Collections.Generic.List[string]
$externalBlocked = $false

# ---------------------------------------------------------------------------
# Gate runner: one log per gate, exit code + duration + repo-relative log path.
# A validator receives the captured text and the gate start time and returns
# one string per failed assertion.
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
    $logPath = Join-Path $logDir ($safeName + '.log')
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
    foreach ($problem in $problems) {
        $red.Add("${Name}: $problem")
    }
    if ($exitCode -eq 0 -and $problems.Count -eq 0) {
        Write-Host "   -> exit 0 ($duration s) log=$(Get-RepoRelative $logPath)"
    } else {
        Write-Host "   -> exit $exitCode ($duration s) RED: $($problems -join '; ')"
        Write-Host "      log=$(Get-RepoRelative $logPath)"
    }
}

# Validators ---------------------------------------------------------------
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
    if ($text -notmatch '210 paths / 257 operations / 315 schemas') {
        return @('contract counts are not 210 paths / 257 operations / 315 schemas')
    }
    return @()
}
$validateUnit = {
    param($text, $started)
    $clean = Strip-Ansi $text
    if ($clean -match '(?m)^\s*FAIL\s') { return @('go test output contains FAIL') }
    return @()
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
$validateW1E2E = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    if (-not (Test-Path -LiteralPath $w1VerdictPath)) {
        $problems.Add("missing fresh verdict $w1VerdictPath")
        return @($problems)
    }
    $verdict = $null
    try { $verdict = Get-Content -LiteralPath $w1VerdictPath -Raw -Encoding UTF8 | ConvertFrom-Json }
    catch { $problems.Add("cannot parse w1-verdict.json: $($_.Exception.Message)"); return @($problems) }
    $fresh = (Get-Item -LiteralPath $w1VerdictPath).LastWriteTime -ge $started.AddMinutes(-1)
    if (-not $fresh) { $problems.Add('w1-verdict.json is older than the gate start (not a fresh run)') }
    if ($verdict.replayed -eq $true) { $problems.Add('w1-verdict.json records replayed=true (the exit gate requires a fresh run)') }
    if ([int]$verdict.flowExit -ne 0) { $problems.Add("flowExit=$($verdict.flowExit)") }
    foreach ($entry in @($verdict.red)) { if ($entry) { $problems.Add([string]$entry) } }
    if ($verdict.assertions) {
        foreach ($property in $verdict.assertions.PSObject.Properties) {
            if (-not $property.Value) { $problems.Add("assertion failed: $($property.Name)") }
        }
    }
    foreach ($key in 'requirements', 'cases', 'planItems', 'waves', 'caseRuns', 'evidenceRows', 'events', 'planEvents', 'resourceDeltaEvents', 'scopeSnapshots') {
        $value = [int64]$verdict.dbCounters.$key
        if ($value -lt 1) { $problems.Add("w1-verdict.dbCounters.$key=$value") }
    }
    if ($verdict.llm) {
        if ($verdict.llm.visionBacked -ne 'proxy') { $problems.Add("llm.visionBacked=$($verdict.llm.visionBacked) (expected proxy)") }
        if ($verdict.llm.visionExercised -ne $false) { $problems.Add("llm.visionExercised=$($verdict.llm.visionExercised) (expected false)") }
        if ($verdict.llm.textOnlyUpstream -ne $true) { $problems.Add("llm.textOnlyUpstream=$($verdict.llm.textOnlyUpstream) (expected true)") }
    } else {
        $problems.Add('llm backing record missing from w1-verdict.json')
    }
    foreach ($shot in 'w1-version-overview.png', 'w1-requirements.png', 'w1-design.png', 'w1-execution.png', 'w1-reports.png') {
        $shotPath = Join-Path $w1Run $shot
        if (-not (Test-Path -LiteralPath $shotPath)) { $problems.Add("screenshot missing: $shot"); continue }
        if ((Get-Item -LiteralPath $shotPath).Length -le 0) { $problems.Add("screenshot empty: $shot") }
    }
    return @($problems)
}
$validateW0Regression = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    foreach ($path in $w0ResultPath, $w0DbChecksPath, $w0VerdictPath) {
        if (-not (Test-Path -LiteralPath $path)) { $problems.Add("missing fresh W0 evidence: $path") }
    }
    if (-not (Test-Path -LiteralPath $w0VerdictPath)) { return @($problems) }
    $fresh = (Get-Item -LiteralPath $w0VerdictPath).LastWriteTime -ge $started.AddMinutes(-1)
    if (-not $fresh) { $problems.Add('w0/run/verdict.json is older than the gate start (not a fresh run)') }
    $verdict = $null
    try { $verdict = Get-Content -LiteralPath $w0VerdictPath -Raw -Encoding UTF8 | ConvertFrom-Json }
    catch { $problems.Add("cannot parse w0/run/verdict.json: $($_.Exception.Message)"); return @($problems) }
    if ([int]$verdict.flowExit -ne 0) { $problems.Add("flowExit=$($verdict.flowExit)") }
    if ([int]$verdict.redCount -ne 0) { $problems.Add("redCount=$($verdict.redCount): $(@($verdict.red) -join '; ')") }
    $dbRed = @($verdict.dbChecks.red)
    if ($dbRed.Count -ne 0) { $problems.Add("dbChecks.red has $($dbRed.Count) entr(ies): $($dbRed -join '; ')") }
    foreach ($key in 'completedRuns', 'planItems', 'planEvents', 'facts', 'waves', 'doneExtractions', 'doneImages', 'versionCompleted', 'stageEventKinds', 'evidenceRows') {
        $value = [int64]$verdict.dbChecks.$key
        if ($value -le 0) { $problems.Add("w0 verdict dbChecks.$key=$value") }
    }
    return @($problems)
}
$validateW0Exit = {
    param($text, $started)
    $problems = New-Object System.Collections.Generic.List[string]
    if (-not (Test-Path -LiteralPath $w0ExitChecksPath)) {
        $problems.Add("missing W0 exit machine summary $w0ExitChecksPath")
        return @($problems)
    }
    $fresh = (Get-Item -LiteralPath $w0ExitChecksPath).LastWriteTime -ge $started.AddMinutes(-1)
    if (-not $fresh) { $problems.Add('w0/run/exit/checks.json is older than the gate start (not a fresh run)') }
    $checks = $null
    try { $checks = Get-Content -LiteralPath $w0ExitChecksPath -Raw -Encoding UTF8 | ConvertFrom-Json }
    catch { $problems.Add("cannot parse W0 exit checks.json: $($_.Exception.Message)"); return @($problems) }
    if (@($checks.failures).Count -ne 0) { $problems.Add("failures: $(@($checks.failures) -join '; ')") }
    $gateChecks = @($checks.checks)
    if ($gateChecks.Count -lt 7) { $problems.Add("only $($gateChecks.Count) W0 exit checks recorded (expected >= 7)") }
    foreach ($check in $gateChecks) {
        if ([int]$check.exitCode -ne 0) { $problems.Add("W0 exit check $($check.id) exited $($check.exitCode)") }
    }
    return @($problems)
}

# ---------------------------------------------------------------------------
# 1. Static gates.
# ---------------------------------------------------------------------------
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

# ---------------------------------------------------------------------------
# 2. Contract gates.
# ---------------------------------------------------------------------------
Invoke-ExitGate -Name 'contract.v021' `
    -Command 'node scripts/v021-contract-check.cjs' `
    -File $nodeExe -Arguments @('scripts\v021-contract-check.cjs') `
    -WorkDir $repoRoot -Validate $validateContract

Invoke-ExitGate -Name 'contract.v022' `
    -Command 'node scripts/v022-contract-check.cjs' `
    -File $nodeExe -Arguments @('scripts\v022-contract-check.cjs') `
    -WorkDir $repoRoot -Validate $validateContract

# ---------------------------------------------------------------------------
# 3. Backend unit suite.
# ---------------------------------------------------------------------------
Invoke-ExitGate -Name 'backend.unit' `
    -Command 'go test ./... -count=1' `
    -File $goExe -Arguments @('test', './...', '-count=1') `
    -WorkDir (Join-Path $repoRoot 'backend') -Validate $validateUnit

# ---------------------------------------------------------------------------
# 4. Backend integration suite (serialized; must actually run).
# ---------------------------------------------------------------------------
Invoke-ExitGate -Name 'backend.integration' `
    -Command 'powershell.exe -File scripts/test-integration.ps1 ./... -count=1 -p 1' `
    -File $powershellExe -Arguments @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', 'scripts\test-integration.ps1', './...', '-count=1', '-p', '1') `
    -WorkDir $repoRoot -Validate $validateIntegration

# ---------------------------------------------------------------------------
# 5. Frontend unit suite + typecheck.
# ---------------------------------------------------------------------------
Invoke-ExitGate -Name 'frontend.test' `
    -Command 'pnpm --dir frontend test' `
    -File $pnpmExe -Arguments @('--dir', 'frontend', 'test') `
    -WorkDir $repoRoot -Validate $validateFrontendTest

Invoke-ExitGate -Name 'frontend.typecheck' `
    -Command 'pnpm --dir frontend typecheck' `
    -File $pnpmExe -Arguments @('--dir', 'frontend', 'typecheck') `
    -WorkDir $repoRoot -Validate $validateExitZeroOnly

# ---------------------------------------------------------------------------
# 6. Fresh W1 composition-root e2e. -ReuseFlow is deliberately never used: the
#    exit evidence must come from a run that re-made the database and the flow.
# ---------------------------------------------------------------------------
if ($SkipE2E) {
    Write-Host ''
    Write-Host '== [w1.e2e] SKIPPED (-SkipE2E debug mode; not valid exit evidence)'
    $red.Add('debug skip: -SkipE2E was used; the W1 exit gate is not valid evidence')
} else {
    Invoke-ExitGate -Name 'w1.e2e' `
        -Command 'powershell.exe -File scripts/v022-w1-e2e.ps1 (fresh, no -ReuseFlow)' `
        -File $powershellExe -Arguments @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', 'scripts\v022-w1-e2e.ps1') `
        -WorkDir $repoRoot -Validate $validateW1E2E
}

# ---------------------------------------------------------------------------
# 6b. The W1 e2e keeps its composition root running on purpose (so -ReuseFlow
#     can replay it). The fresh W0 regression below starts a second root and a
#     second `next dev` in the same frontend/ directory; Next refuses that while
#     the W1 web server still holds frontend/.next/dev/lock, which is exactly
#     what made the previous exit run fail at gate 7. Stop the W1 root here,
#     wait for the ports, and remove a residual dev lock only when no process
#     holds it anymore.
# ---------------------------------------------------------------------------
function Stop-W1CompositionRoot {
    $w1Ports = @(13444, 13348, 13999, 6870)
    foreach ($port in $w1Ports) {
        $listeners = @(Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue)
        if ($listeners.Count -eq 0) {
            Write-Host "   port ${port}: free (no listener)"
            continue
        }
        foreach ($listener in $listeners) {
            $ownerId = [int]$listener.OwningProcess
            $ownerName = '<gone>'
            try { $ownerName = (Get-Process -Id $ownerId -ErrorAction Stop).ProcessName } catch { }
            Stop-Process -Id $ownerId -Force -ErrorAction SilentlyContinue
            Write-Host "   port ${port}: pid=$ownerId ($ownerName) -> stop requested"
        }
    }

    # Wait (bounded, <=60s) for every W1 listener to disappear.
    $pending = New-Object System.Collections.Generic.List[int]
    foreach ($port in $w1Ports) { $pending.Add($port) }
    $deadline = (Get-Date).AddSeconds(60)
    while ($pending.Count -gt 0 -and (Get-Date) -lt $deadline) {
        foreach ($port in @($pending)) {
            if (@(Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue).Count -eq 0) {
                $null = $pending.Remove($port)
            }
        }
        if ($pending.Count -gt 0) { Start-Sleep -Milliseconds 500 }
    }
    foreach ($port in $w1Ports) {
        if ($pending.Contains($port)) {
            $owner = (@(Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue | Select-Object -ExpandProperty OwningProcess -Unique) -join ',')
            Write-Host "   port ${port}: STILL LISTENING after 60s (pid=$owner)"
            $red.Add("harness.ports: W1 port $port still has a listener after 60s (pid=$owner)")
        } else {
            Write-Host "   port ${port}: released"
        }
    }

    # Next 16 writes the dev-server pid into frontend/.next/dev/lock. If that pid
    # is gone the OS lock is gone too, and the leftover file is removed. If the
    # pid is still alive the lock is genuinely held: report it, never delete it.
    $devLock = Join-Path $repoRoot 'frontend\.next\dev\lock'
    if (-not (Test-Path -LiteralPath $devLock)) {
        Write-Host '   dev lock: absent'
        return
    }
    $lockOwnerId = 0
    try {
        $lockInfo = Get-Content -LiteralPath $devLock -Raw -Encoding UTF8 | ConvertFrom-Json
        if ($null -ne $lockInfo.pid) { $lockOwnerId = [int]$lockInfo.pid }
    } catch { }
    if ($lockOwnerId -le 0) {
        Write-Host "   dev lock: present without a readable pid ($devLock)"
    } else {
        $lockDeadline = (Get-Date).AddSeconds(10)
        while ((Get-Date) -lt $lockDeadline -and (Get-Process -Id $lockOwnerId -ErrorAction SilentlyContinue)) {
            Start-Sleep -Milliseconds 500
        }
        $holder = Get-Process -Id $lockOwnerId -ErrorAction SilentlyContinue
        if ($holder) {
            Write-Host "   dev lock: pid=$lockOwnerId ($($holder.ProcessName)) is STILL RUNNING; lock left in place"
            $red.Add("harness.lock: frontend/.next/dev/lock is held by live pid $lockOwnerId ($($holder.ProcessName))")
            return
        }
        Write-Host "   dev lock: holder pid=$lockOwnerId has exited"
    }
    Remove-Item -LiteralPath $devLock -Force -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $devLock) {
        Write-Host "   dev lock: residual file could NOT be removed ($devLock)"
        $red.Add('harness.lock: residual frontend/.next/dev/lock could not be removed')
    } else {
        Write-Host '   dev lock: residual file removed (no process holds it)'
    }
}

if (-not $SkipE2E -and -not $SkipW0 -and -not $externalBlocked) {
    Write-Host ''
    Write-Host '== [w1.e2e cleanup] stopping the W1 composition root before the fresh W0 regression'
    Stop-W1CompositionRoot
}

# ---------------------------------------------------------------------------
# 7. Fresh W0 regression (must rebuild the W0 run/ evidence; -ReuseFlow cannot
#    restore result.json/verdict.json and is not accepted here).
# ---------------------------------------------------------------------------
if ($SkipW0) {
    Write-Host ''
    Write-Host '== [w0.regression] SKIPPED (-SkipW0 debug mode; not valid exit evidence)'
    $red.Add('debug skip: -SkipW0 was used; the W1 exit gate is not valid evidence')
} elseif ($externalBlocked) {
    Write-Host ''
    Write-Host '== [w0.regression] NOT RUN (the W1 e2e gate reported EXTERNAL-BLOCKED; the run stops)'
} else {
    Invoke-ExitGate -Name 'w0.regression' `
        -Command 'powershell.exe -File scripts/v022-w0-regression.ps1 (fresh, no -ReuseFlow)' `
        -File $powershellExe -Arguments @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', 'scripts\v022-w0-regression.ps1') `
        -WorkDir $repoRoot -Validate $validateW0Regression
}

# ---------------------------------------------------------------------------
# 8. W0 exit gate (pre-check + fresh unit/integration/frontend/contract runs +
#    a -ReuseFlow replay of the fresh W0 verdict).
# ---------------------------------------------------------------------------
if (-not $SkipW0 -and -not $externalBlocked) {
    Invoke-ExitGate -Name 'w0.exit' `
        -Command 'powershell.exe -File scripts/v022-w0-exit.ps1' `
        -File $powershellExe -Arguments @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', 'scripts\v022-w0-exit.ps1') `
        -WorkDir $repoRoot -Validate $validateW0Exit
}

# ---------------------------------------------------------------------------
# 9. Zero-data database checks against the fresh W1 database. A zero counter
#    (or a missing page/API observation kind) is a FAIL with the actual value.
# ---------------------------------------------------------------------------
$counters = $null
if (-not $externalBlocked) {
    Write-Host ''
    Write-Host "== [db.zero-data] psql $dbName"
    $countersSql = "select json_build_object(" +
        "'planItems',(select count(*) from version_plan_item)," +
        "'planEvents',(select count(*) from event where type = 'plan.item_changed')," +
        "'waves',(select count(*) from run_wave)," +
        "'scopeSnapshots',(select count(*) from run_wave where scope_snapshot <> '{}'::jsonb)," +
        "'caseRuns',(select count(*) from case_run)," +
        "'caseRunsPassed',(select count(*) from case_run where status = 'passed')," +
        "'evidenceRows',(select count(*) from evidence)," +
        "'events',(select count(*) from event)," +
        "'obsTotal',(select count(*) from execution_discovery_observation)," +
        "'obsPage',(select count(*) from execution_discovery_observation where kind = 'page')," +
        "'obsApi',(select count(*) from execution_discovery_observation where kind = 'api')," +
        "'obsDecisionNotNew',(select count(*) from execution_discovery_observation where decision <> 'new')," +
        "'resourceDeltaEvents',(select count(*) from event where type = 'resource.delta')," +
        "'scopeSnapshotEvents',(select count(*) from event where type = 'version.scope.snapshot_created')" +
        ")::text"
    function Invoke-DbCounters {
        $previousEAP = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            $raw = (& $psql -h $dbHost -p $dbPort -U $dbUser -d $dbName -tAc $countersSql 2>&1 | Out-String).Trim()
            $code = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previousEAP
        }
        if ($code -ne 0) { throw "psql failed (exit=$code): $($raw.Substring(0, [Math]::Min(400, $raw.Length)))" }
        return ($raw | ConvertFrom-Json)
    }
    try {
        $counters = Invoke-DbCounters
    } catch {
        $red.Add("db.zero-data: counters query failed: $($_.Exception.Message)")
    }
    if ($counters) {
        # The discovery delta (W1-12/W1-14) is triggered asynchronously when the
        # wave turns terminal; wait a bounded time for its decision write-back
        # instead of racing it. This waits for the system, it does not relax the
        # zero-data threshold.
        $deadline = (Get-Date).AddSeconds(120)
        while ([int64]$counters.obsTotal -gt 0 -and [int64]$counters.obsDecisionNotNew -lt 1 -and (Get-Date) -lt $deadline) {
            Write-Host '   ... waiting for the discovery delta to settle observations (up to 120s)'
            Start-Sleep -Seconds 5
            try { $counters = Invoke-DbCounters } catch { break }
        }
        foreach ($key in 'planItems', 'planEvents', 'waves', 'scopeSnapshots', 'caseRuns', 'caseRunsPassed', 'evidenceRows', 'events', 'obsTotal', 'obsPage', 'obsApi', 'obsDecisionNotNew', 'resourceDeltaEvents', 'scopeSnapshotEvents') {
            $value = [int64]$counters.$key
            if ($value -lt 1) { $red.Add("db.zero-data: $key=$value") }
        }
        Write-Host '   zero-data counters:'
        foreach ($key in 'planItems', 'planEvents', 'waves', 'scopeSnapshots', 'caseRuns', 'caseRunsPassed', 'evidenceRows', 'events', 'obsTotal', 'obsPage', 'obsApi', 'obsDecisionNotNew', 'resourceDeltaEvents', 'scopeSnapshotEvents') {
            Write-Host ("      {0,-20} = {1}" -f $key, $counters.$key)
        }
    }
}

# ---------------------------------------------------------------------------
# 10. Machine verdict. Recomputed here; no PASS is ever read from old text.
# ---------------------------------------------------------------------------
$gitHead = ''
try { $gitHead = (& git rev-parse HEAD 2>$null | Out-String).Trim() } catch { }

$status = 'FAIL'
if ($externalBlocked) {
    $status = 'EXTERNAL-BLOCKED'
} elseif ($red.Count -eq 0 -and -not $SkipE2E -and -not $SkipW0 -and $null -ne $counters) {
    $status = 'PASS'
}

$verdict = [ordered]@{
    generated_at = (Get-Date -Format o)
    gitHead      = [string]$gitHead
    gates        = @($gates | ForEach-Object { $_ })
    dbCounters   = if ($counters) { $counters } else { [ordered]@{} }
    red          = @($red | ForEach-Object { [string]$_ })
    status       = $status
}
Write-Utf8 $w1ExitVerdictPath ($verdict | ConvertTo-Json -Depth 10)

Write-Host ''
Write-Host "== W1 exit gates ($($gates.Count)):"
foreach ($gate in $gates) {
    Write-Host ("   {0,-26} exit={1,-3} {2,7}s  {3}" -f $gate.name, $gate.exit, $gate.durationSec, $gate.log)
}
Write-Host ''
Write-Host "== W1 exit red items ($($red.Count)):"
foreach ($entry in $red) { Write-Host "   - $entry" }
Write-Host ''
Write-Host "verdict: $(Get-RepoRelative $w1ExitVerdictPath)"
Write-Host "status: $status"
Write-Host ''

if ($status -eq 'PASS') {
    Write-Host 'W1 EXIT: PASS'
    exit 0
}
if ($status -eq 'EXTERNAL-BLOCKED') {
    Write-Host 'W1 EXIT: EXTERNAL-BLOCKED (environment missing; not PASS)'
    exit 2
}
Write-Host 'W1 EXIT: FAIL'
exit 1
