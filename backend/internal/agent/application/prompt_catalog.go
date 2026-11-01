package agentapp

import (
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// PromptVariable is the code-defined template variable exposed by the admin
// API. The domain owns the type so save validation and rendering share one
// catalog shape (spec §8.4).
type PromptVariable = agent.PromptVariable

// executionPromptProfiles are the P3 execution profiles that additionally
// receive the execution task variables. The values are empty strings when no
// execution task is attached to the run.
var executionPromptProfiles = map[string]struct{}{
	ProfileKeyCaseExecutor:    {},
	ProfileKeyFailureAnalyst:  {},
	ProfileKeyCoverageAnalyst: {},
}

// PromptVariables returns the admin-visible variable catalog of a profile.
// Every profile gets the eight common runtime variables; execution profiles
// additionally get the six P3 case-execution variables.
func PromptVariables(profileKey string) []PromptVariable {
	variables := []PromptVariable{
		{Name: "Now", Description: "当前时间（RFC3339 UTC）", Example: "2026-11-01T12:00:00Z", Source: agent.PromptVariableSourceRuntime},
		{Name: "ProfileKey", Description: "当前 Agent profile key", Example: profileKey, Source: agent.PromptVariableSourceRuntime},
		{Name: "ProfileName", Description: "当前 Agent profile 名称", Example: profileExampleName(profileKey), Source: agent.PromptVariableSourceRuntime},
		{Name: "VersionID", Description: "当前版本 ID（十进制字符串）", Example: "2001", Source: agent.PromptVariableSourceRuntime},
		{Name: "VersionTitle", Description: "当前版本标题", Example: "订单系统 Q4", Source: agent.PromptVariableSourceRuntime},
		{Name: "VersionStatus", Description: "当前版本状态", Example: "running", Source: agent.PromptVariableSourceRuntime},
		{Name: "TaskType", Description: "当前任务类型", Example: profileExampleTaskType(profileKey), Source: agent.PromptVariableSourceRuntime},
		{Name: "TaskGoal", Description: "当前任务目标", Example: "回答用户关于登录流程的问题", Source: agent.PromptVariableSourceRuntime},
	}
	if _, ok := executionPromptProfiles[profileKey]; !ok {
		return variables
	}
	return append(variables,
		PromptVariable{Name: "CaseRunID", Description: "当前用例运行 ID（十进制字符串）", Example: "3001", Source: agent.PromptVariableSourceRuntime},
		PromptVariable{Name: "WaveID", Description: "当前执行波次 ID（十进制字符串）", Example: "12", Source: agent.PromptVariableSourceRuntime},
		PromptVariable{Name: "IntentID", Description: "当前执行意图 ID（十进制字符串）", Example: "44", Source: agent.PromptVariableSourceRuntime},
		PromptVariable{Name: "CaseVersionID", Description: "当前用例版本 ID（十进制字符串）", Example: "55", Source: agent.PromptVariableSourceRuntime},
		PromptVariable{Name: "CaseTitle", Description: "当前执行用例标题", Example: "登录流程", Source: agent.PromptVariableSourceRuntime},
		PromptVariable{Name: "Priority", Description: "当前用例优先级", Example: "P1", Source: agent.PromptVariableSourceRuntime},
	)
}

// PromptSampleValues returns one example value per catalog variable. The
// preview endpoint overlays the admin-provided sample values on this map.
func PromptSampleValues(profileKey string) map[string]string {
	values := make(map[string]string)
	for _, variable := range PromptVariables(profileKey) {
		values[variable.Name] = variable.Example
	}
	return values
}

func profileExampleName(profileKey string) string {
	for _, profile := range Profiles() {
		if profile.Key == profileKey {
			return profile.Name
		}
	}
	return profileKey
}

func profileExampleTaskType(profileKey string) string {
	switch profileKey {
	case ProfileKeyClarificationHost:
		return WorkTypeChatTurn
	case ProfileKeyRequirementCurator:
		return WorkTypeRequirementCurate
	case ProfileKeyContextScout:
		return WorkTypeAttachmentIngest
	case ProfileKeyDesignHost:
		return WorkTypeChatTurn
	case ProfileKeyDesignPlanner:
		return WorkTypeDesignRound
	case ProfileKeyCaseAuthor:
		return WorkTypeDesignRequirement
	case ProfileKeyDesignCritic:
		return WorkTypeDesignCritic
	case ProfileKeyDataPlanner:
		return WorkTypeDesignDataPlan
	case ProfileKeyCaseExecutor:
		return WorkTypeExecuteCase
	case ProfileKeyFailureAnalyst:
		return WorkTypeAnalyzeFailure
	case ProfileKeyCoverageAnalyst:
		return WorkTypeAnalyzeCoverage
	case ProfileKeyIssueTriager:
		return WorkTypeIssueTriage
	case ProfileKeyReportWriter:
		return WorkTypeReportWave
	case ProfileKeyVersionSteward:
		return WorkTypeVersionClose
	default:
		return WorkTypeChatTurn
	}
}
