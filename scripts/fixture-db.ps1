# fixture-db.ps1
#
# P7 real-database acceptance fixture (design spec 12.8, plan T29).
#
# Ensures, idempotently and only against the local PostgreSQL on :5433:
#   - database  eztest_p7_target
#   - role      eztest_p7_ro   LOGIN, no superuser/createdb/createrole, SELECT only
#   - role      eztest_p7_rw   LOGIN, seed-only owner/writer of the fixture table
#   - table     public.db_order_fixture(id, status, amount) with 3 deterministic rows
#
# Fixture schema (the acceptance queries rely on exactly this shape):
#   public.db_order_fixture(
#     id     BIGINT PRIMARY KEY,          -- 1, 2, 3
#     status TEXT NOT NULL,               -- paid, pending, cancelled
#     amount NUMERIC(12,2) NOT NULL)      -- 100.00, 50.50, 25.00
# eztest_p7_ro gets USAGE on schema public plus SELECT on this one table and has
# default_transaction_read_only=on; it has no CREATE, no write grant and no
# ownership. eztest_p7_rw owns the database/table and is used only for seeding.
# The read-only probe below proves INSERT, UPDATE and DELETE are all rejected.
#
# The generated credentials are written to .tmp/p7-db-fixture.json (gitignored,
# restricted ACL) and are never printed. `-WhatIf` prints the plan without
# touching PostgreSQL. `-Cleanup` drops only the three dedicated fixture objects.
#
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/fixture-db.ps1 -WhatIf
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/fixture-db.ps1
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/fixture-db.ps1 -Cleanup

[CmdletBinding()]
param(
    [switch]$WhatIf,
    [switch]$Cleanup,
    [string]$EnvPath = "",
    [string]$CredentialsPath = ""
)

$ErrorActionPreference = "Stop"

$script:FixtureDbName = "eztest_p7_target"
$script:ReadOnlyRole = "eztest_p7_ro"
$script:WriteRole = "eztest_p7_rw"
$script:FixtureTable = "db_order_fixture"
$script:SeedRows = 3

$repoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($EnvPath)) { $EnvPath = Join-Path $repoRoot ".env" }
if ([string]::IsNullOrWhiteSpace($CredentialsPath)) {
    $CredentialsPath = Join-Path (Join-Path $repoRoot ".tmp") "p7-db-fixture.json"
}

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
        throw "fixture-db.ps1 only manages the local PostgreSQL; EZTEST_DATABASE_URL host is not loopback"
    }
    $port = $uri.Port
    if ($port -le 0) { $port = 5432 }
    if ($port -ne 5433) {
        throw "fixture-db.ps1 expects the local PostgreSQL on port 5433; got $port"
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
    if ($databaseName -eq $script:FixtureDbName) {
        throw "EZTEST_DATABASE_URL must not point at the fixture database itself"
    }
    return $uri
}


function New-RandomSecret {
    $bytes = New-Object byte[] 24
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    return (($bytes | ForEach-Object { $_.ToString("x2") }) -join "")
}

