"use client"

import { ChevronDown, Layers, Loader2, RefreshCw, Search } from "lucide-react"
import { usePathname, useRouter } from "next/navigation"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { getVersion, listVersions } from "@/features/versions/api"
import type { Version, VersionStatus } from "@/features/versions/types"
import { cn } from "@/lib/utils"

/** localStorage key of the recent-version list (spec §6.1). */
export const RECENT_VERSIONS_KEY = "eztest.recentVersions"

/** The dropdown keeps at most eight recent versions (spec §6.1). */
export const MAX_RECENT_VERSIONS = 8

/** Candidate pool fetched once when the dropdown opens. */
const VERSION_POOL_LIMIT = 50

type DisplayVersion = {
  id: string
  name: string
  label: string
  status: VersionStatus
}

type StoredVersion = {
  id: string
  name: string
  status: VersionStatus
}

const STATUS_META: Record<VersionStatus, { label: string; variant: StatusBadgeVariant }> = {
  draft: { label: "草稿", variant: "neutral" },
  clarifying: { label: "澄清中", variant: "info" },
  designing: { label: "设计中", variant: "warning" },
  reviewing: { label: "评审中", variant: "info" },
  executing: { label: "执行中", variant: "warning" },
  continuing: { label: "继续中", variant: "warning" },
  completed: { label: "已完成", variant: "success" },
  archived: { label: "已归档", variant: "neutral" },
}

