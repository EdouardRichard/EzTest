"use client"

import { useEffect, useState } from "react"

import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import { updateVersion } from "@/features/versions/api"

export type AutoDesignSwitchProps = {
  versionId: string
  enabled: boolean
  /** Fired after the version PATCH succeeds. */
  onChanged?: (enabled: boolean) => void
}

/**
 * Version-level `auto_design_new_requirements` toggle (P2). The switch is
 * optimistic; a failed PATCH rolls the visual state back and shows the error.
 * It only matters before the first design conversation exists (spec §4.6).
 */
export function AutoDesignSwitch({ versionId, enabled, onChanged }: AutoDesignSwitchProps) {
  const [checked, setChecked] = useState(enabled)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<unknown>(null)

  useEffect(() => {
    setChecked(enabled)
  }, [enabled])

  async function toggle(): Promise<void> {
    if (saving) return
    const next = !checked
    setChecked(next)
    setSaving(true)
    setError(null)
    try {
      await updateVersion(versionId, { auto_design_new_requirements: next })
      onChanged?.(next)
    } catch (cause) {
      setChecked(!next)
      setError(cause)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex flex-col gap-1">
      <Button
        variant="secondary"
        size="sm"
        role="switch"
        aria-checked={checked}
        aria-label="新需求自动设计"
        title="默认关闭；开启后新澄清需求会自动开设计轮次并创建设计对话"
        data-testid="auto-design-switch"
        disabled={saving}
        onClick={() => {
          void toggle()
        }}
      >
        新需求自动设计：{checked ? "开" : "关"}
      </Button>
      {error !== null ? <InlineError error={error} /> : null}
    </div>
  )
}
