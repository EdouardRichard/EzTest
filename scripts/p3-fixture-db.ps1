#requires -Version 5.1
<#
.SYNOPSIS
    Creates and reseeds the P3 order/refund fixture tables in a dedicated
    integration/test database.

.DESCRIPTION
    The P3 acceptance flows use two web SUTs (Order Center and Payment Gateway)
    plus a real target database. This script owns the target-database side of
    that fixture: the p3_orders and p3_refunds tables and their deterministic
    seed rows.

    Fixture schema (acceptance queries rely on exactly this shape):
      p3_orders(
        id BIGINT PRIMARY KEY,
        order_no TEXT NOT NULL UNIQUE,
        amount NUMERIC(12,2) NOT NULL,
        currency TEXT NOT NULL DEFAULT 'CNY',
        status TEXT NOT NULL,
        created_at TIMESTAMPTZ NOT NULL DEFAULT now())
      p3_refunds(
        id BIGINT PRIMARY KEY,
        refund_no TEXT NOT NULL UNIQUE,
        order_id BIGINT NOT NULL REFERENCES p3_orders(id),
        amount NUMERIC(12,2) NOT NULL,
        currency TEXT NOT NULL DEFAULT 'CNY',
        status TEXT NOT NULL,
        reason TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMPTZ NOT NULL DEFAULT now())

    Seed rows:
      p3_orders  (1, 'ORD-1001', 5200.00, 'paid')
      p3_refunds (1, 'REF-5001', order 1, 5000.00, 'created')

    Idempotency: both tables are created IF NOT EXISTS, then truncated and
    reseeded in one transaction, so every run leaves exactly the one seed order
    and the one seed refund.

    Safety: the connection URL is accepted from -DatabaseUrl, then the
    EZTEST_TEST_DATABASE_URL environment variable, then the repository .env. Only
    loopback PostgreSQL hosts and databases named *_test or *_e2e are accepted;
    this script never manages a development/production database.

    The postgres account in the URL must exist in the target database; this
    script creates tables, not databases. The password is passed through
    PGPASSWORD and is never printed.

    Compatible with Windows PowerShell 5.1 and PowerShell 7+.

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p3-fixture-db.ps1 `
        -DatabaseUrl $env:EZTEST_TEST_DATABASE_URL

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p3-fixture-db.ps1 -WhatIf
#>
[CmdletBinding()]
param(
    [string]$DatabaseUrl = "",
    [switch]$WhatIf
)

$ErrorActionPreference = "Stop"

$script:OrdersTable = "p3_orders"
$script:RefundsTable = "p3_refunds"
$repoRoot = Split-Path -Parent $PSScriptRoot

function Write-Step {
    param([string]$Message)
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Write-Note {
    param([string]$Message)
    Write-Host "    $Message" -ForegroundColor DarkGray
}

function Read-DotEnvValue {
    param([string]$Path, [string]$Key)
    if (-not (Test-Path -LiteralPath $Path)) { return "" }
    foreach ($line in Get-Content -LiteralPath $Path) {
        if ($line -match '^\s*#' -or $line -match '^\s*$') { continue }
        if ($line -match '^\s*([^=]+)=(.*)$') {
            if ($matches[1].Trim() -ne $Key) { continue }
            $value = $matches[2].Trim()
            if ($value.Length -ge 2) {
                $first = $value.Substring(0, 1)
                $last = $value.Substring($value.Length - 1, 1)
                if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                    $value = $value.Substring(1, $value.Length - 2)
                }
            }
            return $value
        }
    }
    return ""
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

function Resolve-FixtureTarget {
    param([string]$Url)
    $uri = $null
    if (-not [System.Uri]::TryCreate($Url, [System.UriKind]::Absolute, [ref]$uri)) {
        throw "DatabaseUrl is not a valid absolute URL"
    }
    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        throw "DatabaseUrl must use the postgres scheme"
    }
    if ([string]::IsNullOrWhiteSpace($uri.Host)) { throw "DatabaseUrl must include a host" }
    if ($uri.Host -notin @("localhost", "127.0.0.1", "::1")) {
        throw "p3-fixture-db.ps1 only manages a loopback PostgreSQL; got host '$($uri.Host)'"
    }
    $database = [System.Uri]::UnescapeDataString($uri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($database) -or $database.Contains("/")) {
        throw "DatabaseUrl must include a single database name"
    }
    if ($database -notmatch '(?i)(_e2e|_test)$') {
        throw "refusing to touch database '$database': p3-fixture-db.ps1 only manages *_e2e/*_test databases"
    }
    $userInfo = $uri.UserInfo
    $separator = $userInfo.IndexOf(":")
    if ($separator -lt 0) {
        $user = $userInfo
        $password = ""
    } else {
        $user = $userInfo.Substring(0, $separator)
        $password = $userInfo.Substring($separator + 1)
    }
    $user = [System.Uri]::UnescapeDataString($user)
    $password = [System.Uri]::UnescapeDataString($password)
    if ([string]::IsNullOrWhiteSpace($user)) { throw "DatabaseUrl must include a database user" }
    $port = if ($uri.Port -gt 0) { $uri.Port } else { 5432 }
    return [pscustomobject]@{
        Host     = $uri.Host
        Port     = $port
        User     = $user
        Password = $password
        Database = $database
    }
}

if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) { $DatabaseUrl = $env:EZTEST_TEST_DATABASE_URL }
if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
    $DatabaseUrl = Read-DotEnvValue -Path (Join-Path $repoRoot ".env") -Key "EZTEST_TEST_DATABASE_URL"
}
if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) { $DatabaseUrl = $env:EZTEST_DATABASE_URL }
if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
    $DatabaseUrl = Read-DotEnvValue -Path (Join-Path $repoRoot ".env") -Key "EZTEST_DATABASE_URL"
}
if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
    throw "DatabaseUrl is required; pass -DatabaseUrl or set EZTEST_TEST_DATABASE_URL"
}

$target = Resolve-FixtureTarget -Url $DatabaseUrl

Write-Step "P3 fixture database plan ($($target.Host):$($target.Port)/$($target.Database))"
Write-Note "tables   $($script:OrdersTable) + $($script:RefundsTable) (create if missing)"
Write-Note "seed     order 1 ORD-1001 5200.00 paid; refund 1 REF-5001 order 1 5000.00 created"
Write-Note "reseed   TRUNCATE ... RESTART IDENTITY then insert the deterministic rows"
Write-Note "safety   loopback PostgreSQL only; database name must end in _e2e or _test"

if ($WhatIf) {
    Write-Note "[WhatIf] no PostgreSQL statement was executed"
    exit 0
}

$psqlExe = Resolve-ToolPath -LocalPath (Join-Path $repoRoot ".tools\pgsql\bin\psql.exe") -CommandName "psql.exe"
if ([string]::IsNullOrWhiteSpace($psqlExe)) {
    throw "psql executable not found (.tools\pgsql\bin\psql.exe or PATH)"
}

$reseedSql = @'
BEGIN;
CREATE TABLE IF NOT EXISTS p3_orders (
    id BIGINT PRIMARY KEY,
    order_no TEXT NOT NULL UNIQUE,
    amount NUMERIC(12,2) NOT NULL,
    currency TEXT NOT NULL DEFAULT 'CNY',
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS p3_refunds (
    id BIGINT PRIMARY KEY,
    refund_no TEXT NOT NULL UNIQUE,
    order_id BIGINT NOT NULL REFERENCES p3_orders(id),
    amount NUMERIC(12,2) NOT NULL,
    currency TEXT NOT NULL DEFAULT 'CNY',
    status TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
TRUNCATE TABLE p3_refunds, p3_orders RESTART IDENTITY CASCADE;
INSERT INTO p3_orders (id, order_no, amount, status)
VALUES (1, 'ORD-1001', 5200.00, 'paid');
INSERT INTO p3_refunds (id, refund_no, order_id, amount, status)
VALUES (1, 'REF-5001', 1, 5000.00, 'created');
COMMIT;
'@

$verifySql = @'
SELECT
  o.id::text || '|' || o.order_no || '|' || o.amount::text || '|' || o.status || '|' ||
  r.refund_no || '|' || r.amount::text || '|' || r.status || '|' ||
  (SELECT count(*) FROM p3_orders)::text || '|' || (SELECT count(*) FROM p3_refunds)::text
FROM p3_orders o
JOIN p3_refunds r ON r.order_id = o.id
ORDER BY o.id, r.id;
'@

$env:PGPASSWORD = $target.Password
$previousErrorAction = $ErrorActionPreference
$ErrorActionPreference = "SilentlyContinue"
try {
    Write-Step "Creating/reseeding the P3 fixture tables"
    $reseedOutput = & $psqlExe -h $target.Host -p $target.Port -U $target.User -d $target.Database `
        -v ON_ERROR_STOP=1 -q -c $reseedSql 2>&1
    $reseedExit = $LASTEXITCODE
    if ($reseedExit -ne 0) {
        $detail = ($reseedOutput | Out-String).Trim()
        throw "psql reseed failed (exit $reseedExit) on $($target.Database): $detail"
    }

    Write-Step "Verifying the seeded rows"
    $verifyOutput = & $psqlExe -h $target.Host -p $target.Port -U $target.User -d $target.Database `
        -tA -v ON_ERROR_STOP=1 -c $verifySql 2>&1
    $verifyExit = $LASTEXITCODE
    if ($verifyExit -ne 0) {
        $detail = ($verifyOutput | Out-String).Trim()
        throw "psql verification failed (exit $verifyExit) on $($target.Database): $detail"
    }
} finally {
    $ErrorActionPreference = $previousErrorAction
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
}

$rows = @(($verifyOutput | Out-String) -split "`r?`n" | Where-Object { $_.Trim() -ne "" } | ForEach-Object { $_.Trim() })
$expected = "1|ORD-1001|5200.00|paid|REF-5001|5000.00|created|1|1"
Write-Note "rows: $($rows -join '; ')"
if ($rows.Count -ne 1 -or $rows[0] -ne $expected) {
    throw "unexpected P3 fixture rows: '$($rows -join '; ')'; expected '$expected'"
}

Write-Host "P3 fixture database ready: $($target.Database) (p3_orders + p3_refunds reseeded and verified)" -ForegroundColor Green
Write-Note "seed: order 1 ORD-1001 5200.00 paid; refund 1 REF-5001 5000.00 created"
exit 0
