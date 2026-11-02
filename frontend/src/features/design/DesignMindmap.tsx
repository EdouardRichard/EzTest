"use client"

import {
  Background,
  BackgroundVariant,
  type Edge,
  type Node,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
} from "@xyflow/react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import type { CaseRunVerdict } from "@/features/execution/types"

import { CaseCard } from "./CaseCard"
import { type CaseFilterKey, CaseFilters } from "./CaseFilters"
import {
  ALL_FILTER_VALUE,
  type CaseFilterValues,
  cachedLayoutDesignGraph,
  DEFAULT_CASE_FILTERS,
  type DesignExecutionResults,
  type DesignLayoutNode,
  filterDesignLayout,
  matchDesignNodes,
} from "./layout"
import type { DesignNodeActions } from "./nodeActions"
import { RequirementNode } from "./RequirementNode"
import { ScenarioNode } from "./ScenarioNode"
import type { CaseVersionStatus, DesignGraph } from "./types"

import "@xyflow/react/dist/style.css"

const NODE_TYPES = {
  requirement: RequirementNode,
  scenario: ScenarioNode,
  case: CaseCard,
} as const

/** Throttle window for search-driven fitView calls (spec §9.3). */
const FIT_VIEW_THROTTLE_MS = 250

/** Case-status card tint; the arbitrary variant reaches the card inside the node. */
const STATUS_NODE_CLASS: Record<Exclude<CaseVersionStatus, "superseded">, string> = {
  draft: "case-status-draft [&>div]:border-info/60 [&>div]:bg-info/5",
  confirmed: "case-status-confirmed [&>div]:border-success/60 [&>div]:bg-success/5",
  stale: "case-status-stale [&>div]:border-warning/60 [&>div]:bg-warning/5",
}

/** Execution result is the card's left inset accent so it can coexist with the status. */
const RESULT_NODE_CLASS: Record<CaseRunVerdict, string> = {
  passed: "case-result-passed [&>div]:shadow-[inset_4px_0_0_var(--color-success)]",
  failed: "case-result-failed [&>div]:shadow-[inset_4px_0_0_var(--color-destructive)]",
  blocked: "case-result-blocked [&>div]:shadow-[inset_4px_0_0_var(--color-warning)]",
  inconclusive:
    "case-result-inconclusive [&>div]:shadow-[inset_4px_0_0_var(--color-muted-foreground)]",
}

const SEARCH_HIT_CLASS = "mindmap-search-hit rounded-xl ring-2 ring-warning/70"

const EMPTY_RESULTS: DesignExecutionResults = {}

type FitState = {
  timer: ReturnType<typeof setTimeout> | null
  pending: string[] | null
}

export type DesignMindmapProps = {
  graph: DesignGraph
  selectedCaseId?: string | null
  /** `${scope}:${targetId}` while a confirmation request is in flight. */
  confirmingKey?: string | null
  /** Latest terminal execution verdict per case id; absent means "not executed". */
  executionResults?: DesignExecutionResults
  onSelectCase: (caseId: string) => void
  onConfirmCase: (caseId: string) => void
  onConfirmScenario: (scenarioId: string) => void
  onConfirmRequirement: (requirementId: string) => void
}

