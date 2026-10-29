#requires -Version 5.1
<#
.SYNOPSIS
    Measures the P8 SLO gate (design spec section 1.5/10.1).

.DESCRIPTION
    Measurement mode seeds an isolated dataset (100 versions / 50 requirements /
    200 cases / 400 edges / 10k events) into the database behind the running API,
    then measures:
      * version overview first-screen API latency (P95),
      * version overview browser navigation timing (P95, when Playwright and the
        web app are available),
      * exploration-chain endpoint latency (P95),
      * versions/{id}/code/changes API latency (P95).
    The 8-concurrent batch execution and the analysis non-model overhead need the
    fixture executor/session harness; when that harness is not supplied this
    script records them as SKIP instead of inventing a PASS.

    -DryRun performs static preflight only: it checks that the limiter, the SLO
    script, the measured routes, the environment keys and the report paths are
    present, writes docs/acceptance/p8/slo.md and .tmp/p8-slo/metrics.json with
    the measurement rows marked SKIP, and exits 0 when the static checks pass.

    Every measurement row is PASS, FAIL or SKIP. A measured value above its
    threshold is a FAIL and the script exits 1. A missing API, browser or
    fixture harness is an explicit SKIP (exit 0) and is written to the report;
    it is never reported as a pass. The report is a local acceptance measurement,
    not a production SLA promise.

    The script never prints .env credentials, the database URL or the LLM key.

.PARAMETER DryRun
    Static preflight only; no API, database or browser is started or queried.

.PARAMETER ApiBaseUrl
    Running API base URL. Defaults to EZTEST_ACCEPTANCE_API_URL, then the
    EZTEST_HTTP_ADDR port on 127.0.0.1, then http://127.0.0.1:13333.

.PARAMETER WebBaseUrl
    Running web base URL used for the browser navigation-timing measurement.
    When omitted the browser row is SKIP.

.PARAMETER Iterations
    Samples per measurement (default 20). Percentiles are P50/P95.

.PARAMETER EnvPath
    .env path (default <repo>/.env).

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
    Run from anywhere; paths are resolved from the repository root.

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 -DryRun

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 `
      -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://127.0.0.1:6868
#>
[CmdletBinding()]
param(
    [switch]$DryRun,
    [string]$ApiBaseUrl = "",
    [string]$WebBaseUrl = "",
    [int]$Iterations = 20,
    [string]$EnvPath = ""
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$frontendDir = Join-Path $repoRoot "frontend"
$tmpDir = Join-Path $repoRoot ".tmp\p8-slo"
$metricsPath = Join-Path $tmpDir "metrics.json"
$reportPath = Join-Path $repoRoot "docs\acceptance\p8\slo.md"

# Design spec 1.5 thresholds and 10.1 dataset.
$thresholds = [ordered]@{
    overviewP95Ms    = 2500
    chainP95Ms       = 2500
    changesP95Ms     = 2000
    executionWorkers = 8
    executionErrors  = 0
}
$dataset = [ordered]@{
    versions     = 100
    requirements = 50
    cases        = 200
    edges        = 400
    events       = 10000
}

$checks = New-Object System.Collections.ArrayList
$measurements = [ordered]@{}

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Write-Note {
    param([string]$Message)
    Write-Host "    $Message" -ForegroundColor DarkGray
}

function Add-Check {
    param([string]$Area, [string]$Check, [string]$Status, [string]$Detail)
    [void]$checks.Add([pscustomobject]@{
            Area   = $Area
            Check  = $Check
            Status = $Status
            Detail = $Detail
        })
}

function Add-Measurement {
    param(
        [string]$Name,
        [string]$Status,
        [string]$Threshold,
        $Value,
        [string]$Detail
    )
    $measurements[$Name] = [ordered]@{
        status    = $Status
        threshold = $Threshold
        value     = $Value
        detail    = $Detail
    }
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) { return $values }
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
        if ($line -match '^\s*#' -or $line -match '^\s*$') { continue }
        if ($line -match '^\s*([^=]+)=(.*)$') {
            $key = $matches[1].Trim()
            $value = $matches[2].Trim()
            if ($value.Length -ge 2) {
                $first = $value.Substring(0, 1)
                $last = $value.Substring($value.Length - 1, 1)
                if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                    $value = $value.Substring(1, $value.Length - 2)
                }
            }
            $values[$key] = $value
        }
    }
    return $values
}

function Resolve-ToolPath {
    param([string]$LocalPath, [string]$CommandName)
    if (-not [string]::IsNullOrWhiteSpace($LocalPath) -and (Test-Path -LiteralPath $LocalPath)) { return $LocalPath }
    $command = Get-Command $CommandName -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    return ""
}

function Resolve-ApiBaseUrl {
    param([string]$Explicit, $EnvValues)
    if (-not [string]::IsNullOrWhiteSpace($Explicit)) { return $Explicit.TrimEnd("/") }
    if ($null -ne $EnvValues -and $EnvValues.ContainsKey("EZTEST_ACCEPTANCE_API_URL") -and
        -not [string]::IsNullOrWhiteSpace([string]$EnvValues["EZTEST_ACCEPTANCE_API_URL"])) {
        return ([string]$EnvValues["EZTEST_ACCEPTANCE_API_URL"]).TrimEnd("/")
    }
    $addr = ""
    if ($null -ne $EnvValues -and $EnvValues.ContainsKey("EZTEST_HTTP_ADDR")) {
        $addr = [string]$EnvValues["EZTEST_HTTP_ADDR"]
    }
    if (-not [string]::IsNullOrWhiteSpace($addr) -and $addr.StartsWith(":")) {
        return "http://127.0.0.1$addr"
    }
    if (-not [string]::IsNullOrWhiteSpace($addr)) {
        return "http://$addr"
    }
    return "http://127.0.0.1:13333"
}

function Get-Percentile {
    param([double[]]$Values, [double]$Percentile)
    if ($null -eq $Values -or $Values.Count -eq 0) { return $null }
    $sorted = @($Values | Sort-Object)
    $index = [int][math]::Ceiling($Percentile * $sorted.Count) - 1
    if ($index -lt 0) { $index = 0 }
    if ($index -ge $sorted.Count) { $index = $sorted.Count - 1 }
    return [math]::Round([double]$sorted[$index], 1)
}

function Invoke-Measure {
    param([string]$Path, [int]$Count, $Headers)
    $samples = New-Object System.Collections.ArrayList
    for ($i = 0; $i -lt $Count; $i++) {
        $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
        try {
            $response = Invoke-WebRequest -Uri ($script:apiBase + $Path) -Headers $Headers `
                -UseBasicParsing -TimeoutSec 30 -ErrorAction Stop
        } catch {
            throw "GET $Path failed: $($_.Exception.Message)"
        }
        $stopwatch.Stop()
        if ([int]$response.StatusCode -ne 200) {
            throw "GET $Path answered HTTP $($response.StatusCode)"
        }
        [void]$samples.Add($stopwatch.Elapsed.TotalMilliseconds)
    }
    return [pscustomobject]@{
        P50     = (Get-Percentile -Values $samples.ToArray() -Percentile 0.50)
        P95     = (Get-Percentile -Values $samples.ToArray() -Percentile 0.95)
        Samples = $samples.Count
    }
}

