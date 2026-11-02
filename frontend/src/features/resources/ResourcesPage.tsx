"use client"

import { useRouter } from "next/navigation"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { type TabItem, Tabs } from "@/components/ui/Tabs"

import { getSut, listSuts } from "./api"
import { CodeRepoPanel } from "./CodeRepoPanel"
import { DataAccessPanel } from "./DataAccessPanel"
import { PageAPIPanel } from "./PageAPIPanel"
import { SUTPanel } from "./SUTPanel"
import type { ResourceScope, ResourceTab, Sut } from "./types"

export const RESOURCE_TABS: TabItem[] = [
  { value: "suts", label: "SUT 与环境" },
  { value: "pages", label: "页面与 API" },
  { value: "data", label: "数据与账号" },
  { value: "repos", label: "代码仓库" },
]

const TAB_VALUES: ResourceTab[] = ["suts", "pages", "data", "repos"]

/** Normalizes a `?tab=` value; unknown/absent values fall back to the SUT tab. */
export function parseResourceTab(value: string | string[] | null | undefined): ResourceTab {
  const candidate = Array.isArray(value) ? value[0] : value
  return TAB_VALUES.includes(candidate as ResourceTab) ? (candidate as ResourceTab) : "suts"
}

function tabLabel(tab: ResourceTab): string {
  return RESOURCE_TABS.find((item) => item.value === tab)?.label ?? "SUT 与环境"
}

function pickEnvironmentId(detail: Sut, currentId: string): string {
  if (detail.environments.some((environment) => environment.id === currentId)) return currentId
  return (
    detail.environments.find((environment) => environment.is_default)?.id ??
    detail.environments[0]?.id ??
    ""
  )
}

/** `?tab=` deep link for the resources console (spec §11.1). */
export function ResourcesPage({ initialTab }: { initialTab?: string | string[] | null }) {
  const router = useRouter()
  const [tab, setTab] = useState<ResourceTab>(() => parseResourceTab(initialTab))
  const [suts, setSuts] = useState<Sut[]>([])
  const [selectedSutId, setSelectedSutId] = useState("")
  const [detail, setDetail] = useState<Sut | null>(null)
  const [selectedEnvironmentId, setSelectedEnvironmentId] = useState("")
  const [sutsLoading, setSutsLoading] = useState(true)
  const [sutsError, setSutsError] = useState<unknown>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState<unknown>(null)

  const selectedSutIdRef = useRef("")
  const detailRequestRef = useRef(0)

  const loadDetail = useCallback(async (sutId: string): Promise<void> => {
    if (sutId.length === 0) {
      detailRequestRef.current += 1
      setDetail(null)
      setDetailError(null)
      setDetailLoading(false)
      setSelectedEnvironmentId("")
      return
    }

    const requestId = detailRequestRef.current + 1
    detailRequestRef.current = requestId
    setDetailLoading(true)
    setDetailError(null)
    try {
      const next = await getSut(sutId)
      if (detailRequestRef.current !== requestId) return
      setDetail(next)
      setSelectedEnvironmentId((current) => pickEnvironmentId(next, current))
    } catch (caught) {
      if (detailRequestRef.current !== requestId) return
      setDetail(null)
      setDetailError(caught)
    } finally {
      if (detailRequestRef.current === requestId) setDetailLoading(false)
    }
  }, [])

  const loadSuts = useCallback(async (preferredSutId?: string): Promise<string> => {
    setSutsLoading(true)
    setSutsError(null)
    try {
      const items = await listSuts()
      setSuts(items)
      const current = selectedSutIdRef.current
      const nextId =
        preferredSutId && items.some((sut) => sut.id === preferredSutId)
          ? preferredSutId
          : items.some((sut) => sut.id === current)
            ? current
            : (items[0]?.id ?? "")
      selectedSutIdRef.current = nextId
      setSelectedSutId(nextId)
      return nextId
    } catch (caught) {
      setSutsError(caught)
      return ""
    } finally {
      setSutsLoading(false)
    }
  }, [])

  useEffect(() => {
    void (async () => {
      const sutId = await loadSuts()
      await loadDetail(sutId)
    })()
  }, [loadSuts, loadDetail])

  const refresh = useCallback(
    async (preferredSutId?: string) => {
      const sutId = await loadSuts(preferredSutId)
      await loadDetail(sutId)
    },
    [loadSuts, loadDetail],
  )

  const selectSut = useCallback(
    (id: string) => {
      selectedSutIdRef.current = id
      setSelectedSutId(id)
      void loadDetail(id)
    },
    [loadDetail],
  )

  function changeTab(value: string): void {
    const next = parseResourceTab(value)
    setTab(next)
    router.replace(`/resources?tab=${next}`)
  }

  const scope = useMemo<ResourceScope>(
    () => ({
      suts,
      sutId: selectedSutId,
      environmentId: selectedEnvironmentId,
      detail,
      loading: sutsLoading,
      detailLoading,
      error: sutsError,
      detailError,
    }),
    [
      suts,
      selectedSutId,
      selectedEnvironmentId,
      detail,
      sutsLoading,
      detailLoading,
      sutsError,
      detailError,
    ],
  )

  const retry = useCallback(() => {
    void refresh()
  }, [refresh])

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
            工作区
          </p>
          <h1 className="mt-1 text-xl font-semibold text-foreground">资源</h1>
          <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            以 SUT 为中心管理环境、页面/API、目标库、测试账号与代码仓；版本创建向导会复用这些资源。
          </p>
        </div>
      </header>

      <Tabs items={RESOURCE_TABS} value={tab} onValueChange={changeTab} ariaLabel="资源分类" />

      <section role="tabpanel" aria-label={tabLabel(tab)}>
        {tab === "suts" ? (
          <SUTPanel scope={scope} onSelectSut={selectSut} onRefresh={refresh} onRetry={retry} />
        ) : null}
        {tab === "pages" ? (
          <PageAPIPanel
            scope={scope}
            onSelectSut={selectSut}
            onSelectEnvironment={setSelectedEnvironmentId}
            onRefresh={() => refresh()}
            onRetry={retry}
          />
        ) : null}
        {tab === "data" ? (
          <DataAccessPanel
            scope={scope}
            onSelectSut={selectSut}
            onSelectEnvironment={setSelectedEnvironmentId}
            onRefresh={() => refresh()}
            onRetry={retry}
          />
        ) : null}
        {tab === "repos" ? <CodeRepoPanel /> : null}
      </section>
    </div>
  )
}
