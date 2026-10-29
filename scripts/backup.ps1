#requires -Version 5.1
<#
.SYNOPSIS
    Creates a restorable EzTest backup: PostgreSQL custom-format dump, content
    directory archives and a sha256 manifest.

.DESCRIPTION
    The backup contains:
      - db/<database>.dump       pg_dump -Fc of EZTEST_DATABASE_URL;
      - directories/<label>.tar  evidence / reports / exports /
                                 version-attachments / objects directory archives;
      - manifest.json            format version, git version/commit, config key
                                 names, every file path/size/sha256, archive
                                 hashes, per-table row counts and duration.

    Secrets are never part of the backup: .env, .env.*, .secrets/ and secrets/
    entries are excluded from the archives and the manifest, and the .env file
    itself is never copied. The database password is passed to the client tools
    through PGPASSWORD and is never printed. Query parameters from the database
    URL (sslmode, connect_timeout, application_name, ...) are passed to the
    client tools through the corresponding PG* environment variables; unknown
    parameters are rejected instead of being silently dropped.

    Relative content directory values (from .env or the -EvidenceDir-style
    overrides) are resolved against the API working directory <repo>/backend,
    because the API process runs there and its defaults (.data/evidence, ...)
    therefore live in <repo>/backend/.data/. A configured directory that does
    not exist fails the backup unless -AllowMissingDirs is supplied; with that
    switch the directory is warned about and recorded as status=missing.

    The target directory must not exist yet (or must be empty) so a failed or
    mixed backup cannot silently replace valid evidence. Restore it with
    scripts/restore.ps1.

.PARAMETER TargetDir
    Destination directory for the backup. Must be absent or empty. Relative
    values are resolved against the repository root.

.PARAMETER EnvPath
    .env file to read; defaults to <repo>/.env.

.PARAMETER DatabaseUrl
    Overrides EZTEST_DATABASE_URL from .env (useful for a non-default source).

.PARAMETER EvidenceDir
    Overrides EZTEST_EVIDENCE_DIR (the P6 evidence directory). A relative value
    is resolved against the API working directory <repo>/backend.

.PARAMETER ReportDir
    Overrides EZTEST_REPORT_DIR (relative values resolve against <repo>/backend).

.PARAMETER ExportDir
    Overrides EZTEST_EXPORT_DIR (relative values resolve against <repo>/backend).

.PARAMETER AttachmentDir
    Overrides EZTEST_VERSION_ATTACHMENT_DIR (relative values resolve against
    <repo>/backend).

.PARAMETER ObjectDir
    Overrides EZTEST_STORAGE_DIR (relative values resolve against
    <repo>/backend).

.PARAMETER AllowMissingDirs
    Record configured content directories that do not exist as status=missing
    (and warn) instead of failing the backup. Without this switch a missing
    configured directory is treated as a configuration error, because it
    usually means the directory base is wrong.

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/backup.ps1 -TargetDir .tmp/p8-backup/manual

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$TargetDir,
    [string]$EnvPath = "",
    [string]$DatabaseUrl = "",
    [string]$EvidenceDir = "",
    [string]$ReportDir = "",
    [string]$ExportDir = "",
    [string]$AttachmentDir = "",
    [string]$ObjectDir = "",
    [switch]$AllowMissingDirs
)

$ErrorActionPreference = "Stop"

