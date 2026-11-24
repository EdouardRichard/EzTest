"use client"

import { useCallback, useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table } from "@/components/ui/Table"
import { Textarea } from "@/components/ui/Textarea"
import { useAuth } from "@/features/auth/AuthProvider"
import { formatDateTime } from "@/lib/format"

import { createSkill, deleteSkill, listSkills, listSkillVersions, updateSkill } from "./api"
import {
  SKILL_NAME_PATTERN,
  SKILL_SOURCE_LABEL,
  type SkillVersionView,
  type SkillView,
} from "./types"

const VERSION_LIMIT = 20

/**
 * Admin skill library console. The runtime DB is authoritative: edits here
 * take effect on the next read without a rebuild or restart; skills/ files
 * only seed the library once and never overwrite these edits.
 */
export function SkillsPage() {
  const { user } = useAuth()
  const isAdmin = user === null || user.role === "admin"

  const [skills, setSkills] = useState<SkillView[] | null>(null)
  const [listError, setListError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<SkillView | null>(null)
  const [editing, setEditing] = useState<SkillView | null>(null)

  const reload = useCallback(() => setReloadToken((token) => token + 1), [])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken re-runs the explicit refetch
  useEffect(() => {
    if (!isAdmin) return undefined
    let cancelled = false
    setListError(null)
    void listSkills()
      .then((items) => {
        if (!cancelled) setSkills(items)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setSkills(null)
          setListError(cause)
        }
      })
    return () => {
      cancelled = true
    }
  }, [isAdmin, reloadToken])

  const header = (
    <header className="flex flex-wrap items-start justify-between gap-3">
      <div>
        <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
          Agent 能力
        </p>
        <h1 className="mt-1 text-xl font-semibold text-foreground">技能库</h1>
        <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
          SKILL.md 技能包的运行态库。此处的修改立即生效（无需构建或重启），每次修改追加一个
          不可变版本并写入审计；仓库 skills/ 文件仅在首次启动播种，不会覆盖这里的编辑。
        </p>
      </div>
      {isAdmin && (
        <Button onClick={() => setCreating(true)} data-testid="skill-create-open">
          新建技能
        </Button>
      )}
    </header>
  )

  if (!isAdmin) {
    return (
      <div className="space-y-5" data-testid="skills-page">
        {header}
        <Alert tone="warning" title="技能库仅管理员可用">
          当前账号没有管理员权限。如需查看或编辑技能，请联系管理员。
        </Alert>
      </div>
    )
  }

  return (
    <div className="space-y-5" data-testid="skills-page">
      {header}
      {listError !== null ? (
        <ErrorState error={listError} onRetry={reload} />
      ) : skills === null ? (
        <LoadingCard label="加载技能库…" />
      ) : skills.length === 0 ? (
        <EmptyState
          title="技能库为空"
          description="新建一个技能，或在仓库 skills/ 目录放置 SKILL.md 包后重启播种。"
          action={<Button onClick={() => setCreating(true)}>新建技能</Button>}
        />
      ) : (
        <Table<SkillView>
          ariaLabel="技能列表"
          rows={skills}
          rowKey={(skill) => skill.name}
          columns={[
            {
              key: "name",
              header: "技能",
              render: (skill) => (
                <div data-testid={`skill-row-${skill.name}`}>
                  <p className="font-medium text-foreground">{skill.name}</p>
                  <p className="mt-0.5 max-w-md truncate text-xs text-muted-foreground">
                    {skill.description}
                  </p>
                </div>
              ),
            },
            {
              key: "version",
              header: "版本",
              render: (skill) => <Badge tone="info">v{skill.version}</Badge>,
            },
            {
              key: "enabled",
              header: "状态",
              render: (skill) =>
                skill.enabled ? (
                  <Badge tone="success">启用</Badge>
                ) : (
                  <Badge tone="neutral">停用</Badge>
                ),
            },
            {
              key: "updated",
              header: "更新时间",
              render: (skill) => (
                <span className="text-xs text-muted-foreground">
                  {formatDateTime(skill.updated_at)}
                </span>
              ),
            },
            {
              key: "actions",
              header: "操作",
              render: (skill) => (
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => setEditing(skill)}
                    data-testid={`skill-edit-${skill.name}`}
                  >
                    编辑
                  </Button>
                  <Button
                    size="sm"
                    variant="danger"
                    onClick={() => setDeleting(skill)}
                    data-testid={`skill-delete-${skill.name}`}
                  >
                    删除
                  </Button>
                </div>
              ),
            },
          ]}
        />
      )}

      {editing !== null && (
        <SkillEditor
          skill={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            reload()
          }}
        />
      )}

      <SkillCreateDialog
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={() => {
          setCreating(false)
          reload()
        }}
      />

      <SkillDeleteDialog
        skill={deleting}
        onClose={() => setDeleting(null)}
        onDeleted={() => {
          setDeleting(null)
          if (editing?.name === deleting?.name) setEditing(null)
          reload()
        }}
      />
    </div>
  )
}

