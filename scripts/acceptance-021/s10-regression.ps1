#requires -Version 5.1
<#
.SYNOPSIS
    S10 regression acceptance for EzTest 0.2.1.

.DESCRIPTION
    Runs the deterministic regression and static gates required by spec 13.2
    S10:
      * the 0.2.1 contract presence check (node scripts/v021-contract-check.cjs);
      * the deterministic fake-LLM smoke test and the fake-LLM protocol test
        suites (P1/P2/P4/P5/P6/P7);
      * go vet and the Go unit suite (unless -SkipGoTests);
      * frontend lint, typecheck and unit tests (unless -SkipFrontendTests);
      * executor unit tests.

    The full Playwright E2E suite (scripts/e2e.ps1) starts its own dedicated
    eztest_e2e database and API/web/executor on the same ports as the shared
    acceptance stack; running it while S0-S9 uses that stack would corrupt both
    runs. It is therefore not run by default: the scenario records an explicit
    warning with the reason. Pass -RunFullE2E to run it (the caller must stop
    the shared stack first).

    Every gate is recorded as its own check with the command, exit code and a
    log under docs/version-V0.2.1/acceptance/evidence/s7-s10/s10/. A failing
    gate fails the scenario; nothing is skipped silently.

.PARAMETER RunFullE2E
    Run scripts/e2e.ps1 as well (requires the shared acceptance stack to be
    stopped; the e2e script starts its own stack on ports 13333/6868/13336).

.PARAMETER SkipGoTests
    Skip `go test ./...` (the go vet gate still runs).

.PARAMETER SkipFrontendTests
    Skip the frontend unit suite (lint and typecheck still run).

.NOTES
    Exit codes: 0 = passed, 1 = failed acceptance assertion, 2 = blocked
    environment. No git writes, no product code changes.
#>
[CmdletBinding()]
param(
    [string] $EvidenceRoot = "",
    [switch] $RunFullE2E,
    [switch] $SkipGoTests,
    [switch] $SkipFrontendTests,
    [int] $GateTimeoutSeconds = 900
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib.ps1")

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $EvidenceRoot = Join-Path $repoRoot "docs/version-V0.2.1/acceptance/evidence/s7-s10"
}
$context = Initialize-AcceptanceContext -RepoRoot $repoRoot -EvidenceRoot $EvidenceRoot
$scenario = New-AcceptanceScenario -Context $context -Name "s10" -Title "Regression: fake-LLM E2E, contract, static gates (S10)"

$exitCode = 2
$gateResults = [System.Collections.Generic.List[object]]::new()

function Resolve-Tool {
    param([string] $Name, [string[]] $Candidates)
    foreach ($candidate in $Candidates) {
        $command = Get-Command $candidate -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -ne $command) { return $command.Source }
    }
    return $null
}

