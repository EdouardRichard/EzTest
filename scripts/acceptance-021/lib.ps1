#requires -Version 5.1
<#
.SYNOPSIS
    Shared library for the EzTest 0.2.1 unattended acceptance scripts (S0-S10).

.DESCRIPTION
    Everything an acceptance scenario needs is here: configuration discovery,
    bearer login, JSON API calls, SSE waiting, UI screenshots, assertions and
    evidence capture. The scenarios themselves only express the acceptance
    criteria; this file owns the mechanics and the evidence layout.

    Design rules:
      * Unattended: no prompts, deterministic exit codes.
      * Honest: a missing feature or an unreachable service is a failure, never
        a silently skipped check.
      * Credential-safe: bearer tokens / passwords are never written to
        evidence. API payloads are redacted by property name before capture.
      * Reusable: S3-S10 import this same file.

    Exit codes used by every scenario:
      0 = passed
      1 = failed acceptance assertion (the product did not meet the criterion)
      2 = blocked (environment prerequisite unavailable; see evidence/result.json)

.NOTES
    Repository root is inferred from this file's location
    (<repo>/scripts/acceptance-021/lib.ps1). The evidence root defaults to
    <repo>/docs/version-V0.2.1/acceptance/evidence/s0-s2.
#>

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

try { Add-Type -AssemblyName System.Net.Http -ErrorAction Stop } catch { }

$script:AcceptanceSchemaVersion = "1"
$script:RedactionSecrets = @()
$script:ScreenshotNodeScript = $null

# ---------------------------------------------------------------------------
# Small generic helpers
# ---------------------------------------------------------------------------

function Get-JsonProp {
    param(
        [Parameter(Mandatory = $false)] $Object,
        [Parameter(Mandatory = $true)] [string] $Name,
        [Parameter(Mandatory = $false)] $Default = $null
    )
    if ($null -eq $Object) { return $Default }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $Default }
    return $property.Value
}

function Format-AcceptanceRedacted {
    param([Parameter(Mandatory = $false)] [string] $Text)
    if ([string]::IsNullOrEmpty($Text)) { return $Text }
    $result = $Text
    foreach ($secret in $script:RedactionSecrets) {
        if (-not [string]::IsNullOrEmpty($secret) -and $secret.Length -ge 4) {
            $result = $result.Replace($secret, "<redacted>")
        }
    }
    return $result
}

function ConvertTo-RedactedObject {
    param([Parameter(Mandatory = $false)] $Value, [int] $Depth = 0)
    if ($Depth -gt 30) { return "<truncated>" }
    if ($null -eq $Value) { return $null }
    if ($Value -is [string]) { return (Format-AcceptanceRedacted -Text $Value) }
    if ($Value -is [bool] -or $Value -is [int] -or $Value -is [long] -or $Value -is [double] -or $Value -is [decimal]) { return $Value }
    if ($Value -is [System.Collections.IDictionary]) {
        $copy = [ordered]@{}
        foreach ($key in $Value.Keys) {
            $keyText = [string]$key
            if ($keyText -match '(?i)password|passwd|token|api[_-]?key|secret|authorization|credential') {
                $copy[$keyText] = "<redacted>"
            } else {
                $copy[$keyText] = ConvertTo-RedactedObject -Value $Value[$key] -Depth ($Depth + 1)
            }
        }
        return [pscustomobject]$copy
    }
    if ($Value -is [System.Collections.IEnumerable]) {
        $items = @()
        foreach ($item in $Value) {
            $items += , (ConvertTo-RedactedObject -Value $item -Depth ($Depth + 1))
        }
        return , $items
    }
    if ($Value -is [pscustomobject]) {
        $copy = [ordered]@{}
        foreach ($property in $Value.PSObject.Properties) {
            if ($property.Name -match '(?i)password|passwd|token|api[_-]?key|secret|authorization|credential') {
                $copy[$property.Name] = "<redacted>"
            } else {
                $copy[$property.Name] = ConvertTo-RedactedObject -Value $property.Value -Depth ($Depth + 1)
            }
        }
        return [pscustomobject]$copy
    }
    return $Value
}

function Write-EvidenceJson {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $false)] $Value,
        [switch] $NoRedaction
    )
    $directory = Split-Path -Parent $Path
    if (-not [string]::IsNullOrEmpty($directory)) {
        New-Item -ItemType Directory -Force -Path $directory | Out-Null
    }
    $payload = if ($NoRedaction) { $Value } else { ConvertTo-RedactedObject -Value $Value }
    if ($null -eq $payload) {
        $json = "null"
    } elseif ($payload -is [System.Array] -and @($payload).Count -eq 0) {
        $json = "[]"
    } elseif ($payload -is [System.Collections.IDictionary] -and $payload.Count -eq 0) {
        $json = "{}"
    } else {
        $json = $payload | ConvertTo-Json -Depth 40
    }
    [System.IO.File]::WriteAllText($Path, $json, [System.Text.UTF8Encoding]::new($false))
}

function Write-EvidenceText {
    param(
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $false)] [AllowEmptyString()] [string] $Text
    )
    $directory = Split-Path -Parent $Path
    if (-not [string]::IsNullOrEmpty($directory)) {
        New-Item -ItemType Directory -Force -Path $directory | Out-Null
    }
    [System.IO.File]::WriteAllText($Path, (Format-AcceptanceRedacted -Text $Text), [System.Text.UTF8Encoding]::new($false))
}

function Resolve-RepoRelativePath {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Path
    )
    if ([System.IO.Path]::IsPathRooted($Path)) {
        $full = [System.IO.Path]::GetFullPath($Path)
        if ($full.StartsWith($Context.RepoRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
            return $full.Substring($Context.RepoRoot.Length).TrimStart('\', '/')
        }
        return $full
    }
    return $Path
}

# ---------------------------------------------------------------------------
# Configuration / context
# ---------------------------------------------------------------------------

function Read-AcceptanceDotEnv {
    param([Parameter(Mandatory = $true)] [string] $Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path)) { return $values }
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith("#")) { continue }
        $index = $trimmed.IndexOf("=")
        if ($index -lt 1) { continue }
        $key = $trimmed.Substring(0, $index).Trim()
        $value = $trimmed.Substring($index + 1).Trim()
        if ($value.Length -ge 2) {
            $first = $value.Substring(0, 1)
            $last = $value.Substring($value.Length - 1, 1)
            if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                $value = $value.Substring(1, $value.Length - 2)
            }
        }
        $values[$key] = $value
    }
    return $values
}

function Get-AcceptanceSetting {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Name,
        [Parameter(Mandatory = $false)] [string] $Default = ""
    )
    $processValue = [Environment]::GetEnvironmentVariable($Name, "Process")
    if (-not [string]::IsNullOrWhiteSpace($processValue)) { return $processValue }
    if ($Context.DotEnv.ContainsKey($Name) -and -not [string]::IsNullOrWhiteSpace([string]$Context.DotEnv[$Name])) {
        return [string]$Context.DotEnv[$Name]
    }
    return $Default
}

function Get-DatabaseName {
    param([Parameter(Mandatory = $false)] [string] $DatabaseUrl)
    if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) { return "" }
    try {
        $uri = [System.Uri]::new($DatabaseUrl)
        return $uri.AbsolutePath.TrimStart('/')
    } catch {
        if ($DatabaseUrl -match '/([^/?]+)(\?|$)') { return $Matches[1] }
        return ""
    }
}

function Get-RepoHead {
    param([Parameter(Mandatory = $true)] $Context)
    try {
        $head = & git -C $Context.RepoRoot rev-parse --short HEAD 2>$null
        if ($LASTEXITCODE -eq 0 -and $head) { return ([string]$head).Trim() }
    } catch { }
    return "unknown"
}

function New-AcceptanceContext {
    param(
        [Parameter(Mandatory = $false)] [string] $RepoRoot = "",
        [Parameter(Mandatory = $false)] [string] $EvidenceRoot = "",
        [Parameter(Mandatory = $false)] [string] $ApiBaseUrl = "",
        [Parameter(Mandatory = $false)] [string] $WebBaseUrl = "",
        [Parameter(Mandatory = $false)] [string] $ExecutorUrl = "",
        [Parameter(Mandatory = $false)] [string] $SutWebBaseUrl = "",
        [Parameter(Mandatory = $false)] [string] $SutApiBaseUrl = "",
        [Parameter(Mandatory = $false)] [string] $AdminUsername = "",
        [Parameter(Mandatory = $false)] [string] $AdminPassword = "",
        [Parameter(Mandatory = $false)] [string] $SutPassword = ""
    )
    if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
        $RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
    }
    $RepoRoot = [System.IO.Path]::GetFullPath($RepoRoot)
    $dotEnv = Read-AcceptanceDotEnv -Path (Join-Path $RepoRoot ".env")

    $apiPort = if ($dotEnv.ContainsKey("EZTEST_API_PORT")) { [string]$dotEnv["EZTEST_API_PORT"] } else { "13333" }
    $webPort = if ($dotEnv.ContainsKey("EZTEST_WEB_PORT")) { [string]$dotEnv["EZTEST_WEB_PORT"] } else { "6868" }
    $executorPort = if ($dotEnv.ContainsKey("EZTEST_EXECUTOR_PORT")) { [string]$dotEnv["EZTEST_EXECUTOR_PORT"] } else { "13336" }

    if ([string]::IsNullOrWhiteSpace($ApiBaseUrl)) { $ApiBaseUrl = "http://localhost:$apiPort" }
    if ([string]::IsNullOrWhiteSpace($WebBaseUrl)) { $WebBaseUrl = "http://localhost:$webPort" }
    if ([string]::IsNullOrWhiteSpace($ExecutorUrl)) { $ExecutorUrl = "http://127.0.0.1:$executorPort" }
    if ([string]::IsNullOrWhiteSpace($SutWebBaseUrl)) { $SutWebBaseUrl = "http://localhost:5173" }
    if ([string]::IsNullOrWhiteSpace($SutApiBaseUrl)) { $SutApiBaseUrl = "http://localhost:8080" }

    if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
        $EvidenceRoot = Join-Path $RepoRoot "docs/version-V0.2.1/acceptance/evidence/s0-s2"
    } elseif (-not [System.IO.Path]::IsPathRooted($EvidenceRoot)) {
        $EvidenceRoot = Join-Path $RepoRoot $EvidenceRoot
    }

    if ([string]::IsNullOrWhiteSpace($AdminUsername)) {
        $AdminUsername = Get-AcceptanceSetting -Context ([pscustomobject]@{ DotEnv = $dotEnv }) -Name "EZTEST_ADMIN_USERNAME" -Default "admin"
    }
    $passwordCandidates = @()
    if (-not [string]::IsNullOrWhiteSpace($AdminPassword)) {
        $passwordCandidates += $AdminPassword
    } else {
        $fromEnv = Get-AcceptanceSetting -Context ([pscustomobject]@{ DotEnv = $dotEnv }) -Name "EZTEST_ADMIN_PASSWORD" -Default ""
        if (-not [string]::IsNullOrWhiteSpace($fromEnv)) { $passwordCandidates += $fromEnv }
        $passwordCandidates += "admin123"
        $passwordCandidates += "[REDACTED]"
    }
    if ([string]::IsNullOrWhiteSpace($SutPassword)) {
        $SutPassword = Get-AcceptanceSetting -Context ([pscustomobject]@{ DotEnv = $dotEnv }) -Name "SUT_PASSWORD" -Default "[REDACTED]"
    }

    $context = @{
        RepoRoot         = $RepoRoot
        EvidenceRoot     = [System.IO.Path]::GetFullPath($EvidenceRoot)
        ApiBaseUrl       = $ApiBaseUrl.TrimEnd('/')
        WebBaseUrl       = $WebBaseUrl.TrimEnd('/')
        ExecutorUrl      = $ExecutorUrl.TrimEnd('/')
        SutWebBaseUrl    = $SutWebBaseUrl.TrimEnd('/')
        SutApiBaseUrl    = $SutApiBaseUrl.TrimEnd('/')
        AdminUsername    = $AdminUsername
        PasswordCandidates = $passwordCandidates
        SutPassword      = $SutPassword
        DotEnv           = $dotEnv
        DatabaseName     = Get-DatabaseName -DatabaseUrl (Get-AcceptanceSetting -Context ([pscustomobject]@{ DotEnv = $dotEnv }) -Name "EZTEST_DATABASE_URL" -Default "")
        RepoHead         = Get-RepoHead -Context ([pscustomobject]@{ RepoRoot = $RepoRoot })
        StartedAt        = (Get-Date).ToUniversalTime().ToString("o")
        Token            = ""
        User             = $null
        HttpClient       = [System.Net.Http.HttpClient]::new()
        ScreenshotScript = $null
    }
    $context.HttpClient.Timeout = [TimeSpan]::FromSeconds(120)
    New-Item -ItemType Directory -Force -Path $context.EvidenceRoot | Out-Null

    $script:RedactionSecrets = @()
    foreach ($candidate in $passwordCandidates) { if ($candidate) { $script:RedactionSecrets += $candidate } }
    if ($SutPassword) { $script:RedactionSecrets += $SutPassword }
    foreach ($keyName in @("llm_api_key", "llm_key")) {
        if ($dotEnv.ContainsKey($keyName) -and $dotEnv[$keyName]) { $script:RedactionSecrets += [string]$dotEnv[$keyName] }
    }

    return $context
}

