> **0.1 归档；0.2 权威文档见 [docs/version-V0.2/](version-V0.2/README.md) 与 [deploy/README.md](../deploy/README.md)。**

# EzTest 运维手册

> 本文件汇总 P8 运维手册：备份/恢复与密钥（Task 29）、证据 GC（Task 28）、多实例单活（Task 30）、限流/SLO（Task 27）、审计/安全（Task 20/32）与故障处理/日志（Task 33 汇总）。
> 各章节按主题维护；部署步骤见 [部署指南](deployment.md)，不要把本文件当部署清单使用。

## 备份与恢复

### 目标与口径

- **备份内容**：PostgreSQL 业务库（`pg_dump -Fc` 自定义格式）、证据/报告/导出/附件/对象五个内容目录（tar 归档）、`manifest.json`。
- **不备份内容**：`.env`、`.env.*`、`.secrets/`、`secrets/`，以及任何私钥/密码/`EZTEST_SECRET_KEYS`。备份目录只包含业务数据与清单，不包含用于解密业务数据的密钥。
- **RPO（恢复点目标）**：**按需备份，没有 PITR**。没有 WAL 归档/流复制，两次成功备份之间写入的数据不可恢复；生产环境应按业务可接受丢失窗口制定定时备份（例如每日一次 + 重要变更前一次）。详见 `docs/acceptance/p8/backup-drill.md` 的实测记录。
- **RTO（恢复时间目标）**：目标 **≤ 30 分钟**（新建空库/空目录 → 恢复 → 校验 → 应用启动）。实际耗时随数据库体积、磁盘与内容目录大小增长；本机演练数据见 `docs/acceptance/p8/backup-drill.md`。
- **一致性**：`pg_dump` 是数据库的一致快照；manifest 在 dump 前后各统计一次 public 表行数，两次不一致时记录 `rowCountsStable=false`，提示备份期间有并发写入。恢复时 `rowCountsStable=false` 的行数比较**仅作 advisory**：差异写入 `restore-report.json` 的 warnings/`database.rowCountsAdvisory`，不使恢复判定失败（dump 本身仍是一致快照）；需要精确对账时应在低写入窗口重新备份。缺表/行数变化在 `rowCountsStable=true` 时仍参与 `passed` 判定；文件 SHA-256 校验始终参与。`-Force` 覆盖时目标库中不属于备份的 extra tables 作为 advisory 写入报告（见“执行恢复”）。

### 执行备份

命令（在仓库根目录执行；PowerShell 5.1 或 7+）：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/backup.ps1 `
  -TargetDir D:\eztest-backups\2026-10-29-1530
```

- `-TargetDir` 必须不存在或为空目录；脚本拒绝向非空目录写入，避免新旧备份混在一起。
- 数据库连接默认读取 `.env` 的 `EZTEST_DATABASE_URL`，密码只通过 `PGPASSWORD` 传给客户端工具，不打印、不写入 manifest。可用 `-DatabaseUrl` 显式指定其它源库。URL 中的 query 参数（如 `sslmode`、`connect_timeout`、`application_name`）会映射为对应的 libpq `PG*` 环境变量传给 `pg_dump`/`psql`；不支持的参数直接报错（不会静默丢弃 TLS/连接设置）。manifest 只记录参数名（`database.parameters`），不记录值。
- 内容目录默认读取 **API 工作目录** `<repo>/backend` 下的相对路径（API 由 `scripts/dev-api.ps1` 从 `backend/` 启动，`.env.example` 的默认值即 `backend/.data/*`）：
  - `EZTEST_EVIDENCE_DIR`（默认 `.data/evidence` → `backend/.data/evidence`）
  - `EZTEST_REPORT_DIR`（默认 `.data/reports` → `backend/.data/reports`）
  - `EZTEST_EXPORT_DIR`（默认 `.data/exports` → `backend/.data/exports`）
  - `EZTEST_VERSION_ATTACHMENT_DIR`（默认 `.data/version-attachments` → `backend/.data/version-attachments`）
  - `EZTEST_STORAGE_DIR`（默认 `.data/objects` → `backend/.data/objects`）
  也可用 `-EvidenceDir`、`-ReportDir`、`-ExportDir`、`-AttachmentDir`、`-ObjectDir` 临时覆盖；相对值同样按 `<repo>/backend` 解析。建议生产使用绝对路径，避免工作目录变化导致备份到错误位置（manifest 的 `contentBase` 与每个目录的 `resolvedPath` 记录实际解析结果）。
- 配置目录不存在时默认**失败**，因为通常意味着目录基准配置错误（正是历史上把相对路径按仓库根解析导致的问题）。确需允许缺失时传 `-AllowMissingDirs`：脚本输出 `Write-Warning`，manifest 记录 `status: "missing"`（恢复时创建空目录并给出 warning）。
- `.env` 不在内容目录中，因此不会被 tar；若某个配置目录等于仓库根或其祖先，脚本直接拒绝（防止把 `.env` 打进归档）。

备份目录结构：

```text
<target>/
  manifest.json
  db/<database>.dump            # pg_dump -Fc
  directories/evidence.tar
  directories/reports.tar
  directories/exports.tar
  directories/version-attachments.tar
  directories/objects.tar
```

`manifest.json` 关键字段：

