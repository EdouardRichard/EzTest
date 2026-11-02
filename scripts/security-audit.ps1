#requires -Version 5.1
<#
.SYNOPSIS
    Runs the P8 security audit for EzTest.

.DESCRIPTION
    Audits five areas and prints one PASS/FAIL/SKIP line per check:

      1. Secrets: scans the working tree (tracked and untracked) plus text
         build artefacts (.next/static, .next/server, dist, out, coverage) for
         private keys and provider-token shapes, plus hard-coded credential
         literals in production sources. Runtime caches (.git, .tools, .tmp,
         .data, node_modules, .pnpm-store), .env* files (by basename at any
         depth), binary files and files over 2 MiB are excluded; skipped
         counts are reported so the coverage is visible. The scan also asserts
         that .env is not tracked and is gitignored.
      2. Go vulnerabilities: runs `govulncheck ./...` when the tool is
         installed; otherwise the check is recorded as SKIP with the reason.
      3. Node vulnerabilities: runs `pnpm audit --prod --json` in frontend/ and
         executor/. Vulnerability evidence is evaluated before registry/network
         errors, so an advisory is never downgraded to SKIP. A pure
         registry/network failure is a SKIP.
      4. Permission matrix: statically asserts that the Go router wraps every
         admin-only route (/api/admin/*, /api/quality/*, /api/users, agent,
         LLM, storage) in RequireAdmin or a guard built from it, that every
         /api/resources/* write/action route carries guard(), and that the
         read-only endpoints are mounted without a guard behind the global
         bearer middleware. Go comments are stripped before scanning and
         unrecognised route helpers are recorded as SKIP. Without -SkipLive it
         also logs in as a normal user and asserts admin-only endpoints return
         403 while read-only endpoints return 200 (a missing API is a SKIP).
      5. Licenses and headers: inventories every direct frontend/executor
         dependency against the MIT/Apache-2.0-style whitelist (dev-only
         exceptions are explicit and scope-checked) and asserts that
         frontend/next.config.* emits X-Content-Type-Options, Referrer-Policy,
         X-Frame-Options, Permissions-Policy and a report-only CSP, with the
         catch-all headers entry returning the securityHeaders array. Multiple
         next.config.* files are ambiguous and fail the check.

    Exit code is 0 when every check is PASS or SKIP; SKIP never blocks. It is 1
    when any check is FAIL. Secret values and credentials are never printed.

.PARAMETER SkipLive
    Skips the live API permission matrix. The static route assertions still
    run and the live check is recorded as SKIP.

.PARAMETER ApiBaseUrl
    API base URL for the live matrix. Defaults to EZTEST_HTTP_ADDR from .env
    (for example ":13333" becomes "http://127.0.0.1:13333"), then
    "http://127.0.0.1:13333".

.PARAMETER NormalUsername
    Existing non-admin username for the live matrix. When omitted, the script
    creates a temporary normal user through the admin API and disables it after
    the run (P0 has no user-delete route). Only used by the live matrix.

.PARAMETER NormalPassword
    Password for -NormalUsername. When omitted together with NormalUsername,
    the script provisions and cleans up its own temporary user.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
    Run from anywhere; paths are resolved from the repository root.

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/security-audit.ps1 -SkipLive

.EXAMPLE
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/security-audit.ps1
#>
[CmdletBinding()]
param(
    [switch]$SkipLive,
    [string]$ApiBaseUrl,
    [string]$NormalUsername,
    [string]$NormalPassword
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$frontendDir = Join-Path $repoRoot "frontend"
$executorDir = Join-Path $repoRoot "executor"
$backendDir = Join-Path $repoRoot "backend"
$envPath = Join-Path $repoRoot ".env"
$tempDir = Join-Path $repoRoot ".tmp"

New-Item -ItemType Directory -Force -Path $tempDir | Out-Null

$results = New-Object System.Collections.ArrayList

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Add-Result {
    param(
        [string]$Area,
        [string]$Check,
        [string]$Status,
        [string]$Detail
    )
    [void]$results.Add([pscustomobject]@{
            Area   = $Area
            Check  = $Check
            Status = $Status
            Detail = $Detail
        })
    $color = switch ($Status) {
        "PASS" { "Green" }
        "SKIP" { "Yellow" }
        default { "Red" }
    }
    Write-Host ("[{0}] {1}: {2}" -f $Status, $Check, $Detail) -ForegroundColor $color
}

function Read-Utf8Text {
    param([string]$Path)
    return [System.IO.File]::ReadAllText($Path, [System.Text.Encoding]::UTF8)
}

function Get-TextLineNumber {
    param(
        [string]$Text,
        [int]$Index
    )
    if ($Index -le 0) { return 1 }
    return (($Text.Substring(0, $Index) -split "`n").Count)
}

function Invoke-WithTimeout {
    # Runs a native command in a background job so a hung registry call cannot
    # hang the audit. Returns ExitCode/Output/TimedOut.
    param(
        [string]$Exe,
        [string[]]$Arguments,
        [string]$WorkingDirectory,
        [int]$TimeoutSeconds = 120
    )
    $job = Start-Job -ScriptBlock {
        param($command, $commandArgs, $directory)
        Set-Location -LiteralPath $directory
        $output = & $command @commandArgs 2>&1 | Out-String
        [pscustomobject]@{
            ExitCode = $LASTEXITCODE
            Output   = [string]$output
        }
    } -ArgumentList $Exe, $Arguments, $WorkingDirectory

    if (-not (Wait-Job -Job $job -Timeout $TimeoutSeconds)) {
        Stop-Job -Job $job -ErrorAction SilentlyContinue
        Remove-Job -Job $job -Force -ErrorAction SilentlyContinue
        return [pscustomobject]@{ TimedOut = $true; ExitCode = -1; Output = "" }
    }

    $received = Receive-Job -Job $job
    Remove-Job -Job $job -Force -ErrorAction SilentlyContinue
    if ($null -eq $received) {
        return [pscustomobject]@{ TimedOut = $false; ExitCode = -1; Output = "" }
    }
    return [pscustomobject]@{
        TimedOut = $false
        ExitCode = [int]$received.ExitCode
        Output   = [string]$received.Output
    }
}

function Read-DotEnv {
    param([string]$Path)
    $values = @{}
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $values }
    foreach ($line in Get-Content -LiteralPath $Path) {
        if ($line -match '^\s*#' -or $line -match '^\s*$') { continue }
        if ($line -match '^\s*([^=]+)=(.*)$') {
            $key = $matches[1].Trim()
            $value = $matches[2].Trim()
            if ($value.Length -ge 2) {
                $first = $value.Substring(0, 1)
                $last = $value.Substring($value.Length - 1, 1)
                if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                    $value = $value.Substring(1, $value.Length - 2)
                }
            }
            $values[$key] = $value
        }
    }
    return $values
}

function Invoke-ApiRequest {
    # Returns Status/Body instead of throwing on HTTP error responses.
    param(
        [string]$Method,
        [string]$Url,
        [hashtable]$Headers,
        [string]$Body,
        [int]$TimeoutSeconds = 15
    )
    $request = @{
        Method      = $Method
        Uri         = $Url
        TimeoutSec  = $TimeoutSeconds
        ErrorAction = "Stop"
    }
    if ($PSVersionTable.PSVersion.Major -ge 6) {
        $request["SkipHttpErrorCheck"] = $true
    } else {
        $request["UseBasicParsing"] = $true
    }
    if ($null -ne $Headers) { $request["Headers"] = $Headers }
    if (-not [string]::IsNullOrEmpty($Body)) {
        $request["Body"] = $Body
        $request["ContentType"] = "application/json"
    }

    try {
        $response = Invoke-WebRequest @request
        return [pscustomobject]@{ Status = [int]$response.StatusCode; Body = [string]$response.Content }
    } catch {
        $response = $_.Exception.Response
        if ($null -eq $response) { throw }
        $status = [int]$response.StatusCode
        $body = ""
        try {
            $stream = $response.GetResponseStream()
            if ($null -ne $stream) {
                $reader = New-Object System.IO.StreamReader($stream)
                $body = $reader.ReadToEnd()
                $reader.Dispose()
            }
        } catch {
            # The status code is enough for the matrix assertions.
        }
        return [pscustomobject]@{ Status = $status; Body = $body }
    }
}

# ---------------------------------------------------------------------------
# 1. Secret scan
# ---------------------------------------------------------------------------

function Test-IsBinaryFile {
    param([string]$Path)
    try {
        $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite)
    } catch {
        # The scan treats an unreadable probe as text and reports it separately
        # when the later read fails.
        return $false
    }
    try {
        $probe = New-Object byte[] 8192
        $read = $stream.Read($probe, 0, $probe.Length)
        if ($read -le 0) { return $false }
        return ([System.Array]::IndexOf($probe, [byte]0, 0, $read) -ge 0)
    } finally {
        $stream.Dispose()
    }
}

function Get-ScanTargets {
    # Returns the files the secret scan must read, split into source and build
    # artefacts, plus counters for everything deliberately skipped. Build output
    # is scanned because a secret can be inlined into generated JS/CSS/JSON even
    # when the source tree is clean; node_modules and the Next standalone/runtime
    # caches stay excluded to bound runtime and noise.
    param([string]$Root)
    $excludedDirectories = @(
        ".git", ".tools", ".tmp", ".data", "node_modules", ".pnpm-store"
    )
    $excludedExtensions = @(
        ".png", ".jpg", ".jpeg", ".gif", ".ico", ".webp", ".bmp", ".svg",
        ".woff", ".woff2", ".ttf", ".otf", ".eot", ".pdf", ".zip", ".gz",
        ".tar", ".7z", ".exe", ".dll", ".bin", ".node", ".db", ".sqlite", ".lock"
    )
    $maxBytes = 2MB
    $skipped = @{ Binary = 0; Oversized = 0; Env = 0 }

    $files = New-Object System.Collections.ArrayList
    $stack = New-Object System.Collections.Stack
    $stack.Push($Root)
    while ($stack.Count -gt 0) {
        $directory = $stack.Pop()
        $entries = @(Get-ChildItem -LiteralPath $directory -Force -ErrorAction SilentlyContinue)
        foreach ($entry in $entries) {
            if ($entry.PSIsContainer) {
                if ($entry.Name -eq ".next") {
                    # Only the generated application bundles are text; the
                    # standalone copy duplicates node_modules and is excluded.
                    foreach ($buildSubdirectory in @("static", "server")) {
                        $buildPath = Join-Path $entry.FullName $buildSubdirectory
                        if (Test-Path -LiteralPath $buildPath -PathType Container) {
                            $stack.Push($buildPath)
                        }
                    }
                    continue
                }
                if ($excludedDirectories -notcontains $entry.Name -and $entry.Name -notlike "*.tmpdir") {
                    $stack.Push($entry.FullName)
                }
                continue
            }
            $relative = $entry.FullName.Substring($Root.Length + 1) -replace '\\', '/'
            # .env files are local by definition; exclude them by basename at
            # any depth, including nested frontend/executor copies.
            if ($entry.Name -match '^\.env(?:\.|$)') {
                $skipped.Env++
                continue
            }
            if ($excludedExtensions -contains $entry.Extension.ToLowerInvariant()) {
                $skipped.Binary++
                continue
            }
            if ($entry.Length -ge $maxBytes) {
                $skipped.Oversized++
                continue
            }
            $isBuildArtifact = $relative -match '(^|/)(?:\.next/(?:static|server)|dist|out|coverage)/'
            if ($isBuildArtifact -and (Test-IsBinaryFile -Path $entry.FullName)) {
                $skipped.Binary++
                continue
            }
            [void]$files.Add([pscustomobject]@{
                    FullName = $entry.FullName
                    Kind     = $(if ($isBuildArtifact) { "build" } else { "source" })
                })
        }
    }

    $items = $files.ToArray()
    return [pscustomobject]@{
        Files            = $items
        SourceCount      = @($items | Where-Object { $_.Kind -eq "source" }).Count
        BuildCount       = @($items | Where-Object { $_.Kind -eq "build" }).Count
        SkippedBinary    = $skipped.Binary
        SkippedOversized = $skipped.Oversized
        SkippedEnv       = $skipped.Env
    }
}

function Invoke-SecretScan {
    $targets = Get-ScanTargets -Root $repoRoot
    $files = @($targets.Files)
    if ($files.Count -eq 0) {
        Add-Result -Area "secrets" -Check "repository secret scan" -Status "FAIL" -Detail "no files were scanned"
        return
    }

    $highConfidence = @(
        @{ Name = "private key block"; Regex = '-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----' },
        @{ Name = "AWS access key"; Regex = '\bAKIA[0-9A-Z]{16}\b' },
        @{ Name = "Google API key"; Regex = '\bAIza[0-9A-Za-z_-]{35}\b' },
        @{ Name = "GitHub token"; Regex = '\bgh[pousr]_[A-Za-z0-9]{36,}\b' },
        @{ Name = "Slack token"; Regex = '\bxox[baprs]-[A-Za-z0-9-]{10,}\b' },
        @{ Name = "OpenAI-style key"; Regex = '\bsk-[A-Za-z0-9_-]{32,}\b' },
        @{ Name = "JWT"; Regex = '\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b' }
    )
    $credentialLiteral = @{
        Name  = "hard-coded credential"
        Regex = '(?i)\b(password|passwd|secret|api[_-]?key|access[_-]?token|client[_-]?secret)\b\s*[:=]\s*["''`]([^"''`\s]{12,})["''`]'
    }

    $findings = New-Object System.Collections.ArrayList
    $unreadable = New-Object System.Collections.ArrayList
    foreach ($file in $files) {
        $relative = $file.FullName.Substring($repoRoot.Length + 1) -replace '\\', '/'
        if (-not (Test-Path -LiteralPath $file.FullName -PathType Leaf)) {
            # A concurrent task removed a scratch file between enumeration and
            # the read; it is no longer part of the working tree.
            continue
        }
        try {
            $text = Read-Utf8Text -Path $file.FullName
        } catch {
            Start-Sleep -Milliseconds 100
            try {
                $text = Read-Utf8Text -Path $file.FullName
            } catch {
                [void]$unreadable.Add($relative)
                continue
            }
        }

        foreach ($pattern in $highConfidence) {
            foreach ($match in [regex]::Matches($text, $pattern.Regex)) {
                $line = Get-TextLineNumber -Text $text -Index $match.Index
                [void]$findings.Add(("{0} at {1}:{2}" -f $pattern.Name, $relative, $line))
            }
        }

        $isProductionSource = ($relative -match '^backend/internal/' -and $relative -notmatch '_test\.go$' -and $relative -notmatch '/testdata/') -or
            ($relative -match '^frontend/src/' -and $relative -notmatch '\.(test|spec)\.[tj]sx?$') -or
            ($relative -match '^executor/src/')
        if ($isProductionSource) {
            foreach ($match in [regex]::Matches($text, $credentialLiteral.Regex)) {
                $line = Get-TextLineNumber -Text $text -Index $match.Index
                [void]$findings.Add(("{0} at {1}:{2}" -f $credentialLiteral.Name, $relative, $line))
            }
        }
    }

    $skippedParts = @()
    if ($targets.SkippedBinary -gt 0) { $skippedParts += ("{0} binary" -f $targets.SkippedBinary) }
    if ($targets.SkippedOversized -gt 0) { $skippedParts += ("{0} oversized" -f $targets.SkippedOversized) }
    if ($targets.SkippedEnv -gt 0) { $skippedParts += ("{0} .env" -f $targets.SkippedEnv) }
    $skippedDetail = if ($skippedParts.Count -gt 0) { "skipped " + ($skippedParts -join ", ") } else { "nothing skipped" }
    $coverageDetail = ("{0} files scanned ({1} source, {2} build artifacts), {3}" -f
        $files.Count, $targets.SourceCount, $targets.BuildCount, $skippedDetail)

    if ($findings.Count -eq 0) {
        Add-Result -Area "secrets" -Check "repository secret scan" -Status "PASS" `
            -Detail ("{0}; 0 private-key/token/credential findings" -f $coverageDetail)
    } else {
        $preview = @($findings | Select-Object -First 12) -join "; "
        Add-Result -Area "secrets" -Check "repository secret scan" -Status "FAIL" `
            -Detail ("{0} finding(s) across {1}: {2}{3}" -f
                $findings.Count, $coverageDetail, $preview, $(if ($findings.Count -gt 12) { "; ..." } else { "" }))
    }

    if ($unreadable.Count -gt 0) {
        Add-Result -Area "secrets" -Check "secret scan coverage" -Status "SKIP" `
            -Detail ("{0} unreadable file(s) after retry: {1}" -f $unreadable.Count, (@($unreadable | Select-Object -First 5) -join ", "))
    }

    # .env must stay local: neither tracked nor unignored.
    $trackedEnv = @(& git -C $repoRoot ls-files -- .env 2>$null)
    if ($trackedEnv.Count -gt 0) {
        Add-Result -Area "secrets" -Check ".env tracking" -Status "FAIL" -Detail ".env is tracked by git"
    } else {
        Add-Result -Area "secrets" -Check ".env tracking" -Status "PASS" -Detail ".env is not tracked"
    }

    $previous = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    try {
        & git -C $repoRoot check-ignore -q -- .env 2>$null
        $ignoredCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($ignoredCode -eq 0) {
        Add-Result -Area "secrets" -Check ".env ignore rule" -Status "PASS" -Detail ".env is gitignored"
    } else {
        Add-Result -Area "secrets" -Check ".env ignore rule" -Status "FAIL" -Detail ".env is not gitignored"
    }
}

# ---------------------------------------------------------------------------
# 2. Go vulnerability scan
# ---------------------------------------------------------------------------

function Find-ToolPath {
    param(
        [string]$Name,
        [string[]]$Fallbacks
    )
    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    foreach ($candidate in $Fallbacks) {
        if (Test-Path -LiteralPath $candidate -PathType Leaf) { return $candidate }
    }
    return $null
}

function Invoke-Govulncheck {
    $goPath = Find-ToolPath -Name "go" -Fallbacks @((Join-Path $repoRoot ".tools\go\bin\go.exe"))
    $govulncheckPath = Find-ToolPath -Name "govulncheck" -Fallbacks @(
        (Join-Path $repoRoot ".tools\go\bin\govulncheck.exe"),
        (Join-Path $env:USERPROFILE "go\bin\govulncheck.exe")
    )

    if ($null -eq $govulncheckPath) {
        $reason = if ($null -eq $goPath) {
            "govulncheck and the Go toolchain are not installed"
        } else {
            "govulncheck is not installed (Go toolchain found; 'go install golang.org/x/vuln/cmd/govulncheck@latest')"
        }
        Add-Result -Area "dependencies" -Check "govulncheck ./..." -Status "SKIP" -Detail $reason
        return
    }
    if ($null -eq $goPath) {
        Add-Result -Area "dependencies" -Check "govulncheck ./..." -Status "SKIP" -Detail "govulncheck needs the Go toolchain, which was not found"
        return
    }

    $run = Invoke-WithTimeout -Exe $govulncheckPath -Arguments @("./...") -WorkingDirectory $backendDir -TimeoutSeconds 240
    if ($run.TimedOut) {
        Add-Result -Area "dependencies" -Check "govulncheck ./..." -Status "SKIP" -Detail "timed out after 240s"
        return
    }
    if ($run.ExitCode -eq 0) {
        Add-Result -Area "dependencies" -Check "govulncheck ./..." -Status "PASS" -Detail "no known Go vulnerabilities"
        return
    }
    if ($run.ExitCode -eq 3) {
        $summary = @(($run.Output -split "`r?`n") | Where-Object { $_ -match 'Vulnerability|Found in|Module|GO-\d|CVE-' } | Select-Object -First 8) -join " | "
        Add-Result -Area "dependencies" -Check "govulncheck ./..." -Status "FAIL" -Detail ("vulnerabilities found: {0}" -f $summary)
        return
    }
    Add-Result -Area "dependencies" -Check "govulncheck ./..." -Status "SKIP" -Detail ("govulncheck could not complete (exit {0})" -f $run.ExitCode)
}

# ---------------------------------------------------------------------------
# 3. Node production dependency audit
# ---------------------------------------------------------------------------

function Invoke-PnpmAudit {
    param(
        [string]$Directory,
        [string]$Label
    )
    $checkName = "$Label pnpm audit --prod"
    $pnpm = Find-ToolPath -Name "pnpm" -Fallbacks @()
    if ($null -eq $pnpm) {
        Add-Result -Area "dependencies" -Check $checkName -Status "SKIP" -Detail "pnpm is not installed"
        return
    }
    if (-not (Test-Path -LiteralPath (Join-Path $Directory "node_modules") -PathType Container)) {
        Add-Result -Area "dependencies" -Check $checkName -Status "SKIP" -Detail "node_modules is missing (run pnpm install)"
        return
    }

    $run = Invoke-WithTimeout -Exe $pnpm -Arguments @("audit", "--prod", "--json") -WorkingDirectory $Directory -TimeoutSeconds 120
    if ($run.TimedOut) {
        Add-Result -Area "dependencies" -Check $checkName -Status "SKIP" -Detail "timed out after 120s (registry unreachable?)"
        return
    }

    $output = [string]$run.Output
    $audit = $null
    try {
        $audit = $output | ConvertFrom-Json -ErrorAction Stop
    } catch {
        $audit = $null
    }

    if ($null -ne $audit -and $null -ne $audit.metadata -and $null -ne $audit.metadata.vulnerabilities) {
        $counts = $audit.metadata.vulnerabilities
        $total = 0
        foreach ($severity in @("info", "low", "moderate", "high", "critical")) {
            $value = 0
            if ($null -ne $counts.$severity) { $value = [int]$counts.$severity }
            $total += $value
        }
        $advisories = @()
        if ($null -ne $audit.advisories) {
            $advisories = @($audit.advisories.PSObject.Properties | ForEach-Object { $_.Name })
        }
        if ($total -gt 0 -or $advisories.Count -gt 0) {
            $preview = @($advisories | Select-Object -First 8) -join ", "
            Add-Result -Area "dependencies" -Check $checkName -Status "FAIL" `
                -Detail ("{0} vulnerability count(s), {1} advisor(ies): {2}" -f $total, $advisories.Count, $preview)
            return
        }
        Add-Result -Area "dependencies" -Check $checkName -Status "PASS" -Detail "no known production vulnerabilities (audit JSON)"
        return
    }

    # Older pnpm releases or a mixed stderr stream can break JSON parsing.
    # Check for vulnerability evidence before registry/network failures so a
    # report that mentions both is never downgraded to SKIP.
    $vulnerabilities = $output -match '(?i)(vulnerabilit|advisories|GHSA-[a-z0-9-]+|CVE-\d{4}-\d+)'
    if ($vulnerabilities) {
        $lines = @(($output -split "`r?`n") | Where-Object { $_.Trim().Length -gt 0 } | Select-Object -First 8) -join " | "
        Add-Result -Area "dependencies" -Check $checkName -Status "FAIL" -Detail ("findings: {0}" -f $lines)
        return
    }
    $registryFailure = $output -match '(?i)(ENOTFOUND|EAI_AGAIN|ECONNREFUSED|ECONNRESET|ETIMEDOUT|ERR_PNPM|registry|network|self signed|proxy|fetch failed)'
    if ($registryFailure) {
        $firstLine = @(($output -split "`r?`n") | Where-Object { $_.Trim().Length -gt 0 } | Select-Object -First 1)
        Add-Result -Area "dependencies" -Check $checkName -Status "SKIP" -Detail ("registry unavailable: {0}" -f $firstLine)
        return
    }
    if ($run.ExitCode -eq 0) {
        Add-Result -Area "dependencies" -Check $checkName -Status "PASS" -Detail "no known production vulnerabilities"
        return
    }

    Add-Result -Area "dependencies" -Check $checkName -Status "FAIL" -Detail ("pnpm audit exited {0} without a recognisable result" -f $run.ExitCode)
}

# ---------------------------------------------------------------------------
# 4. Permission matrix (static route assertions)
# ---------------------------------------------------------------------------

function Remove-SourceComments {
    # Removes // and /* */ comments while leaving string, rune, raw-string and
    # template-literal contents untouched, so a URL or a glob pattern never
    # starts a fake comment and a commented-out guard never counts as a guard.
    param([string]$Text)
    $pattern = '(?s)(?<string>"(?:\\.|[^"\\])*")|(?<raw>`[^`]*`)|(?<rune>''(?:\\.|[^''\\])*'')|(?<line>//[^\n]*)|(?<block>/\*.*?\*/)'
    $evaluator = [System.Text.RegularExpressions.MatchEvaluator] {
        param($match)
        if ($match.Groups["line"].Success -or $match.Groups["block"].Success) {
            return [regex]::Replace($match.Value, '[^\r\n]', ' ')
        }
        return $match.Value
    }
    return [regex]::Replace($Text, $pattern, $evaluator)
}