function Initialize-AcceptanceContext {
    param(
        [Parameter(Mandatory = $false)] [string] $RepoRoot = "",
        [Parameter(Mandatory = $false)] [string] $EvidenceRoot = "",
        [Parameter(Mandatory = $false)] [string] $ApiBaseUrl = "",
        [Parameter(Mandatory = $false)] [string] $WebBaseUrl = "",
        [Parameter(Mandatory = $false)] [string] $ExecutorUrl = "",
        [Parameter(Mandatory = $false)] [string] $SutWebBaseUrl = "",
        [Parameter(Mandatory = $false)] [string] $SutApiBaseUrl = "",
        [Parameter(Mandatory = $false)] [string] $AdminUsername = "",
        [Parameter(Mandatory = $false)] [string] $AdminPassword = "",
        [Parameter(Mandatory = $false)] [string] $SutPassword = ""
    )
    return New-AcceptanceContext -RepoRoot $RepoRoot -EvidenceRoot $EvidenceRoot `
        -ApiBaseUrl $ApiBaseUrl -WebBaseUrl $WebBaseUrl -ExecutorUrl $ExecutorUrl `
        -SutWebBaseUrl $SutWebBaseUrl -SutApiBaseUrl $SutApiBaseUrl `
        -AdminUsername $AdminUsername -AdminPassword $AdminPassword -SutPassword $SutPassword
}

# ---------------------------------------------------------------------------
# Evidence / scenario
# ---------------------------------------------------------------------------

function Write-AcceptanceLog {
    param(
        [Parameter(Mandatory = $true)] $Scenario,
        [Parameter(Mandatory = $true)] [string] $Message,
        [ValidateSet("info", "warn", "error")] [string] $Level = "info"
    )
    $line = "[{0}] [{1}] {2}" -f (Get-Date).ToString("yyyy-MM-ddTHH:mm:ss"), $Level.ToUpperInvariant(), (Format-AcceptanceRedacted -Text $Message)
    if ($Scenario.LogPath) {
        if (-not (Test-Path -LiteralPath (Split-Path -Parent $Scenario.LogPath))) {
            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Scenario.LogPath) | Out-Null
        }
        [System.IO.File]::AppendAllText($Scenario.LogPath, $line + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
    }
    switch ($Level) {
        "warn" { Write-Host $line -ForegroundColor Yellow }
        "error" { Write-Host $line -ForegroundColor Red }
        default { Write-Host $line }
    }
}

function New-AcceptanceScenario {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Name,
        [Parameter(Mandatory = $false)] [string] $Title = ""
    )
    $directory = Join-Path $Context.EvidenceRoot $Name
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    $scenario = @{
        Name          = $Name
        Title         = if ($Title) { $Title } else { $Name }
        Context       = $Context
        Directory     = $directory
        LogPath       = Join-Path $directory "run.log"
        ResultPath    = Join-Path $directory "result.json"
        Checks        = [System.Collections.Generic.List[object]]::new()
        Artifacts     = [System.Collections.Generic.List[object]]::new()
        StartedAt     = (Get-Date).ToUniversalTime().ToString("o")
        ErrorMessage  = ""
        ExitCode      = 1
    }
    [System.IO.File]::WriteAllText($scenario.LogPath, "", [System.Text.UTF8Encoding]::new($false))
    Write-AcceptanceLog -Scenario $scenario -Message "scenario $Name ($($scenario.Title)) started"
    return $scenario
}

function Add-AcceptanceCheck {
    param(
        [Parameter(Mandatory = $true)] $Scenario,
        [Parameter(Mandatory = $true)] [string] $Name,
        [Parameter(Mandatory = $true)] [bool] $Passed,
        [Parameter(Mandatory = $false)] [string] $Detail = "",
        [ValidateSet("assertion", "blocker", "warning")] [string] $Severity = "assertion"
    )
    $status = if ($Passed) { "pass" } elseif ($Severity -eq "warning") { "warn" } else { "fail" }
    $check = [pscustomobject]@{
        name     = $Name
        status   = $status
        severity = $Severity
        detail   = Format-AcceptanceRedacted -Text $Detail
    }
    [void]$Scenario.Checks.Add($check)
    $level = if ($status -eq "pass") { "info" } elseif ($status -eq "warn") { "warn" } else { "error" }
    $suffix = if ($Detail) { " - $Detail" } else { "" }
    Write-AcceptanceLog -Scenario $Scenario -Level $level -Message ("[{0}] {1}{2}" -f $status.ToUpperInvariant(), $Name, $suffix)
    return
}

function Add-AcceptanceArtifact {
    param(
        [Parameter(Mandatory = $true)] $Scenario,
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $false)] [string] $Kind = "file",
        [Parameter(Mandatory = $false)] [string] $Note = ""
    )
    if ([string]::IsNullOrWhiteSpace($Path)) { return }
    $relative = Resolve-RepoRelativePath -Context $Scenario.Context -Path $Path
    [void]$Scenario.Artifacts.Add([pscustomobject]@{
        kind = $Kind
        path = $relative
        note = Format-AcceptanceRedacted -Text $Note
    })
}

function Complete-AcceptanceScenario {
    param(
        [Parameter(Mandatory = $true)] $Scenario,
        [Parameter(Mandatory = $false)] [ValidateSet("", "passed", "failed", "blocked")] [string] $Status = "",
        [Parameter(Mandatory = $false)] [string] $Message = ""
    )
    $failedBlockers = @($Scenario.Checks | Where-Object { $_.status -eq "fail" -and $_.severity -eq "blocker" })
    $failedAssertions = @($Scenario.Checks | Where-Object { $_.status -eq "fail" })
    if ([string]::IsNullOrWhiteSpace($Status)) {
        if ($failedBlockers.Count -gt 0) { $Status = "blocked" }
        elseif ($failedAssertions.Count -gt 0) { $Status = "failed" }
        else { $Status = "passed" }
    }
    $exitCode = switch ($Status) { "passed" { 0 } "failed" { 1 } default { 2 } }
    $Scenario.ExitCode = $exitCode
    $context = $Scenario.Context
    Add-AcceptanceArtifact -Scenario $Scenario -Path $Scenario.ResultPath -Kind "result" -Note "scenario result"
    $result = [pscustomobject]@{
        schema_version = $script:AcceptanceSchemaVersion
        scenario       = $Scenario.Name
        title          = $Scenario.Title
        status         = $Status
        exit_code      = $exitCode
        started_at     = $Scenario.StartedAt
        ended_at       = (Get-Date).ToUniversalTime().ToString("o")
        message        = Format-AcceptanceRedacted -Text $Message
        error          = Format-AcceptanceRedacted -Text $Scenario.ErrorMessage
        environment    = [pscustomobject]@{
            api_base_url      = $context.ApiBaseUrl
            web_base_url      = $context.WebBaseUrl
            executor_url      = $context.ExecutorUrl
            sut_web_base_url  = $context.SutWebBaseUrl
            sut_api_base_url  = $context.SutApiBaseUrl
            database_name     = $context.DatabaseName
            repo_head         = $context.RepoHead
        }
        checks         = @($Scenario.Checks)
        artifacts      = @($Scenario.Artifacts)
    }
    Write-EvidenceJson -Path $Scenario.ResultPath -Value $result
    $summary = "[{0}] {1}: {2} (exit {3})" -f $Status.ToUpperInvariant(), $Scenario.Name, $Message, $exitCode
    if ($Status -eq "passed") { Write-Host $summary -ForegroundColor Green }
    elseif ($Status -eq "blocked") { Write-Host $summary -ForegroundColor Yellow }
    else { Write-Host $summary -ForegroundColor Red }
    return $exitCode
}

# ---------------------------------------------------------------------------
# HTTP / auth
# ---------------------------------------------------------------------------

function Invoke-Api {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Method,
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $false)] $Body = $null,
        [Parameter(Mandatory = $false)] [string] $Token = "",
        [Parameter(Mandatory = $false)] [string] $BaseUrl = "",
        [Parameter(Mandatory = $false)] [int] $TimeoutSeconds = 60,
        [Parameter(Mandatory = $false)] [string] $Accept = "application/json"
    )
    if ($Path -match '^https?://') {
        $url = $Path
    } elseif (-not [string]::IsNullOrWhiteSpace($BaseUrl)) {
        $url = $BaseUrl.TrimEnd('/') + $Path
    } else {
        $url = $Context.ApiBaseUrl + $Path
    }
    $request = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::new($Method.ToUpperInvariant()), $url)
    [void]$request.Headers.TryAddWithoutValidation("Accept", $Accept)
    if (-not [string]::IsNullOrWhiteSpace($Token)) {
        [void]$request.Headers.TryAddWithoutValidation("Authorization", "Bearer $Token")
    }
    if ($null -ne $Body) {
        $json = if ($Body -is [string]) { $Body } else { $Body | ConvertTo-Json -Depth 30 -Compress }
        $request.Content = [System.Net.Http.StringContent]::new($json, [System.Text.Encoding]::UTF8, "application/json")
    }
    $cts = [System.Threading.CancellationTokenSource]::new([TimeSpan]::FromSeconds([Math]::Max(1, $TimeoutSeconds)))
    $response = $null
    try {
        $response = $Context.HttpClient.SendAsync($request, [System.Net.Http.HttpCompletionOption]::ResponseContentRead, $cts.Token).GetAwaiter().GetResult()
        $text = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
    } catch [System.OperationCanceledException] {
        throw "request timed out after ${TimeoutSeconds}s: $Method $url"
    } finally {
        if ($null -ne $response) { $response.Dispose() }
        $cts.Dispose()
    }
    $status = [int]$response.StatusCode
    $parsed = $null
    if (-not [string]::IsNullOrWhiteSpace($text)) {
        try { $parsed = $text | ConvertFrom-Json -ErrorAction Stop } catch { $parsed = $null }
    }
    $headers = @{}
    foreach ($header in $response.Headers) { $headers[$header.Key] = ($header.Value -join ", ") }
    $data = Get-JsonProp -Object $parsed -Name "data" -Default $null
    $errorInfo = Get-JsonProp -Object $parsed -Name "error" -Default $null
    return [pscustomobject]@{
        Method   = $Method.ToUpperInvariant()
        Url      = $url
        Status   = $status
        Ok       = ($status -ge 200 -and $status -lt 300)
        Data     = $data
        Json     = $parsed
        Error    = $errorInfo
        Text     = $text
        Headers  = $headers
    }
}

function Invoke-ApiOrFail {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Method,
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $false)] $Body = $null,
        [Parameter(Mandatory = $false)] [string] $Token = "",
        [Parameter(Mandatory = $false)] [int[]] $ExpectedStatus = @(200),
        [Parameter(Mandatory = $false)] [string] $Label = "",
        [Parameter(Mandatory = $false)] [int] $TimeoutSeconds = 60
    )
    $response = Invoke-Api -Context $Context -Method $Method -Path $Path -Body $Body -Token $Token -TimeoutSeconds $TimeoutSeconds
    if ($ExpectedStatus -notcontains $response.Status) {
        $code = Get-JsonProp -Object $response.Error -Name "code" -Default ""
        $message = Get-JsonProp -Object $response.Error -Name "message" -Default ($response.Text)
        if ($message.Length -gt 400) { $message = $message.Substring(0, 400) }
        $labelText = if ($Label) { $Label } else { "$Method $Path" }
        throw "$labelText failed: HTTP $($response.Status) code=$code message=$message"
    }
    return $response
}

function New-AcceptanceContentUpload {
    <#
        Uploads one file through POST /api/content (multipart field "file") and
        returns the public ContentObject (sha256/size/mime/url). The bearer token
        never enters the response or evidence; the caller records only the object
        metadata.
    #>
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Path,
        [Parameter(Mandatory = $false)] [string] $Mime = "",
        [Parameter(Mandatory = $false)] [int] $TimeoutSeconds = 120
    )
    if ([string]::IsNullOrWhiteSpace($Context.Token)) { throw "content upload requires a logged-in context" }
    $file = Get-Item -LiteralPath $Path -ErrorAction Stop
    if ($file.PSIsContainer) { throw "content upload path is a directory: $Path" }
    if ([string]::IsNullOrWhiteSpace($Mime)) {
        $Mime = switch ($file.Extension.ToLowerInvariant()) {
            ".md" { "text/markdown" }
            ".markdown" { "text/markdown" }
            ".txt" { "text/plain" }
            ".html" { "text/html" }
            ".htm" { "text/html" }
            default { "application/octet-stream" }
        }
    }
    $stream = [System.IO.File]::OpenRead($file.FullName)
    $fileContent = [System.Net.Http.StreamContent]::new($stream)
    $multipart = [System.Net.Http.MultipartFormDataContent]::new()
    $request = $null
    $response = $null
    $cts = [System.Threading.CancellationTokenSource]::new([TimeSpan]::FromSeconds([Math]::Max(1, $TimeoutSeconds)))
    try {
        $fileContent.Headers.ContentType = [System.Net.Http.Headers.MediaTypeHeaderValue]::Parse($Mime)
        $multipart.Add($fileContent, "file", $file.Name)
        $request = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::Post, $Context.ApiBaseUrl + "/api/content")
        [void]$request.Headers.TryAddWithoutValidation("Accept", "application/json")
        [void]$request.Headers.TryAddWithoutValidation("Authorization", "Bearer $($Context.Token)")
        $request.Content = $multipart
        $response = $Context.HttpClient.SendAsync($request, [System.Net.Http.HttpCompletionOption]::ResponseContentRead, $cts.Token).GetAwaiter().GetResult()
        $text = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
    } catch [System.OperationCanceledException] {
        throw "content upload timed out after ${TimeoutSeconds}s: $Path"
    } finally {
        if ($null -ne $response) { $response.Dispose() }
        if ($null -ne $request) { $request.Dispose() }
        $multipart.Dispose()
        $cts.Dispose()
    }
    $parsed = $null
    if (-not [string]::IsNullOrWhiteSpace($text)) {
        try { $parsed = $text | ConvertFrom-Json -ErrorAction Stop } catch { $parsed = $null }
    }
    $status = [int]$response.StatusCode
    $data = Get-JsonProp -Object $parsed -Name "data" -Default $null
    $errorInfo = Get-JsonProp -Object $parsed -Name "error" -Default $null
    return [pscustomobject]@{
        Status = $status
        Ok     = ($status -ge 200 -and $status -lt 300)
        Data   = $data
        Error  = $errorInfo
        Text   = $text
        Path   = $file.FullName
        Name   = $file.Name
        Size   = $file.Length
        Mime   = $Mime
    }
}

function Get-AcceptanceFileSha256 {
    param([Parameter(Mandatory = $true)] [string] $Path)
    $stream = [System.IO.File]::OpenRead($Path)
    try {
        $sha = [System.Security.Cryptography.SHA256]::Create()
        try {
            $hash = $sha.ComputeHash($stream)
            return ([System.BitConverter]::ToString($hash) -replace '-', '').ToLowerInvariant()
        } finally {
            $sha.Dispose()
        }
    } finally {
        $stream.Dispose()
    }
}

function Connect-EzTest {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $false)] [string] $Username = "",
        [Parameter(Mandatory = $false)] [string] $Password = ""
    )
    if ([string]::IsNullOrWhiteSpace($Username)) { $Username = $Context.AdminUsername }
    $candidates = if (-not [string]::IsNullOrWhiteSpace($Password)) { @($Password) } else { @($Context.PasswordCandidates) }
    $lastError = ""
    foreach ($candidate in $candidates) {
        try {
            $response = Invoke-Api -Context $Context -Method "POST" -Path "/api/auth/login" `
                -Body @{ username = $Username; password = $candidate } -TimeoutSeconds 20
        } catch {
            $lastError = "login request failed: $($_.Exception.Message)"
            continue
        }
        if ($response.Status -eq 200) {
            $token = [string](Get-JsonProp -Object $response.Data -Name "token" -Default "")
            if ([string]::IsNullOrWhiteSpace($token)) {
                $lastError = "login succeeded but returned no token"
                continue
            }
            $Context.Token = $token
            $Context.User = Get-JsonProp -Object $response.Data -Name "user" -Default $null
            $script:RedactionSecrets += $token
            return [pscustomobject]@{ Ok = $true; Token = $token; User = $Context.User; Status = $response.Status }
        }
        $code = Get-JsonProp -Object $response.Error -Name "code" -Default ""
        $lastError = "HTTP $($response.Status) $code"
    }
    return [pscustomobject]@{ Ok = $false; Token = ""; User = $null; Status = 0; Error = $lastError }
}

