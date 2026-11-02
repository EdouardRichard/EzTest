#requires -Version 5.1
<#
.SYNOPSIS
    Statically checks that the CI workflow keeps the EzTest baseline test gate.

.DESCRIPTION
    Reads .github/workflows/ci.yml, strips YAML comments and parses the jobs /
    steps structure, then asserts that the workflow still wires up the gates the
    repository expects:

      - push / pull_request triggers;
      - a test job on Ubuntu with a PostgreSQL 17 service published on 5433;
      - Go unit tests (go test -count=1 ./...) and the integration suite
        (go test -tags=integration -count=1 ./...) in backend/, with
        EZTEST_TEST_DATABASE_URL pointing at the dedicated test database;
      - Node 22 + pnpm, frontend install / lint / typecheck / test in frontend/,
        executor frozen install / test in executor/, and an explicit Chromium
        install for the Playwright-based executor tests;
      - a static acceptance job on Windows running
        scripts/acceptance.ps1 -StaticOnly and this structural check.

    Every gate is matched as a bound step: the step name, the step's
    working-directory and its run command must all agree. A bare "pnpm test"
    elsewhere in the file cannot satisfy the frontend unit-test gate.

    The shared test commands are compared against the Invoke-Checked commands
    that scripts/test.ps1 actually runs, so the parity check cannot be satisfied
    by a comment. CI intentionally omits the local fake-LLM contract scripts,
    Playwright E2E and real-LLM acceptance.

    This is a structural (not execution) check. It exits 1 with one FAIL line
    per missing/forbidden fragment and exits 0 on success.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$workflowPath = Join-Path $repoRoot ".github\workflows\ci.yml"
$relativeWorkflowPath = ".github/workflows/ci.yml"

if (-not (Test-Path -LiteralPath $workflowPath)) {
    Write-Host ("FAIL: {0} does not exist; create the baseline workflow first." -f $relativeWorkflowPath) -ForegroundColor Red
    exit 1
}

function Read-Utf8Text {
    param([string]$Path)
    $text = [System.IO.File]::ReadAllText($Path, [System.Text.Encoding]::UTF8)
    if ($text.Length -gt 0 -and $text[0] -eq [char]0xFEFF) {
        $text = $text.Substring(1)
    }
    return $text
}

$workflow = Read-Utf8Text -Path $workflowPath
if ([string]::IsNullOrWhiteSpace($workflow)) {
    Write-Host ("FAIL: {0} is empty." -f $relativeWorkflowPath) -ForegroundColor Red
    exit 1
}

$script:failures = New-Object System.Collections.ArrayList

function Fail-Check {
    param([string]$Description, [string]$Detail)
    $message = $Description
    if (-not [string]::IsNullOrWhiteSpace($Detail)) {
        $message = "{0} ({1})" -f $Description, $Detail
    }
    $script:failures.Add($message) | Out-Null
    Write-Host ("FAIL: {0}" -f $message) -ForegroundColor Red
}

function Pass-Check {
    param([string]$Description)
    Write-Host ("PASS: {0}" -f $Description) -ForegroundColor Green
}

function Assert-Check {
    param(
        [bool]$Condition,
        [string]$Description,
        [string]$Detail
    )
    if ($Condition) {
        Pass-Check -Description $Description
        return
    }
    Fail-Check -Description $Description -Detail $Detail
}

