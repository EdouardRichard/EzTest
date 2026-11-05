"use client"

import { Dialog } from "@/components/ui/Dialog"

import { BindingWizard } from "./BindingWizard"
import type { Version } from "./types"

export type CreateVersionDialogProps = {
  open: boolean
  onClose: () => void
  onCreated: (version: Version) => void
}

/**
 * Hosts the v0.22 environment-only create wizard (R-C3). The wizard owns the
 * four steps, the catalog/repository/account reads and the create + binding
 * write; the dialog only provides the modal shell and the navigation callback.
 */
export function CreateVersionDialog({ open, onClose, onCreated }: CreateVersionDialogProps) {
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="新建版本"
      description="只需选择环境与版本信息；页面/API/目标库默认全部继承，代码仓与账号可在下一步调整。"
      className="max-w-3xl"
    >
      <div className="max-h-[70vh] overflow-y-auto pr-1">
        <BindingWizard onCreated={onCreated} onCancel={onClose} />
      </div>
    </Dialog>
  )
}
