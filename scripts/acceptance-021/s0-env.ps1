#requires -Version 5.1
<#
.SYNOPSIS
    S0 environment self-check for the EzTest 0.2.1 acceptance.

.DESCRIPTION
    Asserts that every prerequisite the later scenarios rely on is reachable
    right now: the EzTest API and web app, the EzTest login, the real LLM
    configuration, the Playwright executor, the 墨栈博客 SUT web/API and the
    three SUT accounts (admin / openClaw / Hermes).

    The script is unattended: no prompts, deterministic exit codes, and every
    observation is written under
    docs/version-V0.2.1/acceptance/evidence/s0-s2/s0/.

    Environment prerequisites are checked, not installed. If a service is
    down, S0 reports the exact URL/error and exits 2 (blocked). It never fakes
    a pass.

.PARAMETER ApiBaseUrl
    EzTest API origin. Defaults to http://localhost:<EZTEST_API_PORT> (13333).

.PARAMETER WebBaseUrl
    EzTest web origin. Defaults to http://localhost:<EZTEST_WEB_PORT> (6868).

.PARAMETER ExecutorUrl
    Playwright executor origin. Defaults to http://127.0.0.1:<EZTEST_EXECUTOR_PORT>
    (13336).

.PARAMETER SutWebBaseUrl
    墨栈博客 frontend origin. Defaults to http://localhost:5173.

.PARAMETER SutApiBaseUrl
    墨栈博客 API origin. Defaults to http://localhost:8080.

.NOTES
    Exit codes: 0 = passed, 1 = failed acceptance assertion, 2 = blocked
    environment.
#>
[CmdletBinding()]
param(
    [string] $ApiBaseUrl = "",
    [string] $WebBaseUrl = "",
    [string] $ExecutorUrl = "",
    [string] $SutWebBaseUrl = "",
    [string] $SutApiBaseUrl = "",
    [string] $AdminUsername = "",
    [string] $AdminPassword = "",
    [string] $SutPassword = "",
    [string] $EvidenceRoot = ""
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "lib.ps1")

