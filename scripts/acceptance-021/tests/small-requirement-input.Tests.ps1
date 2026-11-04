#requires -Version 5.1
# Focused offline tests for Task 88 (P10-T13): the small requirement input profile
# used by the final S0-S10 acceptance runner.
#
#   powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-021/tests/small-requirement-input.Tests.ps1
#
# The tests cover the pure profile/comparison helpers, the script wiring (parser
# level) and the runner preflight (the runner is invoked only when it already
# declares the small-input parameters, so a RED run never touches the stack).
Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

$testsDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$scriptsDir = Split-Path -Parent $testsDir
$repoRoot = Split-Path -Parent (Split-Path -Parent $scriptsDir)
. (Join-Path $scriptsDir "lib.ps1")

$inputsDir = Join-Path $repoRoot "docs\version-V0.2.1\acceptance\inputs"
$smallMarkdown = Join-Path $inputsDir "small-a.md"
$smallFeature = Join-Path $inputsDir "small-b.feature"
$smallText = Join-Path $inputsDir "small-b.txt"
$smallDocx = Join-Path $inputsDir "small-c.docx"
$expectedIds = "FR-011,FR-014,FR-025"

$script:checks = 0
$script:failures = 0

function Assert-True {
    param([bool] $Condition, [string] $Message)
    $script:checks++
    if (-not $Condition) {
        $script:failures++
        Write-Host "[FAIL] $Message" -ForegroundColor Red
    } else {
        Write-Host "[PASS] $Message"
    }
}

function Assert-Equal {
    param($Expected, $Actual, [string] $Message)
    Assert-True -Condition ($Expected -eq $Actual) -Message ("{0} (expected={1} actual={2})" -f $Message, $Expected, $Actual)
}

function Assert-Throws {
    param([scriptblock] $Action, [string] $Pattern, [string] $Message)
    $script:checks++
    $threw = $false
    $caught = ""
    try { & $Action | Out-Null } catch { $threw = $true; $caught = $_.Exception.Message }
    if (-not $threw) {
        $script:failures++
        Write-Host "[FAIL] $Message (did not throw)" -ForegroundColor Red
        return
    }
    if ($caught -match $Pattern) {
        Write-Host "[PASS] $Message"
    } else {
        $script:failures++
        Write-Host ("[FAIL] {0} (message='{1}')" -f $Message, $caught) -ForegroundColor Red
    }
}

function Get-ScriptParameterNames {
    param([Parameter(Mandatory = $true)] [string] $Path)
    $tokens = $null
    $errors = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$errors)
    if (@($errors).Count -gt 0) { throw "cannot parse ${Path}: $(@($errors | ForEach-Object { $_.Message }) -join '; ')" }
    if ($null -eq $ast.ParamBlock) { return @() }
    return @($ast.ParamBlock.Parameters | ForEach-Object { $_.Name.VariablePath.UserPath })
}

function New-RequirementFixture {
    param([string[]] $FrIds = @("FR-011", "FR-014", "FR-025"))
    $items = @()
    foreach ($id in $FrIds) {
        $items += [pscustomobject]@{
            id          = "req-$id"
            title       = "$id fixture title"
            description = "requirement $id"
            status      = "draft"
            priority    = "P0"
        }
    }
    return $items
}

# ---------------------------------------------------------------------------
# Pure profile helper: the requirement input file decides the expected count and
# the expected FR ID set. It never falls back to the full 67-requirement document.
# ---------------------------------------------------------------------------

function Test-SmallMarkdownProfile {
    $profile = Resolve-AcceptanceRequirementProfile -RequirementInput $smallMarkdown -RepoRoot $repoRoot
    Assert-Equal -Expected "small-input" -Actual $profile.Mode -Message "small-a.md resolves to small-input mode"
    Assert-Equal -Expected 3 -Actual $profile.ExpectedRequirements -Message "small-a.md expects 3 requirements"
    Assert-Equal -Expected $expectedIds -Actual ($profile.ExpectedFrIds -join ",") -Message "small-a.md exposes the exact FR ID set"
    Assert-Equal -Expected 3 -Actual @($profile.Entries).Count -Message "small-a.md yields one baseline entry per FR"
    Assert-Equal -Expected 3 -Actual @($profile.ExtractionEntries).Count -Message "small-a.md yields one extraction entry per FR"
    Assert-Equal -Expected 3 -Actual $profile.V1FrCount -Message "small-a.md counts 3 V1 functional requirements"
    Assert-Equal -Expected 0 -Actual $profile.V2Count -Message "small-a.md contains no V2 requirements"
    Assert-Equal -Expected (Get-AcceptanceFileSha256 -Path $smallMarkdown) -Actual $profile.Sha256 -Message "small-a.md profile records the real file sha256"
    Assert-True -Condition ($profile.Text -match 'FR-011' -and $profile.Text -match 'FR-014' -and $profile.Text -match 'FR-025') -Message "small-a.md profile carries the source text"
}

