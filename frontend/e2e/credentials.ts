/**
 * Shared E2E administrator credentials.
 *
 * EzTest has no built-in administrator password: the API seeds `admin` from
 * `EZTEST_INITIAL_ADMIN_PASSWORD` only while the user table is empty. The E2E
 * suite therefore never falls back to a literal password. It reads the current
 * credentials from the environment and fails with an actionable error when the
 * password is missing.
 *
 * `scripts/e2e.ps1` exports `EZTEST_E2E_ADMIN_USERNAME` and
 * `EZTEST_E2E_ADMIN_PASSWORD`; a manual run can reuse the repository-level
 * `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD` values instead.
 */

function firstNonEmpty(...names: string[]): string {
  for (const name of names) {
    const value = process.env[name]?.trim()
    if (value) return value
  }
  return ""
}

function requireAdminPassword(): string {
  const password = firstNonEmpty("EZTEST_E2E_ADMIN_PASSWORD", "EZTEST_ADMIN_PASSWORD")
  if (password === "") {
    throw new Error(
      "EZTEST_E2E_ADMIN_PASSWORD is unset (EZTEST_ADMIN_PASSWORD is accepted as a fallback). " +
        "EzTest has no built-in administrator password: set the current admin password " +
        "(the deployment's EZTEST_INITIAL_ADMIN_PASSWORD until it is rotated) before running " +
        "the E2E suite.",
    )
  }
  return password
}

/** Administrator login name; the first-start seed always creates `admin`. */
export const ADMIN_USERNAME =
  firstNonEmpty("EZTEST_E2E_ADMIN_USERNAME", "EZTEST_ADMIN_USERNAME") || "admin"

/** Current administrator password; throws during collection when unset. */
export const ADMIN_PASSWORD = requireAdminPassword()
