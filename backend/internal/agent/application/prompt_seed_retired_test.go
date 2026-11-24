package agentapp

import (
	"strings"
	"testing"
)

// RetiredSeed2CaseExecutorBody is the exact case_executor seed prompt shipped at
// ProfileSeedVersion 2 (git 96d4a00c^, before the T13 capability-domain rewrite).
// It still names the retired browser.* atomic actions, the lifted password
// prohibition and read-only SELECT. It is the historical body T13's backfill
// failed to upgrade (its digest was not in the allow-list); T14 registers it via
// prompt_seed_retired.go. This fixture pins the registered digest to the real
// body: a drift in either fails here (method discipline ④).
const RetiredSeed2CaseExecutorBody = `你是 EzTest 的用例执行器（Case Executor）。
角色边界：
- 严格按任务上下文中的用例步骤顺序执行；不得修改需求/用例/确认，不得跳过步骤或不执行断言。
- 每完成一步后调用 execution.step_record 记录 seq/status/observed；每条断言后调用 execution.assertion_record 记录 verdict/actual/reason。
  断言 expected 以任务上下文为准，你提交的 expected 会被服务端覆盖；不得虚构 passed。
- 用例要求等待或时间窗口（例如“120 秒后弹框自动消失”）时，调用 execution.wait 等待（1–300s），不要用重复 snapshot 空转，也不要假设等待后的结果；
  等待结束后必须用 browser.snapshot / browser.screenshot / db.query 等工具实际检查。
- 登录使用 account.credentials 读取账号密码，并通过 browser.fill(secret=true) 输入；不得把密码写入证据、日志或最终回复。
- 上传附件/图片：一律调用 browser.upload_file，target 优先用正文编辑器工具栏里的“图片/上传”按钮（工具会在点击后接管原生文件选择框），也可直接指向 file input；禁止用 browser.click 点击文件输入框或上传按钮。封面/头像的上传输入框（uploadCover）不算正文图片上传。连续失败 2 次或找不到可用 target 时，立即 execution.case_finish(verdict=blocked) 说明能力缺口，不要空转重试。
- 验收标准要求移动端/响应式时，先用 browser.set_viewport(width,height) 切到对应视口，再导航、操作与断言；不要用桌面视口断言移动端专属行为。
- 遇到验证码/2FA/登录无法完成时调用 human.request(kind=login_takeover) 请求人工接管；不能自我 resolve。
- 证据由工具自动落库（截图/console/network/http/db），文本证据用 execution.evidence。
- 断言失败、步骤阻塞或结论不确定时必须留下证据：至少调用一次 browser.screenshot（UI 失败）或 browser.console/network 并最终 case_finish；服务端也会在 failed/blocked/inconclusive 终态自动补一张截图。
- 无法继续（缺账号/环境不可用/步骤阻塞）时调用 execution.case_finish(verdict=blocked) 并说明原因；全部步骤与断言完成后调用 execution.case_finish 提交候选终态。
- 服务端会按步骤与断言真值覆写你的候选终态；如实记录失败即可。
- 需要数据库校验时先用 database.schema_read 确认列名/类型，再写只读 SELECT；SQL 报错时记录该步（execution.step_record status=failed/blocked）并改用 API/UI 断言或修正一次；即使工具失败也要记录步骤后再 case_finish，不能无步骤结果直接结束。
- 所有步骤与断言记录完成后立即调用 execution.case_finish 提交终态；不要为了额外确认继续探索或重复验证，避免耗尽 turn/time 预算导致 inconclusive。
[profile:case_executor]`

// TestRetiredSeed2CaseExecutorDigest pins the registered allow-list digest to the
// exact historical body, and confirms the frozen upgrade policy recognizes it
// (including the Windows CRLF / CR spellings of the same body).
func TestRetiredSeed2CaseExecutorDigest(t *testing.T) {
	if got := seedPromptDigest(RetiredSeed2CaseExecutorBody); got != retiredSeed2CaseExecutorPromptHash {
		t.Fatalf("seed-2 case_executor digest = %s, want registered %s", got, retiredSeed2CaseExecutorPromptHash)
	}
	if !isLegacySeedPrompt(ProfileKeyCaseExecutor, RetiredSeed2CaseExecutorBody) {
		t.Fatal("seed-2 case_executor body is not recognized by the upgrade allow-list")
	}
	lf := normalizePromptBody(RetiredSeed2CaseExecutorBody)
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	cr := strings.ReplaceAll(lf, "\n", "\r")
	if !isLegacySeedPrompt(ProfileKeyCaseExecutor, crlf) || !isLegacySeedPrompt(ProfileKeyCaseExecutor, cr) {
		t.Fatal("line-ending variant of the seed-2 case_executor body is not recognized")
	}
}

// TestRetiredSeedRegistrationPreservesFrozenFixture confirms extending the
// allow-list from the non-frozen registry did not disturb the frozen fixture's
// invariants: still exactly four upgradable keys, and the v0.2.2 bodies (the
// frozen fixtures) are still recognized.
func TestRetiredSeedRegistrationPreservesFrozenFixture(t *testing.T) {
	if len(legacySeedPromptHashes) != 4 {
		t.Fatalf("legacy seed prompt keys = %d, want the 4 upgradable profiles", len(legacySeedPromptHashes))
	}
	for key, body := range legacyPromptBodiesV022 {
		if !isLegacySeedPrompt(key, body) {
			t.Fatalf("frozen v0.2.2 body of %q is no longer recognized", key)
		}
	}
	// The current seed prompt is NOT a legacy body (it must never be "upgraded"
	// to itself by the legacy path).
	current, ok := DefaultPrompt(ProfileKeyCaseExecutor)
	if !ok {
		t.Fatal("case_executor seed prompt missing")
	}
	if isLegacySeedPrompt(ProfileKeyCaseExecutor, current) {
		t.Fatal("the current case_executor seed prompt is wrongly registered as a legacy body")
	}
}
