"use client"

import { useCallback, useState } from "react"

export type UseSelectionResult = {
  /** Selected case ids in selection order (no duplicates). */
  selectedIds: string[]
  isSelected: (caseId: string) => boolean
  /** Adds an unselected case id or removes an already selected one. */
  toggle: (caseId: string) => void
  /** Drops the whole selection (batch action finished / user reset). */
  clear: () => void
}

/**
 * Mindmap multi-select state (0.2.1 spec §9.3). Selection is local to the
 * canvas: it is never persisted and a batch action receives an explicit id
 * list, so the backend gate only ever confirms the cases the user checked.
 */
export function useSelection(): UseSelectionResult {
  const [selectedIds, setSelectedIds] = useState<string[]>([])

  const toggle = useCallback((caseId: string) => {
    setSelectedIds((current) =>
      current.includes(caseId) ? current.filter((id) => id !== caseId) : [...current, caseId],
    )
  }, [])

  const clear = useCallback(() => setSelectedIds([]), [])

  const isSelected = useCallback((caseId: string) => selectedIds.includes(caseId), [selectedIds])

  return { selectedIds, isSelected, toggle, clear }
}