function Test-SmallGherkinProfile {
    $profile = Resolve-AcceptanceRequirementProfile -RequirementInput $smallFeature -RepoRoot $repoRoot
    Assert-Equal -Expected "gherkin" -Actual $profile.Format -Message "small-b.feature is detected as Gherkin"
    Assert-Equal -Expected 3 -Actual $profile.ExpectedRequirements -Message "small-b.feature expects 3 requirements"
    Assert-Equal -Expected $expectedIds -Actual ($profile.ExpectedFrIds -join ",") -Message "small-b.feature exposes the exact FR ID set"
    Assert-Equal -Expected 3 -Actual @($profile.Entries).Count -Message "small-b.feature yields one extraction entry per FR"
}

function Test-SmallGherkinFallbackTxtProfile {
    $profile = Resolve-AcceptanceRequirementProfile -RequirementInput $smallText -RepoRoot $repoRoot
    Assert-Equal -Expected "gherkin" -Actual $profile.Format -Message "small-b.txt is parsed with the byte-identical Gherkin fallback"
    Assert-Equal -Expected 3 -Actual $profile.ExpectedRequirements -Message "small-b.txt expects 3 requirements"
    Assert-Equal -Expected $expectedIds -Actual ($profile.ExpectedFrIds -join ",") -Message "small-b.txt exposes the same FR ID set as the .feature file"
    Assert-Equal -Expected (Get-AcceptanceFileSha256 -Path $smallFeature) -Actual $profile.Sha256 -Message "small-b.txt and small-b.feature are byte-identical"
}

function Test-SmallDocxProfile {
    $profile = Resolve-AcceptanceRequirementProfile -RequirementInput $smallDocx -RepoRoot $repoRoot
    Assert-Equal -Expected "docx" -Actual $profile.Format -Message "small-c.docx is detected as a DOCX input"
    Assert-Equal -Expected 3 -Actual $profile.ExpectedRequirements -Message "small-c.docx expects 3 requirements"
    Assert-Equal -Expected $expectedIds -Actual ($profile.ExpectedFrIds -join ",") -Message "small-c.docx exposes the exact FR ID set"
    Assert-Equal -Expected 3 -Actual @($profile.Entries).Count -Message "small-c.docx yields one baseline entry per FR"
    Assert-Equal -Expected 1 -Actual @($profile.ExtractionEntries).Count -Message "small-c.docx sends one verbatim extraction block (flat text has no per-FR sections)"
    Assert-Equal -Expected 3 -Actual @($profile.ExtractionEntries[0].expected_ids).Count -Message "the DOCX extraction block carries the full expected ID list"
    Assert-True -Condition ($profile.ExtractionEntries[0].text.Length -gt 1000) -Message "the DOCX extraction block contains the full small input text"
    Assert-True -Condition ($profile.Text.Length -gt 1000) -Message "small-c.docx text extraction is non-trivial"
}

function Test-ExpectedRequirementsMismatchFails {
    Assert-Throws -Action {
        Resolve-AcceptanceRequirementProfile -RequirementInput $smallMarkdown -ExpectedRequirements 67 -RepoRoot $repoRoot
    } -Pattern 'ExpectedRequirements=67' -Message "a 3-requirement input with -ExpectedRequirements 67 fails instead of silently importing the full document"
}

function Test-ExpectedFrIdsMismatchFails {
    Assert-Throws -Action {
        Resolve-AcceptanceRequirementProfile -RequirementInput $smallMarkdown -ExpectedFrIds "FR-011,FR-014,FR-999" -RepoRoot $repoRoot
    } -Pattern 'ExpectedFrIds' -Message "an explicit FR ID set that differs from the input fails loudly"
}

