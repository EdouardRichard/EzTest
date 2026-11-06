#requires -Version 5.1
<#
.SYNOPSIS
    Build the EzTest v0.22 FA input isolation package (FA-1).

.DESCRIPTION
    Physically copies section 1 (R01-R12, 41 ACs) of
    docs/version-V0.22/acceptance/typical-requirements.md into the FA evidence
    tree, replacing the internal copy instructions with one neutral preface.
    Section 2 is never copied and is never parsed into the output.

    Outputs (untracked, docs/ is git-ignored):
      docs/version-V0.22/acceptance/evidence/final/inputs/typical-section1.md
      docs/version-V0.22/acceptance/evidence/final/inputs/manifest.json
      docs/version-V0.22/acceptance/evidence/final/inputs/ops-manual.md

    The script is a producer only: scripts/v022-fa-gate.ps1 -Gate inputs is the
    read-only authority that decides PASS/FAIL.

.NOTES
    Run from the repository root:
        powershell.exe -NoProfile -ExecutionPolicy Bypass `
            -File scripts\v022-fa-inputs.ps1
#>

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = New-Object System.Text.UTF8Encoding($false)

. (Join-Path $PSScriptRoot 'v022-fa-common.ps1')

$sourcePath = Join-Path $repoRoot 'docs\version-V0.22\acceptance\typical-requirements.md'
$inputsDir = Join-Path $repoRoot 'docs\version-V0.22\acceptance\evidence\final\inputs'
$sectionOut = Join-Path $inputsDir 'typical-section1.md'
$manifestOut = Join-Path $inputsDir 'manifest.json'
$manualOut = Join-Path $inputsDir 'ops-manual.md'

if (-not (Test-Path -LiteralPath $sourcePath)) {
    throw "missing source: $sourcePath"
}
New-Item -ItemType Directory -Force -Path $inputsDir | Out-Null

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$forbiddenPattern = '第\s*2\s*节|第2节|9\s*已实现|3\s*未实现|内部对照|预期未实现|trap'
$preface = '本次需求输入共 12 条（R01–R12），共 41 个验收标准（AC）；任一 AC 的 Then/And 未满足即判该 AC 失败并记录 observed/expected。'

# --- locate section 1 without copying section 2 ----------------------------
$lines = [System.IO.File]::ReadAllLines($sourcePath, [System.Text.Encoding]::UTF8)
$start = -1
$end = -1
for ($i = 0; $i -lt $lines.Count; $i++) {
    if ($start -lt 0) {
        if ($lines[$i] -match '^##\s+1\.\s+验收输入集') { $start = $i }
    }
    elseif ($lines[$i] -match '^##\s+2\.') {
        $end = $i
        break
    }
}
if ($start -lt 0) { throw 'section 1 heading not found in typical-requirements.md' }
if ($end -lt 0) { throw 'section 2 heading not found; refusing to copy an unbounded section' }

$section = New-Object System.Collections.Generic.List[string]
for ($i = $start; $i -lt $end; $i++) {
    $line = $lines[$i]

    # Normalize the heading: the source adds a copy-instruction suffix that is
    # not part of the neutral input set.
    if ($i -eq $start) {
        $section.Add('## 1. 验收输入集')
        continue
    }

    # Replace the internal copy-instructions blockquote with one neutral line.
    if ($line -match '^\s*>\s*\*\*复制范围\*\*') {
        $section.Add('> ' + $preface)
        while (($i + 1) -lt $end -and $lines[$i + 1] -match '^\s*>') { $i++ }
        continue
    }

    # Defense in depth: never emit an internal marker line even if the source
    # layout changes.
    if ($line -match $forbiddenPattern) { continue }
    if ($line -match '复制范围|期望\s*requirement\s*数') { continue }

    $section.Add($line)
}

while ($section.Count -gt 0 -and [string]::IsNullOrWhiteSpace($section[$section.Count - 1])) {
    $section.RemoveAt($section.Count - 1)
}
$sectionText = ($section -join "`r`n") + "`r`n"

# --- validate the copied set before writing --------------------------------
$reqIds = @(
    [regex]::Matches($sectionText, '(?m)^###\s+(R\d{2})\b') |
        ForEach-Object { $_.Groups[1].Value }
)
$acIds = @(
    [regex]::Matches($sectionText, '(?m)^-\s+\*\*(AC-R\d{2}-\d{2})\b') |
        ForEach-Object { $_.Groups[1].Value }
)
if ($reqIds.Count -ne 12 -or @($reqIds | Sort-Object -Unique).Count -ne 12) {
    throw "section 1 must contain exactly 12 unique requirement headings, got $($reqIds.Count)"
}
if ($acIds.Count -ne 41 -or @($acIds | Sort-Object -Unique).Count -ne 41) {
    throw "section 1 must contain exactly 41 unique ACs, got $($acIds.Count)"
}
if (-not $sectionText.Contains($preface)) {
    throw 'neutral preface was not inserted'
}
$leaks = @([regex]::Matches($sectionText, $forbiddenPattern,
        [System.Text.RegularExpressions.RegexOptions]::IgnoreCase))
if ($leaks.Count -gt 0) {
    throw "copied section still contains $($leaks.Count) internal marker(s)"
}

[System.IO.File]::WriteAllText($sectionOut, $sectionText, $utf8NoBom)

# --- ops manual ------------------------------------------------------------
$manual = @'
# EzTest v0.22 FA 验收操作手册（ops manual）

本手册与 `typical-section1.md` 是验收 Agent 的全部输入。请只使用这两个文件，不要读取第 6 节"禁读清单"中的任何内容。

## 1. FA 栈入口

| 组件 | 地址 |
|---|---|
| EzTest API | http://127.0.0.1:13460 |
| EzTest Web | http://127.0.0.1:6880 |
| EzTest executor | http://127.0.0.1:13360 |

- API 健康检查：`GET http://127.0.0.1:13460/api/health/ready`，期望 HTTP 200。
- 登录账号：`admin`。
- `admin` 密码：从运行环境仓库根目录 `.env` 的 `EZTEST_ADMIN_PASSWORD` 读取；禁止把密码写入日志、截图、报告或任何证据文件。
- 所有验收动作必须通过真实浏览器页面或产品 API 完成；禁止直接写数据库、直接调用内部服务或绕过产品流程。

