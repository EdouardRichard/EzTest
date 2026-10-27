#requires -Version 5.1
<#
.SYNOPSIS
    Runs the P0-P4 acceptance gate for EzTest.

.DESCRIPTION
    Executes the Go unit and integration suites, the P1 Agent unit/integration
    suites, the P3 version/requirements/clarification suites, the P4 codeintel
    suites, the frontend lint/typecheck/test/build gate, the Playwright E2E
    suite (P0/P1/P2 and the three P3 version specs in
    frontend/e2e/versions*.spec.ts; P4 E2E coverage is added by Task 20 and is
    not part of this gate yet), a repeated
    migration run and the health/audit/version-API assertions. The P0-P4
    deliverables are checked (README, OpenAPI, deploy files, Agent/P3/P4 env
    templates, the P3/P4 OpenAPI contract keywords, reason enums and event
    catalog), the P4 fake-LLM test is executed, the P4 migration/table contract
    is derived from order.txt and the Compose file is statically validated with
    node. Container execution and the gated real Agent/P3/P4 LLM acceptance are
    reported as SKIPPED because this host has no Docker and no opt-in for paid
    calls. Prints P1/P3/P4 DoD mapping tables and exits non-zero on any failure.

.PARAMETER StaticOnly
    Runs only the repository-local static checks (README/OpenAPI/env/Compose
    contracts plus the P4 fake-LLM test) without requiring .env, the database,
    the built API or Playwright. Useful on a host without PostgreSQL; the full
    gate remains the default.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
    [switch]$StaticOnly
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$frontendDir = Join-Path $repoRoot "frontend"
$backendDir = Join-Path $repoRoot "backend"
$scriptsDir = Join-Path $repoRoot "scripts"
$tmpDir = Join-Path $repoRoot ".tmp"
$e2eDbName = "eztest_e2e"
$apiPort = 13333
$apiBaseUrl = "http://localhost:$apiPort"

New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null

$checkResults = New-Object System.Collections.ArrayList

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Write-LogTail {
    param(
        [string]$Path,
        [string]$Label,
        [int]$Lines = 60
    )
    if (-not (Test-Path -LiteralPath $Path)) { return }
    Write-Host ""
    Write-Host "--- $Label (last $Lines lines) ---" -ForegroundColor Yellow
    Get-Content -LiteralPath $Path -Tail $Lines -ErrorAction SilentlyContinue | ForEach-Object {
        Write-Host $_
    }
}

function Add-CheckResult {
    param(
        [string]$Dod,
        [string]$CheckName,
        [string]$Status,
        [string]$Detail
    )
    [void]$checkResults.Add([pscustomobject]@{
            DoD    = $Dod
            Check  = $CheckName
            Result = $Status
            Detail = $Detail
        })
}

function Invoke-Check {
    param(
        [string]$Dod,
        [string]$CheckName,
        [scriptblock]$Body
    )
    Write-Step $CheckName
    $timer = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        & $Body
        $timer.Stop()
        Add-CheckResult -Dod $Dod -CheckName $CheckName -Status "PASS" -Detail ("{0:N1}s" -f $timer.Elapsed.TotalSeconds)
        Write-Host "PASS $CheckName" -ForegroundColor Green
        return $true
    } catch {
        $timer.Stop()
        $message = $_.Exception.Message
        Add-CheckResult -Dod $Dod -CheckName $CheckName -Status "FAIL" -Detail $message
        Write-Host "FAIL ${CheckName}: $message" -ForegroundColor Red
        return $false
    }
}

function Invoke-LoggedCommand {
    param(
        [string]$Exe,
        [string[]]$Arguments,
        [string]$WorkingDirectory,
        [string]$LogPath,
        [string]$Label,
        [int]$SuccessTailLines = 0
    )
    Remove-Item -LiteralPath $LogPath -ErrorAction SilentlyContinue
    Push-Location $WorkingDirectory
    $previous = $ErrorActionPreference
    # Native stderr becomes an error record under Windows PowerShell; the log
    # file still receives it, so suppress only the console duplication.
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & $Exe @Arguments *> $LogPath
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        Pop-Location
    }
    if ($code -ne 0) {
        Write-LogTail -Path $LogPath -Label $Label -Lines 100
        throw "$Label failed with exit code $code"
    }
    if ($SuccessTailLines -gt 0) {
        Write-LogTail -Path $LogPath -Label $Label -Lines $SuccessTailLines
    }
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Missing .env at $Path"
    }
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

function Assert-SafeTestDatabaseUrl {
    param(
        [string]$TestDatabaseUrl,
        [string]$DatabaseUrl
    )
    # Defense in depth for the destructive Go integration helper: it drops and
    # recreates the public schema, so the URL must name a dedicated PostgreSQL
    # test/e2e database and must not be the development database. Error messages
    # intentionally omit credentials and the full URLs.
    if ([string]::IsNullOrWhiteSpace($TestDatabaseUrl)) {
        throw "EZTEST_TEST_DATABASE_URL is not set; integration tests need the dedicated test database"
    }
    if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
        throw "EZTEST_DATABASE_URL is not set, so the test database cannot be checked against the development database"
    }

    $testUri = $null
    if (-not [System.Uri]::TryCreate($TestDatabaseUrl, [System.UriKind]::Absolute, [ref]$testUri)) {
        throw "EZTEST_TEST_DATABASE_URL is not a valid absolute URL"
    }
    $devUri = $null
    if (-not [System.Uri]::TryCreate($DatabaseUrl, [System.UriKind]::Absolute, [ref]$devUri)) {
        throw "EZTEST_DATABASE_URL is not a valid absolute URL"
    }

    foreach ($item in @(
            @{ Name = "EZTEST_TEST_DATABASE_URL"; Uri = $testUri },
            @{ Name = "EZTEST_DATABASE_URL"; Uri = $devUri }
        )) {
        if ($item.Uri.Scheme -ne "postgres" -and $item.Uri.Scheme -ne "postgresql") {
            throw "$($item.Name) must use the postgres scheme"
        }
        if ([string]::IsNullOrWhiteSpace($item.Uri.Host)) {
            throw "$($item.Name) must include a host"
        }
        # pgx applies query parameters over the URL path and host:port, so a
        # URL whose path ends in _test can still connect to the development
        # database via ?dbname= or ?database= (and ?host=/?port= can redirect
        # the connection). Reject the target-changing keys case-insensitively
        # instead of trusting the path. The Go validator checks the effective
        # pgx target as a second layer. Never echo the URL or credentials.
        $query = $item.Uri.Query
        if (-not [string]::IsNullOrWhiteSpace($query)) {
            foreach ($pair in ($query.TrimStart([char]"?")) -split "&") {
                if ([string]::IsNullOrWhiteSpace($pair)) { continue }
                $equals = $pair.IndexOf("=")
                $key = if ($equals -ge 0) { $pair.Substring(0, $equals) } else { $pair }
                $key = [System.Uri]::UnescapeDataString($key).Trim().ToLowerInvariant()
                if ($key -eq "dbname" -or $key -eq "database" -or $key -eq "host" -or $key -eq "port") {
                    throw "$($item.Name) must not override the connection target with the query parameter '$key'; put the host, port and database in the URL itself"
                }
            }
        }
    }

    $testName = [System.Uri]::UnescapeDataString($testUri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($testName)) {
        throw "EZTEST_TEST_DATABASE_URL must include a database name"
    }
    if (-not ($testName.EndsWith("_test", [System.StringComparison]::Ordinal) -or
            $testName.EndsWith("_e2e", [System.StringComparison]::Ordinal))) {
        throw "EZTEST_TEST_DATABASE_URL database '$testName' must end with _test or _e2e"
    }

    $devName = [System.Uri]::UnescapeDataString($devUri.AbsolutePath.TrimStart([char[]]"/"))
    if ([string]::IsNullOrWhiteSpace($devName)) {
        throw "EZTEST_DATABASE_URL must include a database name"
    }

    $testPort = if ($testUri.Port -gt 0) { $testUri.Port } else { 5432 }
    $devPort = if ($devUri.Port -gt 0) { $devUri.Port } else { 5432 }
    $testCanonical = "{0}:{1}/{2}" -f $testUri.Host.ToLowerInvariant(), $testPort, $testName
    $devCanonical = "{0}:{1}/{2}" -f $devUri.Host.ToLowerInvariant(), $devPort, $devName
    if ($testCanonical -eq $devCanonical) {
        throw "EZTEST_TEST_DATABASE_URL points at the same database as EZTEST_DATABASE_URL ($testCanonical); refusing to reset it"
    }
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

