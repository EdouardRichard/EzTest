#requires -Version 5.1
<#
.SYNOPSIS
    Shared fixtures and helpers for the EzTest 0.2 P8 acceptance scripts.

.DESCRIPTION
    This file is dot-sourced by scripts/p8-acceptance.ps1 (and by the other P8
    scripts that need the same environment). It owns the reusable, P8-specific
    plumbing:

      * .env/database parsing without ever printing credentials;
      * JSON HTTP calls and authenticated byte downloads;
      * psql calls and polling helpers;
      * process lifecycle for the two target fixtures, the two real executors,
        the PDF renderer and the API binary;
      * check/scenario evidence writers;
      * settings helpers;
      * the P7 -> P8 database clone used by the acceptance run.

    It is deliberately PowerShell 5.1 compatible (no ternary/?? operators, no
    pwsh-only parameters) and never logs an executor/render token, an LLM API
    key, an account password or a database password.

    Nothing in this file changes product code. Fixture databases and the
    isolated content root are environment setup; the product journey itself is
    driven only through the frozen P8 HTTP contract.

.NOTES
    State is kept in $script:P8Context. p8-acceptance.ps1 creates it with
    New-P8Context, fills the database/process details, and then calls the
    helpers below. Every helper accepts explicit parameters so it can also be
    used from a one-off debugging shell.
#>

$script:P8FixturesRoot = $PSScriptRoot
$script:P8Context = $null
$script:P8LastWaitValue = $null
$script:P8LastWaitError = ""

# ---------------------------------------------------------------------------
# context, logging and evidence
# ---------------------------------------------------------------------------

function New-P8Context {
    param(
        [string]$RepoRoot = "",
        [string]$EvidenceDir = ""
    )
    if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
        $RepoRoot = Split-Path -Parent $script:P8FixturesRoot
    }
    if ([string]::IsNullOrWhiteSpace($EvidenceDir)) {
        $EvidenceDir = Join-Path $RepoRoot ".tmp\p8-acceptance"
    }
    $processes = [System.Collections.Generic.List[object]]::new()
    $failures = [System.Collections.Generic.List[string]]::new()
    $bugNotes = [System.Collections.Generic.List[string]]::new()
    $scenarioResults = [System.Collections.Generic.List[object]]::new()
    return @{
        RepoRoot        = $RepoRoot
        EvidenceDir     = $EvidenceDir
        StartedAt       = Get-Date
        BaseUrl         = ""
        ApiPort         = 0
        Token           = ""
        Psql            = ""
        Go              = ""
        Node            = ""
        Db              = @{}
        ExecutorToken   = ""
        RenderToken     = ""
        Processes       = $processes
        Failures        = $failures
        BugNotes        = $bugNotes
        ScenarioResults = $scenarioResults
    }
}

function Set-P8Context {
    param([Parameter(Mandatory = $true)]$Context)
    $script:P8Context = $Context
}

function Get-P8Context {
    if ($null -eq $script:P8Context) {
        $script:P8Context = New-P8Context
    }
    return $script:P8Context
}

function Initialize-P8Evidence {
    param([string]$EvidenceDir = "")
    $context = Get-P8Context
    if (-not [string]::IsNullOrWhiteSpace($EvidenceDir)) {
        $context.EvidenceDir = $EvidenceDir
    }
    if (-not (Test-Path -LiteralPath $context.EvidenceDir)) {
        New-Item -ItemType Directory -Force -Path $context.EvidenceDir | Out-Null
    }
    return $context.EvidenceDir
}

function Write-P8Log {
    param(
        [string]$Message,
        [string]$Color = "Gray"
    )
    $context = Get-P8Context
    if (-not (Test-Path -LiteralPath $context.EvidenceDir)) {
        New-Item -ItemType Directory -Force -Path $context.EvidenceDir | Out-Null
    }
    $line = "$(Get-Date -Format 'HH:mm:ss') $Message"
    Write-Host $line -ForegroundColor $Color
    Add-Content -Path (Join-Path $context.EvidenceDir "acceptance.log") -Value $line -Encoding utf8
}

function Write-P8Evidence {
    param(
        [string]$Name,
        [string]$Content
    )
    $context = Get-P8Context
    if (-not (Test-Path -LiteralPath $context.EvidenceDir)) {
        New-Item -ItemType Directory -Force -Path $context.EvidenceDir | Out-Null
    }
    $path = Join-Path $context.EvidenceDir $Name
    Set-Content -Path $path -Value $Content -Encoding utf8
    return $path
}

