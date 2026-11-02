#requires -Version 5.1
<#
.SYNOPSIS
    Optional real-LLM acceptance for P4 code intelligence (gated by
    EZTEST_RUN_REAL_LLM=1).

.DESCRIPTION
    Without the gate this script prints SKIPPED and exits 0. With the gate open
    it drives one real-LLM P4 acceptance run against a real Git repository:

      * validates EZTEST_DATABASE_URL before anything destructive: the URL must
        use postgres/postgresql, carry an explicit host and a valid port (5432
        when omitted), and must not override the connection target with
        ?dbname=/?database=/?host=/?port=. The API URL for eztest_e2e is rebuilt
        from the validated URL and checked against the psql target, so both
        tools provably use the same database;
      * resets the dedicated eztest_e2e database, builds and starts the current
        API (or reuses an already running API when EZTEST_ACCEPTANCE_API_URL is
        set), then logs in as the administrator;
      * always clones the source repository (default: this checkout, or the Git
        path in EZTEST_ACCEPTANCE_P4_REPO) into .tmp/p4-real-repo and creates a
        uniquely named acceptance branch at a real base commit in that
        disposable clone. The source repository is never written to; it is only
        read by git clone;
      * selects a real base..head pair with at most 20 changed files
        (EZTEST_ACCEPTANCE_P4_BASE/HEAD override the pair);
      * registers the clone as a P2 code repository, creates a version bound to
        it, adds two requirements (one title contains P4-AI-IMPACT) and attempts
        one requirement anchor on a changed file (recorded as skipped with a
        warning when no changed path can be anchored);
      * triggers POST /api/versions/{id}/code/analyses with an explicit
        headCommit and polls until done/failed;
      * verifies that the change list matches `git diff --name-status` (with
        rename detection at 50%, parsed NUL-separated exactly like the backend),
        that stored AI evidence hashes recompute from commit content, that
        impact candidates carry a non-empty reason, a valid confidence and path
        evidence for high/medium, and that no changed path is silently dropped:
        every changed path must be covered by a persisted fact (path or evidence
        file), a version anchor, or an unmapped entry (spec §10.7 and INV-8).
        stats.unmapped is compared with the returned list unless truncation is
        reported;
      * verifies cache reuse (second POST returns reused=true, the same id and
        normalized candidates identical), file-level cache hits in a second
        version bound to the same repo/base analyzing the same head
        (stats.cacheHits > 0), and force versioning (force=true creates
        analysisVersion+1 while the old analysis stays readable and its
        candidates stay unchanged);
      * records the analysis.model/promptVersion provenance and a
        machine-readable, secret-free result at .tmp/p4-real-acceptance.json.

    Fail-closed by default: if health reports llm_configured=false, or any
    analysis in the run returns done+degraded=true (including the reused-API and
    force paths), the run FAILS instead of claiming a real-LLM success. Pass
    -AllowDegraded to opt into the previous behavior: the result is recorded as
    passed_degraded and the AI-only assertions are skipped with an explicit
    warning. If the default LLM configuration is unavailable before the run
    starts, the gate is open but the required llm_* keys are present, so this is
    a failure unless -AllowDegraded is passed explicitly.

    Logs contain only statuses, ids, counts and error codes; they never contain
    code bodies, prompts, credentials or repository file contents.

.PARAMETER DryRun
    Validate paths, the .env key presence (names only), the EZTEST_DATABASE_URL
    target-changing-query/psql-vs-API preflight and the Git/Node/Go/psql tool
    availability without cloning, resetting the database, starting the API or
    spending an LLM call. Exits 0 when the preflight passes. DryRun also works
    while the gate is closed.

.PARAMETER AllowNoUnmapped
    Accept a real commit pair where every changed file maps to a candidate
    (unmapped = 0). Without this switch the run fails because spec §10.7 step 3
    requires at least one real unmapped change; the relaxation is printed and
    recorded in the result JSON so it cannot pass silently.

.PARAMETER AllowDegraded
    Accept a degraded analysis (done + degraded=true) and record it as
    passed_degraded with the AI-only assertions skipped. Without this switch a
    run whose LLM configuration is unavailable or whose first, second-version or
    force analysis is degraded fails, so a degraded run can never masquerade as
    real LLM acceptance.

.NOTES
    This script makes paid upstream calls. It is deliberately not part of
    scripts/acceptance.ps1, which reports it as gated. P4 itself only reads the
    configured repository, but this script needs a branch for the version
    binding, so it clones the source (read-only source access) into the
    gitignored .tmp/p4-real-repo and never creates/updates refs in the source
    checkout. Compatible with Windows PowerShell 5.1 and PowerShell 7+;
    PostgreSQL must already be running.
#>
[CmdletBinding()]
param(
    [switch]$DryRun,
    [switch]$AllowNoUnmapped,
    [switch]$AllowDegraded
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$frontendDir = Join-Path $repoRoot "frontend"
$tmpDir = Join-Path $repoRoot ".tmp"
$envPath = Join-Path $repoRoot ".env"
$resultPath = Join-Path $tmpDir "p4-real-acceptance.json"
$e2eDbName = "eztest_e2e"
$apiPort = 13333
$apiBaseUrl = "http://localhost:$apiPort"
$repoKey = "p4-acceptance-repo"
$acceptanceBranchPrefix = "p4-acceptance"
$requirementMarker = "P4-AI-IMPACT"
$maxChangedFiles = 20
$pollIntervalSeconds = 2
$pollTimeoutSeconds = 420
$evidenceVerifyLimit = 20
$factsPageSize = 100
# The index caps a repository at 50000 files (EZTEST_CODEINTEL_MAX_INDEX_FILES),
# so 500 pages of 100 always lets the coverage check read every persisted fact;
# an unfinished pagination is a hard failure instead of a silent partial check.
$maxFactPages = 500
$requiredEnvKeys = @(
    "EZTEST_DATABASE_URL",
    "EZTEST_ADMIN_USERNAME",
    "EZTEST_ADMIN_PASSWORD",
    "EZTEST_SECRET_KEYS",
    "EZTEST_SECRET_KEY_ACTIVE",
    "llm_base_url",
    "llm_key",
    "llm_model_name"
)

# Secrets parsed from .env live here only so error text can be redacted. They
# are never printed, logged or written to the result JSON.
$script:secretValues = @()
$script:checks = New-Object System.Collections.ArrayList
$script:warnings = New-Object System.Collections.ArrayList
$script:resultData = [ordered]@{
    status       = "running"
    generatedAt  = [DateTime]::UtcNow.ToString("o")
    baseCommit   = $null
    headCommit   = $null
    branch       = $null
    versionId    = $null
    analysisId   = $null
    analysisVersion = $null
    model        = $null
    promptVersion = $null
    forceAnalysisId = $null
    forceAnalysisVersion = $null
    reused       = $null
    degraded     = $null
    allowDegraded = $false
    llmConfigured = $null
    changedFiles = $null
    factCount    = $null
    anchorCount  = $null
    evidenceCount = $null
    verifiedEvidence = $null
    candidateCount = $null
    requirementCandidateCount = $null
    unmappedCount = $null
    statsUnmapped = $null
    unmappedTruncated = $null
    allowNoUnmapped = $false
    secondVersionId = $null
    secondAnalysisId = $null
    cacheHits    = $null
    checks       = @()
    warnings     = @()
    error        = $null
}

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) { throw "Missing .env at $Path" }
    foreach ($line in Get-Content -LiteralPath $Path) {
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

function Assert-SafeDatabaseUrl {
    param([string]$DatabaseUrl)
    # pgx applies query parameters over the URL path and host:port, so a URL
    # whose path names a dedicated e2e database can still connect to another
    # database via ?dbname=/?database= (or redirect the host/port). The script
    # resets eztest_e2e through psql authority while the API receives the URL,
    # so reject target-changing keys before any destructive statement instead
    # of trusting the path. Error messages never echo the URL or credentials.
    $uri = $null
    if (-not [System.Uri]::TryCreate($DatabaseUrl, [System.UriKind]::Absolute, [ref]$uri)) {
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        throw "EZTEST_DATABASE_URL must use the postgres scheme"
    }
    if ([string]::IsNullOrWhiteSpace($uri.Host)) {
        throw "EZTEST_DATABASE_URL must include a host"
    }
    # .NET reports -1 when the URL omits the port; psql/pgx then default to 5432.
    if ($uri.Port -ne -1 -and ($uri.Port -lt 1 -or $uri.Port -gt 65535)) {
        throw "EZTEST_DATABASE_URL port must be between 1 and 65535 when present"
    }
    $query = $uri.Query
    if (-not [string]::IsNullOrWhiteSpace($query)) {
        foreach ($pair in ($query.TrimStart([char]"?")) -split "&") {
            if ([string]::IsNullOrWhiteSpace($pair)) { continue }
            $equals = $pair.IndexOf("=")
            $key = if ($equals -ge 0) { $pair.Substring(0, $equals) } else { $pair }
            $key = [System.Uri]::UnescapeDataString($key).Trim().ToLowerInvariant()
            if ($key -eq "dbname" -or $key -eq "database" -or $key -eq "host" -or $key -eq "port") {
                throw "EZTEST_DATABASE_URL must not override the connection target with the query parameter '$key'; put the host, port and database in the URL itself"
            }
        }
    }
    $databaseName = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($databaseName) -or $databaseName.Contains("/")) {
        throw "EZTEST_DATABASE_URL must include a single database name in its path"
    }
    return $uri
}

function New-E2eDatabaseUrl {
    param([System.Uri]$DatabaseUri, [string]$DatabaseName)
    # Rebuild rather than path-splice so the validated query string (for example
    # sslmode) survives while the database path is replaced deterministically.
    $builder = New-Object System.UriBuilder -ArgumentList @($DatabaseUri)
    $builder.Path = "/" + $DatabaseName
    return $builder.Uri.AbsoluteUri
}

function Get-DatabaseTargetName {
    param([System.Uri]$Uri)
    $name = [System.Uri]::UnescapeDataString($Uri.AbsolutePath.TrimStart([char[]]"/"))
    $port = if ($Uri.Port -gt 0) { $Uri.Port } else { 5432 }
    return ("{0}:{1}/{2}" -f $Uri.Host.ToLowerInvariant(), $port, $name)
}