function Connect-SutAccount {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Account,
        [Parameter(Mandatory = $false)] [string] $Password = ""
    )
    if ([string]::IsNullOrWhiteSpace($Password)) { $Password = $Context.SutPassword }
    try {
        $response = Invoke-Api -Context $Context -Method "POST" -Path "/api/v1/auth/login" `
            -BaseUrl $Context.SutApiBaseUrl -Body @{ account = $Account; password = $Password } -TimeoutSeconds 20
    } catch {
        return [pscustomobject]@{ Ok = $false; Account = $Account; Status = 0; Token = ""; User = $null; Error = $_.Exception.Message }
    }
    if ($response.Status -ne 200) {
        $sutError = Get-JsonProp -Object $response.Json -Name "message" -Default ""
        return [pscustomobject]@{ Ok = $false; Account = $Account; Status = $response.Status; Token = ""; User = $null; Error = $sutError }
    }
    # The SUT wraps its payload in {code,message,data}; tolerate both shapes.
    $payload = if ($null -ne $response.Data) { $response.Data } else { $response.Json }
    $token = [string](Get-JsonProp -Object $payload -Name "token" -Default "")
    if ([string]::IsNullOrWhiteSpace($token)) {
        $access = Get-JsonProp -Object $payload -Name "accessToken" -Default ""
        if ($access) { $token = [string]$access }
    }
    $user = Get-JsonProp -Object $payload -Name "user" -Default $null
    if ($token) { $script:RedactionSecrets += $token }
    return [pscustomobject]@{ Ok = $true; Account = $Account; Status = $response.Status; Token = $token; User = $user; Error = "" }
}

# ---------------------------------------------------------------------------
# Waiting / SSE
# ---------------------------------------------------------------------------

function Wait-Until {
    param(
        [Parameter(Mandatory = $true)] [scriptblock] $Condition,
        [Parameter(Mandatory = $false)] [int] $TimeoutSeconds = 60,
        [Parameter(Mandatory = $false)] [int] $IntervalSeconds = 2,
        [Parameter(Mandatory = $false)] [string] $Description = "condition"
    )
    $deadline = (Get-Date).AddSeconds([Math]::Max(1, $TimeoutSeconds))
    $last = $null
    while ((Get-Date) -lt $deadline) {
        $last = & $Condition
        if ($last) {
            return [pscustomobject]@{ Ok = $true; Value = $last; TimedOut = $false }
        }
        Start-Sleep -Seconds ([Math]::Max(1, $IntervalSeconds))
    }
    return [pscustomobject]@{ Ok = $false; Value = $last; TimedOut = $true; Description = $Description }
}

function Wait-SseEvent {
    <#
        Opens the EzTest SSE stream and returns the first matching event (or all
        events received before the timeout). The event cursor is preserved so a
        caller can continue from the last `id`.
    #>
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Channel,
        [Parameter(Mandatory = $false)] [long] $After = 0,
        [Parameter(Mandatory = $false)] [scriptblock] $Predicate = $null,
        [Parameter(Mandatory = $false)] [int] $TimeoutSeconds = 60,
        [Parameter(Mandatory = $false)] [int] $MaxEvents = 200,
        [Parameter(Mandatory = $false)] [scriptblock] $StopWhen = $null,
        [Parameter(Mandatory = $false)] [switch] $FirstOnly
    )
    $encoded = [System.Uri]::EscapeDataString($Channel)
    $url = "$($Context.ApiBaseUrl)/api/stream?channel=$encoded&after=$After"
    $request = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::Get, $url)
    [void]$request.Headers.TryAddWithoutValidation("Accept", "text/event-stream")
    [void]$request.Headers.TryAddWithoutValidation("Authorization", "Bearer $($Context.Token)")
    [void]$request.Headers.TryAddWithoutValidation("Last-Event-ID", [string]$After)
    $cts = [System.Threading.CancellationTokenSource]::new([TimeSpan]::FromSeconds([Math]::Max(1, $TimeoutSeconds)))
    $response = $null
    $reader = $null
    $events = [System.Collections.Generic.List[object]]::new()
    $deadline = [DateTime]::UtcNow.AddSeconds([Math]::Max(1, $TimeoutSeconds))
    try {
        $response = $Context.HttpClient.SendAsync($request, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead, $cts.Token).GetAwaiter().GetResult()
        if (-not $response.IsSuccessStatusCode) {
            throw "SSE stream returned HTTP $([int]$response.StatusCode)"
        }
        $stream = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
        $reader = [System.IO.StreamReader]::new($stream, [System.Text.Encoding]::UTF8)
        $currentEvent = ""
        $currentId = ""
        $dataLines = [System.Collections.Generic.List[string]]::new()
        $readTask = $reader.ReadLineAsync()
        while ($events.Count -lt $MaxEvents) {
            $hasLine = $false
            try {
                $hasLine = $readTask.Wait(200)
            } catch {
                break
            }
            if (-not $hasLine) {
                if ([DateTime]::UtcNow -ge $deadline) { break }
                continue
            }
            $line = $readTask.Result
            if ($null -eq $line) { break }
            if ($line.Length -eq 0) {
                if ($dataLines.Count -gt 0) {
                    $raw = [string]::Join("`n", $dataLines)
                    $data = $null
                    try { $data = $raw | ConvertFrom-Json -ErrorAction Stop } catch { $data = $null }
                    $eventObject = [pscustomobject]@{ Id = $currentId; Event = $currentEvent; Data = $data; Raw = $raw }
                    $matches = $true
                    if ($null -ne $Predicate) { $matches = [bool](& $Predicate $eventObject) }
                    if ($matches) {
                        [void]$events.Add($eventObject)
                        if ($FirstOnly) { break }
                        if ($null -ne $StopWhen -and [bool](& $StopWhen $eventObject)) { break }
                    }
                }
                $currentEvent = ""
                $currentId = ""
                $dataLines.Clear()
                $readTask = $reader.ReadLineAsync()
                continue
            }
            if ($line.StartsWith(":")) {
                $readTask = $reader.ReadLineAsync()
                continue
            }
            if ($line.StartsWith("event:")) { $currentEvent = $line.Substring(6).Trim() }
            elseif ($line.StartsWith("id:")) { $currentId = $line.Substring(3).Trim() }
            elseif ($line.StartsWith("data:")) { [void]$dataLines.Add($line.Substring(5).TrimStart()) }
            $readTask = $reader.ReadLineAsync()
        }
    } finally {
        try { $cts.Cancel() } catch { }
        if ($null -ne $reader) { $reader.Dispose() }
        if ($null -ne $response) { $response.Dispose() }
        $cts.Dispose()
    }
    $lastId = $After
    if ($events.Count -gt 0) {
        try { $lastId = [long]$events[$events.Count - 1].Id } catch { }
    }
    return [pscustomobject]@{ Events = @($events); LastEventId = $lastId; Count = $events.Count; TimedOut = ($events.Count -eq 0) }
}

