package agentapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	skillapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/skill"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

/* ------------------------- context injection ------------------------------ */

type fakeSkillMetadataReader struct {
	metas []skillapp.Metadata
	err   error
	calls int
}

func (f *fakeSkillMetadataReader) Metadata(context.Context) ([]skillapp.Metadata, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.metas, nil
}

// TestSkillMetadataSectionRendered pins the resident layer placement: both the
// legacy section layout and the cache-first pinned fact blocks carry the skill
// metadata (name/description + the skill.read pointer) when skills are injected.
func TestSkillMetadataSectionRendered(t *testing.T) {
	in := ContextInput{
		VersionPack: VersionPack{VersionID: 7, Title: "v"},
		Skills: []skillapp.Metadata{
			{Name: "playwright-cli", Description: "official CLI usage"},
			{Name: "api-recon", Description: "API test workflow"},
		},
	}
	sections := renderSections(in)
	var skillsSection *Section
	for i := range sections {
		if sections[i].Name == "skills" {
			skillsSection = &sections[i]
		}
	}
	if skillsSection == nil {
		t.Fatal("renderSections has no skills section")
	}
	if !strings.Contains(skillsSection.Content, "playwright-cli") ||
		!strings.Contains(skillsSection.Content, "API test workflow") ||
		!strings.Contains(skillsSection.Content, "skill.read") {
		t.Fatalf("skills section content:\n%s", skillsSection.Content)
	}

	blocks := contextFactBlocks(in)
	joined := strings.Join(blocks, "\n")
	if !strings.Contains(joined, "playwright-cli") || !strings.Contains(joined, "skill.read") {
		t.Fatalf("pinned fact blocks missing the skill metadata layer:\n%s", joined)
	}
}

// TestSkillSectionAbsentWithoutSkills: an un-injected run renders no empty
// skill heading (knowledge precedent).
func TestSkillSectionAbsentWithoutSkills(t *testing.T) {
	in := ContextInput{VersionPack: VersionPack{VersionID: 7, Title: "v"}}
	for _, section := range renderSections(in) {
		if section.Name == "skills" {
			t.Fatal("empty skills section rendered")
		}
	}
	if strings.Contains(strings.Join(contextFactBlocks(in), "\n"), "skill.read") {
		t.Fatal("skill block rendered without injected skills")
	}
}

// TestSkillMetadataForRun pins the runner injection port behavior: nil reader
// disables the layer; a read failure is supplemental (log-only, run continues);
// success injects the metadata.
func TestSkillMetadataForRun(t *testing.T) {
	ctx := context.Background()
	var nilRunner = &Runner{}
	if got := nilRunner.skillMetadataForRun(ctx); got != nil {
		t.Fatalf("nil reader injected %#v", got)
	}
	failing := &Runner{skills: &fakeSkillMetadataReader{err: errors.New("db down")}}
	if got := failing.skillMetadataForRun(ctx); got != nil {
		t.Fatalf("failed read injected %#v", got)
	}
	reader := &fakeSkillMetadataReader{metas: []skillapp.Metadata{{Name: "api-recon", Description: "d"}}}
	ok := &Runner{skills: reader}
	got := ok.skillMetadataForRun(ctx)
	if len(got) != 1 || got[0].Name != "api-recon" || reader.calls != 1 {
		t.Fatalf("injection = %#v (calls %d)", got, reader.calls)
	}
}

/* ----------------------------- profile seed ------------------------------- */

// TestSkillReadInCaseExecutorSeed pins the T19 whitelist + prompt contract:
// skill.read is seeded, registered in the admin catalog, part of the derived
// backfill increment, and the prompt teaches progressive disclosure.
func TestSkillReadInCaseExecutorSeed(t *testing.T) {
	profile, ok := Profile(ProfileKeyCaseExecutor)
	if !ok {
		t.Fatal("case_executor profile missing")
	}
	found := false
	for _, name := range profile.AllowedTools {
		if name == tools.ToolSkillRead {
			found = true
		}
	}
	if !found {
		t.Fatalf("case_executor whitelist missing skill.read: %v", profile.AllowedTools)
	}
	if !strings.Contains(profile.SystemPrompt, "skill.read") {
		t.Fatal("case_executor prompt does not reference skill.read (progressive disclosure)")
	}
	if _, registered := tools.AdminCatalogRegistry().Get(tools.ToolSkillRead); !registered {
		t.Fatal("skill.read not in the admin catalog")
	}
	increment := V022ProfileTools()[ProfileKeyCaseExecutor]
	incrementFound := false
	for _, name := range increment {
		if name == tools.ToolSkillRead {
			incrementFound = true
		}
	}
	if !incrementFound {
		t.Fatalf("backfill increment missing skill.read: %v", increment)
	}
}

// TestSkillReadSeedReconcilesLegacyRows: a row already reconciled to seed 5
// (pre-skill) must be re-reconciled by the bumped seed version so its whitelist
// gains skill.read and its untouched seed prompt is upgraded.
func TestSkillReadSeedReconcilesLegacyRows(t *testing.T) {
	if ProfileSeedVersion <= 5 {
		t.Fatalf("ProfileSeedVersion = %d, want > 5 so seed-5 rows re-reconcile for skill.read", ProfileSeedVersion)
	}
	// The pre-T19 (seed-5 era) prompt body must be recognized by the upgrade
	// allow-list, or stored rows keep a prompt that never names skill.read.
	if !isLegacySeedPrompt(ProfileKeyCaseExecutor, retiredSeed5CaseExecutorBody) {
		t.Fatal("pre-T19 case_executor prompt body is not in the upgrade allow-list")
	}
}

