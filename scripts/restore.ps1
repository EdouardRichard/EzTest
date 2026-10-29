#requires -Version 5.1
<#
.SYNOPSIS
    Restores an EzTest backup created by scripts/backup.ps1 and verifies every
    restored file and database row count.

.DESCRIPTION
    Safety rules:
      - the target database must already exist and be empty; a non-empty target
        is refused unless -Force AND -ConfirmOverwrite are both supplied (the
        explicit second confirmation);
      - the target directory must be absent or empty for the same reason;
      - the database name must end in _test or _e2e, unless -AllowAnyDatabase is
        supplied; postgres/template0/template1 and the configured source/test
        databases are always refused;
      - the manifest is validated (sizes + sha256 of the dump and every archive)
        before any target is touched.

    Verification output is written as JSON (restore-report.json) containing the
    per-file sha256 result, row-count comparison, warnings and the fact that
    secrets are not part of the backup. The script exits non-zero when any
    verification fails.

    Secrets: EZTEST_SECRET_KEYS/EZTEST_SECRET_KEY_ACTIVE and the .env file are
    not backed up. The operator must supply them separately; without the same
    keyring the encrypted LLM configuration (llm_configs.api_key / headers)
    cannot be decrypted.

.PARAMETER BackupDir
    Directory created by scripts/backup.ps1 (contains manifest.json).

.PARAMETER TargetDatabase
    Name of the existing, empty database to restore into.

.PARAMETER TargetRoot
    Directory that receives evidence/reports/exports/version-attachments/objects.

.PARAMETER TargetDatabaseUrl
    Optional full postgres URL for the target. The database name in the URL must
    match -TargetDatabase. When omitted, host/port/user/password are taken from
    EZTEST_DATABASE_URL in .env and only the database name is swapped.

.PARAMETER EnvPath
    .env file to read; defaults to <repo>/.env.

.PARAMETER ReportPath
    Verification report path. Defaults to <TargetRoot>/restore-report.json, or
    <BackupDir>/verify-report.json with -VerifyOnly.

.PARAMETER Force
    Allow a non-empty target database/directory. Requires -ConfirmOverwrite.

.PARAMETER ConfirmOverwrite
    Second, explicit confirmation for -Force. Required together with -Force.

.PARAMETER AllowAnyDatabase
    Allow a target database whose name does not end in _test/_e2e. The configured
    source database and the system/template databases are still refused.

.PARAMETER VerifyOnly
    Do not restore or modify anything; re-verify the target directory files
    against the manifest (used after a restore or for tamper detection).

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/restore.ps1 `
        -BackupDir .tmp/p8-backup/backup -TargetDatabase eztest_restore_test -TargetRoot .tmp/p8-backup/restore

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BackupDir,
    [Parameter(Mandatory = $true)]
    [string]$TargetDatabase,
    [Parameter(Mandatory = $true)]
    [string]$TargetRoot,
    [string]$TargetDatabaseUrl = "",
    [string]$EnvPath = "",
    [string]$ReportPath = "",
    [switch]$Force,
    [switch]$ConfirmOverwrite,
    [switch]$AllowAnyDatabase,
    [switch]$VerifyOnly
)

$ErrorActionPreference = "Stop"

$script:repoRoot = Split-Path -Parent $PSScriptRoot

function Write-Step {
    param([string]$Message)
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

function Parse-PostgresUrl {
    param([string]$Url)
    if ([string]::IsNullOrWhiteSpace($Url)) { throw "EZTEST_DATABASE_URL is missing from .env" }
    $uri = $null
    if (-not [System.Uri]::TryCreate($Url, [System.UriKind]::Absolute, [ref]$uri)) {
        throw "database URL is not a valid absolute URL"
    }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        throw "database URL must use the postgres scheme"
    }
    $separator = $uri.UserInfo.IndexOf(":")
    if ($separator -lt 0) { throw "database URL must include user:password" }
    $database = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($database) -or $database.Contains("/")) {
        throw "database URL must include a single database name"
    }
    return [pscustomobject]@{
        Host     = $uri.Host
        Port     = $(if ($uri.Port -gt 0) { $uri.Port } else { 5432 })
        User     = [System.Uri]::UnescapeDataString($uri.UserInfo.Substring(0, $separator))
        Password = [System.Uri]::UnescapeDataString($uri.UserInfo.Substring($separator + 1))
        Database = $database
    }
}

function Get-Sha256 {
    param([string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Write-Utf8File {
    param([string]$Path, [string]$Content)
    $parent = Split-Path -Parent $Path
    if (-not [string]::IsNullOrWhiteSpace($parent)) {
        New-Item -ItemType Directory -Force -Path $parent | Out-Null
    }
    [System.IO.File]::WriteAllText($Path, $Content, (New-Object System.Text.UTF8Encoding($false)))
}

function Invoke-PsqlQuery {
    param([string]$Database, [string]$Sql, [string]$Operation)
    $tempFile = [System.IO.Path]::GetTempFileName()
    try {
        [System.IO.File]::WriteAllText($tempFile, $Sql, (New-Object System.Text.UTF8Encoding($false)))
        $previous = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        try {
            $output = & $script:psqlExe -h $script:target.Host -p $script:target.Port -U $script:target.User `
                -d $Database -v ON_ERROR_STOP=1 -tA -F '|' -f $tempFile 2>$null
            $code = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
        }
    } finally {
        Remove-Item -LiteralPath $tempFile -Force -ErrorAction SilentlyContinue
    }
    if ($code -ne 0) { throw "$Operation failed on database $Database (psql exit $code)" }
    return @($output)
}