| 字段 | 含义 |
| --- | --- |
| `formatVersion` | 清单格式版本（当前 1） |
| `repository.commit` / `branch` / `version` | 备份时的 git commit、分支、`git describe` |
| `contentBase.absolute` / `relative` | 相对内容目录的解析基准：API 工作目录 `<repo>/backend` 的绝对路径，及其仓库相对表示 `backend` |
| `database.parameters` | 由 `EZTEST_DATABASE_URL` query 参数映射到 `PG*` 环境变量的参数名列表（只有名字，没有值） |
| `database.dumpFile` / `sizeBytes` / `sha256` | 数据库 dump 的相对路径、大小、SHA-256 |
| `database.rowCounts` / `rowCountsStable` | public 表逐表行数；dump 前后是否一致 |
| `directories[].configKey` / `configured` / `source` / `resolvedPath` | 内容目录对应的配置键名、原始配置值（env/覆盖/默认）、来源、实际解析后的绝对路径 |
| `directories[].status` / `archive` / `sha256` / `files[]` | 目录状态（archived/missing）、归档路径、归档哈希，以及逐文件相对路径/大小/SHA-256 |
| `configKeys` | 参与备份的配置键名（**只有键名，没有值**） |
| `excluded` | 明确排除的 `.env`/密钥路径模式 |
| `summary` | 归档目录数、缺失目录数、文件数、字节数 |

哈希口径：manifest 中所有 `sha256` 都是对相应文件/归档**字节内容**计算的 SHA-256（64 位小写十六进制）。dump 与归档哈希用于发现备份完成后的损坏/篡改；`files[]` 逐文件哈希用于恢复后确认恢复内容与备份一致。`manifest.json` 本身不含自身哈希，其可信度依赖备份目录的访问控制与传输校验（见“已知限制”）。

脚本退出码：`0` 成功并写出 `manifest.json`；非 `0` 失败，失败时不得使用目标目录进行恢复（可能只有部分文件，且没有有效 manifest）。

### 执行恢复

前置条件：

1. 目标 PostgreSQL 实例已存在，且已用 `createdb` 新建**空数据库**。恢复脚本不会创建数据库或角色；也不要恢复进生产库。
2. 目标内容根目录不存在或为空。
3. 运维已单独提供 `.env` 与密钥（见下一节）；恢复脚本不会也**无法**从备份中还原密钥。
4. 目标库的 PostgreSQL 主版本与备份时一致或更新（`pg_dump` 自定义格式建议使用同版本/更新版本的 `pg_restore`）。

命令示例：

```powershell
# 1) 新建空库（密码通过 PGPASSWORD 或交互输入，不要写进命令行历史）
createdb -h localhost -p 5433 -U eztest eztest_restore_20261029_test

# 2) 恢复数据库 + 内容目录，并输出校验报告
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/restore.ps1 `
  -BackupDir D:\eztest-backups\2026-10-29-1530 `
  -TargetDatabase eztest_restore_20261029_test `
  -TargetRoot D:\eztest-restore\2026-10-29-1530
```

- 目标库名默认必须是 `*_test` 或 `*_e2e`。以下库**始终拒绝**，`-AllowAnyDatabase` 也不能绕过：`.env` 中的源库名、`EZTEST_TEST_DATABASE_URL` 指向的共享测试库、`postgres`/`template0`/`template1`。`-AllowAnyDatabase` 只放宽“目标库名必须以 `_test`/`_e2e` 结尾”这一条（危险操作，不能覆盖源库/共享测试库）。
- 非空目标库/目录会被拒绝。确需覆盖时必须同时给出 `-Force -ConfirmOverwrite`（第二次确认使用显式开关，避免在 CI/无人值守环境卡在 stdin）。`-Force` 恢复数据库时使用 `pg_restore --clean --if-exists`；该参数**只删除 dump 中存在的对象**，目标库中多出来的表不会被删除。覆盖是显式确认过的场景，这些 extra tables 会写入 `restore-report.json` 的 `database.extraTables`/warnings，但**不计入**恢复失败；未加 `-Force` 时目标库是空库，若恢复后仍出现多表则视为异常并使校验失败。内容目录采用覆盖写入，同样**不会删除**目标中不在备份里的多余文件（报告列在 `files.unexpected`/warnings 中）。
- 恢复前会先校验 manifest 记录的所有归档大小与 SHA-256、检查 tar 成员无绝对路径/`..`，并校验内容目录 label 必须是 `evidence`、`reports`、`exports`、`version-attachments`、`objects` 之一。任何一项不通过都不会改动目标（非法 label 不会创建目录或执行 `tar -C`）。
- 恢复的检查顺序是先确认目标库存在且为空，再创建 `-TargetRoot`；目标库不存在时不会留下新的空目录。
- 恢复使用 `pg_restore --no-owner --no-privileges`：对象归执行恢复的数据库用户所有，不依赖原实例的角色/授权。
- 恢复完成后逐文件校验 SHA-256，并逐表比较行数，写出 `restore-report.json`（默认在 `-TargetRoot` 下）。文件缺失/哈希不符时脚本以非 `0` 退出，报告 `passed: false`。若备份记录的 `rowCountsStable=false`，行数差异仅写成 warning/`database.rowCountsAdvisory=true`，**不影响** `passed`（dump 仍是一致快照）；行数完全一致时 `database.rowCountsMatch=true`。

恢复后复核（不改动目标）：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/restore.ps1 `
  -BackupDir D:\eztest-backups\2026-10-29-1530 `
  -TargetDatabase eztest_restore_20261029_test `
  -TargetRoot D:\eztest-restore\2026-10-29-1530 `
  -ReportPath D:\eztest-restore\verify-report.json `
  -VerifyOnly
