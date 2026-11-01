#requires -Version 5.1
<#
.SYNOPSIS
    P6 real-LLM evaluation report track (spec section 5.4, P6-D-12/P6-D-21).

.DESCRIPTION
    This is the 0.2 replacement of the 0.1 P8 deterministic evaluation gate.
    There is exactly one evaluation track: real_llm. Fake / deterministic modes
    do not exist, and no fake result can be recorded through this script.

    -DryRun is a static preflight: it validates the committed eval/v02 corpus,
    ground truth and baseline against the frozen P6 contract, the DB target,
    the LLM configuration and the toolchain. It never starts a service or
    calls an LLM.

    -Mode real-llm reads an observation produced by the P6 acceptance flow
    (real DeepSeek + real Chrome), derives the sample counts from that real
    data plus the committed ground truth, and POSTs mode=real_llm to
    /api/quality/eval-runs. Thresholds and baseline are report-only: a failed
    metric or a regression never makes this script fail. Only a broken
    API/DB/observation/authentication is an error.

    Outputs: .tmp/p6-eval/result.json and .tmp/p6-eval/observation.json.
    The script never prints .env credentials, database URLs or LLM keys.

.PARAMETER Mode
    Only real-llm is accepted. No fake/deterministic mode exists.

.PARAMETER DryRun
    Static/database/toolchain preflight only; no LLM call and no write.

.PARAMETER ApiBaseUrl
    API origin. Defaults to EZTEST_API_BASE_URL, then
    http://127.0.0.1:<EZTEST_API_PORT or 13333>.

.PARAMETER ObservationFile
    Real observation JSON from the P6 acceptance flow. Defaults to
    EZTEST_EVAL_OBSERVATION, then .tmp/p6-acceptance/observation.json.

.PARAMETER BearerToken
    Admin bearer token. Defaults to EZTEST_ADMIN_TOKEN; otherwise the script
    logs in with the admin username/password from .env (development seed
    defaults admin/admin123 when absent).

.PARAMETER RequestTimeoutSeconds
    Per-request HTTP timeout.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [ValidateSet("real-llm")]
    [string]$Mode = "real-llm",
    [switch]$DryRun,
    [string]$ApiBaseUrl = "",
    [string]$ObservationFile = "",
    [string]$BearerToken = "",
    [int]$RequestTimeoutSeconds = 60
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$evalRoot = Join-Path $repoRoot "eval\v02"
$corpusDir = Join-Path $evalRoot "corpus"
$truthDir = Join-Path $evalRoot "ground-truth"
$tmpDir = Join-Path $repoRoot ".tmp\p6-eval"

$script:RepoRoot = $repoRoot
$script:TmpDir = $tmpDir
$script:LogPath = Join-Path $tmpDir "eval.log"
$script:ApiBase = ""
$script:BearerToken = $BearerToken
$script:ObservationFile = $ObservationFile
$script:TimeoutSeconds = $RequestTimeoutSeconds
$script:DryRun = [bool]$DryRun

$metricNames = @(
    "design_coverage",
    "impact_recall",
    "impact_precision",
    "classification_accuracy",
    "issue_false_positive_rate",
    "supplement_hit_rate",
    "visual_classification_accuracy"
)

# Frozen spec section 5.3 real_llm report thresholds.
$specThresholds = [ordered]@{
    design_coverage                = 0.80
    impact_recall                  = 0.70
    impact_precision               = 0.60
    classification_accuracy        = 0.80
    issue_false_positive_rate      = 0.15
    supplement_hit_rate            = 0.50
    visual_classification_accuracy = 0.80
}

function Write-EvalLog {
    param([string]$Message)
    if ($script:DryRun) { return }
    if ([string]::IsNullOrWhiteSpace($script:LogPath)) { return }
    try {
        $parent = Split-Path -Parent $script:LogPath
        if (-not [string]::IsNullOrWhiteSpace($parent) -and -not (Test-Path -LiteralPath $parent)) {
            New-Item -ItemType Directory -Force -Path $parent | Out-Null
        }
        $line = "[{0}] {1}`r`n" -f (Get-Date).ToUniversalTime().ToString("o"), $Message
        $encoding = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::AppendAllText($script:LogPath, $line, $encoding)
    } catch {
        # Logging must never hide the original evaluation error.
    }
}

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
    Write-EvalLog -Message "STEP $Message"
}

function Write-Info {
    param([string]$Message)
    Write-Host "    $Message"
    Write-EvalLog -Message "INFO $Message"
}

function Write-WarnLine {
    param([string]$Message)
    Write-Host "    WARN: $Message" -ForegroundColor Yellow
    Write-EvalLog -Message "WARN $Message"
}

function Write-ErrorLine {
    param([string]$Message)
    Write-Host "ERROR: $Message" -ForegroundColor Red
    Write-EvalLog -Message "ERROR $Message"
}

function Resolve-RepoPath {
    param([string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path)) { return $Path }
    if ([System.IO.Path]::IsPathRooted($Path)) { return $Path }
    return (Join-Path $repoRoot $Path)
}

function Read-JsonFile {
    param([string]$Path, [string]$Label)
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "missing $Label at $Path"
    }
    try {
        return (Get-Content -LiteralPath $Path -Raw -Encoding UTF8 -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop)
    } catch {
        throw "$Label is not valid JSON ($Path): $($_.Exception.Message)"
    }
}

function Write-JsonFile {
    param([string]$Path, $Value)
    $parent = Split-Path -Parent $Path
    if (-not [string]::IsNullOrWhiteSpace($parent) -and -not (Test-Path -LiteralPath $parent)) {
        New-Item -ItemType Directory -Force -Path $parent | Out-Null
    }
    $json = $Value | ConvertTo-Json -Depth 14
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($Path, $json, $encoding)
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) {
        return $values
    }
    foreach ($rawLine in Get-Content -LiteralPath $Path -Encoding UTF8) {
        $line = $rawLine.Trim()
        if ($line.Length -eq 0 -or $line.StartsWith("#")) { continue }
        $separator = $line.IndexOf("=")
        if ($separator -lt 1) { continue }
        $key = $line.Substring(0, $separator).Trim()
        $value = $line.Substring($separator + 1).Trim()
        if ($value.Length -ge 2) {
            $first = $value.Substring(0, 1)
            $last = $value.Substring($value.Length - 1, 1)
            if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                $value = $value.Substring(1, $value.Length - 2)
            }
        }
        $values[$key] = $value
    }
    return $values
}