$context = Initialize-AcceptanceContext -ApiBaseUrl $ApiBaseUrl -WebBaseUrl $WebBaseUrl `
    -ExecutorUrl $ExecutorUrl -SutWebBaseUrl $SutWebBaseUrl -SutApiBaseUrl $SutApiBaseUrl `
    -AdminUsername $AdminUsername -AdminPassword $AdminPassword -SutPassword $SutPassword `
    -EvidenceRoot $EvidenceRoot
$scenario = New-AcceptanceScenario -Context $context -Name "s0" -Title "环境自检 (SUT/EzTest/LLM/executor)"

try {
    Write-AcceptanceLog -Scenario $scenario -Message "repo=$($context.RepoRoot) api=$($context.ApiBaseUrl) web=$($context.WebBaseUrl) executor=$($context.ExecutorUrl) sut=$($context.SutWebBaseUrl)/$($context.SutApiBaseUrl)"

    # ------------------------------------------------------------------
    # EzTest API + web + login
    # ------------------------------------------------------------------
    $health = $null
    try {
        $health = Invoke-Api -Context $context -Method "GET" -Path "/api/health" -TimeoutSeconds 20
        Write-EvidenceJson -Path (Join-Path $scenario.Directory "eztest-health.json") -Value $health.Json
        Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "eztest-health.json") -Kind "api" -Note "EzTest readiness"
    } catch {
        $health = $null
        $scenario.ErrorMessage = "EzTest API health request failed: $($_.Exception.Message)"
    }
    if ($null -eq $health) {
        Add-AcceptanceCheck -Scenario $scenario -Name "EzTest API reachable at $($context.ApiBaseUrl)/api/health" -Passed $false -Severity blocker -Detail "no HTTP response; start the API with scripts/dev-api.ps1 or scripts/dev-all.ps1"
    } else {
        $apiStatus = [string](Get-JsonProp -Object $health.Data -Name "status" -Default "")
        $dbStatus = [string](Get-JsonProp -Object $health.Data -Name "db" -Default "")
        $settingsLoaded = [bool](Get-JsonProp -Object $health.Data -Name "settings_loaded" -Default $false)
        $version = [string](Get-JsonProp -Object $health.Data -Name "version" -Default "")
        $executorUrlFromApi = [string](Get-JsonProp -Object $health.Data -Name "executor_url" -Default "")
        Add-AcceptanceCheck -Scenario $scenario -Name "EzTest API readiness" -Passed ($health.Status -eq 200 -and $apiStatus -eq "ok" -and $dbStatus -eq "ok") -Severity blocker `
            -Detail "HTTP $($health.Status) status=$apiStatus db=$dbStatus settings_loaded=$settingsLoaded version=$version"
        Add-AcceptanceCheck -Scenario $scenario -Name "EzTest runtime settings loaded" -Passed $settingsLoaded -Severity blocker -Detail "settings_loaded=$settingsLoaded"
        if ($version -notmatch '^0\.2\.1') {
            Add-AcceptanceCheck -Scenario $scenario -Name "EzTest build reports 0.2.1" -Passed $false -Severity warning -Detail "health.version=$version (expected 0.2.1; the health constant may not have been bumped)"
        } else {
            Add-AcceptanceCheck -Scenario $scenario -Name "EzTest build reports 0.2.1" -Passed $true -Severity warning -Detail "health.version=$version"
        }
        Add-AcceptanceCheck -Scenario $scenario -Name "API executor URL" -Passed (-not [string]::IsNullOrWhiteSpace($executorUrlFromApi)) -Severity warning -Detail "executor_url=$executorUrlFromApi"
    }

    if ($context.DatabaseName -ne "eztest_v021") {
        Add-AcceptanceCheck -Scenario $scenario -Name "acceptance database is eztest_v021" -Passed $false -Severity warning `
            -Detail "EZTEST_DATABASE_URL points at '$($context.DatabaseName)'; spec 12.5 asks for a fresh eztest_v021 database"
    } else {
        Add-AcceptanceCheck -Scenario $scenario -Name "acceptance database is eztest_v021" -Passed $true -Severity warning -Detail "database=$($context.DatabaseName)"
    }

    $web = Test-EzTestWebReachable -Context $context -TimeoutSeconds 30
    Add-AcceptanceCheck -Scenario $scenario -Name "EzTest web reachable at $($context.WebBaseUrl)/login" -Passed $web.Ok -Severity blocker -Detail ($(if ($web.Ok) { "HTTP $($web.Status)" } else { "HTTP $($web.Status) $($web.Error)" }))

    $login = Connect-EzTest -Context $context
    if ($login.Ok) {
        Add-AcceptanceCheck -Scenario $scenario -Name "EzTest admin login" -Passed $true -Severity blocker `
            -Detail "user=$([string](Get-JsonProp -Object $login.User -Name "username" -Default "?")) role=$([string](Get-JsonProp -Object $login.User -Name "role" -Default "?"))"
    } else {
        Add-AcceptanceCheck -Scenario $scenario -Name "EzTest admin login" -Passed $false -Severity blocker `
            -Detail "credentials rejected ($($login.Error)); set EZTEST_ADMIN_PASSWORD and SUT_PASSWORD in the environment or .env (no built-in fallback)"
    }

    # ------------------------------------------------------------------
    # Playwright executor
    # ------------------------------------------------------------------
    $executor = Test-ExecutorReachable -Context $context -TimeoutSeconds 15
    Add-AcceptanceCheck -Scenario $scenario -Name "Playwright executor reachable at $($context.ExecutorUrl)/healthz" -Passed $executor.Ok -Severity blocker `
        -Detail ($(if ($executor.Ok) { "status=ok browser=$($executor.BrowserVersion)" } else { "HTTP $($executor.Status) $($executor.Error)" }))
    Write-EvidenceJson -Path (Join-Path $scenario.Directory "executor-health.json") -Value ([pscustomobject]@{
        ok = $executor.Ok; status = $executor.Status; browserVersion = $executor.BrowserVersion; error = $executor.Error; url = "$($context.ExecutorUrl)/healthz"
    })
    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "executor-health.json") -Kind "api" -Note "executor /healthz"

    # ------------------------------------------------------------------
    # Real LLM configuration
    # ------------------------------------------------------------------
    $llm = $null
    if ($login.Ok) {
        try {
            $llm = Initialize-LlmConfiguration -Context $context -TimeoutSeconds 90
            Write-EvidenceJson -Path (Join-Path $scenario.Directory "llm-config.json") -Value $llm
            Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "llm-config.json") -Kind "api" -Note "LLM config + connectivity test"
        } catch {
            $llm = [pscustomobject]@{ Ok = $false; Error = $_.Exception.Message; Seeded = $false; Model = ""; LatencyMS = 0 }
        }
        Add-AcceptanceCheck -Scenario $scenario -Name "real LLM configured and reachable" -Passed ([bool]$llm.Ok) -Severity blocker `
            -Detail ($(if ($llm.Ok) { "model=$($llm.Model) latency=$($llm.LatencyMS)ms seeded=$($llm.Seeded)" } else { $llm.Error }))
    } else {
        Add-AcceptanceCheck -Scenario $scenario -Name "real LLM configured and reachable" -Passed $false -Severity blocker -Detail "skipped because EzTest admin login failed"
    }

    # ------------------------------------------------------------------
    # 墨栈博客 SUT (read-only checks; no SUT code or configuration changes)
    # ------------------------------------------------------------------
    $sutApiHealth = $null
    try {
        $sutApiHealth = Invoke-Api -Context $context -Method "GET" -Path "/api/v1/health" -BaseUrl $context.SutApiBaseUrl -TimeoutSeconds 15
        Write-EvidenceJson -Path (Join-Path $scenario.Directory "sut-health.json") -Value $sutApiHealth.Json
        Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "sut-health.json") -Kind "api" -Note "SUT API health"
    } catch {
        $sutApiHealth = $null
    }
    if ($null -eq $sutApiHealth) {
        Add-AcceptanceCheck -Scenario $scenario -Name "SUT API reachable at $($context.SutApiBaseUrl)/api/v1/health" -Passed $false -Severity blocker -Detail "no HTTP response"
    } else {
        $sutStatus = [string](Get-JsonProp -Object $sutApiHealth.Data -Name "status" -Default "")
        Add-AcceptanceCheck -Scenario $scenario -Name "SUT API reachable at $($context.SutApiBaseUrl)/api/v1/health" -Passed ($sutApiHealth.Status -eq 200 -and $sutStatus -eq "ok") -Severity blocker `
            -Detail "HTTP $($sutApiHealth.Status) status=$sutStatus"
    }

    $sutWeb = $null
    try {
        $sutWeb = Invoke-Api -Context $context -Method "GET" -Path "$($context.SutWebBaseUrl)/" -TimeoutSeconds 20 -Accept "text/html"
    } catch { $sutWeb = $null }
    Add-AcceptanceCheck -Scenario $scenario -Name "SUT web reachable at $($context.SutWebBaseUrl)" -Passed ($null -ne $sutWeb -and $sutWeb.Status -eq 200) -Severity blocker `
        -Detail ($(if ($null -ne $sutWeb) { "HTTP $($sutWeb.Status)" } else { "no HTTP response" }))

    $accountChecks = @(
        @{ name = "admin"; role = "admin" },
        @{ name = "openClaw"; role = "author" },
        @{ name = "Hermes"; role = "author" }
    )
    $sutLoginResults = @()
    foreach ($account in $accountChecks) {
        $result = Connect-SutAccount -Context $context -Account $account.name
        $sutLoginResults += [pscustomobject]@{
            account = $account.name
            ok = $result.Ok
            status = $result.Status
            error = $result.Error
            role = [string](Get-JsonProp -Object $result.User -Name "role" -Default "")
        }
        Add-AcceptanceCheck -Scenario $scenario -Name "SUT login: $($account.name)" -Passed $result.Ok -Severity blocker `
            -Detail ($(if ($result.Ok) { "HTTP 200 role=$([string](Get-JsonProp -Object $result.User -Name "role" -Default "?"))" } else { "HTTP $($result.Status) $($result.Error)" }))
    }
    Write-EvidenceJson -Path (Join-Path $scenario.Directory "sut-logins.json") -Value $sutLoginResults
    Add-AcceptanceArtifact -Scenario $scenario -Path (Join-Path $scenario.Directory "sut-logins.json") -Kind "api" -Note "SUT account logins (no tokens recorded)"

    # ------------------------------------------------------------------
    # Screenshots (evidence, not a pass criterion by themselves)
    # ------------------------------------------------------------------
    if ($login.Ok) {
        try {
            $shot = Save-AcceptanceScreenshot -Context $context -Scenario $scenario -Route "/versions" -FileName "s0-eztest-versions.png" `
                -WaitForSelector "body" -Note "EzTest versions page after admin login"
            Add-AcceptanceCheck -Scenario $scenario -Name "EzTest UI screenshot" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $shot.Path)
        } catch {
            Add-AcceptanceCheck -Scenario $scenario -Name "EzTest UI screenshot" -Passed $false -Severity warning -Detail $_.Exception.Message
        }
    }
    try {
        $sutShot = Save-AcceptanceScreenshot -Context $context -Scenario $scenario -Route "/" -FileName "s0-sut-home.png" `
            -WaitForSelector "body" -Note "墨栈博客 home page" -FullPage $false -BaseUrl $context.SutWebBaseUrl
        Add-AcceptanceCheck -Scenario $scenario -Name "SUT UI screenshot" -Passed $true -Severity warning -Detail (Resolve-RepoRelativePath -Context $context -Path $sutShot.Path)
    } catch {
        Add-AcceptanceCheck -Scenario $scenario -Name "SUT UI screenshot" -Passed $false -Severity warning -Detail $_.Exception.Message
    }

    if (-not $login.Ok) {
        $scenario.ErrorMessage = "EzTest admin login failed; later scenarios cannot run"
    }
} catch {
    $scenario.ErrorMessage = $_.Exception.Message
    Add-AcceptanceCheck -Scenario $scenario -Name "S0 unexpected error" -Passed $false -Severity blocker -Detail ($_.Exception.Message + " | " + $_.ScriptStackTrace)
} finally {
    $exitCode = Complete-AcceptanceScenario -Scenario $scenario -Message "S0 environment self-check"
}

exit $exitCode