function Get-EventCursor {
    <#
        Returns the current tail sequence of one event channel. The SSE handler
        treats after=0 as "live from the tail", so scenarios snapshot the
        cursor before acting and then wait for events after it.
    #>
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Channel
    )
    $encoded = [System.Uri]::EscapeDataString($Channel)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" `
        -Path "/api/events?channel=$encoded&after=0&limit=1" -Token $Context.Token -Label "event snapshot"
    $cursor = Get-JsonProp -Object $response.Json -Name "snapshot_cursor" -Default 0
    return [long]$cursor
}

# ---------------------------------------------------------------------------
# Domain helpers
# ---------------------------------------------------------------------------

function Get-Version {
    param($Context, [string] $VersionId)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId" -Token $Context.Token -Label "GET version"
    return $response.Data
}

function Get-VersionStage {
    param($Context, [string] $VersionId)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/stage" -Token $Context.Token -Label "GET stage"
    return $response.Data
}

function Get-VersionFacts {
    param($Context, [string] $VersionId, [string] $Kind = "", [int] $Limit = 200)
    $path = "/api/versions/$VersionId/facts?limit=$Limit"
    if (-not [string]::IsNullOrWhiteSpace($Kind)) { $path += "&kind=" + [System.Uri]::EscapeDataString($Kind) }
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path $path -Token $Context.Token -Label "GET version facts"
    $items = @($response.Data)
    return $items
}

function Get-VersionRequirements {
    param($Context, [string] $VersionId, [string] $Status = "", [int] $Limit = 200)
    $path = "/api/versions/$VersionId/requirements?limit=$Limit"
    if (-not [string]::IsNullOrWhiteSpace($Status)) { $path += "&status=" + [System.Uri]::EscapeDataString($Status) }
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path $path -Token $Context.Token -Label "GET requirements"
    $items = @($response.Data)
    return $items
}

function Get-VersionWorkItems {
    param($Context, [string] $VersionId, [int] $Limit = 200)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/work-items?limit=$Limit" -Token $Context.Token -Label "GET work items"
    $items = @($response.Data)
    return $items
}

function Get-VersionConversations {
    param($Context, [string] $VersionId, [int] $Limit = 100)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/conversations?limit=$Limit" -Token $Context.Token -Label "GET conversations"
    $items = @($response.Data)
    return $items
}

function Get-ConversationMessages {
    param($Context, [string] $VersionId, [string] $ConversationId, [int] $Limit = 200)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/versions/$VersionId/conversations/$ConversationId/messages?limit=$Limit" -Token $Context.Token -Label "GET messages"
    $items = @($response.Data)
    return $items
}

function New-AcceptanceConversation {
    param($Context, [string] $VersionId, [string] $Kind = "clarification", [string] $Title = "验收澄清")
    $response = Invoke-ApiOrFail -Context $Context -Method "POST" -Path "/api/versions/$VersionId/conversations" `
        -Token $Context.Token -ExpectedStatus @(201) -Label "create conversation" -Body @{ kind = $Kind; title = $Title }
    return $response.Data
}

function New-AcceptanceUserMessage {
    <#
        Posts one user message. When -Attachment is supplied (a ContentObject or
        any object with sha256/name/mime/size) the message carries a text part and
        an attachment part, exactly like the UI's file upload flow. A 409
        attachment_pending is retried because the server rejects a message that
        references an attachment whose extraction has not finished yet.
    #>
    param(
        $Context,
        [string] $VersionId,
        [string] $ConversationId,
        [string] $Text,
        [string] $ClientMessageId = "",
        [Parameter(Mandatory = $false)] $Attachment = $null,
        [Parameter(Mandatory = $false)] [int] $PendingRetrySeconds = 120
    )
    if ([string]::IsNullOrWhiteSpace($ClientMessageId)) {
        $ClientMessageId = "acc-" + [Guid]::NewGuid().ToString("N")
    }
    $parts = @(@{ type = "text"; text = $Text })
    if ($null -ne $Attachment) {
        $parts += @{
            type   = "attachment"
            sha256 = [string](Get-JsonProp -Object $Attachment -Name "sha256" -Default "")
            name   = [string](Get-JsonProp -Object $Attachment -Name "name" -Default "")
            mime   = [string](Get-JsonProp -Object $Attachment -Name "mime" -Default "text/markdown")
            size   = [long](Get-JsonProp -Object $Attachment -Name "size" -Default 0)
        }
    }
    $body = @{
        client_message_id = $ClientMessageId
        parts             = $parts
    }
    $deadline = (Get-Date).AddSeconds([Math]::Max(1, $PendingRetrySeconds))
    while ($true) {
        $response = Invoke-Api -Context $Context -Method "POST" `
            -Path "/api/versions/$VersionId/conversations/$ConversationId/messages" `
            -Token $Context.Token -Body $body
        if ($response.Ok) { return $response.Data }
        $code = [string](Get-JsonProp -Object $response.Error -Name "code" -Default "")
        if ($code -ne "attachment_pending" -or (Get-Date) -ge $deadline) {
            $message = [string](Get-JsonProp -Object $response.Error -Name "message" -Default $response.Text)
            throw "post user message failed: HTTP $($response.Status) code=$code message=$message"
        }
        Start-Sleep -Seconds 2
    }
}

function Get-AssistantText {
    param([Parameter(Mandatory = $false)] $Message)
    $parts = Get-JsonProp -Object $Message -Name "parts" -Default @()
    $chunks = @()
    foreach ($part in @($parts)) {
        $type = [string](Get-JsonProp -Object $part -Name "type" -Default "")
        if ($type -eq "text") {
            $text = [string](Get-JsonProp -Object $part -Name "text" -Default "")
            if ($text) { $chunks += $text }
        }
    }
    return ($chunks -join "`n")
}

function Get-QuestionTexts {
    <#
        Extracts user-visible question sentences from an assistant message's
        text parts. Thinking/tool_call parts are intentionally ignored: the
        acceptance criterion is about questions shown to the user, not the
        model's private reasoning. A sentence is a question when it ends in
        '?' or the full-width '？'; the leading markdown list marker is kept out
        of the normalized text used for duplicate detection.
    #>
    param([Parameter(Mandatory = $false)] $Message)
    $text = Get-AssistantText -Message $Message
    if ([string]::IsNullOrWhiteSpace($text)) { return @() }
    $normalized = $text -replace "`r", ""
    $questions = @()
    foreach ($line in ($normalized -split "`n")) {
        $cursor = 0
        while ($cursor -lt $line.Length) {
            $qIndex = $line.IndexOfAny([char[]]@('?', '？'), $cursor)
            if ($qIndex -lt 0) { break }
            $start = $line.LastIndexOfAny([char[]]@('。', '；', ';', '！', '!'), $qIndex)
            $start = if ($start -lt 0) { $cursor } else { $start + 1 }
            $segment = $line.Substring($start, $qIndex - $start + 1).Trim()
            if ($segment.Length -ge 4) { $questions += $segment }
            $cursor = $qIndex + 1
        }
    }
    return $questions
}

function Get-NormalizedQuestion {
    param([Parameter(Mandatory = $true)] [string] $Question)
    $normalized = $Question -replace '\*\*|__|`|#|>', ' '
    $normalized = $normalized -replace '^\s*(\d+[\.、)]|[-*•])\s*', ''
    $normalized = $normalized -replace '[\s\u3000]+', ''
    $normalized = $normalized.Trim()
    return $normalized.ToLowerInvariant()
}

function Select-NormalizedUnique {
    param([Parameter(Mandatory = $false)] [AllowEmptyCollection()] [string[]] $Items)
    $seen = [System.Collections.Generic.HashSet[string]]::new()
    $result = @()
    foreach ($item in @($Items)) {
        if ($seen.Add($item)) { $result += $item }
    }
    return $result
}

# ---------------------------------------------------------------------------
# Resource binding (SUT / environment / accounts / code repository)
# ---------------------------------------------------------------------------

function Get-AllSuts {
    param($Context)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/resources/suts" -Token $Context.Token -Label "GET SUT list"
    $items = @($response.Data)
    return $items
}

function Get-SutDetail {
    param($Context, [string] $SutId)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/resources/suts/$SutId" -Token $Context.Token -Label "GET SUT detail"
    return $response.Data
}

function Get-CodeRepositories {
    param($Context)
    $response = Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/resources/code-repos" -Token $Context.Token -Label "GET code repositories"
    $items = @($response.Data)
    return $items
}

