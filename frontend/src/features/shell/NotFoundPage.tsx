import { Compass } from "lucide-react"
import Link from "next/link"

/**
 * Product 404 body. It is rendered inside the application shell by
 * `app/not-found.tsx` but deliberately does not require an authenticated
 * session: an unknown URL must still explain itself instead of redirecting.
 */
export function NotFoundPage() {
  return (
    <div
      data-testid="not-found-page"
      className="flex min-h-[60vh] flex-col items-center justify-center px-6 py-16 text-center"
    >
      <span className="flex size-14 items-center justify-center rounded-2xl border border-border bg-card text-muted-foreground">
        <Compass className="size-7" aria-hidden="true" />
      </span>
      <p className="mt-4 text-xs font-semibold uppercase tracking-[0.24em] text-muted-foreground">
        404
      </p>
      <h1 className="mt-2 text-xl font-semibold text-foreground">页面不存在</h1>
      <p className="mt-2 max-w-md text-xs leading-relaxed text-muted-foreground">
        你访问的页面不存在或已被移动。请检查地址，或返回版本列表继续工作。
      </p>
      <Link
        href="/versions"
        className="mt-5 inline-flex items-center rounded-md border border-border bg-card px-4 py-2 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        返回版本列表
      </Link>
    </div>
  )
}
