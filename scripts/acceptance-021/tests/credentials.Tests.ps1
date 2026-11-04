#requires -Version 5.1
# Focused tests for Task 113 (P0-T22): the acceptance library reads the EzTest
# administrator and SUT credentials from the environment or .env only. It must
# never fall back to a built-in password and must fail with a clear message when
# a credential is missing.
#
#   powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-021/tests/credentials.Tests.ps1
Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

$testsDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$scriptsDir = Split-Path -Parent $testsDir
$repoRoot = Split-Path -Parent (Split-Path -Parent $scriptsDir)
. (Join-Path $scriptsDir "lib.ps1")

$script:checks = 0
$script:failures = 0

# The legacy defaults are assembled at runtime so this test itself does not
# reintroduce the literals it forbids in the acceptance library.
$legacyAdminDefault = "legacy-admin-" + "default"
$legacySutDefault = "legacy-sut-" + "password"
$credentialEnvNames = @("EZTEST_ADMIN_PASSWORD", "SUT_PASSWORD", "EZTEST_ADMIN_USERNAME")
$testAdminPassword = "test-acceptance-admin-password"
$testSutPassword = "test-acceptance-sut-password"
$dotEnvAdminPassword = "dotenv-admin-password"
$dotEnvSutPassword = "dotenv-sut-password"

function Assert-True {
    param([bool] $Condition, [string] $Message)
    $script:checks++
    if (-not $Condition) {
        $script:failures++
        Write-Host "[FAIL] $Message" -ForegroundColor Red
    } else {
        Write-Host "[PASS] $Message"
    }
}

function Assert-Equal {
    param($Expected, $Actual, [string] $Message)
    Assert-True -Condition ($Expected -eq $Actual) -Message ("{0} (expected={1} actual={2})" -f $Message, $Expected, $Actual)
}

function Assert-Throws {
    param([scriptblock] $Action, [string] $Pattern, [string] $Message)
    $script:checks++
    $threw = $false
    $caught = ""
    try { & $Action | Out-Null } catch { $threw = $true; $caught = $_.Exception.Message }
    if (-not $threw) {
        $script:failures++
        Write-Host "[FAIL] $Message (did not throw)" -ForegroundColor Red
        return
    }
    if ($caught -match $Pattern) {
        Write-Host "[PASS] $Message"
    } else {
        $script:failures++
        Write-Host ("[FAIL] {0} (message='{1}')" -f $Message, $caught) -ForegroundColor Red
    }
}

