import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { AccessForm } from "./AccessForm"
import type { AccessDTO, SystemDTO } from "./types"

function makeSystem(id: number, key: string, name: string): SystemDTO {
  return {
    id,
    key,
    name,
    kind: "web",
    baseUrl: "http://localhost:13335",
    defaultSessionTtlSeconds: 3600,
    notes: "",
    status: "active",
    createdAt: "2026-10-27T00:00:00Z",
    updatedAt: "2026-10-27T00:00:00Z",
  }
}

const SYSTEMS = [makeSystem(1, "shop", "Shop"), makeSystem(2, "blog", "Blog")]

const ACCESS: AccessDTO = {
  id: 4,
  accountId: 3,
  systemId: 1,
  authType: "basic",
  credentialSet: true,
  usernameOverride: "svc-user",
  loginUrl: "https://example.test/login",
  enabled: false,
  concurrencyLimit: 5,
  sessionTtlSeconds: 1800,
  permissionNote: "只读账号",
}

const BASE_PAYLOAD = {
  systemId: 1,
  authType: "basic",
  usernameOverride: "svc-user",
  loginUrl: "https://example.test/login",
  enabled: false,
  concurrencyLimit: 5,
  sessionTtlSeconds: 1800,
  permissionNote: "只读账号",
}

type SubmitHandler = (input: unknown) => Promise<void>

function renderForm(
  overrides: { access?: AccessDTO; canWrite?: boolean; onSubmit?: SubmitHandler } = {},
) {
  const onSubmit = overrides.onSubmit ?? vi.fn<SubmitHandler>().mockResolvedValue(undefined)
  render(
    <AccessForm
      accountId={3}
      systems={SYSTEMS}
      access={overrides.access}
      canWrite={overrides.canWrite ?? true}
      onSubmit={onSubmit}
    />,
  )
  return onSubmit
}

describe("AccessForm", () => {
  afterEach(() => {
    cleanup()
  })

  it("keeps the credential when the field is left blank", async () => {
    const onSubmit = renderForm({ access: ACCESS })

    expect((screen.getByLabelText("凭据 credential") as HTMLInputElement).value).toBe("")

    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith(BASE_PAYLOAD))
    expect(onSubmit).not.toHaveBeenCalledWith(
      expect.objectContaining({ credential: expect.anything() }),
    )
  })

  it("clears the stored credential only when the clear box is checked", async () => {
    const onSubmit = renderForm({ access: ACCESS })

    fireEvent.click(screen.getByLabelText("清除已保存的凭据"))
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ credential: "" })),
    )
  })

  it("submits a replacement credential without ever echoing the stored one", async () => {
    const onSubmit = renderForm({ access: ACCESS })

    const credential = screen.getByLabelText("凭据 credential") as HTMLInputElement
    expect(credential.value).toBe("")
    expect(credential.type).toBe("password")

    fireEvent.change(credential, { target: { value: "s3cret" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ credential: "s3cret" })),
    )
  })

  it("submits a null ttl when the ttl field is blank", async () => {
    const onSubmit = renderForm({ access: ACCESS })

    fireEvent.change(screen.getByLabelText("会话 TTL（秒）"), { target: { value: "" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ sessionTtlSeconds: null })),
    )
  })

  it("rejects an out-of-range ttl before calling onSubmit", async () => {
    const onSubmit = renderForm({ access: ACCESS })

    fireEvent.change(screen.getByLabelText("会话 TTL（秒）"), { target: { value: "30" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("60")
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("defaults to the first system when the systems list arrives late", async () => {
    const onSubmit = vi.fn<SubmitHandler>().mockResolvedValue(undefined)
    const { rerender } = render(
      <AccessForm accountId={3} systems={[]} canWrite onSubmit={onSubmit} />,
    )

    rerender(<AccessForm accountId={3} systems={SYSTEMS} canWrite onSubmit={onSubmit} />)
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ systemId: 1 })),
    )
  })

  it("renders no credential form for readonly users", () => {
    const { container } = render(
      <AccessForm
        accountId={3}
        systems={SYSTEMS}
        access={ACCESS}
        canWrite={false}
        onSubmit={vi.fn(async (_input: unknown) => {})}
      />,
    )

    expect(container.firstChild).toBeNull()
  })
})
