import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { useState } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listAuthFlows: vi.fn(),
  createAuthFlow: vi.fn(),
  updateAuthFlow: vi.fn(),
  deleteAuthFlow: vi.fn(),
  listAccounts: vi.fn(),
  createAccount: vi.fn(),
  updateAccount: vi.fn(),
  deleteAccount: vi.fn(),
  listSystems: vi.fn(),
  listAccesses: vi.fn(),
  createAccess: vi.fn(),
  updateAccess: vi.fn(),
  deleteAccess: vi.fn(),
}))

vi.mock("./api", () => ({
  listAuthFlows: mocks.listAuthFlows,
  createAuthFlow: mocks.createAuthFlow,
  updateAuthFlow: mocks.updateAuthFlow,
  deleteAuthFlow: mocks.deleteAuthFlow,
  listAccounts: mocks.listAccounts,
  createAccount: mocks.createAccount,
  updateAccount: mocks.updateAccount,
  deleteAccount: mocks.deleteAccount,
  listSystems: mocks.listSystems,
  listAccesses: mocks.listAccesses,
  createAccess: mocks.createAccess,
  updateAccess: mocks.updateAccess,
  deleteAccess: mocks.deleteAccess,
}))

import { ApiError } from "@/lib/api-client"

import { AccessForm } from "./AccessForm"
import { AccountsPanel } from "./AccountsPanel"
import { AuthFlowDialog } from "./AuthFlowDialog"
import type { AccessDTO, AccountDTO, AuthFlowDTO, SystemDTO } from "./types"

function makeSystem(id: number, key: string, name: string): SystemDTO {
  return {
    id,
    key,
    name,
    kind: "web",
    baseUrl: "http://127.0.0.1:13335",
    defaultSessionTtlSeconds: 3600,
    notes: "",
    status: "active",
    createdAt: "2026-10-27T00:00:00Z",
    updatedAt: "2026-10-27T00:00:00Z",
  }
}

const SHOP = makeSystem(1, "shop", "Shop")
const BLOG = makeSystem(2, "blog", "Blog")
const SYSTEMS = [SHOP, BLOG]

const QA_ACCOUNT: AccountDTO = {
  id: 3,
  key: "qa01",
  label: "QA 01",
  username: "qa01",
  kind: "human",
  status: "active",
  notes: "",
}

const SHOP_DEFINITION = {
  login_url: "http://127.0.0.1:13335/p6/login",
  username_selector: "#username",
  password_selector: "#password",
  submit_selector: "button[type=submit]",
  success: { url_contains: "/p6/home" },
  failure: { mfa_selector: "#mfa" },
  timeout_ms: 20000,
}

const SHOP_FLOW: AuthFlowDTO = {
  id: "12",
  system_id: "1",
  name: "Shop 表单登录",
  kind: "form",
  definition: SHOP_DEFINITION,
  enabled: true,
  created_at: "2026-10-27T00:00:00Z",
  updated_at: "2026-10-27T00:00:00Z",
}

const BLOG_FLOW: AuthFlowDTO = {
  id: "13",
  system_id: "2",
  name: "Blog 表单登录",
  kind: "form",
  definition: {
    ...SHOP_DEFINITION,
    login_url: "http://127.0.0.1:13335/p6/blog/login",
  },
  enabled: true,
  created_at: "2026-10-27T00:00:00Z",
  updated_at: "2026-10-27T00:00:00Z",
}

const BOUND_ACCESS: AccessDTO = {
  id: 8,
  accountId: 3,
  systemId: 1,
  authType: "password",
  credentialSet: true,
  usernameOverride: "",
  loginUrl: "",
  enabled: true,
  concurrencyLimit: 1,
  sessionTtlSeconds: null,
  permissionNote: "",
  login_flow_id: "12",
}

const UNBOUND_ACCESS: AccessDTO = {
  id: 9,
  accountId: 3,
  systemId: 2,
  authType: "password",
  credentialSet: true,
  usernameOverride: "",
  loginUrl: "",
  enabled: true,
  concurrencyLimit: 1,
  sessionTtlSeconds: null,
  permissionNote: "",
  login_flow_id: null,
}

function page<T>(items: T[]) {
  return { items, nextCursor: null, hasMore: false }
}

function renderCreateDialog(onSaved = vi.fn()) {
  render(<AuthFlowDialog systems={SYSTEMS} onClose={vi.fn()} onSaved={onSaved} />)
  return onSaved
}