function Test-MissingInputFails {
    Assert-Throws -Action {
        Resolve-AcceptanceRequirementProfile -RequirementInput (Join-Path $inputsDir "does-not-exist.md") -RepoRoot $repoRoot
    } -Pattern 'not found' -Message "a missing requirement input fails loudly"
}

function Test-NoRequirementModeFails {
    Assert-Throws -Action {
        Resolve-AcceptanceRequirementProfile -RepoRoot $repoRoot
    } -Pattern 'no requirement input|RequirementInput|FullSutDoc' -Message "neither a small input nor -FullSutDoc is rejected explicitly (no silent 67 default)"
}

function Test-BothRequirementModesFail {
    Assert-Throws -Action {
        Resolve-AcceptanceRequirementProfile -RequirementInput $smallMarkdown -FullSutDoc -FullSutDocPath $smallMarkdown -RepoRoot $repoRoot
    } -Pattern 'mutually exclusive|not both|either' -Message "small-input and full-document modes are mutually exclusive"
}

function Test-FullDocumentProfile {
    $temp = Join-Path ([System.IO.Path]::GetTempPath()) ("task88-full-" + [guid]::NewGuid().ToString("N") + ".md")
    try {
        $lines = @(
            "# Synthetic full requirement document",
            "",
            "#### FR-001 Synthetic first requirement",
            "**优先级**：P0",
            "**需求状态**：已实现",
            "Body FR-001.",
            "",
            "#### NFR-001 Synthetic non-functional requirement",
            "**优先级**：P1",
            "**需求状态**：已实现",
            "Body NFR-001.",
            "",
            "#### FR-101 Synthetic V2 extension",
            "**优先级**：P2",
            "**需求状态**：延展规划（当前系统不存在）",
            "Body FR-101."
        )
        [System.IO.File]::WriteAllText($temp, (($lines -join "`r`n") + "`r`n"), [System.Text.UTF8Encoding]::new($false))
        $profile = Resolve-AcceptanceRequirementProfile -FullSutDoc -FullSutDocPath $temp -RepoRoot $repoRoot
        Assert-Equal -Expected "full-sut-doc" -Actual $profile.Mode -Message "the legacy full document mode is explicit"
        Assert-Equal -Expected 2 -Actual $profile.ExpectedRequirements -Message "the full document mode counts V1 requirements (2 in the synthetic doc)"
        Assert-Equal -Expected "FR-001,NFR-001" -Actual ($profile.ExpectedFrIds -join ",") -Message "the full document mode expects only the V1 FR/NFR IDs"
        Assert-Equal -Expected 1 -Actual $profile.V2Count -Message "the full document mode records the V2 count without importing it"
    } finally {
        Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue
    }
}

# ---------------------------------------------------------------------------
# Post-import comparison: requirement count and FR ID mapping.
# ---------------------------------------------------------------------------

function Test-RequirementSetComparisonPassesOnExactSmallSet {
    $comparison = Compare-AcceptanceRequirementSet -ExpectedFrIds $expectedIds -ExpectedRequirements 3 -Requirements (New-RequirementFixture)
    Assert-Equal -Expected $true -Actual $comparison.Passed -Message "3 requirements with the exact small-input FR IDs pass the set check"
    Assert-Equal -Expected 3 -Actual $comparison.ActualCount -Message "the comparison reports the actual requirement count"
    Assert-Equal -Expected 3 -Actual $comparison.ExpectedCount -Message "the comparison reports the expected requirement count"
    Assert-Equal -Expected 0 -Actual @($comparison.MissingFrIds).Count -Message "the exact set has no missing FR IDs"
    Assert-Equal -Expected 0 -Actual @($comparison.ExtraFrIds).Count -Message "the exact set has no extra FR IDs"
    Assert-Equal -Expected 0 -Actual @($comparison.DuplicateIds).Count -Message "the exact set has no duplicate FR IDs"
    Assert-Equal -Expected 0 -Actual @($comparison.UntraceableRequirementIds).Count -Message "the exact set has no untraceable requirements"
}

