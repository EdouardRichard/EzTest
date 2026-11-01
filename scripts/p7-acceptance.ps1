#requires -Version 5.1
<#
P7 API/DB acceptance (Task 21).

Boots the real P7 stack against a clone of the P6 acceptance database
(eztest_v02_p6 -> eztest_v02_p7), with an isolated copy of the content root,
the standalone PDF renderer (Node/Playwright) and a dedicated executor, then
walks spec section 12.2 scenarios S0-S12 with HTTP + psql assertions and writes
evidence under .tmp/p7-acceptance plus docs/version-V0.2/acceptance/p7.

The script never patches product code. A product defect is captured as a FAIL
with its evidence and the remaining scenarios still run.

Usage:
  pwsh scripts/p7-acceptance.ps1
  pwsh scripts/p7-acceptance.ps1 -SkipDatabaseReset     # reuse an existing clone
  pwsh scripts/p7-acceptance.ps1 -Scenarios S0,S1      # debug a subset
  pwsh scripts/p7-acceptance.ps1 -KeepRunning          # keep API/renderer/executor
#>
[CmdletBinding()]
param(
  [int]$ApiPort = 13333,
  [int]$ApiBPort = 13344,
  [int]$RendererPort = 13337,
  [int]$ExecutorPort = 13345,
  [string]$TemplateDatabase = "eztest_v02_p6",
  [string]$DatabaseName = "eztest_v02_p7",
  [int]$ExpectedMigrations = 40,
  [string[]]$Scenarios = @(),
  [switch]$SkipDatabaseReset,
  [switch]$KeepRunning
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$script:startedAt = Get-Date

$script:repoRoot = Split-Path -Parent $PSScriptRoot
$script:evidence = Join-Path $script:repoRoot ".tmp\p7-acceptance"
$script:docsDir = Join-Path $script:repoRoot "docs\version-V0.2\acceptance\p7"
New-Item -ItemType Directory -Force -Path $script:evidence | Out-Null
New-Item -ItemType Directory -Force -Path $script:docsDir | Out-Null

$script:scenariosPath = Join-Path $script:evidence "scenarios.txt"
$script:logPath = Join-Path $script:evidence "acceptance.log"
$script:failures = [System.Collections.Generic.List[string]]::new()
$script:scenarioResults = [System.Collections.Generic.List[object]]::new()
$script:processes = [System.Collections.Generic.List[object]]::new()
$script:checks = $null
$script:token = ""
$script:baseUrl = "http://127.0.0.1:$ApiPort"
$script:currentApiPort = $ApiPort
$script:apiA = $null
$script:apiB = $null
$script:renderer = $null
$script:executor = $null
$script:apiRestarts = 0
$script:dataRoot = ""
$script:dbUrl = ""
$script:dbName = $DatabaseName
$script:psql = ""
$script:dbHost = ""
$script:dbPort = "5432"
$script:dbUser = ""
$script:dbPassword = ""
$script:kernel = @{}
$script:bugNotes = [System.Collections.Generic.List[string]]::new()
$script:scenarioFilter = @()
if ($Scenarios.Count -gt 0) {
  $script:scenarioFilter = @($Scenarios | ForEach-Object { $_.Split(",") } | ForEach-Object { $_.Trim().ToUpperInvariant() } | Where-Object { $_ -ne "" })
}

Set-Content -Path $script:scenariosPath -Encoding utf8 -Value "# EzTest P7 API/DB acceptance scenarios ($(Get-Date -Format o))"
Set-Content -Path $script:logPath -Encoding utf8 -Value ""

function Write-Log([string]$Message) {
  Write-Host $Message
  Add-Content -Path $script:logPath -Value $Message -Encoding utf8
}
function Write-Evidence([string]$Name, [string]$Content) {
  $path = Join-Path $script:evidence $Name
  Set-Content -Path $path -Value $Content -Encoding utf8
  return $path
}
function New-Checks {
  return , ([System.Collections.Generic.List[object]]::new())
}
function Add-Check($checks, [bool]$Ok, [string]$Label, [string]$Detail = "") {
  $entry = [pscustomobject]@{ Ok = $Ok; Label = $Label; Detail = $Detail }
  $checks.Add($entry)
  $suffix = if ([string]::IsNullOrWhiteSpace($Detail)) { "" } else { " :: $Detail" }
  if ($Ok) {
    Write-Log "  PASS: $Label$suffix"
  } else {
    Write-Log "  FAIL: $Label$suffix"
    $script:failures.Add("$Label$suffix")
  }
}
function Add-BugNote([string]$Note) {
  $script:bugNotes.Add($Note)
  Add-Content -Path (Join-Path $script:evidence "bug-notes.txt") -Value $Note -Encoding utf8
}
function Complete-Scenario([string]$Name, $Checks, [string]$Summary) {
  $pass = $true
  foreach ($check in $Checks) { if (-not $check.Ok) { $pass = $false } }
  $duration = [int]((Get-Date) - $script:scenarioStarted).TotalSeconds
  $line = ("{0} | {1} | {2}s | {3}" -f $Name, $(if ($pass) { "PASS" } else { "FAIL" }), $duration, $Summary)
  Add-Content -Path $script:scenariosPath -Value $line -Encoding utf8
  $script:scenarioResults.Add([pscustomobject]@{
      Name = $Name; Pass = $pass; Checks = $Checks; Summary = $Summary; DurationSeconds = $duration
    })
  Write-Log $line
}
function Invoke-Scenario([string]$Name, [scriptblock]$Body) {
  if ($script:scenarioFilter.Count -gt 0 -and -not ($script:scenarioFilter -contains $Name.ToUpperInvariant())) {
    Write-Log "SKIP $Name (excluded by -Scenarios)"
    return
  }
  $script:scenarioStarted = Get-Date
  $checks = New-Checks
  Write-Log ""
  Write-Log "== Scenario $Name"
  try {
    & $Body $checks
  } catch {
    Add-Check $checks $false ("$Name threw: " + $_.Exception.Message) ""
    Write-Log ("  STACK: " + $_.ScriptStackTrace)
    Add-BugNote ("$Name exception: " + $_.Exception.Message)
  }
  Complete-Scenario $Name $checks ("checks=" + $checks.Count + " failures=" + (@($checks | Where-Object { -not $_.Ok }).Count))
}

# --- environment helpers ---

function Read-DotEnv([string]$Path) {
  $map = @{}
  foreach ($line in Get-Content -LiteralPath $Path) {
    $trim = $line.Trim()
    if ($trim -eq "" -or $trim.StartsWith("#") -or -not $trim.Contains("=")) { continue }
    $parts = $trim.Split("=", 2)
    $map[$parts[0].Trim()] = $parts[1].Trim()
  }
  return $map
}

function Invoke-Psql {
  param([Parameter(Mandatory = $true)][string]$Sql, [switch]$AllowFailure)
  $sqlPath = Join-Path $script:evidence ("sql-" + [guid]::NewGuid().ToString("N") + ".sql")
  Set-Content -Path $sqlPath -Value $Sql -Encoding utf8
  $previous = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  try {
    $out = & $script:psql -h $script:dbHost -p $script:dbPort -U $script:dbUser -d $script:dbName -tA -F "|" -v ON_ERROR_STOP=1 -P pager=off -f $sqlPath 2>&1
    $code = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $previous
    Remove-Item -LiteralPath $sqlPath -Force -ErrorAction SilentlyContinue
  }
  if ($code -ne 0 -and -not $AllowFailure) {
    throw ("psql failed: " + (($out | Out-String).Trim()))
  }
  return @($out | ForEach-Object { [string]$_ } | Where-Object { $_ -ne "" })
}

function Get-Scalar([string]$Sql) {
  $rows = @(Invoke-Psql -Sql $Sql)
  if ($rows.Count -eq 0) { return "" }
  return ([string]$rows[0]).Trim()
}

function SqlText([string]$Value) {
  return $Value.Replace("'", "''")
}

function Wait-SqlValue {
  param([string]$Sql, [string]$Want, [int]$TimeoutSeconds = 120, [int]$IntervalMs = 2000)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $last = ""
  while ((Get-Date) -lt $deadline) {
    $last = Get-Scalar $Sql
    if ($last -eq $Want) { return $true }
    Start-Sleep -Milliseconds $IntervalMs
  }
  $script:lastWaitValue = $last
  return $false
}

function Wait-SqlNonEmpty {
  param([string]$Sql, [int]$TimeoutSeconds = 120, [int]$IntervalMs = 2000)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $last = ""
  while ((Get-Date) -lt $deadline) {
    $last = Get-Scalar $Sql
    if (-not [string]::IsNullOrWhiteSpace($last) -and $last -ne "0") { return $true }
    Start-Sleep -Milliseconds $IntervalMs
  }
  $script:lastWaitValue = $last
  return $false
}

function Invoke-Api {
  param(
    [string]$Method,
    [string]$Path,
    $Body = $null,
    [string]$Token = "",
    [int]$TimeoutSec = 90
  )
  if ([string]::IsNullOrEmpty($Token)) { $Token = $script:token }
  $headers = @{}
  if (-not [string]::IsNullOrWhiteSpace($Token)) { $headers["Authorization"] = "Bearer $Token" }
  $params = @{
    Uri = ($script:baseUrl + $Path)
    Method = $Method
    Headers = $headers
    UseBasicParsing = $true
    TimeoutSec = $TimeoutSec
  }
  if ($null -ne $Body) {
    $json = if ($Body -is [string]) { $Body } else { $Body | ConvertTo-Json -Depth 20 -Compress }
    $params["Body"] = [System.Text.Encoding]::UTF8.GetBytes($json)
    $params["ContentType"] = "application/json; charset=utf-8"
  }
  try {
    $response = Invoke-WebRequest @params
    $json = $null
    if (-not [string]::IsNullOrWhiteSpace($response.Content)) {
      try { $json = $response.Content | ConvertFrom-Json } catch { $json = $null }
    }
    return [pscustomobject]@{
      Status = [int]$response.StatusCode
      Json = $json
      Text = [string]$response.Content
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
      if ([string]::IsNullOrWhiteSpace($text)) {
        try {
          $reader = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
          $text = $reader.ReadToEnd()
        } catch { }
      }
    }
    $json = $null
    try { $json = $text | ConvertFrom-Json } catch { $json = $null }
    return [pscustomobject]@{
      Status = $status
      Json = $json
      Text = $text
      Headers = $null
    }
  }
}

function Invoke-ApiOnPort {
  param([int]$Port, [string]$Method, [string]$Path, $Body = $null, [string]$Token = "", [int]$TimeoutSec = 90)
  $saved = $script:baseUrl
  try {
    $script:baseUrl = "http://127.0.0.1:$Port"
    return Invoke-Api -Method $Method -Path $Path -Body $Body -Token $Token -TimeoutSec $TimeoutSec
  } finally {
    $script:baseUrl = $saved
  }
}

function Wait-ApiHealth {
  param([int]$Port, [int]$TimeoutSeconds = 150)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    try {
      $r = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/api/health" -UseBasicParsing -TimeoutSec 3
      if ([int]$r.StatusCode -eq 200) { return $true }
    } catch { }
    Start-Sleep -Milliseconds 400
  }
  return $false
}

function Wait-Ready {
  param([int]$Port, [int]$Want = 200, [int]$TimeoutSeconds = 60)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $last = 0
  while ((Get-Date) -lt $deadline) {
    try {
      $r = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/api/health/ready" -UseBasicParsing -TimeoutSec 5
      $last = [int]$r.StatusCode
    } catch {
      if ($null -ne $_.Exception.Response) {
        try { $last = [int]$_.Exception.Response.StatusCode } catch { $last = 0 }
      } else {
        $last = 0
      }
    }
    if ($last -eq $Want) {
      $script:lastWaitValue = [string]$last
      return $true
    }
    Start-Sleep -Milliseconds 500
  }
  $script:lastWaitValue = [string]$last
  return $false
}

function Wait-PortClosed {
  param([int]$Port, [int]$TimeoutSeconds = 15)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    $conn = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
    if (-not $conn) { return $true }
    Start-Sleep -Milliseconds 300
  }
  return $false
}

function Stop-PortProcesses {
  param([int]$Port)
  try {
    $connections = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
    foreach ($connection in $connections) {
      Stop-Process -Id $connection.OwningProcess -Force -ErrorAction SilentlyContinue
    }
  } catch { }
}

function Stop-OwnedProcess {
  param($Process)
  if ($null -eq $Process) { return }
  try {
    if (-not $Process.HasExited) {
      Stop-Process -Id $Process.Id -Force -ErrorAction SilentlyContinue
    }
  } catch { }
}

function Wait-HttpOk {
  param([string]$Url, [int]$TimeoutSeconds = 60)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    try {
      $r = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 5
      if ([int]$r.StatusCode -eq 200) { return $true }
    } catch { }
    Start-Sleep -Milliseconds 500
  }
  return $false
}

function Start-ApiA {
  $index = $script:apiRestarts
  $script:apiRestarts++
  $log = Join-Path $script:evidence ("api-{0}.log" -f $index)
  $err = Join-Path $script:evidence ("api-{0}.err.log" -f $index)
  $env:EZTEST_DATABASE_URL = $script:dbUrl
  $env:EZTEST_API_PORT = [string]$ApiPort
  $env:EZTEST_WEB_PORT = "6868"
  $env:EZTEST_EXECUTOR_PORT = [string]$ExecutorPort
  $binary = Join-Path $script:evidence "eztest-p7.exe"
  $process = Start-Process -FilePath $binary -PassThru -NoNewWindow -RedirectStandardOutput $log -RedirectStandardError $err
  $script:apiA = $process
  $script:processes.Add([pscustomobject]@{ Kind = "api-a"; Process = $process; Log = $log; ErrorLog = $err })
  if (-not (Wait-ApiHealth -Port $ApiPort -TimeoutSeconds 150)) {
    $tail = ""
    if (Test-Path $err) { $tail = (Get-Content $err -Tail 40 | Out-String) }
    throw "API A did not become healthy on $ApiPort. stderr: $tail"
  }
  return $process
}

function Restart-ApiA {
  if ($null -ne $script:apiA) {
    Stop-OwnedProcess $script:apiA
    $null = Wait-PortClosed -Port $ApiPort -TimeoutSeconds 20
  }
  Start-Sleep -Milliseconds 500
  $null = Start-ApiA
  $script:baseUrl = "http://127.0.0.1:$ApiPort"
  $script:currentApiPort = $ApiPort
  $script:token = Login-Admin
  if ([string]::IsNullOrWhiteSpace($script:token)) { throw "admin login failed after API restart" }
}

function Login-Admin {
  $login = Invoke-ApiOnPort -Port $script:currentApiPort -Method "POST" -Path "/api/auth/login" -Body @{ username = "admin"; password = "admin123" } -Token "" -TimeoutSec 30
  if ($login.Status -ne 200 -or $null -eq $login.Json -or $null -eq $login.Json.data) {
    return ""
  }
  return [string]$login.Json.data.token
}

