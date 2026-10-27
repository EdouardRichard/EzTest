#requires -Version 5.1
<#
.SYNOPSIS
    Runs the P0 acceptance gate for EzTest.

.DESCRIPTION
    Executes the Go unit and integration suites, the frontend lint/typecheck/test/build
    gate, the Playwright E2E suite, a repeated migration run and the health/audit
    assertions. The Task 15 deliverables are checked (README, OpenAPI, deploy files)
    and the Compose file is statically validated with node. Container execution is
    reported as SKIPPED because this host has no Docker. Prints a DoD mapping table
    and exits non-zero on any failure.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param()

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

$exitCode = 0
$apiExe = Join-Path $tmpDir "e2e-api.exe"
$hasLLMSeed = $false

try {
    Write-Step "Loading local configuration"
    if (-not (Test-Path -LiteralPath (Join-Path $repoRoot ".env"))) {
        throw "Missing .env at the repository root"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $frontendDir "node_modules"))) {
        throw "frontend/node_modules is missing; run pnpm install in frontend first"
    }

    $goExe = Join-Path $repoRoot ".tools\go\bin\go.exe"
    if (-not (Test-Path -LiteralPath $goExe)) { $goExe = "go" }
    $psqlExe = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
    if (-not (Test-Path -LiteralPath $psqlExe)) { $psqlExe = "psql" }
    $pnpmExe = (Get-Command pnpm.cmd -ErrorAction SilentlyContinue).Source
    if (-not $pnpmExe) { $pnpmExe = (Get-Command pnpm -ErrorAction SilentlyContinue).Source }
    if (-not $pnpmExe) { throw "pnpm is not available on PATH" }
    $nodeCommand = Get-Command node.exe -ErrorAction SilentlyContinue
    if (-not $nodeCommand) { $nodeCommand = Get-Command node -ErrorAction SilentlyContinue }
    if (-not $nodeCommand) { throw "node is not available on PATH" }
    $nodeExe = $nodeCommand.Source
    $scriptHost = Join-Path $PSHOME "pwsh.exe"
    if (-not (Test-Path -LiteralPath $scriptHost)) { $scriptHost = Join-Path $PSHOME "powershell.exe" }
    if (-not (Test-Path -LiteralPath $scriptHost)) { throw "cannot locate a PowerShell host to run scripts/e2e.ps1" }

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

    # Refuse to run the destructive integration helper against anything that is
    # not a dedicated test/e2e database before any suite is started.
    Assert-SafeTestDatabaseUrl -TestDatabaseUrl $testDatabaseUrl -DatabaseUrl $databaseUrl

    $expectedMigrations = @(Get-Content -LiteralPath (Join-Path $backendDir "internal\db\migrations\order.txt") | Where-Object { $_.Trim() -ne "" }).Count
    if ($expectedMigrations -le 0) { throw "no migrations are listed in order.txt" }

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

    [void](Invoke-Check -Dod "1,3" -CheckName "Playwright E2E (real Chrome)" -Body {
            Invoke-LoggedCommand -Exe $scriptHost `
                -Arguments @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $scriptsDir "e2e.ps1")) `
                -WorkingDirectory $repoRoot `
                -LogPath (Join-Path $tmpDir "acceptance-e2e.log") `
                -Label "scripts/e2e.ps1" `
                -SuccessTailLines 12
        })

    [void](Invoke-Check -Dod "2" -CheckName "Migration rerun + health/audit" -Body {
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
                $afterSecond = Get-MigrationFingerprint

                if ($before -ne $afterFirst -or $afterFirst -ne $afterSecond) {
                    throw "schema_migrations changed across API restarts (migrations are not idempotent)"
                }
                $migrationCount = [int]($afterSecond.Split(":")[0])
                if ($migrationCount -ne $expectedMigrations) {
                    throw "applied migrations=$migrationCount, expected $expectedMigrations"
                }
                Write-Host "Migration rerun OK: $migrationCount migrations, checksums unchanged across two API starts."
            } finally {
                Stop-ProcessTree -Process $first
                Stop-ProcessTree -Process $second
                Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue
            }
        })

    [void](Invoke-Check -Dod "5" -CheckName "P0 deliverables and Compose static validation" -Body {
            foreach ($relative in @(
                    "README.md",
                    "docs/openapi/p0.yaml",
                    "deploy/Dockerfile.api",
                    "deploy/Dockerfile.web",
                    "deploy/docker-compose.yml"
                )) {
                $deliverable = Join-Path $repoRoot $relative
                if (-not (Test-Path -LiteralPath $deliverable)) {
                    throw "missing P0 deliverable $relative"
                }
            }
            $readme = Get-Content -LiteralPath (Join-Path $repoRoot "README.md") -Raw
            if ($readme -notmatch "EZTEST_OPENAPI_PATH") {
                throw "README raw go-run instructions do not document EZTEST_OPENAPI_PATH"
            }
            Invoke-LoggedCommand -Exe $nodeExe -Arguments @("scripts/validate-compose.mjs") `
                -WorkingDirectory $frontendDir `
                -LogPath (Join-Path $tmpDir "acceptance-compose-validate.log") `
                -Label "scripts/validate-compose.mjs" `
                -SuccessTailLines 8
        })

    Add-CheckResult -Dod "risk" -CheckName "Compose container run" -Status "SKIPPED" `
        -Detail "No Docker on this host; Compose remains static-validation only (Task 15)."
    Add-CheckResult -Dod "4" -CheckName "Real LLM acceptance" -Status "SKIPPED" `
        -Detail "Gated: run scripts/acceptance-llm.ps1 with EZTEST_RUN_REAL_LLM=1."

    Write-Step "P0 DoD acceptance summary"
    $table = $checkResults | Format-Table -AutoSize -Wrap | Out-String -Width 200
    Write-Host $table

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
