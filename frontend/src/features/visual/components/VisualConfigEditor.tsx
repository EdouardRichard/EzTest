"use client"

import { useCallback, useEffect, useState } from "react"

import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Textarea } from "@/components/ui/Textarea"

import { listVisualAssertions, upsertVisualAssertion } from "../api"
import type { VisualAssertion, VisualRegion } from "../types"

export type VisualConfigTarget = {
  step_seq: number
  assertion_seq: number
  expected?: string
  /** The P2 design assertion id; the P5 design view currently omits it. */
  case_assertion_id?: string
}

export type VisualConfigEditorProps = {
  versionId: string
  caseVersionId: string
  /** Design assertion being configured (kind must be visual). */
  assertion: VisualConfigTarget
  /** P2 `case_assertion` decimal id; falls back to `assertion.case_assertion_id`. */
  caseAssertionId?: string
  /** Optional `version_sut` binding; omitted means the server default. */
  versionSutId?: string
  /** Existing config; absent means "create with defaults". */
  initial?: VisualAssertion | null
  /** Older case version used by the "copy previous config" action. */
  previousCaseVersionId?: string
  onSaved?: (assertion: VisualAssertion) => void
}

type EditorErrors = {
  selector?: string
  threshold?: string
  viewportWidth?: string
  viewportHeight?: string
  deviceScaleFactor?: string
  region?: string
  waitPolicy?: string
}

const SELECTOR_LIMIT = 500
const DEFAULT_THRESHOLD = 0.005
const DEFAULT_VIEWPORT = { width: 1280, height: 720 }
const DEFAULT_DPR = 1

function percentText(value: number): string {
  return String(Number((value * 100).toFixed(4)))
}

function regionFrom(value: unknown): VisualRegion | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null
  const candidate = value as Record<string, unknown>
  const x = Number(candidate.x)
  const y = Number(candidate.y)
  const width = Number(candidate.width)
  const height = Number(candidate.height)
  if ([x, y, width, height].some((item) => !Number.isFinite(item))) return null
  return { x, y, width, height }
}

function waitPolicyText(value: unknown): string {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return ""
  const keys = Object.keys(value)
  return keys.length === 0 ? "" : JSON.stringify(value, null, 2)
}

/**
 * Visual assertion config editor (spec §7.1/§9.4): selector, normalized crop
 * region, threshold, viewport, DPR and wait-policy. Saving upserts the config by
 * the P2 design assertion id; when that id is not exposed by the design API the
 * editor stays read-only instead of guessing an id.
 */