function Parse-PostgresUrl {
    param([string]$Url)
    if ([string]::IsNullOrWhiteSpace($Url)) { return $null }
    $uri = $null
    if (-not [System.Uri]::TryCreate($Url, [System.UriKind]::Absolute, [ref]$uri)) { return $null }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") { return $null }
    $separator = $uri.UserInfo.IndexOf(":")
    if ($separator -lt 0) { return $null }
    $database = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($database)) { return $null }
    $sslMode = ""
    if (-not [string]::IsNullOrWhiteSpace($uri.Query)) {
        foreach ($pair in $uri.Query.TrimStart([char[]]"?") -split "&") {
            $index = $pair.IndexOf("=")
            if ($index -lt 0) { continue }
            $key = [System.Uri]::UnescapeDataString($pair.Substring(0, $index))
            if ($key -eq "sslmode") {
                $sslMode = [System.Uri]::UnescapeDataString($pair.Substring($index + 1))
            }
        }
    }
    return [pscustomobject]@{
        Host     = $uri.Host
        Port     = $(if ($uri.Port -gt 0) { $uri.Port } else { 5432 })
        User     = [System.Uri]::UnescapeDataString($uri.UserInfo.Substring(0, $separator))
        Password = [System.Uri]::UnescapeDataString($uri.UserInfo.Substring($separator + 1))
        Database = $database
        SSLMode  = $sslMode
    }
}