function Test-RequirementSetComparisonFailsOnCountAndIds {
    $missing = Compare-AcceptanceRequirementSet -ExpectedFrIds $expectedIds -ExpectedRequirements 3 -Requirements (New-RequirementFixture -FrIds @("FR-011", "FR-014"))
    Assert-Equal -Expected $false -Actual $missing.Passed -Message "a missing requirement fails the set check"
    Assert-Equal -Expected "FR-025" -Actual (@($missing.MissingFrIds) -join ",") -Message "the comparison reports the missing FR ID"

    $extra = Compare-AcceptanceRequirementSet -ExpectedFrIds $expectedIds -ExpectedRequirements 3 -Requirements (New-RequirementFixture -FrIds @("FR-011", "FR-014", "FR-025", "FR-099"))
    Assert-Equal -Expected $false -Actual $extra.Passed -Message "an extra FR requirement fails the set check"
    Assert-Equal -Expected "FR-099" -Actual (@($extra.ExtraFrIds) -join ",") -Message "the comparison reports the extra FR ID"

    $duplicate = Compare-AcceptanceRequirementSet -ExpectedFrIds $expectedIds -ExpectedRequirements 3 -Requirements @(
        (New-RequirementFixture -FrIds @("FR-011", "FR-014", "FR-025")) +
        (New-RequirementFixture -FrIds @("FR-011"))
    )
    Assert-Equal -Expected $false -Actual $duplicate.Passed -Message "a duplicate FR requirement fails the set check"
    Assert-Equal -Expected "FR-011" -Actual (@($duplicate.DuplicateIds) -join ",") -Message "the comparison reports the duplicate FR ID"

    $untraceable = @(New-RequirementFixture -FrIds @("FR-011", "FR-014")) + [pscustomobject]@{
        id = "req-no-id"; title = "no requirement id"; description = ""; status = "draft"; priority = "P0"
    }
    $loose = Compare-AcceptanceRequirementSet -ExpectedFrIds $expectedIds -ExpectedRequirements 3 -Requirements $untraceable
    Assert-Equal -Expected $false -Actual $loose.Passed -Message "an untraceable requirement fails the set check"
    Assert-Equal -Expected 1 -Actual @($loose.UntraceableRequirementIds).Count -Message "the comparison reports the untraceable requirement"
}

# ---------------------------------------------------------------------------
# Script wiring: the profile must be declared and used by S1/S2/S3/run-all.
# ---------------------------------------------------------------------------

function Test-RunnerDeclaresSmallInputProfile {
    $names = Get-ScriptParameterNames -Path (Join-Path $scriptsDir "run-all.ps1")
    Assert-True -Condition ($names -contains "RequirementInput") -Message "run-all.ps1 declares -RequirementInput"
    Assert-True -Condition ($names -contains "ExpectedRequirements") -Message "run-all.ps1 declares -ExpectedRequirements"
    Assert-True -Condition ($names -contains "FullSutDoc") -Message "run-all.ps1 declares the explicit -FullSutDoc legacy switch"
    $text = Get-Content -LiteralPath (Join-Path $scriptsDir "run-all.ps1") -Raw -Encoding UTF8
    Assert-True -Condition ($text -match 'Resolve-AcceptanceRequirementProfile') -Message "run-all.ps1 resolves the requirement profile before running scenarios"
    Assert-True -Condition ($text -match 'preflight') -Message "run-all.ps1 records a preflight failure"
    Assert-True -Condition ($text -match '\$childArgs \+= @\("-RequirementInput"') -Message "run-all.ps1 passes -RequirementInput to the child scenarios"
    Assert-True -Condition ($text -match 'requirement_profile') -Message "run-all.ps1 records the requirement profile in the summary"
    Assert-True -Condition ($text -match 'state\.json' -and $text -match 'Remove-Item -LiteralPath \$runStatePath') -Message "run-all.ps1 clears stale scenario state before a fresh run"
}

function Test-S1DeclaresAndRecordsRequirementInput {
    $names = Get-ScriptParameterNames -Path (Join-Path $scriptsDir "s1-version.ps1")
    Assert-True -Condition ($names -contains "RequirementInput") -Message "s1-version.ps1 accepts -RequirementInput"
    Assert-True -Condition ($names -contains "ExpectedRequirements") -Message "s1-version.ps1 accepts -ExpectedRequirements"
    $text = Get-Content -LiteralPath (Join-Path $scriptsDir "s1-version.ps1") -Raw -Encoding UTF8
    Assert-True -Condition ($text -match 'Resolve-AcceptanceRequirementProfile') -Message "s1-version.ps1 resolves the requirement input profile"
    Assert-True -Condition ($text -match 'requirement-input\.json') -Message "s1-version.ps1 writes the requirement input evidence"
}