function Invoke-AcceptanceGate {
    param(
        [Parameter(Mandatory = $true)] [string] $Name,
        [Parameter(Mandatory = $true)] [string] $FilePath,
        [Parameter(Mandatory = $false)] [string[]] $Arguments = @(),
        [Parameter(Mandatory = $false)] [string] $WorkingDirectory = $repoRoot,
        [Parameter(Mandatory = $false)] [int] $TimeoutSeconds = 900,
        [Parameter(Mandatory = $false)] [string] $Note = ""
    )
    $safeName = ($Name -replace '[^A-Za-z0-9._-]', '-')
    $logPath = Join-Path $scenario.Directory ("gate-" + $safeName + ".log")
    $started = Get-Date
    $output = @()
    $exit = 1
    $timedOut = $false
    try {
        $job = Start-Job -ScriptBlock {
            param($exe, $argList, $cwd)
            Set-Location $cwd
            $lines = & $exe @argList 2>&1
            $code = $LASTEXITCODE
            return [pscustomobject]@{ Lines = @($lines | ForEach-Object { [string]$_ }); ExitCode = $code }
        } -ArgumentList $FilePath, $Arguments, $WorkingDirectory
        $completed = Wait-Job -Job $job -Timeout $TimeoutSeconds
        if ($null -eq $completed) {
            $timedOut = $true
            Stop-Job -Job $job -ErrorAction SilentlyContinue
            $output = @("gate timed out after ${TimeoutSeconds}s")
            $exit = 124
        } else {
            $received = Receive-Job -Job $job -ErrorAction SilentlyContinue
            $result = $received | Select-Object -Last 1
            if ($null -ne $result) {
                $output = @($result.Lines)
                $exit = [int]$result.ExitCode
            } else {
                $output = @("gate produced no result")
                $exit = 1
            }
        }
        Remove-Job -Job $job -Force -ErrorAction SilentlyContinue
    } catch {
        $output = @("gate invocation error: " + $_.Exception.Message)
        $exit = 1
    }
    $duration = [Math]::Round(((Get-Date) - $started).TotalSeconds, 1)
    Write-EvidenceText -Path $logPath -Text (($output -join [Environment]::NewLine) + [Environment]::NewLine)
    Add-AcceptanceArtifact -Scenario $scenario -Path $logPath -Kind "log" -Note ("S10 gate log: " + $Name)
    $result = [pscustomobject]@{
        name = $Name
        command = $FilePath + " " + ($Arguments -join " ")
        working_directory = $WorkingDirectory
        exit_code = $exit
        timed_out = $timedOut
        duration_seconds = $duration
        log = (Resolve-RepoRelativePath -Context $context -Path $logPath)
        note = $Note
    }
    [void]$gateResults.Add($result)
    Add-AcceptanceCheck -Scenario $scenario -Name ("gate: " + $Name) -Passed ($exit -eq 0) -Severity assertion `
        -Detail ("exit=$exit duration=${duration}s log=$(Split-Path -Leaf $logPath) " + $Note)
    return $result
}

try {
    Write-AcceptanceLog -Scenario $scenario -Message "S10 regression; fullE2E=$([bool]$RunFullE2E) skipGoTests=$([bool]$SkipGoTests) skipFrontendTests=$([bool]$SkipFrontendTests)"

    $node = Resolve-Tool -Name "node" -Candidates @("node.exe", "node")
    $powershellExe = Resolve-Tool -Name "powershell" -Candidates @("powershell.exe", "powershell")
    $pnpm = Resolve-Tool -Name "pnpm" -Candidates @("pnpm.cmd", "pnpm")
    $go = Join-Path $repoRoot ".tools\go\bin\go.exe"
    if (-not (Test-Path -LiteralPath $go)) { $go = Resolve-Tool -Name "go" -Candidates @("go.exe", "go") }

    Add-AcceptanceCheck -Scenario $scenario -Name "regression toolchain available" -Passed ($null -ne $node -and $null -ne $powershellExe -and $null -ne $go) -Severity blocker `
        -Detail ("node=$node powershell=$powershellExe go=$go pnpm=$pnpm")

    if ($null -ne $node) {
        Invoke-AcceptanceGate -Name "v021-contract-check" -FilePath $node -Arguments @((Join-Path $repoRoot "scripts/v021-contract-check.cjs")) `
            -WorkingDirectory $repoRoot -TimeoutSeconds 120 -Note "0.2.1 contract presence" | Out-Null
    }

    if ($null -ne $powershellExe) {
        Invoke-AcceptanceGate -Name "fake-llm-smoke" -FilePath $powershellExe -Arguments @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $repoRoot "scripts/fake-llm-smoke.ps1")) `
            -WorkingDirectory $repoRoot -TimeoutSeconds 120 -Note "deterministic fake-LLM smoke" | Out-Null
    }

    if ($null -ne $node) {
        $fakeTests = @(
            "scripts/fake-llm-p1.test.cjs",
            "scripts/fake-llm-p2.test.cjs",
            "scripts/fake-llm.p4.test.cjs",
            "scripts/fake-llm.p5.test.cjs",
            "scripts/fake-llm.p6.test.cjs",
            "scripts/fake-llm.p7.test.cjs"
        )
        foreach ($relative in $fakeTests) {
            $fileName = Split-Path -Leaf $relative
            Invoke-AcceptanceGate -Name ("fake-llm-tests-" + $fileName) -FilePath $node `
                -Arguments @("--test", (Join-Path $repoRoot $relative)) -WorkingDirectory $repoRoot -TimeoutSeconds 180 `
                -Note "fake-LLM protocol regression" | Out-Null
        }
    }

    if ($null -ne $go) {
        Invoke-AcceptanceGate -Name "go-vet" -FilePath $go -Arguments @("vet", "./...") -WorkingDirectory (Join-Path $repoRoot "backend") -TimeoutSeconds 600 -Note "Go static analysis" | Out-Null
        if (-not $SkipGoTests) {
            Invoke-AcceptanceGate -Name "go-unit-tests" -FilePath $go -Arguments @("test", "-count=1", "./...") -WorkingDirectory (Join-Path $repoRoot "backend") -TimeoutSeconds $GateTimeoutSeconds -Note "Go unit suite" | Out-Null
        } else {
            Add-AcceptanceCheck -Scenario $scenario -Name "gate: go-unit-tests" -Passed $false -Severity warning -Detail "skipped by -SkipGoTests"
        }
    }

    if ($null -ne $pnpm) {
        Invoke-AcceptanceGate -Name "frontend-lint" -FilePath $pnpm -Arguments @("lint") -WorkingDirectory (Join-Path $repoRoot "frontend") -TimeoutSeconds 300 -Note "Biome lint" | Out-Null
        Invoke-AcceptanceGate -Name "frontend-typecheck" -FilePath $pnpm -Arguments @("typecheck") -WorkingDirectory (Join-Path $repoRoot "frontend") -TimeoutSeconds 600 -Note "TypeScript typecheck" | Out-Null
        if (-not $SkipFrontendTests) {
            Invoke-AcceptanceGate -Name "frontend-unit-tests" -FilePath $pnpm -Arguments @("test") -WorkingDirectory (Join-Path $repoRoot "frontend") -TimeoutSeconds $GateTimeoutSeconds -Note "Vitest unit suite" | Out-Null
        } else {
            Add-AcceptanceCheck -Scenario $scenario -Name "gate: frontend-unit-tests" -Passed $false -Severity warning -Detail "skipped by -SkipFrontendTests"
        }
        Invoke-AcceptanceGate -Name "executor-unit-tests" -FilePath $pnpm -Arguments @("test") -WorkingDirectory (Join-Path $repoRoot "executor") -TimeoutSeconds 600 -Note "executor unit suite" | Out-Null
    } else {
        Add-AcceptanceCheck -Scenario $scenario -Name "pnpm available for frontend/executor gates" -Passed $false -Severity warning -Detail "pnpm is not on PATH; frontend/executor gates were not run"
    }

    if ($RunFullE2E) {
        if ($null -ne $powershellExe) {
            Invoke-AcceptanceGate -Name "full-fake-llm-e2e" -FilePath $powershellExe -Arguments @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $repoRoot "scripts/e2e.ps1")) `
                -WorkingDirectory $repoRoot -TimeoutSeconds 1800 -Note "full Playwright fake-LLM E2E suite" | Out-Null
        }
    } else {
        Add-AcceptanceCheck -Scenario $scenario -Name "full Playwright fake-LLM E2E suite" -Passed $false -Severity warning `
            -Detail "not run: scripts/e2e.ps1 starts its own API/web/executor on 13333/6868/13336 and resets eztest_e2e; the shared acceptance stack is using those ports. Re-run S10 with -RunFullE2E after stopping the shared stack."
    }

    Write-EvidenceJson -Path (Join-Path $scenario.Directory "s10-gates.json") -Value ([pscustomobject]@{
        full_e2e_requested = [bool]$RunFullE2E
        skip_go_tests = [bool]$SkipGoTests
        skip_frontend_tests = [bool]$SkipFrontendTests
        gates = @($gateResults)
    })
    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "s10-gates.json") -Kind "result" -Note "S10 gate summary"
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S10 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S10 regression gates"
}

exit $exitCode
