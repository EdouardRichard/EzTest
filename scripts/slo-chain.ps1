#requires -Version 5.1
<#
.SYNOPSIS
    Measures the P8 exploration-chain SLO on a seeded 200-node / 400-edge dataset.

.DESCRIPTION
    This is the focused chain companion of scripts/slo.ps1 (P8 design spec section 5.2/
    section 5.8/section 5.9). It seeds one reserve-id version with exactly 200 chain nodes and
    400 chain edges directly into the database behind a running API:

      50 requirements
      50 cases (each linked to 6 requirements -> 300 requirement_case edges)
      50 execution results (one per case -> 50 case_result edges)
      50 issues (one per result -> 50 result_issue edges)

    Then it measures P95 of:
      * GET /api/versions/{id}/exploration-chain (local 200-node threshold 800ms,
        design spec section 5.2);
      * the browser canvas becoming interactive (navigation until the 200-node
        ARIA tree is rendered and an ArrowDown keypress moves the active node,
        threshold 2500ms, design spec section 1.5/section 5.8).

    Every row is PASS, FAIL or SKIP. A measured P95 above its threshold is FAIL
    and the script exits 1. A missing API/database/browser/fixture is an explicit
    SKIP recorded in the report (exit 0) and is never reported as a pass. A
    browser run that starts but fails is a FAIL, not a SKIP.

    The script never prints .env credentials, the database URL or the token.

.PARAMETER ApiBaseUrl
    Running API base URL. Defaults to EZTEST_ACCEPTANCE_API_URL, then the
    EZTEST_HTTP_ADDR port on 127.0.0.1, then http://127.0.0.1:13333.

.PARAMETER WebBaseUrl
    Running web app base URL. When empty the canvas row is SKIP.

.PARAMETER DatabaseUrl
    PostgreSQL URL of the database the running API uses. Defaults to
    EZTEST_DATABASE_URL from .env. The reserved seed ids are cleaned on exit.

.PARAMETER Iterations
    API samples (default 20, minimum 3).

.PARAMETER BrowserIterations
    Measured browser navigations (default 5, minimum 3). One extra warm-up
    navigation is not counted, so a cold Next dev compile cannot pollute P95.

.PARAMETER EnvPath
    .env path (default <repo>/.env).

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
    Run from anywhere; paths are resolved from the repository root.

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo-chain.ps1 `
      -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://127.0.0.1:6868 `
      -DatabaseUrl $env:EZTEST_E2E_DATABASE_URL
