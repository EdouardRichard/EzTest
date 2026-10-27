"use client"

import {
  ArrowRight,
  CheckCircle2,
  FlaskConical,
  Loader2,
  LockKeyhole,
  UserRound,
} from "lucide-react"
import { useRouter } from "next/navigation"
import { type FormEvent, useEffect, useState } from "react"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { useAuth } from "@/features/auth/AuthProvider"
import { ApiError, getApiBaseUrl } from "@/lib/api-client"

function toLoginMessage(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 429 || cause.code === "rate_limited") {
      return "登录尝试次数过多，请一分钟后再试。"
    }
    if (cause.status === 401 || cause.code === "unauthenticated") {
      return "用户名或密码错误。"
    }
    if (cause.status === 0) {
      return `无法连接 API 服务，请确认 ${getApiBaseUrl()} 已启动。`
    }
    return cause.message
  }
  return "登录失败，请稍后重试。"
}

export default function LoginPage() {
  const { user, loading, login } = useAuth()
  const router = useRouter()
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!loading && user) router.replace("/")
  }, [loading, router, user])

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submitting) return

    setError(null)
    setSubmitting(true)
    try {
      await login(username.trim(), password)
      router.replace("/")
    } catch (cause) {
      setError(toLoginMessage(cause))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className="grid min-h-screen bg-background lg:grid-cols-[1.05fr_1fr]">
      <section className="relative hidden flex-col justify-between overflow-hidden bg-sidebar p-10 lg:flex">
        <div className="login-aurora absolute inset-0" aria-hidden="true" />
        <div className="bg-grid absolute inset-0 opacity-40" aria-hidden="true" />
        <div className="relative flex items-center gap-3">
          <span className="grid h-11 w-11 place-items-center rounded-xl bg-gradient-to-br from-blue-500 to-sky-400 text-white shadow-lg shadow-blue-950/40">
            <FlaskConical className="h-5 w-5" aria-hidden="true" />
          </span>
          <div>
            <p className="text-base font-semibold tracking-tight text-sidebar-foreground">EzTest</p>
            <p className="text-[11px] text-sidebar-muted">AI 端到端测试平台</p>
          </div>
        </div>

        <div className="relative max-w-md">
          <p className="text-[11px] font-semibold uppercase tracking-[0.24em] text-sky-300/80">
            P0 · Foundation
          </p>
          <h1 className="mt-4 text-4xl font-semibold leading-[1.15] tracking-tight text-white">
            把 AI 测试的执行过程，
            <span className="text-sky-300">变成可观察的事实。</span>
          </h1>
          <p className="mt-4 text-sm leading-relaxed text-slate-300">
            从需求澄清到用例脑图、自主执行与证据链，EzTest 让每一次测试结论都有迹可循。
          </p>
          <ul className="mt-8 space-y-3 text-sm text-slate-300">
            <li className="flex items-center gap-3">
              <CheckCircle2 className="h-4 w-4 text-emerald-400" aria-hidden="true" />
              统一的错误信封与请求追踪
            </li>
            <li className="flex items-center gap-3">
              <CheckCircle2 className="h-4 w-4 text-emerald-400" aria-hidden="true" />
              基于 Bearer Token 的会话保护
            </li>
            <li className="flex items-center gap-3">
              <CheckCircle2 className="h-4 w-4 text-emerald-400" aria-hidden="true" />
              深浅色主题与响应式应用壳
            </li>
          </ul>
        </div>

        <p className="relative text-[11px] text-slate-500">EzTest · P0 平台地基</p>
      </section>

      <section className="flex flex-col items-center justify-center px-5 py-12 sm:px-8">
        <div className="w-full max-w-sm">
          <div className="mb-8 flex items-center gap-3 lg:hidden">
            <span className="grid h-10 w-10 place-items-center rounded-xl bg-gradient-to-br from-blue-500 to-sky-400 text-white">
              <FlaskConical className="h-5 w-5" aria-hidden="true" />
            </span>
            <div>
              <p className="text-base font-semibold tracking-tight">EzTest</p>
              <p className="text-[11px] text-muted-foreground">AI 端到端测试平台</p>
            </div>
          </div>

          <div className="rounded-2xl border border-border bg-card p-6 shadow-sm sm:p-8">
            <StatusBadge variant="info" dot={false}>
              安全登录
            </StatusBadge>
            <h2 className="mt-3 text-2xl font-semibold tracking-tight">登录 EzTest</h2>
            <p className="mt-1.5 text-sm text-muted-foreground">使用管理员或测试账号进入平台。</p>

            {error ? (
              <div
                role="alert"
                className="mt-5 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2.5 text-sm text-destructive"
              >
                {error}
              </div>
            ) : null}

            <form onSubmit={handleSubmit} className="mt-5 space-y-4">
              <label className="block space-y-1.5">
                <span className="text-sm font-medium">用户名</span>
                <span className="relative block">
                  <UserRound
                    className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground"
                    aria-hidden="true"
                  />
                  <input
                    name="username"
                    type="text"
                    autoComplete="username"
                    required
                    disabled={submitting}
                    value={username}
                    onChange={(event) => setUsername(event.target.value)}
                    placeholder="admin"
                    className="h-10 w-full rounded-lg border border-border bg-input pl-9 pr-3 text-sm outline-none transition focus:border-primary focus:ring-2 focus:ring-primary/20 disabled:opacity-60"
                  />
                </span>
              </label>

              <label className="block space-y-1.5">
                <span className="text-sm font-medium">密码</span>
                <span className="relative block">
                  <LockKeyhole
                    className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground"
                    aria-hidden="true"
                  />
                  <input
                    name="password"
                    type="password"
                    autoComplete="current-password"
                    required
                    disabled={submitting}
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    placeholder="输入密码"
                    className="h-10 w-full rounded-lg border border-border bg-input pl-9 pr-3 text-sm outline-none transition focus:border-primary focus:ring-2 focus:ring-primary/20 disabled:opacity-60"
                  />
                </span>
              </label>

              <button
                type="submit"
                disabled={submitting || username.trim().length === 0 || password.length === 0}
                className="flex h-10 w-full items-center justify-center gap-2 rounded-lg bg-primary px-4 text-sm font-medium text-primary-foreground transition hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-60"
              >
                {submitting ? (
                  <>
                    <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                    正在登录…
                  </>
                ) : (
                  <>
                    登录
                    <ArrowRight className="h-4 w-4" aria-hidden="true" />
                  </>
                )}
              </button>
            </form>
          </div>

          <p className="mt-6 text-center text-xs leading-relaxed text-muted-foreground">
            凭据仅用于本次登录；Token 保存在当前标签页的 sessionStorage 中。
          </p>
        </div>
      </section>
    </main>
  )
}
