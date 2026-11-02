#requires -Version 5.1
<#
P7 browser E2E (Task 21b). Reuses the API/DB acceptance clone (eztest_v02_p7),
starts the real API binary from the acceptance run on 13333 and the Next.js web
app on 6868, then runs frontend/e2e/p7.spec.ts with the installed Chrome
channel. No fake LLM or fake executor is used.

The clone and its seeded data come from scripts/p7-acceptance.ps1; this script
never resets the database. Screenshots are copied to
docs/version-V0.2/acceptance/p7/screenshots and a summary is written to
docs/version-V0.2/acceptance/p7/e2e.md.

Usage:
  powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p7-e2e.ps1
#>
param(
  [int]$ApiPort = 13333,
  [int]$WebPort = 6868,
  [string]$ApiBaseUrl = "http://127.0.0.1:13333",
  [string]$DatabaseName = "eztest_v02_p7"
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$frontendDir = Join-Path $repoRoot "frontend"
$evidence = Join-Path $repoRoot ".tmp\p7-e2e"
$stagingShots = Join-Path $evidence "screenshots"
$docsDir = Join-Path $repoRoot "docs\version-V0.2\acceptance\p7"
$docsShots = Join-Path $docsDir "screenshots"
$playwrightLog = Join-Path $evidence "playwright.log"
$apiLog = Join-Path $evidence "api.log"
$apiErr = Join-Path $evidence "api.err.log"
$webLog = Join-Path $evidence "web.log"
$webErr = Join-Path $evidence "web.err.log"
$webBaseUrl = "http://127.0.0.1:$WebPort"

New-Item -ItemType Directory -Force -Path $evidence | Out-Null
New-Item -ItemType Directory -Force -Path $stagingShots | Out-Null
New-Item -ItemType Directory -Force -Path $docsDir | Out-Null
New-Item -ItemType Directory -Force -Path $docsShots | Out-Null
Get-ChildItem -Path $stagingShots -Filter "p7-e2e-*.png" -File -ErrorAction SilentlyContinue |
  Remove-Item -Force -ErrorAction SilentlyContinue
Get-ChildItem -Path $docsShots -Filter "p7-e2e-*.png" -File -ErrorAction SilentlyContinue |
  Remove-Item -Force -ErrorAction SilentlyContinue

$startedAt = Get-Date
$exitCode = 0
$failureMessage = ""
$passed = 0
$failed = 0
$wallSeconds = 0
$channel = "chrome"
$apiProcess = $null
$webProcess = $null
$reusedApi = $false

function Test-Health {
  param([string]$Url)
  try {
    $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 3
    return $response.StatusCode -eq 200
  } catch {
    return $false
  }
}

function Wait-Health {
  param(
    [string]$Url,
    [int]$TimeoutSeconds,
    $Process
  )
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    if ($null -ne $Process) {
      $Process.Refresh()
      if ($Process.HasExited) { return $false }
    }
    if (Test-Health $Url) { return $true }
    Start-Sleep -Milliseconds 500
  }
  return $false
}

function Stop-Port {
  param([int]$Port)
  try {
    $connections = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction Stop
    foreach ($connection in $connections) {
      Stop-Process -Id $connection.OwningProcess -Force -ErrorAction SilentlyContinue
    }
  } catch {
  }
}

function Stop-ProcessTree {
  param($Process)
  if ($null -eq $Process) { return }
  try {
    if (-not $Process.HasExited) {
      & taskkill.exe /PID $Process.Id /T /F 2>$null | Out-Null
    }
  } catch {
  }
}

function Read-DotEnv {
  param([string]$Path)
  $map = @{}
  foreach ($line in Get-Content -LiteralPath $Path) {
    $trim = $line.Trim()
    if ($trim -eq "" -or $trim.StartsWith("#") -or -not $trim.Contains("=")) { continue }
    $parts = $trim.Split("=", 2)
    $map[$parts[0].Trim()] = $parts[1].Trim()
  }
  return $map
}