function Get-ConfigValue {
    param($EnvValues, [string]$Key, [string]$Default = "")
    $fromEnvironment = [Environment]::GetEnvironmentVariable($Key)
    if (-not [string]::IsNullOrWhiteSpace($fromEnvironment)) {
        return $fromEnvironment
    }
    if ($null -ne $EnvValues -and $EnvValues.ContainsKey($Key) -and -not [string]::IsNullOrWhiteSpace([string]$EnvValues[$Key])) {
        return [string]$EnvValues[$Key]
    }
    return $Default
}

function Get-DatabaseTarget {
    param([string]$DatabaseUrl)
    if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
        throw "EZTEST_DATABASE_URL is missing; the evaluation needs the P6 database"
    }
    $uri = $null
    if (-not [System.Uri]::TryCreate($DatabaseUrl, [System.UriKind]::Absolute, [ref]$uri)) {
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }
    $scheme = $uri.Scheme.ToLowerInvariant()
    if ($scheme -ne "postgres" -and $scheme -ne "postgresql") {
        throw "EZTEST_DATABASE_URL must use the postgres scheme"
    }
    if ([string]::IsNullOrWhiteSpace($uri.Host)) {
        throw "EZTEST_DATABASE_URL must include a host"
    }
    $hostName = $uri.Host.ToLowerInvariant()
    if (@("localhost", "127.0.0.1", "::1") -notcontains $hostName) {
        throw "the evaluation database must run on the local PostgreSQL host; refusing a remote target"
    }
    $userInfo = $uri.UserInfo
    $separator = $userInfo.IndexOf(":")
    if ($separator -lt 0) {
        throw "EZTEST_DATABASE_URL must contain a user and password"
    }
    $user = [System.Uri]::UnescapeDataString($userInfo.Substring(0, $separator))
    $password = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
    $port = if ($uri.Port -gt 0) { $uri.Port } else { 5432 }
    $database = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($database)) {
        throw "EZTEST_DATABASE_URL must include a database name"
    }
    return [pscustomobject]@{
        Host     = $hostName
        Port     = $port
        User     = $user
        Password = $password
        Database = $database
    }
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

function Invoke-PsqlQuery {
    param($Target, [string]$Sql)
    $previous = $ErrorActionPreference
    $hadPassword = Test-Path Env:PGPASSWORD
    $previousPassword = $env:PGPASSWORD
    $output = @()
    $exitCode = 1
    try {
        $env:PGPASSWORD = $Target.Password
        $ErrorActionPreference = "SilentlyContinue"
        $psqlArgs = @(
            "-h", $Target.Host, "-p", "$($Target.Port)", "-U", $Target.User,
            "-d", $Target.Database, "-v", "ON_ERROR_STOP=1", "-tA", "-c", $Sql
        )
        $output = & $script:PsqlExe @psqlArgs 2>&1
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        if ($hadPassword) {
            $env:PGPASSWORD = $previousPassword
        } else {
            Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
        }
    }
    if ($exitCode -ne 0) {
        throw "psql query failed (exit $exitCode): $($output -join ' ')"
    }
    return (($output | Out-String).Trim())
}

function Resolve-ApiBaseUrl {
    param([string]$Explicit, $EnvValues)
    $candidate = $Explicit
    if ([string]::IsNullOrWhiteSpace($candidate)) {
        $candidate = Get-ConfigValue -EnvValues $EnvValues -Key "EZTEST_API_BASE_URL" -Default ""
    }
    if ([string]::IsNullOrWhiteSpace($candidate)) {
        $port = Get-ConfigValue -EnvValues $EnvValues -Key "EZTEST_API_PORT" -Default "13333"
        $candidate = "http://127.0.0.1:$port"
    }
    $candidate = $candidate.Trim().TrimEnd([char[]]"/")
    $uri = $null
    if (-not [System.Uri]::TryCreate($candidate, [System.UriKind]::Absolute, [ref]$uri)) {
        throw "the API base URL is not a valid absolute URL"
    }
    if ($uri.Scheme -ne "http" -and $uri.Scheme -ne "https") {
        throw "the API base URL must use http or https"
    }
    return $candidate
}

function Test-ObservationShape {
    param($Observation, [string]$CorpusVersion, [string]$ExpectedModel)
    if ($null -eq $Observation) {
        throw "observation is empty"
    }
    if ([string]$Observation.schema -ne "p6-eval-observation/v1") {
        throw "observation.schema must be p6-eval-observation/v1"
    }
    if ([string]$Observation.mode -ne "real_llm") {
        throw "observation.mode must be real_llm (no fake/deterministic track)"
    }
    $model = [string]$Observation.model
    if ([string]::IsNullOrWhiteSpace($model)) {
        throw "observation.model is required"
    }
    if ($model -match "(?i)fake") {
        throw "observation.model '$model' looks like a fake model; P6 only records real LLM runs"
    }
    if (-not [string]::IsNullOrWhiteSpace($ExpectedModel) -and $model -ne $ExpectedModel) {
        throw "observation.model '$model' does not match the corpus/env model '$ExpectedModel'"
    }
    if ([string]$Observation.corpus_version -ne $CorpusVersion) {
        throw "observation.corpus_version '$($Observation.corpus_version)' does not match '$CorpusVersion'"
    }
    if ([string]::IsNullOrWhiteSpace([string]$Observation.version_id)) {
        throw "observation.version_id is required"
    }
    if ($null -eq $Observation.requirements -or $null -eq $Observation.requirements.confirmed_keys) {
        throw "observation.requirements.confirmed_keys is required"
    }
    if ($null -eq $Observation.design -or $null -eq $Observation.design.covered_requirement_keys) {
        throw "observation.design.covered_requirement_keys is required"
    }
    if ($null -eq $Observation.impact -or $null -eq $Observation.impact.selected_requirement_keys) {
        throw "observation.impact.selected_requirement_keys is required"
    }
    if ($null -eq $Observation.execution -or $null -eq $Observation.execution.case_runs) {
        throw "observation.execution.case_runs is required"
    }
    if ($null -eq $Observation.issues -or $null -eq $Observation.issues.ai_issues) {
        throw "observation.issues.ai_issues is required"
    }
    if ($null -eq $Observation.supplement -or $null -eq $Observation.supplement.ai_case_titles) {
        throw "observation.supplement.ai_case_titles is required"
    }
    if ($null -ne $Observation.visual) {
        $injected = 0
        if (-not [int]::TryParse([string]$Observation.visual.injected, [ref]$injected) -or $injected -lt 0) {
            throw "observation.visual.injected must be a non-negative integer"
        }
        if ($null -eq $Observation.visual.cases) {
            throw "observation.visual.cases is required when visual is present"
        }
    }
}

