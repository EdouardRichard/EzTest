#requires -Version 5.1
<#
P6 browser E2E against the real read models. It starts the Next.js web app on
16870 against the P6 API (14345) and runs frontend/e2e/p6.spec.ts. The API/DB
must already exist from scripts/p6-acceptance.ps1 (use -KeepApi there).
No fake LLM or fake executor is used.
#>
param(
  [int]$ApiPort = 14345,
  [int]$WebPort = 16870,
  [string]$ApiBaseUrl = "http://127.0.0.1:14345",
  [string]$DatabaseName = "eztest_v02_p6"
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$evidence = Join-Path $repoRoot ".tmp\p6-acceptance"
$shots = Join-Path $evidence "screenshots"
New-Item -ItemType Directory -Force -Path $shots | Out-Null

function Test-Health {
  try {
    $r = Invoke-WebRequest -Uri "$ApiBaseUrl/api/health" -UseBasicParsing -TimeoutSec 3
    return $r.StatusCode -eq 200
  } catch { return $false }
}
function Stop-Port([int]$Port) {
  try {
    $connections = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction Stop
    foreach ($connection in $connections) {
      Stop-Process -Id $connection.OwningProcess -Force -ErrorAction SilentlyContinue
    }
  } catch { }
}
function Read-DotEnv([string]$Path) {
  $map = @{}
  foreach ($line in Get-Content $Path) {
    $trim = $line.Trim()
    if ($trim -eq "" -or $trim.StartsWith("#") -or -not $trim.Contains("=")) { continue }
    $parts = $trim.Split("=", 2)
    $map[$parts[0].Trim()] = $parts[1].Trim()
  }
  return $map
}
function Get-P6DatabaseUrl([string]$DatabaseName) {
  $dotenv = Read-DotEnv (Join-Path $repoRoot ".env")
  $raw = [string]$dotenv["EZTEST_DATABASE_URL"]
  $pattern = '^(?<scheme>postgres(?:ql)?)://(?<user>[^:/@]+)(?::(?<password>[^@]*))?@(?<host>[^:/?]+)(?::(?<port>\d+))?/(?<database>[^?]+)(?<query>\?.*)?$'
  $m = [regex]::Match($raw, $pattern)
  if (-not $m.Success) { throw "EZTEST_DATABASE_URL is not a supported postgres URL" }
  return ("{0}://{1}:{2}@{3}:{4}/{5}" -f $m.Groups["scheme"].Value,
    [uri]::EscapeDataString([uri]::UnescapeDataString($m.Groups["user"].Value)),
    [uri]::EscapeDataString([uri]::UnescapeDataString($m.Groups["password"].Value)),
    $m.Groups["host"].Value, $m.Groups["port"].Value, $DatabaseName)
}

$apiProcess = $null
if (-not (Test-Health)) {
  $binary = Join-Path $evidence "eztest-p6.exe"
  if (-not (Test-Path $binary)) { throw "missing $binary; run scripts/p6-acceptance.ps1 first" }
  $env:EZTEST_DATABASE_URL = Get-P6DatabaseUrl $DatabaseName
  $env:EZTEST_API_PORT = [string]$ApiPort
  $env:EZTEST_WEB_PORT = [string]$WebPort
  $env:EZTEST_EXECUTOR_PORT = "14347"
  $apiProcess = Start-Process -FilePath $binary -PassThru -NoNewWindow `
    -RedirectStandardOutput (Join-Path $evidence "e2e-api.log") `
    -RedirectStandardError (Join-Path $evidence "e2e-api.err.log")
  $deadline = (Get-Date).AddSeconds(60)
  while ((Get-Date) -lt $deadline -and -not (Test-Health)) { Start-Sleep -Milliseconds 500 }
  if (-not (Test-Health)) { throw "P6 API did not become healthy" }
}

# Resolve the version ID used by the chain and prompt scenarios.
$login = Invoke-RestMethod -Uri "$ApiBaseUrl/api/auth/login" -Method Post -ContentType "application/json" -Body (@{ username = "admin"; password = "admin123" } | ConvertTo-Json)
$token = $login.data.token
$headers = @{ Authorization = "Bearer $token" }
$versions = Invoke-RestMethod -Uri "$ApiBaseUrl/api/versions?limit=1" -Headers $headers
$versionID = [string]$versions.data[0].id

Write-Host "==> Starting the web app on $WebPort"
Stop-Port $WebPort
$env:NEXT_PUBLIC_API_BASE_URL = $ApiBaseUrl
$webLog = Join-Path $evidence "web.log"
$webErr = Join-Path $evidence "web.err.log"
$web = Start-Process -FilePath "pnpm.cmd" -ArgumentList @("dev", "--port", "$WebPort") `
  -WorkingDirectory (Join-Path $repoRoot "frontend") -PassThru -WindowStyle Hidden `
  -RedirectStandardOutput $webLog -RedirectStandardError $webErr

try {
  $deadline = (Get-Date).AddSeconds(120)
  $ready = $false
  while ((Get-Date) -lt $deadline) {
    try {
      $probe = Invoke-WebRequest -Uri "http://127.0.0.1:$WebPort/login" -UseBasicParsing -TimeoutSec 5
      if ($probe.StatusCode -eq 200) { $ready = $true; break }
    } catch { }
    Start-Sleep -Milliseconds 1000
  }
  if (-not $ready) { throw "web app did not become ready; see $webErr" }

  $env:EZTEST_E2E_BASE_URL = "http://127.0.0.1:$WebPort"
  $env:EZTEST_E2E_API_URL = $ApiBaseUrl
  $env:EZTEST_E2E_P6_VERSION_ID = $versionID
  $env:EZTEST_E2E_P6_SCREENSHOT_DIR = $shots
  Push-Location (Join-Path $repoRoot "frontend")
  try {
    & pnpm exec playwright test e2e/p6.spec.ts
    if ($LASTEXITCODE -ne 0) { throw "P6 browser E2E failed" }
  } finally {
    Pop-Location
  }
  Write-Host "P6 E2E PASSED; screenshots in $shots"
} finally {
  if ($null -ne $web) {
    & taskkill.exe /PID $web.Id /T /F 2>$null | Out-Null
  }
  if ($null -ne $apiProcess) {
    & taskkill.exe /PID $apiProcess.Id /T /F 2>$null | Out-Null
  }
}