function Get-P7DatabaseUrl {
  param([string]$Name)
  $dotenv = Read-DotEnv (Join-Path $repoRoot ".env")
  $raw = [string]$dotenv["EZTEST_DATABASE_URL"]
  $pattern = '^(?<scheme>postgres(?:ql)?)://(?<user>[^:/@]+)(?::(?<password>[^@]*))?@(?<host>[^:/?]+)(?::(?<port>\d+))?/(?<database>[^?]+)(?<query>\?.*)?$'
  $m = [regex]::Match($raw, $pattern)
  if (-not $m.Success) { throw "EZTEST_DATABASE_URL is not a supported postgres URL" }
  return ("{0}://{1}:{2}@{3}:{4}/{5}" -f $m.Groups["scheme"].Value,
    [uri]::EscapeDataString([uri]::UnescapeDataString($m.Groups["user"].Value)),
    [uri]::EscapeDataString([uri]::UnescapeDataString($m.Groups["password"].Value)),
    $m.Groups["host"].Value, $m.Groups["port"].Value, $Name)
}

function Write-LogTail {
  param([string]$Path, [string]$Label, [int]$Lines = 80)
  if (-not (Test-Path -LiteralPath $Path)) { return }
  Write-Host ""
  Write-Host "--- $Label (last $Lines lines) ---"
  Get-Content -LiteralPath $Path -Tail $Lines -ErrorAction SilentlyContinue | ForEach-Object {
    Write-Host $_
  }
}

function Copy-Screenshots {
  if (-not (Test-Path -LiteralPath $stagingShots)) { return }
  Get-ChildItem -Path $stagingShots -Filter "p7-e2e-*.png" -File -ErrorAction SilentlyContinue |
    ForEach-Object {
      Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $docsShots $_.Name) -Force
    }
}

function Write-E2eDoc {
  param([string]$Status)
  $shotNames = @()
  if (Test-Path -LiteralPath $docsShots) {
    $shotNames = @(Get-ChildItem -Path $docsShots -Filter "p7-e2e-*.png" -File -ErrorAction SilentlyContinue |
      Sort-Object Name | ForEach-Object { $_.Name })
  }
  $shotList = ""
  if ($shotNames.Count -eq 0) {
    $shotList = "(no screenshots were produced)"
  } else {
    $shotList = ($shotNames | ForEach-Object { "- docs/version-V0.2/acceptance/p7/screenshots/$_" }) -join "`r`n"
  }
  $scenarioRows = @(
    "| 1 | /knowledge list filters + create/approve/revoke/re-approve/reject | $Status |",
    "| 2 | report PDF badge/download + template v2 appendix in HTML | $Status |",
    "| 3 | settings GC dry-run + APPLY confirmation gate + instance roster | $Status |",
    "| 4 | version issues recurrence count/merged chain/reopen dialog | $Status |",
    "| 5 | /reports global centre PDF availability (read-only) | $Status |"
  ) -join "`r`n"
  $failureSection = "None blocking."
  $bugNote = @"
- P7-E2E-BUG-01 (non-blocking): report detail duplicates associated refs.
  delivery_report_ref stores refs per revision and the report detail projection
  returns them for every revision, so report 375072738047426560 returns 20 refs
  for 5 unique entities. ReportDetailPanel renders duplicate rows and React logs
  duplicate-key warnings. Evidence: screenshots p7-e2e-11-report-detail-pdf-done.png
  and p7-e2e-21-global-reports-pdf.png, and .tmp/p7-e2e/web.err.log. No product
  code was patched.
"@
  if ($failureMessage -ne "") {
    $failureSection = "Playwright/driver failure: $failureMessage`r`n`r`nSee .tmp/p7-e2e/playwright.log, api.err.log and web.err.log."
  } else {
    $failureSection = $bugNote
  }
  $doc = @"
# EzTest 0.2 P7 browser E2E (Task 21b)

- Date: $(Get-Date -Format "yyyy-MM-dd HH:mm:ss zzz")
- Branch: feat/v0.2-p7 (no git commands were run)
- Database: $DatabaseName (reused clone from the P7 API/DB acceptance; never reset)
- API: $ApiBaseUrl ; Web: $webBaseUrl
- Browser: Playwright channel $channel (installed Chrome, headless)
- Wall time: ${wallSeconds}s
- Result: $Status (exit code $exitCode; $passed passed, $failed failed)
- Screenshots: $($shotNames.Count)

## Commands

- powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p7-e2e.ps1
- pnpm exec playwright test e2e/p7.spec.ts --reporter=line (from frontend/)

## Scenario results

| # | Scenario | Result |
|---|---|---|
$scenarioRows

## Screenshot paths

$shotList

## Evidence paths

- Playwright log: .tmp/p7-e2e/playwright.log
- API logs: .tmp/p7-e2e/api.log, .tmp/p7-e2e/api.err.log
- Web logs: .tmp/p7-e2e/web.log, .tmp/p7-e2e/web.err.log
- Screenshot staging: .tmp/p7-e2e/screenshots

## Data and mutations

- The clone eztest_v02_p7 is reused as seeded by scripts/p7-acceptance.ps1.
- Knowledge: two drafts are created through the UI. One is approved, revoked and
  re-approved; the other is rejected. They remain in the clone (unique title per run).
- Issues: the recurrence target 990200 is confirmed and then reopened with the
  required reason, restoring the open state (reopen_count increments per run).
  The merged issue is read-only for the chain assertion.
- GC: one dry-run is executed; APPLY is only tested up to the typing gate and is
  never submitted, so the clone content ledger is not modified.
- Report PDF/HTML are downloads only; no report regeneration is triggered.
- Global reports are read-only.
- Starting the API while the PDF renderer is offline makes the reconciler retry
  the still-pending S12 report revision 375317751603920896; its pdf_status can
  move pending -> failed. That revision is not used by this spec.

## Product defects / blockers

$failureSection
"@
  Set-Content -Path (Join-Path $docsDir "e2e.md") -Value $doc -Encoding utf8
}

