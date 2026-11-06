#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 FA flow projection: normalize the W0 regression evidence into
    the machine-readable RUN-x/w0flow/flow.json consumed by -Gate flow.

.DESCRIPTION
    The W0 regression writes result.json (per-check items) and verdict.json
    (flowExit/redCount/dbChecks). -Gate flow additionally needs an explicit
    stage trace with one screenshot per stage, the DB counters and the LLM model
    of the real agent runs. This script derives that view without touching the
    W0 evidence: it reads the copied result/verdict, queries the W0 database and
    writes w0flow/flow.json.

    Exit codes: 0 = flow.json written, 1 = required input missing.
#>
param(
    [ValidateSet('RUN-1', 'RUN-2')][string]$RunDir = 'RUN-1',
    [string]$Database = 'eztest_v022_w0',
    [string]$EvidenceRoot = ''
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot
if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $EvidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final'
}
$runRoot = Join-Path $EvidenceRoot $RunDir
$w0Root = Join-Path $runRoot 'w0flow'
$resultPath = Join-Path $w0Root 'result.json'
$verdictPath = Join-Path $w0Root 'verdict.json'
if (-not (Test-Path -LiteralPath $resultPath)) { Write-Host "RED: missing $RunDir/w0flow/result.json"; exit 1 }
if (-not (Test-Path -LiteralPath $verdictPath)) { Write-Host "RED: missing $RunDir/w0flow/verdict.json"; exit 1 }

function Write-Utf8([string]$Path, [string]$Text) {
    [System.IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding($false)))
}
function Read-JsonUtf8([string]$Path) {
    return (Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json)
}
function Get-DbPassword([string]$Url) {
    $match = [regex]::Match($Url, '^postgres://[^:]+:([^@]+)@')
    if (-not $match.Success) { return '' }
    return [uri]::UnescapeDataString($match.Groups[1].Value)
}
function Get-EnvValue([string]$Key) {
    $line = Select-String -Path (Join-Path $repoRoot '.env') -Pattern ('^' + [regex]::Escape($Key) + '=(.*)$')
    if (-not $line) { throw "missing $Key in .env" }
    return $line.Matches[0].Groups[1].Value.Trim()
}

$result = Read-JsonUtf8 -Path $resultPath
$verdict = Read-JsonUtf8 -Path $verdictPath
$versionId = [string]$result.state.versionId
$dbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
$parts = [uri]$dbUrl
$user = [uri]::UnescapeDataString($parts.UserInfo.Split(':')[0])
$password = Get-DbPassword $dbUrl
$psql = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
$env:PGPASSWORD = $password
function Query([string]$Sql) {
    $value = (& $psql -h $parts.Host -p $parts.Port -U $user -d $Database -Atc $Sql 2>&1 | Out-String).Trim()
    return $value
}
try {
    $scope = if ([string]::IsNullOrWhiteSpace($versionId)) { '' } else { " where version_id = $versionId" }
    $counters = [ordered]@{
        requirements = [int](Query "select count(*) from requirement$scope")
        cases = [int](Query "select count(*) from case_version")
        waves = [int](Query "select count(*) from run_wave")
        caseRuns = [int](Query "select count(*) from case_run")
        evidence = [int](Query "select count(*) from evidence")
    }
    $model = Query "select model from agent_run where model is not null and model <> '' group by model order by count(*) desc limit 1"
    $agentRuns = [int](Query "select count(*) from agent_run where model = '$($model.Replace("'", "''"))'")
}
finally { Remove-Item Env:PGPASSWORD -ErrorAction SilentlyContinue }

$stageShots = [ordered]@{
    created            = @("$RunDir/w0flow/ui-version-overview.png")
    clarifying         = @("$RunDir/w0flow/ui-question-card.png")
    requirement_review = @("$RunDir/w0flow/ui-requirements.png")
    designing          = @("$RunDir/w0flow/ui-design.png")
    case_review        = @("$RunDir/w0flow/ui-design.png")
    executing          = @("$RunDir/w0flow/ui-execution.png")
    reporting          = @("$RunDir/w0flow/ui-reports.png")
    completed          = @("$RunDir/w0flow/ui-version-overview.png")
}
$stages = New-Object System.Collections.Generic.List[object]
foreach ($name in $stageShots.Keys) {
    $stages.Add([pscustomobject][ordered]@{ name = $name; screenshots = @($stageShots[$name]) })
}

$flow = [pscustomobject][ordered]@{
    source = "$RunDir/w0flow/result.json"
    generatedAt = (Get-Date).ToString('o')
    versionId = $versionId
    flowExit = [int]$verdict.flowExit
    red = @($verdict.dbChecks.red)
    stages = @($stages.ToArray())
    dbCounters = $counters
    agentRun = [pscustomobject][ordered]@{ model = $model; runs = $agentRuns }
}
Write-Utf8 (Join-Path $w0Root 'flow.json') ($flow | ConvertTo-Json -Depth 8)
Write-Host "flow.json written: $RunDir/w0flow/flow.json (db=$Database, model=$model, stages=$($stages.Count))"
exit 0