function Count-Intersection {
    param([string[]]$Left, [string[]]$Right)
    $count = 0
    foreach ($item in $Left) {
        if ($Right -contains $item) { $count++ }
    }
    return $count
}

function New-SampleFromObservation {
    param(
        $Observation,
        $ImpactTruth,
        $ClassificationTruth,
        $IssuesTruth,
        $SupplementTruth,
        $VisualTruth
    )
    $confirmedKeys = @($Observation.requirements.confirmed_keys | ForEach-Object { [string]$_ })
    $coveredKeys = @($Observation.design.covered_requirement_keys | ForEach-Object { [string]$_ })
    $selectedKeys = @($Observation.impact.selected_requirement_keys | ForEach-Object { [string]$_ })
    $caseRuns = @($Observation.execution.case_runs)
    $aiIssues = @($Observation.issues.ai_issues)
    $supplementTitles = @($Observation.supplement.ai_case_titles | ForEach-Object { [string]$_ })

    $impactExpected = @($ImpactTruth.expected | ForEach-Object { [string]$_.requirement_key })
    $injected = @($ClassificationTruth.injected_failures)
    $truePositiveTitles = @($IssuesTruth.true_positives | ForEach-Object { [string]$_.title })
    $supplementExpected = @($SupplementTruth.expected_cases | ForEach-Object { [string]$_.title })
    $visualTruthCases = @($VisualTruth.cases)

    $correctClassifications = 0
    foreach ($truth in $injected) {
        $title = [string]$truth.case_title
        $expectedClass = [string]$truth.expected_failure_class
        $run = $null
        foreach ($candidate in $caseRuns) {
            if ([string]$candidate.case_title -eq $title) { $run = $candidate; break }
        }
        if ($null -ne $run -and [string]$run.status -eq "failed" -and [string]$run.failure_class -eq $expectedClass) {
            $correctClassifications++
        }
    }

    $aiIssueTitles = @($aiIssues | ForEach-Object { [string]$_.title })

    $sample = [ordered]@{
        confirmed_requirements  = $confirmedKeys.Count
        covered_requirements    = (Count-Intersection -Left $confirmedKeys -Right $coveredKeys)
        impact_expected         = $impactExpected.Count
        impact_hit              = (Count-Intersection -Left $impactExpected -Right $selectedKeys)
        impact_selected         = $selectedKeys.Count
        injected_failures       = $injected.Count
        correct_classifications = $correctClassifications
        ai_issues               = $aiIssues.Count
        true_positive_issues    = (Count-Intersection -Left $aiIssueTitles -Right $truePositiveTitles)
        supplement_cases        = $supplementTitles.Count
        supplement_hits         = (Count-Intersection -Left $supplementTitles -Right $supplementExpected)
    }

    if ($null -ne $Observation.visual) {
        $visualInjected = [int]$Observation.visual.injected
        $visualCases = @($Observation.visual.cases)
        $visualCorrect = 0
        foreach ($truth in $visualTruthCases) {
            $title = [string]$truth.case_title
            $expectedClass = [string]$truth.expected_classification
            $candidate = $null
            foreach ($case in $visualCases) {
                if ([string]$case.case_title -eq $title) { $candidate = $case; break }
            }
            if ($null -ne $candidate -and [string]$candidate.classification -eq $expectedClass) {
                $visualCorrect++
            }
        }
        if ($visualCorrect -gt $visualInjected) {
            throw "observation.visual.injected ($visualInjected) is smaller than the correct classification count ($visualCorrect)"
        }
        $sample["visual_injected"] = $visualInjected
        $sample["visual_correct"] = $visualCorrect
    }
    return $sample
}

function Assert-Sample {
    param($Sample)
    foreach ($key in $Sample.Keys) {
        if ([int]$Sample[$key] -lt 0) {
            throw "sample.$key must not be negative"
        }
    }
    if ([int]$Sample["covered_requirements"] -gt [int]$Sample["confirmed_requirements"]) {
        throw "sample.covered_requirements must not exceed confirmed_requirements"
    }
    if ([int]$Sample["impact_hit"] -gt [int]$Sample["impact_expected"]) {
        throw "sample.impact_hit must not exceed impact_expected"
    }
    if ([int]$Sample["impact_hit"] -gt [int]$Sample["impact_selected"]) {
        throw "sample.impact_hit must not exceed impact_selected"
    }
    if ([int]$Sample["correct_classifications"] -gt [int]$Sample["injected_failures"]) {
        throw "sample.correct_classifications must not exceed injected_failures"
    }
    if ([int]$Sample["true_positive_issues"] -gt [int]$Sample["ai_issues"]) {
        throw "sample.true_positive_issues must not exceed ai_issues"
    }
    if ([int]$Sample["supplement_hits"] -gt [int]$Sample["supplement_cases"]) {
        throw "sample.supplement_hits must not exceed supplement_cases"
    }
    if ($Sample.Contains("visual_injected") -and [int]$Sample["visual_correct"] -gt [int]$Sample["visual_injected"]) {
        throw "sample.visual_correct must not exceed visual_injected"
    }
}