```

恢复判定为成功后，再切换应用连接串/启动服务。若校验失败，不要把目标库/目录当作可用恢复结果。

### 密钥管理与 `EZTEST_SECRET_KEYS` 丢失影响

- 备份**不包含** `.env`、数据库密码、管理员密码、`llm_key` 与 `EZTEST_SECRET_KEYS`。这些必须由运维使用独立的密码库/密钥管理系统保存，并与备份分开存放（备份泄露不应导致密钥泄露，密钥丢失也不应依赖备份找回）。
- `EZTEST_SECRET_KEYS` 是 JSON 对象：`{ "<版本号>": "<base64 编码的 32 字节 AES-256 key>" }`；`EZTEST_SECRET_KEY_ACTIVE` 指定当前加密使用的版本。后端用该 keyring 加密 `llm_configs.api_key` 与自定义 headers（AES-256-GCM，字段绑定 AAD）。
- **`EZTEST_SECRET_KEYS` 丢失且没有其它副本时，已加密的 LLM Key/Headers 不可恢复**。数据库备份里只有密文，没有密钥；只能重新录入 LLM 配置。同理，轮换时旧版本 key 必须保留到所有密文重新加密完成，否则旧密文不可解密。
- 恢复后至少需要运维另行提供：`EZTEST_DATABASE_URL`（含密码）、`EZTEST_SECRET_KEYS`、`EZTEST_SECRET_KEY_ACTIVE`，以及按需的 `EZTEST_ADMIN_*`（仅用于全新空库首次创建管理员；恢复出的库通常已有管理员，`EnsureAdmin` 不会覆盖）、`llm_*` 种子与执行器 token 等其它环境配置。
- 任何脚本与文档都不得打印密码/密钥；备份目录含业务数据，应按生产数据同等权限保护（例如 ACL/加密卷/对象存储服务端加密），本脚本不提供备份加密。

### 已知限制

- **无 PITR/WAL 归档**：只能恢复到最近一次成功备份的时间点。
- 备份为整库 + 整目录：无增量、无去重、无内置加密/压缩策略（tar 仅打包）；大库需自行估算磁盘与传输时间。
- 内容目录是 tar 整包；如果备份期间有进程正在写入某个大文件，可能得到该文件的中间状态（哈希以实际读到的内容为准），建议在低写入窗口备份。
- 恢复不创建数据库/角色，也不自动执行备份之后新增的迁移；应用启动时会按既有迁移机制补迁移，跨大版本恢复前应先确认兼容性。
- `rowCountsStable=false` 时行数校验只作 advisory（dump 仍是一致快照），不会让恢复失败；应在低写入窗口重新备份以获得可精确对账的 manifest。
- 本机演练使用便携 PostgreSQL 17 与本地磁盘，RTO 实测不代表生产规模；生产需按实际数据量做恢复演练。
- **未做应用级验证**：本任务只验证到 SQL 行数与文件 SHA-256 一级，没有在恢复后用恢复库启动 API 做 `/healthz`、`/api/health` 等 health/关键端点 smoke。该项 defer 到 Task 35（原因与范围见 `docs/acceptance/p8/backup-drill.md`）。
- `-TargetDir`/`-BackupDir`/`-TargetRoot`/`-ReportPath` 等脚本路径参数按仓库根解析（也支持绝对路径）；只有内容目录配置（`.env` 与 `-EvidenceDir` 等覆盖）按 `<repo>/backend` 解析。两套基准不要混淆。

## 相关脚本与证据

| 脚本/文件 | 用途 |
| --- | --- |
| `scripts/backup.ps1` | 生成数据库 dump、目录 tar、`manifest.json` |
| `scripts/restore.ps1` | 安全恢复 + 逐文件 SHA-256/行数校验 + `restore-report.json`；`-VerifyOnly` 复核 |
| `scripts/test-backup.ps1` | 本机端到端演练（`.tmp/p8-backup`），失败非零退出并清理临时库/目录 |
| `docs/acceptance/p8/backup-drill.md` | 演练记录（RPO/RTO、校验结果、密钥说明） |

## 证据 GC 与保留

> 设计依据：规格 §10.2/D65。当前章节覆盖**执行证据文件**（`EZTEST_EVIDENCE_DIR`）的保留与清理。规格 §10.2 的“对象”还列出导出、附件、报告文件与无 DB 行的孤儿文件；P8 **未**自动清理这些对象，已按实现能力明确 re-scope（见“已知限制”与规格 §10.2 实施注记），UI 文案也只承诺执行证据文件。

### 配置

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `EZTEST_GC_ENABLED` | `false` | 是否允许**非 dry-run** 清理。`false` 时 `POST /api/admin/gc` 的 `{dryRun:false}` 返回 409 `conflict`；dry-run 始终可用。生产需显式开启。 |
| `EZTEST_GC_RETENTION_DAYS` | `30` | 未引用证据的保留天数（1–3650）。只有 `created_at` 早于该窗口的未清除证据才可能成为候选。 |
| `EZTEST_GC_BATCH_KEEP` | `3` | 每个版本最近 N 批（按 `batch_no` 降序）的证据始终受保护（1–50）。 |

### API

`POST /api/admin/gc`（admin-only，需 Bearer token 与管理员角色）：

```json
{ "dryRun": true }
```

响应（共享 `data` 信封）：

```json
{
  "data": {
    "dryRun": true,
    "candidates": 12,
    "protected": 48,
    "bytes": 7340032,
    "deleted": 0,
    "failed": 0,
    "skipped": 0,
    "retentionDays": 30,
    "batchKeep": 3,
    "candidateLimit": 10000,
    "runAt": "2026-10-29T08:00:00Z"
  }
}
```

- `dryRun` 缺省/`true`：只统计候选与受保护数量、候选字节数，**不读文件内容、不删除、不写 purged 元数据**；仍写一条审计（`dry_run=true`）。
- `dryRun:false`：在 `EZTEST_GC_ENABLED=true` 时逐个删除候选；否则 409。
- 单次运行最多处理 `candidateLimit`（当前固定 10000）个候选；`candidates` 等于该值时说明可能还有剩余，再次运行即可继续清理（按 `created_at,id` 升序推进）。
- 管理端“设置 → 基础配置”提供 dry-run 预览与二次确认的执行按钮；普通用户无此页面，调用该端点返回 403。

### 保护集（绝不删除）

1. 运行中/计划中的批次（`run_batch.status IN ('planned','running')`）的全部证据。
2. 每个版本最近 `EZTEST_GC_BATCH_KEEP` 批的证据。
3. 未关闭问题（`issue.status IN ('open','confirmed')`）通过 `issue_ref`（`ref_kind='evidence'`）引用的证据。`issue_ref.ref_id` 是十进制字符串，保护 SQL 统一用 `ref_id = evidence.id::text` 比较，避免 bigint 转换越界，也保证候选列表与删除前复核口径一致。
4. 视觉基线证据：GC 保护 `evidence.kind='visual_baseline'` 的行。审批视觉基线只会把断言指向一条**已存在**的 `screenshot` 或 `visual_baseline` 证据（`case_visual_assertion.baseline_evidence_id`），不会新建或复制证据，也不改变证据 kind；当前生产没有写入 `kind='visual_baseline'` 的 producer（executor 比对产生的是 `visual_actual`/`visual_diff`），因此实际被审批的基线通常是执行截图。
5. 已清除证据（`purged_at IS NOT NULL`）不会再次成为候选；删除前复核也会将其视为受保护。
6. 最近 N 批以外的候选仍会在删除前**再次**执行引用校验（`IsProtected`），避免候选列出后新建立的引用被误删。

#### 报告引用不在保护集（fix round 1 修正）

P8 首版保护集宣称“已发布报告引用的证据受保护”，但该 claim 是空的：报告快照 `eztest.report.v1` 只携带 `evidenceCount` 计数，**不携带也不引用 evidence id**；报告仓库的 `report_ref` 生产写入只覆盖 `batch/case_run/issue/requirement/commit`，从不写 `ref_kind='evidence'`。`evidence_ref` 的生产写入也只有 `case_run/step_result/assertion_result`，没有 `report/issue`。因此本实现移除了以下空保护分支（SQL、文档、UI 同步）：

- `report_ref(ref_kind='evidence')` + `report.status='published'`（发布报告）；
- `report.status='stale'` 同理也不保护（stale 只是 published 的失效状态，同样不引用 evidence id）；
- `evidence_ref(ref_kind='report'|'issue')`（无生产写入）。

目前只保护真实存在的引用类型：最近 N 批、运行中批次、未关闭问题 `issue_ref`、`visual_baseline`、已清除行。如果未来让报告显式引用证据（新增快照字段或发布时写 `report_ref(ref_kind='evidence')`），必须同时恢复保护 SQL，并补一条“报告服务生成 → GC 保护”的真实 PostgreSQL 集成测试。

### 删除顺序与安全

对每个候选：

1. 引用复核：命中保护集 → 跳过（`skipped`），不删除。
2. 读取证据文件并复算 SHA-256/大小；与 `evidence.sha256/size_bytes` 不一致 → 跳过（`skipped`），**不删除、不写 purged 标记**。文件已缺失（`fs.ErrNotExist`）同样按跳过处理：行已无文件可删，保留元数据供人工核对，不计为失败（避免 nightly 告警噪声）。
3. 写 `evidence.purged_at` / `purged_reason`（保留行与全部元数据）。`purged_reason` 支持 Unicode（中文等按原样保留；换行/控制字符转空格或 `_`，最多 120 个字符），便于运维用母语记录原因。
4. 删除文件。一旦第 3 步提交，后续删除与回滚使用**脱离请求取消的有界后台 context**（`context.WithoutCancel` + 30s 超时）：客户端断开或请求超时都不会把行留在“已标记但文件仍在”的状态；删除失败会再次用新的有界后台 context 回滚 purged 标记（`failed`），失败原因只写日志。
5. 每次运行写一条审计 `execution.evidence.gc`：只含计数、阈值、dry-run 标志、候选上限与操作者，不含路径、sha256 或证据内容。审计是 **best-effort**：审计 sink 故障只记日志，不会把已完成的 GC 反报为失败。

`purged_reason` 形如 `gc: retention=30d batch_keep=3; reason=<受限操作员备注>`。证据行保留，因此历史 case/step 关联、计数和审计仍可追溯；读取已清除证据会得到“文件缺失”错误，不会读取旧字节。

### 手动执行示例

```powershell
# 1) 只预览（任何环境可用）
$body = @{ dryRun = $true } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:13333/api/admin/gc `
  -Headers @{ Authorization = "Bearer $env:EZTEST_ADMIN_TOKEN" } `
  -ContentType application/json -Body $body