$script:repoRoot = Split-Path -Parent $PSScriptRoot
$script:apiRoot = Join-Path $script:repoRoot "backend"

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
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        throw "EZTEST_DATABASE_URL must use the postgres scheme"
    }
    $separator = $uri.UserInfo.IndexOf(":")
    if ($separator -lt 0) { throw "EZTEST_DATABASE_URL must include user:password" }
    if ([string]::IsNullOrWhiteSpace($uri.Host)) { throw "EZTEST_DATABASE_URL must include a host" }
    $database = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($database) -or $database.Contains("/")) {
        throw "EZTEST_DATABASE_URL must include a single database name"
    }
    $query = [ordered]@{}
    if (-not [string]::IsNullOrWhiteSpace($uri.Query)) {
        foreach ($pair in $uri.Query.TrimStart([char[]]"?") -split "&") {
            if ([string]::IsNullOrWhiteSpace($pair)) { continue }
            $index = $pair.IndexOf("=")
            if ($index -lt 0) {
                $rawKey = $pair
                $rawValue = ""
            } else {
                $rawKey = $pair.Substring(0, $index)
                $rawValue = $pair.Substring($index + 1)
            }
            $key = [System.Uri]::UnescapeDataString($rawKey)
            if ([string]::IsNullOrWhiteSpace($key)) { throw "database URL contains an empty query parameter name" }
            $query[$key] = [System.Uri]::UnescapeDataString($rawValue)
        }
    }
    return [pscustomobject]@{
        Host     = $uri.Host
        Port     = $(if ($uri.Port -gt 0) { $uri.Port } else { 5432 })
        User     = [System.Uri]::UnescapeDataString($uri.UserInfo.Substring(0, $separator))
        Password = [System.Uri]::UnescapeDataString($uri.UserInfo.Substring($separator + 1))
        Database = $database
        Query    = $query
    }
}

# libpq environment variables for the query parameters we pass through to
# pg_dump/psql. Unknown parameters are rejected so a typo cannot silently drop
# TLS or connection settings.
$script:pgQueryEnvironment = @{
    "application_name"      = "PGAPPNAME"
    "connect_timeout"       = "PGCONNECT_TIMEOUT"
    "sslmode"               = "PGSSLMODE"
    "sslcert"               = "PGSSLCERT"
    "sslkey"                = "PGSSLKEY"
    "sslrootcert"           = "PGSSLROOTCERT"
    "sslpassword"           = "PGSSLPASSWORD"
    "sslnegotiation"        = "PGSSLNEGOTIATION"
    "sslcompression"        = "PGSSLCOMPRESSION"
    "gssencmode"            = "PGGSSENCMODE"
    "channel_binding"       = "PGCHANNELBINDING"
    "target_session_attrs"  = "PGTARGETSESSIONATTRS"
    "options"               = "PGOPTIONS"
    "client_encoding"       = "PGCLIENTENCODING"
    "keepalives"            = "PGKEEPALIVES"
    "keepalives_idle"       = "PGKEEPALIVESIDLE"
    "keepalives_interval"   = "PGKEEPALIVESINTERVAL"
    "keepalives_count"      = "PGKEEPALIVESCOUNT"
    "passfile"              = "PGPASSFILE"
    "service"               = "PGSERVICE"
    "servicefile"           = "PGSERVICEFILE"
}

function Set-PostgresQueryEnvironment {
    param($Query)
    $mapped = @{}
    foreach ($key in @($Query.Keys)) {
        $normalized = ([string]$key).ToLowerInvariant()
        if (-not $script:pgQueryEnvironment.ContainsKey($normalized)) {
            throw "unsupported database URL query parameter '$key'; set the equivalent libpq PG* environment variable instead"
        }
        $mapped[$key] = $script:pgQueryEnvironment[$normalized]
    }
    $saved = @{}
    foreach ($key in @($mapped.Keys)) {
        $name = $mapped[$key]
        $saved[$name] = [System.Environment]::GetEnvironmentVariable($name, "Process")
        Set-Item -Path ("Env:" + $name) -Value ([string]$Query[$key])
    }
    return $saved
}

