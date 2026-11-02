#requires -Version 5.1
<#
.SYNOPSIS
    Builds the Docker-free EzTest release packages (Windows x64 + Linux x64).

.DESCRIPTION
    Implements the release packaging pipeline from
    `.tmp/review/packaging-plan.md` §3 (steps 0-9):

      0. Preconditions: go/node/pnpm/docs, git commit, disk space, and the
         web-build guard (fails when port 6868 or a next dev/build process is
         running - coordinate with frontend-ui / stop the dev stack first).
      1. API cross-builds for windows/amd64 and linux/amd64 with -trimpath and
         -X main.version=<Version> (0.2 keeps the version in package main;
         the v0.1 internal/server.Version symbol no longer exists).
      2. Next.js standalone build once (unless -SkipWebBuild), copy
         .next/standalone + .next/static (and public/ if present), flatten the
         pnpm store into a symlink-free node_modules, prune sharp / @img and
         abort when any platform-native binary remains.
      3. Executor + PDF renderer staging: copy package.json/pnpm-lock/src and
         install production, hoisted, symlink-free node_modules for both
         services.
      4. Pinned Playwright browsers per OS: cached download + extraction into
         playwright-browsers/<browser>/ with INSTALLATION_COMPLETE markers.
      5. Official Node 22 LTS runtime per OS (node.exe + LICENSE; bin/node +
         LICENSE) from nodejs.org (checks digests against SHASUMS256.txt).
      6. Assemble both package trees (layout in the plan §2), writing VERSION,
         manifest.json, the runtime scripts, PACKAGE.md, THIRD_PARTY_NOTICES.md,
         .env.example, LICENSE, docs/openapi/api.yaml. The .data content
         directories are created by start-eztest.* at runtime, never shipped.
      7. Runtime-script sanity: PowerShell parser check, WSL `bash -n`, expected
         files, forbidden paths (.env/.git/.tools/.data/node_modules/.cache) and
         repository-path/secret leak scans.
      8. Archives (zip / tar.gz) + SHA256SUMS.txt + archive content scan.
      9. Optional package smoke test on this host (Windows native and one-shot
         WSL Linux harness with a throw-away PostgreSQL 17), unless -SkipSmoke.

    Nothing is committed or pushed: artifacts go to -OutDir (default
    .tmp/release, gitignored) and intermediate files to -WorkDir (default
    .tmp/pkgprobe/build). Downloads are cached in .tmp/pkgprobe/cache.

    IMPORTANT: this script never runs `next build` without checking the web
    guard first; do not run it while the demo/dev web stack is live.

.PARAMETER Version
    Release version stamped into the API binary, VERSION and manifest.json
    (default 0.2.0-preview.1).

.PARAMETER RepoRoot
    Repository root. Defaults to the parent directory of scripts/.

.PARAMETER OutDir
    Output directory for the archives and SHA256SUMS.txt (relative to RepoRoot).

.PARAMETER ApiBaseUrl
    NEXT_PUBLIC_API_BASE_URL inlined into the web bundle at build time
    (default http://localhost:13333, matching the packaged API default).

.PARAMETER WorkDir
    Intermediate build directory (relative to RepoRoot).

.PARAMETER BundlePostgres
    Windows only: bundle the already-cached portable PostgreSQL 17 from
    .tools/pgsql (bin/lib/share) as pgsql/ in the Windows package.

.PARAMETER SlimBrowsers
    Ship the headless shell + ffmpeg only (skip the full Chromium build);
    saves ~200 MB per package.

.PARAMETER SkipWebBuild
    Do not run `pnpm build`; reuse the existing frontend/.next/standalone.
    Fails when that output is missing.

.PARAMETER SkipSmoke
    Do not run the package smoke tests (step 9). Also skips the WSL runs.

.NOTES
    Compatible with Windows PowerShell 5.1 and PowerShell 7+.
    Dot-source the file (`. .\scripts\build-release.ps1`) to import the helper
    functions without running the pipeline.
#>
[CmdletBinding()]
param(
    [string]$Version = "0.2.0-preview.1",
    [string]$RepoRoot = "",
    [string]$OutDir = ".tmp/release",
    [string]$ApiBaseUrl = "http://localhost:13333",
    [string]$WorkDir = ".tmp/pkgprobe/build",
    [switch]$BundlePostgres,
    [switch]$SlimBrowsers,
    [switch]$SkipWebBuild,
    [switch]$SkipSmoke
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

# ---------------------------------------------------------------------------
# Pinned component versions (packaging-plan.md §5/§6).
# ---------------------------------------------------------------------------
$script:PinnedNodeVersion = "22.23.4"
$script:PinnedPlaywrightVersion = "1.64.1"
$script:PinnedChromiumVersion = "156.0.8078.4"
$script:PinnedChromiumRevision = "1248"
$script:PinnedFfmpegRevision = "1013"
$script:ZonkyPostgresJarUrl = "https://repo1.maven.org/maven2/io/zonky/test/postgres/embedded-postgres-binaries-linux-amd64/17.6.0/embedded-postgres-binaries-linux-amd64-17.6.0.jar"
$script:PackageScripts = @(
    "init-config.ps1", "init-config.sh",
    "start-eztest.ps1", "start-eztest.sh",
    "stop-eztest.ps1", "stop-eztest.sh",
    "status-eztest.ps1", "status-eztest.sh",
    "smoke-test.ps1", "smoke-test.sh",
    "install-linux-deps.sh",
    "PACKAGE.md", "THIRD_PARTY_NOTICES.md", ".env.example"
)
$script:Utf8NoBom = New-Object System.Text.UTF8Encoding($false)

# Filled by Initialize-BuildContext / preconditions.
$script:StepIndex = 0
$script:WslAvailable = $false
$script:GoModulePath = ""
$script:Commit = "unknown"
$script:CacheDir = ""
$script:WorkRoot = ""
$script:ReleaseDir = ""
$script:Yaml = ""
$script:FreeDiskWarned = $false

# ---------------------------------------------------------------------------
# Logging / small helpers.
# ---------------------------------------------------------------------------
function Write-Step {
    param([string]$Title)
    $script:StepIndex++
    Write-Host ""
    Write-Host ("== Step {0}: {1}" -f $script:StepIndex, $Title) -ForegroundColor Cyan
}

function Write-Info {
    param([string]$Message)
    Write-Host ("   " + $Message)
}

function Write-Warn {
    param([string]$Message)
    Write-Host ("   WARNING: " + $Message) -ForegroundColor Yellow
}

function Write-Fail {
    param([string]$Message)
    Write-Host ""
    Write-Host ("ERROR: " + $Message) -ForegroundColor Red
    throw $Message
}

function Format-Bytes {
    param([long]$Bytes)
    if ($Bytes -ge 1GB) { return ("{0:N1} GB" -f ($Bytes / 1GB)) }
    if ($Bytes -ge 1MB) { return ("{0:N1} MB" -f ($Bytes / 1MB)) }
    if ($Bytes -ge 1KB) { return ("{0:N1} KB" -f ($Bytes / 1KB)) }
    return "$Bytes B"
}

function Format-Duration {
    param([double]$Seconds)
    if ($Seconds -ge 60) { return ("{0:N0}m {1:N0}s" -f [math]::Floor($Seconds / 60), ($Seconds % 60)) }
    return ("{0:N1}s" -f $Seconds)
}

function Write-FileUtf8NoBom {
    param([string]$Path, [string]$Text)
    $parent = Split-Path -Parent $Path
    if ($parent -and -not (Test-Path -LiteralPath $parent -PathType Container)) {
        New-Item -ItemType Directory -Force -Path $parent | Out-Null
    }
    [System.IO.File]::WriteAllText($Path, $Text, $script:Utf8NoBom)
}

function Read-TextFileUtf8 {
    param([string]$Path)
    return [System.IO.File]::ReadAllText($Path, [System.Text.Encoding]::UTF8)
}

function Get-ChildItemSafe {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { return @() }
    return @(Get-ChildItem -LiteralPath $Path -Force)
}

function Get-FreeDiskBytes {
    param([string]$Path)
    try {
        $root = [System.IO.Path]::GetPathRoot($Path)
        if ([string]::IsNullOrEmpty($root)) { return -1 }
        $drive = New-Object System.IO.DriveInfo($root)
        return $drive.AvailableFreeSpace
    } catch {
        return -1
    }
}

function Get-DirectorySizeBytes {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { return 0 }
    $sum = (Get-ChildItem -LiteralPath $Path -Recurse -File -Force -ErrorAction SilentlyContinue | Measure-Object -Property Length -Sum).Sum
    if ($null -eq $sum) { return 0 }
    return [long]$sum
}

function Get-FileSha256 {
    param([string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

# Deletes a directory tree without following junctions/symlinks (PowerShell
# 5.1's Remove-Item -Recurse would descend into them in some cases).
function Remove-TreeSafe {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Any)) { return }
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
        if ($item.PSIsContainer) {
            [System.IO.Directory]::Delete($item.FullName, $false)
        } else {
            [System.IO.File]::Delete($item.FullName)
        }
        return
    }
    if ($item.PSIsContainer) {
        foreach ($child in @(Get-ChildItem -LiteralPath $item.FullName -Force)) {
            Remove-TreeSafe -Path $child.FullName
        }
        [System.IO.Directory]::Delete($item.FullName, $false)
    } else {
        [System.IO.File]::Delete($item.FullName)
    }
}

function Copy-DirContent {
    param([string]$Source, [string]$Destination)
    if (-not (Test-Path -LiteralPath $Source -PathType Container)) {
        Write-Fail "Copy source directory not found: $Source"
    }
    if (-not (Test-Path -LiteralPath $Destination -PathType Container)) {
        New-Item -ItemType Directory -Force -Path $Destination | Out-Null
    }
    foreach ($entry in @(Get-ChildItem -LiteralPath $Source -Force)) {
        $target = Join-Path $Destination $entry.Name
        if ($entry.PSIsContainer) {
            Copy-Item -LiteralPath $entry.FullName -Destination $target -Recurse -Force
        } else {
            Copy-Item -LiteralPath $entry.FullName -Destination $target -Force
        }
    }
}

function Resolve-RealPath {
    param([string]$Path)
    $item = Get-Item -LiteralPath $Path -Force
    if (-not ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
        return $item.FullName
    }
    $target = $item.Target
    if ($null -eq $target) {
        Write-Fail "Cannot resolve the junction/symlink target of '$Path' (win32 reparse points are supported)."
    }
    if ($target -is [array]) { $target = $target[0] }
    if (-not [System.IO.Path]::IsPathRooted($target)) {
        $target = Join-Path (Split-Path -Parent $Path) $target
    }
    return (Get-Item -LiteralPath $target -Force).FullName
}

function Test-TcpPort {
    param([string]$TargetHost, [int]$Port, [int]$TimeoutMs = 2000)
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $iar = $client.BeginConnect($TargetHost, $Port, $null, $null)
        if (-not $iar.AsyncWaitHandle.WaitOne($TimeoutMs, $false)) { return $false }
        $client.EndConnect($iar)
        return $true
    } catch {
        return $false
    } finally {
        $client.Close()
    }
}

function ConvertTo-WslPath {
    param([string]$WindowsPath)
    $full = [System.IO.Path]::GetFullPath($WindowsPath)
    if ($full -match '^([A-Za-z]):\\(.*)$') {
        $drive = $matches[1].ToLowerInvariant()
        $rest = $matches[2] -replace '\\', '/'
        return "/mnt/$drive/$rest"
    }
    Write-Fail "Cannot convert '$WindowsPath' to a WSL path (only local drive paths are supported)."
}

function Invoke-WithTimeout {
    param(
        [scriptblock]$Condition,
        [int]$Seconds,
        [string]$Label,
        [System.Diagnostics.Process]$Process = $null
    )
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        if ($null -ne $Process) {
            try { $Process.Refresh() } catch { }
            if ($Process.HasExited) { throw "$Label process exited with code $($Process.ExitCode)" }
        }
        if (& $Condition) { return $true }
        Start-Sleep -Milliseconds 400
    }
    return $false
}

# Quotes a native argument list so Start-Process forwards arguments containing
# spaces (e.g. the Go -ldflags value) as a single argument.
function Format-ProcessArgumentList {
    param([string[]]$Arguments)
    $result = @()
    foreach ($argument in @($Arguments)) {
        if ($null -eq $argument) { continue }
        $text = "$argument"
        if ($text.Length -eq 0) { $result += '""'; continue }
        if ($text -match '[\s"]') {
            $result += ('"' + ($text -replace '"', '\"') + '"')
        } else {
            $result += $text
        }
    }
    return $result
}

# Runs a process and captures stdout/stderr. Uses Start-Process -Wait -PassThru
# so the exit code is reliable (without -Wait PowerShell 5.1 loses it) and the
# native stderr never becomes a terminating NativeCommandError.
function Invoke-ProcessCapture {
    param(
        [string]$FilePath,
        [string[]]$Arguments,
        [string]$WorkingDirectory = "",
        [int]$TimeoutMs = 0
    )
    $tempRoot = Join-Path $script:WorkRoot "tmp"
    if (-not (Test-Path -LiteralPath $tempRoot -PathType Container)) {
        New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null
    }
    $stamp = [guid]::NewGuid().ToString("N")
    $stdoutPath = Join-Path $tempRoot ("capture-" + $stamp + ".out")
    $stderrPath = Join-Path $tempRoot ("capture-" + $stamp + ".err")
    $startParams = @{
        FilePath               = $FilePath
        ArgumentList           = (Format-ProcessArgumentList -Arguments $Arguments)
        NoNewWindow            = $true
        Wait                   = $true
        PassThru               = $true
        RedirectStandardOutput = $stdoutPath
        RedirectStandardError  = $stderrPath
    }
    if ($WorkingDirectory.Length -gt 0) { $startParams["WorkingDirectory"] = $WorkingDirectory }
    try {
        $process = Start-Process @startParams
        $stdout = @()
        $stderr = @()
        if (Test-Path -LiteralPath $stdoutPath) { $stdout = @(Get-Content -LiteralPath $stdoutPath -ErrorAction SilentlyContinue) }
        if (Test-Path -LiteralPath $stderrPath) { $stderr = @(Get-Content -LiteralPath $stderrPath -ErrorAction SilentlyContinue) }
        return [pscustomobject]@{
            ExitCode = [int]$process.ExitCode
            StdOut   = $stdout
            StdErr   = $stderr
            Output   = @($stdout + $stderr)
        }
    } finally {
        Remove-Item -LiteralPath $stdoutPath -Force -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath $stderrPath -Force -ErrorAction SilentlyContinue
    }
}