function Write-P8JsonEvidence {
    param(
        [string]$Name,
        $Value,
        [int]$Depth = 20
    )
    $json = $Value | ConvertTo-Json -Depth $Depth
    return Write-P8Evidence -Name $Name -Content $json
}

function Add-P8BugNote {
    param([string]$Note)
    $context = Get-P8Context
    $line = "$(Get-Date -Format o) | $Note"
    $context.BugNotes.Add($line)
    Add-Content -Path (Join-Path $context.EvidenceDir "bug-notes.txt") -Value $line -Encoding utf8
}

function Write-P8Check {
    param(
        $Checks,
        [bool]$Ok,
        [string]$Label,
        [string]$Detail = ""
    )
    $entry = [pscustomobject]@{ Ok = $Ok; Label = $Label; Detail = $Detail }
    $Checks.Add($entry)
    $suffix = ""
    if (-not [string]::IsNullOrWhiteSpace($Detail)) {
        $suffix = " :: $Detail"
    }
    if ($Ok) {
        Write-P8Log "  PASS: $Label$suffix" "Green"
    } else {
        Write-P8Log "  FAIL: $Label$suffix" "Red"
        $context = Get-P8Context
        $context.Failures.Add("$Label$suffix")
        Add-P8BugNote -Note ("FAIL: " + $Label + $suffix)
    }
}

function Write-P8ScenarioSummary {
    param(
        [string]$Name,
        $Checks,
        [string]$Summary = "",
        [int]$DurationSeconds = 0
    )
    $context = Get-P8Context
    $pass = $true
    foreach ($check in $Checks) {
        if (-not $check.Ok) { $pass = $false }
    }
    $line = "{0} | {1} | {2}s | {3}" -f $Name, $(if ($pass) { "PASS" } else { "FAIL" }), $DurationSeconds, $Summary
    Add-Content -Path (Join-Path $context.EvidenceDir "scenarios.txt") -Value $line -Encoding utf8
    $result = [pscustomobject]@{
        Name            = $Name
        Pass            = $pass
        Checks          = $Checks
        Summary         = $Summary
        DurationSeconds = $DurationSeconds
    }
    $context.ScenarioResults.Add($result)
    Write-P8Log $line $(if ($pass) { "Green" } else { "Red" })
    return $result
}

function Get-P8Array {
    param($Value)
    if ($null -eq $Value) { return @() }
    return @($Value)
}