function Test-S2UsesSmallInputOnly {
    $names = Get-ScriptParameterNames -Path (Join-Path $scriptsDir "s2-clarify.ps1")
    Assert-True -Condition ($names -contains "RequirementInput") -Message "s2-clarify.ps1 accepts -RequirementInput"
    Assert-True -Condition ($names -contains "ExpectedRequirements") -Message "s2-clarify.ps1 accepts -ExpectedRequirements"
    Assert-True -Condition ($names -contains "FullSutDoc") -Message "s2-clarify.ps1 has an explicit -FullSutDoc legacy switch"
    $text = Get-Content -LiteralPath (Join-Path $scriptsDir "s2-clarify.ps1") -Raw -Encoding UTF8
    Assert-True -Condition ($text -match 'Resolve-AcceptanceRequirementProfile') -Message "S2 resolves the requirement profile from the supplied input"
    Assert-True -Condition ($text -match 'Compare-AcceptanceRequirementSet') -Message "S2 asserts the imported requirement count and FR mapping"
    Assert-True -Condition ($text -match 'requirement-input\.json') -Message "S2 writes requirement-input evidence (path/sha/count/FR IDs)"
    Assert-True -Condition ($text -match 'small_input_version_id') -Message "S2 hands the small-input version to S3"
    Assert-True -Condition ($text -match 'full_sut_doc_version_id') -Message "S2 hands the explicit legacy full-document version to S3"
    Assert-True -Condition ($text -notmatch '共 67 条') -Message "S2 no longer hardcodes the 67-requirement extraction answer"
    Assert-True -Condition ($text -notmatch '58/58') -Message "S2 no longer hardcodes the 58/58 FR assertion"
    Assert-True -Condition ($text -notmatch 'NFR requirements ingested \(9/9\)') -Message "S2 no longer hardcodes the 9/9 NFR assertion"
}

function Test-S3ReusesSmallInputVersion {
    $names = Get-ScriptParameterNames -Path (Join-Path $scriptsDir "s3-design.ps1")
    Assert-True -Condition ($names -contains "RequirementInput") -Message "s3-design.ps1 accepts -RequirementInput"
    Assert-True -Condition ($names -contains "ExpectedRequirements") -Message "s3-design.ps1 accepts -ExpectedRequirements"
    $text = Get-Content -LiteralPath (Join-Path $scriptsDir "s3-design.ps1") -Raw -Encoding UTF8
    Assert-True -Condition ($text -match 'small_input_version_id') -Message "S3 reuses the version imported by S2 in small-input mode"
    Assert-True -Condition ($text -match 'full_sut_doc_version_id') -Message "S3 reuses the explicit legacy full-document version from S2"
    Assert-True -Condition ($text -match 'Compare-AcceptanceRequirementSet') -Message "S3 asserts the design input is exactly the small-input FR set"
    Assert-True -Condition ($text -match 'design-input-requirements\.json') -Message "S3 writes small-input design traceability evidence"
    Assert-True -Condition ($text -notmatch '67/67') -Message "S3 no longer hardcodes the 67/67 traceability check"
    Assert-True -Condition ($text -notmatch 'V1 58 FR') -Message "S3 no longer hardcodes the 58 FR/9 NFR full-document baseline"
    Assert-True -Condition ($text -match 'RequirementInput') -Message "S3 records the supplied requirement input file"
}

# ---------------------------------------------------------------------------
# Fix round 1: the S3 clarification fallback must be mode-aware. Small-input
# mode may only reference the explicit input count/FR set; legacy mode keeps the
# historical generic 4-requirement wording. A post-gate exact-set recheck must
# also exist so a fallback cannot introduce out-of-set requirements.
# ---------------------------------------------------------------------------

function Test-S3SmallModeFallbackReferencesInputSet {
    if (-not (Get-Command Get-AcceptanceRequirementNudgeText -ErrorAction SilentlyContinue)) {
        Assert-True -Condition $false -Message "Get-AcceptanceRequirementNudgeText helper must exist for the mode-aware S3 fallback"
        return
    }
    $profile = Resolve-AcceptanceRequirementProfile -RequirementInput $smallMarkdown -RepoRoot $repoRoot
    foreach ($kind in @("goal", "draft", "confirm")) {
        $text = Get-AcceptanceRequirementNudgeText -Mode $profile.Mode -ExpectedRequirements $profile.ExpectedRequirements -ExpectedFrIds @($profile.ExpectedFrIds) -Kind $kind
        Assert-True -Condition ($text -match '3 条' -and $text -match 'FR-011' -and $text -match 'FR-014' -and $text -match 'FR-025') -Message "S3 small-mode $kind fallback references only the explicit 3-requirement input set"
        Assert-True -Condition ($text -notmatch '4 条|登录、文章、评论、后台管理') -Message "S3 small-mode $kind fallback never asks for the generic 4-requirement drafts"
        Assert-True -Condition ($text -match '不要新增') -Message "S3 small-mode $kind fallback forbids requirements outside the input set"
    }
}