# Runs a process with stdout/stderr redirected to the given log files and
# returns its exit code reliably on Windows PowerShell 5.1 and PowerShell 7+.
#
# Do NOT use `Start-Process -PassThru` without -Wait here: Windows PowerShell
# 5.1 returns a process object whose ExitCode is empty in that mode, which would
# silently turn every failed child (go/pnpm/wsl/smoke script) into exit code 0.
# .NET Process is used directly instead; it waits only for the direct child, not
# for its process tree (the packaged start script deliberately leaves the API,
# executor, web and renderer running), and its ExitCode is always populated.
function Invoke-ProcessWithLog {
    param(
        [string]$FilePath,
        [string[]]$Arguments,
        [string]$StdOutPath,
        [string]$StdErrPath,
        [string]$WorkingDirectory = "",
        [switch]$Quiet,
        [int]$TailLines = 30
    )
    foreach ($logPath in @($StdOutPath, $StdErrPath)) {
        $parent = Split-Path -Parent $logPath
        if ($parent -and -not (Test-Path -LiteralPath $parent -PathType Container)) {
            New-Item -ItemType Directory -Force -Path $parent | Out-Null
        }
        if (Test-Path -LiteralPath $logPath) { Remove-Item -LiteralPath $logPath -Force -ErrorAction SilentlyContinue }
    }

    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $FilePath
    $psi.Arguments = ((Format-ProcessArgumentList -Arguments $Arguments) -join ' ')
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    try {
        $psi.StandardOutputEncoding = [System.Text.Encoding]::UTF8
        $psi.StandardErrorEncoding = [System.Text.Encoding]::UTF8
    } catch { }
    if ($WorkingDirectory.Length -gt 0) { $psi.WorkingDirectory = $WorkingDirectory }
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $psi
    if (-not $process.Start()) { Write-Fail "Could not start process: $FilePath" }
    $stdoutTask = $process.StandardOutput.ReadToEndAsync()
    $stderrTask = $process.StandardError.ReadToEndAsync()
    $process.WaitForExit()
    $exitCode = [int]$process.ExitCode
    $stdoutText = ""
    $stderrText = ""
    if ($stdoutTask.Wait(120000)) { try { $stdoutText = [string]$stdoutTask.Result } catch { } }
    if ($stderrTask.Wait(120000)) { try { $stderrText = [string]$stderrTask.Result } catch { } }
    $process.Dispose()
    Write-FileUtf8NoBom -Path $StdOutPath -Text $stdoutText
    Write-FileUtf8NoBom -Path $StdErrPath -Text $stderrText

    if (-not $Quiet) {
        foreach ($stream in @(
            @{ Path = $StdOutPath; Label = "stdout" },
            @{ Path = $StdErrPath; Label = "stderr" }
        )) {
            if (-not (Test-Path -LiteralPath $stream.Path)) { continue }
            $lines = @(Get-Content -LiteralPath $stream.Path -ErrorAction SilentlyContinue)
            if ($lines.Count -eq 0) { continue }
            Write-Host ("   --- {0} ({1} lines, tail {2}) ---" -f $stream.Label, $lines.Count, $stream.Path) -ForegroundColor DarkGray
            $tail = if ($TailLines -gt 0 -and $lines.Count -gt $TailLines) { $lines[($lines.Count - $TailLines)..($lines.Count - 1)] } else { $lines }
            foreach ($line in $tail) { Write-Host ("   " + $line) }
        }
    }
    return $exitCode
}

function Get-LogTailText {
    param([string]$Path, [int]$Lines = 15)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return "" }
    return ((Get-Content -LiteralPath $Path -Tail $Lines -ErrorAction SilentlyContinue) -join [Environment]::NewLine)
}

# ---------------------------------------------------------------------------
# Preconditions (step 0).
# ---------------------------------------------------------------------------
function Initialize-BuildContext {
    if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
        $RepoRoot = Split-Path -Parent $PSScriptRoot
    }
    if (-not (Test-Path -LiteralPath $RepoRoot -PathType Container)) {
        Write-Fail "RepoRoot does not exist: $RepoRoot"
    }
    $script:RepoRoot = (Resolve-Path -LiteralPath $RepoRoot).Path
    $script:OutDir = Join-Path $script:RepoRoot $OutDir
    $script:WorkRoot = Join-Path $script:RepoRoot $WorkDir
    $script:CacheDir = Join-Path $script:RepoRoot ".tmp\pkgprobe\cache"
    $script:ReleaseDir = $script:OutDir
    $script:Yaml = Join-Path $script:RepoRoot "docs\openapi\api.yaml"
    $script:IsWindowsHost = ([System.IO.Path]::DirectorySeparatorChar -eq '\')

    if ($Version -notmatch '^\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$') {
        Write-Fail "-Version must look like 0.1.0-preview.1 (got '$Version')."
    }
    if ($ApiBaseUrl -notmatch '^https?://[^/\s]+') {
        Write-Fail "-ApiBaseUrl must be an absolute http(s) URL (got '$ApiBaseUrl')."
    }
    if ($BundlePostgres -and -not $script:IsWindowsHost) {
        Write-Warn "-BundlePostgres only affects the Windows package; continuing."
    }
}

function Get-GoExe {
    $bundled = Join-Path $script:RepoRoot ".tools\go\bin\go.exe"
    if (Test-Path -LiteralPath $bundled -PathType Leaf) { return $bundled }
    $command = Get-Command go.exe -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        $command = Get-Command go -ErrorAction SilentlyContinue
    }
    if ($null -eq $command) {
        Write-Fail "Go is not available. Run .tools/setup-toolchain.ps1 or install Go 1.24+."
    }
    return $command.Source
}

function Get-NodeExe {
    $command = Get-Command node.exe -ErrorAction SilentlyContinue
    if ($null -eq $command) { $command = Get-Command node -ErrorAction SilentlyContinue }
    if ($null -eq $command) { Write-Fail "Node.js is not available on PATH (needed for pnpm and release staging)." }
    return $command.Source
}

function Get-PnpmExe {
    $cmd = Get-Command pnpm.cmd -ErrorAction SilentlyContinue
    if ($null -ne $cmd) { return $cmd.Source }
    $command = Get-Command pnpm -ErrorAction SilentlyContinue
    if ($null -eq $command) { Write-Fail "pnpm is not available on PATH. Install pnpm (corepack enable) and retry." }
    return $command.Source
}

function Get-GitCommit {
    $git = Get-Command git.exe -ErrorAction SilentlyContinue
    if ($null -eq $git) { $git = Get-Command git -ErrorAction SilentlyContinue }
    if ($null -eq $git) {
        Write-Warn "git is not available; the API commit stamp will be 'unknown'."
        return "unknown"
    }
    $commit = ""
    try {
        $revParse = Invoke-ProcessCapture -FilePath $git.Source -Arguments @("-C", $script:RepoRoot, "rev-parse", "--short", "HEAD")
        if ($revParse.ExitCode -eq 0 -and $revParse.StdOut.Count -gt 0) {
            $commit = ("$($revParse.StdOut[0])").Trim()
        }
    } catch { }
    if ([string]::IsNullOrWhiteSpace($commit)) {
        Write-Warn "git rev-parse failed; the API commit stamp will be 'unknown'."
        return "unknown"
    }
    try {
        $status = Invoke-ProcessCapture -FilePath $git.Source -Arguments @("-C", $script:RepoRoot, "status", "--porcelain")
        if ($status.StdOut.Count -gt 0) { Write-Warn "The working tree has uncommitted changes; the commit stamp ($commit) identifies HEAD, not the exact sources." }
    } catch { }
    return $commit
}

function Get-GoModulePath {
    $goMod = Join-Path $script:RepoRoot "backend\go.mod"
    if (-not (Test-Path -LiteralPath $goMod -PathType Leaf)) {
        Write-Fail "backend\go.mod not found under $($script:RepoRoot)."
    }
    $first = Get-Content -LiteralPath $goMod -TotalCount 5 | Where-Object { $_ -match '^module\s+' } | Select-Object -First 1
    if ($null -eq $first) { Write-Fail "Cannot read the module path from backend/go.mod." }
    return ($first -replace '^module\s+', '').Trim()
}

function Test-WebBuildGuard {
    Write-Info "Web-build guard: checking port 6868 and any running next dev/build process"
    $problems = New-Object System.Collections.ArrayList
    if (Test-TcpPort -TargetHost "127.0.0.1" -Port 6868 -TimeoutMs 1500) {
        [void]$problems.Add("Port 6868 is already listening (a web server is serving frontend/.next).")
    }
    $processes = @()
    try {
        $processes = @(Get-CimInstance -ClassName Win32_Process -ErrorAction SilentlyContinue | Where-Object { $_.ProcessId -ne $PID -and $_.CommandLine })
    } catch {
        Write-Warn "Could not enumerate processes; only the port check was performed."
    }
    foreach ($process in $processes) {
        $commandLine = "$($process.CommandLine)"
        if ($commandLine -match '(?i)(next(\.js)?[\s\\/]+(build|dev)|next\.cmd"?[\s]+(build|dev)|pnpm(\.cmd|\.exe)?[\s]+(run[\s]+)?(build|dev)\b)') {
            [void]$problems.Add("Process $($process.ProcessId) looks like a next build/dev run: $($process.Name)")
        }
    }
    if ($problems.Count -gt 0) {
        foreach ($problem in $problems) { Write-Host ("   - " + $problem) -ForegroundColor Yellow }
        Write-Fail @"
Web build guard failed. The frontend .next directory is in use by a running web server or a next build/dev process.
Coordinate with frontend-ui (or stop the dev/demo web stack) before building the release.
If the existing frontend/.next/standalone output is current and you only need to re-assemble
packages, re-run with -SkipWebBuild.
"@
    }
    Write-Info "Web-build guard passed."
}

function Initialize-Wsl {
    $script:WslAvailable = $false
    $wsl = Get-Command wsl.exe -ErrorAction SilentlyContinue
    if ($null -eq $wsl) {
        Write-Warn "WSL is not available: 'bash -n' checks and the Linux package smoke test will be skipped."
        return
    }
    $result = Invoke-ProcessCapture -FilePath $wsl.Source -Arguments @("-e", "bash", "-c", "true") -TimeoutMs 60000
    if ($result.ExitCode -eq 0) {
        $script:WslAvailable = $true
        Write-Info "WSL is available (Linux bash syntax checks and the Linux smoke test can run)."
        return
    }
    Write-Warn "WSL is installed but 'wsl.exe -e bash' did not work: 'bash -n' checks and the Linux smoke test will be skipped."
}

function Test-BuildPreconditions {
    Write-Step "Preconditions and web-build guard"
    Write-Info "Repository : $($script:RepoRoot)"
    Write-Info "Version    : $Version"
    Write-Info "Work dir   : $($script:WorkRoot)"
    Write-Info "Output dir : $($script:ReleaseDir)"

    foreach ($relative in @("backend\go.mod", "frontend\package.json", "frontend\pnpm-lock.yaml", "executor\package.json", "executor\pnpm-lock.yaml", "renderer\package.json", "renderer\pnpm-lock.yaml", "docs\openapi\api.yaml", "scripts\package\start-eztest.ps1", "LICENSE")) {
        $path = Join-Path $script:RepoRoot $relative
        if (-not (Test-Path -LiteralPath $path)) {
            Write-Fail "Required repository file is missing: $relative"
        }
    }
    $script:Yaml = Join-Path $script:RepoRoot "docs\openapi\api.yaml"
    $yamlSize = (Get-Item -LiteralPath $script:Yaml).Length
    if ($yamlSize -lt 100000) { Write-Fail "docs/openapi/api.yaml looks too small ($yamlSize bytes)." }
    Write-Info ("OpenAPI document: docs/openapi/api.yaml ({0})" -f (Format-Bytes $yamlSize))

    $script:GoExe = Get-GoExe
    $script:NodeExe = Get-NodeExe
    $script:PnpmExe = Get-PnpmExe
    Write-Info "Go         : $($script:GoExe)"
    Write-Info "Node       : $($script:NodeExe)"
    Write-Info "pnpm       : $($script:PnpmExe)"

    $script:GoModulePath = Get-GoModulePath
    $script:Commit = Get-GitCommit
    Write-Info "Go module  : $($script:GoModulePath)"
    Write-Info "Commit     : $($script:Commit)"

    $freeBytes = Get-FreeDiskBytes -Path $script:RepoRoot
    if ($freeBytes -gt 0) {
        Write-Info ("Free disk  : {0}" -f (Format-Bytes $freeBytes))
        if ($freeBytes -lt 6GB) {
            Write-Warn "Less than 6 GB free disk space. The build needs about 4 GB (downloads + staging + archives)."
            $script:FreeDiskWarned = $true
        }
    }

    if (-not $SkipWebBuild) {
        Test-WebBuildGuard
    } else {
        $standalone = Join-Path $script:RepoRoot "frontend\.next\standalone"
        if (-not (Test-Path -LiteralPath (Join-Path $standalone "server.js"))) {
            Write-Fail "-SkipWebBuild was passed but frontend/.next/standalone/server.js does not exist. Run the web build once (without -SkipWebBuild, after coordinating with frontend-ui) first."
        }
        Write-Info "-SkipWebBuild: reusing the existing frontend/.next/standalone output."
    }

    Initialize-Wsl

    New-Item -ItemType Directory -Force -Path $script:WorkRoot | Out-Null
    New-Item -ItemType Directory -Force -Path $script:CacheDir | Out-Null
    New-Item -ItemType Directory -Force -Path $script:ReleaseDir | Out-Null
    foreach ($stale in @("payload", "pkg", "smoke", "logs")) {
        Remove-TreeSafe -Path (Join-Path $script:WorkRoot $stale)
    }
    New-Item -ItemType Directory -Force -Path (Join-Path $script:WorkRoot "logs") | Out-Null
}

# ---------------------------------------------------------------------------
# Step 1: API cross-builds.
# ---------------------------------------------------------------------------
function Build-ApiBinaries {
    Write-Step "Go API cross-builds (windows/amd64 + linux/amd64, version $Version)"
    $stage = Join-Path $script:WorkRoot "payload\api"
    # EzTest 0.2 keeps the release version in package main
    # (backend/cmd/eztest/main.go: `var version = "..."`). The v0.1
    # internal/server.Version/.Commit symbols no longer exist, and -X on a
    # missing symbol fails the link, so stamp main.version only.
    $ldflags = "-s -w -X main.version=$Version"
    Write-Info "ldflags: $ldflags"

    $targets = @(
        @{ Os = "windows"; Output = (Join-Path $stage "windows\eztest.exe") },
        @{ Os = "linux"; Output = (Join-Path $stage "linux\eztest") }
    )
    $savedGoos = $env:GOOS
    $savedGoarch = $env:GOARCH
    $savedCgo = $env:CGO_ENABLED
    $backendDir = Join-Path $script:RepoRoot "backend"
    try {
        foreach ($target in $targets) {
            $outputPath = $target.Output
            $parent = Split-Path -Parent $outputPath
            New-Item -ItemType Directory -Force -Path $parent | Out-Null
            $env:GOOS = $target.Os
            $env:GOARCH = "amd64"
            $env:CGO_ENABLED = "0"
            Write-Host ("   [{0}/2] GOOS={0} GOARCH=amd64 CGO_ENABLED=0 ..." -f $target.Os) -NoNewline
            $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
            $buildLog = Join-Path $script:WorkRoot ("logs\go-build-" + $target.Os + ".log")
            $exitCode = Invoke-ProcessWithLog -FilePath $script:GoExe `
                -Arguments @("build", "-trimpath", "-ldflags", $ldflags, "-o", $outputPath, "./cmd/eztest") `
                -StdOutPath $buildLog -StdErrPath ($buildLog + ".err") -WorkingDirectory $backendDir
            $stopwatch.Stop()
            if ($exitCode -ne 0) {
                Write-Host ""
                Write-Fail ("go build failed for GOOS={0} (exit {1}):{2}{3}" -f $target.Os, $exitCode, [Environment]::NewLine, (Get-LogTailText -Path $buildLog))
            }
            if (-not (Test-Path -LiteralPath $outputPath -PathType Leaf)) {
                Write-Fail "go build reported success but $outputPath does not exist."
            }
            $size = (Get-Item -LiteralPath $outputPath).Length
            if ($size -lt 1MB) { Write-Fail "API binary $outputPath is suspiciously small ($size bytes)." }
            Write-Host (" {0} ({1})" -f (Format-Bytes $size), (Format-Duration $stopwatch.Elapsed.TotalSeconds)) -ForegroundColor Green
        }
    } finally {
        $env:GOOS = $savedGoos
        $env:GOARCH = $savedGoarch
        $env:CGO_ENABLED = $savedCgo
    }
}

