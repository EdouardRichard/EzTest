import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { useState } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import { Alert } from "./Alert"
import { Badge } from "./Badge"
import { Button } from "./Button"
import { Dialog } from "./Dialog"
import { EmptyState } from "./EmptyState"
import { ErrorState } from "./ErrorState"
import { Field } from "./Field"
import { Input } from "./Input"
import { Select } from "./Select"
import { Spinner } from "./Spinner"
import { Table, type TableColumn } from "./Table"
import { Tabs } from "./Tabs"
import { Textarea } from "./Textarea"

afterEach(cleanup)

describe("Button", () => {
  it("fires onClick and defaults to type=button", () => {
    const onClick = vi.fn()
    render(<Button onClick={onClick}>保存</Button>)

    const button = screen.getByRole("button", { name: "保存" })
    expect(button).toHaveAttribute("type", "button")
    fireEvent.click(button)
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it("does not fire while disabled", () => {
    const onClick = vi.fn()
    render(
      <Button disabled onClick={onClick}>
        保存
      </Button>,
    )
    fireEvent.click(screen.getByRole("button", { name: "保存" }))
    expect(onClick).not.toHaveBeenCalled()
  })

  it("honours an explicit submit type for forms", () => {
    render(<Button type="submit">登录</Button>)
    expect(screen.getByRole("button", { name: "登录" })).toHaveAttribute("type", "submit")
  })
})

describe("Field and form controls", () => {
  it("associates the label, hint and error with an input", () => {
    render(
      <Field id="title" label="标题" error="标题不能为空" required>
        <Input id="title" aria-invalid aria-describedby="title-error" />
      </Field>,
    )

    expect(screen.getByLabelText(/标题/)).toHaveAttribute("id", "title")
    const error = screen.getByRole("alert")
    expect(error).toHaveTextContent("标题不能为空")
    expect(error).toHaveAttribute("id", "title-error")
  })

  it("renders a hint when there is no error", () => {
    render(
      <Field id="note" label="说明" hint="一句话说明版本">
        <Input id="note" />
      </Field>,
    )
    expect(screen.getByText("一句话说明版本")).toBeInTheDocument()
    expect(screen.getByLabelText("说明")).toBeInTheDocument()
  })

  it("renders select options and a textarea", () => {
    render(
      <div>
        <Field id="role" label="角色">
          <Select id="role" defaultValue="user">
            <option value="user">普通用户</option>
            <option value="admin">管理员</option>
          </Select>
        </Field>
        <Field id="note" label="备注">
          <Textarea id="note" />
        </Field>
      </div>,
    )

    expect(screen.getByLabelText("角色")).toHaveValue("user")
    expect(screen.getByRole("option", { name: "管理员" })).toBeInTheDocument()
    expect(screen.getByLabelText("备注").tagName).toBe("TEXTAREA")
  })
})

describe("Dialog", () => {
  it("renders nothing while closed", () => {
    render(
      <Dialog open={false} onClose={vi.fn()} title="修改密码">
        body
      </Dialog>,
    )
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })

  it("renders a labelled modal and closes via the close button and Escape", () => {
    const onClose = vi.fn()
    render(
      <Dialog open onClose={onClose} title="修改密码" description="首次登录请改密">
        <p>form body</p>
      </Dialog>,
    )

    const dialog = screen.getByRole("dialog", { name: "修改密码" })
    expect(dialog).toHaveAttribute("aria-modal", "true")
    expect(screen.getByText("form body")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "关闭对话框" }))
    expect(onClose).toHaveBeenCalledTimes(1)

    fireEvent.keyDown(window, { key: "Escape" })
    expect(onClose).toHaveBeenCalledTimes(2)
  })
})

const TAB_ITEMS = [
  { value: "overview", label: "总览" },
  { value: "execution", label: "执行" },
]

function TabsHarness() {
  const [value, setValue] = useState("overview")
  return (
    <div>
      <Tabs items={TAB_ITEMS} value={value} onValueChange={setValue} />
      <span>{value}</span>
    </div>
  )
}