function Test-LegacyFallbackKeepsGenericWording {
    if (-not (Get-Command Get-AcceptanceRequirementNudgeText -ErrorAction SilentlyContinue)) {
        Assert-True -Condition $false -Message "Get-AcceptanceRequirementNudgeText helper must exist for the legacy fallback wording check"
        return
    }
    $text = Get-AcceptanceRequirementNudgeText -Mode "legacy" -ExpectedRequirements 0 -ExpectedFrIds @() -Kind "draft"
    Assert-True -Condition ($text -match '4 条左右') -Message "legacy full-document/standalone mode keeps its historical generic 4-requirement nudge"
}

function Test-S3FallbackAndPostGateGuardWiring {
    $text = Get-Content -LiteralPath (Join-Path $scriptsDir "s3-design.ps1") -Raw -Encoding UTF8
    Assert-Equal -Expected 3 -Actual ([regex]::Matches($text, 'Get-AcceptanceRequirementNudgeText')).Count -Message "S3 builds all three clarification fallback texts from the mode-aware helper"
    Assert-True -Condition ($text -notmatch '4 条左右') -Message "S3 has no hardcoded generic 4-requirement nudge left"
    Assert-True -Condition ($text -match 'requirementsAfterGate' -and $text -match 'post-gate requirement set') -Message "S3 re-reads the live requirement set after the requirement hard gate"
    Assert-True -Condition (([regex]::Matches($text, 'Compare-AcceptanceRequirementSet')).Count -ge 3) -Message "S3 re-runs Compare-AcceptanceRequirementSet after the requirement hard gate"
}

function Test-AcceptanceScriptsParse {
    foreach ($file in @(Get-ChildItem -LiteralPath $scriptsDir -Filter "*.ps1" -File)) {
        $tokens = $null
        $errors = $null
        [void][System.Management.Automation.Language.Parser]::ParseFile($file.FullName, [ref]$tokens, [ref]$errors)
        $message = (@($errors | ForEach-Object { $_.Message }) -join "; ")
        Assert-Equal -Expected 0 -Actual @($errors).Count -Message "$($file.Name) parses without errors $message"
    }
}

# ---------------------------------------------------------------------------
# Runner preflight: a small input with -ExpectedRequirements 67 must fail before
# any scenario (and therefore the stack) is touched; no input must fail too.
# The integration checks are skipped on a RED run where the parameters are not
# declared yet, so the pre-implementation run never invokes the old runner.
# ---------------------------------------------------------------------------