function Wait-PortFree {
    param([int]$Port, [int]$TimeoutSeconds = 20)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $listeners = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue
        if (-not $listeners) { return }
        Start-Sleep -Milliseconds 250
    }
    throw "Port $Port is still in use"
}

function Assert-PortFree {
    param([int]$Port)
    $listeners = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue
    if ($listeners) {
        throw "Port $Port is already in use"
    }
}

function Wait-ForHttp {
    param(
        [string]$Url,
        [int]$TimeoutSeconds,
        $Process,
        [string]$Label
    )
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

function Get-MigrationFingerprint {
    $sql = "SELECT count(*)::text || ':' || coalesce(md5(string_agg(version::text || ':' || checksum, ',' ORDER BY version)), '') FROM schema_migrations;"
    return Get-PsqlScalar -Database $e2eDbName -Sql $sql
}

function Start-AcceptanceApi {
    param([int]$RunNumber)
    $outLog = Join-Path $tmpDir "acceptance-api-$RunNumber.out.log"
    $errLog = Join-Path $tmpDir "acceptance-api-$RunNumber.err.log"
    Remove-Item -LiteralPath $outLog, $errLog -ErrorAction SilentlyContinue
    $process = Start-Process -FilePath $apiExe `
        -WorkingDirectory $backendDir `
        -RedirectStandardOutput $outLog `
        -RedirectStandardError $errLog `
        -WindowStyle Hidden `
        -PassThru
    try {
        Wait-ForHttp -Url "$apiBaseUrl/api/health" -TimeoutSeconds 90 -Process $process -Label "API"
    } catch {
        Stop-ProcessTree -Process $process
        throw
    }
    return $process
}

function Assert-Health {
    param($Health)
    if ($Health.data.status -ne "ok") { throw "health status is '$($Health.data.status)', expected 'ok'" }
    if ($Health.data.db -ne "ok") { throw "health db is '$($Health.data.db)', expected 'ok'" }
    if ([string]::IsNullOrWhiteSpace([string]$Health.data.version)) { throw "health response has no version" }
    if ($hasLLMSeed -and -not $Health.data.llm_configured) {
        throw "health reports llm_configured=false although .env provides an LLM key"
    }
}

function Test-AdminAudit {
    $loginPayload = @{ username = $adminUsername; password = $adminPassword } | ConvertTo-Json -Compress
    $login = Invoke-RestMethod -Method Post -Uri "$apiBaseUrl/api/auth/login" `
        -ContentType "application/json" -Body $loginPayload -TimeoutSec 20
    $token = [string]$login.data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "admin login did not return a token" }
    $headers = @{ Authorization = "Bearer $token" }

    $me = Invoke-RestMethod -Uri "$apiBaseUrl/api/auth/me" -Headers $headers -TimeoutSec 20
    if ($me.data.username -ne $adminUsername) { throw "auth/me returned the wrong user" }

    $audit = Invoke-RestMethod -Uri "$apiBaseUrl/api/admin/audit?limit=100" -Headers $headers -TimeoutSec 20
    $items = @($audit.data)
    if ($items.Count -eq 0) { throw "audit list is empty after the E2E run" }

    $actions = @($items | ForEach-Object { [string]$_.action })
    foreach ($expected in @("auth.login_succeeded", "user.created", "sse.test_published")) {
        if ($actions -notcontains $expected) {
            throw "audit list is missing action '$expected'"
        }
    }

    # The environment-seeded LLM configuration is created before any HTTP
    # request exists, so that one entry legitimately has no request_id. Every
    # request-driven action must be correlated.
    $missingRequestIds = @($items | Where-Object {
            [string]::IsNullOrWhiteSpace([string]$_.request_id) -and [string]$_.action -ne "llm.config_created"
        })
    if ($missingRequestIds.Count -gt 0) {
        throw "request-driven audit entries are missing request_id"
    }

    $auditJson = $audit | ConvertTo-Json -Depth 12 -Compress
    $secrets = @($adminPassword, $dbPassword)
    if ($envValues.ContainsKey("llm_key")) { $secrets += [string]$envValues["llm_key"] }
    foreach ($secret in $secrets) {
        if (-not [string]::IsNullOrEmpty($secret) -and $auditJson.Contains($secret)) {
            throw "audit response contains secret material"
        }
    }
    foreach ($forbiddenKey in @('"password"\s*:', '"token"\s*:', '"api_key"\s*:')) {
        if ($auditJson -match $forbiddenKey) {
            throw "audit response contains a credential field"
        }
    }
}