// retiredSeed5CaseExecutorBody is the exact case_executor seed prompt shipped
// at ProfileSeedVersion 5 (git 9e1f9cfe, before the T19 skill reference). It is
// registered by prompt_seed_skills.go; this fixture pins the digest to the real
// historical body (method discipline ④: the guard must fire on the real body).
const retiredSeed5CaseExecutorBody = `你是 EzTest 的用例执行器（Case Executor）。
角色边界：
- 严格按任务上下文中的用例步骤顺序执行；不得修改需求/用例/确认，不得跳过步骤或不执行断言。
- 每完成一步后调用 execution.step_record 记录 seq/status/observed；每条断言后调用 execution.assertion_record 记录 verdict/actual/reason。
  断言 expected 以任务上下文为准，你提交的 expected 会被服务端覆盖；不得虚构 passed。
- 操作被测系统使用能力域工具：浏览器（browser.cli，官方 playwright-cli 全子命令透传）、通用执行（exec.run / shell.* / file.* / pty.*）、HTTP（http.request）、数据库（db.query / database.schema_read）；命令与请求的证据由工具自动落库。
- 浏览器操作用 browser.cli：先 open <url> 打开页面，snapshot 取 ARIA 快照与元素 ref（如 e15，跨 iframe 为 f1e2），再用 click/fill/dblclick/hover/select <ref> 操作；多标签用 tab-list/tab-select，上传先 click 文件输入框再 upload <绝对路径>，等待用 run-code（page.waitForLoadState / locator.waitFor）。子命令与参数原样透传，不限固定动作；CLI 不覆盖的用 exec.run。快照/截图/trace/console 输出自动落证据。
- 用例要求等待或时间窗口（例如“120 秒后弹框自动消失”）时，调用 execution.wait 等待（1–300s），不要用重复读取空转，也不要假设等待后的结果；
  等待结束后必须用 exec.run / http.request / db.query 等工具实际检查。
- 登录使用 account.credentials 读取账号密码，并通过通用执行通道输入；凭据明文 by design，可进入证据与日志。
- 上传附件/图片：通过通用执行/文件工具把文件内容注入上传入口并验证插入结果。连续失败 2 次或找不到可用入口时，立即 execution.case_finish(verdict=blocked) 说明能力缺口，不要空转重试。
- 验收标准要求移动端/响应式时，先在对应视口条件下执行操作与断言；不要用桌面视口断言移动端专属行为。
- 遇到验证码/2FA/登录无法完成时调用 human.request(kind=login_takeover) 请求人工接管；不能自我 resolve。
- 证据由工具自动落库（命令/HTTP/DB 证据），文本证据用 execution.evidence。
- 断言失败、步骤阻塞或结论不确定时必须留下证据：命令/HTTP/DB 证据由工具自动记录，必要时补 execution.evidence 文本证据并最终 case_finish；服务端也会在 failed/blocked/inconclusive 终态自动补一张截图。
- 无法继续（缺账号/环境不可用/步骤阻塞）时调用 execution.case_finish(verdict=blocked) 并说明原因；全部步骤与断言完成后调用 execution.case_finish 提交候选终态。
- 服务端会按步骤与断言真值覆写你的候选终态；如实记录失败即可。
- 需要数据库校验时先用 database.schema_read 确认列名/类型，再写 SQL（读写均可）；SQL 报错时记录该步（execution.step_record status=failed/blocked）并改用 API/UI 断言或修正一次；即使工具失败也要记录步骤后再 case_finish，不能无步骤结果直接结束。
- 所有步骤与断言记录完成后立即调用 execution.case_finish 提交终态；不要为了额外确认继续探索或重复验证，避免耗尽 turn/time 预算导致 inconclusive。
[profile:case_executor]`

// TestRetiredSeed5CaseExecutorDigest pins the registered allow-list digest to
// the exact historical body, including its CRLF/CR spellings.
func TestRetiredSeed5CaseExecutorDigest(t *testing.T) {
	if got := seedPromptDigest(retiredSeed5CaseExecutorBody); got != retiredSeed5CaseExecutorPromptHash {
		t.Fatalf("seed-5 case_executor digest = %s, want registered %s", got, retiredSeed5CaseExecutorPromptHash)
	}
	lf := normalizePromptBody(retiredSeed5CaseExecutorBody)
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	cr := strings.ReplaceAll(lf, "\n", "\r")
	if !isLegacySeedPrompt(ProfileKeyCaseExecutor, crlf) || !isLegacySeedPrompt(ProfileKeyCaseExecutor, cr) {
		t.Fatal("line-ending variant of the seed-5 case_executor body is not recognized")
	}
	// The current seed prompt must NOT be in the allow-list (it is the upgrade
	// target, not a legacy body).
	current, ok := DefaultPrompt(ProfileKeyCaseExecutor)
	if !ok {
		t.Fatal("case_executor default prompt missing")
	}
	if isLegacySeedPrompt(ProfileKeyCaseExecutor, current) {
		t.Fatal("current seed prompt is registered as legacy")
	}
}

// TestSkillReadWhitelistDomain: skill.read is the skill capability domain's
// member; the domain tag is derived from the name (single authority).
func TestSkillReadWhitelistDomain(t *testing.T) {
	if tools.ToolDomain(tools.ToolSkillRead) != "skill" {
		t.Fatalf("domain = %q", tools.ToolDomain(tools.ToolSkillRead))
	}
	var _ SkillMetadataReader = (*skillapp.Service)(nil)
	var _ agent.SkillUsage // the fact type exists in the agent domain
}
