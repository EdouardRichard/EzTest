import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { AgentEvent, AgentMessageDetail } from "./agent-api"

import { Transcript } from "./Transcript"

function makeActivity(seq: number, detail: Record<string, unknown>): AgentEvent {
  return {
    seq,
    channel: "agent:1",
    type: "agent.activity",
    ts: "2026-10-27T00:00:00Z",
    summary: `activity ${seq}`,
    agent_role: "runtime-diagnostic",
    session_id: "1",
    detail,
  }
}

function makeDetail(overrides: Partial<AgentMessageDetail> = {}): AgentMessageDetail {
  return {
    id: 1,
    session_id: 1,
    run_id: 9,
    role: "assistant",
    content: [],
    reasoning: "",
    tool_calls: null,
    tool_call_id: null,
    tool_name: null,
    usage: null,
    created_at: "2026-10-27T00:00:00Z",
    ...overrides,
  }
}

const RUN_STARTED = makeActivity(1, {
  kind: "run_started",
  run_id: 9,
  profile_key: "runtime-diagnostic",
  model: "deepseek-flash",
})

describe("Transcript", () => {
  afterEach(() => {
    cleanup()
  })

  it("renders thinking collapsed and pairs a tool call with its result", async () => {
    const getDetail = vi.fn(async (messageId: number) => {
      if (messageId === 42) {
        return makeDetail({ id: 42, reasoning: "完整推理内容" })
      }
      if (messageId === 43) {
        // The real DTO shape: the assistant message owns `tool_calls`; the tool
        // message only carries its result content.
        return makeDetail({
          id: 43,
          role: "assistant",
          content: [{ type: "text", text: "正在调用工具。" }],
          tool_calls: [{ id: "call_1", name: "test.echo", arguments: '{"text":"完整参数"}' }],
        })
      }
      if (messageId === 77) {
        return makeDetail({
          id: 77,
          role: "tool",
          content: [{ type: "text", text: "完整工具结果" }],
          tool_calls: null,
          tool_call_id: "call_1",
          tool_name: "test.echo",
        })
      }
      throw new Error(`unexpected message id ${messageId}`)
    })

    const activities = [
      RUN_STARTED,
      makeActivity(2, {
        kind: "thinking",
        run_id: 9,
        message_id: 42,
        part: 0,
        final: true,
        text: "我需要先检查运行时健康。",
      }),
      makeActivity(3, {
        kind: "tool_use",
        run_id: 9,
        call_id: "call_1",
        tool: "test.echo",
        args: '{"text":"ping"}',
        args_truncated: false,
        message_id: 43,
      }),
      makeActivity(4, {
        kind: "tool_result",
        run_id: 9,
        call_id: "call_1",
        tool: "test.echo",
        status: "ok",
        duration_ms: 12,
        result: "pong",
        result_truncated: false,
        error_code: "",
        message_id: 77,
      }),
    ]

    render(<Transcript activities={activities} status="live" getDetail={getDetail} />)

    const thinkingButton = screen.getByRole("button", { name: "思考" })
    expect(thinkingButton.getAttribute("aria-expanded")).toBe("false")
    expect(screen.queryByText("我需要先检查运行时健康。")).toBeNull()

    const toolButton = screen.getByRole("button", { name: "工具 test.echo" })
    expect(toolButton.getAttribute("aria-expanded")).toBe("false")
    // Collapsed row summarises the streamed args preview next to the tool name.
    expect(toolButton.textContent).toContain('· {"text":"ping"}')
    expect(screen.queryByText("pong")).toBeNull()
    expect(screen.getByText("成功")).toBeTruthy()
    expect(screen.getByText("12ms")).toBeTruthy()

    fireEvent.click(thinkingButton)
    expect(thinkingButton.getAttribute("aria-expanded")).toBe("true")
    await waitFor(() => expect(getDetail).toHaveBeenCalledWith(42))
    expect(await screen.findByText("完整推理内容")).toBeTruthy()

    fireEvent.click(toolButton)
    // Full arguments come from the assistant message, not the tool message.
    await waitFor(() => expect(getDetail).toHaveBeenCalledWith(43))
    await waitFor(() => expect(getDetail).toHaveBeenCalledWith(77))
    expect(await screen.findByText(/"text": "完整参数"/)).toBeTruthy()
    expect(await screen.findByText("test.echo")).toBeTruthy()
    expect(await screen.findByText("完整工具结果")).toBeTruthy()
  })

  it("falls back to the streamed args preview when tool_use has no message_id", async () => {
    const getDetail = vi.fn(async (messageId: number) => {
      if (messageId === 77) {
        return makeDetail({
          id: 77,
          role: "tool",
          content: [{ type: "text", text: "完整工具结果" }],
          tool_calls: null,
          tool_call_id: "call_1",
          tool_name: "test.echo",
        })
      }
      throw new Error(`unexpected message id ${messageId}`)
    })

    const activities = [
      RUN_STARTED,
      makeActivity(2, {
        kind: "tool_use",
        run_id: 9,
        call_id: "call_1",
        tool: "test.echo",
        args: '{"text":"ping"}',
        args_truncated: false,
      }),
      makeActivity(3, {
        kind: "tool_result",
        run_id: 9,
        call_id: "call_1",
        tool: "test.echo",
        status: "ok",
        duration_ms: 5,
        result: "pong",
        result_truncated: false,
        message_id: 77,
      }),
    ]

    render(<Transcript activities={activities} status="live" getDetail={getDetail} />)

    fireEvent.click(screen.getByRole("button", { name: "工具 test.echo" }))
    await waitFor(() => expect(getDetail).toHaveBeenCalledWith(77))
    expect(getDetail).toHaveBeenCalledTimes(1)
    expect(await screen.findByText(/"text": "ping"/)).toBeTruthy()
    expect(await screen.findByText("完整工具结果")).toBeTruthy()
  })

  it("falls back to the streamed args preview when the assistant detail has no tool_calls", async () => {
    const getDetail = vi.fn(async (messageId: number) => {
      if (messageId === 50) return makeDetail({ id: 50, role: "assistant", tool_calls: null })
      throw new Error(`unexpected message id ${messageId}`)
    })

    const activities = [
      RUN_STARTED,
      makeActivity(2, {
        kind: "tool_use",
        run_id: 9,
        call_id: "call_1",
        tool: "test.echo",
        args: '{"text":"ping"}',
        args_truncated: false,
        message_id: 50,
      }),
    ]

    render(<Transcript activities={activities} status="live" getDetail={getDetail} />)

    fireEvent.click(screen.getByRole("button", { name: "工具 test.echo" }))
    await waitFor(() => expect(getDetail).toHaveBeenCalledWith(50))
    expect(await screen.findByText(/"text": "ping"/)).toBeTruthy()
    expect(screen.getByText("执行中")).toBeTruthy()
  })

  it("concatenates text chunks by message_id and part", () => {
    const activities = [
      RUN_STARTED,
      makeActivity(2, {
        kind: "text",
        run_id: 9,
        message_id: 42,
        part: 0,
        final: false,
        text: "第一段",
      }),
      makeActivity(3, {
        kind: "text",
        run_id: 9,
        message_id: 42,
        part: 0,
        final: true,
        text: "拼接完成",
      }),
      makeActivity(4, {
        kind: "text",
        run_id: 9,
        message_id: 42,
        part: 1,
        final: true,
        text: "第二段",
      }),
      makeActivity(5, {
        kind: "text",
        run_id: 9,
        message_id: 43,
        part: 0,
        final: true,
        text: "另一个消息",
      }),
    ]

    render(<Transcript activities={activities} status="live" />)

    expect(screen.getByText("第一段拼接完成")).toBeTruthy()
    expect(screen.getByText("第二段")).toBeTruthy()
    expect(screen.getByText("另一个消息")).toBeTruthy()
  })

  it("groups activities by run and keeps user messages with their run", () => {
    const activities = [
      makeActivity(1, { kind: "user_message", message_id: 1, text: "检查运行时健康" }),
      makeActivity(2, { kind: "run_started", run_id: 9, profile_key: "runtime-diagnostic" }),
      makeActivity(3, {
        kind: "text",
        run_id: 9,
        message_id: 42,
        part: 0,
        final: true,
        text: "第一轮回答",
      }),
      makeActivity(4, { kind: "user_message", message_id: 2, text: "继续" }),
      makeActivity(5, { kind: "run_started", run_id: 10, profile_key: "runtime-diagnostic" }),
      makeActivity(6, {
        kind: "text",
        run_id: 10,
        message_id: 43,
        part: 0,
        final: true,
        text: "第二轮回答",
      }),
      makeActivity(7, { kind: "run_finished", run_id: 10, status: "done" }),
    ]

    render(<Transcript activities={activities} status="live" />)

    expect(screen.getByText("检查运行时健康")).toBeTruthy()
    expect(screen.getByText("继续")).toBeTruthy()
    expect(screen.getByText("运行 #9")).toBeTruthy()
    expect(screen.getByText("运行 #10")).toBeTruthy()
    expect(screen.getByText("第一轮回答")).toBeTruthy()
    expect(screen.getByText("第二轮回答")).toBeTruthy()
    expect(screen.getByText("运行结束")).toBeTruthy()
  })

  it("shows decisions as warning rows and folds usage at the end of the run", () => {
    const activities = [
      RUN_STARTED,
      makeActivity(2, {
        kind: "decision",
        run_id: 9,
        decision: "context_truncated",
        reason: "历史过长",
      }),
      makeActivity(3, {
        kind: "usage",
        run_id: 9,
        turn: 1,
        prompt_tokens: 1000,
        completion_tokens: 300,
        reasoning_tokens: 200,
        total_tokens: 1500,
      }),
      makeActivity(4, {
        kind: "usage",
        run_id: 9,
        turn: 2,
        prompt_tokens: 500,
        completion_tokens: 100,
        reasoning_tokens: 100,
        total_tokens: 700,
      }),
      makeActivity(5, { kind: "run_finished", run_id: 9, status: "done" }),
    ]

    render(<Transcript activities={activities} status="live" />)

    expect(screen.getByText(/context_truncated/)).toBeTruthy()
    expect(screen.getByText(/历史过长/)).toBeTruthy()

    const usageButton = screen.getByRole("button", { name: "用量" })
    expect(usageButton.getAttribute("aria-expanded")).toBe("false")
    expect(screen.queryByText(/共 2200 tokens/)).toBeNull()

    fireEvent.click(usageButton)
    expect(usageButton.getAttribute("aria-expanded")).toBe("true")
    expect(screen.getByText(/共 2200 tokens/)).toBeTruthy()
    expect(screen.getByText(/第 1 回合/)).toBeTruthy()
    expect(screen.getByText(/第 2 回合/)).toBeTruthy()
  })

  it("shows the terminal reason and abort/error code for a finished run", () => {
    const activities = [
      makeActivity(1, { kind: "run_started", run_id: 9 }),
      makeActivity(2, {
        kind: "run_finished",
        run_id: 9,
        status: "aborted",
        terminal_reason: "aborted_tools",
        abort_code: "user_abort",
      }),
      makeActivity(3, { kind: "run_started", run_id: 10 }),
      makeActivity(4, {
        kind: "run_finished",
        run_id: 10,
        status: "failed",
        terminal_reason: "model_error",
        error_code: "finish_reason=length",
      }),
    ]

    render(<Transcript activities={activities} status="live" />)

    expect(screen.getByText("运行终止：aborted_tools（user_abort）")).toBeTruthy()
    expect(screen.getByText("运行出错：model_error（finish_reason=length）")).toBeTruthy()
  })

  it("shows the error code and duration for a failed tool call", () => {
    const activities = [
      RUN_STARTED,
      makeActivity(2, {
        kind: "tool_use",
        run_id: 9,
        call_id: "call_2",
        tool: "test.fail",
        args: '{"mode":"panic"}',
        args_truncated: false,
      }),
      makeActivity(3, {
        kind: "tool_result",
        run_id: 9,
        call_id: "call_2",
        tool: "test.fail",
        status: "error",
        duration_ms: 1200,
        result: "panic: boom",
        result_truncated: false,
        error_code: "tool_panic",
        message_id: 78,
      }),
    ]

    render(<Transcript activities={activities} status="live" />)

    expect(screen.getByText("失败")).toBeTruthy()
    expect(screen.getByText(/tool_panic/)).toBeTruthy()
    expect(screen.getByText("1200ms")).toBeTruthy()
  })

  it("loads earlier activities from the load-more control", () => {
    const onLoadEarlier = vi.fn()
    render(
      <Transcript activities={[RUN_STARTED]} status="live" hasMore onLoadEarlier={onLoadEarlier} />,
    )

    fireEvent.click(screen.getByRole("button", { name: "加载更早" }))
    expect(onLoadEarlier).toHaveBeenCalledTimes(1)
  })

  it("never shows the empty state while connecting or reconnecting", () => {
    const { rerender } = render(<Transcript activities={[]} status="reconnecting" />)
    expect(screen.queryByText("暂无消息")).toBeNull()
    expect(screen.getByText("正在连接会话…")).toBeTruthy()

    rerender(<Transcript activities={[]} status="connecting" />)
    expect(screen.queryByText("暂无消息")).toBeNull()

    rerender(<Transcript activities={[]} status="live" />)
    expect(screen.getByText("暂无消息")).toBeTruthy()

    rerender(<Transcript activities={[]} status="closed" />)
    expect(screen.queryByText("暂无消息")).toBeNull()
    expect(screen.getByText("连接已断开")).toBeTruthy()
  })

  it("auto-scrolls new activities unless the user scrolled up", () => {
    const first = makeActivity(1, {
      kind: "text",
      run_id: 9,
      message_id: 42,
      part: 0,
      final: true,
      text: "第一条",
    })
    const second = makeActivity(2, {
      kind: "text",
      run_id: 9,
      message_id: 42,
      part: 1,
      final: true,
      text: "第二条",
    })
    const third = makeActivity(3, {
      kind: "text",
      run_id: 9,
      message_id: 42,
      part: 2,
      final: true,
      text: "第三条",
    })

    const { rerender } = render(<Transcript activities={[first]} status="live" />)
    const log = screen.getByRole("log", { name: "转录内容" })
    Object.defineProperty(log, "scrollHeight", { configurable: true, value: 1000 })
    Object.defineProperty(log, "clientHeight", { configurable: true, value: 300 })

    rerender(<Transcript activities={[first, second]} status="live" />)
    expect(log.scrollTop).toBe(1000)

    log.scrollTop = 120
    fireEvent.scroll(log)
    rerender(<Transcript activities={[first, second, third]} status="live" />)
    expect(log.scrollTop).toBe(120)
  })
})
