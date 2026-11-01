#requires -Version 5.1
<#
P8 S11 capability-matrix smoke (Task 13).

Walks the 37-row 0.1 -> 0.2 capability matrix (A1-I2) and runs one
representative P8 entry check per row:

  - HTTP status / response-shape assertions against the running P8 API;
  - DB count / schema assertions against the P8 acceptance database;
  - route/page/feature file assertions for UI-only rows;
  - prior-evidence assertions (file exists + evidence marker) for rows that
    P8 does not re-exercise.

The script logs in as the seeded admin and as a freshly created normal user
(so the admin/user permission boundary is exercised). Rows not exercised by
P8 are reported as PASS(prior) with their P5/P6/P7 evidence pointer. The P9
rows C3 (D63 knowledge) and E5 (D65 evidence GC) point at the already-run
scripts/p7-acceptance.ps1 evidence (S4/S5 and S6/S7); this script does NOT
re-implement P9 behavior.

Exit codes:
  0  no FAIL row (PASS and PASS(prior) are allowed)
  1  at least one FAIL row, or the API/session preflight failed

Usage:
  pwsh scripts/p8-capability.ps1
  pwsh scripts/p8-capability.ps1 -ListOnly
  powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-capability.ps1

Defaults target the P8 acceptance layout:
  API    http://127.0.0.1:13333
  DB     eztest_v02_p8 (built from .env EZTEST_DATABASE_URL)
  Admin  admin / admin123

The script writes its textual log to .tmp/p8-capability/capability.txt
(-ListOnly writes capability-listonly.txt). It does not modify product code,
contracts, design documents or the database schema; the only DB write is the
normal user created through POST /api/admin/users for the permission smoke.
#>
[CmdletBinding()]
param(
  [string]$ApiBase = "http://127.0.0.1:13333",
  [string]$AdminUser = "admin",
  [string]$AdminPassword = "admin123",
  [string]$DatabaseName = "eztest_v02_p8",
  [string]$DatabaseUrl = "",
  [int]$ExpectedMigrations = 42,
  [int]$ExpectedSettings = 85,
  [int]$ExpectedTables = 85,
  [switch]$ListOnly
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$script:repoRoot = Split-Path -Parent $PSScriptRoot
$script:apiBase = $ApiBase.TrimEnd("/")
$script:evidenceDir = Join-Path $script:repoRoot ".tmp\p8-capability"
$script:logPath = Join-Path $script:evidenceDir $(if ($ListOnly) { "capability-listonly.txt" } else { "capability.txt" })
$script:adminToken = ""
$script:userToken = ""
$script:userUsername = ""
$script:versionId = ""
$script:sutId = ""
$script:psql = ""
$script:psqlArgs = @()
$script:databaseName = $DatabaseName
$script:results = [System.Collections.Generic.List[object]]::new()

New-Item -ItemType Directory -Force -Path $script:evidenceDir | Out-Null
Set-Content -Path $script:logPath -Encoding utf8 -Value ""

function Write-Log([string]$Message) {
  Write-Host $Message
  Add-Content -Path $script:logPath -Value $Message -Encoding utf8
}

function Expand-CheckPath([string]$Path) {
  $expanded = $Path
  $expanded = $expanded.Replace("{versionId}", [string]$script:versionId)
  $expanded = $expanded.Replace("{sutId}", [string]$script:sutId)
  return $expanded
}

function Get-RoleToken([string]$Role) {
  switch ($Role.ToLowerInvariant()) {
    "admin" { return $script:adminToken }
    "user" { return $script:userToken }
    default { return "" }
  }
}

function Invoke-Api {
  param(
    [string]$Method,
    [string]$Path,
    $Body = $null,
    [string]$Token = "",
    [int]$TimeoutSec = 30
  )
  $headers = @{}
  if (-not [string]::IsNullOrWhiteSpace($Token)) { $headers["Authorization"] = "Bearer $Token" }
  $parameters = @{
    Uri = ($script:apiBase + $Path)
    Method = $Method
    Headers = $headers
    UseBasicParsing = $true
    TimeoutSec = $TimeoutSec
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
      Status = [int]$response.StatusCode
      Json = $json
      Text = [string]$response.Content
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
    }
  }
}

function Invoke-PsqlScalar([string]$Sql) {
  if ([string]::IsNullOrWhiteSpace($script:psql)) {
    throw "psql is not available (expected .tools\pgsql\bin\psql.exe or psql.exe on PATH)"
  }
  $previous = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  try {
    $psqlArgs = $script:psqlArgs
    $output = & $script:psql @psqlArgs -c $Sql 2>&1
    $code = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $previous
  }
  if ($code -ne 0) {
    throw ("psql failed: " + (($output | Out-String).Trim()))
  }
  $lines = @($output | ForEach-Object { [string]$_ } | Where-Object { $_.Trim() -ne "" })
  if ($lines.Count -eq 0) { return "" }
  return $lines[0].Trim()
}

function Initialize-Database {
  $url = $DatabaseUrl
  if ([string]::IsNullOrWhiteSpace($url)) {
    $dotenvPath = Join-Path $script:repoRoot ".env"
    if (Test-Path -LiteralPath $dotenvPath) {
      foreach ($line in Get-Content -LiteralPath $dotenvPath -Encoding UTF8) {
        if ($line -match "^\s*EZTEST_DATABASE_URL\s*=\s*(.+?)\s*$") {
          $url = $Matches[1].Trim()
          break
        }
      }
    }
  }
  if ([string]::IsNullOrWhiteSpace($url)) {
    Write-Log "WARN: EZTEST_DATABASE_URL is missing; DB assertions will FAIL"
    return
  }
  $pattern = '^(?<scheme>postgres(?:ql)?)://(?<user>[^:/@]+)(?::(?<password>[^@]*))?@(?<host>[^:/?]+)(?::(?<port>\d+))?/(?<database>[^?]+)(?<query>\?.*)?$'
  $match = [regex]::Match($url, $pattern)
  if (-not $match.Success) {
    Write-Log "WARN: EZTEST_DATABASE_URL is not a supported postgres URL; DB assertions will FAIL"
    return
  }
  $user = [uri]::UnescapeDataString($match.Groups["user"].Value)
  $password = [uri]::UnescapeDataString($match.Groups["password"].Value)
  $hostName = $match.Groups["host"].Value
  $port = if ($match.Groups["port"].Success) { $match.Groups["port"].Value } else { "5432" }
  $database = if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) { $DatabaseName } else { $match.Groups["database"].Value }
  $query = if ($match.Groups["query"].Success) { $match.Groups["query"].Value } else { "" }
  $script:databaseName = $database
  $env:PGPASSWORD = $password
  $env:PGCLIENTENCODING = "UTF8"
  if ($query -match "sslmode=disable") { $env:PGSSLMODE = "disable" }
  $script:psqlArgs = @("-h", $hostName, "-p", $port, "-U", $user, "-d", $database, "-tA", "-F", "|", "-v", "ON_ERROR_STOP=1")
  $candidate = Join-Path $script:repoRoot ".tools\pgsql\bin\psql.exe"
  if (Test-Path -LiteralPath $candidate) {
    $script:psql = $candidate
  } else {
    $command = Get-Command psql.exe -ErrorAction SilentlyContinue
    if ($null -ne $command) { $script:psql = $command.Source }
  }
  if ([string]::IsNullOrWhiteSpace($script:psql)) {
    Write-Log "WARN: psql not found; DB assertions will FAIL"
  }
  Write-Log ("    database: {0} on {1}:{2} (psql={3})" -f $database, $hostName, $port, $(if ($script:psql) { $script:psql } else { "missing" }))
}