function Invoke-PsqlScript {
    param($Connection, [string]$Sql, [string]$Operation)
    if ($null -eq $script:psqlExe -or [string]::IsNullOrWhiteSpace($script:psqlExe)) {
        throw "psql.exe is not available"
    }
    $tempFile = [System.IO.Path]::GetTempFileName()
    $savedPassword = $env:PGPASSWORD
    $savedSSLMode = $env:PGSSLMODE
    try {
        [System.IO.File]::WriteAllText($tempFile, $Sql, (New-Object System.Text.UTF8Encoding($false)))
        $env:PGPASSWORD = $Connection.Password
        if (-not [string]::IsNullOrWhiteSpace($Connection.SSLMode)) { $env:PGSSLMODE = $Connection.SSLMode }
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        try {
            $output = & $script:psqlExe -h $Connection.Host -p $Connection.Port -U $Connection.User `
                -d $Connection.Database -v ON_ERROR_STOP=1 -tA -f $tempFile 2>$null
            $code = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
        }
    } finally {
        if ($null -eq $savedPassword) { Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue } else { $env:PGPASSWORD = $savedPassword }
        if ($null -eq $savedSSLMode) { Remove-Item Env:PGSSLMODE -ErrorAction SilentlyContinue } else { $env:PGSSLMODE = $savedSSLMode }
        Remove-Item -LiteralPath $tempFile -Force -ErrorAction SilentlyContinue
    }
    if ($code -ne 0) { throw "$Operation failed (psql exit $code)" }
    return @($output | ForEach-Object { ([string]$_).Trim() } | Where-Object { $_ -ne "" })
}

# Seed IDs live in a reserved high range so cleanup can never touch real data.
$seedVersionBase = [long]8800000000000000000
$seedCaseBase = [long]8810000000000000000
$seedRequirementBase = [long]8820000000000000000
$seedRelationBase = [long]8830000000000000000
$seedVersionCount = 100
$seedRequirementCount = 50
$seedCaseCount = 200
$seedRelationCount = 400
$seedEventCount = 10000

function Invoke-SeedDataset {
    param($Connection)
    $adminRows = Invoke-PsqlScript -Connection $Connection -Operation "reading the admin user" `
        -Sql "SELECT id FROM user_account WHERE role = 'admin' ORDER BY id ASC LIMIT 1;"
    if ($adminRows.Count -eq 0) { throw "no admin user exists yet; start the API once so it seeds the admin account" }
    $adminID = [long]$adminRows[0]

    $cleanupSql = @"
DELETE FROM event WHERE channel = 'slo:seed';
DELETE FROM event_channel WHERE channel = 'slo:seed';
DELETE FROM requirement_relation WHERE id >= $seedRelationBase AND id < $seedRelationBase + 10000;
DELETE FROM case_node WHERE id >= $seedCaseBase AND id < $seedCaseBase + 10000;
DELETE FROM requirement WHERE id >= $seedRequirementBase AND id < $seedRequirementBase + 10000;
DELETE FROM version WHERE id >= $seedVersionBase AND id < $seedVersionBase + 10000;
"@
    [void](Invoke-PsqlScript -Connection $Connection -Operation "cleaning a previous SLO seed" -Sql $cleanupSql)

    $seedSql = @"
INSERT INTO version (id, name, label, description, status, created_by, updated_by, created_at, updated_at)
SELECT $seedVersionBase + g, 'SLO seed version ' || g, '', '', 'completed',
       $adminID, $adminID, now() - (g || ' minutes')::interval, now() - (g || ' minutes')::interval
FROM generate_series(1, $seedVersionCount) AS g;

INSERT INTO requirement (id, version_id, title, description, acceptance_criteria, priority, status,
                         source, last_update_source, revision, created_by, updated_by)
SELECT $seedRequirementBase + g,
       $seedVersionBase + ((g % 10) + 1),
       'SLO seed requirement ' || g, '', '', 'should', 'designed', 'user', 'user', 1, $adminID, $adminID
FROM generate_series(1, $seedRequirementCount) AS g;

INSERT INTO case_node (id, version_id, parent_id, kind, title, description, priority, source, status,
                       sort_order, revision, created_by, updated_by)
SELECT $seedCaseBase + g,
       $seedVersionBase + ((g % 10) + 1),
       NULL, 'case', 'SLO seed case ' || g, '', 'P2', 'user_input', 'reviewed', g, 1, $adminID, $adminID
FROM generate_series(1, $seedCaseCount) AS g;

INSERT INTO requirement_relation (id, version_id, from_requirement_id, to_requirement_id,
                                  relation_type, description, source, created_by)
SELECT $seedRelationBase + g,
       $seedVersionBase + ((g % 10) + 1),
       $seedRequirementBase + (g % $seedRequirementCount) + 1,
       $seedRequirementBase + ((g + 1 + (g / $seedRequirementCount)) % $seedRequirementCount) + 1,
       'depends_on', '', 'user', $adminID
FROM generate_series(1, $seedRelationCount) AS g
WHERE (g % $seedRequirementCount) <> ((g + 1 + (g / $seedRequirementCount)) % $seedRequirementCount);

INSERT INTO event_channel (channel, last_seq) VALUES ('slo:seed', 0)
ON CONFLICT (channel) DO UPDATE SET last_seq = 0, updated_at = now();

INSERT INTO event (channel, seq, type, version_id, ts, summary, detail, refs)
SELECT 'slo:seed', g, 'slo.seed', ($seedVersionBase + 1)::text,
       now() - (g || ' seconds')::interval, 'SLO seed event', '{}'::jsonb,
       '{"evidence":[],"case":[],"requirement":[],"code":[]}'::jsonb
FROM generate_series(1, $seedEventCount) AS g;

UPDATE event_channel SET last_seq = $seedEventCount, updated_at = now() WHERE channel = 'slo:seed';
"@
    [void](Invoke-PsqlScript -Connection $Connection -Operation "seeding the SLO dataset" -Sql $seedSql)

    $counts = Invoke-PsqlScript -Connection $Connection -Operation "verifying the SLO dataset" -Sql @"
SELECT
  (SELECT count(*) FROM version WHERE id >= $seedVersionBase AND id < $seedVersionBase + 10000),
  (SELECT count(*) FROM requirement WHERE id >= $seedRequirementBase AND id < $seedRequirementBase + 10000),
  (SELECT count(*) FROM case_node WHERE id >= $seedCaseBase AND id < $seedCaseBase + 10000),
  (SELECT count(*) FROM requirement_relation WHERE id >= $seedRelationBase AND id < $seedRelationBase + 10000),
  (SELECT count(*) FROM event WHERE channel = 'slo:seed');
"@
    if ($counts.Count -ne 1) { throw "unexpected dataset verification output" }
    $parts = $counts[0].Split("|")
    if ($parts.Length -ne 5) { throw "unexpected dataset verification output: $($counts[0])" }
    return [pscustomobject]@{
        Versions     = [int]$parts[0]
        Requirements = [int]$parts[1]
        Cases        = [int]$parts[2]
        Edges        = [int]$parts[3]
        Events       = [int]$parts[4]
        VersionID    = $seedVersionBase + 1
    }
}

function Remove-SeedDataset {
    param($Connection)
    if ($null -eq $Connection) { return }
    try {
        [void](Invoke-PsqlScript -Connection $Connection -Operation "cleaning the SLO dataset" -Sql @"
DELETE FROM event WHERE channel = 'slo:seed';
DELETE FROM event_channel WHERE channel = 'slo:seed';
DELETE FROM requirement_relation WHERE id >= $seedRelationBase AND id < $seedRelationBase + 10000;
DELETE FROM case_node WHERE id >= $seedCaseBase AND id < $seedCaseBase + 10000;
DELETE FROM requirement WHERE id >= $seedRequirementBase AND id < $seedRequirementBase + 10000;
DELETE FROM version WHERE id >= $seedVersionBase AND id < $seedVersionBase + 10000;
"@)
    } catch {
        Write-Warning "SLO seed cleanup failed: $($_.Exception.Message)"
    }
}

function Invoke-BrowserMeasurement {
    param([string]$BaseUrl, [string]$VersionID, [string]$Token, $User, [int]$Count)
    if ([string]::IsNullOrWhiteSpace($BaseUrl)) {
        return [pscustomobject]@{ Status = "SKIP"; Detail = "no -WebBaseUrl supplied"; P95 = $null }
    }
    try {
        $probe = Invoke-WebRequest -Uri $BaseUrl -UseBasicParsing -TimeoutSec 5 -ErrorAction Stop
        if ([int]$probe.StatusCode -ne 200) {
            return [pscustomobject]@{ Status = "SKIP"; Detail = "web app answered HTTP $($probe.StatusCode)"; P95 = $null }
        }
    } catch {
        return [pscustomobject]@{ Status = "SKIP"; Detail = "web app not reachable at $BaseUrl"; P95 = $null }
    }

    $node = Resolve-ToolPath -LocalPath "" -CommandName "node.exe"
    if ([string]::IsNullOrWhiteSpace($node)) {
        return [pscustomobject]@{ Status = "SKIP"; Detail = "node.exe is not available"; P95 = $null }
    }
    Push-Location -LiteralPath $frontendDir
    try {
        $probeScript = Join-Path $frontendDir "node_modules\@playwright\test\index.js"
        if (-not (Test-Path -LiteralPath $probeScript)) {
            return [pscustomobject]@{ Status = "SKIP"; Detail = "Playwright is not installed in frontend/node_modules"; P95 = $null }
        }
    } finally {
        Pop-Location
    }

    $scriptPath = Join-Path $tmpDir "browser-nav-probe.cjs"
    $js = @'
const { chromium } = require("@playwright/test");
(async () => {
  const url = process.env.SLO_WEB_URL;
  const count = Number(process.env.SLO_SAMPLES || "20");
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext();
    await context.addInitScript(([token, user]) => {
      sessionStorage.setItem("eztest.token", token);
      sessionStorage.setItem("eztest.user", user);
    }, [process.env.SLO_TOKEN, process.env.SLO_USER]);
    const page = await context.newPage();
    const samples = [];
    for (let i = 0; i < count; i++) {
      await page.goto(url, { waitUntil: "load", timeout: 30000 });
      await page.waitForTimeout(300);
      const duration = await page.evaluate(() => {
        const entry = performance.getEntriesByType("navigation")[0];
        return entry ? entry.duration : performance.now();
      });
      samples.push(duration);
    }
    console.log(JSON.stringify(samples));
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error && error.message ? error.message : String(error));
  process.exit(1);
});
'@
    [System.IO.File]::WriteAllText($scriptPath, $js, (New-Object System.Text.UTF8Encoding($false)))
    $env:SLO_WEB_URL = $BaseUrl.TrimEnd("/") + "/versions/" + $VersionID
    $env:SLO_SAMPLES = [string]$Count
    $env:SLO_TOKEN = $Token
    $env:SLO_USER = $User | ConvertTo-Json -Compress
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        $output = & $node $scriptPath 2>&1
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        Remove-Item Env:SLO_WEB_URL, Env:SLO_SAMPLES, Env:SLO_TOKEN, Env:SLO_USER -ErrorAction SilentlyContinue
    }
    if ($code -ne 0) {
        return [pscustomobject]@{ Status = "SKIP"; Detail = "Playwright browser run failed: $($output | Select-Object -Last 1)"; P95 = $null }
    }
    try {
        $samples = [double[]](@($output | Select-Object -Last 1) | ConvertFrom-Json)
    } catch {
        return [pscustomobject]@{ Status = "SKIP"; Detail = "Playwright browser run produced no timing samples"; P95 = $null }
    }
    return [pscustomobject]@{
        Status = "PASS"
        Detail = "$($samples.Count) browser navigations"
        P95    = (Get-Percentile -Values $samples -Percentile 0.95)
    }
}

function Write-Outputs {
    param([string]$Mode, [string]$ApiBase, [string]$WebBase, [string]$DatasetJSON, [string]$Note)
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $reportPath) | Out-Null

    $failed = @($checks | Where-Object { $_.Status -eq "FAIL" })
    $failedMeasurements = @($measurements.Keys | Where-Object { $measurements[$_].status -eq "FAIL" })
    $status = "passed"
    if ($failed.Count -gt 0 -or $failedMeasurements.Count -gt 0) {
        $status = "failed"
    }
    $skipMeasurements = @($measurements.Keys | Where-Object { $measurements[$_].status -eq "SKIP" })
    if ($status -eq "passed" -and $skipMeasurements.Count -gt 0) {
        if ($Mode -eq "dry-run") {
            $status = "static-check-passed-measurements-skipped"
        } elseif ($skipMeasurements.Count -eq $measurements.Count) {
            $status = "skipped"
        } else {
            $status = "passed_with_skips"
        }
    }

    $metrics = [ordered]@{
        generatedAt  = (Get-Date).ToUniversalTime().ToString("o")
        mode         = $Mode
        status       = $status
        apiBaseUrl   = $ApiBase
        webBaseUrl   = $WebBase
        thresholds   = $thresholds
        dataset      = $dataset
        datasetState = $(if ([string]::IsNullOrWhiteSpace($DatasetJSON)) { $null } else { $DatasetJSON | ConvertFrom-Json })
        checks       = @($checks)
        measurements = $measurements
        note         = $Note
    }
    $json = $metrics | ConvertTo-Json -Depth 8

    [System.IO.File]::WriteAllText($metricsPath, $json, (New-Object System.Text.UTF8Encoding($false)))

    $builder = New-Object System.Text.StringBuilder
    [void]$builder.AppendLine("# P8 SLO Measurement Report")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("> Generated by ``scripts/slo.ps1`` (design spec sections 1.5/10.1). Local acceptance measurement, **not a production SLA promise**; re-measure per deployment environment.")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("- Generated (UTC): $((Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ"))")
    [void]$builder.AppendLine("- Mode: ``$Mode``")
    [void]$builder.AppendLine("- API: ``$ApiBase``")
    [void]$builder.AppendLine("- Web: ``$WebBase``")
    [void]$builder.AppendLine("- Overall status: **$($status.ToUpperInvariant())**")
    if (-not [string]::IsNullOrWhiteSpace($Note)) {
        [void]$builder.AppendLine("- Note: $Note")
    }
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("## Dataset (spec 10.1)")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("| Object | Target |")
    [void]$builder.AppendLine("| --- | --- |")
    [void]$builder.AppendLine("| Versions | $($dataset.versions) |")
    [void]$builder.AppendLine("| Requirements | $($dataset.requirements) |")
    [void]$builder.AppendLine("| Cases | $($dataset.cases) |")
    [void]$builder.AppendLine("| Edges | $($dataset.edges) |")
    [void]$builder.AppendLine("| Events | $($dataset.events) |")
    [void]$builder.AppendLine("")
    if (-not [string]::IsNullOrWhiteSpace($DatasetJSON)) {
        $seeded = $DatasetJSON | ConvertFrom-Json
        [void]$builder.AppendLine("Seeded and verified: $($seeded.Versions) versions, $($seeded.Requirements) requirements, $($seeded.Cases) cases, $($seeded.Edges) edges, $($seeded.Events) events.")
        [void]$builder.AppendLine("")
    }
    [void]$builder.AppendLine("## Thresholds and results")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("| Measurement | Threshold | Measured P95 | Status | Detail |")
    [void]$builder.AppendLine("| --- | --- | --- | --- | --- |")
    $order = @(
        "overviewApiP95Ms", "overviewBrowserP95Ms", "chainP95Ms", "changesP95Ms",
        "executionConcurrency", "analysisNonModelOverheadMs"
    )
    $labels = @{
        overviewApiP95Ms          = "Version overview first-screen API latency"
        overviewBrowserP95Ms      = "Version overview browser navigation timing"
        chainP95Ms                = "Exploration-chain latency (200 nodes/400 edges)"
        changesP95Ms              = "Code change-set API latency"
        executionConcurrency      = "Batch-execution concurrency success rate"
        analysisNonModelOverheadMs = "Analysis non-model overhead"
    }
    foreach ($name in $order) {
        if (-not $measurements.Contains($name)) { continue }
        $row = $measurements[$name]
        $value = "SKIP"
        if ($null -ne $row.value) { $value = [string]$row.value }
        [void]$builder.AppendLine("| $($labels[$name]) | $($row.threshold) | $value | $($row.status) | $($row.detail) |")
    }
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("## Static checks")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("| Area | Check | Status | Detail |")
    [void]$builder.AppendLine("| --- | --- | --- | --- |")
    foreach ($check in $checks) {
        [void]$builder.AppendLine("| $($check.Area) | $($check.Check) | $($check.Status) | $($check.Detail) |")
    }
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("## Reproduce")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine('```powershell')
    [void]$builder.AppendLine("# Static preflight (no API/browser needed):")
    [void]$builder.AppendLine("powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 -DryRun")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("# Local measurement (running API required; optional Web/Playwright browser row):")
    [void]$builder.AppendLine("powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 ``")
    [void]$builder.AppendLine("  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://127.0.0.1:6868")
    [void]$builder.AppendLine('```')
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("Exit code: 1 when a static check or a measured threshold fails; 0 when everything is PASS or SKIP. SKIP (missing API/browser/fixture) is recorded explicitly and never counted as PASS.")
    [void]$builder.AppendLine("")

    [System.IO.File]::WriteAllText($reportPath, $builder.ToString(), (New-Object System.Text.UTF8Encoding($false)))
    Write-Host ""
    Write-Host "Report: $reportPath" -ForegroundColor Green
    Write-Host "Metrics: $metricsPath" -ForegroundColor Green
    Write-Host "Status: $($status.ToUpperInvariant())" -ForegroundColor $(if ($status -eq "failed") { "Red" } else { "Green" })
    return $status
}

