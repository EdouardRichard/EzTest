"use client"

import { Clock, Loader2, RefreshCw, Search } from "lucide-react"
import { useRouter } from "next/navigation"
import {
  type KeyboardEvent as ReactKeyboardEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { createPortal } from "react-dom"

import { cn } from "@/components/ui/cn"

import { SEARCH_LIMIT } from "./api"
import {
  GROUP_HINTS,
  GROUP_LABELS,
  SEARCH_TYPES,
  type SearchItem,
  type SearchSnapshot,
  type SearchType,
} from "./types"
import { readRecentSearches, rememberSearch, useGlobalSearch } from "./useGlobalSearch"

type VisibleGroup = {
  type: SearchType
  items: SearchItem[]
}

type FlatItem = {
  type: SearchType
  item: SearchItem
}

/** Orders the snapshot groups by the canonical type order and drops empties. */
function visibleGroupsOf(snapshot: SearchSnapshot): VisibleGroup[] {
  const byType = new Map<SearchType, SearchItem[]>()
  for (const group of snapshot.groups) byType.set(group.type, group.items)
  const groups: VisibleGroup[] = []
  for (const type of SEARCH_TYPES) {
    const items = byType.get(type) ?? []
    if (items.length > 0) groups.push({ type, items })
  }
  return groups
}

/** True when the keyboard event target consumes text input. */
function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const tag = target.tagName
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || target.isContentEditable
}

/**
 * Topbar global-search entry plus the keyboard command palette (spec §4.3.1/§6).
 *
 * The trigger opens the palette; `Ctrl/Cmd+K` opens it anywhere and `/` opens it
 * outside a text field. Typing runs the debounced, cancellable search hook.
 * Results are grouped by type, the arrow keys move the active item across
 * groups, Enter follows the server deep link and Escape closes. Selecting a
 * result remembers the query in localStorage (at most five terms).
 *
 * The Topbar integration (Task 22) only needs to mount `<CommandPalette />`;
 * this component owns its trigger and the global shortcuts.
 */