# ---------------------------------------------------------------------------
# Step 2: web standalone build + symlink-free staging.
# ---------------------------------------------------------------------------
function Materialize-WebNodeModules {
    param([string]$SourceStandalone, [string]$DestinationWeb)
    $sourceNm = Join-Path $SourceStandalone "node_modules"
    if (-not (Test-Path -LiteralPath $sourceNm -PathType Container)) {
        Write-Fail "standalone output has no node_modules: $sourceNm"
    }
    $destinationNm = Join-Path $DestinationWeb "node_modules"
    New-Item -ItemType Directory -Force -Path $destinationNm | Out-Null
    $pnpm = Join-Path $sourceNm ".pnpm"

    if (-not (Test-Path -LiteralPath $pnpm -PathType Container)) {
        Write-Info "No pnpm store in standalone node_modules; copying it as-is and pruning sharp/@img."
        Copy-DirContent -Source $sourceNm -Destination $destinationNm
        foreach ($relative in @("sharp", "@img")) {
            $path = Join-Path $destinationNm $relative
            if (Test-Path -LiteralPath $path) {
                Remove-TreeSafe -Path $path
                Write-Info "Pruned web/node_modules/$relative"
            }
        }
    } else {
        # Flatten the pnpm store into a plain node_modules. Next's standalone
        # output uses junctions into .pnpm on pnpm projects; junctions do not
        # survive zip/tar round-trips reliably, so rebuild a flat tree.
        Write-Info "Flattening the pnpm store into a symlink-free web/node_modules ..."
        $nestedDirs = New-Object System.Collections.ArrayList
        foreach ($packageDir in @(Get-ChildItem -LiteralPath $pnpm -Directory -Force)) {
            $candidate = Join-Path $packageDir.FullName "node_modules"
            if (Test-Path -LiteralPath $candidate -PathType Container) { [void]$nestedDirs.Add($candidate) }
        }
        $hiddenStore = Join-Path $pnpm "node_modules"
        if (Test-Path -LiteralPath $hiddenStore -PathType Container) { [void]$nestedDirs.Add($hiddenStore) }

        $seen = @{}
        $copied = New-Object System.Collections.ArrayList
        $pruned = New-Object System.Collections.ArrayList
        foreach ($nested in $nestedDirs) {
            foreach ($top in @(Get-ChildItem -LiteralPath $nested -Force)) {
                $pairs = @()
                if ($top.Name.StartsWith("@")) {
                    if ($top.PSIsContainer) {
                        foreach ($child in @(Get-ChildItem -LiteralPath $top.FullName -Force)) {
                            $pairs += ,@($top.Name, $child)
                        }
                    }
                } else {
                    $pairs += ,@("", $top)
                }
                foreach ($pair in $pairs) {
                    $scope = [string]$pair[0]
                    $packageEntry = [System.IO.FileSystemInfo]$pair[1]
                    if ($scope.Length -gt 0) { $relativeName = $scope + "/" + $packageEntry.Name } else { $relativeName = $packageEntry.Name }
                    if ($relativeName -eq "sharp" -or $relativeName -like "@img/*") {
                        [void]$pruned.Add($relativeName)
                        continue
                    }
                    $realPath = Resolve-RealPath -Path $packageEntry.FullName
                    $realItem = Get-Item -LiteralPath $realPath -Force
                    if (-not $realItem.PSIsContainer) { continue }
                    if ($seen.ContainsKey($relativeName)) {
                        if ($seen[$relativeName] -eq $realPath) { continue }
                        $oldVersion = ""
                        $newVersion = ""
                        try { $oldVersion = (Get-Content -LiteralPath (Join-Path $seen[$relativeName] "package.json") -Raw | ConvertFrom-Json).version } catch { }
                        try { $newVersion = (Get-Content -LiteralPath (Join-Path $realPath "package.json") -Raw | ConvertFrom-Json).version } catch { }
                        if ($oldVersion -eq $newVersion -and $oldVersion.Length -gt 0) { continue }
                        Write-Fail "The pnpm store contains two versions of the package '$relativeName' ($oldVersion vs $newVersion). A flat node_modules cannot represent both; extend the staging step for this case."
                    }
                    $seen[$relativeName] = $realPath
                    $destination = Join-Path $destinationNm $relativeName
                    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
                    Copy-Item -LiteralPath $realPath -Destination $destination -Recurse -Force
                    [void]$copied.Add($relativeName)
                }
            }
        }
        Write-Info ("Copied {0} packages into web/node_modules (pruned: {1})" -f $copied.Count, (($pruned | Sort-Object -Unique) -join ", "))
    }

    # Verify the trace is pure JS and self-contained.
    $links = @(Get-ChildItem -LiteralPath $destinationNm -Recurse -Force -ErrorAction SilentlyContinue | Where-Object { $_.Attributes -band [System.IO.FileAttributes]::ReparsePoint })
    if ($links.Count -gt 0) {
        Write-Fail ("web/node_modules still contains {0} junction(s)/symlink(s) (e.g. {1}). The package must be symlink-free." -f $links.Count, $links[0].FullName)
    }
    $native = @(Get-ChildItem -LiteralPath $destinationNm -Recurse -File -Force -ErrorAction SilentlyContinue | Where-Object { $_.Extension -in @(".node", ".dll", ".so", ".dylib", ".exe") })
    if ($native.Count -gt 0) {
        $offenders = ($native | Select-Object -First 10 | ForEach-Object { $_.FullName.Replace($destinationNm, "web/node_modules") }) -join ", "
        Write-Fail @"
Platform-native binaries remain in web/node_modules: $offenders
The web bundle must be OS-neutral. sharp/@img are pruned because the application has no next/image usage;
if a new native module appears, verify it is not needed at runtime, extend the prune list in Materialize-WebNodeModules,
and re-run the Windows + Linux smoke tests before publishing.
"@
    }
    foreach ($required in @("next", "react", "react-dom")) {
        $packageJson = Join-Path $destinationNm (Join-Path $required "package.json")
        if (-not (Test-Path -LiteralPath $packageJson -PathType Leaf)) {
            Write-Fail "web/node_modules is missing required package '$required' ($packageJson)."
        }
    }
}

function Build-WebBundle {
    Write-Step "Next.js standalone web bundle"
    $frontendDir = Join-Path $script:RepoRoot "frontend"
    $standalone = Join-Path $frontendDir ".next\standalone"
    $staticDir = Join-Path $frontendDir ".next\static"

    if (-not $SkipWebBuild) {
        Write-Info "Running pnpm build with NEXT_PUBLIC_API_BASE_URL=$ApiBaseUrl"
        $savedBaseUrl = $env:NEXT_PUBLIC_API_BASE_URL
        $savedTelemetry = $env:NEXT_TELEMETRY_DISABLED
        $env:NEXT_PUBLIC_API_BASE_URL = $ApiBaseUrl
        $env:NEXT_TELEMETRY_DISABLED = "1"
        try {
            $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
            $buildStdout = Join-Path $script:WorkRoot "logs\web-build.log"
            $buildStderr = Join-Path $script:WorkRoot "logs\web-build.err.log"
            $exitCode = Invoke-ProcessWithLog -FilePath $script:PnpmExe -Arguments @("build") `
                -StdOutPath $buildStdout -StdErrPath $buildStderr -WorkingDirectory $frontendDir
            $stopwatch.Stop()
            if ($exitCode -ne 0) {
                Write-Fail "pnpm build failed in frontend/ (exit $exitCode). See $buildStdout and $buildStderr; if the default Turbopack build is the problem, retry with: pnpm exec next build --webpack"
            }
            Write-Info ("pnpm build finished in {0}" -f (Format-Duration $stopwatch.Elapsed.TotalSeconds))
            if (-not (Test-Path -LiteralPath (Join-Path $standalone "server.js") -PathType Leaf)) {
                Write-Warn "standalone/server.js is missing after the default build; retrying once with --webpack."
                $exitCode = Invoke-ProcessWithLog -FilePath $script:PnpmExe -Arguments @("exec", "next", "build", "--webpack") `
                    -StdOutPath $buildStdout -StdErrPath $buildStderr -WorkingDirectory $frontendDir
                if ($exitCode -ne 0 -or -not (Test-Path -LiteralPath (Join-Path $standalone "server.js") -PathType Leaf)) {
                    Write-Fail "The Next.js standalone build did not produce frontend/.next/standalone/server.js (also with --webpack). See $buildStdout."
                }
            }
        } finally {
            $env:NEXT_PUBLIC_API_BASE_URL = $savedBaseUrl
            $env:NEXT_TELEMETRY_DISABLED = $savedTelemetry
        }
    } else {
        Write-Info "-SkipWebBuild: not running pnpm build."
    }

    if (-not (Test-Path -LiteralPath $standalone -PathType Container)) {
        Write-Fail "frontend/.next/standalone does not exist. Run without -SkipWebBuild (after coordinating the web build) first."
    }
    if (-not (Test-Path -LiteralPath (Join-Path $standalone "server.js") -PathType Leaf)) {
        Write-Fail "frontend/.next/standalone/server.js is missing; the standalone build is incomplete."
    }
    if (-not (Test-Path -LiteralPath $staticDir -PathType Container)) {
        Write-Fail "frontend/.next/static is missing; web pages would 404 their assets."
    }
    $requiredServerFiles = Join-Path $standalone ".next\required-server-files.json"
    if (-not (Test-Path -LiteralPath $requiredServerFiles -PathType Leaf)) {
        Write-Fail "frontend/.next/standalone/.next/required-server-files.json is missing; the standalone output is incomplete."
    }

    $webDest = Join-Path $script:WorkRoot "payload\web"
    Remove-TreeSafe -Path $webDest
    New-Item -ItemType Directory -Force -Path $webDest | Out-Null
    Write-Info "Copying standalone output (without node_modules) ..."
    foreach ($entry in @(Get-ChildItem -LiteralPath $standalone -Force)) {
        if ($entry.Name -eq "node_modules") { continue }
        $target = Join-Path $webDest $entry.Name
        if ($entry.PSIsContainer) {
            Copy-Item -LiteralPath $entry.FullName -Destination $target -Recurse -Force
        } else {
            Copy-Item -LiteralPath $entry.FullName -Destination $target -Force
        }
    }
    Materialize-WebNodeModules -SourceStandalone $standalone -DestinationWeb $webDest

    $webStatic = Join-Path $webDest ".next\static"
    Copy-Item -LiteralPath $staticDir -Destination $webStatic -Recurse -Force
    $publicDir = Join-Path $frontendDir "public"
    if (Test-Path -LiteralPath $publicDir -PathType Container) {
        Copy-Item -LiteralPath $publicDir -Destination (Join-Path $webDest "public") -Recurse -Force
        Write-Info "Copied frontend/public/ (web/public)"
    } else {
        Write-Info "frontend/public/ does not exist; skipped."
    }

    foreach ($relative in @("server.js", "package.json", ".next\required-server-files.json", "node_modules\next\package.json")) {
        if (-not (Test-Path -LiteralPath (Join-Path $webDest $relative))) {
            Write-Fail "Staged web bundle is missing web/$relative."
        }
    }
    $staticFiles = @(Get-ChildItem -LiteralPath $webStatic -Recurse -File -Force -ErrorAction SilentlyContinue)
    if ($staticFiles.Count -eq 0) { Write-Fail "web/.next/static is empty; pages would 404 their assets." }
    Write-Info ("Web bundle: {0} ({1:N0} static files)" -f (Format-Bytes (Get-DirectorySizeBytes -Path $webDest)), $staticFiles.Count)
}

# ---------------------------------------------------------------------------
# Step 3: executor staging (production, hoisted, symlink-free node_modules).
# ---------------------------------------------------------------------------
function Build-ExecutorStage {
    Write-Step "Executor staging (production, hoisted node_modules)"
    $executorSrc = Join-Path $script:RepoRoot "executor"
    $stage = Join-Path $script:WorkRoot "payload\executor"
    Remove-TreeSafe -Path $stage
    New-Item -ItemType Directory -Force -Path $stage | Out-Null
    foreach ($file in @("package.json", "pnpm-lock.yaml")) {
        Copy-Item -LiteralPath (Join-Path $executorSrc $file) -Destination (Join-Path $stage $file) -Force
    }
    Copy-Item -LiteralPath (Join-Path $executorSrc "src") -Destination (Join-Path $stage "src") -Recurse -Force

    $logPath = Join-Path $script:WorkRoot "logs\executor-install.log"
    $savedSkipDownload = $env:PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD
    $env:PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1"
    $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        Write-Info "pnpm install --prod --frozen-lockfile --node-linker=hoisted"
        $exitCode = Invoke-ProcessWithLog -FilePath $script:PnpmExe `
            -Arguments @("install", "--prod", "--frozen-lockfile", "--node-linker=hoisted") `
            -StdOutPath $logPath -StdErrPath ($logPath + ".err") -WorkingDirectory $stage
    } finally {
        $env:PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = $savedSkipDownload
    }
    $stopwatch.Stop()
    if ($exitCode -ne 0) {
        Write-Fail "pnpm install failed in the executor staging directory (exit $exitCode). See $logPath."
    }
    Write-Info ("pnpm install finished in {0}" -f (Format-Duration $stopwatch.Elapsed.TotalSeconds))

    $nodeModules = Join-Path $stage "node_modules"
    if (-not (Test-Path -LiteralPath $nodeModules -PathType Container)) {
        Write-Fail "pnpm install did not create executor/node_modules."
    }
    $files = @(Get-ChildItem -LiteralPath $nodeModules -Recurse -File -Force -ErrorAction SilentlyContinue)
    $links = @(Get-ChildItem -LiteralPath $nodeModules -Recurse -Force -ErrorAction SilentlyContinue | Where-Object { $_.Attributes -band [System.IO.FileAttributes]::ReparsePoint })
    if ($links.Count -gt 0) {
        Write-Fail ("The executor node_modules contains {0} symlink(s)/junction(s) (e.g. {1}); the package must be symlink-free." -f $links.Count, $links[0].FullName)
    }
    $native = @($files | Where-Object { $_.Extension -in @(".node", ".dll", ".so", ".dylib") })
    if ($native.Count -gt 0) {
        Write-Fail ("The executor node_modules contains platform-native binaries (e.g. {0}); the staged executor must be pure JavaScript." -f $native[0].FullName)
    }
    $playwrightPackageJson = Join-Path $nodeModules "playwright\package.json"
    if (-not (Test-Path -LiteralPath $playwrightPackageJson -PathType Leaf)) {
        Write-Fail "playwright is missing from the staged executor node_modules."
    }
    $installedPlaywright = (Get-Content -LiteralPath $playwrightPackageJson -Raw | ConvertFrom-Json).version
    if ($installedPlaywright -ne $script:PinnedPlaywrightVersion) {
        Write-Fail "Expected playwright $($script:PinnedPlaywrightVersion) in the executor, found '$installedPlaywright'."
    }
    $size = ($files | Measure-Object -Property Length -Sum).Sum
    Write-Info ("Executor staged: {0} files, {1}, playwright {2}" -f $files.Count, (Format-Bytes ([long]$size)), $installedPlaywright)
}

