"use client"

import { useCallback, useEffect, useMemo, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { Textarea } from "@/components/ui/Textarea"
import { ApiError } from "@/lib/api-client"
import { formatDateTime } from "@/lib/format"

import { fetchSettings, groupSettings, type Setting, updateSetting } from "./api"
import { GCPanel } from "./GCPanel"
import { InstancePanel } from "./InstancePanel"

type ParseResult = { ok: true; value: unknown } | { ok: false; error: string }

function parseDraft(setting: Setting, draft: string): ParseResult {
  if (setting.type === "number") {
    const trimmed = draft.trim()
    if (trimmed.length === 0) return { ok: false, error: "请输入数字" }
    const parsed = Number(trimmed)
    if (!Number.isFinite(parsed)) return { ok: false, error: "请输入有效数字" }
    return { ok: true, value: parsed }
  }
  if (setting.type === "boolean") {
    return { ok: true, value: draft === "true" }
  }
  if (setting.type === "json") {
    try {
      return { ok: true, value: JSON.parse(draft) as unknown }
    } catch {
      return { ok: false, error: "JSON 格式不正确" }
    }
  }
  return { ok: true, value: draft }
}

function draftFor(setting: Setting): string {
  if (setting.is_secret) return ""
  if (setting.type === "json") return JSON.stringify(setting.value ?? {}, null, 2)
  if (setting.value === null || setting.value === undefined) return ""
  return String(setting.value)
}

function displayValue(setting: Setting): string {
  if (setting.is_secret) return "已设置（已隐藏）"
  if (setting.value === null || setting.value === undefined) return "—"
  if (setting.type === "boolean") return setting.value === true ? "启用" : "禁用"
  if (setting.type === "json") return JSON.stringify(setting.value)
  return String(setting.value)
}

type Notice = { key: string; text: string }

/** System settings grouped by category with one-key-at-a-time inline editing. */
export function SystemSettings() {
  const [settings, setSettings] = useState<Setting[] | null>(null)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [loading, setLoading] = useState(true)
  const [reloadKey, setReloadKey] = useState(0)

  const [editingKey, setEditingKey] = useState<string | null>(null)
  const [draft, setDraft] = useState("")
  const [formError, setFormError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [notice, setNotice] = useState<Notice | null>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadKey retriggers reload
  useEffect(() => {
    let active = true
    setLoading(true)
    setLoadError(null)
    fetchSettings()
      .then((rows) => {
        if (active) setSettings(rows)
      })
      .catch((caught: unknown) => {
        if (active) setLoadError(caught)
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => {
      active = false
    }
  }, [reloadKey])

  const groups = useMemo(() => groupSettings(settings ?? []), [settings])

  const startEdit = useCallback((setting: Setting) => {
    setEditingKey(setting.key)
    setDraft(draftFor(setting))
    setFormError(null)
    setNotice(null)
  }, [])

  function cancelEdit(): void {
    setEditingKey(null)
    setDraft("")
    setFormError(null)
  }

  async function save(setting: Setting): Promise<void> {
    setFormError(null)
    setNotice(null)

    if (setting.is_secret && draft.length === 0) {
      setEditingKey(null)
      setNotice({ key: setting.key, text: "未修改（密钥保持原值）" })
      return
    }

    const parsed = parseDraft(setting, draft)
    if (!parsed.ok) {
      setFormError(parsed.error)
      return
    }

    setSaving(true)
    try {
      const updated = await updateSetting(setting.key, parsed.value)
      setSettings((current) =>
        current ? current.map((item) => (item.key === updated.key ? updated : item)) : current,
      )
      setEditingKey(null)
      setDraft("")
      setNotice({ key: setting.key, text: "已保存" })
    } catch (caught) {
      setFormError(caught instanceof ApiError ? caught.message : "保存失败，请稍后重试")
    } finally {
      setSaving(false)
    }
  }

  if (loading && settings === null) return <LoadingCard label="正在加载系统设置…" />
  if (loadError) {
    return (
      <ErrorState
        error={loadError}
        title="系统设置加载失败"
        onRetry={() => setReloadKey((n) => n + 1)}
      />
    )
  }
  if (settings === null || settings.length === 0) {
    return <EmptyState title="暂无系统设置" description="当前设置目录为空，请检查 API 种子数据。" />
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-foreground">系统设置</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            按类别分组，单键保存后立即热加载；标有“需重启生效”的配置仍需重启 API 进程。
          </p>
        </div>
        <Button variant="secondary" size="sm" onClick={() => setReloadKey((n) => n + 1)}>
          刷新
        </Button>
      </div>

      {groups.map((group) => (
        <section
          key={group.category}
          className="surface-card overflow-hidden"
          aria-labelledby={`setting-category-${group.category}`}
        >
          <header className="flex items-center justify-between gap-2 border-b border-border px-4 py-3">
            <h3
              id={`setting-category-${group.category}`}
              className="text-xs font-semibold text-foreground"
            >
              {group.label}
              <span className="ml-2 font-mono text-[10px] font-normal text-muted-foreground">
                {group.category}
              </span>
            </h3>
            <span className="text-[10px] text-muted-foreground">{group.settings.length} 项</span>
          </header>

          <ul className="divide-y divide-border">
            {group.settings.map((setting) => {
              const editing = editingKey === setting.key
              return (
                <li
                  key={setting.key}
                  data-testid={`setting-row-${setting.key}`}
                  className="px-4 py-3"
                >
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <code className="font-mono text-[11px] font-semibold text-foreground">
                          {setting.key}
                        </code>
                        <Badge tone="neutral">{setting.type}</Badge>
                        {setting.is_secret ? <Badge tone="info">密钥</Badge> : null}
                        {setting.restart_required ? <Badge tone="warning">需重启生效</Badge> : null}
                      </div>
                      <p className="mt-1 text-xs text-muted-foreground">{setting.description}</p>
                      <p className="mt-0.5 text-[10px] text-muted-foreground">
                        v{setting.version} · 更新于 {formatDateTime(setting.updated_at)}
                      </p>
                    </div>

                    <div className="flex min-w-40 flex-col items-end gap-1.5">
                      <span
                        data-testid={`setting-value-${setting.key}`}
                        className="max-w-64 break-all font-mono text-xs text-foreground"
                      >
                        {displayValue(setting)}
                      </span>
                      {!editing ? (
                        <Button variant="secondary" size="sm" onClick={() => startEdit(setting)}>
                          编辑
                        </Button>
                      ) : null}
                    </div>
                  </div>

                  {editing ? (
                    <div className="mt-3 rounded-lg border border-border bg-muted/40 p-3">
                      <label
                        htmlFor={`setting-input-${setting.key}`}
                        className="text-xs font-medium text-foreground"
                      >
                        {setting.key} 新值
                      </label>
                      <div className="mt-1.5">
                        {setting.type === "boolean" ? (
                          <Select
                            id={`setting-input-${setting.key}`}
                            aria-label={`${setting.key} 新值`}
                            value={draft}
                            onChange={(event) => setDraft(event.target.value)}
                          >
                            <option value="true">启用</option>
                            <option value="false">禁用</option>
                          </Select>
                        ) : setting.type === "json" ? (
                          <Textarea
                            id={`setting-input-${setting.key}`}
                            aria-label={`${setting.key} 新值`}
                            value={draft}
                            onChange={(event) => setDraft(event.target.value)}
                            rows={4}
                          />
                        ) : (
                          <Input
                            id={`setting-input-${setting.key}`}
                            aria-label={`${setting.key} 新值`}
                            type={
                              setting.is_secret
                                ? "password"
                                : setting.type === "number"
                                  ? "number"
                                  : "text"
                            }
                            step={setting.type === "number" ? "any" : undefined}
                            autoComplete={setting.is_secret ? "new-password" : undefined}
                            placeholder={setting.is_secret ? "留空表示不修改" : undefined}
                            value={draft}
                            onChange={(event) => setDraft(event.target.value)}
                          />
                        )}
                      </div>

                      {formError ? (
                        <p role="alert" className="mt-2 text-xs text-destructive">
                          {formError}
                        </p>
                      ) : null}

                      <div className="mt-3 flex items-center gap-2">
                        <Button size="sm" onClick={() => void save(setting)} disabled={saving}>
                          {saving ? "保存中…" : "保存"}
                        </Button>
                        <Button variant="ghost" size="sm" onClick={cancelEdit} disabled={saving}>
                          取消
                        </Button>
                      </div>
                    </div>
                  ) : notice && notice.key === setting.key ? (
                    <p role="status" className="mt-2 text-xs text-success">
                      {notice.text}
                    </p>
                  ) : null}
                </li>
              )
            })}
          </ul>
        </section>
      ))}

      <Alert tone="info" title="生效方式">
        `restart_required` 只做提示，不阻断保存；其余配置在保存成功后立即热加载。
      </Alert>

      <GCPanel />
      <InstancePanel />
    </div>
  )
}
