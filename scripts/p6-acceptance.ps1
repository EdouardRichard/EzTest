#requires -Version 5.1
<#
P6 acceptance — real LLM + real Chrome acceptance evidence for the P6 read
models. It clones the real P4 acceptance database into eztest_v02_p6, applies
the P6 migrations (33), starts the P6 API, and walks S0-S9. No fake LLM is ever
used: S4 starts a real clarification run and proves the edited prompt reached
the next run's context packet; S7 records a real-LLM eval run.

The browser E2E is scripts/p6-e2e.ps1 (run this first; it leaves the API up).
#>
param(
  [int]$ApiPort = 14345,
  [string]$TemplateDatabase = "eztest_v02_p4",
  [string]$DatabaseName = "eztest_v02_p6",
  [switch]$SkipDatabaseReset,
  [switch]$KeepApi
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$evidence = Join-Path $repoRoot ".tmp\p6-acceptance"
New-Item -ItemType Directory -Force -Path $evidence | Out-Null
$scenariosPath = Join-Path $evidence "scenarios.txt"

$script:failures = New-Object System.Collections.Generic.List[string]
$script:token = ""
$script:userToken = ""
$script:versionID = ""

function Write-Log([string]$Message) {
  Write-Host $Message
  Add-Content -Path (Join-Path $evidence "acceptance.log") -Value $Message
}
function Assert-True([bool]$Condition, [string]$Label) {
  if ($Condition) { Write-Log "  PASS: $Label" } else { $script:failures.Add($Label); Write-Log "  FAIL: $Label" }
}
function Assert-Equal($Actual, $Expected, [string]$Label) {
  Assert-True ([string]$Actual -eq [string]$Expected) ("$Label (got " + [string]$Actual + ")")
}
function Assert-Status($Response, [int[]]$Allowed, [string]$Label) {
  $ok = $Allowed -contains $Response.Status
  Assert-True $ok ("$Label (status " + $Response.Status + ")")
}
function Read-DotEnv([string]$Path) {
  $map = @{}
  if (-not (Test-Path $Path)) { throw "missing .env at $Path" }
  foreach ($line in Get-Content $Path) {
    $trim = $line.Trim()
    if ($trim -eq "" -or $trim.StartsWith("#") -or -not $trim.Contains("=")) { continue }
    $parts = $trim.Split("=", 2)
    $map[$parts[0].Trim()] = $parts[1].Trim()
  }
  return $map
}

$dotenv = Read-DotEnv (Join-Path $repoRoot ".env")
foreach ($key in @("EZTEST_DATABASE_URL", "llm_api_key", "llm_base_url", "llm_model_name")) {
  if (-not $dotenv.ContainsKey($key) -or [string]::IsNullOrWhiteSpace($dotenv[$key])) {
    throw "missing $key in .env"
  }
}
$pattern = '^(?<scheme>postgres(?:ql)?)://(?<user>[^:/@]+)(?::(?<password>[^@]*))?@(?<host>[^:/?]+)(?::(?<port>\d+))?/(?<database>[^?]+)(?<query>\?.*)?$'
$m = [regex]::Match($dotenv["EZTEST_DATABASE_URL"], $pattern)
if (-not $m.Success) { throw "EZTEST_DATABASE_URL is not a supported postgres URL" }
$dbUser = [uri]::UnescapeDataString($m.Groups["user"].Value)
$dbPassword = [uri]::UnescapeDataString($m.Groups["password"].Value)
$dbHost = $m.Groups["host"].Value
$dbPort = if ($m.Groups["port"].Success) { $m.Groups["port"].Value } else { "5432" }
$env:PGPASSWORD = $dbPassword
$psql = Join-Path $repoRoot ".tools\pgsql\bin\psql.exe"
if (-not (Test-Path $psql)) { throw "psql missing at $psql" }

function Invoke-Psql([string]$Database, [string]$Sql) {
  $out = & $psql -h $dbHost -p $dbPort -U $dbUser -d $Database -v ON_ERROR_STOP=1 -tAc $Sql 2>&1
  if ($LASTEXITCODE -ne 0) { throw ("psql failed: " + ($out -join " ")) }
  return $out
}
function Invoke-Api([string]$Method, [string]$Path, $Body, [string]$Token) {
  $headers = @{}
  if (-not [string]::IsNullOrWhiteSpace($Token)) { $headers["Authorization"] = "Bearer $Token" }
  try {
    $params = @{
      Uri = "http://127.0.0.1:$ApiPort$Path"
      Method = $Method
      Headers = $headers
      UseBasicParsing = $true
      TimeoutSec = 90
      ContentType = "application/json"
    }
    if ($null -ne $Body) { $params["Body"] = ($Body | ConvertTo-Json -Depth 12) }
    $response = Invoke-WebRequest @params
    $json = $null
    if (-not [string]::IsNullOrWhiteSpace($response.Content)) {
      try { $json = $response.Content | ConvertFrom-Json } catch { $json = $null }
    }
    return @{ Status = [int]$response.StatusCode; Json = $json; Text = $response.Content }
  } catch {
    $status = 0
    $text = $_.Exception.Message
    if ($null -ne $_.Exception.Response) {
      $status = [int]$_.Exception.Response.StatusCode
      try {
        $reader = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
        $text = $reader.ReadToEnd()
      } catch { }
    }
    $json = $null
    try { $json = $text | ConvertFrom-Json } catch { }
    return @{ Status = $status; Json = $json; Text = $text }
  }
}
function Wait-Health([int]$TimeoutSeconds = 60) {
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    $probe = Invoke-Api "GET" "/api/health" $null ""
    if ($probe.Status -eq 200) { return $true }
    Start-Sleep -Milliseconds 500
  }
  return $false
}
function Stop-Port([int]$Port) {
  try {
    $connections = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction Stop
    foreach ($connection in $connections) {
      Stop-Process -Id $connection.OwningProcess -Force -ErrorAction SilentlyContinue
    }
  } catch { }
}
function Complete-Scenario([string]$Name, [bool]$Pass, [string]$Detail) {
  if (-not $Pass) { $script:failures.Add($Name) }
  $line = ("{0} | {1} | {2}" -f $Name, $(if ($Pass) { "PASS" } else { "FAIL" }), $Detail)
  Add-Content -Path $scenariosPath -Value $line
  Write-Log $line
}