# ---------------------------------------------------------------------------
# Step 3b: PDF renderer staging (production, hoisted, symlink-free node_modules).
# ---------------------------------------------------------------------------
function Build-RendererStage {
    Write-Step "PDF renderer staging (production, hoisted node_modules)"
    $rendererSrc = Join-Path $script:RepoRoot "renderer"
    $stage = Join-Path $script:WorkRoot "payload\renderer"
    Remove-TreeSafe -Path $stage
    New-Item -ItemType Directory -Force -Path $stage | Out-Null
    foreach ($file in @("package.json", "pnpm-lock.yaml")) {
        Copy-Item -LiteralPath (Join-Path $rendererSrc $file) -Destination (Join-Path $stage $file) -Force
    }
    Copy-Item -LiteralPath (Join-Path $rendererSrc "src") -Destination (Join-Path $stage "src") -Recurse -Force

    $logPath = Join-Path $script:WorkRoot "logs\renderer-install.log"
    $savedSkipDownload = $env:PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD
    $env:PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1"
    $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        Write-Info "pnpm install --prod --frozen-lockfile --node-linker=hoisted"
        $exitCode = Invoke-ProcessWithLog -FilePath $script:PnpmExe `
            -Arguments @("install", "--prod", "--frozen-lockfile", "--node-linker=hoisted") `
            -StdOutPath $logPath -StdErrPath ($logPath + ".err") -WorkingDirectory $stage
    } finally {
        $env:PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = $savedSkipDownload
    }
    $stopwatch.Stop()
    if ($exitCode -ne 0) {
        Write-Fail "pnpm install failed in the renderer staging directory (exit $exitCode). See $logPath."
    }
    Write-Info ("pnpm install finished in {0}" -f (Format-Duration $stopwatch.Elapsed.TotalSeconds))

    $nodeModules = Join-Path $stage "node_modules"
    if (-not (Test-Path -LiteralPath $nodeModules -PathType Container)) {
        Write-Fail "pnpm install did not create renderer/node_modules."
    }
    $files = @(Get-ChildItem -LiteralPath $nodeModules -Recurse -File -Force -ErrorAction SilentlyContinue)
    $links = @(Get-ChildItem -LiteralPath $nodeModules -Recurse -Force -ErrorAction SilentlyContinue | Where-Object { $_.Attributes -band [System.IO.FileAttributes]::ReparsePoint })
    if ($links.Count -gt 0) {
        Write-Fail ("The renderer node_modules contains {0} symlink(s)/junction(s) (e.g. {1}); the package must be symlink-free." -f $links.Count, $links[0].FullName)
    }
    $native = @($files | Where-Object { $_.Extension -in @(".node", ".dll", ".so", ".dylib") })
    if ($native.Count -gt 0) {
        Write-Fail ("The renderer node_modules contains platform-native binaries (e.g. {0}); the staged renderer must be pure JavaScript." -f $native[0].FullName)
    }
    $playwrightPackageJson = Join-Path $nodeModules "playwright\package.json"
    if (-not (Test-Path -LiteralPath $playwrightPackageJson -PathType Leaf)) {
        Write-Fail "playwright is missing from the staged renderer node_modules."
    }
    $installedPlaywright = (Get-Content -LiteralPath $playwrightPackageJson -Raw | ConvertFrom-Json).version
    if ($installedPlaywright -ne $script:PinnedPlaywrightVersion) {
        Write-Fail "Expected playwright $($script:PinnedPlaywrightVersion) in the renderer, found '$installedPlaywright'."
    }
    $size = ($files | Measure-Object -Property Length -Sum).Sum
    Write-Info ("Renderer staged: {0} files, {1}, playwright {2}" -f $files.Count, (Format-Bytes ([long]$size)), $installedPlaywright)
}

# ---------------------------------------------------------------------------
# Shared download cache.
# ---------------------------------------------------------------------------
function Get-CachedDownload {
    param(
        [string[]]$Urls,
        [string]$CachePath,
        [long]$ExpectedBytes,
        [string]$Label
    )
    if (Test-Path -LiteralPath $CachePath -PathType Leaf) {
        $existingSize = (Get-Item -LiteralPath $CachePath).Length
        if ($ExpectedBytes -le 0 -or $existingSize -eq $ExpectedBytes) {
            Write-Info ("{0}: using cached download ({1})" -f $Label, (Format-Bytes $existingSize))
            return $CachePath
        }
        Write-Warn ("{0}: cached file has {1} bytes, expected {2}; re-downloading" -f $Label, $existingSize, $ExpectedBytes)
        Remove-Item -LiteralPath $CachePath -Force
    }
    if (-not (Test-Path -LiteralPath $script:CacheDir -PathType Container)) {
        New-Item -ItemType Directory -Force -Path $script:CacheDir | Out-Null
    }
    $temporaryPath = $CachePath + ".partial"
    $lastError = ""
    foreach ($url in $Urls) {
        Write-Info ("{0}: downloading {1}" -f $Label, $url)
        $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
        try {
            if (Test-Path -LiteralPath $temporaryPath) { Remove-Item -LiteralPath $temporaryPath -Force }
            Invoke-WebRequest -Uri $url -OutFile $temporaryPath -UseBasicParsing -TimeoutSec 1800
            $stopwatch.Stop()
            $size = (Get-Item -LiteralPath $temporaryPath).Length
            if ($ExpectedBytes -gt 0 -and $size -ne $ExpectedBytes) {
                throw "downloaded $size bytes, expected $ExpectedBytes"
            }
            Move-Item -LiteralPath $temporaryPath -Destination $CachePath -Force
            Write-Info ("{0}: downloaded {1} in {2}" -f $Label, (Format-Bytes $size), (Format-Duration $stopwatch.Elapsed.TotalSeconds))
            return $CachePath
        } catch {
            $stopwatch.Stop()
            $lastError = $_.Exception.Message
            Write-Warn ("{0}: download failed from {1}: {2}" -f $Label, $url, $lastError)
            if (Test-Path -LiteralPath $temporaryPath) { Remove-Item -LiteralPath $temporaryPath -Force -ErrorAction SilentlyContinue }
        }
    }
    Write-Fail @"
Could not download $Label.
Tried:
  $($Urls -join "`n  ")
Last error: $lastError
Downloads are cached in $($script:CacheDir). Copy the file there manually (keep the expected byte size $ExpectedBytes) or fix proxy/network access and retry.
"@
}

function Expand-ZipTo {
    param([string]$ZipPath, [string]$Destination, [string]$Label)
    if (-not (Test-Path -LiteralPath $Destination -PathType Container)) {
        New-Item -ItemType Directory -Force -Path $Destination | Out-Null
    }
    $tar = Get-Command tar.exe -ErrorAction SilentlyContinue
    if ($null -ne $tar) {
        $result = Invoke-ProcessCapture -FilePath $tar.Source -Arguments @("-xf", $ZipPath, "-C", $Destination)
        if ($result.ExitCode -ne 0) {
            Write-Fail ("tar.exe could not extract {0} ({1}) to {2}:{3}{4}" -f $Label, $ZipPath, $Destination, [Environment]::NewLine, (($result.Output | Select-Object -First 10) -join [Environment]::NewLine))
        }
    } else {
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        [System.IO.Compression.ZipFile]::ExtractToDirectory($ZipPath, $Destination)
    }
}

# ---------------------------------------------------------------------------
# Step 4: pinned Playwright browsers.
# ---------------------------------------------------------------------------
function Get-BrowserAssets {
    $revision = $script:PinnedChromiumRevision
    $ffmpegRevision = $script:PinnedFfmpegRevision
    $chromiumVersion = $script:PinnedChromiumVersion
    $cftSuffixes = @(
        "builds/cft/$chromiumVersion",
        "dbazure/download/playwright/builds/cft/$chromiumVersion",
        "dbazure/download/playwright/builds/cft/$chromiumVersion"
    )
    $cftBases = @(
        "https://cdn.playwright.dev",
        "https://cdn.playwright.dev",
        "https://playwright.download.prss.microsoft.com"
    )
    $ffmpegUrls = @{
        "windows" = @(
            "https://cdn.playwright.dev/dbazure/download/playwright/builds/ffmpeg/$ffmpegRevision/ffmpeg-win64.zip",
            "https://cdn.playwright.dev/builds/ffmpeg/$ffmpegRevision/ffmpeg-win64.zip",
            "https://playwright.download.prss.microsoft.com/dbazure/download/playwright/builds/ffmpeg/$ffmpegRevision/ffmpeg-win64.zip"
        )
        "linux" = @(
            "https://cdn.playwright.dev/dbazure/download/playwright/builds/ffmpeg/$ffmpegRevision/ffmpeg-linux.zip",
            "https://cdn.playwright.dev/builds/ffmpeg/$ffmpegRevision/ffmpeg-linux.zip",
            "https://playwright.download.prss.microsoft.com/dbazure/download/playwright/builds/ffmpeg/$ffmpegRevision/ffmpeg-linux.zip"
        )
    }

    $assets = @()
    foreach ($os in @("windows", "linux")) {
        $osDirectory = if ($os -eq "windows") { "win64" } else { "linux64" }
        $chromeZip = if ($os -eq "windows") { "chrome-win64.zip" } else { "chrome-linux64.zip" }
        $shellZip = if ($os -eq "windows") { "chrome-headless-shell-win64.zip" } else { "chrome-headless-shell-linux64.zip" }
        $chromeExe = if ($os -eq "windows") { "chrome-win64/chrome.exe" } else { "chrome-linux64/chrome" }
        $shellExe = if ($os -eq "windows") { "chrome-headless-shell-win64/chrome-headless-shell.exe" } else { "chrome-headless-shell-linux64/chrome-headless-shell" }
        $chromiumUrls = @()
        for ($index = 0; $index -lt $cftBases.Length; $index++) {
            $chromiumUrls += ("{0}/{1}/{2}/{3}" -f $cftBases[$index], $cftSuffixes[$index], $osDirectory, $chromeZip)
        }
        $shellUrls = @()
        for ($index = 0; $index -lt $cftBases.Length; $index++) {
            $shellUrls += ("{0}/{1}/{2}/{3}" -f $cftBases[$index], $cftSuffixes[$index], $osDirectory, $shellZip)
        }
        $assets += [pscustomobject]@{
            Os           = $os
            Name         = "chromium-$revision"
            Label        = "Chromium $chromiumVersion (revision $revision) [$os]"
            CacheFile    = "chromium-$revision-$os.zip"
            Urls         = $chromiumUrls
            Expected     = [long]$(if ($os -eq "windows") { 207073013 } else { 200345244 })
            RequiredFile = $chromeExe
            SlimSkip     = $true
        }
        $assets += [pscustomobject]@{
            Os           = $os
            Name         = "chromium_headless_shell-$revision"
            Label        = "Chromium headless shell (revision $revision) [$os]"
            CacheFile    = "chromium-headless-shell-$revision-$os.zip"
            Urls         = $shellUrls
            Expected     = [long]$(if ($os -eq "windows") { 123826519 } else { 125797539 })
            RequiredFile = $shellExe
            SlimSkip     = $false
        }
        $assets += [pscustomobject]@{
            Os           = $os
            Name         = "ffmpeg-$ffmpegRevision"
            Label        = "FFmpeg (revision $ffmpegRevision) [$os]"
            CacheFile    = "ffmpeg-$ffmpegRevision-$os.zip"
            Urls         = $ffmpegUrls[$os]
            Expected     = [long]$(if ($os -eq "windows") { 1663577 } else { 2302701 })
            RequiredFile = $(if ($os -eq "windows") { "ffmpeg-win64.exe" } else { "ffmpeg-linux" })
            SlimSkip     = $false
        }
    }
    return $assets
}

function Stage-Browsers {
    Write-Step "Playwright browsers (Chromium $($script:PinnedChromiumVersion) / headless shell / FFmpeg $($script:PinnedFfmpegRevision))"
    $assets = Get-BrowserAssets
    $root = Join-Path $script:WorkRoot "payload\browsers"
    $cache = $script:CacheDir
    foreach ($asset in $assets) {
        if ($SlimBrowsers -and $asset.SlimSkip) {
            Write-Info ("{0}: skipped (-SlimBrowsers)" -f $asset.Label)
            continue
        }
        $cachePath = Join-Path $cache $asset.CacheFile
        $zipPath = Get-CachedDownload -Urls $asset.Urls -CachePath $cachePath -ExpectedBytes $asset.Expected -Label $asset.Label
        $target = Join-Path (Join-Path $root $asset.Os) $asset.Name
        Remove-TreeSafe -Path $target
        New-Item -ItemType Directory -Force -Path $target | Out-Null
        $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
        Expand-ZipTo -ZipPath $zipPath -Destination $target -Label $asset.Label
        $stopwatch.Stop()
        $required = Join-Path $target ($asset.RequiredFile -replace '/', '\')
        if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
            Write-Fail ("{0}: extraction finished but the expected file is missing: {1}" -f $asset.Label, $required)
        }
        New-Item -ItemType File -Path (Join-Path $target "INSTALLATION_COMPLETE") -Force | Out-Null
        $size = Get-DirectorySizeBytes -Path $target
        Write-Info ("{0}: extracted to {1} in {2}" -f $asset.Label, (Format-Bytes $size), (Format-Duration $stopwatch.Elapsed.TotalSeconds))
    }
}

# ---------------------------------------------------------------------------
# Step 5: Node.js runtime.
# ---------------------------------------------------------------------------
function Get-NodeRuntimeAssets {
    $version = $script:PinnedNodeVersion
    return @(
        [pscustomobject]@{
            Os        = "windows"
            Label     = "Node.js $version (win-x64 zip)"
            CacheFile = "node-v$version-win-x64.zip"
            Urls      = @("https://nodejs.org/dist/v$version/node-v$version-win-x64.zip")
            Expected  = [long]35604229
            Type      = "zip"
        },
        [pscustomobject]@{
            Os        = "linux"
            Label     = "Node.js $version (linux-x64 tar.xz)"
            CacheFile = "node-v$version-linux-x64.tar.xz"
            Urls      = @("https://nodejs.org/dist/v$version/node-v$version-linux-x64.tar.xz")
            Expected  = [long]31029996
            Type      = "tarxz"
        }
    )
}

function Get-NodeRuntimeShasums {
    $cachePath = Join-Path $script:CacheDir "SHASUMS256.txt"
    try {
        $urls = @("https://nodejs.org/dist/v$($script:PinnedNodeVersion)/SHASUMS256.txt")
        [void](Get-CachedDownload -Urls $urls -CachePath $cachePath -ExpectedBytes 0 -Label "Node.js SHASUMS256.txt")
        return $cachePath
    } catch {
        Write-Warn "Could not download the Node.js SHASUMS256.txt; the runtime archive integrity cannot be verified."
        return ""
    }
}