# Removes a YAML comment from one line: a '#' starts a comment when it is the
# first non-space character or is preceded by whitespace and is not inside a
# single- or double-quoted scalar. This is deliberately line-based; it removes
# whole-line comments (the common way of disabling a step) and inline comments.
function Remove-YamlComment {
    param([string]$Line)
    if ([string]::IsNullOrEmpty($Line)) { return $Line }

    $inSingleQuote = $false
    $inDoubleQuote = $false
    $escaped = $false
    $singleQuote = [char]"'"
    $doubleQuote = [char]'"'
    $backslash = [char]'\'
    $hash = [char]'#'

    for ($i = 0; $i -lt $Line.Length; $i++) {
        $ch = $Line[$i]

        if ($inDoubleQuote) {
            if ($escaped) { $escaped = $false; continue }
            if ($ch -eq $backslash) { $escaped = $true; continue }
            if ($ch -eq $doubleQuote) { $inDoubleQuote = $false }
            continue
        }
        if ($inSingleQuote) {
            if ($ch -eq $singleQuote) {
                if (($i + 1) -lt $Line.Length -and $Line[$i + 1] -eq $singleQuote) {
                    $i++
                    continue
                }
                $inSingleQuote = $false
            }
            continue
        }

        if ($ch -eq $hash) {
            if ($i -eq 0 -or [char]::IsWhiteSpace($Line[$i - 1])) {
                return $Line.Substring(0, $i)
            }
            continue
        }
        if ($ch -eq $doubleQuote) { $inDoubleQuote = $true; continue }
        if ($ch -eq $singleQuote) { $inSingleQuote = $true }
    }

    return $Line
}

function ConvertTo-WorkflowScalar {
    param([string]$Value)
    $trimmed = $Value.Trim()

    if ($trimmed.StartsWith('[') -and $trimmed.EndsWith(']')) {
        $inner = $trimmed.Substring(1, $trimmed.Length - 2).Trim()
        if ($inner.Length -eq 0) { return @() }
        $items = New-Object System.Collections.ArrayList
        foreach ($part in ($inner -split ',')) {
            $items.Add((ConvertTo-WorkflowScalar -Value $part.Trim())) | Out-Null
        }
        return $items.ToArray()
    }

    if ($trimmed.Length -ge 2) {
        $first = $trimmed.Substring(0, 1)
        $last = $trimmed.Substring($trimmed.Length - 1, 1)
        if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
            $trimmed = $trimmed.Substring(1, $trimmed.Length - 2)
        }
    }

    return $trimmed
}

# Adds one key to a YAML mapping and, for an empty value, creates the nested
# mapping or sequence indicated by the next more-indented line. Block scalars
# (|, >, |-, >-) are folded into a single string. The caller's line index is
# advanced past block-scalar bodies through $Index.
function Add-WorkflowMapEntry {
    param(
        [System.Collections.IDictionary]$Map,
        [string]$Key,
        [string]$RawValue,
        [int]$Indent,
        [object[]]$Entries,
        [ref]$Index,
        $Stack
    )

    if ([string]::IsNullOrWhiteSpace($RawValue)) {
        $nextIndex = $Index.Value + 1
        if ($nextIndex -lt $Entries.Count -and [int]$Entries[$nextIndex].Indent -gt $Indent) {
            $next = $Entries[$nextIndex]
            $child = $null
            $childKind = 'map'
            if ($next.Text -eq '-' -or ([string]$next.Text).StartsWith('- ')) {
                $child = New-Object System.Collections.ArrayList
                $childKind = 'list'
            } else {
                $child = @{}
            }
            $Map[$Key] = $child
            $Stack.Add([pscustomobject]@{
                    Kind = $childKind
                    Indent = [int]$next.Indent
                    Node = $child
                }) | Out-Null
        } else {
            $Map[$Key] = $null
        }
        return
    }

    $value = $RawValue.Trim()
    if ($value -match '^[|>][+-]?$') {
        $parts = New-Object System.Collections.ArrayList
        $j = $Index.Value + 1
        while ($j -lt $Entries.Count -and [int]$Entries[$j].Indent -gt $Indent) {
            $parts.Add([string]$Entries[$j].Text) | Out-Null
            $j++
        }
        $Index.Value = $j - 1
        $separator = "`n"
        if ($value.StartsWith('>')) { $separator = ' ' }
        $Map[$Key] = ($parts -join $separator)
        return
    }

    $Map[$Key] = ConvertTo-WorkflowScalar -Value $value
}