function Write-Utf8NoBom {
    param([string]$Path, [string]$Text)
    $parent = Split-Path -Parent $Path
    if (-not [string]::IsNullOrWhiteSpace($parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    [System.IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding($false)))
}

function Set-RestrictedFileAcl {
    param([string]$Path)
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & icacls.exe $Path /inheritance:r *> $null
        $inheritanceExit = $LASTEXITCODE
        & icacls.exe $Path /grant "$($identity):F" *> $null
        $grantExit = $LASTEXITCODE
    } catch {
        $inheritanceExit = -1
        $grantExit = -1
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($inheritanceExit -ne 0 -or $grantExit -ne 0) {
        Write-Warning "could not fully restrict the ACL of $Path; the file lives in the gitignored .tmp tree and is never printed"
    }
}

function Invoke-Psql {
    param(
        [string]$Database,
        [string]$Sql,
        [string]$Operation
    )
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:dbUser -d $Database `
            -v ON_ERROR_STOP=1 -q -c $Sql *> $null
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) { throw "$Operation failed (psql exit $code)" }
}

function Split-ConnectionUserInfo {
    param([string]$UserInfo)
    $separator = $UserInfo.IndexOf(":")
    if ($separator -lt 0) {
        return [pscustomobject]@{ User = $UserInfo; Password = "" }
    }
    return [pscustomobject]@{
        User     = $UserInfo.Substring(0, $separator)
        Password = $UserInfo.Substring($separator + 1)
    }
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

# Role/database setup ran as the local administrator from .env. The generated
# passwords are supplied through placeholders so the SQL template stays literal.
$roleSql = @'
DO $body$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '__RO_ROLE__') THEN
    ALTER ROLE __RO_ROLE__ LOGIN PASSWORD '__RO_PW__' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  ELSE
    CREATE ROLE __RO_ROLE__ LOGIN PASSWORD '__RO_PW__' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '__RW_ROLE__') THEN
    ALTER ROLE __RW_ROLE__ LOGIN PASSWORD '__RW_PW__' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  ELSE
    CREATE ROLE __RW_ROLE__ LOGIN PASSWORD '__RW_PW__' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  END IF;
END
$body$;
'@

$schemaSql = @'
GRANT CONNECT ON DATABASE __DB__ TO __RO_ROLE__, __RW_ROLE__;
GRANT USAGE, CREATE ON SCHEMA public TO __RW_ROLE__;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SET ROLE __RW_ROLE__;
CREATE TABLE IF NOT EXISTS public.__TABLE__ (
  id     BIGINT PRIMARY KEY,
  status TEXT NOT NULL,
  amount NUMERIC(12,2) NOT NULL
);
INSERT INTO public.__TABLE__ (id, status, amount) VALUES
  (1, 'paid', 100.00),
  (2, 'pending', 50.50),
  (3, 'cancelled', 25.00)
ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, amount = EXCLUDED.amount;
DELETE FROM public.__TABLE__ WHERE id NOT IN (1, 2, 3);
RESET ROLE;
REVOKE ALL ON public.__TABLE__ FROM __RO_ROLE__;
GRANT USAGE ON SCHEMA public TO __RO_ROLE__;
GRANT SELECT ON public.__TABLE__ TO __RO_ROLE__;
REVOKE CREATE ON SCHEMA public FROM __RO_ROLE__;
ALTER ROLE __RO_ROLE__ SET default_transaction_read_only = on;
'@

function Expand-FixtureSql {
    param([string]$Template, [string]$RoPassword, [string]$RwPassword)
    return $Template.Replace("__DB__", $script:FixtureDbName).
        Replace("__TABLE__", $script:FixtureTable).
        Replace("__RO_ROLE__", $script:ReadOnlyRole).
        Replace("__RW_ROLE__", $script:WriteRole).
        Replace("__RO_PW__", $RoPassword).
        Replace("__RW_PW__", $RwPassword)
}

function Read-FixtureCredentials {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    try {
        $raw = [System.IO.File]::ReadAllText($Path)
        $parsed = $raw | ConvertFrom-Json
        if ([string]::IsNullOrWhiteSpace([string]$parsed.database)) { return $null }
        if ([string]::IsNullOrWhiteSpace([string]$parsed.readOnly.password)) { return $null }
        if ([string]::IsNullOrWhiteSpace([string]$parsed.write.password)) { return $null }
        return $parsed
    } catch {
        return $null
    }
}

# --- main ------------------------------------------------------------------

if ($Cleanup) {
    Write-Step "Cleanup plan: drop only the dedicated P7 fixture objects"
    Write-Note "database $($script:FixtureDbName) (terminate its connections first)"
    Write-Note "roles $($script:ReadOnlyRole), $($script:WriteRole)"
    Write-Note "credentials file $CredentialsPath"
    if ($WhatIf) {
        Write-Note "[WhatIf] no PostgreSQL statement was executed"
        exit 0
    }
    $envValues = Read-DotEnv -Path $EnvPath
    $adminUrl = if ($envValues.ContainsKey("EZTEST_DATABASE_URL")) { [string]$envValues["EZTEST_DATABASE_URL"] } else { "" }
    if ([string]::IsNullOrWhiteSpace($adminUrl)) { throw "EZTEST_DATABASE_URL is missing from .env" }
    $uri = Assert-LocalDatabaseUrl -DatabaseUrl $adminUrl
    $credentials = Split-ConnectionUserInfo -UserInfo $uri.UserInfo
    $script:dbHost = $uri.Host
    $script:dbPort = $(if ($uri.Port -gt 0) { $uri.Port } else { 5432 })
    $script:dbUser = [System.Uri]::UnescapeDataString($credentials.User)
    $adminPassword = [System.Uri]::UnescapeDataString($credentials.Password)
    $script:psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql.exe"
    if ([string]::IsNullOrWhiteSpace($script:psqlExe)) { throw "psql executable not found" }
    $env:PGPASSWORD = $adminPassword
    try {
        $exists = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$($script:FixtureDbName)';"
        if ($exists -ne "0") {
            Invoke-Psql -Database "postgres" -Operation "terminating fixture database connections" -Sql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$($script:FixtureDbName)' AND pid <> pg_backend_pid();"
            Invoke-Psql -Database "postgres" -Operation "dropping $($script:FixtureDbName)" -Sql "DROP DATABASE IF EXISTS $($script:FixtureDbName);"
        }
        foreach ($role in @($script:ReadOnlyRole, $script:WriteRole)) {
            Invoke-Psql -Database "postgres" -Operation "dropping role $role" -Sql "DROP ROLE IF EXISTS $role;"
        }
    } finally {
        Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
    }
    if (Test-Path -LiteralPath $CredentialsPath) { Remove-Item -LiteralPath $CredentialsPath -Force }
    Write-Host "fixture cleanup done: $($script:FixtureDbName), $($script:ReadOnlyRole), $($script:WriteRole)" -ForegroundColor Green
    exit 0
}

$envValues = Read-DotEnv -Path $EnvPath
$adminUrl = if ($envValues.ContainsKey("EZTEST_DATABASE_URL")) { [string]$envValues["EZTEST_DATABASE_URL"] } else { "" }
if ([string]::IsNullOrWhiteSpace($adminUrl)) { throw "EZTEST_DATABASE_URL is missing from .env" }
$uri = Assert-LocalDatabaseUrl -DatabaseUrl $adminUrl
$credentials = Split-ConnectionUserInfo -UserInfo $uri.UserInfo
$script:dbHost = $uri.Host
$script:dbPort = $(if ($uri.Port -gt 0) { $uri.Port } else { 5432 })
$script:dbUser = [System.Uri]::UnescapeDataString($credentials.User)
$adminPassword = [System.Uri]::UnescapeDataString($credentials.Password)
$adminDatabase = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
if ($adminDatabase -eq $script:FixtureDbName) { throw "EZTEST_DATABASE_URL must not point at the fixture database" }

Write-Step "P7 fixture database plan ($($script:dbHost):$($script:dbPort))"
Write-Note "database  $($script:FixtureDbName) (owner $($script:WriteRole))"
Write-Note "role      $($script:ReadOnlyRole): LOGIN, no superuser/createdb/createrole, SELECT only, default_transaction_read_only=on"
Write-Note "role      $($script:WriteRole): LOGIN, seed-only owner/writer of the fixture table"
Write-Note "table     public.$($script:FixtureTable)(id, status, amount) + $($script:SeedRows) deterministic rows"
Write-Note "grant     CONNECT + USAGE + SELECT to $($script:ReadOnlyRole); no write/DDL"
Write-Note "creds     $CredentialsPath (never printed; restricted ACL)"
Write-Note "safety    loopback PostgreSQL :5433 only; no statement outside $($script:FixtureDbName) besides role/database management"

if ($WhatIf) {
    Write-Note "[WhatIf] no PostgreSQL statement was executed"
    exit 0
}

$script:psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql.exe"
if ([string]::IsNullOrWhiteSpace($script:psqlExe)) { throw "psql executable not found (.tools or PATH)" }

$existing = Read-FixtureCredentials -Path $CredentialsPath
$roPassword = if ($null -ne $existing) { [string]$existing.readOnly.password } else { New-RandomSecret }
$rwPassword = if ($null -ne $existing) { [string]$existing.write.password } else { New-RandomSecret }
if ($roPassword -eq $rwPassword) { throw "generated fixture passwords must differ" }

$env:PGPASSWORD = $adminPassword
try {
    Write-Step "Probing the local PostgreSQL administration account"
    $privileges = Get-PsqlScalar -Database "postgres" -Sql "SELECT rolsuper, rolcreatedb, rolcreaterole FROM pg_roles WHERE rolname = current_user;"
    $parts = $privileges.Split("|")
    if ($parts.Count -ne 3 -or ($parts[0] -ne "t" -and ($parts[1] -ne "t" -or $parts[2] -ne "t"))) {
        throw "the .env database account cannot manage fixture roles/databases (needs CREATEDB and CREATEROLE or superuser)"
    }
    Write-Note "administration account can create the dedicated fixture database and roles"

    Write-Step "Ensuring the fixture roles"
    $roleSqlExpanded = Expand-FixtureSql -Template $roleSql -RoPassword $roPassword -RwPassword $rwPassword
    Invoke-Psql -Database "postgres" -Sql $roleSqlExpanded -Operation "ensuring fixture roles"
    Write-Note "roles $($script:ReadOnlyRole) and $($script:WriteRole) ensured (LOGIN, least privilege)"

    Write-Step "Ensuring the fixture database"
    $exists = Get-PsqlScalar -Database "postgres" -Sql "SELECT count(*) FROM pg_database WHERE datname = '$($script:FixtureDbName)';"
    if ($exists -eq "0") {
        Invoke-Psql -Database "postgres" -Sql "CREATE DATABASE $($script:FixtureDbName) OWNER $($script:WriteRole);" -Operation "creating $($script:FixtureDbName)"
        Write-Note "database $($script:FixtureDbName) created"
    } else {
        Invoke-Psql -Database "postgres" -Sql "ALTER DATABASE $($script:FixtureDbName) OWNER TO $($script:WriteRole);" -Operation "normalizing the fixture database owner"
        Write-Note "database $($script:FixtureDbName) already exists (owner normalized)"
    }

    Write-Step "Ensuring the fixture table, seed rows and read-only grants"
    $schemaSqlExpanded = Expand-FixtureSql -Template $schemaSql -RoPassword $roPassword -RwPassword $rwPassword
    Invoke-Psql -Database $script:FixtureDbName -Sql $schemaSqlExpanded -Operation "applying fixture schema/grants"
    Write-Note "table public.$($script:FixtureTable) seeded with $($script:SeedRows) rows"
} finally {
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
}

Write-Step "Verifying the read-only role cannot write"
$env:PGPASSWORD = $roPassword
$previousErrorAction = $ErrorActionPreference
$ErrorActionPreference = "SilentlyContinue"
try {
    $check = & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:ReadOnlyRole -d $script:FixtureDbName -tA `
        -v ON_ERROR_STOP=1 -c "SELECT current_user || '|' || current_setting('transaction_read_only') || '|' || count(*)::text FROM public.$($script:FixtureTable);" 2>$null
    $checkExit = $LASTEXITCODE
    if ($checkExit -ne 0) { throw "the read-only role could not read the fixture table" }
    $check = ($check | Out-String).Trim()
    if ($check -ne "$($script:ReadOnlyRole)|on|$($script:SeedRows)") {
        throw "read-only probe returned '$check', expected '$($script:ReadOnlyRole)|on|$($script:SeedRows)'"
    }
    # INSERT/UPDATE/DELETE must all be rejected by the role's read-only
    # transaction and grants. Native stderr is expected here, so it is dropped
    # instead of surfacing as a PowerShell error record. The admin cleanup below
    # runs only when the fixture is broken, so a failed probe cannot be masked by
    # a successful undo.
    $writeProbes = @(
        @{
            Label = "INSERT"
            Sql   = "INSERT INTO public.$($script:FixtureTable) (id, status, amount) VALUES (99, 'mutated', 1.00);"
            Undo  = "DELETE FROM public.$($script:FixtureTable) WHERE id = 99;"
        },
        @{
            Label = "UPDATE"
            Sql   = "UPDATE public.$($script:FixtureTable) SET status = 'mutated', amount = 999.00 WHERE id = 1;"
            Undo  = "UPDATE public.$($script:FixtureTable) SET status = 'paid', amount = 100.00 WHERE id = 1;"
        },
        @{
            Label = "DELETE"
            Sql   = "DELETE FROM public.$($script:FixtureTable) WHERE id = 1;"
            Undo  = "INSERT INTO public.$($script:FixtureTable) (id, status, amount) VALUES (1, 'paid', 100.00) ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, amount = EXCLUDED.amount;"
        }
    )
    $rejected = @()
    foreach ($writeProbe in $writeProbes) {
        & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:ReadOnlyRole -d $script:FixtureDbName -tA `
            -v ON_ERROR_STOP=1 -c $writeProbe.Sql 2>$null | Out-Null
        $writeExit = $LASTEXITCODE
        if ($writeExit -eq 0) {
            $env:PGPASSWORD = $adminPassword
            & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:dbUser -d $script:FixtureDbName -q -c $writeProbe.Undo 2>$null | Out-Null
            throw "the read-only role executed $($writeProbe.Label); the fixture is not read-only"
        }
        $rejected += $writeProbe.Label
    }
    # Prove the rejected statements left the fixture rows byte-identical.
    $afterWrites = & $script:psqlExe -h $script:dbHost -p $script:dbPort -U $script:ReadOnlyRole -d $script:FixtureDbName -tA `
        -v ON_ERROR_STOP=1 -c "SELECT count(*)::text || '|' || string_agg(id || ':' || status || ':' || amount::text, ',' ORDER BY id) FROM public.$($script:FixtureTable);" 2>$null
    if ($LASTEXITCODE -ne 0) { throw "the read-only role could not re-read the fixture table after the write probes" }
    $afterWrites = ($afterWrites | Out-String).Trim()
    $expectedRows = "$($script:SeedRows)|1:paid:100.00,2:pending:50.50,3:cancelled:25.00"
    if ($afterWrites -ne $expectedRows) {
        throw "the fixture rows changed after the rejected writes: '$afterWrites', expected '$expectedRows'"
    }
    Write-Note "probe: current_user=$($script:ReadOnlyRole), transaction_read_only=on, rows=$($script:SeedRows), $($rejected -join '/') rejected, rows unchanged"
} finally {
    $ErrorActionPreference = $previousErrorAction
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
}

$payload = [ordered]@{
    generatedAt = [DateTime]::UtcNow.ToString("o")
    host        = "127.0.0.1"
    port        = [int]$script:dbPort
    database    = $script:FixtureDbName
    table       = $script:FixtureTable
    seedRows    = [int]$script:SeedRows
    readOnly    = [ordered]@{ username = $script:ReadOnlyRole; password = $roPassword }
    write       = [ordered]@{ username = $script:WriteRole; password = $rwPassword }
}
$json = ConvertTo-Json -InputObject $payload -Depth 6
Write-Utf8NoBom -Path $CredentialsPath -Text ($json + "`n")
Set-RestrictedFileAcl -Path $CredentialsPath

Write-Host "fixture database ready: $($script:FixtureDbName) ($($script:SeedRows) rows, read-only role verified)" -ForegroundColor Green
Write-Note "credentials were written to $CredentialsPath and were not printed"
exit 0