try {
  if (-not (Test-Health "$ApiBaseUrl/api/health")) {
    $binary = Join-Path (Join-Path $repoRoot ".tmp\p7-acceptance") "eztest-p7.exe"
    if (-not (Test-Path -LiteralPath $binary)) {
      throw "missing $binary; run scripts/p7-acceptance.ps1 first"
    }
    Write-Host "==> Starting the P7 API on $ApiPort (database $DatabaseName)"
    Stop-Port $ApiPort
    Start-Sleep -Milliseconds 300
    $env:EZTEST_DATABASE_URL = Get-P7DatabaseUrl $DatabaseName
    $env:EZTEST_API_PORT = [string]$ApiPort
    $env:EZTEST_WEB_PORT = [string]$WebPort
    $env:EZTEST_EXECUTOR_PORT = "13345"
    $apiProcess = Start-Process -FilePath $binary -WorkingDirectory $repoRoot -PassThru -NoNewWindow `
      -RedirectStandardOutput $apiLog -RedirectStandardError $apiErr
    if (-not (Wait-Health "$ApiBaseUrl/api/health" 120 $apiProcess)) {
      Write-LogTail $apiErr "API stderr"
      throw "P7 API did not become healthy on $ApiPort"
    }
  } else {
    $reusedApi = $true
    Write-Host "==> Reusing the healthy API already listening on $ApiPort"
  }

  Write-Host "==> Starting the web app on $WebPort"
  Stop-Port $WebPort
  Start-Sleep -Milliseconds 300
  $env:NEXT_PUBLIC_API_BASE_URL = $ApiBaseUrl
  $env:NEXT_TELEMETRY_DISABLED = "1"
  $webProcess = Start-Process -FilePath "pnpm.cmd" -ArgumentList @("dev", "--port", "$WebPort") `
    -WorkingDirectory $frontendDir -PassThru -WindowStyle Hidden `
    -RedirectStandardOutput $webLog -RedirectStandardError $webErr
  if (-not (Wait-Health "$webBaseUrl/login" 180 $webProcess)) {
    Write-LogTail $webErr "Web stderr"
    throw "web app did not become ready on $WebPort"
  }

  if ([string]::IsNullOrWhiteSpace($env:EZTEST_E2E_CHANNEL)) {
    $env:EZTEST_E2E_CHANNEL = "chrome"
  }
  $channel = $env:EZTEST_E2E_CHANNEL
  $env:EZTEST_E2E_BASE_URL = $webBaseUrl
  $env:EZTEST_E2E_API_URL = $ApiBaseUrl
  $env:EZTEST_E2E_P7_SCREENSHOT_DIR = $stagingShots
  # Windows PowerShell 5.1 can surface Node's stderr warnings as terminating
  # errors while a native command is redirected. Drop the inherited NO_COLOR
  # (the harness sets it) and force colors off so Node does not warn about the
  # NO_COLOR/FORCE_COLOR conflict, and keep Continue across the native call.
  Remove-Item Env:NO_COLOR -ErrorAction SilentlyContinue
  $env:FORCE_COLOR = "0"

  Write-Host "==> Running the P7 Playwright spec"
  Remove-Item -LiteralPath $playwrightLog -Force -ErrorAction SilentlyContinue
  Push-Location $frontendDir
  $previousErrorAction = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  $playwrightExit = 0
  try {
    & pnpm exec playwright test e2e/p7.spec.ts --reporter=line *> $playwrightLog
    $playwrightExit = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $previousErrorAction
    Pop-Location
  }
  if (Test-Path -LiteralPath $playwrightLog) {
    Get-Content -LiteralPath $playwrightLog | ForEach-Object { Write-Host $_ }
  }
  if ($playwrightExit -ne 0) {
    throw "P7 browser E2E failed (Playwright exit code $playwrightExit)"
  }
} catch {
  $exitCode = 1
  $failureMessage = $_.Exception.Message
  Write-Host ""
  Write-Host "P7 E2E FAILED: $failureMessage"
  Write-LogTail $playwrightLog "Playwright"
  Write-LogTail $apiErr "API stderr"
  Write-LogTail $webErr "Web stderr"
} finally {
  $wallSeconds = [int]((Get-Date) - $startedAt).TotalSeconds

  $logText = ""
  if (Test-Path -LiteralPath $playwrightLog) {
    $logText = Get-Content -LiteralPath $playwrightLog -Raw -ErrorAction SilentlyContinue
    if ($null -eq $logText) { $logText = "" }
  }
  $passedMatch = [regex]::Match($logText, "(\d+)\s+passed")
  if ($passedMatch.Success) { $passed = [int]$passedMatch.Groups[1].Value }
  $failedMatch = [regex]::Match($logText, "(\d+)\s+failed")
  if ($failedMatch.Success) { $failed = [int]$failedMatch.Groups[1].Value }

  Copy-Screenshots
  $shotCount = @(Get-ChildItem -Path $docsShots -Filter "p7-e2e-*.png" -File -ErrorAction SilentlyContinue).Count
  $status = "PASS"
  if ($exitCode -ne 0) { $status = "FAIL" }
  try {
    Write-E2eDoc $status
  } catch {
    Write-Host "WARNING: could not write e2e.md: $($_.Exception.Message)"
  }

  if (-not $reusedApi -and $null -ne $apiProcess) {
    Stop-ProcessTree $apiProcess
  }
  if ($null -ne $webProcess) {
    Stop-ProcessTree $webProcess
  }
  Remove-Item Env:EZTEST_DATABASE_URL -ErrorAction SilentlyContinue
  Remove-Item Env:EZTEST_API_PORT -ErrorAction SilentlyContinue
  Remove-Item Env:EZTEST_WEB_PORT -ErrorAction SilentlyContinue
  Remove-Item Env:EZTEST_EXECUTOR_PORT -ErrorAction SilentlyContinue
  Remove-Item Env:EZTEST_E2E_P7_SCREENSHOT_DIR -ErrorAction SilentlyContinue
  Remove-Item Env:FORCE_COLOR -ErrorAction SilentlyContinue

  if ($exitCode -eq 0) {
    Write-Host ""
    Write-Host "==> P7 E2E PASSED in ${wallSeconds}s; $shotCount screenshots copied to $docsShots"
    Write-Host "==> Summary: $(Join-Path $docsDir 'e2e.md')"
  } else {
    Write-Host ""
    Write-Host "==> P7 E2E FAILED in ${wallSeconds}s; evidence in $evidence"
  }
}

exit $exitCode