function Get-PsqlScalar {
    param([string]$Database, [string]$Sql, [string]$Operation)
    $rows = @(Invoke-PsqlQuery -Database $Database -Sql $Sql -Operation $Operation)
    return (($rows | Out-String).Trim())
}

function Get-RowCounts {
    param([string]$Database)
    $tables = @(Invoke-PsqlQuery -Database $Database -Operation "listing public tables" `
            -Sql "SELECT relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND c.relkind = 'r' ORDER BY relname;")
    $counts = [ordered]@{}
    $clean = @()
    foreach ($row in $tables) {
        $name = ([string]$row).Trim()
        if (-not [string]::IsNullOrWhiteSpace($name)) { $clean += $name }
    }
    if ($clean.Count -eq 0) { return $counts }
    $query = ($clean | ForEach-Object {
            $escaped = $_.Replace('"', '""')
            "SELECT '$escaped' AS table_name, count(*)::bigint AS row_count FROM public.`"$escaped`";"
        }) -join "`n"
    foreach ($row in @(Invoke-PsqlQuery -Database $Database -Operation "counting rows" -Sql $query)) {
        $line = [string]$row
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line.Split("|")
        if ($parts.Length -ne 2) { throw "unexpected row-count output: $line" }
        $counts[$parts[0]] = [long]$parts[1]
    }
    return $counts
}