# ---------------------------------------------------------------------------
# Static preflight shared by -DryRun and -Mode real-llm.
# ---------------------------------------------------------------------------
function Invoke-Preflight {
    param([bool]$ForRealRun)

    Write-Step "Validating the eval/v02 corpus, ground truth and baseline"

    $readmePath = Join-Path $evalRoot "README.md"
    if (-not (Test-Path -LiteralPath $readmePath)) {
        throw "missing eval/v02/README.md"
    }

    $environment = Read-JsonFile -Path (Join-Path $corpusDir "environment.json") -Label "corpus/environment.json"
    $requirements = Read-JsonFile -Path (Join-Path $corpusDir "requirements.json") -Label "corpus/requirements.json"
    $design = Read-JsonFile -Path (Join-Path $corpusDir "design.json") -Label "corpus/design.json"
    $code = Read-JsonFile -Path (Join-Path $corpusDir "code.json") -Label "corpus/code.json"
    $execution = Read-JsonFile -Path (Join-Path $corpusDir "execution.json") -Label "corpus/execution.json"
    $issues = Read-JsonFile -Path (Join-Path $corpusDir "issues.json") -Label "corpus/issues.json"
    $visual = Read-JsonFile -Path (Join-Path $corpusDir "visual.json") -Label "corpus/visual.json"
    $impactTruth = Read-JsonFile -Path (Join-Path $truthDir "impact.json") -Label "ground-truth/impact.json"
    $classificationTruth = Read-JsonFile -Path (Join-Path $truthDir "classification.json") -Label "ground-truth/classification.json"
    $issuesTruth = Read-JsonFile -Path (Join-Path $truthDir "issues.json") -Label "ground-truth/issues.json"
    $supplementTruth = Read-JsonFile -Path (Join-Path $truthDir "supplement.json") -Label "ground-truth/supplement.json"
    $visualTruth = Read-JsonFile -Path (Join-Path $truthDir "visual.json") -Label "ground-truth/visual.json"
    $baseline = Read-JsonFile -Path (Join-Path $evalRoot "baseline.json") -Label "baseline.json"

    $corpusVersion = [string]$requirements.corpus_version
    if ([string]::IsNullOrWhiteSpace($corpusVersion)) {
        throw "corpus/requirements.json has no corpus_version"
    }
    $versionChecks = @(
        @{ Name = "environment.json"; Value = [string]$environment.corpus_version },
        @{ Name = "design.json"; Value = [string]$design.corpus_version },
        @{ Name = "code.json"; Value = [string]$code.corpus_version },
        @{ Name = "execution.json"; Value = [string]$execution.corpus_version },
        @{ Name = "issues.json"; Value = [string]$issues.corpus_version },
        @{ Name = "visual.json"; Value = [string]$visual.corpus_version },
        @{ Name = "ground-truth/impact.json"; Value = [string]$impactTruth.corpus_version },
        @{ Name = "ground-truth/classification.json"; Value = [string]$classificationTruth.corpus_version },
        @{ Name = "ground-truth/issues.json"; Value = [string]$issuesTruth.corpus_version },
        @{ Name = "ground-truth/supplement.json"; Value = [string]$supplementTruth.corpus_version },
        @{ Name = "ground-truth/visual.json"; Value = [string]$visualTruth.corpus_version },
        @{ Name = "baseline.json"; Value = [string]$baseline.corpus_version }
    )
    foreach ($check in $versionChecks) {
        if ($check.Value -ne $corpusVersion) {
            throw "$($check.Name) corpus_version '$($check.Value)' does not match '$corpusVersion'"
        }
    }
    Write-Info "corpus_version = $corpusVersion; 14 artifacts parsed"

    # Baseline: exactly the seven spec metric names, numeric [0,1], frozen
    # thresholds, real_llm mode, report-only description.
    if ([string]$baseline.mode -ne "real_llm") {
        throw "baseline.json mode must be real_llm"
    }
    $baselineProperties = @($baseline.metrics.PSObject.Properties)
    if ($baselineProperties.Count -ne 7) {
        throw "baseline.json must contain exactly 7 metrics (found $($baselineProperties.Count))"
    }
    foreach ($name in $metricNames) {
        $property = $baseline.metrics.PSObject.Properties[$name]
        if ($null -eq $property) { throw "baseline.json is missing metric $name" }
        $value = 0.0
        if (-not [double]::TryParse([string]$property.Value, [ref]$value)) {
            throw "baseline.json metric $name is not a number"
        }
        if ($value -lt 0 -or $value -gt 1) {
            throw "baseline.json metric $name must be within [0,1]"
        }
        $thresholdProperty = $baseline.thresholds.PSObject.Properties[$name]
        if ($null -eq $thresholdProperty) { throw "baseline.json is missing threshold $name" }
        $threshold = 0.0
        if (-not [double]::TryParse([string]$thresholdProperty.Value, [ref]$threshold)) {
            throw "baseline.json threshold $name is not a number"
        }
        $expected = [double]$specThresholds[$name]
        if ([math]::Abs($threshold - $expected) -gt 0.000000001) {
            throw "baseline.json threshold $name = $threshold, expected spec section 5.3 value $expected"
        }
    }
    Write-Info "baseline: 7 metrics; thresholds match spec section 5.3 real_llm"

    # Requirements / design.
    $requirementKeys = @($requirements.requirements | ForEach-Object { [string]$_.key })
    if ($requirementKeys.Count -eq 0) { throw "corpus/requirements.json declares no requirements" }
    if (@($requirementKeys | Sort-Object -Unique).Count -ne $requirementKeys.Count) {
        throw "corpus/requirements.json requirement keys are not unique"
    }
    if ([int]$requirements.expected.confirmed_requirements -ne $requirementKeys.Count) {
        throw "expected.confirmed_requirements must equal the requirement count"
    }
    $expectedCovered = @($requirements.expected.design_covered_requirement_keys | ForEach-Object { [string]$_ })
    if ($expectedCovered.Count -eq 0) { throw "expected.design_covered_requirement_keys is empty" }
    foreach ($key in $expectedCovered) {
        if ($requirementKeys -notcontains $key) { throw "covered requirement $key is not declared" }
    }

    $caseTitles = @($design.cases | ForEach-Object { [string]$_.title })
    if ($caseTitles.Count -eq 0) { throw "corpus/design.json declares no cases" }
    foreach ($case in @($design.cases)) {
        if ($requirementKeys -notcontains [string]$case.requirement_key) {
            throw "design case '$($case.title)' references an unknown requirement"
        }
    }
    foreach ($title in @($design.expected.case_titles | ForEach-Object { [string]$_ })) {
        if ($caseTitles -notcontains $title) { throw "expected case '$title' is not declared" }
    }

    # Code / impact truth.
    foreach ($commit in @([string]$code.repository.base_commit, [string]$code.repository.head_commit)) {
        if ($commit -notmatch "^[0-9a-f]{40}$") {
            throw "corpus/code.json repository commits must be 40 lowercase hex characters"
        }
    }
    $codeImpactKeys = @($code.expected_impact_requirement_keys | ForEach-Object { [string]$_ })
    if ($codeImpactKeys.Count -eq 0) { throw "corpus/code.json declares no expected impact requirements" }
    foreach ($key in $codeImpactKeys) {
        if ($requirementKeys -notcontains $key) { throw "impact requirement $key is not declared" }
    }
    if (@($impactTruth.expected).Count -eq 0) { throw "ground-truth/impact.json declares no expected impact" }
    $truthImpactKeys = @($impactTruth.expected | ForEach-Object { [string]$_.requirement_key })
    if ((($truthImpactKeys | Sort-Object) -join ",") -ne (($codeImpactKeys | Sort-Object) -join ",")) {
        throw "corpus/code.json expected impact requirements do not match ground-truth/impact.json"
    }

    # Execution / classification truth.
    $injectedCorpus = @($execution.injected_failures)
    $injectedTruth = @($classificationTruth.injected_failures)
    if ($injectedTruth.Count -eq 0) { throw "ground-truth/classification.json declares no injected failures" }
    if ($injectedCorpus.Count -ne $injectedTruth.Count) {
        throw "corpus/execution.json and ground-truth/classification.json disagree on the injected failure count"
    }
    for ($index = 0; $index -lt $injectedTruth.Count; $index++) {
        $corpusFailure = $injectedCorpus[$index]
        $truthFailure = $injectedTruth[$index]
        if ([string]$corpusFailure.case_title -ne [string]$truthFailure.case_title) {
            throw "injected failure #$($index + 1) case_title disagrees between corpus and ground truth"
        }
        if ([string]$corpusFailure.expected_failure_class -ne [string]$truthFailure.expected_failure_class) {
            throw "injected failure '$($truthFailure.case_title)' failure class disagrees between corpus and ground truth"
        }
        if ($caseTitles -notcontains [string]$truthFailure.case_title) {
            throw "injected failure '$($truthFailure.case_title)' is not a declared design case"
        }
    }

    # Issues truth.
    if (@($issues.scenarios).Count -eq 0) { throw "corpus/issues.json declares no issue scenarios" }
    $truePositiveTitles = @($issuesTruth.true_positives | ForEach-Object { [string]$_.title })
    if ($truePositiveTitles.Count -eq 0) { throw "ground-truth/issues.json declares no true positives" }
    $corpusAITitles = @($issues.scenarios | Where-Object { [string]$_.source -eq "ai" } | ForEach-Object { [string]$_.title })
    foreach ($title in $truePositiveTitles) {
        if ($corpusAITitles -notcontains $title) {
            throw "true positive '$title' is not a declared AI issue scenario"
        }
    }

    # Supplement / visual truth.
    if (@($supplementTruth.expected_cases).Count -eq 0) {
        throw "ground-truth/supplement.json declares no expected cases"
    }
    $visualAssertions = @($visual.assertions)
    $visualTruthCases = @($visualTruth.cases)
    if ([string]$visualTruth.metric -ne "visual_classification_accuracy") {
        throw "ground-truth/visual.json metric must be visual_classification_accuracy"
    }
    if ($visualAssertions.Count -eq 0 -or $visualTruthCases.Count -eq 0) {
        throw "visual corpus/ground truth must declare at least one case"
    }
    if ($visualAssertions.Count -ne $visualTruthCases.Count) {
        throw "visual corpus and ground truth disagree on the case count"
    }
    foreach ($truthCase in $visualTruthCases) {
        $title = [string]$truthCase.case_title
        $match = $null
        foreach ($assertion in $visualAssertions) {
            if ([string]$assertion.case_title -eq $title) { $match = $assertion; break }
        }
        if ($null -eq $match) { throw "visual truth case '$title' is not declared in corpus/visual.json" }
        if ([string]$match.expected_classification -ne [string]$truthCase.expected_classification) {
            throw "visual case '$title' classification disagrees between corpus and ground truth"
        }
    }
    Write-Info "corpus/ground-truth cross references are consistent"

    # Frozen contract: quality paths/schemas and the P6 frozen marker.
    $openapiPath = Join-Path $repoRoot "docs\version-V0.2\contracts\openapi.yaml"
    if (-not (Test-Path -LiteralPath $openapiPath)) { throw "missing frozen OpenAPI contract" }
    $openapi = Get-Content -LiteralPath $openapiPath -Raw -Encoding UTF8
    foreach ($needle in @(
            "x-frozen: P6",
            "/api/quality/eval-runs:",
            "/api/quality/eval-runs/{id}:",
            "/api/quality/overview:",
            "EvalRunCreateRequest:",
            "EvalSample:",
            "EvalSummary:",
            "QualityOverviewView:"
        )) {
        if (-not $openapi.Contains($needle)) {
            throw "openapi.yaml is missing the frozen P6 contract fragment '$needle'"
        }
    }
    $routesPath = Join-Path $repoRoot "backend\internal\quality\interfaces\httpapi\routes.go"
    if (-not (Test-Path -LiteralPath $routesPath)) { throw "missing quality httpapi routes.go" }
    $routes = Get-Content -LiteralPath $routesPath -Raw -Encoding UTF8
    foreach ($needle in @(
            "GET /api/quality/eval-runs",
            "POST /api/quality/eval-runs",
            "GET /api/quality/eval-runs/{id}",
            "GET /api/quality/overview"
        )) {
        if (-not $routes.Contains($needle)) {
            throw "quality routes.go is missing '$needle'"
        }
    }
    Write-Info "OpenAPI frozen P6 quality paths/schemas and quality routes are present"

    # Migration order must be at 33 and hold the eval migration.
    $orderPath = Join-Path $repoRoot "backend\internal\db\migrations\order.txt"
    if (-not (Test-Path -LiteralPath $orderPath)) { throw "missing migrations/order.txt" }
    $ordered = @(Get-Content -LiteralPath $orderPath -Encoding UTF8 | ForEach-Object { $_.Trim() } | Where-Object {
            $_.Length -gt 0 -and -not $_.StartsWith("#")
        })
    if ($ordered.Count -ne 33) {
        throw "migrations/order.txt must list 33 migrations (found $($ordered.Count))"
    }
    if ($ordered -notcontains "quality_0001_eval.sql") {
        throw "migrations/order.txt must contain quality_0001_eval.sql"
    }
    if ($ordered -notcontains "agent_0005_profile_prompt_revision.sql") {
        throw "migrations/order.txt must contain agent_0005_profile_prompt_revision.sql"
    }
    Write-Info "migrations/order.txt = 33 entries including quality_0001_eval.sql"

    # Environment / database / LLM configuration.
    $envPath = Join-Path $repoRoot ".env"
    if (-not (Test-Path -LiteralPath $envPath)) {
        throw "missing .env at $envPath (EZTEST_DATABASE_URL and LLM settings are required)"
    }
    $envValues = Read-DotEnv -Path $envPath
    $databaseUrl = Get-ConfigValue -EnvValues $envValues -Key "EZTEST_DATABASE_URL" -Default ""
    $databaseTarget = Get-DatabaseTarget -DatabaseUrl $databaseUrl

    $script:PsqlExe = Get-CommandExe -Name "psql.exe" -PreferredPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe")
    $powerShellHost = Join-Path $PSHOME "powershell.exe"
    if (-not (Test-Path -LiteralPath $powerShellHost)) { $powerShellHost = Join-Path $PSHOME "pwsh.exe" }
    if (-not (Test-Path -LiteralPath $powerShellHost)) {
        throw "cannot locate a PowerShell host for the evaluation (PSHOME=$PSHOME)"
    }

    $ping = Invoke-PsqlQuery -Target $databaseTarget -Sql "SELECT 1"
    if ($ping -ne "1") {
        throw "database connectivity check did not return 1"
    }
    $appliedCount = -1
    try {
        $appliedText = Invoke-PsqlQuery -Target $databaseTarget -Sql "SELECT count(*) FROM schema_migrations"
        $parsedApplied = 0
        if ([int]::TryParse($appliedText, [ref]$parsedApplied)) { $appliedCount = $parsedApplied }
    } catch {
        Write-WarnLine "schema_migrations is not readable; the database may be uninitialized"
    }
    if ($ForRealRun) {
        if ($appliedCount -ge 0 -and $appliedCount -lt 33) {
            throw "database has $appliedCount migrations; apply #32/#33 before a real-llm evaluation run"
        }
        $evalTable = Invoke-PsqlQuery -Target $databaseTarget -Sql "SELECT to_regclass('eval_run') IS NOT NULL"
        if ($evalTable -ne "t") {
            throw "eval_run table is missing; apply quality_0001_eval.sql (#32) before a real-llm run"
        }
    } elseif ($appliedCount -ge 0 -and $appliedCount -lt 33) {
        Write-WarnLine "database currently has $appliedCount migrations; real-llm mode requires 33"
    }
    Write-Info "database reachable: $($databaseTarget.Host):$($databaseTarget.Port)/$($databaseTarget.Database); applied migrations=$appliedCount"

    $llmKey = Get-ConfigValue -EnvValues $envValues -Key "llm_api_key" -Default ""
    $llmBase = Get-ConfigValue -EnvValues $envValues -Key "llm_base_url" -Default ""
    $llmModel = Get-ConfigValue -EnvValues $envValues -Key "llm_model_name" -Default ""
    if ([string]::IsNullOrWhiteSpace($llmKey) -or [string]::IsNullOrWhiteSpace($llmBase) -or [string]::IsNullOrWhiteSpace($llmModel)) {
        throw "real-LLM configuration is incomplete in .env (llm_api_key/llm_base_url/llm_model_name)"
    }
    if ($llmModel -match "(?i)fake") {
        throw "the configured LLM model looks fake; P6 evaluation requires a real model"
    }
    $corpusModel = [string]$environment.llm.model
    if ($llmModel -ne $corpusModel) {
        throw "configured LLM model '$llmModel' does not match the frozen corpus model '$corpusModel'"
    }
    Write-Info "llm model = $llmModel (real-LLM track; key/base URL never printed)"

    $adminUsername = Get-ConfigValue -EnvValues $envValues -Key "EZTEST_ADMIN_USERNAME" -Default "admin"
    $adminPassword = Get-ConfigValue -EnvValues $envValues -Key "EZTEST_ADMIN_PASSWORD" -Default "admin123"
    $adminToken = Get-ConfigValue -EnvValues $envValues -Key "EZTEST_ADMIN_TOKEN" -Default ""
    if ([string]::IsNullOrWhiteSpace($adminToken) -and [string]::IsNullOrWhiteSpace($adminUsername)) {
        throw "admin credentials are missing for the evaluation run"
    }

    $resolvedApiBase = Resolve-ApiBaseUrl -Explicit $ApiBaseUrl -EnvValues $envValues
    $script:ApiBase = $resolvedApiBase
    Write-Info "api base = $resolvedApiBase (reachability is checked in real-llm mode only)"

    # Observation: validate the committed template, and an explicitly supplied
    # observation, but never require runtime data during -DryRun.
    $examplePath = Join-Path $evalRoot "observation.example.json"
    if (Test-Path -LiteralPath $examplePath) {
        $exampleObservation = Read-JsonFile -Path $examplePath -Label "observation.example.json"
        Test-ObservationShape -Observation $exampleObservation -CorpusVersion $corpusVersion -ExpectedModel $corpusModel
        # The committed template must stay consistent with the ground truth so
        # the runner's derivation logic is exercised by every dry run. This is a
        # template self-check only; it is never recorded as an evaluation run.
        $exampleSample = New-SampleFromObservation `
            -Observation $exampleObservation `
            -ImpactTruth $impactTruth `
            -ClassificationTruth $classificationTruth `
            -IssuesTruth $issuesTruth `
            -SupplementTruth $supplementTruth `
            -VisualTruth $visualTruth
        Assert-Sample -Sample $exampleSample
        $impactHitTruth = 0
        foreach ($truth in @($impactTruth.expected)) {
            if ([bool]$truth.expected_selected) { $impactHitTruth++ }
        }
        $expectedSample = [ordered]@{
            confirmed_requirements  = [int]$requirements.expected.confirmed_requirements
            covered_requirements    = $expectedCovered.Count
            impact_expected         = @($impactTruth.expected).Count
            impact_hit              = $impactHitTruth
            impact_selected         = [int]$impactTruth.expected_counts.impact_selected
            injected_failures       = $injectedTruth.Count
            correct_classifications = [int]$classificationTruth.expected_counts.correct_classifications
            ai_issues               = [int]$issuesTruth.expected_counts.ai_issues
            true_positive_issues    = [int]$issuesTruth.expected_counts.true_positive_issues
            supplement_cases        = [int]$supplementTruth.expected_counts.supplement_cases
            supplement_hits         = [int]$supplementTruth.expected_counts.supplement_hits
        }
        if ($null -ne $exampleObservation.visual) {
            $expectedSample["visual_injected"] = [int]$visualTruth.expected_counts.visual_injected
            $expectedSample["visual_correct"] = [int]$visualTruth.expected_counts.visual_correct
        }
        foreach ($key in $expectedSample.Keys) {
            if (-not $exampleSample.Contains($key)) {
                throw "observation.example.json does not derive sample.$key"
            }
            if ([int]$exampleSample[$key] -ne [int]$expectedSample[$key]) {
                throw "observation.example.json derives sample.$key=$($exampleSample[$key]), expected $($expectedSample[$key])"
            }
        }
        Write-Info "observation.example.json matches p6-eval-observation/v1 and the committed ground truth"
    }
    if (-not [string]::IsNullOrWhiteSpace($ObservationFile)) {
        $explicitObservationPath = Resolve-RepoPath -Path $ObservationFile
        $explicitObservation = Read-JsonFile -Path $explicitObservationPath -Label "observation file"
        Test-ObservationShape -Observation $explicitObservation -CorpusVersion $corpusVersion -ExpectedModel $corpusModel
        Write-Info "explicit observation file shape is valid"
    }
    Write-Info "toolchain: psql and PowerShell host are available"

    return [pscustomobject]@{
        CorpusVersion       = $corpusVersion
        CorpusModel         = $corpusModel
        Environment         = $environment
        Requirements        = $requirements
        Design              = $design
        Code                = $code
        Execution           = $execution
        Issues              = $issues
        Visual              = $visual
        ImpactTruth         = $impactTruth
        ClassificationTruth = $classificationTruth
        IssuesTruth         = $issuesTruth
        SupplementTruth     = $supplementTruth
        VisualTruth         = $visualTruth
        Baseline            = $baseline
        EnvValues           = $envValues
        DatabaseTarget      = $databaseTarget
        AppliedMigrations   = $appliedCount
        AdminUsername       = $adminUsername
        AdminPassword       = $adminPassword
        AdminToken          = $adminToken
        ApiBaseUrl          = $resolvedApiBase
    }
}