function Initialize-ApiSession {
  Write-Log "==> Preflight"
  $health = Invoke-Api -Method "GET" -Path "/api/health" -Token ""
  if ($health.Status -ne 200) {
    throw ("API health preflight failed: status={0} body={1}" -f $health.Status, $health.Text)
  }
  Write-Log ("    api: {0} (health=200)" -f $script:apiBase)

  $adminLogin = Invoke-Api -Method "POST" -Path "/api/auth/login" -Body @{ username = $AdminUser; password = $AdminPassword } -Token ""
  if ($adminLogin.Status -ne 200 -or $null -eq $adminLogin.Json -or $null -eq $adminLogin.Json.data -or [string]::IsNullOrWhiteSpace([string]$adminLogin.Json.data.token)) {
    throw ("admin login failed: status={0} body={1}" -f $adminLogin.Status, $adminLogin.Text)
  }
  $script:adminToken = [string]$adminLogin.Json.data.token
  Write-Log ("    admin login=200 user={0}" -f $AdminUser)

  $created = $null
  for ($attempt = 0; $attempt -lt 3; $attempt++) {
    $suffix = [guid]::NewGuid().ToString("N").Substring(0, 8)
    $username = "p8cap_$suffix"
    $password = "P8cap!$suffix"
    $createdResponse = Invoke-Api -Method "POST" -Path "/api/admin/users" -Body @{
      username = $username
      display_name = "P8 capability smoke"
      password = $password
    } -Token $script:adminToken
    if ($createdResponse.Status -eq 201) {
      $created = [pscustomobject]@{ Username = $username; Password = $password; Id = [string]$createdResponse.Json.data.id }
      break
    }
    if ($createdResponse.Status -ne 409) {
      throw ("normal user creation failed: status={0} body={1}" -f $createdResponse.Status, $createdResponse.Text)
    }
  }
  if ($null -eq $created) {
    throw "normal user creation failed after 3 username attempts"
  }
  $script:userUsername = $created.Username
  $userLogin = Invoke-Api -Method "POST" -Path "/api/auth/login" -Body @{ username = $created.Username; password = $created.Password } -Token ""
  if ($userLogin.Status -ne 200 -or $null -eq $userLogin.Json -or $null -eq $userLogin.Json.data -or [string]::IsNullOrWhiteSpace([string]$userLogin.Json.data.token)) {
    throw ("normal user login failed: status={0} body={1}" -f $userLogin.Status, $userLogin.Text)
  }
  $script:userToken = [string]$userLogin.Json.data.token
  Write-Log ("    normal user created+login=200 user={0}" -f $script:userUsername)

  $versions = Invoke-Api -Method "GET" -Path "/api/versions?limit=1" -Token $script:adminToken
  if ($versions.Status -eq 200 -and $null -ne $versions.Json -and $null -ne $versions.Json.data -and @($versions.Json.data).Count -gt 0) {
    $script:versionId = [string]$versions.Json.data[0].id
    Write-Log ("    version: {0}" -f $script:versionId)
  } else {
    Write-Log ("WARN: no version found (status={0}); version-scoped rows will FAIL" -f $versions.Status)
  }

  $suts = Invoke-Api -Method "GET" -Path "/api/resources/suts" -Token $script:adminToken
  if ($suts.Status -eq 200 -and $null -ne $suts.Json -and $null -ne $suts.Json.data -and @($suts.Json.data).Count -gt 0) {
    $script:sutId = [string]$suts.Json.data[0].id
    Write-Log ("    sut: {0}" -f $script:sutId)
  } else {
    Write-Log ("WARN: no SUT found (status={0}); SUT-scoped rows will FAIL" -f $suts.Status)
  }
}

function New-CapabilityRow {
  param(
    [string]$Id,
    [string]$Capability,
    [string]$Entry,
    [string]$Check,
    [string]$Prior,
    [array]$Requests = @(),
    [array]$Files = @(),
    [array]$Db = @(),
    [array]$Evidence = @(),
    [bool]$PriorOnly = $false,
    [bool]$P9 = $false,
    [string]$Special = ""
  )
  return [pscustomobject]@{
    Id = $Id
    Capability = $Capability
    Entry = $Entry
    Check = $Check
    Prior = $Prior
    Requests = $Requests
    Files = $Files
    Db = $Db
    Evidence = $Evidence
    PriorOnly = $PriorOnly
    P9 = $P9
    Special = $Special
  }
}

function Test-HttpRequest($Request) {
  $role = if ($Request.ContainsKey("Role")) { [string]$Request.Role } else { "admin" }
  $method = if ($Request.ContainsKey("Method")) { [string]$Request.Method } else { "GET" }
  $path = Expand-CheckPath ([string]$Request.Path)
  $expected = [int]$Request.Status
  $label = if ($Request.ContainsKey("Label")) { [string]$Request.Label } else { "$method $path" }
  $token = Get-RoleToken $role
  if ($role -ne "none" -and [string]::IsNullOrWhiteSpace($token)) {
    return [pscustomobject]@{ Ok = $false; Label = $label; Detail = "no token for role $role" }
  }
  try {
    $body = $null
    if ($Request.ContainsKey("Body")) { $body = $Request.Body }
    $response = Invoke-Api -Method $method -Path $path -Body $body -Token $token
  } catch {
    return [pscustomobject]@{ Ok = $false; Label = $label; Detail = ("request threw: " + $_.Exception.Message) }
  }
  $ok = ($response.Status -eq $expected)
  $detail = "status=$($response.Status) expected=$expected"
  if ($ok -and $Request.ContainsKey("Contains")) {
    $needle = [string]$Request.Contains
    $ok = $response.Text.Contains($needle)
    $detail += " contains=$needle"
  }
  if ($ok -and ($Request.ContainsKey("DataMin") -or $Request.ContainsKey("DataCount"))) {
    $json = $response.Json
    if ($null -eq $json -or $null -eq $json.data) {
      $ok = $false
      $detail += " data=missing"
    } else {
      $count = @($json.data).Count
      if ($Request.ContainsKey("DataMin") -and $count -lt [int]$Request.DataMin) {
        $ok = $false
      }
      if ($Request.ContainsKey("DataCount") -and $count -ne [int]$Request.DataCount) {
        $ok = $false
      }
      $detail += " data_count=$count"
      if ($Request.ContainsKey("DataMin")) { $detail += " min=$($Request.DataMin)" }
      if ($Request.ContainsKey("DataCount")) { $detail += " expected_count=$($Request.DataCount)" }
    }
  }
  return [pscustomobject]@{ Ok = $ok; Label = $label; Detail = $detail }
}