Set-Content -Path $scenariosPath -Value "# EzTest P6 real-LLM/real-Chrome acceptance scenarios"
Write-Log "P6 acceptance on API port $ApiPort (database $DatabaseName from template $TemplateDatabase)"

$binary = Join-Path $evidence "eztest-p6.exe"
Write-Log "==> Building the P6 API"
Push-Location (Join-Path $repoRoot "backend")
& (Join-Path $repoRoot ".tools\go\bin\go.exe") build -o $binary ./cmd/eztest
if ($LASTEXITCODE -ne 0) { throw "go build failed" }
Pop-Location

if (-not $SkipDatabaseReset) {
  Write-Log "==> Cloning $TemplateDatabase -> $DatabaseName"
  $null = Invoke-Psql "postgres" "DROP DATABASE IF EXISTS $DatabaseName WITH (FORCE);"
  $null = Invoke-Psql "postgres" "CREATE DATABASE $DatabaseName TEMPLATE $TemplateDatabase;"
}

Write-Log "==> Starting the P6 API on port $ApiPort"
Stop-Port $ApiPort
$p6Url = "{0}://{1}:{2}@{3}:{4}/{5}" -f $m.Groups["scheme"].Value, [uri]::EscapeDataString($dbUser), [uri]::EscapeDataString($dbPassword), $dbHost, $dbPort, $DatabaseName
$env:EZTEST_DATABASE_URL = $p6Url
$env:EZTEST_API_PORT = [string]$ApiPort
$env:EZTEST_WEB_PORT = "16870"
$env:EZTEST_EXECUTOR_PORT = "14347"
$apiLog = Join-Path $evidence "api.log"
$apiErr = Join-Path $evidence "api.err.log"
$api = Start-Process -FilePath $binary -PassThru -NoNewWindow -RedirectStandardOutput $apiLog -RedirectStandardError $apiErr
try {
  if (-not (Wait-Health 90)) { throw "API did not become healthy; see $apiErr" }

  $login = Invoke-Api "POST" "/api/auth/login" @{ username = "admin"; password = "admin123" } ""
  Assert-Status $login @(200) "admin login"
  $script:token = [string]$login.Json.data.token
  if ([string]::IsNullOrWhiteSpace($script:token)) { throw "admin login returned no token" }

  # A normal user for the read-only/permission scenarios. The account may exist
  # from a previous run; both 201 and 409 are acceptable.
  $createUser = Invoke-Api "POST" "/api/admin/users" @{ username = "p6user"; display_name = "P6 User"; password = "p6user12345" } $script:token
  Assert-True (($createUser.Status -eq 201) -or ($createUser.Status -eq 409)) "create/ensure p6user"
  $userLogin = Invoke-Api "POST" "/api/auth/login" @{ username = "p6user"; password = "p6user12345" } ""
  Assert-Status $userLogin @(200) "p6user login"
  $script:userToken = [string]$userLogin.Json.data.token

  $migrations = [int](Invoke-Psql $DatabaseName "SELECT count(*) FROM schema_migrations;")
  $settings = Invoke-Api "GET" "/api/admin/settings" $null $script:token
  $versionList = Invoke-Api "GET" "/api/versions?limit=100" $null $script:token
  $script:versionID = [string]$versionList.Json.data[0].id

  # S0 gate/health
  $s0 = ($migrations -eq 33) -and ($settings.Json.data.Count -eq 72) -and ($versionList.Status -eq 200)
  $llmList = Invoke-Api "GET" "/api/admin/llm/configs" $null $script:token
  $llmTest = $null
  if ($llmList.Status -eq 200 -and $llmList.Json.data.Count -gt 0) {
    $llmID = [string]$llmList.Json.data[0].id
    $llmTest = Invoke-Api "POST" ("/api/admin/llm/configs/{0}/test" -f $llmID) $null $script:token
  }
  $s0 = $s0 -and ($null -ne $llmTest) -and ($llmTest.Status -eq 200)
  Complete-Scenario "S0" $s0 "health=ok migrations=33 settings=72 realLLM=reachable"

  # S1 quality/eval
  $overview = Invoke-Api "GET" "/api/quality/overview" $null $script:token
  $visual = Invoke-Api "GET" "/api/quality/visual-metrics" $null $script:token
  $user403 = Invoke-Api "GET" "/api/quality/eval-runs" $null $script:userToken
  $s1 = ($overview.Status -eq 200) -and ($overview.Json.data.operational.Count -eq 7) -and ($visual.Status -eq 200) -and ($user403.Status -eq 403)
  Complete-Scenario "S1a" $s1 "overview=7 metrics visual=200 userEval=403"

  # S2 search
  $search = Invoke-Api "GET" "/api/search?q=P4&limit=5" $null $script:token
  $search403 = Invoke-Api "GET" "/api/search?q=" $null $script:token
  $s2 = ($search.Status -eq 200) -and ($search.Json.data.groups.Count -eq 5) -and ($search.Json.data.total -gt 0) -and ($search403.Status -eq 400)
  Complete-Scenario "S2" $s2 ("groups=5 total=" + [string]$search.Json.data.total + " emptyQ=400")

  # S3 todos + permissions
  $todos = Invoke-Api "GET" "/api/todos?limit=50" $null $script:token
  $todosUser = Invoke-Api "GET" "/api/todos?limit=50" $null $script:userToken
  $s3 = ($todos.Status -eq 200) -and ($todos.Json.data.items.Count -gt 0) -and ($todos.Json.data.counts.dead_work_item -gt 0) -and ($todosUser.Json.data.counts.dead_work_item -eq 0)
  Complete-Scenario "S3" $s3 ("adminItems=" + [string]$todos.Json.data.items.Count + " userDead=0")

  # S4 agent centre + prompt version/preview + real next-run token
  $catalog = Invoke-Api "GET" "/api/agent-center/catalog" $null $script:token
  $runs = Invoke-Api "GET" "/api/agent-center/runs?limit=5" $null $script:token
  $profile = Invoke-Api "GET" "/api/agents/profiles/clarification_host" $null $script:token
  $profileUser = Invoke-Api "GET" "/api/agents/profiles" $null $script:userToken
  $promptToken = "[P6-ACCEPTANCE-TOKEN]"
  $newPrompt = $promptToken + "`n" + [string]$profile.Json.data.profile.system_prompt
  $put = Invoke-Api "PUT" "/api/agents/profiles/clarification_host/prompt" @{ system_prompt = $newPrompt; note = "p6 acceptance" } $script:token
  $revisions = Invoke-Api "GET" "/api/agents/profiles/clarification_host/prompt/revisions?limit=5" $null $script:token
  $preview = Invoke-Api "POST" "/api/agents/profiles/clarification_host/prompt/preview" @{ system_prompt = "TOKEN {{.VersionTitle}} [profile:clarification_host]"; sample = @{ VersionTitle = "acceptance" } } $script:token
  $s4api = ($catalog.Json.data.profiles.Count -eq 14) -and ($runs.Status -eq 200) -and ($profileUser.Status -eq 403) -and ($put.Status -eq 200) -and ($revisions.Status -eq 200) -and ($preview.Status -eq 200)

  $conversation = Invoke-Api "POST" ("/api/versions/{0}/conversations" -f $script:versionID) @{ kind = "clarification"; title = "P6 acceptance prompt run" } $script:token
  $conversationID = [string]$conversation.Json.data.id
  $message = Invoke-Api "POST" ("/api/versions/{0}/conversations/{1}/messages" -f $script:versionID, $conversationID) @{ parts = @(@{ type = "text"; text = "请用一句话确认收到验收标记，不需要调用工具。" }) } $script:token
  $tokenFound = $false
  for ($i = 0; $i -lt 40; $i++) {
    Start-Sleep -Seconds 3
    $count = [int](Invoke-Psql $DatabaseName "SELECT count(*) FROM context_packet WHERE version_id=$script:versionID AND sections::text LIKE '%$promptToken%';")
    if ($count -gt 0) { $tokenFound = $true; break }
  }
  $restore = Invoke-Api "POST" "/api/agents/profiles/clarification_host/prompt/restore" @{ version = 1 } $script:token
  $s4 = $s4api -and $tokenFound -and ($restore.Status -eq 200)
  Complete-Scenario "S4" $s4 ("profiles=14 runs=200 promptVersion=" + [string]$put.Json.data.version + " nextRunToken=" + $tokenFound)

  # S5 global centres
  $issues = Invoke-Api "GET" "/api/issues?limit=10" $null $script:token
  $reports = Invoke-Api "GET" "/api/reports?limit=10" $null $script:token
  $s5 = ($issues.Status -eq 200) -and ($issues.Json.data.Count -gt 0) -and ($reports.Status -eq 200) -and ($reports.Json.data.Count -gt 0)
  Complete-Scenario "S5" $s5 ("issues=" + [string]$issues.Json.data.Count + " reports=" + [string]$reports.Json.data.Count)

  # S6 chain v2
  $chain = Invoke-Api "GET" ("/api/versions/{0}/exploration-chain?abnormal_only=true&issue_severity=major&q=refund&requirement_limit=10" -f $script:versionID) $null $script:token
  $badChain = Invoke-Api "GET" ("/api/versions/{0}/exploration-chain?issue_status=bogus" -f $script:versionID) $null $script:token
  $s6 = ($chain.Status -eq 200) -and ($chain.Json.data.counts.by_case_run_status -ne $null) -and ($badChain.Status -eq 400)
  Complete-Scenario "S6" $s6 ("filteredNodes=" + [string]$chain.Json.data.nodes.Count + " invalid=400")

  # S7 real-LLM eval report track
  $observationPath = Join-Path $evidence "observation.json"
  if (-not (Test-Path $observationPath)) { throw "missing observation.json for S7" }
  $env:EZTEST_API_PORT = [string]$ApiPort
  $evalOut = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $repoRoot "scripts\eval.ps1") -Mode real-llm -ApiBaseUrl ("http://127.0.0.1:{0}" -f $ApiPort) -ObservationFile $observationPath 2>&1
  $evalCode = $LASTEXITCODE
  $evalRuns = Invoke-Api "GET" "/api/quality/eval-runs?limit=5" $null $script:token
  $s7 = ($evalCode -eq 0) -and ($evalRuns.Status -eq 200) -and ($evalRuns.Json.data.Count -gt 0)
  Complete-Scenario "S7" $s7 ("evalExit=" + [string]$evalCode + " runs=" + [string]$evalRuns.Json.data.Count)

  # S8 P5 regression: the P5 visual read models still answer on this DB.
  $p5visual = Invoke-Api "GET" "/api/quality/visual-metrics" $null $script:token
  $p5overview = Invoke-Api "GET" ("/api/versions/{0}/visual/overview" -f $script:versionID) $null $script:token
  $s8 = ($p5visual.Status -eq 200) -and ($p5overview.Status -eq 200)
  Complete-Scenario "S8" $s8 ("visualMetrics=" + [string]$p5visual.Status + " visualOverview=" + [string]$p5overview.Status)

  # S9 contract gates
  $gates = @("p2", "p3", "p4", "p5", "p6")
  $gateOK = $true
  foreach ($gate in $gates) {
    & node (Join-Path $repoRoot ("scripts\{0}-contract-check.cjs" -f $gate)) | Out-Null
    if ($LASTEXITCODE -ne 0) { $gateOK = $false }
  }
  Complete-Scenario "S9" $gateOK "p2-p6 contract gates"

  if ($script:failures.Count -gt 0) {
    Write-Log ("P6 ACCEPTANCE FAILED: " + ($script:failures -join "; "))
    exit 1
  }
  Write-Log "P6 ACCEPTANCE PASSED (S0-S9); evidence in $evidence"
  exit 0
} finally {
  if (-not $KeepApi -and $null -ne $api -and -not $api.HasExited) {
    Stop-Process -Id $api.Id -Force -ErrorAction SilentlyContinue
  }
}
