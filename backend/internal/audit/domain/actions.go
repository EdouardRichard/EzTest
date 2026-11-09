// Package audit is the audit context's domain model. Besides the append-only
// entry, it owns the catalog of action names: the persisted names are historical
// and stable, so renames are expressed as aliases instead of rewrites.
package audit

import "strings"

// Action is one audited operation: its persisted name, the admin-facing label
// and any historical or alternate names accepted by the list filter.
type Action struct {
	Name    string
	Label   string
	Aliases []string
}

// Actions is the curated audit action catalog. It covers the actions emitted by
// auth, user, settings, llm, resource, version and agent. Recording code keeps
// using string literals; this catalog is the discovery/filter surface, and the
// frontend candidate list mirrors it (see actions_test.go).
var Actions = []Action{
	{Name: "auth.login", Label: "登录"},
	{Name: "auth.login_failed", Label: "登录失败"},
	{Name: "auth.logout", Label: "退出登录"},
	{Name: "auth.password_changed", Label: "修改密码"},

	{Name: "user.created", Label: "创建用户"},
	{Name: "user.updated", Label: "更新用户"},
	{Name: "user.password_reset", Label: "重置用户密码"},

	{Name: "settings.updated", Label: "更新系统设置", Aliases: []string{"setting.update"}},

	{Name: "llm.config_created", Label: "创建 LLM 配置"},
	{Name: "llm.config_updated", Label: "更新 LLM 配置"},
	{Name: "llm.config_deleted", Label: "删除 LLM 配置"},
	{Name: "llm.config_refreshed", Label: "刷新模型目录"},
	{Name: "llm.model_added", Label: "新增模型"},
	{Name: "llm.model_updated", Label: "更新模型"},
	{Name: "llm.model_deleted", Label: "删除模型"},
	{Name: "llm.model_params_reset", Label: "重置模型参数"},

	{Name: "resource.sut.created", Label: "创建被测系统"},
	{Name: "resource.sut.updated", Label: "更新被测系统"},
	{Name: "resource.sut.deleted", Label: "删除被测系统"},
	{Name: "resource.environment.created", Label: "创建环境"},
	{Name: "resource.environment.updated", Label: "更新环境"},
	{Name: "resource.environment.deleted", Label: "删除环境"},
	{Name: "resource.page.created", Label: "创建页面"},
	{Name: "resource.page.updated", Label: "更新页面"},
	{Name: "resource.page.deleted", Label: "删除页面"},
	{Name: "resource.page.proposed", Label: "提议页面"},
	{Name: "resource.page.confirmed", Label: "确认页面"},
	{Name: "resource.page.rejected", Label: "拒绝页面"},
	{Name: "resource.api.created", Label: "创建接口"},
	{Name: "resource.api.updated", Label: "更新接口"},
	{Name: "resource.api.deleted", Label: "删除接口"},
	{Name: "resource.api.proposed", Label: "提议接口"},
	{Name: "resource.api.confirmed", Label: "确认接口"},
	{Name: "resource.api.rejected", Label: "拒绝接口"},
	{Name: "resource.database.created", Label: "创建数据库"},
	{Name: "resource.database.updated", Label: "更新数据库"},
	{Name: "resource.database.deleted", Label: "删除数据库"},
	{Name: "resource.account.created", Label: "创建账号"},
	{Name: "resource.account.updated", Label: "更新账号"},
	{Name: "resource.account.deleted", Label: "删除账号"},
	{Name: "resource.code_repo.created", Label: "创建代码仓库"},
	{Name: "resource.code_repo.updated", Label: "更新代码仓库"},
	{Name: "resource.code_repo.deleted", Label: "删除代码仓库"},
	{Name: "resource.sut_repository.created", Label: "关联代码仓库"},

	{Name: "version.created", Label: "创建版本"},
	{Name: "version.updated", Label: "更新版本"},
	{Name: "version.reopened", Label: "重开版本"},
	{Name: "version.closed", Label: "关闭版本"},
	{Name: "version.binding.changed", Label: "变更版本绑定"},
	{Name: "version.code_baseline_unresolved", Label: "代码基线未解析"},
	{Name: "version.stage_decision", Label: "阶段决策"},
	{Name: "version.stage_gate_denied", Label: "阶段门禁拒绝"},

	{Name: "requirement.created", Label: "创建需求"},
	{Name: "requirement.updated", Label: "更新需求"},
	{Name: "requirement.confirmed", Label: "确认需求"},
	{Name: "requirement.deduped", Label: "需求去重"},
	{Name: "requirement.deleted", Label: "删除需求"},
	{Name: "requirement.merged", Label: "合并需求"},
	{Name: "requirement.obsoleted", Label: "废弃需求"},
	{Name: "requirement.rolled_back", Label: "回滚需求"},
	{Name: "requirement.rollback_proposal_used", Label: "采用回滚提案"},
	{Name: "requirement.relation_created", Label: "创建需求关系"},
	{Name: "requirement.closed", Label: "关闭需求"},
	{Name: "requirement.reopened", Label: "重开需求"},

	{Name: "agent.profile.updated", Label: "更新 Agent Profile"},
	{Name: "agent.profile.tool_denied", Label: "Profile 工具调用被拒"},
	{Name: "agent.role.tool_denied", Label: "角色工具调用被拒"},
	{Name: "agent.run.finished", Label: "Agent 运行完成"},
	{Name: "agent.run.failed", Label: "Agent 运行失败"},
	{Name: "job.canceled", Label: "取消任务"},
	{Name: "job.requeued", Label: "重排任务"},
}

// HasAction reports whether name is one of the catalog's canonical actions.
func HasAction(name string) bool {
	for _, action := range Actions {
		if action.Name == name {
			return true
		}
	}
	return false
}

// ResolveAlias maps a historical or alternate name to its canonical action.
// Unknown names are returned unchanged so the caller can still use them as a
// prefix.
func ResolveAlias(name string) string {
	for _, action := range Actions {
		for _, alias := range action.Aliases {
			if strings.EqualFold(alias, name) {
				return action.Name
			}
		}
	}
	return name
}
