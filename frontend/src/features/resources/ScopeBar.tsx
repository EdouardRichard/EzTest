"use client"

import { Field } from "@/components/ui/Field"
import { Select } from "@/components/ui/Select"

import type { Sut, SutEnvironment } from "./types"

export type ScopeBarProps = {
  suts: Sut[]
  sutId: string
  environments: SutEnvironment[]
  environmentId: string
  onSelectSut: (id: string) => void
  onSelectEnvironment: (id: string) => void
  disabled?: boolean
}

/** SUT + environment selector shared by the pages and data tabs. */
export function ScopeBar({
  suts,
  sutId,
  environments,
  environmentId,
  onSelectSut,
  onSelectEnvironment,
  disabled,
}: ScopeBarProps) {
  return (
    <div className="surface-card flex flex-wrap items-end gap-3 px-3 py-2.5">
      <div className="w-48">
        <Field id="scope-sut" label="SUT">
          <Select
            id="scope-sut"
            value={sutId}
            disabled={disabled || suts.length === 0}
            onChange={(event) => onSelectSut(event.target.value)}
          >
            {suts.length === 0 ? <option value="">未选择 SUT</option> : null}
            {suts.map((sut) => (
              <option key={sut.id} value={sut.id}>
                {sut.name}
              </option>
            ))}
          </Select>
        </Field>
      </div>
      <div className="w-48">
        <Field id="scope-environment" label="环境">
          <Select
            id="scope-environment"
            value={environmentId}
            disabled={disabled || environments.length === 0}
            onChange={(event) => onSelectEnvironment(event.target.value)}
          >
            {environments.length === 0 ? <option value="">未选择环境</option> : null}
            {environments.map((environment) => (
              <option key={environment.id} value={environment.id}>
                {environment.name}
                {environment.is_default ? "（默认）" : ""}
              </option>
            ))}
          </Select>
        </Field>
      </div>
      <p className="pb-2 text-[11px] text-muted-foreground">
        页面/API、目标库与测试账号按环境隔离；切换环境不会修改其他环境的资源。
      </p>
    </div>
  )
}