function Get-P8FileSha {
    param([string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

# ---------------------------------------------------------------------------
# .env and database URL helpers
# ---------------------------------------------------------------------------

function Import-P8DotEnv {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "missing .env at $Path"
    }
    $map = @{}
    foreach ($line in Get-Content -LiteralPath $Path) {
        $trim = $line.Trim()
        if ($trim -eq "" -or $trim.StartsWith("#")) { continue }
        $eq = $trim.IndexOf("=")
        if ($eq -lt 1) { continue }
        $key = $trim.Substring(0, $eq).Trim()
        $value = $trim.Substring($eq + 1).Trim()
        if ($value.Length -ge 2) {
            $first = $value.Substring(0, 1)
            $last = $value.Substring($value.Length - 1, 1)
            if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                $value = $value.Substring(1, $value.Length - 2)
            }
        }
        $map[$key] = $value
    }
    return $map
}

function ConvertFrom-P8DatabaseUrl {
    param([Parameter(Mandatory = $true)][string]$Url)
    $pattern = '^(?<scheme>postgres(?:ql)?)://(?<user>[^:/@]+)(?::(?<password>[^@]*))?@(?<host>[^:/?]+)(?::(?<port>\d+))?/(?<database>[^?]+)(?<query>\?.*)?$'
    $match = [regex]::Match($Url, $pattern)
    if (-not $match.Success) {
        throw "EZTEST_DATABASE_URL is not a supported postgres URL"
    }
    $port = 5432
    if ($match.Groups["port"].Success) {
        $port = [int]$match.Groups["port"].Value
    }
    $query = ""
    if ($match.Groups["query"].Success) {
        $query = $match.Groups["query"].Value
    }
    return [pscustomobject]@{
        Scheme   = $match.Groups["scheme"].Value
        User     = [uri]::UnescapeDataString($match.Groups["user"].Value)
        Password = [uri]::UnescapeDataString($match.Groups["password"].Value)
        Host     = $match.Groups["host"].Value
        Port     = $port
        Database = [uri]::UnescapeDataString($match.Groups["database"].Value)
        Query    = $query
    }
}

function New-P8DatabaseUrl {
    param(
        [Parameter(Mandatory = $true)][string]$DatabaseName,
        $DatabaseInfo = $null
    )
    $context = Get-P8Context
    $info = $DatabaseInfo
    if ($null -eq $info) { $info = $context.Db }
    if ($null -eq $info -or [string]::IsNullOrWhiteSpace([string]$info.Host)) {
        throw "database information is not initialized"
    }
    $userInfo = [uri]::EscapeDataString([string]$info.User)
    if (-not [string]::IsNullOrWhiteSpace([string]$info.Password)) {
        $userInfo = $userInfo + ":" + [uri]::EscapeDataString([string]$info.Password)
    }
    return "{0}://{1}@{2}:{3}/{4}{5}" -f $info.Scheme, $userInfo, $info.Host, $info.Port, $DatabaseName, $info.Query
}

# ---------------------------------------------------------------------------
# psql
# ---------------------------------------------------------------------------

function Invoke-P8Psql {
    param(
        [Parameter(Mandatory = $true)][string]$Sql,
        [string]$Database = "",
        [switch]$AllowFailure
    )
    $context = Get-P8Context
    if ([string]::IsNullOrWhiteSpace($context.Psql)) {
        throw "psql path is not initialized"
    }
    if ([string]::IsNullOrWhiteSpace($Database)) {
        $Database = [string]$context.Db.Name
    }
    $sqlPath = Join-Path $context.EvidenceDir ("sql-" + [guid]::NewGuid().ToString("N") + ".sql")
    Set-Content -Path $sqlPath -Value $Sql -Encoding utf8
    $previousPassword = $env:PGPASSWORD
    $previousEncoding = $env:PGCLIENTENCODING
    $previousErrorAction = $ErrorActionPreference
    try {
        $env:PGPASSWORD = [string]$context.Db.Password
        $env:PGCLIENTENCODING = "UTF8"
        $ErrorActionPreference = "Continue"
        $arguments = @(
            "-X", "-h", [string]$context.Db.Host, "-p", [string]$context.Db.Port,
            "-U", [string]$context.Db.User, "-d", $Database,
            "-tA", "-F", "|", "-v", "ON_ERROR_STOP=1", "-P", "pager=off", "-f", $sqlPath
        )
        $output = & $context.Psql @arguments 2>&1
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorAction
        if ($null -eq $previousPassword) { Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue }
        else { $env:PGPASSWORD = $previousPassword }
        if ($null -eq $previousEncoding) { Remove-Item Env:PGCLIENTENCODING -ErrorAction SilentlyContinue }
        else { $env:PGCLIENTENCODING = $previousEncoding }
        Remove-Item -LiteralPath $sqlPath -Force -ErrorAction SilentlyContinue
    }
    if ($code -ne 0) {
        $detail = ($output | Out-String).Trim()
        if ($AllowFailure) {
            Write-P8Log "psql failed (allowed): $detail" "Yellow"
            return @()
        }
        throw ("psql failed: " + $detail)
    }
    return @($output | ForEach-Object { [string]$_ } | Where-Object { $_ -ne "" })
}

function Get-P8Scalar {
    param([Parameter(Mandatory = $true)][string]$Sql, [string]$Database = "")
    $rows = @(Invoke-P8Psql -Sql $Sql -Database $Database)
    if ($rows.Count -eq 0) { return "" }
    return ([string]$rows[0]).Trim()
}

function Wait-P8SqlValue {
    param(
        [Parameter(Mandatory = $true)][string]$Sql,
        [Parameter(Mandatory = $true)][string]$Want,
        [int]$TimeoutSeconds = 120,
        [int]$IntervalMilliseconds = 2000
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = ""
    while ((Get-Date) -lt $deadline) {
        $last = Get-P8Scalar -Sql $Sql
        if ($last -eq $Want) { return $true }
        Start-Sleep -Milliseconds $IntervalMilliseconds
    }
    $script:P8LastWaitValue = $last
    return $false
}

function Get-P8DbSettingText {
    param([Parameter(Mandatory = $true)][string]$Key)
    return Get-P8Scalar -Sql ("SELECT value_json #>> '{}' FROM system_setting WHERE key = '" + (ConvertTo-P8SqlText $Key) + "';")
}

function ConvertTo-P8SqlText {
    param([string]$Value)
    return $Value.Replace("'", "''")
}

# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------

function Invoke-P8Api {
    param(
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Path,
        $Body = $null,
        $Token = $null,
        [int]$TimeoutSec = 90,
        [string]$BaseUrl = ""
    )
    $context = Get-P8Context
    if ([string]::IsNullOrWhiteSpace($BaseUrl)) {
        $BaseUrl = $context.BaseUrl
    }
    if ([string]::IsNullOrWhiteSpace($BaseUrl)) {
        throw "API base URL is not initialized"
    }
    if ($null -eq $Token) { $Token = $context.Token }
    $headers = @{}
    if (-not [string]::IsNullOrWhiteSpace([string]$Token)) {
        $headers["Authorization"] = "Bearer $Token"
    }
    $parameters = @{
        Uri             = $BaseUrl.TrimEnd("/") + $Path
        Method          = $Method
        Headers         = $headers
        UseBasicParsing = $true
        TimeoutSec      = $TimeoutSec
    }
    if ($null -ne $Body) {
        $json = if ($Body -is [string]) { $Body } else { $Body | ConvertTo-Json -Depth 20 -Compress }
        $parameters["Body"] = [System.Text.Encoding]::UTF8.GetBytes($json)
        $parameters["ContentType"] = "application/json; charset=utf-8"
    }
    try {
        $response = Invoke-WebRequest @parameters
        $json = $null
        if (-not [string]::IsNullOrWhiteSpace($response.Content)) {
            try { $json = $response.Content | ConvertFrom-Json } catch { $json = $null }
        }
        return [pscustomobject]@{
            Status  = [int]$response.StatusCode
            Json    = $json
            Text    = [string]$response.Content
            Headers = $response.Headers
        }
    } catch {
        $status = 0
        $text = $_.Exception.Message
        if ($null -ne $_.ErrorDetails -and -not [string]::IsNullOrWhiteSpace($_.ErrorDetails.Message)) {
            $text = $_.ErrorDetails.Message
        }
        if ($null -ne $_.Exception.Response) {
            try { $status = [int]$_.Exception.Response.StatusCode } catch { $status = 0 }
            if ([string]::IsNullOrWhiteSpace($text) -or $text -eq $_.Exception.Message) {
                try {
                    $stream = $_.Exception.Response.GetResponseStream()
                    $reader = New-Object System.IO.StreamReader($stream)
                    try { $text = $reader.ReadToEnd() } finally { $reader.Dispose() }
                } catch { }
            }
        }
        $json = $null
        try { $json = $text | ConvertFrom-Json } catch { $json = $null }
        return [pscustomobject]@{
            Status  = $status
            Json    = $json
            Text    = $text
            Headers = $null
        }
    }
}

function Invoke-P8Download {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$OutFile,
        [int]$TimeoutSec = 180,
        [string]$BaseUrl = ""
    )
    $context = Get-P8Context
    if ([string]::IsNullOrWhiteSpace($BaseUrl)) { $BaseUrl = $context.BaseUrl }
    $headers = @{}
    if (-not [string]::IsNullOrWhiteSpace($context.Token)) {
        $headers["Authorization"] = "Bearer $($context.Token)"
    }
    $directory = Split-Path -Parent $OutFile
    if (-not [string]::IsNullOrWhiteSpace($directory) -and -not (Test-Path -LiteralPath $directory)) {
        New-Item -ItemType Directory -Force -Path $directory | Out-Null
    }
    if (Test-Path -LiteralPath $OutFile) { Remove-Item -LiteralPath $OutFile -Force -ErrorAction SilentlyContinue }
    try {
        $response = Invoke-WebRequest -Uri ($BaseUrl.TrimEnd("/") + $Path) -Headers $headers -UseBasicParsing -OutFile $OutFile -TimeoutSec $TimeoutSec
        $status = 0
        if ($null -ne $response -and $null -ne $response.StatusCode) { $status = [int]$response.StatusCode }
        elseif (Test-Path -LiteralPath $OutFile) { $status = 200 }
        $sha = ""
        $size = 0
        if ($status -eq 200 -and (Test-Path -LiteralPath $OutFile)) {
            $sha = Get-P8FileSha -Path $OutFile
            $size = (Get-Item -LiteralPath $OutFile).Length
        }
        return [pscustomobject]@{ Status = $status; Sha256 = $sha; Size = $size; Path = $OutFile }
    } catch {
        $status = 0
        if ($null -ne $_.Exception.Response) {
            try { $status = [int]$_.Exception.Response.StatusCode } catch { $status = 0 }
        }
        return [pscustomobject]@{ Status = $status; Sha256 = ""; Size = 0; Path = $OutFile }
    }
}

function Wait-P8Until {
    param(
        [Parameter(Mandatory = $true)][scriptblock]$Condition,
        [int]$TimeoutSeconds = 120,
        [string]$Description = "condition",
        [int]$IntervalMilliseconds = 1000,
        [switch]$ThrowOnTimeout
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $lastError = ""
    while ((Get-Date) -lt $deadline) {
        try {
            $value = & $Condition
            if ($null -ne $value -and $value -ne $false) {
                return $value
            }
        } catch {
            $lastError = $_.Exception.Message
        }
        Start-Sleep -Milliseconds $IntervalMilliseconds
    }
    $script:P8LastWaitValue = $null
    $script:P8LastWaitError = $lastError
    if ($ThrowOnTimeout) {
        throw ("timeout after {0}s waiting for {1}" -f $TimeoutSeconds, $Description)
    }
    return $null
}

function Wait-P8HttpOk {
    param([string]$Url, [int]$TimeoutSeconds = 60)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 5
            if ([int]$response.StatusCode -eq 200) { return $true }
        } catch { }
        Start-Sleep -Milliseconds 500
    }
    return $false
}