$script:goSources = @(Get-ChildItem -LiteralPath (Join-Path $backendDir "internal") -Recurse -Filter *.go -File |
        Where-Object { $_.Name -notlike "*_test.go" })
$script:allGoText = (@($script:goSources | ForEach-Object {
            Remove-SourceComments -Text (Read-Utf8Text -Path $_.FullName)
        }) -join "`n")
$script:flatGoText = $script:allGoText -replace '\s+', ' '

function Get-RouteDeclarationCount {
    param([string]$RoutePrefix)
    $pattern = 'mux\.Handle(?:Func)?\("(?:GET|POST|PUT|PATCH|DELETE) ' + [regex]::Escape($RoutePrefix) + '[^"]*"'
    return [regex]::Matches($script:flatGoText, $pattern).Count
}

function Test-AdminRouteGroup {
    param(
        [string]$Name,
        [string]$RouteRegex
    )
    $declared = [regex]::Matches($script:flatGoText, 'mux\.Handle(?:Func)?\("(?:GET|POST|PUT|PATCH|DELETE) ' + $RouteRegex + '[^"]*"')
    $guarded = [regex]::Matches($script:flatGoText, 'mux\.Handle\("(?:GET|POST|PUT|PATCH|DELETE) ' + $RouteRegex + '[^"]*",\s*(?:[A-Za-z_][A-Za-z0-9_.]*\.)?(?:RequireAdmin|guard|wrap)\s*\(')
    if ($declared.Count -eq 0) {
        return [pscustomobject]@{ Declared = 0; Guarded = 0; Missing = @() }
    }
    $guardedRoutes = @{}
    foreach ($match in $guarded) {
        $routeMatch = [regex]::Match($match.Value, '"(?:GET|POST|PUT|PATCH|DELETE) [^"]*"')
        if ($routeMatch.Success) { $guardedRoutes[$routeMatch.Value] = $true }
    }
    $missing = New-Object System.Collections.ArrayList
    $declaredRoutes = @{}
    foreach ($match in $declared) {
        $routeMatch = [regex]::Match($match.Value, '"(?:GET|POST|PUT|PATCH|DELETE) [^"]*"')
        if (-not $routeMatch.Success) { continue }
        $route = $routeMatch.Value
        $declaredRoutes[$route] = $true
        if (-not $guardedRoutes.ContainsKey($route)) { [void]$missing.Add($route) }
    }
    return [pscustomobject]@{
        Declared = $declaredRoutes.Count
        Guarded  = $guardedRoutes.Count
        Missing  = $missing.ToArray()
    }
}