function MindmapCanvas({
  graph,
  selectedCaseId = null,
  confirmingKey = null,
  executionResults = EMPTY_RESULTS,
  onSelectCase,
  onConfirmCase,
  onConfirmScenario,
  onConfirmRequirement,
}: DesignMindmapProps) {
  const { zoomIn, zoomOut, fitView } = useReactFlow()
  const [filters, setFilters] = useState<CaseFilterValues>(DEFAULT_CASE_FILTERS)
  const hasExecutionResults = Object.keys(executionResults).length > 0

  // A result filter without execution data would hide every case behind the
  // no-match overlay, so it only becomes active once a caller passes verdicts.
  useEffect(() => {
    if (hasExecutionResults) return
    setFilters((current) =>
      current.result === ALL_FILTER_VALUE ? current : { ...current, result: ALL_FILTER_VALUE },
    )
  }, [hasExecutionResults])

  const actions = useMemo<DesignNodeActions>(
    () => ({
      onSelectCase,
      onConfirmCase,
      onConfirmScenario,
      onConfirmRequirement,
      confirmingKey,
    }),
    [onSelectCase, onConfirmCase, onConfirmScenario, onConfirmRequirement, confirmingKey],
  )

  // The layout is cached by `version_id:graph_hash`, so a snapshot refresh that
  // carries the same content does not recompute every position (spec §9.3).
  const layout = useMemo(() => cachedLayoutDesignGraph(graph), [graph])
  const filteredLayout = useMemo(
    () => filterDesignLayout(layout, filters, executionResults),
    [layout, filters, executionResults],
  )

  const matchedNodes = useMemo(
    () => matchDesignNodes(filteredLayout, filters.query),
    [filteredLayout, filters.query],
  )
  const searchHitIds = useMemo(() => new Set(matchedNodes.map((node) => node.id)), [matchedNodes])
  const matchKey = matchedNodes.map((node) => node.id).join("|")

  // Leading-edge throttle: the first query fits immediately, rapid follow-up
  // keystrokes are coalesced into one trailing fit with the latest matches.
  const fitState = useRef<FitState>({ timer: null, pending: null })
  const fitViewRef = useRef(fitView)
  // `useReactFlow()` is stable today, but keep the ref current without writing
  // it during render (StrictMode- and lint-safe).
  useEffect(() => {
    fitViewRef.current = fitView
  }, [fitView])

  const scheduleFitView = useCallback((nodeIds: string[]) => {
    const state = fitState.current
    const run = (ids: string[]) => {
      void fitViewRef.current({
        nodes: ids.map((id) => ({ id })),
        padding: 0.2,
        duration: 250,
      })
    }
    if (state.timer === null) {
      run(nodeIds)
      state.timer = setTimeout(() => {
        state.timer = null
        const pending = state.pending
        state.pending = null
        if (pending !== null) run(pending)
      }, FIT_VIEW_THROTTLE_MS)
      return
    }
    state.pending = nodeIds
  }, [])

  useEffect(() => {
    if (matchKey.length === 0) {
      // A trailing fit scheduled before the query was cleared must not move the
      // viewport to matches the user no longer sees as selected.
      fitState.current.pending = null
      return
    }
    scheduleFitView(matchKey.split("|"))
  }, [matchKey, scheduleFitView])

  useEffect(() => {
    const state = fitState.current
    return () => {
      if (state.timer !== null) clearTimeout(state.timer)
    }
  }, [])

  const requirementOptions = useMemo(
    () =>
      graph.requirements.map((requirement) => ({
        value: requirement.requirement_id,
        label: requirement.title,
      })),
    [graph.requirements],
  )

  const scenarioOptions = useMemo(() => {
    const requirements =
      filters.requirementId === ALL_FILTER_VALUE
        ? graph.requirements
        : graph.requirements.filter(
            (requirement) => requirement.requirement_id === filters.requirementId,
          )
    return requirements.flatMap((requirement) =>
      requirement.scenarios.map((scenario) => ({
        value: scenario.id,
        label: scenario.name,
      })),
    )
  }, [graph.requirements, filters.requirementId])

  const handleFilterChange = useCallback((key: CaseFilterKey, value: string) => {
    setFilters((current) =>
      key === "requirementId"
        ? { ...current, requirementId: value, scenarioId: ALL_FILTER_VALUE }
        : { ...current, [key]: value },
    )
  }, [])

  const classesFor = useCallback(
    (node: DesignLayoutNode): string => {
      const classes: string[] = []
      if (node.data.kind === "case") {
        classes.push(STATUS_NODE_CLASS[node.data.case.status])
        const result = executionResults[node.data.case.case_id]
        if (result !== undefined) classes.push(RESULT_NODE_CLASS[result])
      }
      if (searchHitIds.has(node.id)) classes.push(SEARCH_HIT_CLASS)
      return classes.join(" ")
    },
    [executionResults, searchHitIds],
  )

  const nodes = useMemo<Node[]>(
    () =>
      filteredLayout.nodes.map((node) => {
        const selected = node.id === `case-${selectedCaseId}`
        return {
          id: node.id,
          type: node.type,
          position: node.position,
          className: classesFor(node),
          selected,
          data: { ...node.data, actions, selected },
        }
      }),
    [filteredLayout, actions, selectedCaseId, classesFor],
  )

  const edges = useMemo<Edge[]>(
    () => filteredLayout.edges.map((edge) => ({ ...edge, type: "smoothstep" })),
    [filteredLayout],
  )

  return (
    <section
      data-testid="design-mindmap"
      aria-label="用例脑图"
      className="flex h-full w-full flex-col overflow-hidden rounded-xl border border-border bg-card"
    >
      <CaseFilters
        values={filters}
        requirementOptions={requirementOptions}
        scenarioOptions={scenarioOptions}
        resultFilterEnabled={hasExecutionResults}
        onChange={handleFilterChange}
      />

      <div className="relative min-h-0 flex-1">
        <div className="absolute right-3 top-3 z-10 flex items-center gap-1">
          <Button
            size="sm"
            variant="secondary"
            data-testid="design-zoom-in"
            aria-label="放大"
            onClick={() => {
              void zoomIn()
            }}
          >
            ＋
          </Button>
          <Button
            size="sm"
            variant="secondary"
            data-testid="design-zoom-out"
            aria-label="缩小"
            onClick={() => {
              void zoomOut()
            }}
          >
            －
          </Button>
          <Button
            size="sm"
            variant="secondary"
            data-testid="design-fit-view"
            aria-label="适应画布"
            onClick={() => {
              void fitView({ padding: 0.2 })
            }}
          >
            适应
          </Button>
        </div>

        {layout.nodes.length === 0 ? (
          <div className="flex h-full items-center justify-center p-6">
            <EmptyState
              title="暂无设计图"
              description="新建或打开设计对话后，设计团队会为您生成需求、场景与用例。"
              className="border-none bg-transparent"
            />
          </div>
        ) : (
          <>
            <ReactFlow
              nodes={nodes}
              edges={edges}
              nodeTypes={NODE_TYPES}
              fitView
              fitViewOptions={{ padding: 0.2 }}
              minZoom={0.2}
              nodesDraggable={false}
              nodesConnectable={false}
              proOptions={{ hideAttribution: true }}
              onNodeClick={(_event, node) => {
                if (node.type === "case") {
                  onSelectCase(node.id.replace(/^case-/, ""))
                }
              }}
            >
              <Background variant={BackgroundVariant.Dots} gap={20} size={1} />
            </ReactFlow>
            {filteredLayout.nodes.length === 0 ? (
              <div className="pointer-events-none absolute inset-0 flex items-center justify-center p-6">
                <p
                  data-testid="design-mindmap-no-matches"
                  role="status"
                  className="rounded-lg border border-border bg-card px-3 py-2 text-xs text-muted-foreground shadow-sm"
                >
                  没有符合筛选条件的用例
                </p>
              </div>
            ) : null}
          </>
        )}
      </div>
    </section>
  )
}

/** Read-only, pan/zoom React Flow mindmap; positions stay local (spec §6.3). */
export function DesignMindmap(props: DesignMindmapProps) {
  return (
    <ReactFlowProvider>
      <MindmapCanvas {...props} />
    </ReactFlowProvider>
  )
}