function Set-Setting {
  param([string]$Key, $Value)
  $path = "/api/admin/settings/" + [uri]::EscapeDataString($Key)
  $resp = Invoke-Api -Method "PATCH" -Path $path -Body @{ value = $Value }
  if ($resp.Status -ne 200) {
    throw "PATCH setting $Key failed with status $($resp.Status): $($resp.Text)"
  }
  return $resp.Json.data
}

function Get-SettingValue([string]$Key) {
  $resp = Invoke-Api -Method "GET" -Path ("/api/admin/settings/" + [uri]::EscapeDataString($Key))
  if ($resp.Status -ne 200) { return $null }
  return $resp.Json.data.value
}

function Download-ApiFile {
  param([string]$Path, [string]$OutFile, [int]$TimeoutSec = 120)
  $headers = @{ Authorization = "Bearer $script:token" }
  try {
    $resp = Invoke-WebRequest -Uri ($script:baseUrl + $Path) -Headers $headers -UseBasicParsing -OutFile $OutFile -TimeoutSec $TimeoutSec
    if ($null -ne $resp -and $null -ne $resp.StatusCode) { return [int]$resp.StatusCode }
    if (Test-Path -LiteralPath $OutFile) { return 200 }
    return 0
  } catch {
    if ($null -ne $_.Exception.Response) {
      try { return [int]$_.Exception.Response.StatusCode } catch { return 0 }
    }
    return 0
  }
}