function Resolve-AcceptanceSutBinding {
    <#
        Idempotently ensures the acceptance SUT, its default environment, the
        three SUT accounts and the SUT code repository exist, then returns the
        binding payload for POST /api/versions. Nothing outside EzTest is
        configured: the SUT itself is only read.
    #>
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $false)] [string] $SutName = "墨栈博客",
        [Parameter(Mandatory = $false)] [string] $EnvironmentName = "local",
        [Parameter(Mandatory = $false)] [string] $CodeRepoPath = "D:\project",
        [Parameter(Mandatory = $false)] [switch] $WithoutEnvironmentResources
    )
    $created = @()
    $suts = @(Get-AllSuts -Context $Context)
    $sut = $suts | Where-Object { [string](Get-JsonProp -Object $_ -Name "name" -Default "") -eq $SutName } | Select-Object -First 1
    if ($null -eq $sut) {
        $response = Invoke-ApiOrFail -Context $Context -Method "POST" -Path "/api/resources/suts" `
            -Token $Context.Token -ExpectedStatus @(201) -Label "create SUT" `
            -Body @{ name = $SutName; intro = "EzTest 0.2.1 acceptance SUT (blog)"; family = "web" }
        $sut = $response.Data
        $created += "sut"
    }
    $sutId = [string](Get-JsonProp -Object $sut -Name "id" -Default "")
    $detail = Get-SutDetail -Context $Context -SutId $sutId
    $environments = @(Get-JsonProp -Object $detail -Name "environments" -Default @())
    $environment = $environments | Where-Object {
        ([string](Get-JsonProp -Object $_ -Name "base_url" -Default "")).TrimEnd('/') -eq $Context.SutWebBaseUrl
    } | Select-Object -First 1
    if ($null -eq $environment) {
        $response = Invoke-ApiOrFail -Context $Context -Method "POST" -Path "/api/resources/suts/$sutId/environments" `
            -Token $Context.Token -ExpectedStatus @(201) -Label "create environment" `
            -Body @{ name = $EnvironmentName; base_url = $Context.SutWebBaseUrl; is_default = $true; status = "active" }
        $environment = $response.Data
        $created += "environment"
        $detail = Get-SutDetail -Context $Context -SutId $sutId
        $environments = @(Get-JsonProp -Object $detail -Name "environments" -Default @())
        $environment = $environments | Where-Object { [string](Get-JsonProp -Object $_ -Name "id" -Default "") -eq [string](Get-JsonProp -Object $environment -Name "id" -Default "") } | Select-Object -First 1
        if ($null -eq $environment) { $environment = $response.Data }
    }
    $environmentId = [string](Get-JsonProp -Object $environment -Name "id" -Default "")

    $existingAccounts = @()
    foreach ($item in $environments) {
        $existingAccounts += @(Get-JsonProp -Object $item -Name "accounts" -Default @())
    }
    $requestedAccounts = @(
        @{ username = "admin"; roles = @("admin") },
        @{ username = "openClaw"; roles = @("author") },
        @{ username = "Hermes"; roles = @("author") }
    )
    $accounts = @()
    foreach ($requested in $requestedAccounts) {
        $account = $existingAccounts | Where-Object {
            [string](Get-JsonProp -Object $_ -Name "username" -Default "") -eq [string]$requested.username -and
            [string](Get-JsonProp -Object $_ -Name "environment_id" -Default "") -eq $environmentId
        } | Select-Object -First 1
        if ($null -eq $account) {
            $response = Invoke-ApiOrFail -Context $Context -Method "POST" -Path "/api/resources/suts/$sutId/accounts" `
                -Token $Context.Token -ExpectedStatus @(201) -Label "create SUT account $($requested.username)" `
                -Body @{
                    environment_id = $environmentId
                    username       = [string]$requested.username
                    password       = $Context.SutPassword
                    roles          = @($requested.roles)
                    notes          = "EzTest 0.2.1 acceptance account"
                    status         = "active"
                }
            $account = $response.Data
            $created += ("account:" + [string]$requested.username)
        } else {
            # Acceptance accounts are reused across runs. A stale secret from an
            # older run would make the production UI login fail even though S0's
            # direct SUT API login still passes, so keep the EzTest-side secret
            # aligned with the configured acceptance password.
            $accountId = [string](Get-JsonProp -Object $account -Name "id" -Default "")
            if ($accountId -ne "" -and -not [string]::IsNullOrWhiteSpace($Context.SutPassword)) {
                [void](Invoke-ApiOrFail -Context $Context -Method "PATCH" -Path "/api/resources/accounts/$accountId" `
                    -Token $Context.Token -Label "sync SUT account $($requested.username) password" `
                    -Body @{ password = $Context.SutPassword })
            }
        }
        $accounts += $account
    }

    $repository = $null
    $repositories = @(Get-CodeRepositories -Context $Context)
    $repository = $repositories | Where-Object {
        ([string](Get-JsonProp -Object $_ -Name "path" -Default "")).TrimEnd('\', '/') -ieq $CodeRepoPath.TrimEnd('\', '/')
    } | Select-Object -First 1
    if ($null -eq $repository) {
        $branch = "main"
        try {
            $branchValue = & git -C $CodeRepoPath rev-parse --abbrev-ref HEAD 2>$null
            if ($LASTEXITCODE -eq 0 -and $branchValue) { $branch = ([string]$branchValue).Trim() }
        } catch { }
        $response = Invoke-ApiOrFail -Context $Context -Method "POST" -Path "/api/resources/code-repos" `
            -Token $Context.Token -ExpectedStatus @(201) -Label "create code repository" `
            -Body @{ name = "墨栈博客 (acceptance)"; path = $CodeRepoPath; branch = $branch; include = @(); exclude = @(); status = "active" }
        $repository = $response.Data
        $created += "code_repository"
    }
    $repositoryId = [string](Get-JsonProp -Object $repository -Name "id" -Default "")

    $accountInputs = @()
    for ($index = 0; $index -lt $requestedAccounts.Count; $index++) {
        $account = $accounts[$index]
        $accountId = [string](Get-JsonProp -Object $account -Name "id" -Default "")
        $roles = @(Get-JsonProp -Object $account -Name "roles" -Default @())
        $role = if ($roles.Count -gt 0) { [string]$roles[0] } else { [string]$requestedAccounts[$index].roles[0] }
        $accountInputs += @{ account_id = $accountId; role = $role }
    }

    # Task 86: a version binding is an immutable snapshot, so a version created
    # after P9 discovery must select the pages/APIs discovery proposed into this
    # environment. Reading the catalog never creates or approves a resource.
    # The S1 discovery version opts out on purpose: it is created before
    # discovery, exactly like the pre-fix flow, while the working version below
    # is created afterwards from the discovered rows.
    $environmentResources = $null
    $bindableResources = $null
    if (-not $WithoutEnvironmentResources) {
        $environmentResources = Get-AcceptanceEnvironmentResources -Context $Context -EnvironmentId $environmentId
        if (-not $environmentResources.Ok) {
            throw "environment resources could not be read for environment ${environmentId}: $($environmentResources.Error)"
        }
        $bindableResources = Get-AcceptanceBindableResources -Pages $environmentResources.Pages -APIs $environmentResources.APIs
    }
    $pageIds = @()
    $apiIds = @()
    if ($null -ne $bindableResources) {
        $pageIds = @($bindableResources.PageIds)
        $apiIds = @($bindableResources.ApiIds)
    }
    $binding = New-AcceptanceBindingPayload -SutId $sutId -EnvironmentId $environmentId `
        -CodeRepositoryId $repositoryId -Accounts $accountInputs -PageIds $pageIds -ApiIds $apiIds
    return [pscustomobject]@{
        Sut             = $sut
        Environment     = $environment
        Accounts        = @($accounts)
        Repository      = $repository
        Binding         = $binding
        Created         = @($created)
        EnvironmentCount = $environments.Count
        Resources       = $environmentResources
        BindableResources = $bindableResources
    }
}

# ---------------------------------------------------------------------------
# Discovery resources -> version binding (Task 86)
# ---------------------------------------------------------------------------
#
# P9 discovery records page/api version facts AND (since Task 76) proposes the
# same pages/APIs as sut_page/sut_api rows of the version's bound environment.
# A fact is not bindable and a version binding is immutable, so the acceptance
# flow must read the environment catalog after discovery and create the working
# version from those IDs. Everything below is read-only: no resource is created,
# approved or faked by the acceptance scripts.

function Get-AcceptanceEnvironmentResources {
    <#
        Reads the page/API resource catalog of one environment. Proposed and
        approved rows are returned as-is; the caller decides what is bindable.
        A transport/HTTP failure is reported in Ok/Error instead of throwing so a
        wait loop can record the final failure state honestly.
    #>
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $EnvironmentId
    )
    if ([string]::IsNullOrWhiteSpace($EnvironmentId)) {
        throw "environment id is required to read discovered page/API resources"
    }
    $errors = @()
    $pageResponse = $null
    $apiResponse = $null
    try {
        $pageResponse = Invoke-Api -Context $Context -Method "GET" -Path "/api/resources/environments/$EnvironmentId/pages" -Token $Context.Token -TimeoutSeconds 30
    } catch {
        $errors += "pages: $($_.Exception.Message)"
    }
    try {
        $apiResponse = Invoke-Api -Context $Context -Method "GET" -Path "/api/resources/environments/$EnvironmentId/apis" -Token $Context.Token -TimeoutSeconds 30
    } catch {
        $errors += "apis: $($_.Exception.Message)"
    }
    $pages = @()
    if ($null -ne $pageResponse) {
        if ($pageResponse.Ok) { $pages = @($pageResponse.Data) }
        else { $errors += "pages: HTTP $($pageResponse.Status)" }
    }
    $apis = @()
    if ($null -ne $apiResponse) {
        if ($apiResponse.Ok) { $apis = @($apiResponse.Data) }
        else { $errors += "apis: HTTP $($apiResponse.Status)" }
    }
    return [pscustomobject]@{
        EnvironmentId = $EnvironmentId
        Pages         = @($pages)
        APIs          = @($apis)
        PageCount     = @($pages).Count
        ApiCount      = @($apis).Count
        Ok            = (@($errors).Count -eq 0)
        Error         = (@($errors) -join "; ")
    }
}

function Get-AcceptanceBindableResources {
    <#
        Filters environment catalog rows down to the rows a version may bind:
        proposed and approved by default. Rejected/status-less rows are skipped,
        duplicate IDs are removed and the catalog order is preserved. This is
        the pure half of the discovery hand-off, tested without a live stack.
    #>
    param(
        [Parameter(Mandatory = $false)] [AllowEmptyCollection()] $Pages = @(),
        [Parameter(Mandatory = $false)] [AllowEmptyCollection()] $APIs = @(),
        [Parameter(Mandatory = $false)] [string[]] $Statuses = @("proposed", "approved")
    )
    $allowed = @()
    foreach ($status in @($Statuses)) {
        $normalized = ([string]$status).Trim().ToLowerInvariant()
        if ($normalized -ne "" -and $allowed -notcontains $normalized) { $allowed += $normalized }
    }
    $bindablePages = @()
    $pageIds = @()
    $skippedPages = 0
    foreach ($page in @($Pages)) {
        $id = [string](Get-JsonProp -Object $page -Name "id" -Default "")
        $status = ([string](Get-JsonProp -Object $page -Name "status" -Default "")).Trim().ToLowerInvariant()
        if ($id -eq "" -or $allowed -notcontains $status -or @($pageIds) -contains $id) {
            $skippedPages++
            continue
        }
        $pageIds += $id
        $bindablePages += $page
    }
    $bindableApis = @()
    $apiIds = @()
    $skippedApis = 0
    foreach ($api in @($APIs)) {
        $id = [string](Get-JsonProp -Object $api -Name "id" -Default "")
        $status = ([string](Get-JsonProp -Object $api -Name "status" -Default "")).Trim().ToLowerInvariant()
        if ($id -eq "" -or $allowed -notcontains $status -or @($apiIds) -contains $id) {
            $skippedApis++
            continue
        }
        $apiIds += $id
        $bindableApis += $api
    }
    return [pscustomobject]@{
        Pages           = @($bindablePages)
        APIs            = @($bindableApis)
        PageIds         = @($pageIds)
        ApiIds          = @($apiIds)
        PageCount       = @($bindablePages).Count
        ApiCount        = @($bindableApis).Count
        SkippedPageCount = $skippedPages
        SkippedApiCount = $skippedApis
        Statuses        = @($allowed)
    }
}

function New-AcceptanceBindingPayload {
    <#
        Builds the POST /api/versions binding payload from the resolved SUT /
        environment / repository / accounts plus the discovered page/API IDs.
        Keeping this pure makes the S1 working-version binding testable without
        a stack.
    #>
    param(
        [Parameter(Mandatory = $true)] [string] $SutId,
        [Parameter(Mandatory = $true)] [string] $EnvironmentId,
        [Parameter(Mandatory = $false)] [string] $CodeRepositoryId = "",
        [Parameter(Mandatory = $false)] [AllowEmptyCollection()] $Accounts = @(),
        [Parameter(Mandatory = $false)] [AllowEmptyCollection()] [string[]] $PageIds = @(),
        [Parameter(Mandatory = $false)] [AllowEmptyCollection()] [string[]] $ApiIds = @(),
        [Parameter(Mandatory = $false)] [AllowEmptyCollection()] [string[]] $DatabaseIds = @()
    )
    return @{
        sut_id             = $SutId
        environment_id     = $EnvironmentId
        code_repository_id = $CodeRepositoryId
        page_ids           = @($PageIds)
        api_ids            = @($ApiIds)
        database_ids       = @($DatabaseIds)
        accounts           = @($Accounts)
    }
}

function New-AcceptanceBindingWithResources {
    <#
        Returns a copy of a resolved binding with page_ids/api_ids replaced by
        the discovered resource IDs. The source binding is not mutated, so the
        pre-discovery version keeps its immutable empty scope for evidence.
    #>
    param(
        [Parameter(Mandatory = $true)] $Binding,
        [Parameter(Mandatory = $true)] $Resources
    )
    $copy = @{}
    if ($Binding -is [System.Collections.IDictionary]) {
        foreach ($key in $Binding.Keys) { $copy[[string]$key] = $Binding[$key] }
    } else {
        foreach ($property in $Binding.PSObject.Properties) { $copy[$property.Name] = $property.Value }
    }
    $copy["page_ids"] = @(Get-JsonProp -Object $Resources -Name "PageIds" -Default @())
    $copy["api_ids"] = @(Get-JsonProp -Object $Resources -Name "ApiIds" -Default @())
    return $copy
}

function Get-AcceptanceVersionResourceCounts {
    <#
        Reads the bound pages/APIs of a version read model. This is the data the
        design VersionPack renders (page name + route template locators) and the
        execution binding turns into navigable SUT page URLs. Discovery version
        facts are deliberately NOT consulted: a fact is not a bound resource.
    #>
    param([Parameter(Mandatory = $false)] $Version = $null)
    $bindings = @()
    if ($null -ne $Version) {
        $bindings = @(Get-JsonProp -Object $Version -Name "bindings" -Default @())
    }
    $pages = @()
    $apis = @()
    $pageIds = @()
    $apiIds = @()
    foreach ($binding in $bindings) {
        foreach ($page in @(Get-JsonProp -Object $binding -Name "pages" -Default @())) {
            $id = [string](Get-JsonProp -Object $page -Name "id" -Default "")
            if ($id -ne "" -and @($pageIds) -contains $id) { continue }
            if ($id -ne "") { $pageIds += $id }
            $pages += [pscustomobject]@{
                id             = $id
                name           = [string](Get-JsonProp -Object $page -Name "name" -Default "")
                url            = [string](Get-JsonProp -Object $page -Name "url" -Default "")
                route_template = [string](Get-JsonProp -Object $page -Name "route_template" -Default "")
                status         = [string](Get-JsonProp -Object $page -Name "status" -Default "")
                source         = [string](Get-JsonProp -Object $page -Name "source" -Default "")
            }
        }
        foreach ($api in @(Get-JsonProp -Object $binding -Name "apis" -Default @())) {
            $id = [string](Get-JsonProp -Object $api -Name "id" -Default "")
            if ($id -ne "" -and @($apiIds) -contains $id) { continue }
            if ($id -ne "") { $apiIds += $id }
            $apis += [pscustomobject]@{
                id            = $id
                name          = [string](Get-JsonProp -Object $api -Name "name" -Default "")
                method        = [string](Get-JsonProp -Object $api -Name "method" -Default "")
                path_template = [string](Get-JsonProp -Object $api -Name "path_template" -Default "")
                status        = [string](Get-JsonProp -Object $api -Name "status" -Default "")
                source        = [string](Get-JsonProp -Object $api -Name "source" -Default "")
            }
        }
    }
    $routePages = @($pages | Where-Object { $_.route_template.Trim() -ne "" })
    $urlPages = @($pages | Where-Object { $_.url.Trim() -ne "" })
    $endpointApis = @($apis | Where-Object { $_.path_template.Trim() -ne "" })
    return [pscustomobject]@{
        Bindings        = @($bindings).Count
        PageCount       = @($pages).Count
        ApiCount        = @($apis).Count
        RoutePageCount  = @($routePages).Count
        UrlPageCount    = @($urlPages).Count
        EndpointApiCount = @($endpointApis).Count
        PageIds         = @($pageIds)
        ApiIds          = @($apiIds)
        Pages           = @($pages)
        APIs            = @($apis)
    }
}

function Get-AcceptanceVersionResourceCheck {
    <#
        Hard acceptance gate for Task 86: a version must bind >0 pages, >0 APIs
        when the environment is expected to have them, and at least one route
        template (the locator VersionPack renders) when design is the consumer.
        Returns separate flags so S1/S3 can record one explicit check per
        criterion and fail loudly instead of passing on 0 bound resources.
    #>
    param(
        [Parameter(Mandatory = $false)] $Counts = $null,
        [switch] $RequireApis,
        [switch] $RequireLocators,
        [switch] $RequirePageUrls
    )
    $pages = 0
    $apis = 0
    $routes = 0
    $urls = 0
    if ($null -ne $Counts) {
        $pages = [int](Get-JsonProp -Object $Counts -Name "PageCount" -Default 0)
        $apis = [int](Get-JsonProp -Object $Counts -Name "ApiCount" -Default 0)
        $routes = [int](Get-JsonProp -Object $Counts -Name "RoutePageCount" -Default 0)
        $urls = [int](Get-JsonProp -Object $Counts -Name "UrlPageCount" -Default 0)
    }
    $pagePassed = ($pages -gt 0)
    $apiPassed = (-not $RequireApis) -or ($apis -gt 0)
    $locatorPassed = (-not $RequireLocators) -or ($routes -gt 0)
    $urlPassed = (-not $RequirePageUrls) -or ($urls -gt 0)
    $passed = ($pagePassed -and $apiPassed -and $locatorPassed -and $urlPassed)
    $reasons = @()
    if (-not $pagePassed) {
        $reasons += "version binds 0 pages; discovery facts are not bindable resources and a pre-discovery version can never gain a page (immutable binding)"
    }
    if (-not $apiPassed) {
        $reasons += "version binds 0 APIs"
    }
    if (-not $locatorPassed) {
        $reasons += "no bound page has a route_template locator for design/execution"
    }
    if (-not $urlPassed) {
        $reasons += "no bound page has a SUT url for execution navigation"
    }
    return [pscustomobject]@{
        Passed        = $passed
        PagePassed    = $pagePassed
        ApiPassed     = $apiPassed
        LocatorPassed = $locatorPassed
        UrlPassed     = $urlPassed
        PageDetail    = "pages=$pages routes=$routes urls=$urls"
        ApiDetail     = "apis=$apis"
        LocatorDetail = "routes=$routes pages=$pages"
        UrlDetail     = "urls=$urls pages=$pages"
        Detail        = if ($passed) { "pages=$pages apis=$apis routes=$routes urls=$urls" } else { "pages=$pages apis=$apis routes=$routes urls=$urls; " + ($reasons -join "; ") }
    }
}

function Wait-AcceptanceEnvironmentResources {
    <#
        Waits for P9 discovery to propose page/API resources into the bound
        environment. Discovery records its facts first and proposes the catalog
        rows in the same run, so S1 must wait separately after the fact check.
        Returns the last snapshot on timeout (Ok/HasResources=false) so evidence
        records what was actually there.
    #>
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $EnvironmentId,
        [Parameter(Mandatory = $false)] [switch] $RequireApis,
        [Parameter(Mandatory = $false)] [int] $TimeoutSeconds = 120,
        [Parameter(Mandatory = $false)] [int] $IntervalSeconds = 3
    )
    $requireApis = [bool]$RequireApis
    $watcher = Wait-Until -TimeoutSeconds $TimeoutSeconds -IntervalSeconds $IntervalSeconds -Description "discovery-proposed environment resources" -Condition {
        $snapshot = Get-AcceptanceEnvironmentResources -Context $Context -EnvironmentId $EnvironmentId
        $bindable = Get-AcceptanceBindableResources -Pages $snapshot.Pages -APIs $snapshot.APIs
        if ($bindable.PageCount -gt 0 -and ((-not $requireApis) -or $bindable.ApiCount -gt 0)) {
            return [pscustomobject]@{ Resources = $snapshot; Bindable = $bindable }
        }
        return $null
    }
    if ($watcher.Ok -and $null -ne $watcher.Value) {
        $value = $watcher.Value
        if ($value -is [System.Array]) { $value = @($value)[0] }
        return [pscustomobject]@{
            Ok        = $true
            TimedOut  = $false
            Resources = $value.Resources
            Bindable  = $value.Bindable
        }
    }
    $snapshot = Get-AcceptanceEnvironmentResources -Context $Context -EnvironmentId $EnvironmentId
    $bindable = Get-AcceptanceBindableResources -Pages $snapshot.Pages -APIs $snapshot.APIs
    return [pscustomobject]@{
        Ok        = $false
        TimedOut  = $true
        Resources = $snapshot
        Bindable  = $bindable
    }
}

function New-AcceptanceVersion {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] [string] $Title,
        [Parameter(Mandatory = $true)] $Binding
    )
    $response = Invoke-ApiOrFail -Context $Context -Method "POST" -Path "/api/versions" `
        -Token $Context.Token -ExpectedStatus @(201) -Label "create version" `
        -Body @{ title = $Title; bindings = @($Binding) }
    return $response.Data
}

# ---------------------------------------------------------------------------
# LLM configuration (S0 environment remediation)
# ---------------------------------------------------------------------------

function Initialize-LlmConfiguration {
    <#
        Ensures the real LLM from .env is configured and reachable. The 0.2.1
        first boot seed expects lowercase llm_base_url/llm_key/llm_model_name;
        this repository's .env uses llm_api_key, so the acceptance script writes
        the config through the admin API when needed. The API key never leaves
        this function or enters evidence.
    #>
    param($Context, [int] $TimeoutSeconds = 90)
    $baseUrl = Get-AcceptanceSetting -Context $Context -Name "llm_base_url" -Default ""
    $apiKey = Get-AcceptanceSetting -Context $Context -Name "llm_api_key" -Default ""
    if ([string]::IsNullOrWhiteSpace($apiKey)) {
        $apiKey = Get-AcceptanceSetting -Context $Context -Name "llm_key" -Default ""
    }
    $modelName = Get-AcceptanceSetting -Context $Context -Name "llm_model_name" -Default ""
    if ([string]::IsNullOrWhiteSpace($baseUrl) -or [string]::IsNullOrWhiteSpace($apiKey) -or [string]::IsNullOrWhiteSpace($modelName)) {
        return [pscustomobject]@{
            Ok = $false; ConfigId = ""; Model = ""; ModelId = ""; Seeded = $false
            Error = "llm_base_url/llm_api_key(or llm_key)/llm_model_name missing from environment or .env"
        }
    }
    $baseUrl = $baseUrl.TrimEnd('/')
    $configs = @((Invoke-ApiOrFail -Context $Context -Method "GET" -Path "/api/admin/llm/configs" -Token $Context.Token -Label "list LLM configs").Data)
    $config = $configs | Where-Object { ([string](Get-JsonProp -Object $_ -Name "base_url" -Default "")).TrimEnd('/') -eq $baseUrl } | Select-Object -First 1
    $seeded = $false
    if ($null -eq $config) {
        $createBody = @{
            name             = "acceptance-$modelName"
            provider         = "acceptance"
            protocol         = "openai_chat_completions"
            base_url         = $baseUrl
            api_key          = $apiKey
            is_default       = $true
            status           = "active"
            timeout_seconds  = 90
            max_retries      = 1
            thinking_type    = "auto"
            reasoning_effort = "none"
        }
        $config = (Invoke-ApiOrFail -Context $Context -Method "POST" -Path "/api/admin/llm/configs" `
            -Token $Context.Token -ExpectedStatus @(200, 201) -Label "create LLM config" -Body $createBody).Data
        $seeded = $true
    }
    $configId = [string](Get-JsonProp -Object $config -Name "id" -Default "")
    if (-not (Get-JsonProp -Object $config -Name "is_default" -Default $false)) {
        $config = (Invoke-ApiOrFail -Context $Context -Method "PATCH" -Path "/api/admin/llm/configs/$configId" `
            -Token $Context.Token -Label "make LLM config default" -Body @{ is_default = $true }).Data
    }
    if (-not (Get-JsonProp -Object $config -Name "api_key_set" -Default $false)) {
        Invoke-ApiOrFail -Context $Context -Method "PATCH" -Path "/api/admin/llm/configs/$configId" `
            -Token $Context.Token -Label "set LLM api key" -Body @{ api_key = $apiKey } | Out-Null
    }

    $models = @(Get-JsonProp -Object $config -Name "models" -Default @())
    $model = $models | Where-Object { [string](Get-JsonProp -Object $_ -Name "model" -Default "") -eq $modelName } | Select-Object -First 1
    if ($null -eq $model) {
        try {
            $refreshed = @((Invoke-ApiOrFail -Context $Context -Method "POST" -Path "/api/admin/llm/configs/$configId/refresh-models" `
                -Token $Context.Token -Label "refresh LLM models" -TimeoutSeconds $TimeoutSeconds).Data)
            $model = $refreshed | Where-Object { [string](Get-JsonProp -Object $_ -Name "model" -Default "") -eq $modelName } | Select-Object -First 1
        } catch {
            # The gateway may not expose /models; fall through to a manual add.
            $model = $null
        }
    }
    if ($null -eq $model) {
        $model = (Invoke-ApiOrFail -Context $Context -Method "POST" -Path "/api/admin/llm/configs/$configId/models" `
            -Token $Context.Token -ExpectedStatus @(200, 201) -Label "add LLM model" `
            -Body @{
                model                 = $modelName
                context_window        = 131072
                max_output            = 8192
                supports_tools        = $true
                supports_vision       = $false
                supports_reasoning    = $false
                supports_prompt_cache = $true
            }).Data
    }
    $modelId = [string](Get-JsonProp -Object $model -Name "id" -Default "")
    # Gateways frequently omit capability flags from /models. The acceptance
    # runtime needs tools, so declare the tool capability explicitly (the model
    # is a real tool-calling model).
    if (-not (Get-JsonProp -Object $model -Name "supports_tools" -Default $false)) {
        $model = (Invoke-ApiOrFail -Context $Context -Method "PATCH" -Path "/api/admin/llm/configs/$configId/models/$modelId" `
            -Token $Context.Token -Label "enable LLM tool capability" -Body @{ supports_tools = $true }).Data
    }
    $defaultModelId = [string](Get-JsonProp -Object $config -Name "default_model_id" -Default "")
    if ($defaultModelId -ne $modelId) {
        $config = (Invoke-ApiOrFail -Context $Context -Method "PATCH" -Path "/api/admin/llm/configs/$configId" `
            -Token $Context.Token -Label "set default model" -Body @{ default_model_id = $modelId }).Data
    }
    $test = (Invoke-ApiOrFail -Context $Context -Method "POST" -Path "/api/admin/llm/configs/$configId/test" `
        -Token $Context.Token -Label "test LLM connection" -TimeoutSeconds $TimeoutSeconds).Data
    $ok = [bool](Get-JsonProp -Object $test -Name "ok" -Default $false)
    $errorText = [string](Get-JsonProp -Object $test -Name "error" -Default "")
    return [pscustomobject]@{
        Ok      = $ok
        ConfigId = $configId
        Model   = $modelName
        ModelId = $modelId
        Seeded  = $seeded
        LatencyMS = [int64](Get-JsonProp -Object $test -Name "latency_ms" -Default 0)
        Error   = if ($ok) { "" } else { "LLM connectivity test failed: $errorText" }
    }
}

# ---------------------------------------------------------------------------
# Screenshot (Playwright via the executor's installed module)
# ---------------------------------------------------------------------------

function Save-AcceptanceScreenshot {
    param(
        [Parameter(Mandatory = $true)] $Context,
        [Parameter(Mandatory = $true)] $Scenario,
        [Parameter(Mandatory = $true)] [string] $Route,
        [Parameter(Mandatory = $true)] [string] $FileName,
        [Parameter(Mandatory = $false)] [string] $WaitForSelector = "",
        [Parameter(Mandatory = $false)] [int] $TimeoutSeconds = 60,
        [Parameter(Mandatory = $false)] [bool] $FullPage = $true,
        [Parameter(Mandatory = $false)] [string] $Note = "",
        [Parameter(Mandatory = $false)] [string] $BaseUrl = ""
    )
    $node = Get-Command node -ErrorAction SilentlyContinue
    if ($null -eq $node) { throw "node is not available on PATH; the screenshot helper requires Node.js" }
    $playwrightRoot = Join-Path $Context.RepoRoot "executor/node_modules"
    if (-not (Test-Path -LiteralPath (Join-Path $playwrightRoot "playwright"))) {
        throw "playwright is not installed under executor/node_modules"
    }
    if ($null -eq $script:ScreenshotNodeScript) {
        $tempRoot = Join-Path $Context.RepoRoot ".tmp/acceptance-021"
        New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null
        $scriptPath = Join-Path $tempRoot "screenshot.cjs"
        $js = @'
const fs = require("fs");
const path = require("path");

async function main() {
  const optionsPath = process.argv[2];
  const opts = JSON.parse(fs.readFileSync(optionsPath, "utf8"));
  let playwright;
  try {
    playwright = require("playwright");
  } catch (error) {
    throw new Error("cannot load playwright: " + error.message);
  }
  let browser;
  try {
    browser = await playwright.chromium.launch({ channel: "chrome", headless: true });
  } catch (chromeError) {
    browser = await playwright.chromium.launch({ headless: true });
  }
  const context = await browser.newContext({
    viewport: { width: opts.width || 1440, height: opts.height || 900 },
    deviceScaleFactor: 1,
  });
  await context.addInitScript(({ token, user }) => {
    try {
      if (token) sessionStorage.setItem("eztest.token", token);
      if (user) sessionStorage.setItem("eztest.user", JSON.stringify(user));
    } catch (error) {
      /* storage can be unavailable on about:blank */
    }
  }, { token: opts.token || "", user: opts.user || null });
  const page = await context.newPage();
  const consoleErrors = [];
  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });
  page.on("pageerror", (error) => consoleErrors.push(String(error)));
  let responseStatus = null;
  try {
    const response = await page.goto(opts.url, { waitUntil: "domcontentloaded", timeout: opts.timeoutMs || 60000 });
    responseStatus = response ? response.status() : null;
  } catch (error) {
    throw new Error("navigate " + opts.url + " failed: " + error.message);
  }
  if (opts.waitForSelector) {
    await page.waitForSelector(opts.waitForSelector, { timeout: opts.timeoutMs || 60000 }).catch(() => {});
  }
  await page.waitForTimeout(opts.settleMs || 1500);
  if (opts.url.pathname && opts.url.pathname.length > 0) {
    // keep the requested URL for the evidence result
  }
  const currentUrl = page.url();
  if (opts.failOnLoginRedirect && currentUrl.includes("/login")) {
    throw new Error("page redirected to /login; the bearer session was rejected");
  }
  await page.screenshot({ path: opts.outPath, fullPage: opts.fullPage !== false });
  const title = await page.title().catch(() => "");
  const bodyText = await page.locator("body").innerText().catch(() => "");
  fs.writeFileSync(opts.resultPath, JSON.stringify({
    ok: true,
    status: responseStatus,
    title,
    url: currentUrl,
    consoleErrors,
    textPrefix: String(bodyText || "").slice(0, 4000),
  }, null, 2));
  await browser.close();
}

main().catch((error) => {
  try {
    fs.writeFileSync(process.argv[2] + ".error", String((error && error.stack) || error));
  } catch (writeError) { /* ignore */ }
  console.error(String((error && error.stack) || error));
  process.exit(1);
});
'@
        [System.IO.File]::WriteAllText($scriptPath, $js, [System.Text.UTF8Encoding]::new($false))
        $script:ScreenshotNodeScript = $scriptPath
    }
    $safeName = ($FileName -replace '[^A-Za-z0-9._-]', '-')
    $outPath = Join-Path $Scenario.Directory $safeName
    $resultPath = Join-Path $Scenario.Directory (($safeName -replace '\.png$', '') + ".json")
    $optionsPath = Join-Path $Context.RepoRoot ".tmp/acceptance-021/screenshot-options-$([Guid]::NewGuid().ToString('N')).json"
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $optionsPath) | Out-Null
    $url = if ($Route -match '^https?://') { $Route }
        elseif (-not [string]::IsNullOrWhiteSpace($BaseUrl)) { $BaseUrl.TrimEnd('/') + $Route }
        else { $Context.WebBaseUrl + $Route }
    $options = @{
        url                  = $url
        outPath              = $outPath
        resultPath           = $resultPath
        token                = $Context.Token
        user                 = $Context.User
        waitForSelector      = $WaitForSelector
        timeoutMs            = $TimeoutSeconds * 1000
        settleMs             = 1500
        fullPage             = $FullPage
        failOnLoginRedirect  = $true
        width                = 1440
        height               = 900
    }
    Write-EvidenceJson -Path $optionsPath -Value $options -NoRedaction | Out-Null
    $previousNodePath = $env:NODE_PATH
    try {
        $env:NODE_PATH = if ($previousNodePath) { "$playwrightRoot;$previousNodePath" } else { $playwrightRoot }
        $output = & $node.Source $script:ScreenshotNodeScript $optionsPath 2>&1
        $exit = $LASTEXITCODE
    } finally {
        $env:NODE_PATH = $previousNodePath
        Remove-Item -LiteralPath $optionsPath -Force -ErrorAction SilentlyContinue
    }
    if ($exit -ne 0) {
        $errorText = if (Test-Path -LiteralPath ($optionsPath + ".error")) { [System.IO.File]::ReadAllText($optionsPath + ".error", [System.Text.Encoding]::UTF8) } else { ($output | Out-String) }
        throw "screenshot $FileName failed: $errorText"
    }
    $result = if (Test-Path -LiteralPath $resultPath) {
        $resultText = [System.IO.File]::ReadAllText($resultPath, [System.Text.Encoding]::UTF8)
        $resultText | ConvertFrom-Json
    } else { $null }
    Add-AcceptanceArtifact -Scenario $Scenario -Path $outPath -Kind "screenshot" -Note $Note
    Add-AcceptanceArtifact -Scenario $Scenario -Path $resultPath -Kind "screenshot-metadata" -Note $Note
    return [pscustomobject]@{ Path = $outPath; ResultPath = $resultPath; Result = $result }
}