function Invoke-EvalApi {
    param([string]$Method, [string]$Path, $Body, [string]$Token)
    $headers = @{ "Accept" = "application/json" }
    if (-not [string]::IsNullOrWhiteSpace($Token)) {
        $headers["Authorization"] = "Bearer $Token"
    }
    $request = @{
        Method     = $Method
        Uri        = ($script:ApiBase + $Path)
        Headers    = $headers
        TimeoutSec = $script:TimeoutSeconds
    }
    if ($null -ne $Body) {
        $request["ContentType"] = "application/json"
        $request["Body"] = ($Body | ConvertTo-Json -Depth 14)
    }
    try {
        return (Invoke-RestMethod @request)
    } catch {
        $detail = ""
        if ($null -ne $_.ErrorDetails -and -not [string]::IsNullOrWhiteSpace([string]$_.ErrorDetails.Message)) {
            $detail = [string]$_.ErrorDetails.Message
        }
        throw "$Method $Path failed: $($_.Exception.Message) $detail"
    }
}

function Get-AdminToken {
    param($Pre)
    if (-not [string]::IsNullOrWhiteSpace($script:BearerToken)) {
        return $script:BearerToken.Trim()
    }
    if (-not [string]::IsNullOrWhiteSpace([string]$Pre.AdminToken)) {
        return ([string]$Pre.AdminToken).Trim()
    }
    $loginBody = [ordered]@{
        username = [string]$Pre.AdminUsername
        password = [string]$Pre.AdminPassword
    }
    $response = Invoke-EvalApi -Method "POST" -Path "/api/auth/login" -Body $loginBody -Token ""
    if ($null -eq $response -or $null -eq $response.data -or [string]::IsNullOrWhiteSpace([string]$response.data.token)) {
        throw "admin login did not return a bearer token"
    }
    return [string]$response.data.token
}

