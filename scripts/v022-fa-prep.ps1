#requires -Version 5.1
<#
.SYNOPSIS
    EzTest v0.22 final-acceptance FA-2 preparation: fresh stack + real vision gate.

.DESCRIPTION
    Builds the FA composition root from the current working tree and leaves it
    RUNNING for FA-4/FA-5:

      * fresh PostgreSQL database eztest_v022_fa (DROP/CREATE via psql);
      * Go API on 127.0.0.1:13460 (fresh DB overrides .env per process);
      * real executor on 127.0.0.1:13360;
      * Next.js web on 127.0.0.1:6880;
      * admin login + real LLM config/model registration (supports_vision=true)
        and default model selection in the fresh DB;
      * a real image call to the .env upstream (OpenAI chat/completions with an
        image_url data URL) that must return HTTP 200 and a non-empty caption;
        no caption proxy is started or used;
      * SUT liveness (health/posts/uploads/web), executor liveness + auth
        (Bearer 200 and no-token 401), direct DB probes;
      * Playwright screenshots of /versions and /settings.

    Evidence (untracked, docs/ is git-ignored):
      docs/version-V0.22/acceptance/evidence/final/prep/prep-verdict.json
      docs/version-V0.22/acceptance/evidence/final/prep/checks/*.json
      docs/version-V0.22/acceptance/evidence/final/prep/screenshots/*.png

    The producer never decides PASS/FAIL by itself: -Gate prep is the read-only
    authority. External dependency failures are recorded as EXTERNAL-BLOCKED,
    never as PASS.

.NOTES
    Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\v022-fa-prep.ps1
        powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts\v022-fa-prep.ps1 -Stop

    -Stop only stops the API/web/executor processes recorded in .tmp/fa/pids.json
    by this script (and their child process trees).
#>

param(
    [switch]$Stop
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

. (Join-Path $PSScriptRoot 'v022-fa-common.ps1')

$work = Join-Path $repoRoot '.tmp\fa'
$evidenceRoot = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final'
$prepDir = Join-Path $evidenceRoot 'prep'
$checkDir = Join-Path $prepDir 'checks'
$shotDir = Join-Path $prepDir 'screenshots'
$pidsPath = Join-Path $work 'pids.json'
$prepVerdictPath = Join-Path $prepDir 'prep-verdict.json'
$prepLogPath = Join-Path $work 'prep.log'
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

$dbName = 'eztest_v022_fa'
$apiPort = 13460
$webPort = 6880
$executorPort = 13360
$apiBase = "http://127.0.0.1:$apiPort"
$webBase = "http://127.0.0.1:$webPort"
$executorBase = "http://127.0.0.1:$executorPort"

$script:apiPid = 0
$script:webPid = 0
$script:executorPid = 0
$script:checks = New-Object System.Collections.Generic.List[object]
$script:failedExternal = $null
$script:failedInternal = $null
$script:dbEztestOk = $false
$script:dbSutOk = $false
$script:dbUploadsOk = $false
$script:executorHttpStatus = 0
$script:executorBearer200 = $false
$script:executorNoToken401 = $false
$script:visionModel = ''
$script:visionBaseUrl = ''
$script:visionCaption = ''
$script:visionExercised = $false
$script:visionHttpStatus = 0
$script:catalogSupportsVision = $false
$script:catalogDefault = $false

New-Item -ItemType Directory -Force -Path $work, $prepDir, $checkDir, $shotDir,
    (Join-Path $evidenceRoot 'logs') | Out-Null
Remove-Item -LiteralPath $prepLogPath -Force -ErrorAction SilentlyContinue

function Write-PrepLog {
    param([string]$Message)
    $line = ('[{0}] {1}' -f (Get-Date).ToString('HH:mm:ss'), $Message)
    Write-Host $line
    try {
        [System.IO.File]::AppendAllText($prepLogPath, $line + "`r`n", $utf8NoBom)
    }
    catch { }
}

function Add-PrepCheck {
    param(
        [string]$Name,
        [bool]$Ok,
        [string]$Detail,
        [switch]$External
    )
    $safeDetail = ([string]$Detail) -replace "`r", ' ' -replace "`n", ' '
    $script:checks.Add([pscustomobject][ordered]@{
            name   = $Name
            ok     = $Ok
            detail = $safeDetail
        })
    $state = if ($Ok) { 'ok' } else { 'FAIL' }
    Write-PrepLog ("CHECK {0}: {1} ({2})" -f $Name, $state, $safeDetail)
    if (-not $Ok) {
        if ($External -and $null -eq $script:failedExternal) { $script:failedExternal = "$Name`: $safeDetail" }
        elseif (-not $External -and $null -eq $script:failedInternal) { $script:failedInternal = "$Name`: $safeDetail" }
    }
}

function Fail-External {
    param([string]$Reason)
    $script:failedExternal = $Reason
    throw [System.Exception]::new('EXTERNAL:' + $Reason)
}

function Fail-Internal {
    param([string]$Reason)
    $script:failedInternal = $Reason
    throw [System.Exception]::new('INTERNAL:' + $Reason)
}

function Get-PortOwner {
    param([int]$Port)
    $connection = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -eq $connection) { return 0 }
    return [int]$connection.OwningProcess
}

function Get-ProcessTreeIds {
    param([int]$RootId)
    $all = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
            Select-Object ProcessId, ParentProcessId)
    $ids = New-Object System.Collections.Generic.List[int]
    $queue = New-Object System.Collections.Generic.Queue[int]
    $queue.Enqueue($RootId)
    while ($queue.Count -gt 0) {
        $current = $queue.Dequeue()
        if ($ids.Contains($current)) { continue }
        $ids.Add($current)
        foreach ($child in @($all | Where-Object { [int]$_.ParentProcessId -eq $current })) {
            if (-not $ids.Contains([int]$child.ProcessId)) { $queue.Enqueue([int]$child.ProcessId) }
        }
    }
    $ordered = $ids.ToArray()
    [array]::Reverse($ordered)
    return $ordered
}

function Stop-ProcessTree {
    param([int]$RootId)
    if ($RootId -le 0) { return }
    foreach ($processId in @(Get-ProcessTreeIds -RootId $RootId)) {
        Stop-Process -Id $processId -Force -ErrorAction SilentlyContinue
    }
}

function Stop-PrepStackFromIds {
    param([int]$ApiPid, [int]$WebPid, [int]$ExecutorPid)
    Stop-ProcessTree -RootId $WebPid
    Stop-ProcessTree -RootId $ExecutorPid
    Stop-ProcessTree -RootId $ApiPid
    Remove-Item -LiteralPath $pidsPath -Force -ErrorAction SilentlyContinue
}

function Stop-PrepStackFromFile {
    if (-not (Test-Path -LiteralPath $pidsPath)) {
        Write-Host 'no recorded FA stack pids to stop'
        return
    }
    try {
        $recorded = Read-JsonUtf8 -Path $pidsPath
    }
    catch {
        Write-Host "pids.json unreadable, nothing stopped: $($_.Exception.Message)"
        return
    }
    $api = [int]$recorded.api
    $web = [int]$recorded.web
    $executor = [int]$recorded.executor
    Stop-PrepStackFromIds -ApiPid $api -WebPid $web -ExecutorPid $executor
    Write-Host "stopped FA stack (api=$api web=$web executor=$executor)"
}

function Get-PostgresParts {
    param([string]$Url)
    $match = [regex]::Match($Url,
        '^postgres(?:ql)?://(?<user>[^:/@]+):(?<password>.*)@(?<host>[^:/@]+):(?<port>\d+)/(?<database>[^?]+)')
    if (-not $match.Success) {
        throw 'unparseable PostgreSQL DSN in .env (expected postgres://user:pass@host:port/db)'
    }
    return [pscustomobject]@{
        User     = [string]$match.Groups['user'].Value
        Password = [string]$match.Groups['password'].Value
        Host     = [string]$match.Groups['host'].Value
        Port     = [int]$match.Groups['port'].Value
        Database = [string]$match.Groups['database'].Value
    }
}

function Invoke-Psql {
    param(
        [string]$Database,
        [string]$Sql,
        [string]$Password,
        [string]$DbHost,
        [int]$Port,
        [string]$User
    )
    $previous = $env:PGPASSWORD
    $previousErrorAction = $ErrorActionPreference
    $env:PGPASSWORD = $Password
    $ErrorActionPreference = 'Continue'
    try {
        $output = & $script:psqlExe -h $DbHost -p $Port -U $User -d $Database -tAc $Sql 2>&1
        return [pscustomobject]@{
            ExitCode = $LASTEXITCODE
            Output   = (@($output) -join "`n")
        }
    }
    finally {
        $ErrorActionPreference = $previousErrorAction
        $env:PGPASSWORD = $previous
    }
}

function Start-ManagedProcess {
    param(
        [string]$FilePath,
        [string[]]$Arguments,
        [string]$WorkingDirectory,
        [string]$StdOut,
        [string]$StdErr,
        [hashtable]$Environment
    )
    $previous = @{}
    foreach ($key in $Environment.Keys) {
        $previous[$key] = [System.Environment]::GetEnvironmentVariable($key, 'Process')
        [System.Environment]::SetEnvironmentVariable($key, [string]$Environment[$key], 'Process')
    }
    try {
        $parameters = @{
            FilePath               = $FilePath
            WorkingDirectory       = $WorkingDirectory
            RedirectStandardOutput = $StdOut
            RedirectStandardError  = $StdErr
            PassThru               = $true
            NoNewWindow            = $true
        }
        if ($null -ne $Arguments -and $Arguments.Count -gt 0) {
            $parameters['ArgumentList'] = $Arguments
        }
        $process = Start-Process @parameters
    }
    finally {
        foreach ($key in $Environment.Keys) {
            [System.Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process')
        }
    }
    return $process
}

function Wait-Http {
    param(
        [string]$Uri,
        [int]$TimeoutSeconds,
        [int]$ExpectedStatus = 200
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = $null
    do {
        $last = Invoke-Api -Method Get -Uri $Uri -TimeoutSec 5
        if ($last.Status -eq $ExpectedStatus) { return $last }
        Start-Sleep -Milliseconds 800
    } while ((Get-Date) -lt $deadline)
    return $last
}

function Save-CheckJson {
    param([string]$Name, $Value)
    Write-JsonUtf8 -Path (Join-Path $checkDir "$Name.json") -Object $Value
}

function Save-PrepVerdict {
    param([string]$Status, [string]$Reason)

    $head = ''
    try { $head = Get-Head } catch { $head = '' }

    $frozenInputs = ''
    $frozenGate = ''
    $frozenIssues = ''
    try {
        $frozenInputs = Get-Sha -Path (Join-Path $evidenceRoot 'inputs\typical-section1.md')
    }
    catch { $frozenInputs = '' }
    try {
        $frozenGate = Get-Sha -Path (Join-Path $PSScriptRoot 'v022-fa-gate.ps1')
    }
    catch { $frozenGate = '' }
    try {
        $frozenIssues = Get-Sha -Path (Join-Path $repoRoot 'docs\version-V0.22\acceptance\issues.md')
    }
    catch { $frozenIssues = '' }

    $checkArray = @($script:checks.ToArray())
    $verdict = [pscustomobject][ordered]@{
        schemaVersion = 1
        status        = $Status
        gitHead       = $head
        generatedAt   = (Get-Date).ToUniversalTime().ToString('o')
        checks        = $checkArray
        db            = [pscustomobject][ordered]@{
            eztestSelect1 = [bool]$script:dbEztestOk
            sutSelect1    = [bool]$script:dbSutOk
            uploads       = [bool]$script:dbUploadsOk
            redis         = 'not_applicable'
        }
        executor      = [pscustomobject][ordered]@{
            url        = $executorBase
            httpStatus = [int]$script:executorHttpStatus
            auth       = [pscustomobject][ordered]@{
                bearer200  = [bool]$script:executorBearer200
                noToken401 = [bool]$script:executorNoToken401
            }
        }
        vision        = [pscustomobject][ordered]@{
            model                 = [string]$script:visionModel
            baseUrl               = [string]$script:visionBaseUrl
            visionExercised       = [bool]$script:visionExercised
            caption               = [string]$script:visionCaption
            httpStatus            = [int]$script:visionHttpStatus
            catalogSupportsVision = [bool]$script:catalogSupportsVision
            catalogDefault        = [bool]$script:catalogDefault
        }
        stack         = [pscustomobject][ordered]@{
            db           = $dbName
            apiPort      = $apiPort
            webPort      = $webPort
            executorPort = $executorPort
            pids         = [pscustomobject][ordered]@{
                api      = [int]$script:apiPid
                web      = [int]$script:webPid
                executor = [int]$script:executorPid
            }
        }
        frozenShas    = [pscustomobject][ordered]@{
            inputs   = $frozenInputs
            gate     = $frozenGate
            issuesMd = $frozenIssues
        }
    }
    if (-not [string]::IsNullOrWhiteSpace($Reason)) {
        $verdict | Add-Member -NotePropertyName blockedReason -NotePropertyValue $Reason
    }
    Write-JsonUtf8 -Path $prepVerdictPath -Object $verdict
    Write-PrepLog "prep-verdict written: status=$Status path=$prepVerdictPath"
}

# ---------------------------------------------------------------------------
# -Stop: only stop the processes recorded by a previous successful start.
# ---------------------------------------------------------------------------
if ($Stop) {
    Stop-PrepStackFromFile
    exit 0
}

# ---------------------------------------------------------------------------
# Reuse guard: never drop a database out from under a live FA stack.
# A listener may be a child of the recorded process (e.g. Next.js dev spawns
# start-server.js), so ownership is checked against the recorded PID's tree.
# ---------------------------------------------------------------------------
function Get-ProcessParentPid([int]$PidValue) {
    try {
        $p = Get-CimInstance Win32_Process -Filter "ProcessId=$PidValue" -ErrorAction Stop
        if ($null -eq $p) { return 0 }
        return [int]$p.ParentProcessId
    }
    catch { return 0 }
}

function Test-PidInTree([int]$RootPid, [int]$CandidatePid) {
    if ($RootPid -le 0 -or $CandidatePid -le 0) { return $false }
    $current = $CandidatePid
    for ($depth = 0; $depth -lt 8 -and $current -gt 0; $depth++) {
        if ($current -eq $RootPid) { return $true }
        $current = Get-ProcessParentPid -PidValue $current
    }
    return $false
}

$portMap = [ordered]@{ api = $apiPort; web = $webPort; executor = $executorPort }
$busy = [ordered]@{}
foreach ($name in $portMap.Keys) {
    $owner = Get-PortOwner -Port ([int]$portMap[$name])
    if ($owner -gt 0) { $busy[$name] = $owner }
}
if ($busy.Count -gt 0) {
    $allOurs = $false
    if (Test-Path -LiteralPath $pidsPath) {
        try {
            $recorded = Read-JsonUtf8 -Path $pidsPath
            $allOurs = $true
            foreach ($name in 'api', 'web', 'executor') {
                $expected = [int]$recorded.$name
                if (-not $busy.Contains($name) -or -not (Test-PidInTree -RootPid $expected -CandidatePid ([int]$busy[$name]))) { $allOurs = $false }
            }
        }
        catch { $allOurs = $false }
    }
    if ($allOurs) {
        Write-Host 'FA stack already running (pids.json matches the listeners); nothing to do (use -Stop first to rebuild).'
        exit 0
    }
    $detail = (@(foreach ($name in $busy.Keys) { "$name port $($portMap[$name]) owned by pid $($busy[$name])" }) -join '; ')
    $reason = "target ports are already in use by processes this script did not start: $detail"
    $script:failedExternal = $reason
    Save-PrepVerdict -Status 'EXTERNAL-BLOCKED' -Reason $reason
    Write-PrepLog "PREP EXTERNAL-BLOCKED: $reason"
    exit 2
}

$exitCode = 1
try {
    # -- preflight: credentials, tools, SUT and PostgreSQL ------------------
    Write-PrepLog 'preflight: reading .env (secrets are never printed)'
    $dbUrl = Get-EnvValue 'EZTEST_DATABASE_URL'
    $adminPassword = Get-EnvValue 'EZTEST_ADMIN_PASSWORD'
    $initialAdminPassword = Get-EnvValue 'EZTEST_INITIAL_ADMIN_PASSWORD'
    $executorToken = Get-EnvValue 'EZTEST_EXECUTOR_TOKEN'
    $llmBase = (Get-EnvValue 'llm_base_url').TrimEnd('/')
    $llmKey = Get-EnvValue 'llm_api_key'
    $llmModel = Get-EnvValue 'llm_model_name'
    $sutDbDsn = Get-EnvValue 'SUT_DB_DSN'
    $sutDbPassword = Get-EnvValue 'SUT_DB_PASSWORD'
    $sutApi = 'http://localhost:8080'
    $sutWeb = 'http://localhost:5173'

    $dbParts = Get-PostgresParts -Url $dbUrl
    $sutParts = Get-PostgresParts -Url $sutDbDsn
    if ([string]::IsNullOrWhiteSpace($sutDbPassword)) { $sutDbPassword = $sutParts.Password }

    $script:psqlExe = Join-Path $repoRoot '.tools\pgsql\bin\psql.exe'
    $goExe = Join-Path $repoRoot '.tools\go\bin\go.exe'
    if (-not (Test-Path -LiteralPath $script:psqlExe)) { Fail-External "PostgreSQL client missing: $script:psqlExe" }
    if (-not (Test-Path -LiteralPath $goExe)) { Fail-Internal "Go toolchain missing: $goExe" }
    $nodeExe = (Get-Command node -ErrorAction SilentlyContinue | Select-Object -First 1).Source
    if ([string]::IsNullOrWhiteSpace($nodeExe)) { Fail-Internal 'node is not on PATH' }

    if ($llmBase -match '^(https?://)?(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(:\d+)?(/|$)') {
        Fail-Internal "llm_base_url points at a local proxy ($llmBase); a real upstream is required (FA-C1)"
    }

    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

    $probe = Invoke-Psql -Database 'postgres' -Sql 'select 1' -Password $dbParts.Password `
        -DbHost $dbParts.Host -Port $dbParts.Port -User $dbParts.User
    if ($probe.ExitCode -ne 0 -or $probe.Output.Trim() -ne '1') {
        Fail-External "PostgreSQL $($dbParts.Host):$($dbParts.Port) is unreachable (psql select 1 failed)"
    }
    Write-PrepLog "preflight: PostgreSQL $($dbParts.Host):$($dbParts.Port) reachable"

    $sutHealth = Invoke-Api -Method Get -Uri "$sutApi/api/v1/health" -TimeoutSec 10
    Add-PrepCheck -Name 'sut.health' -Ok ($sutHealth.Status -eq 200) `
        -Detail "GET $sutApi/api/v1/health -> HTTP $($sutHealth.Status)" -External
    Save-CheckJson -Name 'sut-health' -Value ([pscustomobject]@{ status = $sutHealth.Status; body = $sutHealth.Body })
    if ($sutHealth.Status -ne 200) { Fail-External "SUT health $sutApi/api/v1/health returned HTTP $($sutHealth.Status)" }

    $sutPosts = Invoke-Api -Method Get -Uri "$sutApi/api/v1/posts?page=1&pageSize=1" -TimeoutSec 15
    Add-PrepCheck -Name 'sut.posts' -Ok ($sutPosts.Status -eq 200) `
        -Detail "GET /api/v1/posts?page=1&pageSize=1 -> HTTP $($sutPosts.Status)" -External
    Save-CheckJson -Name 'sut-posts' -Value ([pscustomobject]@{ status = $sutPosts.Status; body = $sutPosts.Body })

    $sutUploads = Invoke-Api -Method Get -Uri "$sutApi/uploads/.gitkeep" -TimeoutSec 10
    Add-PrepCheck -Name 'sut.uploads' -Ok ($sutUploads.Status -eq 200) `
        -Detail "GET /uploads/.gitkeep -> HTTP $($sutUploads.Status)" -External
    Save-CheckJson -Name 'sut-uploads' -Value ([pscustomobject]@{ status = $sutUploads.Status; body = $sutUploads.Body })
    $script:dbUploadsOk = ($sutUploads.Status -eq 200)

    $sutWebResp = Invoke-Api -Method Get -Uri "$sutWeb/" -TimeoutSec 15
    Add-PrepCheck -Name 'sut.web' -Ok ($sutWebResp.Status -eq 200) `
        -Detail "GET $sutWeb/ -> HTTP $($sutWebResp.Status)" -External
    Save-CheckJson -Name 'sut-web' -Value ([pscustomobject]@{ status = $sutWebResp.Status; bodyLength = ([string]$sutWebResp.Raw).Length })

    if (-not [bool]$sutPosts.Ok -and $sutPosts.Status -ne 200) {
        Fail-External "SUT posts API returned HTTP $($sutPosts.Status)"
    }

    # -- fresh database ------------------------------------------------------
    Write-PrepLog "database: recreating $dbName"
    Invoke-Psql -Database 'postgres' `
        -Sql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$dbName' AND pid <> pg_backend_pid()" `
        -Password $dbParts.Password -DbHost $dbParts.Host -Port $dbParts.Port -User $dbParts.User | Out-Null
    $drop = Invoke-Psql -Database 'postgres' -Sql "DROP DATABASE IF EXISTS $dbName" `
        -Password $dbParts.Password -DbHost $dbParts.Host -Port $dbParts.Port -User $dbParts.User
    if ($drop.ExitCode -ne 0) {
        # A connection may have appeared between terminate and drop; retry once.
        Invoke-Psql -Database 'postgres' `
            -Sql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$dbName' AND pid <> pg_backend_pid()" `
            -Password $dbParts.Password -DbHost $dbParts.Host -Port $dbParts.Port -User $dbParts.User | Out-Null
        $drop = Invoke-Psql -Database 'postgres' -Sql "DROP DATABASE IF EXISTS $dbName" `
            -Password $dbParts.Password -DbHost $dbParts.Host -Port $dbParts.Port -User $dbParts.User
    }
    if ($drop.ExitCode -ne 0) {
        Fail-External "could not drop database $dbName (psql exit=$($drop.ExitCode))"
    }
    $create = Invoke-Psql -Database 'postgres' -Sql "CREATE DATABASE $dbName" `
        -Password $dbParts.Password -DbHost $dbParts.Host -Port $dbParts.Port -User $dbParts.User
    if ($create.ExitCode -ne 0) {
        Fail-External "could not create database $dbName (psql exit=$($create.ExitCode))"
    }
    Write-PrepLog "database: $dbName created"

    # -- build the real composition root ------------------------------------
    $backendExe = Join-Path $work 'eztest.exe'
    Remove-Item -LiteralPath $backendExe -Force -ErrorAction SilentlyContinue
    Write-PrepLog 'build: go build ./cmd/eztest'
    $buildLog = Join-Path $work 'build.log'
    Push-Location (Join-Path $repoRoot 'backend')
    try {
        & $goExe build -o $backendExe ./cmd/eztest 2>&1 | Tee-Object -FilePath $buildLog
        $buildExit = $LASTEXITCODE
    }
    finally { Pop-Location }
    if ($buildExit -ne 0) { Fail-Internal "go build ./cmd/eztest failed (exit=$buildExit), see .tmp/fa/build.log" }
    if (-not (Test-Path -LiteralPath $backendExe)) { Fail-Internal "backend binary missing after build: $backendExe" }
    Write-PrepLog 'build: ok'

    # -- API ---------------------------------------------------------------
    Write-PrepLog "api: starting on $apiBase (db=$dbName)"
    $apiOut = Join-Path $work 'api.out.log'
    $apiErr = Join-Path $work 'api.err.log'
    $apiLog = Join-Path $work 'api.log'
    Remove-Item -LiteralPath $apiOut, $apiErr, $apiLog -Force -ErrorAction SilentlyContinue
    $apiProcess = Start-ManagedProcess -FilePath $backendExe -Arguments @() `
        -WorkingDirectory (Join-Path $repoRoot 'backend') -StdOut $apiOut -StdErr $apiErr `
        -Environment @{
            EZTEST_DATABASE_URL           = "postgres://$($dbParts.User):$($dbParts.Password)@$($dbParts.Host):$($dbParts.Port)/$dbName`?sslmode=disable"
            EZTEST_API_PORT               = [string]$apiPort
            EZTEST_WEB_PORT               = [string]$webPort
            EZTEST_EXECUTOR_PORT          = [string]$executorPort
            EZTEST_INITIAL_ADMIN_PASSWORD = $initialAdminPassword
        }
    $script:apiPid = [int]$apiProcess.Id
    $apiReady = Wait-Http -Uri "$apiBase/api/health/ready" -TimeoutSeconds 180
    Add-PrepCheck -Name 'api.ready' -Ok ($apiReady.Status -eq 200) `
        -Detail "GET /api/health/ready -> HTTP $($apiReady.Status)"
    Save-CheckJson -Name 'api-ready' -Value ([pscustomobject]@{ status = $apiReady.Status; body = $apiReady.Body })
    if ($apiReady.Status -ne 200) {
        $tail = ''
        if (Test-Path -LiteralPath $apiErr) {
            $tail = ((Get-Content -LiteralPath $apiErr -Tail 20 -Encoding UTF8) -join ' | ')
        }
        Fail-Internal "api did not become ready (HTTP $($apiReady.Status)); env/log tail: $tail"
    }
    Write-PrepLog "api: ready pid=$script:apiPid"

    # -- executor ----------------------------------------------------------
    Write-PrepLog "executor: starting on $executorBase"
    $executorOut = Join-Path $work 'executor.out.log'
    $executorErr = Join-Path $work 'executor.err.log'
    Remove-Item -LiteralPath $executorOut, $executorErr -Force -ErrorAction SilentlyContinue
    $executorProcess = Start-ManagedProcess -FilePath $nodeExe -Arguments @('src/server.mjs') `
        -WorkingDirectory (Join-Path $repoRoot 'executor') -StdOut $executorOut -StdErr $executorErr `
        -Environment @{
            EZTEST_EXECUTOR_TOKEN = $executorToken
            EZTEST_EXECUTOR_ADDR  = "127.0.0.1:$executorPort"
            EZTEST_API_URL        = $apiBase
            EZTEST_EXECUTOR_NAME  = 'fa'
        }
    $script:executorPid = [int]$executorProcess.Id
    $executorHealth = Wait-Http -Uri "$executorBase/healthz" -TimeoutSeconds 90
    $script:executorHttpStatus = [int]$executorHealth.Status
    Add-PrepCheck -Name 'executor.health' -Ok ($executorHealth.Status -eq 200) `
        -Detail "GET $executorBase/healthz -> HTTP $($executorHealth.Status)"
    Save-CheckJson -Name 'executor-health' -Value ([pscustomobject]@{ status = $executorHealth.Status; body = $executorHealth.Body })
    if ($executorHealth.Status -ne 200) { Fail-Internal "executor did not become ready (HTTP $($executorHealth.Status))" }
    Write-PrepLog "executor: ready pid=$script:executorPid"

    # -- web ---------------------------------------------------------------
    Write-PrepLog "web: starting next dev on $webBase"
    $webOut = Join-Path $work 'web.out.log'
    $webErr = Join-Path $work 'web.err.log'
    Remove-Item -LiteralPath $webOut, $webErr -Force -ErrorAction SilentlyContinue
    $nextBin = Join-Path $repoRoot 'frontend\node_modules\next\dist\bin\next'
    if (-not (Test-Path -LiteralPath $nextBin)) { Fail-Internal "Next.js binary missing: $nextBin" }
    $webProcess = Start-ManagedProcess -FilePath $nodeExe -Arguments @($nextBin, 'dev', '-p', [string]$webPort) `
        -WorkingDirectory (Join-Path $repoRoot 'frontend') -StdOut $webOut -StdErr $webErr `
        -Environment @{ NEXT_PUBLIC_API_BASE_URL = $apiBase }
    $script:webPid = [int]$webProcess.Id
    $webReady = Wait-Http -Uri "$webBase/login" -TimeoutSeconds 300
    Add-PrepCheck -Name 'web.login' -Ok ($webReady.Status -eq 200) `
        -Detail "GET $webBase/login -> HTTP $($webReady.Status)"
    Save-CheckJson -Name 'web-login' -Value ([pscustomobject]@{ status = $webReady.Status; bodyLength = ([string]$webReady.Raw).Length })
    if ($webReady.Status -ne 200) { Fail-Internal "web /login did not become ready (HTTP $($webReady.Status))" }
    Write-PrepLog "web: ready pid=$script:webPid"

    $pids = [pscustomobject][ordered]@{
        api       = [int]$script:apiPid
        web       = [int]$script:webPid
        executor  = [int]$script:executorPid
        db        = $dbName
        startedAt = (Get-Date).ToUniversalTime().ToString('o')
    }
    Write-JsonUtf8 -Path $pidsPath -Object $pids
    Write-PrepLog "stack: running (api=$script:apiPid web=$script:webPid executor=$script:executorPid)"

    # Next.js dev compiles a route on first request; warm the two routes the
    # screenshot step needs so Playwright does not fight the compiler timeout.
    Write-PrepLog 'web: warming /versions and /settings (next dev compile)'
    $warmVersions = Wait-Http -Uri "$webBase/versions" -TimeoutSeconds 240
    $warmSettings = Wait-Http -Uri "$webBase/settings" -TimeoutSeconds 240
    Write-PrepLog "web: warm-up status versions=$($warmVersions.Status) settings=$($warmSettings.Status)"

    # -- admin login + real LLM registration (fresh DB has no config) -------
    Write-PrepLog 'llm: logging in as admin'
    $token = ''
    try {
        $token = Login-Admin -BaseUrl $apiBase -Password $adminPassword
    }
    catch {
        Fail-Internal $_.Exception.Message
    }
    $authHeaders = @{ Authorization = "Bearer $token" }
    $me = Invoke-Api -Method Get -Uri "$apiBase/api/auth/me" -Headers $authHeaders -TimeoutSec 30
    $userJson = ''
    if ($me.Ok -and $null -ne $me.Body.data) {
        $userJson = ($me.Body.data | ConvertTo-Json -Compress -Depth 5)
    }

    $configs = Invoke-Api -Method Get -Uri "$apiBase/api/admin/llm/configs" -Headers $authHeaders -TimeoutSec 30
    if ($configs.Status -ne 200) { Fail-Internal "GET /api/admin/llm/configs -> HTTP $($configs.Status)" }
    $config = @($configs.Body.data) | Where-Object { [string]$_.name -eq 'fa-deepseek' } | Select-Object -First 1
    if ($null -eq $config) {
        $createConfig = Invoke-Api -Method Post -Uri "$apiBase/api/admin/llm/configs" -Headers $authHeaders -Body @{
            name            = 'fa-deepseek'
            base_url        = $llmBase
            api_key         = $llmKey
            protocol        = 'openai_chat_completions'
            is_default      = $true
            timeout_seconds = 180
            max_retries     = 2
        } -TimeoutSec 60
        if ($createConfig.Status -ne 200) {
            Fail-Internal "POST /api/admin/llm/configs -> HTTP $($createConfig.Status): $([string]$createConfig.Body.error.message)"
        }
        $config = $createConfig.Body.data
    }
    else {
        # Robust re-run: the config may exist from a partially failed attempt.
        $updateConfig = Invoke-Api -Method Patch -Uri "$apiBase/api/admin/llm/configs/$([string]$config.id)" `
            -Headers $authHeaders -Body @{
            base_url        = $llmBase
            api_key         = $llmKey
            protocol        = 'openai_chat_completions'
            is_default      = $true
            timeout_seconds = 180
            max_retries     = 2
        } -TimeoutSec 60
        if ($updateConfig.Status -ne 200) {
            Fail-Internal "PATCH /api/admin/llm/configs/$([string]$config.id) -> HTTP $($updateConfig.Status)"
        }
        $config = $updateConfig.Body.data
    }
    $configId = [string]$config.id
    Write-PrepLog "llm: config id=$configId name=fa-deepseek base=$llmBase"

    $model = @($config.models) | Where-Object { [string]$_.model -eq $llmModel } | Select-Object -First 1
    if ($null -eq $model) {
        $addModel = Invoke-Api -Method Post -Uri "$apiBase/api/admin/llm/configs/$configId/models" `
            -Headers $authHeaders -Body @{
            model                 = $llmModel
            context_window        = 128000
            max_output            = 4096
            supports_tools        = $true
            supports_vision       = $true
            supports_reasoning    = $false
            supports_prompt_cache = $false
        } -TimeoutSec 60
        if ($addModel.Status -ne 200) {
            Fail-Internal "POST /api/admin/llm/configs/$configId/models -> HTTP $($addModel.Status): $([string]$addModel.Body.error.message)"
        }
        $model = $addModel.Body.data
    }
    else {
        $updateModel = Invoke-Api -Method Patch -Uri "$apiBase/api/admin/llm/configs/$configId/models/$([string]$model.id)" `
            -Headers $authHeaders -Body @{
            context_window        = 128000
            max_output            = 4096
            supports_tools        = $true
            supports_vision       = $true
            supports_reasoning    = $false
            supports_prompt_cache = $false
        } -TimeoutSec 60
        if ($updateModel.Status -ne 200) {
            Fail-Internal "PATCH model $([string]$model.id) -> HTTP $($updateModel.Status)"
        }
        $model = $updateModel.Body.data
    }
    $modelId = [string]$model.id
    $setDefault = Invoke-Api -Method Patch -Uri "$apiBase/api/admin/llm/configs/$configId" `
        -Headers $authHeaders -Body @{ is_default = $true; default_model_id = $modelId } -TimeoutSec 60
    if ($setDefault.Status -ne 200) {
        Fail-Internal "PATCH default model -> HTTP $($setDefault.Status): $([string]$setDefault.Body.error.message)"
    }
    Write-PrepLog "llm: model=$llmModel id=$modelId supports_vision=true default=$modelId"

    $configAfter = Invoke-Api -Method Get -Uri "$apiBase/api/admin/llm/configs" -Headers $authHeaders -TimeoutSec 30
    Save-CheckJson -Name 'llm-configs' -Value ([pscustomobject]@{ status = $configAfter.Status; body = $configAfter.Body })
    if ($configAfter.Status -ne 200) { Fail-Internal "GET /api/admin/llm/configs (verify) -> HTTP $($configAfter.Status)" }
    $configRow = @($configAfter.Body.data) | Where-Object { [string]$_.id -eq $configId } | Select-Object -First 1
    if ($null -eq $configRow -or [string]$configRow.default_model_id -ne $modelId) {
        Fail-Internal "fresh LLM config default_model_id is not $modelId after registration"
    }

    $catalog = Invoke-Api -Method Get -Uri "$apiBase/api/llm/models" -Headers $authHeaders -TimeoutSec 30
    Save-CheckJson -Name 'llm-models' -Value ([pscustomobject]@{ status = $catalog.Status; body = $catalog.Body })
    if ($catalog.Status -ne 200) { Fail-Internal "GET /api/llm/models -> HTTP $($catalog.Status)" }
    $catalogItem = @($catalog.Body.data) | Where-Object { [string]$_.model -eq $llmModel } | Select-Object -First 1
    if ($null -eq $catalogItem) {
        $script:catalogSupportsVision = $false
        $script:catalogDefault = $false
    }
    else {
        $script:catalogSupportsVision = [bool]$catalogItem.capabilities.supports_vision
        $script:catalogDefault = [bool]$catalogItem.default
    }
    Add-PrepCheck -Name 'vision.catalog' -Ok ($script:catalogSupportsVision -and $script:catalogDefault) `
        -Detail "GET /api/llm/models model=$llmModel supports_vision=$($script:catalogSupportsVision) default=$($script:catalogDefault)"
    if (-not ($script:catalogSupportsVision -and $script:catalogDefault)) {
        Fail-Internal "model $llmModel is not the default vision-capable model in GET /api/llm/models"
    }

    # -- real vision probe against the .env upstream (no proxy) ------------
    Write-PrepLog "vision: probing real upstream $llmBase with an image"
    Add-Type -AssemblyName System.Drawing
    $probePng = Join-Path $work 'vision-probe.png'
    $bitmap = New-Object System.Drawing.Bitmap 480, 160
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $font = New-Object System.Drawing.Font('Arial', 40, [System.Drawing.FontStyle]::Bold)
    try {
        $graphics.Clear([System.Drawing.Color]::White)
        $graphics.DrawString('VISION-OK-42', $font, [System.Drawing.Brushes]::Black, 20, 50)
        $bitmap.Save($probePng, [System.Drawing.Imaging.ImageFormat]::Png)
    }
    finally {
        $font.Dispose()
        $graphics.Dispose()
        $bitmap.Dispose()
    }
    $b64 = [Convert]::ToBase64String([System.IO.File]::ReadAllBytes($probePng))
    $visionBody = @{
        model      = $llmModel
        max_tokens = 256
        stream     = $false
        messages   = @(
            @{
                role    = 'user'
                content = @(
                    @{ type = 'text'; text = 'What exact text is written in this image? Reply with the text only.' },
                    @{ type = 'image_url'; image_url = @{ url = "data:image/png;base64,$b64" } }
                )
            }
        )
    }
    $visionResponse = Invoke-Api -Method Post -Uri "$llmBase/chat/completions" `
        -Headers @{ Authorization = "Bearer $llmKey" } -Body $visionBody -TimeoutSec 180
    $script:visionHttpStatus = [int]$visionResponse.Status
    $content = $null
    if ($null -ne $visionResponse.Body -and $null -ne $visionResponse.Body.choices) {
        $message = @($visionResponse.Body.choices)[0].message
        if ($null -ne $message) {
            $content = $message.content
            if ([string]::IsNullOrWhiteSpace([string]$content)) { $content = $message.reasoning_content }
        }
    }
    if ($content -is [System.Array]) {
        $content = (@($content) | ForEach-Object { [string]$_.text }) -join ' '
    }
    $script:visionCaption = ([string]$content).Trim()
    $script:visionModel = $llmModel
    $script:visionBaseUrl = $llmBase
    $script:visionExercised = ($script:visionHttpStatus -eq 200 -and -not [string]::IsNullOrWhiteSpace($script:visionCaption))
    $null = Add-PrepCheck -Name 'vision.upstream' -Ok ($script:visionHttpStatus -eq 200) `
        -Detail "POST $llmBase/chat/completions (image_url data URL) -> HTTP $($script:visionHttpStatus)"
    if ($script:visionHttpStatus -ne 200) {
        Fail-External "real vision upstream returned HTTP $($script:visionHttpStatus) for the image request"
    }
    $captionPreview = $script:visionCaption
    if ($captionPreview.Length -gt 160) { $captionPreview = $captionPreview.Substring(0, 160) }
    Add-PrepCheck -Name 'vision.caption' -Ok (-not [string]::IsNullOrWhiteSpace($script:visionCaption)) `
        -Detail "caption length=$($script:visionCaption.Length) preview=$captionPreview"
    Save-CheckJson -Name 'vision-upstream' -Value ([pscustomobject]@{
            status   = $script:visionHttpStatus
            model    = $llmModel
            baseUrl  = $llmBase
            caption  = $script:visionCaption
            response = $visionResponse.Body
        })
    if (-not $script:visionExercised) {
        Fail-Internal 'real vision probe returned an empty caption (visionExercised=false)'
    }
    Write-PrepLog "vision: exercised=true model=$llmModel http=$script:visionHttpStatus captionLen=$($script:visionCaption.Length)"

    # -- direct DB probes ---------------------------------------------------
    $eztestDb = Invoke-Psql -Database $dbName -Sql 'select 1' -Password $dbParts.Password `
        -DbHost $dbParts.Host -Port $dbParts.Port -User $dbParts.User
    $script:dbEztestOk = ($eztestDb.ExitCode -eq 0 -and $eztestDb.Output.Trim() -eq '1')
    Add-PrepCheck -Name 'db.eztest' -Ok $script:dbEztestOk `
        -Detail "psql eztest_v022_fa select 1 exit=$($eztestDb.ExitCode)"
    Save-CheckJson -Name 'db-eztest' -Value ([pscustomobject]@{ exit = $eztestDb.ExitCode; output = $eztestDb.Output })

    $sutDb = Invoke-Psql -Database $sutParts.Database -Sql 'select 1' -Password $sutDbPassword `
        -DbHost $sutParts.Host -Port $sutParts.Port -User $sutParts.User
    $script:dbSutOk = ($sutDb.ExitCode -eq 0 -and $sutDb.Output.Trim() -eq '1')
    Add-PrepCheck -Name 'db.sut' -Ok $script:dbSutOk `
        -Detail "psql SUT database select 1 exit=$($sutDb.ExitCode)" -External
    Save-CheckJson -Name 'db-sut' -Value ([pscustomobject]@{ exit = $sutDb.ExitCode; output = $sutDb.Output })

    # -- executor auth: Bearer 200 + no-token 401 --------------------------
    $contexts = Invoke-Api -Method Get -Uri "$executorBase/v1/contexts" `
        -Headers @{ Authorization = "Bearer $executorToken" } -TimeoutSec 30
    $contextsNoToken = Invoke-Api -Method Get -Uri "$executorBase/v1/contexts" -TimeoutSec 30
    $script:executorBearer200 = ($contexts.Status -eq 200)
    $script:executorNoToken401 = ($contextsNoToken.Status -eq 401)
    Add-PrepCheck -Name 'executor.auth' -Ok ($script:executorBearer200 -and $script:executorNoToken401) `
        -Detail "Bearer GET /v1/contexts -> HTTP $($contexts.Status); no-token -> HTTP $($contextsNoToken.Status)"
    Save-CheckJson -Name 'executor-contexts' -Value ([pscustomobject]@{ status = $contexts.Status; body = $contexts.Body })
    Save-CheckJson -Name 'executor-unauthorized' -Value ([pscustomobject]@{ status = $contextsNoToken.Status; body = $contextsNoToken.Body })

    # -- screenshots: real browser, /versions and /settings ----------------
    Write-PrepLog 'screenshots: launching Playwright chrome'
    $shotsScript = Join-Path $work 'shots.mjs'
    @'
import path from "node:path"
import { createRequire } from "node:module"

const requireFromExecutor = createRequire(path.join(process.env.FA_REPO_ROOT, "executor", "package.json"))
const { chromium } = requireFromExecutor("playwright")
const web = process.env.FA_WEB
const outDir = process.env.FA_SHOT_DIR
const adminPassword = process.env.FA_ADMIN_PASSWORD
const apiToken = process.env.FA_TOKEN
const apiUser = process.env.FA_USER_JSON

let browser = null
try {
  browser = await chromium.launch({ headless: true, channel: "chrome" })
} catch (err) {
  browser = await chromium.launch({ headless: true, channel: "msedge" })
}
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  page.setDefaultTimeout(180000)
  let uiLogin = false
  await page.goto(`${web}/login`, { waitUntil: "domcontentloaded", timeout: 180000 })
  await page.waitForTimeout(2000)
  try {
    if (await page.locator("#username").count()) {
      await page.fill("#username", "admin")
      await page.fill("#password", adminPassword)
      await page.locator('button[type="submit"]').click()
      await page.waitForURL(/\/versions/, { timeout: 120000 })
      uiLogin = true
    }
  } catch (err) {
    console.error(`[shots] UI login did not complete: ${err.message}`)
  }

  if (!uiLogin || page.url().includes("/login")) {
    // Deterministic fallback: seed the real browser session with the token the
    // API issued to this script, then open the protected page directly.
    try {
      await page.goto(`${web}/login`, { waitUntil: "domcontentloaded", timeout: 180000 })
    } catch (err) {
      console.error(`[shots] reload before session fallback failed: ${err.message}`)
    }
    await page.evaluate(([tokenValue, userValue]) => {
      window.sessionStorage.setItem("eztest.token", tokenValue)
      window.sessionStorage.setItem("eztest.user", userValue)
    }, [apiToken, apiUser])
    await page.goto(`${web}/versions`, { waitUntil: "domcontentloaded", timeout: 180000 })
  }

  if (!page.url().includes("/versions")) {
    throw new Error(`/versions not reached, current url=${page.url()}`)
  }
  await page.waitForTimeout(2500)
  await page.screenshot({ path: path.join(outDir, "versions.png"), fullPage: true })
  const versionsUrl = page.url()

  await page.goto(`${web}/settings`, { waitUntil: "domcontentloaded", timeout: 180000 })
  if (page.url().includes("/login")) {
    throw new Error(`/settings redirected to login: ${page.url()}`)
  }
  await page.waitForTimeout(3000)
  await page.screenshot({ path: path.join(outDir, "settings.png"), fullPage: true })
  console.log(JSON.stringify({ ok: true, uiLogin, versionsUrl, settingsUrl: page.url() }))
} finally {
  await browser.close()
}
'@ | ForEach-Object {
        [System.IO.File]::WriteAllText($shotsScript, $_, $utf8NoBom)
    }

    $shotOutput = ''
    $shotExit = 1
    $shotEnvironment = @{
        FA_REPO_ROOT      = $repoRoot
        FA_WEB            = $webBase
        FA_SHOT_DIR       = $shotDir
        FA_ADMIN_PASSWORD = $adminPassword
        FA_TOKEN          = $token
        FA_USER_JSON      = $userJson
    }
    $previousEnvironment = @{}
    foreach ($key in $shotEnvironment.Keys) {
        $previousEnvironment[$key] = [System.Environment]::GetEnvironmentVariable($key, 'Process')
        [System.Environment]::SetEnvironmentVariable($key, [string]$shotEnvironment[$key], 'Process')
    }
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $shotOutput = (& $nodeExe $shotsScript 2>&1 | Out-String).Trim()
        $shotExit = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorAction
        foreach ($key in $shotEnvironment.Keys) {
            [System.Environment]::SetEnvironmentVariable($key, $previousEnvironment[$key], 'Process')
        }
    }
    if ($shotExit -ne 0) {
        Fail-Internal "Playwright screenshots failed (node exit=$shotExit): $shotOutput"
    }
    Write-PrepLog "screenshots: $shotOutput"

    $shotRed = New-Object System.Collections.Generic.List[string]
    foreach ($shotName in @('versions.png', 'settings.png')) {
        foreach ($item in @(Assert-Png -Path (Join-Path $shotDir $shotName))) { $shotRed.Add($item) }
    }
    Add-PrepCheck -Name 'screenshots' -Ok ($shotRed.Count -eq 0) -Detail $shotRed
    if ($shotRed.Count -gt 0) { Fail-Internal "screenshot validation failed: $($shotRed -join '; ')" }

    # -- verdict -----------------------------------------------------------
    $allOk = $true
    foreach ($check in $script:checks) { if (-not [bool]$check.ok) { $allOk = $false } }
    $requiredNames = @('db.eztest', 'db.sut', 'api.ready', 'executor.health', 'executor.auth',
        'sut.health', 'sut.posts', 'sut.uploads', 'sut.web', 'vision.upstream', 'vision.catalog', 'screenshots')
    $presentNames = @($script:checks | ForEach-Object { [string]$_.name })
    foreach ($requiredName in $requiredNames) {
        if ($presentNames -notcontains $requiredName) {
            Add-PrepCheck -Name $requiredName -Ok $false -Detail 'required check was not produced'
            $allOk = $false
        }
    }

    if ($allOk) {
        Save-PrepVerdict -Status 'PASS' -Reason ''
        Write-PrepLog 'PREP PASS: stack left running for FA-4/FA-5'
        $exitCode = 0
    }
    else {
        $reason = if ($null -ne $script:failedExternal) { $script:failedExternal }
        elseif ($null -ne $script:failedInternal) { $script:failedInternal }
        else { 'one or more prep checks failed' }
        $status = if ($null -ne $script:failedExternal) { 'EXTERNAL-BLOCKED' } else { 'FAIL' }
        Save-PrepVerdict -Status $status -Reason $reason
        Write-PrepLog "PREP $status`: $reason"
        Stop-PrepStackFromIds -ApiPid $script:apiPid -WebPid $script:webPid -ExecutorPid $script:executorPid
        $exitCode = if ($status -eq 'EXTERNAL-BLOCKED') { 2 } else { 1 }
    }
}
catch {
    $message = [string]$_.Exception.Message
    $isExternal = $message.StartsWith('EXTERNAL:')
    $isInternal = $message.StartsWith('INTERNAL:')
    if ($isExternal -or $isInternal) {
        $reason = $message.Substring($message.IndexOf(':') + 1).Trim()
    }
    else {
        $reason = "unexpected prep failure: $message"
    }
    $status = if ($isExternal) { 'EXTERNAL-BLOCKED' } else { 'FAIL' }
    if ($null -eq $script:failedExternal -and $null -eq $script:failedInternal) {
        if ($isExternal) { $script:failedExternal = $reason } else { $script:failedInternal = $reason }
    }
    try {
        Save-PrepVerdict -Status $status -Reason $reason
    }
    catch {
        Write-PrepLog "could not write prep-verdict: $($_.Exception.Message) at line $($_.InvocationInfo.ScriptLineNumber)"
    }
    Write-PrepLog "PREP $status`: $reason"
    Stop-PrepStackFromIds -ApiPid $script:apiPid -WebPid $script:webPid -ExecutorPid $script:executorPid
    $exitCode = if ($status -eq 'EXTERNAL-BLOCKED') { 2 } else { 1 }
}

exit $exitCode
