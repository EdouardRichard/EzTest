"use client"

import { useCallback, useEffect, useState } from "react"

import {
  deleteDbTarget,
  describeDBTargetError,
  getDbTarget,
  testDbTarget,
  upsertDbTarget,
} from "../api"
import type { DBTargetDTO, DBTargetTestResultDTO, DBTargetWriteInput } from "../types"

export type UseDBTargetResult = {
  target: DBTargetDTO | null
  loading: boolean
  /** True only after a successful GET; the form must not render otherwise. */
  loaded: boolean
  error: string
  notice: string
  saving: boolean
  deleting: boolean
  testing: boolean
  testResult: DBTargetTestResultDTO | null
  reload: () => Promise<void>
  save: (input: DBTargetWriteInput) => Promise<DBTargetDTO | null>
  remove: () => Promise<void>
  testConnection: (input?: DBTargetWriteInput) => Promise<void>
  clearTestResult: () => void
}

/**
 * Loads and mutates the single read-only database target of one environment
 * (spec §7.8). The API is admin-only; `enabled` keeps ordinary user renders from
 * issuing a request that would only produce a 403. Every failure is classified by
 * `describeDBTargetError`, and the plaintext password only crosses the API
 * boundary as a write field — it is never held after a save.
 */
export function useDBTarget(environmentId: number, enabled = true): UseDBTargetResult {
  const [target, setTarget] = useState<DBTargetDTO | null>(null)
  const [loading, setLoading] = useState(enabled)
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<DBTargetTestResultDTO | null>(null)

  const reload = useCallback(async () => {
    if (!enabled) {
      setLoading(false)
      setLoaded(false)
      return
    }
    setLoading(true)
    setError("")
    try {
      setTarget(await getDbTarget(environmentId))
      setLoaded(true)
    } catch (caught) {
      // A failed GET must never leave a blank form editable: saving it would
      // overwrite a target that may already exist.
      setError(describeDBTargetError(caught))
      setLoaded(false)
    } finally {
      setLoading(false)
    }
  }, [enabled, environmentId])

  useEffect(() => {
    if (!enabled) {
      setLoading(false)
      setLoaded(false)
      return
    }
    void reload()
  }, [enabled, reload])

  const save = useCallback(
    async (input: DBTargetWriteInput): Promise<DBTargetDTO | null> => {
      setSaving(true)
      setError("")
      setNotice("")
      try {
        const saved = await upsertDbTarget(environmentId, input)
        setTarget(saved)
        setTestResult(null)
        setNotice("已保存目标数据库。")
        return saved
      } catch (caught) {
        setError(describeDBTargetError(caught))
        return null
      } finally {
        setSaving(false)
      }
    },
    [environmentId],
  )

  const remove = useCallback(async () => {
    setDeleting(true)
    setError("")
    setNotice("")
    try {
      await deleteDbTarget(environmentId)
      setTarget(null)
      setTestResult(null)
      setNotice("已删除目标数据库。")
    } catch (caught) {
      setError(describeDBTargetError(caught))
    } finally {
      setDeleting(false)
    }
  }, [environmentId])

  const testConnection = useCallback(
    async (input?: DBTargetWriteInput) => {
      setTesting(true)
      setError("")
      setNotice("")
      setTestResult(null)
      try {
        // Call without the optional override argument so the POST carries no
        // body at all; the API then probes the stored configuration (T5).
        const result =
          input === undefined
            ? await testDbTarget(environmentId)
            : await testDbTarget(environmentId, input)
        setTestResult(result)
        // Only a probe of the stored configuration persists last_tested (T5
        // contract); mirror that persisted result locally. An override is never
        // persisted, so the stored summary must stay untouched.
        if (input === undefined) {
          setTarget((previous) =>
            previous === null
              ? previous
              : {
                  ...previous,
                  lastTestedAt: new Date().toISOString(),
                  lastTestStatus: result.ok ? "ok" : "failed",
                  lastTestMessage: result.ok ? "" : (result.error?.message ?? ""),
                },
          )
        }
      } catch (caught) {
        setError(describeDBTargetError(caught))
      } finally {
        setTesting(false)
      }
    },
    [environmentId],
  )

  const clearTestResult = useCallback(() => setTestResult(null), [])

  return {
    target,
    loading,
    loaded,
    error,
    notice,
    saving,
    deleting,
    testing,
    testResult,
    reload,
    save,
    remove,
    testConnection,
    clearTestResult,
  }
}