function Restore-EnvironmentValues {
    param([hashtable]$Saved)
    foreach ($name in $Saved.Keys) {
        if ($null -eq $Saved[$name]) {
            Remove-Item -Path ("Env:" + $name) -ErrorAction SilentlyContinue
        } else {
            Set-Item -Path ("Env:" + $name) -Value $Saved[$name]
        }
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
            $output = & $script:psqlExe -h $script:conn.Host -p $script:conn.Port -U $script:conn.User `
                -d $Database -v ON_ERROR_STOP=1 -tA -F '|' -f $tempFile 2>$null
            $code = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
        }
    } finally {
        Remove-Item -LiteralPath $tempFile -Force -ErrorAction SilentlyContinue
    }
    if ($code -ne 0) { throw "$Operation failed (psql exit $code)" }
    return @($output)
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

function Test-RowCountsEqual {
    param($Left, $Right)
    if ($Left.Count -ne $Right.Count) { return $false }
    foreach ($key in $Left.Keys) {
        if (-not $Right.Contains($key)) { return $false }
        if ([long]$Left[$key] -ne [long]$Right[$key]) { return $false }
    }
    return $true
}

function Test-ExcludedRelativePath {
    param([string]$RelativePath)
    foreach ($segment in ($RelativePath -split '/')) {
        if ($segment -eq ".env" -or $segment -like ".env.*") { return $true }
        if ($segment -eq ".secrets" -or $segment -eq "secrets") { return $true }
    }
    return $false
}

function Get-RelativeFileEntries {
    param([string]$Root)
    $entries = New-Object System.Collections.ArrayList
    $rootWithSeparator = $Root.TrimEnd([char[]]"\") + "\"
    foreach ($file in @(Get-ChildItem -LiteralPath $Root -Recurse -File -Force)) {
        $relative = $file.FullName.Substring($rootWithSeparator.Length).Replace('\', '/')
        if (Test-ExcludedRelativePath -RelativePath $relative) { continue }
        [void]$entries.Add([pscustomobject]@{
                path      = $relative
                sizeBytes = [long]$file.Length
                sha256    = (Get-Sha256 -Path $file.FullName)
            })
    }
    $sorted = @($entries | Sort-Object -Property path)
    return $sorted
}

function Assert-ArchiveExcludesSecrets {
    param([string]$ArchivePath)
    $listing = @(& $script:tarExe -tf $ArchivePath 2>$null)
    if ($LASTEXITCODE -ne 0) { throw "cannot list archive $ArchivePath (tar exit $LASTEXITCODE)" }
    $hits = @($listing | Where-Object {
            $_ -match '(^|/)\.env($|/)' -or $_ -match '(^|/)\.env\.[^/]*$' -or
            $_ -match '(^|/)\.secrets($|/)' -or $_ -match '(^|/)secrets($|/)'
        })
    if ($hits.Count -gt 0) {
        throw "archive $ArchivePath contains secret-looking entries: $($hits -join ', ')"
    }
}

function New-DirectoryArchive {
    param([string]$SourceDir, [string]$ArchivePath)
    $archiveParent = Split-Path -Parent $ArchivePath
    New-Item -ItemType Directory -Force -Path $archiveParent | Out-Null
    $excludeArgs = @(
        "--exclude=./.env", "--exclude=*/.env",
        "--exclude=./.env.*", "--exclude=*/.env.*",
        "--exclude=./.secrets", "--exclude=*/.secrets",
        "--exclude=./secrets", "--exclude=*/secrets"
    )
    $tarArgs = @("-cf", $ArchivePath, "-C", $SourceDir) + $excludeArgs + @(".")
    & $script:tarExe @tarArgs
    if ($LASTEXITCODE -ne 0) { throw "tar failed for $SourceDir (exit $LASTEXITCODE)" }
    Assert-ArchiveExcludesSecrets -ArchivePath $ArchivePath
}

function Assert-OutsideRepositoryRoot {
    param([string]$Path, [string]$Label)
    $full = [System.IO.Path]::GetFullPath($Path).TrimEnd([char[]]"\")
    $repo = [System.IO.Path]::GetFullPath($script:repoRoot).TrimEnd([char[]]"\")
    if ($full.Equals($repo, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "$Label must not be the repository root (it contains .env); got $full"
    }
    if (($repo + "\").StartsWith($full + "\", [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "$Label must not be an ancestor of the repository root (it could capture .env); got $full"
    }
    return $full
}

function Resolve-RepoPath {
    param([string]$Path)
    if ([System.IO.Path]::IsPathRooted($Path)) { return [System.IO.Path]::GetFullPath($Path) }
    return [System.IO.Path]::GetFullPath((Join-Path $script:repoRoot $Path))
}

function Resolve-ContentPath {
    param([string]$Path)
    # The API process runs from <repo>/backend, so its relative content roots
    # (the .env.example defaults: .data/evidence, .data/reports, ...) are
    # resolved against backend/, not against the repository root.
    if ([System.IO.Path]::IsPathRooted($Path)) { return [System.IO.Path]::GetFullPath($Path) }
    return [System.IO.Path]::GetFullPath((Join-Path $script:apiRoot $Path))
}

# --------------------------------------------------------------------------
# Main
# --------------------------------------------------------------------------

try {
$targetFull = Resolve-RepoPath -Path $TargetDir
if (-not (Test-Path -LiteralPath $script:apiRoot -PathType Container)) {
    throw "API working directory not found: $($script:apiRoot) (content directory defaults resolve against it)"
}
if ([string]::IsNullOrWhiteSpace($EnvPath)) { $EnvPath = Join-Path $script:repoRoot ".env" }
$envValues = Read-DotEnv -Path $EnvPath
$effectiveUrl = if (-not [string]::IsNullOrWhiteSpace($DatabaseUrl)) { $DatabaseUrl } else { [string]$envValues["EZTEST_DATABASE_URL"] }
$script:conn = Parse-PostgresUrl -Url $effectiveUrl

$script:pgDumpExe = Resolve-ToolPath -LocalPath (Join-Path $script:repoRoot ".tools\pgsql\bin\pg_dump.exe") -CommandName "pg_dump.exe"
$script:psqlExe = Resolve-ToolPath -LocalPath (Join-Path $script:repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql.exe"
$script:tarExe = Resolve-ToolPath -LocalPath "" -CommandName "tar.exe"
foreach ($tool in @($script:pgDumpExe, $script:psqlExe, $script:tarExe)) {
    if ([string]::IsNullOrWhiteSpace($tool)) { throw "Required tool missing (pg_dump.exe, psql.exe, tar.exe)" }
}

$directorySpecs = @(
    [pscustomobject]@{ Label = "evidence"; ConfigKey = "EZTEST_EVIDENCE_DIR"; Override = $EvidenceDir; Default = ".data/evidence" },
    [pscustomobject]@{ Label = "reports"; ConfigKey = "EZTEST_REPORT_DIR"; Override = $ReportDir; Default = ".data/reports" },
    [pscustomobject]@{ Label = "exports"; ConfigKey = "EZTEST_EXPORT_DIR"; Override = $ExportDir; Default = ".data/exports" },
    [pscustomobject]@{ Label = "version-attachments"; ConfigKey = "EZTEST_VERSION_ATTACHMENT_DIR"; Override = $AttachmentDir; Default = ".data/version-attachments" },
    [pscustomobject]@{ Label = "objects"; ConfigKey = "EZTEST_STORAGE_DIR"; Override = $ObjectDir; Default = ".data/objects" }
)

$sources = @()
foreach ($spec in $directorySpecs) {
    $raw = [string]$spec.Override
    $sourceKind = "override"
    if ([string]::IsNullOrWhiteSpace($raw)) {
        $raw = [string]$envValues[$spec.ConfigKey]
        $sourceKind = "env"
    }
    if ([string]::IsNullOrWhiteSpace($raw)) {
        $raw = $spec.Default
        $sourceKind = "default"
    }
    $raw = $raw.Trim()
    $resolved = Resolve-ContentPath -Path $raw
    $full = Assert-OutsideRepositoryRoot -Path $resolved -Label $spec.Label
    if ($targetFull.Equals($full, [System.StringComparison]::OrdinalIgnoreCase) -or
        ($targetFull + "\").StartsWith($full + "\", [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "TargetDir must not be inside the '$($spec.Label)' source directory ($full)"
    }
    $sources += [pscustomobject]@{
        Label      = $spec.Label
        ConfigKey  = $spec.ConfigKey
        Configured = $raw
        Source     = $sourceKind
        Path       = $full
    }
}

$missingSources = @($sources | Where-Object { -not (Test-Path -LiteralPath $_.Path -PathType Container) })
if ($missingSources.Count -gt 0) {
    if (-not $AllowMissingDirs) {
        $names = ($missingSources | ForEach-Object { "$($_.Label) ($($_.Path))" }) -join "; "
        throw "missing content director(ies): $names. Fix the configured path (relative values resolve against $($script:apiRoot)) or pass -AllowMissingDirs to record them as missing."
    }
    foreach ($missing in $missingSources) {
        Write-Warning "content directory '$($missing.Label)' is missing: $($missing.Path); recording status=missing (-AllowMissingDirs)"
    }
}

if (Test-Path -LiteralPath $targetFull) {
    if (-not (Test-Path -LiteralPath $targetFull -PathType Container)) {
        throw "TargetDir exists and is not a directory: $targetFull"
    }
    $existing = @(Get-ChildItem -LiteralPath $targetFull -Force)
    if ($existing.Count -gt 0) {
        throw "TargetDir must be empty; it already contains $($existing.Count) item(s): $targetFull"
    }
} else {
    New-Item -ItemType Directory -Force -Path $targetFull | Out-Null
}
} catch {
    Write-Host ""
    Write-Host "BACKUP FAILED: $($_.Exception.Message)" -ForegroundColor Red
    Write-Host "Do not restore from the target: no valid manifest was written." -ForegroundColor Red
    exit 1
}

$startedAt = Get-Date
$watch = [System.Diagnostics.Stopwatch]::StartNew()
$previousPgPassword = $env:PGPASSWORD
$previousPgQueryEnvironment = @{}
$env:PGPASSWORD = $script:conn.Password

try {
    $previousPgQueryEnvironment = Set-PostgresQueryEnvironment -Query $script:conn.Query
    Write-Step "Backup plan"
    Write-Note "content    relative directory values resolve against $($script:apiRoot)"
    Write-Note "database   $($script:conn.Database) on $($script:conn.Host):$($script:conn.Port) (custom format)"
    foreach ($source in $sources) {
        $state = if (Test-Path -LiteralPath $source.Path -PathType Container) { "exists" } else { "missing (recorded as missing)" }
        Write-Note ("{0,-20} {1} [{2}, {3}]" -f $source.Label, $source.Path, $state, $source.Source)
    }
    Write-Note "target     $targetFull"
    Write-Note "secrets    .env / .env.* / .secrets/ / secrets/ are never archived"

    Write-Step "Counting source rows (before dump)"
    $rowCountsBefore = Get-RowCounts -Database $script:conn.Database
    Write-Note "public tables: $($rowCountsBefore.Count)"

    Write-Step "Dumping database in custom format"
    $dumpRelative = "db/$($script:conn.Database).dump"
    $dumpPath = Join-Path $targetFull ($dumpRelative -replace '/', '\')
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $dumpPath) | Out-Null
    $dumpWatch = [System.Diagnostics.Stopwatch]::StartNew()
    & $script:pgDumpExe -h $script:conn.Host -p $script:conn.Port -U $script:conn.User -d $script:conn.Database -Fc -f $dumpPath
    $dumpCode = $LASTEXITCODE
    $dumpWatch.Stop()
    if ($dumpCode -ne 0) { throw "pg_dump failed (exit $dumpCode); no valid manifest was written" }
    $dumpSize = (Get-Item -LiteralPath $dumpPath).Length
    $dumpSha = Get-Sha256 -Path $dumpPath
    Write-Note ("dump       {0:N0} bytes, sha256 {1}, {2:N1}s" -f $dumpSize, $dumpSha, $dumpWatch.Elapsed.TotalSeconds)

    Write-Step "Counting source rows (after dump)"
    $rowCountsAfter = Get-RowCounts -Database $script:conn.Database
    $rowCountsStable = Test-RowCountsEqual -Left $rowCountsBefore -Right $rowCountsAfter
    if (-not $rowCountsStable) {
        Write-Warning "database changed while the dump was taken; rowCountsStable=false (the dump itself is a consistent snapshot)"
    }

    Write-Step "Archiving content directories"
    $directories = New-Object System.Collections.ArrayList
    $totalContentBytes = [long]0
    $totalFileCount = 0
    foreach ($source in $sources) {
        if (-not (Test-Path -LiteralPath $source.Path -PathType Container)) {
            Write-Warning "content directory '$($source.Label)' is missing: $($source.Path); recorded as status=missing"
            [void]$directories.Add([ordered]@{
                    label        = $source.Label
                    configKey    = $source.ConfigKey
                    configured   = $source.Configured
                    source       = $source.Source
                    resolvedPath = $source.Path
                    status       = "missing"
                    archive      = $null
                    sizeBytes    = 0
                    sha256       = $null
                    fileCount    = 0
                    contentBytes = 0
                    files        = @()
                })
            Write-Note ("{0,-20} missing; recorded without an archive" -f $source.Label)
            continue
        }
        $files = @(Get-RelativeFileEntries -Root $source.Path)
        $contentBytes = [long]0
        foreach ($file in $files) { $contentBytes += [long]$file.sizeBytes }
        $archiveRelative = "directories/$($source.Label).tar"
        $archivePath = Join-Path $targetFull ($archiveRelative -replace '/', '\')
        $archiveWatch = [System.Diagnostics.Stopwatch]::StartNew()
        New-DirectoryArchive -SourceDir $source.Path -ArchivePath $archivePath
        $archiveWatch.Stop()
        $archiveSize = (Get-Item -LiteralPath $archivePath).Length
        $archiveSha = Get-Sha256 -Path $archivePath
        [void]$directories.Add([ordered]@{
                label        = $source.Label
                configKey    = $source.ConfigKey
                configured   = $source.Configured
                source       = $source.Source
                resolvedPath = $source.Path
                status       = "archived"
                archive      = $archiveRelative
                sizeBytes    = [long]$archiveSize
                sha256       = $archiveSha
                fileCount    = $files.Count
                contentBytes = [long]$contentBytes
                files        = $files
            })
        $totalContentBytes += $contentBytes
        $totalFileCount += $files.Count
        Write-Note ("{0,-20} {1,3} file(s), {2:N0} content bytes, {3:N0} archive bytes, {4:N1}s" -f `
                $source.Label, $files.Count, $contentBytes, $archiveSize, $archiveWatch.Elapsed.TotalSeconds)
    }

    $watch.Stop()
    $version = "unknown"
    $branch = "unknown"
    $commit = "unknown"
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        $commit = (& git -C $script:repoRoot rev-parse HEAD 2>$null | Out-String).Trim()
        $branch = (& git -C $script:repoRoot rev-parse --abbrev-ref HEAD 2>$null | Out-String).Trim()
        $version = (& git -C $script:repoRoot describe --tags --always 2>$null | Out-String).Trim()
    } finally {
        $ErrorActionPreference = $previous
    }
    if ([string]::IsNullOrWhiteSpace($commit) -or $commit -eq "unknown") {
        throw "Cannot determine the git commit for the manifest (run inside the EzTest repository)"
    }
    if ([string]::IsNullOrWhiteSpace($version)) { $version = $commit }

    $manifest = [ordered]@{
        formatVersion   = 1
        tool            = "scripts/backup.ps1"
        createdAt       = $startedAt.ToUniversalTime().ToString("o")
        durationSeconds = [math]::Round($watch.Elapsed.TotalSeconds, 2)
        contentBase     = [ordered]@{
            absolute = $script:apiRoot
            relative = $(if ($script:apiRoot.StartsWith(($script:repoRoot.TrimEnd([char[]]"\") + "\"), [System.StringComparison]::OrdinalIgnoreCase)) {
                    $script:apiRoot.Substring($script:repoRoot.TrimEnd([char[]]"\").Length + 1).Replace('\', '/')
                } else { $script:apiRoot })
            rule     = "relative content directory values resolve against the API working directory (<repo>/backend)"
        }
        repository      = [ordered]@{
            version     = $version
            branch      = $branch
            commit      = $commit
            commitShort = $commit.Substring(0, [Math]::Min(12, $commit.Length))
        }
        database        = [ordered]@{
            configKey       = "EZTEST_DATABASE_URL"
            name            = $script:conn.Database
            format          = "custom"
            dumpFile        = $dumpRelative
            sizeBytes       = [long]$dumpSize
            sha256          = $dumpSha
            rowCounts       = $rowCountsAfter
            rowCountsBefore = $rowCountsBefore
            rowCountsStable = [bool]$rowCountsStable
            parameters      = @($script:conn.Query.Keys)
        }
        directories     = @($directories)
        configKeys      = @(
            "EZTEST_DATABASE_URL",
            "EZTEST_EVIDENCE_DIR",
            "EZTEST_REPORT_DIR",
            "EZTEST_EXPORT_DIR",
            "EZTEST_VERSION_ATTACHMENT_DIR",
            "EZTEST_STORAGE_DIR"
        )
        excluded        = @(".env", ".env.*", ".secrets/**", "secrets/**")
        summary         = [ordered]@{
            directoryArchives   = @($directories | Where-Object { $_.status -eq "archived" }).Count
            missingDirectories  = @($directories | Where-Object { $_.status -eq "missing" }).Count
            files               = $totalFileCount
            contentBytes        = [long]$totalContentBytes
            dumpBytes           = [long]$dumpSize
            totalBytes          = [long]($totalContentBytes + $dumpSize)
        }
    }

    $manifestPath = Join-Path $targetFull "manifest.json"
    $manifestTemp = Join-Path $targetFull "manifest.json.tmp"
    Write-Utf8File -Path $manifestTemp -Content ((ConvertTo-Json -InputObject $manifest -Depth 12) + "`n")
    Move-Item -LiteralPath $manifestTemp -Destination $manifestPath -Force

    Write-Host ""
    Write-Host "BACKUP COMPLETE" -ForegroundColor Green
    Write-Note "target      $targetFull"
    Write-Note "manifest    $manifestPath"
    Write-Note "database    $($script:conn.Database) ($('{0:N0}' -f $dumpSize) bytes, $($rowCountsAfter.Count) tables)"
    Write-Note "files       $totalFileCount file(s) across $(@($directories | Where-Object { $_.status -eq 'archived' }).Count) archived director(ies)"
    Write-Note "repository  $branch @ $($manifest.repository.commitShort) ($version)"
    Write-Note "duration    $($manifest.durationSeconds)s"
    Write-Note "secrets     not included; supply EZTEST_SECRET_KEYS/EZTEST_SECRET_KEY_ACTIVE separately on restore"
    exit 0
} catch {
    Write-Host ""
    Write-Host "BACKUP FAILED: $($_.Exception.Message)" -ForegroundColor Red
    Write-Host "Do not restore from ${targetFull}: no valid manifest was written." -ForegroundColor Red
    exit 1
} finally {
    Restore-EnvironmentValues -Saved $previousPgQueryEnvironment
    if ($null -eq $previousPgPassword) {
        Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    } else {
        $env:PGPASSWORD = $previousPgPassword
    }
}