function Invoke-RunAllPreflight {
    param([string[]] $Arguments = @())
    $temp = Join-Path $repoRoot (".tmp\task88-tests\" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Force -Path $temp | Out-Null
    $summaryPath = Join-Path $temp "summary.json"
    $reportPath = Join-Path $temp "report.md"
    $evidenceRoot = Join-Path $temp "evidence"
    $baseArguments = @(
        "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $scriptsDir "run-all.ps1"),
        "-EvidenceRoot", $evidenceRoot,
        "-SummaryPath", $summaryPath,
        "-ReportPath", $reportPath
    )
    $output = @()
    $exit = 1
    # Task 113: the runner now requires explicit acceptance credentials. These
    # tests exercise the requirement-source preflight only, so supply throwaway
    # test values without touching the stack and restore the caller's env.
    $credentialNames = @("EZTEST_ADMIN_PASSWORD", "SUT_PASSWORD")
    $savedCredentials = @{}
    foreach ($name in $credentialNames) {
        $savedCredentials[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
        [Environment]::SetEnvironmentVariable($name, "preflight-test-credential", "Process")
    }
    try {
        try {
            $output = @(& powershell.exe @($baseArguments + @($Arguments)) 2>&1)
            $exit = [int]$LASTEXITCODE
        } finally {
            $summary = $null
            if (Test-Path -LiteralPath $summaryPath) {
                try { $summary = Get-Content -LiteralPath $summaryPath -Raw -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop } catch { $summary = $null }
            }
        }
    } finally {
        foreach ($name in $credentialNames) {
            [Environment]::SetEnvironmentVariable($name, $savedCredentials[$name], "Process")
        }
    }
    return [pscustomobject]@{
        Exit    = $exit
        Output  = @($output)
        Summary = $summary
        Temp    = $temp
    }
}

function Test-RunnerRejectsMissingRequirementInput {
    $names = Get-ScriptParameterNames -Path (Join-Path $scriptsDir "run-all.ps1")
    if (-not ($names -contains "RequirementInput" -and $names -contains "ExpectedRequirements")) {
        Assert-True -Condition $false -Message "run-all.ps1 must declare the small-input profile parameters before the preflight integration check can run"
        return
    }
    $result = Invoke-RunAllPreflight
    try {
        Assert-Equal -Expected 2 -Actual $result.Exit -Message "run-all.ps1 with no requirement input exits 2 (blocked) instead of silently using 67"
        Assert-True -Condition ($null -ne $result.Summary) -Message "the preflight failure writes summary.json"
        if ($null -ne $result.Summary) {
            Assert-Equal -Expected $false -Actual ([bool]$result.Summary.preflight.ok) -Message "the summary records preflight.ok=false"
            Assert-True -Condition ([string]$result.Summary.preflight.error -match 'RequirementInput|FullSutDoc') -Message "the preflight error names the small-input/-FullSutDoc requirement"
            Assert-Equal -Expected 0 -Actual ([int]$result.Summary.totals.passed) -Message "no scenario is counted as passed after a preflight failure"
        }
    } finally {
        Remove-Item -LiteralPath $result.Temp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Test-RunnerRejectsExpectedCountMismatch {
    $names = Get-ScriptParameterNames -Path (Join-Path $scriptsDir "run-all.ps1")
    if (-not ($names -contains "RequirementInput" -and $names -contains "ExpectedRequirements")) {
        Assert-True -Condition $false -Message "run-all.ps1 must declare the small-input profile parameters before the mismatch preflight check can run"
        return
    }
    $result = Invoke-RunAllPreflight -Arguments @("-RequirementInput", $smallMarkdown, "-ExpectedRequirements", "67")
    try {
        Assert-Equal -Expected 2 -Actual $result.Exit -Message "run-all.ps1 rejects a 3-requirement input with -ExpectedRequirements 67"
        Assert-True -Condition ($null -ne $result.Summary) -Message "the count-mismatch preflight writes summary.json"
        if ($null -ne $result.Summary) {
            $preflightError = [string]$result.Summary.preflight.error
            Assert-True -Condition ($preflightError -match '67') -Message "the mismatch error records the requested 67 count"
            Assert-True -Condition ($preflightError -match '3') -Message "the mismatch error records the actual 3 requirements"
            Assert-Equal -Expected 0 -Actual ([int]$result.Summary.totals.passed) -Message "no scenario is counted as passed after the mismatch"
        }
    } finally {
        Remove-Item -LiteralPath $result.Temp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

Test-SmallMarkdownProfile
Test-SmallGherkinProfile
Test-SmallGherkinFallbackTxtProfile
Test-SmallDocxProfile
Test-ExpectedRequirementsMismatchFails
Test-ExpectedFrIdsMismatchFails
Test-MissingInputFails
Test-NoRequirementModeFails
Test-BothRequirementModesFail
Test-FullDocumentProfile
Test-RequirementSetComparisonPassesOnExactSmallSet
Test-RequirementSetComparisonFailsOnCountAndIds
Test-RunnerDeclaresSmallInputProfile
Test-S1DeclaresAndRecordsRequirementInput
Test-S2UsesSmallInputOnly
Test-S3ReusesSmallInputVersion
Test-S3SmallModeFallbackReferencesInputSet
Test-LegacyFallbackKeepsGenericWording
Test-S3FallbackAndPostGateGuardWiring
Test-AcceptanceScriptsParse
Test-RunnerRejectsMissingRequirementInput
Test-RunnerRejectsExpectedCountMismatch

Write-Host ""
Write-Host ("small requirement input tests: {0} checks, {1} failure(s)" -f $script:checks, $script:failures)
if ($script:failures -gt 0) { exit 1 }
exit 0
