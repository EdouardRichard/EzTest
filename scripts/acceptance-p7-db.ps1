# acceptance-p7-db.ps1
#
# P7 real-database acceptance (design spec 12.8, plan T29/T31).
#
# Self-contained, gated by EZTEST_RUN_REAL_DB=1 (closed by default) and safe to
# run only against the local PostgreSQL on :5433:
#   1. admin db-target CRUD + read-only connection test + write-privilege warning
#      + 403 for an ordinary user + no password in any DTO;
#   2. a real execution.db_assert passed run (server-side SQL/expected/target,
#      db_result evidence, assertion_result persisted);
#   3. repeated db_assert of the same assertion is idempotent (one result row and
#      one evidence artifact, the recorded result replayed; the single
#      execution.db_assert.executed audit row is the replay marker);
#   4. a rerun of a case whose snapshot contains a db assertion executes again and
#      cannot false-pass;
#   5. a P6 legacy case_run (inconclusive/case/unsupported_assertion_kind) with a
#      P6-style db-assertion snapshot reruns under P7, executes, and its original
#      case_run/step_result/assertion_result history is not rewritten;
#   6. a value mismatch becomes failed(application) with db_result evidence;
#   7. a write statement is rejected as inconclusive(case, db_readonly_violation)
#      and never changes the fixture table;
#   8. a result above maxRows is inconclusive(case, db_result_truncated) with the
#      truncated db_result evidence;
#   9. an unreachable target and a stored wrong password are both
#      blocked(environment, db_connection_failed);
#  10. a missing target is blocked(environment, db_target_missing);
#  11. ordinary-user 403, audit_log/db_target.last_test_message secret scan and a
#      secret scan over every log/evidence file (passwords are stored per D48 but
#      never appear in an API DTO, audit row, test message, log or evidence).
#
# Execution uses the real API, the real scheduler/agent runtime and the real
# execution.db_assert tool; the LLM is a deterministic scripted stand-in that
# only sends the documented tool calls, and the executor is a health-only stub
# (the db-only cases never call a browser tool).
#
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-p7-db.ps1 -StaticOnly
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-p7-db.ps1 -WhatIf
#   $env:EZTEST_RUN_REAL_DB="1"
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-p7-db.ps1
#
# Evidence: .tmp/p7-real-db/ (+ .tmp/p7-db-latest/ copies). Exit 0 = PASS or SKIP,
# 1 = FAIL. Passwords are never printed and never written to evidence.

[CmdletBinding()]
param(
    [switch]$WhatIf,
    [switch]$StaticOnly,
    [int]$ApiPort = 14363,
    [int]$LlmPort = 14367,
    [int]$ExecutorPort = 14368,
    [int]$FixturePort = 14365,
    [string]$EvidenceDir = ""
)

$ErrorActionPreference = "Stop"

$script:RepoRoot = Split-Path -Parent $PSScriptRoot
$script:BackendDir = Join-Path $script:RepoRoot "backend"
$script:EnvPath = Join-Path $script:RepoRoot ".env"
$script:TmpDir = Join-Path $script:RepoRoot ".tmp"
if ([string]::IsNullOrWhiteSpace($EvidenceDir)) {
    $script:EvidenceRoot = Join-Path $script:TmpDir "p7-real-db"
} else {
    # Normalize a relative EvidenceDir against the repository root so the
    # .tmp-scope guard below cannot be bypassed by the process working directory.
    $candidate = $EvidenceDir
    if (-not [System.IO.Path]::IsPathRooted($candidate)) { $candidate = Join-Path $script:RepoRoot $candidate }
    $script:EvidenceRoot = [System.IO.Path]::GetFullPath($candidate)
}
$script:LatestRoot = Join-Path $script:TmpDir "p7-db-latest"
$script:FixtureCredsPath = Join-Path $script:TmpDir "p7-db-fixture.json"
$script:FixtureScript = Join-Path $script:PSScriptRoot "fixture-db.ps1"
$script:AppDbName = "eztest_p7_db"
$script:FixtureDbName = "eztest_p7_target"
$script:FixtureTable = "db_order_fixture"
$script:FixtureSeedRows = 3
$script:ApiBaseUrl = "http://127.0.0.1:$ApiPort"
$script:FixtureBaseUrl = "http://127.0.0.1:$FixturePort"
$script:ExecutorToken = "p7-db-executor-token"

$script:checks = New-Object System.Collections.ArrayList
$script:phases = New-Object System.Collections.ArrayList
$script:secretValues = New-Object System.Collections.ArrayList
$script:targetEvidence = New-Object System.Collections.ArrayList
$script:worldEvidence = New-Object System.Collections.ArrayList
$script:resultData = [ordered]@{
    status          = "unknown"
    mode            = "real-db"
    generatedAt     = [DateTime]::UtcNow.ToString("o")
    elapsedMs       = $null
    database        = $null
    fixture         = $null
    apiPort         = $ApiPort
    worlds          = @()
    checks          = @()
    timings         = @()
    secretScan      = $null
    error           = $null
}
$script:apiProcess = $null
$script:llmProcess = $null
$script:executorProcess = $null
$script:fixtureProcess = $null
$script:psqlExe = ""
$script:nodeExe = ""
$script:goExe = ""
$script:dbHost = ""
$script:dbPort = 0
$script:dbUser = ""
$script:adminDbPassword = ""
$script:appDbUrl = ""
$script:adminToken = ""
$script:adminHeaders = $null
$script:fixture = $null
$script:startedAt = [DateTime]::UtcNow
$script:skipReason = $null

# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Write-Note {
    param([string]$Message)
    Write-Host "    $Message" -ForegroundColor DarkGray
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

function Resolve-ToolPath {
    param([string]$LocalPath, [string]$CommandName)
    if (-not [string]::IsNullOrWhiteSpace($LocalPath) -and (Test-Path -LiteralPath $LocalPath)) { return $LocalPath }
    $command = Get-Command $CommandName -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    return ""
}

function Get-RedactedText {
    param([string]$Text)
    $redacted = $Text
    foreach ($secret in $script:secretValues) {
        if (-not [string]::IsNullOrEmpty($secret)) { $redacted = $redacted.Replace($secret, "***") }
    }
    return $redacted
}

function Stop-ProcessTree {
    param($Process)
    if ($null -eq $Process) { return }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $Process.Refresh()
        if (-not $Process.HasExited) { & taskkill.exe /PID $Process.Id /T /F 2>$null | Out-Null }
    } catch {
        # Best-effort cleanup.
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Assert-PortFree {
    param([int]$Port)
    if (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue) {
        throw "port $Port is already in use"
    }
}

function Wait-PortFree {
    param([int]$Port, [int]$TimeoutSeconds = 20)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (-not (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue)) { return }
        Start-Sleep -Milliseconds 250
    }
    throw "port $Port is still in use"
}

function Assert-EvidenceRootScope {
    # The real run deletes and recreates the evidence tree. Refuse any target
    # outside the gitignored .tmp subtree (and .tmp itself) before that delete.
    $tmpPath = [System.IO.Path]::GetFullPath($script:TmpDir)
    $evidencePath = [System.IO.Path]::GetFullPath($script:EvidenceRoot)
    if ($evidencePath.Equals($tmpPath, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "EvidenceDir must be a subdirectory of .tmp, not .tmp itself"
    }
    $prefix = $tmpPath.TrimEnd([char[]]@('\', '/')) + [System.IO.Path]::DirectorySeparatorChar
    if (-not $evidencePath.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "EvidenceDir must live under $tmpPath; got $evidencePath"
    }
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
            if ([int]$response.StatusCode -eq 200) { return }
        } catch {
            # Not ready yet.
        }
        Start-Sleep -Milliseconds 500
    }
    throw "timed out waiting for $Label at $Url"
}

function Wait-Until {
    param([scriptblock]$Probe, [int]$TimeoutSeconds, [string]$Label, [int]$IntervalMs = 250)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = ""
    while ((Get-Date) -lt $deadline) {
        try {
            $value = & $Probe
            if ($null -ne $value -and $value -ne $false) { return $value }
        } catch {
            $last = Get-RedactedText -Text $_.Exception.Message
        }
        Start-Sleep -Milliseconds $IntervalMs
    }
    if ($last -ne "") { throw "$Label timed out ($last)" }
    throw "$Label timed out"
}

function Write-Utf8NoBom {
    param([string]$Path, [string]$Text)
    $parent = Split-Path -Parent $Path
    if (-not [string]::IsNullOrWhiteSpace($parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    [System.IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding($false)))
}

function Read-FileShared {
    param([string]$Path)
    # The API/LLM/executor processes keep their log handles open while the
    # acceptance scans them, so open with FileShare.ReadWrite instead of
    # ReadAllText (which demands an exclusive-compatible read).
    $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read,
        [System.IO.FileShare]::ReadWrite)
    try {
        $reader = New-Object System.IO.StreamReader($stream)
        try { return $reader.ReadToEnd() } finally { $reader.Dispose() }
    } finally {
        $stream.Dispose()
    }
}

function Get-Sha256Hex {
    param([byte[]]$Bytes)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { $hash = $sha.ComputeHash($Bytes) } finally { $sha.Dispose() }
    return (($hash | ForEach-Object { $_.ToString("x2") }) -join "")
}

function Get-EvidencePath {
    param([string]$Name)
    return (Join-Path $script:EvidenceRoot $Name)
}

function Write-EvidenceJson {
    param([string]$Name, $Value)
    $path = Get-EvidencePath -Name $Name
    Write-Utf8NoBom -Path $path -Text ((ConvertTo-Json -InputObject $Value -Depth 20) + "`n")
    return $path
}

function Write-EvidenceText {
    param([string]$Name, [string]$Text)
    $path = Get-EvidencePath -Name $Name
    Write-Utf8NoBom -Path $path -Text $Text
    return $path
}

function Complete-Phase {
    param([string]$Name, [datetime]$StartedAt)
    $elapsed = ([DateTime]::UtcNow - $StartedAt).TotalMilliseconds
    [void]$script:phases.Add([pscustomobject]@{
            phase     = $Name
            startedAt = $StartedAt.ToString("o")
            elapsedMs = [int64]$elapsed
        })
    Write-Note ("{0}: {1:N1}s" -f $Name, ($elapsed / 1000.0))
}

function Add-CheckResult {
    param([string]$Name, [string]$Status, [string]$Detail)
    [void]$script:checks.Add([pscustomobject]@{ name = $Name; status = $Status; detail = $Detail })
    $color = "Green"
    if ($Status -eq "skipped") { $color = "Yellow" }
    if ($Status -eq "failed") { $color = "Red" }
    Write-Host ("{0} {1}: {2}" -f $Status.ToUpperInvariant(), $Name, $Detail) -ForegroundColor $color
}

function Invoke-Check {
    param([string]$Name, [scriptblock]$Body)
    Write-Step $Name
    try {
        & $Body
        Add-CheckResult -Name $Name -Status "passed" -Detail "ok"
    } catch {
        Add-CheckResult -Name $Name -Status "failed" -Detail (Get-RedactedText -Text $_.Exception.Message)
    }
}

function Add-Phase {
    param([string]$Name, [datetime]$StartedAt)
    Complete-Phase -Name $Name -StartedAt $StartedAt
}

# ---------------------------------------------------------------------------
# Database URL / psql helpers
# ---------------------------------------------------------------------------

function Split-ConnectionUserInfo {
    param([string]$UserInfo)
    $separator = $UserInfo.IndexOf(":")
    if ($separator -lt 0) { return [pscustomobject]@{ User = $UserInfo; Password = "" } }
    return [pscustomobject]@{
        User     = $UserInfo.Substring(0, $separator)
        Password = $UserInfo.Substring($separator + 1)
    }
}

function Assert-LocalDatabaseUrl {
    param([string]$DatabaseUrl)
    $uri = $null
    if (-not [System.Uri]::TryCreate($DatabaseUrl, [System.UriKind]::Absolute, [ref]$uri)) {
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        throw "EZTEST_DATABASE_URL must use the postgres scheme"
    }
    if ([string]::IsNullOrWhiteSpace($uri.Host)) { throw "EZTEST_DATABASE_URL must include a host" }
    if ($uri.Host -notin @("localhost", "127.0.0.1", "::1")) {
        throw "the P7 db acceptance only targets the local PostgreSQL; EZTEST_DATABASE_URL host is not loopback"
    }
    $port = $uri.Port
    if ($port -le 0) { $port = 5432 }
    if ($port -ne 5433) {
        throw "the P7 db acceptance expects the local PostgreSQL on port 5433; got $port"
    }
    if ($uri.Query -ne "") {
        foreach ($pair in ($uri.Query.TrimStart([char]"?")) -split "&") {
            if ([string]::IsNullOrWhiteSpace($pair)) { continue }
            $equals = $pair.IndexOf("=")
            $key = if ($equals -ge 0) { $pair.Substring(0, $equals) } else { $pair }
            $key = [System.Uri]::UnescapeDataString($key).Trim().ToLowerInvariant()
            if ($key -eq "dbname" -or $key -eq "database" -or $key -eq "host" -or $key -eq "port") {
                throw "EZTEST_DATABASE_URL must not override the local target with '$key'"
            }
        }
    }
    $databaseName = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($databaseName) -or $databaseName.Contains("/")) {
        throw "EZTEST_DATABASE_URL must include a single database name"
    }
    return $uri
}

function New-DatabaseUrl {
    param([System.Uri]$DatabaseUri, [string]$DatabaseName)
    $builder = New-Object System.UriBuilder -ArgumentList @($DatabaseUri)
    $builder.Path = "/" + $DatabaseName
    return $builder.Uri.AbsoluteUri
}

function Get-PsqlScalar {
    param([string]$Database, [string]$Sql)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        $output = & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:dbUser -d $Database -tA -c $Sql 2>$null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "psql scalar query failed (exit $code)" }
    return (($output | Out-String).Trim())
}

function Invoke-PsqlQuiet {
    param([string]$Database, [string]$Sql, [string]$Operation)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:dbUser -d $Database -v ON_ERROR_STOP=1 -q -c $Sql *> $null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "$Operation failed (psql exit $code)" }
}