function Get-FileSha([string]$Path) {
  return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function New-Hex64 {
  $bytes = [System.Security.Cryptography.SHA256]::Create().ComputeHash([System.Text.Encoding]::UTF8.GetBytes(([guid]::NewGuid().ToString("N") + [guid]::NewGuid().ToString("N"))))
  return (($bytes | ForEach-Object { $_.ToString("x2") }) -join "")
}

function New-Hex32 {
  return (New-Hex64).Substring(0, 32)
}

function Write-DataFile {
  param([string]$Sha, [int]$Size, [datetime]$LocalMtime)
  $relative = "objects/{0}/{1}/{2}" -f $Sha.Substring(0, 2), $Sha.Substring(2, 2), $Sha
  $full = Join-Path $script:dataRoot ($relative -replace "/", "\")
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $full) | Out-Null
  $bytes = New-Object byte[] $Size
  for ($i = 0; $i -lt $Size; $i++) { $bytes[$i] = [byte](($i * 31 + $Size) % 251) }
  [System.IO.File]::WriteAllBytes($full, $bytes)
  [System.IO.File]::SetLastWriteTime($full, $LocalMtime)
  return [pscustomobject]@{ RelativePath = $relative; FullPath = $full; Size = $Size }
}

function Start-ClarificationRun {
  param([string]$VersionID, [string]$Title)
  $before = [int64](Get-Scalar "SELECT coalesce(max(id),0) FROM context_packet;")
  $conv = Invoke-Api -Method "POST" -Path ("/api/versions/$VersionID/conversations") -Body @{ kind = "clarification"; title = $Title }
  if ($conv.Status -ne 201) { throw "create conversation failed: $($conv.Status) $($conv.Text)" }
  $conversationID = [string]$conv.Json.data.id
  $body = @{ parts = @(@{ type = "text"; text = "P7 acceptance probe $Title. Reply in one short sentence and do not call tools." }) }
  $message = Invoke-Api -Method "POST" -Path ("/api/versions/$VersionID/conversations/$conversationID/messages") -Body $body -TimeoutSec 60
  if ($message.Status -ne 201) { throw "post clarification message failed: $($message.Status) $($message.Text)" }
  return [pscustomobject]@{ ConversationID = $conversationID; PacketBefore = $before }
}

function Wait-ContextPacket {
  param([string]$VersionID, [int64]$AfterID, [string]$Needle = "", [int]$TimeoutSeconds = 120)
  $like = ""
  if (-not [string]::IsNullOrWhiteSpace($Needle)) {
    $like = " AND sections::text LIKE '%" + (SqlText $Needle) + "%'"
  }
  $sql = "SELECT id || '|' || run_id FROM context_packet WHERE version_id = $VersionID AND id > $AfterID$like ORDER BY id ASC LIMIT 1;"
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $last = ""
  while ((Get-Date) -lt $deadline) {
    $last = Get-Scalar $sql
    if (-not [string]::IsNullOrWhiteSpace($last)) {
      $parts = $last.Split("|")
      return [pscustomobject]@{ PacketID = $parts[0]; RunID = $parts[1] }
    }
    Start-Sleep -Milliseconds 2000
  }
  $script:lastWaitValue = $last
  return $null
}

function Get-PacketHasText([int64]$PacketID, [string]$Needle) {
  $count = [int](Get-Scalar ("SELECT count(*) FROM context_packet WHERE id = $PacketID AND sections::text LIKE '%" + (SqlText $Needle) + "%';"))
  return ($count -gt 0)
}

function Get-DeadRenderJobError {
  param([string]$ReportID, [int]$Revision)
  $dedupe = "report:${ReportID}:rev:$Revision"
  return Get-Scalar ("SELECT last_error FROM work_item WHERE type = 'report.render_pdf' AND dedupe_key = '" + (SqlText $dedupe) + "' AND status = 'dead' ORDER BY id DESC LIMIT 1;")
}

function Get-RevisionRow {
  param([int64]$RevisionID)
  $line = Get-Scalar ("SELECT rv.report_id || '|' || r.version_id || '|' || rv.revision || '|' || rv.status || '|' || rv.render_status || '|' || coalesce(rv.pdf_status,'') || '|' || coalesce(rv.as_of::text,'') || '|' || coalesce(rv.html_ref::text,'') || '|' || rv.input_fingerprint || '|' || rv.template_version || '|' || coalesce(rv.pdf_ref::text,'') || '|' || coalesce(rv.pdf_error,'') FROM delivery_report_revision rv JOIN delivery_report r ON r.id = rv.report_id WHERE rv.id = $RevisionID;")
  if ([string]::IsNullOrWhiteSpace($line)) { return $null }
  $p = $line.Split("|")
  return [pscustomobject]@{
    ReportID = $p[0]; VersionID = $p[1]; Revision = $p[2]; Status = $p[3]; RenderStatus = $p[4]
    PDFStatus = $p[5]; AsOf = $p[6]; HTMLRef = $p[7]; Fingerprint = $p[8]; TemplateVersion = $p[9]; PDFRef = $p[10]; PDFError = $p[11]
  }
}

function Start-Sse {
  param([int]$Port, [string]$Channel, [string]$OutFile, [string]$LastEventId = "")
  if (Test-Path $OutFile) { Remove-Item -LiteralPath $OutFile -Force -ErrorAction SilentlyContinue }
  $url = "http://127.0.0.1:$Port/api/stream?channel=" + [uri]::EscapeDataString($Channel)
  $argString = "-s -N --max-time 30 -H `"Authorization: Bearer $script:token`" -H `"Accept: text/event-stream`""
  if (-not [string]::IsNullOrWhiteSpace($LastEventId)) {
    $argString += " -H `"Last-Event-ID: $LastEventId`""
  }
  $argString += " $url"
  $errFile = $OutFile + ".err"
  return Start-Process -FilePath "curl.exe" -ArgumentList $argString -PassThru -NoNewWindow -RedirectStandardOutput $OutFile -RedirectStandardError $errFile
}

# --- boot phase ---

function Build-ApiBinary {
  Write-Log "==> Building the P7 API binary"
  $binary = Join-Path $script:evidence "eztest-p7.exe"
  Push-Location (Join-Path $script:repoRoot "backend")
  $previous = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  try {
    & $script:go build -o $binary ./cmd/eztest 2>&1 | ForEach-Object { Write-Log $_ }
    $code = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $previous
    Pop-Location
  }
  if ($code -ne 0) { throw "go build failed with exit code $code" }
}

function Reset-Database {
  Write-Log "==> Cloning $TemplateDatabase -> $DatabaseName"
  $dropSql = Join-Path $script:evidence "clone-drop.sql"
  Set-Content -Path $dropSql -Value "DROP DATABASE IF EXISTS $DatabaseName WITH (FORCE); CREATE DATABASE $DatabaseName TEMPLATE $TemplateDatabase;" -Encoding utf8
  $previous = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  try {
    $out = & $script:psql -h $script:dbHost -p $script:dbPort -U $script:dbUser -d postgres -tA -v ON_ERROR_STOP=1 -f $dropSql 2>&1
    $code = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $previous
    Remove-Item -LiteralPath $dropSql -Force -ErrorAction SilentlyContinue
  }
  if ($code -ne 0) { throw ("clone failed: " + (($out | Out-String).Trim())) }
}

function Prepare-DataRoot {
  Write-Log "==> Preparing the isolated content root"
  $source = Join-Path $script:repoRoot ".data"
  $script:dataRoot = Join-Path $script:evidence "data"
  if (-not $SkipDatabaseReset -and (Test-Path $script:dataRoot)) {
    Remove-Item -LiteralPath $script:dataRoot -Recurse -Force
  }
  if (-not (Test-Path $script:dataRoot)) {
    Copy-Item -LiteralPath $source -Destination $script:dataRoot -Recurse -Force
  }
  # Remove ledger files that do not belong to the clone database so the GC scan
  # starts from a deterministic, isolated ledger (the real .data tree is shared
  # by several development databases).
  $known = @(Invoke-Psql -Sql "SELECT sha256 FROM content_object;")
  $knownSet = New-Object 'System.Collections.Generic.HashSet[string]'
  foreach ($sha in $known) { if (-not [string]::IsNullOrWhiteSpace($sha)) { $null = $knownSet.Add($sha.Trim()) } }
  $objectsDir = Join-Path $script:dataRoot "objects"
  if (Test-Path $objectsDir) {
    Get-ChildItem -LiteralPath $objectsDir -Recurse -File | ForEach-Object {
      if (-not $knownSet.Contains($_.Name)) { Remove-Item -LiteralPath $_.FullName -Force }
    }
  }
  $root = $script:dataRoot.Replace("\", "/")
  $settings = @(
    @("paths.data_root", $root),
    @("paths.attachment_dir", "$root/attachments"),
    @("paths.evidence_dir", "$root/evidence"),
    @("paths.report_dir", "$root/reports"),
    @("paths.export_dir", "$root/exports")
  )
  $sql = ""
  foreach ($pair in $settings) {
    $sql += "UPDATE system_setting SET value_json = to_jsonb('" + (SqlText $pair[1]) + "'::text), updated_at = now() WHERE key = '" + $pair[0] + "';`n"
  }
  $null = Invoke-Psql -Sql $sql
  Write-Log "    content root: $script:dataRoot ($($knownSet.Count) ledger objects)"
}

function Start-Renderer {
  $token = (Get-Scalar "SELECT value_json #>> '{}' FROM system_setting WHERE key = 'report.render_token';").Trim()
  if ([string]::IsNullOrWhiteSpace($token)) { throw "report.render_token is missing" }
  $env:EZTEST_RENDER_TOKEN = $token
  $env:EZTEST_RENDERER_ADDR = "127.0.0.1:$RendererPort"
  $env:EZTEST_RENDER_TIMEOUT_SECONDS = "60"
  $log = Join-Path $script:evidence "renderer.log"
  $err = Join-Path $script:evidence "renderer.err.log"
  $process = Start-Process -FilePath "node.exe" -ArgumentList @("renderer/src/server.mjs") -WorkingDirectory $script:repoRoot -PassThru -NoNewWindow -RedirectStandardOutput $log -RedirectStandardError $err
  $script:renderer = $process
  $script:processes.Add([pscustomobject]@{ Kind = "renderer"; Process = $process; Log = $log; ErrorLog = $err })
  if (-not (Wait-HttpOk -Url "http://127.0.0.1:$RendererPort/healthz" -TimeoutSeconds 120)) {
    throw "renderer did not become healthy on $RendererPort"
  }
  return $process
}

function Start-Executor {
  $token = (Get-Scalar "SELECT value_json #>> '{}' FROM system_setting WHERE key = 'execution.executor_token';").Trim()
  if ([string]::IsNullOrWhiteSpace($token)) { throw "execution.executor_token is missing" }
  $env:EZTEST_EXECUTOR_TOKEN = $token
  $env:EZTEST_EXECUTOR_ADDR = "127.0.0.1:$ExecutorPort"
  $env:EZTEST_EXECUTOR_MAX_CONTEXTS = "8"
  $env:EZTEST_EXECUTOR_HEARTBEAT_SECONDS = "2"
  $env:EZTEST_API_URL = "http://127.0.0.1:$ApiPort"
  $log = Join-Path $script:evidence "executor.log"
  $err = Join-Path $script:evidence "executor.err.log"
  $process = Start-Process -FilePath "node.exe" -ArgumentList @("executor/src/server.mjs") -WorkingDirectory $script:repoRoot -PassThru -NoNewWindow -RedirectStandardOutput $log -RedirectStandardError $err
  $script:executor = $process
  $script:processes.Add([pscustomobject]@{ Kind = "executor"; Process = $process; Log = $log; ErrorLog = $err })
  if (-not (Wait-HttpOk -Url "http://127.0.0.1:$ExecutorPort/healthz" -TimeoutSeconds 90)) {
    throw "executor did not become healthy on $ExecutorPort"
  }
  $endpoint = "http://127.0.0.1:$ExecutorPort"
  $registered = Wait-SqlNonEmpty -Sql "SELECT count(*) FROM executor_instance WHERE endpoint = '$endpoint';" -TimeoutSeconds 60 -IntervalMs 1000
  if (-not $registered) { throw "executor push registration was not observed in executor_instance" }
  return $process
}

function Start-ApiB {
  $env:EZTEST_DATABASE_URL = $script:dbUrl
  $env:EZTEST_API_PORT = [string]$ApiBPort
  $env:EZTEST_WEB_PORT = "6868"
  $env:EZTEST_EXECUTOR_PORT = [string]$ExecutorPort
  $log = Join-Path $script:evidence "api-b.log"
  $err = Join-Path $script:evidence "api-b.err.log"
  $process = Start-Process -FilePath (Join-Path $script:evidence "eztest-p7.exe") -PassThru -NoNewWindow -RedirectStandardOutput $log -RedirectStandardError $err
  $script:apiB = $process
  $script:processes.Add([pscustomobject]@{ Kind = "api-b"; Process = $process; Log = $log; ErrorLog = $err })
  if (-not (Wait-ApiHealth -Port $ApiBPort -TimeoutSeconds 150)) { throw "API B did not become healthy on $ApiBPort" }
  $null = Wait-Ready -Port $ApiBPort -Want 200 -TimeoutSeconds 60
  return $process
}

function Initialize-Environment {
  Write-Log "==> Preflight"
  $dotenvPath = Join-Path $script:repoRoot ".env"
  if (-not (Test-Path -LiteralPath $dotenvPath)) { throw "missing .env at $dotenvPath" }
  $dotenv = Read-DotEnv $dotenvPath
  $required = @("EZTEST_DATABASE_URL", "llm_api_key", "llm_base_url", "llm_model_name")
  foreach ($key in $required) {
    if (-not $dotenv.ContainsKey($key) -or [string]::IsNullOrWhiteSpace($dotenv[$key])) { throw "missing $key in .env" }
  }
  $pattern = '^(?<scheme>postgres(?:ql)?)://(?<user>[^:/@]+)(?::(?<password>[^@]*))?@(?<host>[^:/?]+)(?::(?<port>\d+))?/(?<database>[^?]+)(?<query>\?.*)?$'
  $m = [regex]::Match($dotenv["EZTEST_DATABASE_URL"], $pattern)
  if (-not $m.Success) { throw "EZTEST_DATABASE_URL is not a supported postgres URL" }
  $script:dbUser = [uri]::UnescapeDataString($m.Groups["user"].Value)
  $script:dbPassword = [uri]::UnescapeDataString($m.Groups["password"].Value)
  $script:dbHost = $m.Groups["host"].Value
  $script:dbPort = if ($m.Groups["port"].Success) { $m.Groups["port"].Value } else { "5432" }
  $query = if ($m.Groups["query"].Success) { $m.Groups["query"].Value } else { "" }
  $userInfo = [uri]::EscapeDataString($script:dbUser)
  if ($script:dbPassword -ne "") { $userInfo += ":" + [uri]::EscapeDataString($script:dbPassword) }
  $script:dbUrl = "{0}://{1}@{2}:{3}/{4}{5}" -f $m.Groups["scheme"].Value, $userInfo, $script:dbHost, $script:dbPort, $DatabaseName, $query

  $env:PGPASSWORD = $script:dbPassword
  $env:PGCLIENTENCODING = "UTF8"
  $script:psql = Join-Path $script:repoRoot ".tools\pgsql\bin\psql.exe"
  if (-not (Test-Path $script:psql)) { throw "psql missing at $script:psql" }
  $script:go = Join-Path $script:repoRoot ".tools\go\bin\go.exe"
  if (-not (Test-Path $script:go)) { $script:go = "go" }
  if (-not (Get-Command node.exe -ErrorAction SilentlyContinue)) { throw "node.exe not found" }
  if (-not (Get-Command curl.exe -ErrorAction SilentlyContinue)) { throw "curl.exe not found" }
  foreach ($port in @($ApiPort, $ApiBPort, $RendererPort, $ExecutorPort)) {
    Stop-PortProcesses -Port $port
    $null = Wait-PortClosed -Port $port -TimeoutSeconds 10
  }
  Write-Log "    database clone: $DatabaseName (from $TemplateDatabase)"
  Write-Log "    api=$ApiPort api-b=$ApiBPort renderer=$RendererPort executor=$ExecutorPort"
}

function Configure-P7Settings {
  Write-Log "==> Configuring P7 settings"
  $settings = @(
    @("report.template_version", "2"),
    @("report.section_appendix", $true),
    @("report.render_pdf", $true),
    @("report.render_worker_url", "http://127.0.0.1:$RendererPort"),
    @("report.render_max_attempts", 3),
    @("knowledge.review_required", $true),
    @("knowledge.injection_max_items", 5),
    @("knowledge.injection_budget_tokens", 4000),
    @("retention.gc_dry_run", $true),
    @("instance.heartbeat_seconds", 10),
    @("instance.offline_seconds", 10),
    @("execution.executor_url", "http://127.0.0.1:$ExecutorPort"),
    @("execution.executor_urls", "http://127.0.0.1:$ExecutorPort"),
    @("execution.executor_heartbeat_seconds", 2),
    @("execution.executor_offline_seconds", 5)
  )
  foreach ($pair in $settings) {
    $null = Set-Setting -Key $pair[0] -Value $pair[1]
  }
  $settingsCount = (Invoke-Api -Method "GET" -Path "/api/admin/settings").Json.data.Count
  if ($settingsCount -ne 83) { throw "expected 83 settings after P7 seeding, got $settingsCount" }
  Write-Log "    settings=83, template_version=2, render worker -> $RendererPort"
}

function Configure-SutFamilies {
  Write-Log "==> Setting SUT families"
  $suts = (Invoke-Api -Method "GET" -Path "/api/resources/suts").Json.data
  $order = $suts | Where-Object { $_.name -eq "P4 Order Center" } | Select-Object -First 1
  $payment = $suts | Where-Object { $_.name -eq "P4 Payment Gateway" } | Select-Object -First 1
  if ($null -eq $order -or $null -eq $payment) { throw "expected P4 Order Center and P4 Payment Gateway SUTs" }
  $null = Invoke-Api -Method "PATCH" -Path ("/api/resources/suts/" + $order.id) -Body @{ family = "p4-orders" }
  $null = Invoke-Api -Method "PATCH" -Path ("/api/resources/suts/" + $payment.id) -Body @{ family = "p4-payments" }
  $script:orderSutID = [int64]$order.id
  $script:paymentSutID = [int64]$payment.id
  $checkOrder = (Invoke-Api -Method "GET" -Path ("/api/resources/suts/" + $order.id)).Json.data.family
  $checkPayment = (Invoke-Api -Method "GET" -Path ("/api/resources/suts/" + $payment.id)).Json.data.family
  if ($checkOrder -ne "p4-orders" -or $checkPayment -ne "p4-payments") { throw "SUT family update did not persist" }
  Write-Log "    P4 Order Center -> p4-orders, P4 Payment Gateway -> p4-payments"
}

function Resolve-Baseline {
  Write-Log "==> Resolving baseline IDs"
  $versions = (Invoke-Api -Method "GET" -Path "/api/versions?limit=100").Json.data
  $running = $versions | Where-Object { $_.status -eq "running" } | Select-Object -First 1
  $completed = $versions | Where-Object { $_.status -eq "completed" } | Select-Object -First 1
  if ($null -eq $running -or $null -eq $completed) { throw "expected one running and one completed baseline version" }
  $script:v1 = [string]$running.id
  $script:v2 = [string]$completed.id
  $script:issueSource = [int64](Get-Scalar "SELECT id FROM delivery_issue WHERE version_id = $($script:v1) AND status = 'merged' ORDER BY id LIMIT 1;")
  if ($script:issueSource -le 0) { throw "expected a merged baseline issue in the running version" }
  $script:issueSourceKey = Get-Scalar "SELECT dedupe_key FROM delivery_issue WHERE id = $($script:issueSource);"
  $script:issueTarget = [int64](Get-Scalar "SELECT id FROM delivery_issue WHERE version_id = $($script:v1) AND status <> 'merged' ORDER BY id LIMIT 1;")
  if ($script:issueTarget -le 0) { throw "expected a non-merged baseline issue in the running version" }
  Write-Log "    v1=$($script:v1) v2=$($script:v2) mergedIssue=$($script:issueSource) targetIssue=$($script:issueTarget)"
}

# --- scenarios ---

function Scenario-S0($checks) {
  $migrations = [int](Get-Scalar "SELECT count(*) FROM schema_migrations;")
  Add-Check $checks ($migrations -eq $ExpectedMigrations) "migrations=$ExpectedMigrations" "got $migrations"
  $settings = Invoke-Api -Method "GET" -Path "/api/admin/settings"
  Add-Check $checks ($settings.Status -eq 200 -and $settings.Json.data.Count -eq 83) "settings=83" ("status=" + $settings.Status + " count=" + $(if ($null -ne $settings.Json) { $settings.Json.data.Count } else { "n/a" }))
  $rendererStatus = 0
  $rendererBody = $null
  try {
    $rendererHealth = Invoke-WebRequest -Uri "http://127.0.0.1:$RendererPort/healthz" -UseBasicParsing -TimeoutSec 60
    $rendererStatus = [int]$rendererHealth.StatusCode
    $rendererBody = $rendererHealth.Content | ConvertFrom-Json
  } catch {
    if ($null -ne $_.Exception.Response) {
      try { $rendererStatus = [int]$_.Exception.Response.StatusCode } catch { $rendererStatus = 0 }
    }
  }
  Add-Check $checks ($rendererStatus -eq 200 -and $null -ne $rendererBody -and $rendererBody.status -eq "ok") "renderer health=200" ("browser=" + $(if ($rendererBody) { $rendererBody.browser } else { "n/a" }))
  $ready = Invoke-Api -Method "GET" -Path "/api/health/ready" -Token ""
  Add-Check $checks ($ready.Status -eq 200 -and $ready.Json.data.status -eq "ok" -and $ready.Json.data.database -eq "ok" -and $ready.Json.data.storage -eq "ok") "api ready=200" ("status=" + $ready.Status + " db=" + $(if ($ready.Json) { $ready.Json.data.database } else { "n/a" }))
  $llm = Invoke-Api -Method "GET" -Path "/api/admin/llm/configs"
  $llmOK = $false
  if ($llm.Status -eq 200 -and $llm.Json.data.Count -gt 0) {
    $llmID = [string]$llm.Json.data[0].id
    $test = Invoke-Api -Method "POST" -Path ("/api/admin/llm/configs/$llmID/test") -Body @{} -TimeoutSec 60
    $llmOK = ($test.Status -eq 200)
  }
  Add-Check $checks $llmOK "real LLM config reachable"
  $instances = Invoke-Api -Method "GET" -Path "/api/admin/instances"
  $current = @($instances.Json.data | Where-Object { [int]$_.api_port -eq $ApiPort -and [int]$_.pid -eq $script:apiA.Id })
  Add-Check $checks ($current.Count -eq 1 -and $current[0].status -eq "active") "instance registered active" ("node_id=" + $(if ($current.Count -gt 0) { $current[0].node_id } else { "?" }))
}

function Scenario-S1($checks) {
  $line = Get-Scalar "SELECT rv.id || '|' || rv.report_id || '|' || r.version_id || '|' || rv.revision FROM delivery_report_revision rv JOIN delivery_report r ON r.id = rv.report_id WHERE rv.status = 'published' AND rv.render_status = 'done' AND rv.template_version = '1' ORDER BY rv.id ASC LIMIT 1;"
  if ([string]::IsNullOrWhiteSpace($line)) { throw "no published v1 revision found" }
  $p = $line.Split("|")
  $revisionID = [int64]$p[0]
  $reportID = $p[1]
  $versionID = $p[2]
  $revisionNumber = [int]$p[3]
  Write-Log "    selected revision $revisionID (report $reportID rev $revisionNumber version $versionID)"
  $before = Get-RevisionRow -RevisionID $revisionID
  $htmlBefore = Join-Path $script:evidence "s1-html-before.html"
  $htmlStatus = Download-ApiFile -Path "/api/versions/$versionID/reports/$reportID/revisions/$revisionNumber/html" -OutFile $htmlBefore
  $htmlShaBefore = if ($htmlStatus -eq 200) { Get-FileSha $htmlBefore } else { "" }
  Add-Check $checks ($htmlStatus -eq 200) "revision HTML download=200" "status=$htmlStatus sha=$htmlShaBefore"

  $deadline = (Get-Date).AddSeconds(180)
  $finalStatus = ""
  $deadError = ""
  while ((Get-Date) -lt $deadline) {
    $row = Get-RevisionRow -RevisionID $revisionID
    $finalStatus = $row.PDFStatus
    if ($finalStatus -eq "done" -or $finalStatus -eq "failed") { break }
    $deadError = Get-DeadRenderJobError -ReportID $reportID -Revision $revisionNumber
    if (-not [string]::IsNullOrWhiteSpace($deadError)) { break }
    Start-Sleep -Seconds 3
  }
  if (-not [string]::IsNullOrWhiteSpace($deadError)) {
    Add-Check $checks $false "report PDF generated" ("render work item dead: " + $deadError)
    Add-BugNote ("S1 blocked: report.render_pdf work item for revision $revisionID was claimed and killed before the dedicated render worker could process it: " + $deadError)
    $allDead = Get-Scalar "SELECT count(*) || '|' || coalesce(min(last_error),'') FROM work_item WHERE type='report.render_pdf' AND status='dead';"
    Add-BugNote ("S1 evidence: report.render_pdf dead work items (count|error) = " + $allDead)
  } else {
    Add-Check $checks ($finalStatus -eq "done") "revision pdf_status=done" "got $finalStatus"
  }

  $revisionView = Invoke-Api -Method "GET" -Path "/api/versions/$versionID/reports/$reportID/revisions/$revisionNumber"
  Add-Check $checks ($revisionView.Status -eq 200 -and $revisionView.Json.data.pdf_available -eq $true) "pdf_available=true" ("status=" + $revisionView.Status + " pdf_status=" + $(if ($revisionView.Json) { $revisionView.Json.data.pdf_status } else { "n/a" }))

  if ($finalStatus -eq "done") {
    $pdfPath = Join-Path $script:evidence "s1-report.pdf"
    $pdfStatus = Download-ApiFile -Path "/api/versions/$versionID/reports/$reportID/revisions/$revisionNumber/pdf" -OutFile $pdfPath
    Add-Check $checks ($pdfStatus -eq 200) "PDF download=200" "status=$pdfStatus"
    if ($pdfStatus -eq 200) {
      $bytes = [System.IO.File]::ReadAllBytes($pdfPath)
      $magic = if ($bytes.Length -ge 5) { [System.Text.Encoding]::ASCII.GetString($bytes, 0, 5) } else { "" }
      Add-Check $checks ($magic -eq "%PDF-") "PDF magic=%PDF-" ("magic=$magic")
      Add-Check $checks ($bytes.Length -gt 1000) "PDF size" ("bytes=" + $bytes.Length)
      $expectedSha = Get-Scalar "SELECT o.sha256 FROM delivery_report_revision rv JOIN content_ref r ON r.id = rv.pdf_ref JOIN content_object o ON o.sha256 = r.object_id WHERE rv.id = $revisionID;"
      $expectedSize = Get-Scalar "SELECT o.size FROM delivery_report_revision rv JOIN content_ref r ON r.id = rv.pdf_ref JOIN content_object o ON o.sha256 = r.object_id WHERE rv.id = $revisionID;"
      $actualSha = Get-FileSha $pdfPath
      Add-Check $checks ($actualSha -eq $expectedSha) "PDF sha matches ledger" "sha=$actualSha"
      Add-Check $checks ([int64]$expectedSize -eq $bytes.Length) "PDF size matches ledger" "ledger=$expectedSize"
    }
  } else {
    Add-Check $checks $false "PDF download=200" "skipped: pdf_status=$finalStatus"
    Add-Check $checks $false "PDF magic=%PDF-" "skipped"
    Add-Check $checks $false "PDF sha matches ledger" "skipped"
  }

  $after = Get-RevisionRow -RevisionID $revisionID
  Add-Check $checks ($after.AsOf -eq $before.AsOf) "as_of unchanged" ($before.AsOf + " -> " + $after.AsOf)
  Add-Check $checks ($after.HTMLRef -eq $before.HTMLRef) "html_ref unchanged" ($before.HTMLRef + " -> " + $after.HTMLRef)
  Add-Check $checks ($after.Fingerprint -eq $before.Fingerprint) "input_fingerprint unchanged"
  Add-Check $checks ($after.TemplateVersion -eq $before.TemplateVersion) "template_version unchanged" ($before.TemplateVersion + " -> " + $after.TemplateVersion)
  $htmlAfter = Join-Path $script:evidence "s1-html-after.html"
  $htmlStatusAfter = Download-ApiFile -Path "/api/versions/$versionID/reports/$reportID/revisions/$revisionNumber/html" -OutFile $htmlAfter
  $htmlShaAfter = if ($htmlStatusAfter -eq 200) { Get-FileSha $htmlAfter } else { "" }
  Add-Check $checks ($htmlStatusAfter -eq 200 -and $htmlShaAfter -eq $htmlShaBefore) "HTML bytes unchanged" "sha=$htmlShaBefore"
}

function Scenario-S3($checks) {
  $reportID = Get-Scalar "SELECT id FROM delivery_report WHERE version_id = $($script:v2) AND kind = 'version' ORDER BY id DESC LIMIT 1;"
  if ([string]::IsNullOrWhiteSpace($reportID)) { throw "no version report on v2" }
  $latest = [int](Get-Scalar "SELECT max(revision) FROM delivery_report_revision WHERE report_id = $reportID AND status = 'published';")
  $rev1 = Get-Scalar "SELECT id FROM delivery_report_revision WHERE report_id = $reportID AND revision = 1;"
  $revLatestID = Get-Scalar "SELECT id FROM delivery_report_revision WHERE report_id = $reportID AND revision = $latest;"
  $rev1Row = Get-RevisionRow -RevisionID ([int64]$rev1)
  $html1Before = Join-Path $script:evidence "s3-rev1-before.html"
  $null = Download-ApiFile -Path "/api/versions/$($script:v2)/reports/$reportID/revisions/1/html" -OutFile $html1Before
  $sha1Before = Get-FileSha $html1Before
  $htmlLatestBefore = Join-Path $script:evidence "s3-rev$latest-before.html"
  $null = Download-ApiFile -Path "/api/versions/$($script:v2)/reports/$reportID/revisions/$latest/html" -OutFile $htmlLatestBefore
  $shaLatestBefore = Get-FileSha $htmlLatestBefore
  Add-Check $checks ($rev1Row.TemplateVersion -eq "1") "old revision template_version=1" "rev1=$($rev1Row.TemplateVersion)"

  # ON: appendix must be present in a new v2 revision.
  $regen = Invoke-Api -Method "POST" -Path "/api/versions/$($script:v2)/reports/$reportID/regenerate" -Body @{} -TimeoutSec 60
  Add-Check $checks ($regen.Status -eq 202) "regenerate (appendix on) accepted" "status=$($regen.Status) rev=$(if ($regen.Json) { $regen.Json.data.revision } else { '?' })"
  $newRev = [int]$regen.Json.data.revision
  $newRevID = Get-Scalar "SELECT id FROM delivery_report_revision WHERE report_id = $reportID AND revision = $newRev;"
  $published = Wait-SqlValue -Sql "SELECT status FROM delivery_report_revision WHERE id = $newRevID;" -Want "published" -TimeoutSeconds 150 -IntervalMs 2000
  Add-Check $checks $published "new revision published (appendix on)" "revision=$newRev"
  $onRow = Get-RevisionRow -RevisionID ([int64]$newRevID)
  Add-Check $checks ($onRow.TemplateVersion -eq "2") "new revision template_version=2" "got $($onRow.TemplateVersion)"
  $onHtml = Join-Path $script:evidence "s3-appendix-on.html"
  $onStatus = Download-ApiFile -Path "/api/versions/$($script:v2)/reports/$reportID/revisions/$newRev/html" -OutFile $onHtml
  $onText = if ($onStatus -eq 200) { Get-Content $onHtml -Raw } else { "" }
  Add-Check $checks ($onStatus -eq 200 -and $onText.Contains('id="appendix"') -and $onText.Contains('data-template-version="2"')) "appendix section present" ("status=$onStatus appendix=" + $onText.Contains('id="appendix"'))
  $onSha = if ($onStatus -eq 200) { Get-FileSha $onHtml } else { "" }

  # OFF: appendix disabled; restart applies the startup-read section switch.
  $null = Set-Setting -Key "report.section_appendix" -Value $false
  Restart-ApiA
  $regen2 = Invoke-Api -Method "POST" -Path "/api/versions/$($script:v2)/reports/$reportID/regenerate" -Body @{} -TimeoutSec 60
  Add-Check $checks ($regen2.Status -eq 202) "regenerate (appendix off) accepted" "status=$($regen2.Status)"
  $offRev = [int]$regen2.Json.data.revision
  $offRevID = Get-Scalar "SELECT id FROM delivery_report_revision WHERE report_id = $reportID AND revision = $offRev;"
  $offPublished = Wait-SqlValue -Sql "SELECT status FROM delivery_report_revision WHERE id = $offRevID;" -Want "published" -TimeoutSeconds 150 -IntervalMs 2000
  Add-Check $checks $offPublished "new revision published (appendix off)" "revision=$offRev"
  $offRow = Get-RevisionRow -RevisionID ([int64]$offRevID)
  Add-Check $checks ($offRow.TemplateVersion -eq "2") "off revision template_version=2" "got $($offRow.TemplateVersion)"
  $offHtml = Join-Path $script:evidence "s3-appendix-off.html"
  $offStatus = Download-ApiFile -Path "/api/versions/$($script:v2)/reports/$reportID/revisions/$offRev/html" -OutFile $offHtml
  $offText = if ($offStatus -eq 200) { Get-Content $offHtml -Raw } else { "" }
  Add-Check $checks ($offStatus -eq 200 -and -not $offText.Contains('id="appendix"')) "appendix section absent" ("status=$offStatus appendix=" + $offText.Contains('id="appendix"'))

  # Old revisions never re-render.
  $null = Download-ApiFile -Path "/api/versions/$($script:v2)/reports/$reportID/revisions/1/html" -OutFile (Join-Path $script:evidence "s3-rev1-after.html")
  $sha1After = Get-FileSha (Join-Path $script:evidence "s3-rev1-after.html")
  Add-Check $checks ($sha1After -eq $sha1Before -and $rev1Row.TemplateVersion -eq "1") "old revision 1 bytes unchanged" "sha=$sha1Before"
  $null = Download-ApiFile -Path "/api/versions/$($script:v2)/reports/$reportID/revisions/$latest/html" -OutFile (Join-Path $script:evidence "s3-rev$latest-after.html")
  $shaLatestAfter = Get-FileSha (Join-Path $script:evidence "s3-rev$latest-after.html")
  Add-Check $checks ($shaLatestAfter -eq $shaLatestBefore) "pre-existing latest revision bytes unchanged" "revision=$latest"
  $onAfter = Get-Scalar "SELECT html_ref FROM delivery_report_revision WHERE id = $newRevID;"
  Add-Check $checks ($onAfter -eq $onRow.HTMLRef) "appendix-on revision html_ref still immutable" "revision=$newRev"

  # Restore the configured default for the remaining scenarios.
  $null = Set-Setting -Key "report.section_appendix" -Value $true
  Restart-ApiA
  Write-Log "    restored report.section_appendix=true"
}

function Scenario-S4($checks) {
  $title = "P7 acceptance global knowledge"
  $tokenText = "P7K-GLOBAL-" + ([guid]::NewGuid().ToString("N").Substring(0, 10))
  $body = @{
    title = $title; content = "Refund amount equals the paid amount. Acceptance token $tokenText."
    scope = "global"; tags = @("p7-acceptance", "refund")
    source_version_id = $script:v1; promoted_from_version_id = $script:v1
  }
  $create = Invoke-Api -Method "POST" -Path "/api/knowledge/items" -Body $body
  Add-Check $checks ($create.Status -eq 201 -and $create.Json.data.status -eq "draft") "knowledge draft created (version-sourced)" ("status=" + $create.Status + " id=" + $(if ($create.Json) { $create.Json.data.id } else { "?" }))
  $kid = [string]$create.Json.data.id
  Add-Check $checks ($create.Json.data.source_version_id -eq $script:v1) "draft carries source_version_id" ("source=" + $create.Json.data.source_version_id)
  $approve = Invoke-Api -Method "POST" -Path "/api/knowledge/items/$kid/approve" -Body @{ reason = "p7 acceptance" }
  Add-Check $checks ($approve.Status -eq 200 -and $approve.Json.data.status -eq "approved") "admin approve" ("status=" + $approve.Status)
  $run1 = Start-ClarificationRun -VersionID $script:v1 -Title "knowledge injection"
  $packet1 = Wait-ContextPacket -VersionID $script:v1 -AfterID $run1.PacketBefore -Needle "[K-$kid]" -TimeoutSeconds 150
  Add-Check $checks ($null -ne $packet1) "context_packet carries [K-$kid]" ("run=" + $(if ($packet1) { $packet1.RunID } else { "none" }))
  if ($null -ne $packet1) {
    $hasSection = Get-PacketHasText -PacketID ([int64]$packet1.PacketID) -Needle '"name": "knowledge"'
    Add-Check $checks $hasSection "context_packet has knowledge section"
    $usage = Get-Scalar "SELECT count(*) FROM knowledge_usage WHERE knowledge_id = $kid AND agent_run_id = $($packet1.RunID);"
    Add-Check $checks ([int]$usage -ge 1) "knowledge_usage row for the run" "rows=$usage"
  } else {
    Add-Check $checks $false "knowledge_usage row for the run" "no packet"
  }

  $usageBeforeRevoke = [int64](Get-Scalar "SELECT coalesce(max(id),0) FROM knowledge_usage;")
  $revoke = Invoke-Api -Method "POST" -Path "/api/knowledge/items/$kid/revoke" -Body @{ reason = "p7 acceptance revoke" }
  Add-Check $checks ($revoke.Status -eq 200 -and $revoke.Json.data.status -eq "revoked") "admin revoke" ("status=" + $revoke.Status)
  $packetBefore2 = [int64](Get-Scalar "SELECT coalesce(max(id),0) FROM context_packet;")
  $null = Start-ClarificationRun -VersionID $script:v1 -Title "knowledge revoke exclusion"
  $packet2 = Wait-ContextPacket -VersionID $script:v1 -AfterID $packetBefore2 -TimeoutSeconds 150
  Add-Check $checks ($null -ne $packet2) "post-revoke clarification run produced a context packet"
  if ($null -ne $packet2) {
    $stillThere = Get-PacketHasText -PacketID ([int64]$packet2.PacketID) -Needle "[K-$kid]"
    Add-Check $checks (-not $stillThere) "revoked knowledge excluded from later run" "packet=$($packet2.PacketID)"
    $usagesAfter = [int](Get-Scalar "SELECT count(*) FROM knowledge_usage WHERE knowledge_id = $kid AND id > $usageBeforeRevoke;")
    Add-Check $checks ($usagesAfter -eq 0) "no new knowledge_usage after revoke" "rows=$usagesAfter"
  } else {
    Add-Check $checks $false "revoked knowledge excluded from later run" "no packet"
  }

  $reapprove = Invoke-Api -Method "POST" -Path "/api/knowledge/items/$kid/approve" -Body @{ reason = "p7 acceptance re-review" }
  Add-Check $checks ($reapprove.Status -eq 200 -and $reapprove.Json.data.status -eq "approved") "re-approve restores item" ("status=" + $reapprove.Status)
  $packetBefore3 = [int64](Get-Scalar "SELECT coalesce(max(id),0) FROM context_packet;")
  $null = Start-ClarificationRun -VersionID $script:v1 -Title "knowledge re-review"
  $packet3 = Wait-ContextPacket -VersionID $script:v1 -AfterID $packetBefore3 -Needle "[K-$kid]" -TimeoutSeconds 150
  Add-Check $checks ($null -ne $packet3) "re-approved knowledge injected again"
  $script:globalKnowledgeID = $kid
  $script:globalKnowledgeToken = $tokenText
}

function Scenario-S5($checks) {
  $v1 = $script:v1
  $v2 = $script:v2
  $ordersBody = @{
    title = "P7 orders system knowledge"; content = "Order family knowledge token P7K-SYS-ORDERS."
    scope = "system"; system_key = "p4-orders"; tags = @("p7-acceptance", "orders")
    source_version_id = $v1
  }
  $orders = Invoke-Api -Method "POST" -Path "/api/knowledge/items" -Body $ordersBody
  Add-Check $checks ($orders.Status -eq 201 -and $orders.Json.data.scope -eq "system" -and $orders.Json.data.system_key -eq "p4-orders") "create system/*orders* draft" "status=$($orders.Status)"
  $ordersID = [string]$orders.Json.data.id
  $null = Invoke-Api -Method "POST" -Path "/api/knowledge/items/$ordersID/approve" -Body @{}
  $payBody = @{
    title = "P7 payments system knowledge"; content = "Payment family knowledge token P7K-SYS-PAYMENTS."
    scope = "system"; system_key = "p4-payments"; tags = @("p7-acceptance", "payments")
    source_version_id = $v1
  }
  $pay = Invoke-Api -Method "POST" -Path "/api/knowledge/items" -Body $payBody
  Add-Check $checks ($pay.Status -eq 201 -and $pay.Json.data.system_key -eq "p4-payments") "create system/*payments* draft" "status=$($pay.Status)"
  $payID = [string]$pay.Json.data.id
  $null = Invoke-Api -Method "POST" -Path "/api/knowledge/items/$payID/approve" -Body @{}

  # V2 only binds P4 Order Center: orders matches, payments must not.
  $runV2 = Start-ClarificationRun -VersionID $v2 -Title "system scope v2"
  $packetV2 = Wait-ContextPacket -VersionID $v2 -AfterID $runV2.PacketBefore -Needle "[K-$ordersID]" -TimeoutSeconds 150
  Add-Check $checks ($null -ne $packetV2) "V2 injects matching system knowledge" ("orders=$ordersID")
  if ($null -ne $packetV2) {
    Add-Check $checks (-not (Get-PacketHasText -PacketID ([int64]$packetV2.PacketID) -Needle "[K-$payID]")) "V2 excludes non-matching p4-payments item" "payments=$payID"
  } else {
    Add-Check $checks $false "V2 excludes non-matching p4-payments item" "no packet"
  }
  # V1 binds both families: both inject.
  $runV1 = Start-ClarificationRun -VersionID $v1 -Title "system scope v1"
  $packetV1 = Wait-ContextPacket -VersionID $v1 -AfterID $runV1.PacketBefore -Needle "[K-$ordersID]" -TimeoutSeconds 150
  Add-Check $checks ($null -ne $packetV1) "V1 injects matching orders system knowledge"
  if ($null -ne $packetV1) {
    Add-Check $checks (Get-PacketHasText -PacketID ([int64]$packetV1.PacketID) -Needle "[K-$payID]") "V1 injects matching payments system knowledge"
  } else {
    Add-Check $checks $false "V1 injects matching payments system knowledge" "no packet"
  }

  $search = Invoke-Api -Method "GET" -Path "/api/knowledge/items?q=P7K-SYS-ORDERS&tag=p7-acceptance&limit=20"
  $searchHit = $false
  if ($search.Status -eq 200) {
    foreach ($item in $search.Json.data) { if ([string]$item.id -eq $ordersID) { $searchHit = $true } }
  }
  Add-Check $checks ($search.Status -eq 200 -and $searchHit) "full-text/tag search hits system item" ("status=" + $search.Status)
  $tagSearch = Invoke-Api -Method "GET" -Path "/api/knowledge/items?status=approved&tag=payments&limit=20"
  $tagHit = $false
  if ($tagSearch.Status -eq 200) {
    foreach ($item in $tagSearch.Json.data) { if ([string]$item.id -eq $payID) { $tagHit = $true } }
  }
  Add-Check $checks ($tagSearch.Status -eq 200 -and $tagHit) "approved+tag filter hits payments item"

  # Budget: injection_max_items=1 must cap one run to a single citation/usage row.
  $null = Set-Setting -Key "knowledge.injection_max_items" -Value 1
  $packetBefore = [int64](Get-Scalar "SELECT coalesce(max(id),0) FROM context_packet;")
  $null = Start-ClarificationRun -VersionID $v1 -Title "knowledge budget"
  $budgetPacket = Wait-ContextPacket -VersionID $v1 -AfterID $packetBefore -Needle "[K-" -TimeoutSeconds 150
  Add-Check $checks ($null -ne $budgetPacket) "budget run produced a knowledge packet"
  if ($null -ne $budgetPacket) {
    $sections = Get-Scalar "SELECT sections::text FROM context_packet WHERE id = $($budgetPacket.PacketID);"
    $citationCount = ([regex]::Matches($sections, "\[K-\d+\]")).Count
    Add-Check $checks ($citationCount -eq 1) "injection_max_items=1 caps citations" "citations=$citationCount"
    $usageCount = [int](Get-Scalar "SELECT count(*) FROM knowledge_usage WHERE agent_run_id = $($budgetPacket.RunID);")
    Add-Check $checks ($usageCount -eq 1) "knowledge_usage rows capped" "rows=$usageCount"
  } else {
    Add-Check $checks $false "injection_max_items=1 caps citations" "no packet"
  }
  $null = Set-Setting -Key "knowledge.injection_max_items" -Value 5
  $script:ordersKnowledgeID = $ordersID
  $script:paymentsKnowledgeID = $payID
}

function Seed-GcCandidates($checks) {
  $uSha = New-Hex64
  $tSha = New-Hex64
  $sOldSha = New-Hex64
  $sNewSha = New-Hex64
  $oSha = New-Hex64
  $hSha = New-Hex64
  $u = Write-DataFile -Sha $uSha -Size 512 -LocalMtime ((Get-Date).AddDays(-31))
  $t = Write-DataFile -Sha $tSha -Size 128 -LocalMtime ((Get-Date).AddHours(-25))
  $sOld = Write-DataFile -Sha $sOldSha -Size 256 -LocalMtime ((Get-Date).AddDays(-9))
  $sNew = Write-DataFile -Sha $sNewSha -Size 300 -LocalMtime (Get-Date)
  $o = Write-DataFile -Sha $oSha -Size 384 -LocalMtime ((Get-Date).AddDays(-10))
  $h = Write-DataFile -Sha $hSha -Size 64 -LocalMtime (Get-Date)
  $oldArtifact = 990700
  $newArtifact = 990701
  $sql = @"
INSERT INTO content_object (sha256, size, mime, relative_path, created_at) VALUES
 ('$uSha', 512, 'application/octet-stream', '$($u.RelativePath)', now() - interval '31 days'),
 ('$sOldSha', 256, 'text/markdown', '$($sOld.RelativePath)', now() - interval '31 days'),
 ('$sNewSha', 300, 'text/markdown', '$($sNew.RelativePath)', now() - interval '1 hour'),
 ('$oSha', 384, 'text/plain', '$($o.RelativePath)', now() - interval '31 days');
INSERT INTO export_artifact (id, version_id, owner_kind, owner_id, revision, file_format, content_object_id, size_bytes, mime, filename, status, created_at, updated_at)
VALUES
 ($oldArtifact, $($script:v1), 'report', 990702, 1, 'markdown', '$sOldSha', 256, 'text/markdown', 'p7-old.md', 'ready', now() - interval '8 days', now() - interval '8 days'),
 ($newArtifact, $($script:v1), 'report', 990702, 2, 'markdown', '$sNewSha', 300, 'text/markdown', 'p7-new.md', 'ready', now() - interval '1 hour', now() - interval '1 hour');
INSERT INTO content_ref (id, object_id, owner_kind, owner_id, role, created_at) VALUES
 (990703, '$sOldSha', 'export', $oldArtifact, 'export', now() - interval '8 days'),
 (990704, '$sNewSha', 'export', $newArtifact, 'export', now() - interval '1 hour'),
 (990705, '$oSha', 'evidence', 999999999999999999, 'evidence', now() - interval '10 days');
"@
  $null = Invoke-Psql -Sql $sql
  return [pscustomobject]@{
    UnreferencedSha = $uSha; TempSha = $tSha; OldExportSha = $sOldSha; NewExportSha = $sNewSha
    OrphanSha = $oSha; GraceSha = $hSha; OldArtifact = $oldArtifact; NewArtifact = $newArtifact
    UnreferencedFile = $u.FullPath; TempFile = $t.FullPath; OldExportFile = $sOld.FullPath
    NewExportFile = $sNew.FullPath; OrphanFile = $o.FullPath; GraceFile = $h.FullPath
  }
}

function Assert-ProtectedReportContent($checks, [string]$Label) {
  $row = Get-Scalar "SELECT rv.id || '|' || rv.html_ref || '|' || o.sha256 || '|' || o.relative_path FROM delivery_report_revision rv JOIN content_ref r ON r.id = rv.html_ref JOIN content_object o ON o.sha256 = r.object_id WHERE rv.status = 'published' ORDER BY rv.id LIMIT 1;"
  Add-Check $checks (-not [string]::IsNullOrWhiteSpace($row)) "$Label report html ledger row exists" $row
  if (-not [string]::IsNullOrWhiteSpace($row)) {
    $parts = $row.Split("|")
    $full = Join-Path $script:dataRoot ($parts[3] -replace "/", "\")
    Add-Check $checks (Test-Path $full) "$Label report html file exists" $parts[3]
  }
}

function Scenario-S6($checks) {
  $gc = Seed-GcCandidates
  $refsBefore = [int](Get-Scalar "SELECT count(*) FROM content_ref WHERE deleted_at IS NULL;")
  $objectsBefore = [int](Get-Scalar "SELECT count(*) FROM content_object;")
  $run = Invoke-Api -Method "POST" -Path "/api/admin/gc/runs" -Body @{ mode = "dry_run" } -TimeoutSec 120
  Add-Check $checks ($run.Status -eq 200) "dry-run accepted" "status=$($run.Status)"
  $report = $run.Json.data.report
  $counts = $report.candidates
  Add-Check $checks ([int]$counts.unreferenced -eq 1) "dry-run unreferenced=1" ("got " + $counts.unreferenced)
  Add-Check $checks ([int]$counts.temp -eq 1) "dry-run temp=1" ("got " + $counts.temp)
  Add-Check $checks ([int]$counts.superseded_export -eq 1) "dry-run superseded_export=1" ("got " + $counts.superseded_export)
  Add-Check $checks ([int]$counts.orphan_file -eq 1) "dry-run orphan_file=1" ("got " + $counts.orphan_file)
  Add-Check $checks ([int]$report.deleted.objects -eq 0 -and [int]$report.deleted.refs -eq 0 -and [int]$report.deleted.files -eq 0) "dry-run deletes nothing" ("objects=" + $report.deleted.objects + " refs=" + $report.deleted.refs)
  Add-Check $checks ([int]$report.tombstones -eq 0) "dry-run writes no tombstone" ("tombstones=" + $report.tombstones)
  Add-Check $checks ([int]$report.protected.report_content -gt 0) "dry-run protects report content" ("report_content=" + $report.protected.report_content)
  Add-Check $checks ([int]$report.protected.grace -ge 1) "dry-run protects grace content" ("grace=" + $report.protected.grace)
  Add-Check $checks ([int]$report.protected.owner_exists -gt 0) "dry-run protects live owners" ("owner_exists=" + $report.protected.owner_exists)
  $refsAfter = [int](Get-Scalar "SELECT count(*) FROM content_ref WHERE deleted_at IS NULL;")
  $objectsAfter = [int](Get-Scalar "SELECT count(*) FROM content_object;")
  Add-Check $checks ($refsBefore -eq $refsAfter -and $objectsBefore -eq $objectsAfter) "dry-run leaves ledger unchanged" "refs=$refsAfter objects=$objectsAfter"
  Add-Check $checks ((Test-Path $gc.UnreferencedFile) -and (Test-Path $gc.TempFile) -and (Test-Path $gc.OldExportFile) -and (Test-Path $gc.OrphanFile)) "dry-run leaves candidate files on disk"
  Assert-ProtectedReportContent $checks "dry-run"
  $tombstones = [int](Get-Scalar "SELECT count(*) FROM gc_tombstone;")
  Add-Check $checks ($tombstones -eq 0) "dry-run gc_tombstone empty" "rows=$tombstones"
  $script:s6RunID = [string]$run.Json.data.id
  $script:s6Report = $report
  $script:gcSeed = $gc
  $script:s6RefsBefore = $refsBefore
  $script:s6ObjectsBefore = $objectsBefore
}

function Scenario-S7($checks) {
  $gc = $script:gcSeed
  $noConfirm = Invoke-Api -Method "POST" -Path "/api/admin/gc/runs" -Body @{ mode = "apply" } -TimeoutSec 60
  $noConfirmCode = if ($null -ne $noConfirm.Json -and $null -ne $noConfirm.Json.error) { [string]$noConfirm.Json.error.code } else { "" }
  $noConfirmOK = ($noConfirm.Status -eq 400) -and (($noConfirmCode -eq "confirmation_required") -or ($noConfirm.Text -like "*confirmation_required*"))
  Add-Check $checks $noConfirmOK "apply without confirm -> 400 confirmation_required" ("status=" + $noConfirm.Status + " code=" + $noConfirmCode)
  $run = Invoke-Api -Method "POST" -Path "/api/admin/gc/runs" -Body @{ mode = "apply"; confirm = $true } -TimeoutSec 180
  Add-Check $checks ($run.Status -eq 200 -and $run.Json.data.status -eq "completed") "apply confirmed completed" ("status=" + $run.Status + " run=" + $(if ($run.Json) { $run.Json.data.status } else { "?" }))
  $report = $run.Json.data.report
  Add-Check $checks ([int]$report.deleted.objects -eq 3) "apply deleted_objects=3" ("got " + $report.deleted.objects)
  Add-Check $checks ([int]$report.deleted.refs -eq 2) "apply deleted_refs=2" ("got " + $report.deleted.refs)
  Add-Check $checks ([int]$report.deleted.files -eq 4) "apply deleted_files=4" ("got " + $report.deleted.files)
  Add-Check $checks ([int]$report.tombstones -eq 4) "apply tombstones=4" ("got " + $report.tombstones)
  $runID = [string]$run.Json.data.id
  $tombstoneRows = Get-Scalar "SELECT count(*) || '|' || count(DISTINCT reason) FROM gc_tombstone WHERE run_id = $runID;"
  Add-Check $checks ($tombstoneRows -eq "4|4") "gc_tombstone rows (4 rows, 4 reasons)" "rows|reasons=$tombstoneRows"
  $reasons = (Invoke-Psql "SELECT reason FROM gc_tombstone WHERE run_id = $runID ORDER BY reason;") -join ","
  Add-Check $checks ($reasons -eq "orphan_file,superseded_export,temp,unreferenced") "tombstone reasons complete" $reasons
  Add-Check $checks ((Get-Scalar "SELECT count(*) FROM content_object WHERE sha256 = '$($gc.UnreferencedSha)';") -eq "0") "unreferenced object row deleted"
  Add-Check $checks (-not (Test-Path $gc.UnreferencedFile)) "unreferenced file deleted"
  Add-Check $checks (-not (Test-Path $gc.TempFile)) "temp file deleted"
  Add-Check $checks ((Get-Scalar "SELECT count(*) FROM content_ref WHERE id = 990703;") -eq "0") "superseded export ref row collected"
  Add-Check $checks ((Get-Scalar "SELECT count(*) FROM content_object WHERE sha256 = '$($gc.OldExportSha)';") -eq "0") "superseded export object deleted"
  Add-Check $checks (-not (Test-Path $gc.OldExportFile)) "superseded export file deleted"
  Add-Check $checks ((Test-Path $gc.NewExportFile) -and (Get-Scalar "SELECT count(*) FROM content_object WHERE sha256 = '$($gc.NewExportSha)';") -eq "1" -and (Get-Scalar "SELECT count(*) FROM content_ref WHERE id = 990704 AND deleted_at IS NULL;") -eq "1") "current export protected"
  Add-Check $checks ((Get-Scalar "SELECT count(*) FROM content_ref WHERE id = 990705;") -eq "0") "orphan ref row collected"
  Add-Check $checks ((Get-Scalar "SELECT count(*) FROM content_object WHERE sha256 = '$($gc.OrphanSha)';") -eq "0") "orphan object deleted"
  Add-Check $checks (-not (Test-Path $gc.OrphanFile)) "orphan file deleted"
  Add-Check $checks ((Test-Path $gc.GraceFile) -and (Get-Scalar "SELECT count(*) FROM content_object WHERE sha256 = '$($gc.GraceSha)';") -eq "0") "recent grace file untouched"
  Assert-ProtectedReportContent $checks "apply"
  $protectedRefs = [int](Get-Scalar "SELECT count(*) FROM content_ref WHERE owner_kind = 'report_revision' AND role = 'report_html' AND deleted_at IS NULL;")
  Add-Check $checks ($protectedRefs -gt 0) "report html refs remain live" "refs=$protectedRefs"
  $audit = [int](Get-Scalar "SELECT count(*) FROM audit_log WHERE action = 'storage.gc.completed' AND target_id = '$runID';")
  Add-Check $checks ($audit -eq 1) "storage.gc.completed audit row" "rows=$audit"
  $event = [int](Get-Scalar "SELECT count(*) FROM event WHERE type = 'gc.completed' AND envelope->'payload'->>'gc_run_id' = '$runID';")
  Add-Check $checks ($event -eq 1) "gc.completed event row" "rows=$event"
  $refsAfter = [int](Get-Scalar "SELECT count(*) FROM content_ref WHERE deleted_at IS NULL;")
  Add-Check $checks ($refsAfter -eq ($script:s6RefsBefore - 2)) "only candidate refs removed" ("before=$script:s6RefsBefore after=$refsAfter")
  $script:s7RunID = $runID
}

function Scenario-S2($checks) {
  $reportID = Get-Scalar "SELECT rv.report_id FROM delivery_report_revision rv JOIN delivery_report r ON r.id = rv.report_id WHERE r.version_id = $($script:v2) AND r.kind = 'wave' ORDER BY rv.id DESC LIMIT 1;"
  if ([string]::IsNullOrWhiteSpace($reportID)) { throw "no v2 wave report found" }
  $null = Set-Setting -Key "report.render_worker_url" -Value "http://127.0.0.1:13399"
  $null = Set-Setting -Key "report.render_max_attempts" -Value 1
  Restart-ApiA
  $regen = Invoke-Api -Method "POST" -Path "/api/versions/$($script:v2)/reports/$reportID/regenerate" -Body @{} -TimeoutSec 60
  Add-Check $checks ($regen.Status -eq 202) "regenerate with dead renderer accepted" "status=$($regen.Status) rev=$(if ($regen.Json) { $regen.Json.data.revision } else { '?' })"
  $failedRev = [int]$regen.Json.data.revision
  $failedRevID = Get-Scalar "SELECT id FROM delivery_report_revision WHERE report_id = $reportID AND revision = $failedRev;"
  $published = Wait-SqlValue -Sql "SELECT status FROM delivery_report_revision WHERE id = $failedRevID;" -Want "published" -TimeoutSeconds 150
  Add-Check $checks $published "new revision published (dead renderer)" "revision=$failedRev"
  $deadline = (Get-Date).AddSeconds(90)
  $pdfStatus = ""
  $deadError = ""
  while ((Get-Date) -lt $deadline) {
    $pdfStatus = Get-Scalar "SELECT pdf_status FROM delivery_report_revision WHERE id = $failedRevID;"
    if ($pdfStatus -eq "failed" -or $pdfStatus -eq "done") { break }
    $deadError = Get-DeadRenderJobError -ReportID $reportID -Revision $failedRev
    if (-not [string]::IsNullOrWhiteSpace($deadError)) { break }
    Start-Sleep -Seconds 2
  }
  if (-not [string]::IsNullOrWhiteSpace($deadError)) {
    Add-Check $checks $false "pdf_status=failed with dead renderer" ("render work item dead: " + $deadError)
    Add-BugNote ("S2 blocked: report.render_pdf work item for revision $failedRev was claimed and killed: " + $deadError)
  } else {
    Add-Check $checks ($pdfStatus -eq "failed") "pdf_status=failed with dead renderer" "got $pdfStatus"
  }
  $failedRow = Get-RevisionRow -RevisionID ([int64]$failedRevID)
  Add-Check $checks (-not [string]::IsNullOrWhiteSpace($failedRow.PDFError)) "pdf_error recorded" ("error=" + $failedRow.PDFError)
  $failedEvent = [int](Get-Scalar "SELECT count(*) FROM event WHERE type = 'report.pdf_failed' AND envelope->'payload'->>'report_revision_id' = '$failedRevID';")
  Add-Check $checks ($failedEvent -eq 1) "report.pdf_failed event row" "rows=$failedEvent"
  $failedAudit = [int](Get-Scalar "SELECT count(*) FROM audit_log WHERE action = 'delivery.report.pdf_failed' AND target_id = '$failedRevID';")
  Add-Check $checks ($failedAudit -eq 1) "delivery.report.pdf_failed audit row" "rows=$failedAudit"
  if (-not [string]::IsNullOrWhiteSpace($deadError)) {
    Add-BugNote ("S2 evidence: failed revision=$failedRevID pdf_status=$pdfStatus deadError=$deadError preview=$($failedRow.PDFError)")
  }

  # Restore the renderer and require the next revision to succeed.
  $null = Set-Setting -Key "report.render_worker_url" -Value "http://127.0.0.1:$RendererPort"
  $null = Set-Setting -Key "report.render_max_attempts" -Value 3
  Restart-ApiA
  $regen2 = Invoke-Api -Method "POST" -Path "/api/versions/$($script:v2)/reports/$reportID/regenerate" -Body @{} -TimeoutSec 60
  Add-Check $checks ($regen2.Status -eq 202) "regenerate after renderer restore accepted" "status=$($regen2.Status)"
  $okRev = [int]$regen2.Json.data.revision
  $okRevID = Get-Scalar "SELECT id FROM delivery_report_revision WHERE report_id = $reportID AND revision = $okRev;"
  $published2 = Wait-SqlValue -Sql "SELECT status FROM delivery_report_revision WHERE id = $okRevID;" -Want "published" -TimeoutSeconds 150
  Add-Check $checks $published2 "restored revision published" "revision=$okRev"
  $deadline = (Get-Date).AddSeconds(120)
  $okStatus = ""
  $deadError2 = ""
  while ((Get-Date) -lt $deadline) {
    $okStatus = Get-Scalar "SELECT pdf_status FROM delivery_report_revision WHERE id = $okRevID;"
    if ($okStatus -eq "done" -or $okStatus -eq "failed") { break }
    $deadError2 = Get-DeadRenderJobError -ReportID $reportID -Revision $okRev
    if (-not [string]::IsNullOrWhiteSpace($deadError2)) { break }
    Start-Sleep -Seconds 3
  }
  if (-not [string]::IsNullOrWhiteSpace($deadError2)) {
    Add-Check $checks $false "pdf_status=done after renderer restore" ("render work item dead: " + $deadError2)
    Add-BugNote ("S2 restore blocked: report.render_pdf work item for revision $okRev was claimed and killed: " + $deadError2)
  } else {
    Add-Check $checks ($okStatus -eq "done") "pdf_status=done after renderer restore" "got $okStatus"
  }
  if ($okStatus -eq "done") {
    $pdfPath = Join-Path $script:evidence "s2-restored.pdf"
    $pdfStatus = Download-ApiFile -Path "/api/versions/$($script:v2)/reports/$reportID/revisions/$okRev/pdf" -OutFile $pdfPath
    $magic = ""
    if ($pdfStatus -eq 200) {
      $bytes = [System.IO.File]::ReadAllBytes($pdfPath)
      if ($bytes.Length -ge 5) { $magic = [System.Text.Encoding]::ASCII.GetString($bytes, 0, 5) }
    }
    Add-Check $checks ($pdfStatus -eq 200 -and $magic -eq "%PDF-") "restored PDF downloadable" "status=$pdfStatus"
  } else {
    Add-Check $checks $false "restored PDF downloadable" "skipped: pdf_status=$okStatus"
  }
}

function Scenario-S8($checks) {
  # Clean up instance rows left by the acceptance restarts so the two live
  # replicas are the only active instances in the drill.
  $null = Invoke-Psql -Sql "DELETE FROM api_instance WHERE pid <> $($script:apiA.Id);"
  $null = Start-ApiB
  $script:token = Login-Admin
  $instances = Invoke-Api -Method "GET" -Path "/api/admin/instances"
  $active = @($instances.Json.data | Where-Object { $_.status -eq "active" })
  $nodeIDs = @($active | ForEach-Object { [int]$_.node_id } | Sort-Object -Unique)
  Add-Check $checks ($active.Count -eq 2 -and $nodeIDs.Count -eq 2) "two active replicas with distinct node_id" ("active=" + $active.Count + " nodes=" + ($nodeIDs -join ","))
  if ($active.Count -eq 2) {
    Add-Check $checks ($active[0].instance_key -ne $active[1].instance_key) "instance keys are distinct" ($active[0].instance_key + " / " + $active[1].instance_key)
  } else {
    Add-Check $checks $false "instance keys are distinct" "active=$($active.Count)"
  }

  $countsBefore = Get-Scalar "SELECT (SELECT count(*) FROM work_item) || '|' || (SELECT count(*) FROM agent_run) || '|' || (SELECT count(*) FROM case_run) || '|' || (SELECT count(*) FROM schema_migrations);"
  $sseFile = Join-Path $script:evidence "sse-system.txt"
  $sse = Start-Sse -Port $ApiBPort -Channel "system" -OutFile $sseFile
  Start-Sleep -Seconds 2
  # Publish an event from replica A: create+approve a knowledge item (system channel event).
  $body = @{ title = "P7 acceptance SSE event"; content = "SSE cross-instance probe."; scope = "global" }
  $item = Invoke-ApiOnPort -Port $ApiPort -Method "POST" -Path "/api/knowledge/items" -Body $body -Token $script:token
  $sseItemID = [string]$item.Json.data.id
  $null = Invoke-ApiOnPort -Port $ApiPort -Method "POST" -Path "/api/knowledge/items/$sseItemID/approve" -Body @{} -Token $script:token
  $received = $false
  $eventID = ""
  $deadline = (Get-Date).AddSeconds(20)
  while ((Get-Date) -lt $deadline) {
    if (Test-Path $sseFile) {
      $text = Get-Content $sseFile -Raw
      if ($text -match "event: knowledge\.approved[\s\S]*?id: (\d+)") { $received = $true; $eventID = $matches[1]; break }
      if ($text -match "id: (\d+)\s+event: knowledge\.approved") { $received = $true; $eventID = $matches[1]; break }
    }
    Start-Sleep -Milliseconds 500
  }
  Stop-OwnedProcess $sse
  Add-Check $checks ($received -and $eventID -ne "") "replica B SSE received an event published via A" ("event_id=" + $eventID)
  if (-not $received) { Add-BugNote "S8: replica B SSE did not receive the knowledge.approved event"; $tail = if (Test-Path $sseFile) { Get-Content $sseFile -Raw } else { "" }; Write-Evidence "sse-system-miss.txt" $tail | Out-Null }

  # Last-Event-ID replay from B.
  $replayFile = Join-Path $script:evidence "sse-system-replay.txt"
  if ($eventID -ne "") {
    $cursor = [string]([int64]$eventID - 1)
    $replay = Start-Sse -Port $ApiBPort -Channel "system" -OutFile $replayFile -LastEventId $cursor
    Start-Sleep -Seconds 4
    Stop-OwnedProcess $replay
    $replayText = if (Test-Path $replayFile) { Get-Content $replayFile -Raw } else { "" }
    Add-Check $checks ($replayText.Contains("id: $eventID") -and $replayText.Contains("knowledge.approved")) "Last-Event-ID replay re-sends the event" "cursor=$cursor event=$eventID"
  } else {
    Add-Check $checks $false "Last-Event-ID replay re-sends the event" "no event id"
  }
  $countsAfter = Get-Scalar "SELECT (SELECT count(*) FROM work_item) || '|' || (SELECT count(*) FROM agent_run) || '|' || (SELECT count(*) FROM case_run) || '|' || (SELECT count(*) FROM schema_migrations);"
  $script:s8CountsBefore = $countsBefore
  $script:s8CountsAfter = $countsAfter
}

function Scenario-S9($checks) {
  $countsBefore = Get-Scalar "SELECT (SELECT count(*) FROM work_item) || '|' || (SELECT count(*) FROM agent_run) || '|' || (SELECT count(*) FROM case_run) || '|' || (SELECT count(*) FROM schema_migrations);"
  Add-Check $checks ($countsBefore -eq $script:s8CountsAfter) "no duplicate runs while both replicas were up" "before=$($script:s8CountsAfter) after=$countsBefore"
  $dupes = [int](Get-Scalar "SELECT count(*) FROM (SELECT version_id, type, dedupe_key FROM work_item GROUP BY 1,2,3 HAVING count(*) > 1) d;")
  Add-Check $checks ($dupes -eq 0) "no duplicate work_item dedupe rows" ("duplicates=" + $dupes)
  $migrations = [int](Get-Scalar "SELECT count(*) FROM schema_migrations;")
  Add-Check $checks ($migrations -eq $ExpectedMigrations) "migrations re-applied once only" "count=$migrations expected=$ExpectedMigrations"

  $instances = Invoke-Api -Method "GET" -Path "/api/admin/instances"
  $active = @($instances.Json.data | Where-Object { $_.status -eq "active" })
  $aInstance = $active | Where-Object { [int]$_.pid -eq $script:apiA.Id } | Select-Object -First 1
  Add-Check $checks ($active.Count -eq 2 -and $null -ne $aInstance) "two active instances before drain"
  if ($null -eq $aInstance) { throw "api A instance row not found" }
  $lockBefore = @(Invoke-Psql "SELECT pid::text FROM pg_locks WHERE locktype = 'advisory' AND granted AND classid = 0 AND objid = 1163547697 ORDER BY pid;")
  Add-Check $checks ($lockBefore.Count -ge 1) "leader advisory lock held" ("pids=" + ($lockBefore -join ","))

  $drain = Invoke-Api -Method "POST" -Path "/api/admin/instances/$($aInstance.id)/drain" -Body @{} -Token $script:token
  Add-Check $checks ($drain.Status -eq 200 -and $drain.Json.data.status -eq "draining") "drain A accepted" ("status=" + $drain.Status + " state=" + $(if ($drain.Json) { $drain.Json.data.status } else { "?" }))
  $readyA = Wait-Ready -Port $ApiPort -Want 503 -TimeoutSeconds 60
  Add-Check $checks $readyA "A readiness becomes 503" ("last=" + $script:lastWaitValue)
  $readyB = Wait-Ready -Port $ApiBPort -Want 200 -TimeoutSeconds 30
  Add-Check $checks $readyB "B readiness stays 200"

  # Stop A; follower B must take the advisory leader lock.
  $oldPids = @(Invoke-Psql "SELECT pid::text FROM pg_locks WHERE locktype = 'advisory' AND granted AND classid = 0 AND objid = 1163547697 ORDER BY pid;")
  Stop-OwnedProcess $script:apiA
  $null = Wait-PortClosed -Port $ApiPort -TimeoutSeconds 20
  $newPids = @()
  $deadline = (Get-Date).AddSeconds(30)
  while ((Get-Date) -lt $deadline) {
    $newPids = @(Invoke-Psql "SELECT pid::text FROM pg_locks WHERE locktype = 'advisory' AND granted AND classid = 0 AND objid = 1163547697 ORDER BY pid;")
    if ($newPids.Count -ge 1 -and -not ($oldPids -contains $newPids[0])) { break }
    Start-Sleep -Seconds 1
  }
  $newHolder = $newPids.Count -ge 1 -and -not ($oldPids -contains $newPids[0])
  Add-Check $checks $newHolder "leader lock moved to replica B" ("before=" + ($oldPids -join ",") + " after=" + ($newPids -join ","))
  $readyB2 = Wait-Ready -Port $ApiBPort -Want 200 -TimeoutSeconds 30
  Add-Check $checks $readyB2 "B is ready after takeover"

  $countsAfter = Get-Scalar "SELECT (SELECT count(*) FROM work_item) || '|' || (SELECT count(*) FROM agent_run) || '|' || (SELECT count(*) FROM case_run) || '|' || (SELECT count(*) FROM schema_migrations);"
  Add-Check $checks ($countsAfter -eq $countsBefore) "no duplicate work_item/agent_run/case_run after takeover" "before=$countsBefore after=$countsAfter"
  $dupes2 = [int](Get-Scalar "SELECT count(*) FROM (SELECT version_id, type, dedupe_key FROM work_item GROUP BY 1,2,3 HAVING count(*) > 1) d;")
  Add-Check $checks ($dupes2 -eq 0) "no duplicate work_item dedupe rows after takeover" ("duplicates=" + $dupes2)

  # Subsequent scenarios talk to the surviving replica B.
  $script:currentApiPort = $ApiBPort
  $script:baseUrl = "http://127.0.0.1:$ApiBPort"
  $script:token = Login-Admin
  if ([string]::IsNullOrWhiteSpace($script:token)) { throw "admin login failed on replica B" }
}

function Scenario-S10($checks) {
  $endpoint = "http://127.0.0.1:$ExecutorPort"
  $exec = Invoke-Api -Method "GET" -Path "/api/admin/executors"
  $row = @($exec.Json.data | Where-Object { $_.endpoint -eq $endpoint }) | Select-Object -First 1
  Add-Check $checks ($exec.Status -eq 200 -and $null -ne $row -and $row.status -eq "active") "executor registration visible (pull/push)" ("status=" + $(if ($row) { $row.status } else { "missing" }) + " capacity=" + $(if ($row) { $row.capacity } else { "?" }))
  $execLogPath = Join-Path $script:evidence "executor.log"
  $pushLogged = $false
  for ($i = 0; $i -lt 10; $i++) {
    if (Test-Path $execLogPath) {
      $hit = Select-String -Path $execLogPath -Pattern "registered with http://127.0.0.1:$ApiPort" -SimpleMatch -ErrorAction SilentlyContinue | Select-Object -First 1
      if ($null -ne $hit) { $pushLogged = $true; break }
    }
    Start-Sleep -Milliseconds 500
  }
  Add-Check $checks $pushLogged "executor push registration logged" "log=$execLogPath"
  $executorID = [int64](Get-Scalar "SELECT id FROM executor_instance WHERE endpoint = '$endpoint';")
  Add-Check $checks ($executorID -gt 0) "executor row id present" "id=$executorID"

  Stop-OwnedProcess $script:executor
  $offline = Wait-SqlValue -Sql "SELECT status FROM executor_instance WHERE id = $executorID;" -Want "offline" -TimeoutSeconds 60 -IntervalMs 1000
  Add-Check $checks $offline "executor becomes offline after stop" ("status=" + $(Get-Scalar "SELECT status FROM executor_instance WHERE id = $executorID;"))
  Add-Check $checks ((Test-Path (Join-Path $script:evidence "executor.log"))) "executor log retained"

  $fingerprint = New-Hex64
  $sql = @"
INSERT INTO run_wave (id, version_id, status, trigger, counts, started_at, finished_at, created_at, updated_at)
VALUES (990300, $($script:v1), 'done', 'manual', '{}'::jsonb, now(), now(), now(), now()),
       (990301, $($script:v1), 'done', 'manual', '{}'::jsonb, now(), now(), now(), now());
INSERT INTO execution_intent (id, version_id, case_version_id, version_sut_ids, environment_fingerprint, code_baseline, purpose, status, dedupe_key, attempt_no, created_at, updated_at)
VALUES (990310, $($script:v1), 830003, '[]'::jsonb, '$fingerprint', '', 'manual', 'running', 'p7-acceptance-exec-lost-1', 1, now(), now()),
       (990311, $($script:v1), 830004, '[]'::jsonb, '$fingerprint', '', 'manual', 'done', 'p7-acceptance-exec-lost-2', 1, now(), now());
INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, attempts, failure_class, summary, started_at, owner_executor_id, dispatch_epoch, lease_until, created_at, updated_at)
VALUES (990320, 990300, 990310, 830003, 'running', 1, '', '{}'::jsonb, now(), $executorID, 1, now() + interval '10 minutes', now(), now()),
       (990321, 990301, 990311, 830004, 'running', 0, '', '{}'::jsonb, now(), NULL, 0, now() + interval '10 minutes', now(), now());
"@
  $null = Invoke-Psql -Sql $sql
  $interrupted = Wait-SqlValue -Sql "SELECT status FROM case_run WHERE id = 990320;" -Want "pending" -TimeoutSeconds 60 -IntervalMs 1000
  $epoch = Get-Scalar "SELECT dispatch_epoch || '|' || failure_class || '|' || coalesce(summary->>'verdict_reason','') || '|' || coalesce(owner_executor_id::text,'null') || '|' || coalesce(lease_until::text,'null') FROM case_run WHERE id = 990320;"
  Add-Check $checks $interrupted "running case_run interrupted(executor_lost)->pending" ("row=" + $epoch)
  Add-Check $checks ($epoch -eq "2|executor_lost|executor_lost|null|null") "dispatch_epoch incremented and executor cleared" ("row=" + $epoch)
  $other = Get-Scalar "SELECT status || '|' || dispatch_epoch || '|' || coalesce(owner_executor_id::text,'null') FROM case_run WHERE id = 990321;"
  Add-Check $checks ($other -eq "running|0|null") "other running case_run not batch-interrupted" ("row=" + $other)
  $eventCount = [int](Get-Scalar "SELECT count(*) FROM event WHERE type = 'case_run.finished' AND envelope->'payload'->>'case_run_id' = '990320';")
  Add-Check $checks ($eventCount -ge 1) "case_run.finished interrupted event recorded" "rows=$eventCount"
  $audit = [int](Get-Scalar "SELECT count(*) FROM audit_log WHERE action = 'execution.case_run_redispatched' AND metadata->>'case_run_id' = '990320';")
  Add-Check $checks ($audit -ge 1) "redispatched audit row recorded" "rows=$audit"
}

function Scenario-S11($checks) {
  # Baseline for S12: the V1 version-report revision as it stands BEFORE the
  # merge/recurrence mutations below.
  $vReportID = Get-Scalar "SELECT id FROM delivery_report WHERE version_id = $($script:v1) AND kind = 'version' ORDER BY id DESC LIMIT 1;"
  $vLatest = [int](Get-Scalar "SELECT max(revision) FROM delivery_report_revision WHERE report_id = $vReportID AND status = 'published';")
  $vRevID = Get-Scalar "SELECT id FROM delivery_report_revision WHERE report_id = $vReportID AND revision = $vLatest;"
  $vRow = Get-RevisionRow -RevisionID ([int64]$vRevID)
  $vHtml = Join-Path $script:evidence "s12-baseline.html"
  $null = Download-ApiFile -Path "/api/versions/$($script:v1)/reports/$vReportID/revisions/$vLatest/html" -OutFile $vHtml
  $script:s12Baseline = [pscustomobject]@{
    ReportID = $vReportID; Revision = $vLatest; RevisionID = $vRevID; Row = $vRow
    HtmlPath = $vHtml; Sha = (Get-FileSha $vHtml); VersionStatus = (Get-Scalar "SELECT status FROM version WHERE id = $($script:v1);")
  }
  Add-Check $checks (-not [string]::IsNullOrWhiteSpace($vRevID)) "S12 baseline revision captured before merge" "revision=$vLatest as_of=$($vRow.AsOf)"

  # Baseline snapshots of the merge source (already merged into the target issue).
  $sourceBefore = Get-Scalar "SELECT recurrence_count || '|' || status || '|' || coalesce(merged_into_id::text,'') || '|' || (SELECT count(*) FROM delivery_issue_ref WHERE issue_id = $($script:issueSource)) FROM delivery_issue WHERE id = $($script:issueSource);"
  $issueCountBefore = [int](Get-Scalar "SELECT count(*) FROM delivery_issue WHERE version_id = $($script:v1);")
  $targetBefore = Get-Scalar "SELECT recurrence_count || '|' || status FROM delivery_issue WHERE id = $($script:issueTarget);"
  Add-Check $checks ($sourceBefore -like "*|merged|*") "baseline merged source present" "source=$($script:issueSource) row=$sourceBefore"

  # A deterministic merge target so the API merge has two live issues.
  $targetID = 990200
  $dedupe = New-Hex32
  $sql = @"
INSERT INTO delivery_issue (id, version_id, title, summary, status, category, severity, dedupe_key, fingerprint, source, expected_revision, reopen_count, created_at, updated_at)
VALUES ($targetID, $($script:v1), 'P7 acceptance merge target', 'Target for the P7 recurrence redirect.', 'open', 'ui', 'major', '$dedupe', '', 'failure_candidate', 1, 0, now(), now());
"@
  $null = Invoke-Psql -Sql $sql
  $merge = Invoke-Api -Method "POST" -Path "/api/versions/$($script:v1)/issues/$($script:issueTarget)/merge" -Body @{ target_issue_id = [string]$targetID; reason = "p7 acceptance merge" }
  Add-Check $checks ($merge.Status -eq 200 -and $merge.Json.data.status -eq "merged" -and $merge.Json.data.merged_into_id -eq [string]$targetID) "source issue merged into target via API" ("status=" + $merge.Status + " into=" + $(if ($merge.Json) { $merge.Json.data.merged_into_id } else { "?" }))
  $chain = Get-Scalar "SELECT coalesce(merged_into_id::text,'') FROM delivery_issue WHERE id = $($script:issueTarget);"
  Add-Check $checks ($chain -eq [string]$targetID) "merge chain B -> target reaches the merged target" "source_target_merged_into=$chain"

  $fingerprint = New-Hex64
  $sql = @"
INSERT INTO run_wave (id, version_id, status, trigger, counts, started_at, finished_at, created_at, updated_at)
VALUES (990400, $($script:v1), 'done', 'manual', '{}'::jsonb, now(), now(), now(), now());
INSERT INTO execution_intent (id, version_id, case_version_id, version_sut_ids, environment_fingerprint, code_baseline, purpose, status, dedupe_key, attempt_no, created_at, updated_at)
VALUES (990410, $($script:v1), 830002, '[]'::jsonb, '$fingerprint', '', 'manual', 'done', 'p7-acceptance-recurrence-1', 1, now(), now());
INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, attempts, failure_class, summary, started_at, finished_at, created_at, updated_at)
VALUES (990420, 990400, 990410, 830002, 'failed', 1, 'application', '{"last_error":"p7 acceptance recurrence probe 1","verdict_reason":"assertion_failed"}'::jsonb, now() - interval '2 minutes', now(), now(), now());
INSERT INTO assertion_result (id, case_run_id, step_seq, assertion_seq, kind, verdict, reason, expected, actual, target_version_sut_id, created_at, updated_at)
VALUES (990430, 990420, 1, 2, 'ui', 'failed', 'p7 recurrence probe 1', '5200.00', '5000.00', 375072010541207553, now(), now());
"@
  $null = Invoke-Psql -Sql $sql
  $recurrence1 = Wait-SqlValue -Sql "SELECT recurrence_count FROM delivery_issue WHERE id = $targetID;" -Want "1" -TimeoutSeconds 90 -IntervalMs 2000
  Add-Check $checks $recurrence1 "first recurrence lands on merge target (count=1)" ("target=$targetID count=" + $(Get-Scalar "SELECT recurrence_count FROM delivery_issue WHERE id = $targetID;"))
  $recurrenceRow = Get-Scalar "SELECT recurrence_count || '|' || coalesce(last_recurred_at::text,'') FROM delivery_issue WHERE id = $targetID;"
  Add-Check $checks ($recurrenceRow -match '^1\|') "last_recurred_at recorded" $recurrenceRow
  $recurRef = [int](Get-Scalar "SELECT count(*) FROM delivery_issue_ref WHERE issue_id = $targetID AND kind = 'case_run' AND ref_id = 990420 AND role = 'recurrence';")
  Add-Check $checks ($recurRef -eq 1) "recurrence ref role=recurrence on target" "rows=$recurRef"
  $sourceAfter1 = Get-Scalar "SELECT recurrence_count || '|' || status || '|' || coalesce(merged_into_id::text,'') || '|' || (SELECT count(*) FROM delivery_issue_ref WHERE issue_id = $($script:issueSource)) FROM delivery_issue WHERE id = $($script:issueSource);"
  Add-Check $checks ($sourceAfter1 -eq $sourceBefore) "merge source unchanged after first recurrence" "before=$sourceBefore after=$sourceAfter1"
  $ev1 = [int](Get-Scalar "SELECT count(*) FROM event WHERE type = 'issue.recurred' AND envelope->'payload'->>'issue_id' = '$targetID' AND envelope->'payload'->>'source_issue_id' = '$($script:issueSource)';")
  Add-Check $checks ($ev1 -ge 1) "issue.recurred event for target" "rows=$ev1"
  $aud1 = [int](Get-Scalar "SELECT count(*) FROM audit_log WHERE action = 'delivery.issue.recurred' AND metadata->>'issue_id' = '$targetID';")
  Add-Check $checks ($aud1 -ge 1) "delivery.issue.recurred audit row" "rows=$aud1"

  # A second failed case_run of the same case version in ANOTHER wave must
  # increment the same target (cross-wave dedupe stability).
  $sql2 = @"
INSERT INTO run_wave (id, version_id, status, trigger, counts, started_at, finished_at, created_at, updated_at)
VALUES (990401, $($script:v1), 'done', 'manual', '{}'::jsonb, now(), now(), now(), now());
INSERT INTO execution_intent (id, version_id, case_version_id, version_sut_ids, environment_fingerprint, code_baseline, purpose, status, dedupe_key, attempt_no, created_at, updated_at)
VALUES (990411, $($script:v1), 830002, '[]'::jsonb, '$fingerprint', '', 'manual', 'done', 'p7-acceptance-recurrence-2', 1, now(), now());
INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, attempts, failure_class, summary, started_at, finished_at, created_at, updated_at)
VALUES (990421, 990401, 990411, 830002, 'failed', 1, 'application', '{"last_error":"p7 acceptance recurrence probe 2","verdict_reason":"assertion_failed"}'::jsonb, now() - interval '1 minute', now(), now(), now());
INSERT INTO assertion_result (id, case_run_id, step_seq, assertion_seq, kind, verdict, reason, expected, actual, target_version_sut_id, created_at, updated_at)
VALUES (990431, 990421, 1, 2, 'ui', 'failed', 'p7 recurrence probe 2', '5200.00', '5000.00', 375072010541207553, now(), now());
"@
  $null = Invoke-Psql -Sql $sql2
  $recurrence2 = Wait-SqlValue -Sql "SELECT recurrence_count FROM delivery_issue WHERE id = $targetID;" -Want "2" -TimeoutSeconds 90 -IntervalMs 2000
  Add-Check $checks $recurrence2 "second recurrence across another wave (count=2)" ("count=" + $(Get-Scalar "SELECT recurrence_count FROM delivery_issue WHERE id = $targetID;"))
  $recurRef2 = [int](Get-Scalar "SELECT count(*) FROM delivery_issue_ref WHERE issue_id = $targetID AND kind = 'case_run' AND ref_id = 990421 AND role = 'recurrence';")
  Add-Check $checks ($recurRef2 -eq 1) "second recurrence ref role=recurrence" "rows=$recurRef2"
  $sourceAfter2 = Get-Scalar "SELECT recurrence_count || '|' || status || '|' || coalesce(merged_into_id::text,'') || '|' || (SELECT count(*) FROM delivery_issue_ref WHERE issue_id = $($script:issueSource)) FROM delivery_issue WHERE id = $($script:issueSource);"
  Add-Check $checks ($sourceAfter2 -eq $sourceBefore) "merge source unchanged after second recurrence" "before=$sourceBefore after=$sourceAfter2"
  $issueCountAfter = [int](Get-Scalar "SELECT count(*) FROM delivery_issue WHERE version_id = $($script:v1);")
  Add-Check $checks ($issueCountAfter -eq ($issueCountBefore + 1)) "no new issue rows created by recurrence" ("before=$issueCountBefore after=$issueCountAfter")
  $script:recurrenceTargetID = $targetID
  $script:recurrenceWaves = "990400,990401"
}