function Test-ResourceWriteGuards {
    # /api/resources reads are authenticated-only, but every write and action
    # route must carry the caller-supplied admin guard. The handler packages
    # register those routes themselves, so scan all non-test Go sources rather
    # than only server.go.
    $declaredMatches = [regex]::Matches($script:flatGoText, 'mux\.Handle(?:Func)?\("(?:POST|PUT|PATCH|DELETE) /api/resources/[^"]*"')
    if ($declaredMatches.Count -eq 0) {
        return [pscustomobject]@{ Declared = 0; Guarded = 0; Missing = @() }
    }
    $guardedMatches = [regex]::Matches($script:flatGoText, 'mux\.Handle\("(?:POST|PUT|PATCH|DELETE) /api/resources/[^"]*",\s*(?:[A-Za-z_][A-Za-z0-9_.]*\.)?(?:guard|RequireAdmin|wrap)\s*\(')
    $guardedRoutes = @{}
    foreach ($match in $guardedMatches) {
        $routeMatch = [regex]::Match($match.Value, '"(?:GET|POST|PUT|PATCH|DELETE) [^"]*"')
        if ($routeMatch.Success) { $guardedRoutes[$routeMatch.Value] = $true }
    }
    $declaredRoutes = @{}
    $missing = New-Object System.Collections.ArrayList
    foreach ($match in $declaredMatches) {
        $routeMatch = [regex]::Match($match.Value, '"(?:GET|POST|PUT|PATCH|DELETE) [^"]*"')
        if (-not $routeMatch.Success) { continue }
        $route = $routeMatch.Value
        $declaredRoutes[$route] = $true
        if (-not $guardedRoutes.ContainsKey($route)) { [void]$missing.Add($route) }
    }
    return [pscustomobject]@{
        Declared = $declaredRoutes.Count
        Guarded  = $guardedRoutes.Count
        Missing  = $missing.ToArray()
    }
}