# ---------------------------------------------------------------------------
# process lifecycle
# ---------------------------------------------------------------------------

function Test-P8PortInUse {
    param([int]$Port)
    try {
        $connection = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
        return ($null -ne $connection)
    } catch {
        return $false
    }
}

function Wait-P8PortClosed {
    param([int]$Port, [int]$TimeoutSeconds = 20)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (-not (Test-P8PortInUse -Port $Port)) { return $true }
        Start-Sleep -Milliseconds 300
    }
    return $false
}

function Stop-P8PortProcesses {
    param([int]$Port)
    try {
        $connections = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
        foreach ($connection in @($connections)) {
            if ($null -ne $connection) {
                Stop-Process -Id $connection.OwningProcess -Force -ErrorAction SilentlyContinue
            }
        }
    } catch { }
}

function Stop-P8Process {
    param(
        $Process,
        [int]$Port = 0,
        [int]$TimeoutSeconds = 15
    )
    foreach ($item in @($Process)) {
        if ($null -eq $item) { continue }
        $target = $item
        if ($null -ne $item.Process) { $target = $item.Process }
        try {
            if ($null -ne $target -and -not $target.HasExited) {
                Stop-Process -Id $target.Id -Force -ErrorAction SilentlyContinue
                $target.WaitForExit(5000) | Out-Null
            }
        } catch { }
    }
    if ($Port -gt 0) {
        Wait-P8PortClosed -Port $Port -TimeoutSeconds $TimeoutSeconds | Out-Null
    }
}

