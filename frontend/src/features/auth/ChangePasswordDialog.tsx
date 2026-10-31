"use client"

import type { FormEvent } from "react"
import { useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { ApiError } from "@/lib/api-client"

import { useAuth } from "./AuthProvider"

export type ChangePasswordDialogProps = {
  open: boolean
  onClose: () => void
}

/** Self-service password change backed by `POST /api/auth/password`. */
export function ChangePasswordDialog({ open, onClose }: ChangePasswordDialogProps) {
  const { changePassword } = useAuth()
  const [oldPassword, setOldPassword] = useState("")
  const [newPassword, setNewPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  function reset(): void {
    setOldPassword("")
    setNewPassword("")
    setConfirmPassword("")
    setError(null)
    setSuccess(null)
  }

  function handleClose(): void {
    reset()
    onClose()
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setError(null)
    setSuccess(null)

    if (oldPassword.length === 0 || newPassword.length === 0 || confirmPassword.length === 0) {
      setError("请填写完整的密码信息")
      return
    }
    if (newPassword.length < 8) {
      setError("新密码至少 8 个字符")
      return
    }
    if (newPassword.length > 128) {
      setError("新密码不能超过 128 个字符")
      return
    }
    if (newPassword !== confirmPassword) {
      setError("两次输入的新密码不一致")
      return
    }
    if (newPassword === oldPassword) {
      setError("新密码不能与旧密码相同")
      return
    }

    setPending(true)
    try {
      await changePassword(oldPassword, newPassword)
      setSuccess("密码已修改")
      setOldPassword("")
      setNewPassword("")
      setConfirmPassword("")
    } catch (caught) {
      setError(caught instanceof ApiError ? caught.message : "修改密码失败，请稍后重试")
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog
      open={open}
      onClose={handleClose}
      title="修改密码"
      description="密码长度 8–128 个字符；修改后请使用新密码重新登录其他设备。"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4" noValidate>
        <Field id="old-password" label="旧密码" required>
          <Input
            id="old-password"
            type="password"
            autoComplete="current-password"
            value={oldPassword}
            onChange={(event) => setOldPassword(event.target.value)}
          />
        </Field>
        <Field id="new-password" label="新密码" required>
          <Input
            id="new-password"
            type="password"
            autoComplete="new-password"
            value={newPassword}
            onChange={(event) => setNewPassword(event.target.value)}
          />
        </Field>
        <Field id="confirm-password" label="确认新密码" required>
          <Input
            id="confirm-password"
            type="password"
            autoComplete="new-password"
            value={confirmPassword}
            onChange={(event) => setConfirmPassword(event.target.value)}
          />
        </Field>

        {error ? <Alert tone="danger">{error}</Alert> : null}
        {success ? <Alert tone="success">{success}</Alert> : null}

        <div className="flex items-center justify-end gap-2 pt-1">
          <Button variant="secondary" onClick={handleClose}>
            取消
          </Button>
          <Button type="submit" disabled={pending}>
            {pending ? "保存中…" : "保存新密码"}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