function Resolve-ToolPath {
    param([string]$LocalPath, [string]$CommandName)
    if (-not [string]::IsNullOrWhiteSpace($LocalPath) -and (Test-Path -LiteralPath $LocalPath -PathType Leaf)) {
        return $LocalPath
    }
    $command = Get-Command $CommandName -ErrorAction SilentlyContinue
    if ($null -ne $command -and -not [string]::IsNullOrWhiteSpace([string]$command.Source)) {
        return [string]$command.Source
    }
    return ""
}

function Get-RedactedText {
    param([string]$Text)
    if ([string]::IsNullOrEmpty($Text)) { return "" }
    foreach ($secret in $script:secretValues) {
        if (-not [string]::IsNullOrEmpty($secret)) {
            $Text = $Text.Replace($secret, "[redacted]")
        }
    }
    $Text = $Text -replace "[\r\n]+", " "
    if ($Text.Length -gt 400) { $Text = $Text.Substring(0, 400) + "..." }
    return $Text
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

function Assert-PortFree {
    param([int]$Port)
    $listeners = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue
    if ($listeners) { throw "Port $Port is already in use; stop the other process or set EZTEST_ACCEPTANCE_API_URL" }
}

function Wait-ForHttp {
    param([string]$Url, [int]$TimeoutSeconds, $Process, [string]$Label)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($null -ne $Process) {
            $Process.Refresh()
            if ($Process.HasExited) { throw "$Label exited early with code $($Process.ExitCode)" }
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

function Get-PsqlScalar {
    param([string]$Database, [string]$Sql)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        $output = & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d $Database -tA -c $Sql 2>$null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) {
        throw "psql scalar query failed (exit $code)"
    }
    return (($output | Out-String).Trim())
}

function Invoke-PsqlQuiet {
    param([string]$Database, [string]$Sql, [string]$Operation)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $psqlExe -h $dbHost -p $dbPort -U $dbUser -d $Database -v ON_ERROR_STOP=1 -q -c $Sql *> $null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "$Operation failed (psql exit $code)" }
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Uri,
        $Body = $null,
        $Headers = $null,
        [int]$TimeoutSec = 30
    )
    $arguments = @{
        Method     = $Method
        Uri        = $Uri
        TimeoutSec = $TimeoutSec
        UseBasicParsing = $true
    }
    if ($null -ne $Headers) { $arguments["Headers"] = $Headers }
    if ($null -ne $Body) {
        $json = $Body | ConvertTo-Json -Compress -Depth 8
        $arguments["ContentType"] = "application/json; charset=utf-8"
        # Windows PowerShell 5.1 would otherwise encode non-ASCII request text
        # with the ANSI code page; always send explicit UTF-8 bytes.
        $arguments["Body"] = [System.Text.Encoding]::UTF8.GetBytes($json)
    }
    $response = Invoke-WebRequest @arguments
    $parsed = $null
    if (-not [string]::IsNullOrWhiteSpace([string]$response.Content)) {
        $parsed = $response.Content | ConvertFrom-Json
    }
    $data = $null
    if ($null -ne $parsed) { $data = $parsed.data }
    return [pscustomobject]@{
        Status = [int]$response.StatusCode
        Raw    = $parsed
        Data   = $data
    }
}

function Get-ApiErrorSummary {
    param($ErrorRecord)
    $status = 0
    $code = ""
    $reason = ""
    $rawBody = $null
    try {
        $response = $ErrorRecord.Exception.Response
        if ($null -ne $response) {
            $status = [int]$response.StatusCode
            if ($response -is [System.Net.HttpWebResponse]) {
                $stream = $response.GetResponseStream()
                if ($null -ne $stream) {
                    $reader = New-Object System.IO.StreamReader($stream)
                    try { $rawBody = $reader.ReadToEnd() } finally { $reader.Close() }
                }
            } elseif ($null -ne $response.Content) {
                $rawBody = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            }
        }
    } catch {
        # Keep only what was extracted safely.
    }
    if ([string]::IsNullOrWhiteSpace($rawBody)) {
        try {
            if ($null -ne $ErrorRecord.ErrorDetails -and -not [string]::IsNullOrWhiteSpace($ErrorRecord.ErrorDetails.Message)) {
                $rawBody = $ErrorRecord.ErrorDetails.Message
            }
        } catch {
            # No body available.
        }
    }
    if (-not [string]::IsNullOrWhiteSpace($rawBody)) {
        # Only the shared error envelope's code and details.reason are reported;
        # the body and any echoed request data are deliberately discarded.
        try {
            $parsed = $rawBody | ConvertFrom-Json
            $code = [string]$parsed.error.code
            if ($null -ne $parsed.error.details -and $null -ne $parsed.error.details.reason) {
                $reason = [string]$parsed.error.details.reason
            }
        } catch {
            $code = ""
            $reason = ""
        }
    }
    if ($status -gt 0) {
        $parts = @("HTTP $status")
        if (-not [string]::IsNullOrWhiteSpace($code)) { $parts += "error_code=$code" }
        if (-not [string]::IsNullOrWhiteSpace($reason)) { $parts += "reason=$reason" }
        return ($parts -join " ")
    }
    return (Get-RedactedText -Text ([string]$ErrorRecord.Exception.Message))
}

function Invoke-GitText {
    param([string[]]$Arguments)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        $output = & $gitExe -C $repoPath @Arguments
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "git command failed with exit code $code" }
    return (($output | Out-String).TrimEnd())
}

function Resolve-GitCommit {
    param([string]$Spec)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        $output = & $gitExe -C $repoPath rev-parse --verify "$Spec^{commit}" 2>$null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { return "" }
    $resolved = (($output | Out-String).Trim())
    if ($resolved -notmatch '^[a-f0-9]{40}$') { return "" }
    return $resolved
}

function Test-GitAncestor {
    param([string]$Base, [string]$Head)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $gitExe -C $repoPath merge-base --is-ancestor $Base $Head 2>$null
        return ($LASTEXITCODE -eq 0)
    } finally {
        $ErrorActionPreference = $previous
    }
}

function ConvertTo-DiffEntries {
    param([string]$Base, [string]$Head)
    $previous = $ErrorActionPreference
    $previousEncoding = $null
    try { $previousEncoding = [Console]::OutputEncoding } catch { }
    $ErrorActionPreference = "SilentlyContinue"
    try {
        # Decode git's raw path bytes as UTF-8, matching the backend's -z parser;
        # Windows PowerShell 5.1 otherwise uses the console code page.
        try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false) } catch { }
        # -z disables path quoting. The backend parses the same NUL-separated
        # records with --find-renames=50%, so mirror the rename threshold and
        # the full status strings (for example R100) instead of guessing.
        $output = & $gitExe -C $repoPath diff --name-status -z --no-ext-diff --no-textconv --find-renames=50% $Base $Head
        $code = $LASTEXITCODE
    } finally {
        if ($null -ne $previousEncoding) {
            try { [Console]::OutputEncoding = $previousEncoding } catch { }
        }
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "git diff --name-status failed with exit code $code" }

    $text = (-join @($output))
    $records = $text -split "`0"
    $entries = @()
    for ($index = 0; $index -lt $records.Count;) {
        $status = [string]$records[$index]
        $index++
        if ([string]::IsNullOrEmpty($status)) { continue }
        if ($status -notmatch '^[A-Z][0-9]*$') {
            throw "git diff --name-status returned an unexpected status token"
        }
        if ($status.StartsWith("R") -or $status.StartsWith("C")) {
            if ($index + 1 -ge $records.Count) {
                throw "git diff --name-status returned a rename/copy record without two paths"
            }
            $oldPath = [string]$records[$index]
            $newPath = [string]$records[$index + 1]
            $index += 2
            if ([string]::IsNullOrEmpty($oldPath) -or [string]::IsNullOrEmpty($newPath)) {
                throw "git diff --name-status returned a rename/copy record with an empty path"
            }
            $entries += [pscustomobject]@{
                Path    = $newPath
                OldPath = $oldPath
                Status  = $status
            }
        } else {
            if ($index -ge $records.Count) {
                throw "git diff --name-status returned a record without a path"
            }
            $path = [string]$records[$index]
            $index++
            if ([string]::IsNullOrEmpty($path)) {
                throw "git diff --name-status returned a record with an empty path"
            }
            $entries += [pscustomobject]@{
                Path    = $path
                OldPath = $null
                Status  = $status
            }
        }
    }
    return ,$entries
}

function Test-PreferredDiff {
    param($Entries)
    return (@($Entries | Where-Object {
                $_.Status -eq "D" -or $_.Path -match '\.(md|txt|rst|json|ya?ml)$'
            }).Count -gt 0)
}

function Select-CommitPair {
    param([string]$RepositoryPath)
    $explicitBase = $env:EZTEST_ACCEPTANCE_P4_BASE
    $explicitHead = $env:EZTEST_ACCEPTANCE_P4_HEAD

    $headSpec = "HEAD"
    if (-not [string]::IsNullOrWhiteSpace($explicitHead)) {
        $headSpec = $explicitHead
    } elseif ([string]::IsNullOrWhiteSpace($env:EZTEST_ACCEPTANCE_P4_REPO)) {
        # The default acceptance clone is created fresh from this checkout, so
        # origin/HEAD names the current default branch tip instead of a stale
        # local branch.
        $originHead = Resolve-GitCommit -Spec "origin/HEAD"
        if (-not [string]::IsNullOrWhiteSpace($originHead)) { $headSpec = $originHead }
    }

    $head = Resolve-GitCommit -Spec $headSpec
    if ([string]::IsNullOrWhiteSpace($head)) {
        throw "could not resolve EZTEST_ACCEPTANCE_P4_HEAD (or HEAD) to a 40-hex commit"
    }

    if (-not [string]::IsNullOrWhiteSpace($explicitBase)) {
        $base = Resolve-GitCommit -Spec $explicitBase
        if ([string]::IsNullOrWhiteSpace($base)) {
            throw "could not resolve EZTEST_ACCEPTANCE_P4_BASE to a 40-hex commit"
        }
        if ($base -eq $head) { throw "base and head must be different commits" }
        if (-not (Test-GitAncestor -Base $base -Head $head)) {
            throw "EZTEST_ACCEPTANCE_P4_BASE is not an ancestor of EZTEST_ACCEPTANCE_P4_HEAD"
        }
        $entries = ConvertTo-DiffEntries -Base $base -Head $head
        $count = @($entries).Count
        if ($count -lt 1 -or $count -gt $maxChangedFiles) {
            throw "the explicit base..head pair changes $count files; P4 acceptance needs 1-$maxChangedFiles"
        }
        return [pscustomobject]@{ Base = $base; Head = $head; Entries = $entries }
    }

    # No explicit base: search back a few commits for a real pair with 1-20
    # changed files, preferring a pair that also contains a documentation or
    # deleted file (a likely unmapped change) and at least two files.
    $fallback = $null
    $preferred = $null
    for ($depth = 1; $depth -le 8; $depth++) {
        $candidateBase = Resolve-GitCommit -Spec "$head~$depth"
        if ([string]::IsNullOrWhiteSpace($candidateBase)) { continue }
        if ($candidateBase -eq $head) { continue }
        $entries = ConvertTo-DiffEntries -Base $candidateBase -Head $head
        $count = @($entries).Count
        if ($count -lt 1 -or $count -gt $maxChangedFiles) { continue }
        $pair = [pscustomobject]@{ Base = $candidateBase; Head = $head; Entries = $entries }
        if ($null -eq $fallback) { $fallback = $pair }
        if ($count -ge 2 -and (Test-PreferredDiff -Entries $entries)) {
            $preferred = $pair
            break
        }
        if ($null -eq $preferred -and $count -ge 2) {
            $preferred = $pair
        }
    }
    if ($null -ne $preferred) { return $preferred }
    if ($null -ne $fallback) { return $fallback }
    throw "no recent commit pair with 1-$maxChangedFiles changed files; set EZTEST_ACCEPTANCE_P4_BASE/HEAD explicitly"
}