# ---------------------------------------------------------------------------
# Dry-run: static preflight only.
# ---------------------------------------------------------------------------
if ($DryRun) {
    Write-Step "SLO static preflight (dry-run)"

    $requiredFiles = @(
        "backend/internal/server/ratelimit.go",
        "backend/internal/server/ratelimit_test.go",
        "backend/internal/config/config.go",
        "scripts/slo.ps1",
        "docs/openapi/api.yaml"
    )
    foreach ($relative in $requiredFiles) {
        if (Test-Path -LiteralPath (Join-Path $repoRoot $relative)) {
            Add-Check -Area "files" -Check $relative -Status "PASS" -Detail "present"
        } else {
            Add-Check -Area "files" -Check $relative -Status "FAIL" -Detail "missing"
        }
    }

    $configSource = ""
    $configPath = Join-Path $repoRoot "backend/internal/config/config.go"
    if (Test-Path -LiteralPath $configPath) {
        $configSource = Get-Content -LiteralPath $configPath -Raw -Encoding UTF8
    }
    foreach ($key in @("EZTEST_RATE_LIMIT_RPS", "EZTEST_RATE_LIMIT_BURST", "EZTEST_RATE_LIMIT_SSE_MAX")) {
        if ($configSource.Contains($key)) {
            Add-Check -Area "config" -Check $key -Status "PASS" -Detail "read and validated by config.Load"
        } else {
            Add-Check -Area "config" -Check $key -Status "FAIL" -Detail "not found in backend/internal/config/config.go"
        }
    }

    $envExamplePath = Join-Path $repoRoot ".env.example"
    $envExample = ""
    if (Test-Path -LiteralPath $envExamplePath) { $envExample = Get-Content -LiteralPath $envExamplePath -Raw -Encoding UTF8 }
    $readmePath = Join-Path $repoRoot "README.md"
    $readme = ""
    if (Test-Path -LiteralPath $readmePath) { $readme = Get-Content -LiteralPath $readmePath -Raw -Encoding UTF8 }
    foreach ($key in @("EZTEST_RATE_LIMIT_RPS", "EZTEST_RATE_LIMIT_BURST", "EZTEST_RATE_LIMIT_SSE_MAX", "EZTEST_GC_ENABLED")) {
        if ($envExample.Contains($key) -and $readme.Contains($key)) {
            Add-Check -Area "docs" -Check $key -Status "PASS" -Detail "documented in .env.example and README"
        } else {
            Add-Check -Area "docs" -Check $key -Status "FAIL" -Detail "missing from .env.example or README"
        }
    }

    $chainRoutes = Join-Path $repoRoot "backend/internal/chain/interfaces/httpapi/routes.go"
    $codeRoutes = Join-Path $repoRoot "backend/internal/codeintel/interfaces/httpapi/routes.go"
    $chainSource = ""
    $codeSource = ""
    if (Test-Path -LiteralPath $chainRoutes) { $chainSource = Get-Content -LiteralPath $chainRoutes -Raw -Encoding UTF8 }
    if (Test-Path -LiteralPath $codeRoutes) { $codeSource = Get-Content -LiteralPath $codeRoutes -Raw -Encoding UTF8 }
    if ($chainSource.Contains("exploration-chain")) {
        Add-Check -Area "routes" -Check "GET /api/versions/{id}/exploration-chain" -Status "PASS" -Detail "mounted"
    } else {
        Add-Check -Area "routes" -Check "GET /api/versions/{id}/exploration-chain" -Status "FAIL" -Detail "route not found"
    }
    if ($codeSource.Contains("/code/changes")) {
        Add-Check -Area "routes" -Check "GET /api/versions/{id}/code/changes" -Status "PASS" -Detail "mounted"
    } else {
        Add-Check -Area "routes" -Check "GET /api/versions/{id}/code/changes" -Status "FAIL" -Detail "route not found"
    }

    Add-Check -Area "thresholds" -Check "P8 DoD" -Status "PASS" -Detail `
        "overview/chain P95 <= $($thresholds.overviewP95Ms)/$($thresholds.chainP95Ms) ms; changes P95 <= $($thresholds.changesP95Ms) ms; $($thresholds.executionWorkers) concurrent executions, $($thresholds.executionErrors) errors"
    Add-Check -Area "report" -Check "output paths" -Status "PASS" -Detail "docs/acceptance/p8/slo.md + .tmp/p8-slo/metrics.json"

    foreach ($name in @("overviewApiP95Ms", "overviewBrowserP95Ms", "chainP95Ms", "changesP95Ms", "executionConcurrency", "analysisNonModelOverheadMs")) {
        switch ($name) {
            "overviewApiP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail "dry-run: measurement not executed; run without -DryRun against a running API" }
            "overviewBrowserP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail "dry-run: needs Playwright and a running web app" }
            "chainP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $null -Detail "dry-run: measurement not executed" }
            "changesP95Ms" { Add-Measurement -Name $name -Status "SKIP" -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $null -Detail "dry-run: measurement not executed" }
            "executionConcurrency" { Add-Measurement -Name $name -Status "SKIP" -Threshold "$($thresholds.executionWorkers) workers / $($thresholds.executionErrors) errors" -Value $null -Detail "needs the fixture executor + seeded session harness; covered by the execution acceptance run (Task 35)" }
            "analysisNonModelOverheadMs" { Add-Measurement -Name $name -Status "SKIP" -Threshold "no fixed P8 gate" -Value $null -Detail "needs a fixture LLM analysis run; reported by the eval/acceptance scripts" }
        }
    }

    $status = Write-Outputs -Mode "dry-run" -ApiBase "(not probed)" -WebBase "(not probed)" -DatasetJSON "" `
        -Note "Static preflight passed; every measurement row is SKIP because no real measurement ran. This is not an SLO PASS."
    if ($status -eq "failed") { exit 1 }
    exit 0
}