# 2) 显式开启后执行（生产需变更管理审批）
$env:EZTEST_GC_ENABLED = "true"   # 重启 API 生效
$body = @{ dryRun = $false; reason = "monthly retention" } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:13333/api/admin/gc `
  -Headers @{ Authorization = "Bearer $env:EZTEST_ADMIN_TOKEN" } `
  -ContentType application/json -Body $body
```

`scripts/backup.ps1` 备份的是**当前仍在磁盘上的**证据；GC 删除不可逆，执行前应确保相应备份/保留策略已覆盖业务要求。

### 已知限制

- **对象范围 re-scope（P8）**：当前自动清理只覆盖**执行证据文件**（`EZTEST_EVIDENCE_DIR`）。规格 §10.2 列出的导出文件、版本附件、报告 Markdown 文件，以及“无 DB 行的孤儿文件”**不在 P8 自动删除范围**；这些对象缺少足够的 DB 引用/保护元数据做安全判定，明确留给 P9/后续任务（规格 §10.2 已加实施注记，UI 文案同步只承诺执行证据文件）。
- 报告（`published`/`stale`）不参与证据保护：报告快照只有证据计数、没有 evidence id，`report_ref(ref_kind='evidence')` 无生产写入（详见“报告引用不在保护集”）。这是行为边界，不是配置项。
- 文件缺失但元数据行仍存在的证据会被反复列为候选并跳过；需要人工核对 `purged_at`/文件系统后决定修复或手工清理，P8 不自动改写这类行。
- 单次运行最多处理 10000 个候选（响应 `candidateLimit`）；超大队列需要多次运行。
- 定时维护任务已接线：每日一次、leader 持有、默认只 dry-run（见下一节「多实例单活」）；管理员仍可手动 `POST /api/admin/gc`。
- 证据行保留但文件已删；前端/API 读取此类证据会报文件缺失，应在报表/审计中结合 `purged_at` 解释。
- 本机验收仅覆盖单元测试与 env-gated PostgreSQL 集成测试；生产首次执行前应在目标环境先跑 dry-run，并核对 `candidates`/`protected`。

## 多实例单活（leader 锁）与每日维护

> 设计依据：规格 §10.2/§10.5/D67。多个 API 实例共享同一个 PostgreSQL 时，执行调度 tick、自主 worker tick 与每日证据维护通过会话级 advisory lock 单活；单实例部署行为不变。

### 配置

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `EZTEST_INSTANCE_ID` | `hostname-pid` | 实例标识，出现在启动/退出日志与锁 debug 日志；最长 64 字符。多副本部署建议显式设置唯一值。 |
| `EZTEST_LEADER_LOCK_KEY` | `1163411512` | advisory lock 基础 key（int64）；所有实例必须一致。三个循环派生出相邻 key：调度 = `base`、自主 worker = `base+1`、每日维护 = `base+2`，因此不同循环互不阻塞，而同一循环的多个实例互斥。 |

### 行为

- 每个 tick 先 `pg_try_advisory_lock(key)`：获得锁的实例执行该 tick，结束时 `pg_advisory_unlock` 并归还连接；未获锁的实例跳过该 tick，仅记 debug 日志（不改变已落库状态）。
- 锁是会话级的：进程崩溃/连接断开后 PostgreSQL 自动释放；优雅退出时 tick 结束即释放，不会把锁留给下一个实例。
- 单实例（或未配置锁的测试）行为不变：每个 tick 都执行。
- 启动/退出日志都带 `instance_id`；启动日志同时记录 `leader_lock_key`。锁日志不含任何密钥。
- 调度/自主 tick 的锁只覆盖"本轮扫描与派发"，不覆盖长跑的 case runner；case 运行的互斥由既有 run/lease CAS 保证。

### 每日维护

- 进程启动后立即执行一次，之后每 24 小时一次；仅 leader 实例执行。
- 默认 dry-run（不删除文件、不写 purged 标记，只记录候选/受保护/字节数）；设置 `EZTEST_GC_ENABLED=true` 后该定时任务改为真实清理（与 `POST /api/admin/gc` 的开关一致）。
- 完成日志只含计数、保留天数与批次保护数，不含证据路径/sha256/内容。
- 验证锁：`SELECT pid, classid, objid FROM pg_locks WHERE locktype = 'advisory' AND objid IN (<base>, <base+1>, <base+2>);`（仅持有锁的 tick 期间可见）。
- 已知边界：advisory lock 依赖所有实例连接同一个 PostgreSQL；跨外部存储/多租户编排不在 P8 范围。SSE 广播与 Agent runner 仍为进程内单实例。

## 限流与 SLO

> 设计依据：规格 §10.1/D64。限流是**进程内**内存令牌桶（每实例独立），不是网关级全局限流；多副本部署时实际允许速率约为“实例数 × 配置值”，需要按实例预算合计。

### 配置与行为

| 变量 | 默认 | 范围 | 行为 |
| --- | --- | --- | --- |
| `EZTEST_RATE_LIMIT_RPS` | `50` | 0–1000 | 每用户（认证后）/每 IP（公开路由）每秒补充令牌；`0` 关闭普通桶（SSE 并发上限仍生效）。 |
| `EZTEST_RATE_LIMIT_BURST` | `100` | 1–5000 | 桶容量/突发。 |
| `EZTEST_RATE_LIMIT_SSE_MAX` | `10` | 1–100 | 每用户/IP 并发 SSE 流；超限立即 429，提示客户端关闭旧流。 |

- 超限返回 **429**，错误码 `rate_limited`，`Retry-After` 为整数秒；`details` 含 `scope`（`user`/`ip`）、`reason`（`requests_per_second`/`sse_concurrency`）、`limit`、`retry_after_seconds`。
- `X-Request-ID` 仍照常回显；限流命中日志包含 path/scope/reason/limit/retry_after，**不含 token、用户 ID 明文或请求体**。
- 豁免：`/healthz`、`/api/health`、`/api/openapi.yaml`、`/favicon.ico`、`/assets/`、`/static/`、`/_next/` 与 CORS 预检；SSE 心跳不计入普通桶。
- SSE 判定：`GET` 且路径为 `/api/stream`、`/api/stream/events` 或 `Accept: text/event-stream`；连接结束（含 panic）释放槽位。
- 限流是单实例行为；多副本要按“每个实例”分别配置容量，或用外部网关做全局额度。P8 不做分布式限流。

### SLO 口径与当前状态

| 测量项 | 阈值 | 当前状态 |
| --- | --- | --- |
| 版本总览首屏 API P95 | ≤ 2500 ms | `scripts/slo.ps1 -DryRun` 静态 PASS；真实测量 **SKIP**（未运行，Task 35 最终验收执行）。 |
| 版本总览浏览器 navigation timing P95 | ≤ 2500 ms | 同上，真实浏览器测量 SKIP。 |
| 探索链路端点 P95（200 节点/400 边） | ≤ 2500 ms（专项脚本使用 800 ms 本地阈值） | `scripts/slo-chain.ps1` **PASS**：API P95 92.2 ms、画布可交互 1109 ms（本机口径，2026-10-29，见 `docs/acceptance/p8/slo-chain.md`）。 |
| 批次 8 并发执行 | 0 错误 | 依赖 fixture executor/会话 harness；`scripts/slo.ps1` 在缺失时记 SKIP，未实跑。 |
| 代码变更集 API P95 | ≤ 2000 ms | `scripts/slo.ps1` 真实测量 SKIP。 |
| 代码分析非模型开销 | 无固定 P8 门禁 | 由评测/验收脚本报告，未作为硬门禁。 |

命令：

```powershell
# 静态预检（不需要 API/浏览器；会写 docs/acceptance/p8/slo.md 的 SKIP 报告）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 -DryRun