function Resolve-ObservationPath {
    param($Pre)
    if (-not [string]::IsNullOrWhiteSpace($script:ObservationFile)) {
        return (Resolve-RepoPath -Path $script:ObservationFile)
    }
    $fromEnvironment = Get-ConfigValue -EnvValues $Pre.EnvValues -Key "EZTEST_EVAL_OBSERVATION" -Default ""
    if (-not [string]::IsNullOrWhiteSpace($fromEnvironment)) {
        return (Resolve-RepoPath -Path $fromEnvironment)
    }
    $candidates = @(
        (Join-Path $repoRoot ".tmp\p6-acceptance\observation.json"),
        (Join-Path $repoRoot ".tmp\p6-eval\observation.input.json"),
        (Join-Path $repoRoot ".tmp\p6-real-llm\observation.json")
    )
    foreach ($candidate in $candidates) {
        if (Test-Path -LiteralPath $candidate) { return $candidate }
    }
    throw "no real-LLM observation found; pass -ObservationFile (tried: $($candidates -join ', '))"
}

function Write-FailureEvidence {
    param([string]$Message, [string]$ObservationPath, $Observation)
    try {
        New-Item -ItemType Directory -Force -Path $script:TmpDir | Out-Null
        if (-not [string]::IsNullOrWhiteSpace($ObservationPath) -and (Test-Path -LiteralPath $ObservationPath)) {
            $destination = Join-Path $script:TmpDir "observation.json"
            if ([System.IO.Path]::GetFullPath($ObservationPath) -ne [System.IO.Path]::GetFullPath($destination)) {
                Copy-Item -LiteralPath $ObservationPath -Destination $destination -Force
            }
        }
        $failure = [ordered]@{
            schema      = "p6-eval-result/v1"
            ok          = $false
            mode        = "real_llm"
            report_only = $true
            error       = $Message
            failed_at   = (Get-Date).ToUniversalTime().ToString("o")
        }
        if ($null -ne $Observation) {
            $failure["version_id"] = [string]$Observation.version_id
            $failure["corpus_version"] = [string]$Observation.corpus_version
        }
        Write-JsonFile -Path (Join-Path $script:TmpDir "result.json") -Value $failure
    } catch {
        Write-WarnLine "could not write failure evidence: $($_.Exception.Message)"
    }
}