function Test-FileCheck([string]$RelativePath) {
  $full = Join-Path $script:repoRoot $RelativePath
  $ok = Test-Path -LiteralPath $full
  return [pscustomobject]@{
    Ok = $ok
    Label = "file: $RelativePath"
    Detail = $(if ($ok) { "exists" } else { "missing" })
  }
}

function Test-DbCheck($DbCheck) {
  $label = if ($DbCheck.ContainsKey("Label")) { [string]$DbCheck.Label } else { "db assertion" }
  try {
    $actual = Invoke-PsqlScalar ([string]$DbCheck.Sql)
  } catch {
    return [pscustomobject]@{ Ok = $false; Label = $label; Detail = ("psql error: " + $_.Exception.Message) }
  }
  $expected = [string]$DbCheck.Expect
  $ok = ($actual -eq $expected)
  return [pscustomobject]@{
    Ok = $ok
    Label = $label
    Detail = "actual=$actual expected=$expected"
  }
}

function Test-EvidenceCheck($Evidence) {
  $relative = [string]$Evidence.Path
  $full = Join-Path $script:repoRoot ($relative.Replace("/", "\"))
  if (-not (Test-Path -LiteralPath $full)) {
    return [pscustomobject]@{ Ok = $false; Label = "evidence: $relative"; Detail = "missing" }
  }
  $detail = "exists"
  if ($Evidence.ContainsKey("Marker")) {
    $marker = [string]$Evidence.Marker
    try {
      $text = [System.IO.File]::ReadAllText($full)
      $found = $text.Contains($marker)
      return [pscustomobject]@{
        Ok = $found
        Label = "evidence: $relative"
        Detail = "marker=$marker found=$found"
      }
    } catch {
      return [pscustomobject]@{ Ok = $false; Label = "evidence: $relative"; Detail = ("read error: " + $_.Exception.Message) }
    }
  }
  return [pscustomobject]@{ Ok = $true; Label = "evidence: $relative"; Detail = $detail }
}

function Add-RowCheck($Checks, [bool]$Ok, [string]$Label, [string]$Detail = "") {
  $Checks.Add([pscustomobject]@{ Ok = $Ok; Label = $Label; Detail = $Detail })
}

function Test-CapabilityRow($Row) {
  $checks = [System.Collections.Generic.List[object]]::new()
  try {
    if ($Row.Special -eq "auth") {
      Add-RowCheck $checks (-not [string]::IsNullOrWhiteSpace($script:adminToken)) "admin token present" ""
      Add-RowCheck $checks (-not [string]::IsNullOrWhiteSpace($script:userToken)) "normal user token present" ("user=" + $script:userUsername)
    }
    foreach ($request in @($Row.Requests)) {
      if ($null -eq $request) { continue }
      $checks.Add((Test-HttpRequest $request))
    }
    foreach ($relative in @($Row.Files)) {
      if ([string]::IsNullOrWhiteSpace([string]$relative)) { continue }
      $checks.Add((Test-FileCheck ([string]$relative)))
    }
    foreach ($dbCheck in @($Row.Db)) {
      if ($null -eq $dbCheck) { continue }
      $checks.Add((Test-DbCheck $dbCheck))
    }
    foreach ($evidence in @($Row.Evidence)) {
      if ($null -eq $evidence) { continue }
      $checks.Add((Test-EvidenceCheck $evidence))
    }
    if ($Row.P9) {
      Add-RowCheck $checks $true "P9 behavior references p7-acceptance.ps1 (not re-run in S11)" "S4/S5 for C3; S6/S7 for E5"
    }
    if ($checks.Count -eq 0) {
      Add-RowCheck $checks $false "row has no check" ""
    }
  } catch {
    Add-RowCheck $checks $false "row threw" $_.Exception.Message
  }
  $failed = @($checks | Where-Object { -not $_.Ok })
  $result = "PASS"
  if ($failed.Count -gt 0) {
    $result = "FAIL"
  } elseif ($Row.PriorOnly) {
    $result = "PASS(prior)"
  }
  return [pscustomobject]@{
    Row = $Row
    Result = $result
    Checks = $checks
    Failed = $failed
  }
}

function Get-ResultTable($Results) {
  $tableRows = foreach ($result in $Results) {
    [pscustomobject]@{
      ID = $result.Row.Id
      Capability = $result.Row.Capability
      Entry = $result.Row.Entry
      Check = $result.Row.Check
      Prior = $result.Row.Prior
      Result = $result.Result
    }
  }
  return ($tableRows | Format-Table @(
      @{ Label = "ID"; Expression = { $_.ID }; Width = 4 },
      @{ Label = "Capability"; Expression = { $_.Capability }; Width = 20 },
      @{ Label = "Entry(API/UI)"; Expression = { $_.Entry }; Width = 36 },
      @{ Label = "P8 check"; Expression = { $_.Check }; Width = 58 },
      @{ Label = "Prior evidence"; Expression = { $_.Prior }; Width = 44 },
      @{ Label = "Result"; Expression = { $_.Result }; Width = 11 }
    ) -Wrap | Out-String -Width 260)
}

function Get-ExpectedIds {
  return @(
    "A1", "A2", "A3", "A4", "A5", "A6",
    "B1", "B2", "B3", "B4",
    "C1", "C2", "C3",
    "D1", "D2", "D3", "D4",
    "E1", "E2", "E3", "E4", "E5",
    "F1", "F2", "F3",
    "G1", "G2", "G3", "G4",
    "H1", "H2", "H3", "H4", "H5", "H6",
    "I1", "I2"
  )
}

# ---------------------------------------------------------------------------
# 37-row capability check table (A1-I2).
# Each row carries exactly one representative P8 check plus the prior evidence
# path. PriorOnly rows are not re-exercised by P8 and are reported as
# PASS(prior); P9 rows point at scripts/p7-acceptance.ps1 evidence.
# ---------------------------------------------------------------------------

$rowDefinitions = @(
  @{
    Id = "A1"; Capability = "身份/用户/权限"
    Entry = "API /api/auth/*, /api/admin/users/*; UI /login"
    Check = "admin+normal user login 200; /api/auth/me 200; normal user GET /api/admin/users 403; login page exists"
    Prior = "P0 phase-00-handoff.md; P6 acceptance/p6/acceptance.md"
    Special = "auth"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/auth/me"; Status = 200; Label = "admin GET /api/auth/me" }
      @{ Role = "user"; Method = "GET"; Path = "/api/auth/me"; Status = 200; Label = "user GET /api/auth/me" }
      @{ Role = "user"; Method = "GET"; Path = "/api/admin/users"; Status = 403; Label = "user GET /api/admin/users forbidden" }
    )
    Files = @("frontend/src/app/login/page.tsx", "frontend/src/features/auth/LoginPage.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-00-handoff.md" }
      @{ Path = "docs/version-V0.2/acceptance/p6/acceptance.md" }
    )
  }
  @{
    Id = "A2"; Capability = "LLM 配置/网关"
    Entry = "API /api/admin/llm/configs, /api/admin/llm/models; UI /settings"
    Check = "admin configs=200 with >=1 config; models=200; LLM settings panel exists"
    Prior = "P0 phase-00-handoff.md; P6 acceptance S0 real config test"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/admin/llm/configs"; Status = 200; DataMin = 1; Label = "admin LLM configs" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/admin/llm/models"; Status = 200; Label = "admin LLM models" }
    )
    Files = @("frontend/src/app/(app)/settings/page.tsx", "frontend/src/features/settings/LLMSettings.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-00-handoff.md" }
      @{ Path = "docs/version-V0.2/acceptance/p6/acceptance.md" }
    )
  }
  @{
    Id = "A3"; Capability = "事件与 SSE"
    Entry = "API /api/events, /api/stream; version detail SSE"
    Check = "GET /api/events?channel=system&limit=1 = 200 and contains snapshot_cursor"
    Prior = "P0 phase-00-handoff.md; P7 acceptance S8 cross-replica SSE"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/events?channel=system&limit=1"; Status = 200; Contains = "snapshot_cursor"; Label = "system event snapshot" }
    )
    Files = @("frontend/src/features/clarification/useVersionEvents.ts")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-00-handoff.md" }
      @{ Path = "docs/version-V0.2/acceptance/p7/acceptance.md"; Marker = "### S8" }
    )
  }
  @{
    Id = "A4"; Capability = "文件/证据存储"
    Entry = "API /api/content/*, /api/versions/{id}/evidence; UI execution evidence"
    Check = "GET version evidence=200; EvidenceViewer exists"
    Prior = "P0 phase-00-handoff.md; P3 phase-03-handoff.md; P5 phase-05-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/evidence?limit=1"; Status = 200; Label = "version evidence list" }
    )
    Files = @("frontend/src/features/execution/EvidenceViewer.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-00-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-03-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-05-handoff.md" }
    )
  }
  @{
    Id = "A5"; Capability = "审计/幂等/限流"
    Entry = "API /api/admin/audit-logs; DB audit_log; rate-limit middleware"
    Check = "admin audit-logs=200; normal user=403; audit_log non-empty (rate-limit 429 covered by P8 S10)"
    Prior = "P0 phase-00-handoff.md; P6 phase-06-handoff.md; P8 S10 hardening (p8-resilience.ps1)"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/admin/audit-logs?limit=1"; Status = 200; Label = "admin audit log" }
      @{ Role = "user"; Method = "GET"; Path = "/api/admin/audit-logs?limit=1"; Status = 403; Label = "user audit log forbidden" }
    )
    Files = @("frontend/src/features/settings/AuditPanel.tsx")
    Db = @(
      @{ Label = "audit_log non-empty"; Sql = "SELECT CASE WHEN count(*) > 0 THEN 'ok' ELSE 'empty' END FROM audit_log;"; Expect = "ok" }
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-00-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-06-handoff.md" }
    )
  }
  @{
    Id = "A6"; Capability = "配置/迁移/部署"
    Entry = "API /api/health, /api/admin/settings; DB migrations/settings; deploy/"
    Check = "health=200 version 0.2.0-p8; settings=85; migrations=42; business tables=85; deploy/README exists"
    Prior = "P7 phase-07-handoff.md; P8 contracts/README.md; p8-contract-check.cjs"
    Requests = @(
      @{ Role = "none"; Method = "GET"; Path = "/api/health"; Status = 200; Contains = "0.2.0-p8"; Label = "health version 0.2.0-p8" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/admin/settings"; Status = 200; DataCount = 85; Label = "settings count 85" }
    )
    Files = @("deploy/README.md", "backend/internal/db/migrations/order.txt")
    Db = @(
      @{ Label = "migrations count"; Sql = "SELECT count(*) FROM schema_migrations;"; Expect = "42" }
      @{ Label = "settings count"; Sql = "SELECT count(*) FROM system_setting;"; Expect = "85" }
      @{ Label = "business table count"; Sql = "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE' AND table_name <> 'schema_migrations';"; Expect = "85" }
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-07-handoff.md" }
      @{ Path = "docs/version-V0.2/contracts/README.md"; Marker = "0.2.0-p8" }
    )
  }
  @{
    Id = "B1"; Capability = "系统/环境/应用模型"
    Entry = "API /api/resources/suts, /api/resources/catalog; UI /resources"
    Check = "SUT list=200 with >=1 SUT; catalog=200; resources page exists"
    Prior = "P2 phase-02-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/resources/suts"; Status = 200; DataMin = 1; Label = "SUT list" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/resources/catalog"; Status = 200; Label = "resources catalog" }
    )
    Files = @("frontend/src/app/(app)/resources/page.tsx", "frontend/src/features/resources/ResourcesPage.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-02-handoff.md" }
    )
  }
  @{
    Id = "B2"; Capability = "账号/Session/登录配方"
    Entry = "API /api/resources/suts/{id}/accounts; UI resources accounts"
    Check = "GET SUT accounts=200; SUT/DataAccess panels exist"
    Prior = "P2 phase-02-handoff.md; P6 capability-parity B2"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/resources/suts/{sutId}/accounts"; Status = 200; Label = "SUT accounts" }
    )
    Files = @("frontend/src/features/resources/SUTPanel.tsx", "frontend/src/features/resources/DataAccessPanel.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-02-handoff.md" }
      @{ Path = "docs/version-V0.2/acceptance/p6/capability-parity.md"; Marker = "| B2 |" }
    )
  }
  @{
    Id = "B3"; Capability = "只读 DB 断言"
    Entry = "API /api/resources/suts/{id}/databases; executor DB assertion"
    Check = "entry smoke: SUT databases=200; behavior not re-run by S11"
    Prior = "P3 phase-03-handoff.md; P6 capability-parity B3 (P3 evidence)"
    PriorOnly = $true
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/resources/suts/{sutId}/databases"; Status = 200; Label = "SUT target databases" }
    )
    Files = @("frontend/src/features/resources/DataAccessPanel.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-03-handoff.md" }
      @{ Path = "docs/version-V0.2/acceptance/p6/capability-parity.md"; Marker = "| B3 |" }
    )
  }
  @{
    Id = "B4"; Capability = "本地 Git/代码智能"
    Entry = "API /api/versions/{id}/code/*; UI code tab"
    Check = "entry smoke: version code repos=200; CodeTab exists; behavior not re-run by S11"
    Prior = "P5 phase-05-handoff.md (13/13 PASS); P6 capability-parity B4"
    PriorOnly = $true
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/code/repos"; Status = 200; Label = "version code repos" }
    )
    Files = @("frontend/src/features/code-intel/CodeTab.tsx", "frontend/src/features/code-intel/components/ImpactPanel.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-05-handoff.md"; Marker = "13/13" }
      @{ Path = "docs/version-V0.2/acceptance/p6/capability-parity.md"; Marker = "| B4 |" }
    )
  }
  @{
    Id = "C1"; Capability = "版本生命周期"
    Entry = "API /api/versions/*; UI /versions and version detail"
    Check = "versions=200 with >=1 version; version detail=200; versions pages exist"
    Prior = "P1 phase-01-handoff.md; P4 phase-04-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions?limit=1"; Status = 200; DataMin = 1; Label = "version list" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}"; Status = 200; Label = "version detail" }
    )
    Files = @("frontend/src/app/(app)/versions/page.tsx", "frontend/src/features/versions/VersionDetailPage.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-01-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-04-handoff.md" }
    )
  }
  @{
    Id = "C2"; Capability = "需求/澄清/关系/附件"
    Entry = "API /api/versions/{id}/requirements, /conversations; UI requirements tab"
    Check = "requirements=200; conversations=200; requirements/chat/sidebar/attachment UI exists"
    Prior = "P1 phase-01-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/requirements?limit=1"; Status = 200; Label = "requirements list" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/conversations"; Status = 200; Label = "conversations list" }
    )
    Files = @(
      "frontend/src/features/clarification/RequirementsTab.tsx"
      "frontend/src/features/clarification/ChatPanel.tsx"
      "frontend/src/features/clarification/RequirementSidebar.tsx"
      "frontend/src/features/clarification/AttachmentUploader.tsx"
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-01-handoff.md" }
    )
  }
  @{
    Id = "C3"; Capability = "知识库（P9 D63）"
    Entry = "API /api/knowledge/items; UI /knowledge"
    Check = "entry smoke: admin knowledge=200; behavior uses p7-acceptance S4/S5 (not re-run)"
    Prior = "P7 scripts/p7-acceptance.ps1 S4/S5; acceptance/p7/acceptance.md"
    PriorOnly = $true
    P9 = $true
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/knowledge/items?limit=1"; Status = 200; Label = "knowledge items" }
    )
    Files = @("frontend/src/app/(app)/knowledge/page.tsx", "frontend/src/features/knowledge/KnowledgePage.tsx")
    Evidence = @(
      @{ Path = "scripts/p7-acceptance.ps1"; Marker = "Scenario-S4" }
      @{ Path = "scripts/p7-acceptance.ps1"; Marker = "Scenario-S5" }
      @{ Path = "docs/version-V0.2/acceptance/p7/acceptance.md"; Marker = "### S4" }
      @{ Path = "docs/version-V0.2/acceptance/p7/acceptance.md"; Marker = "### S5" }
      @{ Path = "docs/version-V0.2/acceptance/p7/scenarios.txt"; Marker = "S4 | PASS" }
      @{ Path = "docs/version-V0.2/acceptance/p7/scenarios.txt"; Marker = "S5 | PASS" }
    )
  }
  @{
    Id = "D1"; Capability = "用例脑图模型"
    Entry = "API /api/versions/{id}/design/graph; UI design tab"
    Check = "design graph=200; DesignTab/DesignMindmap exist"
    Prior = "P2 phase-02-handoff.md; P5 phase-05-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/design/graph"; Status = 200; Label = "design graph" }
    )
    Files = @("frontend/src/features/design/DesignTab.tsx", "frontend/src/features/design/DesignMindmap.tsx", "frontend/src/features/design/CaseDetailPanel.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-02-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-05-handoff.md" }
    )
  }
  @{
    Id = "D2"; Capability = "设计生成/评审"
    Entry = "API /api/versions/{id}/design/overview; UI DesignChat"
    Check = "design overview=200; DesignChat exists"
    Prior = "P2 phase-02-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/design/overview"; Status = 200; Label = "design overview" }
    )
    Files = @("frontend/src/features/design/DesignTab.tsx", "frontend/src/features/design/DesignChat.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-02-handoff.md" }
    )
  }
  @{
    Id = "D3"; Capability = "视觉断言"
    Entry = "API /api/quality/visual-metrics, /api/versions/{id}/visual/overview; UI visual"
    Check = "entry smoke: visual metrics/overview=200; VisualView exists; behavior not re-run by S11"
    Prior = "P5 phase-05-handoff.md (13/13 PASS); P6 capability-parity D3"
    PriorOnly = $true
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/quality/visual-metrics?versionId={versionId}"; Status = 200; Label = "visual metrics" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/visual/overview"; Status = 200; Label = "visual overview" }
    )
    Files = @(
      "frontend/src/features/visual/VisualView.tsx"
      "frontend/src/features/visual/VisualQualityPage.tsx"
      "frontend/src/features/visual/components/VisualDiffViewer.tsx"
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-05-handoff.md"; Marker = "13/13" }
      @{ Path = "docs/version-V0.2/acceptance/p6/capability-parity.md"; Marker = "| D3 |" }
    )
  }
  @{
    Id = "D4"; Capability = "脑图导出"
    Entry = "API /api/versions/{id}/exports; UI ExportCasesDialog"
    Check = "exports=200; export dialog/download helpers exist"
    Prior = "P2 phase-02-handoff.md; P5 phase-05-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/exports?limit=1"; Status = 200; Label = "exports list" }
    )
    Files = @("frontend/src/features/design/ExportCasesDialog.tsx", "frontend/src/features/design/download.ts")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-02-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-05-handoff.md" }
    )
  }
  @{
    Id = "E1"; Capability = "批次/调度/并发（波次）"
    Entry = "API /api/versions/{id}/waves, /execution/overview; UI execution tab"
    Check = "waves=200; execution overview=200; WaveList/ExecutionToolbar exist"
    Prior = "P3 phase-03-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/waves?limit=1"; Status = 200; Label = "wave list" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/execution/overview"; Status = 200; Label = "execution overview" }
    )
    Files = @("frontend/src/features/execution/WaveList.tsx", "frontend/src/features/execution/ExecutionToolbar.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-03-handoff.md" }
    )
  }
  @{
    Id = "E2"; Capability = "执行/浏览器/证据"
    Entry = "API /execution/overview, /evidence; UI ExecutionTab/EvidenceViewer"
    Check = "execution overview=200; evidence=200; execution/evidence UI exists"
    Prior = "P3 phase-03-handoff.md; P5 phase-05-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/execution/overview"; Status = 200; Label = "execution overview" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/evidence?limit=1"; Status = 200; Label = "execution evidence" }
    )
    Files = @("frontend/src/features/execution/ExecutionTab.tsx", "frontend/src/features/execution/EvidenceViewer.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-03-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-05-handoff.md" }
    )
  }
  @{
    Id = "E3"; Capability = "暂停/恢复/检查点"
    Entry = "API /api/versions/{id}/waves/{waveId}/pause|resume; UI PauseDialog"
    Check = "entry smoke: execution overview=200; PauseDialog exists; pause/resume behavior is P8 S5/S9, not re-run by S11"
    Prior = "P3 phase-03-handoff.md; P8 S5/S9 (p8-acceptance.ps1/p8-resilience.ps1)"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/execution/overview"; Status = 200; Label = "execution overview" }
    )
    Files = @("frontend/src/features/execution/PauseDialog.tsx", "frontend/src/features/execution/PausedBanner.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-03-handoff.md" }
    )
  }
  @{
    Id = "E4"; Capability = "失败分析/补全"
    Entry = "API /versions/{id}/issues; agent failure/coverage tasks; UI issues tab"
    Check = "entry smoke: version issues=200; IssuesTab exists; behavior not re-run by S11"
    Prior = "P3 phase-03-handoff.md; P4 phase-04-handoff.md; P6 capability-parity E4"
    PriorOnly = $true
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/issues?limit=1"; Status = 200; Label = "version issues" }
    )
    Files = @("frontend/src/features/delivery/IssuesTab.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-03-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-04-handoff.md" }
      @{ Path = "docs/version-V0.2/acceptance/p6/capability-parity.md"; Marker = "| E4 |" }
    )
  }
  @{
    Id = "E5"; Capability = "证据 GC（P9 D65）"
    Entry = "API /api/admin/gc/runs; UI settings GC panel"
    Check = "entry smoke: admin gc runs=200; GCPanel exists; behavior uses p7-acceptance S6/S7 (not re-run)"
    Prior = "P7 scripts/p7-acceptance.ps1 S6/S7; acceptance/p7/acceptance.md"
    PriorOnly = $true
    P9 = $true
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/admin/gc/runs?limit=1"; Status = 200; Label = "GC runs list" }
    )
    Files = @("frontend/src/features/settings/GCPanel.tsx")
    Db = @(
      @{ Label = "GC tables exist"; Sql = "SELECT CASE WHEN to_regclass('public.gc_run') IS NOT NULL AND to_regclass('public.gc_tombstone') IS NOT NULL THEN 'ok' ELSE 'missing' END;"; Expect = "ok" }
    )
    Evidence = @(
      @{ Path = "scripts/p7-acceptance.ps1"; Marker = "Scenario-S6" }
      @{ Path = "scripts/p7-acceptance.ps1"; Marker = "Scenario-S7" }
      @{ Path = "docs/version-V0.2/acceptance/p7/acceptance.md"; Marker = "### S6" }
      @{ Path = "docs/version-V0.2/acceptance/p7/acceptance.md"; Marker = "### S7" }
      @{ Path = "docs/version-V0.2/acceptance/p7/scenarios.txt"; Marker = "S6 | PASS" }
      @{ Path = "docs/version-V0.2/acceptance/p7/scenarios.txt"; Marker = "S7 | PASS" }
    )
  }
  @{
    Id = "F1"; Capability = "问题"
    Entry = "API /api/versions/{id}/issues, /api/issues; UI issues tab"
    Check = "version issues=200; global issues=200; IssuesTab/GlobalIssuesPage exist"
    Prior = "P4 phase-04-handoff.md; P6 phase-06-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/issues?limit=1"; Status = 200; Label = "version issues" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/issues?limit=1"; Status = 200; Label = "global issues" }
    )
    Files = @("frontend/src/features/delivery/IssuesTab.tsx", "frontend/src/features/delivery/GlobalIssuesPage.tsx", "frontend/src/app/(app)/issues/page.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-04-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-06-handoff.md" }
    )
  }
  @{
    Id = "F2"; Capability = "报告（HTML/PDF）"
    Entry = "API /api/versions/{id}/reports, /api/reports; UI reports tab"
    Check = "version reports=200; global reports=200; ReportsTab exists; PDF delegates to P7 S1/S3 prior evidence"
    Prior = "P4 phase-04-handoff.md; P6 phase-06-handoff.md; P7 S1/S3 PDF"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/reports?limit=1"; Status = 200; Label = "version reports" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/reports?limit=1"; Status = 200; Label = "global reports" }
    )
    Files = @("frontend/src/features/delivery/ReportsTab.tsx", "frontend/src/features/delivery/ReportDetailPanel.tsx", "frontend/src/app/(app)/reports/page.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-04-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-06-handoff.md" }
      @{ Path = "docs/version-V0.2/acceptance/p7/acceptance.md"; Marker = "### S1" }
      @{ Path = "docs/version-V0.2/acceptance/p7/acceptance.md"; Marker = "### S3" }
    )
  }
  @{
    Id = "F3"; Capability = "全局问题/报告中心"
    Entry = "API /api/issues, /api/reports; UI /issues, /reports"
    Check = "global issues=200; global reports=200; both pages exist"
    Prior = "P6 phase-06-handoff.md; acceptance/p6/acceptance.md S5"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/issues?limit=1"; Status = 200; Label = "global issues" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/reports?limit=1"; Status = 200; Label = "global reports" }
    )
    Files = @("frontend/src/app/(app)/issues/page.tsx", "frontend/src/app/(app)/reports/page.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-06-handoff.md" }
      @{ Path = "docs/version-V0.2/acceptance/p6/acceptance.md"; Marker = "S5" }
    )
  }
  @{
    Id = "G1"; Capability = "Agent profile/session/run/message"
    Entry = "API /api/agent-center/runs; DB agent_run; team-room read model"
    Check = "agent runs=200; version team room=200"
    Prior = "P1 phase-01-handoff.md; P6 phase-06-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/agent-center/runs?limit=1"; Status = 200; Label = "agent runs" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/team-rooms/clarification?limit=1"; Status = 200; Label = "clarification team room" }
    )
    Files = @("frontend/src/features/agent-center/AgentCenterPage.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-01-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-06-handoff.md" }
    )
  }
  @{
    Id = "G2"; Capability = "Agent 中心/评测"
    Entry = "API /api/agent-center/catalog, /api/quality/*; UI /agents, /quality"
    Check = "entry smoke: catalog=200 >=1, quality overview=200, eval-runs=200; pages exist; behavior not re-run by S11"
    Prior = "P6 phase-06-handoff.md; acceptance/p6/acceptance.md S1/S4/S7; p6 screenshots"
    PriorOnly = $true
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/agent-center/catalog"; Status = 200; DataMin = 1; Label = "agent catalog" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/quality/overview"; Status = 200; Label = "quality overview" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/quality/eval-runs?limit=1"; Status = 200; Label = "eval runs" }
    )
    Files = @(
      "frontend/src/app/(app)/agents/page.tsx"
      "frontend/src/features/agent-center/AgentCenterPage.tsx"
      "frontend/src/app/(app)/quality/page.tsx"
      "frontend/src/features/quality/QualityPage.tsx"
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-06-handoff.md"; Marker = "S0" }
      @{ Path = "docs/version-V0.2/acceptance/p6/acceptance.md"; Marker = "S1" }
      @{ Path = "docs/version-V0.2/acceptance/p6/capability-parity.md"; Marker = "| G2 |" }
    )
  }
  @{
    Id = "G3"; Capability = "任务板/团队频道/上下文"
    Entry = "DB work_item/team_room_post/team_room_digest/context_packet; API team-rooms"
    Check = "team room=200; work_item/team_room_*/context_packet tables exist"
    Prior = "P1 phase-01-handoff.md; P6 phase-06-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/team-rooms/clarification?limit=1"; Status = 200; Label = "clarification team room" }
    )
    Files = @("frontend/src/features/clarification/TeamRoomPanel.tsx")
    Db = @(
      @{ Label = "task board/context tables exist"; Sql = "SELECT CASE WHEN to_regclass('public.work_item') IS NOT NULL AND to_regclass('public.team_room_post') IS NOT NULL AND to_regclass('public.team_room_digest') IS NOT NULL AND to_regclass('public.context_packet') IS NOT NULL THEN 'ok' ELSE 'missing' END;"; Expect = "ok" }
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-01-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-06-handoff.md" }
    )
  }
  @{
    Id = "G4"; Capability = "LLM 路由/预算/成本"
    Entry = "API /api/agents/profiles, /api/agent-center/catalog; settings"
    Check = "agent profiles=200 >=1; catalog=200"
    Prior = "P0 phase-00-handoff.md; P1 phase-01-handoff.md"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/agents/profiles"; Status = 200; DataMin = 1; Label = "agent profiles" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/agent-center/catalog"; Status = 200; Label = "agent catalog" }
    )
    Files = @("frontend/src/features/agent-center/ProfileEditor.tsx", "frontend/src/features/settings/LLMSettings.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-00-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-01-handoff.md" }
    )
  }
  @{
    Id = "H1"; Capability = "应用壳/版本详情"
    Entry = "8 global shell items + 8 version tabs; UI shell"
    Check = "AppShell/VersionDetailPage/tabs.ts exist; shell files present"
    Prior = "P6 phase-06-handoff.md (8 shell / 8 tabs)"
    Files = @(
      "frontend/src/app/(app)/layout.tsx"
      "frontend/src/features/shell/AppShell.tsx"
      "frontend/src/app/(app)/versions/[id]/page.tsx"
      "frontend/src/features/versions/VersionDetailPage.tsx"
      "frontend/src/features/versions/tabs.ts"
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-06-handoff.md" }
    )
  }
  @{
    Id = "H2"; Capability = "澄清 UI"
    Entry = "requirements/clarification tab"
    Check = "RequirementsTab/ChatPanel/RequirementSidebar/AttachmentUploader exist"
    Prior = "P1 phase-01-handoff.md"
    Files = @(
      "frontend/src/features/clarification/RequirementsTab.tsx"
      "frontend/src/features/clarification/ChatPanel.tsx"
      "frontend/src/features/clarification/RequirementSidebar.tsx"
      "frontend/src/features/clarification/AttachmentUploader.tsx"
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-01-handoff.md" }
    )
  }
  @{
    Id = "H3"; Capability = "脑图 UI"
    Entry = "design tab"
    Check = "DesignTab/DesignMindmap/CaseDetailPanel exist"
    Prior = "P2 phase-02-handoff.md"
    Files = @("frontend/src/features/design/DesignTab.tsx", "frontend/src/features/design/DesignMindmap.tsx", "frontend/src/features/design/CaseDetailPanel.tsx")
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-02-handoff.md" }
    )
  }
  @{
    Id = "H4"; Capability = "执行 UI"
    Entry = "execution tab"
    Check = "ExecutionTab/WaveList/EvidenceViewer/PauseDialog exist"
    Prior = "P3 phase-03-handoff.md"
    Files = @(
      "frontend/src/features/execution/ExecutionTab.tsx"
      "frontend/src/features/execution/WaveList.tsx"
      "frontend/src/features/execution/EvidenceViewer.tsx"
      "frontend/src/features/execution/PauseDialog.tsx"
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-03-handoff.md" }
    )
  }
  @{
    Id = "H5"; Capability = "问题/报告 UI"
    Entry = "issues/reports tabs + global pages"
    Check = "IssuesTab/ReportsTab/GlobalIssuesPage/GlobalReportsPage exist"
    Prior = "P4 phase-04-handoff.md; P6 phase-06-handoff.md"
    Files = @(
      "frontend/src/features/delivery/IssuesTab.tsx"
      "frontend/src/features/delivery/ReportsTab.tsx"
      "frontend/src/features/delivery/GlobalIssuesPage.tsx"
      "frontend/src/features/delivery/GlobalReportsPage.tsx"
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-04-handoff.md" }
      @{ Path = "docs/version-V0.2/phases/phase-06-handoff.md" }
    )
  }
  @{
    Id = "H6"; Capability = "搜索/待办/链路"
    Entry = "API /api/search, /api/todos, /exploration-chain; UI CommandPalette/TodoDrawer/ChainTab"
    Check = "search=200; todos=200; chain=200; three UI files exist"
    Prior = "P6 phase-06-handoff.md; acceptance/p6/acceptance.md S2/S3/S6"
    Requests = @(
      @{ Role = "admin"; Method = "GET"; Path = "/api/search?q=acceptance&limit=1"; Status = 200; Label = "global search" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/todos?limit=1"; Status = 200; Label = "global todos" }
      @{ Role = "admin"; Method = "GET"; Path = "/api/versions/{versionId}/exploration-chain"; Status = 200; Label = "exploration chain" }
    )
    Files = @(
      "frontend/src/features/search/CommandPalette.tsx"
      "frontend/src/features/shell/TodoDrawer.tsx"
      "frontend/src/features/chain/ChainTab.tsx"
      "frontend/src/features/chain/ChainGraph.tsx"
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-06-handoff.md" }
      @{ Path = "docs/version-V0.2/acceptance/p6/acceptance.md"; Marker = "S2" }
    )
  }
  @{
    Id = "I1"; Capability = "测试体系/fake LLM/E2E"
    Entry = "Go unit+integration, frontend tests, Playwright E2E, contract gates"
    Check = "entry smoke: test.ps1, p8 contract gate, frontend/e2e exist; P8 S13/regression evidence not re-run by S11"
    Prior = "P6 phase-06-handoff.md full test suite; P7 phase-07-handoff.md executor/renderer tests"
    PriorOnly = $true
    Files = @(
      "scripts/test.ps1"
      "scripts/p8-contract-check.cjs"
      "frontend/e2e"
      "executor/package.json"
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-06-handoff.md"; Marker = "S0" }
      @{ Path = "docs/version-V0.2/phases/phase-07-handoff.md"; Marker = "D59" }
    )
  }
  @{
    Id = "I2"; Capability = "打包/CI/SLO/安全/备份"
    Entry = "deploy/, check-ci, security-audit, slo, backup scripts"
    Check = "entry smoke: deploy/README + CI/SLO/security/backup scripts exist; P8 S13 static/packaging evidence not re-run by S11"
    Prior = "P7 phase-07-handoff.md; P5/P6 partial evidence; Docker not actually run"
    PriorOnly = $true
    Files = @(
      "deploy/README.md"
      "scripts/check-ci.ps1"
      "scripts/security-audit.ps1"
      "scripts/slo.ps1"
      "scripts/backup.ps1"
    )
    Evidence = @(
      @{ Path = "docs/version-V0.2/phases/phase-07-handoff.md"; Marker = "deploy/" }
    )
  }
)