function Test-VersionApiSmoke {
    # P3 global gate: unauthenticated requests are 401, an authenticated list
    # returns the P3 cursor envelope, P3-owned IDs are JSON strings while P2
    # resource/user IDs stay numbers, and an unknown version is 404.
    try {
        Invoke-RestMethod -Uri "$apiBaseUrl/api/versions" -TimeoutSec 20 | Out-Null
        throw "GET /api/versions without a bearer token unexpectedly succeeded"
    } catch {
        $response = $_.Exception.Response
        if ($null -eq $response -or [int]$response.StatusCode -ne 401) {
            throw "GET /api/versions without a bearer token did not return 401: $($_.Exception.Message)"
        }
    }

    $loginPayload = @{ username = $adminUsername; password = $adminPassword } | ConvertTo-Json -Compress
    $login = Invoke-RestMethod -Method Post -Uri "$apiBaseUrl/api/auth/login" `
        -ContentType "application/json" -Body $loginPayload -TimeoutSec 20
    $token = [string]$login.data.token
    if ([string]::IsNullOrWhiteSpace($token)) { throw "admin login did not return a token for the version smoke check" }
    $headers = @{ Authorization = "Bearer $token" }

    $page = Invoke-RestMethod -Uri "$apiBaseUrl/api/versions?limit=1" -Headers $headers -TimeoutSec 20
    foreach ($field in @("data", "next_cursor", "has_more")) {
        if (-not ($page.PSObject.Properties.Name -contains $field)) {
            throw "GET /api/versions response is missing '$field'"
        }
    }
    if ($page.data -isnot [array]) { throw "GET /api/versions 'data' is not an array" }
    foreach ($version in @($page.data)) {
        if ($version.id -isnot [string] -or [string]$version.id -notmatch '^[0-9]+$') {
            throw "version id is not a decimal string: '$($version.id)'"
        }
        if ($version.revision -is [string]) { throw "version revision must be a JSON number" }
        if ($null -ne $version.systemId -and $version.systemId -is [string]) {
            throw "version systemId must stay a JSON number (P2 resource ID)"
        }
        if ($null -ne $version.createdBy -and $version.createdBy -is [string]) {
            throw "version createdBy must stay a JSON number (user ID)"
        }
    }

    try {
        Invoke-RestMethod -Uri "$apiBaseUrl/api/versions/123456789012345678" -Headers $headers -TimeoutSec 20 | Out-Null
        throw "GET /api/versions/{unknown} unexpectedly succeeded"
    } catch {
        $response = $_.Exception.Response
        if ($null -eq $response -or [int]$response.StatusCode -ne 404) {
            throw "GET /api/versions/{unknown} did not return 404: $($_.Exception.Message)"
        }
    }

    Write-Host "Version API smoke OK: 401 unauthenticated, cursor envelope, string P3 IDs/numbers for P2 IDs, 404 unknown version."
}

$exitCode = 0
$apiExe = Join-Path $tmpDir "e2e-api.exe"
$hasLLMSeed = $false

try {
    Write-Step "Loading local configuration"
    $envValues = @{}
    if (-not $StaticOnly) {
        if (-not (Test-Path -LiteralPath (Join-Path $repoRoot ".env"))) {
            throw "Missing .env at the repository root"
        }
        if (-not (Test-Path -LiteralPath (Join-Path $frontendDir "node_modules"))) {
            throw "frontend/node_modules is missing; run pnpm install in frontend first"
        }
    }

    $goExe = Join-Path $repoRoot ".tools\go\bin\go.exe"
    if (-not (Test-Path -LiteralPath $goExe)) { $goExe = "go" }
    $psqlExe = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    if (-not (Test-Path -LiteralPath $psqlExe)) { $psqlExe = "psql" }
    $pnpmExe = ""
    if (-not $StaticOnly) {
        $pnpmExe = (Get-Command pnpm.cmd -ErrorAction SilentlyContinue).Source
        if (-not $pnpmExe) { $pnpmExe = (Get-Command pnpm -ErrorAction SilentlyContinue).Source }
        if (-not $pnpmExe) { throw "pnpm is not available on PATH" }
    }
    $nodeCommand = Get-Command node.exe -ErrorAction SilentlyContinue
    if (-not $nodeCommand) { $nodeCommand = Get-Command node -ErrorAction SilentlyContinue }
    if (-not $nodeCommand) { throw "node is not available on PATH" }
    $nodeExe = $nodeCommand.Source
    $scriptHost = Join-Path $PSHOME "pwsh.exe"
    if (-not (Test-Path -LiteralPath $scriptHost)) { $scriptHost = Join-Path $PSHOME "powershell.exe" }
    if (-not (Test-Path -LiteralPath $scriptHost)) { throw "cannot locate a PowerShell host to run helper scripts" }

    if (-not $StaticOnly) {
        $envValues = Read-DotEnv -Path (Join-Path $repoRoot ".env")
        $databaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_DATABASE_URL"
        $testDatabaseUrl = Get-RequiredValue -Values $envValues -Key "EZTEST_TEST_DATABASE_URL"
        $adminUsername = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_USERNAME"
        $adminPassword = Get-RequiredValue -Values $envValues -Key "EZTEST_ADMIN_PASSWORD"
        $hasLLMSeed = $envValues.ContainsKey("llm_key") -and -not [string]::IsNullOrWhiteSpace([string]$envValues["llm_key"])

        $databaseUri = [System.Uri]$databaseUrl
        if (-not $databaseUri.IsAbsoluteUri) { throw "EZTEST_DATABASE_URL is not an absolute URL" }
        $userInfo = $databaseUri.UserInfo
        $separator = $userInfo.IndexOf(":")
        if ($separator -lt 0) { throw "EZTEST_DATABASE_URL must contain a user and password" }
        $dbUser = [System.Uri]::UnescapeDataString($userInfo.Substring(0, $separator))
        $dbPassword = [System.Uri]::UnescapeDataString($userInfo.Substring($separator + 1))
        $dbHost = $databaseUri.Host
        $dbPort = $databaseUri.Port

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

        # Refuse to run the destructive integration helper against anything that
        # is not a dedicated test/e2e database before any suite is started.
        Assert-SafeTestDatabaseUrl -TestDatabaseUrl $testDatabaseUrl -DatabaseUrl $databaseUrl
    }

    # The migration expectation is always derived from order.txt, never
    # hard-coded. The P4 table list is parsed from the codeintel migrations that
    # order.txt actually lists, so adding a migration updates the expectation.
    $migrationOrder = @(Get-Content -LiteralPath (Join-Path $backendDir "internal\db\migrations\order.txt") | Where-Object { $_.Trim() -ne "" } | ForEach-Object { $_.Trim() })
    $expectedMigrations = $migrationOrder.Count
    if ($expectedMigrations -le 0) { throw "no migrations are listed in order.txt" }
    $p4MigrationNames = @($migrationOrder | Where-Object { $_ -match '^codeintel_.*\.sql$' })
    if ($p4MigrationNames.Count -eq 0) { throw "order.txt lists no P4 codeintel migrations" }
    $p4MigrationTables = @()
    foreach ($migrationName in $p4MigrationNames) {
        $migrationPath = Join-Path $backendDir ("internal\db\migrations\" + $migrationName)
        if (-not (Test-Path -LiteralPath $migrationPath)) {
            throw "order.txt lists a missing migration: $migrationName"
        }
        $migrationSql = Get-Content -LiteralPath $migrationPath -Raw
        foreach ($tableMatch in [regex]::Matches($migrationSql, 'CREATE TABLE IF NOT EXISTS\s+([a-zA-Z0-9_]+)')) {
            $p4MigrationTables += $tableMatch.Groups[1].Value
        }
    }
    $p4MigrationTables = @($p4MigrationTables | Select-Object -Unique)
    if ($p4MigrationTables.Count -eq 0) {
        throw "the codeintel migrations listed in order.txt define no tables"
    }

    if (-not $StaticOnly) {
        # Integration tests open EZTEST_TEST_DATABASE_URL, the dedicated test database.
        $env:EZTEST_TEST_DATABASE_URL = $testDatabaseUrl

        [void](Invoke-Check -Dod "4,5" -CheckName "Go unit tests" -Body {
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-count=1", "./...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-go-unit.log") `
                -Label "go test ./..."
        })

    [void](Invoke-Check -Dod "1,3,4" -CheckName "Go integration tests" -Body {
            $env:EZTEST_TEST_DATABASE_URL = $testDatabaseUrl
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-tags=integration", "-count=1", "./...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-go-integration.log") `
                -Label "go test -tags=integration ./..."
        })

    # P1 DoD 105: the Agent runtime has its own focused suites in addition to
    # the whole-module runs above, so a regression names the Agent package.
    [void](Invoke-Check -Dod "P1-1" -CheckName "Agent unit tests" -Body {
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-count=1", "./internal/agent/...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-agent-unit.log") `
                -Label "go test ./internal/agent/..."
        })

    [void](Invoke-Check -Dod "P1-1" -CheckName "Agent integration tests" -Body {
            $env:EZTEST_TEST_DATABASE_URL = $testDatabaseUrl
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-tags=integration", "-count=1", "./internal/agent/...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-agent-integration.log") `
                -Label "go test -tags=integration ./internal/agent/..."
        })

    # P3 version / requirements / clarification domain, application, repository,
    # handlers and Snowflake-ID suites.
    [void](Invoke-Check -Dod "P3" -CheckName "Version context unit tests" -Body {
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-count=1", "./internal/version/...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-version-unit.log") `
                -Label "go test ./internal/version/..."
        })

    [void](Invoke-Check -Dod "P3" -CheckName "Version context integration tests" -Body {
            $env:EZTEST_TEST_DATABASE_URL = $testDatabaseUrl
            Invoke-LoggedCommand -Exe $goExe -Arguments @("test", "-tags=integration", "-count=1", "./internal/version/...") `
                -WorkingDirectory $backendDir `
                -LogPath (Join-Path $tmpDir "acceptance-version-integration.log") `
                -Label "go test -tags=integration ./internal/version/..."
        })

    [void](Invoke-Check -Dod "5" -CheckName "Frontend lint" -Body {
            Invoke-LoggedCommand -Exe $pnpmExe -Arguments @("lint") `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-frontend-lint.log") `
                -Label "pnpm lint"
        })

    [void](Invoke-Check -Dod "5" -CheckName "Frontend typecheck" -Body {
            Invoke-LoggedCommand -Exe $pnpmExe -Arguments @("typecheck") `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-frontend-typecheck.log") `
                -Label "pnpm typecheck"
        })

    [void](Invoke-Check -Dod "5" -CheckName "Frontend unit tests" -Body {
            Invoke-LoggedCommand -Exe $pnpmExe -Arguments @("test") `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-frontend-test.log") `
                -Label "pnpm test"
        })

    [void](Invoke-Check -Dod "5" -CheckName "Frontend production build" -Body {
            Invoke-LoggedCommand -Exe $pnpmExe -Arguments @("build") `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-frontend-build.log") `
                -Label "pnpm build"
        })

    [void](Invoke-Check -Dod "1,3,P1-1,P1-2,P1-3" -CheckName "Playwright E2E (real Chrome, P0 + Agent console)" -Body {
            Invoke-LoggedCommand -Exe $scriptHost `
                -Arguments @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $scriptsDir "e2e.ps1")) `
                -WorkingDirectory $repoRoot `
                -LogPath (Join-Path $tmpDir "acceptance-e2e.log") `
                -Label "scripts/e2e.ps1" `
                -SuccessTailLines 12
        })

    [void](Invoke-Check -Dod "2,P3" -CheckName "Migration rerun + health/audit/version API" -Body {
            $env:PGPASSWORD = $dbPassword
            $first = $null
            $second = $null
            try {
                # Rebuild from the current sources so the migration rerun does
                # not reuse a stale binary from an earlier acceptance run.
                Invoke-LoggedCommand -Exe $goExe -Arguments @("build", "-o", $apiExe, "./cmd/eztest") `
                    -WorkingDirectory $backendDir `
                    -LogPath (Join-Path $tmpDir "acceptance-api-build.log") `
                    -Label "go build ./cmd/eztest"

                $before = Get-MigrationFingerprint

                $env:EZTEST_ENV = "dev"
                $env:EZTEST_DATABASE_URL = $e2eDatabaseUrl
                $env:EZTEST_HTTP_ADDR = ":$apiPort"
                $env:EZTEST_WEB_ORIGIN = "http://localhost:6868"
                $env:EZTEST_ENABLE_TEST_ENDPOINTS = "true"
                $env:EZTEST_STORAGE_DIR = (Join-Path $tmpDir "e2e-objects")
                $env:EZTEST_ADMIN_USERNAME = $adminUsername
                $env:EZTEST_ADMIN_PASSWORD = $adminPassword
                foreach ($key in @("EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "llm_base_url", "llm_key", "llm_model_name")) {
                    if ($envValues.ContainsKey($key)) {
                        Set-Item -Path ("Env:" + $key) -Value $envValues[$key]
                    }
                }

                Assert-PortFree -Port $apiPort
                $first = Start-AcceptanceApi -RunNumber 1
                $healthFirst = Invoke-RestMethod -Uri "$apiBaseUrl/api/health" -TimeoutSec 20
                Assert-Health -Health $healthFirst
                $afterFirst = Get-MigrationFingerprint
                Stop-ProcessTree -Process $first
                $first = $null
                Wait-PortFree -Port $apiPort

                $second = Start-AcceptanceApi -RunNumber 2
                $healthSecond = Invoke-RestMethod -Uri "$apiBaseUrl/api/health" -TimeoutSec 20
                Assert-Health -Health $healthSecond
                Test-AdminAudit
                Test-VersionApiSmoke
                $afterSecond = Get-MigrationFingerprint

                if ($before -ne $afterFirst -or $afterFirst -ne $afterSecond) {
                    throw "schema_migrations changed across API restarts (migrations are not idempotent)"
                }
                $migrationCount = [int]($afterSecond.Split(":")[0])
                if ($migrationCount -ne $expectedMigrations) {
                    throw "applied migrations=$migrationCount, expected $expectedMigrations"
                }

                # P4 table expectations come from the CREATE TABLE statements of
                # the codeintel migrations order.txt lists (no hard-coded table
                # count), so a new migration cannot silently skip its tables.
                $tableInList = ($p4MigrationTables | ForEach-Object { "'" + $_ + "'" }) -join ","
                $tableSql = "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ($tableInList);"
                $presentTables = [int](Get-PsqlScalar -Database $e2eDbName -Sql $tableSql)
                if ($presentTables -ne $p4MigrationTables.Count) {
                    throw "P4 tables present=$presentTables, expected $($p4MigrationTables.Count) (from order.txt codeintel migrations)"
                }
                Write-Host "Migration rerun OK: $migrationCount migrations, checksums unchanged across two API starts; P4 tables present: $presentTables."
            } finally {
                Stop-ProcessTree -Process $first
                Stop-ProcessTree -Process $second
                Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
            }
        })
    }

    [void](Invoke-Check -Dod "5,P1-4" -CheckName "P0/P1 deliverables and Compose static validation" -Body {
            foreach ($relative in @(
                    "README.md",
                    "docs/openapi/api.yaml",
                    "deploy/Dockerfile.api",
                    "deploy/Dockerfile.web",
                    "deploy/docker-compose.yml"
                )) {
                $deliverable = Join-Path $repoRoot $relative
                if (-not (Test-Path -LiteralPath $deliverable)) {
                    throw "missing P0/P1 deliverable $relative"
                }
            }
            $readme = Get-Content -LiteralPath (Join-Path $repoRoot "README.md") -Raw
            if ($readme -notmatch "EZTEST_OPENAPI_PATH") {
                throw "README raw go-run instructions do not document EZTEST_OPENAPI_PATH"
            }
            $openapi = Get-Content -LiteralPath (Join-Path $repoRoot "docs/openapi/api.yaml") -Raw
            foreach ($needle in @(
                    "/api/agents/sessions",
                    "agent.activity",
                    "reasoning_tokens",
                    # P2 resources contract: the resource paths plus the two
                    # write-only markers proving credentials and storage state
                    # are represented without ever echoing their values.
                    "/api/resources/systems",
                    "/api/resources/sessions",
                    "/api/resources/code-repos",
                    "storageState",
                    "credentialSet"
                )) {
                if ($openapi -notmatch [regex]::Escape($needle)) {
                    throw "docs/openapi/api.yaml is missing $needle"
                }
            }
            $envTemplate = Get-Content -LiteralPath (Join-Path $repoRoot ".env.example") -Raw
            foreach ($agentVar in @(
                    "EZTEST_AGENT_FILE_ROOT",
                    "EZTEST_AGENT_HTTP_ALLOWLIST",
                    "EZTEST_AGENT_TOOL_TIMEOUT_MS",
                    "EZTEST_AGENT_MAX_CONCURRENT_RUNS"
                )) {
                if ($envTemplate -notmatch [regex]::Escape($agentVar)) {
                    throw ".env.example is missing $agentVar"
                }
            }
            Invoke-LoggedCommand -Exe $nodeExe -Arguments @("scripts/validate-compose.mjs") `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-compose-validate.log") `
                -Label "scripts/validate-compose.mjs" `
                -SuccessTailLines 8
        })

    [void](Invoke-Check -Dod "P3" -CheckName "P3 deliverables and OpenAPI/SSE contract" -Body {
            $readme = Get-Content -LiteralPath (Join-Path $repoRoot "README.md") -Raw
            foreach ($needle in @(
                    "/versions",
                    "EZTEST_VERSION_ATTACHMENT_DIR",
                    "EZTEST_SNOWFLAKE_NODE_ID",
                    "version-attachments",
                    "scripts/acceptance.ps1"
                )) {
                if ($readme -notmatch [regex]::Escape($needle)) {
                    throw "README.md is missing P3 keyword $needle"
                }
            }

            $openapi = Get-Content -LiteralPath (Join-Path $repoRoot "docs/openapi/api.yaml") -Raw
            foreach ($needle in @(
                    # P3 method/path families. The full P3 paths are used for
                    # /events and /stream because the bare fragments also match
                    # the earlier P0/P1 endpoints.
                    "/api/versions",
                    "/start-clarification",
                    "/enter-design",
                    "/requirements",
                    "/relations",
                    "/attachments",
                    "/clarification",
                    "/decisions",
                    "/api/versions/{id}/events",
                    "/api/versions/{id}/stream",
                    # P3 contract markers: acknowledge gate, context event,
                    # string IDs and multipart upload.
                    "acknowledgeUnclarified",
                    "clarification.context_updated",
                    "snowflake",
                    "multipart/form-data"
                )) {
                if ($openapi -notmatch [regex]::Escape($needle)) {
                    throw "docs/openapi/api.yaml is missing P3 contract keyword $needle"
                }
            }

            # These markers predate P3 and would pass on the P0/P1 sections
            # alone, so scope them to the P3 objects that own them.
            $p3EventSnapshot = [regex]::Match($openapi, '(?ms)^    VersionEventSnapshot:\r?\n.*?(?=^    [A-Za-z]|\z)')
            if (-not $p3EventSnapshot.Success -or $p3EventSnapshot.Value -notmatch 'snapshot_cursor') {
                throw "docs/openapi/api.yaml is missing VersionEventSnapshot.snapshot_cursor"
            }
            $p3Stream = [regex]::Match($openapi, '(?ms)^  /api/versions/\{id\}/stream:\r?\n.*?(?=^  /|^components:)')
            if (-not $p3Stream.Success -or $p3Stream.Value -notmatch 'Last-Event-ID') {
                throw "docs/openapi/api.yaml is missing the P3 stream Last-Event-ID contract"
            }

            # Full spec §7.1 details.reason table plus the field-level
            # invalid_context_summary reason emitted by context.summary_update.
            foreach ($reason in @(
                    "version_state_conflict",
                    "requirement_state_conflict",
                    "open_questions_remaining",
                    "unclarified_requirements",
                    "requirements_required",
                    "duplicate_relation",
                    "relation_requirement_mismatch",
                    "duplicate_attachment",
                    "revision_conflict",
                    "clarification_session_active",
                    "clarification_session_not_active",
                    "clarification_session_required",
                    "clarification_question_state_conflict",
                    "decision_state_conflict",
                    "agent_busy",
                    "attachment_too_large",
                    "attachment_limit_exceeded",
                    "unsupported_attachment_type",
                    "invalid_attachment_content",
                    "invalid_context_summary",
                    "system_not_found",
                    "system_disabled",
                    "environment_not_found",
                    "environment_system_mismatch",
                    "code_repository_not_found",
                    "code_repository_disabled",
                    "branch_not_found",
                    "path_not_found",
                    "path_not_git_repo",
                    "resource_unavailable",
                    "ai_unavailable"
                )) {
                if ($openapi -notmatch [regex]::Escape($reason)) {
                    throw "docs/openapi/api.yaml is missing P3 error reason $reason"
                }
            }

            # Full spec §6.11 event catalog plus the P3 clarification.context_updated,
            # scoped to the P3 VersionEventType enum so earlier-phase event types
            # cannot satisfy the check.
            $p3EventCatalog = [regex]::Match($openapi, '(?ms)^    VersionEventType:\r?\n.*?(?=^    [A-Za-z])')
            if (-not $p3EventCatalog.Success) {
                throw "docs/openapi/api.yaml is missing the VersionEventType event catalog"
            }
            foreach ($eventType in @(
                    "version.created",
                    "version.updated",
                    "version.status_changed",
                    "requirement.added",
                    "requirement.updated",
                    "requirement.clarified",
                    "requirement.reopened",
                    "requirement.deleted",
                    "requirement.relation_added",
                    "requirement.relation_updated",
                    "requirement.relation_removed",
                    "clarification.session_started",
                    "clarification.session_completed",
                    "clarification.question_updated",
                    "clarification.context_updated",
                    "decision.proposed",
                    "decision.decided",
                    "attachment.added",
                    "attachment.removed",
                    "agent.activity"
                )) {
                if ($p3EventCatalog.Value -notmatch [regex]::Escape($eventType)) {
                    throw "docs/openapi/api.yaml VersionEventType is missing version event type $eventType"
                }
            }

            $envTemplate = Get-Content -LiteralPath (Join-Path $repoRoot ".env.example") -Raw
            foreach ($versionVar in @("EZTEST_VERSION_ATTACHMENT_DIR", "EZTEST_SNOWFLAKE_NODE_ID")) {
                if ($envTemplate -notmatch [regex]::Escape($versionVar)) {
                    throw ".env.example is missing $versionVar"
                }
            }
            $compose = Get-Content -LiteralPath (Join-Path $repoRoot "deploy/docker-compose.yml") -Raw
            foreach ($composeNeedle in @("EZTEST_VERSION_ATTACHMENT_DIR", "attachments:/data/version-attachments")) {
                if ($compose -notmatch [regex]::Escape($composeNeedle)) {
                    throw "deploy/docker-compose.yml is missing P3 attachment setting $composeNeedle"
                }
            }
            if ($envTemplate -notmatch [regex]::Escape("version-attachments")) {
                throw ".env.example is missing the version-attachments default path"
            }
        })

    [void](Invoke-Check -Dod "P4" -CheckName "P4 deliverables, OpenAPI, fake LLM and migration contract" -Body {
            $readme = Get-Content -LiteralPath (Join-Path $repoRoot "README.md") -Raw
            foreach ($needle in @(
                    "EZTEST_CODEINTEL_MAX_CHANGED_FILES",
                    "EZTEST_CODEINTEL_PROMPT_VERSION",
                    "EZTEST_CODEINTEL_MAX_DIFF_BYTES",
                    "EZTEST_ACCEPTANCE_P4_REPO",
                    "/api/code/repos",
                    "/api/versions/{id}/code",
                    "scripts/fake-llm.p4.test.cjs",
                    "scripts/acceptance-codeintel-llm.ps1",
                    "code.impact_computed",
                    "code.analyze",
                    "read-only"
                )) {
                if ($readme -notmatch [regex]::Escape($needle)) {
                    throw "README.md is missing P4 keyword $needle"
                }
            }

            $openapi = Get-Content -LiteralPath (Join-Path $repoRoot "docs/openapi/api.yaml") -Raw
            foreach ($needle in @(
                    # Both P4 API prefixes and every documented path family.
                    "/api/code/repos",
                    "/api/code/repos/{repoId}/index",
                    "/api/code/repos/{repoId}/index-runs/latest",
                    "/api/code/repos/{repoId}/index-runs/{runId}",
                    "/api/code/repos/{repoId}/tree",
                    "/api/code/repos/{repoId}/file",
                    "/api/code/repos/{repoId}/diff",
                    "/api/code/repos/{repoId}/diff/file",
                    "/api/code/repos/{repoId}/facts",
                    "/api/code/repos/{repoId}/search",
                    "/api/code/repos/{repoId}/commits/{sha}",
                    "/api/versions/{versionId}/code/changes",
                    "/api/versions/{versionId}/code/analyses",
                    "/api/versions/{versionId}/code/analyses/latest",
                    "/api/versions/{versionId}/code/analyses/{analysisId}",
                    "/api/versions/{versionId}/code/analyses/{analysisId}/candidates/{candidateId}",
                    "/api/versions/{versionId}/code/anchors",
                    "/api/versions/{versionId}/code/anchors/{anchorId}",
                    # Wire-contract fields the frontend depends on.
                    "changeSetId",
                    "changeSummary",
                    "factVersions",
                    "nextCursor",
                    "headBlobHash"
                )) {
                if ($openapi -notmatch [regex]::Escape($needle)) {
                    throw "docs/openapi/api.yaml is missing P4 contract keyword $needle"
                }
            }

            # P4 schema names must exist as their own component entries; a bare
            # mention inside another description cannot satisfy this check.
            foreach ($schemaName in @(
                    "RepoSummary",
                    "IndexRun",
                    "ChangeList",
                    "FileDiff",
                    "FactsPage",
                    "Analysis",
                    "Candidate",
                    "Anchor",
                    "CodeEntity",
                    "CodeErrorReason"
                )) {
                if ($openapi -notmatch ("(?m)^    " + $schemaName + ":")) {
                    throw "docs/openapi/api.yaml is missing the P4 schema $schemaName"
                }
            }

            # The reason needles must live in the P4 reason enum itself, not in
            # an unrelated P3 table that happens to mention the same word.
            $p4ReasonCatalog = [regex]::Match($openapi, '(?ms)^    CodeErrorReason:\r?\n.*?(?=^    [A-Za-z])')
            if (-not $p4ReasonCatalog.Success) {
                throw "docs/openapi/api.yaml is missing the CodeErrorReason reason catalog"
            }
            foreach ($reason in @(
                    "code_repository_not_bound",
                    "baseline_commit_missing",
                    "invalid_head",
                    "not_changed",
                    "unsupported_content",
                    "index_in_progress",
                    "analysis_in_progress",
                    "analysis_queue_full",
                    "analysis_not_done",
                    "anchor_duplicate",
                    "anchor_not_found",
                    "unsupported_subject_kind",
                    "path_not_git_repo",
                    "git_unavailable"
                )) {
                if ($p4ReasonCatalog.Value -notmatch [regex]::Escape($reason)) {
                    throw "docs/openapi/api.yaml CodeErrorReason is missing P4 reason $reason"
                }
            }

            # P4 event types must be in the version event catalog; the P3 check
            # above already pins the earlier catalog entries.
            $p4EventCatalog = [regex]::Match($openapi, '(?ms)^    VersionEventType:\r?\n.*?(?=^    [A-Za-z])')
            if (-not $p4EventCatalog.Success) {
                throw "docs/openapi/api.yaml is missing the VersionEventType event catalog"
            }
            foreach ($eventType in @("code.changed", "code.impact_computed")) {
                if ($p4EventCatalog.Value -notmatch [regex]::Escape($eventType)) {
                    throw "docs/openapi/api.yaml VersionEventType is missing P4 event type $eventType"
                }
            }

            # The P4 env template keys are part of the deliverable; no values
            # are read or reported here.
            $envTemplate = Get-Content -LiteralPath (Join-Path $repoRoot ".env.example") -Raw
            foreach ($codeintelVar in @(
                    "EZTEST_CODEINTEL_MAX_CHANGED_FILES",
                    "EZTEST_CODEINTEL_MAX_FILE_BYTES",
                    "EZTEST_CODEINTEL_MAX_INDEX_FILES",
                    "EZTEST_CODEINTEL_ANALYSIS_TIMEOUT_MS",
                    "EZTEST_CODEINTEL_MAX_CONCURRENT_ANALYSES",
                    "EZTEST_CODEINTEL_PROMPT_VERSION",
                    "EZTEST_CODEINTEL_MAX_EXCERPT_LINES",
                    "EZTEST_CODEINTEL_MAX_DIFF_BYTES"
                )) {
                if ($envTemplate -notmatch [regex]::Escape($codeintelVar)) {
                    throw ".env.example is missing P4 variable $codeintelVar"
                }
            }

            # P4 migrations must be the tail of order.txt and their tables are
            # parsed dynamically (no hard-coded 16 or table count).
            if ($migrationOrder.Count -lt 2 -or
                $migrationOrder[$migrationOrder.Count - 2] -notmatch '^codeintel_' -or
                $migrationOrder[$migrationOrder.Count - 1] -notmatch '^codeintel_') {
                throw "order.txt must append the P4 codeintel migrations at its tail"
            }
            if ($p4MigrationTables.Count -lt 1) {
                throw "the P4 codeintel migrations listed in order.txt define no tables"
            }

            # Parse the OpenAPI document with the repository's existing YAML
            # dependency (frontend/node_modules/yaml); this catches a broken
            # YAML document that the regex needles above would miss. StaticOnly
            # does not require frontend/node_modules, so fail with an actionable
            # message instead of a bare node module-resolution error.
            $yamlModule = Join-Path $frontendDir "node_modules\yaml"
            if (-not (Test-Path -LiteralPath $yamlModule)) {
                throw "frontend/node_modules/yaml is missing; run 'pnpm install' in frontend (the OpenAPI YAML parse uses that dependency)"
            }
            # Windows PowerShell 5.1 strips embedded double quotes from native
            # arguments, so the JavaScript program uses single-quoted strings.
            $openapiParseCode = "import { readFileSync } from 'node:fs'; import { parse } from 'yaml'; const doc = parse(readFileSync(process.argv[1], 'utf8')); if (!doc || typeof doc !== 'object' || !doc.paths || !doc.components || !doc.components.schemas) { throw new Error('invalid OpenAPI document'); } const p4 = Object.keys(doc.paths).filter((p) => p.startsWith('/api/code/repos') || p.includes('/code/changes') || p.includes('/code/analyses') || p.includes('/code/anchors')); if (p4.length < 18) { throw new Error('expected at least 18 P4 paths, found ' + p4.length); } console.log('OpenAPI parsed: ' + Object.keys(doc.paths).length + ' paths, ' + Object.keys(doc.components.schemas).length + ' schemas, ' + p4.length + ' P4 paths');"
            Invoke-LoggedCommand -Exe $nodeExe `
                -Arguments @("--input-type=module", "-e", $openapiParseCode, (Join-Path $repoRoot "docs/openapi/api.yaml")) `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-openapi-parse.log") `
                -Label "OpenAPI YAML parse" `
                -SuccessTailLines 2

            # The P4 fake-LLM contract test is a gate, not an optional script.
            # Its fixture is deterministic and rebuilt only when absent.
            $fixtureDir = Join-Path $tmpDir "p4-e2e-repo"
            if (-not (Test-Path -LiteralPath (Join-Path $fixtureDir ".git"))) {
                Invoke-LoggedCommand -Exe $scriptHost `
                    -Arguments @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $scriptsDir "fixture-codeintel-repo.ps1")) `
                    -WorkingDirectory $repoRoot `
                    -LogPath (Join-Path $tmpDir "acceptance-p4-fixture.log") `
                    -Label "scripts/fixture-codeintel-repo.ps1" `
                    -SuccessTailLines 4
            }
            Invoke-LoggedCommand -Exe $nodeExe `
                -Arguments @("scripts/fake-llm.p4.test.cjs") `
                -WorkingDirectory $repoRoot `
                -LogPath (Join-Path $tmpDir "acceptance-fake-llm-p4.log") `
                -Label "node scripts/fake-llm.p4.test.cjs" `
                -SuccessTailLines 8
        })

    Add-CheckResult -Dod "risk" -CheckName "Compose container run" -Status "SKIPPED" `
        -Detail "No Docker on this host; Compose remains static-validation only (Task 15)."
    Add-CheckResult -Dod "4" -CheckName "Real LLM acceptance" -Status "SKIPPED" `
        -Detail "Gated: run scripts/acceptance-llm.ps1 with EZTEST_RUN_REAL_LLM=1."
    Add-CheckResult -Dod "P1-1" -CheckName "Real Agent LLM acceptance" -Status "SKIPPED" `
        -Detail "Gated: run scripts/acceptance-agent-llm.ps1 with EZTEST_RUN_REAL_LLM=1."
    Add-CheckResult -Dod "P3" -CheckName "P3 real LLM acceptance" -Status "SKIPPED" `
        -Detail "Gated: manual P3 acceptance (spec §10.7) with EZTEST_ACCEPTANCE_REAL_LLM=1; screenshots under .tmp/p3-acceptance/."
    Add-CheckResult -Dod "P4" -CheckName "P4 real LLM acceptance" -Status "SKIPPED" `
        -Detail "Gated: run scripts/acceptance-codeintel-llm.ps1 with EZTEST_RUN_REAL_LLM=1; results under .tmp/p4-real-acceptance.json."

    Write-Step "P0-P4 DoD acceptance summary"
    $table = $checkResults | Format-Table -AutoSize -Wrap | Out-String -Width 200
    Write-Host $table

    # The P1 exit criteria are proven by suites spread across the gate (the
    # gated real-LLM script is the only opt-in piece), so map each criterion to
    # its evidence explicitly instead of hiding the link in a check name.
    Write-Step "P1 DoD mapping (implementation-phases.md L105-L108)"
    $p1DodMapping = @(
        [pscustomobject][ordered]@{
            DoD         = "P1-1"
            Source      = "L105"
            Requirement = "Diagnostic Agent completes multi-turn conversation + tool calls, live in the UI and replayable"
            Evidence    = "Agent unit/integration suites; Playwright agent-console E2E (Task 16); OpenAPI /api/agents + agent.activity"
        },
        [pscustomobject][ordered]@{
            DoD         = "P1-2"
            Source      = "L106"
            Requirement = "A run can be aborted with correct events/state; tool errors and timeouts do not take the service down"
            Evidence    = "Run abort and tool executor suites; agent-console E2E stop flow"
        },
        [pscustomobject][ordered]@{
            DoD         = "P1-3"
            Source      = "L107"
            Requirement = "Agent configuration (system prompt/model) changes take effect on the next run"
            Evidence    = "Profile domain/application suites; agent-console E2E prompt edit flow"
        },
        [pscustomobject][ordered]@{
            DoD         = "P1-4"
            Source      = "L108"
            Requirement = "Event/tool contract frozen at V1 for compatible extension"
            Evidence    = "Agent activity domain/application suites; docs/openapi/api.yaml agent schemas"
        }
    )
    $p1Table = $p1DodMapping | Format-Table -AutoSize -Wrap | Out-String -Width 200
    Write-Host $p1Table

    # P3 exit criteria (spec §10.6) mapped to the evidence collected above; the
    # real-LLM manual acceptance remains the only gated piece. The three P3
    # Playwright specs (frontend/e2e/versions*.spec.ts) are part of scripts/e2e.ps1
    # and passed in T19 (17 total / 3 P3).
    Write-Step "P3 DoD mapping (spec §10.6)"
    $p3DodMapping = @(
        [pscustomobject][ordered]@{
            DoD         = "P3-1"
            Requirement = "A version records 3+ requirements; AI multi-turn questioning; human answers/edits; requirements/relations/decisions persisted"
            Evidence    = "Version unit/integration suites; workbench/clarification component tests; P3 real-LLM acceptance (gated); P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-2"
            Requirement = "Requirement confirmation and Version design entry follow the state rules (draft -> clarifying -> designing; draft -> clarified)"
            Evidence    = "Domain state-machine tests; HTTP 200/409/422 OpenAPI contract; enter-design acknowledge component test; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-3"
            Requirement = "Refresh/reconnect loses neither the conversation nor the draft requirements"
            Evidence    = "Workbench reload + SSE reconnect component tests; flat /events snapshot + version stream in OpenAPI; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-4"
            Requirement = "The requirement relation graph renders correctly"
            Evidence    = "RelationGraph component tests; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-5"
            Requirement = "Manual entry/confirmation still works when AI is unavailable"
            Evidence    = "AI-unavailable component test; 503 ai_unavailable contract; Version API smoke; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-6"
            Requirement = "Version detail container, 9-tab workbench and overview skeleton"
            Evidence    = "VersionTabs/VersionDetailPage tab + ?tab deep-link component tests; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-7"
            Requirement = "Attachments and multimodal context-recon within limits"
            Evidence    = "Attachment multipart upload/download integration tests; recon multimodal parts application test; OpenAPI attachment paths; P3 Playwright E2E in frontend/e2e/versions*.spec.ts (T19: 3 P3 specs passed)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-8"
            Requirement = "Optional P2 resource bindings with validation and degraded display"
            Evidence    = "Create/bind validation tests; bindings.available=false contract; OpenAPI VersionBindings"
        },
        [pscustomobject][ordered]@{
            DoD         = "P3-9"
            Requirement = "Snowflake IDs and decimal-string JSON serialization"
            Evidence    = "Snowflake unit tests; string-ID serialization tests; acceptance Version API smoke; OpenAPI string schemas"
        }
    )
    $p3Table = $p3DodMapping | Format-Table -AutoSize -Wrap | Out-String -Width 200
    Write-Host $p3Table

    # P4 exit criteria (spec §10.6) mapped to the evidence collected above;
    # the gated real-LLM run remains the only opt-in piece.
    Write-Step "P4 DoD mapping (spec §10.6)"
    $p4DodMapping = @(
        [pscustomobject][ordered]@{
            DoD         = "P4-1"
            Requirement = "Any-language repository: file-level diff, AI interface/route/page facts with evidence"
            Evidence    = "codeintel domain/gitx suites; P4 OpenAPI tree/file/diff/facts paths; P4 fake-LLM test; gated acceptance-codeintel-llm.ps1"
        },
        [pscustomobject][ordered]@{
            DoD         = "P4-2"
            Requirement = "Same-commit analyses reuse the cache; re-analysis creates a new version without overwriting"
            Evidence    = "codeintel application cache/version suites; gated script asserts reused=true with identical normalized candidates, a second same-base version with stats.cacheHits>0, and force analysisVersion+1"
        },
        [pscustomobject][ordered]@{
            DoD         = "P4-3"
            Requirement = "Impact candidates carry reason/confidence/evidence, can be toggled, and unmapped changes are visible"
            Evidence    = "codeintel impact/candidate suites; P4 Analysis/Candidate/Unmapped OpenAPI schemas; gated script checks candidate reason/confidence/evidence and requires >=1 unmapped (or explicit -AllowNoUnmapped)"
        },
        [pscustomobject][ordered]@{
            DoD         = "P4-4"
            Requirement = "AI failure degrades without blocking the main flow"
            Evidence    = "codeintel degraded suites; P4 Analysis.degraded contract; gated script skips AI-only assertions when degraded"
        }
    )
    $p4Table = $p4DodMapping | Format-Table -AutoSize -Wrap | Out-String -Width 200
    Write-Host $p4Table

    $failed = @($checkResults | Where-Object { $_.Result -eq "FAIL" })
    $passed = @($checkResults | Where-Object { $_.Result -eq "PASS" })
    $skipped = @($checkResults | Where-Object { $_.Result -eq "SKIPPED" })
    Write-Host ("Result: {0} passed, {1} failed, {2} skipped" -f $passed.Count, $failed.Count, $skipped.Count)
    if ($failed.Count -gt 0) {
        $exitCode = 1
        Write-Host "ACCEPTANCE FAILED" -ForegroundColor Red
    } else {
        Write-Host "ACCEPTANCE PASSED" -ForegroundColor Green
    }
} catch {
    Write-Host ""
    Write-Host "ACCEPTANCE FAILED: $($_.Exception.Message)" -ForegroundColor Red
    $exitCode = 1
    if ($null -ne $checkResults -and $checkResults.Count -gt 0) {
        $checkResults | Format-Table -AutoSize -Wrap | Out-String -Width 200 | Write-Host
    }
} finally {
    Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
}

exit $exitCode
