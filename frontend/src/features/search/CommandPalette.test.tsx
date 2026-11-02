import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const nav = vi.hoisted(() => ({ push: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => nav }))

import { CommandPalette } from "./CommandPalette"
import { RECENT_SEARCHES_KEY } from "./useGlobalSearch"

const SNAPSHOT = {
  groups: [
    {
      type: "version",
      items: [
        {
          id: "11",
          title: "登录版本",
          subtitle: "",
          version_id: "11",
          version_title: "登录版本",
          status: "running",
          url: "/versions/11?tab=overview",
        },
      ],
    },
    {
      type: "requirement",
      items: [
        {
          id: "21",
          title: "登录需求",
          subtitle: "版本甲",
          version_id: "11",
          version_title: "版本甲",
          status: "clarified",
          url: "/versions/11?tab=requirements&requirement=21",
        },
      ],
    },
    {
      type: "case",
      items: [
        {
          id: "300",
          title: "登录流程",
          subtitle: "版本甲",
          version_id: "11",
          version_title: "版本甲",
          status: "reviewed",
          url: "/versions/11?tab=design&case=300",
        },
        {
          id: "301",
          title: "登出流程",
          subtitle: "版本甲",
          version_id: "11",
          version_title: "版本甲",
          status: "draft",
          url: "/versions/11?tab=design&case=301",
        },
      ],
    },
    {
      type: "issue",
      items: [
        {
          id: "400",
          title: "登录失败",
          subtitle: "版本甲",
          version_id: "11",
          version_title: "版本甲",
          status: "open",
          url: "/versions/11?tab=issues&issue=400",
        },
      ],
    },
    {
      type: "report",
      items: [
        {
          id: "500",
          title: "波次报告",
          subtitle: "ready",
          version_id: "11",
          version_title: "版本甲",
          status: "ready",
          url: "/versions/11?tab=reports&report=500",
        },
      ],
    },
  ],
  total: 6,
  truncated: false,
  degraded: false,
}

function jsonResponse(body: unknown): Response {
  const text = JSON.stringify(body)
  return {
    ok: true,
    status: 200,
    headers: new Headers({ "content-type": "application/json" }),
    text: async () => text,
    json: async () => body,
  } as unknown as Response
}

function okFetch(body: unknown = { data: SNAPSHOT }) {
  return vi.fn().mockResolvedValue(jsonResponse(body))
}

function openPalette(): HTMLInputElement {
  fireEvent.click(screen.getByTestId("global-search-trigger"))
  return screen.getByRole("searchbox", { name: "搜索关键词" }) as HTMLInputElement
}

