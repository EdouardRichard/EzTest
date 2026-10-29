# P8 备份/恢复演练记录（Task 29 · fix round 1）

- 验收时间：2026-10-29 15:43:22–15:43:54 +08:00
- 仓库/分支：`D:\Project_new\EzTest` / `dev`
- 演练时 HEAD：`e76c30b81d63bf266e93d1970194ae929c3b9bcb`（manifest 记录值；该 HEAD 含并行 P8 任务提交，本任务 fix commit 在演练之后）
- 环境：Windows + Windows PowerShell 5.1.26100；便携 PostgreSQL 17（`.tools/pgsql/bin`，`localhost:5433`）；数据库 `eztest`（24 张 public 表、共 65 行，dump 87,475 字节）；内容目录使用确定性 fixture（主用例）与本机真实 `backend/.data`（默认布局用例）
- 结论：**PASS**。`scripts/test-backup.ps1` **175/175** 断言通过，exit 0；备份、恢复、逐文件 SHA-256、逐表行数、覆盖 round-trip、不稳定行数 advisory、路径穿越拒绝、篡改检测均符合预期。

## 1. 演练命令与覆盖范围

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test-backup.ps1
```

fix round 1 新增/扩展的覆盖：

- 默认目录解析：相对 `EZTEST_*_DIR` 按 API 工作目录 `<repo>/backend` 解析（真实数据在 `backend/.data/*`）；在五个真实目录播种文件，备份必须包含非零文件且恢复逐文件校验通过；
- 配置目录缺失：默认失败并提示 `-AllowMissingDirs`；传该开关后 `Write-Warning` 并记录 `status=missing`；
- `-AllowAnyDatabase` 不再绕过安全守卫：源库、共享测试库即使带 `-AllowAnyDatabase` 仍拒绝；系统库始终拒绝；
- 覆盖安全分支：非空目标库、非空备份 `-TargetDir`、`-Force -ConfirmOverwrite` 覆盖 round-trip；
- manifest 目录 label 路径穿越（`..\..\p8-evil-<ts>`）在创建目录/`tar -C` 之前拒绝；
- `rowCountsStable=false` 的行数差异只写 warning/report，不算恢复失败；
- `-Force` 下 extra tables 的语义（`pg_restore --clean` 只删除 dump 中存在的对象）；
- 数据库 URL query 参数映射到 `PG*` 环境变量（默认布局用例使用 `application_name`/`connect_timeout`）。

脚本默认在 `.tmp/p8-backup` 工作：fixture 覆盖五个内容目录（嵌套证据、空子目录、二进制对象、`.env`/`.secrets` 诱饵），真实 `eztest` 库仍作为数据库来源。

## 2. RPO / RTO

| 项目 | 结论 |
| --- | --- |
| RPO | **按需备份，无 PITR**。没有 WAL 归档/流复制，最近一次成功备份之后的数据不可恢复；manifest 的 `rowCountsStable=true` 表示本次 dump 前后行数一致。 |
| RTO 目标 | **≤ 30 分钟**（预建空库/空目录 → 恢复 → 校验 → 应用启动） |
| 本机实测恢复 | 恢复命令整体墙钟 **0.99 s**（含 manifest/归档 SHA-256 校验、`pg_restore` **0.2 s**、tar 解包、逐文件 SHA-256、24 表行数比对、报告写入）；临时库建库本身 < 1 s |
| 本机实测备份 | 整体墙钟 **1.53 s**（manifest 内部计时 **1.11 s**，`pg_dump` **0.6 s**） |
| 整轮演练 | **31.57 s**（21 次 backup/restore 子进程调用、临时库建/删、默认布局播种与恢复；并行任务占用 CPU/IO 时耗时会上浮） |

> 这是 24 表 / 87 KB dump / 5 个 fixture 文件（默认布局 29 个文件）的本机口径，不代表生产规模；生产 RTO 需按真实数据量与存储重新演练。

## 3. 校验结果

### 3.1 备份 manifest 与载荷

- `manifest.json`：`formatVersion=1`；记录 `dev @ e76c30b`、备份时间、耗时、`contentBase.absolute=D:\Project_new\EzTest\backend`（`relative=backend`）、6 个 artifact（1 个 DB dump + 5 个目录 tar）的相对路径/大小/SHA-256、5 个内容文件的大小/SHA-256、24 张表的行数、6 个配置键名（只有键名）、`database.parameters=["sslmode"]`、排除模式。
- 数据库 dump：87,475 字节，SHA-256 `562d183b12444260fcd8bc404b711107a32c4fc57d5a95e68780562318ddcbd5`。
- 五个 fixture 目录归档均存在且 SHA-256 与 manifest 一致（evidence 4,096 B / reports 2,560 B / exports 2,560 B / version-attachments 2,560 B / objects 3,584 B）；归档中**没有** `.env`、`.env.*`、`.secrets`、`secrets` 成员。
- 真实 `.env` 中的密码/密钥值未出现在任何备份文件中（测试逐字节扫描 4 个 secret 值）；备份树没有 `.env` 文件。

### 3.2 恢复与一致性

| 校验项 | 期望 | 结果 |
| --- | --- | --- |
| 数据库 dump/归档 SHA-256 | 与 manifest 一致 | 6/6 PASS |
| 恢复表数/行数 | 24 表逐表一致（共 65 行） | 24/24 PASS，0 mismatch |
| 恢复文件 | 5/5 存在、大小与 SHA-256 一致 | 5/5 PASS |
| 关键文件内容 | evidence sentinel 内容逐字节一致 | PASS |
| 空子目录 | 恢复后仍存在 | PASS |
| `.env`/`.secrets` 诱饵 | 不在备份、也不在恢复目录 | PASS |
| `restore-report.json` | `passed=true`、`files.verified=5`、`database.rowCountsMatch=true`、`rowCountsStable=true`、`rowCountsAdvisory=false`、`secrets.included=false` | PASS |

### 3.3 覆盖、不稳定行数与默认布局

- **覆盖 round-trip**：在主恢复完成后向目标库加入 `p8_extra_table`，再以 `-Force -ConfirmOverwrite` 对同一非空库/非空目录重放恢复——exit 0、`passed=true`；报告 `database.extraTables=["p8_extra_table"]`、`extraTablesAdvisory=true` 且含 warning；该表在恢复后仍存在（`pg_restore --clean` 只删除 dump 中存在的对象）。
- **不稳定行数**：复制 manifest 并设置 `rowCountsStable=false`、把一张表期望行数 +1000；恢复到新库后 exit 0、`passed=true`、`rowCountsMatch=false`、`rowCountsAdvisory=true`，warning 说明行数比较仅作参考。
- **默认布局**：不传目录覆盖、URL 追加 `application_name=eztest_p8_backup_drill&connect_timeout=10`，在 `backend/.data/{evidence,reports,exports,version-attachments,objects}` 各播种 1 个文件。备份成功：五个目录 `resolvedPath` 均为 `backend\.data\<label>`，archive 均非零（3,072/3,072/65,024/3,072/3,072 字节），共 **29** 个文件（5 个播种文件 + 24 个已有 exports 文件），`database.parameters=["sslmode","application_name","connect_timeout"]`。恢复到临时库 PASS：报告 `passed=true`，29/29 文件 SHA-256 一致，24 表行数一致；播种文件内容哈希一致。演练后播种文件与临时目录已清理。

### 3.4 安全拒绝与失败检测

| 用例 | 结果 |
| --- | --- |
| 目标库名不以 `_test`/`_e2e` 结尾且无 `-AllowAnyDatabase` | 非 0 退出，提示 allowlist 与 `-AllowAnyDatabase` |
| 目标目录非空 | 非 0 退出，提示必须为空 |
| 只给 `-Force`、缺少 `-ConfirmOverwrite` | 非 0 退出，提示需要第二次确认 |
| 目标库非空（含 1 个 user object） | 非 0 退出，提示 not empty |
| 备份 `-TargetDir` 非空 | 非 0 退出，未写 manifest |
| 目标库不存在 | 非 0 退出，且未创建 `TargetRoot` |
| 共享测试库 `eztest_test` | 非 0 退出；**加 `-AllowAnyDatabase` 仍拒绝** |
| 源库 `eztest` + `-AllowAnyDatabase` | 非 0 退出，提示源库守卫 |
| 系统库 `postgres` + `-AllowAnyDatabase` | 非 0 退出，提示系统库守卫 |
| 配置目录缺失且无 `-AllowMissingDirs` | 非 0 退出，未写 manifest，提示 `-AllowMissingDirs` |
| 配置目录缺失 + `-AllowMissingDirs` | exit 0，warning，manifest `status=missing` |
| manifest 中 dump SHA-256 被篡改 | 非 0 退出，**在触碰目标前**报告 integrity 失败 |
| manifest 目录 label 为 `..\..\p8-evil-<ts>` | 非 0 退出，提示 unsupported directory label；未创建 TargetRoot，未在 `.tmp` 外创建 `p8-evil-*` 目录 |
| 恢复后篡改一个文件再 `-VerifyOnly` | 非 0 退出，报告 `passed=false` 并列出 SHA-256/大小不匹配 |

## 4. 应用级验证（未执行，defer Task 35）

本次演练**没有**在恢复后用恢复库启动 API 做 `/healthz`/`/api/health` 等 health/关键端点 smoke，即**未做应用级验证**。原因：fix round 1 执行期间并行 P8 任务正在改动 `backend/`（codeintel/quality 等）且工作树一度无法编译；本任务门禁不能依赖其它任务的在途代码。恢复结果目前只验证到 SQL 行数与文件 SHA-256 一级。该项 defer 到 **Task 35**（验收/端到端汇总）执行：用恢复出的临时库 + 恢复目录启动 API（临时端口），至少 smoke `/healthz`、`/api/health` 与 `/api/openapi.yaml`。

## 5. 密钥丢失说明

备份**不包含** `.env`、数据库密码、管理员密码、`llm_key` 或 `EZTEST_SECRET_KEYS`。

- `EZTEST_SECRET_KEYS` 是版本号 → base64 32 字节 AES-256 key 的 JSON；后端用它加密 `llm_configs` 的 LLM API Key 与自定义 headers（AES-256-GCM）。
- **如果 `EZTEST_SECRET_KEYS` 丢失且没有独立副本，数据库备份中的已加密 LLM 配置不可恢复**（不是“恢复失败”，而是密文永久无法解密），只能重新录入 LLM Key/Headers。
- 恢复后需运维另行提供：`EZTEST_DATABASE_URL`（含密码）、`EZTEST_SECRET_KEYS`、`EZTEST_SECRET_KEY_ACTIVE`；管理员密码仅用于全新空库首次创建管理员，恢复出的库通常已含管理员账号。

## 6. 已知限制

1. **无 PITR/WAL**：只能恢复到最近一次成功备份。
2. 备份是整库 + 整目录 tar：无增量、无去重、无内置加密；备份目录按业务数据同等权限保护。
3. 备份期间若有进程写入内容目录，归档可能是该文件某一时刻的字节；哈希以实际归档内容为准。建议低写入窗口备份。
4. `rowCountsStable=false` 时行数比较仅作 advisory（不影响恢复 `passed`），本次演练主用例为 `true`。
5. 恢复不创建数据库/角色，需预建空库；不自动执行备份之后新增的迁移（应用启动时按既有迁移机制处理，跨大版本前需评估）。
6. 演练期间有并行任务在 `dev` 上提交/写入数据库；每轮演练内部 `rowCountsStable` 均为 `true`，不同轮次行数/哈希会变化，属预期。
7. 本机 RTO 数据不能直接外推到生产；生产必须用真实数据量、真实磁盘和真实密钥走一遍完整恢复。
8. 恢复端 `-Force` 只覆盖备份中存在的对象/文件，不删除目标中多余的表/文件（报告会列为 extraTables/unexpected/warning）。
9. 使用 `-AllowMissingDirs` 时缺失目录以空目录形式恢复；不校验“本应有数据但为空”，运维需自行确认配置正确。
10. **未做应用级验证**（见第 4 节），defer Task 35。

## 7. 证据（gitignored，本地）

| 路径 | 内容 |
| --- | --- |
| `.tmp/p8-backup/backup/manifest.json` | 主用例备份 manifest（contentBase、解析路径、commit、哈希、行数、参数名） |
| `.tmp/p8-backup/restore-report.json` | 主恢复校验报告（`passed=true`、24 表、5 文件） |
| `.tmp/p8-backup/overwrite-report.json` | `-Force -ConfirmOverwrite` round-trip 报告（extra table advisory） |
| `.tmp/p8-backup/unstable-restore-report.json` | `rowCountsStable=false` advisory 报告 |
| `.tmp/p8-backup/default-layout-backup/manifest.json` | 真实 `backend/.data` 默认布局 manifest（29 文件、resolvedPath、URL 参数） |
| `.tmp/p8-backup/default-layout-restore-report.json` | 默认布局恢复报告（`passed=true`、29/29 文件） |
| `.tmp/p8-backup/test-report.json` | 演练报告（175/175 断言、耗时、默认布局摘要、应用级验证 defer 说明） |
| `.tmp/task29-fix-final.log` | 最终完整命令输出（175/175，backup 1.53 s / restore 0.99 s） |
