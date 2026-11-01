import { Alert } from "@/components/ui/Alert"
import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { Dialog } from "@/components/ui/Dialog"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"

import type { CodeChangedFileStatus, CodeChangedFileView, CodeFileDiffView } from "../types"
import { CODE_CHANGED_STATUS_LABELS } from "../types"
import { useFocusTrap } from "./useFocusTrap"

export type DiffDrawerProps = {
  open: boolean
  file: CodeChangedFileView | null
  diff: CodeFileDiffView | null
  loading: boolean
  error: unknown
  onClose: () => void
  onRetry: () => void
}

const STATUS_TONE: Record<CodeChangedFileStatus, BadgeTone> = {
  added: "success",
  modified: "info",
  deleted: "danger",
  renamed: "warning",
}

function patchLineClass(line: string): string {
  if (line.startsWith("+++") || line.startsWith("---")) return "text-muted-foreground"
  if (line.startsWith("@@")) return "text-info"
  if (line.startsWith("+")) return "bg-success/10 text-success"
  if (line.startsWith("-")) return "bg-destructive/10 text-destructive"
  return "text-foreground"
}

/** On-demand patch drawer (spec §9.2): ESC/focus trap, binary/large/deleted hints. */
export function DiffDrawer({
  open,
  file,
  diff,
  loading,
  error,
  onClose,
  onRetry,
}: DiffDrawerProps) {
  useFocusTrap(open)

  const title = diff?.path ?? file?.path ?? "文件 diff"
  const description =
    diff !== null
      ? `${CODE_CHANGED_STATUS_LABELS[diff.status]} · +${diff.additions} / -${diff.deletions}`
      : "按需加载单文件 patch"

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      description={description}
      className="max-w-4xl"
      footer={
        <Button variant="secondary" size="sm" onClick={onClose}>
          关闭
        </Button>
      }
    >
      {loading ? (
        <LoadingCard label="正在加载文件 diff…" />
      ) : error !== null ? (
        <ErrorState error={error} title="文件 diff 加载失败" onRetry={onRetry} />
      ) : diff === null ? (
        <EmptyState title="无可用 patch" description="该文件没有可显示的 diff 内容。" />
      ) : (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={STATUS_TONE[diff.status]}>{CODE_CHANGED_STATUS_LABELS[diff.status]}</Badge>
            <Badge tone="neutral">+{diff.additions}</Badge>
            <Badge tone="neutral">-{diff.deletions}</Badge>
            {diff.binary ? <Badge tone="warning">二进制</Badge> : null}
            {diff.large ? <Badge tone="warning">超限</Badge> : null}
            {diff.deleted ? <Badge tone="danger">已删除</Badge> : null}
          </div>

          {diff.binary ? (
            <Alert tone="info">二进制文件不显示 patch</Alert>
          ) : diff.deleted ? (
            <Alert tone="warning">文件已删除，无可用 patch</Alert>
          ) : diff.large ? (
            <Alert tone="warning">patch 超过 256 KiB，已截断</Alert>
          ) : diff.patch === null || diff.patch === undefined || diff.patch.length === 0 ? (
            <EmptyState title="无可用 patch" description="该文件没有可显示的 diff 内容。" />
          ) : (
            <section
              aria-label="文件 patch"
              className="rounded-lg border border-border bg-muted/30"
            >
              <pre className="max-h-[60vh] overflow-auto p-3 font-mono text-[11px] leading-relaxed">
                {diff.patch.split("\n").map((line, index) => (
                  <span
                    // biome-ignore lint/suspicious/noArrayIndexKey: patch lines are positional
                    key={index}
                    className={cn("block whitespace-pre-wrap", patchLineClass(line))}
                  >
                    {line.length > 0 ? line : " "}
                  </span>
                ))}
              </pre>
            </section>
          )}
        </div>
      )}
    </Dialog>
  )
}
