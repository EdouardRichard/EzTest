import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { useState } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listSystems: vi.fn(),
  listAccesses: vi.fn(),
  listEnvironments: vi.fn(),
  importSession: vi.fn(),
}))

vi.mock("./api", () => ({
  listAccounts: mocks.listAccounts,
  listSystems: mocks.listSystems,
  listAccesses: mocks.listAccesses,
  listEnvironments: mocks.listEnvironments,
  importSession: mocks.importSession,
}))

import { ApiError } from "@/lib/api-client"

import { ImportSessionDialog } from "./ImportSessionDialog"
import type {
  AccessDTO,
  AccountDTO,
  EnvironmentDTO,
  SessionDTO,
  SnapshotDTO,
  SystemDTO,
} from "./types"

const SYSTEM: SystemDTO = {
  id: 1,
  key: "shop",
  name: "Shop",
  kind: "web",
  baseUrl: "http://localhost:13335",
  defaultSessionTtlSeconds: 3600,
  notes: "",
  status: "active",
  createdAt: "2026-10-27T00:00:00Z",
  updatedAt: "2026-10-27T00:00:00Z",
}

const ACCOUNT: AccountDTO = {
  id: 3,
  key: "qa01",
  label: "QA 01",
  username: "qa01",
  kind: "human",
  status: "active",
  notes: "",
}

const ACCESS: AccessDTO = {
  id: 11,
  accountId: 3,
  systemId: 1,
  authType: "manual",
  credentialSet: false,
  usernameOverride: "",
  loginUrl: "",
  enabled: true,
  concurrencyLimit: 1,
  sessionTtlSeconds: null,
  permissionNote: "",
}

const ENVIRONMENT: EnvironmentDTO = {
  id: 5,
  systemId: 1,
  key: "local",
  name: "本地",
  baseUrl: "http://localhost:13335",
  kind: "staging",
  isProd: false,
  allowedHosts: [],
  loginCheck: null,
  allowAutoLogin: false,
  requireApproval: false,
}

const SNAPSHOT: SnapshotDTO = {
  id: 7,
  sessionId: 1,
  kind: "login_check",
  items: {},
  summary: { total: 0, allowed: 0, denied: 0, error: 0, degraded: true },
  permissionHash: "",
  degraded: true,
  capturedAt: "2026-10-27T09:00:00Z",
}

const SESSION: SessionDTO = {
  id: 1,
  accountId: 3,
  systemId: 1,
  environmentId: 5,
  accessId: 11,
  state: "needs_manual",
  acquisition: "import",
  storageStateKind: "storage_state",
  cookieCount: 0,
  originCount: 0,
  storageStateHash: "state-hash",
  expiresAt: "2026-10-28T12:00:00Z",
  lastValidatedAt: null,
  lastUsedAt: null,
  failureReason: "login_check_not_configured",
  invalidatedReason: "",
  expiring: false,
  latestSnapshot: SNAPSHOT,
  activeLease: null,
  latestTakeover: null,
}

function page<T>(items: T[]) {
  return { items, nextCursor: null, hasMore: false }
}

async function selectAccountAccessAndEnvironment() {
  await screen.findByRole("option", { name: /QA 01/ })
  fireEvent.change(screen.getByLabelText("账号"), { target: { value: "3" } })
  await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(3, { limit: 100 }))
  await screen.findByRole("option", { name: /Shop/ })
  fireEvent.change(screen.getByLabelText("Access"), { target: { value: "11" } })
  await waitFor(() => expect(mocks.listEnvironments).toHaveBeenCalledWith(1))
  await screen.findByRole("option", { name: /本地/ })
  fireEvent.change(screen.getByLabelText("环境"), { target: { value: "5" } })
}

function renderDialog(onImported = vi.fn().mockResolvedValue(undefined)) {
  render(<ImportSessionDialog onClose={vi.fn()} onImported={onImported} />)
  return onImported
}

/** Mounts the dialog from a real trigger so focus return can be asserted. */
function DialogHarness() {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        打开导入
      </button>
      {open ? <ImportSessionDialog onClose={() => setOpen(false)} onImported={vi.fn()} /> : null}
    </>
  )
}