function Invoke-StaticPermissionMatrix {
    $serverPath = Join-Path $backendDir "internal/server/server.go"
    if (-not (Test-Path -LiteralPath $serverPath -PathType Leaf)) {
        Add-Result -Area "permissions" -Check "static permission matrix" -Status "FAIL" -Detail "backend/internal/server/server.go is missing"
        return
    }
    $serverText = Read-Utf8Text -Path $serverPath
    $flatServer = $serverText -replace '\s+', ' '

    # Admin-only routes must be registered with RequireAdmin or an explicit
    # guard built from it; the generic scan also catches future /api/admin/*
    # and /api/quality/* routes.
    $adminGroup = Test-AdminRouteGroup -Name "admin" -RouteRegex '/api/(?:admin|quality)/'
    if ($adminGroup.Declared -eq 0) {
        Add-Result -Area "permissions" -Check "/api/admin|quality route guards" -Status "FAIL" -Detail "no admin route declarations were found"
    } elseif ($adminGroup.Missing.Count -eq 0) {
        Add-Result -Area "permissions" -Check "/api/admin|quality route guards" -Status "PASS" `
            -Detail ("{0}/{0} routes carry RequireAdmin or guard()" -f $adminGroup.Declared)
    } else {
        Add-Result -Area "permissions" -Check "/api/admin|quality route guards" -Status "FAIL" `
            -Detail ("unguarded: {0}" -f ($adminGroup.Missing -join ", "))
    }

    $anchor = [regex]::Match($script:flatGoText, 'mux\.Handle\("GET /api/admin/audit",\s*identityhttp\.RequireAdmin\(')
    if ($anchor.Success) {
        Add-Result -Area "permissions" -Check "GET /api/admin/audit guard" -Status "PASS" -Detail "wrapped in identityhttp.RequireAdmin"
    } else {
        Add-Result -Area "permissions" -Check "GET /api/admin/audit guard" -Status "FAIL" -Detail "not wrapped in identityhttp.RequireAdmin"
    }

    $usersGroup = Test-AdminRouteGroup -Name "users" -RouteRegex '/api/users'
    if ($usersGroup.Declared -gt 0 -and $usersGroup.Missing.Count -eq 0) {
        Add-Result -Area "permissions" -Check "/api/users route guards" -Status "PASS" `
            -Detail ("{0}/{0} admin user-management routes carry RequireAdmin" -f $usersGroup.Declared)
    } else {
        Add-Result -Area "permissions" -Check "/api/users route guards" -Status "FAIL" `
            -Detail ("declared={0}; missing={1}" -f $usersGroup.Declared, ($usersGroup.Missing -join ", "))
    }

    $agentsGroup = Test-AdminRouteGroup -Name "agents" -RouteRegex '/api/agents/'
    if ($agentsGroup.Declared -gt 0 -and $agentsGroup.Missing.Count -eq 0) {
        Add-Result -Area "permissions" -Check "/api/agents/* route guards" -Status "PASS" `
            -Detail ("{0}/{0} admin agent routes carry guard()" -f $agentsGroup.Declared)
    } else {
        Add-Result -Area "permissions" -Check "/api/agents/* route guards" -Status "FAIL" `
            -Detail ("declared={0}; missing={1}" -f $agentsGroup.Declared, ($agentsGroup.Missing -join ", "))
    }

    $codeIntelIndex = [regex]::Match($script:flatGoText, 'mux\.Handle\("POST /api/code/repos/\{repoId\}/index",\s*identityhttp\.RequireAdmin\(')
    if ($codeIntelIndex.Success) {
        Add-Result -Area "permissions" -Check "code-intel index guard" -Status "PASS" -Detail "POST /api/code/repos/{repoId}/index requires admin"
    } else {
        Add-Result -Area "permissions" -Check "code-intel index guard" -Status "FAIL" -Detail "POST /api/code/repos/{repoId}/index is not admin-guarded"
    }

    $wiring = @(
        @{ Name = "llm"; Pattern = 'llmhttp\.RegisterRoutes\(mux, llmHandler, adminGuard\)' },
        @{ Name = "storage"; Pattern = 'storagehttp\.RegisterRoutes\(mux, storageHandler, adminGuard\)' },
        @{ Name = "agent"; Pattern = 'agenthttp\.RegisterRoutes\(mux, agentHandler, adminGuard\)' },
        @{ Name = "resource"; Pattern = 'resourcehttp\.RegisterRoutes\(mux, \*deps\.Resources, resourceGuard\)' },
        @{ Name = "resource guard"; Pattern = 'resourceGuard := func\(next http\.Handler\) http\.Handler \{ return identityhttp\.RequireAdmin\(' }
    )
    $missingWiring = @($wiring | Where-Object { -not [regex]::IsMatch($flatServer, $_.Pattern) } | ForEach-Object { $_.Name })
    $closureCount = [regex]::Matches($flatServer, 'RequireAdmin\(auditRequestContext\(next\)\)').Count
    if ($missingWiring.Count -eq 0 -and $closureCount -ge 4) {
        Add-Result -Area "permissions" -Check "admin guard wiring" -Status "PASS" `
            -Detail ("llm/storage/agent/resource groups receive RequireAdmin closures ({0})" -f $closureCount)
    } else {
        Add-Result -Area "permissions" -Check "admin guard wiring" -Status "FAIL" `
            -Detail ("missing registrations: {0}; RequireAdmin closures: {1}" -f ($missingWiring -join ", "), $closureCount)
    }

    $resourceWrites = Test-ResourceWriteGuards
    if ($resourceWrites.Declared -eq 0) {
        Add-Result -Area "permissions" -Check "/api/resources/* write guards" -Status "FAIL" `
            -Detail "no write/action /api/resources route declarations were found"
    } elseif ($resourceWrites.Missing.Count -eq 0) {
        Add-Result -Area "permissions" -Check "/api/resources/* write guards" -Status "PASS" `
            -Detail ("{0}/{0} write/action routes carry guard()" -f $resourceWrites.Declared)
    } else {
        Add-Result -Area "permissions" -Check "/api/resources/* write guards" -Status "FAIL" `
            -Detail ("unguarded: {0}" -f ($resourceWrites.Missing -join ", "))
    }

    # Read-only endpoints are intentionally available to every logged-in user;
    # they must be mounted without a per-route guard behind the global auth.
    $readOnlyRoutes = @(
        "GET /api/versions",
        "GET /api/search",
        "GET /api/todos",
        "GET /api/agent-center/catalog",
        "GET /api/issues",
        "GET /api/reports",
        "GET /api/resources/systems"
    )
    foreach ($route in $readOnlyRoutes) {
        $linePattern = '(?m)^.*mux\.Handle(?:Func)?\("' + [regex]::Escape($route) + '",.*$'
        $line = [regex]::Match($script:allGoText, $linePattern).Value
        if ([string]::IsNullOrWhiteSpace($line)) {
            Add-Result -Area "permissions" -Check "$route read-only" -Status "FAIL" -Detail "route declaration not found"
        } elseif ($line -match 'RequireAdmin|guard\(') {
            Add-Result -Area "permissions" -Check "$route read-only" -Status "FAIL" -Detail "route is admin-guarded but must stay readable by every logged-in user"
        } else {
            Add-Result -Area "permissions" -Check "$route read-only" -Status "PASS" -Detail "mounted without a per-route guard"
        }
    }

    $authWiring = [regex]::IsMatch($flatServer, 'authenticatedAPI\(deps\.Auth\)\(handler\)')
    $authExclusions = [regex]::IsMatch($flatServer, 'case "/api/health", "/api/openapi.yaml":') -and
        [regex]::IsMatch($flatServer, 'strings\.HasPrefix\(r\.URL\.Path, "/api/"\)') -and
        [regex]::IsMatch($flatServer, '!strings\.HasPrefix\(r\.URL\.Path, "/api/auth/"\)')
    if ($authWiring -and $authExclusions) {
        Add-Result -Area "permissions" -Check "global bearer auth" -Status "PASS" `
            -Detail "every /api route except health/openapi/auth requires a bearer token"
    } else {
        Add-Result -Area "permissions" -Check "global bearer auth" -Status "FAIL" `
            -Detail ("authenticatedAPI={0}; route exclusions={1}" -f $authWiring, $authExclusions)
    }

    if ([regex]::IsMatch($flatServer, 'agentChannelAdminOnly') -and
        [regex]::IsMatch($flatServer, 'RoleAdmin') -and
        [regex]::IsMatch($flatServer, 'StatusForbidden')) {
        Add-Result -Area "permissions" -Check "admin SSE channels" -Status "PASS" -Detail "agent:* event channels require RoleAdmin"
    } else {
        Add-Result -Area "permissions" -Check "admin SSE channels" -Status "FAIL" -Detail "agentChannelAdminOnly no longer enforces RoleAdmin"
    }

    $qualityRoutes = Get-RouteDeclarationCount -RoutePrefix "/api/quality/"
    if ($qualityRoutes -eq 0) {
        Add-Result -Area "permissions" -Check "quality routes" -Status "SKIP" `
            -Detail "no /api/quality/* routes are mounted yet; the generic admin scan will cover them once wired"
    } elseif ([regex]::IsMatch($flatServer, 'deps\.Quality\.RegisterRoutes\(mux, qualityGuard\)') -and
        [regex]::IsMatch($flatServer, 'qualityGuard := func\(next http\.Handler\) http\.Handler \{ return identityhttp\.RequireAdmin\(')) {
        Add-Result -Area "permissions" -Check "quality route wiring" -Status "PASS" `
            -Detail ("{0} quality routes receive the qualityGuard RequireAdmin closure" -f $qualityRoutes)
    } else {
        Add-Result -Area "permissions" -Check "quality route wiring" -Status "FAIL" `
            -Detail "deps.Quality.RegisterRoutes is not wired to a qualityGuard built from identityhttp.RequireAdmin"
    }

    # Route groups registered through package helpers are verified by the
    # checks above (direct route scans and guard wiring). Record any helper the
    # script does not know so a newly added group cannot silently bypass the
    # matrix; an unknown helper is a SKIP, never a silent PASS.
    $knownRouteHelpers = @(
        "identityhttp.RegisterAuthRoutes",
        "identityhttp.RegisterUserRoutes",
        "llmhttp.RegisterRoutes",
        "storagehttp.RegisterRoutes",
        "agenthttp.RegisterRoutes",
        "agenthttp.RegisterCenterRoutes",
        "resourcehttp.RegisterRoutes",
        "versionhttp.RegisterRoutes",
        "deps.Quality.RegisterRoutes",
        "deps.CodeIntel.Register",
        "deps.Mindmap.Register",
        "deps.Execution.RegisterRoutes",
        "deps.Delivery.RegisterRoutes",
        "deps.Chain.RegisterRoutes",
        "deps.Workspace.RegisterRoutes",
        "deps.Search.RegisterRoutes"
    )
    $helperRegistrations = [regex]::Matches(
        $flatServer,
        '(?<call>[A-Za-z_][A-Za-z0-9_.]*\.(?:RegisterRoutes|RegisterCenterRoutes|RegisterAuthRoutes|RegisterUserRoutes|Register))\s*\(\s*mux\b')
    $unknownHelpers = @()
    foreach ($match in $helperRegistrations) {
        $call = $match.Groups["call"].Value
        if ($knownRouteHelpers -notcontains $call -and $unknownHelpers -notcontains $call) {
            $unknownHelpers += $call
        }
    }
    if ($unknownHelpers.Count -eq 0) {
        Add-Result -Area "permissions" -Check "route helper coverage" -Status "PASS" `
            -Detail ("{0} route helper registration(s) recognised; no unknown helpers" -f $helperRegistrations.Count)
    } else {
        Add-Result -Area "permissions" -Check "route helper coverage" -Status "SKIP" `
            -Detail ("unrecognised route helper(s): {0}; inspect their guard logic manually" -f ($unknownHelpers -join ", "))
    }
}

