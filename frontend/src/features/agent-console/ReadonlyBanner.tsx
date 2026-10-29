import { ShieldCheck } from "lucide-react"

/**
 * Read-only notice shown on the Agent centre for non-administrator accounts.
 * It states what remains available instead of only what is blocked.
 */
export function ReadonlyBanner() {
  return (
    <div
      role="status"
      data-testid="readonly-banner"
      className="flex items-start gap-3 rounded-2xl border border-sky-500/25 bg-sky-500/10 px-4 py-3 text-sm"
    >
      <ShieldCheck
        className="mt-0.5 h-4 w-4 shrink-0 text-sky-700 dark:text-sky-300"
        aria-hidden="true"
      />
      <div>
        <p className="font-medium">只读模式</p>
        <p className="mt-0.5 text-muted-foreground">
          当前账户可以查看角色目录、运行观察与质量指标；Agent 配置与运行操作仅管理员可用。
        </p>
      </div>
    </div>
  )
}