describe("ImportSessionDialog", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listAccounts.mockResolvedValue(page([ACCOUNT]))
    mocks.listSystems.mockResolvedValue(page([SYSTEM]))
    mocks.listAccesses.mockResolvedValue([ACCESS])
    mocks.listEnvironments.mockResolvedValue([ENVIRONMENT])
    mocks.importSession.mockResolvedValue(SESSION)
  })

  afterEach(cleanup)

  it("imports a storage_state and shows state, snapshot and degraded flag", async () => {
    const onImported = renderDialog()
    await selectAccountAccessAndEnvironment()

    fireEvent.change(screen.getByLabelText("JSON 内容"), {
      target: { value: JSON.stringify({ cookies: [], origins: [] }) },
    })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    await waitFor(() =>
      expect(mocks.importSession).toHaveBeenCalledWith(11, {
        environmentId: 5,
        kind: "storage_state",
        storageState: { cookies: [], origins: [] },
      }),
    )
    const status = await screen.findByRole("status")
    expect(status.textContent).toContain("needs_manual")
    expect(status.textContent).toContain("能力未验证")
    expect(onImported).toHaveBeenCalledWith(SESSION)
  })

  it("reports a denied login check as a failure instead of claiming success", async () => {
    const denied: SessionDTO = {
      ...SESSION,
      state: "needs_manual",
      failureReason: "login_failed",
      latestSnapshot: {
        ...SNAPSHOT,
        degraded: false,
        items: { login: { kind: "page", observed: "denied", status: 403 } },
        summary: { total: 1, allowed: 0, denied: 1, error: 0, degraded: false },
      },
    }
    mocks.importSession.mockResolvedValue(denied)
    renderDialog()
    await selectAccountAccessAndEnvironment()

    fireEvent.change(screen.getByLabelText("JSON 内容"), {
      target: { value: JSON.stringify({ cookies: [], origins: [] }) },
    })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    const status = await screen.findByRole("status")
    expect(status.textContent).toContain("needs_manual")
    expect(status.textContent).toContain("能力校验未通过")
    expect(status.textContent).toContain("拒绝")
    expect(status.textContent).not.toContain("能力校验通过")
  })

  it("reports an errored login check as a failure instead of claiming success", async () => {
    const errored: SessionDTO = {
      ...SESSION,
      state: "needs_manual",
      failureReason: "env_unreachable",
      latestSnapshot: {
        ...SNAPSHOT,
        degraded: false,
        items: { login: { kind: "page", observed: "error", status: 0 } },
        summary: { total: 1, allowed: 0, denied: 0, error: 1, degraded: false },
      },
    }
    mocks.importSession.mockResolvedValue(errored)
    renderDialog()
    await selectAccountAccessAndEnvironment()

    fireEvent.change(screen.getByLabelText("JSON 内容"), {
      target: { value: JSON.stringify({ cookies: [], origins: [] }) },
    })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    const status = await screen.findByRole("status")
    expect(status.textContent).toContain("能力校验未通过")
    expect(status.textContent).toContain("出错")
    expect(status.textContent).not.toContain("能力校验通过")
  })

  it("reports an allowed login check as passed", async () => {
    const allowed: SessionDTO = {
      ...SESSION,
      state: "valid",
      failureReason: "",
      latestSnapshot: {
        ...SNAPSHOT,
        degraded: false,
        items: { login: { kind: "page", observed: "allowed", status: 200 } },
        summary: { total: 1, allowed: 1, denied: 0, error: 0, degraded: false },
      },
    }
    mocks.importSession.mockResolvedValue(allowed)
    renderDialog()
    await selectAccountAccessAndEnvironment()

    fireEvent.change(screen.getByLabelText("JSON 内容"), {
      target: { value: JSON.stringify({ cookies: [], origins: [] }) },
    })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    const status = await screen.findByRole("status")
    expect(status.textContent).toContain("valid")
    expect(status.textContent).toContain("能力校验通过")
    expect(status.textContent).not.toContain("能力校验未通过")
  })

  it("reads a small storage_state file into the JSON box", async () => {
    renderDialog()
    await selectAccountAccessAndEnvironment()

    const file = new File(
      [JSON.stringify({ cookies: [{ name: "sid", value: "1" }], origins: [] })],
      "state.json",
      { type: "application/json" },
    )
    fireEvent.change(screen.getByLabelText("JSON 文件"), { target: { files: [file] } })

    const textarea = screen.getByLabelText("JSON 内容") as HTMLTextAreaElement
    await waitFor(() => expect(textarea.value).toContain('"sid"'))

    fireEvent.click(screen.getByRole("button", { name: "导入" }))
    await waitFor(() =>
      expect(mocks.importSession).toHaveBeenCalledWith(11, {
        environmentId: 5,
        kind: "storage_state",
        storageState: { cookies: [{ name: "sid", value: "1" }], origins: [] },
      }),
    )
  })

  it("imports token headers from JSON", async () => {
    renderDialog()
    await selectAccountAccessAndEnvironment()

    fireEvent.change(screen.getByLabelText("导入类型"), { target: { value: "token" } })
    fireEvent.change(screen.getByLabelText("JSON 内容"), {
      target: { value: JSON.stringify({ headers: { Authorization: "Bearer fixture-token" } }) },
    })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    await waitFor(() =>
      expect(mocks.importSession).toHaveBeenCalledWith(11, {
        environmentId: 5,
        kind: "token",
        headers: { Authorization: "Bearer fixture-token" },
      }),
    )
  })

  it("rejects malformed JSON and token input without headers", async () => {
    renderDialog()
    await selectAccountAccessAndEnvironment()
    fireEvent.change(screen.getByLabelText("导入类型"), { target: { value: "token" } })

    fireEvent.change(screen.getByLabelText("JSON 内容"), { target: { value: '{"headers":' } })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))
    let alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("JSON")

    fireEvent.change(screen.getByLabelText("JSON 内容"), { target: { value: '{"cookies":[]}' } })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))
    alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("headers")
    expect(mocks.importSession).not.toHaveBeenCalled()
  })

  it("rejects an import file larger than 5 MiB", async () => {
    renderDialog()
    await selectAccountAccessAndEnvironment()

    const oversized = new File([new Uint8Array(5 * 1024 * 1024 + 1)], "big.json", {
      type: "application/json",
    })
    fireEvent.change(screen.getByLabelText("JSON 文件"), { target: { files: [oversized] } })

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("5 MiB")
    expect(mocks.importSession).not.toHaveBeenCalled()
  })

  it("focuses the dialog on open, closes on Escape and restores the trigger focus", async () => {
    render(<DialogHarness />)
    const trigger = screen.getByRole("button", { name: "打开导入" })
    trigger.focus()
    fireEvent.click(trigger)

    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText("账号")))

    fireEvent.keyDown(document.activeElement as HTMLElement, { key: "Escape" })
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    expect(document.activeElement).toBe(trigger)
  })

  it("keeps Tab focus inside the dialog", async () => {
    renderDialog()
    const dialog = screen.getByRole("dialog")
    const first = within(dialog).getByRole("button", { name: "关闭" })
    const buttons = within(dialog).getAllByRole("button")
    const last = buttons[buttons.length - 1]

    last.focus()
    fireEvent.keyDown(last, { key: "Tab" })
    expect(document.activeElement).toBe(first)

    first.focus()
    fireEvent.keyDown(first, { key: "Tab", shiftKey: true })
    expect(document.activeElement).toBe(last)
  })

  it("requires an account, access, environment and payload before importing", async () => {
    renderDialog()

    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("账号")
    expect(mocks.importSession).not.toHaveBeenCalled()
  })

  it("shows the 422 reason and field returned by the import API", async () => {
    mocks.importSession.mockRejectedValue(
      new ApiError({
        status: 422,
        code: "invalid_argument",
        message: "storage state is invalid",
        details: { reason: "invalid_storage_state", field: "cookies" },
      }),
    )
    renderDialog()
    await selectAccountAccessAndEnvironment()

    fireEvent.change(screen.getByLabelText("JSON 内容"), {
      target: { value: JSON.stringify({ cookies: [], origins: [] }) },
    })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("invalid_storage_state")
    expect(alert.textContent).toContain("cookies")
  })
})
