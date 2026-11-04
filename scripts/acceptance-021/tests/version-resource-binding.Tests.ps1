#requires -Version 5.1
# Focused tests for the S1/S3 version -> discovered-resource binding order
# (Task 86 / P10-T11). These helpers are pure so they can be tested without a
# live stack:
#   powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-021/tests/version-resource-binding.Tests.ps1
Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

$testsDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$libDir = Split-Path -Parent $testsDir
. (Join-Path $libDir "lib.ps1")

$script:failures = 0
$script:checks = 0

function Assert-True {
    param([bool] $Condition, [string] $Message)
    $script:checks++
    if (-not $Condition) {
        $script:failures++
        Write-Host "[FAIL] $Message" -ForegroundColor Red
    } else {
        Write-Host "[PASS] $Message"
    }
}

function Assert-Equal {
    param($Expected, $Actual, [string] $Message)
    Assert-True -Condition ($Expected -eq $Actual) -Message ("{0} (expected={1} actual={2})" -f $Message, $Expected, $Actual)
}

function New-TestPage {
    param(
        [string] $Id,
        [string] $Name,
        [string] $Url,
        [string] $Route,
        [string] $Status,
        [string] $Source = "ai"
    )
    return [pscustomobject]@{
        id             = $Id
        name           = $Name
        url            = $Url
        route_template = $Route
        status         = $Status
        source         = $Source
    }
}

function New-TestApi {
    param(
        [string] $Id,
        [string] $Method,
        [string] $Path,
        [string] $Status,
        [string] $Source = "ai"
    )
    return [pscustomobject]@{
        id            = $Id
        method        = $Method
        path_template = $Path
        status        = $Status
        source        = $Source
    }
}

function New-TestVersion {
    param(
        [AllowEmptyCollection()] $Pages = @(),
        [AllowEmptyCollection()] $Apis = @()
    )
    return [pscustomobject]@{
        id       = "9001"
        title    = "fixture version"
        bindings = @(
            [pscustomobject]@{
                pages = @($Pages)
                apis  = @($Apis)
            }
        )
    }
}

# ---------------------------------------------------------------------------
# Discovery resources: only proposed/approved rows are bindable, IDs are
# deduplicated and their order is stable.
# ---------------------------------------------------------------------------

function Test-BindableResourcesFiltersRejectedAndDeduplicates {
    $pages = @(
        (New-TestPage -Id "10" -Name "登录页" -Url "http://localhost:5173/login" -Route "/login" -Status "proposed"),
        (New-TestPage -Id "11" -Name "文章列表" -Url "http://localhost:5173/posts" -Route "/posts" -Status "approved"),
        (New-TestPage -Id "12" -Name "后台" -Url "http://localhost:5173/admin" -Route "/admin" -Status "rejected"),
        (New-TestPage -Id "10" -Name "登录页" -Url "http://localhost:5173/login" -Route "/login" -Status "proposed")
    )
    $apis = @(
        (New-TestApi -Id "20" -Method "GET" -Path "/api/v1/posts" -Status "proposed"),
        (New-TestApi -Id "21" -Method "POST" -Path "/api/v1/posts" -Status "approved"),
        (New-TestApi -Id "22" -Method "DELETE" -Path "/api/v1/posts/1" -Status "rejected"),
        (New-TestApi -Id "23" -Method "GET" -Path "/api/v1/health" -Status "")
    )

    $bindable = Get-AcceptanceBindableResources -Pages $pages -APIs $apis

    Assert-Equal -Expected 2 -Actual $bindable.PageCount -Message "proposed+approved pages are bindable (rejected and duplicate skipped)"
    Assert-Equal -Expected "10,11" -Actual ($bindable.PageIds -join ",") -Message "page IDs keep their stable order and deduplicate"
    Assert-Equal -Expected 2 -Actual $bindable.ApiCount -Message "proposed+approved APIs are bindable"
    Assert-Equal -Expected "20,21" -Actual ($bindable.ApiIds -join ",") -Message "API IDs keep their stable order"
    Assert-Equal -Expected 2 -Actual $bindable.SkippedPageCount -Message "the rejected and duplicate pages are counted as skipped"
    Assert-Equal -Expected 2 -Actual $bindable.SkippedApiCount -Message "the rejected and status-less APIs are counted as skipped"
}