function Initialize-RepoWorkspace {
    # Never create refs or fetch in the caller's repository: resolve the source,
    # then read it only through `git clone` into the gitignored .tmp workspace.
    # The clone is recreated on every run so a stale branch/commit from an earlier
    # acceptance run can never be reused.
    $sourceRepo = $repoRoot
    $explicitRepo = $env:EZTEST_ACCEPTANCE_P4_REPO
    if (-not [string]::IsNullOrWhiteSpace($explicitRepo)) {
        $candidate = [string]$explicitRepo
        if (-not (Test-Path -LiteralPath $candidate -PathType Container)) {
            throw "EZTEST_ACCEPTANCE_P4_REPO does not exist: $candidate"
        }
        $sourceRepo = (Resolve-Path -LiteralPath $candidate -ErrorAction Stop).ProviderPath
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        try {
            & $gitExe -C $sourceRepo rev-parse --git-dir *> $null
            $gitDirExit = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
        }
        if ($gitDirExit -ne 0) {
            throw "EZTEST_ACCEPTANCE_P4_REPO is not a Git repository: $sourceRepo"
        }
    }

    $clonePath = Join-Path $tmpDir "p4-real-repo"
    $sourceFull = [System.IO.Path]::GetFullPath($sourceRepo)
    $cloneFull = [System.IO.Path]::GetFullPath($clonePath)
    if ($sourceFull.Equals($cloneFull, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "EZTEST_ACCEPTANCE_P4_REPO must be a source repository, not the .tmp/p4-real-repo clone target"
    }
    $tmpFull = [System.IO.Path]::GetFullPath($tmpDir).TrimEnd([char[]]"\/")
    if (-not $cloneFull.StartsWith($tmpFull + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "refusing to use an acceptance clone outside .tmp: $cloneFull"
    }

    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
    if (Test-Path -LiteralPath $clonePath) {
        Write-Step "Replacing the previous disposable P4 acceptance clone at .tmp/p4-real-repo"
        Remove-Item -LiteralPath $clonePath -Recurse -Force
    }

    Write-Step "Cloning the source repository into .tmp/p4-real-repo (source is never written)"
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $gitExe clone --quiet --no-hardlinks $sourceFull $clonePath 2>$null
        $cloneExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($cloneExit -ne 0) { throw "git clone of the P4 acceptance source failed (exit $cloneExit)" }
    if (-not (Test-Path -LiteralPath (Join-Path $clonePath ".git"))) {
        throw "the P4 acceptance clone does not contain a Git worktree: $clonePath"
    }
    return $clonePath
}

function Ensure-CodeRepo {
    param([string]$RepositoryPath, [string]$Branch, [hashtable]$Headers)
    $list = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/resources/code-repos?limit=100" -Headers $Headers -TimeoutSec 30
    $existing = $null
    if ($null -ne $list.Data) {
        foreach ($item in @($list.Data)) {
            if ($null -ne $item -and [string]$item.key -eq $repoKey) { $existing = $item; break }
        }
    }
    if ($null -ne $existing) {
        $needsPatch = ([string]$existing.localPath -ne $RepositoryPath) -or
            ([string]$existing.fixedBranch -ne $Branch) -or
            ([string]$existing.status -ne "active")
        if ($needsPatch) {
            $patched = Invoke-Api -Method Patch -Uri "$apiBaseUrl/api/resources/code-repos/$($existing.id)" `
                -Headers $Headers -TimeoutSec 60 -Body @{
                    localPath   = $RepositoryPath
                    fixedBranch = $Branch
                    status      = "active"
                }
            return $patched.Data
        }
        return $existing
    }
    $created = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/resources/code-repos" -Headers $Headers -TimeoutSec 60 -Body @{
        key         = $repoKey
        name        = "P4 real acceptance repository"
        localPath   = $RepositoryPath
        fixedBranch = $Branch
    }
    return $created.Data
}

function Get-VersionChangeList {
    param([string]$VersionId, [string]$Head, [hashtable]$Headers)
    $files = @()
    $first = $null
    $cursor = ""
    for ($page = 0; $page -lt 10; $page++) {
        $url = "$apiBaseUrl/api/versions/$VersionId/code/changes?head=$Head&limit=100"
        if (-not [string]::IsNullOrWhiteSpace($cursor)) {
            $url = $url + "&cursor=" + [System.Uri]::EscapeDataString($cursor)
        }
        $response = Invoke-Api -Method Get -Uri $url -Headers $Headers -TimeoutSec 60
        if ($null -eq $first) { $first = $response.Data }
        if ($null -eq $response.Data) { break }
        $files += @($response.Data.files)
        if (-not [bool]$response.Data.hasMore) { break }
        $cursor = [string]$response.Data.nextCursor
        if ([string]::IsNullOrWhiteSpace($cursor)) { break }
    }
    return [pscustomobject]@{ First = $first; Files = $files }
}

function Assert-ChangeListMatchesGit {
    param($GitEntries, $ApiFiles)
    $gitCount = @($GitEntries).Count
    $apiCount = @($ApiFiles).Count
    if ($gitCount -ne $apiCount) {
        throw "version change list has $apiCount files but git diff reports $gitCount"
    }
    $apiByPath = @{}
    foreach ($file in @($ApiFiles)) {
        if ($null -eq $file) { continue }
        $path = [string]$file.path
        if ($apiByPath.ContainsKey($path)) {
            throw "version change list contains the same path twice: $path"
        }
        $apiByPath[$path] = $file
    }
    foreach ($entry in @($GitEntries)) {
        $path = [string]$entry.Path
        if (-not $apiByPath.ContainsKey($path)) {
            throw "version change list is missing a file reported by git diff: $path"
        }
        $apiFile = $apiByPath[$path]
        # Both sides use --find-renames=50%, so the full status string (M, A,
        # D, T, R100, C075) must match; comparing only the first character
        # would hide a rename/copy mismatch.
        if ([string]$apiFile.status -ne [string]$entry.Status) {
            throw "version change list status mismatch for $($path): api=$([string]$apiFile.status) git=$([string]$entry.Status)"
        }
        if ($null -ne $entry.OldPath) {
            if ([string]::IsNullOrWhiteSpace([string]$apiFile.oldPath) -or
                [string]$apiFile.oldPath -ne [string]$entry.OldPath) {
                throw "version change list rename oldPath mismatch for $($path)"
            }
        }
    }
}

function Get-CodeFacts {
    param([int64]$RepoId, [string]$Commit, [hashtable]$Headers)
    # Facts are paged; the unmapped-coverage assertion is only sound when every
    # page was read, so an unfinished pagination is a hard failure rather than a
    # silent partial set.
    $facts = @()
    $cursor = ""
    for ($page = 0; $page -lt $maxFactPages; $page++) {
        $url = "$apiBaseUrl/api/code/repos/$RepoId/facts?commit=" + [System.Uri]::EscapeDataString($Commit) + "&limit=$factsPageSize"
        if (-not [string]::IsNullOrWhiteSpace($cursor)) {
            $url = $url + "&cursor=" + [System.Uri]::EscapeDataString($cursor)
        }
        $response = Invoke-Api -Method Get -Uri $url -Headers $Headers -TimeoutSec 60
        if ($null -ne $response.Raw.data) { $facts += @($response.Raw.data) }
        if (-not [bool]$response.Raw.has_more) {
            return [pscustomobject]@{ Facts = $facts; Pages = $page + 1 }
        }
        $cursor = [string]$response.Raw.next_cursor
        if ([string]::IsNullOrWhiteSpace($cursor)) {
            throw "facts pagination reported has_more=true without a next_cursor"
        }
    }
    throw "facts pagination still reported has_more=true after $maxFactPages pages; refusing a partial unmapped-coverage check"
}

function Get-VersionAnchors {
    param([string]$VersionId, [hashtable]$Headers)
    $response = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$VersionId/code/anchors" -Headers $Headers -TimeoutSec 30
    $anchors = @()
    if ($null -ne $response.Data) { $anchors = @($response.Data) }
    # Emit the elements (not the array as one object) so the caller's @() wrap
    # yields one item per anchor for any anchor count.
    return $anchors
}

function Wait-Analysis {
    param([string]$VersionId, [string]$AnalysisId, [hashtable]$Headers, [int]$TimeoutSeconds)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $lastStatus = "unknown"
    while ((Get-Date) -lt $deadline) {
        try {
            $response = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$VersionId/code/analyses/$AnalysisId" -Headers $Headers -TimeoutSec 30
        } catch {
            Start-Sleep -Seconds $pollIntervalSeconds
            continue
        }
        $lastStatus = [string]$response.Data.status
        if ($lastStatus -eq "done" -or $lastStatus -eq "failed") {
            return $response
        }
        Start-Sleep -Seconds $pollIntervalSeconds
    }
    throw "timed out after ${TimeoutSeconds}s waiting for analysis (last status=$lastStatus)"
}

function Get-CandidateFingerprint {
    param($Candidates)
    # Canonical form for the spec §10.7 step 4 comparison: candidates sorted by
    # id, evidence sorted by file/range/hash, and only contract fields emitted
    # (so projection/order noise cannot mask a real candidate change).
    $normalized = @()
    foreach ($candidate in @(@($Candidates) | Sort-Object -Property { [string]$_.id })) {
        if ($null -eq $candidate) { continue }
        $evidence = @()
        $sortable = @()
        foreach ($item in @($candidate.evidence)) {
            if ($null -eq $item) { continue }
            $startLine = $(if ($null -eq $item.startLine) { $null } else { [int]$item.startLine })
            $endLine = $(if ($null -eq $item.endLine) { $null } else { [int]$item.endLine })
            $excerptHash = $(if ($null -eq $item.excerptHash) { $null } else { [string]$item.excerptHash })
            $sortable += [pscustomobject]@{
                SortKey     = "{0}|{1}|{2}|{3}" -f [string]$item.file, $(if ($null -eq $startLine) { "-" } else { $startLine }), $(if ($null -eq $endLine) { "-" } else { $endLine }), [string]$excerptHash
                File        = [string]$item.file
                StartLine   = $startLine
                EndLine     = $endLine
                ExcerptHash = $excerptHash
            }
        }
        foreach ($item in @($sortable | Sort-Object -Property SortKey)) {
            $evidence += [ordered]@{
                file        = $item.File
                startLine   = $item.StartLine
                endLine     = $item.EndLine
                excerptHash = $item.ExcerptHash
            }
        }
        $normalized += [ordered]@{
            id          = [string]$candidate.id
            subjectKind = [string]$candidate.subjectKind
            subjectId   = [string]$candidate.subjectId
            reason      = [string]$candidate.reason
            confidence  = [string]$candidate.confidence
            source      = [string]$candidate.source
            evidence    = @($evidence)
            selected    = [bool]$candidate.selected
        }
    }
    return (ConvertTo-Json -InputObject @($normalized) -Depth 10 -Compress)
}

function Write-EvidenceHashHelper {
    $helperPath = Join-Path $tmpDir "p4-evidence-hash.cjs"
    $code = @'
"use strict";
// Recompute the spec section 6.3 excerpt hash from the commit-pinned Git blob.
// This helper deliberately prints only the hash, never file content.
const { execFileSync } = require("node:child_process");
const { createHash } = require("node:crypto");

const [repo, commit, file, startRaw, endRaw] = process.argv.slice(2);
const start = Number(startRaw);
const end = Number(endRaw);
if (!Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end < start) {
  process.exit(3);
}
let bytes;
try {
  bytes = execFileSync("git", ["-C", repo, "cat-file", "blob", `${commit}:${file}`], {
    maxBuffer: 8 * 1024 * 1024,
    stdio: ["ignore", "pipe", "ignore"],
  });
} catch {
  process.exit(2);
}
const lines = (() => {
  // Mirror application/evidence.go commitContentLines (spec section 6.3):
  // split on "\n", drop the implicit trailing empty element only when the
  // content ends with "\n", and strip one trailing "\r" per line. A zero-byte
  // blob has zero lines (the backend rejects evidence for it), while a file
  // containing only "\n" has one empty line.
  if (bytes.length === 0) return [];
  if (bytes.includes(0)) process.exit(3);
  let text;
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    process.exit(3);
  }
  const parts = text.split("\n");
  if (parts.length > 0 && parts[parts.length - 1] === "" && text.endsWith("\n")) parts.pop();
  return parts.map((line) => (line.endsWith("\r") ? line.slice(0, -1) : line));
})();
if (start > lines.length || end > lines.length) process.exit(3);
const excerpt = lines.slice(start - 1, end).join("\n");
process.stdout.write("sha256:" + createHash("sha256").update(excerpt, "utf8").digest("hex") + "\n");
'@
    [System.IO.File]::WriteAllText($helperPath, $code, (New-Object System.Text.UTF8Encoding($false)))
    return $helperPath
}

function Add-CheckResult {
    param([string]$Name, [string]$Status, [string]$Detail)
    [void]$script:checks.Add([pscustomobject]@{
            name   = $Name
            status = $Status
            detail = $Detail
        })
    $color = "Green"
    if ($Status -eq "skipped") { $color = "Yellow" }
    if ($Status -eq "failed") { $color = "Red" }
    Write-Host ("{0} {1}: {2}" -f $Status.ToUpperInvariant(), $Name, $Detail) -ForegroundColor $color
}

function Write-AcceptanceResult {
    $script:resultData.checks = @($script:checks)
    $script:resultData.warnings = @($script:warnings)
    $json = ConvertTo-Json -InputObject $script:resultData -Depth 10
    [System.IO.File]::WriteAllText($resultPath, $json + "`n", (New-Object System.Text.UTF8Encoding($false)))
}

function Invoke-DryRun {
    Write-Step "DryRun: validating paths and environment (no clone, database reset, API or LLM call)"
    $problems = @()

    if (-not (Test-Path -LiteralPath $repoRoot -PathType Container)) { $problems += "repository root not found" }
    if (-not (Test-Path -LiteralPath $backendDir -PathType Container)) { $problems += "backend directory not found" }

    $gitExe = Resolve-ToolPath -LocalPath "" -CommandName "git"
    $nodeExe = Resolve-ToolPath -LocalPath "" -CommandName "node"
    if ([string]::IsNullOrWhiteSpace($gitExe)) { $problems += "git executable not found on PATH" }
    if ([string]::IsNullOrWhiteSpace($nodeExe)) { $problems += "node executable not found on PATH" }

    $presentKeys = @()
    $dryRunTarget = $null
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
        $problems += ".env not found at $envPath"
    } else {
        $envValues = Read-DotEnv -Path $envPath
        foreach ($key in $requiredEnvKeys) {
            if (-not $envValues.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$envValues[$key])) {
                $problems += "missing .env key: $key"
            } else {
                $presentKeys += $key
            }
        }
        # Preflight the destructive part of the real run: the same guard runs
        # before any database reset, so DryRun proves the URL cannot redirect
        # the API away from the eztest_e2e database that psql resets.
        if ($envValues.ContainsKey("EZTEST_DATABASE_URL") -and
            -not [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_DATABASE_URL"])) {
            try {
                $dryRunDatabaseUri = Assert-SafeDatabaseUrl -DatabaseUrl ([string]$envValues["EZTEST_DATABASE_URL"])
                $dryRunE2eUrl = New-E2eDatabaseUrl -DatabaseUri $dryRunDatabaseUri -DatabaseName $e2eDbName
                $dryRunE2eUri = [System.Uri]$dryRunE2eUrl
                $dryRunTarget = Get-DatabaseTargetName -Uri $dryRunE2eUri
                $dryRunPort = if ($dryRunDatabaseUri.Port -gt 0) { $dryRunDatabaseUri.Port } else { 5432 }
                $psqlTarget = "{0}:{1}/{2}" -f $dryRunDatabaseUri.Host.ToLowerInvariant(), $dryRunPort, $e2eDbName
                if ($psqlTarget -ne $dryRunTarget) {
                    $problems += "EZTEST_DATABASE_URL preflight: the rebuilt API target differs from the psql target"
                }
            } catch {
                $problems += ("EZTEST_DATABASE_URL preflight: " + $_.Exception.Message)
            }
        }
    }

    if ([string]::IsNullOrWhiteSpace($env:EZTEST_ACCEPTANCE_API_URL)) {
        $goExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\go\bin\go.exe") -CommandName "go"
        $psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql"
        if ([string]::IsNullOrWhiteSpace($goExe)) { $problems += "go executable not found (.tools or PATH)" }
        if ([string]::IsNullOrWhiteSpace($psqlExe)) { $problems += "psql executable not found (.tools or PATH)" }
    }

    if ($problems.Count -gt 0) {
        Write-Host "DRYRUN FAIL:" -ForegroundColor Red
        foreach ($problem in $problems) { Write-Host "  - $problem" -ForegroundColor Red }
        return 1
    }
    Write-Host "DryRun: repository root: $repoRoot"
    Write-Host ("DryRun: .env found; required keys present ({0}/{1})" -f $presentKeys.Count, $requiredEnvKeys.Count)
    if ($null -ne $dryRunTarget) {
        Write-Host ("DryRun: database preflight OK; API and psql both target {0} (no target-changing query keys)" -f $dryRunTarget)
    }
    Write-Host "DryRun: git executable found: $gitExe"
    Write-Host "DryRun: node executable found: $nodeExe"
    if ([string]::IsNullOrWhiteSpace($env:EZTEST_ACCEPTANCE_P4_REPO)) {
        Write-Host "DryRun: the real run will delete/reclone .tmp/p4-real-repo from this checkout (source is never written)"
    } else {
        Write-Host "DryRun: the real run will delete/reclone .tmp/p4-real-repo from EZTEST_ACCEPTANCE_P4_REPO (source is never written)"
    }
    if ([string]::IsNullOrWhiteSpace($env:EZTEST_ACCEPTANCE_API_URL)) {
        Write-Host "DryRun: the real run will reset $e2eDbName, build and start the API on :$apiPort"
    } else {
        Write-Host "DryRun: the real run will reuse the API at EZTEST_ACCEPTANCE_API_URL"
    }
    if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
        Write-Host "DryRun: gate is closed (EZTEST_RUN_REAL_LLM != 1); the real run would print SKIPPED and exit 0."
    } else {
        Write-Host "DryRun: gate is open; the real run will make paid LLM calls."
    }
    if ($AllowDegraded) {
        Write-Host "DryRun: -AllowDegraded is set; a degraded analysis would be recorded as passed_degraded, not failed."
    } else {
        Write-Host "DryRun: llm_configured=false or a degraded analysis will fail the run by default (pass -AllowDegraded to record it instead)."
    }
    if ($AllowNoUnmapped) {
        Write-Host "DryRun: -AllowNoUnmapped is set; unmapped=0 will be recorded as skipped, not failed."
    } else {
        Write-Host "DryRun: at least one real unmapped change will be required (pass -AllowNoUnmapped to relax)."
    }
    Write-Host "DRYRUN OK" -ForegroundColor Green
    return 0
}

if ($DryRun) {
    exit (Invoke-DryRun)
}

if ($env:EZTEST_RUN_REAL_LLM -ne "1") {
    Write-Host "SKIPPED: real codeintel LLM acceptance is disabled (set EZTEST_RUN_REAL_LLM=1 to run it)."
    exit 0
}

# Spec §10.7 step 3 requires at least one real unmapped file. The relaxation is
# explicit and printed so a fully mapped pair cannot pass silently.
$script:resultData.allowNoUnmapped = [bool]$AllowNoUnmapped
if ($AllowNoUnmapped) {
    Write-Host "Unmapped policy: -AllowNoUnmapped is set; unmapped=0 will be recorded as skipped, not failed." -ForegroundColor Yellow
} else {
    Write-Host "Unmapped policy: at least one real unmapped change is required (pass -AllowNoUnmapped to relax)."
}

# R43: a degraded run is not real-LLM acceptance, so it fails closed unless the
# caller explicitly opts in. The relaxation is recorded in the result JSON.
$script:resultData.allowDegraded = [bool]$AllowDegraded
if ($AllowDegraded) {
    Write-Host "Degraded policy: -AllowDegraded is set; a degraded analysis will be recorded as passed_degraded, not failed." -ForegroundColor Yellow
} else {
    Write-Host "Degraded policy: llm_configured=false or a degraded analysis will fail the run (pass -AllowDegraded to record it instead)."
}

$exitCode = 0
$apiProcess = $null
$repoPath = ""
$gitExe = ""
$nodeExe = ""
$psqlExe = ""
$goExe = ""
$baseCommit = ""
$headCommit = ""
$repoId = 0
$versionId = ""
$reqIds = @()

try {
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null

    Write-Step "Loading .env and validating required keys"
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) { throw "Missing .env at $envPath" }
    $envValues = Read-DotEnv -Path $envPath

    $missingKeys = @()
    foreach ($key in $requiredEnvKeys) {
        if (-not $envValues.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$envValues[$key])) {
            $missingKeys += $key
        }
    }
    if ($missingKeys.Count -gt 0) {
        throw ("EZTEST_RUN_REAL_LLM=1 but .env is missing required key(s): " + ($missingKeys -join ", ") + ". Add the missing key(s) to .env or unset EZTEST_RUN_REAL_LLM.")
    }

    $adminUsername = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_USERNAME"
    $adminPassword = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_PASSWORD"
    $llmKey = Get-RequiredValue -Values $envValues -Key "llm_key"
    $llmBaseURL = Get-RequiredValue -Values $envValues -Key "llm_base_url"
    $llmModel = Get-RequiredValue -Values $envValues -Key "llm_model_name"
    $databaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_DATABASE_URL"

    $script:secretValues = @($adminPassword, $llmKey, $databaseUrl)
    if ($envValues.ContainsKey("EZTEST_SECRET_KEYS")) {
        $rawSecretKeys = [string]$envValues["EZTEST_SECRET_KEYS"]
        if (-not [string]::IsNullOrWhiteSpace($rawSecretKeys)) {
            $script:secretValues += $rawSecretKeys
            try {
                $secretObject = $rawSecretKeys | ConvertFrom-Json
                foreach ($property in $secretObject.PSObject.Properties) {
                    if ($null -ne $property.Value -and -not [string]::IsNullOrWhiteSpace([string]$property.Value)) {
                        $script:secretValues += [string]$property.Value
                    }
                }
            } catch {
                # A malformed keyring is reported by the API at startup.
            }
        }
    }

    # Validate the URL and rebuild the eztest_e2e API URL from its validated
    # components before any destructive psql statement. The query may keep
    # non-target keys such as sslmode, but dbname/database/host/port are
    # rejected by Assert-SafeDatabaseUrl because pgx would let them override
    # the path/host/port the psql reset uses.
    $databaseUri = Assert-SafeDatabaseUrl -DatabaseUrl $databaseUrl
    $userInfo = $databaseUri.UserInfo
    $separator = $userInfo.IndexOf(":")
    if ($separator -lt 0) { throw "EZTEST_DATABASE_URL must contain a user and password" }
    $dbUser = [System.Uri]::UnescapeDataString($userInfo.Substring(0, $separator))
    $dbPassword = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
    $dbHost = $databaseUri.Host
    $dbPort = if ($databaseUri.Port -gt 0) { $databaseUri.Port } else { 5432 }
    $script:secretValues += $dbPassword
    $e2eDatabaseUrl = New-E2eDatabaseUrl -DatabaseUri $databaseUri -DatabaseName $e2eDbName
    $e2eDatabaseUri = [System.Uri]$e2eDatabaseUrl
    $psqlTarget = "{0}:{1}/{2}" -f $dbHost.ToLowerInvariant(), $dbPort, $e2eDbName
    $apiTarget = Get-DatabaseTargetName -Uri $e2eDatabaseUri
    if ($psqlTarget -ne $apiTarget) {
        throw "EZTEST_DATABASE_URL preflight failed: the API target and the psql reset target differ; refusing to reset a database the API might not use"
    }
    Write-Host ("Database target validated: API and psql both use {0}" -f $psqlTarget)

    $gitExe = Resolve-ToolPath -LocalPath "" -CommandName "git"
    if ([string]::IsNullOrWhiteSpace($gitExe)) { throw "git executable not found on PATH" }
    $nodeExe = Resolve-ToolPath -LocalPath "" -CommandName "node"
    if ([string]::IsNullOrWhiteSpace($nodeExe)) { throw "node executable not found on PATH" }

    $reuseApiUrl = [string]$env:EZTEST_ACCEPTANCE_API_URL
    if ([string]::IsNullOrWhiteSpace($reuseApiUrl)) {
        $goExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\go\bin\go.exe") -CommandName "go"
        if ([string]::IsNullOrWhiteSpace($goExe)) { throw "go executable not found (.tools or PATH)" }
        $psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql"
        if ([string]::IsNullOrWhiteSpace($psqlExe)) { throw "psql executable not found (.tools or PATH)" }

        Assert-PortFree -Port $apiPort

        Write-Step "Resetting database $e2eDbName and building the API"
        $env:PGPASSWORD = $dbPassword
        try {
            $exists = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$e2eDbName';"
            if ($exists -ne "0") {
                $terminateSql = "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$e2eDbName' AND pid <> pg_backend_pid();"
                Invoke-PsqlQuiet -Database "postgres" -Sql $terminateSql -Operation "terminating connections to $e2eDbName"
                Invoke-PsqlQuiet -Database "postgres" -Sql "DROP DATABASE $e2eDbName;" -Operation "dropping database $e2eDbName"
            }
            Invoke-PsqlQuiet -Database "postgres" -Sql "CREATE DATABASE $e2eDbName;" -Operation "creating database $e2eDbName"
        } finally {
            Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
        }

        $apiExe = Join-Path $tmpDir "acceptance-codeintel-llm-api.exe"
        $buildLog = Join-Path $tmpDir "acceptance-codeintel-llm-build.log"
        Push-Location $backendDir
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        try {
            & $goExe build -o $apiExe ./cmd/eztest *> $buildLog
            $buildExit = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
            Pop-Location
        }
        if ($buildExit -ne 0) { throw "go build ./cmd/eztest failed (see .tmp/acceptance-codeintel-llm-build.log)" }

        Write-Step "Starting API on :$apiPort with the real LLM configuration from .env"
        $env:EZTEST_ENV = "dev"
        $env:EZTEST_DATABASE_URL = $e2eDatabaseUrl
        $env:EZTEST_HTTP_ADDR = ":$apiPort"
        $env:EZTEST_WEB_ORIGIN = "http://localhost:6868"
        $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
        $env:EZTEST_STORAGE_DIR = (Join-Path $tmpDir "e2e-objects")
        $env:EZTEST_VERSION_ATTACHMENT_DIR = (Join-Path $tmpDir "p4-version-attachments")
        $env:EZTEST_OPENAPI_PATH = (Join-Path $repoRoot "docs\openapi\api.yaml")
        $env:EZTEST_ADMIN_USERNAME = $adminUsername
        $env:EZTEST_ADMIN_PASSWORD = $adminPassword
        foreach ($key in @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "llm_base_url", "llm_key", "llm_model_name")) {
            if ($envValues.ContainsKey($key)) { Set-Item -Path ("Env:" + $key) -Value $envValues[$key] }
        }
        # Honour any P4 tuning the operator put in .env; the API validates the
        # ranges itself and fails fast on an invalid value.
        foreach ($key in @($envValues.Keys)) {
            if ([string]$key -like "EZTEST_CODEINTEL_*" -and -not [string]::IsNullOrWhiteSpace([string]$envValues[$key])) {
                Set-Item -Path ("Env:" + $key) -Value $envValues[$key]
            }
        }
        Remove-Item Env:EZTEST_TEST_DATABASE_URL -ErrorAction SilentlyContinue

        $apiOut = Join-Path $tmpDir "acceptance-codeintel-llm-api.out.log"
        $apiErr = Join-Path $tmpDir "acceptance-codeintel-llm-api.err.log"
        Remove-Item -LiteralPath $apiOut, $apiErr -ErrorAction SilentlyContinue
        $apiProcess = Start-Process -FilePath $apiExe `
            -WorkingDirectory $backendDir `
            -RedirectStandardOutput $apiOut `
            -RedirectStandardError $apiErr `
            -WindowStyle Hidden `
            -PassThru
        try {
            Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 90 -Process $apiProcess -Label "API"
        } catch {
            Stop-ProcessTree -Process $apiProcess
            $apiProcess = $null
            throw
        }
    } else {
        $apiBaseUrl = $reuseApiUrl.TrimEnd("/")
        Write-Step "Reusing the API at $apiBaseUrl (EZTEST_ACCEPTANCE_API_URL)"
        Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 30 -Process $null -Label "API"
    }

    Write-Step "Checking health and LLM configuration"
    $health = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/health" -TimeoutSec 20
    if ($null -eq $health.Raw -or [string]$health.Raw.data.status -ne "ok") {
        throw "API health is not ok"
    }
    $llmConfigured = [bool]$health.Raw.data.llm_configured
    $script:resultData.llmConfigured = $llmConfigured
    if (-not $llmConfigured) {
        # R43 fail-closed: the gate is open, so a run without a usable LLM
        # configuration cannot be real-LLM acceptance. Only an explicit
        # -AllowDegraded opt-in may record the expected done+degraded result.
        if (-not $AllowDegraded) {
            throw "health reports llm_configured=false; refusing to claim real-LLM acceptance by default (pass -AllowDegraded to record an explicitly degraded run)"
        }
        Write-Host "WARNING: health reports llm_configured=false (allowed by -AllowDegraded); the analysis is expected to degrade." -ForegroundColor Yellow
        [void]$script:warnings.Add("health reports llm_configured=false; degraded mode explicitly allowed by -AllowDegraded")
    }

    Write-Step "Logging in as admin"
    $login = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/auth/login" -TimeoutSec 20 -Body @{
        username = $adminUsername
        password = $adminPassword
    }
    $token = [string]$login.Data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "admin login did not return a token" }
    $headers = @{ Authorization = "Bearer $token" }

    Write-Step "Preparing the real repository clone and selecting a commit pair"
    $repoPath = Initialize-RepoWorkspace
    Write-Host "Repository checkout prepared: $repoPath"

    $pair = Select-CommitPair -RepositoryPath $repoPath
    $baseCommit = [string]$pair.Base
    $headCommit = [string]$pair.Head
    $gitEntries = @($pair.Entries)
    $script:resultData.baseCommit = $baseCommit
    $script:resultData.headCommit = $headCommit
    $script:resultData.changedFiles = @($gitEntries).Count
    Write-Host ("Selected real commit pair: base={0} head={1} changedFiles={2}" -f $baseCommit.Substring(0, 12), $headCommit.Substring(0, 12), @($gitEntries).Count)
    Add-CheckResult -Name "real commit pair" -Status "passed" -Detail ("base..head with {0} changed files" -f @($gitEntries).Count)

    # Create a uniquely named branch at the base commit inside the disposable
    # clone. The analysis explicitly asks for the later head commit, exactly as
    # the §10.7 manual path does. The clone is ours, so no fallback to an
    # existing branch and no ref writes in the caller's repository are needed.
    $acceptanceBranch = "{0}-{1}-{2}" -f $acceptanceBranchPrefix, $baseCommit.Substring(0, 12), ([guid]::NewGuid().ToString("N").Substring(0, 8))
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $gitExe -C $repoPath update-ref "refs/heads/$acceptanceBranch" $baseCommit 2>$null
        $branchExit = $LASTEXITCODE
        if ($branchExit -eq 0) {
            $branchTip = ((& $gitExe -C $repoPath rev-parse --verify "refs/heads/$acceptanceBranch^{commit}" 2>$null | Out-String).Trim())
        } else {
            $branchTip = ""
        }
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($branchExit -ne 0 -or $branchTip -ne $baseCommit) {
        throw "could not create the unique acceptance branch '$acceptanceBranch' at the selected base commit in .tmp/p4-real-repo"
    }
    $script:resultData.branch = $acceptanceBranch
    Write-Host ("Acceptance branch created in the disposable clone: {0}" -f $acceptanceBranch)

    Write-Step "Registering the code repository and creating a bound version"
    $repo = Ensure-CodeRepo -RepositoryPath $repoPath -Branch $acceptanceBranch -Headers $headers
    $repoId = [int64]$repo.id
    if ($repoId -le 0) { throw "code repository registration did not return a numeric id" }
    Write-Host ("Code repository ensured (repoId={0}, fixedBranch={1})" -f $repoId, $acceptanceBranch)

    $version = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions" -Headers $headers -TimeoutSec 30 -Body @{
        name               = "P4 real LLM acceptance"
        description        = "Automated P4 code-intelligence acceptance run."
        codeRepositoryId   = $repoId
        codeBranch         = $acceptanceBranch
        codeBaselineCommit = $baseCommit
    }
    $versionId = [string]$version.Data.id
    if ([string]::IsNullOrWhiteSpace($versionId)) { throw "version creation did not return an id" }
    $script:resultData.versionId = $versionId
    Write-Host ("Version created (versionId={0})" -f $versionId)

    Write-Step "Adding two requirements (one carries the P4-AI-IMPACT marker)"
    $req1 = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$versionId/requirements" -Headers $headers -TimeoutSec 30 -Body @{
        title              = "${requirementMarker}: refund reason parameter"
        description        = "Acceptance requirement: the refund API and its call site carry a reason parameter."
        acceptanceCriteria = "The changed files expose the reason parameter end to end."
        priority           = "must"
    }
    $req2 = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$versionId/requirements" -Headers $headers -TimeoutSec 30 -Body @{
        title              = "Order refund status display"
        description        = "Acceptance requirement: the order page displays the refund status."
        acceptanceCriteria = "The order page reflects the refund state."
        priority           = "should"
    }
    $reqIds = @([string]$req1.Data.id, [string]$req2.Data.id)
    foreach ($reqId in $reqIds) {
        if ([string]::IsNullOrWhiteSpace($reqId)) { throw "requirement creation did not return an id" }
    }
    Write-Host ("Requirements created: {0}" -f ($reqIds -join ", "))

    Write-Step "Creating one requirement anchor on a changed file (direct-candidate source)"
    $anchorCreated = $false
    $anchorFailures = @()
    $anchorCandidates = @($gitEntries | Where-Object { $_.Status -match '^[MTDRC]' })
    foreach ($entry in $anchorCandidates) {
        if ($anchorCreated) { break }
        try {
            $anchor = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$versionId/code/anchors" -Headers $headers -TimeoutSec 30 -Body @{
                subjectKind = "requirement"
                subjectId   = $reqIds[0]
                filePath    = [string]$entry.Path
            }
            if ([string]::IsNullOrWhiteSpace([string]$anchor.Data.id)) { throw "anchor creation did not return an id" }
            $anchorCreated = $true
            Add-CheckResult -Name "requirement anchor" -Status "passed" -Detail ("anchor created for requirement {0}" -f $reqIds[0])
        } catch {
            # An added/renamed path may not exist at the branch tip used for
            # anchor validation; try the next base-side changed file.
            $anchorFailures += (Get-ApiErrorSummary -ErrorRecord $_)
        }
    }
    if (-not $anchorCreated) {
        Add-CheckResult -Name "requirement anchor" -Status "skipped" -Detail "no changed file could be anchored at the base tip; direct-anchor candidate not seeded"
        if ($anchorFailures.Count -gt 0) {
            [void]$script:warnings.Add("anchor creation skipped (" + (($anchorFailures | Select-Object -Unique) -join "; ") + ")")
        } else {
            [void]$script:warnings.Add("no anchor-eligible changed file; direct requirement candidate is not guaranteed")
        }
    }

    Write-Step "Verifying the version change preview against git diff --name-status"
    $changes = Get-VersionChangeList -VersionId $versionId -Head $headCommit -Headers $headers
    Assert-ChangeListMatchesGit -GitEntries $gitEntries -ApiFiles $changes.Files
    Add-CheckResult -Name "change list vs git diff" -Status "passed" -Detail ("{0} files match" -f @($changes.Files).Count)

    Write-Step "Triggering the real-LLM impact analysis (explicit headCommit)"
    $start = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$versionId/code/analyses" -Headers $headers -TimeoutSec 60 -Body @{
        headCommit = $headCommit
        force      = $false
    }
    $analysisId = [string]$start.Data.id
    if ([string]::IsNullOrWhiteSpace($analysisId)) { throw "analysis trigger did not return an id" }
    $initialVersion = [int]$start.Data.analysisVersion
    $initialReused = [bool]$start.Data.reused
    $script:resultData.analysisId = $analysisId
    $script:resultData.analysisVersion = $initialVersion
    $script:resultData.reused = $initialReused
    Write-Host ("Analysis queued (analysisId={0}, analysisVersion={1}, HTTP {2})" -f $analysisId, $initialVersion, $start.Status)
    if ($start.Status -ne 202) {
        [void]$script:warnings.Add("first analysis trigger returned HTTP $($start.Status) instead of 202")
    }

    $analysisResponse = Wait-Analysis -VersionId $versionId -AnalysisId $analysisId -Headers $headers -TimeoutSeconds $pollTimeoutSeconds
    $analysis = $analysisResponse.Data
    $analysisStatus = [string]$analysis.status
    if ($analysisStatus -eq "failed") {
        $codes = @()
        if ($null -ne $analysis.errors) {
            foreach ($analysisError in @($analysis.errors)) {
                if ($null -ne $analysisError) { $codes += [string]$analysisError.code }
            }
        }
        throw ("analysis failed (error_codes=" + (($codes | Select-Object -Unique) -join ",") + ")")
    }
    if ($analysisStatus -ne "done") { throw "analysis reached unexpected terminal status '$analysisStatus'" }
    if ([string]$analysis.versionId -ne $versionId) { throw "analysis versionId does not match the bound version" }
    if ([int64]$analysis.repoId -ne $repoId) { throw "analysis repoId does not match the code repository" }
    if ([string]$analysis.baseCommit -ne $baseCommit -or [string]$analysis.headCommit -ne $headCommit) {
        throw "analysis base/head commits do not match the selected real commit pair"
    }

    $degraded = [bool]$analysis.degraded
    $script:resultData.degraded = $degraded
    Add-CheckResult -Name "analysis completed" -Status "passed" -Detail ("status=done degraded={0}" -f $degraded)

    # Provenance: record exactly which model/prompt produced the analysis. The
    # prompt version is always set (config default); a non-degraded real-LLM run
    # must also record the resolved model.
    $analysisModel = [string]$analysis.model
    $analysisPromptVersion = [string]$analysis.promptVersion
    $script:resultData.model = $analysisModel
    $script:resultData.promptVersion = $analysisPromptVersion
    if ([string]::IsNullOrWhiteSpace($analysisPromptVersion)) {
        throw "analysis did not record promptVersion provenance"
    }
    if (-not $degraded -and [string]::IsNullOrWhiteSpace($analysisModel)) {
        throw "analysis did not record the model used by a non-degraded run"
    }
    Add-CheckResult -Name "analysis provenance" -Status "passed" -Detail ("model={0} promptVersion={1}" -f $analysisModel, $analysisPromptVersion)

    # R43 fail-closed: a degraded analysis must never be reported as real-LLM
    # acceptance unless the caller explicitly opted in with -AllowDegraded (the
    # old skip-AI-checks behavior).
    if ($degraded -and -not $AllowDegraded) {
        throw "analysis degraded=true; refusing to claim real-LLM acceptance by default (pass -AllowDegraded to record an explicitly degraded run)"
    }

    $stats = $analysis.stats
    if ($null -eq $stats) { throw "completed analysis has no stats" }
    if ([int]$stats.changedFiles -ne @($gitEntries).Count) {
        throw "analysis stats.changedFiles=$($stats.changedFiles) but git diff reports $(@($gitEntries).Count)"
    }
    Add-CheckResult -Name "stats.changedFiles" -Status "passed" -Detail ([string]$stats.changedFiles)

    # Re-read the preview after the analysis; when a persisted change set exists,
    # changeSetId is expected to be non-null (a null is reported, not failed,
    # because the preview contract explicitly allows it).
    $changesAfter = Get-VersionChangeList -VersionId $versionId -Head $headCommit -Headers $headers
    Assert-ChangeListMatchesGit -GitEntries $gitEntries -ApiFiles $changesAfter.Files
    if ($null -ne $changesAfter.First -and $null -eq $changesAfter.First.changeSetId) {
        [void]$script:warnings.Add("change preview still reports changeSetId=null after the analysis")
    }

    Write-Step "Verifying AI facts and evidence hashes against commit content"
    $factsResult = Get-CodeFacts -RepoId $repoId -Commit $headCommit -Headers $headers
    $facts = @($factsResult.Facts)
    $script:resultData.factCount = $facts.Count
    Write-Host ("Facts read: {0} entries across {1} page(s)" -f $facts.Count, $factsResult.Pages)

    # Version anchors also cover changed paths for the unmapped-completeness
    # assertion (spec §6.6 step 7 / INV-8). The endpoint returns one plain array.
    $anchors = @(Get-VersionAnchors -VersionId $versionId -Headers $headers)
    $script:resultData.anchorCount = $anchors.Count

    $evidenceTotal = 0
    $evidenceVerified = 0
    $aiFactsWithEvidence = 0
    $hashHelper = Write-EvidenceHashHelper
    foreach ($fact in @($facts)) {
        if ($null -eq $fact) { continue }
        $evidenceItems = @($fact.evidence)
        if ([string]$fact.source -eq "ai" -and $evidenceItems.Count -gt 0) { $aiFactsWithEvidence++ }
        foreach ($evidence in $evidenceItems) {
            if ($null -eq $evidence) { continue }
            if ([string]::IsNullOrWhiteSpace([string]$evidence.file)) {
                throw "a stored fact has evidence without a file"
            }
            $evidenceTotal++
            if ($null -eq $evidence.excerptHash -or [string]::IsNullOrWhiteSpace([string]$evidence.excerptHash)) {
                # Anchor-sourced evidence intentionally carries only the file.
                continue
            }
            if ([string]$evidence.excerptHash -notmatch '^sha256:[a-f0-9]{64}$') {
                throw "a stored fact has a malformed evidence excerpt hash"
            }
            if ($evidenceVerified -lt $evidenceVerifyLimit) {
                $startLine = [int]$evidence.startLine
                $endLine = [int]$evidence.endLine
                if ($startLine -lt 1 -or $endLine -lt $startLine) {
                    throw "a stored fact has an invalid evidence line range"
                }
                $computed = & $nodeExe $hashHelper $repoPath $headCommit ([string]$evidence.file) $startLine $endLine 2>$null
                if ($LASTEXITCODE -ne 0) {
                    throw "a stored evidence range could not be verified against the commit (missing file or out-of-range lines)"
                }
                $computedText = (($computed | Out-String).Trim())
                if ($computedText -ne [string]$evidence.excerptHash) {
                    throw "a stored evidence excerpt hash does not match the commit content"
                }
                $evidenceVerified++
            }
        }
    }
    $script:resultData.evidenceCount = $evidenceTotal
    $script:resultData.verifiedEvidence = $evidenceVerified
    if ($degraded) {
        Add-CheckResult -Name "AI evidence" -Status "skipped" -Detail "analysis degraded=true; AI evidence assertions are skipped"
    } elseif ($aiFactsWithEvidence -eq 0) {
        throw "analysis is not degraded but no AI fact with evidence was materialized"
    } elseif ($evidenceVerified -eq 0) {
        throw "stored AI evidence could not be verified against the commit content"
    } else {
        Add-CheckResult -Name "AI evidence" -Status "passed" -Detail ("{0} evidence hashes recomputed from the commit" -f $evidenceVerified)
    }

    Write-Step "Verifying impact candidates and unmapped changes"
    $candidates = @($analysis.candidates)
    $script:resultData.candidateCount = $candidates.Count
    $requirementCandidates = @($candidates | Where-Object { $null -ne $_ -and [string]$_.subjectKind -eq "requirement" })
    $script:resultData.requirementCandidateCount = $requirementCandidates.Count
    $matchedCreatedRequirement = @($requirementCandidates | Where-Object { $reqIds -contains [string]$_.subjectId })
    if ($matchedCreatedRequirement.Count -eq 0) {
        if ($degraded -and -not $anchorCreated) {
            Add-CheckResult -Name "impact candidates" -Status "skipped" -Detail "degraded run without a seeded anchor cannot produce a deterministic requirement candidate"
            [void]$script:warnings.Add("candidate assertion skipped: degraded run and no requirement anchor")
        } else {
            throw "analysis produced no candidate for either created requirement"
        }
    } else {
        Add-CheckResult -Name "impact candidates" -Status "passed" -Detail ("{0} candidates, {1} for the created requirements" -f $candidates.Count, $matchedCreatedRequirement.Count)
    }
    $selectedLow = @($candidates | Where-Object { $null -ne $_ -and [string]$_.confidence -eq "low" -and [bool]$_.selected })
    if ($selectedLow.Count -gt 0) {
        throw "a low-confidence candidate is selected by default"
    }

    # Spec §6.6/INV-2: every candidate carries a reason, a valid confidence and
    # traceable path evidence; high/medium candidates must never be evidence-free.
    if ($candidates.Count -eq 0) {
        Add-CheckResult -Name "candidate reason/confidence/evidence" -Status "skipped" -Detail "analysis produced no candidates"
    } else {
        $validConfidences = @("high", "medium", "low")
        foreach ($candidate in $candidates) {
            if ($null -eq $candidate) { throw "analysis candidates contain a null entry" }
            $candidateId = [string]$candidate.id
            if ([string]::IsNullOrWhiteSpace([string]$candidate.reason)) {
                throw "candidate $candidateId has an empty reason"
            }
            $candidateConfidence = [string]$candidate.confidence
            if ($validConfidences -notcontains $candidateConfidence) {
                throw "candidate $candidateId has invalid confidence '$candidateConfidence'"
            }
            if ($candidateConfidence -eq "high" -or $candidateConfidence -eq "medium") {
                $pathEvidence = @($candidate.evidence | Where-Object {
                        $null -ne $_ -and -not [string]::IsNullOrWhiteSpace([string]$_.file)
                    })
                if ($pathEvidence.Count -eq 0) {
                    throw "candidate $candidateId has confidence '$candidateConfidence' without path evidence"
                }
            }
        }
        Add-CheckResult -Name "candidate reason/confidence/evidence" -Status "passed" -Detail ("{0} candidates validated" -f $candidates.Count)
    }

    if ($null -eq $analysis.PSObject.Properties["unmapped"]) {
        throw "analysis does not expose an unmapped array"
    }
    $unmapped = @($analysis.unmapped)
    $script:resultData.unmappedCount = $unmapped.Count
    $changedPaths = @{}
    foreach ($gitEntry in @($gitEntries)) { $changedPaths[[string]$gitEntry.Path] = $true }
    $validUnmappedReasons = @("deleted_file", "unsupported_content", "no_fact_or_anchor", "ai_unavailable", "unmatched_contract")
    foreach ($unmappedEntry in $unmapped) {
        if ($null -eq $unmappedEntry) { continue }
        if ([string]::IsNullOrWhiteSpace([string]$unmappedEntry.path) -or
            [string]::IsNullOrWhiteSpace([string]$unmappedEntry.reason) -or
            [string]::IsNullOrWhiteSpace([string]$unmappedEntry.suggestion)) {
            throw "an unmapped change is missing path/reason/suggestion"
        }
        if (-not $changedPaths.ContainsKey([string]$unmappedEntry.path)) {
            throw "unmapped change path '$([string]$unmappedEntry.path)' is not part of the analyzed change set"
        }
        if ($validUnmappedReasons -notcontains [string]$unmappedEntry.reason) {
            throw "unmapped change has invalid reason '$([string]$unmappedEntry.reason)'"
        }
    }
    if ($unmapped.Count -eq 0) {
        if ($AllowNoUnmapped) {
            Add-CheckResult -Name "unmapped changes" -Status "skipped" -Detail "unmapped=0 explicitly allowed by -AllowNoUnmapped"
            [void]$script:warnings.Add("unmapped=0 explicitly allowed by -AllowNoUnmapped for the selected commit pair")
            Write-Host "unmapped policy: -AllowNoUnmapped is set; unmapped=0 is recorded as skipped." -ForegroundColor Yellow
        } else {
            throw "unmapped=0 for the selected commit pair; spec §10.7 requires >=1 real unmapped change (pass -AllowNoUnmapped to relax explicitly)"
        }
    } else {
        Add-CheckResult -Name "unmapped changes" -Status "passed" -Detail ([string]$unmapped.Count)
    }

    # INV-8 / spec §10.7 "no silent omission": every changed path must either be
    # covered by a persisted AI fact (fact path or verified evidence file), a
    # version anchor, or be listed explicitly in unmapped. This mirrors the
    # backend's impactCoverageFacts: AI fact kinds with at least one verified
    # evidence location, regardless of confidence, and never the T0 file-entity
    # rows (those would cover every indexed path and hide a real omission).
    $coverageFactKinds = @("page", "route", "api", "client_call", "component", "module", "symbol")
    $coverage = @{}
    foreach ($fact in $facts) {
        if ($null -eq $fact) { continue }
        if ($coverageFactKinds -notcontains [string]$fact.kind) { continue }
        $verifiedEvidence = @($fact.evidence | Where-Object {
                $null -ne $_ -and -not [string]::IsNullOrWhiteSpace([string]$_.file)
            })
        if ($verifiedEvidence.Count -eq 0) { continue }
        $factPath = [string]$fact.path
        if (-not [string]::IsNullOrWhiteSpace($factPath)) { $coverage[$factPath] = $true }
        foreach ($evidence in $verifiedEvidence) {
            $coverage[[string]$evidence.file] = $true
        }
    }
    foreach ($anchor in $anchors) {
        if ($null -eq $anchor) { continue }
        $anchorPath = [string]$anchor.filePath
        if (-not [string]::IsNullOrWhiteSpace($anchorPath)) { $coverage[$anchorPath] = $true }
    }
    $unmappedPaths = @{}
    foreach ($unmappedEntry in $unmapped) {
        if ($null -eq $unmappedEntry) { continue }
        $unmappedPaths[[string]$unmappedEntry.path] = $true
    }
    $uncovered = @($changedPaths.Keys | Where-Object {
            -not $coverage.ContainsKey([string]$_) -and -not $unmappedPaths.ContainsKey([string]$_)
        } | Sort-Object)
    if ($uncovered.Count -gt 0) {
        throw ("unmapped completeness failed: {0} changed path(s) are neither covered by a fact/anchor nor listed in unmapped: {1}" -f $uncovered.Count, ($uncovered -join ", "))
    }
    Add-CheckResult -Name "unmapped completeness" -Status "passed" -Detail ("{0} changed paths covered by facts/anchors or unmapped" -f $changedPaths.Count)

    # stats.unmapped must agree with the returned list. Truncation is the only
    # allowed difference; the counter is compared to the list unless the backend
    # reports stats.unmappedTruncated=true (older DTO projections omit it).
    if ($null -eq $stats.PSObject.Properties["unmapped"]) {
        throw "analysis stats do not expose the unmapped counter"
    }
    $statsUnmapped = [int]$stats.unmapped
    $script:resultData.statsUnmapped = $statsUnmapped
    $unmappedTruncated = $false
    if ($null -ne $stats.PSObject.Properties["unmappedTruncated"]) {
        $unmappedTruncated = [bool]$stats.unmappedTruncated
    }
    $script:resultData.unmappedTruncated = $unmappedTruncated
    if ($unmappedTruncated) {
        if ($statsUnmapped -lt $unmapped.Count) {
            throw "stats.unmapped=$statsUnmapped is smaller than the returned unmapped list ($($unmapped.Count) entries)"
        }
        Add-CheckResult -Name "stats.unmapped" -Status "passed" -Detail ("{0} total, {1} returned after truncation" -f $statsUnmapped, $unmapped.Count)
    } elseif ($statsUnmapped -ne $unmapped.Count) {
        throw "analysis stats.unmapped=$statsUnmapped but the returned unmapped list has $($unmapped.Count) entries"
    } else {
        Add-CheckResult -Name "stats.unmapped" -Status "passed" -Detail ([string]$statsUnmapped)
    }

    $initialFingerprint = Get-CandidateFingerprint -Candidates $analysis.candidates

    Write-Step "Verifying cache reuse (second POST must return reused=true)"
    $second = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$versionId/code/analyses" -Headers $headers -TimeoutSec 60 -Body @{
        headCommit = $headCommit
        force      = $false
    }
    if ($second.Status -ne 200) {
        throw "second analysis POST returned HTTP $($second.Status), expected 200 for a reused analysis"
    }
    if (-not [bool]$second.Data.reused) { throw "second analysis POST did not report reused=true" }
    if ([string]$second.Data.id -ne $analysisId) { throw "second analysis POST returned a different analysis id" }
    $reusedAnalysis = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$versionId/code/analyses/$analysisId" -Headers $headers -TimeoutSec 30
    $reusedFingerprint = Get-CandidateFingerprint -Candidates $reusedAnalysis.Data.candidates
    if ($initialFingerprint -ne $reusedFingerprint) {
        throw "the reused analysis returned different normalized candidates"
    }
    $script:resultData.reused = $true
    Add-CheckResult -Name "cache reuse" -Status "passed" -Detail "reused=true, same analysis id, normalized candidates identical"

    Write-Step "Verifying file-level cache hits in a second version bound to the same repo/base"
    $secondVersion = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions" -Headers $headers -TimeoutSec 30 -Body @{
        name               = "P4 real LLM acceptance (cache check)"
        description        = "Second acceptance version bound to the same repository and baseline."
        codeRepositoryId   = $repoId
        codeBranch         = $acceptanceBranch
        codeBaselineCommit = $baseCommit
    }
    $secondVersionId = [string]$secondVersion.Data.id
    if ([string]::IsNullOrWhiteSpace($secondVersionId)) { throw "second version creation did not return an id" }
    $script:resultData.secondVersionId = $secondVersionId
    $secondStart = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$secondVersionId/code/analyses" -Headers $headers -TimeoutSec 60 -Body @{
        headCommit = $headCommit
        force      = $false
    }
    if ($secondStart.Status -ne 202) {
        throw "second version analysis POST returned HTTP $($secondStart.Status), expected 202 for a new analysis"
    }
    $secondAnalysisId = [string]$secondStart.Data.id
    if ([string]::IsNullOrWhiteSpace($secondAnalysisId)) { throw "second version analysis trigger did not return an id" }
    $script:resultData.secondAnalysisId = $secondAnalysisId
    $secondAnalysisResponse = Wait-Analysis -VersionId $secondVersionId -AnalysisId $secondAnalysisId -Headers $headers -TimeoutSeconds $pollTimeoutSeconds
    $secondAnalysis = $secondAnalysisResponse.Data
    if ([string]$secondAnalysis.status -eq "failed") { throw "second version analysis failed" }
    if ([string]$secondAnalysis.status -ne "done") {
        throw "second version analysis reached unexpected terminal status '$([string]$secondAnalysis.status)'"
    }
    if ([string]$secondAnalysis.baseCommit -ne $baseCommit -or [string]$secondAnalysis.headCommit -ne $headCommit) {
        throw "second version analysis base/head commits do not match the selected real commit pair"
    }
    $secondStats = $secondAnalysis.stats
    if ($null -eq $secondStats) { throw "second version analysis has no stats" }
    $cacheHits = [int]$secondStats.cacheHits
    $script:resultData.cacheHits = $cacheHits
    $secondDegraded = [bool]$secondAnalysis.degraded
    if ($secondDegraded -and -not $AllowDegraded) {
        throw "second version analysis degraded=true; refusing to claim real-LLM acceptance by default (pass -AllowDegraded to record an explicitly degraded run)"
    }
    if ($degraded -or $secondDegraded) {
        Add-CheckResult -Name "second version cache hits" -Status "skipped" -Detail ("degraded=true; stats.cacheHits={0} is not enforced" -f $cacheHits)
        [void]$script:warnings.Add("second version cacheHits assertion skipped because the analysis degraded")
    } elseif ($cacheHits -le 0) {
        throw "second version analysis stats.cacheHits=$cacheHits, expected >0 for the same repo/base/head"
    } else {
        Add-CheckResult -Name "second version cache hits" -Status "passed" -Detail ("stats.cacheHits={0}" -f $cacheHits)
    }

    Write-Step "Verifying force creates analysisVersion+1 and preserves the old analysis"
    $forced = Invoke-Api -Method Post -Uri "$apiBaseUrl/api/versions/$versionId/code/analyses" -Headers $headers -TimeoutSec 60 -Body @{
        headCommit = $headCommit
        force      = $true
    }
    if ($forced.Status -ne 202) {
        throw "force analysis POST returned HTTP $($forced.Status), expected 202 for a new analysis"
    }
    if ([bool]$forced.Data.reused) { throw "force analysis POST reported reused=true" }
    $forceAnalysisId = [string]$forced.Data.id
    $forceVersion = [int]$forced.Data.analysisVersion
    if ($forceVersion -ne ($initialVersion + 1)) {
        throw "force analysisVersion=$forceVersion, expected $($initialVersion + 1)"
    }
    $script:resultData.forceAnalysisId = $forceAnalysisId
    $script:resultData.forceAnalysisVersion = $forceVersion
    $forceAnalysisResponse = Wait-Analysis -VersionId $versionId -AnalysisId $forceAnalysisId -Headers $headers -TimeoutSeconds $pollTimeoutSeconds
    $forceAnalysis = $forceAnalysisResponse.Data
    if ([string]$forceAnalysis.status -eq "failed") {
        throw "force analysis failed"
    }
    if ([string]$forceAnalysis.status -ne "done") {
        throw "force analysis reached unexpected terminal status '$([string]$forceAnalysis.status)'"
    }
    $forceDegraded = [bool]$forceAnalysis.degraded
    if ($forceDegraded -and -not $AllowDegraded) {
        throw "force analysis degraded=true; refusing to claim real-LLM acceptance by default (pass -AllowDegraded to record an explicitly degraded run)"
    }
    $oldAgain = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$versionId/code/analyses/$analysisId" -Headers $headers -TimeoutSec 30
    $oldAgainFingerprint = Get-CandidateFingerprint -Candidates $oldAgain.Data.candidates
    if ($initialFingerprint -ne $oldAgainFingerprint) {
        throw "the old analysis candidates changed after the force run"
    }
    Add-CheckResult -Name "force versioning" -Status "passed" -Detail ("analysisVersion {0} -> {1}; old analysis unchanged" -f $initialVersion, $forceVersion)

    $anyDegraded = $degraded -or $secondDegraded -or $forceDegraded
    $script:resultData.degraded = $anyDegraded
    if ($anyDegraded) {
        $script:resultData.status = "passed_degraded"
    } else {
        $script:resultData.status = "passed"
    }
} catch {
    $summary = Get-ApiErrorSummary -ErrorRecord $_
    Write-Host ""
    Write-Host "REAL CODEINTEL LLM ACCEPTANCE FAILED: $summary" -ForegroundColor Red
    $script:resultData.status = "failed"
    $script:resultData.error = $summary
    $exitCode = 1
} finally {
    Stop-ProcessTree -Process $apiProcess
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    Write-AcceptanceResult
    Write-Host ("Result written to .tmp/p4-real-acceptance.json (status={0})" -f $script:resultData.status)
}

if ($exitCode -eq 0) {
    if ($script:resultData.status -eq "passed_degraded") {
        Write-Host "PASS (degraded, explicitly allowed by -AllowDegraded): AI-only assertions were skipped; T0/caching/versioning checks passed." -ForegroundColor Yellow
    } else {
        Write-Host "PASS: real codeintel LLM acceptance completed." -ForegroundColor Green
    }
} else {
    Write-Host "FAILED: real codeintel LLM acceptance did not pass." -ForegroundColor Red
}

exit $exitCode