function Scenario-S12($checks) {
  $baseline = $script:s12Baseline
  if ($null -eq $baseline) { throw "S12 baseline was not captured" }
  $reportID = $baseline.ReportID
  $latest = [int]$baseline.Revision
  $oldRevID = [string]$baseline.RevisionID
  $oldRow = $baseline.Row
  $oldHtmlBefore = $baseline.HtmlPath
  $oldShaBefore = $baseline.Sha
  $versionStatusBefore = $baseline.VersionStatus
  Add-Check $checks (-not [string]::IsNullOrWhiteSpace($oldRevID)) "old version-report revision baseline captured" "revision=$latest as_of=$($oldRow.AsOf)"

  # After S11 the old revision must be byte-identical (as-of report isolation).
  $oldHtmlAfter = Join-Path $script:evidence "s12-old-after.html"
  $null = Download-ApiFile -Path "/api/versions/$($script:v1)/reports/$reportID/revisions/$latest/html" -OutFile $oldHtmlAfter
  $oldShaAfter = Get-FileSha $oldHtmlAfter
  $oldRowAfter = Get-RevisionRow -RevisionID ([int64]$oldRevID)
  Add-Check $checks ($oldShaAfter -eq $oldShaBefore) "old report HTML bytes unchanged after merge/recurrence" "sha=$oldShaBefore"
  Add-Check $checks ($oldRowAfter.AsOf -eq $oldRow.AsOf) "old report as_of unchanged after merge/recurrence" ($oldRow.AsOf + " -> " + $oldRowAfter.AsOf)
  Add-Check $checks ($oldRowAfter.Fingerprint -eq $oldRow.Fingerprint -and $oldRowAfter.HTMLRef -eq $oldRow.HTMLRef) "old report identity columns unchanged"

  # Regenerate: the new revision must reflect the current (post-merge) state.
  $regen = Invoke-Api -Method "POST" -Path "/api/versions/$($script:v1)/reports/$reportID/regenerate" -Body @{} -TimeoutSec 60
  Add-Check $checks ($regen.Status -eq 202) "version report regenerate accepted" "status=$($regen.Status)"
  $newRev = [int]$regen.Json.data.revision
  $newRevID = Get-Scalar "SELECT id FROM delivery_report_revision WHERE report_id = $reportID AND revision = $newRev;"
  $published = Wait-SqlValue -Sql "SELECT status FROM delivery_report_revision WHERE id = $newRevID;" -Want "published" -TimeoutSeconds 180 -IntervalMs 2000
  Add-Check $checks $published "new version-report revision published" "revision=$newRev"
  $newRow = Get-RevisionRow -RevisionID ([int64]$newRevID)
  Add-Check $checks ($newRow.Fingerprint -ne $oldRow.Fingerprint) "new revision input_fingerprint reflects current state" ("old=" + $oldRow.Fingerprint.Substring(0, 12) + "... new=" + $newRow.Fingerprint.Substring(0, 12) + "...")
  $newHtml = Join-Path $script:evidence "s12-new.html"
  $newStatus = Download-ApiFile -Path "/api/versions/$($script:v1)/reports/$reportID/revisions/$newRev/html" -OutFile $newHtml
  $newText = if ($newStatus -eq 200) { Get-Content $newHtml -Raw } else { "" }
  Add-Check $checks ($newStatus -eq 200 -and $newText.Contains("data-issue-id=`"$($script:recurrenceTargetID)`"")) "new report HTML includes the merged target issue" ("status=$newStatus target=$($script:recurrenceTargetID)")
  Add-Check $checks ($newStatus -eq 200 -and $newText.Contains("990420")) "new report HTML includes the recurrent failed case_run" "case_run=990420"
  $oldText = Get-Content $oldHtmlBefore -Raw
  Add-Check $checks (-not $oldText.Contains("data-issue-id=`"$($script:recurrenceTargetID)`"")) "old report HTML does not include post-merge state"
  $versionStatusAfter = Get-Scalar "SELECT status FROM version WHERE id = $($script:v1);"
  Add-Check $checks ($versionStatusAfter -eq $versionStatusBefore) "version close predicate state unchanged" ($versionStatusBefore + " -> " + $versionStatusAfter)
  $reportState = Get-Scalar "SELECT status FROM delivery_report WHERE id = $reportID;"
  Add-Check $checks ($reportState -eq "published") "version report still published" "status=$reportState"
}

# --- reporting ---

function Write-AcceptanceReport {
  $finishedAt = Get-Date
  $wall = $finishedAt - $script:startedAt
  # Persist the concrete render-work-item evidence next to the report.
  try {
    $deadRows = @(Invoke-Psql "SELECT coalesce(team,'') || '|' || coalesce(status,'') || '|' || coalesce(type,'') || '|' || coalesce(last_error,'') || '|' || count(*) FROM work_item WHERE type = 'report.render_pdf' GROUP BY team, status, type, last_error ORDER BY status;")
    if ($deadRows.Count -gt 0) {
      $bugPath = Join-Path $script:evidence "bug-render-workitems.txt"
      Set-Content -Path $bugPath -Value ("team|status|type|last_error|count`n" + ($deadRows -join "`n")) -Encoding utf8
      $deadInvalid = [int](Get-Scalar "SELECT count(*) FROM work_item WHERE type = 'report.render_pdf' AND status = 'dead' AND last_error LIKE 'invalid_work_type%';")
      if ($deadInvalid -gt 0) {
        Add-BugNote ("P7-BUG-01: the generic agent worker claims team=delivery work items and kills report.render_pdf items with 'invalid_work_type: unsupported work item type ''report.render_pdf''' before the dedicated RenderWorker can claim them. $deadInvalid dead render work item(s) observed; evidence: .tmp/p7-acceptance/bug-render-workitems.txt. This blocks the automatic PDF pipeline in S1/S2 (HTML publishing and everything else is unaffected).")
      }
    }
  } catch { }
  $scenarioLines = @()
  foreach ($result in $script:scenarioResults) {
    $scenarioLines += ("| {0} | {1} | {2}s | {3} |" -f $result.Name, $(if ($result.Pass) { "PASS" } else { "FAIL" }), $result.DurationSeconds, $result.Summary)
  }
  $bugText = if ($script:bugNotes.Count -eq 0) { "No product defects observed." } else { (@($script:bugNotes | ForEach-Object { "- " + $_ }) -join "`n") }
  $detailSections = @()
  foreach ($result in $script:scenarioResults) {
    $checkLines = @()
    foreach ($check in $result.Checks) {
      $checkLines += ("- {0} {1}{2}" -f $(if ($check.Ok) { "PASS" } else { "FAIL" }), $check.Label, $(if ([string]::IsNullOrWhiteSpace($check.Detail)) { "" } else { " :: " + $check.Detail }))
    }
    $detailSections += ("### {0} - {1}`n`n{2}`n" -f $result.Name, $(if ($result.Pass) { "PASS" } else { "FAIL" }), ($checkLines -join "`n"))
  }
  $md = @"
# EzTest 0.2 P7 API/DB acceptance (Task 21)

- Date: $(Get-Date -Format "yyyy-MM-dd HH:mm:ss zzz")
- Branch: feat/v0.2-p7 (no git commands were run)
- Database: $script:dbName cloned from $TemplateDatabase
- Content root: $script:dataRoot
- API A: http://127.0.0.1:$ApiPort ; API B: http://127.0.0.1:$ApiBPort
- Renderer: http://127.0.0.1:$RendererPort ; Executor: http://127.0.0.1:$ExecutorPort
- Real LLM: .env DeepSeek configuration stored in the cloned settings center
- Wall time: $([int]$wall.TotalSeconds)s ($($wall.ToString("hh\:mm\:ss")))
- Exit status: $(if ($script:failures.Count -eq 0) { "0 (all scenarios PASS)" } else { "1 (" + $script:failures.Count + " failing checks)" })

## Results

| Scenario | Result | Duration | Summary |
|---|---|---|---|
$($scenarioLines -join "`n")

## Product defects / blockers

$bugText

## Check detail

$($detailSections -join "`n`n")

## Commands / method

- Acceptance driver: powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p7-acceptance.ps1 (also runnable with pwsh).
- Clone: psql -d postgres -f clone.sql (DROP DATABASE IF EXISTS eztest_v02_p7 WITH (FORCE); CREATE DATABASE eztest_v02_p7 TEMPLATE eztest_v02_p6).
- Boot: GET /api/health, GET /api/health/ready, POST /api/auth/login (admin), PATCH /api/admin/settings/{key}, GET /api/versions.
- Report PDF: POST /api/versions/{v}/reports/{r}/regenerate; GET .../revisions/{n}; GET .../revisions/{n}/html; GET .../revisions/{n}/pdf; psql delivery_report_revision/work_item/event/audit_log.
- Knowledge: POST/GET /api/knowledge/items; POST .../approve|reject|revoke; GET /api/knowledge/items?q=&tag=&status=; context_packet/knowledge_usage asserted via psql.
- GC: POST /api/admin/gc/runs {mode,confirm}; GET /api/admin/gc/runs/{id}; psql content_object/content_ref/gc_tombstone/audit_log/event.
- Multi-replica: GET /api/admin/instances, POST /api/admin/instances/{id}/drain; pg_locks classid=0 objid=1163547697 (0x455A5431).
- SSE: curl -s -N "http://127.0.0.1:13344/api/stream?channel=system" -H "Authorization: Bearer ..." -H "Last-Event-ID: ...".
- Executor: GET /api/admin/executors; internal push register/heartbeat evidence in executor.log; psql case_run/execution_intent recovery assertions.
- Issues: POST /api/versions/{v}/issues/{id}/merge; psql delivery_issue/delivery_issue_ref/event/audit_log recurrence assertions.

## Evidence files

- Scenario log: .tmp/p7-acceptance/scenarios.txt
- Full log: .tmp/p7-acceptance/acceptance.log
- API logs: .tmp/p7-acceptance/api-*.log, api-b.log
- Renderer/executor logs: .tmp/p7-acceptance/renderer*.log, executor*.log
- PDF/HTML snapshots: .tmp/p7-acceptance/s1-report.pdf, s1-html-*.html, s3-*.html, s12-*.html
- SSE captures: .tmp/p7-acceptance/sse-system*.txt
- Bug notes: .tmp/p7-acceptance/bug-notes.txt
- Render work-item evidence: .tmp/p7-acceptance/bug-render-workitems.txt

## Notes

- S1/S2 exercise the automatic report.render_pdf path (reconciler -> work_item -> dedicated render worker). They are not bypassed with hand-seeded work items.
- S6/S7 GC candidates are inserted into the cloned database and the isolated content root; all timestamps are backdated deterministically.
- S9 stops API A after draining it; API B is the surviving replica for S10-S12.
- No product code, contract or other script was modified by this acceptance run.
"@
  $mdPath = Join-Path $script:docsDir "acceptance.md"
  Set-Content -Path $mdPath -Value $md -Encoding utf8
  Copy-Item -LiteralPath $script:scenariosPath -Destination (Join-Path $script:docsDir "scenarios.txt") -Force
  Write-Log "==> Evidence written to $mdPath"
  return $mdPath
}

# --- main ---

$exitCode = 1
try {
  Initialize-Environment
  Build-ApiBinary
  if (-not $SkipDatabaseReset) { Reset-Database }
  # Reset-Database runs before the clone exists; it must not query the clone settings.
  Prepare-DataRoot
  $null = Start-ApiA
  $script:token = Login-Admin
  if ([string]::IsNullOrWhiteSpace($script:token)) { throw "admin login failed on first boot" }
  $null = Wait-Ready -Port $ApiPort -Want 200 -TimeoutSeconds 60
  Configure-P7Settings
  Configure-SutFamilies
  $null = Start-Renderer
  $null = Start-Executor
  # Restart so the startup-read template version, sections, render URL and
  # executor URL settings take effect.
  Restart-ApiA
  $null = Wait-Ready -Port $ApiPort -Want 200 -TimeoutSeconds 60
  Resolve-Baseline

  Invoke-Scenario "S0" { param($checks) Scenario-S0 $checks }
  Invoke-Scenario "S1" { param($checks) Scenario-S1 $checks }
  Invoke-Scenario "S3" { param($checks) Scenario-S3 $checks }
  Invoke-Scenario "S4" { param($checks) Scenario-S4 $checks }
  Invoke-Scenario "S5" { param($checks) Scenario-S5 $checks }
  Invoke-Scenario "S6" { param($checks) Scenario-S6 $checks }
  Invoke-Scenario "S7" { param($checks) Scenario-S7 $checks }
  Invoke-Scenario "S2" { param($checks) Scenario-S2 $checks }
  Invoke-Scenario "S8" { param($checks) Scenario-S8 $checks }
  Invoke-Scenario "S9" { param($checks) Scenario-S9 $checks }
  Invoke-Scenario "S10" { param($checks) Scenario-S10 $checks }
  Invoke-Scenario "S11" { param($checks) Scenario-S11 $checks }
  Invoke-Scenario "S12" { param($checks) Scenario-S12 $checks }

  if ($script:failures.Count -eq 0) {
    Write-Log "P7 ACCEPTANCE PASSED"
    $exitCode = 0
  } else {
    Write-Log ("P7 ACCEPTANCE FAILED: " + $script:failures.Count + " check(s)")
    foreach ($failure in $script:failures) { Write-Log (" - " + $failure) }
    $exitCode = 1
  }
} catch {
  Write-Log ("P7 ACCEPTANCE ABORTED: " + $_.Exception.Message)
  Write-Log $_.ScriptStackTrace
  $script:failures.Add("FATAL: " + $_.Exception.Message)
  $exitCode = 1
} finally {
  try {
    $reportPath = Write-AcceptanceReport
  } catch {
    Write-Host ("failed to write acceptance report: " + $_.Exception.Message)
  }
  $wall = (Get-Date) - $script:startedAt
  Write-Log ("Total wall time: " + $wall.ToString("hh\:mm\:ss") + " (" + [int]$wall.TotalSeconds + "s)")
  if (-not $KeepRunning) {
    foreach ($entry in @($script:processes)) {
      Stop-OwnedProcess $entry.Process
    }
    foreach ($port in @($ApiPort, $ApiBPort, $RendererPort, $ExecutorPort)) {
      Stop-PortProcesses -Port $port
    }
  } else {
    Write-Log "KeepRunning: API/renderer/executor processes left running (pids: A=$($script:apiA.Id) B=$($script:apiB.Id) renderer=$($script:renderer.Id) executor=$($script:executor.Id))"
  }
  exit $exitCode
}