function Test-BindableResourcesHonorsRequestedStatuses {
    $pages = @(
        (New-TestPage -Id "10" -Name "登录页" -Url "http://localhost:5173/login" -Route "/login" -Status "proposed"),
        (New-TestPage -Id "11" -Name "文章列表" -Url "http://localhost:5173/posts" -Route "/posts" -Status "approved")
    )
    $bindable = Get-AcceptanceBindableResources -Pages $pages -APIs @() -Statuses @("proposed")
    Assert-Equal -Expected 1 -Actual $bindable.PageCount -Message "an explicit status filter binds only the proposed pages"
    Assert-Equal -Expected "10" -Actual ($bindable.PageIds -join ",") -Message "the approved page is excluded by the explicit filter"
}

function Test-BindableResourcesEmptyEnvironment {
    $bindable = Get-AcceptanceBindableResources -Pages @() -APIs @()
    Assert-Equal -Expected 0 -Actual $bindable.PageCount -Message "an environment without page resources has 0 bindable pages"
    Assert-Equal -Expected 0 -Actual $bindable.ApiCount -Message "an environment without API resources has 0 bindable APIs"
    Assert-Equal -Expected 0 -Actual @($bindable.PageIds).Count -Message "empty page input yields an empty ID array"
    Assert-Equal -Expected 0 -Actual @($bindable.ApiIds).Count -Message "empty API input yields an empty ID array"
}

# ---------------------------------------------------------------------------
# Binding payload: the working version is created from the resolved binding
# plus the discovered page/API IDs. Everything else is preserved.
# ---------------------------------------------------------------------------

function Test-BindingPayloadCarriesResourcesAndAccounts {
    $accounts = @(@{ account_id = "100"; role = "admin" })
    $payload = New-AcceptanceBindingPayload -SutId "1" -EnvironmentId "2" -CodeRepositoryId "3" `
        -Accounts $accounts -PageIds @("10", "11") -ApiIds @("20")

    Assert-Equal -Expected "1" -Actual ([string]$payload.sut_id) -Message "binding payload keeps the SUT id"
    Assert-Equal -Expected "2" -Actual ([string]$payload.environment_id) -Message "binding payload keeps the environment id"
    Assert-Equal -Expected "3" -Actual ([string]$payload.code_repository_id) -Message "binding payload keeps the repository id"
    Assert-Equal -Expected "10,11" -Actual ($payload.page_ids -join ",") -Message "binding payload carries the discovered page IDs"
    Assert-Equal -Expected "20" -Actual ($payload.api_ids -join ",") -Message "binding payload carries the discovered API IDs"
    Assert-Equal -Expected "admin" -Actual ([string]$payload.accounts[0].role) -Message "binding payload keeps the account roles"

    $empty = New-AcceptanceBindingPayload -SutId "1" -EnvironmentId "2"
    Assert-Equal -Expected 0 -Actual @($empty.page_ids).Count -Message "a binding without resources has an empty page_ids array"
    Assert-Equal -Expected 0 -Actual @($empty.api_ids).Count -Message "a binding without resources has an empty api_ids array"
}

function Test-BindingWithResourcesReplacesOnlyTheScope {
    $binding = New-AcceptanceBindingPayload -SutId "1" -EnvironmentId "2" -CodeRepositoryId "3" `
        -Accounts @(@{ account_id = "100"; role = "admin" })
    $resources = Get-AcceptanceBindableResources -Pages @(
            (New-TestPage -Id "10" -Name "登录页" -Url "http://localhost:5173/login" -Route "/login" -Status "proposed")
        ) -APIs @(
            (New-TestApi -Id "20" -Method "GET" -Path "/api/v1/posts" -Status "proposed")
        )

    $withResources = New-AcceptanceBindingWithResources -Binding $binding -Resources $resources

    Assert-Equal -Expected "1" -Actual ([string]$withResources.sut_id) -Message "resource binding keeps the SUT id"
    Assert-Equal -Expected "100" -Actual ([string]$withResources.accounts[0].account_id) -Message "resource binding keeps the account bindings"
    Assert-Equal -Expected "10" -Actual ($withResources.page_ids -join ",") -Message "resource binding adds the discovered page IDs"
    Assert-Equal -Expected "20" -Actual ($withResources.api_ids -join ",") -Message "resource binding adds the discovered API IDs"
    Assert-Equal -Expected 0 -Actual @($binding.page_ids).Count -Message "the source binding is not mutated"
}

# ---------------------------------------------------------------------------
# Version read model: the hard gate reads the bound pages/APIs, never the
# discovery facts. This is the regression that made S1/S3 pass on 0 pages.
# ---------------------------------------------------------------------------