$script:rows = @()
foreach ($definition in $rowDefinitions) {
  $script:rows += New-CapabilityRow @definition
}

$expectedIds = Get-ExpectedIds
$actualIds = @($script:rows | ForEach-Object { $_.Id })
$missingIds = @($expectedIds | Where-Object { $actualIds -notcontains $_ })
$extraIds = @($actualIds | Where-Object { $expectedIds -notcontains $_ })
$uniqueCount = @($actualIds | Select-Object -Unique).Count
if ($script:rows.Count -ne 37 -or $uniqueCount -ne 37 -or $missingIds.Count -gt 0 -or $extraIds.Count -gt 0) {
  throw ("capability table is not the expected 37 rows A1-I2: count={0} unique={1} missing={2} extra={3}" -f $script:rows.Count, $uniqueCount, ($missingIds -join ","), ($extraIds -join ","))
}

Write-Log ("EzTest P8 S11 capability smoke - {0}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss zzz"))
Write-Log ("repo: {0}" -f $script:repoRoot)
Write-Log ("rows: 37 (A1-I2); evidence: {0}" -f $script:logPath)

if ($ListOnly) {
  Write-Log "Mode: -ListOnly (no API/DB calls; definitions only)"
  $listResults = @()
  foreach ($row in $script:rows) {
    $listResults += [pscustomobject]@{ Row = $row; Result = "DEFINED" }
  }
  Write-Log (Get-ResultTable $listResults)
  Write-Log "37 capability rows defined; run without -ListOnly to execute against the P8 API/DB."
  exit 0
}

