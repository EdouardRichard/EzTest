"use client"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"

import { PAUSE_MODE_LABEL } from "./labels"
import type { CaseRun, RunWave } from "./types"

export type PausedBannerProps = {
  wave: RunWave | null
  caseRun: CaseRun | null
  onResumeWave: () => void
  onResumeCaseRun: () => void
  busy?: boolean
}

/**
 * Paused banner (spec §12.1): wave or case-run pause mode/reason, the
 * completed count and the resume action. Pause is execution control only, so
 * the version header keeps showing `running` (D31) and this banner explains it.
 */
export function PausedBanner({
  wave,
  caseRun,
  onResumeWave,
  onResumeCaseRun,
  busy = false,
}: PausedBannerProps) {
  if (wave === null && caseRun === null) return null

  return (
    <Alert tone="warning" title="执行已暂停">
      <div className="flex flex-wrap items-center gap-2">
        {wave !== null ? (
          <>
            <span className="text-[11px]">波次 #{wave.id}</span>
            <Badge tone="warning">
              {wave.pause_mode === "" ? "已暂停" : PAUSE_MODE_LABEL[wave.pause_mode]}
            </Badge>
            {wave.pause_reason.length > 0 ? (
              <span className="text-[11px] text-muted-foreground">原因：{wave.pause_reason}</span>
            ) : null}
            <span className="text-[11px] text-muted-foreground">
              已完成 {wave.counts.passed}/{wave.counts.total}
            </span>
            <Button size="sm" disabled={busy} onClick={onResumeWave}>
              恢复
            </Button>
          </>
        ) : null}

        {caseRun !== null ? (
          <>
            <span className="text-[11px]">用例「{caseRun.title}」已暂停</span>
            <Button size="sm" disabled={busy} onClick={onResumeCaseRun}>
              恢复
            </Button>
          </>
        ) : null}
      </div>
    </Alert>
  )
}
