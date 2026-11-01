#requires -Version 5.1
<#
.SYNOPSIS
    EzTest 0.2 P8 resilience / long-run acceptance driver (S9-S10).

.DESCRIPTION
    Boots the real P8 stack on a clone of the P7 acceptance database and runs
    the resilience/hardening targets of
    docs/superpowers/specs/2026-11-02-eztest-v02-p8-hardening-acceptance-design.md
    sections 11.3 (S9/S10) and 11.4:

      S9  long context
          context.window_tokens=8000 + agent.tool_result_max_bytes=8192,
          30 rapid clarification user messages, rolling conversation summary,
          context.compacted event, no context_overflow terminal run, and a
          latest-run context packet that contains the rolling summary.

      S10 version
          /api/health reports 0.2.0-p8, /api/health/ready exposes
          inflight_requests/inflight_work.

      S10 rate limit
          security.rate_limit_per_minute=30, 40 GET /api/versions requests,
          at least one 429 carrying Retry-After, then restore the old value.

      S10 retention
          one >90d and one recent event row + audit_log row inserted as test
          data, API restart, startup retention pass deletes only the old rows
          and writes audit_log(action=retention.cleanup).

      S10 long execution
          attempt to have the real design agent produce >=12 cases and run
          them in two waves. When the real agent cannot deterministically
          produce 12 cases, the script runs scripts/p3-acceptance.ps1 against
          its own isolated database/ports and records that deterministic
          six-case execution evidence instead. No case/run rows are faked.

    A product defect is a FAIL with evidence. The script never patches
    product code and never writes product rows to fake a scenario. The first
    run may legitimately FAIL.

    This script is PowerShell 5.1 compatible. It never prints a password,
    bearer token, LLM key or executor/render token.

.PARAMETER ApiPort
    API port; default 13333. The API is restarted once for the retention check.

.PARAMETER RendererPort
    PDF renderer port; default 13337.

.PARAMETER ExecutorAPort
    Executor A port; default 13345.

.PARAMETER ExecutorBPort
    Executor B port; default 13346.

.PARAMETER OrdersPort
    Orders target fixture port; default 14080.

.PARAMETER PaymentsPort
    Payments target fixture port; default 14081.

.PARAMETER DatabaseName
    Acceptance database; default eztest_v02_p8.

.PARAMETER TemplateDatabase
    Clone source database; default eztest_v02_p7.

.PARAMETER SkipDatabaseReset
    Reuse the existing acceptance database instead of cloning the template.

.PARAMETER Scenarios
    Optional subset:
      S9                    long context
      S10                   all hardening targets
      S10-VERSION           version/readiness
      S10-RATE-LIMIT        rate limit / Retry-After
      S10-RETENTION         event/audit retention
      S10-LONG-EXECUTION    12-case attempt + p3 fallback
    Commas are accepted, for example -Scenarios S10-VERSION,S10-RATE-LIMIT.

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-resilience.ps1

.EXAMPLE
    pwsh scripts/p8-resilience.ps1 -SkipDatabaseReset -Scenarios S9,S10-RATE-LIMIT

.NOTES
    Evidence: .tmp/p8-resilience/ (scenarios.txt, acceptance.log, logs, JSON,
    resilience.md). Exit code 0 only when every attempted check passed.
#>
[CmdletBinding()]
param(
    [int]$ApiPort = 13333,
    [int]$RendererPort = 13337,
    [int]$ExecutorAPort = 13345,
    [int]$ExecutorBPort = 13346,
    [int]$OrdersPort = 14080,
    [int]$PaymentsPort = 14081,
    [string]$DatabaseName = "eztest_v02_p8",
    [string]$TemplateDatabase = "eztest_v02_p7",
    [switch]$SkipDatabaseReset,
    [string[]]$Scenarios = @()
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$script:repoRoot = Split-Path -Parent $PSScriptRoot
$script:evidenceDir = Join-Path $script:repoRoot ".tmp\p8-resilience"
$script:runSuffix = (Get-Date -Format "MMddHHmmss") + "-" + (Get-Random -Minimum 1000 -Maximum 9999)
$script:startedAt = Get-Date
$script:ids = @{}
$script:settingsOld = @{}
$script:settingsMissing = [System.Collections.Generic.List[string]]::new()
$script:apiRecord = $null
$script:databaseUrl = ""
$script:binaryPath = ""
$script:startedProcesses = $false
$script:bootstrapped = $false
$script:cleanupRestoreFailures = 0
$script:scenarioFilter = @()
$script:allowedScenarios = @("S9", "S10", "S10-VERSION", "S10-RATE-LIMIT", "S10-RETENTION", "S10-LONG-EXECUTION")

. (Join-Path $PSScriptRoot "p8-fixtures.ps1")

$script:context = New-P8Context -RepoRoot $script:repoRoot -EvidenceDir $script:evidenceDir
Set-P8Context -Context $script:context
Initialize-P8Evidence -EvidenceDir $script:evidenceDir | Out-Null
Set-Content -Path (Join-Path $script:evidenceDir "acceptance.log") -Value "" -Encoding utf8
Set-Content -Path (Join-Path $script:evidenceDir "scenarios.txt") -Value ("# EzTest P8 resilience acceptance ($(Get-Date -Format o))") -Encoding utf8

foreach ($item in @($Scenarios)) {
    foreach ($part in [string]$item.Split(",")) {
        $name = $part.Trim().ToUpperInvariant()
        if ($name -eq "") { continue }
        if ($script:allowedScenarios -notcontains $name) {
            Write-Host "unknown scenario '$name'; allowed: $($script:allowedScenarios -join ', ')" -ForegroundColor Red
            exit 2
        }
        $script:scenarioFilter += $name
    }
}

# ---------------------------------------------------------------------------
# small shared helpers
# ---------------------------------------------------------------------------

function Test-P8ScenarioSelected {
    param([Parameter(Mandatory = $true)][string]$Name)
    if ($script:scenarioFilter.Count -eq 0) { return $true }
    if ($script:scenarioFilter -contains $Name) { return $true }
    if ($script:scenarioFilter -contains "S10" -and $Name.StartsWith("S10-")) { return $true }
    return $false
}

function Get-P8ShortText {
    param([string]$Text, [int]$MaxLength = 400)
    if ([string]::IsNullOrWhiteSpace($Text)) { return "" }
    $single = ($Text -replace '\s+', ' ').Trim()
    if ($single.Length -le $MaxLength) { return $single }
    return $single.Substring(0, $MaxLength) + "..."
}

function Start-P8TimedScenario {
    return [pscustomobject]@{
        Name    = ""
        Checks  = [System.Collections.Generic.List[object]]::new()
        Started = Get-Date
    }
}

function Complete-P8TimedScenario {
    param(
        $Scenario,
        [string]$Summary = ""
    )
    $duration = [int]((Get-Date) - $Scenario.Started).TotalSeconds
    Write-P8ScenarioSummary -Name $Scenario.Name -Checks $Scenario.Checks -Summary $Summary -DurationSeconds $duration | Out-Null
}

function Get-P8Health {
    param([string]$Path = "/api/health", [switch]$Ready)
    if ($Ready) { return Invoke-P8Api -Method "GET" -Path $Path -Token "" -TimeoutSec 30 }
    return Invoke-P8Api -Method "GET" -Path $Path -Token "" -TimeoutSec 30
}

function Get-P8HeaderValue {
    param($Headers, [string]$Name)
    if ($null -eq $Headers) { return "" }
    foreach ($candidate in @($Name, $Name.ToLowerInvariant())) {
        try {
            $value = $Headers[$candidate]
            if ($null -ne $value -and -not [string]::IsNullOrWhiteSpace([string]$value)) { return [string]$value }
        } catch { }
    }
    try {
        $properties = $Headers.PSObject.Properties
        if ($null -ne $properties -and $null -ne $properties[$Name]) { return [string]$properties[$Name].Value }
    } catch { }
    try {
        if ($null -ne $Headers.RetryAfter) { return [string]$Headers.RetryAfter }
    } catch { }
    try {
        $values = $null
        if ($Headers.TryGetValues($Name, [ref]$values)) {
            return (($values | ForEach-Object { [string]$_ }) -join ",")
        }
    } catch { }
    return ""
}

function Read-P8ExceptionBody {
    param($Exception)
    if ($null -eq $Exception) { return "" }
    try {
        if ($null -ne $Exception.ErrorDetails -and -not [string]::IsNullOrWhiteSpace($Exception.ErrorDetails.Message)) {
            return [string]$Exception.ErrorDetails.Message
        }
    } catch { }
    try {
        $response = $Exception.Response
        if ($null -ne $response) {
            $stream = $response.GetResponseStream()
            if ($null -ne $stream) {
                $reader = New-Object System.IO.StreamReader($stream)
                try { return $reader.ReadToEnd() } finally { $reader.Dispose() }
            }
        }
    } catch { }
    return [string]$Exception.Message
}

function Invoke-P8HttpProbe {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [int]$TimeoutSec = 20
    )
    $context = Get-P8Context
    $headers = @{}
    if (-not [string]::IsNullOrWhiteSpace([string]$context.Token)) {
        $headers["Authorization"] = "Bearer $($context.Token)"
    }
    try {
        $response = Invoke-WebRequest -Uri ($context.BaseUrl.TrimEnd("/") + $Path) -Method "GET" -Headers $headers -UseBasicParsing -TimeoutSec $TimeoutSec
        return [pscustomobject]@{
            Status     = [int]$response.StatusCode
            RetryAfter = (Get-P8HeaderValue -Headers $response.Headers -Name "Retry-After")
            Body       = [string]$response.Content
        }
    } catch {
        $status = 0
        $retryAfter = ""
        $body = Read-P8ExceptionBody -Exception $_.Exception
        if ($null -ne $_.Exception.Response) {
            try { $status = [int]$_.Exception.Response.StatusCode } catch { $status = 0 }
            try { $retryAfter = Get-P8HeaderValue -Headers $_.Exception.Response.Headers -Name "Retry-After" } catch { }
        }
        return [pscustomobject]@{
            Status     = $status
            RetryAfter = $retryAfter
            Body       = [string]$body
        }
    }
}

# ---------------------------------------------------------------------------
# boot / environment
# ---------------------------------------------------------------------------

function Build-P8ApiBinary {
    Write-P8Log "==> Building the P8 API binary"
    $context = Get-P8Context
    if ([string]::IsNullOrWhiteSpace([string]$context.Go)) { throw "go toolchain is not initialized" }
    $binary = Join-Path $script:evidenceDir "eztest-p8.exe"
    Push-Location (Join-Path $script:repoRoot "backend")
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & $context.Go build -o $binary ./cmd/eztest 2>&1 | ForEach-Object { Write-P8Log "    $_" }
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
        Pop-Location
    }
    if ($code -ne 0) { throw "go build failed with exit code $code" }
    if (-not (Test-Path -LiteralPath $binary)) { throw "go build did not produce $binary" }
    return $binary
}