## 2. EzTest 操作步骤

1. **登录**：打开 web 入口，使用 `admin` 登录，确认页面进入已登录状态。
2. **建 SUT**：在系统/资源设置中登记被测系统（SUT），填写前端与后端仓库信息，保存后确认 SUT 记录可见。
3. **环境**：为 SUT 配置可访问环境（SUT 前台/后台 URL、数据库与 Redis 连接、executor 地址）；用健康检查或探活操作确认可达。
4. **账号**：登记验收使用的 SUT 账号（作者/管理员等），确认账号可用于后续登录。
5. **建版**：通过建版向导创建版本，绑定 SUT、环境、账号与仓库，确认版本进入可设计状态。
6. **上传需求**：把 `typical-section1.md` 作为唯一需求输入上传；等待抽取完成。
7. **确认需求**：确认抽取结果恰为 12 条 requirement（R01–R12）、41 个 AC；不得新增、合并、拆分或遗漏。
8. **设计**：进入设计阶段，生成并确认测试用例/测试设计；核对每个 AC 至少映射到一个用例。
9. **执行**：启动 executor 执行；观察版本阶段推进，直到版本状态为 `completed`；记录失败项与证据。
10. **报告**：打开/导出验收报告，核对 12 条 requirement 与 41 个 AC 的结论、截图和证据路径。

## 3. 缺陷分级与处置（P0/P1/P2）

- **P0（阻断）**：主流程不可用、数据损坏/丢失、安全或权限泄漏、证据伪造、需求被错误标记为通过。必须立即修复；修复后重跑失败步骤与受影响阶段；P0 未清零不得判 PASS。
- **P1（重要）**：影响可用性、正确性或验收结论，但不阻断主流程。尽量修复；无法在本轮修复的，登记 `deferred` 并写明原因、影响、临时规避和残余风险，由用户签字批准；`open` 不算有结论。
- **P2（一般）**：体验、文案、样式或非阻断问题。登记并排期，不阻断本轮 PASS。

## 4. 截图要求

- 每个阶段至少 1 张截图；
- 视口固定 1440×900，使用 `fullPage: true` 截取整页；
- PNG 必须非空白：宽高 ≥800×600，采样颜色数 ≥8；
- 文件命名 `<阶段>-<序号>-<短描述>.png`，并在报告/台账中给出相对路径。

## 5. 需求与 AC 台账

- `typical-section1.md` 恰含 12 条输入（R01–R12），因此必须创建且只创建 12 个 requirement；
- 恰含 41 个验收标准（AC）；逐 AC 记录 `status`、`observed`、`expected` 与证据路径；
- 未实现的需求必须判为 gap/blocked 并记录证据，不得编造通过；
- 台账中的 requirement/AC 集合必须与输入完全一致，不得增删改。

## 6. 禁读清单

验收 Agent 不得读取、搜索、引用或把以下内容作为输入：

- `docs/superpowers/specs/**`、`docs/superpowers/plans/**`
- `PROGRESS.md`、`ANCHOR.md`、`README.md`
- `docs/version-V0.22/acceptance/full-flow-acceptance.md` §7
- `docs/version-V0.22/acceptance/typical-requirements.md` §2（只允许使用本输入包中的第 1 节副本）
- `docs/version-V0.2.1/**`
- `docs/version-V0.22/acceptance/reviews/**`
- 旧 evidence：`docs/version-V0.22/acceptance/evidence/w0/**`、`w1/**`、`w2/**`，以及 `evidence/final/` 下非本任务产物
- 任何标注"禁止 AI 阅读"或"禁止验收 Agent 阅读"的文件

## 7. 输入完整性

- 输入包只包含 `typical-section1.md` 与本手册；`manifest.json` 仅用于校验输入文件 SHA256，不包含实现状态。
'@

[System.IO.File]::WriteAllText($manualOut, $manual.TrimEnd() + "`r`n", $utf8NoBom)

# --- manifest --------------------------------------------------------------
$manifest = [pscustomobject][ordered]@{
    source         = 'docs/version-V0.22/acceptance/typical-requirements.md'
    sha256         = (Get-Sha -Path $sectionOut)
    requirements   = $reqIds.Count
    acs            = $acIds.Count
    requirementIds = @($reqIds)
    acIds          = @($acIds)
    generatedAt    = (Get-Date).ToString('o')
}
Write-JsonUtf8 -Path $manifestOut -Object $manifest

Write-Host ("input package: {0} requirements / {1} ACs, sha256={2}" -f `
        $reqIds.Count, $acIds.Count, $manifest.sha256)
Write-Host ("typical-section1.md -> {0}" -f $sectionOut)
Write-Host ("ops-manual.md      -> {0}" -f $manualOut)
Write-Host ("manifest.json      -> {0}" -f $manifestOut)