# ---------------------------------------------------------------------------
# Dry run.
# ---------------------------------------------------------------------------
if ($DryRun) {
    try {
        $pre = Invoke-Preflight -ForRealRun $false
        Write-Info "dry-run would POST mode=real_llm to $($pre.ApiBaseUrl)/api/quality/eval-runs"
        Write-Step "EVAL PREFLIGHT PASSED (dry-run; no LLM call, no write)"
        exit 0
    } catch {
        Write-ErrorLine "EVAL DRY-RUN FAILED: $($_.Exception.Message)"
        exit 1
    }
}

# ---------------------------------------------------------------------------
# Real-LLM report track.
# ---------------------------------------------------------------------------
$observationPath = ""
$observation = $null
# The real-llm run keeps a fresh transcript under .tmp/p6-eval/eval.log so a
# failure can be diagnosed after the console is gone. DryRun never writes.
if (-not [string]::IsNullOrWhiteSpace($script:LogPath)) {
    try {
        $logParent = Split-Path -Parent $script:LogPath
        New-Item -ItemType Directory -Force -Path $logParent | Out-Null
        Remove-Item -LiteralPath $script:LogPath -Force -ErrorAction SilentlyContinue
    } catch {
        # A log setup failure must not mask the evaluation result.
    }
}
try {
    $pre = Invoke-Preflight -ForRealRun $true
    Write-Info "mode = $Mode (only real-llm exists; no fake/deterministic track)"
    Write-Step "Checking the P6 API and real-LLM data"

    $health = Invoke-EvalApi -Method "GET" -Path "/api/health" -Body $null -Token ""
    if ($null -eq $health -or $null -eq $health.data -or [string]$health.data.status -ne "ok") {
        throw "API health check failed at $($pre.ApiBaseUrl)"
    }
    Write-Info "API health = ok"

    $observationPath = Resolve-ObservationPath -Pre $pre
    $observation = Read-JsonFile -Path $observationPath -Label "real-LLM observation"
    Test-ObservationShape -Observation $observation -CorpusVersion $pre.CorpusVersion -ExpectedModel $pre.CorpusModel
    Write-Info "observation = $observationPath (version=$($observation.version_id), source=$($observation.source))"

    Write-Step "Deriving the evaluation sample from real data and committed ground truth"
    $sample = New-SampleFromObservation `
        -Observation $observation `
        -ImpactTruth $pre.ImpactTruth `
        -ClassificationTruth $pre.ClassificationTruth `
        -IssuesTruth $pre.IssuesTruth `
        -SupplementTruth $pre.SupplementTruth `
        -VisualTruth $pre.VisualTruth
    Assert-Sample -Sample $sample
    Write-Info ("sample = confirmed {0}/{0}, covered {1}, impact {2}/{3} (selected {4}), classification {5}/{6}, ai issues {7} (tp {8}), supplement {9}/{10}" -f `
            $sample["confirmed_requirements"], $sample["covered_requirements"], $sample["impact_hit"], `
            $sample["impact_expected"], $sample["impact_selected"], $sample["correct_classifications"], `
            $sample["injected_failures"], $sample["ai_issues"], $sample["true_positive_issues"], `
            $sample["supplement_hits"], $sample["supplement_cases"])
    if ($sample.Contains("visual_injected")) {
        Write-Info ("visual = {0}/{1}" -f $sample["visual_correct"], $sample["visual_injected"])
    }

    $baselineMap = [ordered]@{}
    foreach ($name in $metricNames) {
        $baselineMap[$name] = [double]$pre.Baseline.metrics.PSObject.Properties[$name].Value
    }

    Write-Step "Recording the real-LLM evaluation run through the admin API"
    $token = Get-AdminToken -Pre $pre
    $requestBody = [ordered]@{
        mode           = "real_llm"
        corpus_version = $pre.CorpusVersion
        model          = [string]$observation.model
        sample         = $sample
        baseline       = $baselineMap
        summary        = [ordered]@{
            source           = "p6-acceptance-observation"
            observation_file = $observationPath
            version_id       = [string]$observation.version_id
            generated_at     = [string]$observation.generated_at
            report_track     = "real_llm"
            corpus_files     = @(
                "corpus/environment.json",
                "corpus/requirements.json",
                "corpus/design.json",
                "corpus/code.json",
                "corpus/execution.json",
                "corpus/issues.json",
                "corpus/visual.json",
                "ground-truth/impact.json",
                "ground-truth/classification.json",
                "ground-truth/issues.json",
                "ground-truth/supplement.json",
                "ground-truth/visual.json"
            )
        }
    }
    if (-not [string]::IsNullOrWhiteSpace([string]$observation.started_at)) {
        $requestBody["started_at"] = [string]$observation.started_at
    }
    if (-not [string]::IsNullOrWhiteSpace([string]$observation.finished_at)) {
        $requestBody["finished_at"] = [string]$observation.finished_at
    }

    $response = Invoke-EvalApi -Method "POST" -Path "/api/quality/eval-runs" -Body $requestBody -Token $token
    if ($null -eq $response -or $null -eq $response.data -or [string]::IsNullOrWhiteSpace([string]$response.data.id)) {
        throw "the eval API returned no run data"
    }
    $run = $response.data

    $metricsOut = [ordered]@{}
    $regressions = @()
    foreach ($metric in @($run.metrics)) {
        $detail = $metric.detail
        $entry = [ordered]@{
            value       = [double]$metric.value
            threshold   = [double]$metric.threshold
            passed      = [bool]$metric.passed
            reason      = [string]$detail.reason
            numerator   = [int]$detail.numerator
            denominator = [int]$detail.denominator
            baseline    = $null
        }
        if ($null -ne $detail.baseline) {
            $entry["baseline"] = [double]$detail.baseline
        }
        $metricsOut[[string]$metric.name] = $entry
        if ([string]$detail.reason -eq "baseline_regression") {
            $regressions += [string]$metric.name
        }
    }

    $result = [ordered]@{
        schema            = "p6-eval-result/v1"
        ok                = $true
        mode              = "real_llm"
        report_only       = $true
        corpus_version    = [string]$pre.CorpusVersion
        model             = [string]$observation.model
        run_id            = [string]$run.id
        status            = [string]$run.status
        metrics           = $metricsOut
        regressions       = $regressions
        sample            = $sample
        baseline          = $baselineMap
        observation_file  = $observationPath
        api_base_url      = $pre.ApiBaseUrl
        recorded_at       = (Get-Date).ToUniversalTime().ToString("o")
    }

    New-Item -ItemType Directory -Force -Path $script:TmpDir | Out-Null
    Write-JsonFile -Path (Join-Path $script:TmpDir "result.json") -Value $result
    $observationDestination = Join-Path $script:TmpDir "observation.json"
    if ([System.IO.Path]::GetFullPath($observationPath) -ne [System.IO.Path]::GetFullPath($observationDestination)) {
        Copy-Item -LiteralPath $observationPath -Destination $observationDestination -Force
    }

    Write-Step "REAL-LLM EVAL RECORDED"
    Write-Info "run #$($result.run_id) status=$($result.status) (report-only; thresholds never block)"
    foreach ($name in $metricNames) {
        if ($null -eq $metricsOut[$name]) { continue }
        $metric = $metricsOut[$name]
        $line = "{0,-32} value={1} threshold={2} passed={3} reason={4}" -f `
            $name, $metric["value"], $metric["threshold"], $metric["passed"], $metric["reason"]
        Write-Info $line
    }
    if ($regressions.Count -gt 0) {
        Write-Info "baseline regressions (reported, non-blocking): $($regressions -join ', ')"
    }
    Write-Info "result = $(Join-Path $script:TmpDir 'result.json')"
    Write-EvalLog -Message "DONE run=$($result.run_id) status=$($result.status) report_only=true"
    exit 0
} catch {
    Write-FailureEvidence -Message $_.Exception.Message -ObservationPath $observationPath -Observation $observation
    Write-ErrorLine "EVAL FAILED: $($_.Exception.Message)"
    exit 1
}
