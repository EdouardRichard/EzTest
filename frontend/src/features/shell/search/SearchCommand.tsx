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

import { cn } from "@/lib/utils"

import {
  readRecentSearches,
  rememberSearch,
  SEARCH_TYPES,
  type SearchItem,
  type SearchSnapshot,
  type SearchType,
  useGlobalSearch,
} from "./useGlobalSearch"

/** Chinese label of each result group (spec §6.2). */
const GROUP_LABELS: Record<SearchType, string> = {
  version: "版本",
  requirement: "需求",
  case: "用例",
  issue: "问题",
}

const GROUP_HINTS: Record<SearchType, string> = {
  version: "打开版本总览",
  requirement: "打开需求",
  case: "打开用例脑图",
  issue: "查看问题",
}

type VisibleGroup = {
  type: SearchType
  items: SearchItem[]
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
 * Topbar global-search entry plus the keyboard command palette (spec §6.2).
 *
 * The trigger opens the palette (`Ctrl/Cmd+K` or `/` also open it); typing runs
 * the debounced, cancellable search hook. Results are grouped by type, the
 * arrow keys move the active item across groups, Enter follows the deep link and
 * Escape closes. A selected query is remembered in localStorage (max five).
 */
export function SearchCommand() {
  const router = useRouter()
  const [mounted, setMounted] = useState(false)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState("")
  const [recent, setRecent] = useState<string[]>([])
  const [activeIndex, setActiveIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)

  const { snapshot, loading, error, retry } = useGlobalSearch(query)
  const groups = useMemo(() => visibleGroupsOf(snapshot), [snapshot])
  const flatItems = useMemo(
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

  // Re-read the recent list every time the palette opens, so another tab's
  // selection is visible without a reload.
  useEffect(() => {
    if (!open) return
    setRecent(readRecentSearches())
    inputRef.current?.focus()
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
  // only outside a text field, Escape closes the palette (spec §6.2).
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault()
        setQuery("")
        setOpen(true)
        return
      }
      if (event.key === "Escape") {
        if (!open) return
        event.preventDefault()
        setOpen(false)
        setQuery("")
        return
      }
      if (event.key === "/" && !open && !isEditableTarget(event.target)) {
        event.preventDefault()
        setQuery("")
        setOpen(true)
      }
    }
    document.addEventListener("keydown", onKeyDown)
    return () => document.removeEventListener("keydown", onKeyDown)
  }, [open])

  function openItem(item: SearchItem): void {
    setRecent(rememberSearch(query, recent))
    setOpen(false)
    setQuery("")
    router.push(item.url)
  }

  function onInputKeyDown(event: ReactKeyboardEvent<HTMLInputElement>): void {
    if (event.key === "ArrowDown") {
      event.preventDefault()
      setActiveIndex((previous) => Math.max(0, Math.min(flatItems.length - 1, previous + 1)))
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
      return
    }
    if (event.key === "Escape") {
      event.preventDefault()
      closePalette()
    }
  }

  const showEmpty = trimmedQuery !== "" && !loading && error === "" && flatItems.length === 0
  const showRecents = trimmedQuery === "" && recent.length > 0

  return (
    <>
      <button
        type="button"
        data-testid="search-trigger"
        onClick={openPalette}
        aria-label="全局搜索"
        aria-haspopup="dialog"
        aria-expanded={open}
        className="flex h-9 items-center gap-2 rounded-lg border border-border bg-card px-2.5 text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Search className="h-4 w-4" aria-hidden="true" />
        <span className="hidden lg:inline">搜索</span>
        <kbd className="hidden rounded border border-border bg-muted px-1 py-0.5 text-[10px] font-medium sm:inline">
          Ctrl K
        </kbd>
      </button>

      {open && mounted
        ? createPortal(
            <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/40 px-4 pt-24 backdrop-blur-sm">
              <div
                role="dialog"
                aria-modal="true"
                aria-label="全局搜索"
                aria-busy={loading || undefined}
                className="w-full max-w-2xl overflow-hidden rounded-xl border border-border bg-card shadow-2xl shadow-black/20"
              >
                <div className="flex items-center gap-2 border-b border-border px-3 py-2.5">
                  <Search className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                  <input
                    ref={inputRef}
                    type="search"
                    aria-label="搜索关键词"
                    placeholder="搜索版本、需求、用例、问题…"
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    onKeyDown={onInputKeyDown}
                    className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                  />
                  {loading ? (
                    <Loader2
                      className="h-4 w-4 shrink-0 animate-spin text-muted-foreground"
                      aria-label="搜索中"
                    />
                  ) : null}
                  <kbd className="hidden shrink-0 rounded border border-border bg-muted px-1 py-0.5 text-[10px] text-muted-foreground sm:inline">
                    Esc
                  </kbd>
                </div>

                <div className="max-h-[60vh] overflow-y-auto p-2">
                  {error === "" ? null : (
                    <div
                      role="alert"
                      className="mb-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2.5"
                    >
                      <p className="text-sm font-medium text-destructive">搜索失败</p>
                      <p className="mt-0.5 text-xs text-muted-foreground">{error}</p>
                      <button
                        type="button"
                        onClick={retry}
                        className="mt-2 inline-flex items-center gap-1 rounded-md border border-border bg-card px-2.5 py-1 text-xs font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        <RefreshCw className="h-3 w-3" aria-hidden="true" />
                        重试
                      </button>
                    </div>
                  )}

                  {snapshot.degraded && error === "" ? (
                    <p
                      data-testid="search-degraded"
                      className="mb-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-1.5 text-xs text-amber-700 dark:text-amber-300"
                    >
                      部分结果暂时不可用
                    </p>
                  ) : null}

                  {showRecents ? (
                    <section data-testid="recent-searches">
                      <p className="px-2 pb-1 pt-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                        最近搜索
                      </p>
                      <ul className="space-y-0.5">
                        {recent.map((term) => (
                          <li key={term}>
                            <button
                              type="button"
                              data-testid="recent-search"
                              onClick={() => {
                                setQuery(term)
                                inputRef.current?.focus()
                              }}
                              className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-sm transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            >
                              <Clock
                                className="h-3.5 w-3.5 shrink-0 text-muted-foreground"
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
                    <p className="px-3 py-8 text-center text-sm text-muted-foreground">
                      输入关键词，搜索版本、需求、用例和问题
                    </p>
                  ) : null}

                  {loading && flatItems.length === 0 && error === "" ? (
                    <p className="flex items-center justify-center gap-2 px-3 py-8 text-sm text-muted-foreground">
                      <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                      正在搜索…
                    </p>
                  ) : null}

                  {showEmpty ? (
                    <p className="px-3 py-8 text-center text-sm text-muted-foreground">
                      未找到匹配结果
                    </p>
                  ) : null}

                  {groups.map((group) => (
                    <section key={group.type} data-testid="search-group" data-kind={group.type}>
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
                                data-testid="search-result-item"
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
                                    {item.subtitle !== "" ? item.subtitle : item.versionName}
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
                  {snapshot.total > 0 ? <span>共 {snapshot.total} 条</span> : null}
                </div>
              </div>
            </div>,
            document.body,
          )
        : null}
    </>
  )
}