/* ------------------------------- editor ---------------------------------- */

function SkillEditor({
  skill,
  onClose,
  onSaved,
}: {
  skill: SkillView
  onClose: () => void
  onSaved: () => void
}) {
  const [description, setDescription] = useState(skill.description)
  const [body, setBody] = useState(skill.body)
  const [enabled, setEnabled] = useState(skill.enabled)
  const [note, setNote] = useState("")
  const [current, setCurrent] = useState(skill)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<unknown>(null)
  const [savedVersion, setSavedVersion] = useState<number | null>(null)

  const [versions, setVersions] = useState<SkillVersionView[] | null>(null)
  const [versionsError, setVersionsError] = useState<unknown>(null)
  const [versionToken, setVersionToken] = useState(0)
  const [viewing, setViewing] = useState<SkillVersionView | null>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: versionToken re-runs the explicit refetch
  useEffect(() => {
    let cancelled = false
    setVersionsError(null)
    void listSkillVersions(skill.name, { limit: VERSION_LIMIT })
      .then((page) => {
        if (!cancelled) setVersions(page.items)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setVersions(null)
          setVersionsError(cause)
        }
      })
    return () => {
      cancelled = true
    }
  }, [skill.name, versionToken])

  async function save() {
    setSaving(true)
    setSaveError(null)
    setSavedVersion(null)
    try {
      const stored = await updateSkill(skill.name, {
        description,
        body,
        enabled,
        note,
        expected_version: current.version,
      })
      setCurrent(stored)
      setDescription(stored.description)
      setBody(stored.body)
      setEnabled(stored.enabled)
      setNote("")
      setSavedVersion(stored.version)
      setVersionToken((token) => token + 1)
      onSaved()
    } catch (cause) {
      setSaveError(cause)
    } finally {
      setSaving(false)
    }
  }

  return (
    <section
      className="space-y-4 rounded-lg border border-border bg-card p-4"
      data-testid="skill-editor"
    >
      <header className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-foreground">
            编辑技能：{current.name} <Badge tone="info">v{current.version}</Badge>
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            保存后立即生效（无需重启）；内容相同的保存不会追加版本。
          </p>
        </div>
        <Button size="sm" variant="ghost" onClick={onClose}>
          关闭
        </Button>
      </header>

      <Field id="skill-edit-description" label="描述（元数据层）">
        <Input
          id="skill-edit-description"
          value={description}
          onChange={(event) => setDescription(event.target.value)}
          maxLength={2000}
        />
      </Field>
      <Field id="skill-edit-body" label="正文（SKILL.md 内容）" required>
        <Textarea
          id="skill-edit-body"
          value={body}
          onChange={(event) => setBody(event.target.value)}
          rows={12}
          className="font-mono text-xs"
        />
      </Field>
      <div className="flex flex-wrap items-end gap-4">
        <label className="flex items-center gap-2 text-xs text-foreground">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(event) => setEnabled(event.target.checked)}
            data-testid="skill-edit-enabled"
          />
          启用
        </label>
        <Field id="skill-edit-note" label="版本备注" className="min-w-64 flex-1">
          <Input
            id="skill-edit-note"
            value={note}
            onChange={(event) => setNote(event.target.value)}
            placeholder="本次修改说明（写入版本历史）"
          />
        </Field>
        <Button onClick={() => void save()} disabled={saving} data-testid="skill-save">
          {saving ? "保存中…" : "保存"}
        </Button>
      </div>
      {saveError !== null && <InlineError error={saveError} />}
      {savedVersion !== null && (
        <Alert tone="success" title={`已保存为 v${savedVersion}`}>
          新内容已生效，下一次读取即使用此版本。
        </Alert>
      )}

      <div className="space-y-2" data-testid="skill-versions">
        <h3 className="text-xs font-semibold text-foreground">版本历史（append-only）</h3>
        {versionsError !== null ? (
          <InlineError error={versionsError} />
        ) : versions === null ? (
          <LoadingCard label="加载版本历史…" />
        ) : versions.length === 0 ? (
          <p className="text-xs text-muted-foreground">暂无版本记录。</p>
        ) : (
          <ul className="space-y-1.5">
            {versions.map((version) => (
              <li
                key={version.version}
                className="flex flex-wrap items-center gap-2 rounded-md border border-border px-3 py-2 text-xs"
                data-testid={`skill-version-${version.version}`}
              >
                <Badge tone="info">v{version.version}</Badge>
                <Badge tone={version.source === "seed" ? "neutral" : "success"}>
                  {SKILL_SOURCE_LABEL[version.source]}
                </Badge>
                <span className="text-muted-foreground">{formatDateTime(version.created_at)}</span>
                {version.note !== "" && <span className="text-foreground">{version.note}</span>}
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setViewing(version)}
                  data-testid={`skill-version-view-${version.version}`}
                >
                  查看正文
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <Dialog
        open={viewing !== null}
        onClose={() => setViewing(null)}
        title={viewing !== null ? `${skill.name} v${viewing.version} 正文` : ""}
      >
        <pre className="max-h-96 overflow-auto whitespace-pre-wrap px-5 py-4 font-mono text-xs">
          {viewing?.body}
        </pre>
      </Dialog>
    </section>
  )
}

/* ------------------------------- create ---------------------------------- */

function SkillCreateDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [body, setBody] = useState("")
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [nameError, setNameError] = useState<string | null>(null)

  async function submit() {
    if (!SKILL_NAME_PATTERN.test(name)) {
      setNameError("名称须匹配 ^[a-z][a-z0-9_-]{0,63}$（小写字母开头，可含数字/中划线/下划线）")
      return
    }
    setNameError(null)
    setSaving(true)
    setError(null)
    try {
      await createSkill({ name, description, body })
      setName("")
      setDescription("")
      setBody("")
      onCreated()
    } catch (cause) {
      setError(cause)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="新建技能"
      description="创建后即为 v1；正文只存运行态库，不改代码。"
      className="max-w-lg"
    >
      <div className="space-y-4 px-5 py-4" data-testid="skill-create-dialog">
        <Field id="skill-create-name" label="名称（包名）" required error={nameError ?? undefined}>
          <Input
            id="skill-create-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="例如 api-recon"
          />
        </Field>
        <Field id="skill-create-description" label="描述" required>
          <Input
            id="skill-create-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            maxLength={2000}
          />
        </Field>
        <Field id="skill-create-body" label="正文（SKILL.md 内容）" required>
          <Textarea
            id="skill-create-body"
            value={body}
            onChange={(event) => setBody(event.target.value)}
            rows={8}
            className="font-mono text-xs"
          />
        </Field>
        {error !== null && <InlineError error={error} />}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button onClick={() => void submit()} disabled={saving} data-testid="skill-create-submit">
            {saving ? "创建中…" : "创建"}
          </Button>
        </div>
      </div>
    </Dialog>
  )
}

/* ------------------------------- delete ---------------------------------- */

function SkillDeleteDialog({
  skill,
  onClose,
  onDeleted,
}: {
  skill: SkillView | null
  onClose: () => void
  onDeleted: () => void
}) {
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState<unknown>(null)

  async function confirm() {
    if (skill === null) return
    setDeleting(true)
    setError(null)
    try {
      await deleteSkill(skill.name)
      onDeleted()
    } catch (cause) {
      setError(cause)
    } finally {
      setDeleting(false)
    }
  }

  return (
    <Dialog
      open={skill !== null}
      onClose={onClose}
      title={skill !== null ? `删除技能 ${skill.name}？` : ""}
      description="删除后所有读取立即不可见；版本历史保留，重启播种不会复活该技能。"
    >
      <div className="space-y-4 px-5 py-4" data-testid="skill-delete-dialog">
        {error !== null && <InlineError error={error} />}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button
            variant="danger"
            onClick={() => void confirm()}
            disabled={deleting}
            data-testid="skill-delete-confirm"
          >
            {deleting ? "删除中…" : "确认删除"}
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
