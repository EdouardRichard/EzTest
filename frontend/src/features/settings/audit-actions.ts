/**
 * Static audit action candidates shown in the AuditPanel datalist.
 *
 * This is the admin-facing suggestion list for the audit filter, not an
 * allowlist: the list filter accepts any action, and unknown names still match
 * by prefix. There is deliberately no candidate endpoint. The backend keeps the
 * canonical catalog in `backend/internal/audit/domain/actions.go`, and its
 * `TestFrontendCatalogMatchesBackend` test fails if this list drifts. Keep
 * entries in the same order as the backend catalog.
 */
export type AuditAction = {
  name: string
  label: string
  aliases: string[]
}

export const AUDIT_ACTIONS: AuditAction[] = [
  { name: "auth.login", label: "登录", aliases: [] },
  { name: "auth.login_failed", label: "登录失败", aliases: [] },
  { name: "auth.logout", label: "退出登录", aliases: [] },
  { name: "auth.password_changed", label: "修改密码", aliases: [] },

  { name: "user.created", label: "创建用户", aliases: [] },
  { name: "user.updated", label: "更新用户", aliases: [] },
  { name: "user.password_reset", label: "重置用户密码", aliases: [] },

  { name: "settings.updated", label: "更新系统设置", aliases: ["setting.update"] },

  { name: "llm.config_created", label: "创建 LLM 配置", aliases: [] },
  { name: "llm.config_updated", label: "更新 LLM 配置", aliases: [] },
  { name: "llm.config_deleted", label: "删除 LLM 配置", aliases: [] },
  { name: "llm.config_refreshed", label: "刷新模型目录", aliases: [] },
  { name: "llm.model_added", label: "新增模型", aliases: [] },
  { name: "llm.model_updated", label: "更新模型", aliases: [] },
  { name: "llm.model_deleted", label: "删除模型", aliases: [] },
  { name: "llm.model_params_reset", label: "重置模型参数", aliases: [] },

  { name: "resource.sut.created", label: "创建被测系统", aliases: [] },
  { name: "resource.sut.updated", label: "更新被测系统", aliases: [] },
  { name: "resource.sut.deleted", label: "删除被测系统", aliases: [] },
  { name: "resource.environment.created", label: "创建环境", aliases: [] },
  { name: "resource.environment.updated", label: "更新环境", aliases: [] },
  { name: "resource.environment.deleted", label: "删除环境", aliases: [] },
  { name: "resource.page.created", label: "创建页面", aliases: [] },
  { name: "resource.page.updated", label: "更新页面", aliases: [] },
  { name: "resource.page.deleted", label: "删除页面", aliases: [] },
  { name: "resource.page.proposed", label: "提议页面", aliases: [] },
  { name: "resource.page.confirmed", label: "确认页面", aliases: [] },
  { name: "resource.page.rejected", label: "拒绝页面", aliases: [] },
  { name: "resource.api.created", label: "创建接口", aliases: [] },
  { name: "resource.api.updated", label: "更新接口", aliases: [] },
  { name: "resource.api.deleted", label: "删除接口", aliases: [] },
  { name: "resource.api.proposed", label: "提议接口", aliases: [] },
  { name: "resource.api.confirmed", label: "确认接口", aliases: [] },
  { name: "resource.api.rejected", label: "拒绝接口", aliases: [] },
  { name: "resource.database.created", label: "创建数据库", aliases: [] },
  { name: "resource.database.updated", label: "更新数据库", aliases: [] },
  { name: "resource.database.deleted", label: "删除数据库", aliases: [] },
  { name: "resource.account.created", label: "创建账号", aliases: [] },
  { name: "resource.account.updated", label: "更新账号", aliases: [] },
  { name: "resource.account.deleted", label: "删除账号", aliases: [] },
  { name: "resource.code_repo.created", label: "创建代码仓库", aliases: [] },
  { name: "resource.code_repo.updated", label: "更新代码仓库", aliases: [] },
  { name: "resource.code_repo.deleted", label: "删除代码仓库", aliases: [] },
  { name: "resource.sut_repository.created", label: "关联代码仓库", aliases: [] },

  { name: "version.created", label: "创建版本", aliases: [] },
  { name: "version.updated", label: "更新版本", aliases: [] },
  { name: "version.reopened", label: "重开版本", aliases: [] },
  { name: "version.closed", label: "关闭版本", aliases: [] },
  { name: "version.binding.changed", label: "变更版本绑定", aliases: [] },
  { name: "version.code_baseline_unresolved", label: "代码基线未解析", aliases: [] },
  { name: "version.stage_decision", label: "阶段决策", aliases: [] },
  { name: "version.stage_gate_denied", label: "阶段门禁拒绝", aliases: [] },

  { name: "requirement.created", label: "创建需求", aliases: [] },
  { name: "requirement.updated", label: "更新需求", aliases: [] },
  { name: "requirement.confirmed", label: "确认需求", aliases: [] },
  { name: "requirement.deduped", label: "需求去重", aliases: [] },
  { name: "requirement.deleted", label: "删除需求", aliases: [] },
  { name: "requirement.merged", label: "合并需求", aliases: [] },
  { name: "requirement.obsoleted", label: "废弃需求", aliases: [] },
  { name: "requirement.rolled_back", label: "回滚需求", aliases: [] },
  { name: "requirement.rollback_proposal_used", label: "采用回滚提案", aliases: [] },
  { name: "requirement.relation_created", label: "创建需求关系", aliases: [] },
  { name: "requirement.closed", label: "关闭需求", aliases: [] },
  { name: "requirement.reopened", label: "重开需求", aliases: [] },

  { name: "agent.profile.updated", label: "更新 Agent Profile", aliases: [] },
  { name: "agent.profile.tool_denied", label: "Profile 工具调用被拒", aliases: [] },
  { name: "agent.role.tool_denied", label: "角色工具调用被拒", aliases: [] },
  { name: "agent.run.finished", label: "Agent 运行完成", aliases: [] },
  { name: "agent.run.failed", label: "Agent 运行失败", aliases: [] },
  { name: "job.canceled", label: "取消任务", aliases: [] },
  { name: "job.requeued", label: "重排任务", aliases: [] },

  // Actions recorded by other contexts. They are not part of the backend audit
  // package's own emitters but are valuable in the admin filter; keep the order
  // in sync with the backend mirror.
  { name: "agent_profile.backfilled", label: "回填 Agent Profile", aliases: [] },
  { name: "quality.eval_recorded", label: "记录质量评估", aliases: [] },
  { name: "case.updated", label: "更新用例", aliases: [] },
  { name: "execution.credentials_read", label: "读取执行凭证", aliases: [] },
  { name: "visual.diff.create", label: "创建视觉差异", aliases: [] },
  { name: "content.uploaded", label: "上传内容", aliases: [] },
]