#>
[CmdletBinding()]
param(
    [string]$ApiBaseUrl = "",
    [string]$WebBaseUrl = "",
    [string]$DatabaseUrl = "",
    [int]$Iterations = 20,
    [int]$BrowserIterations = 5,
    [string]$EnvPath = ""
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$frontendDir = Join-Path $repoRoot "frontend"
$tmpDir = Join-Path $repoRoot ".tmp\p8-slo-chain"
$metricsPath = Join-Path $tmpDir "metrics.json"
$reportPath = Join-Path $repoRoot "docs\acceptance\p8\slo-chain.md"
$probePath = Join-Path $tmpDir "canvas-probe.cjs"

# Design spec section 5.2 (API) and section 1.5/section 5.8 (canvas interactive) thresholds.
$apiP95ThresholdMs = 800
$canvasP95ThresholdMs = 2500
$dataset = [ordered]@{
    requirements = 50
    cases        = 50
    results      = 50
    issues       = 50
    nodes        = 200
    edges        = 400
}

# Reserved id range, disjoint from scripts/slo.ps1 (88.0-88.3e18) and from the
# Snowflake ids the application generates.
$versionBase = [long]8840000000000000000
$requirementBase = [long]8841000000000000000
$caseBase = [long]8842000000000000000
$sourceBase = [long]8843000000000000000
$batchID = [long]8844000000000000001
$caseRunBase = [long]8845000000000000000
$issueBase = [long]8846000000000000000
$seedCount = 50
$seedVersionID = [long]($versionBase + 1)

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
    if (-not [string]::IsNullOrWhiteSpace($LocalPath) -and (Test-Path -LiteralPath $LocalPath)) {
        return $LocalPath
    }
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
    if (-not [string]::IsNullOrWhiteSpace($addr)) { return "http://$addr" }
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
        if (-not [string]::IsNullOrWhiteSpace($Connection.SSLMode)) {
            $env:PGSSLMODE = $Connection.SSLMode
        }
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        try {
            $output = & $script:psqlExe -h $Connection.Host -p $Connection.Port -U $Connection.User `
                -d $Connection.Database -v ON_ERROR_STOP=1 -q -tA -f $tempFile 2>$null
            $code = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
        }
    } finally {
        if ($null -eq $savedPassword) {
            Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
        } else {
            $env:PGPASSWORD = $savedPassword
        }
        if ($null -eq $savedSSLMode) {
            Remove-Item Env:PGSSLMODE -ErrorAction SilentlyContinue
        } else {
            $env:PGSSLMODE = $savedSSLMode
        }
        Remove-Item -LiteralPath $tempFile -Force -ErrorAction SilentlyContinue
    }
    if ($code -ne 0) { throw "$Operation failed (psql exit $code)" }
    return @($output | ForEach-Object { ([string]$_).Trim() } | Where-Object { $_ -ne "" })
}

function Invoke-SeedDataset {
    param($Connection)
    # A single SQL row is unrolled to a scalar string by PowerShell; wrap it so
    # Count/indexing always describe rows, not characters.
    $adminRows = @(Invoke-PsqlScript -Connection $Connection -Operation "reading the admin user" `
        -Sql "SELECT id FROM user_account WHERE role = 'admin' ORDER BY id ASC LIMIT 1;")
    if ($adminRows.Count -eq 0) {
        throw "no admin user exists; start the API once so it seeds the admin account"
    }
    $adminID = [long]$adminRows[0]

    $cleanupSql = @"
DELETE FROM issue WHERE id >= $issueBase AND id < $issueBase + 10000;
DELETE FROM case_run WHERE id >= $caseRunBase AND id < $caseRunBase + 10000;
DELETE FROM run_batch WHERE id = $batchID;
DELETE FROM case_source WHERE id >= $sourceBase AND id < $sourceBase + 10000;
DELETE FROM case_node WHERE id >= $caseBase AND id < $caseBase + 10000;
DELETE FROM requirement WHERE id >= $requirementBase AND id < $requirementBase + 10000;
DELETE FROM version WHERE id >= $versionBase AND id < $versionBase + 10000;
"@
    [void](Invoke-PsqlScript -Connection $Connection -Operation "cleaning a previous chain seed" `
        -Sql $cleanupSql)

    $seedSql = @"
INSERT INTO version (id, name, label, description, status, revision, created_by, updated_by,
                     created_at, updated_at)
VALUES ($seedVersionID, 'P8 SLO chain 200 nodes', 'p8-slo-chain', '', 'completed', 1,
        $adminID, $adminID, now() - interval '2 minutes', now() - interval '1 minute');

INSERT INTO requirement (id, version_id, title, description, acceptance_criteria, priority,
                         status, source, last_update_source, revision, created_by, updated_by,
                         created_at, updated_at)
SELECT $requirementBase + g, $seedVersionID, 'SLO chain requirement ' || g, '', '',
       'must', 'clarified', 'user', 'user', 1, $adminID, $adminID,
       now() - (g || ' seconds')::interval, now() - interval '1 minute'
FROM generate_series(1, $seedCount) AS g;

INSERT INTO case_node (id, version_id, kind, title, priority, source, status, sort_order,
                       revision, created_by, updated_by, created_at, updated_at)
SELECT $caseBase + g, $seedVersionID, 'case', 'SLO chain case ' || g, 'P1', 'user_input',
       'executed', g - 1, 1, $adminID, $adminID,
       now() - (g || ' seconds')::interval, now() - interval '1 minute'
FROM generate_series(1, $seedCount) AS g;

-- Six requirement links per case -> 300 requirement_case edges.
INSERT INTO case_source (id, version_id, node_id, ref_kind, ref_id, ref_key, source)
SELECT $sourceBase + g * 10 + k,
       $seedVersionID,
       $caseBase + g,
       'requirement',
       $requirementBase + (((g - 1 + k) % $seedCount) + 1),
       'slo-chain-source-' || g || '-' || k,
       'user_input'
FROM generate_series(1, $seedCount) AS g
CROSS JOIN generate_series(0, 5) AS k;

INSERT INTO run_batch (id, version_id, batch_no, status, baseline_hash, graph_hash,
                       case_count, passed_count, failed_count, created_by,
                       started_at, finished_at, autonomy_done)
VALUES ($batchID, $seedVersionID, 1, 'done',
        repeat('a', 64), repeat('a', 64),
        $seedCount, 25, 25, $adminID,
        now() - interval '90 seconds', now() - interval '60 seconds', true);

INSERT INTO case_run (id, batch_id, version_id, case_node_id, case_revision, case_hash,
                      case_snapshot, attempt, status, failure_class, summary, duration_ms,
                      started_at, finished_at)
SELECT $caseRunBase + g, $batchID, $seedVersionID, $caseBase + g, 1, repeat('b', 64),
       '{}'::jsonb, 1,
       CASE WHEN g % 2 = 0 THEN 'failed' ELSE 'passed' END,
       CASE WHEN g % 2 = 0 THEN 'application' ELSE NULL END,
       CASE WHEN g % 2 = 0 THEN 'SLO chain failed' ELSE 'SLO chain passed' END,
       1000 + g, now() - interval '70 seconds', now() - interval '60 seconds'
FROM generate_series(1, $seedCount) AS g;

INSERT INTO issue (id, version_id, batch_id, case_run_id, case_node_id, source, category,
                   severity, status, confidence, title, description,
                   created_by, updated_by, created_at, updated_at)
SELECT $issueBase + g, $seedVersionID, $batchID, $caseRunBase + g, $caseBase + g,
       'human', 'other',
       CASE g % 4 WHEN 0 THEN 'blocker' WHEN 1 THEN 'major' WHEN 2 THEN 'minor' ELSE 'info' END,
       'open', 'medium', 'SLO chain issue ' || g, '',
       $adminID, $adminID, now() - (g || ' seconds')::interval, now() - interval '1 minute'
FROM generate_series(1, $seedCount) AS g;
"@
    [void](Invoke-PsqlScript -Connection $Connection -Operation "seeding the chain SLO dataset" `
        -Sql $seedSql)

    $counts = @(Invoke-PsqlScript -Connection $Connection -Operation "verifying the chain SLO dataset" -Sql @"
SELECT
  (SELECT count(*) FROM requirement WHERE version_id = $seedVersionID),
  (SELECT count(*) FROM case_node WHERE version_id = $seedVersionID AND kind = 'case'),
  (SELECT count(*) FROM case_run WHERE version_id = $seedVersionID),
  (SELECT count(*) FROM issue WHERE version_id = $seedVersionID),
  (SELECT count(*) FROM case_source WHERE version_id = $seedVersionID AND ref_kind = 'requirement');
"@)
    if ($counts.Count -ne 1) { throw "unexpected dataset verification output" }
    $parts = $counts[0].Split("|")
    if ($parts.Length -ne 5) { throw "unexpected dataset verification output: $($counts[0])" }
    return [pscustomobject]@{
        VersionID    = $seedVersionID
        Requirements = [int]$parts[0]
        Cases        = [int]$parts[1]
        Results      = [int]$parts[2]
        Issues       = [int]$parts[3]
        CaseSources  = [int]$parts[4]
    }
}

function Remove-SeedDataset {
    param($Connection)
    if ($null -eq $Connection) { return }
    try {
        [void](Invoke-PsqlScript -Connection $Connection -Operation "cleaning the chain SLO dataset" -Sql @"
DELETE FROM issue WHERE id >= $issueBase AND id < $issueBase + 10000;
DELETE FROM case_run WHERE id >= $caseRunBase AND id < $caseRunBase + 10000;
DELETE FROM run_batch WHERE id = $batchID;
DELETE FROM case_source WHERE id >= $sourceBase AND id < $sourceBase + 10000;
DELETE FROM case_node WHERE id >= $caseBase AND id < $caseBase + 10000;
DELETE FROM requirement WHERE id >= $requirementBase AND id < $requirementBase + 10000;
DELETE FROM version WHERE id >= $versionBase AND id < $versionBase + 10000;
"@)
    } catch {
        Write-Warning "chain SLO seed cleanup failed: $($_.Exception.Message)"
    }
}

function Invoke-ApiMeasurement {
    param([string]$Path, [int]$Count, [hashtable]$Headers)
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
        $payload = $response.Content | ConvertFrom-Json
        $nodes = @($payload.data.nodes)
        if ($nodes.Count -ne $dataset.nodes) {
            throw "GET $Path returned $($nodes.Count) nodes, expected $($dataset.nodes)"
        }
        [void]$samples.Add($stopwatch.Elapsed.TotalMilliseconds)
    }
    return [pscustomobject]@{
        P50     = (Get-Percentile -Values $samples.ToArray() -Percentile 0.50)
        P95     = (Get-Percentile -Values $samples.ToArray() -Percentile 0.95)
        Samples = $samples.Count
    }
}

function Invoke-CanvasMeasurement {
    param([string]$BaseUrl, [string]$VersionID, [string]$Token, $User, [int]$Count)
    if ([string]::IsNullOrWhiteSpace($BaseUrl)) {
        return [pscustomobject]@{ Status = "SKIP"; Detail = "no -WebBaseUrl supplied"; P95 = $null }
    }
    try {
        $probe = Invoke-WebRequest -Uri ($BaseUrl.TrimEnd("/") + "/login") -UseBasicParsing `
            -TimeoutSec 10 -ErrorAction Stop
        if ([int]$probe.StatusCode -ne 200) {
            return [pscustomobject]@{
                Status = "SKIP"; Detail = "web app answered HTTP $($probe.StatusCode)"; P95 = $null
            }
        }
    } catch {
        return [pscustomobject]@{
            Status = "SKIP"; Detail = "web app not reachable at $BaseUrl"; P95 = $null
        }
    }

    $node = Resolve-ToolPath -LocalPath "" -CommandName "node.exe"
    if ([string]::IsNullOrWhiteSpace($node)) {
        return [pscustomobject]@{ Status = "SKIP"; Detail = "node.exe is not available"; P95 = $null }
    }
    $playwright = Join-Path $frontendDir "node_modules\@playwright\test\package.json"
    if (-not (Test-Path -LiteralPath $playwright)) {
        return [pscustomobject]@{
            Status = "SKIP"; Detail = "Playwright is not installed in frontend/node_modules"; P95 = $null
        }
    }

    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
    $js = @'
const { chromium } = require("@playwright/test");
(async () => {
  const url = process.env.SLO_CHAIN_WEB_URL;
  const token = process.env.SLO_CHAIN_TOKEN;
  const user = process.env.SLO_CHAIN_USER;
  const count = Number(process.env.SLO_CHAIN_SAMPLES || "5");
  const channel = process.env.SLO_CHAIN_CHANNEL || "chrome";
  const browser = await chromium.launch({ headless: true, channel });
  try {
    const context = await browser.newContext();
    await context.addInitScript(([t, u]) => {
      sessionStorage.setItem("eztest.token", t);
      sessionStorage.setItem("eztest.user", u);
    }, [token, user]);
    const page = await context.newPage();
    const ready = async () => {
      await page.getByTestId("exploration-tab").waitFor({ state: "visible", timeout: 60000 });
      await page.getByTestId("chain-canvas").waitFor({ state: "visible", timeout: 60000 });
      await page.waitForFunction(
        (expected) => document.querySelectorAll('[data-testid^="chain-treeitem-"]').length === expected,
        Number(process.env.SLO_CHAIN_NODES || "200"),
        { timeout: 60000 },
      );
    };
    // Warm-up: the first Next dev compile must not pollute the measured P95.
    await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
    await ready();
    const samples = [];
    for (let i = 0; i < count; i++) {
      const started = Date.now();
      await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60000 });
      await ready();
      const first = page.locator('[data-testid^="chain-treeitem-"]').first();
      await first.focus();
      const previousActive = await page.evaluate(() => {
        const active = document.querySelector('[data-testid^="chain-treeitem-"][data-active="true"]');
        return active ? active.getAttribute("data-testid") : "";
      });
      await page.keyboard.press("ArrowDown");
      await page.waitForFunction(
        (previous) => {
          const active = document.querySelector('[data-testid^="chain-treeitem-"][data-active="true"]');
          return active !== null && active.getAttribute("data-testid") !== previous;
        },
        previousActive,
        { timeout: 30000 },
      );
      samples.push(Date.now() - started);
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
    [System.IO.File]::WriteAllText($probePath, $js, (New-Object System.Text.UTF8Encoding($false)))

    $env:SLO_CHAIN_WEB_URL = $BaseUrl.TrimEnd("/") + "/versions/" + $VersionID + "?tab=exploration"
    $env:SLO_CHAIN_TOKEN = $Token
    $env:SLO_CHAIN_USER = $User | ConvertTo-Json -Compress
    $env:SLO_CHAIN_SAMPLES = [string]$Count
    $env:SLO_CHAIN_NODES = [string]$dataset.nodes
    if ([string]::IsNullOrWhiteSpace($env:EZTEST_E2E_CHANNEL)) {
        $env:SLO_CHAIN_CHANNEL = "chrome"
    } else {
        $env:SLO_CHAIN_CHANNEL = $env:EZTEST_E2E_CHANNEL
    }
    $savedNodePath = $env:NODE_PATH
    $env:NODE_PATH = Join-Path $frontendDir "node_modules"
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        Push-Location -LiteralPath $frontendDir
        try {
            $output = & $node $probePath 2>&1
            $code = $LASTEXITCODE
        } finally {
            Pop-Location
        }
    } finally {
        $ErrorActionPreference = $previous
        if ($null -eq $savedNodePath) {
            Remove-Item Env:NODE_PATH -ErrorAction SilentlyContinue
        } else {
            $env:NODE_PATH = $savedNodePath
        }
        Remove-Item Env:SLO_CHAIN_WEB_URL, Env:SLO_CHAIN_TOKEN, Env:SLO_CHAIN_USER,
            Env:SLO_CHAIN_SAMPLES, Env:SLO_CHAIN_NODES, Env:SLO_CHAIN_CHANNEL `
            -ErrorAction SilentlyContinue
    }
    $lastLine = @($output) | Select-Object -Last 1
    if ($code -ne 0) {
        return [pscustomobject]@{
            Status = "FAIL"
            Detail = "Playwright canvas probe failed: $lastLine"
            P95    = $null
        }
    }
    try {
        $samples = [double[]](@($output | Select-Object -Last 1) | ConvertFrom-Json)
    } catch {
        return [pscustomobject]@{
            Status = "FAIL"
            Detail = "Playwright canvas probe produced no timing samples"
            P95    = $null
        }
    }
    if ($samples.Count -lt 3) {
        return [pscustomobject]@{
            Status = "FAIL"; Detail = "Playwright canvas probe returned too few samples"; P95 = $null
        }
    }
    return [pscustomobject]@{
        Status = "PASS"
        Detail = "$($samples.Count) measured navigations (1 warm-up excluded)"
        P95    = (Get-Percentile -Values $samples -Percentile 0.95)
    }
}

function Write-Outputs {
    param(
        [string]$ApiBase,
        [string]$WebBase,
        [string]$DatasetJSON,
        [string]$Note
    )
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $reportPath) | Out-Null

    $failed = @($checks | Where-Object { $_.Status -eq "FAIL" })
    $failedMeasurements = @($measurements.Keys | Where-Object { $measurements[$_].status -eq "FAIL" })
    $status = "passed"
    if ($failed.Count -gt 0 -or $failedMeasurements.Count -gt 0) {
        $status = "failed"
    }
    $skipMeasurements = @($measurements.Keys | Where-Object { $measurements[$_].status -eq "SKIP" })
    if ($status -eq "passed" -and $skipMeasurements.Count -gt 0) { $status = "passed_with_skips" }

    $metrics = [ordered]@{
        generatedAt  = (Get-Date).ToUniversalTime().ToString("o")
        mode         = "measure"
        status       = $status
        apiBaseUrl   = $ApiBase
        webBaseUrl   = $WebBase
        thresholds   = [ordered]@{
            apiP95Ms    = $apiP95ThresholdMs
            canvasP95Ms = $canvasP95ThresholdMs
        }
        dataset      = $dataset
        datasetState = $(if ([string]::IsNullOrWhiteSpace($DatasetJSON)) { $null } else { $DatasetJSON | ConvertFrom-Json })
        checks       = @($checks)
        measurements = $measurements
        note         = $Note
    }
    [System.IO.File]::WriteAllText($metricsPath, ($metrics | ConvertTo-Json -Depth 8),
        (New-Object System.Text.UTF8Encoding($false)))

    $builder = New-Object System.Text.StringBuilder
    [void]$builder.AppendLine("# P8 Exploration Chain SLO Report (200 nodes / 400 edges)")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("> Generated by ``scripts/slo-chain.ps1`` (design spec section 5.2/section 5.8/section 5.9). Local acceptance measurement, **not a production SLA promise**; re-measure per deployment environment.")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("- Generated (UTC): $((Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ"))")
    [void]$builder.AppendLine("- API: ``$ApiBase``")
    [void]$builder.AppendLine("- Web: ``$(if ([string]::IsNullOrWhiteSpace($WebBase)) { "(not supplied)" } else { $WebBase })``")
    [void]$builder.AppendLine("- Overall status: **$($status.ToUpperInvariant())**")
    if (-not [string]::IsNullOrWhiteSpace($Note)) {
        [void]$builder.AppendLine("- Note: $Note")
    }
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("## Dataset")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("| Object | Target |")
    [void]$builder.AppendLine("| --- | --- |")
    [void]$builder.AppendLine("| Requirements | $($dataset.requirements) |")
    [void]$builder.AppendLine("| Cases | $($dataset.cases) |")
    [void]$builder.AppendLine("| Results | $($dataset.results) |")
    [void]$builder.AppendLine("| Issues | $($dataset.issues) |")
    [void]$builder.AppendLine("| Nodes | $($dataset.nodes) |")
    [void]$builder.AppendLine("| Edges | $($dataset.edges) |")
    [void]$builder.AppendLine("")
    if (-not [string]::IsNullOrWhiteSpace($DatasetJSON)) {
        $seeded = $DatasetJSON | ConvertFrom-Json
        [void]$builder.AppendLine("Seeded and verified: $($seeded.Requirements) requirements, $($seeded.Cases) cases, $($seeded.Results) results, $($seeded.Issues) issues, $($seeded.CaseSources) requirement links (version ``$($seeded.VersionID)``).")
        [void]$builder.AppendLine("")
    }
    [void]$builder.AppendLine("## Thresholds and results")
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("| Measurement | Threshold | Measured P95 | Status | Detail |")
    [void]$builder.AppendLine("| --- | --- | --- | --- | --- |")
    $order = @("chainApiP95Ms", "chainCanvasP95Ms")
    $labels = @{
        chainApiP95Ms    = "Exploration-chain API latency (200 nodes/400 edges)"
        chainCanvasP95Ms = "Chain canvas interactive latency (200 nodes/400 edges)"
    }
    foreach ($name in $order) {
        if (-not $measurements.Contains($name)) { continue }
        $row = $measurements[$name]
        $value = "SKIP"
        if ($null -ne $row.value) { $value = [string]$row.value }
        [void]$builder.AppendLine("| $($labels[$name]) | $($row.threshold) | $value | $($row.status) | $($row.detail) |")
    }
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("## Checks")
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
    [void]$builder.AppendLine("powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo-chain.ps1 ``")
    [void]$builder.AppendLine("  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://127.0.0.1:6868 ``")
    [void]$builder.AppendLine("  -DatabaseUrl `$env:EZTEST_E2E_DATABASE_URL")
    [void]$builder.AppendLine('```')
    [void]$builder.AppendLine("")
    [void]$builder.AppendLine("Exit code: 1 when a measured threshold fails; 0 when rows are PASS or an environment row is SKIP. SKIP is never counted as PASS and every skipped row states why it could not be measured.")
    [void]$builder.AppendLine("")
    [System.IO.File]::WriteAllText($reportPath, $builder.ToString(), (New-Object System.Text.UTF8Encoding($false)))
    Write-Host ""
    Write-Host "Metrics: $metricsPath" -ForegroundColor Green
    Write-Host "Report: $reportPath" -ForegroundColor Green
    Write-Host "Status: $($status.ToUpperInvariant())" -ForegroundColor $(if ($status -eq "failed") { "Red" } else { "Green" })
    return $status
}