function Test-VersionCountsExposeBoundResourcesAndLocators {
    $version = New-TestVersion -Pages @(
        (New-TestPage -Id "10" -Name "登录页" -Url "http://localhost:5173/login" -Route "/login" -Status "proposed"),
        (New-TestPage -Id "11" -Name "无路由页面" -Url "http://localhost:5173/raw" -Route "" -Status "approved")
    ) -Apis @(
        (New-TestApi -Id "20" -Method "GET" -Path "/api/v1/posts" -Status "proposed")
    )

    $counts = Get-AcceptanceVersionResourceCounts -Version $version
    Assert-Equal -Expected 2 -Actual $counts.PageCount -Message "version counts read the bound pages"
    Assert-Equal -Expected 1 -Actual $counts.ApiCount -Message "version counts read the bound APIs"
    Assert-Equal -Expected "10,11" -Actual ($counts.PageIds -join ",") -Message "version counts expose the bound page IDs"
    Assert-Equal -Expected 1 -Actual $counts.RoutePageCount -Message "only pages with a route template are design locators"
    Assert-Equal -Expected 2 -Actual $counts.UrlPageCount -Message "pages with a SUT URL are execution-navigable"

    $check = Get-AcceptanceVersionResourceCheck -Counts $counts -RequireApis -RequireLocators -RequirePageUrls
    Assert-Equal -Expected $true -Actual $check.Passed -Message "pages + APIs + one route locator + page URLs pass the hard gate"
    Assert-Equal -Expected $true -Actual $check.PagePassed -Message "the page gate reports version_pages > 0"
    Assert-Equal -Expected $true -Actual $check.ApiPassed -Message "the API gate reports version_apis > 0"
    Assert-Equal -Expected $true -Actual $check.LocatorPassed -Message "the locator gate reports a route template"
    Assert-Equal -Expected $true -Actual $check.UrlPassed -Message "the URL gate reports a navigable SUT page URL"
}

function Test-VersionResourceGateFailsOnPreFixEvidenceShape {
    # This is the S1 evidence shape that shipped with the defect: discovery
    # recorded page=4/api=47 version facts, but the version was created before
    # discovery and its immutable binding carries no pages/APIs.
    $facts = [pscustomobject]@{ login_session = 1; page = 4; api = 47 }
    $version = New-TestVersion -Pages @() -Apis @()

    $counts = Get-AcceptanceVersionResourceCounts -Version $version
    Assert-Equal -Expected 0 -Actual $counts.PageCount -Message "the pre-fix version binds 0 pages even though discovery recorded $($facts.page) page facts"
    Assert-Equal -Expected 0 -Actual $counts.ApiCount -Message "the pre-fix version binds 0 APIs even though discovery recorded $($facts.api) API facts"

    $check = Get-AcceptanceVersionResourceCheck -Counts $counts -RequireApis -RequireLocators
    Assert-Equal -Expected $false -Actual $check.Passed -Message "the hard gate FAILS on the pre-fix 0-page version (it must not pass on facts alone)"
    Assert-Equal -Expected $false -Actual $check.PagePassed -Message "the page gate fails on the pre-fix version"
    Assert-True -Condition ($check.PageDetail -match 'pages=0') -Message "the failure detail reports pages=0"
    Assert-True -Condition ($check.ApiDetail -match 'apis=0') -Message "the failure detail reports apis=0"
}

function Test-VersionResourceGateRequiresApisOnlyWhenExpected {
    $counts = Get-AcceptanceVersionResourceCounts -Version (New-TestVersion -Pages @(
            (New-TestPage -Id "10" -Name "登录页" -Url "http://localhost:5173/login" -Route "/login" -Status "proposed")
        ) -Apis @())

    $withoutApis = Get-AcceptanceVersionResourceCheck -Counts $counts -RequireLocators
    Assert-Equal -Expected $true -Actual $withoutApis.Passed -Message "pages alone pass when APIs are not expected"

    $withApis = Get-AcceptanceVersionResourceCheck -Counts $counts -RequireApis -RequireLocators
    Assert-Equal -Expected $false -Actual $withApis.Passed -Message "the API gate fails when API resources are expected but missing"
    Assert-Equal -Expected $false -Actual $withApis.ApiPassed -Message "the API gate reports the missing APIs"
}

