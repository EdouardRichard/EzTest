# EzTest 运维手册

> 本文件由 P8 任务按章节补充。当前章节：备份/恢复与密钥管理（Task 29）。
> GC、SLO、多实例单活、安全审计与故障处理章节由对应任务追加，勿在本文件重复维护同一主题。

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
