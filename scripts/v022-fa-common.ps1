#requires -Version 5.1
<#
.SYNOPSIS
    Shared helpers for the EzTest v0.22 final-acceptance (FA) scripts.

.DESCRIPTION
    Dot-source this file from an FA script:

        . (Join-Path $PSScriptRoot 'v022-fa-common.ps1')

    The helpers are intentionally side-effect free: they read .env / JSON / PNG
    files and return values or a list of RED strings. They never print secret
    values and never call exit; each gate decides its own exit code.

    Encoding rules (Windows PowerShell 5.1, zh-CN host):
      - read .env / JSON / Markdown as UTF-8;
      - write JSON as UTF-8 without BOM via UTF8Encoding($false);
      - scripts themselves are saved with a UTF-8 BOM because they contain CJK
        literal strings.

.NOTES
    Repository root is the parent directory of this script (scripts/).
#>

$script:FaRepoRoot = Split-Path -Parent $PSScriptRoot
$script:FaUtf8NoBom = New-Object System.Text.UTF8Encoding($false)

function Get-FaRepoRoot {
    return $script:FaRepoRoot
}

function Get-EnvValue {
    <#
    .SYNOPSIS
        Read one key from the repository .env file.
    .DESCRIPTION
        Returns the raw value with surrounding single/double quotes removed.
        The value is never written to the console or to any evidence file.
    #>
    param(
        [Parameter(Mandatory = $true)]
        [string]$Key
    )

    $envPath = Join-Path $script:FaRepoRoot '.env'
    if (-not (Test-Path -LiteralPath $envPath)) {
        throw 'missing .env in repository root'
    }

    $pattern = '^\s*' + [regex]::Escape($Key) + '\s*=(.*)$'
    foreach ($line in [System.IO.File]::ReadAllLines($envPath, [System.Text.Encoding]::UTF8)) {
        if ($line -match $pattern) {
            $value = $Matches[1].Trim()
            if ($value.Length -ge 2) {
                $first = $value[0]
                $last = $value[$value.Length - 1]
                if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                    $value = $value.Substring(1, $value.Length - 2)
                }
            }
            return $value
        }
    }

    throw "missing $Key in .env"
}

function Read-JsonUtf8 {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    if (-not (Test-Path -LiteralPath $Path)) {
        throw "missing JSON file: $Path"
    }
    $full = (Resolve-Path -LiteralPath $Path).Path
    $raw = [System.IO.File]::ReadAllText($full, [System.Text.Encoding]::UTF8)
    return ($raw | ConvertFrom-Json)
}

function Write-JsonUtf8 {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [Parameter(Mandatory = $true)]
        $Object,
        [int]$Depth = 20
    )

    $parent = Split-Path -Parent $Path
    if ($parent -and -not (Test-Path -LiteralPath $parent)) {
        New-Item -ItemType Directory -Force -Path $parent | Out-Null
    }
    $json = $Object | ConvertTo-Json -Depth $Depth
    [System.IO.File]::WriteAllText($Path, $json, $script:FaUtf8NoBom)
}

function Get-Sha {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    if (-not (Test-Path -LiteralPath $Path)) {
        throw "missing file: $Path"
    }
    $hash = Get-FileHash -LiteralPath $Path -Algorithm SHA256
    return $hash.Hash.ToLowerInvariant()
}

function Get-Head {
    <#
    .SYNOPSIS
        Return the current repository HEAD as a 40-character lowercase SHA.
    #>
    $output = & git -C $script:FaRepoRoot rev-parse HEAD 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $output) {
        throw 'git rev-parse HEAD failed'
    }
    return ([string]$output).Trim()
}

