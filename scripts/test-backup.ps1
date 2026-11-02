#requires -Version 5.1
<#
.SYNOPSIS
    Local backup/restore drill for EzTest (P8 Task 29).

.DESCRIPTION
    Exercises scripts/backup.ps1 and scripts/restore.ps1 end to end against the
    local PostgreSQL from .env:

      1. creates deterministic fixture files under .tmp/p8-backup/fixture
         (evidence/report/export/attachment/object directories, plus .env and
         .secrets decoys that must never be archived);
      2. backs up the configured EZTEST_DATABASE_URL database and the fixture
         directories into .tmp/p8-backup/backup;
      3. asserts manifest.json lists the custom-format dump, every archive, all
         fixture files with size + sha256, the config key names, the resolved
         content base, and the git version/commit, and that no .env or secret
         value leaked into the backup;
      4. asserts the dangerous branches are refused: unsafe database name,
         source database and shared test database (even with -AllowAnyDatabase),
         system database, non-empty target database/directory, a non-empty
         backup target and -Force without the second confirmation;
      5. rejects a manifest whose directory label escapes the target (path
         traversal) and a manifest whose dump hash was tampered with;
      6. restores the database into a fresh `*_test` database and the
         directories into a fresh target, then compares row counts and per-file
         sha256, and re-runs the restore with -Force -ConfirmOverwrite to cover
         the overwrite round-trip and the "extra tables stay behind" semantics;
      7. restores a manifest marked rowCountsStable=false and asserts the
         unstable row counts are advisory (warning, not a failed restore);
      8. exercises the real default layout: relative EZTEST_*_DIR values are
         resolved against the API working directory backend/, so files seeded
         in backend/.data/{evidence,reports,exports,version-attachments,objects}
         must be backed up (non-zero files) and restore with a passing report;
      9. tampers with one restored file and asserts -VerifyOnly detects it.

    The temporary databases and the restored/fixture directories are removed
    afterwards; the main and default-layout backups plus test-report.json are
    kept as evidence. Exits non-zero when any assertion fails. Passwords are
    read from .env and are never printed.

    Application-level verification (starting the API against the restored
    database) is NOT part of this drill: it is deferred to Task 35 because the
    parallel P8 tasks' backend working tree may not build. See
    docs/acceptance/p8/backup-drill.md ("未做应用级验证").

.PARAMETER WorkDir
    Scratch directory, default .tmp/p8-backup. It must live under .tmp (but not
    be .tmp itself) so the drill can clean it up safely.

.PARAMETER Keep
    Keep the temporary databases, restored files and fixture tree for debugging.

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test-backup.ps1
#>
[CmdletBinding()]
param(
    [string]$WorkDir = "",
    [switch]$Keep
)

$ErrorActionPreference = "Stop"

$script:repoRoot = Split-Path -Parent $PSScriptRoot
$script:failures = New-Object System.Collections.ArrayList
$script:passes = New-Object System.Collections.ArrayList
$script:createdDatabases = New-Object System.Collections.ArrayList
$script:seededPaths = New-Object System.Collections.ArrayList
$script:seedRoots = New-Object System.Collections.ArrayList
$script:createdDataRoots = New-Object System.Collections.ArrayList
$script:createdBackendData = $false
$script:seedDirName = ""
$script:commandIndex = 0
$script:workReady = $false
$script:backupSeconds = $null
$script:restoreSeconds = $null
$script:workDir = ""
$script:backupDir = ""
$script:tempDatabase = ""
$script:startedAt = (Get-Date)
$script:defaultLayoutSummary = $null
$script:apiSmokeDeferred = $true

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Write-Note {
    param([string]$Message)
    Write-Host "    $Message" -ForegroundColor DarkGray
}

function Add-Pass {
    param([string]$Message)
    [void]$script:passes.Add($Message)
    Write-Host "PASS  $Message" -ForegroundColor Green
}

function Add-Failure {
    param([string]$Message)
    [void]$script:failures.Add($Message)
    Write-Host "FAIL  $Message" -ForegroundColor Red
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if ($Condition) { Add-Pass $Message } else { Add-Failure $Message }
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
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        throw "EZTEST_DATABASE_URL must use the postgres scheme"
    }
    $separator = $uri.UserInfo.IndexOf(":")
    if ($separator -lt 0) { throw "EZTEST_DATABASE_URL must include user:password" }
    $database = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($database) -or $database.Contains("/")) {
        throw "EZTEST_DATABASE_URL must include a single database name"
    }
    return [pscustomobject]@{
        Host     = $uri.Host
        Port     = $(if ($uri.Port -gt 0) { $uri.Port } else { 5432 })
        User     = [System.Uri]::UnescapeDataString($uri.UserInfo.Substring(0, $separator))
        Password = [System.Uri]::UnescapeDataString($uri.UserInfo.Substring($separator + 1))
        Database = $database
    }
}

function Write-Utf8File {
    param([string]$Path, [string]$Content)
    $parent = Split-Path -Parent $Path
    if (-not [string]::IsNullOrWhiteSpace($parent)) {
        New-Item -ItemType Directory -Force -Path $parent | Out-Null
    }
    [System.IO.File]::WriteAllText($Path, $Content, (New-Object System.Text.UTF8Encoding($false)))
}