export function CommandPalette() {
  const router = useRouter()
  const [mounted, setMounted] = useState(false)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState("")
  const [recent, setRecent] = useState<string[]>([])
  const [activeIndex, setActiveIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const dialogRef = useRef<HTMLDivElement>(null)
  const restoreFocusRef = useRef<HTMLElement | null>(null)

  const { snapshot, loading, error, retry } = useGlobalSearch(query)
  const groups = useMemo(() => visibleGroupsOf(snapshot), [snapshot])
  const flatItems = useMemo<FlatItem[]>(
    () => groups.flatMap((group) => group.items.map((item) => ({ type: group.type, item }))),
    [groups],
  )
  const indexByKey = useMemo(() => {
    const map = new Map<string, number>()
    flatItems.forEach((entry, index) => {
      map.set(`${entry.type}-${entry.item.id}-${entry.item.url}`, index)
    })
    return map
  }, [flatItems])
  const trimmedQuery = query.trim()

  useEffect(() => setMounted(true), [])

  // Re-read the recent list every time the palette opens, remember what had
  // focus, and move focus into the search box.
  useEffect(() => {
    if (!open) return undefined
    setRecent(readRecentSearches())
    restoreFocusRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null
    inputRef.current?.focus()
    return () => {
      restoreFocusRef.current?.focus()
      restoreFocusRef.current = null
    }
  }, [open])

  // A new query or a new result set restarts the keyboard selection at the top.
  // biome-ignore lint/correctness/useExhaustiveDependencies: query/snapshot are intentional reset triggers, not values read by the effect
  useEffect(() => {
    setActiveIndex(0)
  }, [query, snapshot])

  const openPalette = useCallback(() => {
    setQuery("")
    setOpen(true)
  }, [])

  const closePalette = useCallback(() => {
    setOpen(false)
    setQuery("")
  }, [])

  // Global shortcuts: Ctrl/Cmd+K opens anywhere (also over inputs), `/` opens
  // only outside a text field, Escape closes the palette.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault()
        openPalette()
        return
      }
      if (event.key === "Escape") {
        if (!open) return
        event.preventDefault()
        closePalette()
        return
      }
      if (event.key === "/" && !open && !isEditableTarget(event.target)) {
        event.preventDefault()
        openPalette()
      }
    }
    document.addEventListener("keydown", onKeyDown)
    return () => document.removeEventListener("keydown", onKeyDown)
  }, [open, openPalette, closePalette])

  function openItem(item: SearchItem): void {
    setRecent((current) => rememberSearch(query, current))
    setOpen(false)
    setQuery("")
    router.push(item.url)
  }

  function onInputKeyDown(event: ReactKeyboardEvent<HTMLInputElement>): void {
    if (flatItems.length === 0) return
    if (event.key === "ArrowDown") {
      event.preventDefault()
      setActiveIndex((previous) => Math.min(flatItems.length - 1, previous + 1))
      return
    }
    if (event.key === "ArrowUp") {
      event.preventDefault()
      setActiveIndex((previous) => Math.max(0, previous - 1))
      return
    }
    if (event.key === "Enter") {
      event.preventDefault()
      // The snapshot is cleared as soon as a new query loads, but never open a
      // result while the search is in flight: it could belong to the old query.
      if (loading) return
      const target = flatItems[activeIndex] ?? flatItems[0]
      if (target !== undefined) openItem(target.item)
    }
  }

  // Focus trap: keep Tab / Shift+Tab cycling through the dialog's controls.
  function onDialogKeyDown(event: ReactKeyboardEvent<HTMLDivElement>): void {
    if (event.key !== "Tab") return
    const dialog = dialogRef.current
    if (dialog === null) return
    const focusable = Array.from(
      dialog.querySelectorAll<HTMLElement>("button:not([disabled]), input:not([disabled])"),
    )
    if (focusable.length === 0) return
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    const active = document.activeElement
    if (event.shiftKey) {
      if (active === first || active === null || !dialog.contains(active)) {
        event.preventDefault()
        last.focus()
      }
      return
    }
    if (active === last || active === null || !dialog.contains(active)) {
      event.preventDefault()
      first.focus()
    }
  }

  const showEmpty = trimmedQuery !== "" && !loading && error === "" && flatItems.length === 0
  const showRecents = trimmedQuery === "" && recent.length > 0

  return (
    <>
      <button
        type="button"
        data-testid="global-search-trigger"
        onClick={openPalette}
        aria-label="全局搜索"
        aria-haspopup="dialog"
        aria-expanded={open}
        className="flex h-9 items-center gap-2 rounded-lg border border-border bg-card px-2.5 text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Search className="size-4" aria-hidden="true" />
        <span className="hidden lg:inline">搜索</span>
        <kbd className="hidden rounded border border-border bg-muted px-1 py-0.5 text-[10px] font-medium sm:inline">
          Ctrl K
        </kbd>
      </button>

      {open && mounted
        ? createPortal(
            <div
              data-testid="global-search-overlay"
              className="fixed inset-0 z-50 flex items-stretch justify-center bg-black/40 backdrop-blur-sm sm:items-start sm:p-4 sm:pt-24"
            >
              <button
                type="button"
                data-testid="global-search-backdrop"
                aria-label="关闭全局搜索遮罩"
                onClick={closePalette}
                className="absolute inset-0 h-full w-full cursor-default"
              />
              <div
                ref={dialogRef}
                role="dialog"
                aria-modal="true"
                aria-label="全局搜索"
                aria-busy={loading || undefined}
                data-testid="global-search-dialog"
                onKeyDown={onDialogKeyDown}
                className="relative z-10 flex h-full w-full flex-col overflow-hidden bg-card text-card-foreground sm:h-auto sm:max-w-2xl sm:rounded-xl sm:border sm:border-border sm:shadow-2xl"
              >
                <div className="flex items-center gap-2 border-b border-border px-3 py-2.5">
                  <Search className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                  <input
                    ref={inputRef}
                    type="search"
                    data-testid="global-search-input"
                    aria-label="搜索关键词"
                    placeholder="搜索版本、需求、用例、问题、报告…"
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    onKeyDown={onInputKeyDown}
                    className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                  />
                  {loading ? (
                    <Loader2
                      className="size-4 shrink-0 animate-spin text-muted-foreground"
                      aria-hidden="true"
                    />
                  ) : null}
                  <kbd className="hidden shrink-0 rounded border border-border bg-muted px-1 py-0.5 text-[10px] text-muted-foreground sm:inline">
                    Esc
                  </kbd>
                  <button
                    type="button"
                    data-testid="global-search-close"
                    aria-label="关闭全局搜索"
                    onClick={closePalette}
                    className="shrink-0 rounded-md border border-border bg-card px-2 py-1 text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    关闭
                  </button>
                </div>

                <div className="max-h-[60vh] flex-1 overflow-y-auto p-2 sm:max-h-[60vh]">
                  {error !== "" ? (
                    <div
                      role="alert"
                      data-testid="global-search-error"
                      className="mb-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2.5"
                    >
                      <p className="text-sm font-medium text-destructive">搜索失败</p>
                      <p className="mt-0.5 text-xs text-muted-foreground">{error}</p>
                      <button
                        type="button"
                        data-testid="global-search-retry"
                        onClick={retry}
                        className="mt-2 inline-flex items-center gap-1 rounded-md border border-border bg-card px-2.5 py-1 text-xs font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        <RefreshCw className="size-3" aria-hidden="true" />
                        重试
                      </button>
                    </div>
                  ) : null}

                  {snapshot.degraded && error === "" ? (
                    <p
                      data-testid="global-search-degraded"
                      className="mb-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-1.5 text-xs text-warning"
                    >
                      部分结果暂时不可用
                    </p>
                  ) : null}

                  {snapshot.truncated && error === "" ? (
                    <p
                      data-testid="global-search-truncated"
                      className="mb-2 rounded-lg border border-border bg-muted/50 px-3 py-1.5 text-xs text-muted-foreground"
                    >
                      结果已截断，每类仅显示前 {SEARCH_LIMIT} 条
                    </p>
                  ) : null}

                  {showRecents ? (
                    <section data-testid="global-search-recent">
                      <p className="px-2 pb-1 pt-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                        最近搜索
                      </p>
                      <ul className="space-y-0.5">
                        {recent.map((term) => (
                          <li key={term}>
                            <button
                              type="button"
                              data-testid="global-search-recent-item"
                              onClick={() => {
                                setQuery(term)
                                inputRef.current?.focus()
                              }}
                              className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-sm transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            >
                              <Clock
                                className="size-3.5 shrink-0 text-muted-foreground"
                                aria-hidden="true"
                              />
                              <span className="truncate">{term}</span>
                            </button>
                          </li>
                        ))}
                      </ul>
                    </section>
                  ) : null}

                  {trimmedQuery === "" && !showRecents ? (
                    <p
                      data-testid="global-search-hint"
                      className="px-3 py-8 text-center text-sm text-muted-foreground"
                    >
                      输入关键词，搜索版本、需求、用例、问题和报告
                    </p>
                  ) : null}

                  {loading && flatItems.length === 0 && error === "" ? (
                    <p
                      data-testid="global-search-loading"
                      className="flex items-center justify-center gap-2 px-3 py-8 text-sm text-muted-foreground"
                    >
                      <Loader2 className="size-4 animate-spin" aria-hidden="true" />
                      正在搜索…
                    </p>
                  ) : null}

                  {showEmpty ? (
                    <p
                      data-testid="global-search-empty"
                      className="px-3 py-8 text-center text-sm text-muted-foreground"
                    >
                      未找到匹配结果
                    </p>
                  ) : null}

                  {groups.map((group) => (
                    <section
                      key={group.type}
                      data-testid={`global-search-group-${group.type}`}
                      data-type={group.type}
                    >
                      <p className="px-2 pb-1 pt-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                        {GROUP_LABELS[group.type]}
                        <span className="ml-1.5 font-normal normal-case text-muted-foreground/70">
                          {GROUP_HINTS[group.type]}
                        </span>
                      </p>
                      <ul className="space-y-0.5">
                        {group.items.map((item) => {
                          const index = indexByKey.get(`${group.type}-${item.id}-${item.url}`) ?? 0
                          const active = index === activeIndex
                          return (
                            <li key={`${group.type}-${item.id}-${item.url}`}>
                              <button
                                type="button"
                                data-testid={`global-search-item-${group.type}-${item.id}`}
                                data-active={active ? "true" : "false"}
                                data-url={item.url}
                                onMouseEnter={() => setActiveIndex(index)}
                                onClick={() => openItem(item)}
                                className={cn(
                                  "flex w-full items-start gap-2 rounded-lg px-2.5 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                                  active ? "bg-muted" : "hover:bg-muted/60",
                                )}
                              >
                                <span className="min-w-0 flex-1">
                                  <span className="block truncate text-sm font-medium">
                                    {item.title}
                                  </span>
                                  <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
                                    {item.subtitle !== "" ? item.subtitle : item.version_title}
                                  </span>
                                </span>
                                {item.status === "" ? null : (
                                  <span className="shrink-0 rounded-full border border-border bg-muted px-1.5 py-0.5 text-[10px] leading-none text-muted-foreground">
                                    {item.status}
                                  </span>
                                )}
                              </button>
                            </li>
                          )
                        })}
                      </ul>
                    </section>
                  ))}
                </div>

                <div className="flex items-center justify-between border-t border-border px-3 py-2 text-[11px] text-muted-foreground">
                  <span>↑↓ 选择 · Enter 打开 · Esc 关闭</span>
                  {snapshot.total > 0 ? (
                    <span data-testid="global-search-total">共 {snapshot.total} 条</span>
                  ) : null}
                </div>
              </div>
            </div>,
            document.body,
          )
        : null}
    </>
  )
}