function Assert-RelativePathSafe {
    param([string]$RelativePath, [string]$Label)
    if ([string]::IsNullOrWhiteSpace($RelativePath)) { throw "$Label is empty in the manifest" }
    if ([System.IO.Path]::IsPathRooted($RelativePath)) { throw "$Label must be a relative path: $RelativePath" }
    foreach ($segment in ($RelativePath.Replace('\', '/')).Split('/')) {
        if ($segment -eq ".." -or $segment -eq "") { throw "$Label escapes its directory: $RelativePath" }
    }
}

function Resolve-RelativeFile {
    param([string]$Root, [string]$RelativePath)
    return (Join-Path $Root ($RelativePath -replace '/', '\'))
}

function Test-UserObjects {
    param([string]$Database)
    return (Get-PsqlScalar -Database $Database -Operation "checking whether the target is empty" -Sql @"
SELECT count(*)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
  AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f');
"@)
}

function Get-FileVerification {
    param($Manifest, [string]$Root)
    $missing = New-Object System.Collections.ArrayList
    $mismatches = New-Object System.Collections.ArrayList
    $unexpected = New-Object System.Collections.ArrayList
    $expected = @{}
    $expectedFiles = 0
    $verifiedFiles = 0
    foreach ($directory in @($Manifest.directories)) {
        $label = [string]$directory.label
        Assert-RelativePathSafe -RelativePath $label -Label "directory label"
        $directoryRoot = Join-Path $Root $label
        foreach ($file in @($directory.files)) {
            if ($null -eq $file) { continue }
            $relative = [string]$file.path
            Assert-RelativePathSafe -RelativePath $relative -Label "file path"
            $expectedFiles++
            $key = "$label/$relative"
            $expected[$key] = $true
            $targetPath = Resolve-RelativeFile -Root $directoryRoot -RelativePath $relative
            if (-not (Test-Path -LiteralPath $targetPath -PathType Leaf)) {
                [void]$missing.Add($key)
                continue
            }
            $actualSize = (Get-Item -LiteralPath $targetPath).Length
            if ([long]$file.sizeBytes -ne [long]$actualSize) {
                [void]$mismatches.Add("$key (size expected $($file.sizeBytes) got $actualSize)")
                continue
            }
            $actualSha = Get-Sha256 -Path $targetPath
            if ($actualSha -ne [string]$file.sha256) {
                [void]$mismatches.Add("$key (sha256 expected $($file.sha256) got $actualSha)")
                continue
            }
            $verifiedFiles++
        }
        if (Test-Path -LiteralPath $directoryRoot -PathType Container) {
            $rootWithSeparator = $directoryRoot.TrimEnd([char[]]"\") + "\"
            foreach ($actualFile in @(Get-ChildItem -LiteralPath $directoryRoot -Recurse -File -Force)) {
                $relative = $actualFile.FullName.Substring($rootWithSeparator.Length).Replace('\', '/')
                $key = "$label/$relative"
                if (-not $expected.ContainsKey($key)) { [void]$unexpected.Add($key) }
            }
        }
    }
    return [pscustomobject]@{
        Expected   = $expectedFiles
        Verified   = $verifiedFiles
        Missing    = @($missing)
        Mismatches = @($mismatches)
        Unexpected = @($unexpected)
    }
}

function Get-ManifestIntegrity {
    param($Manifest, [string]$BackupRoot)
    $errors = @()
    $artifacts = @()
    $dumpRelative = [string]$Manifest.database.dumpFile
    Assert-RelativePathSafe -RelativePath $dumpRelative -Label "database dump path"
    $dumpPath = Resolve-RelativeFile -Root $BackupRoot -RelativePath $dumpRelative
    if (-not (Test-Path -LiteralPath $dumpPath -PathType Leaf)) {
        $errors += "missing database dump: $dumpRelative"
    } else {
        if ([long](Get-Item -LiteralPath $dumpPath).Length -ne [long]$Manifest.database.sizeBytes) {
            $errors += "database dump size does not match the manifest: $dumpRelative"
        }
        if ((Get-Sha256 -Path $dumpPath) -ne [string]$Manifest.database.sha256) {
            $errors += "database dump sha256 does not match the manifest: $dumpRelative"
        }
        $artifacts += $dumpRelative
    }
    foreach ($directory in @($Manifest.directories)) {
        if ([string]$directory.status -ne "archived") { continue }
        $archiveRelative = [string]$directory.archive
        Assert-RelativePathSafe -RelativePath $archiveRelative -Label "directory archive path"
        $archivePath = Resolve-RelativeFile -Root $BackupRoot -RelativePath $archiveRelative
        if (-not (Test-Path -LiteralPath $archivePath -PathType Leaf)) {
            $errors += "missing archive: $archiveRelative"
            continue
        }
        if ([long](Get-Item -LiteralPath $archivePath).Length -ne [long]$directory.sizeBytes) {
            $errors += "archive size does not match the manifest: $archiveRelative"
        }
        if ((Get-Sha256 -Path $archivePath) -ne [string]$directory.sha256) {
            $errors += "archive sha256 does not match the manifest: $archiveRelative"
        }
        $members = @(& $script:tarExe -tf $archivePath 2>$null)
        if ($LASTEXITCODE -ne 0) {
            $errors += "cannot list archive: $archiveRelative"
        } else {
            foreach ($member in $members) {
                $normalized = ([string]$member).Replace('\', '/')
                if ($normalized.StartsWith("/") -or $normalized -match '(^|/)\.\.(/|$)') {
                    $errors += "archive member escapes its target: $member in $archiveRelative"
                }
            }
        }
        $artifacts += $archiveRelative
        foreach ($file in @($directory.files)) {
            if ($null -eq $file) { continue }
            Assert-RelativePathSafe -RelativePath ([string]$file.path) -Label "file path"
        }
    }
    return [pscustomobject]@{ Errors = @($errors); Artifacts = @($artifacts) }
}

function Compare-RowCounts {
    param($ManifestCounts, $RestoredCounts)
    $mismatches = New-Object System.Collections.ArrayList
    $checked = 0
    foreach ($property in $ManifestCounts.PSObject.Properties) {
        $checked++
        $name = [string]$property.Name
        $expected = [long]$property.Value
        if (-not $RestoredCounts.Contains($name)) {
            [void]$mismatches.Add("$name missing after restore")
            continue
        }
        $actual = [long]$RestoredCounts[$name]
        if ($actual -ne $expected) {
            [void]$mismatches.Add("$name expected $expected got $actual")
        }
    }
    $extraTables = @($RestoredCounts.Keys | Where-Object { $null -eq $ManifestCounts.PSObject.Properties[$_] })
    foreach ($table in $extraTables) {
        [void]$mismatches.Add("$table is not part of the backup (extra tables after restore)")
    }
    return [pscustomobject]@{ Checked = $checked; Mismatches = @($mismatches) }
}

function Resolve-RepoPath {
    param([string]$Path)
    if ([System.IO.Path]::IsPathRooted($Path)) { return [System.IO.Path]::GetFullPath($Path) }
    return [System.IO.Path]::GetFullPath((Join-Path $script:repoRoot $Path))
}

# --------------------------------------------------------------------------
# Main
# --------------------------------------------------------------------------

try {
$backupRoot = Resolve-RepoPath -Path $BackupDir
if ([string]::IsNullOrWhiteSpace($EnvPath)) { $EnvPath = Join-Path $script:repoRoot ".env" }
$targetRootFull = Resolve-RepoPath -Path $TargetRoot
if ([string]::IsNullOrWhiteSpace($ReportPath)) {
    if ($VerifyOnly) {
        $ReportPath = Join-Path $backupRoot "verify-report.json"
    } else {
        $ReportPath = Join-Path $targetRootFull "restore-report.json"
    }
}
$reportFull = Resolve-RepoPath -Path $ReportPath
$targetDb = $TargetDatabase.Trim()

$script:pgRestoreExe = Resolve-ToolPath -LocalPath (Join-Path $script:repoRoot ".tools\pgsql\bin\pg_restore.exe") -CommandName "pg_restore.exe"
$script:psqlExe = Resolve-ToolPath -LocalPath (Join-Path $script:repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql.exe"
$script:tarExe = Resolve-ToolPath -LocalPath "" -CommandName "tar.exe"
foreach ($tool in @($script:pgRestoreExe, $script:psqlExe, $script:tarExe)) {
    if ([string]::IsNullOrWhiteSpace($tool)) { throw "Required tool missing (pg_restore.exe, psql.exe, tar.exe)" }
}

# The secondary confirmation is deliberately an explicit switch: this command
# runs in non-interactive acceptance/CI contexts and must never block on stdin.
if ($ConfirmOverwrite -and -not $Force) { throw "-ConfirmOverwrite requires -Force" }
if ($Force -and -not $ConfirmOverwrite) {
    throw "-Force requires the second confirmation -ConfirmOverwrite (restores are not interactive)"
}

$manifestPath = Join-Path $backupRoot "manifest.json"
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    throw "manifest.json not found in $backupRoot"
}
$manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
if ([int]$manifest.formatVersion -ne 1) { throw "unsupported manifest formatVersion $($manifest.formatVersion)" }

Write-Step "Validating backup manifest and integrity"
$integrity = Get-ManifestIntegrity -Manifest $manifest -BackupRoot $backupRoot
if ($integrity.Errors.Count -gt 0) {
    throw ("backup integrity check failed: " + ($integrity.Errors -join "; "))
}
Write-Note "verified $($integrity.Artifacts.Count) artifact hash(es) before touching the target"

# Database-name safety. The defaults allow only throwaway databases.
if ($targetDb -notmatch '^[A-Za-z0-9_]+$') {
    throw "TargetDatabase may only contain letters, digits and underscores: $targetDb"
}
if ($targetDb -in @("postgres", "template0", "template1")) {
    throw "refusing to restore into the PostgreSQL system database '$targetDb'"
}
$envValues = Read-DotEnv -Path $EnvPath
$sourceConn = Parse-PostgresUrl -Url ([string]$envValues["EZTEST_DATABASE_URL"])
$testDatabaseName = ""
if (-not [string]::IsNullOrWhiteSpace([string]$envValues["EZTEST_TEST_DATABASE_URL"])) {
    try { $testDatabaseName = (Parse-PostgresUrl -Url ([string]$envValues["EZTEST_TEST_DATABASE_URL"])).Database } catch { $testDatabaseName = "" }
}
if (-not $VerifyOnly -and -not $AllowAnyDatabase) {
    if ($targetDb -notmatch '_(test|e2e)$') {
        throw "restore targets must end in _test or _e2e (use -AllowAnyDatabase for an explicit override): $targetDb"
    }
    if ($targetDb -eq $sourceConn.Database) {
        throw "refusing to restore over the configured source database '$targetDb' (use -AllowAnyDatabase for an explicit override)"
    }
    if (-not [string]::IsNullOrWhiteSpace($testDatabaseName) -and $targetDb -eq $testDatabaseName) {
        throw "refusing to restore over the shared test database '$targetDb' (use -AllowAnyDatabase for an explicit override)"
    }
}

if (-not [string]::IsNullOrWhiteSpace($TargetDatabaseUrl)) {
    $targetConn = Parse-PostgresUrl -Url $TargetDatabaseUrl
    if ($targetConn.Database -ne $targetDb) {
        throw "TargetDatabaseUrl points at '$($targetConn.Database)' but -TargetDatabase is '$targetDb'"
    }
} else {
    $targetConn = [pscustomobject]@{
        Host     = $sourceConn.Host
        Port     = $sourceConn.Port
        User     = $sourceConn.User
        Password = $sourceConn.Password
        Database = $targetDb
    }
}
$script:target = $targetConn
} catch {
    Write-Host ""
    Write-Host "RESTORE FAILED: $($_.Exception.Message)" -ForegroundColor Red
    exit 1
}

$warnings = New-Object System.Collections.ArrayList
$startedAt = Get-Date
$watch = [System.Diagnostics.Stopwatch]::StartNew()
$previousPgPassword = $env:PGPASSWORD
$env:PGPASSWORD = $targetConn.Password

try {
    if ($VerifyOnly) {
        Write-Step "Verify-only: checking restored files against the manifest"
        $fileResult = Get-FileVerification -Manifest $manifest -Root $targetRootFull
        Write-Note "expected $($fileResult.Expected) file(s), verified $($fileResult.Verified), missing $($fileResult.Missing.Count), sha256 mismatches $($fileResult.Mismatches.Count), unexpected $($fileResult.Unexpected.Count)"
        $watch.Stop()
        $passed = ($fileResult.Missing.Count -eq 0 -and $fileResult.Mismatches.Count -eq 0)
        $report = [ordered]@{
            formatVersion   = 1
            tool            = "scripts/restore.ps1"
            mode            = "verify-only"
            verifiedAt      = (Get-Date).ToUniversalTime().ToString("o")
            durationSeconds = [math]::Round($watch.Elapsed.TotalSeconds, 2)
            backupDir       = $backupRoot
            backupCreatedAt = [string]$manifest.createdAt
            manifestCommit  = [string]$manifest.repository.commit
            target          = [ordered]@{ database = $targetDb; root = $targetRootFull }
            database        = [ordered]@{ restored = $false; skipped = $true; rowCountsMatch = $null }
            files           = [ordered]@{
                expected   = $fileResult.Expected
                verified   = $fileResult.Verified
                missing    = @($fileResult.Missing)
                mismatches = @($fileResult.Mismatches)
                unexpected = @($fileResult.Unexpected)
            }
            warnings        = @($warnings)
            secrets         = [ordered]@{
                included     = $false
                requiredKeys = @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE")
                note         = "Secrets are not part of the backup. Supply the same EZTEST_SECRET_KEYS/EZTEST_SECRET_KEY_ACTIVE separately; encrypted LLM configuration is unrecoverable without them."
            }
            passed          = [bool]$passed
        }
        Write-Utf8File -Path $reportFull -Content ((ConvertTo-Json -InputObject $report -Depth 12) + "`n")
        Write-Host ""
        if ($passed) {
            Write-Host "VERIFY PASSED: $($fileResult.Verified)/$($fileResult.Expected) file(s) match the manifest." -ForegroundColor Green
            exit 0
        }
        Write-Host "VERIFY FAILED: missing $($fileResult.Missing.Count), sha256 mismatches $($fileResult.Mismatches.Count)." -ForegroundColor Red
        foreach ($message in $fileResult.Missing) { Write-Host "  missing: $message" -ForegroundColor Red }
        foreach ($message in $fileResult.Mismatches) { Write-Host "  mismatch: $message" -ForegroundColor Red }
        exit 1
    }

    Write-Step "Checking the target before restoring"
    if (Test-Path -LiteralPath $targetRootFull -PathType Leaf) {
        throw "TargetRoot is a file, not a directory: $targetRootFull"
    }
    if (Test-Path -LiteralPath $targetRootFull -PathType Container) {
        $existing = @(Get-ChildItem -LiteralPath $targetRootFull -Force)
        if ($existing.Count -gt 0 -and -not ($Force -and $ConfirmOverwrite)) {
            throw "TargetRoot must be empty (it contains $($existing.Count) item(s)); pass -Force -ConfirmOverwrite to overwrite: $targetRootFull"
        }
        if ($existing.Count -gt 0) {
            Write-Note "target directory is not empty; -Force -ConfirmOverwrite received, existing files are kept and manifest files are overwritten"
            [void]$warnings.Add("target directory was not empty; existing files were kept and manifest files were overwritten")
        }
    } elseif (-not (Test-Path -LiteralPath $targetRootFull)) {
        New-Item -ItemType Directory -Force -Path $targetRootFull | Out-Null
    }

    $dbExists = Get-PsqlScalar -Database "postgres" -Operation "checking target database existence" `
        -Sql "SELECT count(*) FROM pg_database WHERE datname = '$targetDb';"
    if ($dbExists -eq "0") {
        throw "target database '$targetDb' does not exist; create an empty database first, e.g.: createdb -h $($targetConn.Host) -p $($targetConn.Port) -U $($targetConn.User) $targetDb"
    }
    $userObjects = Test-UserObjects -Database $targetDb
    if ([long]$userObjects -gt 0 -and -not ($Force -and $ConfirmOverwrite)) {
        throw "target database '$targetDb' is not empty ($userObjects user object(s)); pass -Force -ConfirmOverwrite to overwrite"
    }
    if ([long]$userObjects -gt 0) {
        Write-Note "target database is not empty; -Force -ConfirmOverwrite received, pg_restore --clean will drop objects present in the dump"
        [void]$warnings.Add("target database was not empty; pg_restore --clean --if-exists was used")
    }

    Write-Step "Restoring database $targetDb"
    $dumpPath = Resolve-RelativeFile -Root $backupRoot -RelativePath ([string]$manifest.database.dumpFile)
    $restoreArgs = @(
        "-h", $targetConn.Host, "-p", [string]$targetConn.Port, "-U", $targetConn.User,
        "-d", $targetDb, "--no-owner", "--no-privileges", "--exit-on-error"
    )
    if ($Force -and $ConfirmOverwrite) { $restoreArgs += @("--clean", "--if-exists") }
    $restoreArgs += $dumpPath
    $dbWatch = [System.Diagnostics.Stopwatch]::StartNew()
    & $script:pgRestoreExe @restoreArgs
    $restoreCode = $LASTEXITCODE
    $dbWatch.Stop()
    if ($restoreCode -ne 0) { throw "pg_restore failed (exit $restoreCode)" }
    Write-Note ("database restored in {0:N1}s" -f $dbWatch.Elapsed.TotalSeconds)

    $restoredCounts = Get-RowCounts -Database $targetDb
    $rowCompare = Compare-RowCounts -ManifestCounts $manifest.database.rowCounts -RestoredCounts $restoredCounts
    $rowCountsMatch = ($rowCompare.Mismatches.Count -eq 0)
    Write-Note "row counts: $($rowCompare.Checked) table(s) compared, $($rowCompare.Mismatches.Count) mismatch(es)"
    if (-not [bool]$manifest.database.rowCountsStable) {
        [void]$warnings.Add("the backup recorded rowCountsStable=false (database changed during backup); row-count comparison is advisory")
    }
    foreach ($mismatch in $rowCompare.Mismatches) { Write-Note "mismatch: $mismatch" }

    Write-Step "Restoring content directories"
    $restoredArchives = 0
    foreach ($directory in @($manifest.directories)) {
        $label = [string]$directory.label
        if ([string]$directory.status -ne "archived") {
            New-Item -ItemType Directory -Force -Path (Join-Path $targetRootFull $label) | Out-Null
            [void]$warnings.Add("directory '$label' was missing at backup time; an empty target directory was created")
            Write-Note "$label missing at backup time; created empty target directory"
            continue
        }
        $archivePath = Resolve-RelativeFile -Root $backupRoot -RelativePath ([string]$directory.archive)
        $directoryRoot = Join-Path $targetRootFull $label
        New-Item -ItemType Directory -Force -Path $directoryRoot | Out-Null
        $tarWatch = [System.Diagnostics.Stopwatch]::StartNew()
        & $script:tarExe -xf $archivePath -C $directoryRoot
        $tarCode = $LASTEXITCODE
        $tarWatch.Stop()
        if ($tarCode -ne 0) { throw "tar extraction failed for '$label' (exit $tarCode)" }
        $restoredArchives++
        Write-Note ("{0,-20} extracted {1:N1}s" -f $label, $tarWatch.Elapsed.TotalSeconds)
    }

    Write-Step "Verifying restored files against the manifest"
    $fileResult = Get-FileVerification -Manifest $manifest -Root $targetRootFull
    Write-Note "expected $($fileResult.Expected) file(s), verified $($fileResult.Verified), missing $($fileResult.Missing.Count), sha256 mismatches $($fileResult.Mismatches.Count), unexpected $($fileResult.Unexpected.Count)"
    foreach ($message in $fileResult.Missing) { Write-Note "missing: $message" }
    foreach ($message in $fileResult.Mismatches) { Write-Note "mismatch: $message" }
    if ($fileResult.Unexpected.Count -gt 0) {
        [void]$warnings.Add("$($fileResult.Unexpected.Count) unexpected file(s) exist in the target but are not part of the backup")
    }

    $passed = ($fileResult.Missing.Count -eq 0 -and $fileResult.Mismatches.Count -eq 0 -and $rowCountsMatch)
    $watch.Stop()
    $report = [ordered]@{
        formatVersion   = 1
        tool            = "scripts/restore.ps1"
        mode            = "restore"
        verifiedAt      = (Get-Date).ToUniversalTime().ToString("o")
        durationSeconds = [math]::Round($watch.Elapsed.TotalSeconds, 2)
        backupDir       = $backupRoot
        backupCreatedAt = [string]$manifest.createdAt
        manifestCommit  = [string]$manifest.repository.commit
        target          = [ordered]@{ database = $targetDb; root = $targetRootFull }
        database        = [ordered]@{
            restored       = $true
            tablesCompared = $rowCompare.Checked
            rowCountsMatch = [bool]$rowCountsMatch
            mismatches     = @($rowCompare.Mismatches)
        }
        directories     = [ordered]@{ restoredArchives = $restoredArchives }
        files           = [ordered]@{
            expected   = $fileResult.Expected
            verified   = $fileResult.Verified
            missing    = @($fileResult.Missing)
            mismatches = @($fileResult.Mismatches)
            unexpected = @($fileResult.Unexpected)
        }
        warnings        = @($warnings)
        secrets         = [ordered]@{
            included     = $false
            requiredKeys = @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE")
            note         = "Secrets are not part of the backup. Supply the same EZTEST_SECRET_KEYS/EZTEST_SECRET_KEY_ACTIVE separately; encrypted LLM configuration is unrecoverable without them."
        }
        passed          = [bool]$passed
    }
    Write-Utf8File -Path $reportFull -Content ((ConvertTo-Json -InputObject $report -Depth 12) + "`n")

    Write-Host ""
    if ($passed) {
        Write-Host "RESTORE PASSED" -ForegroundColor Green
    } else {
        Write-Host "RESTORE FAILED: verification did not pass." -ForegroundColor Red
    }
    Write-Note "database    $targetDb ($($rowCompare.Checked) table(s) compared, row counts match: $rowCountsMatch)"
    Write-Note "files       $($fileResult.Verified)/$($fileResult.Expected) verified, $($fileResult.Missing.Count) missing, $($fileResult.Mismatches.Count) sha256 mismatch(es)"
    Write-Note "report      $reportFull"
    Write-Note "secrets     not included; supply EZTEST_SECRET_KEYS/EZTEST_SECRET_KEY_ACTIVE separately (encrypted LLM keys are unrecoverable without them)"
    if ($warnings.Count -gt 0) {
        foreach ($warning in $warnings) { Write-Note "warning: $warning" }
    }
    if (-not $passed) { exit 1 }
    exit 0
} catch {
    Write-Host ""
    Write-Host "RESTORE FAILED: $($_.Exception.Message)" -ForegroundColor Red
    Write-Host "Target database '$targetDb' / directory '$targetRootFull' was not reported as verified." -ForegroundColor Red
    exit 1
} finally {
    if ($null -eq $previousPgPassword) {
        Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    } else {
        $env:PGPASSWORD = $previousPgPassword
    }
}
