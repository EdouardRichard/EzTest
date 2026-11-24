import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ReportContentView } from "./ReportContentView"

/**
 * A v3-structured report HTML fixture. The authoritative report is the
 * server-rendered, byte-frozen template v3 document (T9/T10); the frontend view
 * renders that document verbatim (no second React truth). The data-element /
 * data-section / state-* vocabulary used here is pinned against the real Go
 * template by `report-view.authority.test.ts`.
 */
const V3_HTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>版本总结</title>
<style>.state-verified{color:#1a7f37;}.state-unverified{color:#7d4e00;}.state-failed{color:#cf222e;}.state-uncovered{color:#59636e;}</style>
</head><body>
<main data-template-version="3">
<section id="overview" data-section="summary"><h2>概览</h2><p class="conclusion" data-conclusion="defect">存在产品缺陷，需修复后重跑。</p></section>
<section id="defects" data-section="issues"><h2>缺陷五要素卡片</h2>
<article class="defect-card" data-issue-id="9001">
<header><h3>登录失败</h3><span>问题 #9001</span></header>
<div class="card-context">
<p><strong>用例：</strong>用户登录</p>
<p><strong>用例说明：</strong>验证用户可以使用账号密码登录</p>
<p><strong>前置条件：</strong>已存在有效账号</p>
<p><strong>被测系统：</strong>订单系统</p>
<p><strong>系统说明：</strong>面向内部的订单管理系统</p>
</div>
<div class="card-element" data-element="requirement"><h4>① 关联需求</h4><p>需求 #7001：用户登录</p><p>作为用户，我想要登录，以便访问订单</p></div>
<div class="card-element" data-element="steps"><h4>② 执行步骤</h4><ol><li>步骤 1：点击登录按钮 —— 目标：登录入口（预期：进入登录页）</li></ol></div>
<div class="card-element" data-element="ac"><h4>③ 验收标准（AC）</h4><ul><li><span class="mono">AC-1</span>：给定 有效账号；当 提交登录；则 登录成功（<span class="state state-failed">失败</span>）</li></ul></div>
<div class="card-element" data-element="expected"><h4>④ 预期结果</h4><p>登录成功并跳转首页</p></div>
<div class="card-element" data-element="actual"><h4>⑤ 实际结果</h4><p>返回 500 错误</p><p><strong>失败原因：</strong>服务端异常</p><p><strong>可看证据：</strong></p><ul><li><a class="evidence-link" href="/api/versions/2001/evidence/5001/content">查看证据 #5001（screenshot）</a> <span class="mono">abc</span></li></ul></div>
</article>
</section>
<section id="ac-matrix" data-section="coverage"><h2>AC 验证矩阵</h2>
<table><thead><tr><th>需求</th><th>AC</th><th>给定</th><th>当</th><th>则</th><th>验证状态</th><th>覆盖用例版本</th></tr></thead>
<tbody>
<tr data-ac-id="AC-1"><td class="num">7001</td><td class="mono">AC-1</td><td>有效账号</td><td>提交登录</td><td>登录成功</td><td><span class="state state-failed">失败</span><span class="ac-failed-hint" data-ac-failed-hint="true">用例版本 3001 有 1 个失败执行（最新 case_run 4001）</span></td><td>3001</td></tr>
<tr data-ac-id="AC-2"><td class="num">7001</td><td class="mono">AC-2</td><td>已登录</td><td>查看订单</td><td>显示订单列表</td><td><span class="state state-verified">已验证</span></td><td>3002</td></tr>
<tr data-ac-id="AC-3"><td class="num">7001</td><td class="mono">AC-3</td><td>已登录</td><td>导出订单</td><td>导出成功</td><td><span class="state state-unverified">未验证</span></td><td>3003</td></tr>
<tr data-ac-id="AC-4"><td class="num">7001</td><td class="mono">AC-4</td><td>管理员</td><td>删除订单</td><td>删除成功</td><td><span class="state state-uncovered">未覆盖</span></td><td></td></tr>
</tbody></table>
<div class="judge" data-section="judge"><h3>裁判评估（设计裁判事实）</h3><div class="judge-row" data-judge-case-id="3001"><p><strong>用例 #3001</strong>：评审状态 <span class="state">通过</span>；裁判结论 <span class="state">pass</span></p></div></div>
</section>
<section id="evidence" data-section="evidence"><h2>证据附录</h2><table><tbody><tr data-evidence-id="5001"><td class="num">5001</td><td class="num">4001</td><td>screenshot</td><td><a class="evidence-link" href="/api/versions/2001/evidence/5001/content">查看证据</a></td><td class="mono">abc</td></tr></tbody></table></section>
</main></body></html>`

function shadowRoot(): ShadowRoot {
  const host = screen.getByTestId("report-content")
  const root = host.shadowRoot
  if (root === null) throw new Error("report content shadow root was not attached")
  return root
}

function renderReady(): void {
  render(
    <ReportContentView
      html={V3_HTML}
      loading={false}
      error={null}
      onRetry={vi.fn()}
      onOpenEvidencePath={vi.fn()}
    />,
  )
}

describe("ReportCardFiveElements", () => {
  afterEach(cleanup)

  it("renders the five defect-card elements in fixed order with NL context", () => {
    renderReady()
    const root = shadowRoot()
    const card = root.querySelector("article.defect-card[data-issue-id='9001']")
    expect(card).not.toBeNull()
    const elements = [...(card?.querySelectorAll("[data-element]") ?? [])].map((node) =>
      node.getAttribute("data-element"),
    )
    expect(elements).toEqual(["requirement", "steps", "ac", "expected", "actual"])
    // Natural-language context: requirement user_story, case description/preconditions,
    // step action + target_description, SUT description.
    expect(card?.textContent).toContain("作为用户，我想要登录，以便访问订单")
    expect(card?.textContent).toContain("验证用户可以使用账号密码登录")
    expect(card?.textContent).toContain("已存在有效账号")
    expect(card?.textContent).toContain("点击登录按钮")
    expect(card?.textContent).toContain("目标：登录入口")
    expect(card?.textContent).toContain("面向内部的订单管理系统")
    // expected / actual
    expect(card?.querySelector("[data-element='expected']")?.textContent).toContain(
      "登录成功并跳转首页",
    )
    expect(card?.querySelector("[data-element='actual']")?.textContent).toContain("返回 500 错误")
  })

  it("exposes a viewable evidence link and forwards its content path on click", () => {
    const onOpenEvidencePath = vi.fn()
    render(
      <ReportContentView
        html={V3_HTML}
        loading={false}
        error={null}
        onRetry={vi.fn()}
        onOpenEvidencePath={onOpenEvidencePath}
      />,
    )
    const root = shadowRoot()
    const link = root.querySelector("a.evidence-link") as HTMLAnchorElement
    expect(link).not.toBeNull()
    expect(link.getAttribute("href")).toBe("/api/versions/2001/evidence/5001/content")
    fireEvent.click(link)
    expect(onOpenEvidencePath).toHaveBeenCalledWith("/api/versions/2001/evidence/5001/content")
  })

  it("shows a loading state while the HTML is being fetched", () => {
    render(<ReportContentView html={null} loading error={null} onRetry={vi.fn()} />)
    expect(screen.getByTestId("report-content-loading")).toBeInTheDocument()
  })

  it("shows an error state with retry when the HTML fetch fails", () => {
    const onRetry = vi.fn()
    render(
      <ReportContentView html={null} loading={false} error={new Error("boom")} onRetry={onRetry} />,
    )
    expect(screen.getByTestId("report-content-error")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /重试/ }))
    expect(onRetry).toHaveBeenCalled()
  })

  it("shows an empty state when no HTML is available", () => {
    render(<ReportContentView html={null} loading={false} error={null} onRetry={vi.fn()} />)
    expect(screen.getByTestId("report-content-empty")).toBeInTheDocument()
  })

  it("keeps the report body rendered when loading returns to ready with UNCHANGED html", () => {
    // Final fix wave #3: a waiver refetch returns the SAME bytes (a requirement
    // waiver does not regenerate the version report). The loading branch unmounts
    // the shadow host; when ready returns with unchanged html the host remounts as
    // a NEW element. The shadow root must be re-attached on that remount — the
    // report body must never end up an empty box.
    const onOpenEvidencePath = vi.fn()
    const { rerender } = render(
      <ReportContentView
        html={V3_HTML}
        loading={false}
        error={null}
        onRetry={vi.fn()}
        onOpenEvidencePath={onOpenEvidencePath}
      />,
    )
    expect(shadowRoot().querySelector("article.defect-card")).not.toBeNull()

    // Waiver refetch begins: the loading branch replaces the host section.
    rerender(
      <ReportContentView
        html={V3_HTML}
        loading
        error={null}
        onRetry={vi.fn()}
        onOpenEvidencePath={onOpenEvidencePath}
      />,
    )
    expect(screen.getByTestId("report-content-loading")).toBeInTheDocument()

    // Refetch resolves with the SAME html string: ready remounts a new host.
    rerender(
      <ReportContentView
        html={V3_HTML}
        loading={false}
        error={null}
        onRetry={vi.fn()}
        onOpenEvidencePath={onOpenEvidencePath}
      />,
    )
    const root = shadowRoot()
    expect(root.querySelector("article.defect-card[data-issue-id='9001']")).not.toBeNull()
    expect(root.textContent).toContain("登录失败")
    // Evidence links still route through the authenticated handler after remount.
    const link = root.querySelector("a.evidence-link") as HTMLAnchorElement
    fireEvent.click(link)
    expect(onOpenEvidencePath).toHaveBeenCalledWith("/api/versions/2001/evidence/5001/content")
  })

  it("re-attaches the report body after an error retry returns the same html", () => {
    // Same remount shape via the error branch (onRetry after a transient failure).
    const { rerender } = render(
      <ReportContentView html={V3_HTML} loading={false} error={null} onRetry={vi.fn()} />,
    )
    expect(shadowRoot().querySelector("article.defect-card")).not.toBeNull()
    rerender(
      <ReportContentView
        html={V3_HTML}
        loading={false}
        error={new Error("transient")}
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByTestId("report-content-error")).toBeInTheDocument()
    rerender(<ReportContentView html={V3_HTML} loading={false} error={null} onRetry={vi.fn()} />)
    expect(shadowRoot().querySelector("article.defect-card")).not.toBeNull()
  })
})

describe("ReportContentLandmarks", () => {
  afterEach(cleanup)

  it("hosts the verbatim report in a non-landmark container (no nested main region)", () => {
    // H-G4 #33: the byte-frozen report body contains its own `<main>`; the host
    // must be a generic container so the report's main is not nested inside an
    // extra landmark region.
    renderReady()
    const host = screen.getByTestId("report-content")
    expect(host.tagName).not.toBe("SECTION")
    expect(host.getAttribute("role")).toBeNull()
    expect(host.hasAttribute("aria-label")).toBe(false)
    // The verbatim document (and its own main landmark) is still rendered.
    expect(shadowRoot().querySelector("main")).not.toBeNull()
  })
})

describe("ACMatrixVerificationStates", () => {
  afterEach(cleanup)

  it("renders each AC with its verification state class and Chinese label", () => {
    renderReady()
    const root = shadowRoot()
    const expected: Array<[string, string, string]> = [
      ["AC-1", "state-failed", "失败"],
      ["AC-2", "state-verified", "已验证"],
      ["AC-3", "state-unverified", "未验证"],
      ["AC-4", "state-uncovered", "未覆盖"],
    ]
    for (const [acId, stateClass, label] of expected) {
      const row = root.querySelector(`tr[data-ac-id='${acId}']`)
      expect(row, `row for ${acId}`).not.toBeNull()
      const state = row?.querySelector(".state")
      expect(state?.classList.contains(stateClass), `${acId} has ${stateClass}`).toBe(true)
      expect(state?.textContent).toBe(label)
    }
  })

  it("renders the failed-run hint on a verified/failed AC and the judge display", () => {
    renderReady()
    const root = shadowRoot()
    const hint = root.querySelector("[data-ac-failed-hint]")
    expect(hint?.textContent).toContain("失败执行")
    const judge = root.querySelector("[data-section='judge']")
    expect(judge?.textContent).toContain("裁判评估")
    expect(judge?.textContent).toContain("pass")
  })
})
