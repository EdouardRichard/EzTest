"use client"

import { type FormEvent, useCallback, useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { LoadingCard } from "@/components/ui/Spinner"
import { createVersion, getResourceCatalog } from "./api"
import { BindingWizard } from "./BindingWizard"
import type { Catalog, Version, VersionDraft, VersionDraftErrors } from "./types"
import { buildCreateVersionPayload, createVersionDraft, validateVersionDraft } from "./wizard"

const CLEAN_ERRORS: VersionDraftErrors = { byBinding: {}, valid: false }

export type CreateVersionDialogProps = {
  open: boolean
  onClose: () => void
  onCreated: (version: Version) => void
}

/**
 * Creates a version: a title plus one or more SUT bindings. The catalog is
 * loaded on open, the draft is validated against it, and the API payload is
 * built by `buildCreateVersionPayload`.
 */
export function CreateVersionDialog({ open, onClose, onCreated }: CreateVersionDialogProps) {
  const [catalog, setCatalog] = useState<Catalog | null>(null)
  const [catalogLoading, setCatalogLoading] = useState(false)
  const [catalogError, setCatalogError] = useState<unknown>(null)
  const [draft, setDraft] = useState<VersionDraft>(createVersionDraft)
  const [errors, setErrors] = useState<VersionDraftErrors>(CLEAN_ERRORS)
  const [submitError, setSubmitError] = useState<unknown>(null)
  const [submitting, setSubmitting] = useState(false)

  const loadCatalog = useCallback(async () => {
    setCatalogLoading(true)
    setCatalogError(null)
    try {
      setCatalog(await getResourceCatalog())
    } catch (error) {
      setCatalog(null)
      setCatalogError(error)
    } finally {
      setCatalogLoading(false)
    }
  }, [])

  useEffect(() => {
    if (!open) return
    setDraft(createVersionDraft())
    setErrors(CLEAN_ERRORS)
    setSubmitError(null)
    setCatalog(null)
    void loadCatalog()
  }, [open, loadCatalog])

  function updateTitle(title: string): void {
    setDraft((current) => ({ ...current, title }))
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    if (catalog === null) return

    const nextErrors = validateVersionDraft(draft, catalog)
    setErrors(nextErrors)
    if (!nextErrors.valid) return

    setSubmitting(true)
    setSubmitError(null)
    try {
      onCreated(await createVersion(buildCreateVersionPayload(draft)))
    } catch (error) {
      setSubmitError(error)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="新建版本"
      description="填写标题并配置多 SUT 绑定；创建后进入版本总览。"
      className="max-w-3xl"
    >
      <form
        onSubmit={(event) => void handleSubmit(event)}
        noValidate
        className="flex max-h-[70vh] flex-col gap-4"
      >
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto pr-1">
          <Field id="create-version-title" label="版本标题" required error={errors.title}>
            <Input
              id="create-version-title"
              value={draft.title}
              placeholder="例如：订单中心 · 2026 Q4 迭代"
              autoComplete="off"
              aria-invalid={errors.title ? true : undefined}
              aria-describedby={errors.title ? "create-version-title-error" : undefined}
              onChange={(event) => updateTitle(event.target.value)}
            />
          </Field>

          {catalogLoading ? <LoadingCard label="正在加载资源目录…" /> : null}

          {catalogError ? (
            <ErrorState
              title="资源目录加载失败"
              error={catalogError}
              onRetry={() => void loadCatalog()}
            />
          ) : null}

          {!catalogLoading && catalogError === null && catalog !== null ? (
            catalog.suts.length === 0 ? (
              <Alert tone="warning" title="暂无可绑定的 SUT">
                请先在资源页创建 SUT、环境与页面/API，再回来创建版本。
              </Alert>
            ) : (
              <BindingWizard
                catalog={catalog}
                value={draft.bindings}
                errors={errors.byBinding}
                disabled={submitting}
                onChange={(bindings) => setDraft((current) => ({ ...current, bindings }))}
              />
            )
          ) : null}

          {errors.bindings ? (
            <p role="alert" className="text-xs text-destructive">
              {errors.bindings}
            </p>
          ) : null}

          {submitError ? <InlineError error={submitError} /> : null}
        </div>

        <div className="flex items-center justify-end gap-2 border-t border-border pt-3">
          <Button variant="secondary" onClick={onClose} disabled={submitting}>
            取消
          </Button>
          <Button type="submit" disabled={submitting || catalogLoading || catalog === null}>
            {submitting ? "创建中…" : "创建版本"}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