describe("Tabs", () => {
  it("renders a tablist, marks the selected tab and switches on click", () => {
    render(<TabsHarness />)

    expect(screen.getByRole("tablist")).toBeInTheDocument()
    expect(screen.getByRole("tab", { name: "总览" })).toHaveAttribute("aria-selected", "true")
    expect(screen.getByRole("tab", { name: "执行" })).toHaveAttribute("aria-selected", "false")

    fireEvent.click(screen.getByRole("tab", { name: "执行" }))
    expect(screen.getByRole("tab", { name: "执行" })).toHaveAttribute("aria-selected", "true")
    expect(screen.getByText("execution")).toBeInTheDocument()
  })
})

describe("Badge and Alert", () => {
  it("exposes the badge tone", () => {
    render(<Badge tone="success">已启用</Badge>)
    expect(screen.getByText("已启用")).toHaveAttribute("data-tone", "success")
  })

  it("announces warning and error alerts", () => {
    render(
      <div>
        <Alert tone="warning" title="注意">
          默认密码需要修改
        </Alert>
        <Alert tone="danger">登录失败</Alert>
      </div>,
    )

    expect(screen.getAllByRole("alert")).toHaveLength(2)
    expect(screen.getByText("登录失败").closest("[data-tone]")).toHaveAttribute(
      "data-tone",
      "danger",
    )
    expect(screen.getByText("默认密码需要修改").closest("[data-tone]")).toHaveAttribute(
      "data-tone",
      "warning",
    )
  })
})

describe("Table", () => {
  type Row = { id: string; title: string }

  const columns: TableColumn<Row>[] = [
    { key: "title", header: "标题", render: (row) => row.title },
    { key: "id", header: "ID", render: (row) => row.id },
  ]

  it("renders headers and rows", () => {
    render(
      <Table
        ariaLabel="版本列表"
        columns={columns}
        rows={[{ id: "1", title: "迭代" }]}
        rowKey={(row) => row.id}
      />,
    )

    expect(screen.getByRole("table", { name: "版本列表" })).toBeInTheDocument()
    expect(screen.getByRole("columnheader", { name: "标题" })).toBeInTheDocument()
    expect(screen.getByRole("cell", { name: "迭代" })).toBeInTheDocument()
  })

  it("renders the empty fallback when there are no rows", () => {
    render(
      <Table
        ariaLabel="版本列表"
        columns={columns}
        rows={[]}
        rowKey={(row) => row.id}
        empty="暂无版本"
      />,
    )

    expect(screen.getByText("暂无版本")).toBeInTheDocument()
  })
})

describe("EmptyState, ErrorState and Spinner", () => {
  it("renders an empty state with an action", () => {
    render(
      <EmptyState
        title="暂无版本"
        description="创建第一个版本开始测试"
        action={<Button>新建版本</Button>}
      />,
    )

    expect(screen.getByText("暂无版本")).toBeInTheDocument()
    expect(screen.getByText("创建第一个版本开始测试")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "新建版本" })).toBeInTheDocument()
  })

  it("renders an API error with its request id and retry action", () => {
    const onRetry = vi.fn()
    const error = new ApiError({
      status: 503,
      code: "unavailable",
      message: "服务暂不可用",
      requestId: "req_abc123",
      retryable: true,
    })
    render(<ErrorState error={error} onRetry={onRetry} />)

    expect(screen.getByText("服务暂不可用")).toBeInTheDocument()
    expect(screen.getByText(/req_abc123/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it("renders a plain Error and a spinner status", () => {
    render(<ErrorState error={new Error("网络中断")} />)
    expect(screen.getByText("网络中断")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "重试" })).not.toBeInTheDocument()

    render(<Spinner label="正在验证会话" />)
    expect(screen.getByRole("status", { name: "正在验证会话" })).toBeInTheDocument()
  })
})