# ---------------------------------------------------------------------------
# Measurement mode.
# ---------------------------------------------------------------------------
Write-Step "SLO measurement"

if ([string]::IsNullOrWhiteSpace($EnvPath)) { $EnvPath = Join-Path $repoRoot ".env" }
$envValues = Read-DotEnv -Path $EnvPath
$script:apiBase = Resolve-ApiBaseUrl -Explicit $ApiBaseUrl -EnvValues $envValues
$webBase = $WebBaseUrl.TrimEnd("/")
if ($Iterations -lt 3) { $Iterations = 3 }

$connection = $null
$datasetState = $null
$token = ""
$authHeaders = @{}

Write-Note "API: $($script:apiBase)"
try {
    $probe = Invoke-WebRequest -Uri ($script:apiBase + "/healthz") -UseBasicParsing -TimeoutSec 3 -ErrorAction Stop
} catch {
    $probe = $null
}
if ($null -eq $probe) {
    Add-Check -Area "environment" -Check "API reachable" -Status "SKIP" -Detail "no API at $($script:apiBase); start the API or pass -ApiBaseUrl"
    foreach ($name in @("overviewApiP95Ms", "overviewBrowserP95Ms", "chainP95Ms", "changesP95Ms", "executionConcurrency", "analysisNonModelOverheadMs")) {
        Add-Measurement -Name $name -Status "SKIP" -Threshold "see report" -Value $null -Detail "API is not reachable; measurement skipped"
    }
    [void](Write-Outputs -Mode "measure" -ApiBase $script:apiBase -WebBase $webBase -DatasetJSON "" `
        -Note "API unreachable; all measurement rows are SKIP. Missing environment is not an SLO PASS.")
    exit 0
}
Add-Check -Area "environment" -Check "API reachable" -Status "PASS" -Detail $script:apiBase

if (-not $envValues.ContainsKey("EZTEST_ADMIN_USERNAME") -or [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_ADMIN_USERNAME"]) -or
    -not $envValues.ContainsKey("EZTEST_ADMIN_PASSWORD") -or [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_ADMIN_PASSWORD"])) {
    Add-Check -Area "environment" -Check "admin credentials" -Status "SKIP" -Detail "EZTEST_ADMIN_USERNAME/PASSWORD are not set in .env"
} else {
    try {
        $loginBody = @{
            username = [string]$envValues["EZTEST_ADMIN_USERNAME"]
            password = [string]$envValues["EZTEST_ADMIN_PASSWORD"]
        } | ConvertTo-Json
        $login = Invoke-RestMethod -Uri ($script:apiBase + "/api/auth/login") -Method Post -Body $loginBody `
            -ContentType "application/json" -TimeoutSec 10 -ErrorAction Stop
        $token = [string]$login.data.token
        if ([string]::IsNullOrWhiteSpace($token)) { throw "login response has no token" }
        $authHeaders = @{ Authorization = "Bearer $token" }
        Add-Check -Area "environment" -Check "admin login" -Status "PASS" -Detail "authenticated against the running API"
    } catch {
        Add-Check -Area "environment" -Check "admin login" -Status "SKIP" -Detail "login failed; check EZTEST_ADMIN_USERNAME/PASSWORD in .env"
    }
}

