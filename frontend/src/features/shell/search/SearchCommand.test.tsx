import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { RECENT_SEARCHES_KEY } from "./useGlobalSearch"

const nav = vi.hoisted(() => ({ push: vi.fn() }))

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: nav.push }),
}))

import { SearchCommand } from "./SearchCommand"

const SNAPSHOT = {
  groups: [
    {
      type: "version",
      items: [
        {
          id: "11",
          title: "登录版本",
          subtitle: "标签甲",
          versionId: "11",
          versionName: "登录版本",
          status: "reviewing",
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
          subtitle: "登录版本",
          versionId: "11",
          versionName: "登录版本",
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
          subtitle: "登录版本",
          versionId: "11",
          versionName: "登录版本",
          status: "reviewed",
          url: "/versions/11?tab=mindmap&case=300",
        },
        {
          id: "301",
          title: "登出流程",
          subtitle: "登录版本",
          versionId: "11",
          versionName: "登录版本",
          status: "draft",
          url: "/versions/11?tab=mindmap&case=301",
        },
      ],
    },
    {
      type: "issue",
      items: [
        {
          id: "400",
          title: "登录失败",
          subtitle: "登录版本",
          versionId: "11",
          versionName: "登录版本",
          status: "open",
          url: "/versions/11?tab=issues&issue=400",
        },
      ],
    },
  ],
  total: 5,
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

function openCommand(): HTMLElement {
  fireEvent.click(screen.getByTestId("search-trigger"))
  return screen.getByRole("searchbox", { name: "搜索关键词" })
}

async function typeQuery(input: HTMLElement, value: string): Promise<void> {
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

describe("SearchCommand", () => {
  it("opens from the topbar trigger and debounces the query by 300ms", async () => {
    const fetchMock = okFetch()
    vi.stubGlobal("fetch", fetchMock)
    render(<SearchCommand />)

    const input = openCommand()
    fireEvent.change(input, { target: { value: "登录" } })
    expect(fetchMock).not.toHaveBeenCalled()

    await act(async () => {
      vi.advanceTimersByTime(299)
    })
    expect(fetchMock).not.toHaveBeenCalled()

    await act(async () => {
      vi.advanceTimersByTime(1)
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/search?q=")
  })

  it("renders the grouped results, the active item and the deep links", async () => {
    vi.stubGlobal("fetch", okFetch())
    render(<SearchCommand />)
    const input = openCommand()
    await typeQuery(input, "登录")

    expect(screen.getByText("版本")).not.toBeNull()
    expect(screen.getByText("需求")).not.toBeNull()
    expect(screen.getByText("用例")).not.toBeNull()
    expect(screen.getByText("问题")).not.toBeNull()

    const items = screen.getAllByTestId("search-result-item")
    expect(items).toHaveLength(5)
    expect(items[0]?.getAttribute("data-active")).toBe("true")
    expect(items[0]?.getAttribute("data-url")).toBe("/versions/11?tab=overview")
    expect(screen.getAllByText("登录版本").length).toBeGreaterThan(0)
    expect(screen.getByText("登录失败")).not.toBeNull()
  })

  it("opens with Ctrl/Cmd+K and closes with Escape", () => {
    vi.stubGlobal("fetch", okFetch())
    render(<SearchCommand />)

    expect(screen.queryByRole("dialog", { name: "全局搜索" })).toBeNull()
    fireEvent.keyDown(document, { key: "k", ctrlKey: true })
    expect(screen.getByRole("dialog", { name: "全局搜索" })).not.toBeNull()

    fireEvent.keyDown(document, { key: "Escape" })
    expect(screen.queryByRole("dialog", { name: "全局搜索" })).toBeNull()
  })

  it("moves the active item with the arrow keys and opens it with Enter", async () => {
    vi.stubGlobal("fetch", okFetch())
    render(<SearchCommand />)
    const input = openCommand()
    await typeQuery(input, "登录")

    fireEvent.keyDown(input, { key: "ArrowDown" })
    const items = screen.getAllByTestId("search-result-item")
    expect(items[1]?.getAttribute("data-active")).toBe("true")
    expect(items[0]?.getAttribute("data-active")).toBe("false")

    fireEvent.keyDown(input, { key: "Enter" })
    expect(nav.push).toHaveBeenCalledWith("/versions/11?tab=requirements&requirement=21")
    expect(screen.queryByRole("dialog", { name: "全局搜索" })).toBeNull()
    expect(window.localStorage.getItem(RECENT_SEARCHES_KEY)).toContain("登录")
  })

  it("renders the loading state while a search is in flight", async () => {
    let resolveSearch: ((response: Response) => void) | undefined
    const fetchMock = vi.fn().mockImplementation(
      () =>
        new Promise<Response>((resolve) => {
          resolveSearch = resolve
        }),
    )
    vi.stubGlobal("fetch", fetchMock)
    render(<SearchCommand />)
    const input = openCommand()

    await act(async () => {
      fireEvent.change(input, { target: { value: "登录" } })
      vi.advanceTimersByTime(300)
    })
    await act(async () => {
      await Promise.resolve()
    })
    expect(screen.getByText("正在搜索…")).not.toBeNull()

    await act(async () => {
      resolveSearch?.(jsonResponse({ data: SNAPSHOT }))
      await Promise.resolve()
    })
    expect(screen.queryByText("正在搜索…")).toBeNull()
    expect(screen.getAllByText("登录版本").length).toBeGreaterThan(0)
  })

  it("clears the previous results as soon as a new query starts loading", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ data: SNAPSHOT }))
      .mockImplementationOnce(() => new Promise<Response>(() => {}))
    vi.stubGlobal("fetch", fetchMock)
    render(<SearchCommand />)
    const input = openCommand()
    await typeQuery(input, "登录")
    expect(screen.getAllByTestId("search-result-item")).toHaveLength(5)

    fireEvent.change(input, { target: { value: "登录2" } })

    // During the debounce/loading window the previous query's hits must not be
    // shown (nor openable), otherwise a fast Enter opens the wrong deep link.
    expect(screen.queryAllByTestId("search-result-item")).toHaveLength(0)
    expect(screen.getByText("正在搜索…")).not.toBeNull()
  })

  it("does not open the previous result when Enter is pressed while a new query loads", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ data: SNAPSHOT }))
      .mockImplementationOnce(() => new Promise<Response>(() => {}))
    vi.stubGlobal("fetch", fetchMock)
    render(<SearchCommand />)
    const input = openCommand()
    await typeQuery(input, "登录")
    expect(screen.getAllByTestId("search-result-item")).toHaveLength(5)

    fireEvent.change(input, { target: { value: "登录2" } })
    fireEvent.keyDown(input, { key: "Enter" })

    expect(nav.push).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog", { name: "全局搜索" })).not.toBeNull()
  })

  it("shows the degraded hint when only part of the result set is available", async () => {
    vi.stubGlobal("fetch", okFetch({ data: { ...SNAPSHOT, degraded: true } }))
    render(<SearchCommand />)
    const input = openCommand()
    await typeQuery(input, "登录")

    expect(screen.getByTestId("search-degraded").textContent).toContain("部分结果暂时不可用")
  })

  it("opens the palette with the / shortcut outside a text field", () => {
    vi.stubGlobal("fetch", okFetch())
    render(<SearchCommand />)

    expect(screen.queryByRole("dialog", { name: "全局搜索" })).toBeNull()
    fireEvent.keyDown(document, { key: "/" })
    expect(screen.getByRole("dialog", { name: "全局搜索" })).not.toBeNull()

    fireEvent.keyDown(document, { key: "Escape" })
    expect(screen.queryByRole("dialog", { name: "全局搜索" })).toBeNull()
  })

  it("renders the empty state for a query without matches", async () => {
    vi.stubGlobal(
      "fetch",
      okFetch({ data: { groups: [{ type: "case", items: [] }], total: 0, truncated: false } }),
    )
    render(<SearchCommand />)
    const input = openCommand()
    await typeQuery(input, "没有结果")

    expect(screen.getByText("未找到匹配结果")).not.toBeNull()
    expect(screen.queryAllByTestId("search-result-item")).toHaveLength(0)
  })

  it("renders the error state and retries the search", async () => {
    const fetchMock = vi.fn().mockRejectedValue(new Error("网络不可用"))
    vi.stubGlobal("fetch", fetchMock)
    render(<SearchCommand />)
    const input = openCommand()
    await typeQuery(input, "登录")

    expect(screen.getByRole("alert").textContent).toContain("无法连接 API 服务")
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    await act(async () => {
      vi.advanceTimersByTime(300)
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("shows at most five recent searches from localStorage", () => {
    vi.stubGlobal("fetch", okFetch())
    window.localStorage.setItem(
      RECENT_SEARCHES_KEY,
      JSON.stringify(["一", "二", "三", "四", "五", "六"]),
    )
    render(<SearchCommand />)
    openCommand()

    const recent = screen.getAllByTestId("recent-search")
    expect(recent).toHaveLength(5)
    expect(recent.map((entry) => entry.textContent)).toEqual(["一", "二", "三", "四", "五"])

    fireEvent.click(recent[0] as HTMLElement)
    const input = screen.getByRole("searchbox", { name: "搜索关键词" })
    expect((input as HTMLInputElement).value).toBe("一")
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
    render(<SearchCommand />)
    const input = openCommand()

    await act(async () => {
      fireEvent.change(input, { target: { value: "登" } })
      vi.advanceTimersByTime(300)
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const firstSignal = fetchMock.mock.calls[0]?.[1]?.signal as AbortSignal | undefined
    expect(firstSignal?.aborted).toBe(false)

    fireEvent.change(input, { target: { value: "登录" } })
    expect(firstSignal?.aborted).toBe(true)

    await act(async () => {
      vi.advanceTimersByTime(300)
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(resolveFirst).toBeTypeOf("function")
  })
})