function Start-P8NodeProcess {
    param(
        [Parameter(Mandatory = $true)][string]$ScriptRelativePath,
        [Parameter(Mandatory = $true)][string]$LogName,
        [Parameter(Mandatory = $true)][hashtable]$Environment,
        [string]$WorkingDirectory = "",
        [string]$Kind = "node"
    )
    $context = Get-P8Context
    if ([string]::IsNullOrWhiteSpace($context.Node)) {
        $nodeCommand = Get-Command node.exe -ErrorAction SilentlyContinue
        if ($null -eq $nodeCommand) { $nodeCommand = Get-Command node -ErrorAction SilentlyContinue }
        if ($null -eq $nodeCommand) { throw "node executable not found on PATH" }
        $context.Node = $nodeCommand.Source
    }
    if ([string]::IsNullOrWhiteSpace($WorkingDirectory)) {
        $WorkingDirectory = $context.RepoRoot
    }
    $log = Join-Path $context.EvidenceDir ($LogName + ".log")
    $errorLog = Join-Path $context.EvidenceDir ($LogName + ".err.log")
    $keys = @($Environment.Keys)
    $previous = @{}
    foreach ($key in $keys) {
        $previous[$key] = [System.Environment]::GetEnvironmentVariable($key, [System.EnvironmentVariableTarget]::Process)
        Set-Item -Path ("Env:" + $key) -Value ([string]$Environment[$key])
    }
    try {
        $process = Start-Process -FilePath $context.Node `
            -ArgumentList @($ScriptRelativePath) `
            -WorkingDirectory $WorkingDirectory `
            -PassThru -NoNewWindow `
            -RedirectStandardOutput $log `
            -RedirectStandardError $errorLog
    } finally {
        foreach ($key in $keys) {
            if ($null -eq $previous[$key]) { Remove-Item -Path ("Env:" + $key) -ErrorAction SilentlyContinue }
            else { Set-Item -Path ("Env:" + $key) -Value $previous[$key] }
        }
    }
    $record = [pscustomobject]@{
        Kind      = $Kind
        Name      = $LogName
        Process   = $process
        Log       = $log
        ErrorLog  = $errorLog
        StartedAt = Get-Date
    }
    $context.Processes.Add($record)
    return $record
}

function Get-P8LogTail {
    param([string]$Path, [int]$Lines = 30)
    if (-not (Test-Path -LiteralPath $Path)) { return "" }
    return ((Get-Content -LiteralPath $Path -Tail $Lines) | Out-String).Trim()
}

function Start-P8Fixture {
    param(
        [int]$OrdersPort = 14080,
        [int]$PaymentsPort = 14081,
        [int]$TimeoutSeconds = 60
    )
    $context = Get-P8Context
    foreach ($port in @($OrdersPort, $PaymentsPort)) {
        if (Test-P8PortInUse -Port $port) {
            throw "port $port is already in use; stop the stale target fixture first"
        }
    }
    $fixtureScript = Join-Path $context.RepoRoot "scripts\fixture-target.cjs"
    if (-not (Test-Path -LiteralPath $fixtureScript)) {
        throw "fixture-target.cjs not found at $fixtureScript"
    }
    $orders = Start-P8NodeProcess -Kind "fixture-orders" -LogName "fixture-orders" `
        -ScriptRelativePath $fixtureScript -WorkingDirectory $context.RepoRoot `
        -Environment @{ FIXTURE_PORT = [string]$OrdersPort; FIXTURE_VARIANT = "orders" }
    $payments = Start-P8NodeProcess -Kind "fixture-payments" -LogName "fixture-payments" `
        -ScriptRelativePath $fixtureScript -WorkingDirectory $context.RepoRoot `
        -Environment @{ FIXTURE_PORT = [string]$PaymentsPort; FIXTURE_VARIANT = "payments" }
    if (-not (Wait-P8HttpOk -Url "http://127.0.0.1:$OrdersPort/p3/login" -TimeoutSeconds $TimeoutSeconds)) {
        $tail = Get-P8LogTail -Path $orders.ErrorLog
        throw "orders fixture did not become healthy on $OrdersPort. $tail"
    }
    if (-not (Wait-P8HttpOk -Url "http://127.0.0.1:$PaymentsPort/p3/login" -TimeoutSeconds $TimeoutSeconds)) {
        $tail = Get-P8LogTail -Path $payments.ErrorLog
        throw "payments fixture did not become healthy on $PaymentsPort. $tail"
    }
    return @($orders, $payments)
}

function Start-P8Renderer {
    param(
        [int]$Port = 13337,
        [int]$TimeoutSeconds = 120
    )
    $context = Get-P8Context
    if (Test-P8PortInUse -Port $Port) {
        throw "port $Port is already in use; stop the stale renderer first"
    }
    if ([string]::IsNullOrWhiteSpace($context.RenderToken)) {
        throw "report.render_token is missing; cannot start the renderer"
    }
    $record = Start-P8NodeProcess -Kind "renderer" -LogName "renderer" `
        -ScriptRelativePath (Join-Path $context.RepoRoot "renderer\src\server.mjs") `
        -WorkingDirectory $context.RepoRoot `
        -Environment @{
            EZTEST_RENDERER_ADDR           = "127.0.0.1:$Port"
            EZTEST_RENDER_TOKEN            = $context.RenderToken
            EZTEST_RENDER_TIMEOUT_SECONDS  = "60"
            EZTEST_RENDER_CHANNEL          = "chrome"
        }
    if (-not (Wait-P8HttpOk -Url "http://127.0.0.1:$Port/healthz" -TimeoutSeconds $TimeoutSeconds)) {
        $tail = Get-P8LogTail -Path $record.ErrorLog
        throw "renderer did not become healthy on $Port. $tail"
    }
    return $record
}

function Start-P8Executor {
    param(
        [int[]]$Ports = @(13345, 13346),
        [int]$ApiPort = 13333,
        [string]$Token = "",
        [int]$MaxContexts = 8,
        [int]$HeartbeatSeconds = 2,
        [int]$TimeoutSeconds = 120,
        [switch]$WaitRegistered
    )
    $context = Get-P8Context
    if ([string]::IsNullOrWhiteSpace($Token)) { $Token = $context.ExecutorToken }
    if ([string]::IsNullOrWhiteSpace($Token)) {
        throw "execution.executor_token is missing; cannot start the executor"
    }
    $records = @()
    $index = 0
    foreach ($port in $Ports) {
        if (Test-P8PortInUse -Port $port) {
            throw "port $port is already in use; stop the stale executor first"
        }
        $index += 1
        $name = "p8-executor-" + [char](64 + $index)
        $record = Start-P8NodeProcess -Kind ("executor-" + $name) -LogName ("executor-" + $name) `
            -ScriptRelativePath (Join-Path $context.RepoRoot "executor\src\server.mjs") `
            -WorkingDirectory $context.RepoRoot `
            -Environment @{
                EZTEST_EXECUTOR_ADDR            = "127.0.0.1:$port"
                EZTEST_EXECUTOR_TOKEN           = $Token
                EZTEST_EXECUTOR_NAME            = $name
                EZTEST_EXECUTOR_MAX_CONTEXTS    = [string]$MaxContexts
                EZTEST_EXECUTOR_HEARTBEAT_SECONDS = [string]$HeartbeatSeconds
                EZTEST_API_URL                  = "http://127.0.0.1:$ApiPort"
                EZTEST_EXEC_ALLOWED_HOSTS       = "127.0.0.1,localhost"
            }
        if (-not (Wait-P8HttpOk -Url "http://127.0.0.1:$port/healthz" -TimeoutSeconds $TimeoutSeconds)) {
            $tail = Get-P8LogTail -Path $record.ErrorLog
            throw "executor on $port did not become healthy. $tail"
        }
        $records += $record
        if ($WaitRegistered) {
            $endpoint = "http://127.0.0.1:$port"
            $registered = Wait-P8Until -TimeoutSeconds 60 -IntervalMilliseconds 1000 -Description "executor $endpoint registration" -Condition {
                $count = [int](Get-P8Scalar -Sql ("SELECT count(*) FROM executor_instance WHERE endpoint = '" + (ConvertTo-P8SqlText $endpoint) + "';"))
                if ($count -ge 1) { return $true }
                return $null
            }
            if ($null -eq $registered) {
                throw "executor push registration was not observed for $endpoint"
            }
        }
    }
    return $records
}

function Start-P8Api {
    param(
        [int]$Port = 13333,
        [Parameter(Mandatory = $true)][string]$DatabaseUrl,
        [int]$ExecutorPort = 13345,
        [int]$WebPort = 6868,
        [string]$BinaryPath = "",
        [int]$TimeoutSeconds = 180
    )
    $context = Get-P8Context
    if (Test-P8PortInUse -Port $Port) {
        throw "port $Port is already in use; stop the stale API first"
    }
    if ([string]::IsNullOrWhiteSpace($BinaryPath)) {
        $BinaryPath = Join-Path $context.EvidenceDir "eztest-p8.exe"
    }
    if (-not (Test-Path -LiteralPath $BinaryPath)) {
        throw "API binary not found at $BinaryPath"
    }
    $log = Join-Path $context.EvidenceDir "api-a.log"
    $errorLog = Join-Path $context.EvidenceDir "api-a.err.log"
    $keys = @("EZTEST_DATABASE_URL", "EZTEST_API_PORT", "EZTEST_WEB_PORT", "EZTEST_EXECUTOR_PORT")
    $values = @{
        EZTEST_DATABASE_URL   = $DatabaseUrl
        EZTEST_API_PORT       = [string]$Port
        EZTEST_WEB_PORT       = [string]$WebPort
        EZTEST_EXECUTOR_PORT  = [string]$ExecutorPort
    }
    $previous = @{}
    foreach ($key in $keys) {
        $previous[$key] = [System.Environment]::GetEnvironmentVariable($key, [System.EnvironmentVariableTarget]::Process)
        Set-Item -Path ("Env:" + $key) -Value $values[$key]
    }
    try {
        $process = Start-Process -FilePath $BinaryPath -PassThru -NoNewWindow `
            -RedirectStandardOutput $log -RedirectStandardError $errorLog
    } finally {
        foreach ($key in $keys) {
            if ($null -eq $previous[$key]) { Remove-Item -Path ("Env:" + $key) -ErrorAction SilentlyContinue }
            else { Set-Item -Path ("Env:" + $key) -Value $previous[$key] }
        }
    }
    $record = [pscustomobject]@{
        Kind      = "api"
        Name      = "api"
        Process   = $process
        Log       = $log
        ErrorLog  = $errorLog
        StartedAt = Get-Date
    }
    $context.Processes.Add($record)
    $healthy = Wait-P8Until -TimeoutSeconds $TimeoutSeconds -IntervalMilliseconds 500 -Description "API health on $Port" -Condition {
        if ($null -ne $process -and $process.HasExited) {
            throw ("API exited with code " + $process.ExitCode + ": " + (Get-P8LogTail -Path $errorLog))
        }
        $probe = Invoke-P8Api -Method "GET" -Path "/api/health" -Token "" -BaseUrl "http://127.0.0.1:$Port" -TimeoutSec 5
        if ($probe.Status -eq 200 -and $null -ne $probe.Json -and $null -ne $probe.Json.data) { return $probe }
        return $null
    }
    if ($null -eq $healthy) {
        $tail = Get-P8LogTail -Path $errorLog
        throw "API did not become healthy on $Port. $tail"
    }
    return $record
}

# ---------------------------------------------------------------------------
# settings
# ---------------------------------------------------------------------------

function Set-P8Setting {
    param(
        [Parameter(Mandatory = $true)][string]$Key,
        $Value
    )
    $path = "/api/admin/settings/" + [uri]::EscapeDataString($Key)
    $response = Invoke-P8Api -Method "PATCH" -Path $path -Body @{ value = $Value }
    if ($response.Status -ne 200) {
        throw "PATCH setting $Key failed with status $($response.Status): $($response.Text)"
    }
    return $response.Json.data
}

function Get-P8Setting {
    param([Parameter(Mandatory = $true)][string]$Key)
    $path = "/api/admin/settings/" + [uri]::EscapeDataString($Key)
    $response = Invoke-P8Api -Method "GET" -Path $path
    if ($response.Status -ne 200) { return $null }
    return $response.Json.data
}

function Get-P8SettingValue {
    param([Parameter(Mandatory = $true)][string]$Key)
    $setting = Get-P8Setting -Key $Key
    if ($null -eq $setting) { return $null }
    return $setting.value
}

# ---------------------------------------------------------------------------
# database clone
# ---------------------------------------------------------------------------

function New-P8Database {
    param(
        [Parameter(Mandatory = $true)][string]$DatabaseName,
        [switch]$DropExisting
    )
    if ($DatabaseName -notmatch '^[A-Za-z0-9_]+$') {
        throw "database name must be [A-Za-z0-9_]+; got '$DatabaseName'"
    }
    if ($DropExisting) {
        $drop = "DROP DATABASE IF EXISTS $DatabaseName WITH (FORCE);"
        Invoke-P8Psql -Sql $drop -Database "postgres" | Out-Null
    }
    $exists = Get-P8Scalar -Sql ("SELECT count(*) FROM pg_database WHERE datname = '" + $DatabaseName + "';") -Database "postgres"
    if ([int]$exists -eq 0) {
        Invoke-P8Psql -Sql "CREATE DATABASE $DatabaseName;" -Database "postgres" | Out-Null
    }
    $verified = Get-P8Scalar -Sql ("SELECT count(*) FROM pg_database WHERE datname = '" + $DatabaseName + "';") -Database "postgres"
    if ([int]$verified -ne 1) {
        throw "database '$DatabaseName' was not created"
    }
    return $DatabaseName
}

function New-P8CloneDatabase {
    param(
        [Parameter(Mandatory = $true)][string]$TemplateDatabase,
        [Parameter(Mandatory = $true)][string]$DatabaseName
    )
    if ($TemplateDatabase -notmatch '^[A-Za-z0-9_]+$' -or $DatabaseName -notmatch '^[A-Za-z0-9_]+$') {
        throw "template/database names must be [A-Za-z0-9_]+"
    }
    $templateExists = Get-P8Scalar -Sql ("SELECT count(*) FROM pg_database WHERE datname = '" + $TemplateDatabase + "';") -Database "postgres"
    if ([int]$templateExists -ne 1) {
        throw "template database '$TemplateDatabase' does not exist; run scripts/p7-acceptance.ps1 first"
    }
    $cloneSql = @"
DROP DATABASE IF EXISTS $DatabaseName WITH (FORCE);
CREATE DATABASE $DatabaseName TEMPLATE $TemplateDatabase;
"@
    Invoke-P8Psql -Sql $cloneSql -Database "postgres" | Out-Null
    $cloned = Get-P8Scalar -Sql ("SELECT count(*) FROM pg_database WHERE datname = '" + $DatabaseName + "';") -Database "postgres"
    if ([int]$cloned -ne 1) {
        throw "clone $TemplateDatabase -> $DatabaseName failed"
    }
    return $DatabaseName
}
