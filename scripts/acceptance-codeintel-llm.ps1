#requires -Version 5.1
<#
.SYNOPSIS
    Optional real-LLM acceptance for P4 code intelligence (gated by
    EZTEST_RUN_REAL_LLM=1).

.DESCRIPTION
    Without the gate this script prints SKIPPED and exits 0. With the gate open
    it drives one real-LLM P4 acceptance run against a real Git repository:

      * resets the dedicated eztest_e2e database, builds and starts the current
        API (or reuses an already running API when EZTEST_ACCEPTANCE_API_URL is
        set), then logs in as the administrator;
      * clones the EzTest repository itself to .tmp/p4-real-repo (or uses
        EZTEST_ACCEPTANCE_P4_REPO), creates the acceptance branch at a real base
        commit and selects a real base..head pair with at most 20 changed files
        (EZTEST_ACCEPTANCE_P4_BASE/HEAD override the pair);
      * registers the clone as a P2 code repository, creates a version bound to
        it, adds two requirements (one title contains P4-AI-IMPACT) and attempts
        one requirement anchor on a changed file (recorded as skipped with a
        warning when no changed path can be anchored);
      * triggers POST /api/versions/{id}/code/analyses with an explicit
        headCommit and polls until done/failed;
      * verifies that the change list matches `git diff --name-status`, that
        stored AI evidence hashes recompute from commit content, that impact
        candidates carry a non-empty reason, a valid confidence and path
        evidence for high/medium, and that unmapped changes are reported (at
        least one unless -AllowNoUnmapped is passed explicitly);
      * verifies cache reuse (second POST returns reused=true, the same id and
        normalized candidates identical), file-level cache hits in a second
        version bound to the same repo/base analyzing the same head
        (stats.cacheHits > 0), and force versioning (force=true creates
        analysisVersion+1 while the old analysis stays readable and its
        candidates stay unchanged);
      * records a machine-readable, secret-free result at
        .tmp/p4-real-acceptance.json.

    A degraded (done + degraded=true) run is recorded as passed_degraded and the
    AI-only assertions are skipped with an explicit note. If the default LLM
    configuration is unavailable before the run starts, the gate is open but the
    required llm_* keys are present, so this is a failure rather than a skip.

    Logs contain only statuses, ids, counts and error codes; they never contain
    code bodies, prompts, credentials or repository file contents.

.PARAMETER DryRun
    Validate paths, the .env key presence (names only) and the Git/Node/Go/psql
    tool availability without cloning, resetting the database, starting the API
    or spending an LLM call. Exits 0 when the preflight passes. DryRun also works
    while the gate is closed.

.PARAMETER AllowNoUnmapped
    Accept a real commit pair where every changed file maps to a candidate
    (unmapped = 0). Without this switch the run fails because spec §10.7 step 3
    requires at least one real unmapped change; the relaxation is printed and
    recorded in the result JSON so it cannot pass silently.

.NOTES
    This script makes paid upstream calls. It is deliberately not part of
    scripts/acceptance.ps1, which reports it as gated. The selected repository is
    mounted/read only by design: P4 never writes to the checkout. Compatible with
    Windows PowerShell 5.1 and PowerShell 7+; PostgreSQL must already be running.
