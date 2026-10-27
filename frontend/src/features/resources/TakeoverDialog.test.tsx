import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  requestTakeover: vi.fn(),
  completeTakeover: vi.fn(),
}))

vi.mock("./api", () => ({
  requestTakeover: mocks.requestTakeover,
  completeTakeover: mocks.completeTakeover,
}))

import { TakeoverDialog } from "./TakeoverDialog"

describe("TakeoverDialog", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.requestTakeover.mockResolvedValue({ takeover: { id: 9, status: "waiting" } })
    mocks.completeTakeover.mockResolvedValue({ takeover: { id: 9, status: "completed" } })
  })

  afterEach(cleanup)

  it("requests a takeover with an enum reason, instructions and expiry", async () => {
    const onDone = vi.fn().mockResolvedValue(undefined)
    render(<TakeoverDialog mode="request" sessionId={3} onClose={vi.fn()} onDone={onDone} />)

    // Spec §7.6: the backend only accepts the closed reason enum, so the form
    // must be a select and free text belongs in instructions.text.
    const reason = screen.getByLabelText("接管原因")
    expect(reason.tagName).toBe("SELECT")
    const select = reason as HTMLSelectElement
    expect(select.value).toBe("other")
    expect(Array.from(select.options).map((option) => option.value)).toEqual([
      "mfa",
      "captcha",
      "approval",
      "login_unknown",
      "other",
    ])

    fireEvent.change(reason, { target: { value: "mfa" } })
    fireEvent.change(screen.getByLabelText("操作指引"), {
      target: { value: "5 分钟内完成短信验证码" },
    })
    fireEvent.change(screen.getByLabelText("有效期（秒）"), { target: { value: "900" } })
    fireEvent.click(screen.getByRole("button", { name: "确认发起" }))

    await waitFor(() =>
      expect(mocks.requestTakeover).toHaveBeenCalledWith(3, {
        reason: "mfa",
        instructions: { text: "5 分钟内完成短信验证码" },
        expiresInSeconds: 900,
      }),
    )
    await waitFor(() => expect(onDone).toHaveBeenCalled())
  })

  it("defaults to the other reason and keeps free text in instructions", async () => {
    const onDone = vi.fn().mockResolvedValue(undefined)
    render(<TakeoverDialog mode="request" sessionId={3} onClose={vi.fn()} onDone={onDone} />)

    fireEvent.change(screen.getByLabelText("操作指引"), {
      target: { value: "请人工完成短信验证码" },
    })
    fireEvent.click(screen.getByRole("button", { name: "确认发起" }))

    await waitFor(() =>
      expect(mocks.requestTakeover).toHaveBeenCalledWith(3, {
        reason: "other",
        instructions: { text: "请人工完成短信验证码" },
        // The form sends the documented default expiry explicitly.
        expiresInSeconds: 1800,
      }),
    )
    await waitFor(() => expect(onDone).toHaveBeenCalled())
  })

  it("completes a takeover with token headers", async () => {
    const onDone = vi.fn().mockResolvedValue(undefined)
    render(
      <TakeoverDialog
        mode="complete"
        takeoverId={7}
        defaultKind="token"
        onClose={vi.fn()}
        onDone={onDone}
      />,
    )

    expect((screen.getByLabelText("导入类型") as HTMLSelectElement).value).toBe("token")
    fireEvent.change(screen.getByLabelText("JSON 内容"), {
      target: { value: JSON.stringify({ headers: { Authorization: "Bearer fixture-token" } }) },
    })
    fireEvent.click(screen.getByRole("button", { name: "上传并完成" }))

    await waitFor(() =>
      expect(mocks.completeTakeover).toHaveBeenCalledWith(7, {
        kind: "token",
        headers: { Authorization: "Bearer fixture-token" },
      }),
    )
    await waitFor(() => expect(onDone).toHaveBeenCalled())
  })

  it("completes a takeover with a storage_state file", async () => {
    render(
      <TakeoverDialog
        mode="complete"
        takeoverId={7}
        defaultKind="storage_state"
        onClose={vi.fn()}
        onDone={vi.fn().mockResolvedValue(undefined)}
      />,
    )

    const file = new File([JSON.stringify({ cookies: [], origins: [] })], "state.json", {
      type: "application/json",
    })
    fireEvent.change(screen.getByLabelText("JSON 文件"), { target: { files: [file] } })

    const textarea = screen.getByLabelText("JSON 内容") as HTMLTextAreaElement
    await waitFor(() => expect(textarea.value).toContain('"cookies"'))

    fireEvent.click(screen.getByRole("button", { name: "上传并完成" }))

    await waitFor(() =>
      expect(mocks.completeTakeover).toHaveBeenCalledWith(7, {
        kind: "storage_state",
        storageState: { cookies: [], origins: [] },
      }),
    )
  })

  it("rejects malformed JSON without calling the API", async () => {
    render(
      <TakeoverDialog
        mode="complete"
        takeoverId={7}
        defaultKind="storage_state"
        onClose={vi.fn()}
        onDone={vi.fn().mockResolvedValue(undefined)}
      />,
    )

    fireEvent.change(screen.getByLabelText("JSON 内容"), { target: { value: '{"cookies":' } })
    fireEvent.click(screen.getByRole("button", { name: "上传并完成" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("JSON")
    expect(mocks.completeTakeover).not.toHaveBeenCalled()
  })

  it("rejects a file larger than 5 MiB", async () => {
    render(
      <TakeoverDialog
        mode="complete"
        takeoverId={7}
        defaultKind="storage_state"
        onClose={vi.fn()}
        onDone={vi.fn().mockResolvedValue(undefined)}
      />,
    )

    const oversized = new File([new Uint8Array(5 * 1024 * 1024 + 1)], "big.json", {
      type: "application/json",
    })
    fireEvent.change(screen.getByLabelText("JSON 文件"), { target: { files: [oversized] } })

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("5 MiB")
    expect(mocks.completeTakeover).not.toHaveBeenCalled()
  })
})