function Get-Sha256 {
    param([string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Invoke-PsqlRows {
    param([string]$Database, [string]$Sql)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        $output = & $script:psqlExe -h $script:conn.Host -p $script:conn.Port -U $script:conn.User `
            -d $Database -v ON_ERROR_STOP=1 -tA -F '|' -c $Sql 2>$null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "psql query failed on database $Database (exit $code)" }
    return @($output)
}

function Get-PsqlScalar {
    param([string]$Database, [string]$Sql)
    $rows = @(Invoke-PsqlRows -Database $Database -Sql $Sql)
    return (($rows | Out-String).Trim())
}

function Get-RowCounts {
    param([string]$Database)
    $tables = @(Invoke-PsqlRows -Database $Database -Sql "SELECT relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND c.relkind = 'r' ORDER BY relname;")
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
    foreach ($row in @(Invoke-PsqlRows -Database $Database -Sql $query)) {
        $line = [string]$row
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line.Split("|")
        if ($parts.Length -ne 2) { throw "unexpected row-count output line: $line" }
        $counts[$parts[0]] = [long]$parts[1]
    }
    return $counts
}

function Invoke-EzScript {
    param([string]$Label, [string]$Path, [hashtable]$Parameters)
    $argList = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $Path)
    foreach ($key in $Parameters.Keys) {
        $value = $Parameters[$key]
        if ($value -is [bool]) {
            if ($value) { $argList += "-$key" }
        } elseif ($null -ne $value -and -not [string]::IsNullOrWhiteSpace([string]$value)) {
            $argList += @("-$key", [string]$value)
        }
    }
    $script:commandIndex++
    $logPath = Join-Path $script:workDir ("command-{0:d2}-{1}.log" -f $script:commandIndex, $Label)
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & $script:powershellExe @argList *> $logPath
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorAction
    }
    $output = ""
    if (Test-Path -LiteralPath $logPath) {
        $output = [System.IO.File]::ReadAllText($logPath)
        foreach ($line in ($output -split "`r?`n")) {
            if (-not [string]::IsNullOrWhiteSpace($line)) { Write-Host "    | $line" -ForegroundColor DarkGray }
        }
    }
    return [pscustomobject]@{ ExitCode = $code; Output = $output; LogPath = $logPath }
}

function Get-DirectoryEntry {
    param($Manifest, [string]$Label)
    foreach ($entry in @($Manifest.directories)) {
        if ($null -ne $entry -and $entry.label -eq $Label) { return $entry }
    }
    return $null
}

function Get-ManifestFileList {
    param($Manifest)
    $items = New-Object System.Collections.ArrayList
    foreach ($directory in @($Manifest.directories)) {
        foreach ($file in @($directory.files)) {
            if ($null -eq $file) { continue }
            [void]$items.Add([pscustomobject]@{
                    Label    = [string]$directory.label
                    Relative = [string]$file.path
                    Size     = [long]$file.sizeBytes
                    Sha      = [string]$file.sha256
                })
        }
    }
    return $items.ToArray()
}

function Find-SecretLeak {
    param([string]$Root, [string[]]$Secrets)
    if ($Secrets.Count -eq 0) { return $null }
    $latin1 = [System.Text.Encoding]::GetEncoding(28591)
    foreach ($file in @(Get-ChildItem -LiteralPath $Root -Recurse -File -Force)) {
        $bytes = [System.IO.File]::ReadAllBytes($file.FullName)
        $text = $latin1.GetString($bytes)
        foreach ($secret in $Secrets) {
            if ($secret.Length -ge 8 -and $text.Contains($secret)) {
                return $file.FullName
            }
        }
    }
    return $null
}

function New-FixtureTree {
    param([string]$Root)
    $evidence = Join-Path $Root "evidence"
    $reports = Join-Path $Root "reports"
    $exports = Join-Path $Root "exports"
    $attachments = Join-Path $Root "version-attachments"
    $objects = Join-Path $Root "objects"
    foreach ($directory in @($evidence, $reports, $exports, $attachments, $objects)) {
        New-Item -ItemType Directory -Force -Path $directory | Out-Null
    }
    Write-Utf8File -Path (Join-Path $evidence "rounds\run-1\evidence.txt") -Content "evidence-sentinel-2026-10-29`n"
    Write-Utf8File -Path (Join-Path $reports "report-001.md") -Content "# Drill report`nrow counts and hashes are compared.`n"
    Write-Utf8File -Path (Join-Path $exports "export-001.csv") -Content "id,name`n1,alpha`n2,beta`n"
    Write-Utf8File -Path (Join-Path $attachments "attachment-001.txt") -Content "attachment-sentinel`n"
    $objectBytes = New-Object byte[] 256
    for ($i = 0; $i -lt 256; $i++) { $objectBytes[$i] = [byte]$i }
    New-Item -ItemType Directory -Force -Path (Join-Path $objects "sha256\ab") | Out-Null
    [System.IO.File]::WriteAllBytes((Join-Path $objects "sha256\ab\object-001.bin"), $objectBytes)
    New-Item -ItemType Directory -Force -Path (Join-Path $evidence "empty-subdir") | Out-Null
    # Decoys: these must be excluded from both the archives and the manifest.
    Write-Utf8File -Path (Join-Path $evidence ".env") -Content "EZTEST_DECOY_SECRET=should-not-be-backed-up`n"
    Write-Utf8File -Path (Join-Path $evidence ".secrets\hidden.txt") -Content "hidden-secret-value`n"
    Write-Utf8File -Path (Join-Path $objects ".secrets\hidden.txt") -Content "hidden-secret-value`n"
}

function New-TempDatabase {
    param([string]$Name)
    & $script:createdbExe -h $script:conn.Host -p $script:conn.Port -U $script:conn.User $Name
    if ($LASTEXITCODE -ne 0) { throw "createdb $Name failed (exit $LASTEXITCODE)" }
    [void]$script:createdDatabases.Add($Name)
    Add-Pass "created empty temporary database $Name"
}

function Remove-TempDatabase {
    param([string]$Name)
    if (-not $script:createdDatabases.Contains($Name)) { return }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $script:psqlExe -h $script:conn.Host -p $script:conn.Port -U $script:conn.User -d "postgres" -q `
            -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$Name' AND pid <> pg_backend_pid();" *> $null
        & $script:dropdbExe -h $script:conn.Host -p $script:conn.Port -U $script:conn.User --if-exists $Name *> $null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "dropping temporary database $Name failed (exit $code)" }
    [void]$script:createdDatabases.Remove($Name)
}

function Add-DataSeed {
    param([string]$DataRoot, [string]$Label, [string]$Relative, [string]$Content)
    $labelRoot = Join-Path $DataRoot $Label
    if (-not (Test-Path -LiteralPath $labelRoot -PathType Container)) {
        New-Item -ItemType Directory -Force -Path $labelRoot | Out-Null
        [void]$script:createdDataRoots.Add($labelRoot)
    }
    $seedRoot = Join-Path $labelRoot $script:seedDirName
    if (-not $script:seedRoots.Contains($seedRoot)) {
        New-Item -ItemType Directory -Force -Path $seedRoot | Out-Null
        [void]$script:seedRoots.Add($seedRoot)
    }
    $path = Join-Path $seedRoot ($Relative -replace '/', '\')
    Write-Utf8File -Path $path -Content $Content
    [void]$script:seededPaths.Add($path)
    return $path
}

function Remove-DataSeeds {
    foreach ($path in @($script:seededPaths)) {
        if (Test-Path -LiteralPath $path) {
            Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
        }
    }
    foreach ($root in @($script:seedRoots)) {
        if (Test-Path -LiteralPath $root) {
            Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
    foreach ($root in @($script:createdDataRoots)) {
        if (Test-Path -LiteralPath $root) {
            $remaining = @(Get-ChildItem -LiteralPath $root -Force)
            if ($remaining.Count -eq 0) { Remove-Item -LiteralPath $root -Force -ErrorAction SilentlyContinue }
        }
    }
    if ($script:createdBackendData) {
        $backendData = Join-Path $script:repoRoot "backend\.data"
        if (Test-Path -LiteralPath $backendData) {
            $remaining = @(Get-ChildItem -LiteralPath $backendData -Force)
            if ($remaining.Count -eq 0) { Remove-Item -LiteralPath $backendData -Force -ErrorAction SilentlyContinue }
        }
    }
}

function Write-DrillReport {
    param([string]$Path)
    $finished = Get-Date
    $manifestSummary = $null
    if ($null -ne $manifest) {
        $totalRows = [long]0
        $tableCount = 0
        foreach ($property in $manifest.database.rowCounts.PSObject.Properties) {
            $tableCount++
            $totalRows += [long]$property.Value
        }
        $manifestFiles = 0
        foreach ($directory in @($manifest.directories)) { $manifestFiles += @($directory.files).Count }
        $manifestSummary = [ordered]@{
            commit          = [string]$manifest.repository.commit
            branch          = [string]$manifest.repository.branch
            version         = [string]$manifest.repository.version
            database        = [string]$manifest.database.name
            dumpBytes       = [long]$manifest.database.sizeBytes
            tables          = $tableCount
            totalRows       = $totalRows
            directories     = @($manifest.directories).Count
            files           = $manifestFiles
            rowCountsStable = [bool]$manifest.database.rowCountsStable
        }
    }
    $payload = [ordered]@{
        tool            = "scripts/test-backup.ps1"
        startedAt       = $script:startedAt.ToString("o")
        finishedAt      = $finished.ToString("o")
        durationSeconds = [math]::Round(($finished - $script:startedAt).TotalSeconds, 2)
        workDir         = $script:workDir
        backupDir       = $script:backupDir
        tempDatabase    = $script:tempDatabase
        keepArtifacts   = [bool]$Keep
        backupSeconds   = $script:backupSeconds
        restoreSeconds  = $script:restoreSeconds
        manifest        = $manifestSummary
        defaultLayout   = $script:defaultLayoutSummary
        applicationSmoke = [ordered]@{
            performed  = (-not $script:apiSmokeDeferred)
            deferredTo = $(if ($script:apiSmokeDeferred) { "Task 35" } else { $null })
            note       = $(if ($script:apiSmokeDeferred) { "Not performed: starting the API against the restored database is deferred to Task 35 (parallel P8 backend working tree may not build); restore verification is SQL/file-level only." } else { "" })
        }
        assertions      = [ordered]@{
            passed   = $script:passes.Count
            failed   = $script:failures.Count
            failures = @($script:failures)
        }
        passed          = ($script:failures.Count -eq 0)
    }
    Write-Utf8File -Path $Path -Content ((ConvertTo-Json -InputObject $payload -Depth 8) + "`n")
}

# --------------------------------------------------------------------------
# Main
# --------------------------------------------------------------------------

$backupScript = Join-Path $PSScriptRoot "backup.ps1"
$restoreScript = Join-Path $PSScriptRoot "restore.ps1"

Write-Step "Preflight"
$missing = @()
foreach ($scriptPath in @($backupScript, $restoreScript)) {
    if (-not (Test-Path -LiteralPath $scriptPath)) { $missing += $scriptPath }
}
if ($missing.Count -gt 0) {
    foreach ($scriptPath in $missing) { Add-Failure "missing script $scriptPath" }
    Write-Host ""
    Write-Host "BACKUP DRILL FAILED: required scripts are missing." -ForegroundColor Red
    exit 1
}
Add-Pass "backup.ps1 and restore.ps1 exist"

$envPath = Join-Path $script:repoRoot ".env"
if (-not (Test-Path -LiteralPath $envPath)) {
    Add-Failure "missing .env at $envPath"
    Write-Host "BACKUP DRILL FAILED: .env is required." -ForegroundColor Red
    exit 1
}

$script:dotenv = Read-DotEnv -Path $envPath
$script:conn = Parse-PostgresUrl -Url ([string]$script:dotenv["EZTEST_DATABASE_URL"])

$script:pgDumpExe = Resolve-ToolPath -LocalPath (Join-Path $script:repoRoot ".tools\pgsql\bin\pg_dump.exe") -CommandName "pg_dump.exe"
$script:psqlExe = Resolve-ToolPath -LocalPath (Join-Path $script:repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql.exe"
$script:createdbExe = Resolve-ToolPath -LocalPath (Join-Path $script:repoRoot ".tools\pgsql\bin\createdb.exe") -CommandName "createdb.exe"
$script:dropdbExe = Resolve-ToolPath -LocalPath (Join-Path $script:repoRoot ".tools\pgsql\bin\dropdb.exe") -CommandName "dropdb.exe"
$script:pgIsReadyExe = Resolve-ToolPath -LocalPath (Join-Path $script:repoRoot ".tools\pgsql\bin\pg_isready.exe") -CommandName "pg_isready.exe"
$script:tarExe = Resolve-ToolPath -LocalPath "" -CommandName "tar.exe"
$script:powershellExe = Join-Path $env:SystemRoot "System32\WindowsPowerShell\v1.0\powershell.exe"
if (-not (Test-Path -LiteralPath $script:powershellExe)) { $script:powershellExe = "powershell.exe" }

$toolFailures = 0
foreach ($tool in @($script:pgDumpExe, $script:psqlExe, $script:createdbExe, $script:dropdbExe, $script:pgIsReadyExe, $script:tarExe)) {
    if ([string]::IsNullOrWhiteSpace($tool)) { $toolFailures++ }
}
if ($toolFailures -gt 0) {
    Add-Failure "PostgreSQL client tools and tar.exe are required (.tools/pgsql/bin or PATH)"
    Write-Host "BACKUP DRILL FAILED: missing tools." -ForegroundColor Red
    exit 1
}

if ([string]::IsNullOrWhiteSpace($WorkDir)) {
    $script:workDir = Join-Path $script:repoRoot ".tmp\p8-backup"
} elseif ([System.IO.Path]::IsPathRooted($WorkDir)) {
    $script:workDir = [System.IO.Path]::GetFullPath($WorkDir)
} else {
    $script:workDir = [System.IO.Path]::GetFullPath((Join-Path $script:repoRoot $WorkDir))
}
$tmpRoot = [System.IO.Path]::GetFullPath((Join-Path $script:repoRoot ".tmp"))
$tmpRootPrefix = $tmpRoot.TrimEnd([char[]]"\") + "\"
if ($script:workDir.Equals($tmpRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
    -not $script:workDir.StartsWith($tmpRootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    Write-Host "FAIL  WorkDir must live below $tmpRootPrefix (not .tmp itself); got $($script:workDir)" -ForegroundColor Red
    exit 1
}

$script:backupDir = Join-Path $script:workDir "backup"
$fixtureRoot = Join-Path $script:workDir "fixture"
$restoreRoot = Join-Path $script:workDir "restore"
$nonEmptyTarget = Join-Path $script:workDir "nonempty-target"
$emptyTarget = Join-Path $script:workDir "empty-target"
$restoreReportPath = Join-Path $script:workDir "restore-report.json"
$overwriteReportPath = Join-Path $script:workDir "overwrite-report.json"
$verifyReportPath = Join-Path $script:workDir "verify-report.json"
$drillReportPath = Join-Path $script:workDir "test-report.json"
$nonEmptyDbTarget = Join-Path $script:workDir "nonempty-db-target"
$missingDbTarget = Join-Path $script:workDir "missing-db-target"
$nonEmptyBackupTarget = Join-Path $script:workDir "nonempty-backup-target"
$missingDirBackup = Join-Path $script:workDir "missing-dir-backup"
$allowMissingBackup = Join-Path $script:workDir "allow-missing-backup"
$sharedDbTarget = Join-Path $script:workDir "shared-db-target"
$sourceDbTarget = Join-Path $script:workDir "source-db-target"
$systemDbTarget = Join-Path $script:workDir "system-db-target"
$tamperedBackup = Join-Path $script:workDir "backup-tampered"
$tamperedTarget = Join-Path $script:workDir "tampered-target"
$evilBackup = Join-Path $script:workDir "backup-evil-label"
$evilTarget = Join-Path $script:workDir "evil-label-target"
$unstableBackup = Join-Path $script:workDir "backup-unstable"
$unstableRoot = Join-Path $script:workDir "unstable-restore"
$unstableReportPath = Join-Path $script:workDir "unstable-restore-report.json"
$defaultBackupDir = Join-Path $script:workDir "default-layout-backup"
$defaultRestoreRoot = Join-Path $script:workDir "default-layout-restore"
$defaultRestoreReportPath = Join-Path $script:workDir "default-layout-restore-report.json"
$defaultEnvPath = Join-Path $script:workDir "source-url.tmp"
$backendDataRoot = Join-Path $script:repoRoot "backend\.data"
$stamp = (Get-Date).ToString("yyyyMMddHHmmss")
$script:seedDirName = "p8-drill-$stamp"
$script:tempDatabase = "eztest_backup_drill_${stamp}_test"
$evilDatabase = "eztest_backup_drill_evil_${stamp}_test"
$unstableDatabase = "eztest_backup_drill_unstable_${stamp}_test"
$defaultDatabase = "eztest_backup_drill_default_${stamp}_test"
$nonEmptyDatabase = "eztest_backup_drill_nonempty_${stamp}_test"
$missingDatabase = "eztest_backup_drill_missing_${stamp}_test"
$unsafeDatabase = "eztest_backup_drill_unsafe_$stamp"

$previousPgPassword = $env:PGPASSWORD
$env:PGPASSWORD = $script:conn.Password

try {
    Write-Step "Preparing $($script:workDir)"
    if (Test-Path -LiteralPath $script:workDir) {
        Remove-Item -LiteralPath $script:workDir -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $script:workDir | Out-Null
    $script:workReady = $true
    New-FixtureTree -Root $fixtureRoot
    foreach ($directory in @($nonEmptyTarget, $emptyTarget, $nonEmptyDbTarget, $sharedDbTarget, $sourceDbTarget, $systemDbTarget, $nonEmptyBackupTarget)) {
        New-Item -ItemType Directory -Force -Path $directory | Out-Null
    }
    Write-Utf8File -Path (Join-Path $nonEmptyTarget "existing.txt") -Content "do not overwrite`n"
    Write-Utf8File -Path (Join-Path $nonEmptyBackupTarget "existing.txt") -Content "do not overwrite`n"

    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $script:pgIsReadyExe -h $script:conn.Host -p $script:conn.Port *> $null
        $readyExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorAction
    }
    if ($readyExit -ne 0) { throw "PostgreSQL is not reachable at $($script:conn.Host):$($script:conn.Port)" }
    Add-Pass "PostgreSQL is reachable at $($script:conn.Host):$($script:conn.Port)"

    New-TempDatabase -Name $script:tempDatabase

    Write-Step "Backing up database $($script:conn.Database) and fixture directories"
    $backupWatch = [System.Diagnostics.Stopwatch]::StartNew()
    $backupResult = Invoke-EzScript -Label "backup" -Path $backupScript -Parameters @{
        TargetDir     = $script:backupDir
        EvidenceDir   = (Join-Path $fixtureRoot "evidence")
        ReportDir     = (Join-Path $fixtureRoot "reports")
        ExportDir     = (Join-Path $fixtureRoot "exports")
        AttachmentDir = (Join-Path $fixtureRoot "version-attachments")
        ObjectDir     = (Join-Path $fixtureRoot "objects")
    }
    $backupWatch.Stop()
    $script:backupSeconds = [math]::Round($backupWatch.Elapsed.TotalSeconds, 2)
    Assert-True ($backupResult.ExitCode -eq 0) "backup.ps1 exits 0 (wall time $($script:backupSeconds)s)"

    Write-Step "Asserting the backup manifest and payload"
    $manifestPath = Join-Path $script:backupDir "manifest.json"
    Assert-True (Test-Path -LiteralPath $manifestPath) "manifest.json exists"
    $manifest = $null
    if (Test-Path -LiteralPath $manifestPath) {
        try {
            $manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
            Add-Pass "manifest.json parses as JSON"
        } catch {
            Add-Failure "manifest.json does not parse: $($_.Exception.Message)"
        }
    }
    $fixtureExpectedPaths = @{
        evidence            = (Join-Path $fixtureRoot "evidence")
        reports             = (Join-Path $fixtureRoot "reports")
        exports             = (Join-Path $fixtureRoot "exports")
        "version-attachments" = (Join-Path $fixtureRoot "version-attachments")
        objects             = (Join-Path $fixtureRoot "objects")
    }
    if ($null -ne $manifest) {
        Assert-True ([int]$manifest.formatVersion -eq 1) "manifest.formatVersion is 1"
        Assert-True (-not [string]::IsNullOrWhiteSpace([string]$manifest.repository.commit)) "manifest records the git commit"
        Assert-True ([string]$manifest.repository.commit -match '^[0-9a-f]{7,40}$') "manifest git commit looks like a SHA"
        Assert-True (-not [string]::IsNullOrWhiteSpace([string]$manifest.repository.branch)) "manifest records the git branch"

        $expectedApiRoot = [System.IO.Path]::GetFullPath((Join-Path $script:repoRoot "backend"))
        Assert-True ([string]$manifest.contentBase.absolute -eq $expectedApiRoot) "manifest records the absolute content base backend/ (got $($manifest.contentBase.absolute))"
        Assert-True ([string]$manifest.contentBase.relative -eq "backend") "manifest records the repository-relative content base backend"
        $sourceUrlForCheck = [string]$script:dotenv["EZTEST_DATABASE_URL"]
        $expectedUrlParameters = @()
        if ($sourceUrlForCheck.Contains("?")) {
            $queryText = ([System.Uri]$sourceUrlForCheck).Query.TrimStart([char[]]"?")
            $expectedUrlParameters = @($queryText -split "&" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | ForEach-Object {
                    [System.Uri]::UnescapeDataString((($_ -split "=", 2)[0]))
                })
        }
        Assert-True ($null -ne $manifest.database.PSObject.Properties["parameters"]) "manifest records the database URL query parameter names"
        Assert-True ((@($manifest.database.parameters) -join ',') -eq ($expectedUrlParameters -join ',')) "manifest records exactly the database URL query parameter names ($($expectedUrlParameters -join ', '))"

        $expectedKeys = @("EZTEST_DATABASE_URL", "EZTEST_EVIDENCE_DIR", "EZTEST_REPORT_DIR", "EZTEST_EXPORT_DIR", "EZTEST_VERSION_ATTACHMENT_DIR", "EZTEST_STORAGE_DIR")
        $actualKeys = @($manifest.configKeys)
        $missingKeys = @($expectedKeys | Where-Object { $actualKeys -notcontains $_ })
        Assert-True ($missingKeys.Count -eq 0) "manifest lists the backup config key names (missing: $($missingKeys -join ', '))"

        $dumpPath = Join-Path $script:backupDir ([string]$manifest.database.dumpFile -replace '/', '\')
        Assert-True (Test-Path -LiteralPath $dumpPath) "custom-format database dump exists ($($manifest.database.dumpFile))"
        if (Test-Path -LiteralPath $dumpPath) {
            Assert-True ((Get-Sha256 -Path $dumpPath) -eq [string]$manifest.database.sha256) "database dump sha256 matches the manifest"
            Assert-True ([long]$manifest.database.sizeBytes -eq (Get-Item -LiteralPath $dumpPath).Length) "database dump size matches the manifest"
        }
        Assert-True ([int]$manifest.database.rowCountsStable -eq 1 -or [bool]$manifest.database.rowCountsStable) "database row counts were stable around the dump"
        $manifestTableCount = @($manifest.database.rowCounts.PSObject.Properties).Count
        Assert-True ($manifestTableCount -gt 0) "manifest records row counts for $manifestTableCount public tables"

        $expectedLabels = @("evidence", "reports", "exports", "version-attachments", "objects")
        foreach ($label in $expectedLabels) {
            $entry = Get-DirectoryEntry -Manifest $manifest -Label $label
            Assert-True ($null -ne $entry) "manifest has a directory entry for '$label'"
            if ($null -eq $entry) { continue }
            Assert-True ([string]$entry.status -eq "archived") "directory '$label' is archived"
            Assert-True ([string]$entry.resolvedPath -eq [System.IO.Path]::GetFullPath($fixtureExpectedPaths[$label])) "directory '$label' records the absolute resolved path"
            $archivePath = Join-Path $script:backupDir ([string]$entry.archive -replace '/', '\')
            Assert-True (Test-Path -LiteralPath $archivePath) "directory '$label' archive exists ($($entry.archive))"
            if (Test-Path -LiteralPath $archivePath) {
                Assert-True ((Get-Sha256 -Path $archivePath) -eq [string]$entry.sha256) "directory '$label' archive sha256 matches the manifest"
                $listing = @(& $script:tarExe -tf $archivePath 2>$null)
                $excludedHits = @($listing | Where-Object {
                        $_ -match '(^|/)\.env($|\.)' -or $_ -match '(^|/)\.secrets(/|$)' -or $_ -match '(^|/)secrets(/|$)'
                    })
                Assert-True ($excludedHits.Count -eq 0) "directory '$label' archive contains no .env/.secrets entries"
            }
        }

        $evidenceEntry = Get-DirectoryEntry -Manifest $manifest -Label "evidence"
        if ($null -ne $evidenceEntry) {
            $evidencePaths = @($evidenceEntry.files | ForEach-Object { [string]$_.path })
            Assert-True ($evidencePaths -contains "rounds/run-1/evidence.txt") "manifest lists nested evidence file with a relative path"
            Assert-True (@($evidenceEntry.files).Count -eq 1) "manifest excludes the .env and .secrets decoys from evidence (got $($evidencePaths.Count) file(s))"
        }

        $expectedFiles = @(
            @{ Label = "evidence"; Relative = "rounds/run-1/evidence.txt" },
            @{ Label = "reports"; Relative = "report-001.md" },
            @{ Label = "exports"; Relative = "export-001.csv" },
            @{ Label = "version-attachments"; Relative = "attachment-001.txt" },
            @{ Label = "objects"; Relative = "sha256/ab/object-001.bin" }
        )
        $manifestFiles = @(Get-ManifestFileList -Manifest $manifest)
        Assert-True ($manifestFiles.Count -eq $expectedFiles.Count) "manifest lists all $($expectedFiles.Count) fixture files (got $($manifestFiles.Count))"
        foreach ($expected in $expectedFiles) {
            $entry = Get-DirectoryEntry -Manifest $manifest -Label $expected.Label
            $fileEntry = $null
            if ($null -ne $entry) {
                $fileEntry = @($entry.files | Where-Object { $_.path -eq $expected.Relative }) | Select-Object -First 1
            }
            Assert-True ($null -ne $fileEntry) "manifest lists $($expected.Label)/$($expected.Relative)"
            if ($null -ne $fileEntry) {
                $sourcePath = Join-Path (Join-Path $fixtureRoot $expected.Label) ($expected.Relative -replace '/', '\')
                Assert-True ([string]$fileEntry.sha256 -eq (Get-Sha256 -Path $sourcePath)) "manifest sha256 matches source for $($expected.Label)/$($expected.Relative)"
                Assert-True ([long]$fileEntry.sizeBytes -eq (Get-Item -LiteralPath $sourcePath).Length) "manifest size matches source for $($expected.Label)/$($expected.Relative)"
            }
        }

        $leakedEnvFiles = @(Get-ChildItem -LiteralPath $script:backupDir -Recurse -File -Force | Where-Object {
                $_.Name -eq ".env" -or $_.Name -like ".env.*" -or $_.FullName -match '\\\.secrets\\'
            })
        Assert-True ($leakedEnvFiles.Count -eq 0) "backup tree contains no .env/.secrets files"

        $secretValues = @()
        foreach ($key in $script:dotenv.Keys) {
            if ($key -match '(?i)(PASSWORD|SECRET|^llm_key$)') {
                $value = [string]$script:dotenv[$key]
                if ($value.Length -ge 8) { $secretValues += $value }
            }
        }
        $leak = Find-SecretLeak -Root $script:backupDir -Secrets $secretValues
        Assert-True ($null -eq $leak) "no .env secret value appears in the backup files (leak: $leak)"
        $manifestText = [System.IO.File]::ReadAllText($manifestPath)
        Assert-True (-not $manifestText.Contains("should-not-be-backed-up")) "manifest does not carry the .env decoy secret"
        Assert-True (-not $manifestText.Contains("hidden-secret-value")) "manifest does not carry the .secrets decoy secret"
    }

    Write-Step "Asserting backup refuses a non-empty target directory"
    $nonEmptyBackupResult = Invoke-EzScript -Label "backup-nonempty-target" -Path $backupScript -Parameters @{
        TargetDir     = $nonEmptyBackupTarget
        EvidenceDir   = (Join-Path $fixtureRoot "evidence")
        ReportDir     = (Join-Path $fixtureRoot "reports")
        ExportDir     = (Join-Path $fixtureRoot "exports")
        AttachmentDir = (Join-Path $fixtureRoot "version-attachments")
        ObjectDir     = (Join-Path $fixtureRoot "objects")
    }
    Assert-True ($nonEmptyBackupResult.ExitCode -ne 0) "backup rejects a non-empty -TargetDir"
    Assert-True ($nonEmptyBackupResult.Output -match 'must be empty|TargetDir|not empty') "backup explains that -TargetDir must be empty"
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $nonEmptyBackupTarget "manifest.json"))) "rejected non-empty backup target has no manifest"

    Write-Step "Asserting missing content directories fail unless -AllowMissingDirs is given"
    $missingSource = Join-Path $script:workDir "missing-evidence-source"
    $missingDirResult = Invoke-EzScript -Label "backup-missing-dir" -Path $backupScript -Parameters @{
        TargetDir     = $missingDirBackup
        EvidenceDir   = $missingSource
        ReportDir     = (Join-Path $fixtureRoot "reports")
        ExportDir     = (Join-Path $fixtureRoot "exports")
        AttachmentDir = (Join-Path $fixtureRoot "version-attachments")
        ObjectDir     = (Join-Path $fixtureRoot "objects")
    }
    Assert-True ($missingDirResult.ExitCode -ne 0) "backup rejects a missing content directory by default"
    Assert-True ($missingDirResult.Output -match 'AllowMissingDirs|missing content director') "backup names -AllowMissingDirs in the missing-directory error"
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $missingDirBackup "manifest.json"))) "missing-directory failure writes no manifest"

    $allowMissingResult = Invoke-EzScript -Label "backup-allow-missing-dir" -Path $backupScript -Parameters @{
        TargetDir       = $allowMissingBackup
        EvidenceDir     = $missingSource
        ReportDir       = (Join-Path $fixtureRoot "reports")
        ExportDir       = (Join-Path $fixtureRoot "exports")
        AttachmentDir   = (Join-Path $fixtureRoot "version-attachments")
        ObjectDir       = (Join-Path $fixtureRoot "objects")
        AllowMissingDirs = $true
    }
    Assert-True ($allowMissingResult.ExitCode -eq 0) "backup succeeds with -AllowMissingDirs for a missing content directory"
    Assert-True ($allowMissingResult.Output -match 'missing') "backup warns about the missing directory when -AllowMissingDirs is given"
    $allowMissingManifestPath = Join-Path $allowMissingBackup "manifest.json"
    if (Test-Path -LiteralPath $allowMissingManifestPath) {
        $allowMissingManifest = Get-Content -LiteralPath $allowMissingManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
        $missingEntry = Get-DirectoryEntry -Manifest $allowMissingManifest -Label "evidence"
        Assert-True ($null -ne $missingEntry -and [string]$missingEntry.status -eq "missing") "manifest records the allowed missing directory as status=missing"
    } else {
        Add-Failure "manifest was not written for the -AllowMissingDirs backup"
    }

    Write-Step "Asserting restore refuses unsafe targets"
    $refuseDbResult = Invoke-EzScript -Label "restore-unsafe-db" -Path $restoreScript -Parameters @{
        BackupDir      = $script:backupDir
        TargetDatabase = $unsafeDatabase
        TargetRoot     = $emptyTarget
        ReportPath     = (Join-Path $script:workDir "refuse-db-report.json")
    }
    Assert-True ($refuseDbResult.ExitCode -ne 0) "restore rejects a database name that is not *_test/*_e2e"
    Assert-True ($refuseDbResult.Output -match 'AllowAnyDatabase|_test|_e2e') "restore names the database allowlist in its error"

    $refuseDirResult = Invoke-EzScript -Label "restore-nonempty-dir" -Path $restoreScript -Parameters @{
        BackupDir      = $script:backupDir
        TargetDatabase = $script:tempDatabase
        TargetRoot     = $nonEmptyTarget
        ReportPath     = (Join-Path $script:workDir "refuse-dir-report.json")
    }
    Assert-True ($refuseDirResult.ExitCode -ne 0) "restore rejects a non-empty target directory"
    Assert-True ($refuseDirResult.Output -match 'empty|not empty|non-empty') "restore explains that the target must be empty"

    $forceOnlyResult = Invoke-EzScript -Label "restore-force-only" -Path $restoreScript -Parameters @{
        BackupDir      = $script:backupDir
        TargetDatabase = $script:tempDatabase
        TargetRoot     = $nonEmptyTarget
        ReportPath     = (Join-Path $script:workDir "refuse-force-report.json")
        Force          = $true
    }
    Assert-True ($forceOnlyResult.ExitCode -ne 0) "restore rejects -Force without the second confirmation"
    Assert-True ($forceOnlyResult.Output -match 'ConfirmOverwrite|confirmation') "restore names -ConfirmOverwrite as the second confirmation"

    $testDatabaseName = ""
    if (-not [string]::IsNullOrWhiteSpace([string]$script:dotenv["EZTEST_TEST_DATABASE_URL"])) {
        try { $testDatabaseName = (Parse-PostgresUrl -Url ([string]$script:dotenv["EZTEST_TEST_DATABASE_URL"])).Database } catch { $testDatabaseName = "" }
    }
    if (-not [string]::IsNullOrWhiteSpace($testDatabaseName)) {
        $sharedDbResult = Invoke-EzScript -Label "restore-shared-test-db" -Path $restoreScript -Parameters @{
            BackupDir      = $script:backupDir
            TargetDatabase = $testDatabaseName
            TargetRoot     = $sharedDbTarget
            ReportPath     = (Join-Path $script:workDir "refuse-shared-db-report.json")
        }
        Assert-True ($sharedDbResult.ExitCode -ne 0) "restore refuses the shared test database '$testDatabaseName'"
        Assert-True ($sharedDbResult.Output -match 'shared test database') "restore names the shared-test-database guard in its error"

        $sharedAllowResult = Invoke-EzScript -Label "restore-shared-test-db-allow-any" -Path $restoreScript -Parameters @{
            BackupDir       = $script:backupDir
            TargetDatabase  = $testDatabaseName
            TargetRoot      = (Join-Path $script:workDir "shared-allow-target")
            ReportPath      = (Join-Path $script:workDir "refuse-shared-allow-report.json")
            AllowAnyDatabase = $true
        }
        Assert-True ($sharedAllowResult.ExitCode -ne 0) "restore still refuses the shared test database with -AllowAnyDatabase"
        Assert-True ($sharedAllowResult.Output -match 'shared test database') "-AllowAnyDatabase does not bypass the shared-test-database guard"
    }

    $sourceDbResult = Invoke-EzScript -Label "restore-source-db-allow-any" -Path $restoreScript -Parameters @{
        BackupDir       = $script:backupDir
        TargetDatabase  = $script:conn.Database
        TargetRoot      = $sourceDbTarget
        ReportPath      = (Join-Path $script:workDir "refuse-source-db-report.json")
        AllowAnyDatabase = $true
    }
    Assert-True ($sourceDbResult.ExitCode -ne 0) "restore still refuses the configured source database with -AllowAnyDatabase"
    Assert-True ($sourceDbResult.Output -match 'source database') "-AllowAnyDatabase does not bypass the source-database guard"

    $systemDbResult = Invoke-EzScript -Label "restore-system-db-allow-any" -Path $restoreScript -Parameters @{
        BackupDir       = $script:backupDir
        TargetDatabase  = "postgres"
        TargetRoot      = $systemDbTarget
        ReportPath      = (Join-Path $script:workDir "refuse-system-db-report.json")
        AllowAnyDatabase = $true
    }
    Assert-True ($systemDbResult.ExitCode -ne 0) "restore still refuses the PostgreSQL system database with -AllowAnyDatabase"
    Assert-True ($systemDbResult.Output -match 'system database') "-AllowAnyDatabase does not bypass the system-database guard"

    Write-Step "Asserting restore refuses a non-empty target database and checks the DB before creating the directory"
    New-TempDatabase -Name $nonEmptyDatabase
    Invoke-PsqlRows -Database $nonEmptyDatabase -Sql "CREATE TABLE p8_nonempty_marker (id integer);" | Out-Null
    $nonEmptyDbResult = Invoke-EzScript -Label "restore-nonempty-db" -Path $restoreScript -Parameters @{
        BackupDir      = $script:backupDir
        TargetDatabase = $nonEmptyDatabase
        TargetRoot     = $nonEmptyDbTarget
        ReportPath     = (Join-Path $script:workDir "refuse-nonempty-db-report.json")
    }
    Assert-True ($nonEmptyDbResult.ExitCode -ne 0) "restore rejects a non-empty target database without confirmation"
    Assert-True ($nonEmptyDbResult.Output -match 'not empty') "restore explains that the target database must be empty"
    Remove-TempDatabase -Name $nonEmptyDatabase
    $dropped = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$nonEmptyDatabase';"
    Assert-True ($dropped -eq "0") "temporary non-empty database $nonEmptyDatabase was dropped"

    $missingDbResult = Invoke-EzScript -Label "restore-missing-db" -Path $restoreScript -Parameters @{
        BackupDir      = $script:backupDir
        TargetDatabase = $missingDatabase
        TargetRoot     = $missingDbTarget
        ReportPath     = (Join-Path $script:workDir "refuse-missing-db-report.json")
    }
    Assert-True ($missingDbResult.ExitCode -ne 0) "restore refuses a target database that does not exist"
    Assert-True ($missingDbResult.Output -match 'does not exist') "restore explains that the target database must exist"
    Assert-True (-not (Test-Path -LiteralPath $missingDbTarget)) "restore checks the target database before creating TargetRoot"

    Write-Step "Asserting a corrupted manifest is rejected"
    Copy-Item -LiteralPath $script:backupDir -Destination $tamperedBackup -Recurse
    $tamperedManifestPath = Join-Path $tamperedBackup "manifest.json"
    $tamperedManifest = Get-Content -LiteralPath $tamperedManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $tamperedManifest.database.sha256 = ("0" * 64)
    Write-Utf8File -Path $tamperedManifestPath -Content ((ConvertTo-Json -InputObject $tamperedManifest -Depth 12) + "`n")
    $tamperedResult = Invoke-EzScript -Label "restore-tampered-manifest" -Path $restoreScript -Parameters @{
        BackupDir      = $tamperedBackup
        TargetDatabase = $script:tempDatabase
        TargetRoot     = $tamperedTarget
        ReportPath     = (Join-Path $script:workDir "tampered-report.json")
    }
    Assert-True ($tamperedResult.ExitCode -ne 0) "restore rejects a manifest whose dump sha256 does not match"
    Assert-True ($tamperedResult.Output -match 'integrity|sha256') "restore reports the manifest integrity failure"

    Write-Step "Asserting a manifest directory label cannot escape the target"
    Copy-Item -LiteralPath $script:backupDir -Destination $evilBackup -Recurse
    $evilManifestPath = Join-Path $evilBackup "manifest.json"
    $evilManifest = Get-Content -LiteralPath $evilManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $evilManifest.directories[0].label = "..\..\p8-evil-$stamp"
    Write-Utf8File -Path $evilManifestPath -Content ((ConvertTo-Json -InputObject $evilManifest -Depth 12) + "`n")
    $evilPath = [System.IO.Path]::GetFullPath((Join-Path $evilTarget "..\..\p8-evil-$stamp"))
    New-TempDatabase -Name $evilDatabase
    $evilResult = Invoke-EzScript -Label "restore-evil-label" -Path $restoreScript -Parameters @{
        BackupDir      = $evilBackup
        TargetDatabase = $evilDatabase
        TargetRoot     = $evilTarget
        ReportPath     = (Join-Path $script:workDir "evil-label-report.json")
    }
    Assert-True ($evilResult.ExitCode -ne 0) "restore rejects a manifest directory label containing .."
    Assert-True ($evilResult.Output -match 'label') "restore reports the invalid directory label"
    Assert-True (-not (Test-Path -LiteralPath $evilPath)) "restore never creates a directory outside TargetRoot for a traversal label"
    Assert-True (-not (Test-Path -LiteralPath $evilTarget)) "restore validates the label before creating TargetRoot"
    Remove-TempDatabase -Name $evilDatabase
    $evilDropped = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$evilDatabase';"
    Assert-True ($evilDropped -eq "0") "temporary traversal-test database $evilDatabase was dropped"

    Write-Step "Restoring into $($script:tempDatabase) and a fresh directory"
    $restoreWatch = [System.Diagnostics.Stopwatch]::StartNew()
    $restoreResult = Invoke-EzScript -Label "restore" -Path $restoreScript -Parameters @{
        BackupDir      = $script:backupDir
        TargetDatabase = $script:tempDatabase
        TargetRoot     = $restoreRoot
        ReportPath     = $restoreReportPath
    }
    $restoreWatch.Stop()
    $script:restoreSeconds = [math]::Round($restoreWatch.Elapsed.TotalSeconds, 2)
    Assert-True ($restoreResult.ExitCode -eq 0) "restore.ps1 exits 0 (wall time $($script:restoreSeconds)s)"

    if ($null -ne $manifest) {
        $restoredCounts = Get-RowCounts -Database $script:tempDatabase
        $mismatches = @()
        foreach ($property in $manifest.database.rowCounts.PSObject.Properties) {
            $name = [string]$property.Name
            $expectedCount = [long]$property.Value
            if (-not $restoredCounts.Contains($name)) {
                $mismatches += "$name missing"
            } elseif ([long]$restoredCounts[$name] -ne $expectedCount) {
                $mismatches += "$name expected $expectedCount got $($restoredCounts[$name])"
            }
        }
        $extraTables = @($restoredCounts.Keys | Where-Object { $null -eq $manifest.database.rowCounts.PSObject.Properties[$_] })
        Assert-True ($mismatches.Count -eq 0 -and $extraTables.Count -eq 0) "restored row counts match the backup manifest for $($restoredCounts.Count) tables (mismatches: $($mismatches -join '; '))"

        $missingRestoredFiles = 0
        $hashMismatches = 0
        $manifestFiles = @(Get-ManifestFileList -Manifest $manifest)
        foreach ($file in $manifestFiles) {
            $targetPath = Join-Path (Join-Path $restoreRoot $file.Label) ($file.Relative -replace '/', '\')
            if (-not (Test-Path -LiteralPath $targetPath)) { $missingRestoredFiles++; continue }
            if ([long]$file.Size -ne (Get-Item -LiteralPath $targetPath).Length) { $hashMismatches++; continue }
            if ((Get-Sha256 -Path $targetPath) -ne $file.Sha) { $hashMismatches++ }
        }
        Assert-True ($missingRestoredFiles -eq 0) "all $($manifestFiles.Count) manifest files were restored (missing: $missingRestoredFiles)"
        Assert-True ($hashMismatches -eq 0) "all restored files match manifest sha256 (mismatches: $hashMismatches)"

        $restoredSentinel = Join-Path $restoreRoot "evidence\rounds\run-1\evidence.txt"
        Assert-True ((Test-Path -LiteralPath $restoredSentinel) -and ([System.IO.File]::ReadAllText($restoredSentinel) -eq "evidence-sentinel-2026-10-29`n")) "restored evidence sentinel content is intact"
        Assert-True (Test-Path -LiteralPath (Join-Path $restoreRoot "evidence\empty-subdir")) "restored evidence tree keeps the empty subdirectory"
        Assert-True (-not (Test-Path -LiteralPath (Join-Path $restoreRoot "evidence\.env"))) "restored evidence tree has no .env decoy"
        Assert-True (-not (Test-Path -LiteralPath (Join-Path $restoreRoot "evidence\.secrets"))) "restored evidence tree has no .secrets decoy"

        if (Test-Path -LiteralPath $restoreReportPath) {
            $restoreReport = Get-Content -LiteralPath $restoreReportPath -Raw -Encoding UTF8 | ConvertFrom-Json
            Assert-True ([bool]$restoreReport.passed) "restore report says passed=true"
            Assert-True ([int]$restoreReport.files.verified -eq $manifestFiles.Count) "restore report verified $($manifestFiles.Count) files"
            Assert-True ([bool]$restoreReport.database.rowCountsMatch) "restore report says row counts match"
            Assert-True (-not [bool]$restoreReport.database.rowCountsAdvisory) "restore report does not mark stable row counts as advisory"
            Assert-True (-not [bool]$restoreReport.secrets.included) "restore report records that secrets are not included"
        } else {
            Add-Failure "restore report was not written to $restoreReportPath"
        }

        Write-Step "Asserting -Force -ConfirmOverwrite round-trip and extra-table semantics"
        Invoke-PsqlRows -Database $script:tempDatabase -Sql "CREATE TABLE p8_extra_table (id integer);" | Out-Null
        $overwriteResult = Invoke-EzScript -Label "restore-overwrite" -Path $restoreScript -Parameters @{
            BackupDir        = $script:backupDir
            TargetDatabase   = $script:tempDatabase
            TargetRoot       = $restoreRoot
            ReportPath       = $overwriteReportPath
            Force            = $true
            ConfirmOverwrite = $true
        }
        Assert-True ($overwriteResult.ExitCode -eq 0) "restore succeeds with -Force -ConfirmOverwrite over a non-empty target database/directory"
        Assert-True ($overwriteResult.Output -match 'not empty|overwrit|--clean') "overwrite restore reports that it overwrote the target"
        if (Test-Path -LiteralPath $overwriteReportPath) {
            $overwriteReport = Get-Content -LiteralPath $overwriteReportPath -Raw -Encoding UTF8 | ConvertFrom-Json
            Assert-True ([bool]$overwriteReport.passed) "overwrite restore report says passed=true"
            Assert-True ([bool]$overwriteReport.database.extraTablesAdvisory) "overwrite report marks extra tables as advisory"
            Assert-True (@($overwriteReport.database.extraTables) -contains "p8_extra_table") "overwrite report lists the extra table that -Force leaves behind"
            Assert-True (@($overwriteReport.warnings) -join ' ' -match 'extra table|not part of the backup') "overwrite report warns about tables not present in the backup"
        } else {
            Add-Failure "overwrite restore report was not written to $overwriteReportPath"
        }
        $extraStillThere = Get-PsqlScalar -Database $script:tempDatabase -Sql "SELECT count(*) FROM pg_class WHERE relname = 'p8_extra_table';"
        Assert-True ($extraStillThere -eq "1") "-Force keeps tables that are not part of the backup (pg_restore --clean only drops dumped objects)"

        Write-Step "Asserting rowCountsStable=false is advisory, not a restore failure"
        Copy-Item -LiteralPath $script:backupDir -Destination $unstableBackup -Recurse
        $unstableManifestPath = Join-Path $unstableBackup "manifest.json"
        $unstableManifest = Get-Content -LiteralPath $unstableManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
        $unstableManifest.database.rowCountsStable = $false
        $firstTable = @($unstableManifest.database.rowCounts.PSObject.Properties)[0]
        $firstTableName = [string]$firstTable.Name
        $unstableManifest.database.rowCounts.$firstTableName = [long]$unstableManifest.database.rowCounts.$firstTableName + 1000
        Write-Utf8File -Path $unstableManifestPath -Content ((ConvertTo-Json -InputObject $unstableManifest -Depth 12) + "`n")
        New-TempDatabase -Name $unstableDatabase
        $unstableResult = Invoke-EzScript -Label "restore-unstable-counts" -Path $restoreScript -Parameters @{
            BackupDir      = $unstableBackup
            TargetDatabase = $unstableDatabase
            TargetRoot     = $unstableRoot
            ReportPath     = $unstableReportPath
        }
        Assert-True ($unstableResult.ExitCode -eq 0) "restore exits 0 when rowCountsStable=false and rows differ"
        Assert-True ($unstableResult.Output -match 'rowCountsStable=false|advisory') "restore warns that unstable row counts are advisory"
        if (Test-Path -LiteralPath $unstableReportPath) {
            $unstableReport = Get-Content -LiteralPath $unstableReportPath -Raw -Encoding UTF8 | ConvertFrom-Json
            Assert-True ([bool]$unstableReport.passed) "unstable-count restore report says passed=true"
            Assert-True (-not [bool]$unstableReport.database.rowCountsMatch) "unstable-count report records the row-count mismatch"
            Assert-True ([bool]$unstableReport.database.rowCountsAdvisory) "unstable-count report marks the row counts as advisory"
            Assert-True (@($unstableReport.warnings) -join ' ' -match 'rowCountsStable=false') "unstable-count report carries the advisory warning"
        } else {
            Add-Failure "unstable-count restore report was not written to $unstableReportPath"
        }
        Remove-TempDatabase -Name $unstableDatabase
        $unstableDropped = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$unstableDatabase';"
        Assert-True ($unstableDropped -eq "0") "temporary unstable-count database $unstableDatabase was dropped"

        Write-Step "Asserting -VerifyOnly detects a tampered file"
        Add-Content -LiteralPath $restoredSentinel -Value "tampered"
        $verifyResult = Invoke-EzScript -Label "restore-verify-tampered" -Path $restoreScript -Parameters @{
            BackupDir      = $script:backupDir
            TargetDatabase = $script:tempDatabase
            TargetRoot     = $restoreRoot
            ReportPath     = $verifyReportPath
            VerifyOnly     = $true
        }
        Assert-True ($verifyResult.ExitCode -ne 0) "restore -VerifyOnly exits non-zero after tampering"
        if (Test-Path -LiteralPath $verifyReportPath) {
            $verifyReport = Get-Content -LiteralPath $verifyReportPath -Raw -Encoding UTF8 | ConvertFrom-Json
            Assert-True (-not [bool]$verifyReport.passed) "verify report says passed=false after tampering"
            Assert-True (@($verifyReport.files.mismatches).Count -ge 1) "verify report lists the sha256 mismatch"
        } else {
            Add-Failure "verify report was not written to $verifyReportPath"
        }
    }

    Write-Step "Asserting the real default layout (backend/.data) with relative EZTEST_*_DIR values"
    if (-not (Test-Path -LiteralPath $backendDataRoot -PathType Container)) {
        New-Item -ItemType Directory -Force -Path $backendDataRoot | Out-Null
        $script:createdBackendData = $true
    }
    $seededFiles = [ordered]@{}
    foreach ($label in @("evidence", "reports", "exports", "version-attachments", "objects")) {
        $seededFiles[$label] = Add-DataSeed -DataRoot $backendDataRoot -Label $label `
            -Relative "seed-$label.txt" -Content "default-layout-$label-$stamp`n"
    }
    $sourceUrl = [string]$script:dotenv["EZTEST_DATABASE_URL"]
    $separator = if ($sourceUrl.Contains("?")) { "&" } else { "?" }
    $queryUrl = $sourceUrl + $separator + "application_name=eztest_p8_backup_drill&connect_timeout=10"
    Write-Utf8File -Path $defaultEnvPath -Content ("EZTEST_DATABASE_URL=" + $queryUrl + "`n")
    $defaultBackupResult = Invoke-EzScript -Label "backup-default-layout" -Path $backupScript -Parameters @{
        TargetDir = $defaultBackupDir
        EnvPath   = $defaultEnvPath
    }
    Assert-True ($defaultBackupResult.ExitCode -eq 0) "backup succeeds with the default backend/.data layout and URL query parameters"
    $defaultManifestPath = Join-Path $defaultBackupDir "manifest.json"
    $defaultManifest = $null
    if (Test-Path -LiteralPath $defaultManifestPath) {
        $defaultManifest = Get-Content -LiteralPath $defaultManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
    } else {
        Add-Failure "default-layout manifest.json was not written"
    }
    if ($null -ne $defaultManifest) {
        Assert-True ([string]$defaultManifest.contentBase.absolute -eq [System.IO.Path]::GetFullPath((Join-Path $script:repoRoot "backend"))) "default-layout manifest resolves relative directories against backend/"
        $defaultLabels = @("evidence", "reports", "exports", "version-attachments", "objects")
        foreach ($label in $defaultLabels) {
            $entry = Get-DirectoryEntry -Manifest $defaultManifest -Label $label
            $expectedPath = [System.IO.Path]::GetFullPath((Join-Path $backendDataRoot $label))
            Assert-True ($null -ne $entry -and [string]$entry.resolvedPath -eq $expectedPath) "default-layout directory '$label' resolves to backend/.data/$label"
            Assert-True ($null -ne $entry -and [string]$entry.status -eq "archived" -and [long]$entry.sizeBytes -gt 0 -and [int]$entry.fileCount -gt 0) "default-layout directory '$label' archive contains non-zero files"
        }
        $defaultFileList = @(Get-ManifestFileList -Manifest $defaultManifest)
        Assert-True ($defaultFileList.Count -ge 5) "default-layout backup lists non-zero files (got $($defaultFileList.Count))"
        foreach ($label in $defaultLabels) {
            $relative = "p8-drill-$stamp/seed-$label.txt"
            $fileEntry = @($defaultFileList | Where-Object { $_.Label -eq $label -and $_.Relative -eq $relative }) | Select-Object -First 1
            Assert-True ($null -ne $fileEntry) "default-layout manifest lists the seeded backend/.data/$label file"
            if ($null -ne $fileEntry) {
                Assert-True ([string]$fileEntry.Sha -eq (Get-Sha256 -Path $seededFiles[$label])) "default-layout manifest sha256 matches the seeded backend/.data/$label file"
            }
        }
        Assert-True (@($defaultManifest.database.parameters) -contains "application_name" -and @($defaultManifest.database.parameters) -contains "connect_timeout") "default-layout manifest records URL query parameters passed through PG* environment variables"

        New-TempDatabase -Name $defaultDatabase
        $defaultRestoreResult = Invoke-EzScript -Label "restore-default-layout" -Path $restoreScript -Parameters @{
            BackupDir      = $defaultBackupDir
            TargetDatabase = $defaultDatabase
            TargetRoot     = $defaultRestoreRoot
            ReportPath     = $defaultRestoreReportPath
        }
        Assert-True ($defaultRestoreResult.ExitCode -eq 0) "default-layout backup restores and verifies successfully"
        if (Test-Path -LiteralPath $defaultRestoreReportPath) {
            $defaultReport = Get-Content -LiteralPath $defaultRestoreReportPath -Raw -Encoding UTF8 | ConvertFrom-Json
            Assert-True ([bool]$defaultReport.passed) "default-layout restore report says passed=true"
        } else {
            Add-Failure "default-layout restore report was not written"
        }
        foreach ($label in $defaultLabels) {
            $restoredSeed = Join-Path (Join-Path $defaultRestoreRoot $label) ("p8-drill-$stamp\seed-$label.txt")
            Assert-True ((Test-Path -LiteralPath $restoredSeed) -and (Get-Sha256 -Path $restoredSeed) -eq (Get-Sha256 -Path $seededFiles[$label])) "default-layout restore verifies the seeded backend/.data/$label file"
        }
        $defaultTableCount = @($defaultManifest.database.rowCounts.PSObject.Properties).Count
        $script:defaultLayoutSummary = [ordered]@{
            contentBase        = [string]$defaultManifest.contentBase.absolute
            files              = $defaultFileList.Count
            seededFiles        = $defaultLabels.Count
            tables             = $defaultTableCount
            restorePassed      = [bool]$defaultReport.passed
            urlQueryParameters = @($defaultManifest.database.parameters)
        }
        Remove-TempDatabase -Name $defaultDatabase
        $defaultDropped = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$defaultDatabase';"
        Assert-True ($defaultDropped -eq "0") "temporary default-layout database $defaultDatabase was dropped"
    }
} catch {
    Add-Failure "unexpected error: $($_.Exception.Message)"
} finally {
    Write-Step "Cleanup"
    Remove-DataSeeds
    if (Test-Path -LiteralPath $defaultEnvPath) {
        Remove-Item -LiteralPath $defaultEnvPath -Force -ErrorAction SilentlyContinue
    }
    $escapedEvilPath = [System.IO.Path]::GetFullPath((Join-Path $evilTarget "..\..\p8-evil-$stamp"))
    if ($escapedEvilPath.StartsWith($tmpRootPrefix, [System.StringComparison]::OrdinalIgnoreCase) -and
        (Test-Path -LiteralPath $escapedEvilPath)) {
        Remove-Item -LiteralPath $escapedEvilPath -Recurse -Force -ErrorAction SilentlyContinue
    }
    if ($script:createdDatabases.Count -gt 0) {
        if ($Keep) {
            foreach ($name in @($script:createdDatabases)) {
                Write-Note "keeping temporary database $name (-Keep)"
            }
        } else {
            $droppedCount = 0
            foreach ($name in @($script:createdDatabases)) {
                try {
                    Remove-TempDatabase -Name $name
                    $droppedCount++
                } catch {
                    Add-Failure "cleanup of temporary database '$name' failed: $($_.Exception.Message)"
                }
            }
            if ($droppedCount -gt 0) { Add-Pass "$droppedCount temporary database(s) were dropped" }
        }
    }
    if (-not $Keep) {
        foreach ($directory in @($fixtureRoot, $restoreRoot, $nonEmptyTarget, $emptyTarget,
                $nonEmptyDbTarget, $missingDbTarget, $nonEmptyBackupTarget, $missingDirBackup,
                $allowMissingBackup, $sharedDbTarget, $sourceDbTarget, $systemDbTarget,
                $tamperedBackup, $tamperedTarget, $evilBackup, $evilTarget, $unstableBackup,
                $unstableRoot, $defaultRestoreRoot, (Join-Path $script:workDir "shared-allow-target"))) {
            if (Test-Path -LiteralPath $directory) {
                try {
                    Remove-Item -LiteralPath $directory -Recurse -Force
                } catch {
                    Add-Failure "cleanup of $directory failed: $($_.Exception.Message)"
                }
            }
        }
        Write-Note "kept backup evidence at $($script:backupDir) and $defaultBackupDir"
    } else {
        Write-Note "kept all drill artifacts under $($script:workDir) (-Keep)"
    }

    if ($script:workReady -and (Test-Path -LiteralPath $script:workDir)) {
        try {
            Write-DrillReport -Path $drillReportPath
            Write-Note "drill report: $drillReportPath"
        } catch {
            Add-Failure "writing the drill report failed: $($_.Exception.Message)"
        }
    }

    if ($null -eq $previousPgPassword) {
        Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    } else {
        $env:PGPASSWORD = $previousPgPassword
    }
}

Write-Host ""
if ($script:failures.Count -gt 0) {
    Write-Host "BACKUP DRILL FAILED: $($script:failures.Count) assertion(s) failed, $($script:passes.Count) passed." -ForegroundColor Red
    foreach ($failure in $script:failures) { Write-Host "  - $failure" -ForegroundColor Red }
    exit 1
}
Write-Host "BACKUP DRILL PASSED: $($script:passes.Count) assertions passed (backup $($script:backupSeconds)s, restore $($script:restoreSeconds)s)." -ForegroundColor Green
exit 0