function Invoke-WithCleanCredentialEnv {
    param([Parameter(Mandatory = $true)] [scriptblock] $Action)
    $saved = @{}
    foreach ($name in $credentialEnvNames) {
        $saved[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
        [Environment]::SetEnvironmentVariable($name, $null, "Process")
    }
    try {
        & $Action
    } finally {
        foreach ($name in $credentialEnvNames) {
            [Environment]::SetEnvironmentVariable($name, $saved[$name], "Process")
        }
    }
}

function New-TempRepo {
    param([Parameter(Mandatory = $false)] [string] $DotEnv = "")
    $path = Join-Path $repoRoot (".tmp\task113-credentials\" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Force -Path $path | Out-Null
    if (-not [string]::IsNullOrWhiteSpace($DotEnv)) {
        [System.IO.File]::WriteAllText((Join-Path $path ".env"), $DotEnv, [System.Text.UTF8Encoding]::new($false))
    }
    return $path
}

function Remove-TempRepo {
    param([Parameter(Mandatory = $true)] [string] $Path)
    Remove-Item -LiteralPath $Path -Recurse -Force -ErrorAction SilentlyContinue
}

function Get-ContextCredential {
    param([Parameter(Mandatory = $true)] $Context, [Parameter(Mandatory = $true)] [string] $Name)
    if ($Context.ContainsKey($Name)) { return [string]$Context[$Name] }
    return "<missing>"
}

# ---------------------------------------------------------------------------
# Static guard: the library must not carry a built-in password or a candidate
# fallback list.
# ---------------------------------------------------------------------------
function Test-LibraryHasNoBuiltInCredentials {
    $text = Get-Content -LiteralPath (Join-Path $scriptsDir "lib.ps1") -Raw -Encoding UTF8
    Assert-True -Condition (-not $text.Contains($legacyAdminDefault)) -Message "lib.ps1 no longer contains the old product admin password"
    Assert-True -Condition (-not $text.Contains($legacySutDefault)) -Message "lib.ps1 no longer contains the old SUT password"
    Assert-True -Condition ($text -notmatch 'PasswordCandidates') -Message "lib.ps1 no longer tries a list of password candidates"
    Assert-True -Condition ($text -match 'EZTEST_ADMIN_PASSWORD') -Message "lib.ps1 names EZTEST_ADMIN_PASSWORD in its credential contract"
    Assert-True -Condition ($text -match 'SUT_PASSWORD') -Message "lib.ps1 names SUT_PASSWORD in its credential contract"
}

# ---------------------------------------------------------------------------
# Missing credentials fail the preflight with an actionable message; no
# fallback is attempted.
# ---------------------------------------------------------------------------
function Test-MissingAdminPasswordFails {
    $temp = New-TempRepo
    try {
        Invoke-WithCleanCredentialEnv {
            Assert-Throws -Action {
                New-AcceptanceContext -RepoRoot $temp -EvidenceRoot (Join-Path $temp "evidence")
            } -Pattern 'EZTEST_ADMIN_PASSWORD' -Message "missing EZTEST_ADMIN_PASSWORD fails the context preflight"
        }
    } finally {
        Remove-TempRepo -Path $temp
    }
}

function Test-MissingSutPasswordFails {
    $temp = New-TempRepo
    try {
        Invoke-WithCleanCredentialEnv {
            [Environment]::SetEnvironmentVariable("EZTEST_ADMIN_PASSWORD", $testAdminPassword, "Process")
            Assert-Throws -Action {
                New-AcceptanceContext -RepoRoot $temp -EvidenceRoot (Join-Path $temp "evidence")
            } -Pattern 'SUT_PASSWORD' -Message "missing SUT_PASSWORD fails the context preflight"
        }
    } finally {
        Remove-TempRepo -Path $temp
    }
}

function Test-EmptyCredentialsAreMissing {
    $temp = New-TempRepo
    try {
        Invoke-WithCleanCredentialEnv {
            [Environment]::SetEnvironmentVariable("EZTEST_ADMIN_PASSWORD", "   ", "Process")
            [Environment]::SetEnvironmentVariable("SUT_PASSWORD", "   ", "Process")
            Assert-Throws -Action {
                New-AcceptanceContext -RepoRoot $temp -EvidenceRoot (Join-Path $temp "evidence")
            } -Pattern 'EZTEST_ADMIN_PASSWORD' -Message "blank credentials are rejected like missing ones"
        }
    } finally {
        Remove-TempRepo -Path $temp
    }
}

# ---------------------------------------------------------------------------
# Process environment is the primary source and drives redaction.
# ---------------------------------------------------------------------------
function Test-EnvironmentCredentialsAreUsed {
    $temp = New-TempRepo
    try {
        Invoke-WithCleanCredentialEnv {
            [Environment]::SetEnvironmentVariable("EZTEST_ADMIN_PASSWORD", $testAdminPassword, "Process")
            [Environment]::SetEnvironmentVariable("SUT_PASSWORD", $testSutPassword, "Process")
            $context = New-AcceptanceContext -RepoRoot $temp -EvidenceRoot (Join-Path $temp "evidence")
            $adminPassword = Get-ContextCredential -Context $context -Name "AdminPassword"
            $sutPassword = Get-ContextCredential -Context $context -Name "SutPassword"
            Assert-True -Condition ($context.ContainsKey("AdminPassword")) -Message "the context exposes the admin password source"
            Assert-Equal -Expected $testAdminPassword -Actual $adminPassword -Message "EZTEST_ADMIN_PASSWORD is used as-is"
            Assert-Equal -Expected $testSutPassword -Actual $sutPassword -Message "SUT_PASSWORD is used as-is"
            Assert-True -Condition (-not $context.ContainsKey("PasswordCandidates")) -Message "the context no longer carries a fallback candidate list"
            Assert-True -Condition ($script:RedactionSecrets -contains $testAdminPassword) -Message "the admin password is registered as a redaction secret"
            Assert-True -Condition ($script:RedactionSecrets -contains $testSutPassword) -Message "the SUT password is registered as a redaction secret"
        }
    } finally {
        Remove-TempRepo -Path $temp
    }
}

# ---------------------------------------------------------------------------
# A repository .env is the fallback source when the process environment does
# not define the credential.
# ---------------------------------------------------------------------------
function Test-DotEnvCredentialsAreUsed {
    $dotEnv = @(
        "EZTEST_ADMIN_PASSWORD=$dotEnvAdminPassword",
        "SUT_PASSWORD=$dotEnvSutPassword"
    ) -join [Environment]::NewLine
    $temp = New-TempRepo -DotEnv $dotEnv
    try {
        Invoke-WithCleanCredentialEnv {
            $context = New-AcceptanceContext -RepoRoot $temp -EvidenceRoot (Join-Path $temp "evidence")
            Assert-Equal -Expected $dotEnvAdminPassword -Actual (Get-ContextCredential -Context $context -Name "AdminPassword") -Message ".env EZTEST_ADMIN_PASSWORD is used when the process env is unset"
            Assert-Equal -Expected $dotEnvSutPassword -Actual (Get-ContextCredential -Context $context -Name "SutPassword") -Message ".env SUT_PASSWORD is used when the process env is unset"
        }
    } finally {
        Remove-TempRepo -Path $temp
    }
}

# ---------------------------------------------------------------------------
# An explicit caller value remains supported and still bypasses the fallback.
# ---------------------------------------------------------------------------
function Test-ExplicitCredentialsAreAccepted {
    $temp = New-TempRepo
    try {
        Invoke-WithCleanCredentialEnv {
            $context = New-AcceptanceContext -RepoRoot $temp -EvidenceRoot (Join-Path $temp "evidence") `
                -AdminPassword $testAdminPassword -SutPassword $testSutPassword
            Assert-Equal -Expected $testAdminPassword -Actual (Get-ContextCredential -Context $context -Name "AdminPassword") -Message "explicit -AdminPassword is accepted"
            Assert-Equal -Expected $testSutPassword -Actual (Get-ContextCredential -Context $context -Name "SutPassword") -Message "explicit -SutPassword is accepted"
        }
    } finally {
        Remove-TempRepo -Path $temp
    }
}

# ---------------------------------------------------------------------------
# The runner treats missing credentials as a blocked environment preflight:
# exit code 2 with a message that names the variable, before any scenario runs.
# ---------------------------------------------------------------------------
function Test-RunnerFailsClosedWithoutCredentials {
    $temp = New-TempRepo
    try {
        Invoke-WithCleanCredentialEnv {
            $output = @()
            $exit = -1
            try {
                $output = @(& powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $scriptsDir "run-all.ps1") `
                        -EvidenceRoot (Join-Path $temp "evidence") `
                        -SummaryPath (Join-Path $temp "summary.json") `
                        -ReportPath (Join-Path $temp "report.md") 2>&1)
                $exit = [int]$LASTEXITCODE
            } catch {
                $output = @($_.Exception.Message)
            }
            $text = (@($output) | ForEach-Object { [string]$_ }) -join [Environment]::NewLine
            Assert-Equal -Expected 2 -Actual $exit -Message "run-all.ps1 with no credentials exits 2 (blocked)"
            Assert-True -Condition ($text -match 'EZTEST_ADMIN_PASSWORD') -Message "run-all.ps1 names the missing EZTEST_ADMIN_PASSWORD"
            Assert-True -Condition ($text -match 'SUT_PASSWORD') -Message "run-all.ps1 names the missing SUT_PASSWORD"
            Assert-True -Condition (-not $text.Contains($legacyAdminDefault)) -Message "run-all.ps1 never prints a fallback password"
        }
    } finally {
        Remove-TempRepo -Path $temp
    }
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------
Test-LibraryHasNoBuiltInCredentials
Test-MissingAdminPasswordFails
Test-MissingSutPasswordFails
Test-EmptyCredentialsAreMissing
Test-EnvironmentCredentialsAreUsed
Test-DotEnvCredentialsAreUsed
Test-ExplicitCredentialsAreAccepted
Test-RunnerFailsClosedWithoutCredentials

Write-Host ""
Write-Host ("acceptance credential tests: {0} checks, {1} failure(s)" -f $script:checks, $script:failures)
if ($script:failures -gt 0) { exit 1 }
exit 0