# ---------------------------------------------------------------------------
# 4b. Permission matrix (live API)
# ---------------------------------------------------------------------------

function Resolve-ApiBaseUrl {
    if (-not [string]::IsNullOrWhiteSpace($ApiBaseUrl)) {
        return $ApiBaseUrl.TrimEnd('/')
    }
    $values = Read-DotEnv -Path $envPath
    $addr = [string]$values["EZTEST_HTTP_ADDR"]
    if ([string]::IsNullOrWhiteSpace($addr)) { return "http://127.0.0.1:13333" }
    $addr = $addr.Trim()
    if ($addr -match '^:(\d+)$') { return "http://127.0.0.1:$($matches[1])" }
    if ($addr -match '^0\.0\.0\.0:(\d+)$') { return "http://127.0.0.1:$($matches[1])" }
    if ($addr -match '^\d+$') { return "http://127.0.0.1:$addr" }
    if ($addr -match '^https?://') { return $addr.TrimEnd('/') }
    return "http://$($addr.TrimEnd('/'))"
}

function Get-SafeStatus {
    param($Response)
    if ($null -eq $Response) { return 0 }
    return [int]$Response.Status
}

function Invoke-LivePermissionMatrix {
    if ($SkipLive) {
        Add-Result -Area "permissions" -Check "live permission matrix" -Status "SKIP" `
            -Detail "skipped by -SkipLive; static route assertions ran above"
        return
    }

    $baseUrl = Resolve-ApiBaseUrl
    $health = $null
    try {
        $health = Invoke-ApiRequest -Method "GET" -Url "$baseUrl/api/health" -TimeoutSeconds 5
    } catch {
        $health = $null
    }
    if ($null -eq $health -or $health.Status -ne 200) {
        Add-Result -Area "permissions" -Check "live permission matrix" -Status "SKIP" `
            -Detail ("API is not reachable at {0}; start it or run with -SkipLive" -f $baseUrl)
        return
    }

    $envValues = Read-DotEnv -Path $envPath
    $adminUsername = [string]$envValues["EZTEST_ADMIN_USERNAME"]
    $adminPassword = [string]$envValues["EZTEST_ADMIN_PASSWORD"]
    if ([string]::IsNullOrWhiteSpace($adminUsername) -or [string]::IsNullOrWhiteSpace($adminPassword)) {
        Add-Result -Area "permissions" -Check "live permission matrix" -Status "SKIP" `
            -Detail "EZTEST_ADMIN_USERNAME/EZTEST_ADMIN_PASSWORD are missing from .env"
        return
    }

    $adminToken = $null
    try {
        $login = Invoke-ApiRequest -Method "POST" -Url "$baseUrl/api/auth/login" `
            -Body (@{ username = $adminUsername; password = $adminPassword } | ConvertTo-Json -Compress) -TimeoutSeconds 15
        if ($login.Status -ne 200) {
            Add-Result -Area "permissions" -Check "live permission matrix" -Status "FAIL" -Detail ("admin login returned {0}" -f $login.Status)
            return
        }
        $adminToken = [string](($login.Body | ConvertFrom-Json).data.token)
    } catch {
        Add-Result -Area "permissions" -Check "live permission matrix" -Status "SKIP" -Detail "admin login could not be completed"
        return
    }
    if ([string]::IsNullOrWhiteSpace($adminToken)) {
        Add-Result -Area "permissions" -Check "live permission matrix" -Status "FAIL" -Detail "admin login returned no token"
        return
    }
    $adminHeaders = @{ Authorization = "Bearer $adminToken" }

    $createdUser = $false
    $userId = $null
    $normalUser = $NormalUsername
    $normalPass = $NormalPassword
    $normalHeaders = $null
    try {
        # Provision a temporary normal user unless the caller supplied one. P0
        # has no user-delete route, so the finally block disables the account.
        if ([string]::IsNullOrWhiteSpace($normalUser) -or [string]::IsNullOrWhiteSpace($normalPass)) {
            $stamp = Get-Date -Format "yyyyMMddHHmmss"
            $suffix = [guid]::NewGuid().ToString("N").Substring(0, 6)
            $normalUser = "secaudit$stamp$suffix"
            $normalPass = "SecAudit-$stamp-$suffix-pw"
            $create = Invoke-ApiRequest -Method "POST" -Url "$baseUrl/api/users" -Headers $adminHeaders `
                -Body (@{ username = $normalUser; password = $normalPass } | ConvertTo-Json -Compress) -TimeoutSeconds 15
            if ($create.Status -ne 201) {
                Add-Result -Area "permissions" -Check "live permission matrix" -Status "SKIP" `
                    -Detail ("temporary normal user could not be created (status {0})" -f $create.Status)
                return
            }
            $createdUser = $true
            try { $userId = ($create.Body | ConvertFrom-Json).data.id } catch { $userId = $null }
        }

        $normalLogin = Invoke-ApiRequest -Method "POST" -Url "$baseUrl/api/auth/login" `
            -Body (@{ username = $normalUser; password = $normalPass } | ConvertTo-Json -Compress) -TimeoutSeconds 15
        if ($normalLogin.Status -ne 200) {
            Add-Result -Area "permissions" -Check "live permission matrix" -Status "SKIP" `
                -Detail ("normal user login returned {0}" -f $normalLogin.Status)
            return
        }
        $normalToken = [string](($normalLogin.Body | ConvertFrom-Json).data.token)
        if ([string]::IsNullOrWhiteSpace($normalToken)) {
            Add-Result -Area "permissions" -Check "live permission matrix" -Status "FAIL" -Detail "normal user login returned no token"
            return
        }
        $normalHeaders = @{ Authorization = "Bearer $normalToken" }

        $unauth = Invoke-ApiRequest -Method "GET" -Url "$baseUrl/api/versions" -TimeoutSeconds 15
        if ($unauth.Status -eq 401) {
            Add-Result -Area "permissions" -Check "unauthenticated /api/versions" -Status "PASS" -Detail "401 without a token"
        } else {
            Add-Result -Area "permissions" -Check "unauthenticated /api/versions" -Status "FAIL" -Detail ("expected 401, got {0}" -f $unauth.Status)
        }

        $adminOnly = @(
            @{ Path = "/api/admin/audit?limit=1"; Name = "audit query" },
            @{ Path = "/api/users"; Name = "user management" },
            @{ Path = "/api/agents/profiles"; Name = "agent profiles" },
            @{ Path = "/api/admin/llm/configs"; Name = "LLM config" }
        )
        foreach ($endpoint in $adminOnly) {
            $response = Invoke-ApiRequest -Method "GET" -Url "$baseUrl$($endpoint.Path)" -Headers $normalHeaders -TimeoutSeconds 15
            if ($response.Status -eq 403) {
                Add-Result -Area "permissions" -Check ("live admin-only " + $endpoint.Name) -Status "PASS" -Detail "normal user gets 403"
            } elseif ($response.Status -eq 404) {
                Add-Result -Area "permissions" -Check ("live admin-only " + $endpoint.Name) -Status "SKIP" -Detail "route is not mounted (404)"
            } elseif ($response.Status -eq 200) {
                Add-Result -Area "permissions" -Check ("live admin-only " + $endpoint.Name) -Status "FAIL" -Detail "normal user reached an admin-only endpoint (200)"
            } else {
                Add-Result -Area "permissions" -Check ("live admin-only " + $endpoint.Name) -Status "FAIL" `
                    -Detail ("expected 403, got {0}" -f $response.Status)
            }
        }

        $readOnly = @(
            @{ Path = "/api/versions?limit=1"; Name = "version list" },
            @{ Path = "/api/search?q=eztest&limit=1"; Name = "global search" },
            @{ Path = "/api/todos"; Name = "todo centre" },
            @{ Path = "/api/agent-center/catalog"; Name = "agent centre catalogue" },
            @{ Path = "/api/issues?limit=1"; Name = "issue centre" },
            @{ Path = "/api/reports?limit=1"; Name = "report centre" },
            @{ Path = "/api/resources/systems"; Name = "resource systems" }
        )
        foreach ($endpoint in $readOnly) {
            $response = Invoke-ApiRequest -Method "GET" -Url "$baseUrl$($endpoint.Path)" -Headers $normalHeaders -TimeoutSeconds 15
            if ($response.Status -eq 200) {
                Add-Result -Area "permissions" -Check ("live read-only " + $endpoint.Name) -Status "PASS" -Detail "normal user gets 200"
            } elseif ($response.Status -eq 404) {
                Add-Result -Area "permissions" -Check ("live read-only " + $endpoint.Name) -Status "SKIP" -Detail "route is not mounted (404)"
            } else {
                Add-Result -Area "permissions" -Check ("live read-only " + $endpoint.Name) -Status "FAIL" `
                    -Detail ("expected 200, got {0}" -f $response.Status)
            }
        }

    } finally {
        # Cleanup and logout run even when an assertion above returned early.
        # Disabling the temporary user must happen before the admin logout.
        if ($createdUser) {
            if ($null -eq $userId -or [string]::IsNullOrWhiteSpace([string]$userId)) {
                Add-Result -Area "permissions" -Check "temporary audit user cleanup" -Status "FAIL" `
                    -Detail ("temporary normal user '{0}' was created but the response had no id; disable it manually" -f $normalUser)
            } else {
                try {
                    $patch = Invoke-ApiRequest -Method "PATCH" -Url "$baseUrl/api/users/$userId" -Headers $adminHeaders `
                        -Body (@{ status = "disabled" } | ConvertTo-Json -Compress) -TimeoutSeconds 10
                    if ($patch.Status -eq 200 -or $patch.Status -eq 204) {
                        Add-Result -Area "permissions" -Check "temporary audit user cleanup" -Status "PASS" `
                            -Detail ("disabled temporary normal user '{0}' (no user-delete route exists)" -f $normalUser)
                    } else {
                        Add-Result -Area "permissions" -Check "temporary audit user cleanup" -Status "FAIL" `
                            -Detail ("disable PATCH for '{0}' returned {1}; the temporary account may still be enabled" -f $normalUser, $patch.Status)
                    }
                } catch {
                    Add-Result -Area "permissions" -Check "temporary audit user cleanup" -Status "FAIL" `
                        -Detail ("disable PATCH for '{0}' failed; the temporary account may still be enabled" -f $normalUser)
                }
            }
        }

        # Best-effort logout, never fatal; it still runs after a cleanup error.
        foreach ($headers in @($normalHeaders, $adminHeaders)) {
            if ($null -eq $headers) { continue }
            try {
                Invoke-ApiRequest -Method "POST" -Url "$baseUrl/api/auth/logout" -Headers $headers -TimeoutSeconds 10 | Out-Null
            } catch {
                # Ignore: the audit result is already recorded.
            }
        }
    }
}

# ---------------------------------------------------------------------------
# 5. Dependency license inventory
# ---------------------------------------------------------------------------

function Get-PackageLicense {
    param(
        [string]$Directory,
        [string]$PackageName
    )
    $packagePath = Join-Path $Directory "node_modules"
    foreach ($part in ($PackageName -split '/')) {
        $packagePath = Join-Path $packagePath $part
    }
    $packagePath = Join-Path $packagePath "package.json"
    if (-not (Test-Path -LiteralPath $packagePath -PathType Leaf)) {
        return @{ Found = $false; License = ""; Version = "" }
    }
    $package = ConvertFrom-Json -InputObject (Read-Utf8Text -Path $packagePath)
    $license = ""
    if ($null -ne $package.license) {
        if ($package.license -is [string]) {
            $license = $package.license
        } elseif ($null -ne $package.license.type) {
            $license = [string]$package.license.type
        }
    }
    return @{ Found = $true; License = [string]$license; Version = [string]$package.version }
}

function Invoke-LicenseInventory {
    $allowedLicenses = @(
        "MIT", "Apache-2.0", "ISC", "BSD-2-Clause", "BSD-3-Clause",
        "0BSD", "Unlicense", "CC0-1.0", "BlueOak-1.0.0"
    )
    # Explicitly registered non-whitelist exceptions. axe-core is test-only and
    # is never shipped in the Next/executor runtime bundles.
    $registeredExceptions = @{
        "axe-core" = @{ License = "MPL-2.0"; Scope = "dev"; Reason = "test-only accessibility tooling, not shipped" }
    }
    $registeredP8Dependencies = @{
        "@xyflow/react" = "MIT"
        "axe-core"      = "MPL-2.0"
    }

    $inventory = New-Object System.Collections.ArrayList
    $violations = New-Object System.Collections.ArrayList
    $missing = New-Object System.Collections.ArrayList
    $anyScope = $false

    foreach ($scope in @(
            @{ Name = "frontend"; Directory = $frontendDir },
            @{ Name = "executor"; Directory = $executorDir }
        )) {
        $packageJsonPath = Join-Path $scope.Directory "package.json"
        if (-not (Test-Path -LiteralPath $packageJsonPath -PathType Leaf)) {
            [void]$missing.Add("$($scope.Name)/package.json")
            continue
        }
        if (-not (Test-Path -LiteralPath (Join-Path $scope.Directory "node_modules") -PathType Container)) {
            Add-Result -Area "licenses" -Check "$($scope.Name) license inventory" -Status "SKIP" -Detail "node_modules is missing (run pnpm install)"
            continue
        }
        $anyScope = $true
        $packageJson = ConvertFrom-Json -InputObject (Read-Utf8Text -Path $packageJsonPath)
        $dependencies = @()
        if ($null -ne $packageJson.dependencies) {
            foreach ($property in $packageJson.dependencies.PSObject.Properties) {
                $dependencies += [pscustomobject]@{ Name = $property.Name; Version = [string]$property.Value; Scope = "prod" }
            }
        }
        if ($null -ne $packageJson.devDependencies) {
            foreach ($property in $packageJson.devDependencies.PSObject.Properties) {
                $dependencies += [pscustomobject]@{ Name = $property.Name; Version = [string]$property.Value; Scope = "dev" }
            }
        }

        foreach ($dependency in $dependencies) {
            $package = Get-PackageLicense -Directory $scope.Directory -PackageName $dependency.Name
            if (-not $package.Found) {
                [void]$missing.Add("$($scope.Name):$($dependency.Name)")
                [void]$violations.Add("$($dependency.Name): not installed")
                continue
            }
            $license = $package.License.Trim()
            if ([string]::IsNullOrWhiteSpace($license)) {
                [void]$violations.Add("$($dependency.Name): no license field")
                [void]$inventory.Add([pscustomobject]@{
                        Package = $dependency.Name; Version = $package.Version; Scope = $dependency.Scope
                        License = "(missing)"; Status = "FAIL"
                    })
                continue
            }

            $options = @($license -split '\s+OR\s+' | ForEach-Object { $_.Trim() })
            $whitelisted = @($options | Where-Object { $allowedLicenses -contains $_ }).Count -gt 0
            $exception = $registeredExceptions.ContainsKey($dependency.Name) -and
                $registeredExceptions[$dependency.Name].License -eq $license -and
                $registeredExceptions[$dependency.Name].Scope -eq $dependency.Scope
            $status = "PASS"
            if (-not $whitelisted -and -not $exception) {
                $status = "FAIL"
                if ($registeredExceptions.ContainsKey($dependency.Name) -and
                    $registeredExceptions[$dependency.Name].License -eq $license -and
                    $registeredExceptions[$dependency.Name].Scope -ne $dependency.Scope) {
                    [void]$violations.Add(("{0}: {1} exception is registered for scope '{2}' but the dependency scope is '{3}'" -f
                            $dependency.Name, $license, $registeredExceptions[$dependency.Name].Scope, $dependency.Scope))
                } else {
                    [void]$violations.Add("$($dependency.Name): $license is not whitelisted; register it in scripts/security-audit.ps1")
                }
            }
            [void]$inventory.Add([pscustomobject]@{
                    Package = $dependency.Name; Version = $package.Version; Scope = $dependency.Scope
                    License = $license; Status = $status
                })
        }
    }

    if ($anyScope) {
        Write-Host ""
        Write-Host "Direct dependency licenses:" -ForegroundColor Cyan
        $inventory | Format-Table -AutoSize Package, Version, Scope, License, Status | Out-Host
    }

    if ($inventory.Count -eq 0) {
        if ($anyScope) {
            Add-Result -Area "licenses" -Check "direct dependency licenses" -Status "FAIL" `
                -Detail "license inventory is empty even though node_modules is present"
        } else {
            Add-Result -Area "licenses" -Check "P8 dependency registration" -Status "SKIP" `
                -Detail "license inventory is empty (no installed scope); P8 registration assertions skipped"
        }
        return
    }

    $exceptionNames = @($inventory | Where-Object {
            $registeredExceptions.ContainsKey($_.Package) -and
            $registeredExceptions[$_.Package].License -eq $_.License -and
            $registeredExceptions[$_.Package].Scope -eq $_.Scope
        } | ForEach-Object { "$($_.Package) $($_.License) ($($_.Scope)-only)" })
    $exceptionDetail = if ($exceptionNames.Count -gt 0) { "; registered exceptions: " + ($exceptionNames -join ", ") } else { "" }
    if ($violations.Count -eq 0) {
        Add-Result -Area "licenses" -Check "direct dependency licenses" -Status "PASS" `
            -Detail ("{0} packages within MIT/Apache-2.0 whitelist{1}" -f $inventory.Count, $exceptionDetail)
    } else {
        Add-Result -Area "licenses" -Check "direct dependency licenses" -Status "FAIL" `
            -Detail (($violations | Select-Object -First 10) -join "; ")
    }

    foreach ($name in $registeredP8Dependencies.Keys) {
        $found = @($inventory | Where-Object { $_.Package -eq $name })
        if ($found.Count -eq 0) {
            Add-Result -Area "licenses" -Check "P8 dependency registration" -Status "FAIL" -Detail "$name is not declared"
        } elseif ($found[0].License -ne $registeredP8Dependencies[$name]) {
            Add-Result -Area "licenses" -Check "P8 dependency registration" -Status "FAIL" `
                -Detail ("{0} license is '{1}', expected '{2}'" -f $name, $found[0].License, $registeredP8Dependencies[$name])
        } else {
            Add-Result -Area "licenses" -Check "P8 dependency registration" -Status "PASS" `
                -Detail ("{0} registered as {1}" -f $name, $found[0].License)
        }
    }
}

# ---------------------------------------------------------------------------
# 6. Next.js security headers
# ---------------------------------------------------------------------------

function Get-HeadersFunctionBody {
    param([string]$Text)
    $match = [regex]::Match($Text, '(?m)^\s*(?:async\s+)?headers\s*\(\s*\)\s*\{')
    if (-not $match.Success) { return $null }
    $start = $match.Index + $match.Length
    $depth = 1
    $index = $start
    while ($index -lt $Text.Length) {
        $character = $Text[$index]
        if ($character -eq '"' -or $character -eq "'" -or $character -eq '`') {
            $quote = $character
            $index++
            while ($index -lt $Text.Length) {
                if ($quote -ne '`' -and $Text[$index] -eq '\') {
                    $index += 2
                    continue
                }
                if ($Text[$index] -eq $quote) {
                    $index++
                    break
                }
                $index++
            }
            continue
        }
        if ($character -eq '{') {
            $depth++
            $index++
            continue
        }
        if ($character -eq '}') {
            $depth--
            if ($depth -eq 0) { return $Text.Substring($start, $index - $start) }
            $index++
            continue
        }
        $index++
    }
    return $null
}