function Assert-Png {
    <#
    .SYNOPSIS
        Validate a PNG screenshot: magic bytes, dimensions and non-blankness.
    .DESCRIPTION
        Returns an array of RED strings. An empty array means the PNG passed:
          - file exists and starts with the 8-byte PNG signature;
          - IHDR width >= MinWidth and height >= MinHeight (default 800x600);
          - at least MinUniqueColors distinct colors in a sampled grid
            (default 8), so an all-white / single-color image fails.
        The function never calls exit; the caller aggregates the RED list.
    #>
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [int]$MinWidth = 800,
        [int]$MinHeight = 600,
        [int]$MinUniqueColors = 8
    )

    $red = New-Object System.Collections.Generic.List[string]

    if (-not (Test-Path -LiteralPath $Path)) {
        $red.Add("png missing: $Path")
        return @($red)
    }
    $full = (Resolve-Path -LiteralPath $Path).Path

    $bytes = $null
    try {
        $bytes = [System.IO.File]::ReadAllBytes($full)
    }
    catch {
        $red.Add("png unreadable: $Path ($($_.Exception.Message))")
        return @($red)
    }

    if ($bytes.Length -lt 24) {
        $red.Add("png too short: $Path ($($bytes.Length) bytes)")
        return @($red)
    }

    $magic = @(0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A)
    $magicOk = $true
    for ($i = 0; $i -lt $magic.Count; $i++) {
        if ([int]$bytes[$i] -ne [int]$magic[$i]) { $magicOk = $false; break }
    }
    if (-not $magicOk) {
        $red.Add("png magic mismatch: $Path")
        return @($red)
    }

    # IHDR is the first chunk: width/height are big-endian at offsets 16..23.
    $width = ([int]$bytes[16] -shl 24) -bor ([int]$bytes[17] -shl 16) -bor ([int]$bytes[18] -shl 8) -bor [int]$bytes[19]
    $height = ([int]$bytes[20] -shl 24) -bor ([int]$bytes[21] -shl 16) -bor ([int]$bytes[22] -shl 8) -bor [int]$bytes[23]
    if ($width -lt $MinWidth -or $height -lt $MinHeight) {
        $red.Add("png too small: $Path (${width}x${height}, need >= ${MinWidth}x${MinHeight})")
    }

    try {
        Add-Type -AssemblyName System.Drawing -ErrorAction Stop
        $bitmap = New-Object System.Drawing.Bitmap($full)
        try {
            $colors = New-Object 'System.Collections.Generic.HashSet[string]'
            $stepX = [Math]::Max(1, [int][Math]::Floor($bitmap.Width / 40))
            $stepY = [Math]::Max(1, [int][Math]::Floor($bitmap.Height / 40))
            for ($y = 0; $y -lt $bitmap.Height; $y += $stepY) {
                for ($x = 0; $x -lt $bitmap.Width; $x += $stepX) {
                    $pixel = $bitmap.GetPixel($x, $y)
                    [void]$colors.Add(('{0:X8}' -f $pixel.ToArgb()))
                }
            }
            if ($colors.Count -lt $MinUniqueColors) {
                $red.Add("png looks blank: $Path ($($colors.Count) sampled colors, need >= $MinUniqueColors)")
            }
        }
        finally {
            $bitmap.Dispose()
        }
    }
    catch {
        $red.Add("png decode failed: $Path ($($_.Exception.Message))")
    }

    return @($red)
}

function Assert-ExactSetStrings {
    <#
    .SYNOPSIS
        Compare two string collections as exact sets.
    .DESCRIPTION
        Returns an array of RED strings for duplicates, missing items and
        unexpected items. It never calls exit, so several set checks can be
        aggregated by a gate before it decides PASS/FAIL.
    #>
    param(
        $Actual,
        $Expected,
        [string]$Label = 'set'
    )

    $red = New-Object System.Collections.Generic.List[string]

    $actualList = New-Object System.Collections.Generic.List[string]
    foreach ($item in @($Actual)) {
        if ($null -ne $item) { $actualList.Add([string]$item) }
    }
    $expectedList = New-Object System.Collections.Generic.List[string]
    foreach ($item in @($Expected)) {
        if ($null -ne $item) { $expectedList.Add([string]$item) }
    }

    $grouped = @($actualList | Group-Object)
    foreach ($group in $grouped) {
        if ($group.Count -gt 1) {
            $red.Add("${Label}: duplicate '$($group.Name)' x$($group.Count)")
        }
    }

    $actualUnique = @($actualList | Sort-Object -Unique)
    $expectedUnique = @($expectedList | Sort-Object -Unique)

    $missing = @($expectedUnique | Where-Object { $actualUnique -notcontains $_ })
    $unexpected = @($actualUnique | Where-Object { $expectedUnique -notcontains $_ })

    if ($missing.Count -gt 0) {
        $red.Add("${Label}: missing $(($missing | Sort-Object) -join ', ')")
    }
    if ($unexpected.Count -gt 0) {
        $red.Add("${Label}: unexpected $(($unexpected | Sort-Object) -join ', ')")
    }

    return @($red)
}
