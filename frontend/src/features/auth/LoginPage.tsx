"use client"

import { KeyRound, ShieldCheck, Sparkles, Wifi } from "lucide-react"
import { useRouter } from "next/navigation"
import type { FormEvent } from "react"
import { useEffect, useRef, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Button } from "@/components/ui/Button"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Spinner } from "@/components/ui/Spinner"
import { ApiError } from "@/lib/api-client"

import { useAuth } from "./AuthProvider"

const HIGHLIGHTS = [
  { icon: Sparkles, text: "AI 驱动的需求澄清、用例设计与执行闭环" },
  { icon: Wifi, text: "SSE 实时事件投影，DB 为唯一真值" },
  { icon: ShieldCheck, text: "受控网络内的单团队测试工作区" },
]

/**
 * Login screen for the fixed default admin seed. There is intentionally no
 * bootstrap/initialise-admin form: the API seeds admin/admin123 on first start.
 */
export function LoginPage() {
  const { login, loading, token } = useAuth()
  const router = useRouter()
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const navigated = useRef(false)

  useEffect(() => {
    if (loading || token === null || navigated.current) return
    navigated.current = true
    router.replace("/versions")
  }, [loading, token, router])

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setError(null)
    if (username.trim().length === 0 || password.length === 0) {
      setError("请输入用户名和密码")
      return
    }

    setPending(true)
    navigated.current = true
    try {
      await login(username.trim(), password)
      router.push("/versions")
    } catch (caught) {
      navigated.current = false
      setError(caught instanceof ApiError ? caught.message : "登录失败，请稍后重试")
    } finally {
      setPending(false)
    }
  }

  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Spinner label="正在验证会话…" />
      </div>
    )
  }

  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden bg-background px-4 py-10">
      <div className="bg-grid pointer-events-none absolute inset-0 opacity-60" aria-hidden="true" />
      <div className="relative grid w-full max-w-4xl overflow-hidden rounded-2xl border border-border bg-card shadow-2xl lg:grid-cols-[1.05fr_1fr]">
        <section className="login-aurora hidden flex-col justify-between gap-8 px-8 py-10 text-primary-foreground lg:flex">
          <div className="space-y-4">
            <div className="flex items-center gap-3">
              <span className="flex size-10 items-center justify-center rounded-xl bg-white/15 text-sm font-bold tracking-wide">
                EZ
              </span>
              <div>
                <p className="text-base font-semibold">EzTest</p>
                <p className="text-xs text-white/70">AI 驱动端到端测试</p>
              </div>
            </div>
            <h2 className="max-w-xs text-2xl font-semibold leading-snug">
              从需求到执行证据，一个受控平台完成闭环
            </h2>
          </div>
          <ul className="space-y-3 text-xs text-white/80">
            {HIGHLIGHTS.map((item) => (
              <li key={item.text} className="flex items-start gap-2.5">
                <item.icon className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
                <span>{item.text}</span>
              </li>
            ))}
          </ul>
        </section>

        <section className="flex flex-col justify-center gap-5 px-6 py-9 sm:px-9">
          <header className="space-y-1">
            <h1 className="text-xl font-semibold text-foreground">登录 EzTest</h1>
            <p className="text-xs text-muted-foreground">使用平台账号进入工作区</p>
          </header>

          <Alert tone="info" title="默认管理员账号">
            首次启动默认 admin / admin123，登录后请在设置中修改密码
          </Alert>

          <form onSubmit={handleSubmit} className="flex flex-col gap-4" noValidate>
            <Field id="username" label="用户名" required>
              <Input
                id="username"
                name="username"
                autoComplete="username"
                placeholder="admin"
                value={username}
                onChange={(event) => setUsername(event.target.value)}
              />
            </Field>
            <Field id="password" label="密码" required>
              <Input
                id="password"
                name="password"
                type="password"
                autoComplete="current-password"
                placeholder="••••••••"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
              />
            </Field>

            {error ? <Alert tone="danger">{error}</Alert> : null}

            <Button type="submit" disabled={pending} className="w-full">
              <KeyRound className="size-4" aria-hidden="true" />
              {pending ? "登录中…" : "登录"}
            </Button>
          </form>

          <p className="text-[11px] leading-relaxed text-muted-foreground">
            开发测试环境 · 受控网络。固定默认凭据仅供首启使用，请登录后立即改密。
          </p>
        </section>
      </div>
    </div>
  )
}