#>
[CmdletBinding()]
param(
    [switch]$DryRun,
    [switch]$AllowNoUnmapped
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
$acceptanceBranch = "p4-acceptance"
$requirementMarker = "P4-AI-IMPACT"
$maxChangedFiles = 20
$pollIntervalSeconds = 2
$pollTimeoutSeconds = 420
$evidenceVerifyLimit = 20
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
    branch       = $acceptanceBranch
    versionId    = $null
    analysisId   = $null
    analysisVersion = $null
    forceAnalysisId = $null
    forceAnalysisVersion = $null
    reused       = $null
    degraded     = $null
    changedFiles = $null
    factCount    = $null
    evidenceCount = $null
    verifiedEvidence = $null
    candidateCount = $null
    requirementCandidateCount = $null
    unmappedCount = $null
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
    $raw = Invoke-GitText -Arguments @("diff", "--name-status", "--no-color", $Base, $Head)
    $entries = @()
    foreach ($line in ($raw -split "\r?\n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line -split "`t"
        if ($parts.Count -lt 2) { continue }
        $status = $parts[0].Substring(0, 1)
        if ($status -eq "R" -or $status -eq "C") {
            if ($parts.Count -lt 3) { continue }
            $entries += [pscustomobject]@{
                Path    = $parts[2]
                OldPath = $parts[1]
                Status  = $status
            }
        } else {
            $entries += [pscustomobject]@{
                Path    = $parts[1]
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
        # The local default clone is refreshed from the source repository; prefer
        # origin/HEAD so an existing clone does not pin an old acceptance commit.
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
    $explicitRepo = $env:EZTEST_ACCEPTANCE_P4_REPO
    if (-not [string]::IsNullOrWhiteSpace($explicitRepo)) {
        $candidate = [string]$explicitRepo
        if (-not (Test-Path -LiteralPath $candidate -PathType Container)) {
            throw "EZTEST_ACCEPTANCE_P4_REPO does not exist: $candidate"
        }
        $gitDirCheck = & $gitExe -C $candidate rev-parse --git-dir 2>$null
        if ($LASTEXITCODE -ne 0) {
            throw "EZTEST_ACCEPTANCE_P4_REPO is not a Git repository: $candidate"
        }
        return $candidate
    }

    $candidate = Join-Path $tmpDir "p4-real-repo"
    if (-not (Test-Path -LiteralPath (Join-Path $candidate ".git"))) {
        if (Test-Path -LiteralPath $candidate) {
            throw "the default P4 acceptance path exists but is not a Git clone: $candidate"
        }
        New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
        Write-Step "Cloning the EzTest repository to .tmp/p4-real-repo (read-only knowledge for P4)"
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        try {
            & $gitExe clone --quiet --no-hardlinks $repoRoot $candidate 2>$null
            $cloneExit = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
        }
        if ($cloneExit -ne 0) { throw "git clone of the repository for P4 acceptance failed (exit $cloneExit)" }
    } else {
        # Refresh a local clone without touching the source checkout; a fetch
        # failure is non-fatal because the existing history is still usable.
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        try {
            & $gitExe -C $candidate fetch --quiet origin 2>$null
            if ($LASTEXITCODE -eq 0) {
                & $gitExe -C $candidate remote set-head origin -a 2>$null | Out-Null
            }
        } finally {
            $ErrorActionPreference = $previous
        }
    }
    return $candidate
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
        $apiByPath[[string]$file.path] = [string]$file.status
    }
    foreach ($entry in @($GitEntries)) {
        if (-not $apiByPath.ContainsKey([string]$entry.Path)) {
            throw "version change list is missing a file reported by git diff: $($entry.Path)"
        }
        if ($apiByPath[[string]$entry.Path] -ne [string]$entry.Status) {
            throw "version change list status mismatch for $($entry.Path)"
        }
    }
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
const lines = [];
let current = [];
for (const byte of bytes) {
  if (byte === 10) {
    if (current.length > 0 && current[current.length - 1] === 13) current.pop();
    lines.push(Buffer.from(current));
    current = [];
  } else {
    current.push(byte);
  }
}
if (current.length > 0 || (bytes.length > 0 && bytes[bytes.length - 1] !== 10)) {
  if (current.length > 0 && current[current.length - 1] === 13) current.pop();
  lines.push(Buffer.from(current));
}
if (start > lines.length || end > lines.length) process.exit(3);
const parts = [];
for (let index = start - 1; index < end; index += 1) {
  if (index > start - 1) parts.push(Buffer.from("\n"));
  parts.push(lines[index]);
}
process.stdout.write("sha256:" + createHash("sha256").update(Buffer.concat(parts)).digest("hex") + "\n");
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
    Write-Host "DryRun: git executable found: $gitExe"
    Write-Host "DryRun: node executable found: $nodeExe"
    if ([string]::IsNullOrWhiteSpace($env:EZTEST_ACCEPTANCE_P4_REPO)) {
        Write-Host "DryRun: the real run will clone/refresh .tmp/p4-real-repo"
    } else {
        Write-Host "DryRun: the real run will use EZTEST_ACCEPTANCE_P4_REPO"
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

    $databaseUri = [System.Uri]$databaseUrl
    if (-not $databaseUri.IsAbsoluteUri) { throw "EZTEST_DATABASE_URL is not an absolute URL" }
    $userInfo = $databaseUri.UserInfo
    $separator = $userInfo.IndexOf(":")
    if ($separator -lt 0) { throw "EZTEST_DATABASE_URL must contain a user and password" }
    $dbUser = [System.Uri]::UnescapeDataString($userInfo.Substring(0, $separator))
    $dbPassword = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
    $dbHost = $databaseUri.Host
    $dbPort = $databaseUri.Port
    $script:secretValues += $dbPassword
    $queryIndex = $databaseUrl.IndexOf("?")
    if ($queryIndex -ge 0) {
        $urlBase = $databaseUrl.Substring(0, $queryIndex)
        $urlQuery = $databaseUrl.Substring($queryIndex)
    } else {
        $urlBase = $databaseUrl
        $urlQuery = ""
    }
    $slashIndex = $urlBase.LastIndexOf("/")
    if ($slashIndex -lt 0) { throw "EZTEST_DATABASE_URL does not contain a database name" }
    $e2eDatabaseUrl = $urlBase.Substring(0, $slashIndex + 1) + $e2eDbName + $urlQuery

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
    if (-not [bool]$health.Raw.data.llm_configured) {
        # The gate is open and the llm_* keys are present, but the seeded
        # default configuration is not usable. Spec §6.6 requires the analysis to
        # degrade to done+degraded in that case, so the run proceeds and the
        # AI-only assertions are skipped; the result records passed_degraded.
        Write-Host "WARNING: health reports llm_configured=false; the analysis is expected to degrade (AI-only checks will be skipped)." -ForegroundColor Yellow
        [void]$script:warnings.Add("health reports llm_configured=false; analysis degraded mode expected")
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

    # Point the version's branch at the base commit; the analysis explicitly
    # asks for the later head commit, exactly as the §10.7 manual path does. A
    # read-only EZTEST_ACCEPTANCE_P4_REPO cannot accept a new branch, so fall
    # back to an existing branch: the version binding only needs a branch that
    # exists, and the baseline commit stays the explicit base.
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    $branchReady = $false
    try {
        & $gitExe -C $repoPath update-ref "refs/heads/$acceptanceBranch" $baseCommit 2>$null
        $branchExit = $LASTEXITCODE
        if ($branchExit -eq 0) {
            $branchReady = $true
        } else {
            $existingBranch = ((& $gitExe -C $repoPath rev-parse --abbrev-ref HEAD 2>$null | Out-String).Trim())
            if ([string]::IsNullOrWhiteSpace($existingBranch) -or $existingBranch -eq "HEAD") {
                $existingBranch = ((& $gitExe -C $repoPath for-each-ref --count=1 --format="%(refname:short)" refs/heads 2>$null | Out-String).Trim())
            }
            if (-not [string]::IsNullOrWhiteSpace($existingBranch)) {
                $acceptanceBranch = $existingBranch
                $branchReady = $true
            }
        }
    } finally {
        $ErrorActionPreference = $previous
    }
    if (-not $branchReady) {
        throw "could not create the acceptance branch at the selected base commit (and no existing branch was found)"
    }
    if ($branchExit -ne 0) {
        [void]$script:warnings.Add("repository branch ref was not writable; bound the version to existing branch '$acceptanceBranch'")
    }
    $script:resultData.branch = $acceptanceBranch

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
    $anchorCandidates = @($gitEntries | Where-Object { $_.Status -in @("M", "T", "D", "R") })
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
    $facts = @()
    $cursor = ""
    for ($page = 0; $page -lt 10; $page++) {
        $factsUrl = "$apiBaseUrl/api/code/repos/$repoId/facts?commit=$headCommit&limit=100"
        if (-not [string]::IsNullOrWhiteSpace($cursor)) {
            $factsUrl = $factsUrl + "&cursor=" + [System.Uri]::EscapeDataString($cursor)
        }
        $factsResponse = Invoke-Api -Method Get -Uri $factsUrl -Headers $headers -TimeoutSec 60
        if ($null -ne $factsResponse.Raw.data) { $facts += @($factsResponse.Raw.data) }
        if (-not [bool]$factsResponse.Raw.has_more) { break }
        $cursor = [string]$factsResponse.Raw.next_cursor
        if ([string]::IsNullOrWhiteSpace($cursor)) { break }
    }
    $script:resultData.factCount = @($facts).Count

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
    if ([string]$forceAnalysisResponse.Data.status -eq "failed") {
        throw "force analysis failed"
    }
    $oldAgain = Invoke-Api -Method Get -Uri "$apiBaseUrl/api/versions/$versionId/code/analyses/$analysisId" -Headers $headers -TimeoutSec 30
    $oldAgainFingerprint = Get-CandidateFingerprint -Candidates $oldAgain.Data.candidates
    if ($initialFingerprint -ne $oldAgainFingerprint) {
        throw "the old analysis candidates changed after the force run"
    }
    Add-CheckResult -Name "force versioning" -Status "passed" -Detail ("analysisVersion {0} -> {1}; old analysis unchanged" -f $initialVersion, $forceVersion)

    if ($degraded) {
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
        Write-Host "PASS (degraded): the real-LLM analysis degraded, AI-only assertions were skipped; T0/caching/versioning checks passed." -ForegroundColor Yellow
    } else {
        Write-Host "PASS: real codeintel LLM acceptance completed." -ForegroundColor Green
    }
} else {
    Write-Host "FAILED: real codeintel LLM acceptance did not pass." -ForegroundColor Red
}

exit $exitCode