# ---------------------------------------------------------------------------
# Scenario bootstrap helpers
# ---------------------------------------------------------------------------

function Test-EzTestWebReachable {
    param($Context, [int] $TimeoutSeconds = 30)
    try {
        $response = Invoke-Api -Context $Context -Method "GET" -Path "$($Context.WebBaseUrl)/login" -TimeoutSeconds $TimeoutSeconds -Accept "text/html"
        return [pscustomobject]@{ Ok = ($response.Status -eq 200); Status = $response.Status; Error = "" }
    } catch {
        return [pscustomobject]@{ Ok = $false; Status = 0; Error = $_.Exception.Message }
    }
}

function Test-ExecutorReachable {
    param($Context, [int] $TimeoutSeconds = 15)
    try {
        $response = Invoke-Api -Context $Context -Method "GET" -Path "$($Context.ExecutorUrl)/healthz" -TimeoutSeconds $TimeoutSeconds
        $browser = [string](Get-JsonProp -Object $response.Json -Name "browserVersion" -Default "")
        $status = [string](Get-JsonProp -Object $response.Json -Name "status" -Default "")
        return [pscustomobject]@{
            Ok    = ($response.Status -eq 200 -and $status -eq "ok" -and -not [string]::IsNullOrWhiteSpace($browser))
            Status = $response.Status
            BrowserVersion = $browser
            Error = ""
        }
    } catch {
        return [pscustomobject]@{ Ok = $false; Status = 0; BrowserVersion = ""; Error = $_.Exception.Message }
    }
}