/** Mounts the dialog from a real trigger so focus return can be asserted. */
function DialogHarness() {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        打开配方
      </button>
      {open ? (
        <AuthFlowDialog systems={SYSTEMS} onClose={() => setOpen(false)} onSaved={vi.fn()} />
      ) : null}
    </>
  )
}

/** Fills the fields shared by the create tests, leaving defaults in place. */
function fillMinimalRecipe() {
  fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Shop 表单登录" } })
  fireEvent.change(screen.getByLabelText("登录 URL"), {
    target: { value: "http://127.0.0.1:13335/p6/login" },
  })
  fireEvent.change(screen.getByLabelText("用户名选择器"), { target: { value: "#username" } })
  fireEvent.change(screen.getByLabelText("密码选择器"), { target: { value: "#password" } })
  fireEvent.change(screen.getByLabelText("提交选择器"), {
    target: { value: "button[type=submit]" },
  })
  fireEvent.change(screen.getByLabelText("成功 URL 包含"), { target: { value: "/p6/home" } })
}

const CREATED_FLOW: AuthFlowDTO = {
  ...SHOP_FLOW,
  id: "14",
  name: "新配方",
}

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset()
  mocks.listSystems.mockResolvedValue(page(SYSTEMS))
  mocks.listAccounts.mockResolvedValue(page([QA_ACCOUNT]))
  mocks.listAccesses.mockResolvedValue([BOUND_ACCESS, UNBOUND_ACCESS])
  mocks.listAuthFlows.mockResolvedValue(page([SHOP_FLOW, BLOG_FLOW]))
  mocks.createAuthFlow.mockResolvedValue(CREATED_FLOW)
  mocks.updateAuthFlow.mockResolvedValue(SHOP_FLOW)
  mocks.deleteAuthFlow.mockResolvedValue(undefined)
  mocks.updateAccess.mockResolvedValue(BOUND_ACCESS)
})

afterEach(cleanup)