function Test-VersionResourceGateRequiresRouteLocator {
    $counts = Get-AcceptanceVersionResourceCounts -Version (New-TestVersion -Pages @(
            (New-TestPage -Id "10" -Name "无路由页面" -Url "http://localhost:5173/raw" -Route "" -Status "proposed")
        ) -Apis @(
            (New-TestApi -Id "20" -Method "GET" -Path "/api/v1/posts" -Status "proposed")
        ))

    $withoutLocator = Get-AcceptanceVersionResourceCheck -Counts $counts
    Assert-Equal -Expected $true -Actual $withoutLocator.Passed -Message "a page without a route passes when locators are not required"

    $withLocator = Get-AcceptanceVersionResourceCheck -Counts $counts -RequireLocators
    Assert-Equal -Expected $false -Actual $withLocator.Passed -Message "a page without a route template is not a design locator"
    Assert-True -Condition ($withLocator.LocatorDetail -match 'routes=0') -Message "the locator failure reports routes=0"
}

function Test-VersionResourceGateRequiresPageUrl {
    $counts = Get-AcceptanceVersionResourceCounts -Version (New-TestVersion -Pages @(
            (New-TestPage -Id "10" -Name "登录页" -Url "" -Route "/login" -Status "proposed")
        ) -Apis @(
            (New-TestApi -Id "20" -Method "GET" -Path "/api/v1/posts" -Status "proposed")
        ))

    $withoutUrl = Get-AcceptanceVersionResourceCheck -Counts $counts -RequireLocators
    Assert-Equal -Expected $true -Actual $withoutUrl.Passed -Message "a page without a SUT URL passes when execution URLs are not required"

    $withUrl = Get-AcceptanceVersionResourceCheck -Counts $counts -RequireLocators -RequirePageUrls
    Assert-Equal -Expected $false -Actual $withUrl.Passed -Message "a page without a SUT URL cannot feed the execution page URLs"
    Assert-Equal -Expected $false -Actual $withUrl.UrlPassed -Message "the URL gate reports the missing SUT page URL"
    Assert-True -Condition ($withUrl.UrlDetail -match 'urls=0') -Message "the URL failure reports urls=0"
}

function Test-VersionCountsHandleMissingVersion {
    $counts = Get-AcceptanceVersionResourceCounts -Version $null
    Assert-Equal -Expected 0 -Actual $counts.PageCount -Message "a missing version has 0 bound pages"
    Assert-Equal -Expected 0 -Actual $counts.ApiCount -Message "a missing version has 0 bound APIs"
    $check = Get-AcceptanceVersionResourceCheck -Counts $counts -RequireApis
    Assert-Equal -Expected $false -Actual $check.Passed -Message "a missing version fails the resource gate"
}

# ---------------------------------------------------------------------------
# Wait wiring: S1 must wait after the fact check for the catalog proposals.
# The HTTP reader is stubbed so the wait/return shape is covered offline.
# ---------------------------------------------------------------------------

$script:stubResourceMode = "empty"
$script:stubResourceCalls = 0

function Get-AcceptanceEnvironmentResources {
    param($Context, $EnvironmentId)
    $script:stubResourceCalls++
    if ($script:stubResourceMode -eq "resources") {
        $pages = @((New-TestPage -Id "10" -Name "登录页" -Url "http://localhost:5173/login" -Route "/login" -Status "proposed"))
        $apis = @((New-TestApi -Id "20" -Method "GET" -Path "/api/v1/posts" -Status "proposed"))
        return [pscustomobject]@{
            EnvironmentId = $EnvironmentId
            Pages         = $pages
            APIs          = $apis
            PageCount     = 1
            ApiCount      = 1
            Ok            = $true
            Error         = ""
        }
    }
    return [pscustomobject]@{
        EnvironmentId = $EnvironmentId
        Pages         = @()
        APIs          = @()
        PageCount     = 0
        ApiCount      = 0
        Ok            = $true
        Error         = ""
    }
}

function Test-WaitForDiscoveredResourcesReturnsBindableRows {
    $script:stubResourceMode = "resources"
    $script:stubResourceCalls = 0
    $waiter = Wait-AcceptanceEnvironmentResources -Context ([pscustomobject]@{}) -EnvironmentId "2" -RequireApis -TimeoutSeconds 5 -IntervalSeconds 1

    Assert-Equal -Expected $true -Actual $waiter.Ok -Message "the wait succeeds once discovery-proposed pages and APIs are visible"
    Assert-Equal -Expected $false -Actual $waiter.TimedOut -Message "a satisfied wait is not timed out"
    Assert-Equal -Expected 1 -Actual $waiter.Bindable.PageCount -Message "the wait returns the bindable pages"
    Assert-Equal -Expected 1 -Actual $waiter.Bindable.ApiCount -Message "the wait returns the bindable APIs"
    Assert-Equal -Expected "10" -Actual ($waiter.Bindable.PageIds -join ",") -Message "the wait exposes the discovered page IDs used for the working version"
}