function Get-VersionFactKindCounts {
    param($Context, [string] $VersionId)
    $facts = @(Get-VersionFacts -Context $Context -VersionId $VersionId -Limit 500)
    $counts = [ordered]@{}
    foreach ($fact in $facts) {
        $kind = [string](Get-JsonProp -Object $fact -Name "kind" -Default "")
        if ([string]::IsNullOrWhiteSpace($kind)) { $kind = "(unknown)" }
        if ($counts.Contains($kind)) { $counts[$kind] = $counts[$kind] + 1 } else { $counts[$kind] = 1 }
    }
    return [pscustomobject]@{ Total = $facts.Count; Counts = $counts; Facts = $facts }
}

# ---------------------------------------------------------------------------
# SUT requirement document baseline (Task 72)
# ---------------------------------------------------------------------------

function Read-SutRequirementDocument {
    <#
        Parses the real SUT requirement Markdown baseline into its 99 requirement
        entries: V1 FR-001..FR-058, NFR-001..NFR-009 and V2 FR-101..FR-132.
        The parser reads the document as data (read-only); it never rewrites it.
        Each entry carries the exact document text so callers can feed real
        chunks to the clarification flow without inventing content.
    #>
    param([Parameter(Mandatory = $true)] [string] $Path)
    if (-not (Test-Path -LiteralPath $Path)) { throw "SUT requirement document not found: $Path" }
    $fullPath = (Get-Item -LiteralPath $Path).FullName
    $lines = [System.IO.File]::ReadAllLines($fullPath, [System.Text.Encoding]::UTF8)
    $entries = @()
    for ($i = 0; $i -lt $lines.Count; $i++) {
        $line = $lines[$i]
        if ($line -notmatch '^####\s+((?:FR|NFR)-\d{3})\s+(.+?)\s*$') { continue }
        $id = [string]$Matches[1]
        $title = [string]$Matches[2]
        $titleText = $title
        $bodyEnd = $lines.Count
        for ($j = $i + 1; $j -lt $lines.Count; $j++) {
            if ($lines[$j] -match '^#{1,4}\s') { $bodyEnd = $j; break }
        }
        $priority = ""
        $status = ""
        for ($j = $i + 1; $j -lt [Math]::Min($i + 15, $bodyEnd); $j++) {
            if ($lines[$j] -match '^\*\*优先级\*\*[：:]\s*(P\d)') { $priority = [string]$Matches[1] }
            if ($lines[$j] -match '^\*\*需求状态\*\*[：:]\s*(.+?)\s*$') { $status = [string]$Matches[1] }
        }
        $version = "V1"
        if ($id -match '^FR-1\d{2}$') { $version = "V2" }
        elseif ($status -match '延展' -or $titleText -match '延展·未实现') { $version = "V2" }
        $bodyLines = @()
        for ($j = $i; $j -lt $bodyEnd; $j++) { $bodyLines += $lines[$j] }
        $text = ($bodyLines -join "`n").TrimEnd()
        $entries += [pscustomobject]@{
            id            = $id
            title         = $titleText
            priority      = $priority
            version       = $version
            version_label = if ($version -eq "V2") { "延展·未实现" } else { "V1" }
            section       = ""
            document_path = $fullPath
            line_start    = $i + 1
            line_end      = $bodyEnd
            text          = $text
            char_count    = $text.Length
        }
    }
    return $entries
}