function Invoke-PsqlChecked {
    # Same as Invoke-PsqlQuiet but keeps psql's stderr in the failure detail.
    # The SQL travels through a UTF-8 file (-f) because Windows PowerShell strips
    # embedded double quotes when a native -c argument carries JSON. Only used for
    # the P6 legacy seed whose SQL contains no secret value.
    param([string]$Database, [string]$Sql, [string]$Operation)
    $sqlFile = Join-Path $script:TmpDir ("p7-db-psql-" + [guid]::NewGuid().ToString("N") + ".sql")
    $errorFile = Join-Path $script:TmpDir ("p7-db-psql-" + [guid]::NewGuid().ToString("N") + ".err")
    Write-Utf8NoBom -Path $sqlFile -Text ($Sql + "`n")
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:dbUser -d $Database -v ON_ERROR_STOP=1 -q -f $sqlFile 2> $errorFile | Out-Null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    $detail = ""
    if (Test-Path -LiteralPath $errorFile) {
        $detail = (Read-FileShared -Path $errorFile).Trim()
        Remove-Item -LiteralPath $errorFile -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $sqlFile -Force -ErrorAction SilentlyContinue
    if ($code -ne 0) {
        throw "$Operation failed (psql exit $code): $(Get-RedactedText -Text $detail)"
    }
}

function Get-FixtureTableFingerprint {
    # Read-only fingerprint of the fixture table through the dedicated read-only
    # role; used to prove a rejected write never mutated the target.
    $entry = $script:fixture
    $previousErrorAction = $ErrorActionPreference
    $previousPassword = [string]$env:PGPASSWORD
    $ErrorActionPreference = "SilentlyContinue"
    $env:PGPASSWORD = [string]$entry.readOnly.password
    try {
        $raw = & $script:psqlExe -h $entry.host -p $entry.port -U $entry.readOnly.username -d $entry.database -tA `
            -v ON_ERROR_STOP=1 -c "SELECT id || '|' || status || '|' || amount::text FROM $($entry.table) ORDER BY id;" 2>$null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorAction
        if ([string]::IsNullOrEmpty($previousPassword)) {
            Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
        } else {
            $env:PGPASSWORD = $previousPassword
        }
    }
    if ($code -ne 0) { throw "could not read the fixture table through the read-only role" }
    return (($raw | Out-String).Trim())
}

function Get-AppDbScalar {
    param([string]$Sql)
    return (Get-PsqlScalar -Database $script:AppDbName -Sql $Sql)
}

function Get-AppDbRowFingerprint {
    # Full-row digest: proves an original history row (and its updated_at) is
    # byte-identical before and after a rerun. $Table is always a compile-time
    # constant of this script, never external input.
    param([string]$Table, [int64]$Id)
    $value = (Get-AppDbScalar -Sql ("SELECT md5(row_to_json(t)::text) FROM {0} t WHERE t.id = {1};" -f $Table, $Id)).Trim()
    if ([string]::IsNullOrWhiteSpace($value)) { throw "no $Table row with id $Id" }
    return $value
}

function Add-P7LegacyDBCaseRun {
    # Seed a P6-era db-assertion case run: P6 never executed db assertions, so the
    # historical row is inconclusive(case, unsupported_assertion_kind) with a
    # not_run assertion record. The snapshot is the real P7 case snapshot with the
    # P7-only node.origin removed and a historical captured_at, i.e. the exact
    # shape a P6 snapshot has. P7 rerun must copy it unchanged, execute it under
    # the current rules and leave this history row untouched.
    param(
        [string]$VersionId,
        [string]$BatchId,
        [string]$SourceRunId,
        $Case
    )
    $caseRunId = [int64]880000000000000001
    $stepResultId = [int64]880000000000000002
    $assertionResultId = [int64]880000000000000003

    $snapshotText = Get-AppDbScalar -Sql ("SELECT case_snapshot::text FROM case_run WHERE id = {0};" -f $SourceRunId)
    if ([string]::IsNullOrWhiteSpace($snapshotText)) { throw "cannot read the P7 snapshot of case run $SourceRunId" }
    $caseHash = (Get-AppDbScalar -Sql ("SELECT case_hash FROM case_run WHERE id = {0};" -f $SourceRunId)).Trim()
    if ($caseHash -notmatch '^[0-9a-f]{64}$') { throw "the source case hash is '$caseHash', expected 64 lowercase hex characters" }

    $snapshot = $snapshotText | ConvertFrom-Json
    if ($null -ne $snapshot.node.PSObject.Properties["origin"]) {
        $snapshot.node.PSObject.Properties.Remove("origin")
    }
    $snapshot.captured_at = [DateTime]::UtcNow.AddDays(-2).ToString("yyyy-MM-ddTHH:mm:ssZ")
    $snapshotJson = ConvertTo-Json -InputObject $snapshot -Depth 20 -Compress
    $assertionTarget = "p7-db-target"
    if (@($snapshot.assertions).Count -gt 0 -and -not [string]::IsNullOrWhiteSpace([string]$snapshot.assertions[0].target)) {
        $assertionTarget = [string]$snapshot.assertions[0].target
    }
    $expected = [string]$Case.Expected

    $sql = @'
BEGIN;
INSERT INTO case_run
  (id, batch_id, version_id, case_node_id, case_revision, case_hash, source,
   case_snapshot, attempt, status, failure_class, failure_reason, summary,
   cleanup_required, duration_ms, started_at, finished_at, created_at, updated_at)
VALUES
  (@RUN@, @BATCH@, @VERSION@, @CASE@, 1, '@CASEHASH@', 'baseline',
   $p6snapshot$@SNAPSHOT@$p6snapshot$, 2, 'inconclusive', 'case',
   'unsupported_assertion_kind', 'P6 legacy db assertion record', false, 42,
   now() - interval '2 days', now() - interval '2 days',
   now() - interval '2 days', now() - interval '2 days');
INSERT INTO step_result
  (id, batch_id, case_run_id, seq, action, expected, actual, status,
   evidence_count, started_at, finished_at, created_at, updated_at)
VALUES
  (@STEP@, @BATCH@, @RUN@, 1, 'P6 legacy db assertion step',
   'The read-only query returns the expected rows',
   'P6 did not execute db assertions', 'passed', 0,
   now() - interval '2 days', now() - interval '2 days',
   now() - interval '2 days', now() - interval '2 days');
INSERT INTO assertion_result
  (id, batch_id, case_run_id, step_result_id, assertion_id, kind, target,
   expected, actual, verdict, failure_reason, created_at, updated_at)
VALUES
  (@ASSERTRESULT@, @BATCH@, @RUN@, @STEP@, @ASSERTION@, 'db', '@TARGET@',
   '@EXPECTED@', '', 'not_run', 'unsupported_assertion_kind',
   now() - interval '2 days', now() - interval '2 days');
COMMIT;
'@
    $sql = $sql.Replace("@RUN@", $caseRunId.ToString()).
        Replace("@BATCH@", $BatchId).
        Replace("@VERSION@", $VersionId).
        Replace("@CASE@", [string]$Case.CaseId).
        Replace("@CASEHASH@", $caseHash).
        Replace("@STEP@", $stepResultId.ToString()).
        Replace("@ASSERTRESULT@", $assertionResultId.ToString()).
        Replace("@ASSERTION@", [string]$Case.AssertionId).
        Replace("@TARGET@", $assertionTarget.Replace("'", "''")).
        Replace("@EXPECTED@", $expected.Replace("'", "''")).
        Replace("@SNAPSHOT@", $snapshotJson)
    Invoke-PsqlChecked -Database $script:AppDbName -Sql $sql -Operation "seeding the P6 legacy db case run"

    $fingerprint = (Get-AppDbRowFingerprint -Table "case_run" -Id $caseRunId) + "|" +
        (Get-AppDbRowFingerprint -Table "step_result" -Id $stepResultId) + "|" +
        (Get-AppDbRowFingerprint -Table "assertion_result" -Id $assertionResultId)
    return [pscustomobject]@{
        CaseRunId         = $caseRunId
        StepResultId      = $stepResultId
        AssertionResultId = $assertionResultId
        AssertionId       = [string]$Case.AssertionId
        Fingerprint       = $fingerprint
        SnapshotJson      = $snapshotJson
    }
}

# ---------------------------------------------------------------------------
# HTTP API helpers
# ---------------------------------------------------------------------------

function Get-ApiErrorSummary {
    param($ErrorRecord)
    $status = 0
    $reason = ""
    $rawBody = $null
    if ($null -ne $ErrorRecord.Exception -and $null -ne $ErrorRecord.Exception.Data -and
        $ErrorRecord.Exception.Data.Contains("apiStatus")) {
        return [pscustomobject]@{
            Status = [int]$ErrorRecord.Exception.Data["apiStatus"]
            Reason = [string]$ErrorRecord.Exception.Data["apiReason"]
            Text   = (Get-RedactedText -Text ([string]$ErrorRecord.Exception.Message))
        }
    }
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
        # Keep what was extracted safely.
    }
    if ([string]::IsNullOrWhiteSpace($rawBody) -and $null -ne $ErrorRecord.ErrorDetails) {
        $rawBody = [string]$ErrorRecord.ErrorDetails.Message
    }
    if (-not [string]::IsNullOrWhiteSpace($rawBody)) {
        try {
            $parsed = $rawBody | ConvertFrom-Json
            if ($null -ne $parsed.error.details) { $reason = [string]$parsed.error.details.reason }
        } catch {
            $reason = ""
        }
    }
    return [pscustomobject]@{
        Status = $status
        Reason = $reason
        Text   = (Get-RedactedText -Text ([string]$ErrorRecord.Exception.Message))
    }
}

function Invoke-Api {
    param(
        [string]$Method,
        [string]$Path,
        $Body = $null,
        $Headers = $null,
        [int]$TimeoutSec = 180,
        [switch]$NoThrow
    )
    $requestArgs = @{
        Method          = $Method
        Uri             = ($script:ApiBaseUrl + $Path)
        TimeoutSec      = $TimeoutSec
        UseBasicParsing = $true
    }
    if ($null -ne $Headers) { $requestArgs["Headers"] = $Headers }
    if ($null -ne $Body) {
        $requestArgs["ContentType"] = "application/json; charset=utf-8"
        $requestArgs["Body"] = [System.Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Compress -Depth 20))
    }
    try {
        $response = Invoke-WebRequest @requestArgs
    } catch {
        $info = Get-ApiErrorSummary -ErrorRecord $_
        if ($NoThrow) {
            return [pscustomobject]@{ Status = $info.Status; Raw = $null; Data = $null; Error = $info }
        }
        $failure = New-Object System.Exception ("{0} {1} failed: HTTP {2} reason={3}" -f $Method, $Path, $info.Status, $info.Reason)
        $failure.Data["apiStatus"] = $info.Status
        $failure.Data["apiReason"] = $info.Reason
        throw $failure
    }
    $raw = $null
    if ($response.Content -is [string] -and -not [string]::IsNullOrWhiteSpace([string]$response.Content)) {
        try { $raw = $response.Content | ConvertFrom-Json } catch { $raw = $null }
    }
    $data = $null
    if ($null -ne $raw) { $data = $raw.data }
    return [pscustomobject]@{ Status = [int]$response.StatusCode; Raw = $raw; Data = $data; Error = $null }
}

function Assert-ApiError {
    param([string]$Label, [scriptblock]$Call, [int]$Status, [string]$Reason = "")
    try {
        & $Call | Out-Null
    } catch {
        $info = Get-ApiErrorSummary -ErrorRecord $_
        if ($info.Status -eq 0) { throw "$Label failed without an HTTP response: $($info.Text)" }
        if ($info.Status -ne $Status) { throw "$Label returned HTTP $($info.Status), expected $Status" }
        if (-not [string]::IsNullOrWhiteSpace($Reason) -and $info.Reason -ne $Reason) {
            throw "$Label returned reason '$($info.Reason)', expected '$Reason'"
        }
        return $info
    }
    throw "$Label unexpectedly succeeded"
}

function Get-Token {
    param([string]$Username, [string]$Password)
    $login = Invoke-Api -Method Post -Path "/api/auth/login" -Body @{ username = $Username; password = $Password }
    if ($login.Status -ne 200) { throw "login for $Username returned HTTP $($login.Status)" }
    $token = [string]$login.Data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "login for $Username did not return a token" }
    return $token
}

function Invoke-Download {
    param([string]$Path, $Headers)
    $request = [System.Net.HttpWebRequest]::Create($script:ApiBaseUrl + $Path)
    $request.Method = "GET"
    if ($null -ne $Headers) {
        foreach ($key in $Headers.Keys) { $request.Headers[$key] = [string]$Headers[$key] }
    }
    $response = $request.GetResponse()
    try {
        $status = [int]$response.StatusCode
        $hash = [string]$response.Headers["X-Content-SHA256"]
        $contentType = [string]$response.ContentType
        $memory = New-Object System.IO.MemoryStream
        $stream = $response.GetResponseStream()
        try { $stream.CopyTo($memory) } finally { $stream.Close() }
        $bytes = $memory.ToArray()
        $memory.Dispose()
        return [pscustomobject]@{
            Status      = $status
            HeaderSha256 = $hash
            ContentType = $contentType
            Bytes       = $bytes
            Sha256      = (Get-Sha256Hex -Bytes $bytes)
            Text        = [System.Text.Encoding]::UTF8.GetString($bytes)
        }
    } finally {
        $response.Close()
    }
}

# ---------------------------------------------------------------------------
# World seeding (real resource/version/mindmap APIs)
# ---------------------------------------------------------------------------

function New-P7DbSession {
    param([string]$AccessId, [string]$EnvironmentId)
    # Log in to the fixture login form and import the real session cookie.
    $request = [System.Net.HttpWebRequest]::Create("$($script:FixtureBaseUrl)/p6/login")
    $request.Method = "POST"
    $request.AllowAutoRedirect = $false
    $request.ContentType = "application/x-www-form-urlencoded"
    $body = [System.Text.Encoding]::UTF8.GetBytes("username=fixture&password=fixture-pass")
    $request.ContentLength = $body.Length
    $stream = $request.GetRequestStream()
    try { $stream.Write($body, 0, $body.Length) } finally { $stream.Close() }
    $response = $null
    try {
        $response = $request.GetResponse()
    } catch [System.Net.WebException] {
        $response = $_.Exception.Response
    }
    if ($null -eq $response) { throw "fixture P6 login returned no response" }
    try {
        $status = [int]$response.StatusCode
        $cookieHeader = [string]$response.Headers["Set-Cookie"]
    } finally {
        $response.Close()
    }
    if ($status -ne 302) { throw "fixture P6 login returned HTTP $status, expected 302" }
    if ($cookieHeader -notmatch 'p6_session=([^;]+)') { throw "fixture P6 login did not set p6_session" }
    $cookie = $Matches[1]
    $state = @{
        cookies = @(@{
                name     = "p6_session"
                value    = $cookie
                domain   = "127.0.0.1"
                path     = "/"
                expires  = -1
                httpOnly = $true
                secure   = $false
                sameSite = "Lax"
            })
        origins = @(@{ origin = $script:FixtureBaseUrl; localStorage = @(); sessionStorage = @() })
    }
    $imported = Invoke-Api -Method Post -Path "/api/resources/accesses/$AccessId/sessions" -Headers $script:adminHeaders -Body @{
        environmentId = [int64]$EnvironmentId
        kind          = "storage_state"
        storageState  = $state
        note          = "P7 db acceptance imported fixture session"
    }
    if ($imported.Status -ne 201) { throw "POST import session returned HTTP $($imported.Status)" }
    if ([string]$imported.Data.state -ne "valid") {
        throw "the imported fixture session is '$($imported.Data.state)', expected valid (check the fixture login-check)"
    }
    return [string]$imported.Data.id
}

function New-P7DbCase {
    param(
        [string]$VersionId,
        [string]$ScenarioId,
        [string]$RequirementId,
        [string]$Title,
        [string]$Sql,
        [string]$Expected,
        [string]$TargetName
    )
    $case = Invoke-Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes" -Headers $script:adminHeaders -Body @{
        kind           = "case"
        parentId       = $ScenarioId
        title          = $Title
        priority       = "P0"
        preconditions  = "Loopback PostgreSQL :5433 read-only fixture target"
        requirementIds = @($RequirementId)
    }
    if ($case.Status -ne 201) { throw "POST case '$Title' returned HTTP $($case.Status)" }
    $caseId = [string]$case.Data.id
    $step = Invoke-Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes/$caseId/steps" -Headers $script:adminHeaders -Body @{
        action   = "Execute the read-only db assertion: $Title"
        expected = "The read-only query returns the expected rows"
    }
    if ($step.Status -ne 201) { throw "POST case step '$Title' returned HTTP $($step.Status)" }
    $assertion = Invoke-Api -Method Post -Path "/api/versions/$VersionId/mindmap/nodes/$caseId/assertions" -Headers $script:adminHeaders -Body @{
        stepId     = [string]$step.Data.id
        kind       = "db"
        target     = $TargetName
        expression = $Sql
        expected   = $Expected
    }
    if ($assertion.Status -ne 201) { throw "POST case assertion '$Title' returned HTTP $($assertion.Status)" }
    return [pscustomobject]@{
        CaseId        = $caseId
        StepId        = [string]$step.Data.id
        AssertionId   = [string]$assertion.Data.id
        Title         = $Title
        Sql           = $Sql
        Expected      = $Expected
    }
}

function New-P7DbWorld {
    param(
        [string]$Name,
        [string]$CaseTitle,
        [string]$Sql,
        [string]$Expected,
        [string]$HoldTitle = "",
        [string]$HoldSql = "",
        [string]$HoldExpected = "",
        [int]$SessionCount = 1
    )
    $stamp = Get-Date -Format "HHmmssfff"
    $suffix = [guid]::NewGuid().ToString("N").Substring(0, 8)

    $system = Invoke-Api -Method Post -Path "/api/resources/systems" -Headers $script:adminHeaders -Body @{
        key     = "p7db-sys-$stamp"
        name    = "$Name system"
        kind    = "web"
        baseUrl = $script:FixtureBaseUrl
    }
    if ($system.Status -ne 201) { throw "POST system returned HTTP $($system.Status)" }

    $environment = Invoke-Api -Method Post -Path "/api/resources/systems/$($system.Data.id)/environments" -Headers $script:adminHeaders -Body @{
        key          = "p7db-env-$stamp"
        name         = "$Name environment"
        baseUrl      = $script:FixtureBaseUrl
        kind         = "dev"
        allowedHosts = @("127.0.0.1")
        loginCheck   = @{
            url          = "$($script:FixtureBaseUrl)/p6/home"
            expectStatus = @(200)
            bodyContains = "P6 Home"
        }
    }
    if ($environment.Status -ne 201) { throw "POST environment returned HTTP $($environment.Status)" }
    $environmentId = [string]$environment.Data.id

    $flow = Invoke-Api -Method Post -Path "/api/resources/auth-flows" -Headers $script:adminHeaders -Body @{
        system_id  = [string]$system.Data.id
        name       = "$Name login $suffix"
        kind       = "form"
        definition = @{
            login_url         = "$($script:FixtureBaseUrl)/p6/login"
            username_selector = "#username"
            password_selector = "#password"
            submit_selector   = "button[type=submit]"
            success           = @{ url_contains = "/p6/home" }
            failure           = @{ invalid_selector = ".login-error"; mfa_selector = "#mfa" }
            timeout_ms        = 10000
        }
        enabled    = $true
    }
    if ($flow.Status -ne 201) { throw "POST auth flow returned HTTP $($flow.Status)" }

    $account = Invoke-Api -Method Post -Path "/api/resources/accounts" -Headers $script:adminHeaders -Body @{
        key      = "p7db-account-$stamp"
        label    = "$Name account"
        username = "fixture"
        kind     = "human"
    }
    if ($account.Status -ne 201) { throw "POST account returned HTTP $($account.Status)" }

    $access = Invoke-Api -Method Post -Path "/api/resources/accounts/$($account.Data.id)/accesses" -Headers $script:adminHeaders -Body @{
        systemId         = [int64]$system.Data.id
        authType         = "password"
        credential       = "fixture-pass"
        usernameOverride = "fixture"
        concurrencyLimit = [int]$SessionCount
        loginUrl         = "$($script:FixtureBaseUrl)/p6/login"
        login_flow_id    = [string]$flow.Data.id
        enabled          = $true
    }
    if ($access.Status -ne 201) { throw "POST access returned HTTP $($access.Status)" }

    $sessionIds = @()
    for ($i = 0; $i -lt $SessionCount; $i++) {
        $sessionIds += (New-P7DbSession -AccessId ([string]$access.Data.id) -EnvironmentId $environmentId)
    }

    $version = Invoke-Api -Method Post -Path "/api/versions" -Headers $script:adminHeaders -Body @{
        name          = "$Name version $suffix"
        label         = "p7db-$suffix"
        description   = "P7 real database acceptance version ($Name)"
        systemId      = [int64]$system.Data.id
        environmentId = [int64]$environmentId
    }
    if ($version.Status -ne 201) { throw "POST version returned HTTP $($version.Status)" }
    $versionId = [string]$version.Data.id

    $requirement = Invoke-Api -Method Post -Path "/api/versions/$versionId/requirements" -Headers $script:adminHeaders -Body @{
        title              = "$Name data consistency requirement"
        description        = "The read-only query must match the local fixture seed data."
        acceptanceCriteria = "Db assertions execute read-only; connection/permission failures never become application defects."
        priority           = "must"
    }
    if ($requirement.Status -ne 201) { throw "POST requirement returned HTTP $($requirement.Status)" }
    $requirementId = [string]$requirement.Data.id

    $clarify = Invoke-Api -Method Post -Path "/api/versions/$versionId/start-clarification" -Headers $script:adminHeaders
    if ($clarify.Status -ne 200) { throw "POST start-clarification returned HTTP $($clarify.Status)" }
    $confirmed = Invoke-Api -Method Post -Path "/api/versions/$versionId/requirements/$requirementId/confirm" -Headers $script:adminHeaders
    if ($confirmed.Status -ne 200) { throw "POST requirement confirm returned HTTP $($confirmed.Status)" }
    $enter = Invoke-Api -Method Post -Path "/api/versions/$versionId/enter-design" -Headers $script:adminHeaders -Body @{ acknowledgeUnclarified = $true }
    if ($enter.Status -ne 200) { throw "POST enter-design returned HTTP $($enter.Status)" }

    $scenario = Invoke-Api -Method Post -Path "/api/versions/$versionId/mindmap/nodes" -Headers $script:adminHeaders -Body @{
        kind  = "scenario"
        title = "$Name database assertion scenario"
    }
    if ($scenario.Status -ne 201) { throw "POST scenario returned HTTP $($scenario.Status)" }
    $scenarioId = [string]$scenario.Data.id

    $cases = @()
    $cases += (New-P7DbCase -VersionId $versionId -ScenarioId $scenarioId -RequirementId $requirementId `
            -Title $CaseTitle -Sql $Sql -Expected $Expected -TargetName "p7-db-target")
    if (-not [string]::IsNullOrWhiteSpace($HoldTitle)) {
        $cases += (New-P7DbCase -VersionId $versionId -ScenarioId $scenarioId -RequirementId $requirementId `
                -Title $HoldTitle -Sql $HoldSql -Expected $HoldExpected -TargetName "p7-db-target")
    }

    $submit = Invoke-Api -Method Post -Path "/api/versions/$versionId/mindmap/submit-review" -Headers $script:adminHeaders -Body @{}
    if ($submit.Status -ne 200) { throw "POST submit-review returned HTTP $($submit.Status)" }
    $approve = Invoke-Api -Method Post -Path "/api/versions/$versionId/mindmap/review" -Headers $script:adminHeaders -Body @{
        decision            = "approve"
        acknowledgeFindings = $true
    }
    if ($approve.Status -ne 200) { throw "POST review approve returned HTTP $($approve.Status)" }
    if ([string]$approve.Data.version.status -ne "executing") {
        throw "approval left the version in '$($approve.Data.version.status)', expected executing"
    }
    if ($null -eq $approve.Data.baseline) { throw "approval did not freeze a baseline" }

    return [pscustomobject]@{
        Name          = $Name
        SystemId      = [string]$system.Data.id
        EnvironmentId = $environmentId
        AccessId      = [string]$access.Data.id
        SessionIds    = @($sessionIds)
        VersionId     = $versionId
        RequirementId = $requirementId
        Cases         = @($cases)
        BaselineHash  = [string]$approve.Data.baseline.graphHash
    }
}

function Set-P7DbTarget {
    param(
        [string]$EnvironmentId,
        [string]$Username,
        [string]$Password,
        [int]$Port = 5433,
        [string]$HostName = "127.0.0.1",
        [int]$MaxRows = 100,
        [int]$StatementTimeoutMS = 5000,
        [string]$Name = "p7-db-target"
    )
    $response = Invoke-Api -Method Put -Path "/api/resources/environments/$EnvironmentId/db-target" -Headers $script:adminHeaders -Body @{
        name               = $Name
        host               = $HostName
        port               = $Port
        database           = $script:FixtureDbName
        username           = $Username
        password           = $Password
        sslmode            = "disable"
        connectTimeoutMs   = 500
        statementTimeoutMs = $StatementTimeoutMS
        maxRows            = $MaxRows
        enabled            = $true
    }
    if ($response.Status -ne 200) { throw "PUT db-target returned HTTP $($response.Status)" }
    return $response.Data
}

function Assert-NoPasswordInDto {
    param($Dto, [string]$Label, [string[]]$Secrets)
    $json = ConvertTo-Json -InputObject $Dto -Depth 12
    if ($json -match '"password"\s*:') { throw "$Label DTO carries a password field" }
    foreach ($secret in $Secrets) {
        if (-not [string]::IsNullOrEmpty($secret) -and $json.Contains($secret)) {
            throw "$Label DTO carries a target password value"
        }
    }
    return $json
}

function Get-Batch {
    param([string]$VersionId, [string]$BatchId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId/batches/$BatchId" -Headers $script:adminHeaders
    if ($response.Status -ne 200) { throw "GET batch returned HTTP $($response.Status)" }
    return $response.Data
}

function Wait-BatchTerminal {
    param([string]$VersionId, [string]$BatchId, [int]$TimeoutSeconds = 180)
    return (Wait-Until -TimeoutSeconds $TimeoutSeconds -Label "batch $BatchId terminal" -Probe {
            $batch = Get-Batch -VersionId $VersionId -BatchId $BatchId
            if (@("done", "failed", "cancelled") -contains [string]$batch.status) { return $batch }
            return $null
        })
}

function Get-CaseRuns {
    param([string]$VersionId, [string]$BatchId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId/batches/$BatchId/case-runs?limit=100" -Headers $script:adminHeaders
    if ($response.Status -ne 200) { throw "GET case runs returned HTTP $($response.Status)" }
    return @($response.Data)
}

function Get-CaseRun {
    param([string]$VersionId, [string]$BatchId, [string]$CaseRunId)
    $response = Invoke-Api -Method Get -Path "/api/versions/$VersionId/batches/$BatchId/case-runs/$CaseRunId" -Headers $script:adminHeaders
    if ($response.Status -ne 200) { throw "GET case run returned HTTP $($response.Status)" }
    return $response.Data
}

function Wait-CaseRunTerminal {
    param([string]$VersionId, [string]$BatchId, [string]$CaseRunId, [int]$TimeoutSeconds = 120)
    return (Wait-Until -TimeoutSeconds $TimeoutSeconds -Label "case run $CaseRunId terminal" -Probe {
            $run = Get-CaseRun -VersionId $VersionId -BatchId $BatchId -CaseRunId $CaseRunId
            if (@("passed", "failed", "blocked", "inconclusive", "skipped", "interrupted") -contains [string]$run.status) { return $run }
            return $null
        })
}

function Start-P7DbBatch {
    param($World, [string[]]$CaseIds = @())
    $body = @{ kind = "baseline" }
    if ($null -ne $CaseIds -and $CaseIds.Count -gt 0) { $body["caseIds"] = @($CaseIds) } else { $body["caseIds"] = @($World.Cases | ForEach-Object { $_.CaseId }) }
    $created = Invoke-Api -Method Post -Path "/api/versions/$($World.VersionId)/batches" -Headers $script:adminHeaders -Body $body
    if ($created.Status -ne 201) { throw "POST batch returned HTTP $($created.Status)" }
    return $created.Data
}

function Get-CaseRunForCase {
    param($World, [string]$BatchId, [string]$CaseNodeId)
    $runs = @(Get-CaseRuns -VersionId $World.VersionId -BatchId $BatchId)
    $matching = @($runs | Where-Object { [string]$_.case_node_id -eq $CaseNodeId })
    if ($matching.Count -eq 0) { return $null }
    return $matching[$matching.Count - 1]
}

function Assert-CaseOutcome {
    param(
        $World,
        $Batch,
        $Case,
        [string]$ExpectedStatus,
        [string]$ExpectedClass = "",
        [string]$ExpectedVerdict,
        [string]$ExpectedReason = "",
        [string]$ExpectedTarget = "p7-db-target",
        [bool]$ExpectEvidence = $true
    )
    $run = Get-CaseRunForCase -World $World -BatchId ([string]$Batch.id) -CaseNodeId $Case.CaseId
    if ($null -eq $run) { throw "batch $($Batch.id) has no case run for case $($Case.CaseId)" }
    $detail = Get-CaseRun -VersionId $World.VersionId -BatchId ([string]$Batch.id) -CaseRunId ([string]$run.id)
    if ([string]$detail.status -ne $ExpectedStatus) {
        throw "case '$($Case.Title)' is '$($detail.status)' reason='$($detail.failure_reason)', expected '$ExpectedStatus'"
    }
    if ($ExpectedClass -ne "") {
        if ([string]$detail.failure_class -ne $ExpectedClass) {
            throw "case '$($Case.Title)' failure_class is '$($detail.failure_class)', expected '$ExpectedClass'"
        }
    } elseif ($null -ne $detail.failure_class -and [string]$detail.failure_class -ne "") {
        throw "case '$($Case.Title)' unexpectedly carries failure_class '$($detail.failure_class)'"
    }
    if (@($detail.steps).Count -lt 1) { throw "case '$($Case.Title)' recorded no step" }
    if (@($detail.assertions).Count -ne 1) { throw "case '$($Case.Title)' recorded $(@($detail.assertions).Count) assertions, expected 1" }
    $assertion = @($detail.assertions)[0]
    if ([string]$assertion.kind -ne "db") { throw "case '$($Case.Title)' assertion kind is '$($assertion.kind)', expected db" }
    if ([string]$assertion.verdict -ne $ExpectedVerdict) {
        throw "case '$($Case.Title)' assertion verdict is '$($assertion.verdict)', expected '$ExpectedVerdict'"
    }
    if ($ExpectedReason -ne "" -and [string]$assertion.failure_reason -ne $ExpectedReason) {
        throw "case '$($Case.Title)' assertion failure_reason is '$($assertion.failure_reason)', expected '$ExpectedReason'"
    }
    $dbEvidence = @($detail.evidence | Where-Object { [string]$_.kind -eq "db_result" })
    if ($ExpectEvidence -and $dbEvidence.Count -ne 1) {
        throw "case '$($Case.Title)' has $($dbEvidence.Count) db_result artifacts, expected exactly 1"
    }
    if (-not $ExpectEvidence -and $dbEvidence.Count -ne 0) {
        throw "case '$($Case.Title)' captured $($dbEvidence.Count) db_result artifacts, expected none"
    }

    # Persisted rows, not only the HTTP projection.
    $assertionRow = Get-AppDbScalar -Sql ("SELECT count(*) || '|' || verdict || '|' || failure_reason || '|' || target FROM assertion_result WHERE case_run_id = {0} AND assertion_id = {1} GROUP BY verdict, failure_reason, target;" -f $run.id, $Case.AssertionId)
    if ([string]::IsNullOrWhiteSpace($assertionRow)) { throw "case '$($Case.Title)' has no persisted assertion_result row" }
    $parts = $assertionRow.Split("|")
    if ($parts[0] -ne "1") { throw "case '$($Case.Title)' persisted $($parts[0]) assertion rows for one snapshot assertion (idempotency violation)" }
    if ($parts[1] -ne $ExpectedVerdict) { throw "case '$($Case.Title)' persisted verdict '$($parts[1])', expected '$ExpectedVerdict'" }
    if ($ExpectedReason -ne "" -and $parts[2] -ne $ExpectedReason) {
        throw "case '$($Case.Title)' persisted reason '$($parts[2])', expected '$ExpectedReason'"
    }
    if ($ExpectedTarget -ne "" -and $parts[3] -ne $ExpectedTarget) { throw "case '$($Case.Title)' persisted target '$($parts[3])', expected $ExpectedTarget" }

    $evidenceCount = [int](Get-AppDbScalar -Sql ("SELECT count(*) FROM evidence WHERE case_run_id = {0} AND kind = 'db_result';" -f $run.id))
    if ($ExpectEvidence -and $evidenceCount -ne 1) {
        throw "case '$($Case.Title)' persisted $evidenceCount db_result evidence rows, expected exactly 1"
    }
    if (-not $ExpectEvidence -and $evidenceCount -ne 0) {
        throw "case '$($Case.Title)' persisted $evidenceCount db_result evidence rows, expected none"
    }
    return [pscustomobject]@{
        World       = $World
        Batch       = $Batch
        Case        = $Case
        Run         = $detail
        Assertion   = $assertion
        DbEvidence  = @($dbEvidence)
    }
}

# ---------------------------------------------------------------------------
# Static self-check / dry run
# ---------------------------------------------------------------------------

function Invoke-StaticChecks {
    Write-Step "Static self-check (no database, no API, no browser)"
    $problems = New-Object System.Collections.ArrayList

    foreach ($relative in @(
            "backend\cmd\eztest\main.go",
            "backend\internal\execution\application\db_assert.go",
            "backend\internal\execution\application\ports.go",
            "backend\internal\execution\application\agent_tool\db_assert.go",
            "backend\internal\execution\infrastructure\resource\db_query.go",
            "backend\internal\resource\application\db_target_service.go",
            "backend\internal\resource\infrastructure\pgreadonly\querier.go",
            "backend\internal\resource\interfaces\httpapi\handlers_db_target.go",
            "backend\internal\db\migrations\execution_0002_autonomy.sql",
            "scripts\fixture-target.cjs",
            "scripts\fixture-db.ps1",
            "scripts\acceptance-p7-db.ps1",
            "docs\openapi\api.yaml")) {
        if (-not (Test-Path -LiteralPath (Join-Path $script:RepoRoot $relative))) { [void]$problems.Add("missing $relative") }
    }

    $needles = @(
        @{ Path = "backend\cmd\eztest\main.go"; Text = "NewDBTargetService" },
        @{ Path = "backend\cmd\eztest\main.go"; Text = "executionresource.NewDBQuery" },
        @{ Path = "backend\cmd\eztest\main.go"; Text = "execution.db_assert" },
        @{ Path = "backend\internal\execution\application\db_assert.go"; Text = "func (r *CaseRecorder) DBAssert" },
        @{ Path = "backend\internal\execution\application\ports.go"; Text = "db_result_truncated" },
        @{ Path = "backend\internal\execution\application\agent_tool\db_assert.go"; Text = "execution.db_assert" },
        @{ Path = "backend\internal\resource\infrastructure\pgreadonly\querier.go"; Text = "pgx.ReadOnly" },
        @{ Path = "backend\internal\resource\infrastructure\pgreadonly\querier.go"; Text = "role_has_write_privileges" },
        @{ Path = "backend\internal\resource\interfaces\httpapi\handlers_db_target.go"; Text = "/db-target/test" },
        @{ Path = "backend\internal\db\migrations\execution_0002_autonomy.sql"; Text = "db_result" },
        @{ Path = "docs\openapi\api.yaml"; Text = "db-target" }
    )
    foreach ($needle in $needles) {
        $path = Join-Path $script:RepoRoot $needle.Path
        if (-not (Test-Path -LiteralPath $path)) {
            # A moved/renamed production file must fail the static gate instead of
            # silently skipping the wiring needle.
            [void]$problems.Add("missing $($needle.Path) (needle '$($needle.Text)')")
            continue
        }
        $content = [System.IO.File]::ReadAllText($path)
        if ($content.IndexOf($needle.Text, [System.StringComparison]::Ordinal) -lt 0) {
            [void]$problems.Add("$($needle.Path) does not contain '$($needle.Text)'")
        }
    }

    if (-not (Test-Path -LiteralPath $script:EnvPath -PathType Leaf)) {
        [void]$problems.Add("missing .env")
    } else {
        $values = Read-DotEnv -Path $script:EnvPath
        foreach ($key in @("EZTEST_DATABASE_URL", "EZTEST_ADMIN_USERNAME", "EZTEST_ADMIN_PASSWORD", "EZTEST_SECRET_KEYS")) {
            if (-not $values.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$values[$key])) {
                [void]$problems.Add("missing .env key $key")
            }
        }
        if ($values.ContainsKey("EZTEST_DATABASE_URL") -and -not [string]::IsNullOrWhiteSpace([string]$values["EZTEST_DATABASE_URL"])) {
            try {
                $uri = Assert-LocalDatabaseUrl -DatabaseUrl ([string]$values["EZTEST_DATABASE_URL"])
                Write-Note ("database target: {0}:{1} (application database '{2}')" -f $uri.Host, $uri.Port, $script:AppDbName)
            } catch {
                [void]$problems.Add((Get-RedactedText -Text $_.Exception.Message))
            }
        }
    }

    foreach ($tool in @(
            @{ Path = ".tools\go\bin\go.exe"; Command = "go.exe" },
            @{ Path = ".tools\pgsql\bin\psql.exe"; Command = "psql.exe" },
            @{ Path = ""; Command = "node.exe" })) {
        $resolved = Resolve-ToolPath -LocalPath $(if ($tool.Path) { Join-Path $script:RepoRoot $tool.Path } else { "" }) -CommandName $tool.Command
        if ([string]::IsNullOrWhiteSpace($resolved)) { [void]$problems.Add("missing tool $($tool.Command)") }
    }

    if ($problems.Count -gt 0) {
        foreach ($problem in $problems) { Write-Host "    static problem: $problem" -ForegroundColor Red }
        throw "static self-check failed with $($problems.Count) problem(s)"
    }
    Write-Note "repository layout, P7 db-assert wiring, OpenAPI, .env keys and toolchain look consistent"
}

# ---------------------------------------------------------------------------
# Fake LLM (wire-compatible, scripted execution agent)
# ---------------------------------------------------------------------------

$fakeLlmSource = @'
"use strict";
// Deterministic execution-agent stand-in for the P7 real database acceptance.
// It answers the OpenAI-compatible chat-completions protocol used by the agent
// gateway and only sends the documented execution tools:
//   execution_case_context -> execution_step_record (per step)
//   -> execution_db_assert (twice per assertion, to prove idempotency)
//   -> execution_case_finish (derived from the tool result).
// No prompt or credential is logged; the transcript only records the case run,
// the tool name and the assertion id.
const http = require("node:http");
const fs = require("node:fs");

const PORT = Number(process.env.P7_DB_LLM_PORT || 14367);
const HOST = "127.0.0.1";
const TRANSCRIPT = process.env.P7_DB_TRANSCRIPT || "";
const EXEC_MARKER = "EzTest \u7684\u6267\u884c Agent";
const MODEL = "p7-db-fake-model";

function extractText(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .map((part) => {
      if (typeof part === "string") return part;
      if (part && typeof part.text === "string") return part.text;
      return "";
    })
    .join("");
}

function systemPromptText(messages) {
  const message = messages.find((candidate) => candidate && candidate.role === "system");
  return message ? extractText(message.content) : "";
}

function findLastUserIndex(messages) {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index] && messages[index].role === "user") return index;
  }
  return -1;
}

function parseJson(text) {
  try {
    const parsed = JSON.parse(String(text || ""));
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

function describeRun(messages, lastUserIndex) {
  const byId = new Map();
  const toolResults = [];
  for (let index = lastUserIndex + 1; index < messages.length; index += 1) {
    const message = messages[index];
    if (!message) continue;
    if (message.role === "assistant" && Array.isArray(message.tool_calls)) {
      for (const call of message.tool_calls) {
        if (call && call.id) {
          byId.set(call.id, {
            name: (call.function && call.function.name) || "",
            args: (call.function && call.function.arguments) || "{}",
          });
        }
      }
      continue;
    }
    if (message.role === "tool") {
      const call = byId.get(message.tool_call_id) || { name: "", args: "{}" };
      toolResults.push({ name: call.name, args: call.args, text: extractText(message.content), call_id: message.tool_call_id || "" });
    }
  }
  return { toolResults };
}

function argumentId(entry, key) {
  const parsed = parseJson(entry.args);
  if (parsed && typeof parsed[key] !== "undefined") return String(parsed[key]);
  return "";
}

function latestCaseContext(toolResults) {
  for (let index = toolResults.length - 1; index >= 0; index -= 1) {
    if (toolResults[index].name !== "execution_case_context") continue;
    const parsed = parseJson(toolResults[index].text);
    if (parsed !== null) return parsed;
  }
  return null;
}

function transcript(entry) {
  if (!TRANSCRIPT) return;
  try {
    fs.appendFileSync(TRANSCRIPT, JSON.stringify(entry) + "\n", "utf8");
  } catch (error) {
    console.error("p7-db fake llm transcript: " + error.message);
  }
}

function toolCall(name, args) {
  return { kind: "tool", name, args };
}

function textAnswer(text) {
  return { kind: "text", text };
}

function firstLine(text) {
  const line = String(text || "").split(/\r?\n/)[0];
  return line.slice(0, 240);
}

function finishDecision(last) {
  const payload = last ? parseJson(last.text) : null;
  if (payload && typeof payload.verdict === "string") {
    if (payload.verdict === "passed") return { verdict: "passed", reason: "db assertion passed" };
    if (payload.verdict === "failed") {
      return { verdict: "failed", failure_class: "application", reason: "db assertion value mismatch" };
    }
  }
  const raw = last ? String(last.text || "") : "";
  if (raw.startsWith("db_connection_failed") || raw.startsWith("db_target_missing")) {
    return { verdict: "blocked", failure_class: "environment", reason: firstLine(raw) };
  }
  return { verdict: "inconclusive", failure_class: "case", reason: firstLine(raw) };
}

function classifyDbResult(text) {
  const payload = parseJson(text);
  if (payload && typeof payload.verdict === "string") {
    return {
      verdict: String(payload.verdict),
      reason: String(payload.reason || ""),
      row_count: Number(payload.rowCount || 0),
      elapsed_ms: Number(payload.elapsedMs || 0),
      truncated: Boolean(payload.truncated),
    };
  }
  const raw = String(text || "");
  const reasons = [
    "db_connection_failed",
    "db_target_missing",
    "db_readonly_violation",
    "db_result_truncated",
    "db_query_failed",
    "db_expected_invalid",
  ];
  for (const reason of reasons) {
    if (raw.startsWith(reason)) {
      return { verdict: "not_run", reason, row_count: 0, elapsed_ms: 0, truncated: false };
    }
  }
  return { verdict: "error", reason: firstLine(raw), row_count: 0, elapsed_ms: 0, truncated: false };
}

function planTurn(messages) {
  const lastUserIndex = findLastUserIndex(messages);
  if (lastUserIndex < 0) return { plan: textAnswer("P7 DB acceptance: no execution instruction."), context: null };
  const run = describeRun(messages, lastUserIndex);
  if (run.toolResults.some((result) => result.name === "execution_case_finish")) {
    return { plan: textAnswer("P7 DB acceptance: the case has already finished."), context: null };
  }
  const context = latestCaseContext(run.toolResults);
  if (context === null) return { plan: toolCall("execution_case_context", {}), context: null };

  const recordedSteps = new Set(
    run.toolResults
      .filter((result) => result.name === "execution_step_record")
      .map((result) => Number(argumentId(result, "seq"))),
  );
  const steps = Array.isArray(context.steps) ? context.steps : [];
  for (const step of steps) {
    if (!recordedSteps.has(Number(step.seq))) {
      return {
        plan: toolCall("execution_step_record", {
          seq: Number(step.seq),
          status: "passed",
          actual: "P7 DB acceptance: fixture step executed",
        }),
        context,
      };
    }
  }

  const assertions = (Array.isArray(context.assertions) ? context.assertions : []).filter(
    (assertion) => assertion && String(assertion.kind) === "db" && String(assertion.assertion_id) !== "",
  );
  const dbCalls = run.toolResults.filter((result) => result.name === "execution_db_assert");
  for (const assertion of assertions) {
    const assertionId = String(assertion.assertion_id);
    const calls = dbCalls.filter((result) => argumentId(result, "assertionId") === assertionId);
    // Exactly two calls: the second one must replay the recorded result without
    // executing the SQL again (spec 9.5 idempotency).
    if (calls.length < 2) {
      return { plan: toolCall("execution_db_assert", { assertionId }), context };
    }
  }

  const decision = finishDecision(dbCalls.length > 0 ? dbCalls[dbCalls.length - 1] : null);
  return { plan: toolCall("execution_case_finish", decision), context, decision };
}

function makeUsage(promptTokens, completionTokens) {
  return {
    prompt_tokens: promptTokens,
    completion_tokens: completionTokens,
    total_tokens: promptTokens + completionTokens,
    completion_tokens_details: { reasoning_tokens: 0 },
  };
}

function nowSeconds() {
  return Math.floor(Date.now() / 1000);
}

function chunk(delta, finishReason, usage) {
  const payload = {
    id: "chatcmpl-p7-db-" + nowSeconds(),
    object: "chat.completion.chunk",
    created: nowSeconds(),
    model: MODEL,
    choices: [{ index: 0, delta, finish_reason: finishReason === undefined ? null : finishReason }],
  };
  if (usage) payload.usage = usage;
  return payload;
}

function startSse(res) {
  res.writeHead(200, {
    "Content-Type": "text/event-stream; charset=utf-8",
    "Cache-Control": "no-cache, no-transform",
    Connection: "keep-alive",
    "X-Accel-Buffering": "no",
  });
  if (typeof res.flushHeaders === "function") res.flushHeaders();
}

function writeChunk(res, payload) {
  if (res.writableEnded || res.destroyed) return false;
  res.write("data: " + (typeof payload === "string" ? payload : JSON.stringify(payload)) + "\n\n");
  return true;
}

function streamText(res, text) {
  startSse(res);
  writeChunk(res, chunk({ content: text }));
  writeChunk(res, chunk({}, "stop", makeUsage(180, 24)));
  writeChunk(res, "[DONE]");
  res.end();
}

function streamTool(res, plan) {
  startSse(res);
  const callId = "p7db_call_" + nowSeconds() + "_" + Math.floor(Math.random() * 100000);
  writeChunk(res, chunk({ reasoning_content: "P7 DB acceptance: deterministic execution agent turn." }));
  writeChunk(res, chunk({
    tool_calls: [{
      index: 0,
      id: callId,
      type: "function",
      function: { name: plan.name, arguments: JSON.stringify(plan.args) },
    }],
  }));
  writeChunk(res, chunk({}, "tool_calls", makeUsage(420, 96)));
  writeChunk(res, "[DONE]");
  res.end();
}

function sendJson(res, status, body) {
  if (res.headersSent || res.writableEnded || res.destroyed) return;
  const payload = JSON.stringify(body);
  res.writeHead(status, { "Content-Type": "application/json; charset=utf-8", "Content-Length": Buffer.byteLength(payload) });
  res.end(payload);
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on("data", (part) => {
      size += part.length;
      if (size > 8 * 1024 * 1024) {
        reject(new Error("request body too large"));
        req.destroy();
        return;
      }
      chunks.push(part);
    });
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    req.on("error", reject);
  });
}

async function handle(req, res) {
  const pathname = new URL(req.url || "/", "http://" + HOST + ":" + PORT).pathname;
  if (req.method === "GET" && pathname === "/v1/models") {
    sendJson(res, 200, { object: "list", data: [{ id: MODEL, object: "model", owned_by: "p7-db-acceptance" }] });
    return;
  }
  if (req.method !== "POST" || pathname !== "/v1/chat/completions") {
    sendJson(res, 404, { error: { message: "not found" } });
    return;
  }
  let body;
  try {
    body = JSON.parse(await readBody(req));
  } catch {
    sendJson(res, 400, { error: { message: "invalid json body" } });
    return;
  }
  const messages = Array.isArray(body.messages) ? body.messages : [];
  const stream = body.stream === true;
  const systemText = systemPromptText(messages);
  if (!systemText.includes(EXEC_MARKER)) {
    // P7 autonomy agents (failure analysis / triage / supplement) are not part
    // of this acceptance; a text-only answer lets their runs settle without
    // creating materialized content.
    transcript({ kind: "other", tool: "text", model: MODEL });
    sendJson(res, 200, {
      id: "chatcmpl-p7-db-other",
      object: "chat.completion",
      created: nowSeconds(),
      model: MODEL,
      choices: [{ index: 0, message: { role: "assistant", content: "P7 DB acceptance: no action." }, finish_reason: "stop" }],
      usage: makeUsage(64, 8),
    });
    return;
  }

  const result = planTurn(messages);
  const plan = result.plan;
  const context = result.context;
  // Record every db_assert outcome observed in the conversation history. The
  // acceptance dedupes by call id; this is what proves the repeated call
  // replayed the recorded verdict instead of re-evaluating the SQL.
  const logUserIndex = findLastUserIndex(messages);
  const logRun = logUserIndex >= 0 ? describeRun(messages, logUserIndex) : { toolResults: [] };
  const logContext = latestCaseContext(logRun.toolResults);
  for (const toolResult of logRun.toolResults) {
    if (toolResult.name !== "execution_db_assert") continue;
    const classification = classifyDbResult(toolResult.text);
    transcript({
      kind: "result",
      case_run_id: logContext !== null ? String(logContext.case_run_id || "") : "",
      tool: "execution_db_assert",
      call_id: String(toolResult.call_id || ""),
      assertion_id: argumentId(toolResult, "assertionId"),
      verdict: classification.verdict,
      reason: classification.reason,
      row_count: classification.row_count,
      elapsed_ms: classification.elapsed_ms,
      truncated: classification.truncated,
    });
  }
  if (context !== null) {
    transcript({
      kind: "call",
      case_run_id: String(context.case_run_id || ""),
      case_node_id: String(context.case_node_id || ""),
      title: String(context.title || ""),
      tool: plan.name,
      assertion_id: plan.kind === "tool" ? String(plan.args.assertionId || "") : "",
      verdict: result.decision ? String(result.decision.verdict || "") : "",
    });
  } else {
    transcript({ kind: "call", case_run_id: "", tool: plan.name, assertion_id: "" });
  }

  if (plan.kind === "tool") { streamTool(res, plan); return; }
  streamText(res, plan.text);
}

const server = http.createServer((req, res) => {
  handle(req, res).catch((error) => {
    console.error("p7-db fake llm: " + (error && error.stack ? error.stack : error));
    if (!res.headersSent) {
      sendJson(res, 500, { error: { message: "fake llm internal error" } });
    } else if (!res.writableEnded) {
      try { res.end(); } catch { /* best effort */ }
    }
  });
});

server.listen(PORT, HOST, () => {
  console.log("p7-db fake llm listening on http://" + HOST + ":" + PORT);
});
'@

# ---------------------------------------------------------------------------
# Acceptance main flow
# ---------------------------------------------------------------------------

$exitCode = 0

function Invoke-Acceptance {
    Write-Step "Gate and preflight"
    $envValues = Read-DotEnv -Path $script:EnvPath
    if (-not $envValues.ContainsKey("EZTEST_DATABASE_URL") -or [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_DATABASE_URL"])) {
        throw "EZTEST_DATABASE_URL is missing from .env"
    }
    $adminUri = Assert-LocalDatabaseUrl -DatabaseUrl ([string]$envValues["EZTEST_DATABASE_URL"])
    $credentials = Split-ConnectionUserInfo -UserInfo $adminUri.UserInfo
    $script:dbHost = $adminUri.Host
    $script:dbPort = $(if ($adminUri.Port -gt 0) { $adminUri.Port } else { 5432 })
    $script:dbUser = [System.Uri]::UnescapeDataString($credentials.User)
    $script:adminDbPassword = [System.Uri]::UnescapeDataString($credentials.Password)
    $script:appDbUrl = New-DatabaseUrl -DatabaseUri $adminUri -DatabaseName $script:AppDbName
    $adminUsername = [string]$envValues["EZTEST_ADMIN_USERNAME"]
    $adminPassword = [string]$envValues["EZTEST_ADMIN_PASSWORD"]
    if ([string]::IsNullOrWhiteSpace($adminUsername) -or [string]::IsNullOrWhiteSpace($adminPassword)) {
        throw "EZTEST_ADMIN_USERNAME / EZTEST_ADMIN_PASSWORD are missing from .env"
    }
    [void]$script:secretValues.Add($adminPassword)
    [void]$script:secretValues.Add($script:adminDbPassword)
    if ($envValues.ContainsKey("llm_key") -and -not [string]::IsNullOrWhiteSpace([string]$envValues["llm_key"])) {
        [void]$script:secretValues.Add([string]$envValues["llm_key"])
    }
    if ($envValues.ContainsKey("EZTEST_SECRET_KEYS")) {
        $rawKeys = [string]$envValues["EZTEST_SECRET_KEYS"]
        if (-not [string]::IsNullOrWhiteSpace($rawKeys)) {
            [void]$script:secretValues.Add($rawKeys)
            try {
                $parsedKeys = $rawKeys | ConvertFrom-Json
                foreach ($property in $parsedKeys.PSObject.Properties) {
                    if (-not [string]::IsNullOrWhiteSpace([string]$property.Value)) {
                        [void]$script:secretValues.Add([string]$property.Value)
                    }
                }
            } catch {
                # A malformed keyring is reported by the API at startup.
            }
        }
    }

    $script:psqlExe = Resolve-ToolPath -LocalPath (Join-Path $script:RepoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql.exe"
    $script:goExe = Resolve-ToolPath -LocalPath (Join-Path $script:RepoRoot ".tools\go\bin\go.exe") -CommandName "go.exe"
    $script:nodeExe = Resolve-ToolPath -LocalPath "" -CommandName "node.exe"
    if ([string]::IsNullOrWhiteSpace($script:psqlExe)) { throw "psql executable not found (.tools or PATH)" }
    if ([string]::IsNullOrWhiteSpace($script:goExe)) { throw "go executable not found (.tools or PATH)" }
    if ([string]::IsNullOrWhiteSpace($script:nodeExe)) { throw "node executable not found on PATH" }
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) { throw "git executable not found on PATH" }

    $env:PGPASSWORD = $script:adminDbPassword
    try {
        $probe = Get-PsqlScalar -Database "postgres" -Sql "SELECT 1;"
        if ($probe -ne "1") { throw "PostgreSQL SELECT 1 returned '$probe'" }
    } catch {
        # The gate is open but the local PostgreSQL is not available: report a
        # transparent SKIP (exit 0) rather than a false PASS or a harness FAIL.
        $script:skipReason = "local PostgreSQL is not reachable at $($script:dbHost):$($script:dbPort): " + (Get-RedactedText -Text $_.Exception.Message)
        return
    }
    Add-CheckResult -Name "preflight: local PostgreSQL and toolchain" -Status "passed" -Detail "PostgreSQL :5433 answered SELECT 1; go/psql/node available"

    # Port availability was preflighted before the evidence tree was reset.
    $script:resultData.database = [ordered]@{
        host = $script:dbHost
        port = $script:dbPort
        applicationDatabase = $script:AppDbName
        fixtureDatabase = $script:FixtureDbName
        fixtureTable = $script:FixtureTable
        seedRows = $script:FixtureSeedRows
    }

    Write-Step "Preparing the dedicated fixture database and read-only role"
    $fixtureLog = Get-EvidencePath -Name "logs\fixture-db.log"
    $fixtureOutput = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $script:FixtureScript 2>&1
    $fixtureExit = $LASTEXITCODE
    Write-Utf8NoBom -Path $fixtureLog -Text (($fixtureOutput | Out-String) + "`n")
    if ($fixtureExit -ne 0) { throw "scripts/fixture-db.ps1 failed (see logs/fixture-db.log)" }
    if (-not (Test-Path -LiteralPath $script:FixtureCredsPath)) { throw "the fixture credentials file was not created" }
    $script:fixture = Get-Content -LiteralPath $script:FixtureCredsPath -Raw | ConvertFrom-Json
    if ([string]$script:fixture.database -ne $script:FixtureDbName) { throw "unexpected fixture database '$($script:fixture.database)'" }
    if ([string]$script:fixture.readOnly.username -ne "eztest_p7_ro") { throw "unexpected read-only role '$($script:fixture.readOnly.username)'" }
    if ([string]$script:fixture.readOnly.password -eq [string]$script:fixture.write.password) { throw "fixture passwords must differ" }
    [void]$script:secretValues.Add([string]$script:fixture.readOnly.password)
    [void]$script:secretValues.Add([string]$script:fixture.write.password)
    $script:resultData.fixture = [ordered]@{
        database = $script:fixture.database
        table    = $script:fixture.table
        seedRows = [int]$script:fixture.seedRows
        readOnlyRole = [string]$script:fixture.readOnly.username
        writeRole    = [string]$script:fixture.write.username
    }
    Add-CheckResult -Name "fixture database and read-only role" -Status "passed" -Detail "eztest_p7_target + eztest_p7_ro/eztest_p7_rw ensured; read-only INSERT/UPDATE/DELETE rejected"

    Write-Step "Resetting the dedicated acceptance database and building the API"
    # PGPASSWORD stays set for the whole run; Get-FixtureTableFingerprint saves
    # and restores it around its read-only role probe.
    Invoke-PsqlQuiet -Database "postgres" -Sql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$($script:AppDbName)' AND pid <> pg_backend_pid();" -Operation "terminating $($script:AppDbName) connections"
    Invoke-PsqlQuiet -Database "postgres" -Sql "DROP DATABASE IF EXISTS $($script:AppDbName) WITH (FORCE);" -Operation "dropping $($script:AppDbName)"
    Invoke-PsqlQuiet -Database "postgres" -Sql "CREATE DATABASE $($script:AppDbName);" -Operation "creating $($script:AppDbName)"
    Add-CheckResult -Name "acceptance database rebuilt" -Status "passed" -Detail "$($script:AppDbName) dropped/recreated on the local PostgreSQL"

    $apiExe = Join-Path $script:TmpDir "p7-db-acceptance-api.exe"
    $buildLog = Get-EvidencePath -Name "logs\build.log"
    Push-Location $script:BackendDir
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $script:goExe build -o $apiExe ./cmd/eztest *> $buildLog
        $buildExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        Pop-Location
    }
    if ($buildExit -ne 0) { throw "go build ./cmd/eztest failed (see logs/build.log)" }
    Add-CheckResult -Name "API build" -Status "passed" -Detail "go build ./cmd/eztest produced the acceptance binary"

    Write-Step "Starting the fake fixture target, the health-only executor and the scripted LLM"
    $logsDir = Get-EvidencePath -Name "logs"
    New-Item -ItemType Directory -Force -Path $logsDir | Out-Null
    $storageDir = Join-Path $script:EvidenceRoot "storage"
    $reportDir = Join-Path $script:EvidenceRoot "reports"
    $evidenceStore = Join-Path $script:EvidenceRoot "evidence-store"
    foreach ($dir in @($storageDir, $reportDir, $evidenceStore)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }

    $fixtureOut = Join-Path $logsDir "fixture-target.out.log"
    $fixtureErr = Join-Path $logsDir "fixture-target.err.log"
    $env:FIXTURE_PORT = "$FixturePort"
    $script:fixtureProcess = Start-Process -FilePath $script:nodeExe -ArgumentList "`"$(Join-Path $script:RepoRoot 'scripts\fixture-target.cjs')`"" `
        -WorkingDirectory $script:RepoRoot -RedirectStandardOutput $fixtureOut -RedirectStandardError $fixtureErr -WindowStyle Hidden -PassThru
    Wait-ForHttp -Url "http://127.0.0.1:$FixturePort/health" -TimeoutSeconds 30 -Process $script:fixtureProcess -Label "fixture target"

    $executorStub = Join-Path $script:EvidenceRoot "fake-executor.cjs"
    Write-Utf8NoBom -Path $executorStub -Text @'
"use strict";
// Health-only stand-in for the Playwright executor. The db-only acceptance
// cases never call a browser tool, so only the batch-create health gate needs
// an executor endpoint.
const http = require("node:http");
const port = Number(process.env.P7_DB_EXECUTOR_PORT || 14368);
const server = http.createServer((req, res) => {
  if (req.method === "GET" && req.url === "/healthz") {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ status: "ok", version: "p7-db-health-only", contexts: 0, maxContexts: 2 }));
    return;
  }
  res.writeHead(404, { "Content-Type": "application/json" });
  res.end(JSON.stringify({ error: { code: "not_found", message: "health-only executor" } }));
});
server.listen(port, "127.0.0.1");
'@
    $executorOut = Join-Path $logsDir "fake-executor.out.log"
    $executorErr = Join-Path $logsDir "fake-executor.err.log"
    $env:P7_DB_EXECUTOR_PORT = "$ExecutorPort"
    $script:executorProcess = Start-Process -FilePath $script:nodeExe -ArgumentList "`"$executorStub`"" -WorkingDirectory $script:RepoRoot `
        -RedirectStandardOutput $executorOut -RedirectStandardError $executorErr -WindowStyle Hidden -PassThru
    Wait-ForHttp -Url "http://127.0.0.1:$ExecutorPort/healthz" -TimeoutSeconds 30 -Process $script:executorProcess -Label "fake executor"

    $fakeLlmPath = Join-Path $script:EvidenceRoot "fake-llm.cjs"
    Write-Utf8NoBom -Path $fakeLlmPath -Text $fakeLlmSource
    $llmTranscript = Get-EvidencePath -Name "fake-llm-transcript.jsonl"
    Remove-Item -LiteralPath $llmTranscript -ErrorAction SilentlyContinue
    $llmOut = Join-Path $logsDir "fake-llm.out.log"
    $llmErr = Join-Path $logsDir "fake-llm.err.log"
    $env:P7_DB_LLM_PORT = "$LlmPort"
    $env:P7_DB_TRANSCRIPT = $llmTranscript
    $script:llmProcess = Start-Process -FilePath $script:nodeExe -ArgumentList "`"$fakeLlmPath`"" -WorkingDirectory $script:RepoRoot `
        -RedirectStandardOutput $llmOut -RedirectStandardError $llmErr -WindowStyle Hidden -PassThru
    Wait-ForHttp -Url "http://127.0.0.1:$LlmPort/v1/models" -TimeoutSeconds 30 -Process $script:llmProcess -Label "scripted LLM"
    Add-CheckResult -Name "deterministic harness services" -Status "passed" -Detail "fixture login target, health-only executor and scripted execution LLM are ready"

    Write-Step "Starting the API on :$ApiPort with the scripted LLM"
    $env:EZTEST_ENV = "dev"
    $env:EZTEST_DATABASE_URL = $script:appDbUrl
    $env:EZTEST_HTTP_ADDR = ":$ApiPort"
    $env:EZTEST_WEB_ORIGIN = "http://localhost:6868"
    $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
    $env:EZTEST_STORAGE_DIR = $storageDir
    $env:EZTEST_REPORT_DIR = $reportDir
    $env:EZTEST_EVIDENCE_DIR = $evidenceStore
    $env:EZTEST_OPENAPI_PATH = (Join-Path $script:RepoRoot "docs\openapi\api.yaml")
    $env:EZTEST_EXECUTOR_URL = "http://127.0.0.1:$ExecutorPort"
    $env:EZTEST_EXECUTOR_TOKEN = $script:ExecutorToken
    $env:EZTEST_EXEC_WORKER_INTERVAL_MS = "200"
    $env:EZTEST_EXEC_MAX_CONCURRENT_CASES = "2"
    $env:EZTEST_EXEC_CASE_TIMEOUT_MS = "120000"
    # The rerun window relies on a deliberately slow db assertion; keep the
    # per-tool timeout above its 10s wall time (default is 10000ms).
    $env:EZTEST_AGENT_TOOL_TIMEOUT_MS = "30000"
    $env:EZTEST_AUTONOMY_BATCHES_PER_TICK = "4"
    $env:EZTEST_ADMIN_USERNAME = $adminUsername
    $env:EZTEST_ADMIN_PASSWORD = $adminPassword
    $env:EZTEST_LOG_LEVEL = "info"
    if ($envValues.ContainsKey("EZTEST_SECRET_KEYS")) { $env:EZTEST_SECRET_KEYS = [string]$envValues["EZTEST_SECRET_KEYS"] }
    if ($envValues.ContainsKey("EZTEST_SECRET_KEY_ACTIVE")) { $env:EZTEST_SECRET_KEY_ACTIVE = [string]$envValues["EZTEST_SECRET_KEY_ACTIVE"] }
    $env:llm_base_url = "http://127.0.0.1:$LlmPort"
    $env:llm_key = "p7-db-scripted-llm-key"
    $env:llm_model_name = "p7-db-fake-model"
    Remove-Item Env:EZTEST_TEST_DATABASE_URL -ErrorAction SilentlyContinue
    Remove-Item Env:EZTEST_FAKE_LLM_URL -ErrorAction SilentlyContinue
    Remove-Item Env:EZTEST_E2E_FAKE_LLM_URL -ErrorAction SilentlyContinue

    $apiOut = Join-Path $logsDir "api.out.log"
    $apiErr = Join-Path $logsDir "api.err.log"
    $script:apiProcess = Start-Process -FilePath $apiExe -WorkingDirectory $script:BackendDir `
        -RedirectStandardOutput $apiOut -RedirectStandardError $apiErr -WindowStyle Hidden -PassThru
    Wait-ForHttp -Url "$($script:ApiBaseUrl)/api/health" -TimeoutSeconds 180 -Process $script:apiProcess -Label "API"

    $health = Invoke-Api -Method Get -Path "/api/health"
    if ([string]$health.Data.status -ne "ok" -or [string]$health.Data.db -ne "ok") { throw "API health is not ok" }
    if (-not [bool]$health.Data.llm_configured) { throw "health reports llm_configured=false" }
    $execTables = [int](Get-AppDbScalar -Sql "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('run_batch','case_run','step_result','assertion_result','evidence','evidence_ref','db_target');")
    if ($execTables -ne 7) { throw "the acceptance database has $execTables/7 execution/db-target tables after boot" }
    $script:adminToken = Get-Token -Username $adminUsername -Password $adminPassword
    $script:adminHeaders = @{ Authorization = "Bearer $script:adminToken" }
    Add-CheckResult -Name "migrations, health and admin login" -Status "passed" -Detail "7/7 execution/db-target tables; health ok; admin token obtained"

    $phase = [DateTime]::UtcNow
    Write-Step "Seeding the main fixture world (pass + rerun + slow hold)"
    $mainWorld = New-P7DbWorld -Name "P7 DB main" `
        -CaseTitle "P7 DB read-only count passes" `
        -Sql "SELECT count(*) FROM $($script:FixtureTable)" `
        -Expected '{"rows":[["3"]],"ordered":true}' `
        -HoldTitle "P7 DB slow hold" `
        -HoldSql "SELECT 1 FROM pg_sleep(10)" `
        -HoldExpected '{"rows":[["1"]],"ordered":true}' `
        -SessionCount 2
    $script:mainWorld = $mainWorld
    Complete-Phase -Name "seed main world" -StartedAt $phase
    Add-CheckResult -Name "seed main world and approved baseline" -Status "passed" -Detail ("version {0}, {1} cases, {2} imported sessions, executing with a frozen baseline" -f $mainWorld.VersionId, @($mainWorld.Cases).Count, @($mainWorld.SessionIds).Count)

    Invoke-Check -Name "admin db-target CRUD, read-only test, write-privilege warning, no password, 403" -Body {
        $ro = @{ Username = [string]$script:fixture.readOnly.username; Password = [string]$script:fixture.readOnly.password }
        $put = Invoke-Api -Method Put -Path "/api/resources/environments/$($mainWorld.EnvironmentId)/db-target" -Headers $script:adminHeaders -Body @{
            name               = "p7-db-target"
            host               = "127.0.0.1"
            port               = [int]$script:fixture.port
            database           = [string]$script:fixture.database
            username           = $ro.Username
            password           = $ro.Password
            sslmode            = "disable"
            connectTimeoutMs   = 500
            statementTimeoutMs = 15000
            maxRows            = 100
            enabled            = $true
        }
        if ($put.Status -ne 200) { throw "PUT db-target returned HTTP $($put.Status)" }
        if (-not [bool]$put.Data.credentialSet) { throw "PUT db-target did not report credentialSet=true" }
        if ([bool]$put.Data.enabled -ne $true) { throw "PUT db-target did not enable the target" }
        if ([string]$put.Data.username -ne $ro.Username) { throw "PUT db-target username is '$($put.Data.username)'" }
        $putJson = Assert-NoPasswordInDto -Dto $put.Data -Label "PUT db-target" -Secrets @($ro.Password)

        $get = Invoke-Api -Method Get -Path "/api/resources/environments/$($mainWorld.EnvironmentId)/db-target" -Headers $script:adminHeaders
        if ($get.Status -ne 200) { throw "GET db-target returned HTTP $($get.Status)" }
        if (-not [bool]$get.Data.credentialSet) { throw "GET db-target did not report credentialSet=true" }
        [void](Assert-NoPasswordInDto -Dto $get.Data -Label "GET db-target" -Secrets @($ro.Password))

        $storedTest = Invoke-Api -Method Post -Path "/api/resources/environments/$($mainWorld.EnvironmentId)/db-target/test" -Headers $script:adminHeaders -Body @{}
        if ($storedTest.Status -ne 200) { throw "POST db-target/test returned HTTP $($storedTest.Status)" }
        if (-not [bool]$storedTest.Data.ok) { throw "the stored target test failed: $($storedTest.Data.error.reason)" }
        if (-not [bool]$storedTest.Data.transactionReadOnly) { throw "the stored target test did not confirm transaction_read_only" }
        if ([string]$storedTest.Data.currentUser -ne $ro.Username) { throw "the stored target test current_user is '$($storedTest.Data.currentUser)'" }
        if (@($storedTest.Data.warnings).Count -ne 0) { throw "the read-only role produced warnings: $($storedTest.Data.warnings -join ',')" }
        [void](Assert-NoPasswordInDto -Dto $storedTest.Data -Label "db-target/test" -Secrets @($ro.Password))

        $update = Invoke-Api -Method Put -Path "/api/resources/environments/$($mainWorld.EnvironmentId)/db-target" -Headers $script:adminHeaders -Body @{
            name               = "p7-db-target"
            host               = "127.0.0.1"
            port               = [int]$script:fixture.port
            database           = [string]$script:fixture.database
            username           = $ro.Username
            maxRows            = 100
            statementTimeoutMs = 15000
        }
        if ($update.Status -ne 200) { throw "PUT db-target without password returned HTTP $($update.Status)" }
        if (-not [bool]$update.Data.credentialSet) { throw "PUT without password dropped the stored credential" }
        $retest = Invoke-Api -Method Post -Path "/api/resources/environments/$($mainWorld.EnvironmentId)/db-target/test" -Headers $script:adminHeaders -Body @{}
        if (-not [bool]$retest.Data.ok) { throw "the target test failed after a password-less update: $($retest.Data.error.reason)" }

        $adminOverride = Invoke-Api -Method Post -Path "/api/resources/environments/$($mainWorld.EnvironmentId)/db-target/test" -Headers $script:adminHeaders -Body @{
            host     = $script:dbHost
            port     = [int]$script:dbPort
            database = "postgres"
            username = $script:dbUser
            password = $script:adminDbPassword
        }
        if ($adminOverride.Status -ne 200) { throw "POST write-privilege override test returned HTTP $($adminOverride.Status)" }
        if (-not [bool]$adminOverride.Data.ok) { throw "the write-privilege override test failed: $($adminOverride.Data.error.reason)" }
        if (@($adminOverride.Data.warnings) -notcontains "role_has_write_privileges") {
            throw "the high-privilege override test did not warn role_has_write_privileges (warnings: $($adminOverride.Data.warnings -join ','))"
        }
        [void](Assert-NoPasswordInDto -Dto $adminOverride.Data -Label "write-privilege override test" -Secrets @($script:adminDbPassword))
        $afterOverride = Invoke-Api -Method Get -Path "/api/resources/environments/$($mainWorld.EnvironmentId)/db-target" -Headers $script:adminHeaders
        if ([string]$afterOverride.Data.username -ne $ro.Username) { throw "the override test persisted the unsaved form values" }
        if (-not [bool]$afterOverride.Data.credentialSet) { throw "the override test cleared the stored credential" }

        $wrongPasswordValue = "definitely-wrong-p7-db-password"
        [void]$script:secretValues.Add($wrongPasswordValue)
        $wrongPassword = Invoke-Api -Method Post -Path "/api/resources/environments/$($mainWorld.EnvironmentId)/db-target/test" -Headers $script:adminHeaders -Body @{
            host     = "127.0.0.1"
            port     = [int]$script:fixture.port
            database = [string]$script:fixture.database
            username = $ro.Username
            password = $wrongPasswordValue
        }
        if ($wrongPassword.Status -ne 200) { throw "POST wrong-password test returned HTTP $($wrongPassword.Status)" }
        if ([bool]$wrongPassword.Data.ok) { throw "the wrong-password target test unexpectedly succeeded" }
        if ([string]$wrongPassword.Data.error.reason -ne "db_connection_failed") {
            throw "the wrong-password test reason is '$($wrongPassword.Data.error.reason)', expected db_connection_failed"
        }
        # The failing test response must not echo the submitted password back.
        [void](Assert-NoPasswordInDto -Dto $wrongPassword.Data -Label "wrong-password target test" -Secrets @($wrongPasswordValue, $ro.Password))

        $stamp = Get-Date -Format "HHmmss"
        $userName = "p7dbuser$stamp"
        $userPassword = "P7Db-$stamp-Pass!"
        [void]$script:secretValues.Add($userPassword)
        $createdUser = Invoke-Api -Method Post -Path "/api/users" -Headers $script:adminHeaders -Body @{ username = $userName; password = $userPassword }
        if ($createdUser.Status -ne 201) { throw "POST ordinary user returned HTTP $($createdUser.Status)" }
        $userToken = Get-Token -Username $userName -Password $userPassword
        $userHeaders = @{ Authorization = "Bearer $userToken" }
        $envPath = "/api/resources/environments/$($mainWorld.EnvironmentId)/db-target"
        Assert-ApiError -Label "ordinary user GET db-target" -Status 403 -Call { Invoke-Api -Method Get -Path $envPath -Headers $userHeaders } | Out-Null
        Assert-ApiError -Label "ordinary user PUT db-target" -Status 403 -Call {
            Invoke-Api -Method Put -Path $envPath -Headers $userHeaders -Body @{ host = "127.0.0.1"; port = 5433; database = "x"; username = "x"; password = "x" }
        } | Out-Null
        Assert-ApiError -Label "ordinary user POST db-target/test" -Status 403 -Call {
            Invoke-Api -Method Post -Path "$envPath/test" -Headers $userHeaders -Body @{}
        } | Out-Null
        Assert-ApiError -Label "ordinary user DELETE db-target" -Status 403 -Call { Invoke-Api -Method Delete -Path $envPath -Headers $userHeaders } | Out-Null

        [void]$script:targetEvidence.Add([pscustomobject]@{
                label        = "stored"
                host         = [string]$get.Data.host
                port         = [int]$get.Data.port
                database     = [string]$get.Data.database
                username     = [string]$get.Data.username
                credentialSet = [bool]$get.Data.credentialSet
                enabled      = [bool]$get.Data.enabled
                readOnlyTest = [pscustomobject]@{
                    ok                  = [bool]$storedTest.Data.ok
                    transactionReadOnly = [bool]$storedTest.Data.transactionReadOnly
                    currentUser         = [string]$storedTest.Data.currentUser
                    warnings            = @($storedTest.Data.warnings)
                    latencyMs           = [int64]$storedTest.Data.latencyMs
                }
                highPrivilegeWarnings = @($adminOverride.Data.warnings)
                wrongPasswordReason   = [string]$wrongPassword.Data.error.reason
            })
    }

    Invoke-Check -Name "db assertion passed + P6 rerun executes again and cannot false-pass" -Body {
        $phaseStart = [DateTime]::UtcNow
        $mainCase = @($mainWorld.Cases)[0]
        $holdCase = @($mainWorld.Cases)[1]
        $batch = Start-P7DbBatch -World $mainWorld
        $script:mainBatch = $batch
        $batchId = [string]$batch.id
        $firstRun = Wait-Until -TimeoutSeconds 90 -Label "first pass case run terminal" -Probe {
            $run = Get-CaseRunForCase -World $mainWorld -BatchId $batchId -CaseNodeId $mainCase.CaseId
            if ($null -ne $run -and @("passed", "failed", "blocked", "inconclusive", "interrupted", "skipped") -contains [string]$run.status) { return $run }
            return $null
        }
        if ([string]$firstRun.status -ne "passed") {
            $detail = Get-CaseRun -VersionId $mainWorld.VersionId -BatchId $batchId -CaseRunId ([string]$firstRun.id)
            throw "the pass case finished '$($detail.status)' reason='$($detail.failure_reason)'"
        }
        $beforeRerun = Get-CaseRun -VersionId $mainWorld.VersionId -BatchId $batchId -CaseRunId ([string]$firstRun.id)
        if ([string]$beforeRerun.status -ne "passed") { throw "the pass case is no longer passed before the rerun" }
        $before = Assert-CaseOutcome -World $mainWorld -Batch $batch -Case $mainCase -ExpectedStatus "passed" -ExpectedVerdict "passed" -ExpectEvidence $true

        $rerun = Invoke-Api -Method Post -Path "/api/versions/$($mainWorld.VersionId)/batches/$batchId/case-runs/$($firstRun.id)/rerun" -Headers $script:adminHeaders -Body @{}
        if ($rerun.Status -ne 201) { throw "POST case rerun returned HTTP $($rerun.Status)" }
        $rerunId = [string]$rerun.Data.id
        if ([string]$rerun.Data.retry_of_case_run_id -ne [string]$firstRun.id) { throw "the rerun does not point at the original case run" }
        $rerunRun = Wait-CaseRunTerminal -VersionId $mainWorld.VersionId -BatchId $batchId -CaseRunId $rerunId -TimeoutSeconds 90
        if ([string]$rerunRun.status -ne "passed") { throw "the rerun case finished '$($rerunRun.status)' reason='$($rerunRun.failure_reason)'" }
        $rerunDetail = Get-CaseRun -VersionId $mainWorld.VersionId -BatchId $batchId -CaseRunId $rerunId
        $rerunAssertions = @($rerunDetail.assertions)
        if ($rerunAssertions.Count -ne 1 -or [string]$rerunAssertions[0].verdict -ne "passed") {
            throw "the rerun did not re-execute the db assertion to passed"
        }
        $rerunEvidence = @($rerunDetail.evidence | Where-Object { [string]$_.kind -eq "db_result" })
        if ($rerunEvidence.Count -ne 1) { throw "the rerun produced $($rerunEvidence.Count) db_result artifacts, expected exactly 1" }
        # A fresh case run has no recorded assertion, so it must execute the SQL
        # again; the audit sink records exactly one execution for this attempt.
        $rerunAudit = [int](Get-AppDbScalar -Sql ("SELECT count(*) FROM audit_log WHERE action = 'execution.db_assert.executed' AND metadata->>'case_run_id' = '{0}';" -f $rerunId))
        if ($rerunAudit -ne 1) { throw "the rerun case run has $rerunAudit execution.db_assert.executed audit rows, expected 1" }

        $done = Wait-BatchTerminal -VersionId $mainWorld.VersionId -BatchId $batchId -TimeoutSeconds 120
        if ([string]$done.status -ne "done") { throw "the main batch ended '$($done.status)' reason='$($done.failure_reason)'" }
        $holdRun = Assert-CaseOutcome -World $mainWorld -Batch $batch -Case $holdCase -ExpectedStatus "passed" -ExpectedVerdict "passed" -ExpectEvidence $true
        $runs = @(Get-CaseRuns -VersionId $mainWorld.VersionId -BatchId $batchId)
        if ($runs.Count -ne 3) { throw "the main batch has $($runs.Count) case runs, expected 3 (pass + hold + rerun)" }
        $allPassed = @($runs | Where-Object { [string]$_.status -ne "passed" })
        if ($allPassed.Count -ne 0) { throw "the main batch has non-passed case runs: $($allPassed | ForEach-Object { $_.id + '=' + $_.status })" }
        $script:mainPassEvidence = $before
        $script:mainHoldEvidence = $holdRun
        $script:mainRerun = [pscustomobject]@{ caseRunId = $rerunId; originalId = [string]$firstRun.id; assertionVerdict = [string]$rerunAssertions[0].verdict; evidenceCount = $rerunEvidence.Count; auditExecutions = $rerunAudit }
        Add-Phase -Name "pass + rerun" -StartedAt $phaseStart
    }

    Invoke-Check -Name "db assertion evidence is valid and the repeated call is idempotent" -Body {
        $passed = $script:mainPassEvidence
        if ($null -eq $passed) { throw "the pass evidence is missing" }
        $evidence = @($passed.DbEvidence)[0]
        $download = Invoke-Download -Path "/api/versions/$($mainWorld.VersionId)/evidence/$($evidence.id)" -Headers $script:adminHeaders
        if ($download.Status -ne 200) { throw "GET db_result evidence returned HTTP $($download.Status)" }
        if ($download.HeaderSha256 -ne $download.Sha256) { throw "the db_result evidence sha256 header does not match the bytes" }
        if ([string]$evidence.sha256 -ne $download.Sha256) { throw "the evidence row sha256 does not match the downloaded bytes" }
        if ([int64]$evidence.size_bytes -ne $download.Bytes.Length) { throw "the evidence row size does not match the downloaded bytes" }
        $document = $download.Text | ConvertFrom-Json
        if ([string]$document.sql -ne $passed.Case.Sql) { throw "the db_result evidence SQL is '$($document.sql)'" }
        if ([int]$document.row_count -ne 1) { throw "the db_result evidence row_count is $($document.row_count), expected 1" }
        if ([bool]$document.truncated) { throw "the passed db_result evidence is marked truncated" }
        if (@($document.rows)[0][0] -ne "3") { throw "the db_result evidence value is '$(@($document.rows)[0][0])', expected 3" }
        if (-not ($download.Text -match '"columns"')) { throw "the db_result evidence has no columns" }
        [void](Write-EvidenceJson -Name "evidence\db-result-passed.json" -Value $document)

        # The scripted agent sent execution.db_assert twice per assertion and the
        # second response must replay the recorded verdict; the transcript proves
        # both calls happened while the persisted row count proves only one
        # execution result exists.
        $transcriptPath = Get-EvidencePath -Name "fake-llm-transcript.jsonl"
        if (-not (Test-Path -LiteralPath $transcriptPath)) { throw "the fake-llm transcript is missing" }
        $entries = @(Get-Content -LiteralPath $transcriptPath | ForEach-Object { $_ | ConvertFrom-Json })
        $caseRunId = [string]$passed.Run.id
        $dbCalls = @($entries | Where-Object { [string]$_.kind -eq "call" -and [string]$_.case_run_id -eq $caseRunId -and [string]$_.tool -eq "execution_db_assert" })
        if ($dbCalls.Count -ne 2) { throw "the pass case run made $($dbCalls.Count) db_assert calls, expected 2 (idempotency probe)" }
        if ([string]$dbCalls[0].assertion_id -ne [string]$dbCalls[1].assertion_id) { throw "the two db_assert calls used different assertions" }
        $resultEntries = @($entries | Where-Object { [string]$_.kind -eq "result" -and [string]$_.case_run_id -eq $caseRunId -and [string]$_.tool -eq "execution_db_assert" })
        $lastByCall = @{}
        foreach ($resultEntry in $resultEntries) {
            $callId = [string]$resultEntry.call_id
            if ([string]::IsNullOrWhiteSpace($callId)) { continue }
            $lastByCall[$callId] = $resultEntry
        }
        $uniqueResults = @($lastByCall.Values)
        if ($uniqueResults.Count -ne 2) { throw "the pass case run has $($uniqueResults.Count) distinct db_assert results, expected 2" }
        foreach ($uniqueResult in $uniqueResults) {
            if ([string]$uniqueResult.verdict -ne "passed") { throw "a repeated db_assert result is '$($uniqueResult.verdict)', expected passed" }
        }
        $replayedVerdict = [string]$uniqueResults[$uniqueResults.Count - 1].verdict
        if ($replayedVerdict -ne [string]$uniqueResults[0].verdict) { throw "the repeated db_assert call changed the recorded verdict" }
        # Replay marker: both calls must report the same persisted elapsed time and
        # the audit sink must contain exactly one execution for this case run. A
        # re-execute+upsert would create a second audit row and a fresh elapsed
        # value; the repeat path reads the recorded db_result metadata instead.
        $elapsedValues = @($uniqueResults | ForEach-Object { [string]$_.elapsed_ms } | Select-Object -Unique)
        if ($elapsedValues.Count -ne 1) { throw "the two db_assert results report different elapsed_ms values ($($elapsedValues -join ',')); the second call re-executed the SQL" }
        $recordedElapsed = (Get-AppDbScalar -Sql ("SELECT metadata->>'elapsed_ms' FROM evidence WHERE case_run_id = {0} AND kind = 'db_result';" -f $caseRunId)).Trim()
        if ($recordedElapsed -ne $elapsedValues[0]) {
            throw "the repeated db_assert elapsed_ms '$($elapsedValues[0])' does not match the recorded db_result metadata '$recordedElapsed'"
        }
        $replayAudit = [int](Get-AppDbScalar -Sql ("SELECT count(*) FROM audit_log WHERE action = 'execution.db_assert.executed' AND metadata->>'case_run_id' = '{0}';" -f $caseRunId))
        if ($replayAudit -ne 1) { throw "the two db_assert calls wrote $replayAudit execution.db_assert.executed audit rows, expected 1 (replay marker)" }
        $evidenceRows = [int](Get-AppDbScalar -Sql ("SELECT count(*) FROM evidence WHERE case_run_id = {0} AND kind = 'db_result';" -f $caseRunId))
        if ($evidenceRows -ne 1) { throw "the idempotent assertion persisted $evidenceRows db_result rows" }
        $refRows = [int](Get-AppDbScalar -Sql ("SELECT count(*) FROM evidence_ref er JOIN evidence e ON e.id = er.evidence_id WHERE e.case_run_id = {0} AND er.ref_kind = 'assertion_result' AND e.kind = 'db_result';" -f $caseRunId))
        if ($refRows -ne 1) { throw "the idempotent assertion persisted $refRows evidence_ref rows" }
        $script:resultData.idempotency = [ordered]@{
            caseRunId    = $caseRunId
            dbAssertCalls = $dbCalls.Count
            replayedVerdict = $replayedVerdict
            distinctDbResults = $uniqueResults.Count
            elapsedMs    = $elapsedValues[0]
            auditExecutions = $replayAudit
            assertionRows = 1
            evidenceRows  = $evidenceRows
            evidenceRefs  = $refRows
        }
    }

    Invoke-Check -Name "P6 legacy db-assertion history reruns under P7 and is not rewritten" -Body {
        $phaseStart = [DateTime]::UtcNow
        $world = New-P7DbWorld -Name "P7 DB legacy" `
            -CaseTitle "P7 DB P6 legacy db assertion" `
            -Sql "SELECT count(*) FROM $($script:FixtureTable)" `
            -Expected '{"rows":[["3"]],"ordered":true}' `
            -HoldTitle "P7 DB legacy hold" `
            -HoldSql "SELECT 1 FROM pg_sleep(10)" `
            -HoldExpected '{"rows":[["1"]],"ordered":true}' `
            -SessionCount 2
        [void](Set-P7DbTarget -EnvironmentId $world.EnvironmentId -Username ([string]$script:fixture.readOnly.username) `
                -Password ([string]$script:fixture.readOnly.password) -Port ([int]$script:fixture.port) -MaxRows 100 -StatementTimeoutMS 15000)
        $batch = Start-P7DbBatch -World $world
        $batchId = [string]$batch.id
        $case = @($world.Cases)[0]
        $holdCase = @($world.Cases)[1]

        # Wait for the P7 seed attempt, then replace it in history with a P6 legacy
        # attempt (attempt 2) whose snapshot predates P7 (no node.origin). The hold
        # case keeps the batch active while the legacy rerun is requested.
        $attempt1 = Wait-Until -TimeoutSeconds 60 -Label "legacy world P7 seed run terminal" -Probe {
            $run = Get-CaseRunForCase -World $world -BatchId $batchId -CaseNodeId $case.CaseId
            if ($null -ne $run -and @("passed", "failed", "blocked", "inconclusive", "interrupted", "skipped") -contains [string]$run.status) { return $run }
            return $null
        }
        if ([string]$attempt1.status -ne "passed") {
            throw "the P7 seed attempt finished '$($attempt1.status)' reason='$($attempt1.failure_reason)'"
        }
        $legacy = Add-P7LegacyDBCaseRun -VersionId $world.VersionId -BatchId $batchId -SourceRunId ([string]$attempt1.id) -Case $case
        $before = $legacy.Fingerprint
        $legacyRow = (Get-AppDbScalar -Sql ("SELECT status || '|' || failure_class || '|' || failure_reason FROM case_run WHERE id = {0};" -f $legacy.CaseRunId)).Trim()
        if ($legacyRow -ne "inconclusive|case|unsupported_assertion_kind") {
            throw "the seeded P6 history row reads '$legacyRow', expected inconclusive|case|unsupported_assertion_kind"
        }

        $rerun = Invoke-Api -Method Post -Path "/api/versions/$($world.VersionId)/batches/$batchId/case-runs/$($legacy.CaseRunId)/rerun" -Headers $script:adminHeaders -Body @{}
        if ($rerun.Status -ne 201) { throw "POST P6 legacy rerun returned HTTP $($rerun.Status)" }
        $rerunId = [string]$rerun.Data.id
        if ([string]$rerun.Data.retry_of_case_run_id -ne [string]$legacy.CaseRunId) {
            throw "the legacy rerun does not point at the original P6 case run"
        }
        if ([int]$rerun.Data.attempt -ne 3) { throw "the legacy rerun attempt is $($rerun.Data.attempt), expected 3" }
        $rerunRun = Wait-CaseRunTerminal -VersionId $world.VersionId -BatchId $batchId -CaseRunId $rerunId -TimeoutSeconds 90
        if ([string]$rerunRun.status -ne "passed") {
            throw "the P7 rerun of the P6 db case finished '$($rerunRun.status)' reason='$($rerunRun.failure_reason)'"
        }
        $rerunDetail = Get-CaseRun -VersionId $world.VersionId -BatchId $batchId -CaseRunId $rerunId
        $rerunAssertions = @($rerunDetail.assertions)
        if ($rerunAssertions.Count -ne 1 -or [string]$rerunAssertions[0].verdict -ne "passed") {
            throw "the P6 legacy rerun did not execute the db assertion to passed"
        }
        $rerunEvidence = @($rerunDetail.evidence | Where-Object { [string]$_.kind -eq "db_result" })
        if ($rerunEvidence.Count -ne 1) { throw "the P6 legacy rerun produced $($rerunEvidence.Count) db_result artifacts, expected 1" }
        $rerunPersisted = (Get-AppDbScalar -Sql ("SELECT verdict || '|' || failure_reason || '|' || target FROM assertion_result WHERE case_run_id = {0} AND assertion_id = {1};" -f $rerunId, $legacy.AssertionId)).Trim()
        if ($rerunPersisted -ne "passed||p7-db-target") {
            throw "the P6 legacy rerun persisted '$rerunPersisted', expected passed||p7-db-target"
        }

        # No retroactive rewrite: the original case_run, step_result and
        # assertion_result rows (including updated_at) must be byte-identical.
        $after = (Get-AppDbRowFingerprint -Table "case_run" -Id $legacy.CaseRunId) + "|" +
            (Get-AppDbRowFingerprint -Table "step_result" -Id $legacy.StepResultId) + "|" +
            (Get-AppDbRowFingerprint -Table "assertion_result" -Id $legacy.AssertionResultId)
        if ($after -ne $before) { throw "rerunning the P6 legacy case rewrote its original history rows" }
        $legacyAssertion = (Get-AppDbScalar -Sql ("SELECT verdict || '|' || failure_reason FROM assertion_result WHERE case_run_id = {0} AND assertion_id = {1};" -f $legacy.CaseRunId, $legacy.AssertionId)).Trim()
        if ($legacyAssertion -ne "not_run|unsupported_assertion_kind") {
            throw "the original P6 assertion changed to '$legacyAssertion', expected not_run|unsupported_assertion_kind"
        }
        $legacyEvidenceCount = [int](Get-AppDbScalar -Sql ("SELECT count(*) FROM evidence WHERE case_run_id = {0};" -f $legacy.CaseRunId))
        if ($legacyEvidenceCount -ne 0) { throw "the P6 legacy run gained $legacyEvidenceCount evidence artifacts" }

        $done = Wait-BatchTerminal -VersionId $world.VersionId -BatchId $batchId -TimeoutSeconds 120
        if ([string]$done.status -ne "done") { throw "the P6 legacy batch ended '$($done.status)' reason='$($done.failure_reason)'" }
        [void](Assert-CaseOutcome -World $world -Batch $batch -Case $holdCase -ExpectedStatus "passed" -ExpectedVerdict "passed" -ExpectEvidence $true)
        $script:resultData.legacy = [ordered]@{
            versionId           = $world.VersionId
            batchId             = $batchId
            seedP7RunId         = [string]$attempt1.id
            legacyCaseRunId     = $legacy.CaseRunId.ToString()
            legacyAttempt       = 2
            legacyStatus        = "inconclusive"
            legacyFailureClass  = "case"
            legacyReason        = "unsupported_assertion_kind"
            legacyAssertion     = "not_run"
            historyUnchanged    = $true
            rerunCaseRunId      = $rerunId
            rerunAttempt        = 3
            rerunStatus         = "passed"
            rerunVerdict        = "passed"
            rerunEvidenceRows   = $rerunEvidence.Count
            persistedAfterRerun = $rerunPersisted
            counts              = $done.counts
        }
        Add-Phase -Name "P6 legacy world" -StartedAt $phaseStart
    }

    Invoke-Check -Name "db-target delete, missing target and restore" -Body {
        $path = "/api/resources/environments/$($mainWorld.EnvironmentId)/db-target"
        $deleted = Invoke-Api -Method Delete -Path $path -Headers $script:adminHeaders
        if ($deleted.Status -ne 204) { throw "DELETE db-target returned HTTP $($deleted.Status)" }
        $after = Invoke-Api -Method Get -Path $path -Headers $script:adminHeaders
        if ($after.Status -ne 200) { throw "GET db-target after delete returned HTTP $($after.Status)" }
        if ($null -ne $after.Data) { throw "GET db-target after delete returned a target" }
        $missingTest = Assert-ApiError -Label "POST db-target/test without a target" -Status 409 -Reason "db_target_missing" -Call {
            Invoke-Api -Method Post -Path "$path/test" -Headers $script:adminHeaders -Body @{}
        }
        $ro = @{ Username = [string]$script:fixture.readOnly.username; Password = [string]$script:fixture.readOnly.password }
        $restored = Set-P7DbTarget -EnvironmentId $mainWorld.EnvironmentId -Username $ro.Username -Password $ro.Password `
            -Port ([int]$script:fixture.port) -MaxRows 100 -StatementTimeoutMS 15000
        if (-not [bool]$restored.credentialSet) { throw "the restored target does not report credentialSet=true" }
        $retest = Invoke-Api -Method Post -Path "$path/test" -Headers $script:adminHeaders -Body @{}
        if (-not [bool]$retest.Data.ok) { throw "the restored target test failed: $($retest.Data.error.reason)" }
        $script:resultData.targetDelete = [ordered]@{ deleted = 204; missingReason = [string]$missingTest.Reason; restored = $true }
    }

    Invoke-Check -Name "value mismatch is failed(application) with db_result evidence" -Body {
        $phaseStart = [DateTime]::UtcNow
        $world = New-P7DbWorld -Name "P7 DB mismatch" -CaseTitle "P7 DB value mismatch" `
            -Sql "SELECT count(*) FROM $($script:FixtureTable)" -Expected '{"rows":[["4"]],"ordered":true}'
        [void](Set-P7DbTarget -EnvironmentId $world.EnvironmentId -Username ([string]$script:fixture.readOnly.username) `
                -Password ([string]$script:fixture.readOnly.password) -Port ([int]$script:fixture.port) -MaxRows 100 -StatementTimeoutMS 5000)
        $batch = Start-P7DbBatch -World $world
        $done = Wait-BatchTerminal -VersionId $world.VersionId -BatchId ([string]$batch.id) -TimeoutSeconds 90
        if ([string]$done.status -ne "done") { throw "the mismatch batch ended '$($done.status)'" }
        if ([int]$done.counts.passed -ne 0) { throw "the mismatch batch reported $($done.counts.passed) passed cases" }
        if ([int]$done.counts.failed -ne 1) { throw "the mismatch batch reported $($done.counts.failed) failed cases, expected 1" }
        $outcome = Assert-CaseOutcome -World $world -Batch $batch -Case @($world.Cases)[0] -ExpectedStatus "failed" -ExpectedClass "application" -ExpectedVerdict "failed" -ExpectedReason "assertion_mismatch" -ExpectEvidence $true
        $script:resultData.mismatch = [ordered]@{
            versionId   = $world.VersionId
            caseRunId   = [string]$outcome.Run.id
            failureClass = "application"
            verdict     = "failed"
            reason      = "assertion_mismatch"
            counts      = $done.counts
        }
        Add-Phase -Name "mismatch world" -StartedAt $phaseStart
    }

    Invoke-Check -Name "write SQL is inconclusive(case, db_readonly_violation) and never mutates the fixture" -Body {
        $phaseStart = [DateTime]::UtcNow
        $before = Get-FixtureTableFingerprint
        $world = New-P7DbWorld -Name "P7 DB write" -CaseTitle "P7 DB write SQL rejected" `
            -Sql "UPDATE $($script:FixtureTable) SET status = 'mutated' WHERE id = 1" -Expected '{"rows":[["0"]],"ordered":true}'
        [void](Set-P7DbTarget -EnvironmentId $world.EnvironmentId -Username ([string]$script:fixture.readOnly.username) `
                -Password ([string]$script:fixture.readOnly.password) -Port ([int]$script:fixture.port) -MaxRows 100 -StatementTimeoutMS 5000)
        $batch = Start-P7DbBatch -World $world
        $done = Wait-BatchTerminal -VersionId $world.VersionId -BatchId ([string]$batch.id) -TimeoutSeconds 90
        if ([string]$done.status -ne "done") { throw "the write-SQL batch ended '$($done.status)'" }
        if ([int]$done.counts.failed -ne 0) { throw "the write-SQL batch reported $($done.counts.failed) failed (application) cases" }
        if ([int]$done.counts.inconclusive -ne 1) { throw "the write-SQL batch reported $($done.counts.inconclusive) inconclusive cases, expected 1" }
        $outcome = Assert-CaseOutcome -World $world -Batch $batch -Case @($world.Cases)[0] -ExpectedStatus "inconclusive" -ExpectedClass "case" -ExpectedVerdict "not_run" -ExpectedReason "db_readonly_violation" -ExpectEvidence $false
        $after = Get-FixtureTableFingerprint
        if ($after -ne $before) { throw "the rejected write mutated the fixture table" }
        $script:resultData.writeViolation = [ordered]@{
            versionId   = $world.VersionId
            caseRunId   = [string]$outcome.Run.id
            failureClass = "case"
            verdict     = "not_run"
            reason      = "db_readonly_violation"
            tableUnchanged = $true
            counts      = $done.counts
        }
        Add-Phase -Name "write-SQL world" -StartedAt $phaseStart
    }

    Invoke-Check -Name "row limit is inconclusive(case, db_result_truncated) with truncated evidence" -Body {
        $phaseStart = [DateTime]::UtcNow
        $world = New-P7DbWorld -Name "P7 DB truncated" -CaseTitle "P7 DB result truncated" `
            -Sql "SELECT id FROM $($script:FixtureTable) ORDER BY id" -Expected '{"rows":[["1"],["2"],["3"]],"ordered":true}'
        [void](Set-P7DbTarget -EnvironmentId $world.EnvironmentId -Username ([string]$script:fixture.readOnly.username) `
                -Password ([string]$script:fixture.readOnly.password) -Port ([int]$script:fixture.port) -MaxRows 2 -StatementTimeoutMS 5000)
        $batch = Start-P7DbBatch -World $world
        $done = Wait-BatchTerminal -VersionId $world.VersionId -BatchId ([string]$batch.id) -TimeoutSeconds 90
        if ([string]$done.status -ne "done") { throw "the truncation batch ended '$($done.status)'" }
        if ([int]$done.counts.failed -ne 0) { throw "the truncation batch reported $($done.counts.failed) failed (application) cases" }
        if ([int]$done.counts.inconclusive -ne 1) { throw "the truncation batch reported $($done.counts.inconclusive) inconclusive cases, expected 1" }
        $outcome = Assert-CaseOutcome -World $world -Batch $batch -Case @($world.Cases)[0] -ExpectedStatus "inconclusive" -ExpectedClass "case" -ExpectedVerdict "not_run" -ExpectedReason "db_result_truncated" -ExpectEvidence $true
        $evidence = @($outcome.DbEvidence)[0]
        $download = Invoke-Download -Path "/api/versions/$($world.VersionId)/evidence/$($evidence.id)" -Headers $script:adminHeaders
        if ($download.Status -ne 200) { throw "GET truncated db_result evidence returned HTTP $($download.Status)" }
        $document = $download.Text | ConvertFrom-Json
        if (-not [bool]$document.truncated) { throw "the truncated db_result evidence is not marked truncated" }
        if (@($document.rows).Count -gt 2) { throw "the truncated db_result evidence has $(@($document.rows).Count) rows, maxRows is 2" }
        [void](Write-EvidenceJson -Name "evidence\db-result-truncated.json" -Value $document)
        $script:resultData.truncated = [ordered]@{
            versionId   = $world.VersionId
            caseRunId   = [string]$outcome.Run.id
            failureClass = "case"
            verdict     = "not_run"
            reason      = "db_result_truncated"
            truncated   = [bool]$document.truncated
            rows         = @($document.rows).Count
            counts      = $done.counts
        }
        Add-Phase -Name "truncation world" -StartedAt $phaseStart
    }

    Invoke-Check -Name "unreachable target is blocked(environment, db_connection_failed)" -Body {
        $phaseStart = [DateTime]::UtcNow
        $world = New-P7DbWorld -Name "P7 DB connection" -CaseTitle "P7 DB connection failure" `
            -Sql "SELECT 1" -Expected '{"rows":[["1"]],"ordered":true}'
        $closedPort = 1
        [void](Invoke-Api -Method Put -Path "/api/resources/environments/$($world.EnvironmentId)/db-target" -Headers $script:adminHeaders -Body @{
                name               = "p7-db-target"
                host               = "127.0.0.1"
                port               = $closedPort
                database           = [string]$script:fixture.database
                username           = [string]$script:fixture.readOnly.username
                password           = [string]$script:fixture.readOnly.password
                sslmode            = "disable"
                connectTimeoutMs   = 500
                statementTimeoutMs = 5000
                maxRows            = 100
                enabled            = $true
            })
        $batch = Start-P7DbBatch -World $world
        $done = Wait-BatchTerminal -VersionId $world.VersionId -BatchId ([string]$batch.id) -TimeoutSeconds 90
        if ([string]$done.status -ne "done") { throw "the connection-failure batch ended '$($done.status)'" }
        if ([int]$done.counts.failed -ne 0) { throw "the connection-failure batch reported $($done.counts.failed) failed (application) cases" }
        if ([int]$done.counts.blocked -ne 1) { throw "the connection-failure batch reported $($done.counts.blocked) blocked cases, expected 1" }
        $outcome = Assert-CaseOutcome -World $world -Batch $batch -Case @($world.Cases)[0] -ExpectedStatus "blocked" -ExpectedClass "environment" -ExpectedVerdict "not_run" -ExpectedReason "db_connection_failed" -ExpectEvidence $false
        $script:resultData.connectionFailure = [ordered]@{
            versionId   = $world.VersionId
            caseRunId   = [string]$outcome.Run.id
            failureClass = "environment"
            verdict     = "not_run"
            reason      = "db_connection_failed"
            counts      = $done.counts
        }
        Add-Phase -Name "connection world" -StartedAt $phaseStart
    }

    Invoke-Check -Name "wrong-password target is blocked(environment, db_connection_failed)" -Body {
        $phaseStart = [DateTime]::UtcNow
        $world = New-P7DbWorld -Name "P7 DB wrong password" -CaseTitle "P7 DB wrong-password target" `
            -Sql "SELECT 1" -Expected '{"rows":[["1"]],"ordered":true}'
        $wrongCasePassword = "definitely-wrong-p7-db-case-password"
        [void]$script:secretValues.Add($wrongCasePassword)
        $put = Invoke-Api -Method Put -Path "/api/resources/environments/$($world.EnvironmentId)/db-target" -Headers $script:adminHeaders -Body @{
            name               = "p7-db-target"
            host               = "127.0.0.1"
            port               = [int]$script:fixture.port
            database           = [string]$script:fixture.database
            username           = [string]$script:fixture.readOnly.username
            password           = $wrongCasePassword
            sslmode            = "disable"
            connectTimeoutMs   = 500
            statementTimeoutMs = 5000
            maxRows            = 100
            enabled            = $true
        }
        if ($put.Status -ne 200) { throw "PUT wrong-password db-target returned HTTP $($put.Status)" }
        [void](Assert-NoPasswordInDto -Dto $put.Data -Label "wrong-password db-target PUT" -Secrets @($wrongCasePassword))
        # Test the stored wrong-password target: this persists last_test_status=
        # failed plus a generic last_test_message that the secret scan inspects.
        $storedWrongTest = Invoke-Api -Method Post -Path "/api/resources/environments/$($world.EnvironmentId)/db-target/test" -Headers $script:adminHeaders -Body @{}
        if ($storedWrongTest.Status -ne 200) { throw "POST stored wrong-password test returned HTTP $($storedWrongTest.Status)" }
        if ([bool]$storedWrongTest.Data.ok) { throw "the stored wrong-password target test unexpectedly succeeded" }
        if ([string]$storedWrongTest.Data.error.reason -ne "db_connection_failed") {
            throw "the stored wrong-password test reason is '$($storedWrongTest.Data.error.reason)', expected db_connection_failed"
        }
        [void](Assert-NoPasswordInDto -Dto $storedWrongTest.Data -Label "stored wrong-password target test" -Secrets @($wrongCasePassword))
        $batch = Start-P7DbBatch -World $world
        $done = Wait-BatchTerminal -VersionId $world.VersionId -BatchId ([string]$batch.id) -TimeoutSeconds 90
        if ([string]$done.status -ne "done") { throw "the wrong-password batch ended '$($done.status)'" }
        if ([int]$done.counts.failed -ne 0) { throw "the wrong-password batch reported $($done.counts.failed) failed (application) cases" }
        if ([int]$done.counts.blocked -ne 1) { throw "the wrong-password batch reported $($done.counts.blocked) blocked cases, expected 1" }
        $outcome = Assert-CaseOutcome -World $world -Batch $batch -Case @($world.Cases)[0] -ExpectedStatus "blocked" -ExpectedClass "environment" -ExpectedVerdict "not_run" -ExpectedReason "db_connection_failed" -ExpectEvidence $false
        $script:resultData.wrongPasswordTarget = [ordered]@{
            versionId   = $world.VersionId
            caseRunId   = [string]$outcome.Run.id
            failureClass = "environment"
            verdict     = "not_run"
            reason      = "db_connection_failed"
            counts      = $done.counts
        }
        Add-Phase -Name "wrong-password world" -StartedAt $phaseStart
    }

    Invoke-Check -Name "missing target is blocked(environment, db_target_missing)" -Body {
        $phaseStart = [DateTime]::UtcNow
        $world = New-P7DbWorld -Name "P7 DB missing target" -CaseTitle "P7 DB target missing" `
            -Sql "SELECT 1" -Expected '{"rows":[["1"]],"ordered":true}'
        # No db target is configured for this environment on purpose.
        $batch = Start-P7DbBatch -World $world
        $done = Wait-BatchTerminal -VersionId $world.VersionId -BatchId ([string]$batch.id) -TimeoutSeconds 90
        if ([string]$done.status -ne "done") { throw "the missing-target batch ended '$($done.status)'" }
        if ([int]$done.counts.failed -ne 0) { throw "the missing-target batch reported $($done.counts.failed) failed (application) cases" }
        if ([int]$done.counts.blocked -ne 1) { throw "the missing-target batch reported $($done.counts.blocked) blocked cases, expected 1" }
        $outcome = Assert-CaseOutcome -World $world -Batch $batch -Case @($world.Cases)[0] -ExpectedStatus "blocked" -ExpectedClass "environment" -ExpectedVerdict "not_run" -ExpectedReason "db_target_missing" -ExpectedTarget "" -ExpectEvidence $false
        $script:resultData.missingTarget = [ordered]@{
            versionId   = $world.VersionId
            caseRunId   = [string]$outcome.Run.id
            failureClass = "environment"
            verdict     = "not_run"
            reason      = "db_target_missing"
            counts      = $done.counts
        }
        Add-Phase -Name "missing-target world" -StartedAt $phaseStart
    }

    Invoke-Check -Name "audit/db_target secret scan and event snapshot" -Body {
        $events = Invoke-Api -Method Get -Path "/api/versions/$($mainWorld.VersionId)/events?limit=100" -Headers $script:adminHeaders
        if ($events.Status -ne 200) { throw "GET version events returned HTTP $($events.Status)" }
        $eventText = ConvertTo-Json -InputObject $events.Raw -Depth 12
        [void](Write-EvidenceText -Name "events.json" -Text ($eventText + "`n"))
        foreach ($required in @("batch.created", "run.started", "batch.finished")) {
            if ($eventText -notmatch [regex]::Escape($required)) { throw "the event snapshot is missing $required" }
        }

        # Password/secret leak scan over every log, API response evidence and
        # downloaded artifact in the acceptance tree. The fixture credential file
        # is outside this tree and is the only place a password is stored.
        $secrets = @($script:secretValues | Where-Object { -not [string]::IsNullOrEmpty([string]$_) } | Select-Object -Unique)
        $files = @(Get-ChildItem -LiteralPath $script:EvidenceRoot -Recurse -File -ErrorAction SilentlyContinue)
        $leaks = New-Object System.Collections.ArrayList
        foreach ($file in $files) {
            $text = Read-FileShared -Path $file.FullName
            foreach ($secret in $secrets) {
                if ($text.Contains([string]$secret)) {
                    [void]$leaks.Add([pscustomobject]@{ file = $file.FullName; secretLength = ([string]$secret).Length })
                }
            }
        }
        # The db-target DTO evidence must never carry a password field/value.
        $targetJson = ConvertTo-Json -InputObject @($script:targetEvidence) -Depth 12
        if ($targetJson -match '"password"\s*:') { throw "the db-target evidence contains a password field" }
        foreach ($secret in $secrets) {
            if ($targetJson.Contains([string]$secret)) { throw "the db-target evidence contains a secret value" }
        }

        # The plaintext password is stored in db_target per D48, but it must never
        # reach the shared audit log, a last_test_message or a db_assert audit
        # record. Query those columns and scan them for every secret; the audit
        # projection of execution.db_assert.executed must stay ids/counts only.
        $auditText = Get-AppDbScalar -Sql "SELECT coalesce(string_agg(action || '|' || coalesce(target_type, '') || '|' || coalesce(target_id, '') || '|' || metadata::text, E'\n'), '') FROM audit_log;"
        $lastTestText = Get-AppDbScalar -Sql "SELECT coalesce(string_agg(last_test_message, E'\n'), '') FROM db_target;"
        $lastTestRows = [int](Get-AppDbScalar -Sql "SELECT count(*) FROM db_target WHERE last_test_status = 'failed' AND last_test_message <> '';")
        if ($lastTestRows -lt 1) {
            throw "the db_target.last_test_message scan has no stored failed test row to inspect"
        }
        $auditAssertCount = [int](Get-AppDbScalar -Sql "SELECT count(*) FROM audit_log WHERE action = 'execution.db_assert.executed';")
        if ($auditAssertCount -lt 1) { throw "no execution.db_assert.executed audit entry was recorded" }
        $auditTargetTypes = (Get-AppDbScalar -Sql "SELECT coalesce(string_agg(DISTINCT target_type, ','), '') FROM audit_log WHERE action = 'execution.db_assert.executed';").Trim()
        if ($auditTargetTypes -ne "case_run") {
            throw "execution.db_assert.executed target_type is '$auditTargetTypes', expected case_run"
        }
        $dbAssertAuditJson = Get-AppDbScalar -Sql "SELECT coalesce(json_agg(metadata)::text, '[]') FROM audit_log WHERE action = 'execution.db_assert.executed';"
        if ($dbAssertAuditJson -match '"password"\s*:') {
            throw "an execution.db_assert.executed audit record carries a password field"
        }
        $allowedAuditKeys = @("assertion_result_id", "case_run_id", "elapsed_ms", "row_count", "verdict")
        $auditMetadataKeys = (Get-AppDbScalar -Sql "SELECT coalesce(string_agg(k, ',' ORDER BY k), '') FROM (SELECT DISTINCT jsonb_object_keys(metadata) AS k FROM audit_log WHERE action = 'execution.db_assert.executed') keys;").Trim()
        $expectedAuditKeys = (@($allowedAuditKeys | Sort-Object) -join ",")
        if ($auditMetadataKeys -ne $expectedAuditKeys) {
            throw "execution.db_assert.executed metadata keys are '$auditMetadataKeys', expected '$expectedAuditKeys'"
        }
        $sensitiveAuditRows = [int](Get-AppDbScalar -Sql "SELECT count(*) FROM audit_log WHERE action = 'execution.db_assert.executed' AND (metadata ? 'password' OR metadata ? 'sql' OR metadata ? 'query' OR metadata ? 'credential' OR metadata ? 'rows' OR metadata ? 'preview');")
        if ($sensitiveAuditRows -ne 0) {
            throw "an execution.db_assert.executed audit record carries a sensitive metadata key"
        }
        $databaseLeaks = New-Object System.Collections.ArrayList
        foreach ($source in @(
                [pscustomobject]@{ label = "audit_log"; text = $auditText },
                [pscustomobject]@{ label = "db_target.last_test_message"; text = $lastTestText })) {
            foreach ($secret in $secrets) {
                if ($source.text.Contains([string]$secret)) {
                    [void]$databaseLeaks.Add([pscustomobject]@{ source = $source.label; secretLength = ([string]$secret).Length })
                }
            }
        }
        $scan = [ordered]@{
            scannedFiles     = $files.Count
            secretValues     = $secrets.Count
            leaks            = @($leaks)
            auditLog         = [ordered]@{
                scannedDbAssertRows     = $auditAssertCount
                dbAssertTargetType      = $auditTargetTypes
                dbAssertMetadataKeys    = $allowedAuditKeys
                scannedLastTestMessages = $lastTestRows
                databaseLeaks           = @($databaseLeaks)
            }
            dtoPasswordField = $false
            passed           = ($leaks.Count -eq 0 -and $databaseLeaks.Count -eq 0)
        }
        [void](Write-EvidenceJson -Name "secret-scan.json" -Value $scan)
        $script:resultData.secretScan = $scan
        if ($leaks.Count -gt 0) {
            throw "the secret scan found $($leaks.Count) leak(s); the first is in $($leaks[0].file)"
        }
        if ($databaseLeaks.Count -gt 0) {
            throw "the audit/db_target secret scan found $($databaseLeaks.Count) leak(s); the first is in $($databaseLeaks[0].source)"
        }
    }

    Invoke-Check -Name "world evidence summary" -Body {
        $script:resultData.worlds = @(
            [ordered]@{
                label       = "main"
                versionId   = $mainWorld.VersionId
                batchId     = [string]$script:mainBatch.id
                scenario    = "passed + rerun + hold"
                rerun       = $script:mainRerun
            },
            [ordered]@{ label = "mismatch"; versionId = $script:resultData.mismatch.versionId; caseRunId = $script:resultData.mismatch.caseRunId },
            [ordered]@{ label = "write"; versionId = $script:resultData.writeViolation.versionId; caseRunId = $script:resultData.writeViolation.caseRunId },
            [ordered]@{ label = "truncated"; versionId = $script:resultData.truncated.versionId; caseRunId = $script:resultData.truncated.caseRunId },
            [ordered]@{ label = "legacy"; versionId = $script:resultData.legacy.versionId; caseRunId = [string]$script:resultData.legacy.legacyCaseRunId; rerunCaseRunId = $script:resultData.legacy.rerunCaseRunId },
            [ordered]@{ label = "connection"; versionId = $script:resultData.connectionFailure.versionId; caseRunId = $script:resultData.connectionFailure.caseRunId },
            [ordered]@{ label = "wrong-password"; versionId = $script:resultData.wrongPasswordTarget.versionId; caseRunId = $script:resultData.wrongPasswordTarget.caseRunId },
            [ordered]@{ label = "missing"; versionId = $script:resultData.missingTarget.versionId; caseRunId = $script:resultData.missingTarget.caseRunId }
        )
        [void](Write-EvidenceJson -Name "worlds.json" -Value $script:resultData.worlds)
        [void](Write-EvidenceJson -Name "targets.json" -Value @($script:targetEvidence))
    }

    [void](Write-EvidenceJson -Name "checks.json" -Value @($script:checks))
}

# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

# Closed gate: never touch the database, the API or the evidence tree.
if (-not $StaticOnly -and -not $WhatIf -and $env:EZTEST_RUN_REAL_DB -ne "1") {
    Write-Host ""
    Write-Host "SKIPPED: real P7 database acceptance is disabled." -ForegroundColor Yellow
    Write-Host "Set `$env:EZTEST_RUN_REAL_DB=`"1`" to run against the local PostgreSQL on :5433." -ForegroundColor Yellow
    Write-Host "SKIPPED"
    Write-Host "EXIT=0"
    exit 0
}

try {
    Invoke-StaticChecks
} catch {
    Write-Host "STATIC SELF-CHECK FAILED: $(Get-RedactedText -Text $_.Exception.Message)" -ForegroundColor Red
    Write-Host "EXIT=1"
    exit 1
}

if ($StaticOnly) {
    Write-Host ""
    Write-Host "STATIC ONLY: P7 database acceptance wiring looks consistent" -ForegroundColor Green
    Write-Host "EXIT=0"
    exit 0
}

if ($WhatIf) {
    Write-Step "Dry-run plan (no database reset, no API, no browser)"
    Write-Note "fixture: scripts/fixture-db.ps1 ensures eztest_p7_target + eztest_p7_ro/eztest_p7_rw"
    Write-Note "application database: $($script:AppDbName) on the local PostgreSQL :5433 (rebuilt)"
    Write-Note "services: API :$ApiPort, scripted LLM :$LlmPort, health-only executor :$ExecutorPort, fixture :$FixturePort"
    Write-Note "checks: admin target CRUD/read-only/warning/403/wrong-password DTO; passed; idempotent repeat with replay audit marker; rerun; P6 legacy rerun/history; mismatch/failed(application); write/inconclusive; truncation/inconclusive; connection+wrong-password/blocked; missing target/blocked; audit/db_target/file secret scan"
    Write-Note "gate: set `$env:EZTEST_RUN_REAL_DB=`"1`" to execute"
    Write-Host ""
    Write-Host "WHATIF OK: no change was made" -ForegroundColor Green
    Write-Host "EXIT=0"
    exit 0
}

# Preflight the destructive evidence reset: the evidence directory must stay
# inside .tmp and every fixed port must be free *before* any earlier evidence is
# removed. A failed preflight leaves the previous evidence bundle intact.
try {
    Assert-EvidenceRootScope
    foreach ($port in @($ApiPort, $LlmPort, $ExecutorPort, $FixturePort)) { Assert-PortFree -Port $port }
} catch {
    Write-Host "PREFLIGHT FAILED: $(Get-RedactedText -Text $_.Exception.Message)" -ForegroundColor Red
    Write-Host "EXIT=1"
    exit 1
}

# A real run keeps only this run's artifacts so the secret scan and the
# evidence bundle cannot include files from an earlier invocation.
if (Test-Path -LiteralPath $script:EvidenceRoot) { Remove-Item -LiteralPath $script:EvidenceRoot -Recurse -Force }
New-Item -ItemType Directory -Force -Path $script:EvidenceRoot | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $script:EvidenceRoot "logs") | Out-Null

try {
    Invoke-Acceptance

    if (-not [string]::IsNullOrWhiteSpace($script:skipReason)) {
        $script:resultData.status = "skipped"
        $script:resultData.error = $script:skipReason
        Write-Host ""
        Write-Host "SKIPPED: $($script:skipReason)" -ForegroundColor Yellow
        Write-Host "EXIT=0"
        exit 0
    }

    $failedChecks = @($script:checks | Where-Object { $_.status -eq "failed" })
    if ($failedChecks.Count -gt 0 -or $null -ne $script:resultData.error) {
        $script:resultData.status = "failed"
        $exitCode = 1
    } else {
        $script:resultData.status = "passed"
    }
} catch {
    $script:resultData.error = Get-RedactedText -Text $_.Exception.Message
    Add-CheckResult -Name "harness" -Status "failed" -Detail $script:resultData.error
    $script:resultData.status = "failed"
    $exitCode = 1
    Write-Host "P7 REAL DB ACCEPTANCE FAILED: $($script:resultData.error)" -ForegroundColor Red
} finally {
    Stop-ProcessTree -Process $script:apiProcess
    Stop-ProcessTree -Process $script:llmProcess
    Stop-ProcessTree -Process $script:executorProcess
    Stop-ProcessTree -Process $script:fixtureProcess
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    try {
        $script:resultData.elapsedMs = [int64](([DateTime]::UtcNow - $script:startedAt).TotalMilliseconds)
        $script:resultData.checks = @($script:checks)
        $script:resultData.timings = @($script:phases)
        Write-Utf8NoBom -Path (Join-Path $script:EvidenceRoot "run.json") -Text ((ConvertTo-Json -InputObject $script:resultData -Depth 20) + "`n")
        $lines = New-Object System.Collections.ArrayList
        [void]$lines.Add("# P7 real database acceptance evidence")
        [void]$lines.Add("")
        [void]$lines.Add(("- status: ``{0}``" -f $script:resultData.status))
        [void]$lines.Add(("- mode: real local PostgreSQL :5433, read-only target, real ``execution.db_assert``"))
        [void]$lines.Add(("- generated: {0}" -f $script:resultData.generatedAt))
        [void]$lines.Add(("- elapsed: {0:N1}s" -f ($script:resultData.elapsedMs / 1000.0)))
        if ($null -ne $script:resultData.fixture) {
            [void]$lines.Add(("- fixture: ``{0}`` table ``{1}`` ({2} rows), read-only role ``{3}``" -f $script:resultData.fixture.database, $script:resultData.fixture.table, $script:resultData.fixture.seedRows, $script:resultData.fixture.readOnlyRole))
        }
        [void]$lines.Add("")
        [void]$lines.Add("| check | status | detail |")
        [void]$lines.Add("|---|---|---|")
        foreach ($check in $script:checks) {
            $detail = ([string]$check.detail).Replace("|", "\|").Replace("`r", " ").Replace("`n", " ")
            [void]$lines.Add(("| {0} | {1} | {2} |" -f $check.name, $check.status, $detail))
        }
        [void]$lines.Add("")
        if ($null -ne $script:resultData.error) { [void]$lines.Add(("ERROR: " + $script:resultData.error)) }
        Write-Utf8NoBom -Path (Join-Path $script:EvidenceRoot "verification.md") -Text (($lines -join "`n") + "`n")
        # Final self-scan of every artifact written after the main secret scan
        # (run.json, verification.md, checks/targets/worlds/secret-scan); a leak
        # there is redacted and fails the run.
        $generatedLeaks = New-Object System.Collections.ArrayList
        $generatedFiles = @(Get-ChildItem -LiteralPath $script:EvidenceRoot -Recurse -File -ErrorAction SilentlyContinue)
        foreach ($generatedFile in $generatedFiles) {
            $generatedText = Read-FileShared -Path $generatedFile.FullName
            $redactedText = $generatedText
            foreach ($secret in $script:secretValues) {
                if ([string]::IsNullOrEmpty([string]$secret)) { continue }
                if ($redactedText.Contains([string]$secret)) {
                    $redactedText = $redactedText.Replace([string]$secret, "***")
                    [void]$generatedLeaks.Add($generatedFile.Name)
                }
            }
            if ($redactedText -ne $generatedText) { Write-Utf8NoBom -Path $generatedFile.FullName -Text $redactedText }
        }
        if ($generatedLeaks.Count -gt 0) {
            Add-CheckResult -Name "generated evidence secret scan" -Status "failed" -Detail ("redacted {0} generated summary leak(s)" -f $generatedLeaks.Count)
            $script:resultData.status = "failed"
            $script:resultData.checks = @($script:checks)
            $exitCode = 1
            $runText = ConvertTo-Json -InputObject $script:resultData -Depth 20
            foreach ($secret in $script:secretValues) {
                if (-not [string]::IsNullOrEmpty([string]$secret)) { $runText = $runText.Replace([string]$secret, "***") }
            }
            Write-Utf8NoBom -Path (Join-Path $script:EvidenceRoot "run.json") -Text ($runText + "`n")
            [void]$lines.Add("")
            [void]$lines.Add(("FAILED: generated evidence secret scan redacted {0} leak(s)." -f $generatedLeaks.Count))
            Write-Utf8NoBom -Path (Join-Path $script:EvidenceRoot "verification.md") -Text (($lines -join "`n") + "`n")
        }
        New-Item -ItemType Directory -Force -Path $script:LatestRoot | Out-Null
        Copy-Item -LiteralPath (Join-Path $script:EvidenceRoot "run.json") -Destination (Join-Path $script:LatestRoot "run.json") -Force
        Copy-Item -LiteralPath (Join-Path $script:EvidenceRoot "verification.md") -Destination (Join-Path $script:LatestRoot "verification.md") -Force
    } catch {
        Write-Host "Could not write the acceptance evidence: $($_.Exception.Message)" -ForegroundColor Yellow
        $exitCode = 1
    }
    $passed = @($script:checks | Where-Object { $_.status -eq "passed" }).Count
    $failed = @($script:checks | Where-Object { $_.status -eq "failed" }).Count
    Write-Host ""
    Write-Host ("SUMMARY: {0} passed, {1} failed; status={2}" -f $passed, $failed, $script:resultData.status) -ForegroundColor $(if ($failed -gt 0 -or $exitCode -ne 0) { "Red" } else { "Green" })
    Write-Host ("EVIDENCE: {0}" -f $script:EvidenceRoot)
    Write-Host "EXIT=$exitCode"
}
exit $exitCode
