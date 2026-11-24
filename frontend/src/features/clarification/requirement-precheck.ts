"use client"

import { useEffect, useState } from "react"

import { getSetting } from "@/features/settings/api"

/**
 * Client-side precheck of the requirement CONFIRMATION gate (v0.2.4-E, spec
 * SC-E1/E2). It mirrors the server's `ValidateConfirmed` increment
 * (backend/internal/version/domain/requirement.go): a whole-value placeholder
 * blacklist (trim + lower-case exact match, never a substring) on title, the
 * three user-story parts, every AC given/when/then and sut_description, plus a
 * minimum rune-length floor on sut_description.
 *
 * The word list and threshold are READ FROM SETTINGS at runtime — never
 * hard-coded here. The server remains the authoritative gate; this is only a
 * convenience pre-check so the form can show the server rules and reject early.
 */
export const PLACEHOLDER_TERMS_KEY = "validation.placeholder_terms"
export const MIN_DESCRIPTION_CHARS_KEY = "validation.min_description_chars"

export type ValidationRules = {
  placeholderTerms: string[]
  minDescriptionChars: number
}

export type RequirementPrecheckValues = {
  title: string
  asA: string
  iWant: string
  soThat: string
  sutDescription: string
  criteria: Array<{ given: string; when: string; then: string }>
}

export type RequirementPrecheckErrors = {
  title?: string
  as_a?: string
  i_want?: string
  so_that?: string
  sut_description?: string
  acceptance_criteria?: string
}

/** Mirrors Go NormalizePlaceholderTerm: trim + lower-case. */
export function normalizePlaceholderTerm(value: string): string {
  return value.trim().toLowerCase()
}

/** Mirrors Go IsPlaceholder: whole trimmed+lowercased value equals a term. */
export function isPlaceholder(value: string, terms: string[]): boolean {
  const normalized = normalizePlaceholderTerm(value)
  return terms.some((term) => normalized === normalizePlaceholderTerm(term))
}

const PLACEHOLDER_MESSAGE = "不能是占位词（整值匹配）"

/**
 * Mirrors the ValidateConfirmed increment. Returns field errors keyed like the
 * editor's EditorErrors; an empty object means the pre-check passed. The server
 * still re-validates authoritatively on confirm.
 */
export function precheckConfirmation(
  values: RequirementPrecheckValues,
  rules: ValidationRules,
): RequirementPrecheckErrors {
  const errors: RequirementPrecheckErrors = {}
  const terms = rules.placeholderTerms
  if (isPlaceholder(values.title, terms)) errors.title = PLACEHOLDER_MESSAGE
  if (isPlaceholder(values.asA, terms)) errors.as_a = PLACEHOLDER_MESSAGE
  if (isPlaceholder(values.iWant, terms)) errors.i_want = PLACEHOLDER_MESSAGE
  if (isPlaceholder(values.soThat, terms)) errors.so_that = PLACEHOLDER_MESSAGE
  if (isPlaceholder(values.sutDescription, terms)) errors.sut_description = PLACEHOLDER_MESSAGE

  for (let index = 0; index < values.criteria.length; index += 1) {
    const criterion = values.criteria[index]
    const parts: Array<[string, string]> = [
      ["given", criterion.given],
      ["when", criterion.when],
      ["then", criterion.then],
    ]
    for (const [part, value] of parts) {
      if (isPlaceholder(value, terms)) {
        errors.acceptance_criteria = `第 ${index + 1} 条验收标准的 ${part} 不能是占位词`
        break
      }
    }
    if (errors.acceptance_criteria !== undefined) break
  }

  // Rune-length floor on the trimmed sut_description (Go len([]rune(...))).
  // Spread iterates code points, matching rune count for astral characters.
  if (errors.sut_description === undefined) {
    const length = [...values.sutDescription.trim()].length
    if (length < rules.minDescriptionChars) {
      errors.sut_description = `被测系统描述至少 ${rules.minDescriptionChars} 字（当前 ${length} 字）`
    }
  }
  return errors
}

/**
 * Reads the confirmation rules from the settings center. The settings API is
 * admin-only; for a non-admin viewer (or any read failure) the client pre-check
 * is skipped — the server stays authoritative. No word list or threshold is
 * hard-coded.
 *
 * The returned state distinguishes an UNAVAILABLE read (non-admin, transport —
 * skipping the pre-check there is intended) from a MALFORMED value (the read
 * succeeded but the setting's shape is unexpected). A malformed value must not
 * be silently coerced to `[]`/`0` (H-F10 #44, 宪法七.6 不静默降级): the caller
 * surfaces `invalidShape` so the skipped pre-check is observable.
 */
export type ValidationRulesState = {
  /** Rules when both settings were readable and well-shaped; null otherwise. */
  rules: ValidationRules | null
  /** Set only when a successful read yielded a malformed value shape. */
  invalidShape: string | null
}

export function useValidationRules(): ValidationRulesState {
  const [state, setState] = useState<ValidationRulesState>({ rules: null, invalidShape: null })
  useEffect(() => {
    let cancelled = false
    void Promise.all([getSetting(PLACEHOLDER_TERMS_KEY), getSetting(MIN_DESCRIPTION_CHARS_KEY)])
      .then(([termsSetting, minSetting]) => {
        if (cancelled) return
        const problems: string[] = []
        let placeholderTerms: string[] = []
        // The server reads this key as a JSON array of strings and fails closed
        // (503) otherwise; mirror that shape instead of dropping bad entries.
        if (
          Array.isArray(termsSetting.value) &&
          termsSetting.value.every((term) => typeof term === "string")
        ) {
          placeholderTerms = termsSetting.value.filter(
            (term): term is string => typeof term === "string",
          )
        } else {
          problems.push(`${PLACEHOLDER_TERMS_KEY} 不是字符串数组`)
        }
        let minDescriptionChars = 0
        if (typeof minSetting.value === "number") {
          minDescriptionChars = minSetting.value
        } else {
          problems.push(`${MIN_DESCRIPTION_CHARS_KEY} 不是数字`)
        }
        if (problems.length > 0) {
          setState({ rules: null, invalidShape: problems.join("；") })
          return
        }
        setState({ rules: { placeholderTerms, minDescriptionChars }, invalidShape: null })
      })
      .catch(() => {
        // Non-admin / unavailable: intended skip, server enforces the gate.
      })
    return () => {
      cancelled = true
    }
  }, [])
  return state
}
