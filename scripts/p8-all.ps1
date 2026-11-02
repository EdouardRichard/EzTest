#requires -Version 5.1
<#
P8 aggregate acceptance driver.

Runs the P8 acceptance in the evidence order:
  1. p7-acceptance.ps1  (P9 five items on a fresh p6 -> p7 clone)
  2. clone p7 -> p8
  3. p8-acceptance.ps1  (S0-S6 full journey: real LLM + real Chrome + two SUTs)
  4. p8-resilience.ps1  (S9 long context/execution, S10 hardening targets)
  5. p8-multi.ps1       (S8 dual replica / rolling / real executor loss)
  6. p8-capability.ps1  (S11 37-row matrix smoke)
  7. p8-e2e.ps1         (S12 browser E2E + screenshots)
  8. p8-static-check.ps1 (S13 docs/contract/compose/CI)

Each step is recorded in .tmp/p8-acceptance/all-summary.txt with its exit code
and log path. A failing step does not stop later independent steps, but the
aggregate exit code is non-zero.

Usage:
  powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-all.ps1
  powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-all.ps1 -SkipP7
  powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-all.ps1 -Only acceptance,multi
#>
[CmdletBinding()]
param(
  [string[]]$Only = @(),
  [switch]$SkipP7,
  [switch]$SkipStatic,
  [switch]$SkipE2E,
  [switch]$SkipMulti,
  [switch]$SkipResilience,
  [switch]$SkipCapability
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$evidence = Join-Path $repoRoot ".tmp\p8-acceptance"
New-Item -ItemType Directory -Force -Path $evidence | Out-Null
$summaryPath = Join-Path $evidence "all-summary.txt"
$startedAt = Get-Date

function Want-Step {
  param([string]$Name)
  if ($Only.Count -gt 0) { return $Only -contains $Name }
  switch ($Name) {
    "p7"         { return -not $SkipP7 }
    "acceptance" { return $true }
    "resilience" { return -not $SkipResilience }
    "multi"      { return -not $SkipMulti }
    "capability" { return -not $SkipCapability }
    "e2e"        { return -not $SkipE2E }
    "static"     { return -not $SkipStatic }
    default      { return $false }
  }
}

$rows = New-Object System.Collections.Generic.List[object]
function Run-Step {
  param([string]$Name, [string]$Script, [string[]]$Arguments = @())
  if (-not (Want-Step $Name)) {
    $rows.Add([pscustomobject]@{ Step = $Name; Exit = "SKIP"; Log = "" })
    return
  }
  $log = Join-Path $evidence ("{0}.log" -f $Name)
  $console = Join-Path $evidence ("{0}.console.log" -f $Name)
  $started = Get-Date
  Write-Host ("== {0} ({1}) ==" -f $Name, $Script)
  $arguments = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $repoRoot $Script)) + $Arguments
  & powershell.exe @arguments 2>&1 | Tee-Object -FilePath $log | Tee-Object -FilePath $console -Append
  $code = $LASTEXITCODE
  $duration = [int]((Get-Date) - $started).TotalSeconds
  $rows.Add([pscustomobject]@{ Step = $Name; Exit = $code; Log = $log; Seconds = $duration })
  Write-Host ("== {0}: exit {1} in {2}s ==" -f $Name, $code, $duration)
}

# 1. P9 regression on a fresh p7 clone.
Run-Step "p7" "scripts\p7-acceptance.ps1"

# 2. Clone p7 -> p8 (p7-acceptance reset the template when it ran).
if (Want-Step "acceptance") {
  $envFile = Join-Path $repoRoot ".env"
  $databaseUrl = $env:EZTEST_DATABASE_URL
  if (-not $databaseUrl -and (Test-Path $envFile)) {
    foreach ($line in Get-Content $envFile) {
      if ($line -match '^\s*EZTEST_DATABASE_URL\s*=\s*(.+)$') { $databaseUrl = $Matches[1].Trim() }
    }
  }
  if (-not $databaseUrl) { throw "EZTEST_DATABASE_URL is required to clone the p7 database" }
  $psql = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
  if (-not (Test-Path $psql)) { $psql = "psql" }
  $cloneSql = Join-Path $evidence "clone-p8.sql"
  @"
DROP DATABASE IF EXISTS eztest_v02_p8 WITH (FORCE);
CREATE DATABASE eztest_v02_p8 TEMPLATE eztest_v02_p7;
"@ | Set-Content -Path $cloneSql -Encoding ASCII
  $adminUrl = $databaseUrl -replace '/eztest_v02[^/?]*(\?|$)', '/postgres$1'
  Write-Host "== clone eztest_v02_p7 -> eztest_v02_p8 =="
  & $psql $adminUrl -v ON_ERROR_STOP=1 -f $cloneSql 2>&1 | Tee-Object -FilePath (Join-Path $evidence "clone.log")
  if ($LASTEXITCODE -ne 0) { throw "p8 database clone failed" }
  $rows.Add([pscustomobject]@{ Step = "clone-p8"; Exit = 0; Log = (Join-Path $evidence "clone.log") })
}