function Test-WaitForDiscoveredResourcesTimesOutHonestly {
    $script:stubResourceMode = "empty"
    $script:stubResourceCalls = 0
    $waiter = Wait-AcceptanceEnvironmentResources -Context ([pscustomobject]@{}) -EnvironmentId "2" -RequireApis -TimeoutSeconds 1 -IntervalSeconds 1

    Assert-Equal -Expected $false -Actual $waiter.Ok -Message "an environment with 0 discovered resources must not report a successful wait"
    Assert-Equal -Expected $true -Actual $waiter.TimedOut -Message "the wait reports the timeout instead of hanging"
    Assert-Equal -Expected 0 -Actual $waiter.Bindable.PageCount -Message "the timed-out wait carries the last (empty) page snapshot"
    Assert-True -Condition ($script:stubResourceCalls -ge 1) -Message "the wait polls the resource catalog"
}

# ---------------------------------------------------------------------------
# Script wiring / parse safety (no live stack).
# ---------------------------------------------------------------------------

function Test-AcceptanceScriptsParse {
    $names = @("lib.ps1", "s1-version.ps1", "s2-clarify.ps1", "s3-design.ps1", "lib-s3-s6.ps1")
    foreach ($name in $names) {
        $path = Join-Path $libDir $name
        $tokens = $null
        $errors = $null
        [void][System.Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$errors)
        $message = (@($errors | ForEach-Object { $_.Message }) -join "; ")
        Assert-Equal -Expected 0 -Actual @($errors).Count -Message "$name parses without errors $message"
    }
}

function Test-S1AndS3WireTheResourceGate {
    $s1 = Get-Content -LiteralPath (Join-Path $libDir "s1-version.ps1") -Raw -Encoding UTF8
    $s3 = Get-Content -LiteralPath (Join-Path $libDir "s3-design.ps1") -Raw -Encoding UTF8
    $lib = Get-Content -LiteralPath (Join-Path $libDir "lib.ps1") -Raw -Encoding UTF8

    Assert-True -Condition ($s1 -match 'Wait-AcceptanceEnvironmentResources') -Message "S1 waits for discovery-proposed environment resources after discovery"
    Assert-True -Condition ($s1 -match 'New-AcceptanceBindingWithResources') -Message "S1 creates the working version from the discovered resources"
    Assert-True -Condition ($s1 -match 'version_pages > 0') -Message "S1 has the hard version_pages > 0 assertion"
    Assert-True -Condition ($s1 -match 'version_apis > 0') -Message "S1 has the hard version_apis > 0 assertion"
    Assert-True -Condition ($s1 -match 'SUT page URLs for execution') -Message "S1 asserts the bound pages carry SUT URLs for execution"
    Assert-True -Condition ($s3 -match 'Get-AcceptanceVersionResourceCheck') -Message "S3 gates design on the version resource check"
    Assert-True -Condition ($s3 -match 'version_pages > 0') -Message "S3 has the hard version_pages > 0 assertion"
    Assert-True -Condition ($s3 -match 'version_apis > 0') -Message "S3 has the hard version_apis > 0 assertion"
    Assert-True -Condition ($s3 -match 'SUT page URLs for execution') -Message "S3 asserts the bound pages carry SUT URLs for execution"
    Assert-True -Condition ($lib -match 'function Get-AcceptanceVersionResourceCheck') -Message "lib.ps1 owns the version resource gate helper"
    Assert-True -Condition ($lib -match 'WithoutEnvironmentResources') -Message "lib.ps1 can create the pre-discovery version with no bound resources"
}

Test-BindableResourcesFiltersRejectedAndDeduplicates
Test-BindableResourcesHonorsRequestedStatuses
Test-BindableResourcesEmptyEnvironment
Test-BindingPayloadCarriesResourcesAndAccounts
Test-BindingWithResourcesReplacesOnlyTheScope
Test-VersionCountsExposeBoundResourcesAndLocators
Test-VersionResourceGateFailsOnPreFixEvidenceShape
Test-VersionResourceGateRequiresApisOnlyWhenExpected
Test-VersionResourceGateRequiresRouteLocator
Test-VersionResourceGateRequiresPageUrl
Test-VersionCountsHandleMissingVersion
Test-WaitForDiscoveredResourcesReturnsBindableRows
Test-WaitForDiscoveredResourcesTimesOutHonestly
Test-AcceptanceScriptsParse
Test-S1AndS3WireTheResourceGate

Write-Host ""
Write-Host ("version resource binding tests: {0} checks, {1} failure(s)" -f $script:checks, $script:failures)
if ($script:failures -gt 0) { exit 1 }
exit 0