# 本机 SLO 测量（需运行中的 API；Web/Playwright 可选）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 `
  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://127.0.0.1:6868

# 链路专项（200 节点/400 边；需要 API + Web + DB）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo-chain.ps1 `
  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://127.0.0.1:6868 `
  -DatabaseUrl $env:EZTEST_DATABASE_URL
```

报告与说明：

- `docs/acceptance/p8/slo.md`：`scripts/slo.ps1` 的输出；当前为 `STATIC-CHECK-PASSED-MEASUREMENTS-SKIPPED`，**不是 SLO PASS**。
- `docs/acceptance/p8/slo-chain.md`：链路专项 PASS 记录（92.2 ms / 1109 ms）。
- SLO 是**本地验收口径，不是生产 SLA**；生产必须在目标网络、目标数据量与真实浏览器上重测。
- SKIP 永远不等于 PASS；脚本在测量到超阈值时以非 0 退出。

## 审计与安全

### 审计查看器

- 入口：管理员登录 → `/settings` → **审计日志** Tab；对应 API `GET /api/admin/audit`（admin-only）。
- 过滤：`actor`（用户 ID）、`action`（精确动作名）、`from`/`to`（RFC3339，含边界）；游标分页，按时间倒序。
- 导出：前端把**已加载**的条目导出为 CSV/Markdown（浏览器端生成，不新增服务端导出端点）；导出内容同样不含密钥/凭据正文。
- 审计记录字段：actor、action、target、request_id、IP、user-agent、非敏感 metadata、时间；不记录密码、token、storageState、LLM key、SQL 结果、证据正文、prompt 或知识正文。

### 已覆盖的写操作（P8）

| 域 | 动作示例 |
| --- | --- |
| 认证/用户 | `auth.login_succeeded`、`auth.login_failed`、`auth.logout`、`user.created`、`user.updated`、`user.password_reset` |
| LLM/存储 | `llm.config_created/updated/deleted`、`llm.test_succeeded/failed`、`storage.object_created` |
| 版本/需求 | `version.created/updated/continued/completed`、`version.requirement_created/updated/deleted/confirmed/reopened` |
| 脑图/评审 | `mindmap.case_created/updated/deleted`、`mindmap.case_reordered`、`mindmap.steps_updated`、`case.export.created`、`mindmap.tool.invoked` |
| 执行/GC | `execution.evidence.gc`、`agent.tool.invoked`、执行/接管相关动作；GC 审计为 best-effort（sink 故障只记日志，不回滚已完成的清理）。 |
| 交付 | `delivery.issue.created`、`delivery.analysis.started/finished`、报告发布/导出相关动作 |
| 资源/Session | `resource.*` 导入/校验/接管/DB 目标等动作 |

> `case.baseline_frozen` 与 `case.visual_baseline_approved` 是版本事件，不是审计动作；mindmap 评审批准链路（`ReviewDeps`）当前没有 audit sink。
>
> 动作名是持久化契约，稳定不改；新增写操作应沿用 `<context>.<verb>` 并补测试/文档。

### 安全头与边界

- Next 下发 `X-Content-Type-Options: nosniff`、`Referrer-Policy: strict-origin-when-cross-origin`、`X-Frame-Options: DENY`、`Permissions-Policy` 与 **`Content-Security-Policy-Report-Only`**；CSP 尚不强制。
- API 响应不带这些头；生产由反向代理统一补 `HSTS`/CSP/头（见 [部署指南](deployment.md#5-反向代理tls-与安全头)）。
- 已知边界：token 存 `sessionStorage`，无 HttpOnly 会话；TLS/反代由部署方负责；LLM `base_url` 信任管理员；Access/Session 凭据与 DB 目标密码按既有决策明文存储，但 API 不回显、不入日志/事件/证据/prompt。
- 权限矩阵与秘密扫描结果见 `docs/acceptance/p8/security-audit.md`；脚本 `scripts/security-audit.ps1`（`-SkipLive` 可离线执行）。

## 密钥与凭据轮换

### `EZTEST_SECRET_KEYS` 轮换

`EZTEST_SECRET_KEYS` 是 `{ "<版本号>": "<base64 32 字节密钥>" }`，`EZTEST_SECRET_KEY_ACTIVE` 指定**新加密**使用的版本。解密会尝试 keyring 中所有版本，因此旧密文仍可读。

推荐步骤（先在测试环境演练）：

1. 生成新密钥（不要复用旧值）：
   ```powershell
   $bytes = New-Object byte[] 32
   [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
   [Convert]::ToBase64String($bytes)
   ```
2. 在 `EZTEST_SECRET_KEYS` 中**追加**新版本（如 `"2"`），旧版本保留；把 `EZTEST_SECRET_KEY_ACTIVE` 改为新版本；滚动重启 API。
3. 重新录入需要由新 key 加密的 LLM 配置：当前没有批量重加密工具；管理员在设置页编辑配置并重新填写 `llm_key`/headers，才会用 active key 重新加密。留空 key 表示保留旧密文。
4. 用 `/api/health` 的 `llm_configured=true`、LLM 连通性测试和一次真实调用验证解密/加密都正常。
5. 确认没有密文仍依赖旧版本后，才能从 keyring 移除旧版本；**移除前务必保持备份**。移除后再回滚将无法解密旧数据。

要点：

- 备份不包含 keyring；keyring 丢失且无其它副本时，已加密的 LLM Key/headers **不可恢复**，只能重新录入。
- 轮换不等于撤销：旧 token/密码策略见下节；不要把 keyring 写进 `.env.example`、工单或聊天记录。
- 轮换期间新旧实例可能短暂并存；所有实例必须拿到包含新旧版本的同一 keyring，否则旧实例无法解密新密文。

### 其它凭据

- **管理员密码**：通过用户管理重置；旧 token 会被撤销。种子密码仅首次空库创建时使用。
- **executor token**：API 与 executor 必须同时替换 `EZTEST_EXECUTOR_TOKEN` 并重启两者；轮换窗口内执行批次会失败，选择无批次运行时操作。
- **数据库密码**：按 PostgreSQL 常规流程轮换；同步更新 `.env`/`POSTGRES_PASSWORD` 与 `EZTEST_DATABASE_URL`，滚动重启 API。恢复脚本不会创建角色/数据库。
- **LLM Key**：在设置页更新；API/DTO/审计/日志/事件均不回显。审计只记录“测试成功/失败”等动作。
- **Session/Access 凭据**：按 P2 的导入/失效/撤销流程处理；不提供查看原文的 API。

## 日志与故障处理

### 日志

- 输出到 stdout/stderr，结构化字段含 `request_id`、method/path/status、耗时与错误；级别由 `EZTEST_LOG_LEVEL` 控制（`debug`/`info`/`warn`/`error`）。
- 不记录密码、token、LLM key、storageState、SQL 结果集、证据正文、prompt、知识正文或文件系统路径（错误信封也不泄漏）。
- Compose：`docker compose --env-file .env -f deploy/docker-compose.yml logs -f api`；生产建议接日志驱动/采集。
- 关键启动日志：`instance_id`、`leader_lock_key`、`env`、迁移结果；退出日志同样带 `instance_id`。
- 限流命中是 `warn`；leader 未获锁是 `debug`；GC/维护完成日志只有计数/阈值。

### 故障排查顺序

1. **看健康**：`GET /healthz`（进程）→ `GET /api/health`（DB/LLM/限流/GC 参数）。
2. **看日志**：按 `request_id` 关联；先排除 DB、迁移、端口、CORS、executor。
3. **看权限**：401 = 未认证/过期；403 = 非管理员（或知识编辑者规则）；409 = 状态冲突/GC 未开启；422 = 字段校验；429 = 限流/队列；503 = DB/LLM/executor 不可用。
4. **看数据**：`purged_at` 解释证据缺失；报告 stale 看 `input_fingerprint` 与证据计数；问题/报告 revision CAS 冲突需刷新后重试。
5. **不要手改数据库**：先备份；迁移只追加；报告/证据/审计的修复优先走应用流程或恢复演练。

### 常见问题

| 问题 | 判断/处理 |
| --- | --- |
| API 启动即失败，提示迁移校验和不匹配 | 历史迁移被修改；不要改已应用迁移，新增迁移修正或从备份恢复。 |
| `/api/health` 503、`db=down` | PG 未启动/连接串错误/连接池耗尽；查 `pg_isready`、连接数、密码/sslmode。 |
| 浏览器 401/403 | token 过期或角色不足；管理员接口需 admin；知识编辑仅创建者/admin。 |
| 429 `rate_limited` | 看 `details.reason` 与 `Retry-After`；调 RPS/BURST/SSE_MAX 或降低并发；多实例按实例合计。 |
| SSE 断线/无消息 | 反代缓冲/超时；关闭 buffering、放宽 read timeout、用 HTTP/1.1；确认心跳 20s。 |
| GC 409 | `EZTEST_GC_ENABLED=false`；先 dry-run，生产显式开启并重启。 |
| GC 一直有候选但删不掉 | 文件缺失/哈希不符/受保护引用；按设计跳过，人工核对 `purged_at` 与磁盘。 |
| 报告 stale | 证据/失败分类计数变化或升级后首次读取；确认后重新生成（新 revision），不要改旧 revision。 |
| 执行批次失败、executor unavailable | executor 容器/端口/token/Chromium 资源；查 executor `/healthz` 与 API 日志。 |
| 用例以 `inconclusive(model, tool_call_budget_exhausted)` 结束 | 该 case run 的 Agent 工具调用超过 `EZTEST_EXEC_MAX_TOOL_CALLS`（默认 `30`，1–200）；提高预算或简化用例。这是 model 分类，不会记为 `failed`。 |
| WebSocket 被拒绝（`host_not_allowed`） | executor 的 `EZTEST_EXEC_ALLOWED_HOSTS` 默认仅 `localhost,127.0.0.1`，且 WS 还需通过 context `allowedHosts`；远程目标需在 executor 进程显式加入 host。HTTP 导航/请求不受该白名单影响。 |
| LLM 503 `llm_not_configured` | 默认 LLM 缺失/key 解密失败；设置页重配并测试；核对 keyring active 版本。 |
| 知识未注入 | 仅 `approved` 条目会注入；draft/rejected 不注入。检查条数/字节预算：P5 design 运行的 `stats.knowledgeRefs` 有结构化 ID 列表，P3 澄清/分析/侦察只在提示词中带 `knowledgeRefs:` 行；截断时查 `knowledge_truncated` 日志。 |
| 审计查不到某个动作 | 该路由可能未接审计；以 `docs/acceptance/p8/security-audit.md` 的覆盖矩阵为准，新增写路径需补审计。 |

## P8 已知限制与交付状态

- **Docker/Compose 未实跑**：镜像未构建、栈未启动；只有静态校验。首次部署按 [部署指南](deployment.md) 验证。
- **视觉断言（D62）已接线，真实浏览器验收待 Task 35**：迁移、mindmap 编辑/审批、`main/config` 装配、executor `visual.compare`、`execution.visual_assert`、四态分类、前端编辑/审批/查看器与评测语料均已提交；两条 `/visual` 路由已挂载，`scripts/eval.ps1 -DryRun` 会重放视觉语料并产出 `visual_classification_accuracy`。仍待真实 Chromium 的 Go→executor 端到端与浏览器验收（Task 34/35）；`EZTEST_VISUAL_DIFF_THRESHOLD` 尚未读取，阈值按断言配置。不要写成“真实环境已验收”。
- **SLO**：`scripts/slo.ps1` 目前只有 dry-run 静态 PASS；真实全量测量待最终验收；链路专项已 PASS（本机口径）。
- **备份恢复**：DB 行数 + 文件 SHA-256 已演练；恢复库上的应用级 smoke 未做（defer Task 35）。
- **GC 范围**：只自动清理 `EZTEST_EVIDENCE_DIR` 下证据；导出、附件、报告 Markdown、无 DB 行孤儿文件不自动删除（见上文“证据 GC 与保留”）。
- **报告保护**：报告快照不含 evidence id，`report_ref(ref_kind='evidence')` 无生产写入，故发布/过期报告不参与证据保护；未来引入引用时必须恢复保护 SQL + 真实 PG 集成测试。
- **多实例**：只做单活锁与执行器硬化；SSE/Agent runner 仍进程内；限流按实例内存计数；无多实例运维编排（P9）。
- **权限**：仍是单管理员 + 普通用户两层，无多租户/RBAC；token 存 `sessionStorage`。
- **迁移**：只追加、无 down migration；回退依赖备份恢复。
- **配置**：`EZTEST_ANALYSIS_MAX_CASES`（1–50，默认 20）与 `EZTEST_SUPPLEMENT_MAX_CASES`（1–20，默认 5）已在 P8 接线；`EZTEST_EXEC_MAX_TOOL_CALLS` 默认 30（1–200），executor 的 WS 白名单 `EZTEST_EXEC_ALLOWED_HOSTS` 默认仅 loopback；`EZTEST_AUTONOMY_BATCHES_PER_TICK` 默认 4，>20 钳制为 20，<1/非整数启动失败。`EZTEST_CHAIN_MAX_NODES`/`EZTEST_SEARCH_MAX_RESULTS_PER_TYPE`/`EZTEST_TODOS_MAX` 仍未接线为环境变量（使用代码默认/请求上限）。以 [发布说明](release-notes/p8.md) 为准。

## 相关文档

- [部署指南](deployment.md)
- [用户手册](user-guide.md)
- [开发指南](development.md)
- [P8 发布说明](release-notes/p8.md)
- [备份演练记录](acceptance/p8/backup-drill.md)
- [安全审计记录](acceptance/p8/security-audit.md)
- [链路 SLO 记录](acceptance/p8/slo-chain.md)
- [SLO 静态报告](acceptance/p8/slo.md)