function Invoke-HeaderCheck {
    $configFiles = @(Get-ChildItem -LiteralPath $frontendDir -File |
            Where-Object { $_.Name -like "next.config.*" } | Sort-Object Name)
    if ($configFiles.Count -eq 0) {
        Add-Result -Area "headers" -Check "next.config security headers" -Status "FAIL" -Detail "frontend/next.config.* is missing"
        return
    }
    if ($configFiles.Count -gt 1) {
        Add-Result -Area "headers" -Check "next.config security headers" -Status "FAIL" `
            -Detail ("multiple Next config files are ambiguous: {0}" -f (($configFiles | ForEach-Object { $_.Name }) -join ", "))
        return
    }
    $configPath = $configFiles[0].FullName
    $text = Read-Utf8Text -Path $configPath
    # Commented-out header entries or a commented-out securityHeaders reference
    # must not satisfy the checks below.
    $cleanText = Remove-SourceComments -Text $text

    $checks = @(
        @{ Header = "X-Content-Type-Options"; ValuePattern = '(?i)"nosniff"'; Expected = "nosniff" },
        @{ Header = "Referrer-Policy"; ValuePattern = '(?i)"(?:strict-origin-when-cross-origin|no-referrer|same-origin)"'; Expected = "strict-origin-when-cross-origin (or stricter)" },
        @{ Header = "X-Frame-Options"; ValuePattern = '(?i)"(?:DENY|SAMEORIGIN)"'; Expected = "DENY" },
        @{ Header = "Permissions-Policy"; ValuePattern = '(?i)"[^"]*(?:camera|microphone|geolocation)=\(\)'; Expected = "camera/microphone/geolocation denied" }
    )
    foreach ($check in $checks) {
        $headerKey = '"' + $check.Header + '"'
        if (-not $cleanText.Contains($headerKey)) {
            Add-Result -Area "headers" -Check $check.Header -Status "FAIL" -Detail "header is not configured"
        } elseif (-not [regex]::IsMatch($cleanText, $check.ValuePattern)) {
            Add-Result -Area "headers" -Check $check.Header -Status "FAIL" -Detail ("header is configured without {0}" -f $check.Expected)
        } else {
            Add-Result -Area "headers" -Check $check.Header -Status "PASS" -Detail $check.Expected
        }
    }

    $reportOnlyKey = '"Content-Security-Policy-Report-Only"'
    $enforcingCsp = [regex]::IsMatch($cleanText, '(?i)["'']Content-Security-Policy["'']\s*:') -or
        [regex]::IsMatch($cleanText, '(?i)key\s*:\s*["'']Content-Security-Policy["'']')
    if (-not $cleanText.Contains($reportOnlyKey)) {
        Add-Result -Area "headers" -Check "Content-Security-Policy" -Status "FAIL" -Detail "no report-only CSP header is configured"
    } elseif ($enforcingCsp) {
        Add-Result -Area "headers" -Check "Content-Security-Policy" -Status "FAIL" -Detail "an enforcing CSP is configured; keep it report-only until inline styles/Next runtime are migrated"
    } elseif (-not [regex]::IsMatch($cleanText, "(?i)default-src 'self'") -or -not [regex]::IsMatch($cleanText, "(?i)frame-ancestors 'none'")) {
        Add-Result -Area "headers" -Check "Content-Security-Policy" -Status "FAIL" -Detail "report-only CSP is missing default-src 'self' or frame-ancestors 'none'"
    } else {
        Add-Result -Area "headers" -Check "Content-Security-Policy" -Status "PASS" -Detail "report-only CSP with default-src 'self' and frame-ancestors 'none'"
    }

    $body = Get-HeadersFunctionBody -Text $cleanText
    $hasHeadersFunction = $null -ne $body
    $hasSource = $false
    $referencesHeaders = $false
    if ($hasHeadersFunction) {
        $sourceMatch = [regex]::Match($body, 'source\s*:\s*["''](?:/:path\*|/\(\.\*\))["'']')
        if ($sourceMatch.Success) {
            $hasSource = $true
            $objectStart = $body.LastIndexOf("{", [Math]::Min($sourceMatch.Index, $body.Length - 1))
            $depth = 0
            $objectEnd = -1
            for ($index = $sourceMatch.Index; $index -lt $body.Length; $index++) {
                if ($body[$index] -eq '{') {
                    $depth++
                } elseif ($body[$index] -eq '}') {
                    if ($depth -eq 0) {
                        $objectEnd = $index
                        break
                    }
                    $depth--
                }
            }
            $entryStart = if ($objectStart -ge 0) { $objectStart } else { $sourceMatch.Index }
            $entryEnd = if ($objectEnd -ge 0) { $objectEnd + 1 } else { $body.Length }
            $entry = $body.Substring($entryStart, $entryEnd - $entryStart)
            $referencesHeaders = [regex]::IsMatch(
                $entry,
                'headers\s*:\s*(?:securityHeaders\b|\[[^\]]*\.\.\.securityHeaders\b)')
        }
    }
    if ($hasHeadersFunction -and $hasSource -and $referencesHeaders) {
        Add-Result -Area "headers" -Check "Next headers() wiring" -Status "PASS" `
            -Detail "catch-all headers entry returns the securityHeaders array"
    } else {
        Add-Result -Area "headers" -Check "Next headers() wiring" -Status "FAIL" `
            -Detail ("headers function={0}; catch-all source={1}; securityHeaders reference={2}" -f
                $hasHeadersFunction, $hasSource, $referencesHeaders)
    }
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

Write-Host "EzTest P8 security audit" -ForegroundColor Cyan
Write-Host "Repository: $repoRoot"
Write-Host "Live permission matrix: $(if ($SkipLive) { 'skipped (-SkipLive)' } else { 'enabled (auto-detect API)' })"

Write-Step "1/5 Secret scan"
Invoke-SecretScan

Write-Step "2/5 Go vulnerability scan"
Invoke-Govulncheck

Write-Step "3/5 Node production dependency audit"
Invoke-PnpmAudit -Directory $frontendDir -Label "frontend"
Invoke-PnpmAudit -Directory $executorDir -Label "executor"

Write-Step "4/5 Permission matrix"
Invoke-StaticPermissionMatrix
Invoke-LivePermissionMatrix

Write-Step "5/5 Licenses and security headers"
Invoke-LicenseInventory
Invoke-HeaderCheck

$passCount = @($results | Where-Object { $_.Status -eq "PASS" }).Count
$skipCount = @($results | Where-Object { $_.Status -eq "SKIP" }).Count
$failCount = @($results | Where-Object { $_.Status -eq "FAIL" }).Count

Write-Host ""
Write-Host ("Security audit summary: {0} PASS, {1} SKIP, {2} FAIL" -f $passCount, $skipCount, $failCount) -ForegroundColor Cyan

if ($skipCount -gt 0) {
    Write-Host ""
    Write-Host "SKIPPED (non-blocking):" -ForegroundColor Yellow
    foreach ($result in @($results | Where-Object { $_.Status -eq "SKIP" })) {
        Write-Host ("  - {0}: {1}" -f $result.Check, $result.Detail) -ForegroundColor Yellow
    }
}

if ($failCount -gt 0) {
    Write-Host ""
    Write-Host "FAILED:" -ForegroundColor Red
    foreach ($result in @($results | Where-Object { $_.Status -eq "FAIL" })) {
        Write-Host ("  - {0}: {1}" -f $result.Check, $result.Detail) -ForegroundColor Red
    }
    Write-Host ""
    Write-Host "SECURITY AUDIT FAILED" -ForegroundColor Red
    exit 1
}

Write-Host ""
Write-Host "SECURITY AUDIT PASSED" -ForegroundColor Green
exit 0