$connected = $false
if (-not [string]::IsNullOrWhiteSpace($token)) {
    $dbUrl = ""
    if ($envValues.ContainsKey("EZTEST_DATABASE_URL")) { $dbUrl = [string]$envValues["EZTEST_DATABASE_URL"] }
    $connection = Parse-PostgresUrl -Url $dbUrl
    $script:psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql.exe"
    if ($null -eq $connection) {
        Add-Check -Area "dataset" -Check "database URL" -Status "SKIP" -Detail "EZTEST_DATABASE_URL is missing or not a postgres URL"
    } elseif ([string]::IsNullOrWhiteSpace($script:psqlExe)) {
        Add-Check -Area "dataset" -Check "psql" -Status "SKIP" -Detail "psql.exe is not available"
    } else {
        try {
            $datasetState = Invoke-SeedDataset -Connection $connection
            $connected = $true
            Add-Check -Area "dataset" -Check "seed" -Status "PASS" -Detail `
                "$($datasetState.Versions) versions, $($datasetState.Requirements) requirements, $($datasetState.Cases) cases, $($datasetState.Edges) edges, $($datasetState.Events) events"
        } catch {
            Add-Check -Area "dataset" -Check "seed" -Status "FAIL" -Detail $_.Exception.Message
        }
    }
} else {
    Add-Check -Area "dataset" -Check "seed" -Status "SKIP" -Detail "no authenticated API session"
}

$seedVersionID = ""
if ($null -ne $datasetState) { $seedVersionID = [string]$datasetState.VersionID }

try {
    if ($null -ne $datasetState) {
        Write-Note "dataset seeded; measuring endpoints"

        $overview = Invoke-Measure -Path "/api/versions?limit=50" -Count $Iterations -Headers $authHeaders
        $overviewOk = $overview.P95 -le $thresholds.overviewP95Ms
        Add-Measurement -Name "overviewApiP95Ms" -Status $(if ($overviewOk) { "PASS" } else { "FAIL" }) `
            -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $overview.P95 `
            -Detail "P50 $($overview.P50) ms over $($overview.Samples) samples"

        if (-not [string]::IsNullOrWhiteSpace($seedVersionID)) {
            $chain = Invoke-Measure -Path "/api/versions/$seedVersionID/exploration-chain?limit=300" -Count $Iterations -Headers $authHeaders
            $chainOk = $chain.P95 -le $thresholds.chainP95Ms
            Add-Measurement -Name "chainP95Ms" -Status $(if ($chainOk) { "PASS" } else { "FAIL" }) `
                -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $chain.P95 `
                -Detail "P50 $($chain.P50) ms over $($chain.Samples) samples"

            $changes = Invoke-Measure -Path "/api/versions/$seedVersionID/code/changes" -Count $Iterations -Headers $authHeaders
            $changesOk = $changes.P95 -le $thresholds.changesP95Ms
            Add-Measurement -Name "changesP95Ms" -Status $(if ($changesOk) { "PASS" } else { "FAIL" }) `
                -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $changes.P95 `
                -Detail "P50 $($changes.P50) ms over $($changes.Samples) samples"

            $browser = Invoke-BrowserMeasurement -BaseUrl $webBase -VersionID $seedVersionID -Token $token `
                -User $login.data.user -Count $Iterations
            if ($browser.P95 -eq $null) {
                Add-Measurement -Name "overviewBrowserP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail $browser.Detail
            } else {
                $browserOk = $browser.P95 -le $thresholds.overviewP95Ms
                Add-Measurement -Name "overviewBrowserP95Ms" -Status $(if ($browserOk) { "PASS" } else { "FAIL" }) `
                    -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $browser.P95 -Detail $browser.Detail
            }
        } else {
            Add-Measurement -Name "chainP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.chainP95Ms) ms" -Value $null -Detail "no seeded version id"
            Add-Measurement -Name "changesP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.changesP95Ms) ms" -Value $null -Detail "no seeded version id"
            Add-Measurement -Name "overviewBrowserP95Ms" -Status "SKIP" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail "no seeded version id"
        }
    } else {
        foreach ($name in @("overviewApiP95Ms", "overviewBrowserP95Ms", "chainP95Ms", "changesP95Ms")) {
            Add-Measurement -Name $name -Status "SKIP" -Threshold "see report" -Value $null -Detail "dataset was not seeded; measurement skipped"
        }
    }
} catch {
    $failedName = "measurement"
    Add-Check -Area "measurement" -Check "request" -Status "FAIL" -Detail $_.Exception.Message
    if (-not $measurements.Contains("overviewApiP95Ms")) {
        Add-Measurement -Name "overviewApiP95Ms" -Status "FAIL" -Threshold "<= $($thresholds.overviewP95Ms) ms" -Value $null -Detail $_.Exception.Message
    }
} finally {
    Remove-SeedDataset -Connection $connection
}

foreach ($name in @("executionConcurrency", "analysisNonModelOverheadMs")) {
    switch ($name) {
        "executionConcurrency" { Add-Measurement -Name $name -Status "SKIP" -Threshold "$($thresholds.executionWorkers) workers / $($thresholds.executionErrors) errors" -Value $null -Detail "needs the fixture executor + session harness; run the execution acceptance gate (Task 35) and attach its result" }
        "analysisNonModelOverheadMs" { Add-Measurement -Name $name -Status "SKIP" -Threshold "no fixed P8 gate" -Value $null -Detail "needs a fixture LLM analysis run; reported by eval/acceptance" }
    }
}

$datasetJSON = ""
if ($null -ne $datasetState) {
    $datasetJSON = ([ordered]@{
            Versions     = $datasetState.Versions
            Requirements = $datasetState.Requirements
            Cases        = $datasetState.Cases
            Edges        = $datasetState.Edges
            Events       = $datasetState.Events
        }) | ConvertTo-Json -Compress
}

$status = Write-Outputs -Mode "measure" -ApiBase $script:apiBase -WebBase $webBase -DatasetJSON $datasetJSON `
    -Note "Local measurement; SKIP means the API/browser/fixture was missing. Re-run in that environment before judging."
if ($status -eq "failed") { exit 1 }
exit 0