function Stage-NodeRuntime {
    Write-Step "Node.js runtime ($($script:PinnedNodeVersion) LTS, per OS)"
    $assets = Get-NodeRuntimeAssets
    $shasumsPath = Get-NodeRuntimeShasums
    $root = Join-Path $script:WorkRoot "payload\node"
    foreach ($asset in $assets) {
        $cachePath = Join-Path $script:CacheDir $asset.CacheFile
        $archivePath = Get-CachedDownload -Urls $asset.Urls -CachePath $cachePath -ExpectedBytes $asset.Expected -Label $asset.Label
        if ($shasumsPath.Length -gt 0) {
            $expectedHash = ""
            foreach ($line in Get-Content -LiteralPath $shasumsPath) {
                if ($line -match "^([0-9a-fA-F]{64})\s+\*?$([regex]::Escape($asset.CacheFile))$") {
                    $expectedHash = $matches[1].ToLowerInvariant()
                    break
                }
            }
            if ($expectedHash.Length -eq 0) {
                Write-Warn ("{0}: filename not found in SHASUMS256.txt; skipping digest verification." -f $asset.Label)
            } else {
                $actualHash = Get-FileSha256 -Path $archivePath
                if ($actualHash -ne $expectedHash) {
                    Remove-Item -LiteralPath $archivePath -Force
                    Write-Fail ("{0}: SHA-256 mismatch (expected {1}, got {2}). Deleted the cached archive; retry the build." -f $asset.Label, $expectedHash, $actualHash)
                }
                Write-Info ("{0}: SHA-256 verified" -f $asset.Label)
            }
        }
        $target = Join-Path $root $asset.Os
        Remove-TreeSafe -Path $target
        New-Item -ItemType Directory -Force -Path $target | Out-Null
        $topDirectory = "node-v$($script:PinnedNodeVersion)"
        if ($asset.Type -eq "zip") {
            $temporary = Join-Path $script:WorkRoot ("tmp\node-" + $asset.Os)
            Remove-TreeSafe -Path $temporary
            New-Item -ItemType Directory -Force -Path $temporary | Out-Null
            Expand-ZipTo -ZipPath $archivePath -Destination $temporary -Label $asset.Label
            $extracted = Join-Path $temporary "$topDirectory-win-x64"
            if (-not (Test-Path -LiteralPath (Join-Path $extracted "node.exe") -PathType Leaf)) {
                Write-Fail "The Node.js win-x64 archive did not contain node.exe (unexpected layout)."
            }
            Copy-Item -LiteralPath (Join-Path $extracted "node.exe") -Destination (Join-Path $target "node.exe") -Force
            Copy-Item -LiteralPath (Join-Path $extracted "LICENSE") -Destination (Join-Path $target "LICENSE") -Force
            Remove-TreeSafe -Path $temporary
            $nodePath = Join-Path $target "node.exe"
        } else {
            $tar = Get-Command tar.exe -ErrorAction SilentlyContinue
            if ($null -eq $tar) {
                Write-Fail "tar.exe (bsdtar, built into Windows 10+) is required to extract the Linux Node.js .tar.xz runtime."
            }
            $memberNode = "$topDirectory-linux-x64/bin/node"
            $memberLicense = "$topDirectory-linux-x64/LICENSE"
            $result = Invoke-ProcessCapture -FilePath $tar.Source -Arguments @("-xJf", $archivePath, "-C", $target, "--strip-components=1", $memberNode, $memberLicense)
            if ($result.ExitCode -ne 0) {
                Write-Fail ("tar.exe could not extract bin/node + LICENSE from {0}:{1}{2}" -f $archivePath, [Environment]::NewLine, (($result.Output | Select-Object -First 10) -join [Environment]::NewLine))
            }
            $nodePath = Join-Path $target "bin\node"
        }
        if (-not (Test-Path -LiteralPath $nodePath -PathType Leaf)) {
            Write-Fail ("{0}: extracted runtime is missing {1}" -f $asset.Label, $nodePath)
        }
        if (-not (Test-Path -LiteralPath (Join-Path $target "LICENSE") -PathType Leaf)) {
            Write-Fail ("{0}: the Node.js LICENSE file was not extracted." -f $asset.Label)
        }
        $nodeSize = (Get-Item -LiteralPath $nodePath).Length
        Write-Info ("{0}: node binary {1}" -f $asset.Label, (Format-Bytes $nodeSize))
    }
}

# ---------------------------------------------------------------------------
# Step 6: package assembly.
# ---------------------------------------------------------------------------
function Get-PackageName {
    param([string]$OsName)
    return "eztest-$Version-$OsName-x64"
}

function Get-GoModuleNotices {
    $backendDir = Join-Path $script:RepoRoot "backend"
    try {
        $result = Invoke-ProcessCapture -FilePath $script:GoExe -Arguments @("list", "-m", "all") -WorkingDirectory $backendDir
        $lines = @($result.StdOut)
        $exitCode = $result.ExitCode
        if ($exitCode -ne 0 -or $lines.Count -eq 0) {
            return "- The Go module dependency list could not be generated (``go list -m all`` failed at build time); see the repository's ``backend/go.mod`` and ``go.sum``."
        }
        $rendered = New-Object System.Collections.ArrayList
        foreach ($line in $lines) {
            $trimmed = ("$line").Trim()
            if ($trimmed.Length -eq 0) { continue }
            if ($trimmed -eq $script:GoModulePath -or $trimmed -like "$($script:GoModulePath) *") { continue }
            [void]$rendered.Add("- " + $trimmed)
        }
        if ($rendered.Count -eq 0) {
            return "- Only the Go standard library (BSD-3-Clause) is linked by the API binary."
        }
        return ($rendered -join "`n")
    } catch {
        return "- The Go module dependency list could not be generated at build time; see the repository's ``backend/go.mod`` and ``go.sum``."
    }
}

function Get-TemplateTokens {
    param([string]$OsName)
    $tokens = @{
        "__VERSION__"             = $Version
        "__NODE_VERSION__"        = $script:PinnedNodeVersion
        "__PLAYWRIGHT_VERSION__"  = $script:PinnedPlaywrightVersion
        "__CHROMIUM_VERSION__"    = $script:PinnedChromiumVersion
        "__CHROMIUM_REVISION__"   = $script:PinnedChromiumRevision
        "__FFMPEG_REVISION__"     = $script:PinnedFfmpegRevision
        "__API_BASE_URL__"        = $ApiBaseUrl
        "__PLATFORM__"            = $OsName
        "__COMMIT__"              = $script:Commit
    }
    return $tokens
}

function Copy-PackageScripts {
    param([string]$PackageRoot)
    $sourceDir = Join-Path $script:RepoRoot "scripts\package"
    foreach ($name in $script:PackageScripts) {
        $source = Join-Path $sourceDir $name
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
            Write-Fail "Missing packaging template: scripts/package/$name"
        }
        $destination = Join-Path $PackageRoot $name
        if ($name -eq "PACKAGE.md" -or $name -eq "THIRD_PARTY_NOTICES.md") {
            $text = Read-TextFileUtf8 -Path $source
            $text = $text -replace "`r`n", "`n"
            $tokens = Get-TemplateTokens -OsName ""
            foreach ($token in $tokens.Keys) {
                $text = $text.Replace($token, [string]$tokens[$token])
            }
            if ($name -eq "THIRD_PARTY_NOTICES.md") {
                $text = $text.Replace("<!-- GO_MODULES -->", (Get-GoModuleNotices))
            }
            Write-FileUtf8NoBom -Path $destination -Text $text
        } elseif ($name.EndsWith(".sh")) {
            # Git on Windows can check these out with CRLF; bash would fail on
            # the shebang/logic (`set -u` errors), so normalize to LF.
            $text = Read-TextFileUtf8 -Path $source
            $text = $text -replace "`r`n", "`n"
            Write-FileUtf8NoBom -Path $destination -Text $text
        } else {
            Copy-Item -LiteralPath $source -Destination $destination -Force
        }
    }
}

function Write-Manifest {
    param([string]$PackageRoot, [string]$OsName)
    $postgresBundled = ($BundlePostgres -and $OsName -eq "windows")
    $manifest = [ordered]@{
        version         = $Version
        commit          = $script:Commit
        built_at        = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
        platform        = $OsName
        arch            = "x64"
        node            = [ordered]@{ version = $script:PinnedNodeVersion }
        playwright      = [ordered]@{
            version            = $script:PinnedPlaywrightVersion
            chromium           = $script:PinnedChromiumVersion
            chromium_revision  = [int]$script:PinnedChromiumRevision
            ffmpeg_revision    = [int]$script:PinnedFfmpegRevision
            slim_browsers      = [bool]$SlimBrowsers
        }
        postgres        = [ordered]@{ required = $true; bundled = [bool]$postgresBundled }
        api             = [ordered]@{ version = $Version; binary = $(if ($OsName -eq "windows") { "bin/eztest.exe" } else { "bin/eztest" }) }
        web             = [ordered]@{ api_base_url = $ApiBaseUrl; standalone = $true }
        renderer        = [ordered]@{ dir = "renderer"; playwright_version = $script:PinnedPlaywrightVersion }
        openapi         = "docs/openapi/api.yaml"
        package_scripts = $script:PackageScripts
    }
    $json = $manifest | ConvertTo-Json -Depth 8
    Write-FileUtf8NoBom -Path (Join-Path $PackageRoot "manifest.json") -Text ($json + "`n")
}

function Add-BundledPostgres {
    param([string]$PackageRoot)
    $source = Join-Path $script:RepoRoot ".tools\pgsql"
    if (-not (Test-Path -LiteralPath $source -PathType Container)) {
        Write-Fail "-BundlePostgres was passed but .tools/pgsql does not exist. Run .tools/setup-toolchain.ps1 first."
    }
    $target = Join-Path $PackageRoot "pgsql"
    New-Item -ItemType Directory -Force -Path $target | Out-Null
    foreach ($name in @("bin", "lib", "share")) {
        $directory = Join-Path $source $name
        if (-not (Test-Path -LiteralPath $directory -PathType Container)) { continue }
        Write-Info "Bundling portable PostgreSQL: pgsql/$name"
        Copy-Item -LiteralPath $directory -Destination (Join-Path $target $name) -Recurse -Force
    }
    $readme = @"
# Portable PostgreSQL 17 (bundled)

This package was built with -BundlePostgres and ships the portable PostgreSQL 17
distribution from the EzTest development toolchain.

Start a cluster on the package's data directory (run from the package root):

    pgsql\bin\initdb.exe -D pgsql\data -U eztest -A trust --locale=C
    pgsql\bin\pg_ctl.exe -D pgsql\data -l pgsql\pg.log -w start
    pgsql\bin\createdb.exe -h 127.0.0.1 -p 5432 -U eztest eztest

Then set EZTEST_DATABASE_URL in .env to
    postgres://eztest@127.0.0.1:5432/eztest?sslmode=disable
and start EzTest. Stop the cluster with:

    pgsql\bin\pg_ctl.exe -D pgsql\data -m fast -w stop

The bundled server is for demo/evaluation use; use a managed PostgreSQL 17
service for anything you care about, and back up the database with
pgsql\bin\pg_dump.exe together with the data\ directory.
"@
    Write-FileUtf8NoBom -Path (Join-Path $target "README-PORTABLE.md") -Text ($readme -replace "`r`n", "`n")
    Write-Info ("Bundled PostgreSQL: {0}" -f (Format-Bytes (Get-DirectorySizeBytes -Path $target)))
}

function Assemble-Package {
    param([string]$OsName)
    Write-Step ("Assembling package for {0}" -f $OsName)
    $packageName = Get-PackageName -OsName $OsName
    $parent = Join-Path $script:WorkRoot ("pkg\" + $OsName)
    Remove-TreeSafe -Path $parent
    $packageRoot = Join-Path $parent $packageName
    New-Item -ItemType Directory -Force -Path $packageRoot | Out-Null
    foreach ($relative in @("bin", "docs\openapi", "web", "executor", "renderer", "playwright-browsers", "runtime\node")) {
        New-Item -ItemType Directory -Force -Path (Join-Path $packageRoot $relative) | Out-Null
    }

    $apiBinary = if ($OsName -eq "windows") { "eztest.exe" } else { "eztest" }
    Copy-Item -LiteralPath (Join-Path (Join-Path $script:WorkRoot "payload\api") (Join-Path $OsName $apiBinary)) -Destination (Join-Path $packageRoot (Join-Path "bin" $apiBinary)) -Force
    Copy-Item -LiteralPath $script:Yaml -Destination (Join-Path $packageRoot "docs\openapi\api.yaml") -Force
    Copy-DirContent -Source (Join-Path $script:WorkRoot "payload\web") -Destination (Join-Path $packageRoot "web")
    Copy-DirContent -Source (Join-Path $script:WorkRoot "payload\executor") -Destination (Join-Path $packageRoot "executor")
    Copy-DirContent -Source (Join-Path $script:WorkRoot "payload\renderer") -Destination (Join-Path $packageRoot "renderer")
    Copy-DirContent -Source (Join-Path (Join-Path $script:WorkRoot "payload\browsers") $OsName) -Destination (Join-Path $packageRoot "playwright-browsers")
    Copy-DirContent -Source (Join-Path (Join-Path $script:WorkRoot "payload\node") $OsName) -Destination (Join-Path $packageRoot "runtime\node")

    Copy-PackageScripts -PackageRoot $packageRoot
    Copy-Item -LiteralPath (Join-Path $script:RepoRoot "LICENSE") -Destination (Join-Path $packageRoot "LICENSE") -Force
    Write-FileUtf8NoBom -Path (Join-Path $packageRoot "VERSION") -Text ($Version + "`n")
    Write-Manifest -PackageRoot $packageRoot -OsName $OsName

    if ($BundlePostgres -and $OsName -eq "windows") {
        Add-BundledPostgres -PackageRoot $packageRoot
    }

    $size = Get-DirectorySizeBytes -Path $packageRoot
    Write-Info ("Package tree: {0} ({1})" -f $packageRoot, (Format-Bytes $size))
    return $packageRoot
}

# ---------------------------------------------------------------------------
# Step 7: sanity checks.
# ---------------------------------------------------------------------------
function Test-PowerShellSyntax {
    param([string]$Path)
    $tokens = $null
    $errors = $null
    [void][System.Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$errors)
    if ($null -ne $errors -and $errors.Count -gt 0) {
        $details = ($errors | Select-Object -First 5 | ForEach-Object { "line $($_.Extent.StartLineNumber): $($_.Message)" }) -join "; "
        return "PowerShell parse error in $([System.IO.Path]::GetFileName($Path)): $details"
    }
    return ""
}

function Test-BashSyntaxBatch {
    param([string[]]$Paths)
    if (-not $script:WslAvailable) { return @() }
    if ($Paths.Count -eq 0) { return @() }
    # Windows PowerShell 5.1 mangles quotes when a whole script is passed via
    # `-c`; run a tiny LF runner file instead and pass plain path arguments.
    $runnerDir = Join-Path $script:WorkRoot "tmp"
    if (-not (Test-Path -LiteralPath $runnerDir -PathType Container)) {
        New-Item -ItemType Directory -Force -Path $runnerDir | Out-Null
    }
    $runnerPath = Join-Path $runnerDir "bash-syntax-check.sh"
    $runnerText = @'
for f in "$@"; do
    bash -n "$f" || { echo "SYNTAX_ERROR: $f"; exit 1; }
done
exit 0
'@
    Write-FileUtf8NoBom -Path $runnerPath -Text ($runnerText -replace "`r`n", "`n")
    $wslRunner = ConvertTo-WslPath -WindowsPath $runnerPath
    $linuxPaths = @()
    foreach ($path in $Paths) { $linuxPaths += (ConvertTo-WslPath -WindowsPath $path) }
    $wsl = Get-Command wsl.exe -ErrorAction SilentlyContinue
    if ($null -eq $wsl) { return @() }
    $result = Invoke-ProcessCapture -FilePath $wsl.Source -Arguments (@("-e", "bash", $wslRunner) + $linuxPaths) -TimeoutMs 300000
    $exitCode = $result.ExitCode
    $problems = @()
    if ($exitCode -ne 0) {
        foreach ($line in @($result.Output)) {
            $text = "$line".Trim()
            if ($text -match 'SYNTAX_ERROR|syntax error|unexpected') { $problems += $text }
        }
        if ($problems.Count -eq 0) {
            $problems += ("bash -n failed with exit code $exitCode (WSL output unavailable; run bash -n manually on the package scripts)")
        }
    }
    return $problems
}

function Test-RepositoryLeak {
    param([string[]]$TextFiles)
    $problems = @()
    $repoPath = $script:RepoRoot
    $repoForward = $repoPath.Replace('\', '/')
    $patterns = @(
        @{ Label = "absolute repository path"; Pattern = [regex]::Escape($repoPath) },
        @{ Label = "absolute repository path (forward slashes)"; Pattern = [regex]::Escape($repoForward) },
        @{ Label = ".tools path"; Pattern = '\.tools[\\/]' },
        @{ Label = "scripts/package path"; Pattern = 'scripts[\\/]package' },
        @{ Label = "WSL repository mount"; Pattern = '/mnt/[a-z]/Project' }
    )
    foreach ($file in $TextFiles) {
        $baseName = [System.IO.Path]::GetFileName($file)
        $text = Read-TextFileUtf8 -Path $file
        foreach ($rule in $patterns) {
            if ($text -match $rule.Pattern) {
                $problems += ("$baseName contains a repository-specific path ({0})" -f $rule.Label)
            }
        }
    }
    return $problems
}

function Get-RepositorySecretValues {
    $envPath = Join-Path $script:RepoRoot ".env"
    $values = @()
    if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
        Write-Warn "No repository .env found; the package secret-leak scan only checks for generic secret shapes."
        return $values
    }
    $secretKeys = @("EZTEST_DATABASE_URL", "EZTEST_TEST_DATABASE_URL", "EZTEST_SECRET_KEYS", "EZTEST_ADMIN_PASSWORD", "EZTEST_EXECUTOR_TOKEN", "POSTGRES_PASSWORD", "llm_key")
    foreach ($line in [System.IO.File]::ReadAllLines($envPath, [System.Text.Encoding]::UTF8)) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith("#")) { continue }
        $index = $trimmed.IndexOf("=")
        if ($index -lt 1) { continue }
        $key = $trimmed.Substring(0, $index).Trim()
        if ($secretKeys -notcontains $key) { continue }
        $value = $trimmed.Substring($index + 1).Trim()
        if ($value.Length -ge 8 -and -not $value.Contains("<")) {
            $values += [pscustomobject]@{ Key = $key; Value = $value }
        }
    }
    return $values
}

