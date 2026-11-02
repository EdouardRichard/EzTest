#requires -Version 5.1
<#
P8 browser E2E (Task 14 / S12). Starts the real API on 13333 against the
eztest_v02_p8 acceptance clone and the Next.js web app on 6868, then runs
frontend/e2e/p8.spec.ts with the installed Chrome channel. No fake LLM, no fake
executor and no fixed seed ids: the spec discovers the S0-S6 data through the
real API in beforeAll.

The script never resets the database. Screenshots are copied to
docs/version-V0.2/acceptance/p8/screenshots and a summary is written to
docs/version-V0.2/acceptance/p8/e2e.md.

The API binary is taken from the P8 acceptance run when present
(.tmp/p8-acceptance/eztest-p8.exe); otherwise the script builds it from source
and only falls back to the P7 binary as a last resort. A healthy API already
listening on 13333 is reused as-is because it may be the acceptance run's server.

Usage:
  powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-e2e.ps1
  powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-e2e.ps1 -ApiPort 13333 -WebPort 6868
#>
param(
  [int]$ApiPort = 13333,
  [int]$WebPort = 6868,
  [string]$ApiBaseUrl = "http://127.0.0.1:13333",
  [string]$DatabaseName = "eztest_v02_p8"
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$frontendDir = Join-Path $repoRoot "frontend"
$evidence = Join-Path $repoRoot ".tmp\p8-e2e"
$stagingShots = Join-Path $evidence "screenshots"
$docsDir = Join-Path (Join-Path (Join-Path $repoRoot "docs\version-V0.2") "acceptance") "p8"
$docsShots = Join-Path $docsDir "screenshots"
$playwrightLog = Join-Path $evidence "playwright.log"
$apiLog = Join-Path $evidence "api.log"
$apiErr = Join-Path $evidence "api.err.log"
$webLog = Join-Path $evidence "web.log"
$webErr = Join-Path $evidence "web.err.log"
$annotationFile = Join-Path $evidence "p8-e2e-annotations.txt"
$webBaseUrl = "http://127.0.0.1:$WebPort"

New-Item -ItemType Directory -Force -Path $evidence | Out-Null
New-Item -ItemType Directory -Force -Path $stagingShots | Out-Null
New-Item -ItemType Directory -Force -Path $docsDir | Out-Null
New-Item -ItemType Directory -Force -Path $docsShots | Out-Null
Get-ChildItem -Path $stagingShots -Filter "p8-e2e-*.png" -File -ErrorAction SilentlyContinue |
  Remove-Item -Force -ErrorAction SilentlyContinue
Get-ChildItem -Path $docsShots -Filter "p8-e2e-*.png" -File -ErrorAction SilentlyContinue |
  Remove-Item -Force -ErrorAction SilentlyContinue

$startedAt = Get-Date
$exitCode = 0
$failureMessage = ""
$passed = 0
$failed = 0
$skipped = 0
$wallSeconds = 0
$channel = "chrome"
$apiProcess = $null
$webProcess = $null
$reusedApi = $false
$shotCount = 0
$scenarioTitles = @(
  "resources console lists SUTs, pages/APIs, data access and repositories",
  "versions list creates a version and opens the discovered journey version",
  "requirements workspace shows the conversation and confirms a requirement",
  "design mindmap renders the scenario graph and confirms a draft case",
  "execution workspace shows waves, evidence and the pause round-trip",
  "issues tab shows the merged chain, merges a pair and reopens an issue",
  "reports tab downloads the generated PDF and HTML",
  "exploration chain filters and node deep links survive reload",
  "completed version header reopens the version",
  "global knowledge, quality, agents, issues and reports centres render",
  "global settings show the GC and instance panels with live in-flight counters"
)

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

function Get-P8DatabaseUrl {
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

function Build-ApiBinary {
  $target = Join-Path $evidence "eztest-p8.exe"
  $go = Join-Path (Join-Path (Join-Path $repoRoot ".tools") "go\bin") "go.exe"
  if (-not (Test-Path -LiteralPath $go)) {
    $goCommand = Get-Command go.exe -ErrorAction SilentlyContinue
    if ($null -eq $goCommand) {
      throw "missing .tmp\p8-acceptance\eztest-p8.exe and no Go toolchain; run scripts/p8-acceptance.ps1 first"
    }
    $go = $goCommand.Source
  }
  Write-Host "==> Building the P8 API binary from source (no acceptance binary found)"
  Push-Location (Join-Path $repoRoot "backend")
  $previous = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  $code = 1
  try {
    & $go build -o $target ./cmd/eztest 2>&1 | ForEach-Object { Write-Host $_ }
    $code = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $previous
    Pop-Location
  }
  if ($code -ne 0 -or -not (Test-Path -LiteralPath $target)) {
    throw "go build failed with exit code $code"
  }
  return $target
}

function Resolve-ApiBinary {
  $p8Candidates = @(
    (Join-Path (Join-Path $repoRoot ".tmp\p8-acceptance") "eztest-p8.exe"),
    (Join-Path (Join-Path $repoRoot ".tmp\p8-acceptance") "eztest.exe")
  )
  foreach ($candidate in $p8Candidates) {
    if (Test-Path -LiteralPath $candidate) { return $candidate }
  }
  try {
    return (Build-ApiBinary)
  } catch {
    $p7 = Join-Path (Join-Path $repoRoot ".tmp\p7-acceptance") "eztest-p7.exe"
    if (Test-Path -LiteralPath $p7) {
      Write-Host "WARNING: using the P7 API binary ($p7); the P8 acceptance binary was not found and the source build failed: $($_.Exception.Message)"
      return $p7
    }
    throw
  }
}

function Copy-Screenshots {
  if (-not (Test-Path -LiteralPath $stagingShots)) { return }
  Get-ChildItem -Path $stagingShots -Filter "p8-e2e-*.png" -File -ErrorAction SilentlyContinue |
    ForEach-Object {
      Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $docsShots $_.Name) -Force
    }
}

function Read-AnnotationSection {
  param([string]$Heading)
  if (-not (Test-Path -LiteralPath $annotationFile)) {
    return "(annotation file not found; the spec did not reach afterAll)"
  }
  $lines = Get-Content -LiteralPath $annotationFile -Encoding UTF8
  $collected = New-Object System.Collections.Generic.List[string]
  $inSection = $false
  foreach ($line in $lines) {
    if ($line -eq $Heading) { $inSection = $true; continue }
    if ($inSection -and $line.StartsWith("## ")) { break }
    if ($inSection) { $collected.Add($line) }
  }
  $text = ($collected -join "`r`n").Trim()
  if ($text.Length -eq 0) { return "(none)" }
  return $text
}

function Get-ScenarioStatus {
  param(
    [string]$LogText,
    [string]$Title,
    [int]$RunExitCode
  )
  $escaped = [regex]::Escape($Title)
  $marker = "p8\.spec\.ts:\d+:\d+.*" + $escaped + "\s*$"
  if ($RunExitCode -eq 0) {
    $skipPattern = "(?m)^\s*-\s+\d+\s+\[[^\]]+\].*" + $marker
    if ($LogText -match $skipPattern) { return "SKIP" }
    return "PASS"
  }
  $failurePattern = "(?m)^\s*\d+\)\s+.*" + $marker
  if ($LogText -match $failurePattern) { return "FAIL" }
  return "NOT RUN"
}

function Write-E2eDoc {
  param([string]$Status)
  $shotNames = @()
  if (Test-Path -LiteralPath $docsShots) {
    $shotNames = @(Get-ChildItem -Path $docsShots -Filter "p8-e2e-*.png" -File -ErrorAction SilentlyContinue |
      Sort-Object Name | ForEach-Object { $_.Name })
  }
  $shotList = "(no screenshots were produced)"
  if ($shotNames.Count -gt 0) {
    $shotList = ($shotNames | ForEach-Object { "- docs/version-V0.2/acceptance/p8/screenshots/$_" }) -join "`r`n"
  }

  $logText = ""
  if (Test-Path -LiteralPath $playwrightLog) {
    $logText = [System.IO.File]::ReadAllText($playwrightLog)
    if ($null -eq $logText) { $logText = "" }
  }

  $mutationsText = Read-AnnotationSection "## Mutations performed by the spec"
  $gapsText = Read-AnnotationSection "## Data gaps / explicitly skipped assertions"

  # Whole-scenario skips are explicit in the spec annotations
  # ("<scenario>: ... the scenario is skipped"). Map them onto the table rows.
  $skipLabels = @()
  if ($gapsText -ne "(none)") {
    foreach ($line in ($gapsText -split "`r?`n")) {
      $skipMatch = [regex]::Match($line, "^- (?<label>[a-z-]+): .*scenario is skipped")
      if ($skipMatch.Success) { $skipLabels += $skipMatch.Groups["label"].Value }
    }
  }
  $scenarioLabels = @(
    "resources", "versions", "requirements", "design", "execution", "issues",
    "reports", "chain", "version-reopen", "", "global-settings"
  )

  $scenarioRows = @()
  $scenarioStatuses = @()
  foreach ($title in $scenarioTitles) {
    $scenarioStatuses += (Get-ScenarioStatus $logText $title $exitCode)
  }
  # Serial mode stops after the first failure: scenarios before it passed,
  # scenarios after it were not run. The failure header identifies the first one.
  $firstFailure = -1
  for ($i = 0; $i -lt $scenarioStatuses.Count; $i++) {
    if ($scenarioStatuses[$i] -eq "FAIL") { $firstFailure = $i; break }
  }
  for ($i = 0; $i -lt $scenarioStatuses.Count; $i++) {
    if ($scenarioStatuses[$i] -eq "NOT RUN" -and $firstFailure -gt $i) {
      $scenarioStatuses[$i] = "PASS"
    }
    if ($scenarioStatuses[$i] -eq "PASS" -and $scenarioLabels[$i] -ne "" -and
      $skipLabels -contains $scenarioLabels[$i]) {
      $scenarioStatuses[$i] = "SKIP"
    }
  }
  for ($i = 0; $i -lt $scenarioTitles.Count; $i++) {
    $scenarioRows += ("| {0} | {1} | {2} |" -f ($i + 1), $scenarioTitles[$i], $scenarioStatuses[$i])
  }
  $scenarioTable = $scenarioRows -join "`r`n"

  $failureSection = "None blocking observed by this run. Data gaps are listed above and in .tmp/p8-e2e/p8-e2e-annotations.txt."
  if ($failureMessage -ne "") {
    $failureSection = "Playwright/driver failure: $failureMessage`r`n`r`nSee .tmp/p8-e2e/playwright.log, api.err.log and web.err.log."
  }

  $resultText = "$Status (exit code $exitCode; $passed passed, $failed failed, $skipped skipped)"

  $doc = @"
# EzTest 0.2 P8 browser E2E (S12 / Task 14)

- Date: $(Get-Date -Format "yyyy-MM-dd HH:mm:ss zzz")
- Branch: feat/v0.2-p8 (no git commands were run)
- Database: $DatabaseName (reused S0-S6 clone; never reset)
- API: $ApiBaseUrl ; Web: $webBaseUrl
- Browser: Playwright channel $channel (installed Chrome, headless)
- Wall time: ${wallSeconds}s
- Result: $resultText
- Screenshots: $($shotNames.Count)
- Seed policy: no fixed ids; the spec logs in through the real API in beforeAll and discovers the data (EZTEST_E2E_P8_* overrides stay available).

## Commands

- powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-e2e.ps1
- pnpm exec playwright test e2e/p8.spec.ts --reporter=line (from frontend/)

## Scenario results

The table reflects the per-test Playwright line-reporter outcome (PASS / FAIL /
SKIP / NOT RUN); the full log is .tmp/p8-e2e/playwright.log.

| # | Scenario | Result |
|---|---|---|
$scenarioTable

## Screenshot paths

$shotList

## Evidence paths

- Playwright log: .tmp/p8-e2e/playwright.log
- API logs: .tmp/p8-e2e/api.log, .tmp/p8-e2e/api.err.log
- Web logs: .tmp/p8-e2e/web.log, .tmp/p8-e2e/web.err.log
- Screenshot staging: .tmp/p8-e2e/screenshots
- Spec annotations: .tmp/p8-e2e/p8-e2e-annotations.txt
- Downloads (report PDF/HTML): .tmp/p8-e2e/downloads

## Data and mutations

The P8 spec runs against the eztest_v02_p8 clone produced by the P8 acceptance
run. Mutations are recorded by the spec itself and copied here:

$mutationsText

## Data gaps and explicit annotations

$gapsText

## Product defects / blockers

$failureSection

## Rerun

- Rerun the same command after stopping any server this script left on ports 13333/6868 (the script stops what it started).
- The spec is rerunnable: issue merge/reopen and version reopen change the clone, and discovery uses the new state; a requirement confirm consumes the draft. For a full data reset, recreate eztest_v02_p8 from eztest_v02_p7 and rerun scripts/p8-acceptance.ps1, then this script.
- Environment overrides: EZTEST_E2E_ADMIN_USERNAME/PASSWORD, EZTEST_E2E_P8_VERSION_ID, EZTEST_E2E_P8_REPORT_ID, EZTEST_E2E_P8_REPORT_VERSION_ID, EZTEST_E2E_CHANNEL=msedge, EZTEST_E2E_BASE_URL, EZTEST_E2E_API_URL.
"@
  Set-Content -Path (Join-Path $docsDir "e2e.md") -Value $doc -Encoding utf8
}

try {
  if (-not (Test-Health "$ApiBaseUrl/api/health")) {
    $binary = Resolve-ApiBinary
    Write-Host "==> Starting the P8 API on $ApiPort (database $DatabaseName, binary $binary)"
    Stop-Port $ApiPort
    Start-Sleep -Milliseconds 300
    $env:EZTEST_DATABASE_URL = Get-P8DatabaseUrl $DatabaseName
    $env:EZTEST_API_PORT = [string]$ApiPort
    $env:EZTEST_WEB_PORT = [string]$WebPort
    $env:EZTEST_EXECUTOR_PORT = "13345"
    $apiProcess = Start-Process -FilePath $binary -WorkingDirectory $repoRoot -PassThru -NoNewWindow `
      -RedirectStandardOutput $apiLog -RedirectStandardError $apiErr
    if (-not (Wait-Health "$ApiBaseUrl/api/health" 120 $apiProcess)) {
      Write-LogTail $apiErr "API stderr"
      throw "P8 API did not become healthy on $ApiPort"
    }
  } else {
    $reusedApi = $true
    Write-Host "==> Reusing the healthy API already listening on $ApiPort"
    Write-Host "    (the database name cannot be verified through the API; stop it to force a restart against $DatabaseName)"
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
  $env:EZTEST_E2E_P8_SCREENSHOT_DIR = $stagingShots
  # Windows PowerShell 5.1 can surface Node's stderr warnings as terminating
  # errors while a native command is redirected. Drop the inherited NO_COLOR
  # (the harness sets it) and force colors off so Node does not warn about the
  # NO_COLOR/FORCE_COLOR conflict, and keep Continue across the native call.
  Remove-Item Env:NO_COLOR -ErrorAction SilentlyContinue
  $env:FORCE_COLOR = "0"

  Write-Host "==> Running the P8 Playwright spec"
  Remove-Item -LiteralPath $playwrightLog -Force -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $annotationFile -Force -ErrorAction SilentlyContinue
  Push-Location $frontendDir
  $previousErrorAction = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  $playwrightExit = 0
  try {
    & pnpm exec playwright test e2e/p8.spec.ts --reporter=line *> $playwrightLog
    $playwrightExit = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $previousErrorAction
    Pop-Location
  }
  if (Test-Path -LiteralPath $playwrightLog) {
    Get-Content -LiteralPath $playwrightLog | ForEach-Object { Write-Host $_ }
  }
  if ($playwrightExit -ne 0) {
    throw "P8 browser E2E failed (Playwright exit code $playwrightExit)"
  }
} catch {
  $exitCode = 1
  $failureMessage = $_.Exception.Message
  Write-Host ""
  Write-Host "P8 E2E FAILED: $failureMessage"
  Write-LogTail $playwrightLog "Playwright"
  Write-LogTail $apiErr "API stderr"
  Write-LogTail $webErr "Web stderr"
} finally {
  $wallSeconds = [int]((Get-Date) - $startedAt).TotalSeconds

  $logText = ""
  if (Test-Path -LiteralPath $playwrightLog) {
    $logText = [System.IO.File]::ReadAllText($playwrightLog)
    if ($null -eq $logText) { $logText = "" }
  }
  $passedMatch = [regex]::Match($logText, "(\d+)\s+passed")
  if ($passedMatch.Success) { $passed = [int]$passedMatch.Groups[1].Value }
  $failedMatch = [regex]::Match($logText, "(\d+)\s+failed")
  if ($failedMatch.Success) { $failed = [int]$failedMatch.Groups[1].Value }
  $skippedMatch = [regex]::Match($logText, "(\d+)\s+skipped")
  if ($skippedMatch.Success) { $skipped = [int]$skippedMatch.Groups[1].Value }

  Copy-Screenshots
  $shotCount = @(Get-ChildItem -Path $docsShots -Filter "p8-e2e-*.png" -File -ErrorAction SilentlyContinue).Count
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
  Remove-Item Env:EZTEST_E2E_P8_SCREENSHOT_DIR -ErrorAction SilentlyContinue
  Remove-Item Env:FORCE_COLOR -ErrorAction SilentlyContinue

  if ($exitCode -eq 0) {
    Write-Host ""
    Write-Host "==> P8 E2E PASSED in ${wallSeconds}s; $shotCount screenshots copied to $docsShots"
    Write-Host "==> Summary: $(Join-Path $docsDir 'e2e.md')"
  } else {
    Write-Host ""
    Write-Host "==> P8 E2E FAILED in ${wallSeconds}s; evidence in $evidence"
  }
}

exit $exitCode