try {
  Initialize-Database
  Initialize-ApiSession
} catch {
  Write-Log ("PREFLIGHT FAILED: " + $_.Exception.Message)
  Write-Log ("log: " + $script:logPath)
  exit 1
}

Write-Log ""
Write-Log "==> Running 37 capability checks"

foreach ($row in $script:rows) {
  $result = Test-CapabilityRow $row
  $script:results.Add($result)
  Write-Log ("[{0}] {1} - {2}" -f $row.Id, $row.Capability, $result.Result)
  foreach ($check in $result.Checks) {
    $status = if ($check.Ok) { "PASS" } else { "FAIL" }
    $detail = if ([string]::IsNullOrWhiteSpace([string]$check.Detail)) { "" } else { " :: " + $check.Detail }
    Write-Log ("    {0} {1}{2}" -f $status, $check.Label, $detail)
  }
}

Write-Log ""
Write-Log "==> Capability matrix result"
Write-Log (Get-ResultTable $script:results)

$passCount = @($script:results | Where-Object { $_.Result -eq "PASS" }).Count
$priorCount = @($script:results | Where-Object { $_.Result -eq "PASS(prior)" }).Count
$failResults = @($script:results | Where-Object { $_.Result -eq "FAIL" })
Write-Log ("summary: PASS={0} PASS(prior)={1} FAIL={2} total={3}" -f $passCount, $priorCount, $failResults.Count, $script:results.Count)
Write-Log ("log: " + $script:logPath)

if ($failResults.Count -gt 0) {
  Write-Log ""
  Write-Log "FAILED rows:"
  foreach ($failure in $failResults) {
    Write-Log ("  {0} {1}" -f $failure.Row.Id, $failure.Row.Capability)
  }
  Write-Log "P8 CAPABILITY SMOKE FAILED"
  exit 1
}

Write-Log "P8 CAPABILITY SMOKE PASSED (no FAIL)"
exit 0