function Test-PackageTree {
    param([string]$PackageRoot, [string]$OsName)
    Write-Step ("Sanity checks for {0} package" -f $OsName)
    $packageName = Split-Path -Leaf $PackageRoot
    $problems = New-Object System.Collections.ArrayList
    $apiBinary = if ($OsName -eq "windows") { "bin\eztest.exe" } else { "bin\eztest" }
    $nodeBinary = if ($OsName -eq "windows") { "runtime\node\node.exe" } else { "runtime\node\bin\node" }

    # 1. Expected files.
    $expected = @(
        "VERSION", "manifest.json", "PACKAGE.md", "THIRD_PARTY_NOTICES.md", "LICENSE", ".env.example",
        "docs\openapi\api.yaml", $apiBinary, $nodeBinary, "runtime\node\LICENSE",
        "web\server.js", "web\package.json", "web\.next\required-server-files.json", "web\node_modules\next\package.json",
        "executor\src\server.mjs", "executor\node_modules\playwright\package.json",
        "renderer\src\server.mjs", "renderer\src\render.mjs", "renderer\node_modules\playwright\package.json"
    )
    foreach ($name in $script:PackageScripts) { $expected += $name }
    foreach ($relative in $expected) {
        if (-not (Test-Path -LiteralPath (Join-Path $PackageRoot $relative))) {
            [void]$problems.Add("missing expected file/directory: $relative")
        }
    }
    $versionText = ""
    if (Test-Path -LiteralPath (Join-Path $PackageRoot "VERSION")) {
        $versionText = (Get-Content -LiteralPath (Join-Path $PackageRoot "VERSION") -TotalCount 1).Trim()
        if ($versionText -ne $Version) { [void]$problems.Add("VERSION file says '$versionText', expected '$Version'") }
    }
    $manifestPath = Join-Path $PackageRoot "manifest.json"
    if (Test-Path -LiteralPath $manifestPath) {
        try {
            $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
            if ("$($manifest.version)" -ne $Version) { [void]$problems.Add("manifest.json version '$($manifest.version)' != '$Version'") }
            if ("$($manifest.platform)" -ne $OsName) { [void]$problems.Add("manifest.json platform '$($manifest.platform)' != '$OsName'") }
            if ("$($manifest.commit)" -ne $script:Commit) { [void]$problems.Add("manifest.json commit does not match the build commit") }
        } catch {
            [void]$problems.Add("manifest.json is not valid JSON: $($_.Exception.Message)")
        }
    }
    $openapi = Join-Path $PackageRoot "docs\openapi\api.yaml"
    if ((Test-Path -LiteralPath $openapi) -and (Get-Item -LiteralPath $openapi).Length -lt 100000) {
        [void]$problems.Add("docs/openapi/api.yaml is smaller than 100 kB")
    }
    $envExample = Join-Path $PackageRoot ".env.example"
    if (Test-Path -LiteralPath $envExample) {
        $exampleText = Read-TextFileUtf8 -Path $envExample
        $requiredExample = @(
            "EZTEST_DATABASE_URL=",
            "EZTEST_API_PORT=13333",
            "EZTEST_WEB_PORT=6868",
            "EZTEST_EXECUTOR_PORT=13336",
            "EZTEST_EXECUTOR_TOKEN=eztest-dev-executor-token",
            "EZTEST_RENDER_TOKEN=eztest-dev-render-token",
            "EZTEST_EXECUTOR_ADDR=127.0.0.1:13336",
            "EZTEST_EXECUTOR_MAX_CONTEXTS=",
            "EZTEST_EXEC_ALLOWED_HOSTS=",
            "EZTEST_VISUAL_BASELINE_MAX_BYTES=",
            "EZTEST_ADMIN_USERNAME=admin",
            "EZTEST_ADMIN_PASSWORD=admin123",
            "PLAYWRIGHT_BROWSERS_PATH=__PACKAGE_ROOT__/playwright-browsers",
            "NODE_ENV=production",
            "PORT=6868",
            "HOSTNAME=127.0.0.1"
        )
        foreach ($needle in $requiredExample) {
            if (-not $exampleText.Contains($needle)) {
                [void]$problems.Add(".env.example is missing the 0.2 key '$needle'")
            }
        }
        $obsoleteExample = @("EZTEST_HTTP_ADDR", "EZTEST_SECRET_KEYS", "EZTEST_SECRET_KEY_ACTIVE", "EZTEST_WEB_ORIGIN", "EZTEST_OPENAPI_PATH", "EZTEST_STORAGE_DIR", "EZTEST_VERSION_ATTACHMENT_DIR", "EZTEST_EVIDENCE_DIR", "EZTEST_REPORT_DIR", "EZTEST_EXPORT_DIR", "EZTEST_GC_ENABLED", "EZTEST_LOG_LEVEL")
        foreach ($obsolete in $obsoleteExample) {
            if ($exampleText.Contains($obsolete)) {
                [void]$problems.Add(".env.example still contains the obsolete 0.1 key '$obsolete'")
            }
        }
        if (-not $exampleText.Contains("__PACKAGE_ROOT__")) {
            [void]$problems.Add(".env.example lost the __PACKAGE_ROOT__ tokens (init-config would not render absolute paths)")
        }
        $remainingTokens = $exampleText.Replace("__PACKAGE_ROOT__", "")
        if ($remainingTokens -match '__[A-Z][A-Z0-9_]*__') {
            [void]$problems.Add(".env.example contains an unexpected template token: " + [regex]::Match($remainingTokens, '__[A-Z][A-Z0-9_]*__').Value)
        }
    }
    foreach ($templateName in @("PACKAGE.md", "THIRD_PARTY_NOTICES.md")) {
        $templatePath = Join-Path $PackageRoot $templateName
        if (-not (Test-Path -LiteralPath $templatePath)) { continue }
        $templateText = Read-TextFileUtf8 -Path $templatePath
        if ($templateText -match '__[A-Z][A-Z0-9_]*__') {
            [void]$problems.Add("$templateName still contains the unreplaced template token " + [regex]::Match($templateText, '__[A-Z][A-Z0-9_]*__').Value)
        }
    }

    # 2. Browser payload for this OS.
    $assets = Get-BrowserAssets
    foreach ($asset in $assets) {
        if ($asset.Os -ne $OsName) { continue }
        if ($SlimBrowsers -and $asset.SlimSkip) { continue }
        $browserDir = Join-Path (Join-Path $PackageRoot "playwright-browsers") $asset.Name
        if (-not (Test-Path -LiteralPath (Join-Path $browserDir "INSTALLATION_COMPLETE"))) {
            [void]$problems.Add("missing INSTALLATION_COMPLETE marker in playwright-browsers/$($asset.Name)")
        }
        if (-not (Test-Path -LiteralPath (Join-Path $browserDir ($asset.RequiredFile -replace '/', '\')))) {
            [void]$problems.Add("missing $($asset.RequiredFile) in playwright-browsers/$($asset.Name)")
        }
    }

    # 3. Forbidden paths and stray secrets anywhere in the tree.
    $forbiddenPatterns = @(
        @{ Pattern = '(^|[\\/])\.env$'; Label = "repository .env" },
        @{ Pattern = '(^|[\\/])\.git([\\/]|$)'; Label = ".git directory" },
        @{ Pattern = '(^|[\\/])\.tools([\\/]|$)'; Label = ".tools directory" },
        @{ Pattern = '(^|[\\/])\.data([\\/]|$)'; Label = ".data directory" },
        @{ Pattern = 'node_modules[\\/]\.cache([\\/]|$)'; Label = "node_modules/.cache" },
        @{ Pattern = '\.local\.md$'; Label = "local secrets markdown" },
        @{ Pattern = '(^|[\\/])\.tmp([\\/]|$)'; Label = ".tmp directory" }
    )
    $allEntries = @(Get-ChildItem -LiteralPath $PackageRoot -Recurse -Force -ErrorAction SilentlyContinue)
    foreach ($entry in $allEntries) {
        $relative = $entry.FullName.Substring($PackageRoot.Length)
        foreach ($rule in $forbiddenPatterns) {
            if ($relative -match $rule.Pattern) {
                [void]$problems.Add("forbidden $($rule.Label) in package: $relative")
            }
        }
    }

    # 4. Web and executor bundles must stay symlink-free and native-free.
    foreach ($bundle in @("web\node_modules", "executor\node_modules", "renderer\node_modules")) {
        $bundlePath = Join-Path $PackageRoot $bundle
        if (-not (Test-Path -LiteralPath $bundlePath -PathType Container)) { continue }
        $links = @(Get-ChildItem -LiteralPath $bundlePath -Recurse -Force -ErrorAction SilentlyContinue | Where-Object { $_.Attributes -band [System.IO.FileAttributes]::ReparsePoint })
        if ($links.Count -gt 0) { [void]$problems.Add("$bundle contains $($links.Count) symlink(s)/junction(s)") }
        $native = @(Get-ChildItem -LiteralPath $bundlePath -Recurse -File -Force -ErrorAction SilentlyContinue | Where-Object { $_.Extension -in @(".node", ".dll", ".so", ".dylib", ".exe") })
        if ($native.Count -gt 0) { [void]$problems.Add("$bundle contains platform-native binaries (e.g. $($native[0].Name))") }
    }

    # 5. Runtime script syntax.
    $ps1Files = @(Get-ChildItem -LiteralPath $PackageRoot -Filter "*.ps1" -File -Force)
    foreach ($file in $ps1Files) {
        $problem = Test-PowerShellSyntax -Path $file.FullName
        if ($problem.Length -gt 0) { [void]$problems.Add($problem) }
    }
    Write-Info ("PowerShell syntax: {0} script(s) parsed" -f $ps1Files.Count)
    $shFiles = @(Get-ChildItem -LiteralPath $PackageRoot -Filter "*.sh" -File -Force)
    if ($script:WslAvailable) {
        $bashProblems = Test-BashSyntaxBatch -Paths ($shFiles | ForEach-Object { $_.FullName })
        foreach ($problem in $bashProblems) { [void]$problems.Add($problem) }
        Write-Info ("Bash syntax: {0} script(s) checked with WSL bash -n" -f $shFiles.Count)
    } else {
        Write-Warn "Skipping bash -n checks (WSL unavailable)."
    }
    foreach ($file in $shFiles) {
        $content = Read-TextFileUtf8 -Path $file.FullName
        if ($content.Contains("`r`n")) { [void]$problems.Add("$($file.Name) contains CRLF line endings (bash would fail)") }
    }

    # 6. Repository path / generic secret leak scan over the package's text files.
    $textFiles = @()
    foreach ($entry in @(Get-ChildItem -LiteralPath $PackageRoot -File -Force)) {
        if ($entry.Extension -in @(".md", ".txt", ".json", ".ps1", ".sh", ".example", "") -or $entry.Name -eq ".env.example" -or $entry.Name -eq "LICENSE" -or $entry.Name -eq "VERSION") {
            $textFiles += $entry.FullName
        }
    }
    foreach ($problem in (Test-RepositoryLeak -TextFiles $textFiles)) { [void]$problems.Add($problem) }
    $secretPatterns = @(
        @{ Label = "private key"; Pattern = "-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----" },
        @{ Label = "OpenAI-style API key"; Pattern = "sk-[A-Za-z0-9_-]{20,}" },
        @{ Label = "GitHub token"; Pattern = "(ghp|gho|ghs|ghu)_[A-Za-z0-9]{20,}" },
        @{ Label = "AWS access key"; Pattern = "AKIA[0-9A-Z]{16}" },
        @{ Label = "literal bearer token"; Pattern = "Bearer [A-Za-z0-9._-]{30,}" }
    )
    foreach ($file in $textFiles) {
        $text = Read-TextFileUtf8 -Path $file
        foreach ($rule in $secretPatterns) {
            if ($text -match $rule.Pattern) { [void]$problems.Add("$([System.IO.Path]::GetFileName($file)) contains a $($rule.Label) pattern") }
        }
    }
    $repositorySecrets = Get-RepositorySecretValues
    if ($repositorySecrets.Count -gt 0) {
        $scanned = 0
        foreach ($file in $textFiles) {
            $text = Read-TextFileUtf8 -Path $file
            foreach ($secret in $repositorySecrets) {
                if ($text.Contains($secret.Value)) {
                    [void]$problems.Add("repository secret from .env key $($secret.Key) leaked into $([System.IO.Path]::GetFileName($file))")
                }
            }
            $scanned++
        }
        Write-Info ("Secret scan: {0} .env value(s) checked against {1} package text file(s) (values never printed)" -f $repositorySecrets.Count, $scanned)
    }

    if ($problems.Count -gt 0) {
        foreach ($problem in $problems) { Write-Host ("   - " + $problem) -ForegroundColor Red }
        Write-Fail ("Package sanity checks failed for {0} ({1} problem(s))." -f $packageName, $problems.Count)
    }
    Write-Info ("Package sanity: {0} checks passed ({1} entries inspected, {2})" -f ($expected.Count + 6), $allEntries.Count, (Format-Bytes (Get-DirectorySizeBytes -Path $PackageRoot)))
}

# ---------------------------------------------------------------------------
# Step 8: archives and checksums.
# ---------------------------------------------------------------------------
function Get-TarExe {
    $tar = Get-Command tar.exe -ErrorAction SilentlyContinue
    if ($null -ne $tar) { return $tar.Source }
    return ""
}

function New-PackageArchive {
    param([string]$PackageRoot, [string]$OsName)
    Write-Step ("Archiving {0} package" -f $OsName)
    $packageName = Split-Path -Leaf $PackageRoot
    $parent = Split-Path -Parent $PackageRoot
    $extension = if ($OsName -eq "windows") { ".zip" } else { ".tar.gz" }
    $archivePath = Join-Path $script:ReleaseDir ($packageName + $extension)
    if (Test-Path -LiteralPath $archivePath) { Remove-Item -LiteralPath $archivePath -Force }
    $tar = Get-TarExe
    $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
    if ($tar.Length -gt 0) {
        Write-Info "Creating the archive (this can take a few minutes for ~0.5-1 GB input) ..."
        $tarArguments = if ($OsName -eq "windows") { @("-a", "-c", "-f", $archivePath, $packageName) } else { @("-czf", $archivePath, $packageName) }
        $result = Invoke-ProcessCapture -FilePath $tar -Arguments $tarArguments -WorkingDirectory $parent -TimeoutMs 1800000
        $exitCode = $result.ExitCode
        if ($exitCode -ne 0) {
            Write-Fail ("tar.exe failed to create {0} (exit {1}):{2}{3}" -f $archivePath, $exitCode, [Environment]::NewLine, (($result.Output | Select-Object -First 10) -join [Environment]::NewLine))
        }
    } elseif ($OsName -eq "windows") {
        Write-Warn "tar.exe not available; falling back to Compress-Archive (slower, and empty directories may be skipped)."
        Compress-Archive -Path (Join-Path $PackageRoot "*") -DestinationPath $archivePath -CompressionLevel Optimal
    } else {
        Write-Fail "tar.exe (bsdtar) is required to create the Linux .tar.gz archive on Windows."
    }
    $stopwatch.Stop()
    if (-not (Test-Path -LiteralPath $archivePath -PathType Leaf)) {
        Write-Fail "Archive was not created: $archivePath"
    }
    $size = (Get-Item -LiteralPath $archivePath).Length
    if ($size -lt 100MB) {
        Write-Fail ("Archive $archivePath is only {0}; expected several hundred MB - the archive creation silently failed." -f (Format-Bytes $size))
    }
    Write-Info ("Archive: {0} ({1}) in {2}" -f $archivePath, (Format-Bytes $size), (Format-Duration $stopwatch.Elapsed.TotalSeconds))
    return $archivePath
}

function Test-ArchiveContents {
    param([string]$ArchivePath, [string]$OsName)
    $packageName = "eztest-$Version-$OsName-x64"
    $tar = Get-TarExe
    if ($tar.Length -eq 0) {
        Write-Warn "tar.exe is unavailable; skipping the archive content scan."
        return
    }
    $result = Invoke-ProcessCapture -FilePath $tar -Arguments @("-tf", $ArchivePath) -TimeoutMs 600000
    if ($result.ExitCode -ne 0) {
        Write-Fail ("Could not list archive contents: {0}{1}{2}" -f $ArchivePath, [Environment]::NewLine, (($result.Output | Select-Object -First 10) -join [Environment]::NewLine))
    }
    $entries = @($result.StdOut)
    $problems = New-Object System.Collections.ArrayList
    $count = 0
    foreach ($entry in @($entries)) {
        $name = "$entry".Trim()
        if ($name.Length -eq 0) { continue }
        $count++
        if (-not ($name -eq $packageName -or $name.StartsWith($packageName + "/"))) {
            [void]$problems.Add("archive entry outside the package directory: $name")
        }
        if ($name -match '(^|/)\.env$') { [void]$problems.Add("archive contains a repository .env: $name") }
        if ($name -match '(^|/)\.git(/|$)') { [void]$problems.Add("archive contains .git: $name") }
        if ($name -match '(^|/)\.tools(/|$)') { [void]$problems.Add("archive contains .tools: $name") }
        if ($name -match '(^|/)\.data(/|$)') { [void]$problems.Add("archive contains .data: $name") }
        if ($name -match 'node_modules/\.cache/') { [void]$problems.Add("archive contains node_modules/.cache: $name") }
    }
    foreach ($required in @("$packageName/VERSION", "$packageName/manifest.json", "$packageName/web/server.js", "$packageName/docs/openapi/api.yaml", "$packageName/runtime/node", "$packageName/playwright-browsers", "$packageName/renderer/src/server.mjs", "$packageName/renderer/node_modules/playwright/package.json", "$packageName/start-eztest.ps1", "$packageName/start-eztest.sh")) {
        $found = $false
        foreach ($entry in @($entries)) {
            if ("$entry".Trim() -eq $required -or "$entry".Trim().StartsWith($required + "/")) { $found = $true; break }
        }
        if (-not $found) { [void]$problems.Add("archive is missing required entry: $required") }
    }
    if ($problems.Count -gt 0) {
        foreach ($problem in $problems) { Write-Host ("   - " + $problem) -ForegroundColor Red }
        Write-Fail ("Archive content scan failed for {0}." -f (Split-Path -Leaf $ArchivePath))
    }
    Write-Info ("Archive scan: {0:N0} entries, no forbidden paths" -f $count)
}

function Write-Checksums {
    param([string[]]$ArchivePaths)
    $lines = @()
    foreach ($path in $ArchivePaths) {
        $hash = Get-FileSha256 -Path $path
        $lines += ("{0}  {1}" -f $hash, (Split-Path -Leaf $path))
    }
    $checksumPath = Join-Path $script:ReleaseDir "SHA256SUMS.txt"
    Write-FileUtf8NoBom -Path $checksumPath -Text (($lines -join "`n") + "`n")
    Write-Info "SHA256SUMS.txt:"
    foreach ($line in $lines) { Write-Info ("  " + $line) }
    return $checksumPath
}

# ---------------------------------------------------------------------------
# Step 9: package smoke tests (Windows native + one-shot WSL Linux).
# ---------------------------------------------------------------------------
function Assert-SmokePortsFree {
    # The Windows smoke test stops its throw-away PostgreSQL asynchronously, so a
    # port can still be bound for a few seconds when the Linux smoke starts. Poll
    # for a bounded time instead of failing on a transient conflict.
    param([int]$TimeoutSeconds = 60)
    $checks = @(
        @{ Port = 13333; Label = "API" },
        @{ Port = 6868; Label = "web" },
        @{ Port = 13336; Label = "executor" },
        @{ Port = 13337; Label = "PDF renderer" },
        @{ Port = 55432; Label = "smoke PostgreSQL (Windows)" },
        @{ Port = 55433; Label = "smoke PostgreSQL (Linux)" }
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ($true) {
        $inUse = @()
        foreach ($check in $checks) {
            if (Test-TcpPort -TargetHost "127.0.0.1" -Port $check.Port -TimeoutMs 1000) {
                $inUse += ("{0} ({1})" -f $check.Port, $check.Label)
            }
        }
        if ($inUse.Count -eq 0) { return }
        if ((Get-Date) -ge $deadline) {
            Write-Fail @"
The package smoke test needs these ports free, but they are already in use:
  $($inUse -join ", ")
Stop the dev/demo stack (api 13333, web 6868, executor 13336, renderer 13337) and the throw-away PostgreSQL probes,
or re-run with -SkipSmoke to only produce the archives.
"@
        }
        Start-Sleep -Milliseconds 1000
    }
}

function Set-DotEnvValue {
    param([string]$Path, [string]$Name, [string]$Value)
    $lines = [System.IO.File]::ReadAllLines($Path, [System.Text.Encoding]::UTF8)
    $updated = New-Object System.Collections.ArrayList
    $replaced = $false
    foreach ($line in $lines) {
        if ($line -match ("^" + [regex]::Escape($Name) + "=")) {
            [void]$updated.Add($Name + "=" + $Value)
            $replaced = $true
        } else {
            [void]$updated.Add($line)
        }
    }
    if (-not $replaced) { [void]$updated.Add($Name + "=" + $Value) }
    [System.IO.File]::WriteAllText($Path, (($updated -join "`n") + "`n"), $script:Utf8NoBom)
}

function Invoke-WindowsPowerShellScript {
    param([string]$ScriptPath, [string[]]$Arguments, [string]$LogPath)
    $powershell = Join-Path $env:SystemRoot "System32\WindowsPowerShell\v1.0\powershell.exe"
    if (-not (Test-Path -LiteralPath $powershell)) { $powershell = "powershell.exe" }
    $allArguments = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $ScriptPath) + $Arguments
    return (Invoke-ProcessWithLog -FilePath $powershell -Arguments $allArguments -StdOutPath $LogPath -StdErrPath ($LogPath + ".err"))
}

function Invoke-WindowsSmoke {
    param([string]$ZipPath)
    Write-Step "Package smoke test: Windows x64"
    Assert-SmokePortsFree
    $smokeRoot = Join-Path $script:WorkRoot "smoke\windows"
    Remove-TreeSafe -Path $smokeRoot
    New-Item -ItemType Directory -Force -Path $smokeRoot | Out-Null
    $logPath = Join-Path $script:WorkRoot "logs\smoke-windows.log"
    $pgBin = Join-Path $script:RepoRoot ".tools\pgsql\bin"
    $pgCtl = Join-Path $pgBin "pg_ctl.exe"
    $pgIsReady = Join-Path $pgBin "pg_isready.exe"
    $initdb = Join-Path $pgBin "initdb.exe"
    $createdb = Join-Path $pgBin "createdb.exe"
    foreach ($tool in @($pgCtl, $pgIsReady, $initdb, $createdb)) {
        if (-not (Test-Path -LiteralPath $tool)) {
            Write-Fail "Windows smoke test needs the cached portable PostgreSQL 17: missing $tool (run .tools/setup-toolchain.ps1)."
        }
    }
    $pgRoot = Join-Path $smokeRoot "pg"
    $pgData = Join-Path $pgRoot "data"
    New-Item -ItemType Directory -Force -Path $pgRoot | Out-Null
    $pgStarted = $false
    $packageRoot = ""
    try {
        Write-Info "Starting throw-away PostgreSQL 17 on 127.0.0.1:55432"
        $initdbResult = Invoke-ProcessCapture -FilePath $initdb -Arguments @("-D", $pgData, "-U", "eztest", "-A", "trust", "--locale=C") -TimeoutMs 300000
        if ($initdbResult.ExitCode -ne 0) {
            Write-Fail ("initdb failed: " + (($initdbResult.Output | Select-Object -Last 5) -join " | "))
        }
        Add-Content -LiteralPath (Join-Path $pgData "postgresql.conf") -Value "`nport = 55432`nlisten_addresses = '127.0.0.1'`n"
        Start-Process -FilePath $pgCtl -ArgumentList @("-D", $pgData, "-l", (Join-Path $pgRoot "pg.log"), "-w", "start") -WindowStyle Hidden
        $ready = Invoke-WithTimeout -Seconds 60 -Label "postgres" -Condition {
            $probe = Invoke-ProcessCapture -FilePath $pgIsReady -Arguments @("-h", "127.0.0.1", "-p", "55432") -TimeoutMs 10000
            return ($probe.ExitCode -eq 0)
        }
        if (-not $ready) { Write-Fail "Throw-away PostgreSQL did not become ready on 127.0.0.1:55432." }
        $pgStarted = $true
        $createdbResult = Invoke-ProcessCapture -FilePath $createdb -Arguments @("-h", "127.0.0.1", "-p", "55432", "-U", "eztest", "eztest") -TimeoutMs 60000
        if ($createdbResult.ExitCode -ne 0) { Write-Fail ("createdb failed for the throw-away smoke database: " + (($createdbResult.Output | Select-Object -Last 5) -join " | ")) }
        Write-Info "PostgreSQL ready."

        Write-Info "Extracting $ZipPath"
        $extractRoot = Join-Path $smokeRoot "extract"
        New-Item -ItemType Directory -Force -Path $extractRoot | Out-Null
        $tar = Get-TarExe
        if ($tar.Length -eq 0) { Write-Fail "tar.exe is required to extract the Windows package archive." }
        $extractResult = Invoke-ProcessCapture -FilePath $tar -Arguments @("-xf", $ZipPath, "-C", $extractRoot) -TimeoutMs 900000
        if ($extractResult.ExitCode -ne 0) { Write-Fail "Could not extract $ZipPath." }
        $packageRoot = Join-Path $extractRoot (Get-PackageName -OsName "windows")
        if (-not (Test-Path -LiteralPath (Join-Path $packageRoot "start-eztest.ps1") -PathType Leaf)) {
            Write-Fail "Extracted package is missing start-eztest.ps1: $packageRoot"
        }

        $databaseUrl = "postgres://eztest@127.0.0.1:55432/eztest?sslmode=disable"
        Write-Info "Running init-config.ps1"
        $initCode = Invoke-WindowsPowerShellScript -ScriptPath (Join-Path $packageRoot "init-config.ps1") -Arguments @("-DatabaseUrl", $databaseUrl) -LogPath (Join-Path $script:WorkRoot "logs\smoke-windows-init.log")
        if ($initCode -ne 0) { Write-Fail "The packaged init-config.ps1 failed (exit $initCode)." }
        $envPath = Join-Path $packageRoot ".env"
        Set-DotEnvValue -Path $envPath -Name "EZTEST_EXECUTOR_MAX_CONTEXTS" -Value "1"

        Write-Info "Running the packaged start-eztest.ps1"
        $startCode = Invoke-WindowsPowerShellScript -ScriptPath (Join-Path $packageRoot "start-eztest.ps1") -Arguments @() -LogPath (Join-Path $script:WorkRoot "logs\smoke-windows-start.log")
        if ($startCode -ne 0) { Write-Fail "The packaged start-eztest.ps1 failed (exit $startCode); see logs\smoke-windows-start.log and $packageRoot\logs." }

        Write-Info "Running the packaged smoke-test.ps1"
        $smokeCode = Invoke-WindowsPowerShellScript -ScriptPath (Join-Path $packageRoot "smoke-test.ps1") -Arguments @() -LogPath $logPath
        if ($smokeCode -ne 0) {
            Write-Fail "The packaged smoke-test.ps1 FAILED (exit $smokeCode). See $logPath."
        }
        Write-Info "Windows package smoke test PASSED."
    } finally {
        if ($packageRoot.Length -gt 0) {
            $runDir = Join-Path $packageRoot "run"
            if (Test-Path -LiteralPath (Join-Path $packageRoot "stop-eztest.ps1")) {
                Write-Info "Stopping the smoke package..."
                [void](Invoke-WindowsPowerShellScript -ScriptPath (Join-Path $packageRoot "stop-eztest.ps1") -Arguments @() -LogPath (Join-Path $script:WorkRoot "logs\smoke-windows-stop.log"))
            }
            foreach ($component in @("web", "renderer", "executor", "api")) {
                $pidFile = Join-Path $runDir ($component + ".pid")
                if (Test-Path -LiteralPath $pidFile) {
                    $pidText = (Get-Content -LiteralPath $pidFile -TotalCount 1 -ErrorAction SilentlyContinue)
                    if ("$pidText".Trim() -match '^\d+$') {
                        $taskkill = Get-Command taskkill.exe -ErrorAction SilentlyContinue
                        if ($null -ne $taskkill) {
                            [void](Invoke-ProcessCapture -FilePath $taskkill.Source -Arguments @("/PID", "$pidText".Trim(), "/T", "/F") -TimeoutMs 30000)
                        }
                    }
                    Remove-Item -LiteralPath $pidFile -Force -ErrorAction SilentlyContinue
                }
            }
        }
        if ($pgStarted) {
            Write-Info "Stopping the throw-away PostgreSQL..."
            [void](Invoke-ProcessCapture -FilePath $pgCtl -Arguments @("-D", $pgData, "-m", "fast", "-w", "stop") -TimeoutMs 60000)
            # -w already waits, but poll the port so the next smoke step starts on a
            # genuinely free port instead of racing the shutdown.
            $deadline = (Get-Date).AddSeconds(30)
            while ((Get-Date) -lt $deadline) {
                if (-not (Test-TcpPort -TargetHost "127.0.0.1" -Port 55432 -TimeoutMs 500)) { break }
                Start-Sleep -Milliseconds 500
            }
        }
    }
}

function Invoke-LinuxSmoke {
    param([string]$TarGzPath)
    Write-Step "Package smoke test: Linux x64 (WSL)"
    if (-not $script:WslAvailable) {
        Write-Warn "WSL is unavailable; SKIPPING the Linux package smoke test."
        Write-Warn "Run it manually on a Linux host: extract the tar.gz, run 'sudo bash install-linux-deps.sh', then start-eztest.sh + smoke-test.sh."
        return "skipped"
    }
    Assert-SmokePortsFree
    $smokeRoot = Join-Path $script:WorkRoot "smoke\linux"
    Remove-TreeSafe -Path $smokeRoot
    New-Item -ItemType Directory -Force -Path $smokeRoot | Out-Null
    $scriptPath = Join-Path $smokeRoot "smoke-linux.sh"
    $logPath = Join-Path $script:WorkRoot "logs\smoke-linux.log"
    $packageName = Get-PackageName -OsName "linux"
    $linuxScript = @'
#!/usr/bin/env bash
# Generated by scripts/build-release.ps1: one-shot Linux package smoke test.
set -u
TAR_PATH="__TAR_PATH__"
PACKAGE_NAME="__PACKAGE_NAME__"
JAR_URL="__JAR_URL__"
ROOT="/tmp/eztest-pkg-smoke"
PG_PORT=55433

if [ "${EZTEST_SMOKE_STAGED:-0}" != "1" ]; then
    echo "== stage: extract package + PostgreSQL 17 binaries =="
    rm -rf "$ROOT"
    mkdir -p "$ROOT"
    tar -xzf "$TAR_PATH" -C "$ROOT" || { echo "TAR_EXTRACTION_FAILED"; exit 1; }
    [ -d "$ROOT/$PACKAGE_NAME" ] || { echo "PACKAGE_DIR_MISSING: $ROOT/$PACKAGE_NAME"; exit 1; }
    mkdir -p "$ROOT/pg"
    command -v curl >/dev/null 2>&1 || { echo "CURL_MISSING (apt-get install curl)"; exit 1; }
    echo "downloading PostgreSQL 17 linux binaries (~15 MB)..."
    curl -fsSL --max-time 300 -o "$ROOT/pg17.jar" "$JAR_URL" || { echo "PG_JAR_DOWNLOAD_FAILED"; exit 1; }
    command -v python3 >/dev/null 2>&1 || { echo "PYTHON3_MISSING (apt-get install python3)"; exit 1; }
    python3 - "$ROOT/pg17.jar" "$ROOT/pg" <<'PY' || { echo "PG_JAR_EXTRACTION_FAILED"; exit 1; }
import sys, zipfile
jar, destination = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(jar) as archive:
    archive.extractall(destination)
PY
    for archive in "$ROOT"/pg/*.txz; do
        [ -e "$archive" ] || continue
        tar -xJf "$archive" -C "$ROOT/pg" || { echo "PG_PAYLOAD_EXTRACTION_FAILED"; exit 1; }
    done
    PGBIN="$(find "$ROOT/pg" -maxdepth 4 -name initdb -type f | head -1)"
    [ -n "$PGBIN" ] || { echo "PG_BINARIES_NOT_FOUND"; exit 1; }
    PGBIN="$(dirname "$PGBIN")"
    chmod +x "$PGBIN"/* 2>/dev/null || true
    if [ "$(id -u)" = "0" ]; then
        echo "== stage: install browser dependencies (apt) =="
        export DEBIAN_FRONTEND=noninteractive
        chmod +x "$ROOT/$PACKAGE_NAME/runtime/node/bin/node" 2>/dev/null || true
        if command -v apt-get >/dev/null 2>&1; then
            apt-get update -qq >/dev/null 2>&1 || true
            "$ROOT/$PACKAGE_NAME/runtime/node/bin/node" "$ROOT/$PACKAGE_NAME/executor/node_modules/playwright/cli.js" install-deps chromium >/dev/null 2>&1 || true
            apt-get install -y --no-install-recommends fonts-liberation fonts-noto-color-emoji xvfb >/dev/null 2>&1 || true
            apt-get install -y --no-install-recommends libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libatspi2.0-0t64 libcairo2 libcups2t64 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0t64 libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2 >/dev/null 2>&1 || true
        fi
        id -u ezsmoke >/dev/null 2>&1 || useradd -m -s /bin/bash ezsmoke
        chmod -R a+rx "$ROOT" 2>/dev/null || true
        chown -R ezsmoke:ezsmoke "$ROOT" 2>/dev/null || true
        echo "== stage: re-running as non-root user (PostgreSQL refuses root) =="
        exec runuser -u ezsmoke -- env HOME=/home/ezsmoke EZTEST_SMOKE_STAGED=1 PATH=/usr/local/bin:/usr/bin:/bin bash "$0" "$@"
    fi
    echo "== stage done (running as non-root) =="
fi

ROOT="/tmp/eztest-pkg-smoke"
PACKAGE_NAME="__PACKAGE_NAME__"
PACKAGE="$ROOT/$PACKAGE_NAME"
PGBIN="$(find "$ROOT/pg" -maxdepth 4 -name initdb -type f | head -1)"
[ -n "$PGBIN" ] || { echo "PG_BINARIES_NOT_FOUND"; exit 1; }
PGBIN="$(dirname "$PGBIN")"
PGDATA="$ROOT/pgdata"

cleanup() {
    if [ -d "$PGDATA" ] && [ -n "$PGBIN" ]; then
        "$PGBIN/pg_ctl" -D "$PGDATA" -m fast -w stop >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

echo "== PostgreSQL 17 (throw-away, 127.0.0.1:$PG_PORT) =="
"$PGBIN/initdb" -D "$PGDATA" -U eztest -A trust --locale=C >"$ROOT/initdb.log" 2>&1 || { echo "INITDB_FAILED"; tail -5 "$ROOT/initdb.log"; exit 1; }
printf 'port = %s\nlisten_addresses = %s\n' "$PG_PORT" "'127.0.0.1'" >> "$PGDATA/postgresql.conf"
"$PGBIN/pg_ctl" -D "$PGDATA" -l "$ROOT/pg.log" -w start || { echo "PG_START_FAILED"; tail -10 "$ROOT/pg.log"; exit 1; }
echo "PostgreSQL ready (zonky minimal build: using the built-in 'postgres' database)."

echo "== package init-config =="
bash "$PACKAGE/init-config.sh" --database-url "postgres://eztest@127.0.0.1:$PG_PORT/postgres?sslmode=disable" || { echo "INIT_CONFIG_FAILED"; exit 1; }
sed -i 's/^EZTEST_EXECUTOR_MAX_CONTEXTS=.*/EZTEST_EXECUTOR_MAX_CONTEXTS=1/' "$PACKAGE/.env"

echo "== package start-eztest.sh =="
if ! bash "$PACKAGE/start-eztest.sh"; then
    echo "PACKAGE_START_FAILED"
    echo "--- api log ---"; tail -20 "$PACKAGE/logs/api.err.log" 2>/dev/null; tail -20 "$PACKAGE/logs/api.log" 2>/dev/null
    echo "--- executor log ---"; tail -20 "$PACKAGE/logs/executor.err.log" 2>/dev/null; tail -20 "$PACKAGE/logs/executor.log" 2>/dev/null
    echo "--- renderer log ---"; tail -20 "$PACKAGE/logs/renderer.err.log" 2>/dev/null; tail -20 "$PACKAGE/logs/renderer.log" 2>/dev/null
    echo "--- web log ---"; tail -20 "$PACKAGE/logs/web.err.log" 2>/dev/null
    exit 1
fi

echo "== package smoke-test.sh =="
bash "$PACKAGE/smoke-test.sh"
SMOKE_CODE=$?

echo "== package stop-eztest.sh =="
bash "$PACKAGE/stop-eztest.sh" || true

if [ "$SMOKE_CODE" -ne 0 ]; then
    echo "PACKAGE_SMOKE_FAILED"
    exit "$SMOKE_CODE"
fi
echo "LINUX_PACKAGE_SMOKE_PASSED"
exit 0
'@
    $linuxScript = $linuxScript.Replace("__TAR_PATH__", (ConvertTo-WslPath -WindowsPath $TarGzPath))
    $linuxScript = $linuxScript.Replace("__PACKAGE_NAME__", $packageName)
    $linuxScript = $linuxScript.Replace("__JAR_URL__", $script:ZonkyPostgresJarUrl)
    $linuxScript = $linuxScript.Replace("`r`n", "`n")
    # Prefer the cached PostgreSQL jar when present: the WSL download of the
    # ~15 MB zonky artifact has timed out on slow links, which would fail the
    # smoke step for a packaging-unrelated reason.
    $cachedJar = Join-Path $script:CacheDir "zonky-pg17-linux-amd64.jar"
    if (Test-Path -LiteralPath $cachedJar) {
        $downloadBlock = "    echo `"downloading PostgreSQL 17 linux binaries (~15 MB)...`"`n" +
            "    curl -fsSL --max-time 300 -o `"`$ROOT/pg17.jar`" `"`$JAR_URL`" || { echo `"PG_JAR_DOWNLOAD_FAILED`"; exit 1; }"
        $wslJarPath = ConvertTo-WslPath -WindowsPath $cachedJar
        $copyBlock = '    cp "' + $wslJarPath + '" "$ROOT/pg17.jar" || { echo "PG_JAR_COPY_FAILED"; exit 1; }'
        $patched = $linuxScript.Replace($downloadBlock, $copyBlock)
        if ($patched -eq $linuxScript) {
            Write-Warn "Could not inject the cached PostgreSQL jar into the Linux smoke script; falling back to the download."
        } else {
            $linuxScript = $patched
            Write-Info "Linux smoke test will use the cached PostgreSQL jar: $cachedJar"
        }
    }
    Write-FileUtf8NoBom -Path $scriptPath -Text $linuxScript
    $wslScriptPath = ConvertTo-WslPath -WindowsPath $scriptPath
    $wsl = Get-Command wsl.exe -ErrorAction SilentlyContinue
    if ($null -eq $wsl) { Write-Warn "WSL disappeared; skipping the Linux package smoke test."; return "skipped" }
    Write-Info "Running the one-shot Linux smoke harness in WSL (installs browser deps if running as root; may take a few minutes) ..."
    Write-Info "Output is streamed below and also written to $logPath"
    $exitCode = Invoke-ProcessWithLog -FilePath $wsl.Source -Arguments @("-e", "bash", $wslScriptPath) `
        -StdOutPath $logPath -StdErrPath ($logPath + ".err")
    if ($exitCode -ne 0) {
        Write-Fail "The Linux package smoke test FAILED (exit $exitCode). See $logPath and the WSL output above."
    }
    Write-Info "Linux package smoke test PASSED."
    return "passed"
}

# ---------------------------------------------------------------------------
# Main pipeline.
# ---------------------------------------------------------------------------
function Main {
    $pipelineStart = [System.Diagnostics.Stopwatch]::StartNew()
    Write-Host ""
    Write-Host "EzTest release packaging (Docker-free)" -ForegroundColor Cyan
    Write-Host "  version: $Version | api base URL: $ApiBaseUrl | slim browsers: $($SlimBrowsers.IsPresent) | bundle postgres: $($BundlePostgres.IsPresent)"

    Initialize-BuildContext
    Test-BuildPreconditions
    Build-ApiBinaries
    Build-WebBundle
    Build-ExecutorStage
    Build-RendererStage
    Stage-Browsers
    Stage-NodeRuntime

    $packages = @(
        @{ Os = "windows"; Root = "" },
        @{ Os = "linux"; Root = "" }
    )
    foreach ($entry in $packages) {
        $entry.Root = Assemble-Package -OsName $entry.Os
    }
    foreach ($entry in $packages) {
        Test-PackageTree -PackageRoot $entry.Root -OsName $entry.Os
    }

    $archives = @()
    foreach ($entry in $packages) {
        $archive = New-PackageArchive -PackageRoot $entry.Root -OsName $entry.Os
        Test-ArchiveContents -ArchivePath $archive -OsName $entry.Os
        $archives += $archive
    }
    $checksumPath = Write-Checksums -ArchivePaths $archives

    $smokeResults = @()
    if ($SkipSmoke) {
        Write-Step "Package smoke tests"
        Write-Warn "-SkipSmoke was passed: the Windows and Linux package smoke tests were NOT run."
        $smokeResults += "windows: skipped"
        $smokeResults += "linux: skipped"
    } else {
        $windowsZip = $archives[0]
        $linuxTar = $archives[1]
        Invoke-WindowsSmoke -ZipPath $windowsZip
        $linuxResult = Invoke-LinuxSmoke -TarGzPath $linuxTar
        $smokeResults += "windows: passed"
        $smokeResults += ("linux: " + $linuxResult)
    }

    $pipelineStart.Stop()
    Write-Host ""
    Write-Host "Release artifacts" -ForegroundColor Cyan
    Write-Host ("  output dir : {0}" -f $script:ReleaseDir)
    $totalBytes = 0
    foreach ($archive in $archives) {
        $size = (Get-Item -LiteralPath $archive).Length
        $totalBytes += $size
        Write-Host ("  {0,-46} {1,10}  {2}" -f [System.IO.Path]::GetFileName($archive), (Format-Bytes $size), (Get-FileSha256 -Path $archive)) -ForegroundColor Green
    }
    Write-Host ("  total      : {0}" -f (Format-Bytes $totalBytes))
    Write-Host ("  checksums  : {0}" -f $checksumPath)
    Write-Host ("  packages   : {0}" -f (($packages | ForEach-Object { $_.Root }) -join " ; "))
    Write-Host ("  smoke      : {0}" -f ($smokeResults -join " ; "))
    Write-Host ("  elapsed    : {0}" -f (Format-Duration $pipelineStart.Elapsed.TotalSeconds))
    if ($script:FreeDiskWarned) {
        Write-Warn "The build ran with less than 6 GB free disk space; check that the archives are complete."
    }
    Write-Host ""
    Write-Host "Next steps (release runbook):" -ForegroundColor Cyan
    Write-Host "  1. Inspect $($script:ReleaseDir)\SHA256SUMS.txt and the two archives."
    Write-Host "  2. Publish the GitHub release with both archives + SHA256SUMS.txt and docs/release-notes/*."
    Write-Host "  3. Update the README '使用方式' section with the PostgreSQL 17 and Linux browser-deps prerequisites."
    Write-Host ""
}

# Dot-source this file to import the helper functions without running the build.
if ($MyInvocation.InvocationName -ne ".") {
    Main
}