# ---------------------------------------------------------------------------
# Measurement mode.
# ---------------------------------------------------------------------------
Write-Step "P8 exploration-chain SLO measurement (200 nodes / 400 edges)"

if ([string]::IsNullOrWhiteSpace($EnvPath)) { $EnvPath = Join-Path $repoRoot ".env" }
$envValues = Read-DotEnv -Path $EnvPath
$script:apiBase = Resolve-ApiBaseUrl -Explicit $ApiBaseUrl -EnvValues $envValues
$webBase = $WebBaseUrl.TrimEnd("/")
if ($Iterations -lt 3) { $Iterations = 3 }
if ($BrowserIterations -lt 3) { $BrowserIterations = 3 }

$connection = $null
$datasetState = $null
$token = ""
$login = $null
$authHeaders = @{}

Write-Note "API: $($script:apiBase)"
$apiReachable = $false
try {
    $health = Invoke-WebRequest -Uri ($script:apiBase + "/api/health") -UseBasicParsing `
        -TimeoutSec 5 -ErrorAction Stop
    $apiReachable = [int]$health.StatusCode -eq 200
} catch {
    $apiReachable = $false
}
if (-not $apiReachable) {
    Add-Check -Area "environment" -Check "API reachable" -Status "SKIP" `
        -Detail "no API at $($script:apiBase); start the API or pass -ApiBaseUrl"
    foreach ($name in @("chainApiP95Ms", "chainCanvasP95Ms")) {
        Add-Measurement -Name $name -Status "SKIP" -Threshold "see report" -Value $null `
            -Detail "API is not reachable; measurement skipped"
    }
    [void](Write-Outputs -ApiBase $script:apiBase -WebBase $webBase -DatasetJSON "" `
        -Note "API unreachable; every measurement row is SKIP. Missing environment is not an SLO PASS.")
    exit 0
}
Add-Check -Area "environment" -Check "API reachable" -Status "PASS" -Detail $script:apiBase