function statusMeta(status: string): { label: string; variant: StatusBadgeVariant } {
  return (
    STATUS_META[status as VersionStatus] ?? {
      label: status === "" ? "未知" : status,
      variant: "neutral",
    }
  )
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

/** Reads the current version id from a `/versions/{id}` pathname (or its mindmap route). */
export function currentVersionID(pathname: string | null): string {
  const match = /^\/versions\/([^/]+)/.exec(pathname ?? "")
  return match === null ? "" : decodeURIComponent(match[1] ?? "")
}

function toDisplay(version: Version | StoredVersion): DisplayVersion {
  return {
    id: version.id,
    name: version.name,
    label: "label" in version && typeof version.label === "string" ? version.label : "",
    status: version.status,
  }
}

function readRecent(): StoredVersion[] {
  if (typeof window === "undefined") return []
  try {
    const parsed: unknown = JSON.parse(window.localStorage.getItem(RECENT_VERSIONS_KEY) ?? "[]")
    if (!Array.isArray(parsed)) return []
    const recent: StoredVersion[] = []
    for (const raw of parsed) {
      if (!isRecord(raw)) continue
      if (
        typeof raw.id !== "string" ||
        typeof raw.name !== "string" ||
        typeof raw.status !== "string"
      ) {
        continue
      }
      recent.push({ id: raw.id, name: raw.name, status: raw.status as VersionStatus })
    }
    return recent.slice(0, MAX_RECENT_VERSIONS)
  } catch {
    // A corrupt payload must not break the shell: start with an empty list.
    return []
  }
}

function writeRecent(recent: StoredVersion[]): void {
  if (typeof window === "undefined") return
  try {
    window.localStorage.setItem(RECENT_VERSIONS_KEY, JSON.stringify(recent))
  } catch {
    // Storage can be unavailable (private mode); the switcher still works for
    // the current page.
  }
}

function dedupeByID(versions: DisplayVersion[]): DisplayVersion[] {
  const seen = new Set<string>()
  const unique: DisplayVersion[] = []
  for (const version of versions) {
    if (seen.has(version.id)) continue
    seen.add(version.id)
    unique.push(version)
  }
  return unique
}

type VersionOptionProps = {
  option: DisplayVersion
  active: boolean
  testID: string
  onSelect: (target: DisplayVersion, push: (href: string) => void) => void
}

/**
 * One dropdown option. The router hook lives in this child, which only mounts
 * while the dropdown is open: the topbar itself stays router-free and the
 * options use `router.push` (client navigation, no full reload) when clicked.
 */
function VersionOption({ option, active, testID, onSelect }: VersionOptionProps) {
  const router = useRouter()
  const meta = statusMeta(option.status)
  return (
    <li>
      <button
        type="button"
        data-testid={testID}
        onClick={() => onSelect(option, (href) => router.push(href))}
        className={cn(
          "flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-sm transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          active && "bg-muted",
        )}
      >
        <span className="min-w-0 flex-1">
          <span className="block truncate font-medium">{option.name}</span>
          {option.label === "" ? null : (
            <span className="block truncate text-[11px] text-muted-foreground">{option.label}</span>
          )}
        </span>
        <StatusBadge variant={meta.variant} className="shrink-0 px-1.5 py-0.5">
          {meta.label}
        </StatusBadge>
      </button>
    </li>
  )
}

/**
 * Preserves the current tab/batch/filter query when switching inside a version
 * context (spec §6.1). The version detail container falls back to its default
 * batch when the target version does not own the preserved `batch` id.
 */
function preservedSearch(pathname: string | null): string {
  if (typeof window === "undefined") return ""
  if (currentVersionID(pathname) === "") return ""
  return window.location.search
}

/**
 * Topbar version switcher (spec §6.1): the current version name plus status
 * badge after the logo, and a dropdown with the recent versions from
 * localStorage and a local search over the loaded version list. Switching uses
 * `router.push` (no full reload), preserves the tab/batch query and records the
 * target in the recent list. Request failures stay inside the dropdown with a
 * retry and never block the shell.
 */
export function VersionSwitcher() {
  const pathname = usePathname()
  const versionID = currentVersionID(pathname)

  const [current, setCurrent] = useState<Version | null>(null)
  const [currentLoading, setCurrentLoading] = useState(false)
  const [currentFailed, setCurrentFailed] = useState(false)
  const [recent, setRecent] = useState<StoredVersion[]>([])
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState("")
  const [pool, setPool] = useState<Version[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")
  const [attempted, setAttempted] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  const requestRef = useRef(0)

  useEffect(() => {
    setRecent(readRecent())
  }, [])

  // Resolve the version named by the route so the trigger can show its current
  // name and status badge. A missing/failed read degrades to the generic label.
  useEffect(() => {
    if (versionID === "") {
      setCurrent(null)
      setCurrentFailed(false)
      setCurrentLoading(false)
      return
    }
    let active = true
    setCurrentLoading(true)
    setCurrentFailed(false)
    getVersion(versionID)
      .then((version) => {
        if (active) setCurrent(version)
      })
      .catch(() => {
        if (active) {
          setCurrent(null)
          setCurrentFailed(true)
        }
      })
      .finally(() => {
        if (active) setCurrentLoading(false)
      })
    return () => {
      active = false
    }
  }, [versionID])

  const loadVersions = useCallback(async (): Promise<void> => {
    const requestID = ++requestRef.current
    setLoading(true)
    setError("")
    try {
      const page = await listVersions({ limit: VERSION_POOL_LIMIT })
      if (requestRef.current !== requestID) return
      setPool(page.items)
    } catch {
      if (requestRef.current !== requestID) return
      setError("版本列表加载失败，请重试。")
    } finally {
      if (requestRef.current === requestID) {
        setLoading(false)
        setAttempted(true)
      }
    }
  }, [])

  // Load the candidate list the first time the dropdown opens, so the topbar
  // itself pays no version-list request until the user asks for it.
  useEffect(() => {
    if (open && !attempted && !loading) void loadVersions()
  }, [open, attempted, loading, loadVersions])

  // Close the dropdown on navigation and on outside click / Escape.
  // biome-ignore lint/correctness/useExhaustiveDependencies: pathname is the navigation trigger; the effect body only closes the menu
  useEffect(() => {
    setOpen(false)
  }, [pathname])

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

  const options = useMemo<DisplayVersion[]>(() => {
    const trimmed = query.trim().toLowerCase()
    const loaded = dedupeByID([...pool.map(toDisplay), ...recent.map(toDisplay)])
    if (trimmed !== "") {
      return loaded
        .filter((version) =>
          `${version.name} ${version.label} ${version.id}`.toLowerCase().includes(trimmed),
        )
        .slice(0, 12)
    }
    if (recent.length > 0) return recent.map(toDisplay)
    return pool.slice(0, MAX_RECENT_VERSIONS).map(toDisplay)
  }, [pool, recent, query])

  const showRecent = query.trim() === "" && recent.length > 0
  const currentMeta = current === null ? null : statusMeta(current.status)

  function selectVersion(target: DisplayVersion, push: (href: string) => void): void {
    const entry: StoredVersion = { id: target.id, name: target.name, status: target.status }
    const next = [entry, ...recent.filter((candidate) => candidate.id !== entry.id)].slice(
      0,
      MAX_RECENT_VERSIONS,
    )
    setRecent(next)
    writeRecent(next)
    setOpen(false)
    setQuery("")
    push(`/versions/${target.id}${preservedSearch(pathname)}`)
  }

  let triggerLabel = "选择版本"
  if (versionID !== "") {
    if (currentLoading) triggerLabel = "加载中…"
    else if (current !== null) triggerLabel = current.name
    else if (currentFailed) triggerLabel = "版本不可用"
  }

  return (
    <div ref={containerRef} className="relative min-w-0">
      <button
        type="button"
        onClick={() => setOpen((previous) => !previous)}
        aria-label="切换版本"
        aria-haspopup="dialog"
        aria-expanded={open}
        className="flex h-9 min-w-0 max-w-[15rem] items-center gap-2 rounded-lg border border-border bg-card px-2.5 text-sm transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Layers className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="truncate font-medium">{triggerLabel}</span>
        {currentMeta === null ? null : (
          <StatusBadge variant={currentMeta.variant} className="shrink-0 px-1.5 py-0.5">
            {currentMeta.label}
          </StatusBadge>
        )}
        <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
      </button>

      {open ? (
        <div
          role="dialog"
          aria-label="版本切换"
          className="absolute left-0 top-full z-50 mt-2 w-72 overflow-hidden rounded-xl border border-border bg-card shadow-xl shadow-black/10"
        >
          <div className="flex items-center gap-2 border-b border-border px-2.5 py-2">
            <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
            <label htmlFor="version-switcher-search" className="sr-only">
              搜索版本
            </label>
            <input
              id="version-switcher-search"
              aria-label="搜索版本"
              type="search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="搜索版本…"
              className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            />
          </div>

          <div className="max-h-80 overflow-y-auto p-1.5">
            {error === "" ? null : (
              <div
                role="alert"
                className="rounded-lg border border-destructive/30 bg-destructive/10 px-2.5 py-2"
              >
                <p className="text-xs text-destructive">{error}</p>
                <button
                  type="button"
                  onClick={() => void loadVersions()}
                  className="mt-1.5 inline-flex items-center gap-1 rounded-md border border-border bg-card px-2 py-1 text-xs font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <RefreshCw className="h-3 w-3" aria-hidden="true" />
                  重试
                </button>
              </div>
            )}

            {loading && options.length === 0 && error === "" ? (
              <p className="flex items-center justify-center gap-2 px-2.5 py-6 text-xs text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                正在加载版本…
              </p>
            ) : null}

            {!loading && error === "" && options.length === 0 ? (
              <p className="px-2.5 py-6 text-center text-xs text-muted-foreground">暂无版本</p>
            ) : null}

            {options.length === 0 ? null : (
              <p className="px-2 pb-1 pt-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                {query.trim() === "" ? (showRecent ? "最近版本" : "最新版本") : "搜索结果"}
              </p>
            )}
            <ul className="space-y-0.5">
              {options.map((option) => (
                <VersionOption
                  key={option.id}
                  option={option}
                  active={option.id === versionID}
                  testID={showRecent ? "recent-version" : `version-option-${option.id}`}
                  onSelect={selectVersion}
                />
              ))}
            </ul>
          </div>
        </div>
      ) : null}
    </div>
  )
}