function Prepare-P8ContentRoot {
    Write-P8Log "==> Preparing the resilience content root"
    $source = Join-Path $script:repoRoot ".data"
    $dataRoot = Join-Path $script:evidenceDir "data"
    if (-not $SkipDatabaseReset -and (Test-Path -LiteralPath $dataRoot)) {
        Remove-Item -LiteralPath $dataRoot -Recurse -Force
    }
    if (-not (Test-Path -LiteralPath $dataRoot)) {
        if (Test-Path -LiteralPath $source) {
            Copy-Item -LiteralPath $source -Destination $dataRoot -Recurse -Force
        } else {
            New-Item -ItemType Directory -Force -Path $dataRoot | Out-Null
        }
    }
    foreach ($child in @("objects", "evidence", "reports", "exports", "attachments")) {
        $path = Join-Path $dataRoot $child
        if (-not (Test-Path -LiteralPath $path)) {
            New-Item -ItemType Directory -Force -Path $path | Out-Null
        }
    }
    $root = $dataRoot.Replace("\", "/")
    $settings = @(
        @("paths.data_root", $root),
        @("paths.attachment_dir", "$root/attachments"),
        @("paths.evidence_dir", "$root/evidence"),
        @("paths.report_dir", "$root/reports"),
        @("paths.export_dir", "$root/exports")
    )
    $sql = ""
    foreach ($pair in $settings) {
        $sql += "UPDATE system_setting SET value_json = to_jsonb('" + (ConvertTo-P8SqlText $pair[1]) + "'::text), updated_at = now() WHERE key = '" + $pair[0] + "';`n"
    }
    Invoke-P8Psql -Sql $sql | Out-Null
    $updated = [int](Get-P8Scalar -Sql "SELECT count(*) FROM system_setting WHERE key IN ('paths.data_root','paths.attachment_dir','paths.evidence_dir','paths.report_dir','paths.export_dir');")
    if ($updated -ne 5) { throw "P8 path settings were not found in the database (found $updated of 5)" }
    $context = Get-P8Context
    $context.DataRoot = $dataRoot
    Write-P8Log "    content root: $dataRoot"
}

function Login-P8Admin {
    $context = Get-P8Context
    $login = Invoke-P8Api -Method "POST" -Path "/api/auth/login" -Body @{ username = "admin"; password = "admin123" } -Token "" -TimeoutSec 30
    if ($login.Status -ne 200 -or $null -eq $login.Json -or $null -eq $login.Json.data -or [string]::IsNullOrWhiteSpace([string]$login.Json.data.token)) {
        throw "admin login failed: status=$($login.Status) $($login.Text)"
    }
    $context.Token = [string]$login.Json.data.token
}

function Get-P8SecretToken {
    param([string]$Key)
    $token = (Get-P8DbSettingText -Key $Key).Trim()
    if ([string]::IsNullOrWhiteSpace($token)) { throw "setting '$Key' is missing from the acceptance database" }
    return $token
}

function Configure-P8Settings {
    Write-P8Log "==> Configuring P8 settings"
    $settings = @(
        @("report.render_pdf", $true),
        @("report.render_worker_url", "http://127.0.0.1:$RendererPort"),
        @("report.render_max_attempts", 3),
        @("execution.executor_url", "http://127.0.0.1:$ExecutorAPort"),
        @("execution.executor_urls", "http://127.0.0.1:$ExecutorAPort,http://127.0.0.1:$ExecutorBPort"),
        @("execution.executor_heartbeat_seconds", 2),
        @("execution.executor_offline_seconds", 5),
        @("instance.heartbeat_seconds", 10),
        @("instance.offline_seconds", 10)
    )
    foreach ($pair in $settings) {
        Set-P8Setting -Key $pair[0] -Value $pair[1] | Out-Null
    }
    Write-P8Log "    render -> $RendererPort, executors -> $ExecutorAPort/$ExecutorBPort"
}

function Initialize-P8Environment {
    Write-P8Log "==> Preflight"
    $context = Get-P8Context
    $dotenvPath = Join-Path $script:repoRoot ".env"
    $dotenv = Import-P8DotEnv -Path $dotenvPath
    foreach ($key in @("EZTEST_DATABASE_URL", "EZTEST_API_PORT", "EZTEST_WEB_PORT", "EZTEST_EXECUTOR_PORT", "llm_api_key", "llm_base_url", "llm_model_name")) {
        if (-not $dotenv.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$dotenv[$key])) {
            throw "missing $key in .env"
        }
    }
    $dbInfo = ConvertFrom-P8DatabaseUrl -Url $dotenv["EZTEST_DATABASE_URL"]
    $context.Db = @{
        Scheme   = $dbInfo.Scheme
        Host     = $dbInfo.Host
        Port     = $dbInfo.Port
        User     = $dbInfo.User
        Password = $dbInfo.Password
        Name     = $DatabaseName
        Query    = $dbInfo.Query
    }
    $env:PGPASSWORD = $dbInfo.Password
    $env:PGCLIENTENCODING = "UTF8"

    $psql = Join-Path $script:repoRoot ".tools\pgsql\bin\psql.exe"
    if (-not (Test-Path -LiteralPath $psql)) {
        $command = Get-Command psql.exe -ErrorAction SilentlyContinue
        if ($null -eq $command) { throw "psql not found (.tools/pgsql/bin/psql.exe or PATH)" }
        $psql = $command.Source
    }
    $context.Psql = $psql

    $go = Join-Path $script:repoRoot ".tools\go\bin\go.exe"
    if (-not (Test-Path -LiteralPath $go)) {
        $command = Get-Command go -ErrorAction SilentlyContinue
        if ($null -eq $command) { throw "go toolchain not found" }
        $go = $command.Source
    }
    $context.Go = $go

    $node = Get-Command node.exe -ErrorAction SilentlyContinue
    if ($null -eq $node) { $node = Get-Command node -ErrorAction SilentlyContinue }
    if ($null -eq $node) { throw "node executable not found" }
    $context.Node = $node.Source

    foreach ($port in @($ApiPort, $RendererPort, $ExecutorAPort, $ExecutorBPort, $OrdersPort, $PaymentsPort)) {
        if (Test-P8PortInUse -Port $port) {
            Write-P8Log "    stopping stale listener on port $port" "Yellow"
            Stop-P8PortProcesses -Port $port
            Wait-P8PortClosed -Port $port -TimeoutSeconds 10 | Out-Null
        }
    }

    if (-not $SkipDatabaseReset) {
        Write-P8Log "==> Cloning $TemplateDatabase -> $DatabaseName"
        New-P8CloneDatabase -TemplateDatabase $TemplateDatabase -DatabaseName $DatabaseName | Out-Null
    } else {
        $exists = Get-P8Scalar -Sql ("SELECT count(*) FROM pg_database WHERE datname = '" + $DatabaseName + "';") -Database "postgres"
        if ([int]$exists -ne 1) {
            throw "database '$DatabaseName' does not exist; omit -SkipDatabaseReset to clone $TemplateDatabase"
        }
    }

    $script:binaryPath = Build-P8ApiBinary
    Prepare-P8ContentRoot

    $context.ExecutorToken = Get-P8SecretToken -Key "execution.executor_token"
    $context.RenderToken = Get-P8SecretToken -Key "report.render_token"

    $script:databaseUrl = New-P8DatabaseUrl -DatabaseName $DatabaseName
    Write-P8Log "==> Starting API on $ApiPort"
    $script:startedProcesses = $true
    $script:apiRecord = Start-P8Api -Port $ApiPort -DatabaseUrl $script:databaseUrl -ExecutorPort $ExecutorAPort -BinaryPath $script:binaryPath
    $context.BaseUrl = "http://127.0.0.1:$ApiPort"
    $context.ApiPort = $ApiPort
    Login-P8Admin

    Write-P8Log "==> Starting renderer, executors and target fixtures"
    Start-P8Renderer -Port $RendererPort | Out-Null
    Start-P8Executor -Ports @($ExecutorAPort, $ExecutorBPort) -ApiPort $ApiPort -Token $context.ExecutorToken -WaitRegistered | Out-Null
    Start-P8Fixture -OrdersPort $OrdersPort -PaymentsPort $PaymentsPort | Out-Null

    Configure-P8Settings
    $script:bootstrapped = $true
    Write-P8Log "    API/renderer/executors/fixtures ready"
}

# ---------------------------------------------------------------------------
# settings tracking / restore
# ---------------------------------------------------------------------------

function Set-P8SettingTracked {
    param(
        [Parameter(Mandatory = $true)][string]$Key,
        $Value
    )
    if (-not $script:settingsOld.ContainsKey($Key) -and -not $script:settingsMissing.Contains($Key)) {
        $old = Get-P8SettingValue -Key $Key
        if ($null -ne $old) {
            $script:settingsOld[$Key] = $old
        } else {
            $script:settingsMissing.Add($Key)
            Write-P8Log "    old value for '$Key' is unavailable; restore will be skipped" "Yellow"
        }
    }
    Set-P8Setting -Key $Key -Value $Value | Out-Null
}