async function typeQuery(input: HTMLInputElement, value: string): Promise<void> {
  fireEvent.change(input, { target: { value } })
  await act(async () => {
    vi.advanceTimersByTime(300)
  })
  await act(async () => {
    await Promise.resolve()
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  nav.push.mockReset()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  window.localStorage.clear()
})

describe("CommandPalette", () => {
  it("opens with Ctrl/Cmd+K, / and the topbar trigger, and closes with Escape", () => {
    vi.stubGlobal("fetch", okFetch())
    render(<CommandPalette />)

    expect(screen.queryByRole("dialog", { name: "全局搜索" })).toBeNull()

    fireEvent.keyDown(document, { key: "k", ctrlKey: true })
    expect(screen.getByRole("dialog", { name: "全局搜索" })).toBeInTheDocument()
    fireEvent.keyDown(document, { key: "Escape" })
    expect(screen.queryByRole("dialog", { name: "全局搜索" })).toBeNull()

    fireEvent.keyDown(document, { key: "k", metaKey: true })
    expect(screen.getByRole("dialog", { name: "全局搜索" })).toBeInTheDocument()
    fireEvent.keyDown(document, { key: "Escape" })

    fireEvent.keyDown(document, { key: "/" })
    expect(screen.getByRole("dialog", { name: "全局搜索" })).toBeInTheDocument()
    fireEvent.keyDown(document, { key: "Escape" })

    fireEvent.click(screen.getByTestId("global-search-trigger"))
    expect(screen.getByRole("dialog", { name: "全局搜索" })).toBeInTheDocument()
  })

  it("debounces input by 300ms and requests the five types with the default limit", async () => {
    const fetchMock = okFetch()
    vi.stubGlobal("fetch", fetchMock)
    render(<CommandPalette />)
    const input = openPalette()

    fireEvent.change(input, { target: { value: "登录" } })
    expect(fetchMock).not.toHaveBeenCalled()

    await act(async () => {
      vi.advanceTimersByTime(299)
    })
    expect(fetchMock).not.toHaveBeenCalled()

    await act(async () => {
      vi.advanceTimersByTime(1)
    })
    await act(async () => {
      await Promise.resolve()
    })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const requestUrl = new URL(String(fetchMock.mock.calls[0]?.[0]))
    expect(requestUrl.pathname).toBe("/api/search")
    expect(requestUrl.searchParams.get("q")).toBe("登录")
    expect(requestUrl.searchParams.get("types")).toBe("version,requirement,case,issue,report")
    expect(requestUrl.searchParams.get("limit")).toBe("5")
  })

  it("renders the five groups in canonical order with their deep links", async () => {
    vi.stubGlobal("fetch", okFetch())
    render(<CommandPalette />)
    const input = openPalette()
    await typeQuery(input, "登录")

    const groupEls = screen.getAllByTestId(/^global-search-group-/)
    expect(groupEls.map((group) => group.getAttribute("data-type"))).toEqual([
      "version",
      "requirement",
      "case",
      "issue",
      "report",
    ])
    expect(
      within(screen.getByTestId("global-search-group-version")).getAllByText("登录版本").length,
    ).toBeGreaterThan(0)
    expect(screen.getByText("波次报告")).toBeInTheDocument()

    const items = screen.getAllByTestId(/^global-search-item-/)
    expect(items).toHaveLength(6)
    expect(items[0]).toHaveAttribute("data-active", "true")
    expect(items[0]).toHaveAttribute("data-url", "/versions/11?tab=overview")
    expect(items[1]).toHaveAttribute("data-url", "/versions/11?tab=requirements&requirement=21")
  })

  it("navigates with ArrowDown/ArrowUp and opens the active deep link with Enter", async () => {
    vi.stubGlobal("fetch", okFetch())
    render(<CommandPalette />)
    const input = openPalette()
    await typeQuery(input, "登录")

    fireEvent.keyDown(input, { key: "ArrowDown" })
    fireEvent.keyDown(input, { key: "ArrowDown" })
    let items = screen.getAllByTestId(/^global-search-item-/)
    expect(items[2]).toHaveAttribute("data-active", "true")
    expect(items[0]).toHaveAttribute("data-active", "false")

    fireEvent.keyDown(input, { key: "ArrowUp" })
    items = screen.getAllByTestId(/^global-search-item-/)
    expect(items[1]).toHaveAttribute("data-active", "true")

    fireEvent.keyDown(input, { key: "Enter" })
    expect(nav.push).toHaveBeenCalledWith("/versions/11?tab=requirements&requirement=21")
    expect(screen.queryByRole("dialog", { name: "全局搜索" })).toBeNull()
    expect(window.localStorage.getItem(RECENT_SEARCHES_KEY)).toContain("登录")
  })

  it("shows the empty state when the query has no match", async () => {
    vi.stubGlobal(
      "fetch",
      okFetch({
        data: {
          groups: [{ type: "case", items: [] }],
          total: 0,
          truncated: false,
          degraded: false,
        },
      }),
    )
    render(<CommandPalette />)
    const input = openPalette()
    await typeQuery(input, "没有结果")

    expect(screen.getByTestId("global-search-empty")).toHaveTextContent("未找到匹配结果")
    expect(screen.queryAllByTestId(/^global-search-item-/)).toHaveLength(0)
  })

  it("shows loading and drops stale results as soon as a new query starts", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ data: SNAPSHOT }))
    vi.stubGlobal("fetch", fetchMock)
    render(<CommandPalette />)
    const input = openPalette()
    await typeQuery(input, "登录")
    expect(screen.getAllByTestId(/^global-search-item-/)).toHaveLength(6)

    fireEvent.change(input, { target: { value: "登录2" } })
    expect(screen.queryAllByTestId(/^global-search-item-/)).toHaveLength(0)
    expect(screen.getByTestId("global-search-loading")).toBeInTheDocument()

    fireEvent.keyDown(input, { key: "Enter" })
    expect(nav.push).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog", { name: "全局搜索" })).toBeInTheDocument()
  })

  it("shows the error state and retries the search", async () => {
    const fetchMock = vi.fn().mockRejectedValue(new Error("网络不可用"))
    vi.stubGlobal("fetch", fetchMock)
    render(<CommandPalette />)
    const input = openPalette()
    await typeQuery(input, "登录")

    expect(screen.getByTestId("global-search-error")).toBeInTheDocument()
    expect(screen.getByRole("alert")).toHaveTextContent("无法连接 API 服务")
    fireEvent.click(screen.getByTestId("global-search-retry"))
    await act(async () => {
      vi.advanceTimersByTime(300)
    })
    await act(async () => {
      await Promise.resolve()
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("cancels the in-flight request when the query changes", async () => {
    let resolveFirst: ((response: Response) => void) | undefined
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveFirst = resolve
          }),
      )
      .mockResolvedValue(jsonResponse({ data: SNAPSHOT }))
    vi.stubGlobal("fetch", fetchMock)
    render(<CommandPalette />)
    const input = openPalette()

    await act(async () => {
      fireEvent.change(input, { target: { value: "登" } })
      vi.advanceTimersByTime(300)
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const firstInit = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    expect(firstInit?.signal?.aborted).toBe(false)

    fireEvent.change(input, { target: { value: "登录" } })
    expect(firstInit?.signal?.aborted).toBe(true)

    await act(async () => {
      vi.advanceTimersByTime(300)
    })
    await act(async () => {
      await Promise.resolve()
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(resolveFirst).toBeTypeOf("function")
  })

  it("shows degraded and truncated hints from the snapshot", async () => {
    vi.stubGlobal("fetch", okFetch({ data: { ...SNAPSHOT, truncated: true, degraded: true } }))
    render(<CommandPalette />)
    const input = openPalette()
    await typeQuery(input, "登录")

    expect(screen.getByTestId("global-search-degraded")).toHaveTextContent("部分结果暂时不可用")
    expect(screen.getByTestId("global-search-truncated")).toHaveTextContent("结果已截断")
  })

  it("shows at most five recent searches and replays a selected term", () => {
    vi.stubGlobal("fetch", okFetch())
    window.localStorage.setItem(
      RECENT_SEARCHES_KEY,
      JSON.stringify(["一", "二", "三", "四", "五", "六"]),
    )
    render(<CommandPalette />)
    openPalette()

    const recent = screen.getAllByTestId("global-search-recent-item")
    expect(recent).toHaveLength(5)
    expect(recent.map((entry) => entry.textContent)).toEqual(["一", "二", "三", "四", "五"])

    fireEvent.click(recent[0] as HTMLElement)
    expect((screen.getByRole("searchbox", { name: "搜索关键词" }) as HTMLInputElement).value).toBe(
      "一",
    )
  })

  it("keeps keyboard focus inside the dialog", () => {
    vi.stubGlobal("fetch", okFetch())
    render(<CommandPalette />)
    const input = openPalette()
    expect(document.activeElement).toBe(input)

    fireEvent.keyDown(input, { key: "Tab", shiftKey: true })
    const close = screen.getByTestId("global-search-close")
    expect(document.activeElement).toBe(close)

    fireEvent.keyDown(close, { key: "Tab" })
    expect(document.activeElement).toBe(input)
  })
})
