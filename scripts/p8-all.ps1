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
Run-Step "capability" "scripts\p8-capability.ps1"
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