function Set-P8SettingWithRetry {
    param(
        [Parameter(Mandatory = $true)][string]$Key,
        $Value,
        [int]$TimeoutSeconds = 60
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $lastError = ""
    while ((Get-Date) -lt $deadline) {
        try {
            Set-P8Setting -Key $Key -Value $Value | Out-Null
            return $true
        } catch {
            $lastError = Get-P8ShortText -Text $_.Exception.Message
            Start-Sleep -Seconds 2
        }
    }
    Write-P8Log "    restore of '$Key' failed: $lastError" "Red"
    return $false
}

function Restore-P8TrackedSetting {
    param([Parameter(Mandatory = $true)][string]$Key)
    if (-not $script:settingsOld.ContainsKey($Key)) {
        if ($script:settingsMissing.Contains($Key)) {
            Write-P8Log "    no recorded old value for '$Key'; restore skipped" "Yellow"
        }
        return $true
    }
    $old = $script:settingsOld[$Key]
    $ok = Set-P8SettingWithRetry -Key $Key -Value $old -TimeoutSeconds 60
    if ($ok) {
        $script:settingsOld.Remove($Key)
        Write-P8Log "    restored setting $Key"
    }
    return $ok
}

function Restore-P8AllTrackedSettings {
    $keys = @($script:settingsOld.Keys)
    foreach ($key in $keys) {
        if (-not (Restore-P8TrackedSetting -Key $key)) {
            $script:cleanupRestoreFailures += 1
            Add-P8BugNote "cleanup: failed to restore setting '$key'"
        }
    }
}

# ---------------------------------------------------------------------------
# version / binding / conversation helpers
# ---------------------------------------------------------------------------

function Get-P8VersionList {
    $response = Invoke-P8Api -Method "GET" -Path "/api/versions?limit=50"
    if ($response.Status -ne 200 -or $null -eq $response.Json) { throw "GET /api/versions failed: status=$($response.Status) $($response.Text)" }
    return @(Get-P8Array $response.Json.data)
}

function Get-P8VersionDetail {
    param([string]$VersionID)
    $response = Invoke-P8Api -Method "GET" -Path "/api/versions/$VersionID"
    if ($response.Status -ne 200 -or $null -eq $response.Json) { throw "GET version $VersionID failed: status=$($response.Status) $($response.Text)" }
    return $response.Json.data
}

function ConvertTo-P8BindingFromEnvironment {
    param($Sut, $Environment)
    $pages = @(Get-P8Array $Environment.pages)
    if ($pages.Count -eq 0) {
        $pageResponse = Invoke-P8Api -Method "GET" -Path ("/api/resources/environments/" + [string]$Environment.id + "/pages?limit=50")
        if ($pageResponse.Status -eq 200 -and $null -ne $pageResponse.Json) { $pages = @(Get-P8Array $pageResponse.Json.data) }
    }
    $apis = @(Get-P8Array $Environment.apis)
    if ($apis.Count -eq 0) {
        $apiResponse = Invoke-P8Api -Method "GET" -Path ("/api/resources/environments/" + [string]$Environment.id + "/apis?limit=50")
        if ($apiResponse.Status -eq 200 -and $null -ne $apiResponse.Json) { $apis = @(Get-P8Array $apiResponse.Json.data) }
    }
    $pageIDs = @($pages | Where-Object { $null -ne $_ -and -not [string]::IsNullOrWhiteSpace([string]$_.id) } | ForEach-Object { [string]$_.id })
    $apiIDs = @($apis | Where-Object { $null -ne $_ -and -not [string]::IsNullOrWhiteSpace([string]$_.id) } | ForEach-Object { [string]$_.id })
    $accounts = @(Get-P8Array $Environment.accounts)
    if ($accounts.Count -eq 0) {
        $accountResponse = Invoke-P8Api -Method "GET" -Path ("/api/resources/suts/" + [string]$Sut.id + "/accounts?limit=50")
        if ($accountResponse.Status -eq 200 -and $null -ne $accountResponse.Json) { $accounts = @(Get-P8Array $accountResponse.Json.data) }
    }
    $accountBindings = @()
    foreach ($account in $accounts) {
        if ($null -eq $account -or [string]::IsNullOrWhiteSpace([string]$account.id)) { continue }
        if (-not [string]::IsNullOrWhiteSpace([string]$account.environment_id) -and [string]$account.environment_id -ne [string]$Environment.id) { continue }
        $accountBindings += @{ account_id = [string]$account.id; role = "user" }
    }
    return [pscustomobject]@{
        SutID         = [string]$Sut.id
        SutName       = [string]$Sut.name
        EnvironmentID = [string]$Environment.id
        BaseURL       = [string]$Environment.base_url
        PageIDs       = $pageIDs
        APIIDs        = $apiIDs
        Accounts      = $accountBindings
    }
}

function Get-P8FixtureBinding {
    param(
        [string]$PreferredBaseUrl = "",
        [switch]$AllowAny
    )
    $response = Invoke-P8Api -Method "GET" -Path "/api/resources/suts?limit=100"
    if ($response.Status -ne 200 -or $null -eq $response.Json) {
        throw "GET /api/resources/suts failed: status=$($response.Status) $($response.Text)"
    }
    $anyBinding = $null
    foreach ($sut in @(Get-P8Array $response.Json.data)) {
        $environments = @(Get-P8Array $sut.environments)
        if ($environments.Count -eq 0) {
            $envResponse = Invoke-P8Api -Method "GET" -Path ("/api/resources/suts/" + [string]$sut.id + "/environments?limit=50")
            if ($envResponse.Status -eq 200 -and $null -ne $envResponse.Json) { $environments = @(Get-P8Array $envResponse.Json.data) }
        }
        foreach ($environment in $environments) {
            if ($null -eq $environment) { continue }
            $baseURL = [string]$environment.base_url
            if ([string]::IsNullOrWhiteSpace($baseURL)) { continue }
            if ($null -eq $anyBinding) {
                $anyBinding = ConvertTo-P8BindingFromEnvironment -Sut $sut -Environment $environment
            }
            if (-not [string]::IsNullOrWhiteSpace($PreferredBaseUrl)) {
                $left = $baseURL.Trim().TrimEnd("/")
                $right = $PreferredBaseUrl.Trim().TrimEnd("/")
                if ($left -eq $right) {
                    return ConvertTo-P8BindingFromEnvironment -Sut $sut -Environment $environment
                }
            }
        }
    }
    if ($AllowAny) { return $anyBinding }
    return $null
}

function New-P8ResilienceVersion {
    param(
        [Parameter(Mandatory = $true)][string]$Title,
        $Binding = $null
    )
    $bindings = @()
    if ($null -ne $Binding) {
        $bindings = @(@{
            sut_id = [string]$Binding.SutID
            environment_id = [string]$Binding.EnvironmentID
            code_repository_id = $null
            page_ids = @($Binding.PageIDs)
            api_ids = @($Binding.APIIDs)
            database_ids = @()
            accounts = @($Binding.Accounts)
        })
    }
    $body = @{ title = $Title; bindings = $bindings }
    $response = Invoke-P8Api -Method "POST" -Path "/api/versions" -Body $body
    if ($response.Status -eq 201 -and $null -ne $response.Json) {
        return $response.Json.data
    }
    throw "create version failed: status=$($response.Status) $($response.Text)"
}

function Resolve-P8LongContextVersion {
    $versions = @(Get-P8VersionList)
    $preferred = @($versions | Where-Object { @("initial", "running") -contains [string]$_.status })
    if ($preferred.Count -gt 0) {
        $view = Get-P8VersionDetail -VersionID ([string]$preferred[0].id)
        return [pscustomobject]@{ ID = [string]$view.id; Title = [string]$view.title; Created = $false; Detail = $view }
    }
    $emptyBindingError = ""
    try {
        Write-P8Log "    no initial/running version found; trying a title + empty bindings create" "Yellow"
        $view = New-P8ResilienceVersion -Title ("P8 resilience long context " + $script:runSuffix) -Binding $null
        return [pscustomobject]@{ ID = [string]$view.id; Title = [string]$view.title; Created = $true; Detail = $view }
    } catch {
        $emptyBindingError = Get-P8ShortText -Text $_.Exception.Message
        Write-P8Log "    empty-bindings create was rejected: $emptyBindingError" "Yellow"
    }
    $binding = Get-P8FixtureBinding -PreferredBaseUrl ("http://127.0.0.1:" + $OrdersPort) -AllowAny
    if ($null -ne $binding -and -not [string]::IsNullOrWhiteSpace([string]$binding.SutID)) {
        Write-P8Log "    creating the long-context version bound to $($binding.SutName)" "Yellow"
        $view = New-P8ResilienceVersion -Title ("P8 resilience long context " + $script:runSuffix) -Binding $binding
        return [pscustomobject]@{ ID = [string]$view.id; Title = [string]$view.title; Created = $true; Detail = $view }
    }
    if ($versions.Count -gt 0) {
        $view = Get-P8VersionDetail -VersionID ([string]$versions[0].id)
        Write-P8Log "    no fixture binding available; reusing the newest existing version" "Yellow"
        return [pscustomobject]@{ ID = [string]$view.id; Title = [string]$view.title; Created = $false; Detail = $view }
    }
    throw "no version exists and no fixture-bound resource is available to create one (the frozen API requires 1-20 bindings; empty bindings: $emptyBindingError)"
}

function Get-P8ConversationMessages {
    param([string]$VersionID, [string]$ConversationID)
    $response = Invoke-P8Api -Method "GET" -Path "/api/versions/$VersionID/conversations/$ConversationID/messages?limit=200"
    if ($response.Status -ne 200 -or $null -eq $response.Json) {
        throw "GET conversation messages failed: status=$($response.Status) $($response.Text)"
    }
    return @(Get-P8Array $response.Json.data)
}

function Send-P8ConversationMessage {
    param(
        [string]$VersionID,
        [string]$ConversationID,
        [string]$Text,
        [string]$ClientMessageID
    )
    $body = @{
        client_message_id = $ClientMessageID
        parts             = @(@{ type = "text"; text = $Text })
    }
    return Invoke-P8Api -Method "POST" -Path "/api/versions/$VersionID/conversations/$ConversationID/messages" -Body $body -TimeoutSec 120
}

function Get-P8DesignGraph {
    param([string]$VersionID)
    $response = Invoke-P8Api -Method "GET" -Path "/api/versions/$VersionID/design/graph"
    if ($response.Status -ne 200 -or $null -eq $response.Json) {
        throw "GET design graph failed: status=$($response.Status) $($response.Text)"
    }
    return $response.Json.data
}

function Get-P8DesignCaseCards {
    param($Graph)
    $cards = [System.Collections.Generic.List[object]]::new()
    if ($null -eq $Graph) { return $cards }
    foreach ($requirement in @(Get-P8Array $Graph.requirements)) {
        foreach ($scenario in @(Get-P8Array $requirement.scenarios)) {
            foreach ($case in @(Get-P8Array $scenario.cases)) {
                if ($null -ne $case) { $cards.Add($case) }
            }
        }
    }
    return $cards
}

function Get-P8DesignCaseDetail {
    param([string]$VersionID, [string]$CaseID)
    $response = Invoke-P8Api -Method "GET" -Path "/api/versions/$VersionID/design/cases/$CaseID"
    if ($response.Status -ne 200 -or $null -eq $response.Json) {
        throw "GET design case $CaseID failed: status=$($response.Status) $($response.Text)"
    }
    return $response.Json.data
}

function Get-P8Waves {
    param([string]$VersionID)
    $response = Invoke-P8Api -Method "GET" -Path "/api/versions/$VersionID/waves?limit=100"
    if ($response.Status -ne 200 -or $null -eq $response.Json) {
        throw "GET waves failed: status=$($response.Status) $($response.Text)"
    }
    return @(Get-P8Array $response.Json.data)
}

function Get-P8WaveRuns {
    param([string]$VersionID, [string]$WaveID)
    $response = Invoke-P8Api -Method "GET" -Path "/api/versions/$VersionID/waves/$WaveID/case-runs?limit=100"
    if ($response.Status -ne 200 -or $null -eq $response.Json) {
        throw "GET wave runs failed: status=$($response.Status) $($response.Text)"
    }
    return @(Get-P8Array $response.Json.data)
}

function Find-P8TerminalWaveForCases {
    param(
        [string]$VersionID,
        [string[]]$CaseVersionIDs,
        [string[]]$ExcludeWaveIDs = @()
    )
    try {
        $waves = @(Get-P8Waves -VersionID $VersionID)
    } catch {
        return $null
    }
    foreach ($wave in $waves) {
        $waveID = [string]$wave.id
        if ($ExcludeWaveIDs -contains $waveID) { continue }
        if (@("done", "failed", "cancelled") -notcontains [string]$wave.status) { continue }
        try {
            $runs = @(Get-P8WaveRuns -VersionID $VersionID -WaveID $waveID)
        } catch {
            continue
        }
        $matched = @($runs | Where-Object { $CaseVersionIDs -contains [string]$_.case_version_id })
        if ($matched.Count -ge $CaseVersionIDs.Count) {
            return [pscustomobject]@{ Wave = $wave; Runs = $runs; MatchedRuns = $matched }
        }
    }
    return $null
}

function Invoke-P8ConfirmCaseBatch {
    param([string]$VersionID, [string[]]$CaseIDs)
    $failed = [System.Collections.Generic.List[string]]::new()
    foreach ($caseID in $CaseIDs) {
        $body = @{ scope = "case"; target_id = $caseID }
        $response = Invoke-P8Api -Method "POST" -Path "/api/versions/$VersionID/design/confirmations" -Body $body
        if ($response.Status -ne 200) {
            $failed.Add("$caseID=$($response.Status)")
        }
    }
    return $failed
}

function Ensure-P8IntentsForCaseVersions {
    param(
        [string]$VersionID,
        [string[]]$CaseVersionIDs,
        [int]$TimeoutSeconds = 90
    )
    $started = Get-Date
    $deadline = $started.AddSeconds($TimeoutSeconds)
    $manualCreated = $false
    while ((Get-Date) -lt $deadline) {
        $response = Invoke-P8Api -Method "GET" -Path "/api/versions/$VersionID/execution/intents?limit=100"
        if ($response.Status -eq 200 -and $null -ne $response.Json) {
            $intents = @(Get-P8Array $response.Json.data)
            $have = @($intents | ForEach-Object { [string]$_.case_version_id })
            $missing = @($CaseVersionIDs | Where-Object { $have -notcontains $_ })
            if ($missing.Count -eq 0) { return $true }
            # Confirmation creates the intents asynchronously. Give that path a
            # short head start before falling back to manual retest intents, so
            # the same case version is not dispatched twice.
            if (-not $manualCreated -and ((Get-Date) - $started).TotalSeconds -ge 10) {
                $manualCreated = $true
                foreach ($caseVersionID in $missing) {
                    $create = Invoke-P8Api -Method "POST" -Path "/api/versions/$VersionID/execution/intents" -Body @{ case_version_id = $caseVersionID; purpose = "manual" }
                    if ($create.Status -ne 201 -and $create.Status -ne 409) {
                        Write-P8Log "    manual intent for $caseVersionID returned $($create.Status): $(Get-P8ShortText $create.Text)" "Yellow"
                    }
                }
            }
        }
        Start-Sleep -Seconds 2
    }
    return $false
}

function New-P8RequirementForVersion {
    param(
        [string]$VersionID,
        [string]$VersionSutID,
        [string]$Title,
        [string]$Description,
        [string]$AcceptanceCriteria
    )
    $body = @{
        title               = $Title
        description         = $Description
        acceptance_criteria = $AcceptanceCriteria
        priority            = "P2"
        targets             = @{
            version_suts = @($VersionSutID)
            pages        = @()
            apis         = @()
        }
    }
    $response = Invoke-P8Api -Method "POST" -Path "/api/versions/$VersionID/requirements" -Body $body
    if ($response.Status -ne 201 -or $null -eq $response.Json) {
        throw "create requirement failed: status=$($response.Status) $($response.Text)"
    }
    $requirementID = [string]$response.Json.data.id
    $confirm = Invoke-P8Api -Method "POST" -Path "/api/versions/$VersionID/requirements/$requirementID/confirmations" -Body $null
    if ($confirm.Status -ne 200) {
        throw "confirm requirement $requirementID failed: status=$($confirm.Status) $($confirm.Text)"
    }
    return $requirementID
}

# ---------------------------------------------------------------------------
# S9: long context
# ---------------------------------------------------------------------------

function Invoke-P8ScenarioS9 {
    $scenario = Start-P8TimedScenario
    $scenario.Name = "S9"
    $checks = $scenario.Checks
    $summaryText = ""
    try {
        $context = Get-P8Context
        Write-P8Log "==> S9 long context (window=8000, 30 user messages)"

        Set-P8SettingTracked -Key "context.window_tokens" -Value 8000
        Set-P8SettingTracked -Key "agent.tool_result_max_bytes" -Value 8192
        $window = Get-P8SettingValue -Key "context.window_tokens"
        $toolBytes = Get-P8SettingValue -Key "agent.tool_result_max_bytes"
        Write-P8Check $checks ([int64]$window -eq 8000 -and [int64]$toolBytes -eq 8192) "S9 settings applied" ("window=$window tool_result_max_bytes=$toolBytes")

        $version = Resolve-P8LongContextVersion
        $versionID = [string]$version.ID
        Write-P8Check $checks (-not [string]::IsNullOrWhiteSpace($versionID)) "S9 version available" ("id=$versionID created=$($version.Created) title=$($version.Title)")
        if ([string]::IsNullOrWhiteSpace($versionID)) { throw "no version available for S9" }
        $script:ids.longContextVersion = $versionID

        $conversationBody = @{ kind = "clarification"; title = ("P8 resilience long context " + $script:runSuffix) }
        $conversationResponse = Invoke-P8Api -Method "POST" -Path "/api/versions/$versionID/conversations" -Body $conversationBody
        $conversationID = ""
        if ($conversationResponse.Status -eq 201 -and $null -ne $conversationResponse.Json) {
            $conversationID = [string]$conversationResponse.Json.data.id
        }
        Write-P8Check $checks ($conversationResponse.Status -eq 201 -and -not [string]::IsNullOrWhiteSpace($conversationID)) "S9 clarification conversation created" ("status=$($conversationResponse.Status) conversation=$conversationID")
        if ([string]::IsNullOrWhiteSpace($conversationID)) { throw "clarification conversation creation failed: $($conversationResponse.Text)" }
        $script:ids.longContextConversation = $conversationID

        $accepted = 0
        $postFailures = [System.Collections.Generic.List[string]]::new()
        for ($index = 1; $index -le 30; $index++) {
            $padding = (" context-padding-" + $script:runSuffix + "-" + $index + "-" + ("x" * 80))
            $text = ("P8 S9 long-context turn " + $index + "/30. Keep this clarification turn in the rolling memory." + $padding)
            $response = Send-P8ConversationMessage -VersionID $versionID -ConversationID $conversationID -Text $text -ClientMessageID ("p8-s9-" + $script:runSuffix + "-" + $index)
            if ($response.Status -eq 201) {
                $accepted += 1
            } else {
                $postFailures.Add(("turn " + $index + "=" + $response.Status))
            }
        }
        Write-P8Check $checks ($accepted -eq 30) "S9 posted 30 user messages" ("accepted=$accepted failures=" + (($postFailures | Select-Object -First 5) -join ","))

        $messages = @(Get-P8ConversationMessages -VersionID $versionID -ConversationID $conversationID)
        $userMessages = @($messages | Where-Object { [string]$_.role -eq "user" })
        Write-P8Check $checks ($userMessages.Count -ge 30) "S9 30 user messages readable as original text" ("user_messages=" + $userMessages.Count + " total=" + $messages.Count)

        $wait = Wait-P8Until -TimeoutSeconds 600 -IntervalMilliseconds 5000 -Description "S9 summary + context.compacted" -Condition {
            $summaryCount = [int](Get-P8Scalar -Sql ("SELECT count(*) FROM conversation_summary WHERE conversation_id = " + [int64]$conversationID + ";"))
            $compactedCount = [int](Get-P8Scalar -Sql ("SELECT count(*) FROM event WHERE channel = 'version:" + [int64]$versionID + "' AND type = 'context.compacted';"))
            if ($summaryCount -ge 1 -and $compactedCount -ge 1) { return $true }
            return $null
        }
        $summaryParts = @((Get-P8Scalar -Sql ("SELECT count(*) || '|' || COALESCE(max(version),0) || '|' || COALESCE(max(length(summary)),0) || '|' || COALESCE(max(CASE WHEN jsonb_typeof(pinned_facts) = 'array' THEN jsonb_array_length(pinned_facts) ELSE 0 END),0) FROM conversation_summary WHERE conversation_id = " + [int64]$conversationID + ";")).Split("|"))
        $summaryCount = 0
        $summaryLength = 0
        $pinnedCount = 0
        if ($summaryParts.Count -ge 4) {
            $summaryCount = [int]$summaryParts[0]
            $summaryLength = [int]$summaryParts[2]
            $pinnedCount = [int]$summaryParts[3]
        }
        $compactedDB = [int](Get-P8Scalar -Sql ("SELECT count(*) FROM event WHERE channel = 'version:" + [int64]$versionID + "' AND type = 'context.compacted';"))
        Write-P8Check $checks ($null -ne $wait -and $summaryCount -ge 1) "S9 conversation_summary rows exist" ("wait_ok=$($null -ne $wait) rows=$summaryCount summary_length=$summaryLength pinned_facts=$pinnedCount")
        Write-P8Check $checks ($summaryLength -gt 0) "S9 rolling summary is non-empty" ("summary_length=$summaryLength")

        $eventsResponse = Invoke-P8Api -Method "GET" -Path ("/api/events?channel=" + [uri]::EscapeDataString("version:$versionID") + "&limit=200")
        $eventTypes = [System.Collections.Generic.HashSet[string]]::new()
        if ($eventsResponse.Status -eq 200 -and $null -ne $eventsResponse.Json) {
            foreach ($event in @(Get-P8Array $eventsResponse.Json.data)) {
                if ($null -ne $event -and -not [string]::IsNullOrWhiteSpace([string]$event.type)) {
                    $null = $eventTypes.Add([string]$event.type)
                }
            }
        }
        Write-P8Check $checks ($compactedDB -ge 1) "S9 version channel has context.compacted events" ("count=$compactedDB")
        Write-P8Check $checks $eventTypes.Contains("context.compacted") "S9 events API exposes context.compacted" ("status=$($eventsResponse.Status) types=" + (($eventTypes | Sort-Object) -join ","))

        $idle = Wait-P8Until -TimeoutSeconds 300 -IntervalMilliseconds 5000 -Description "S9 agent runs idle" -Condition {
            $running = [int](Get-P8Scalar -Sql ("SELECT count(*) FROM agent_run WHERE conversation_id = " + [int64]$conversationID + " AND status = 'running';"))
            if ($running -eq 0) { return $true }
            return $null
        }
        $runRow = Get-P8Scalar -Sql ("SELECT id || '|' || status || '|' || COALESCE(terminal_reason,'') || '|' || COALESCE(context_packet_id,0)::text FROM agent_run WHERE conversation_id = " + [int64]$conversationID + " ORDER BY id DESC LIMIT 1;")
        $runParts = @($runRow.Split("|"))
        $runID = ""
        $runStatus = ""
        $runReason = ""
        $packetID = 0
        if ($runParts.Count -ge 4) {
            $runID = [string]$runParts[0]
            $runStatus = [string]$runParts[1]
            $runReason = [string]$runParts[2]
            $packetID = [int64]$runParts[3]
        }
        Write-P8Check $checks ($null -ne $idle -and -not [string]::IsNullOrWhiteSpace($runID) -and $runStatus -ne "running") "S9 latest agent run reached a terminal state" ("idle=$($null -ne $idle) run=$runID status=$runStatus reason=$runReason")
        $overflow = ($runStatus -in @("failed", "interrupted")) -and ($runReason -like "context_overflow*")
        Write-P8Check $checks (-not $overflow) "S9 latest agent run is not a context_overflow terminal failure" ("status=$runStatus reason=$runReason")

        $packetRow = ""
        $packetSummaryPresent = 0
        $packetSectionCount = 0
        if ($packetID -gt 0) {
            $packetRow = Get-P8Scalar -Sql ("SELECT count(*) || '|' || COALESCE(MAX(CASE WHEN s->>'name' = 'conversation' AND EXISTS (SELECT 1 FROM conversation_summary cs WHERE cs.conversation_id = " + [int64]$conversationID + " AND length(cs.summary) > 0 AND position(left(cs.summary,16) in COALESCE(s->>'content','')) > 0) THEN 1 ELSE 0 END),0) FROM context_packet p LEFT JOIN LATERAL jsonb_array_elements(p.sections) s ON TRUE WHERE p.run_id = " + [int64]$runID + ";")
            $packetParts = @($packetRow.Split("|"))
            if ($packetParts.Count -ge 2) {
                $packetSectionCount = [int]$packetParts[0]
                $packetSummaryPresent = [int]$packetParts[1]
            }
        }
        Write-P8Check $checks ($packetID -gt 0 -and $packetSectionCount -ge 1) "S9 latest run has a context packet" ("packet_id=$packetID sections=$packetSectionCount")
        Write-P8Check $checks ($packetSummaryPresent -eq 1) "S9 context packet conversation section contains the rolling summary" ("packet_id=$packetID summary_prefix_present=$packetSummaryPresent")
        $pinnedPresent = 0
        if ($packetID -gt 0 -and $pinnedCount -gt 0) {
            $pinnedPresent = [int](Get-P8Scalar -Sql ("SELECT CASE WHEN EXISTS (SELECT 1 FROM conversation_summary cs, jsonb_array_elements_text(cs.pinned_facts) f WHERE cs.conversation_id = " + [int64]$conversationID + " AND position(f in (SELECT COALESCE(s->>'content','') FROM context_packet p, jsonb_array_elements(p.sections) s WHERE p.run_id = " + [int64]$runID + " AND s->>'name' = 'conversation' LIMIT 1)) > 0) THEN 1 ELSE 0 END;"))
        }
        Write-P8Check $checks ($pinnedCount -eq 0 -or $pinnedPresent -eq 1) "S9 context packet preserves pinned facts when present" ("pinned_facts=$pinnedCount present_in_packet=$pinnedPresent")

        $snapshot = [pscustomobject]@{
            version_id = $versionID
            conversation_id = $conversationID
            accepted_messages = $accepted
            user_messages = $userMessages.Count
            summary_rows = $summaryCount
            summary_length = $summaryLength
            pinned_facts = $pinnedCount
            compacted_events_db = $compactedDB
            compacted_event_api = $eventTypes.Contains("context.compacted")
            run_id = $runID
            run_status = $runStatus
            terminal_reason = $runReason
            context_packet_id = $packetID
            context_packet_sections = $packetSectionCount
            packet_has_summary = $packetSummaryPresent
            generated_at = (Get-Date -Format o)
        }
        Write-P8JsonEvidence -Name "s9-long-context.json" -Value $snapshot | Out-Null
        $summaryText = "messages=$accepted summary_rows=$summaryCount pinned=$pinnedCount packet=$packetID run=$runStatus path=long-context"
    } catch {
        Write-P8Check $checks $false "S9 fatal" (Get-P8ShortText $_.Exception.Message)
        $summaryText = "fatal: " + (Get-P8ShortText $_.Exception.Message 200)
    } finally {
        if (-not (Restore-P8TrackedSetting -Key "context.window_tokens")) {
            Write-P8Check $checks $false "S9 restore context.window_tokens" ""
        }
        if (-not (Restore-P8TrackedSetting -Key "agent.tool_result_max_bytes")) {
            Write-P8Check $checks $false "S9 restore agent.tool_result_max_bytes" ""
        }
        Complete-P8TimedScenario -Scenario $scenario -Summary $summaryText
    }
}

# ---------------------------------------------------------------------------
# S10: API version / readiness
# ---------------------------------------------------------------------------

function Invoke-P8ScenarioS10Version {
    $scenario = Start-P8TimedScenario
    $scenario.Name = "S10-VERSION"
    $checks = $scenario.Checks
    try {
        Write-P8Log "==> S10 version/readiness"
        $health = Get-P8Health -Path "/api/health"
        $version = ""
        $dbState = ""
        if ($null -ne $health.Json -and $null -ne $health.Json.data) {
            $version = [string]$health.Json.data.version
            $dbState = [string]$health.Json.data.db
        }
        Write-P8Check $checks ($health.Status -eq 200 -and $version -eq "0.2.0-p8") "S10 /api/health reports 0.2.0-p8" ("status=$($health.Status) version=$version db=$dbState")

        $ready = Get-P8Health -Path "/api/health/ready" -Ready
        $readyStatus = ""
        $fields = @()
        if ($null -ne $ready.Json -and $null -ne $ready.Json.data) {
            $readyStatus = [string]$ready.Json.data.status
            $fields = @($ready.Json.data.PSObject.Properties.Name)
        }
        $hasRequests = $fields -contains "inflight_requests"
        $hasWork = $fields -contains "inflight_work"
        Write-P8Check $checks ($ready.Status -eq 200 -and $readyStatus -eq "ok" -and $hasRequests -and $hasWork) "S10 readiness exposes inflight_requests/inflight_work" ("status=$($ready.Status) readiness=$readyStatus fields=" + (($fields | Sort-Object) -join ","))

        $snapshot = [pscustomobject]@{
            health = $health.Json
            readiness = $ready.Json
            inflight_requests_present = $hasRequests
            inflight_work_present = $hasWork
            generated_at = (Get-Date -Format o)
        }
        Write-P8JsonEvidence -Name "s10-version.json" -Value $snapshot | Out-Null
    } catch {
        Write-P8Check $checks $false "S10 version fatal" (Get-P8ShortText $_.Exception.Message)
    } finally {
        Complete-P8TimedScenario -Scenario $scenario -Summary "version=$version"
    }
}

# ---------------------------------------------------------------------------
# S10: rate limit
# ---------------------------------------------------------------------------

function Invoke-P8ScenarioS10RateLimit {
    $scenario = Start-P8TimedScenario
    $scenario.Name = "S10-RATE-LIMIT"
    $checks = $scenario.Checks
    $summaryText = ""
    try {
        Write-P8Log "==> S10 rate limit (30/min, 40 requests)"
        Set-P8SettingTracked -Key "security.rate_limit_per_minute" -Value 30
        $applied = Get-P8SettingValue -Key "security.rate_limit_per_minute"
        Write-P8Check $checks ([int64]$applied -eq 30) "S10 rate limit setting=30" ("value=$applied")

        $statuses = [System.Collections.Generic.List[int]]::new()
        $retryAfter = [System.Collections.Generic.List[string]]::new()
        $bodies = [System.Collections.Generic.List[string]]::new()
        for ($index = 1; $index -le 40; $index++) {
            $probe = Invoke-P8HttpProbe -Path "/api/versions?limit=10" -TimeoutSec 20
            $statuses.Add([int]$probe.Status)
            $retryAfter.Add([string]$probe.RetryAfter)
            $bodies.Add((Get-P8ShortText -Text $probe.Body -MaxLength 120))
        }
        $okCount = @($statuses | Where-Object { $_ -eq 200 }).Count
        $limitedCount = @($statuses | Where-Object { $_ -eq 429 }).Count
        $withRetry = 0
        for ($index = 0; $index -lt $statuses.Count; $index++) {
            if ($statuses[$index] -eq 429 -and -not [string]::IsNullOrWhiteSpace($retryAfter[$index])) { $withRetry += 1 }
        }
        Write-P8Check $checks ($okCount -ge 1) "S10 40 requests include at least one accepted request" ("ok=$okCount limited=$limitedCount")
        Write-P8Check $checks ($limitedCount -ge 1) "S10 40 requests produce at least one HTTP 429" ("ok=$okCount limited=$limitedCount")
        Write-P8Check $checks ($withRetry -ge 1) "S10 at least one 429 carries Retry-After" ("with_retry_after=$withRetry sample=" + (($retryAfter | Where-Object { $_ -ne "" } | Select-Object -First 3) -join ","))

        $snapshot = [pscustomobject]@{
            requested = 40
            ok = $okCount
            limited = $limitedCount
            retry_after_count = $withRetry
            statuses = @($statuses)
            retry_after = @($retryAfter)
            sample_bodies = @($bodies | Select-Object -First 3)
            generated_at = (Get-Date -Format o)
        }
        Write-P8JsonEvidence -Name "s10-rate-limit.json" -Value $snapshot | Out-Null

        Start-Sleep -Seconds 3
        if (-not (Restore-P8TrackedSetting -Key "security.rate_limit_per_minute")) {
            Write-P8Check $checks $false "S10 restore security.rate_limit_per_minute" "restore call failed"
        }
        $restored = $false
        $restoreProbe = Wait-P8Until -TimeoutSeconds 60 -IntervalMilliseconds 2000 -Description "rate limit restored" -Condition {
            $probe = Invoke-P8HttpProbe -Path "/api/versions?limit=1" -TimeoutSec 10
            if ($probe.Status -eq 200) { return $true }
            return $null
        }
        if ($null -ne $restoreProbe) { $restored = $true }
        $restoredValue = Get-P8SettingValue -Key "security.rate_limit_per_minute"
        Write-P8Check $checks ($restored -and [int64]$restoredValue -eq 0) "S10 rate limit restored to 0" ("value=$restoredValue request_ok=$restored")
        $summaryText = "ok=$okCount limited=$limitedCount retry_after=$withRetry"
    } catch {
        Write-P8Check $checks $false "S10 rate limit fatal" (Get-P8ShortText $_.Exception.Message)
        $summaryText = "fatal: " + (Get-P8ShortText $_.Exception.Message 200)
        if (-not (Restore-P8TrackedSetting -Key "security.rate_limit_per_minute")) {
            $script:cleanupRestoreFailures += 1
        }
    } finally {
        Complete-P8TimedScenario -Scenario $scenario -Summary $summaryText
    }
}

# ---------------------------------------------------------------------------
# S10: event/audit retention
# ---------------------------------------------------------------------------

function Invoke-P8ScenarioS10Retention {
    $scenario = Start-P8TimedScenario
    $scenario.Name = "S10-RETENTION"
    $checks = $scenario.Checks
    $summaryText = ""
    $channel = ""
    $auditTarget = ""
    try {
        Write-P8Log "==> S10 retention (old rows deleted, recent retained, retention.cleanup audit)"
        Set-P8SettingTracked -Key "retention.event_days" -Value 90
        Set-P8SettingTracked -Key "security.audit_retention_days" -Value 90
        $eventDays = Get-P8SettingValue -Key "retention.event_days"
        $auditDays = Get-P8SettingValue -Key "security.audit_retention_days"
        Write-P8Check $checks ([int64]$eventDays -eq 90 -and [int64]$auditDays -eq 90) "S10 retention days applied (event=90 audit=90)" ("event=$eventDays audit=$auditDays")

        $baseline = [int64](Get-P8Scalar -Sql "SELECT COALESCE(max(id),0) FROM audit_log WHERE action = 'retention.cleanup';")
        $channel = "p8-retention-" + $script:runSuffix + "-" + [guid]::NewGuid().ToString("N").Substring(0, 8)
        $auditTarget = $channel
        $oldAuditID = [int64]([DateTime]::UtcNow.Ticks + 101)
        $recentAuditID = [int64]([DateTime]::UtcNow.Ticks + 202)
        $sql = @"
INSERT INTO event_channel (channel, last_seq) VALUES ('$channel', 2)
ON CONFLICT (channel) DO NOTHING;
INSERT INTO event (channel, seq, type, envelope, created_at) VALUES
    ('$channel', 1, 'p8.retention.old', '{}'::jsonb, now() - interval '5000 days'),
    ('$channel', 2, 'p8.retention.recent', '{}'::jsonb, now())
ON CONFLICT (channel, seq) DO NOTHING;
INSERT INTO audit_log (id, actor_id, action, target_kind, target_id, request_id, ip, user_agent, metadata, created_at)
VALUES
    ($oldAuditID, NULL, 'p8.retention.old', 'p8-retention', '$auditTarget', '', '', '', '{}'::jsonb, now() - interval '5000 days'),
    ($recentAuditID, NULL, 'p8.retention.recent', 'p8-retention', '$auditTarget', '', '', '', '{}'::jsonb, now())
ON CONFLICT (id) DO NOTHING;
"@
        Invoke-P8Psql -Sql $sql | Out-Null
        $before = Get-P8Scalar -Sql ("SELECT (SELECT count(*) FROM event WHERE channel = '$channel') || '|' || (SELECT count(*) FROM audit_log WHERE target_id = '$auditTarget');")
        Write-P8Check $checks ($before -eq "2|2") "S10 test rows inserted (2 event + 2 audit)" ("counts=$before")

        $oldLog = $script:apiRecord.Log
        if (Test-Path -LiteralPath $oldLog) {
            Copy-Item -LiteralPath $oldLog -Destination (Join-Path $script:evidenceDir "api-before-retention-restart.log") -Force
        }
        Write-P8Log "    restarting the API for the startup retention pass"
        Stop-P8Process -Process $script:apiRecord -Port $ApiPort -TimeoutSeconds 30
        $portClosed = Wait-P8PortClosed -Port $ApiPort -TimeoutSeconds 20
        Write-P8Check $checks $portClosed "S10 API stopped before retention restart" ("port_closed=$portClosed")

        $script:apiRecord = Start-P8Api -Port $ApiPort -DatabaseUrl $script:databaseUrl -ExecutorPort $ExecutorAPort -BinaryPath $script:binaryPath
        Login-P8Admin
        Write-P8Check $checks $true "S10 API restarted for startup retention pass" ("health=200")

        $cleanupWait = Wait-P8Until -TimeoutSeconds 180 -IntervalMilliseconds 3000 -Description "retention.cleanup audit row" -Condition {
            $count = [int](Get-P8Scalar -Sql ("SELECT count(*) FROM audit_log WHERE action = 'retention.cleanup' AND id > " + $baseline + ";"))
            if ($count -ge 1) { return $true }
            return $null
        }
        $oldEvents = [int](Get-P8Scalar -Sql ("SELECT count(*) FROM event WHERE channel = '$channel' AND seq = 1;"))
        $recentEvents = [int](Get-P8Scalar -Sql ("SELECT count(*) FROM event WHERE channel = '$channel' AND seq = 2;"))
        $oldAudits = [int](Get-P8Scalar -Sql ("SELECT count(*) FROM audit_log WHERE target_id = '$auditTarget' AND action = 'p8.retention.old';"))
        $recentAudits = [int](Get-P8Scalar -Sql ("SELECT count(*) FROM audit_log WHERE target_id = '$auditTarget' AND action = 'p8.retention.recent';"))
        Write-P8Check $checks ($null -ne $cleanupWait) "S10 startup retention pass wrote audit_log(retention.cleanup)" ("new_rows=$([int](Get-P8Scalar -Sql ("SELECT count(*) FROM audit_log WHERE action = 'retention.cleanup' AND id > " + $baseline + ";")))")
        Write-P8Check $checks ($oldEvents -eq 0 -and $oldAudits -eq 0) "S10 old event/audit rows deleted" ("old_events=$oldEvents old_audits=$oldAudits")
        Write-P8Check $checks ($recentEvents -eq 1 -and $recentAudits -eq 1) "S10 recent event/audit rows retained" ("recent_events=$recentEvents recent_audits=$recentAudits")

        $cleanupMeta = Get-P8Scalar -Sql ("SELECT metadata->>'events_deleted' || '|' || metadata->>'audit_deleted' || '|' || metadata->>'cutoff' FROM audit_log WHERE action = 'retention.cleanup' AND id > " + $baseline + " ORDER BY id ASC LIMIT 1;")
        $metaParts = @($cleanupMeta.Split("|"))
        $cleanupOK = ($metaParts.Count -ge 3 -and [int]$metaParts[0] -ge 1 -and [int]$metaParts[1] -ge 1 -and -not [string]::IsNullOrWhiteSpace([string]$metaParts[2]))
        Write-P8Check $checks $cleanupOK "S10 retention.cleanup payload reports deletions and cutoff" ("meta=$cleanupMeta")

        $snapshot = [pscustomobject]@{
            channel = $channel
            audit_target = $auditTarget
            baseline_cleanup_id = $baseline
            before_counts = $before
            old_events = $oldEvents
            recent_events = $recentEvents
            old_audits = $oldAudits
            recent_audits = $recentAudits
            cleanup_meta = $cleanupMeta
            generated_at = (Get-Date -Format o)
        }
        Write-P8JsonEvidence -Name "s10-retention.json" -Value $snapshot | Out-Null
        $summaryText = "old_deleted=$($oldEvents -eq 0 -and $oldAudits -eq 0) recent_retained=$($recentEvents -eq 1 -and $recentAudits -eq 1) cleanup_audit=$($null -ne $cleanupWait)"
    } catch {
        Write-P8Check $checks $false "S10 retention fatal" (Get-P8ShortText $_.Exception.Message)
        $summaryText = "fatal: " + (Get-P8ShortText $_.Exception.Message 200)
    } finally {
        if (-not (Restore-P8TrackedSetting -Key "retention.event_days")) {
            Write-P8Check $checks $false "S10 restore retention.event_days" ""
        }
        if (-not (Restore-P8TrackedSetting -Key "security.audit_retention_days")) {
            Write-P8Check $checks $false "S10 restore security.audit_retention_days" ""
        }
        Complete-P8TimedScenario -Scenario $scenario -Summary $summaryText
    }
}

# ---------------------------------------------------------------------------
# S10: long execution (12-case real-design attempt, p3-acceptance fallback)
# ---------------------------------------------------------------------------

function Get-P8P3ScenarioEvidence {
    param(
        [string]$Path,
        [DateTime]$NotBefore = [DateTime]::MinValue
    )
    if (-not (Test-Path -LiteralPath $Path)) {
        return [pscustomobject]@{ Exists = $false; Fresh = $false; Pass = 0; Fail = 0; S2 = $false; S3 = $false; S4 = $false; S5 = $false; Lines = @() }
    }
    $lastWrite = (Get-Item -LiteralPath $Path).LastWriteTime
    $fresh = $lastWrite -ge $NotBefore.AddSeconds(-5)
    $lines = @(Get-Content -LiteralPath $Path)
    $pass = @($lines | Where-Object { $_ -match '^PASS\|' })
    $fail = @($lines | Where-Object { $_ -match '^FAIL\|' })
    return [pscustomobject]@{
        Exists = $true
        Fresh  = $fresh
        Pass   = $pass.Count
        Fail   = $fail.Count
        S2     = @($lines | Where-Object { $_ -match '^PASS\|S2 ' }).Count -ge 1
        S3     = @($lines | Where-Object { $_ -match '^PASS\|S3 ' }).Count -ge 1
        S4     = @($lines | Where-Object { $_ -match '^PASS\|S4 ' }).Count -ge 1
        S5     = @($lines | Where-Object { $_ -match '^PASS\|S5 ' }).Count -ge 1
        Lines  = $lines
    }
}

function Invoke-P8P3Fallback {
    $p3Script = Join-Path $script:repoRoot "scripts\p3-acceptance.ps1"
    if (-not (Test-Path -LiteralPath $p3Script)) {
        return [pscustomobject]@{ Available = $false; ExitCode = -1; TimedOut = $false; ElapsedSeconds = 0; Evidence = $null; OutputLog = ""; ErrorLog = "" }
    }
    $p3ApiPort = 15333
    $p3ExecutorPort = 15336
    $p3OrdersPort = 15385
    $p3PaymentsPort = 15386
    $p3WebPort = 16869
    $shell = Get-Command powershell.exe -ErrorAction SilentlyContinue
    if ($null -eq $shell) { $shell = Get-Command pwsh -ErrorAction SilentlyContinue }
    if ($null -eq $shell) { throw "neither powershell.exe nor pwsh is available for the p3 fallback" }

    $outLog = Join-Path $script:evidenceDir "p3-acceptance.out.log"
    $errLog = Join-Path $script:evidenceDir "p3-acceptance.err.log"
    $arguments = @(
        "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $p3Script,
        "-ApiPort", [string]$p3ApiPort,
        "-ExecutorPort", [string]$p3ExecutorPort,
        "-OrdersPort", [string]$p3OrdersPort,
        "-PaymentsPort", [string]$p3PaymentsPort,
        "-WebPort", [string]$p3WebPort
    )
    Write-P8Log "    starting p3-acceptance fallback (isolated DB/ports $p3ApiPort/$p3ExecutorPort/$p3OrdersPort/$p3PaymentsPort)"
    $started = Get-Date
    $process = Start-Process -FilePath $shell.Source -ArgumentList $arguments -PassThru -NoNewWindow -RedirectStandardOutput $outLog -RedirectStandardError $errLog
    $deadline = (Get-Date).AddSeconds(3600)
    $timedOut = $false
    while (-not $process.HasExited -and (Get-Date) -lt $deadline) {
        Start-Sleep -Seconds 3
        try { $process.Refresh() } catch { }
    }
    if (-not $process.HasExited) {
        $timedOut = $true
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        foreach ($port in @($p3ApiPort, $p3ExecutorPort, $p3OrdersPort, $p3PaymentsPort, $p3WebPort)) {
            Stop-P8PortProcesses -Port $port
        }
    }
    $exitCode = -1
    if (-not $timedOut) {
        try { $process.WaitForExit(10000) | Out-Null; $exitCode = [int]$process.ExitCode } catch { $exitCode = -1 }
    }
    foreach ($port in @($p3ApiPort, $p3ExecutorPort, $p3OrdersPort, $p3PaymentsPort, $p3WebPort)) {
        if (Test-P8PortInUse -Port $port) { Stop-P8PortProcesses -Port $port }
    }
    $p3Summary = Join-Path $script:repoRoot ".tmp\p3-acceptance\scenarios.txt"
    $evidence = Get-P8P3ScenarioEvidence -Path $p3Summary -NotBefore $started
    return [pscustomobject]@{
        Available      = $true
        ExitCode       = $exitCode
        TimedOut       = $timedOut
        ElapsedSeconds = [int]((Get-Date) - $started).TotalSeconds
        Evidence       = $evidence
        OutputLog      = $outLog
        ErrorLog       = $errLog
        ScenarioFile   = $p3Summary
    }
}

function Invoke-P8ScenarioS10LongExecution {
    $scenario = Start-P8TimedScenario
    $scenario.Name = "S10-LONG-EXECUTION"
    $checks = $scenario.Checks
    $summaryText = ""
    $path = ""
    $attemptFailure = ""
    $snapshot = $null
    try {
        Write-P8Log "==> S10 long execution (real-design 12-case attempt, then p3 fallback if needed)"
        $binding = $null
        $versionID = ""
        $versionSutID = ""
        $caseVersionIDs = @()
        $wave1ID = ""
        $wave2ID = ""
        $caseCount = 0
        try {
            $binding = Get-P8FixtureBinding -PreferredBaseUrl ("http://127.0.0.1:" + $OrdersPort)
            if ($null -eq $binding) { throw "no orders fixture binding is available for a 12-case design attempt" }
            $version = New-P8ResilienceVersion -Title ("P8 resilience long execution " + $script:runSuffix) -Binding $binding
            $versionID = [string]$version.id
            $null = Invoke-P8Api -Method "PATCH" -Path "/api/versions/$versionID" -Body @{ auto_design_new_requirements = $true }
            $detail = Get-P8VersionDetail -VersionID $versionID
            $bindings = @(Get-P8Array $detail.bindings)
            if ($bindings.Count -lt 1) { throw "new long-execution version has no binding" }
            $versionSutID = [string]$bindings[0].id

            $null = New-P8RequirementForVersion -VersionID $versionID -VersionSutID $versionSutID `
                -Title ("P8-LONG-ORDERS-" + $script:runSuffix) `
                -Description ("The orders fixture order detail page must remain stable through a 12-case, two-wave execution run. Validate visible order text and amounts.") `
                -AcceptanceCriteria ("Order detail shows ORD-1001 and the paid amount 5200.00; the refund amount is 5000.00.")
            $null = New-P8RequirementForVersion -VersionID $versionID -VersionSutID $versionSutID `
                -Title ("P8-LONG-UI-" + $script:runSuffix) `
                -Description ("The orders login and slow pages must be navigable and expose stable headings for twelve independent UI cases.") `
                -AcceptanceCriteria ("The login page renders its sign-in form and the slow page eventually shows its completion state.")

            $designConversation = Invoke-P8Api -Method "POST" -Path "/api/versions/$versionID/conversations" -Body @{ kind = "design"; title = ("P8 resilience long execution design " + $script:runSuffix) }
            if ($designConversation.Status -ne 201 -or $null -eq $designConversation.Json) {
                throw "design conversation creation failed: status=$($designConversation.Status) $($designConversation.Text)"
            }
            $designConversationID = [string]$designConversation.Json.data.id
            $designPrompt = ("Create exactly 12 independent, executable UI test cases in the design graph now. " +
                "The version is bound to the local orders fixture at http://127.0.0.1:$OrdersPort. " +
                "Use version_sut_id=$versionSutID and account_role=user on every step. " +
                "Cases 1-6 must open http://127.0.0.1:$OrdersPort/p3/orders/1, call browser.snapshot and browser.screenshot, and assert stable visible content such as #order-no ORD-1001, #paid-amount 5200.00, or #refund-amount 5000.00. " +
                "Cases 7-12 must open http://127.0.0.1:$OrdersPort/p3/login or http://127.0.0.1:$OrdersPort/p3/slow?ms=500, call browser.snapshot, and assert a stable heading or completion text. " +
                "Each case must have exactly one step and at least one ui assertion. Do not ask questions; produce the 12 cases now.")
            $message = Send-P8ConversationMessage -VersionID $versionID -ConversationID $designConversationID -Text $designPrompt -ClientMessageID ("p8-long-design-" + $script:runSuffix)
            if ($message.Status -ne 201) { throw "design prompt rejected: status=$($message.Status) $($message.Text)" }

            $graphResult = $null
            $attempts = [System.Collections.Generic.List[object]]::new()
            for ($attempt = 1; $attempt -le 3; $attempt++) {
                $deadline = (Get-Date).AddSeconds(300)
                $found = $null
                while ((Get-Date) -lt $deadline) {
                    $graph = Get-P8DesignGraph -VersionID $versionID
                    $cards = @(Get-P8DesignCaseCards -Graph $graph)
                    $attempts.Add([pscustomobject]@{ attempt = $attempt; at = (Get-Date -Format o); cases = $cards.Count })
                    if ($cards.Count -ge 12) { $found = [pscustomobject]@{ Graph = $graph; Cards = $cards }; break }
                    Start-Sleep -Seconds 4
                }
                if ($null -ne $found) { $graphResult = $found; break }
                if ($attempt -lt 3) {
                    $nudge = ("The design graph currently contains " + @(Get-P8DesignCaseCards -Graph (Get-P8DesignGraph -VersionID $versionID)).Count + " cases. Add the missing independent cases now so the graph contains exactly 12 executable cases (6 batch A, 6 batch B). Keep every case single-step with explicit version_sut_id=$versionSutID.")
                    $nudgeResponse = Send-P8ConversationMessage -VersionID $versionID -ConversationID $designConversationID -Text $nudge -ClientMessageID ("p8-long-nudge-" + $attempt + "-" + $script:runSuffix)
                    if ($nudgeResponse.Status -ne 201) { break }
                    Start-Sleep -Seconds 10
                }
            }
            Write-P8JsonEvidence -Name "s10-long-execution-attempts.json" -Value @($attempts) | Out-Null
            if ($null -eq $graphResult) { throw "real design agent did not produce 12 cases within the attempt window" }

            $cards = @($graphResult.Cards)
            $selected = @($cards | Select-Object -First 40)
            $pairs = [System.Collections.Generic.List[object]]::new()
            $seenCaseIDs = [System.Collections.Generic.HashSet[string]]::new()
            foreach ($card in $selected) {
                $caseID = [string]$card.case_id
                if ([string]::IsNullOrWhiteSpace($caseID)) { continue }
                if (-not $seenCaseIDs.Add($caseID)) { continue }
                $caseDetail = Get-P8DesignCaseDetail -VersionID $versionID -CaseID $caseID
                $caseVersionID = [string]$caseDetail.current_version_id
                if ([string]::IsNullOrWhiteSpace($caseVersionID)) { continue }
                $pairs.Add([pscustomobject]@{ CaseID = $caseID; CaseVersionID = $caseVersionID })
                if ($pairs.Count -ge 12) { break }
            }
            if ($pairs.Count -lt 12) { throw "only $($pairs.Count) usable case versions were exposed by the design graph" }
            $caseIDs = @($pairs | ForEach-Object { [string]$_.CaseID })
            $caseVersionIDs = @($pairs | ForEach-Object { [string]$_.CaseVersionID })
            $caseCount = $caseVersionIDs.Count

            $batchA = @($caseIDs[0..5])
            $batchAVersions = @($caseVersionIDs[0..5])
            $batchB = @($caseIDs[6..11])
            $batchBVersions = @($caseVersionIDs[6..11])

            $failedA = @(Invoke-P8ConfirmCaseBatch -VersionID $versionID -CaseIDs $batchA)
            if ($failedA.Count -gt 0) { throw "batch A confirmation failed: $($failedA -join ',')" }
            if (-not (Ensure-P8IntentsForCaseVersions -VersionID $versionID -CaseVersionIDs $batchAVersions -TimeoutSeconds 90)) {
                throw "batch A intents were not created"
            }
            $wave1 = Wait-P8Until -TimeoutSeconds 900 -IntervalMilliseconds 3000 -Description "batch A wave" -Condition {
                $found = Find-P8TerminalWaveForCases -VersionID $versionID -CaseVersionIDs $batchAVersions
                if ($null -ne $found) { return $found }
                return $null
            }
            if ($null -eq $wave1) { throw "batch A did not produce a terminal wave" }
            $wave1ID = [string]$wave1.Wave.id

            $failedB = @(Invoke-P8ConfirmCaseBatch -VersionID $versionID -CaseIDs $batchB)
            if ($failedB.Count -gt 0) { throw "batch B confirmation failed: $($failedB -join ',')" }
            if (-not (Ensure-P8IntentsForCaseVersions -VersionID $versionID -CaseVersionIDs $batchBVersions -TimeoutSeconds 90)) {
                throw "batch B intents were not created"
            }
            $wave2 = Wait-P8Until -TimeoutSeconds 900 -IntervalMilliseconds 3000 -Description "batch B wave" -Condition {
                $found = Find-P8TerminalWaveForCases -VersionID $versionID -CaseVersionIDs $batchBVersions -ExcludeWaveIDs @($wave1ID)
                if ($null -ne $found) { return $found }
                return $null
            }
            if ($null -eq $wave2) { throw "batch B did not produce a terminal wave" }
            $wave2ID = [string]$wave2.Wave.id

            $allRuns = @($wave1.Runs) + @($wave2.Runs)
            $distinct = @($allRuns | Where-Object { $caseVersionIDs -contains [string]$_.case_version_id } | ForEach-Object { [string]$_.case_version_id } | Sort-Object -Unique)
            Write-P8Check $checks ([string]$wave1ID -ne [string]$wave2ID) "S10 long execution used two waves" ("wave1=$wave1ID wave2=$wave2ID")
            Write-P8Check $checks ($distinct.Count -ge 12) "S10 long execution recorded 12 case runs across two waves" ("case_versions=" + $distinct.Count + " runs=" + $allRuns.Count)
            Write-P8Check $checks (@($allRuns | Where-Object { @("done", "failed", "cancelled") -contains [string]$_.status }).Count -ge 12) "S10 long execution runs reached terminal states" ("terminal=" + @($allRuns | Where-Object { @("done", "failed", "cancelled") -contains [string]$_.status }).Count)
            $path = "real-design"
            $summaryText = "path=real-design cases=$($distinct.Count) wave1=$wave1ID wave2=$wave2ID"
            $snapshot = [pscustomobject]@{ path = $path; version_id = $versionID; case_versions = $caseVersionIDs; wave1 = $wave1ID; wave2 = $wave2ID; runs = @($allRuns | ForEach-Object { [pscustomobject]@{ id = $_.id; case_version_id = $_.case_version_id; status = $_.status } }) }
        } catch {
            $attemptFailure = Get-P8ShortText -Text $_.Exception.Message -MaxLength 300
            Add-P8BugNote ("S10 long execution: real-design attempt did not complete; using p3 fallback: " + $attemptFailure)
            Write-P8Log "    real-design attempt failed: $attemptFailure" "Yellow"
            if ($null -ne $snapshot) { $snapshot | Add-Member -NotePropertyName attempt_failure -NotePropertyValue $attemptFailure -Force }
        }

        if ($path -ne "real-design") {
            $fallback = Invoke-P8P3Fallback
            $p3Evidence = $fallback.Evidence
            Write-P8Check $checks ([bool]$fallback.Available) "S10 p3 fallback script available" ("available=$($fallback.Available)")
            Write-P8Check $checks ($fallback.Available -and -not $fallback.TimedOut -and $fallback.ExitCode -eq 0) "S10 p3 fallback exit code 0" ("exit=$($fallback.ExitCode) timed_out=$($fallback.TimedOut) elapsed=$($fallback.ElapsedSeconds)s output=$($fallback.OutputLog)")
            Write-P8Check $checks ($null -ne $p3Evidence -and $p3Evidence.Exists -and $p3Evidence.Fresh -and $p3Evidence.Pass -ge 6 -and $p3Evidence.Fail -eq 0) "S10 p3 fallback deterministic execution scenarios PASS" ("exists=$($p3Evidence.Exists) fresh=$($p3Evidence.Fresh) pass=$($p3Evidence.Pass) fail=$($p3Evidence.Fail) s2=$($p3Evidence.S2) s3=$($p3Evidence.S3) s4=$($p3Evidence.S4) s5=$($p3Evidence.S5)")
            Write-P8Check $checks ($null -ne $p3Evidence -and $p3Evidence.Fresh -and $p3Evidence.S2 -and $p3Evidence.S3 -and $p3Evidence.S4 -and $p3Evidence.S5) "S10 p3 fallback covers seeded cases, wave, single-SUT and cross-SUT execution" ("fresh=$($p3Evidence.Fresh) s2=$($p3Evidence.S2) s3=$($p3Evidence.S3) s4=$($p3Evidence.S4) s5=$($p3Evidence.S5)")
            $path = "p3-acceptance-fallback"
            if ($null -ne $p3Evidence -and $p3Evidence.Exists) {
                $summaryText = "path=p3-fallback exit=$($fallback.ExitCode) p3_pass=$($p3Evidence.Pass) p3_fail=$($p3Evidence.Fail) attempts=6 cases"
            } else {
                $summaryText = "path=p3-fallback exit=$($fallback.ExitCode) evidence=missing"
            }
            $snapshot = [pscustomobject]@{
                path = $path
                attempt_failure = $attemptFailure
                p3_available = $fallback.Available
                p3_exit_code = $fallback.ExitCode
                p3_timed_out = $fallback.TimedOut
                p3_elapsed_seconds = $fallback.ElapsedSeconds
                p3_scenarios = $p3Evidence
                p3_output_log = $fallback.OutputLog
                p3_error_log = $fallback.ErrorLog
                generated_at = (Get-Date -Format o)
            }
        }
        Write-P8JsonEvidence -Name "s10-long-execution.json" -Value $snapshot | Out-Null
    } catch {
        Write-P8Check $checks $false "S10 long execution fatal" (Get-P8ShortText $_.Exception.Message)
        $summaryText = "fatal: " + (Get-P8ShortText $_.Exception.Message 200)
    } finally {
        Complete-P8TimedScenario -Scenario $scenario -Summary $summaryText
    }
}

# ---------------------------------------------------------------------------
# evidence summary / process cleanup / main
# ---------------------------------------------------------------------------

function Write-P8EnvironmentEvidence {
    $lines = [System.Collections.Generic.List[string]]::new()
    $lines.Add("EzTest P8 resilience acceptance")
    $lines.Add("started_at: " + (Get-Date -Format o))
    $lines.Add("script: " + $PSCommandPath)
    $lines.Add("database: " + $DatabaseName)
    $lines.Add("template_database: " + $TemplateDatabase)
    $lines.Add("skip_database_reset: " + [bool]$SkipDatabaseReset)
    $lines.Add("api_port: " + $ApiPort)
    $lines.Add("renderer_port: " + $RendererPort)
    $lines.Add("executor_a_port: " + $ExecutorAPort)
    $lines.Add("executor_b_port: " + $ExecutorBPort)
    $lines.Add("orders_fixture_port: " + $OrdersPort)
    $lines.Add("payments_fixture_port: " + $PaymentsPort)
    $lines.Add("evidence_dir: " + $script:evidenceDir)
    $branch = ""
    $commit = ""
    try { $branch = ((& git rev-parse --abbrev-ref HEAD) | Out-String).Trim() } catch { $branch = "" }
    try { $commit = ((& git rev-parse --short HEAD) | Out-String).Trim() } catch { $commit = "" }
    $lines.Add("git_branch: " + $branch)
    $lines.Add("git_commit: " + $commit)
    $lines.Add("scenarios: " + $(if ($script:scenarioFilter.Count -eq 0) { "all" } else { $script:scenarioFilter -join "," }))
    Write-P8Evidence -Name "environment.txt" -Content ($lines -join [Environment]::NewLine) | Out-Null
    $command = "pwsh scripts/p8-resilience.ps1 -ApiPort $ApiPort -RendererPort $RendererPort -ExecutorAPort $ExecutorAPort -ExecutorBPort $ExecutorBPort -OrdersPort $OrdersPort -PaymentsPort $PaymentsPort -DatabaseName $DatabaseName"
    if ($Scenarios.Count -gt 0) { $command += " -Scenarios " + ($Scenarios -join ",") }
    if ($SkipDatabaseReset) { $command += " -SkipDatabaseReset" }
    Write-P8Evidence -Name "run-command.txt" -Content $command | Out-Null
}

function Write-P8ResilienceMarkdown {
    $context = Get-P8Context
    $lines = [System.Collections.Generic.List[string]]::new()
    $lines.Add("# EzTest 0.2 P8 resilience acceptance (S9/S10)")
    $lines.Add("")
    $lines.Add("- Date: " + (Get-Date -Format o))
    $lines.Add("- Branch/commit: recorded in environment.txt")
    $lines.Add("- Database: $DatabaseName (template $TemplateDatabase, skip_reset=$([bool]$SkipDatabaseReset))")
    $lines.Add("- API: http://127.0.0.1:$ApiPort ; renderer: http://127.0.0.1:$RendererPort")
    $lines.Add("- Executors: http://127.0.0.1:$ExecutorAPort, http://127.0.0.1:$ExecutorBPort")
    $lines.Add("- Fixtures: http://127.0.0.1:$OrdersPort (orders), http://127.0.0.1:$PaymentsPort (payments)")
    $lines.Add("- Evidence: $script:evidenceDir")
    $lines.Add("- Exit status: " + $(if ($context.Failures.Count -gt 0) { "1 (FAIL)" } else { "0 (PASS)" }))
    $lines.Add("")
    $lines.Add("## Results")
    $lines.Add("")
    $lines.Add("| Scenario | Result | Duration | Summary |")
    $lines.Add("|---|---|---|---|")
    foreach ($result in @($context.ScenarioResults)) {
        $state = "FAIL"
        if ($result.Pass) { $state = "PASS" }
        $summary = ([string]$result.Summary).Replace("|", "/")
        $lines.Add("| $($result.Name) | $state | $($result.DurationSeconds)s | $summary |")
    }
    $lines.Add("")
    $lines.Add("## Check detail")
    $lines.Add("")
    foreach ($result in @($context.ScenarioResults)) {
        $lines.Add("### $($result.Name) - " + $(if ($result.Pass) { "PASS" } else { "FAIL" }))
        $lines.Add("")
        foreach ($check in @($result.Checks)) {
            $mark = "FAIL"
            if ($check.Ok) { $mark = "PASS" }
            $lines.Add("- $mark $($check.Label) :: $($check.Detail)")
        }
        $lines.Add("")
    }
    if ($context.BugNotes.Count -gt 0) {
        $lines.Add("## Bug notes")
        $lines.Add("")
        foreach ($note in @($context.BugNotes)) { $lines.Add("- $note") }
        $lines.Add("")
    }
    Write-P8Evidence -Name "resilience.md" -Content ($lines -join [Environment]::NewLine) | Out-Null
}

function Stop-P8ResilienceProcesses {
    if (-not $script:startedProcesses) { return }
    Write-P8Log "==> Stopping resilience processes"
    foreach ($record in @($script:context.Processes)) {
        if ($null -eq $record) { continue }
        Stop-P8Process -Process $record.Process
    }
    foreach ($port in @($ApiPort, $RendererPort, $ExecutorAPort, $ExecutorBPort, $OrdersPort, $PaymentsPort)) {
        if (Test-P8PortInUse -Port $port) {
            Stop-P8PortProcesses -Port $port
            Wait-P8PortClosed -Port $port -TimeoutSeconds 10 | Out-Null
        }
    }
}

Write-P8EnvironmentEvidence
$bootstrapChecks = [System.Collections.Generic.List[object]]::new()
$bootstrapStarted = Get-Date
try {
    Initialize-P8Environment
    if (Test-P8ScenarioSelected -Name "S9") {
        Invoke-P8ScenarioS9
    }
    if (Test-P8ScenarioSelected -Name "S10-VERSION") {
        Invoke-P8ScenarioS10Version
    }
    if (Test-P8ScenarioSelected -Name "S10-RATE-LIMIT") {
        Invoke-P8ScenarioS10RateLimit
    }
    if (Test-P8ScenarioSelected -Name "S10-RETENTION") {
        Invoke-P8ScenarioS10Retention
    }
    if (Test-P8ScenarioSelected -Name "S10-LONG-EXECUTION") {
        Invoke-P8ScenarioS10LongExecution
    }
} catch {
    Write-P8Check $bootstrapChecks $false "bootstrap" (Get-P8ShortText $_.Exception.Message)
    Write-P8ScenarioSummary -Name "BOOTSTRAP" -Checks $bootstrapChecks -Summary ("fatal: " + (Get-P8ShortText $_.Exception.Message 200)) -DurationSeconds ([int]((Get-Date) - $bootstrapStarted).TotalSeconds) | Out-Null
} finally {
    try { Restore-P8AllTrackedSettings } catch {
        $script:cleanupRestoreFailures += 1
        Add-P8BugNote ("cleanup: settings restore threw: " + (Get-P8ShortText $_.Exception.Message))
    }
    try { Stop-P8ResilienceProcesses } catch {
        Add-P8BugNote ("cleanup: process stop threw: " + (Get-P8ShortText $_.Exception.Message))
    }
    if ($script:cleanupRestoreFailures -gt 0) {
        $script:context.Failures.Add("cleanup: $($script:cleanupRestoreFailures) setting restore failure(s)")
    }
    Write-P8ResilienceMarkdown
    $elapsed = (Get-Date) - $script:startedAt
    Write-P8Log ("RESULT: " + $(if ($script:context.Failures.Count -eq 0) { "PASS" } else { "FAIL" }) + " (" + ("{0:N1}" -f $elapsed.TotalSeconds) + "s)") $(if ($script:context.Failures.Count -eq 0) { "Green" } else { "Red" })
    Write-P8Log ("Evidence: " + $script:evidenceDir)
}

if ($script:context.Failures.Count -gt 0) {
    exit 1
}
exit 0