function New-SutRequirementChunks {
    <#
        Groups requirement entries into prompt-sized chunks. Chunk content is
        copied verbatim from the document and bounded below the 20000-rune user
        message limit. V2 entries are excluded from extraction by default.
    #>
    param(
        [Parameter(Mandatory = $true)] $Entries,
        [Parameter(Mandatory = $false)] [string] $Version = "V1",
        [Parameter(Mandatory = $false)] [int] $MaxChars = 15000
    )
    $selected = @($Entries | Where-Object { [string]$_.version -eq $Version })
    $chunks = @()
    $current = @()
    $currentChars = 0
    foreach ($entry in $selected) {
        $size = [int]$entry.char_count
        if ($current.Count -gt 0 -and ($currentChars + $size) -gt $MaxChars) {
            $chunks += , @($current)
            $current = @()
            $currentChars = 0
        }
        $current += $entry
        $currentChars += $size
    }
    if ($current.Count -gt 0) { $chunks += , @($current) }
    return $chunks
}

function Get-RequirementSutId {
    <#
        Returns the FR/NFR ID a requirement is traceable to, or "" when it has
        none. The title is the primary key (the extraction prompt requires the
        title to start with the ID); descriptions are a fallback only for
        requirements whose title lost the ID marker.
    #>
    param([Parameter(Mandatory = $false)] $Requirement)
    $title = [string](Get-JsonProp -Object $Requirement -Name "title" -Default "")
    if ($title -match '((?:FR|NFR)-\d{3})') { return [string]$Matches[1] }
    $description = [string](Get-JsonProp -Object $Requirement -Name "description" -Default "")
    if ($description -match '需求编号[：:]\s*((?:FR|NFR)-\d{3})') { return [string]$Matches[1] }
    if ($description -match '((?:FR|NFR)-\d{3})') { return [string]$Matches[1] }
    return ""
}

function Get-SutRequirementCoverage {
    <#
        Joins the parsed document baseline with the version's requirement list.
        Returns per-entry rows and a summary. The join is by FR/NFR ID extracted
        from the requirement title/description, never by fuzzy title matching.
    #>
    param(
        [Parameter(Mandatory = $true)] $Entries,
        [Parameter(Mandatory = $false)] $Requirements = @(),
        [Parameter(Mandatory = $false)] [string] $Version = "V1",
        [Parameter(Mandatory = $false)] [string] $IdPattern = ""
    )
    $requirementList = @($Requirements)
    $byId = @{}
    $duplicates = @()
    foreach ($requirement in $requirementList) {
        $sutId = Get-RequirementSutId -Requirement $requirement
        if ($sutId -eq "") { continue }
        if ($byId.ContainsKey($sutId)) { $duplicates += $sutId } else { $byId[$sutId] = $requirement }
    }
    $rows = @()
    foreach ($entry in @($Entries)) {
        if (-not [string]::IsNullOrWhiteSpace($Version) -and [string]$entry.version -ne $Version) { continue }
        if (-not [string]::IsNullOrWhiteSpace($IdPattern) -and [string]$entry.id -notmatch $IdPattern) { continue }
        $requirement = $null
        if ($byId.ContainsKey([string]$entry.id)) { $requirement = $byId[[string]$entry.id] }
        $rows += [pscustomobject]@{
            sut_id              = [string]$entry.id
            title               = [string]$entry.title
            priority            = [string]$entry.priority
            doc_version         = [string]$entry.version
            version_label       = [string]$entry.version_label
            requirement_id      = if ($null -ne $requirement) { [string](Get-JsonProp -Object $requirement -Name "id" -Default "") } else { "" }
            requirement_title   = if ($null -ne $requirement) { [string](Get-JsonProp -Object $requirement -Name "title" -Default "") } else { "" }
            requirement_status  = if ($null -ne $requirement) { [string](Get-JsonProp -Object $requirement -Name "status" -Default "") } else { "" }
            requirement_priority = if ($null -ne $requirement) { [string](Get-JsonProp -Object $requirement -Name "priority" -Default "") } else { "" }
            covered             = ($null -ne $requirement)
        }
    }
    $coveredRows = @($rows | Where-Object { $_.covered })
    $p0Rows = @($rows | Where-Object { $_.priority -eq "P0" })
    $p0Covered = @($p0Rows | Where-Object { $_.covered })
    $missing = @($rows | Where-Object { -not $_.covered } | ForEach-Object { $_.sut_id })
    return [pscustomobject]@{
        Rows              = $rows
        Total             = $rows.Count
        Covered           = $coveredRows.Count
        Missing           = $missing
        P0Total           = $p0Rows.Count
        P0Covered         = $p0Covered.Count
        P0Missing         = @($p0Rows | Where-Object { -not $_.covered } | ForEach-Object { $_.sut_id })
        DuplicateIds      = @($duplicates | Select-Object -Unique)
        UntraceableCount  = @($requirementList | Where-Object { (Get-RequirementSutId -Requirement $_) -eq "" }).Count
    }
}