Run-Step "acceptance" "scripts\p8-acceptance.ps1"
Run-Step "resilience" "scripts\p8-resilience.ps1"
Run-Step "multi" "scripts\p8-multi.ps1"

# Capability needs a live API on 13333; acceptance/resilience/multi stop their
# own stacks. Start a temporary API from the acceptance binary for this smoke.
$capabilityApi = $null
if (Want-Step "capability") {
  $healthy = $false
  try {
    $probe = Invoke-RestMethod -Uri "http://127.0.0.1:13333/api/health" -TimeoutSec 3
    $healthy = $probe.data.status -eq "ok"
  } catch { }
  if (-not $healthy) {
    $binary = Join-Path $evidence "eztest-p8.exe"
    if (-not (Test-Path $binary)) { throw "capability API binary not found: $binary" }
    $dbUrl = $databaseUrl
    $envFile = Join-Path $repoRoot ".env"
    if (-not $dbUrl -and (Test-Path $envFile)) {
      foreach ($line in Get-Content $envFile) {
        if ($line -match '^\s*EZTEST_DATABASE_URL\s*=\s*(.+)$') { $dbUrl = $Matches[1].Trim() }
      }
    }
    if (-not $dbUrl) { throw "EZTEST_DATABASE_URL is required for the capability API" }
    $dbUrl = $dbUrl -replace '/eztest_v02[^/?]*(\?|$)', '/eztest_v02_p8$1'
    $previous = @{
      EZTEST_DATABASE_URL = $env:EZTEST_DATABASE_URL
      EZTEST_API_PORT     = $env:EZTEST_API_PORT
      EZTEST_WEB_PORT     = $env:EZTEST_WEB_PORT
      EZTEST_EXECUTOR_PORT = $env:EZTEST_EXECUTOR_PORT
    }
    $env:EZTEST_DATABASE_URL = $dbUrl
    $env:EZTEST_API_PORT = "13333"
    $env:EZTEST_WEB_PORT = "6868"
    $env:EZTEST_EXECUTOR_PORT = "13345"
    try {
      $capabilityApi = Start-Process -FilePath $binary -PassThru -NoNewWindow `
        -RedirectStandardOutput (Join-Path $evidence "capability-api.log") `
        -RedirectStandardError (Join-Path $evidence "capability-api.err.log")
    } finally {
      foreach ($key in $previous.Keys) { Set-Item -Path ("Env:" + $key) -Value $previous[$key] }
    }
    $deadline = (Get-Date).AddSeconds(120)
    $healthy = $false
    while ((Get-Date) -lt $deadline) {
      try {
        $probe = Invoke-RestMethod -Uri "http://127.0.0.1:13333/api/health" -TimeoutSec 3
        if ($probe.data.status -eq "ok") { $healthy = $true; break }
      } catch { }
      Start-Sleep -Milliseconds 500
    }
    if (-not $healthy) { throw "capability API did not become healthy on 13333" }
  }
}

try {
  Run-Step "capability" "scripts\p8-capability.ps1"
} finally {
  if ($null -ne $capabilityApi -and -not $capabilityApi.HasExited) {
    Stop-Process -Id $capabilityApi.Id -Force -ErrorAction SilentlyContinue
  }
}
Run-Step "e2e" "scripts\p8-e2e.ps1"
Run-Step "static" "scripts\p8-static-check.ps1"

$finishedAt = Get-Date
$failed = @($rows | Where-Object { $_.Exit -ne "SKIP" -and $_.Exit -ne 0 })
$header = @(
  "P8 acceptance aggregate",
  ("started:  {0}" -f $startedAt.ToString("yyyy-MM-dd HH:mm:ss zzz")),
  ("finished: {0}" -f $finishedAt.ToString("yyyy-MM-dd HH:mm:ss zzz")),
  ("wall:     {0}s" -f [int]($finishedAt - $startedAt).TotalSeconds),
  ("result:   {0}" -f $(if ($failed.Count -eq 0) { "PASS" } else { "FAIL" })),
  ""
)
$table = $rows | Format-Table Step, Exit, Seconds, Log -AutoSize | Out-String -Width 240
$header + $table | Set-Content -Path $summaryPath -Encoding UTF8
Get-Content $summaryPath

if ($failed.Count -gt 0) {
  Write-Error ("p8-all failed: {0}" -f (($failed | ForEach-Object { $_.Step }) -join ", "))
  exit 1
}
Write-Host "P8 ALL PASSED"
exit 0
