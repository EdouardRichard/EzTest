"use client"

import { Bell } from "lucide-react"
import { useEffect, useRef, useState } from "react"

import { useAuth } from "@/features/auth/AuthProvider"

import { TodoPanel } from "./TodoPanel"
import { useTodos } from "./useTodos"

/**
 * Topbar todo entry (spec §6.3): a bell with the total count badge that opens
 * the grouped todo panel. The hook polls every 30 s and refreshes on route
 * changes; a failed read keeps the bell usable and surfaces the retry inside
 * the panel, never blocking the shell.
 */
export function TodoButton() {
  // Administrators additionally review draft knowledge entries (spec §9.4).
  const { user } = useAuth()
  const { todos, loading, error, refresh } = useTodos(user?.role === "admin")
  const [open, setOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)

  const counted = todos.counts.review + todos.counts.takeover + todos.counts.gate
  const total = counted > 0 ? counted : todos.items.length
  const badge = total > 99 ? "99+" : String(total)

  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: MouseEvent) => {
      const container = containerRef.current
      if (container !== null && event.target instanceof Node && !container.contains(event.target)) {
        setOpen(false)
      }
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false)
    }
    document.addEventListener("mousedown", onPointerDown)
    document.addEventListener("keydown", onKeyDown)
    return () => {
      document.removeEventListener("mousedown", onPointerDown)
      document.removeEventListener("keydown", onKeyDown)
    }
  }, [open])

  return (
    <div ref={containerRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((previous) => !previous)}
        aria-label="待办中心"
        aria-haspopup="dialog"
        aria-expanded={open}
        className="relative grid h-9 w-9 place-items-center rounded-lg border border-border bg-card text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Bell className="h-4 w-4" aria-hidden="true" />
        {total > 0 ? (
          <span
            data-testid="todo-count"
            className="absolute -right-1 -top-1 grid min-w-[1.05rem] place-items-center rounded-full bg-primary px-1 text-[10px] font-semibold leading-[1.05rem] text-primary-foreground"
          >
            {badge}
          </span>
        ) : null}
      </button>

      {open ? (
        <div className="absolute right-0 top-full z-50 mt-2">
          <TodoPanel
            todos={todos}
            loading={loading}
            error={error}
            onRetry={() => void refresh()}
            onClose={() => setOpen(false)}
            onNavigate={() => setOpen(false)}
          />
        </div>
      ) : null}
    </div>
  )
}
