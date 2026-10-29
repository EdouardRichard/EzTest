#requires -Version 5.1
<#
.SYNOPSIS
    Runs the P8 evaluation gate (design spec §9.1/D61).

.DESCRIPTION
    Prepares an isolated eztest_eval database, builds and starts the API, the
    deterministic fake LLM, the fixture target and the Playwright executor, then
    drives the whole clarification/design/review/execution/supplement/failure/
    issue/report chain. Metrics are derived from the real API data and the
    committed eval/ground-truth files; the result is written through the admin
    API (POST /api/quality/eval-runs), compared with eval/baseline.json and
    summarised in .tmp/p8-eval/*.json.

    -DryRun validates the corpus, the baseline, the database target and the
    toolchain without starting a service or calling an LLM. It is the hook used
    by scripts/acceptance.ps1 -StaticOnly.

    -RealLLM is gated by EZTEST_RUN_REAL_LLM=1 and records a report-only run
    (real model, real executor); it never gates a release.

    The script never prints .env credentials, the database URL or the LLM key.

.PARAMETER DryRun
    Static preflight only (corpus/baseline/DB target/toolchain); no LLM call.

.PARAMETER RealLLM
    Gated real-LLM report. Requires EZTEST_RUN_REAL_LLM=1 and llm_* in .env.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [switch]$DryRun,
    [switch]$RealLLM
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$scriptsDir = Join-Path $repoRoot "scripts"
$evalRoot = Join-Path $repoRoot "eval"
$corpusDir = Join-Path $evalRoot "corpus"
$truthDir = Join-Path $evalRoot "ground-truth"
$tmpRoot = Join-Path $repoRoot ".tmp"
$tmpDir = Join-Path $tmpRoot "p8-eval"

$metricNames = @(
    "design_coverage",
    "impact_recall",
    "impact_precision",
    "classification_accuracy",
    "issue_false_positive_rate",
    "supplement_hit_rate"
)

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Write-LogTail {
    param([string]$Path, [string]$Label, [int]$Lines = 60)
    if (-not (Test-Path -LiteralPath $Path)) { return }
    Write-Host ""
    Write-Host "--- $Label (last $Lines lines) ---" -ForegroundColor Yellow
    Get-Content -LiteralPath $Path -Tail $Lines -Encoding UTF8 -ErrorAction SilentlyContinue | ForEach-Object {
        Write-Host $_
    }
}

function Read-JsonFile {
    param([string]$Path, [string]$Label)
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "missing $Label at $Path"
    }
    try {
        return (Get-Content -LiteralPath $Path -Raw -Encoding UTF8 -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop)
    } catch {
        throw "$Label is not valid JSON ($Path)"
    }
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Missing .env at $Path; the evaluation needs EZTEST_DATABASE_URL and the admin credentials"
    }
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

function Get-RequiredValue {
    param($Values, [string]$Key)
    if (-not $Values.ContainsKey($Key) -or [string]::IsNullOrWhiteSpace([string]$Values[$Key])) {
        throw "Missing required value $Key in .env"
    }
    return [string]$Values[$Key]
}

function Get-CommandExe {
    param([string]$Name, [string]$PreferredPath)
    if (-not [string]::IsNullOrWhiteSpace($PreferredPath) -and (Test-Path -LiteralPath $PreferredPath)) {
        return $PreferredPath
    }
    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        throw "$Name is not available on PATH and $PreferredPath does not exist"
    }
    return $command.Source
}

# Invoke-VisualEvaluation replays eval/ground-truth/visual.json through the
# production execution-domain classifier (backend/internal/execution/evalvisual)
# and returns its JSON metric document. It needs only the Go toolchain, so both
# the deterministic run and -DryRun consume the same evaluation.
function Invoke-VisualEvaluation {
    param([string]$GoExe, [string]$RepoRoot)
    $backendDir = Join-Path $RepoRoot "backend"
    $corpusPath = Join-Path $RepoRoot "eval\corpus\visual.json"
    $truthPath = Join-Path $RepoRoot "eval\ground-truth\visual.json"
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
    $errLog = Join-Path $tmpDir "visual-eval.err.log"
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    Push-Location $backendDir
    try {
        $output = & $GoExe run ./internal/execution/evalvisual -corpus $corpusPath -ground-truth $truthPath 2>$errLog
        $evalExit = $LASTEXITCODE
    } finally {
        Pop-Location
        $ErrorActionPreference = $previous
    }
    if ($evalExit -ne 0) {
        $detail = ""
        if (Test-Path -LiteralPath $errLog) {
            $detail = (Get-Content -LiteralPath $errLog -Raw -Encoding UTF8 -ErrorAction SilentlyContinue)
        }
        throw "visual assertion evaluation failed (exit $evalExit): $detail"
    }
    try {
        $result = ($output | Out-String) | ConvertFrom-Json -ErrorAction Stop
    } catch {
        throw "visual assertion evaluation did not return valid JSON"
    }
    if ([string]::IsNullOrWhiteSpace([string]$result.metric)) {
        throw "visual assertion evaluation returned no metric"
    }
    if (-not [bool]$result.passed) {
        throw ("visual_classification_accuracy {0:P1} is below the {1:P1} threshold ({2}/{3} cases classified correctly)" -f `
                [double]$result.accuracy, [double]$result.threshold, [int]$result.correct, [int]$result.total)
    }
    return $result
}

# ---------------------------------------------------------------------------
# Static preflight: corpus, baseline, database target and toolchain. It is
# shared by -DryRun and the full run so both reject the same broken inputs.
# ---------------------------------------------------------------------------
function Invoke-Preflight {
    $requiredFiles = @{
        "README"                 = (Join-Path $evalRoot "README.md")
        "requirements corpus"    = (Join-Path $corpusDir "requirements.json")
        "environment corpus"     = (Join-Path $corpusDir "environment.json")
        "impact ground truth"    = (Join-Path $truthDir "impact.json")
        "classification truth"   = (Join-Path $truthDir "classification.json")
        "issues ground truth"    = (Join-Path $truthDir "issues.json")
        "supplement ground truth" = (Join-Path $truthDir "supplement.json")
        "visual corpus"          = (Join-Path $corpusDir "visual.json")
        "visual ground truth"    = (Join-Path $truthDir "visual.json")
        "baseline"               = (Join-Path $evalRoot "baseline.json")
    }
    $checked = New-Object System.Collections.ArrayList
    foreach ($entry in $requiredFiles.GetEnumerator() | Sort-Object Name) {
        if (-not (Test-Path -LiteralPath $entry.Value)) {
            throw "missing eval artifact: $($entry.Key) ($($entry.Value))"
        }
        [void]$checked.Add($entry.Key)
    }

    $requirements = Read-JsonFile -Path $requiredFiles["requirements corpus"] -Label "requirements corpus"
    $environment = Read-JsonFile -Path $requiredFiles["environment corpus"] -Label "environment corpus"
    $impact = Read-JsonFile -Path $requiredFiles["impact ground truth"] -Label "impact ground truth"
    $classification = Read-JsonFile -Path $requiredFiles["classification truth"] -Label "classification ground truth"
    $issuesTruth = Read-JsonFile -Path $requiredFiles["issues ground truth"] -Label "issues ground truth"
    $supplement = Read-JsonFile -Path $requiredFiles["supplement ground truth"] -Label "supplement ground truth"
    $visualCorpus = Read-JsonFile -Path $requiredFiles["visual corpus"] -Label "visual corpus"
    $visualTruth = Read-JsonFile -Path $requiredFiles["visual ground truth"] -Label "visual ground truth"
    $baseline = Read-JsonFile -Path $requiredFiles["baseline"] -Label "baseline"

    # Corpus version consistency is the freeze contract: a mismatch means a file
    # was edited without bumping the version and the gate must fail.
    $corpusVersion = [string]$requirements.corpusVersion
    if ([string]::IsNullOrWhiteSpace($corpusVersion)) {
        throw "requirements.json has no corpusVersion"
    }
    foreach ($pair in @(
            @{ Name = "environment.json"; Value = [string]$environment.corpusVersion },
            @{ Name = "impact.json"; Value = [string]$impact.corpusVersion },
            @{ Name = "classification.json"; Value = [string]$classification.corpusVersion },
            @{ Name = "issues.json"; Value = [string]$issuesTruth.corpusVersion },
            @{ Name = "supplement.json"; Value = [string]$supplement.corpusVersion },
            @{ Name = "visual.json (corpus)"; Value = [string]$visualCorpus.corpusVersion },
            @{ Name = "visual.json (ground truth)"; Value = [string]$visualTruth.corpusVersion },
            @{ Name = "baseline.json"; Value = [string]$baseline.corpusVersion }
        )) {
        if ($pair.Value -ne $corpusVersion) {
            throw "$($pair.Name) corpusVersion '$($pair.Value)' does not match '$corpusVersion'"
        }
    }

    if (@($requirements.requirements).Count -eq 0) {
        throw "requirements.json declares no requirements"
    }
    if (@($requirements.expected.designCoveredRequirementKeys).Count -eq 0) {
        throw "requirements.json declares no covered requirement keys"
    }
    if ([int]$requirements.expected.confirmedRequirements -ne @($requirements.requirements).Count) {
        throw "requirements.json expected.confirmedRequirements must equal the requirement count"
    }
    if (@($requirements.expected.caseTitles).Count -eq 0) {
        throw "requirements.json declares no case title contract"
    }

    if ([string]::IsNullOrWhiteSpace([string]$environment.fixture.databaseName) -or
        [string]$environment.fixture.databaseName -notmatch '^[a-z][a-z0-9_]*_eval$') {
        throw "environment.json fixture.databaseName must match ^[a-z][a-z0-9_]*_eval$"
    }
    foreach ($portKey in @("targetPort", "executorPort", "fakeLlmPort", "apiPort")) {
        $port = [int]$environment.fixture.$portKey
        if ($port -lt 1024 -or $port -gt 65535) {
            throw "environment.json fixture.$portKey is not a usable port"
        }
    }
    $fixtureBase = [System.Uri][string]$environment.environment.baseUrl
    if (-not $fixtureBase.IsAbsoluteUri -or $fixtureBase.Port -ne [int]$environment.fixture.targetPort) {
        throw "environment.environment.baseUrl must point at the fixture.targetPort"
    }
    if (@($environment.environment.allowedHosts) -notcontains "127.0.0.1") {
        throw "environment.environment.allowedHosts must include 127.0.0.1"
    }
    if ([string]::IsNullOrWhiteSpace([string]$environment.fixture.executorToken)) {
        throw "environment.json fixture.executorToken is empty"
    }

    if (@($impact.expected).Count -eq 0) { throw "impact.json declares no expected cases" }
    if ([string]$impact.repository.baseCommit -notmatch '^[0-9a-f]{40}$' -or
        [string]$impact.repository.headCommit -notmatch '^[0-9a-f]{40}$') {
        throw "impact.json repository commits are not 40-hex SHAs"
    }
    if (@($classification.injectedFailures).Count -eq 0) {
        throw "classification.json declares no injected failures"
    }
    if (@($issuesTruth.truePositives).Count -eq 0) {
        throw "issues.json declares no true positives"
    }
    if (@($supplement.expectedCases).Count -eq 0) {
        throw "supplement.json declares no expected cases"
    }
    if ([string]$visualCorpus.scenario.assertion.kind -ne "visual") {
        throw "visual.json scenario.assertion.kind must be visual"
    }
    if ([string]$visualTruth.metric -ne "visual_classification_accuracy") {
        throw "ground-truth/visual.json metric must be visual_classification_accuracy"
    }
    if (@($visualTruth.cases).Count -eq 0) {
        throw "ground-truth/visual.json declares no cases"
    }

    $baselineMetrics = @{}
    foreach ($name in $metricNames) {
        $property = $baseline.metrics.PSObject.Properties[$name]
        if ($null -eq $property) {
            throw "baseline.json is missing metric $name"
        }
        $value = 0.0
        if (-not [double]::TryParse([string]$property.Value, [ref]$value)) {
            throw "baseline.json metric $name is not a number"
        }
        if ($value -lt 0 -or $value -gt 1) {
            throw "baseline.json metric $name must be within [0,1]"
        }
        $baselineMetrics[$name] = $value
    }

    $envValues = Read-DotEnv -Path (Join-Path $repoRoot ".env")
    $databaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_DATABASE_URL"
    $adminUsername = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_USERNAME"
    $adminPassword = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_PASSWORD"

    $databaseUri = $null
    if (-not [System.Uri]::TryCreate($databaseUrl, [System.UriKind]::Absolute, [ref]$databaseUri)) {
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }
    $scheme = $databaseUri.Scheme.ToLowerInvariant()
    if ($scheme -ne "postgres" -and $scheme -ne "postgresql") {
        throw "EZTEST_DATABASE_URL must use the postgres scheme"
    }
    if ([string]::IsNullOrWhiteSpace($databaseUri.Host)) {
        throw "EZTEST_DATABASE_URL must include a host"
    }
    $dbHost = $databaseUri.Host.ToLowerInvariant()
    if ($dbHost -ne "localhost" -and $dbHost -ne "127.0.0.1" -and $dbHost -ne "::1") {
        throw "the evaluation database must run on the local PostgreSQL host; refusing a remote target"
    }
    $userInfo = $databaseUri.UserInfo
    $separator = $userInfo.IndexOf(":")
    if ($separator -lt 0) {
        throw "EZTEST_DATABASE_URL must contain a user and password"
    }
    $dbUser = [System.Uri]::UnescapeDataString($userInfo.Substring(0, $separator))
    $dbPassword = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
    $dbPort = if ($databaseUri.Port -gt 0) { $databaseUri.Port } else { 5432 }
    $devDbName = [System.Uri]::UnescapeDataString($databaseUri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($devDbName)) {
        throw "EZTEST_DATABASE_URL must include a database name"
    }
    $evalDbName = [string]$environment.fixture.databaseName
    if ($devDbName -eq $evalDbName) {
        throw "EZTEST_DATABASE_URL already points at the evaluation database '$evalDbName'; point it at the development database instead"
    }
    # pgx applies query parameters over the URL path: reject a target override so
    # the checked eval database is the one actually created and dropped.
    $query = $databaseUri.Query
    if (-not [string]::IsNullOrWhiteSpace($query)) {
        foreach ($pair in ($query.TrimStart([char]"?")) -split "&") {
            if ([string]::IsNullOrWhiteSpace($pair)) { continue }
            $equals = $pair.IndexOf("=")
            $key = if ($equals -ge 0) { $pair.Substring(0, $equals) } else { $pair }
            $key = [System.Uri]::UnescapeDataString($key).Trim().ToLowerInvariant()
            if ($key -eq "dbname" -or $key -eq "database" -or $key -eq "host" -or $key -eq "port") {
                throw "EZTEST_DATABASE_URL must not override the connection target with '$key'"
            }
        }
    }
    $queryIndex = $databaseUrl.IndexOf("?")
    if ($queryIndex -ge 0) {
        $urlBase = $databaseUrl.Substring(0, $queryIndex)
        $urlQuery = $databaseUrl.Substring($queryIndex)
    } else {
        $urlBase = $databaseUrl
        $urlQuery = ""
    }
    $slashIndex = $urlBase.LastIndexOf("/")
    if ($slashIndex -lt 0) {
        throw "EZTEST_DATABASE_URL does not contain a database name"
    }
    $evalDatabaseUrl = $urlBase.Substring(0, $slashIndex + 1) + $evalDbName + $urlQuery

    $goExe = Get-CommandExe -Name "go.exe" -PreferredPath (Join-Path $repoRoot ".tools\go\bin\go.exe")
    $nodeExe = Get-CommandExe -Name "node.exe" -PreferredPath ""
    $psqlExe = Get-CommandExe -Name "psql.exe" -PreferredPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe")
    $gitExe = Get-CommandExe -Name "git.exe" -PreferredPath (Join-Path $repoRoot ".tools\git\bin\git.exe")
    foreach ($relative in @(
            "executor\src\server.mjs",
            "scripts\fixture-target.cjs",
            "scripts\fake-llm.cjs",
            "scripts\fixture-codeintel-repo.ps1"
        )) {
        if (-not (Test-Path -LiteralPath (Join-Path $repoRoot $relative))) {
            throw "missing evaluation tool: $relative"
        }
    }
    if (-not (Test-Path -LiteralPath (Join-Path $repoRoot "executor\node_modules"))) {
        throw "executor/node_modules is missing; run pnpm install in executor first"
    }
    $scriptHost = Join-Path $PSHOME "pwsh.exe"
    if (-not (Test-Path -LiteralPath $scriptHost)) { $scriptHost = Join-Path $PSHOME "powershell.exe" }
    if (-not (Test-Path -LiteralPath $scriptHost)) { throw "cannot locate a PowerShell host to run helper scripts" }

    # The visual scenario is evaluated through the production classifier here so
    # both the deterministic gate and -DryRun reject a drifted corpus/classifier.
    $visualEval = Invoke-VisualEvaluation -GoExe $goExe -RepoRoot $repoRoot

    if ($RealLLM) {
        if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
            throw "real-LLM evaluation is gated: set EZTEST_RUN_REAL_LLM=1 to run it"
        }
        if (-not $envValues.ContainsKey("llm_key") -or [string]::IsNullOrWhiteSpace([string]$envValues["llm_key"])) {
            throw "real-LLM evaluation requires llm_key in .env (the key is never printed)"
        }
        if ([string]::IsNullOrWhiteSpace([string]$envValues["llm_base_url"])) {
            throw "real-LLM evaluation requires llm_base_url in .env"
        }
    }

    return [pscustomobject]@{
        CorpusVersion    = $corpusVersion
        Requirements     = $requirements
        Environment      = $environment
        ImpactTruth      = $impact
        Classification   = $classification
        IssuesTruth      = $issuesTruth
        SupplementTruth  = $supplement
        VisualCorpus     = $visualCorpus
        VisualTruth      = $visualTruth
        VisualEval       = $visualEval
        Baseline         = $baseline
        BaselineMetrics  = $baselineMetrics
        EnvValues        = $envValues
        DatabaseUrl      = $databaseUrl
        EvalDatabaseUrl  = $evalDatabaseUrl
        EvalDbName       = $evalDbName
        DevDbName        = $devDbName
        DbHost           = $dbHost
        DbPort           = $dbPort
        DbUser           = $dbUser
        DbPassword       = $dbPassword
        AdminUsername    = $adminUsername
        AdminPassword    = $adminPassword
        GoExe            = $goExe
        NodeExe          = $nodeExe
        PsqlExe          = $psqlExe
        GitExe           = $gitExe
        ScriptHost       = $scriptHost
        CheckedArtifacts = @($checked)
    }
}

# ---------------------------------------------------------------------------
# Process/HTTP helpers (the evaluation uses its own ports and never touches a
# developer process).
# ---------------------------------------------------------------------------
function Test-PortFree {
    param([int]$Port)
    $listeners = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue
    return (-not $listeners)
}

function Stop-ProcessTree {
    param($Process)
    if ($null -eq $Process) { return }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $Process.Refresh()
        if (-not $Process.HasExited) {
            & taskkill.exe /PID $Process.Id /T /F 2>$null | Out-Null
        }
    } catch {
        # Best-effort cleanup.
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Wait-ForHttp {
    param([string]$Url, [int]$TimeoutSeconds, $Process, [string]$Label)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($null -ne $Process) {
            $Process.Refresh()
            if ($Process.HasExited) {
                throw "$Label exited early with code $($Process.ExitCode)"
            }
        }
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 10
            if ($response.StatusCode -eq 200) { return }
        } catch {
            # Not ready yet.
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Timed out waiting for $Label at $Url"
}

function Start-EvalNodeProcess {
    param([string]$ScriptPath, [string]$LogName, [string]$Label)
    $outLog = Join-Path $tmpDir "$LogName.out.log"
    $errLog = Join-Path $tmpDir "$LogName.err.log"
    Remove-Item -LiteralPath $outLog, $errLog -ErrorAction SilentlyContinue
    return Start-Process -FilePath $pre.NodeExe `
        -ArgumentList "`"$ScriptPath`"" `
        -WorkingDirectory $repoRoot `
        -RedirectStandardOutput $outLog `
        -RedirectStandardError $errLog `
        -WindowStyle Hidden `
        -PassThru
}

function New-EvalDatabase {
    param($Pre)
    $env:PGPASSWORD = $Pre.DbPassword
    $maintenanceDb = "postgres"
    $probeArgs = @("-h", $Pre.DbHost, "-p", "$($Pre.DbPort)", "-U", $Pre.DbUser, "-d", $maintenanceDb, "-tA", "-c", "SELECT 1")
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $Pre.PsqlExe @probeArgs *> $null
        $probeExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($probeExit -ne 0) {
        # Fall back to the configured database as the maintenance connection.
        $maintenanceDb = $Pre.DevDbName
    }
    $dbLog = Join-Path $tmpDir "database.log"
    # DROP DATABASE cannot run inside a transaction block, so it needs its own
    # psql invocation; CREATE DATABASE follows separately.
    $dropSql = "DROP DATABASE IF EXISTS `"$($Pre.EvalDbName)`" WITH (FORCE);"
    $createSql = "CREATE DATABASE `"$($Pre.EvalDbName)`";"
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $Pre.PsqlExe -h $Pre.DbHost -p "$($Pre.DbPort)" -U $Pre.DbUser -d $maintenanceDb -v ON_ERROR_STOP=1 -c $dropSql *> $dbLog
        $dropExit = $LASTEXITCODE
        if ($dropExit -eq 0) {
            & $Pre.PsqlExe -h $Pre.DbHost -p "$($Pre.DbPort)" -U $Pre.DbUser -d $maintenanceDb -v ON_ERROR_STOP=1 -c $createSql *>> $dbLog
            $createExit = $LASTEXITCODE
        } else {
            $createExit = $dropExit
        }
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($createExit -ne 0) {
        Write-LogTail -Path $dbLog -Label "database preparation" -Lines 40
        throw "could not recreate the '$($Pre.EvalDbName)' database; the configured PostgreSQL role needs CREATEDB"
    }
}

function New-FixtureCoordinates {
    param($Pre)
    $fixtureRepo = Join-Path $tmpRoot "p4-e2e-repo"
    if (-not (Test-Path -LiteralPath (Join-Path $fixtureRepo ".git"))) {
        & $Pre.ScriptHost -NoProfile -ExecutionPolicy Bypass -File (Join-Path $scriptsDir "fixture-codeintel-repo.ps1") *> (Join-Path $tmpDir "fixture-repo.log")
        if ($LASTEXITCODE -ne 0) {
            Write-LogTail -Path (Join-Path $tmpDir "fixture-repo.log") -Label "fixture repository" -Lines 40
            throw "scripts/fixture-codeintel-repo.ps1 failed"
        }
    }
    $baseCommit = ((& $Pre.GitExe -C $fixtureRepo rev-parse HEAD~1 2>$null | Out-String).Trim())
    $headCommit = ((& $Pre.GitExe -C $fixtureRepo rev-parse HEAD 2>$null | Out-String).Trim())
    $branch = ((& $Pre.GitExe -C $fixtureRepo rev-parse --abbrev-ref HEAD 2>$null | Out-String).Trim())
    if ($baseCommit -notmatch '^[0-9a-f]{40}$' -or $headCommit -notmatch '^[0-9a-f]{40}$') {
        throw "could not resolve the deterministic fixture commits"
    }
    if ([string]::IsNullOrWhiteSpace($branch) -or $branch -eq "HEAD") { $branch = "main" }
    return [pscustomobject]@{
        Path       = $fixtureRepo
        BaseCommit = $baseCommit
        HeadCommit = $headCommit
        Branch     = $branch
    }
}

# The runner is embedded so the evaluation stays a one-file deliverable and the
# acceptance script can execute it without a second tracked script. It talks to
# the real API/fixture/executor only; every metric is derived from the API
# responses and the committed ground truth.
$runnerScript = @'
import { readFileSync, writeFileSync } from "node:fs";

const API = process.env.P8_EVAL_API_URL;
const FAKE = process.env.P8_EVAL_FAKE_LLM_URL;
const FIXTURE = process.env.P8_EVAL_FIXTURE_URL;
const MODE = process.env.P8_EVAL_MODE === "real_llm" ? "real_llm" : "deterministic";
const DETERMINISTIC = MODE === "deterministic";
const observationPath = process.env.P8_EVAL_OBSERVATION;
const resultPath = process.env.P8_EVAL_RESULT;

function readJson(path) {
  return JSON.parse(readFileSync(path, "utf8"));
}

const requirementsCorpus = readJson(process.env.P8_EVAL_REQUIREMENTS);
const environmentCorpus = readJson(process.env.P8_EVAL_ENVIRONMENT);
const baselineFile = readJson(process.env.P8_EVAL_BASELINE);
const impactTruth = readJson(process.env.P8_EVAL_GT_IMPACT);
const classificationTruth = readJson(process.env.P8_EVAL_GT_CLASSIFICATION);
const issuesTruth = readJson(process.env.P8_EVAL_GT_ISSUES);
const supplementTruth = readJson(process.env.P8_EVAL_GT_SUPPLEMENT);
// The visual metric is produced by backend/internal/execution/evalvisual from
// eval/corpus/visual.json + eval/ground-truth/visual.json through the production
// classifier; the runner only records it next to the six §9.1 metrics.
const visualResultPath = process.env.P8_EVAL_VISUAL_RESULT;
const visualEval = visualResultPath ? readJson(visualResultPath) : null;

const startedAt = new Date();
const observation = {
  corpusVersion: requirementsCorpus.corpusVersion,
  mode: MODE,
  startedAt: startedAt.toISOString(),
  phases: [],
  counts: {},
};

function phase(name, detail) {
  observation.phases.push({ name, at: new Date().toISOString(), ...(detail || {}) });
  console.log("[eval] " + name);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

let token = "";

async function api(method, path, body, expected) {
  const allowed = expected || [200, 201, 202, 204];
  const headers = {};
  if (token !== "") headers.Authorization = "Bearer " + token;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const response = await fetch(API + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  let parsed = null;
  try {
    parsed = text === "" ? null : JSON.parse(text);
  } catch {
    parsed = null;
  }
  if (!allowed.includes(response.status)) {
    const code = parsed?.error?.code ?? "";
    const reason = parsed?.error?.details?.reason ?? "";
    throw new Error(`${method} ${path} -> HTTP ${response.status} ${code} ${reason}`.trim());
  }
  return parsed?.data;
}

async function poll(label, fn, done, timeoutMs, intervalMs) {
  const timeout = timeoutMs ?? 240000;
  const interval = intervalMs ?? 1000;
  const deadline = Date.now() + timeout;
  let last = null;
  while (Date.now() < deadline) {
    last = await fn();
    if (done(last)) return last;
    await sleep(interval);
  }
  const summary = JSON.stringify(last);
  throw new Error(`${label} timed out after ${timeout}ms (last: ${summary === undefined ? "none" : summary.slice(0, 300)})`);
}

async function setFakeMode(kind, mode) {
  const response = await fetch(`${FAKE}/__${kind}-mode`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ mode }),
  });
  if (!response.ok) throw new Error(`could not switch the fake LLM ${kind} mode to ${mode}`);
}

function suffixCounts() {
  return {
    confirmedRequirements: 0,
    coveredRequirements: 0,
    impactExpected: 0,
    impactHit: 0,
    impactSelected: 0,
    injectedFailures: 0,
    correctClassifications: 0,
    aiIssues: 0,
    truePositiveIssues: 0,
    supplementCases: 0,
    supplementHits: 0,
  };
}

async function login() {
  const data = await api(
    "POST",
    "/api/auth/login",
    { username: process.env.P8_EVAL_ADMIN_USERNAME, password: process.env.P8_EVAL_ADMIN_PASSWORD },
    [200],
  );
  token = String(data.token ?? "");
  if (token === "") throw new Error("admin login did not return a token");
}

async function seedVersion() {
  const system = await api("POST", "/api/resources/systems", {
    key: "p8-eval-system",
    name: "P8 evaluation system",
    kind: "web",
    baseUrl: environmentCorpus.environment.baseUrl,
  });
  const environment = await api("POST", `/api/resources/systems/${system.id}/environments`, {
    key: environmentCorpus.environment.key,
    name: environmentCorpus.environment.name,
    kind: environmentCorpus.environment.kind,
    baseUrl: environmentCorpus.environment.baseUrl,
    allowedHosts: environmentCorpus.environment.allowedHosts,
  });
  const repo = await api("POST", "/api/resources/code-repos", {
    key: "p8-eval-repo",
    name: "P8 evaluation fixture repo",
    localPath: process.env.P8_EVAL_FIXTURE_REPO,
    fixedBranch: process.env.P8_EVAL_FIXTURE_BRANCH,
  });
  const version = await api("POST", "/api/versions", {
    name: requirementsCorpus.version.name,
    label: requirementsCorpus.version.label,
    description: requirementsCorpus.version.description,
    systemId: system.id,
    environmentId: environment.id,
    codeRepositoryId: repo.id,
    codeBranch: process.env.P8_EVAL_FIXTURE_BRANCH,
    codeBaselineCommit: process.env.P8_EVAL_BASE_COMMIT,
  });
  const requirements = [];
  for (const requirement of requirementsCorpus.requirements) {
    const created = await api("POST", `/api/versions/${version.id}/requirements`, {
      title: requirement.title,
      description: requirement.description,
      acceptanceCriteria: requirement.acceptanceCriteria,
      priority: requirement.priority,
    });
    requirements.push({ key: requirement.key, id: created.id });
  }
  await api("POST", `/api/versions/${version.id}/start-clarification`, undefined);
  for (const requirement of requirements) {
    await api("POST", `/api/versions/${version.id}/requirements/${requirement.id}/confirm`, undefined);
  }
  await api("POST", `/api/versions/${version.id}/enter-design`, { acknowledgeUnclarified: false });
  observation.versionId = String(version.id);
  return { system, environment, repo, version, requirements };
}

async function indexRepository(repoId) {
  await api("POST", `/api/code/repos/${repoId}/index`, undefined, [202]);
  await poll(
    "repository index",
    () => api("GET", `/api/code/repos/${repoId}/index-runs/latest`, undefined, [200]),
    (run) => run && run.status === "done",
    180000,
    300,
  );
}

async function runMindmap(kind) {
  const run = await api("POST", `/api/versions/${observation.versionId}/mindmap/runs`, { kind }, [200, 202]);
  const runId = String(run.runId ?? run.id ?? "");
  if (runId === "") throw new Error(`mindmap ${kind} run did not return an id`);
  const settled = await poll(
    `mindmap ${kind} run`,
    () => api("GET", `/api/versions/${observation.versionId}/mindmap/runs/${runId}`, undefined, [200]),
    (current) => current && ["done", "failed", "degraded"].includes(current.status),
    240000,
    500,
  );
  if (settled.status !== "done") {
    throw new Error(`mindmap ${kind} run ended ${settled.status} (${settled.reason ?? "no reason"})`);
  }
  return runId;
}

async function getGraph() {
  return api("GET", `/api/versions/${observation.versionId}/mindmap`, undefined, [200]);
}

async function approveBaseline() {
  await runMindmap("review");
  await api("POST", `/api/versions/${observation.versionId}/mindmap/submit-review`, {}, [200]);
  const approved = await api(
    "POST",
    `/api/versions/${observation.versionId}/mindmap/review`,
    { decision: "approve", acknowledgeFindings: true },
    [200],
  );
  if (approved.baseline == null) throw new Error("mindmap approval froze no baseline");
  return approved.baseline;
}

async function analyseImpact() {
  if (DETERMINISTIC) await setFakeMode("p4", "impact-ai");
  const started = await api("POST", `/api/versions/${observation.versionId}/code/analyses`, {}, [200, 202]);
  observation.analysisId = String(started.id ?? "");
  const analysis = await poll(
    "P4 impact analysis",
    async () => {
      const response = await fetch(API + `/api/versions/${observation.versionId}/code/analyses/latest`, {
        headers: { Authorization: "Bearer " + token },
      });
      if (response.status === 404) return null;
      const payload = await response.json();
      return payload?.data ?? null;
    },
    (current) => current && ["done", "failed", "degraded"].includes(current.status),
    300000,
    500,
  );
  if (analysis.status !== "done") {
    if (DETERMINISTIC) {
      throw new Error(`P4 impact analysis ended ${analysis.status}; the deterministic gate cannot measure impact`);
    }
    return null;
  }
  return analysis;
}

function selectedRequirementCandidates(analysis) {
  if (analysis === null) return new Map();
  const selected = new Map();
  for (const candidate of analysis.candidates ?? []) {
    if (candidate.subjectKind !== "requirement" || candidate.selected !== true) continue;
    selected.set(String(candidate.subjectId), candidate);
  }
  return selected;
}

function measureImpact(graph, analysis) {
  const expected = impactTruth.expected;
  const selected = selectedRequirementCandidates(analysis);
  const cases = (graph.nodes ?? []).filter((node) => node.kind === "case");
  let hit = 0;
  for (const truth of expected) {
    const node = cases.find((candidate) => candidate.title === truth.caseTitle);
    if (!node) continue;
    const requirementSources = (node.sources ?? [])
      .filter((source) => source.refKind === "requirement")
      .map((source) => String(source.refId));
    if (requirementSources.some((id) => selected.has(id))) hit += 1;
  }
  return { expected: expected.length, hit, selected: selected.size };
}

async function setEnvironmentExecution(seed) {
  await api(
    "PATCH",
    `/api/resources/environments/${seed.environment.id}`,
    {
      allowAutoLogin: true,
      loginCheck: { url: "/p6/home", expectStatus: [200], bodyContains: "P6 Home" },
    },
    [200],
  );
  const loginUrl = `${FIXTURE}/p6/login`;
  const flow = await api("POST", "/api/resources/auth-flows", {
    system_id: String(seed.system.id),
    name: "P8 evaluation login",
    kind: "form",
    definition: {
      login_url: loginUrl,
      username_selector: "#username",
      password_selector: "#password",
      submit_selector: "button[type=submit]",
      success: { url_contains: "/p6/home" },
      failure: { invalid_selector: ".login-error", mfa_selector: "#mfa" },
      timeout_ms: 10000,
    },
    enabled: true,
  });
  const account = await api("POST", "/api/resources/accounts", {
    key: environmentCorpus.accounts[0].key,
    label: environmentCorpus.accounts[0].label,
    username: environmentCorpus.accounts[0].username,
    kind: "human",
  });
  const access = await api("POST", `/api/resources/accounts/${account.id}/accesses`, {
    systemId: seed.system.id,
    authType: "password",
    concurrencyLimit: 1,
    credential: "fixture-pass",
    usernameOverride: "fixture",
    loginUrl,
    login_flow_id: flow.id,
    enabled: true,
  });

  const loginResponse = await fetch(`${FIXTURE}/p6/login`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ username: "fixture", password: "fixture-pass" }).toString(),
    redirect: "manual",
  });
  if (loginResponse.status !== 302) {
    throw new Error(`fixture login returned HTTP ${loginResponse.status}, expected 302`);
  }
  const cookies =
    typeof loginResponse.headers.getSetCookie === "function" ? loginResponse.headers.getSetCookie() : [];
  let cookie = "";
  for (const raw of cookies.length > 0 ? cookies : [loginResponse.headers.get("set-cookie") ?? ""]) {
    for (const part of raw.split(",")) {
      const pair = part.split(";")[0]?.trim() ?? "";
      const separator = pair.indexOf("=");
      if (separator > 0 && pair.slice(0, separator) === "p6_session") {
        cookie = pair.slice(separator + 1);
      }
    }
  }
  if (cookie === "") throw new Error("fixture login did not set the p6_session cookie");
  const fixtureUrl = new URL(FIXTURE);
  await api(
    "POST",
    `/api/resources/accesses/${access.id}/sessions`,
    {
      environmentId: seed.environment.id,
      kind: "storage_state",
      storageState: {
        cookies: [
          {
            name: "p6_session",
            value: cookie,
            domain: fixtureUrl.hostname,
            path: "/",
            expires: -1,
            httpOnly: true,
            secure: false,
            sameSite: "Lax",
          },
        ],
        origins: [{ origin: fixtureUrl.origin, localStorage: [], sessionStorage: [] }],
      },
      note: "P8 evaluation imported fixture session",
    },
    [201],
  );
}

async function runBaselineBatch() {
  if (DETERMINISTIC) await setFakeMode("p6", "fail");
  const batch = await api("POST", `/api/versions/${observation.versionId}/batches`, {}, [201]);
  observation.batchId = String(batch.id);
  const done = await poll(
    "baseline batch",
    () => api("GET", `/api/versions/${observation.versionId}/batches/${batch.id}`, undefined, [200]),
    (current) => current && ["done", "failed", "cancelled"].includes(current.status),
    600000,
    1000,
  );
  if (done.status !== "done") {
    throw new Error(`baseline batch ended ${done.status} (${done.failure_reason ?? "no reason"})`);
  }
  // The supplement batch must execute with the normal planner.
  if (DETERMINISTIC) await setFakeMode("p6", "normal");
  return done;
}

async function caseRuns(batchId) {
  return api("GET", `/api/versions/${observation.versionId}/batches/${batchId}/case-runs?limit=100`, undefined, [200]);
}

function measureClassification(runs) {
  const injected = classificationTruth.injectedFailures;
  let correct = 0;
  for (const truth of injected) {
    const run = runs.find((candidate) => candidate.title === truth.caseTitle);
    if (run && run.status === "failed" && run.failure_class === truth.expectedFailureClass) correct += 1;
  }
  return { injected: injected.length, correct };
}

async function waitAnalyses(batchId) {
  let analyses = [];
  try {
    analyses = await poll(
      "P7 failure analysis and issue triage",
      () => api("GET", `/api/versions/${observation.versionId}/batches/${batchId}/analysis`, undefined, [200]),
      (rows) =>
        Array.isArray(rows) &&
        ["failure", "issue_triage"].every((kind) => {
          const run = rows.find((candidate) => candidate.kind === kind);
          return run && ["done", "failed", "degraded"].includes(run.status);
        }),
      300000,
      500,
    );
  } catch (error) {
    if (DETERMINISTIC) throw error;
    return;
  }
  for (const kind of ["failure", "issue_triage"]) {
    const run = analyses.find((candidate) => candidate.kind === kind);
    if (run && run.status !== "done" && DETERMINISTIC) {
      throw new Error(`${kind} analysis ended ${run.status}`);
    }
  }
}

async function measureIssues() {
  let issues = [];
  try {
    issues = await poll(
      "AI issue materialization",
      () => api("GET", `/api/versions/${observation.versionId}/issues?limit=100`, undefined, [200]),
      (rows) => Array.isArray(rows) && rows.filter((issue) => issue.source === "ai").length >= (issuesTruth.expectedAIIssues ?? 1),
      240000,
      500,
    );
  } catch (error) {
    if (DETERMINISTIC) throw error;
    issues = await api("GET", `/api/versions/${observation.versionId}/issues?limit=100`, undefined, [200]);
  }
  const aiIssues = (issues ?? []).filter((issue) => issue.source === "ai");
  const truthTitles = new Set(issuesTruth.truePositives.map((truth) => truth.title));
  const truePositives = aiIssues.filter((issue) => truthTitles.has(issue.title)).length;
  return { aiIssues: aiIssues.length, truePositives };
}

async function waitSupplementBatch() {
  let batches = [];
  try {
    batches = await poll(
      "supplement child batch",
      () => api("GET", `/api/versions/${observation.versionId}/batches?limit=100`, undefined, [200]),
      (rows) =>
        Array.isArray(rows) &&
        rows.some((batch) => batch.kind === "supplement" && batch.parentBatchId === observation.batchId),
      600000,
      1000,
    );
  } catch (error) {
    if (DETERMINISTIC) throw error;
    return null;
  }
  const child = batches.find(
    (batch) => batch.kind === "supplement" && batch.parentBatchId === observation.batchId,
  );
  if (!child) {
    if (DETERMINISTIC) throw new Error("no supplement child batch appeared");
    return null;
  }
  const done = await poll(
    "supplement batch",
    () => api("GET", `/api/versions/${observation.versionId}/batches/${child.id}`, undefined, [200]),
    (current) => current && ["done", "failed", "cancelled"].includes(current.status),
    600000,
    1000,
  );
  if (done.status !== "done") {
    if (DETERMINISTIC) throw new Error(`supplement batch ended ${done.status}`);
    return null;
  }
  observation.supplementBatchId = String(done.id);
  return done;
}

async function waitVersionCompleted() {
  try {
    await poll(
      "version completion",
      () => api("GET", `/api/versions/${observation.versionId}`, undefined, [200]),
      (current) => current && current.status === "completed",
      600000,
      1000,
    );
  } catch (error) {
    if (DETERMINISTIC) throw error;
  }
  try {
    await poll(
      "batch report",
      () => api("GET", `/api/versions/${observation.versionId}/reports?limit=100`, undefined, [200]),
      (rows) => Array.isArray(rows) && rows.length > 0,
      240000,
      1000,
    );
  } catch (error) {
    if (DETERMINISTIC) throw error;
  }
}

function measureSupplement() {
  const nodes = (observation.supplementGraph?.nodes ?? []).filter(
    (node) => node.kind === "case" && node.source === "ai_supplement",
  );
  const truthTitles = new Set(supplementTruth.expectedCases.map((entry) => entry.title));
  const hits = nodes.filter((node) => truthTitles.has(node.title)).length;
  return { cases: nodes.length, hits };
}

async function main() {
  await login();
  const seed = await seedVersion();
  phase("seed", { versionId: observation.versionId });
  // The execution batch leases the imported fixture session; without it every
  // case run is blocked(session, no_session) before the agent can start.
  await setEnvironmentExecution(seed);
  phase("execution-session");

  const designRun = await runMindmap("design");
  phase("design", { runId: designRun });
  let graph = await getGraph();
  const confirmed = new Set(seed.requirements.map((requirement) => String(requirement.id)));
  const covered = new Set();
  for (const entry of graph.coverage?.matrix ?? []) {
    if ((entry.caseIds ?? []).length > 0) covered.add(String(entry.requirementId));
  }
  const coveredConfirmed = [...covered].filter((id) => confirmed.has(id));
  observation.counts.confirmedRequirements = confirmed.size;
  observation.counts.coveredRequirements = coveredConfirmed.length;
  if (DETERMINISTIC) {
    for (const key of requirementsCorpus.expected.designCoveredRequirementKeys) {
      const requirement = seed.requirements.find((candidate) => candidate.key === key);
      if (requirement && !covered.has(String(requirement.id))) {
        throw new Error(`design coverage is missing the frozen requirement ${key}`);
      }
    }
  }
  if ((graph.nodes ?? []).filter((node) => node.kind === "case").length === 0) {
    throw new Error("design run materialized no cases");
  }
  phase("design-coverage", {
    confirmed: confirmed.size,
    covered: coveredConfirmed.length,
    cases: (graph.nodes ?? []).filter((node) => node.kind === "case").length,
  });

  // P4 impact: index the fixed repository and anchor R1 to the changed file, then
  // run the analysis after the baseline exists.
  await indexRepository(seed.repo.id);
  const firstRequirement = seed.requirements[0];
  await api("POST", `/api/versions/${observation.versionId}/code/anchors`, {
    subjectKind: "requirement",
    subjectId: String(firstRequirement.id),
    filePath: impactTruth.repository.anchorFile,
    startLine: impactTruth.repository.anchorLine,
    endLine: impactTruth.repository.anchorLine,
  });
  phase("code-index");

  await approveBaseline();
  phase("review-baseline");
  const analysis = await analyseImpact();
  const impact = measureImpact(await getGraph(), analysis);
  observation.counts.impactExpected = impact.expected;
  observation.counts.impactHit = impact.hit;
  observation.counts.impactSelected = impact.selected;
  phase("impact", impact);

  const baselineBatch = await runBaselineBatch();
  phase("baseline-batch", { batchId: observation.batchId, counts: baselineBatch.counts });
  const runs = await caseRuns(observation.batchId);
  const classification = measureClassification(runs);
  observation.counts.injectedFailures = classification.injected;
  observation.counts.correctClassifications = classification.correct;
  phase("classification", classification);

  await waitAnalyses(observation.batchId);
  const issues = await measureIssues();
  observation.counts.aiIssues = issues.aiIssues;
  observation.counts.truePositiveIssues = issues.truePositives;
  phase("issues", issues);

  await waitSupplementBatch();
  observation.supplementGraph = await getGraph();
  const supplement = measureSupplement();
  observation.counts.supplementCases = supplement.cases;
  observation.counts.supplementHits = supplement.hits;
  phase("supplement", supplement);

  await waitVersionCompleted();
  if (visualEval) {
    observation.visual = visualEval;
    phase("visual-classification", {
      metric: visualEval.metric,
      accuracy: visualEval.accuracy,
      correct: visualEval.correct,
      total: visualEval.total,
    });
    if (DETERMINISTIC && !visualEval.passed) {
      throw new Error(
        `visual_classification_accuracy ${visualEval.accuracy} is below the ${visualEval.threshold} threshold`,
      );
    }
  }
  phase("report");

  const sample = observation.counts;
  const finishedAt = new Date();
  observation.finishedAt = finishedAt.toISOString();
  observation.sample = sample;
  writeFileSync(observationPath, JSON.stringify(observation, null, 2), "utf8");

  const run = await api(
    "POST",
    "/api/quality/eval-runs",
    {
      mode: MODE,
      corpusVersion: requirementsCorpus.corpusVersion,
      model:
        MODE === "deterministic"
          ? environmentCorpus.fixture.fakeLlmModel
          : process.env.P8_EVAL_REAL_MODEL || "real-llm",
      startedAt: observation.startedAt,
      finishedAt: observation.finishedAt,
      baseline: baselineFile.metrics,
      sample,
      summary: {
        scenario: "p8-eval",
        deterministic: DETERMINISTIC,
        versionId: observation.versionId,
        baselineBatchId: observation.batchId,
        supplementBatchId: observation.supplementBatchId ?? null,
        analysisId: observation.analysisId ?? null,
        visual: visualEval
          ? {
              metric: visualEval.metric,
              accuracy: visualEval.accuracy,
              threshold: visualEval.threshold,
              correct: visualEval.correct,
              total: visualEval.total,
              passed: visualEval.passed,
            }
          : null,
        corpusFiles: [
          "corpus/requirements.json",
          "corpus/environment.json",
          "corpus/visual.json",
          "ground-truth/impact.json",
          "ground-truth/classification.json",
          "ground-truth/issues.json",
          "ground-truth/supplement.json",
          "ground-truth/visual.json",
        ],
      },
    },
    [201],
  );

  const metrics = {};
  const regressions = [];
  for (const metric of run.metrics ?? []) {
    metrics[metric.name] = {
      value: metric.value,
      threshold: metric.threshold,
      passed: metric.passed,
      reason: metric.detail?.reason ?? "",
      numerator: metric.detail?.numerator ?? null,
      denominator: metric.detail?.denominator ?? null,
      baseline: metric.detail?.baseline ?? null,
    };
    if (metric.detail?.reason === "baseline_regression") regressions.push(metric.name);
  }
  // The §9.3 visual metric is a corpus-level classification detail. It never
  // replaces the six §9.1 metrics and is gated by the Go helper in preflight.
  if (visualEval) {
    metrics[visualEval.metric] = {
      value: visualEval.accuracy,
      threshold: visualEval.threshold,
      passed: visualEval.passed,
      reason: visualEval.passed ? "ok" : "below_threshold",
      numerator: visualEval.correct,
      denominator: visualEval.total,
      baseline: null,
    };
  }
  const gatePassed = run.status === "passed";
  const result = {
    ok: DETERMINISTIC ? gatePassed : true,
    mode: MODE,
    corpusVersion: requirementsCorpus.corpusVersion,
    runId: String(run.id),
    status: run.status,
    gatePassed,
    reportOnly: !DETERMINISTIC,
    metrics,
    regressions,
    visual: visualEval,
    baseline: baselineFile.metrics,
    observationPath,
    finishedAt: observation.finishedAt,
  };
  writeFileSync(resultPath, JSON.stringify(result, null, 2), "utf8");
  console.log("[eval] recorded quality run #" + result.runId + " status=" + result.status);
  if (DETERMINISTIC && !gatePassed) {
    process.exitCode = 1;
  }
}

try {
  await main();
} catch (error) {
  observation.finishedAt = new Date().toISOString();
  observation.error = String(error && error.message ? error.message : error);
  try {
    writeFileSync(observationPath, JSON.stringify(observation, null, 2), "utf8");
  } catch {
    // The directory is prepared by the caller; ignoring a write failure keeps the
    // original error as the reported one.
  }
  console.error("[eval] FAILED: " + observation.error);
  process.exitCode = 1;
}
'@

$exitCode = 0
try {
    Write-Step "Validating the evaluation corpus, baseline and toolchain"
    $pre = Invoke-Preflight
    Write-Host ("Corpus {0}: {1} requirements, {2} ground-truth groups; baseline {3} metrics." -f `
            $pre.CorpusVersion, @($pre.Requirements.requirements).Count, 4, $pre.BaselineMetrics.Count)
    Write-Host "Corpus/baseline/DB-target/toolchain preflight passed."

    if ($DryRun) {
        New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
        $dryRunPath = Join-Path $tmpDir "dry-run.json"
        $dryRunMode = "deterministic"
        if ($RealLLM) { $dryRunMode = "real_llm" }
        $dryRunCheckedAt = (Get-Date).ToUniversalTime().ToString("o")
        $dryRunReport = [ordered]@{}
        $dryRunReport["ok"] = $true
        $dryRunReport["mode"] = "dry_run"
        $dryRunReport["checkedAt"] = $dryRunCheckedAt
        $dryRunReport["corpusVersion"] = $pre.CorpusVersion
        $dryRunReport["artifacts"] = @($pre.CheckedArtifacts)
        $dryRunReport["baseline"] = $pre.BaselineMetrics
        $dryRunReport["visual"] = [ordered]@{
            metric    = [string]$pre.VisualEval.metric
            accuracy  = [double]$pre.VisualEval.accuracy
            threshold = [double]$pre.VisualEval.threshold
            cases     = [int]$pre.VisualEval.total
            passed    = [bool]$pre.VisualEval.passed
        }
        $dryRunReport["databaseTarget"] = [ordered]@{
            host         = $pre.DbHost
            port         = $pre.DbPort
            databaseName = $pre.EvalDbName
            targetSafe   = $true
        }
        $dryRunReport["toolchain"] = [ordered]@{
            go      = $pre.GoExe
            node    = $pre.NodeExe
            psql    = $pre.PsqlExe
            git     = $pre.GitExe
            mode    = $dryRunMode
            llmCall = $false
        }
        $json = $dryRunReport | ConvertTo-Json -Depth 8
        [System.IO.File]::WriteAllText($dryRunPath, $json, (New-Object System.Text.UTF8Encoding($false)))
        Write-Host "Wrote $dryRunPath"
        Write-Host "EVAL DRYRUN PASSED" -ForegroundColor Green
        exit 0
    }

    $mode = if ($RealLLM) { "real_llm" } else { "deterministic" }
    $fixture = [pscustomobject]@{
        TargetPort   = [int]$pre.Environment.fixture.targetPort
        ExecutorPort = [int]$pre.Environment.fixture.executorPort
        FakeLlmPort  = [int]$pre.Environment.fixture.fakeLlmPort
        ApiPort      = [int]$pre.Environment.fixture.apiPort
        ExecutorToken = [string]$pre.Environment.fixture.executorToken
        FakeModel    = [string]$pre.Environment.fixture.fakeLlmModel
        DatabaseName = [string]$pre.Environment.fixture.databaseName
    }

    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
    foreach ($port in @($fixture.ApiPort, $fixture.FakeLlmPort, $fixture.TargetPort, $fixture.ExecutorPort)) {
        if (-not (Test-PortFree -Port $port)) {
            throw "port $port is already in use; stop the conflicting process before running the evaluation"
        }
    }

    $dataRoot = Join-Path $tmpDir "data"
    foreach ($child in @("objects", "evidence", "exports", "reports", "attachments")) {
        New-Item -ItemType Directory -Force -Path (Join-Path $dataRoot $child) | Out-Null
    }
    $apiExe = Join-Path $tmpDir "eztest-eval-api.exe"
    $buildLog = Join-Path $tmpDir "build.log"
    Write-Step "Building the API"
    Push-Location $backendDir
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $pre.GoExe build -o $apiExe ./cmd/eztest *> $buildLog
        $buildExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        Pop-Location
    }
    if ($buildExit -ne 0) {
        Write-LogTail -Path $buildLog -Label "go build" -Lines 60
        throw "go build ./cmd/eztest failed"
    }

    Write-Step "Recreating the isolated $($fixture.DatabaseName) database"
    New-EvalDatabase -Pre $pre

    Write-Step "Preparing the deterministic fixture repository"
    $fixtureRepo = New-FixtureCoordinates -Pre $pre
    Write-Host ("Fixture commits base={0} head={1}." -f $fixtureRepo.BaseCommit.Substring(0, 8), $fixtureRepo.HeadCommit.Substring(0, 8))

    $fakeProcess = $null
    $fixtureProcess = $null
    $executorProcess = $null
    $apiProcess = $null
    try {
        $fakeBaseUrl = "http://127.0.0.1:$($fixture.FakeLlmPort)"
        if (-not $RealLLM) {
            Write-Step "Starting the deterministic fake LLM on port $($fixture.FakeLlmPort)"
            $env:EZTEST_FAKE_LLM_PORT = "$($fixture.FakeLlmPort)"
            $env:EZTEST_FAKE_LLM_P3 = "1"
            $env:EZTEST_FAKE_LLM_P4_MODE = "ok"
            $env:EZTEST_FAKE_LLM_P5_MODE = "normal"
            $env:EZTEST_FAKE_LLM_P6_MODE = "normal"
            $env:EZTEST_FAKE_LLM_P7_MODE = "normal"
            $fakeProcess = Start-EvalNodeProcess -ScriptPath (Join-Path $repoRoot "scripts\fake-llm.cjs") -LogName "fake-llm" -Label "fake LLM"
            Wait-ForHttp -Url "$fakeBaseUrl/v1/models" -TimeoutSeconds 30 -Process $fakeProcess -Label "fake LLM"
        }

        Write-Step "Starting the fixture target on port $($fixture.TargetPort)"
        $env:FIXTURE_PORT = "$($fixture.TargetPort)"
        $fixtureProcess = Start-EvalNodeProcess -ScriptPath (Join-Path $repoRoot "scripts\fixture-target.cjs") -LogName "fixture" -Label "fixture target"
        Wait-ForHttp -Url "http://127.0.0.1:$($fixture.TargetPort)/health" -TimeoutSeconds 30 -Process $fixtureProcess -Label "fixture target"

        Write-Step "Starting the Playwright executor on port $($fixture.ExecutorPort)"
        $env:EZTEST_EXECUTOR_ADDR = "127.0.0.1:$($fixture.ExecutorPort)"
        $env:EZTEST_EXECUTOR_TOKEN = $fixture.ExecutorToken
        $env:EZTEST_EXECUTOR_MAX_CONTEXTS = "4"
        $executorProcess = Start-EvalNodeProcess -ScriptPath (Join-Path $repoRoot "executor\src\server.mjs") -LogName "executor" -Label "executor"
        Wait-ForHttp -Url "http://127.0.0.1:$($fixture.ExecutorPort)/healthz" -TimeoutSeconds 90 -Process $executorProcess -Label "executor"

        Write-Step "Starting the evaluation API on port $($fixture.ApiPort)"
        $env:EZTEST_ENV = "dev"
        $env:EZTEST_DATABASE_URL = $pre.EvalDatabaseUrl
        $env:EZTEST_HTTP_ADDR = ":$($fixture.ApiPort)"
        $env:EZTEST_WEB_ORIGIN = "http://localhost:6868"
        $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
        $env:EZTEST_STORAGE_DIR = (Join-Path $dataRoot "objects")
        $env:EZTEST_EVIDENCE_DIR = (Join-Path $dataRoot "evidence")
        $env:EZTEST_EXPORT_DIR = (Join-Path $dataRoot "exports")
        $env:EZTEST_REPORT_DIR = (Join-Path $dataRoot "reports")
        $env:EZTEST_VERSION_ATTACHMENT_DIR = (Join-Path $dataRoot "attachments")
        $env:EZTEST_ADMIN_USERNAME = $pre.AdminUsername
        $env:EZTEST_ADMIN_PASSWORD = $pre.AdminPassword
        $env:EZTEST_EXECUTOR_URL = "http://127.0.0.1:$($fixture.ExecutorPort)"
        $env:EZTEST_EXECUTOR_TOKEN = $fixture.ExecutorToken
        $env:EZTEST_AUTONOMY_BATCHES_PER_TICK = "8"
        $env:EZTEST_EXEC_MAX_CONCURRENT_CASES = "1"
        $env:EZTEST_SNOWFLAKE_NODE_ID = "2"
        foreach ($key in @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE")) {
            if ($pre.EnvValues.ContainsKey($key) -and -not [string]::IsNullOrWhiteSpace([string]$pre.EnvValues[$key])) {
                Set-Item -Path ("Env:" + $key) -Value $pre.EnvValues[$key]
            }
        }
        if ($RealLLM) {
            $env:llm_base_url = Get-RequiredValue -Values $pre.EnvValues -Key "llm_base_url"
            $env:llm_key = Get-RequiredValue -Values $pre.EnvValues -Key "llm_key"
            $env:llm_model_name = [string]$pre.EnvValues["llm_model_name"]
        } else {
            $env:llm_base_url = $fakeBaseUrl
            $env:llm_key = "p8-eval-fake-llm"
            $env:llm_model_name = $fixture.FakeModel
        }
        Remove-Item Env:EZTEST_TEST_DATABASE_URL -ErrorAction SilentlyContinue

        $apiOut = Join-Path $tmpDir "api.out.log"
        $apiErr = Join-Path $tmpDir "api.err.log"
        Remove-Item -LiteralPath $apiOut, $apiErr -ErrorAction SilentlyContinue
        $apiProcess = Start-Process -FilePath $apiExe `
            -WorkingDirectory $backendDir `
            -RedirectStandardOutput $apiOut `
            -RedirectStandardError $apiErr `
            -WindowStyle Hidden `
            -PassThru
        $apiBaseUrl = "http://127.0.0.1:$($fixture.ApiPort)"
        Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 90 -Process $apiProcess -Label "evaluation API"
        $health = Invoke-RestMethod -Uri "$apiBaseUrl/api/health" -TimeoutSec 20
        if ($health.data.status -ne "ok" -or $health.data.db -ne "ok") {
            throw "evaluation API health is not ok"
        }
        if (-not [bool]$health.data.llm_configured) {
            if ($RealLLM) {
                throw "llm_configured=false; the real-LLM evaluation cannot start without a usable LLM configuration"
            }
            throw "llm_configured=false; the deterministic fake LLM configuration was not seeded"
        }

        Write-Step "Running the $mode evaluation chain"
        $runnerPath = Join-Path $tmpDir "runner.mjs"
        [System.IO.File]::WriteAllText($runnerPath, $runnerScript, (New-Object System.Text.UTF8Encoding($false)))
        $observationPath = Join-Path $tmpDir "observation.json"
        $resultPath = Join-Path $tmpDir "result.json"
        # The visual metric is computed from the committed corpus/ground truth by
        # the Go helper and included in the recorded observation/result.
        $visualEvalPath = Join-Path $tmpDir "visual-eval.json"
        [System.IO.File]::WriteAllText($visualEvalPath,
            ($pre.VisualEval | ConvertTo-Json -Depth 8),
            (New-Object System.Text.UTF8Encoding($false)))

        $env:P8_EVAL_API_URL = $apiBaseUrl
        $env:P8_EVAL_ADMIN_USERNAME = $pre.AdminUsername
        $env:P8_EVAL_ADMIN_PASSWORD = $pre.AdminPassword
        $env:P8_EVAL_FAKE_LLM_URL = $fakeBaseUrl
        $env:P8_EVAL_FIXTURE_URL = "http://127.0.0.1:$($fixture.TargetPort)"
        $env:P8_EVAL_FIXTURE_REPO = $fixtureRepo.Path
        $env:P8_EVAL_FIXTURE_BRANCH = $fixtureRepo.Branch
        $env:P8_EVAL_BASE_COMMIT = $fixtureRepo.BaseCommit
        $env:P8_EVAL_HEAD_COMMIT = $fixtureRepo.HeadCommit
        $env:P8_EVAL_MODE = $mode
        $env:P8_EVAL_REQUIREMENTS = Join-Path $corpusDir "requirements.json"
        $env:P8_EVAL_ENVIRONMENT = Join-Path $corpusDir "environment.json"
        $env:P8_EVAL_BASELINE = Join-Path $evalRoot "baseline.json"
        $env:P8_EVAL_GT_IMPACT = Join-Path $truthDir "impact.json"
        $env:P8_EVAL_GT_CLASSIFICATION = Join-Path $truthDir "classification.json"
        $env:P8_EVAL_GT_ISSUES = Join-Path $truthDir "issues.json"
        $env:P8_EVAL_GT_SUPPLEMENT = Join-Path $truthDir "supplement.json"
        $env:P8_EVAL_OBSERVATION = $observationPath
        $env:P8_EVAL_RESULT = $resultPath
        $env:P8_EVAL_VISUAL_RESULT = $visualEvalPath
        $env:P8_EVAL_REAL_MODEL = [string]$pre.EnvValues["llm_model_name"]

        $runnerLog = Join-Path $tmpDir "runner.log"
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        try {
            & $pre.NodeExe $runnerPath *> $runnerLog
            $runnerExit = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
        }
        Write-LogTail -Path $runnerLog -Label "evaluation runner" -Lines 40
        if ($runnerExit -ne 0) {
            Write-LogTail -Path $apiErr -Label "API stderr" -Lines 60
            throw "the evaluation runner failed with exit code $runnerExit (see .tmp/p8-eval/runner.log)"
        }
        if (-not (Test-Path -LiteralPath $resultPath)) {
            throw "the evaluation runner did not write $resultPath"
        }
        $result = Get-Content -LiteralPath $resultPath -Raw -Encoding UTF8 | ConvertFrom-Json

        Write-Step "Evaluation metrics"
        foreach ($name in $metricNames) {
            $metric = $result.metrics.$name
            if ($null -eq $metric) { continue }
            $verdict = if ([bool]$metric.passed) { "PASS" } else { "FAIL" }
            $counts = ""
            if ($null -ne $metric.numerator -and $null -ne $metric.denominator) {
                $counts = " ({0}/{1})" -f $metric.numerator, $metric.denominator
            }
            Write-Host ("  {0,-28} {1,8:P1}  threshold {2,6:P1}  {3}{4}" -f `
                    $name, [double]$metric.value, [double]$metric.threshold, $verdict, $counts)
        }
        if ($null -ne $result.visual) {
            $visualVerdict = if ([bool]$result.visual.passed) { "PASS" } else { "FAIL" }
            Write-Host ("  {0,-28} {1,8:P1}  threshold {2,6:P1}  {3} ({4}/{5})" -f `
                    [string]$result.visual.metric, [double]$result.visual.accuracy,
                    [double]$result.visual.threshold, $visualVerdict,
                    [int]$result.visual.correct, [int]$result.visual.total)
        }
        if (@($result.regressions).Count -gt 0) {
            Write-Host ("  baseline regressions: " + (@($result.regressions) -join ", ")) -ForegroundColor Yellow
        }
        Write-Host ("Evaluation run #{0} recorded with status '{1}'." -f $result.runId, $result.status)

        if ($mode -eq "deterministic") {
            if (-not [bool]$result.gatePassed) {
                throw "the deterministic evaluation gate failed (run #$($result.runId))"
            }
            Write-Host "EVAL PASSED" -ForegroundColor Green
        } else {
            Write-Host ("REAL LLM REPORT RECORDED (status '{0}'); the report never gates a release." -f $result.status)
            Write-Host "EVAL REPORT PASSED" -ForegroundColor Green
        }
    } finally {
        Stop-ProcessTree -Process $apiProcess
        Stop-ProcessTree -Process $executorProcess
        Stop-ProcessTree -Process $fixtureProcess
        Stop-ProcessTree -Process $fakeProcess
        Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
        Remove-Item Env:llm_key -ErrorAction SilentlyContinue
        Remove-Item Env:P8_EVAL_ADMIN_PASSWORD -ErrorAction SilentlyContinue
    }
} catch {
    Write-Host ""
    Write-Host "EVAL FAILED: $($_.Exception.Message)" -ForegroundColor Red
    if (-not [string]::IsNullOrWhiteSpace($_.ScriptStackTrace)) {
        Write-Host $_.ScriptStackTrace -ForegroundColor DarkGray
    }
    Write-LogTail -Path (Join-Path $tmpDir "api.err.log") -Label "API stderr" -Lines 60
    Write-LogTail -Path (Join-Path $tmpDir "executor.err.log") -Label "executor stderr" -Lines 40
    Write-LogTail -Path (Join-Path $tmpDir "fixture.err.log") -Label "fixture stderr" -Lines 20
    Write-LogTail -Path (Join-Path $tmpDir "fake-llm.err.log") -Label "fake LLM stderr" -Lines 20
    $exitCode = 1
}

exit $exitCode