describe("AuthFlowDialog", () => {
  it("rejects a recipe with a missing name, URL and selectors", async () => {
    renderCreateDialog()

    fireEvent.click(screen.getByTestId("auth-flow-save"))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("名称")
    expect(mocks.createAuthFlow).not.toHaveBeenCalled()
  })

  it("rejects a login URL that is not http(s)", async () => {
    renderCreateDialog()
    fillMinimalRecipe()
    fireEvent.change(screen.getByLabelText("登录 URL"), { target: { value: "ftp://bad.test/x" } })

    fireEvent.click(screen.getByTestId("auth-flow-save"))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("http")
    expect(mocks.createAuthFlow).not.toHaveBeenCalled()
  })

  it("rejects a success rule that is left blank", async () => {
    renderCreateDialog()
    fillMinimalRecipe()
    fireEvent.change(screen.getByLabelText("成功 URL 包含"), { target: { value: "  " } })

    fireEvent.click(screen.getByTestId("auth-flow-save"))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("成功")
    expect(mocks.createAuthFlow).not.toHaveBeenCalled()
  })

  it("rejects a timeout outside 5000-60000 ms", async () => {
    renderCreateDialog()
    fillMinimalRecipe()
    fireEvent.change(screen.getByLabelText("超时（毫秒）"), { target: { value: "1000" } })

    fireEvent.click(screen.getByTestId("auth-flow-save"))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("5000")
    expect(mocks.createAuthFlow).not.toHaveBeenCalled()
  })

  it("creates a form recipe with url_contains success and failure selectors", async () => {
    const onSaved = renderCreateDialog()
    fillMinimalRecipe()
    fireEvent.change(screen.getByLabelText("MFA 选择器"), { target: { value: "#mfa" } })
    fireEvent.change(screen.getByLabelText("验证码选择器"), { target: { value: "#captcha" } })
    fireEvent.change(screen.getByLabelText("无效凭据选择器"), { target: { value: ".login-error" } })

    fireEvent.click(screen.getByTestId("auth-flow-save"))

    await waitFor(() =>
      expect(mocks.createAuthFlow).toHaveBeenCalledWith({
        system_id: "1",
        name: "Shop 表单登录",
        kind: "form",
        definition: {
          login_url: "http://127.0.0.1:13335/p6/login",
          username_selector: "#username",
          password_selector: "#password",
          submit_selector: "button[type=submit]",
          success: { url_contains: "/p6/home" },
          failure: {
            mfa_selector: "#mfa",
            captcha_selector: "#captcha",
            invalid_selector: ".login-error",
          },
          timeout_ms: 20000,
        },
        enabled: true,
      }),
    )
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(CREATED_FLOW))
  })

  it("creates a selector-based success rule without a url_contains rule", async () => {
    renderCreateDialog()
    fillMinimalRecipe()
    fireEvent.click(screen.getByLabelText("选择器"))
    fireEvent.change(screen.getByLabelText("成功选择器"), { target: { value: "#home" } })

    fireEvent.click(screen.getByTestId("auth-flow-save"))

    await waitFor(() => expect(mocks.createAuthFlow).toHaveBeenCalled())
    const payload = mocks.createAuthFlow.mock.calls[0][0] as {
      definition: { success: Record<string, string> }
    }
    expect(payload.definition.success).toEqual({ selector: "#home" })
    expect(payload.definition.success).not.toHaveProperty("url_contains")
  })

  it("edits an existing recipe without resubmitting its immutable system", async () => {
    const onSaved = vi.fn()
    render(
      <AuthFlowDialog systems={SYSTEMS} flow={SHOP_FLOW} onClose={vi.fn()} onSaved={onSaved} />,
    )

    expect((screen.getByLabelText("系统") as HTMLSelectElement).disabled).toBe(true)
    expect((screen.getByLabelText("名称") as HTMLInputElement).value).toBe("Shop 表单登录")
    expect((screen.getByLabelText("成功 URL 包含") as HTMLInputElement).value).toBe("/p6/home")

    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Shop 表单登录 v2" } })
    fireEvent.click(screen.getByTestId("auth-flow-save"))

    await waitFor(() => expect(mocks.updateAuthFlow).toHaveBeenCalled())
    const [id, payload] = mocks.updateAuthFlow.mock.calls[0] as [string, Record<string, unknown>]
    expect(id).toBe("12")
    expect(payload).not.toHaveProperty("system_id")
    expect(payload).toMatchObject({
      name: "Shop 表单登录 v2",
      kind: "form",
      enabled: true,
      definition: { login_url: "http://127.0.0.1:13335/p6/login" },
    })
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
  })

  it("shows the 422 auth_flow_invalid reason returned by the API", async () => {
    mocks.createAuthFlow.mockRejectedValue(
      new ApiError({
        status: 422,
        code: "invalid_argument",
        message: "recipe rejected",
        details: { reason: "auth_flow_invalid", field: "definition.login_url" },
      }),
    )
    renderCreateDialog()
    fillMinimalRecipe()

    fireEvent.click(screen.getByTestId("auth-flow-save"))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("auth_flow_invalid")
    expect(alert.textContent).toContain("definition.login_url")
  })

  it("focuses the first field, closes on Escape and restores the trigger focus", async () => {
    render(<DialogHarness />)
    const trigger = screen.getByRole("button", { name: "打开配方" })
    trigger.focus()
    fireEvent.click(trigger)

    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText("系统")))

    fireEvent.keyDown(document.activeElement as HTMLElement, { key: "Escape" })
    await waitFor(() => expect(screen.queryByTestId("auth-flow-dialog")).toBeNull())
    expect(document.activeElement).toBe(trigger)
  })

  it("keeps Tab focus inside the dialog", () => {
    renderCreateDialog()
    const dialog = screen.getByTestId("auth-flow-dialog")
    const buttons = within(dialog).getAllByRole("button")
    const first = buttons[0]
    const last = buttons[buttons.length - 1]

    last.focus()
    fireEvent.keyDown(last, { key: "Tab" })
    expect(document.activeElement).toBe(first)

    first.focus()
    fireEvent.keyDown(first, { key: "Tab", shiftKey: true })
    expect(document.activeElement).toBe(last)
  })
})