export function VisualConfigEditor({
  versionId,
  assertion,
  caseAssertionId,
  versionSutId,
  initial = null,
  previousCaseVersionId,
  onSaved,
}: VisualConfigEditorProps) {
  const resolvedCaseAssertionId = caseAssertionId ?? assertion.case_assertion_id

  const [selector, setSelector] = useState(initial?.selector ?? "")
  const [threshold, setThreshold] = useState(percentText(initial?.threshold ?? DEFAULT_THRESHOLD))
  const [viewportWidth, setViewportWidth] = useState(
    String(initial?.viewport_width ?? DEFAULT_VIEWPORT.width),
  )
  const [viewportHeight, setViewportHeight] = useState(
    String(initial?.viewport_height ?? DEFAULT_VIEWPORT.height),
  )
  const [deviceScaleFactor, setDeviceScaleFactor] = useState(
    String(initial?.device_scale_factor ?? DEFAULT_DPR),
  )
  const [regionEnabled, setRegionEnabled] = useState(
    initial?.region !== null && initial?.region !== undefined,
  )
  const [region, setRegion] = useState<VisualRegion>(
    regionFrom(initial?.region) ?? { x: 0, y: 0, width: 1, height: 1 },
  )
  const [waitPolicy, setWaitPolicy] = useState(waitPolicyText(initial?.wait_policy))
  const [errors, setErrors] = useState<EditorErrors>({})
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [copying, setCopying] = useState(false)

  // A saved/updated config replaces the form state (revision changes included).
  useEffect(() => {
    if (initial === null || initial === undefined) return
    setSelector(initial.selector)
    setThreshold(percentText(initial.threshold))
    setViewportWidth(String(initial.viewport_width))
    setViewportHeight(String(initial.viewport_height))
    setDeviceScaleFactor(String(initial.device_scale_factor))
    setRegionEnabled(initial.region !== null && initial.region !== undefined)
    setRegion(regionFrom(initial.region) ?? { x: 0, y: 0, width: 1, height: 1 })
    setWaitPolicy(waitPolicyText(initial.wait_policy))
  }, [initial])

  const copyPrevious = useCallback(async () => {
    if (previousCaseVersionId === undefined) return
    setCopying(true)
    setError(null)
    setNotice(null)
    try {
      const previous = await listVisualAssertions(versionId, {
        caseVersionId: previousCaseVersionId,
      })
      const match =
        previous.find(
          (item) =>
            item.step_seq === assertion.step_seq && item.assertion_seq === assertion.assertion_seq,
        ) ?? previous[0]
      if (match === undefined) {
        setNotice("上一版本没有可复制的视觉配置")
        return
      }
      setSelector(match.selector)
      setThreshold(percentText(match.threshold))
      setViewportWidth(String(match.viewport_width))
      setViewportHeight(String(match.viewport_height))
      setDeviceScaleFactor(String(match.device_scale_factor))
      setRegionEnabled(match.region !== null && match.region !== undefined)
      setRegion(regionFrom(match.region) ?? { x: 0, y: 0, width: 1, height: 1 })
      setWaitPolicy(waitPolicyText(match.wait_policy))
      setErrors({})
      setNotice("已复制上一版本配置，保存后生效")
    } catch (cause) {
      setError(cause)
    } finally {
      setCopying(false)
    }
  }, [assertion.assertion_seq, assertion.step_seq, previousCaseVersionId, versionId])

  function validate(): { values: number[]; fail: boolean; parsedWait: unknown } {
    const next: EditorErrors = {}
    const thresholdValue = Number(threshold)
    const width = Number(viewportWidth)
    const height = Number(viewportHeight)
    const dpr = Number(deviceScaleFactor)

    if (selector.length > SELECTOR_LIMIT) {
      next.selector = `Selector 不能超过 ${SELECTOR_LIMIT} 字符`
    }
    if (!Number.isFinite(thresholdValue) || thresholdValue < 0 || thresholdValue > 10) {
      next.threshold = "阈值需在 0–10% 之间"
    }
    if (!Number.isInteger(width) || width < 1 || width > 10000) {
      next.viewportWidth = "视口宽度需在 1–10000 之间"
    }
    if (!Number.isInteger(height) || height < 1 || height > 10000) {
      next.viewportHeight = "视口高度需在 1–10000 之间"
    }
    if (!Number.isFinite(dpr) || dpr < 0.5 || dpr > 4) {
      next.deviceScaleFactor = "设备像素比需在 0.5–4 之间"
    }
    if (regionEnabled) {
      const values = [region.x, region.y, region.width, region.height]
      if (values.some((item) => !Number.isFinite(item) || item < 0 || item > 1)) {
        next.region = "区域坐标需为 0–1 的归一化值"
      } else if (region.width <= 0 || region.height <= 0) {
        next.region = "区域宽高必须大于 0"
      }
    }

    let parsedWait: unknown = null
    if (waitPolicy.trim().length > 0) {
      try {
        const parsed: unknown = JSON.parse(waitPolicy)
        if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
          next.waitPolicy = "等待策略必须是 JSON 对象"
        } else {
          parsedWait = parsed
        }
      } catch {
        next.waitPolicy = "等待策略必须是 JSON 对象"
      }
    }

    setErrors(next)
    return {
      values: [thresholdValue, width, height, dpr],
      fail: Object.keys(next).length > 0,
      parsedWait,
    }
  }

  async function save(): Promise<void> {
    if (resolvedCaseAssertionId === undefined || saving) return
    const result = validate()
    setError(null)
    setNotice(null)
    if (result.fail) return

    const [thresholdValue] = result.values
    setSaving(true)
    try {
      const saved = await upsertVisualAssertion(versionId, resolvedCaseAssertionId, {
        ...(versionSutId !== undefined ? { versionSutId } : {}),
        selector: selector.trim(),
        region: regionEnabled ? region : null,
        threshold: Number((thresholdValue / 100).toFixed(6)),
        viewport: { width: result.values[1], height: result.values[2] },
        deviceScaleFactor: result.values[3],
        waitPolicy: result.parsedWait as Record<string, unknown> | null,
      })
      setNotice(`视觉配置已保存（revision ${saved.config_revision}）`)
      onSaved?.(saved)
    } catch (cause) {
      setError(cause)
    } finally {
      setSaving(false)
    }
  }

  return (
    <section
      data-testid="visual-config-editor"
      aria-label={`视觉断言 ${assertion.step_seq}.${assertion.assertion_seq}`}
      className="rounded-lg border border-border bg-muted/20 p-3"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-xs font-semibold text-foreground">
          视觉断言 {assertion.step_seq}.{assertion.assertion_seq}
          {assertion.expected !== undefined && assertion.expected.length > 0
            ? ` · ${assertion.expected}`
            : ""}
        </h4>
        {previousCaseVersionId !== undefined ? (
          <Button
            variant="secondary"
            size="sm"
            disabled={copying}
            onClick={() => {
              void copyPrevious()
            }}
          >
            {copying ? "复制中…" : "复制上一版本配置"}
          </Button>
        ) : null}
      </header>

      {resolvedCaseAssertionId === undefined ? (
        <p
          data-testid="visual-config-missing-id"
          className="mt-2 rounded-md border border-warning/30 bg-warning/10 px-2 py-1.5 text-[11px] text-warning"
        >
          无法确定设计断言 ID，当前只能查看/复制配置；保存需后端在设计断言中返回 case_assertion_id。
        </p>
      ) : null}

      <div className="mt-2 grid gap-2 sm:grid-cols-2">
        <Field
          id={`visual-selector-${assertion.step_seq}-${assertion.assertion_seq}`}
          label="Selector"
        >
          <Input
            id={`visual-selector-${assertion.step_seq}-${assertion.assertion_seq}`}
            value={selector}
            maxLength={SELECTOR_LIMIT}
            placeholder="留空 = 整页 viewport"
            onChange={(event) => setSelector(event.target.value)}
          />
        </Field>
        <Field
          id={`visual-threshold-${assertion.step_seq}-${assertion.assertion_seq}`}
          label="阈值（%）"
          hint="差异像素比例，0–10"
        >
          <Input
            id={`visual-threshold-${assertion.step_seq}-${assertion.assertion_seq}`}
            type="number"
            min={0}
            max={10}
            step={0.1}
            value={threshold}
            onChange={(event) => setThreshold(event.target.value)}
          />
        </Field>
        <Field id={`visual-viewport-width-${assertion.step_seq}`} label="视口宽">
          <Input
            id={`visual-viewport-width-${assertion.step_seq}`}
            type="number"
            min={1}
            max={10000}
            value={viewportWidth}
            onChange={(event) => setViewportWidth(event.target.value)}
          />
        </Field>
        <Field id={`visual-viewport-height-${assertion.step_seq}`} label="视口高">
          <Input
            id={`visual-viewport-height-${assertion.step_seq}`}
            type="number"
            min={1}
            max={10000}
            value={viewportHeight}
            onChange={(event) => setViewportHeight(event.target.value)}
          />
        </Field>
        <Field id={`visual-dpr-${assertion.step_seq}`} label="设备像素比">
          <Input
            id={`visual-dpr-${assertion.step_seq}`}
            type="number"
            min={0.5}
            max={4}
            step={0.25}
            value={deviceScaleFactor}
            onChange={(event) => setDeviceScaleFactor(event.target.value)}
          />
        </Field>
      </div>

      <div className="mt-2">
        <label className="flex items-center gap-1.5 text-[11px] text-foreground">
          <input
            type="checkbox"
            checked={regionEnabled}
            onChange={(event) => setRegionEnabled(event.target.checked)}
            className="size-3.5 accent-primary"
          />
          启用区域裁剪
        </label>
        {regionEnabled ? (
          <div className="mt-1.5 grid grid-cols-2 gap-2 sm:grid-cols-4">
            {(
              [
                ["区域 X", "x"],
                ["区域 Y", "y"],
                ["区域宽", "width"],
                ["区域高", "height"],
              ] as const
            ).map(([label, key]) => (
              <Field key={key} id={`visual-region-${key}-${assertion.step_seq}`} label={label}>
                <Input
                  id={`visual-region-${key}-${assertion.step_seq}`}
                  type="number"
                  min={0}
                  max={1}
                  step={0.01}
                  value={region[key]}
                  onChange={(event) =>
                    setRegion((current) => ({ ...current, [key]: Number(event.target.value) }))
                  }
                />
              </Field>
            ))}
          </div>
        ) : null}
      </div>

      <div className="mt-2">
        <Field
          id={`visual-wait-${assertion.step_seq}`}
          label="等待策略（JSON）"
          hint="留空 = networkidle + fonts + 冻结动画"
        >
          <Textarea
            id={`visual-wait-${assertion.step_seq}`}
            rows={2}
            value={waitPolicy}
            className="min-h-16 text-xs"
            onChange={(event) => setWaitPolicy(event.target.value)}
          />
        </Field>
      </div>

      {Object.keys(errors).length > 0 ? (
        <ul role="alert" className="mt-2 space-y-0.5 text-[11px] text-destructive">
          {Object.values(errors).map((message) => (
            <li key={message}>{message}</li>
          ))}
        </ul>
      ) : null}

      <div className="mt-2 flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          disabled={saving || resolvedCaseAssertionId === undefined}
          onClick={() => {
            void save()
          }}
        >
          {saving ? "保存中…" : "保存视觉配置"}
        </Button>
        {notice !== null ? (
          <p role="status" className="text-[11px] text-success">
            {notice}
          </p>
        ) : null}
      </div>

      {error !== null ? (
        <div className="mt-1.5">
          <InlineError error={error} />
        </div>
      ) : null}
    </section>
  )
}