# Parses the GitHub Actions YAML subset used by ci.yml into nested hashtables
# and lists. It understands indentation-based mappings, sequences and block
# scalars, which is enough to bind step names to working-directory and run.
function ConvertFrom-WorkflowYaml {
    param([object[]]$Entries)

    $root = @{}
    $stack = New-Object System.Collections.ArrayList
    $stack.Add([pscustomobject]@{ Kind = 'map'; Indent = 0; Node = $root }) | Out-Null

    $i = 0
    while ($i -lt $Entries.Count) {
        $entry = $Entries[$i]
        $indent = [int]$entry.Indent
        $text = [string]$entry.Text

        while ($stack.Count -gt 1 -and $stack[$stack.Count - 1].Indent -gt $indent) {
            $stack.RemoveAt($stack.Count - 1)
        }
        $top = $stack[$stack.Count - 1]
        $isListItem = ($text -eq '-') -or $text.StartsWith('- ')

        if ($isListItem) {
            if ($top.Kind -ne 'list' -or $top.Indent -ne $indent) {
                throw ("line {0}: list item is not at a list's indentation" -f $entry.Line)
            }
            $list = $top.Node
            $content = $text.Substring(1).Trim()
            if ($content.Length -eq 0) {
                $list.Add($null) | Out-Null
                $i++
                continue
            }

            $itemMatch = [regex]::Match($content, '^([^:#][^:]*):(?:[ \t]+(.*))?$')
            if ($itemMatch.Success) {
                $item = @{}
                $list.Add($item) | Out-Null
                $keyIndent = $indent + 2
                $stack.Add([pscustomobject]@{ Kind = 'map'; Indent = $keyIndent; Node = $item }) | Out-Null
                $key = $itemMatch.Groups[1].Value.Trim()
                $rawValue = ''
                if ($itemMatch.Groups[2].Success) { $rawValue = $itemMatch.Groups[2].Value }
                Add-WorkflowMapEntry -Map $item -Key $key -RawValue $rawValue -Indent $keyIndent `
                    -Entries $Entries -Index ([ref]$i) -Stack $stack
            } else {
                $list.Add((ConvertTo-WorkflowScalar -Value $content)) | Out-Null
            }
            $i++
            continue
        }

        if ($top.Kind -ne 'map' -or $top.Indent -ne $indent) {
            throw ("line {0}: expected a mapping key at indentation {1}" -f $entry.Line, $indent)
        }

        $mapMatch = [regex]::Match($text, '^([^:#][^:]*):(?:[ \t]+(.*))?$')
        if (-not $mapMatch.Success) {
            throw ("line {0}: cannot parse mapping key" -f $entry.Line)
        }
        $key = $mapMatch.Groups[1].Value.Trim()
        $rawValue = ''
        if ($mapMatch.Groups[2].Success) { $rawValue = $mapMatch.Groups[2].Value }
        Add-WorkflowMapEntry -Map $top.Node -Key $key -RawValue $rawValue -Indent $indent `
            -Entries $Entries -Index ([ref]$i) -Stack $stack
        $i++
    }

    return $root
}

# Remove comments and blank lines, then index the remaining lines by their
# indentation. Any tab used as indentation would break the structural parser, so
# fail explicitly rather than silently mis-reading the workflow.
$entries = New-Object System.Collections.ArrayList
$strippedLines = New-Object System.Collections.ArrayList
$lineNumber = 0
foreach ($rawLine in ($workflow -split "\r?\n")) {
    $lineNumber++
    $withoutComment = Remove-YamlComment -Line $rawLine
    if ($withoutComment -match '^[ ]*\t') {
        Write-Host ("FAIL: {0}:{1} uses a tab for indentation; YAML requires spaces." -f $relativeWorkflowPath, $lineNumber) -ForegroundColor Red
        exit 1
    }
    if ([string]::IsNullOrWhiteSpace($withoutComment)) { continue }
    $strippedLines.Add($withoutComment.TrimEnd()) | Out-Null
    $indent = 0
    while ($indent -lt $withoutComment.Length -and $withoutComment[$indent] -eq ' ') { $indent++ }
    $entries.Add([pscustomobject]@{
            Indent = $indent
            Text = $withoutComment.Substring($indent).TrimEnd()
            Line = $lineNumber
        }) | Out-Null
}
$script:strippedText = $strippedLines -join "`n"

try {
    $model = ConvertFrom-WorkflowYaml -Entries $entries.ToArray()
} catch {
    Write-Host ("FAIL: could not parse {0} as the expected GitHub Actions YAML subset: {1}" -f $relativeWorkflowPath, $_.Exception.Message) -ForegroundColor Red
    exit 1
}

function Get-MapValue {
    param($Map, [string]$Key)
    if (-not ($Map -is [System.Collections.IDictionary])) { return $null }
    if (-not $Map.Contains($Key)) { return $null }
    return $Map[$Key]
}

function Get-Steps {
    param($Job)
    if (-not ($Job -is [System.Collections.IDictionary])) { return @() }
    if (-not $Job.Contains('steps')) { return @() }
    $steps = $Job['steps']
    if ($null -eq $steps) { return @() }
    return @($steps)
}

function Find-StepByName {
    param($Job, [string]$Name)
    $found = @()
    foreach ($step in (Get-Steps -Job $Job)) {
        if (-not ($step -is [System.Collections.IDictionary])) { continue }
        $stepName = Get-MapValue -Map $step -Key 'name'
        if (($stepName -is [string]) -and $stepName.Trim() -ieq $Name) {
            $found += $step
        }
    }
    return $found
}

# A gate is satisfied only when a single step with the expected name also has
# the expected working-directory and run command. Deleting the step, renaming
# it, moving the command to another directory or commenting it out all fail.
function Test-StepGate {
    param(
        $Job,
        [string]$JobId,
        [string]$StepName,
        [string]$Run,
        [string]$Description,
        [string]$WorkingDirectory
    )

    $matchesForStep = @(Find-StepByName -Job $Job -Name $StepName)
    if ($matchesForStep.Count -eq 0) {
        Fail-Check -Description $Description -Detail ("no step named '{0}' in job '{1}'" -f $StepName, $JobId)
        return
    }
    if ($matchesForStep.Count -gt 1) {
        Fail-Check -Description $Description -Detail ("step name '{0}' is duplicated in job '{1}'" -f $StepName, $JobId)
        return
    }

    $step = $matchesForStep[0]
    if (-not [string]::IsNullOrEmpty($WorkingDirectory)) {
        $actualWorkingDirectory = Get-MapValue -Map $step -Key 'working-directory'
        if (-not ($actualWorkingDirectory -is [string]) -or $actualWorkingDirectory.Trim() -ne $WorkingDirectory) {
            $rendered = '<missing>'
            if ($actualWorkingDirectory -is [string]) { $rendered = $actualWorkingDirectory }
            Fail-Check -Description $Description -Detail ("step '{0}' in job '{1}' has working-directory '{2}', expected '{3}'" -f $StepName, $JobId, $rendered, $WorkingDirectory)
            return
        }
    }

    $actualRun = Get-MapValue -Map $step -Key 'run'
    if (-not ($actualRun -is [string])) {
        Fail-Check -Description $Description -Detail ("step '{0}' in job '{1}' has no run command" -f $StepName, $JobId)
        return
    }
    $normalizedRun = ($actualRun -replace '\s+', ' ').Trim()
    if ($normalizedRun -ne $Run) {
        Fail-Check -Description $Description -Detail ("step '{0}' in job '{1}' runs '{2}', expected '{3}'" -f $StepName, $JobId, $normalizedRun, $Run)
        return
    }

    Pass-Check -Description $Description
}

function Get-StepByUses {
    param($Job, [string]$Pattern)
    foreach ($step in (Get-Steps -Job $Job)) {
        if (-not ($step -is [System.Collections.IDictionary])) { continue }
        $uses = Get-MapValue -Map $step -Key 'uses'
        if (($uses -is [string]) -and ($uses -match $Pattern)) {
            return $step
        }
    }
    return $null
}

function Test-ForbiddenPattern {
    param([string]$Pattern, [string]$Description)
    if ([regex]::IsMatch($script:strippedText, $Pattern, [System.Text.RegularExpressions.RegexOptions]::IgnoreCase)) {
        Fail-Check -Description $Description -Detail ("forbidden pattern matched: {0}" -f $Pattern)
        return
    }
    Pass-Check -Description $Description
}

# Reads the commands scripts/test.ps1 really executes (its Invoke-Checked calls)
# so the CI parity check is a comparison, not a comment reference.
function Get-LocalGateCommands {
    param([string]$RepoRoot)
    $testScriptPath = Join-Path $RepoRoot 'scripts\test.ps1'
    if (-not (Test-Path -LiteralPath $testScriptPath)) { return $null }

    $content = Read-Utf8Text -Path $testScriptPath
    $pattern = '(?s)-Label\s+"(?<label>[^"]+)"\s+-Exe\s+(?<exe>\$[A-Za-z_][A-Za-z0-9_.]*)\s*`?\s*-Arguments\s+@\((?<args>[^)]*)\)\s*`?\s*-WorkingDirectory\s+(?<wd>\$[A-Za-z_][A-Za-z0-9_]*)'
    $commands = @{}

    foreach ($match in [regex]::Matches($content, $pattern)) {
        $label = $match.Groups['label'].Value
        $exeVariable = $match.Groups['exe'].Value
        $argumentsText = $match.Groups['args'].Value
        $workingDirectoryVariable = $match.Groups['wd'].Value

        $arguments = @()
        foreach ($argumentMatch in [regex]::Matches($argumentsText, '"([^"]*)"')) {
            $arguments += $argumentMatch.Groups[1].Value
        }

        $exeName = $null
        switch -Regex ($exeVariable) {
            '^\$goExe$' { $exeName = 'go'; break }
            '^\$pnpmExe$' { $exeName = 'pnpm'; break }
            '^\$pnpmCommand\.Source$' { $exeName = 'pnpm'; break }
            '^\$nodeExe$' { $exeName = 'node'; break }
            default { $exeName = $null }
        }

        $workingDirectory = $null
        switch -Regex ($workingDirectoryVariable) {
            '^\$backendDir$' { $workingDirectory = 'backend'; break }
            '^\$frontendDir$' { $workingDirectory = 'frontend'; break }
            '^\$executorDir$' { $workingDirectory = 'executor'; break }
            '^\$repoRoot$' { $workingDirectory = '.'; break }
            default { $workingDirectory = $null }
        }

        if ($null -ne $exeName -and $null -ne $workingDirectory) {
            $commands[$label] = @{
                Command = (($exeName + ' ' + ($arguments -join ' ')).Trim())
                WorkingDirectory = $workingDirectory
            }
        }
    }

    return $commands
}

# --- Trigger and job structure ------------------------------------------------
$onValue = $null
if ($model.Contains('on')) { $onValue = $model['on'] }
$triggerNames = @()
if ($onValue -is [System.Collections.IDictionary]) {
    foreach ($triggerKey in $onValue.Keys) { $triggerNames += [string]$triggerKey }
} elseif ($null -ne $onValue) {
    if ($onValue -is [array]) {
        foreach ($trigger in $onValue) { $triggerNames += [string]$trigger }
    } else {
        $triggerNames += [string]$onValue
    }
}
$hasPush = $false
$hasPullRequest = $false
foreach ($triggerName in $triggerNames) {
    if ($triggerName.Trim() -ieq 'push') { $hasPush = $true }
    if ($triggerName.Trim() -ieq 'pull_request') { $hasPullRequest = $true }
}
Assert-Check -Condition ($hasPush -and $hasPullRequest) -Description 'workflow runs on push and pull_request' `
    -Detail ("triggers: {0}" -f ($triggerNames -join ', '))

$jobs = $null
if ($model.Contains('jobs')) { $jobs = $model['jobs'] }
if (-not ($jobs -is [System.Collections.IDictionary])) {
    Fail-Check -Description 'workflow defines jobs' -Detail 'jobs: mapping is missing or malformed'
    $jobs = @{}
}

$testJob = $null
if ($jobs.Contains('test')) { $testJob = $jobs['test'] }
$testRunsOn = Get-MapValue -Map $testJob -Key 'runs-on'
Assert-Check -Condition (($testRunsOn -is [string]) -and $testRunsOn.Trim() -eq 'ubuntu-latest') `
    -Description 'test job runs on Ubuntu' -Detail ("runs-on: {0}" -f $testRunsOn)

$services = Get-MapValue -Map $testJob -Key 'services'
$postgresService = $null
if ($services -is [System.Collections.IDictionary] -and $services.Contains('postgres')) {
    $postgresService = $services['postgres']
}
$postgresImage = Get-MapValue -Map $postgresService -Key 'image'
Assert-Check -Condition (($postgresImage -is [string]) -and $postgresImage.Trim() -eq 'postgres:17') `
    -Description 'PostgreSQL 17 service image' -Detail ("image: {0}" -f $postgresImage)

$postgresPorts = @()
if ($postgresService -is [System.Collections.IDictionary] -and $postgresService.Contains('ports')) {
    $postgresPorts = @($postgresService['ports'])
}
$hasPostgresPort = $false
foreach ($port in $postgresPorts) {
    if (([string]$port).Trim() -eq '5433:5432') { $hasPostgresPort = $true }
}
Assert-Check -Condition $hasPostgresPort -Description 'PostgreSQL service is published on port 5433' `
    -Detail ("ports: {0}" -f ($postgresPorts -join ', '))

# --- Toolchain ----------------------------------------------------------------
$goStep = Get-StepByUses -Job $testJob -Pattern '^actions/setup-go@'
Assert-Check -Condition ($null -ne $goStep) -Description 'Go toolchain is set up' `
    -Detail 'no actions/setup-go step in the test job'
if ($null -ne $goStep) {
    $goWith = Get-MapValue -Map $goStep -Key 'with'
    $goVersionFile = Get-MapValue -Map $goWith -Key 'go-version-file'
    Assert-Check -Condition (($goVersionFile -is [string]) -and $goVersionFile.Trim() -eq 'backend/go.mod') `
        -Description 'Go version follows backend/go.mod (Go 1.24)' -Detail ("go-version-file: {0}" -f $goVersionFile)
    $goCacheDependencyPath = Get-MapValue -Map $goWith -Key 'cache-dependency-path'
    Assert-Check -Condition (($goCacheDependencyPath -is [string]) -and $goCacheDependencyPath -match 'backend/go\.sum') `
        -Description 'Go module cache uses backend/go.sum' -Detail ("cache-dependency-path: {0}" -f $goCacheDependencyPath)
}

$nodeStep = Get-StepByUses -Job $testJob -Pattern '^actions/setup-node@'
$nodeIssues = @()
if ($null -eq $nodeStep) {
    $nodeIssues += 'no actions/setup-node step in the test job'
} else {
    $nodeWith = Get-MapValue -Map $nodeStep -Key 'with'
    $nodeVersion = Get-MapValue -Map $nodeWith -Key 'node-version'
    if (-not (($nodeVersion -is [string]) -and $nodeVersion.Trim() -eq '22')) {
        $nodeIssues += ("node-version is '{0}', expected '22'" -f $nodeVersion)
    }
    $nodeCache = Get-MapValue -Map $nodeWith -Key 'cache'
    if (-not (($nodeCache -is [string]) -and $nodeCache.Trim() -eq 'pnpm')) {
        $nodeIssues += ("cache is '{0}', expected 'pnpm'" -f $nodeCache)
    }
    $nodeCacheDependencyPath = Get-MapValue -Map $nodeWith -Key 'cache-dependency-path'
    if (-not (($nodeCacheDependencyPath -is [string]) -and
            $nodeCacheDependencyPath -match 'frontend/pnpm-lock\.yaml' -and
            $nodeCacheDependencyPath -match 'executor/pnpm-lock\.yaml')) {
        $nodeIssues += 'cache-dependency-path must list both lockfiles'
    }
}
Assert-Check -Condition ($nodeIssues.Count -eq 0) -Description 'Node 22 is set up with the pnpm cache' `
    -Detail ($nodeIssues -join '; ')

$pnpmStep = Get-StepByUses -Job $testJob -Pattern '^pnpm/action-setup@'
$pnpmVersion = $null
if ($null -ne $pnpmStep) {
    $pnpmWith = Get-MapValue -Map $pnpmStep -Key 'with'
    $pnpmVersion = Get-MapValue -Map $pnpmWith -Key 'version'
}
Assert-Check -Condition (($pnpmVersion -is [string]) -and $pnpmVersion.Trim() -eq '11') `
    -Description 'pnpm is installed' -Detail ("pnpm version: {0}" -f $pnpmVersion)

# --- Dependency installs ------------------------------------------------------
Test-StepGate -Job $testJob -JobId 'test' -StepName 'Install frontend dependencies' `
    -WorkingDirectory 'frontend' -Run 'pnpm install --frozen-lockfile' `
    -Description 'frontend dependencies install from the frozen lockfile'
Test-StepGate -Job $testJob -JobId 'test' -StepName 'Install executor dependencies' `
    -WorkingDirectory 'executor' -Run 'pnpm install --frozen-lockfile' `
    -Description 'executor dependencies install from the frozen lockfile'

# Executor tests launch Chromium. Install it explicitly so CI does not depend on
# whatever browser happens to be preinstalled in the runner image.
Test-StepGate -Job $testJob -JobId 'test' -StepName 'Install Playwright Chromium' `
    -WorkingDirectory 'executor' -Run 'pnpm exec playwright install --with-deps chromium' `
    -Description 'Playwright Chromium is installed explicitly for the executor tests'

# --- Go gates -----------------------------------------------------------------
$jobEnvironment = Get-MapValue -Map $testJob -Key 'env'
$testDatabaseUrl = Get-MapValue -Map $jobEnvironment -Key 'EZTEST_TEST_DATABASE_URL'
Assert-Check -Condition ($testDatabaseUrl -is [string]) `
    -Description 'integration tests receive EZTEST_TEST_DATABASE_URL' `
    -Detail 'test job env does not define EZTEST_TEST_DATABASE_URL'
Assert-Check -Condition (($testDatabaseUrl -is [string]) -and $testDatabaseUrl -match '5433/eztest_test') `
    -Description 'integration tests target the dedicated eztest_test database' `
    -Detail ("EZTEST_TEST_DATABASE_URL: {0}" -f $testDatabaseUrl)

Test-StepGate -Job $testJob -JobId 'test' -StepName 'Go unit tests' `
    -WorkingDirectory 'backend' -Run 'go test -count=1 ./...' `
    -Description 'Go unit tests run in backend/'
Test-StepGate -Job $testJob -JobId 'test' -StepName 'Go integration tests' `
    -WorkingDirectory 'backend' -Run 'go test -tags=integration -count=1 ./...' `
    -Description 'Go integration tests run in backend/'

# --- Frontend and executor gates ---------------------------------------------
Test-StepGate -Job $testJob -JobId 'test' -StepName 'Frontend lint' `
    -WorkingDirectory 'frontend' -Run 'pnpm lint' `
    -Description 'frontend lint runs in frontend/'
Test-StepGate -Job $testJob -JobId 'test' -StepName 'Frontend typecheck' `
    -WorkingDirectory 'frontend' -Run 'pnpm typecheck' `
    -Description 'frontend typecheck runs in frontend/'
Test-StepGate -Job $testJob -JobId 'test' -StepName 'Frontend unit tests' `
    -WorkingDirectory 'frontend' -Run 'pnpm test' `
    -Description 'frontend unit tests run in frontend/'
Test-StepGate -Job $testJob -JobId 'test' -StepName 'Executor unit tests' `
    -WorkingDirectory 'executor' -Run 'pnpm test' `
    -Description 'executor unit tests run in executor/'

# --- Static acceptance and the CI structural check ---------------------------
$staticJob = $null
if ($jobs.Contains('static-acceptance')) { $staticJob = $jobs['static-acceptance'] }
$staticRunsOn = Get-MapValue -Map $staticJob -Key 'runs-on'
Assert-Check -Condition (($staticRunsOn -is [string]) -and $staticRunsOn.Trim() -eq 'windows-latest') `
    -Description 'static acceptance job runs on Windows' -Detail ("runs-on: {0}" -f $staticRunsOn)

Test-StepGate -Job $staticJob -JobId 'static-acceptance' -StepName 'Static acceptance gate' `
    -Run 'pwsh -NoProfile -File scripts/acceptance.ps1 -StaticOnly' `
    -Description 'static acceptance gate runs through pwsh -File in the static job'
Test-StepGate -Job $staticJob -JobId 'static-acceptance' -StepName 'CI structural check' `
    -Run 'pwsh -NoProfile -File scripts/check-ci.ps1' `
    -Description 'CI structural check runs in the static job'

# --- Parity with the commands scripts/test.ps1 really runs -------------------
$localCommands = Get-LocalGateCommands -RepoRoot $repoRoot
$parityProblems = @()
$parityGates = @(
    @{ Label = 'Go unit tests'; Command = 'go test -count=1 ./...'; WorkingDirectory = 'backend' },
    @{ Label = 'Go integration tests'; Command = 'go test -tags=integration -count=1 ./...'; WorkingDirectory = 'backend' },
    @{ Label = 'Frontend lint'; Command = 'pnpm lint'; WorkingDirectory = 'frontend' },
    @{ Label = 'Frontend typecheck'; Command = 'pnpm typecheck'; WorkingDirectory = 'frontend' },
    @{ Label = 'Frontend unit tests'; Command = 'pnpm test'; WorkingDirectory = 'frontend' },
    @{ Label = 'Executor unit tests'; Command = 'pnpm test'; WorkingDirectory = 'executor' }
)
if ($null -eq $localCommands) {
    $parityProblems += 'scripts/test.ps1 is missing, so parity cannot be checked'
} else {
    foreach ($gate in $parityGates) {
        if (-not $localCommands.Contains($gate.Label)) {
            $parityProblems += ("scripts/test.ps1 has no '{0}' Invoke-Checked command" -f $gate.Label)
            continue
        }
        $localCommand = $localCommands[$gate.Label]
        if ($localCommand.Command -ne $gate.Command) {
            $parityProblems += ("'{0}' runs '{1}' locally but '{2}' in CI" -f $gate.Label, $localCommand.Command, $gate.Command)
        }
        if ($localCommand.WorkingDirectory -ne $gate.WorkingDirectory) {
            $parityProblems += ("'{0}' runs in '{1}' locally but in '{2}' in CI" -f $gate.Label, $localCommand.WorkingDirectory, $gate.WorkingDirectory)
        }
    }
}
Assert-Check -Condition ($parityProblems.Count -eq 0) `
    -Description 'workflow gate commands match scripts/test.ps1' `
    -Detail ($parityProblems -join '; ')

# --- Out-of-scope gates -------------------------------------------------------
Test-ForbiddenPattern -Pattern 'scripts[\\/]e2e\.ps1' -Description 'Playwright E2E gate is not run in CI'
Test-ForbiddenPattern -Pattern '\bplaywright[ \t]+test\b' -Description 'Playwright test runner is not invoked in CI'
Test-ForbiddenPattern -Pattern 'scripts[\\/]acceptance[-A-Za-z0-9_]*llm[-A-Za-z0-9_]*\.ps1' -Description 'real-LLM acceptance scripts are not run in CI'
Test-ForbiddenPattern -Pattern 'llm[_-]?key' -Description 'no real LLM key is configured in CI'

$unstaticAcceptance = @()
if ($jobs -is [System.Collections.IDictionary]) {
    foreach ($jobId in $jobs.Keys) {
        $job = $jobs[$jobId]
        foreach ($step in (Get-Steps -Job $job)) {
            $runCommand = Get-MapValue -Map $step -Key 'run'
            if (($runCommand -is [string]) -and
                ($runCommand -match 'scripts[\\/]acceptance\.ps1') -and
                ($runCommand -notmatch '-StaticOnly')) {
                $unstaticAcceptance += ("job '{0}' step '{1}'" -f $jobId, (Get-MapValue -Map $step -Key 'name'))
            }
        }
    }
}
Assert-Check -Condition ($unstaticAcceptance.Count -eq 0) `
    -Description 'full acceptance gate is not run in CI' `
    -Detail ($unstaticAcceptance -join '; ')

if ($script:failures.Count -gt 0) {
    Write-Host ("FAIL: {0} structural CI check(s) failed for {1}." -f $script:failures.Count, $relativeWorkflowPath) -ForegroundColor Red
    exit 1
}

Write-Host ("PASS: {0} keeps the baseline CI gates." -f $relativeWorkflowPath) -ForegroundColor Green
exit 0