describe("login recipe management in AccountsPanel", () => {
  it("hides recipe management and skips the admin-only list for normal users", async () => {
    render(<AccountsPanel canWrite={false} />)

    expect(await screen.findByText("QA 01")).toBeTruthy()
    expect(screen.queryByRole("heading", { name: "登录配方" })).toBeNull()
    expect(screen.queryByRole("button", { name: "新建配方" })).toBeNull()
    expect(mocks.listAuthFlows).not.toHaveBeenCalled()
  })

  it("lists recipes with their system for admins", async () => {
    render(<AccountsPanel canWrite />)

    expect(await screen.findByText("Shop 表单登录")).toBeTruthy()
    expect(screen.getByText("Blog 表单登录")).toBeTruthy()
    expect(screen.getByRole("heading", { name: "登录配方" })).toBeTruthy()
    expect(mocks.listAuthFlows).toHaveBeenCalledWith({ limit: 100 })
  })

  it("creates a recipe from the panel and reloads the list", async () => {
    render(<AccountsPanel canWrite />)
    await screen.findByText("Shop 表单登录")

    fireEvent.click(screen.getByRole("button", { name: "新建配方" }))
    expect(screen.getByTestId("auth-flow-dialog")).toBeTruthy()
    fillMinimalRecipe()
    fireEvent.click(screen.getByTestId("auth-flow-save"))

    await waitFor(() => expect(mocks.createAuthFlow).toHaveBeenCalled())
    await waitFor(() => expect(mocks.listAuthFlows).toHaveBeenCalledTimes(2))
    expect((await screen.findByRole("status")).textContent).toContain("已创建登录配方")
    expect(screen.queryByTestId("auth-flow-dialog")).toBeNull()
  })

  it("edits a recipe from the panel", async () => {
    render(<AccountsPanel canWrite />)
    await screen.findByText("Shop 表单登录")

    fireEvent.click(screen.getByRole("button", { name: "编辑配方 Shop 表单登录" }))
    const dialog = screen.getByTestId("auth-flow-dialog")
    expect(dialog).toBeTruthy()
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Shop 登录 v2" } })
    fireEvent.click(screen.getByTestId("auth-flow-save"))

    await waitFor(() => expect(mocks.updateAuthFlow).toHaveBeenCalled())
    const [id, payload] = mocks.updateAuthFlow.mock.calls[0] as [string, Record<string, unknown>]
    expect(id).toBe("12")
    expect(payload).toMatchObject({ name: "Shop 登录 v2" })
    expect((await screen.findByRole("status")).textContent).toContain("已更新登录配方")
  })

  it("deletes a recipe only after the inline confirmation", async () => {
    render(<AccountsPanel canWrite />)
    await screen.findByText("Shop 表单登录")

    fireEvent.click(screen.getByRole("button", { name: "删除配方 Shop 表单登录" }))
    expect(mocks.deleteAuthFlow).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "确认删除配方 Shop 表单登录" }))

    await waitFor(() => expect(mocks.deleteAuthFlow).toHaveBeenCalledWith("12"))
    expect((await screen.findByRole("status")).textContent).toContain("已删除登录配方")
    await waitFor(() => expect(screen.queryByText("Shop 表单登录")).toBeNull())
    expect(screen.getByText("Blog 表单登录")).toBeTruthy()
  })

  it("shows the current binding and only the selected system's recipes", async () => {
    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")
    fireEvent.click(screen.getByRole("button", { name: "QA 01" }))
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(3, { limit: 100 }))

    fireEvent.click(screen.getByRole("button", { name: "编辑 Access 8" }))
    const select = screen.getByTestId("access-login-flow-select") as HTMLSelectElement
    await waitFor(() => expect(select.value).toBe("12"))

    const options = Array.from(select.options).map((option) => option.textContent ?? "")
    expect(options.join("|")).toContain("Shop 表单登录")
    expect(options.join("|")).not.toContain("Blog 表单登录")
  })

  it("binds and clears login_flow_id from the access form", async () => {
    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")
    fireEvent.click(screen.getByRole("button", { name: "QA 01" }))
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(3, { limit: 100 }))

    fireEvent.click(screen.getByRole("button", { name: "编辑 Access 8" }))
    await waitFor(() => expect(screen.getByTestId("access-login-flow-select")).toBeTruthy())
    fireEvent.change(screen.getByTestId("access-login-flow-select"), { target: { value: "" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(mocks.updateAccess).toHaveBeenCalledWith(
        8,
        expect.objectContaining({ login_flow_id: null }),
      ),
    )

    fireEvent.click(screen.getByRole("button", { name: "编辑 Access 9" }))
    await waitFor(() => expect(screen.getByTestId("access-login-flow-select")).toBeTruthy())
    fireEvent.change(screen.getByTestId("access-login-flow-select"), { target: { value: "13" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(mocks.updateAccess).toHaveBeenCalledWith(
        9,
        expect.objectContaining({ login_flow_id: "13" }),
      ),
    )
  })

  it("starts a create form without a login_flow_id key when nothing is bound", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined)
    render(
      <AccessForm
        accountId={3}
        systems={SYSTEMS}
        flows={[SHOP_FLOW, BLOG_FLOW]}
        canWrite
        onSubmit={onSubmit}
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalled())
    expect(onSubmit.mock.calls[0][0]).not.toHaveProperty("login_flow_id")
  })
})