$hasCredentials = $envValues.ContainsKey("EZTEST_ADMIN_USERNAME") -and
    -not [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_ADMIN_USERNAME"]) -and
    $envValues.ContainsKey("EZTEST_ADMIN_PASSWORD") -and
    -not [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_ADMIN_PASSWORD"])
if (-not $hasCredentials) {
    Add-Check -Area "environment" -Check "admin credentials" -Status "SKIP" `
        -Detail "EZTEST_ADMIN_USERNAME/PASSWORD are not set in .env"
} else {
    try {
        $loginBody = @{
            username = [string]$envValues["EZTEST_ADMIN_USERNAME"]
            password = [string]$envValues["EZTEST_ADMIN_PASSWORD"]
        } | ConvertTo-Json
        $login = Invoke-RestMethod -Uri ($script:apiBase + "/api/auth/login") -Method Post `
            -Body $loginBody -ContentType "application/json" -TimeoutSec 15 -ErrorAction Stop
        $token = [string]$login.data.token
        if ([string]::IsNullOrWhiteSpace($token)) { throw "login response has no token" }
        $authHeaders = @{ Authorization = "Bearer $token" }
        Add-Check -Area "environment" -Check "admin login" -Status "PASS" `
            -Detail "authenticated against the running API"
    } catch {
        Add-Check -Area "environment" -Check "admin login" -Status "SKIP" `
            -Detail "login failed; check EZTEST_ADMIN_USERNAME/PASSWORD in .env"
    }
}

$dbUrl = $DatabaseUrl
if ([string]::IsNullOrWhiteSpace($dbUrl) -and $envValues.ContainsKey("EZTEST_DATABASE_URL")) {
    $dbUrl = [string]$envValues["EZTEST_DATABASE_URL"]
}
$connection = Parse-PostgresUrl -Url $dbUrl
$script:psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") `
    -CommandName "psql.exe"

if (-not [string]::IsNullOrWhiteSpace($token)) {
    if ($null -eq $connection) {
        Add-Check -Area "dataset" -Check "database URL" -Status "SKIP" `
            -Detail "DatabaseUrl/EZTEST_DATABASE_URL is missing or not a postgres URL"
    } elseif ([string]::IsNullOrWhiteSpace($script:psqlExe)) {
        Add-Check -Area "dataset" -Check "psql" -Status "SKIP" -Detail "psql.exe is not available"
    } else {
        try {
            $datasetState = Invoke-SeedDataset -Connection $connection
            Add-Check -Area "dataset" -Check "seed" -Status "PASS" `
                -Detail "$($datasetState.Requirements) requirements, $($datasetState.Cases) cases, $($datasetState.Results) results, $($datasetState.Issues) issues, $($datasetState.CaseSources) requirement links"
            # The read model must return exactly the measured graph; a stale or
            # partial seed is a dataset failure, never a timing PASS.
            $verify = Invoke-WebRequest -Uri (
                $script:apiBase + "/api/versions/$seedVersionID/exploration-chain?limit=300&requirementLimit=100"
            ) -Headers $authHeaders -UseBasicParsing -TimeoutSec 30 -ErrorAction Stop
            $verifyPayload = $verify.Content | ConvertFrom-Json
            $verifyNodes = @($verifyPayload.data.nodes).Count
            $verifyEdges = @($verifyPayload.data.edges).Count
            if ($verifyNodes -ne $dataset.nodes -or $verifyEdges -ne $dataset.edges) {
                throw "read model returned $verifyNodes nodes/$verifyEdges edges, expected $($dataset.nodes)/$($dataset.edges)"
            }
            Add-Check -Area "dataset" -Check "read model" -Status "PASS" `
                -Detail "GET exploration-chain returned $verifyNodes nodes and $verifyEdges edges"
        } catch {
            Add-Check -Area "dataset" -Check "seed" -Status "FAIL" -Detail $_.Exception.Message
        }
    }
} else {
    Add-Check -Area "dataset" -Check "seed" -Status "SKIP" -Detail "no authenticated API session"
}

try {
    if ($null -ne $datasetState) {
        $chain = Invoke-ApiMeasurement -Path (
            "/api/versions/$seedVersionID/exploration-chain?limit=300&requirementLimit=100"
        ) -Count $Iterations -Headers $authHeaders
        $apiOk = $chain.P95 -le $apiP95ThresholdMs
        Add-Measurement -Name "chainApiP95Ms" -Status $(if ($apiOk) { "PASS" } else { "FAIL" }) `
            -Threshold "<= $apiP95ThresholdMs ms" -Value $chain.P95 `
            -Detail "P50 $($chain.P50) ms over $($chain.Samples) samples (200 nodes/400 edges)"

        $canvas = Invoke-CanvasMeasurement -BaseUrl $webBase -VersionID ([string]$seedVersionID) `
            -Token $token -User $login.data.user -Count $BrowserIterations
        if ($canvas.P95 -eq $null) {
            Add-Measurement -Name "chainCanvasP95Ms" -Status $canvas.Status `
                -Threshold "<= $canvasP95ThresholdMs ms" -Value $null -Detail $canvas.Detail
        } else {
            $canvasOk = $canvas.P95 -le $canvasP95ThresholdMs
            Add-Measurement -Name "chainCanvasP95Ms" -Status $(if ($canvasOk) { "PASS" } else { "FAIL" }) `
                -Threshold "<= $canvasP95ThresholdMs ms" -Value $canvas.P95 `
                -Detail $canvas.Detail
        }
    } else {
        foreach ($name in @("chainApiP95Ms", "chainCanvasP95Ms")) {
            Add-Measurement -Name $name -Status "SKIP" -Threshold "see report" -Value $null `
                -Detail "dataset was not seeded; measurement skipped"
        }
    }
} catch {
    Add-Check -Area "measurement" -Check "request" -Status "FAIL" -Detail $_.Exception.Message
    if (-not $measurements.Contains("chainApiP95Ms")) {
        Add-Measurement -Name "chainApiP95Ms" -Status "FAIL" -Threshold "<= $apiP95ThresholdMs ms" `
            -Value $null -Detail $_.Exception.Message
    }
} finally {
    Remove-SeedDataset -Connection $connection
}

$datasetJSON = ""
if ($null -ne $datasetState) {
    $datasetJSON = ([ordered]@{
            VersionID    = $datasetState.VersionID
            Requirements = $datasetState.Requirements
            Cases        = $datasetState.Cases
            Results      = $datasetState.Results
            Issues       = $datasetState.Issues
            CaseSources  = $datasetState.CaseSources
        }) | ConvertTo-Json -Compress
}

$status = Write-Outputs -ApiBase $script:apiBase -WebBase $webBase -DatasetJSON $datasetJSON `
    -Note "Local measurement. SKIP means the API/database/browser was missing; a measured browser failure is FAIL, never SKIP."
if ($status -eq "failed") { exit 1 }
exit 0
