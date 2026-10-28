/** Shared DTOs for the P2 public-resource API (spec §7). */

export type SystemDTO = {
  id: number
  key: string
  name: string
  kind: "web" | "api"
  baseUrl: string
  defaultSessionTtlSeconds: number
  notes: string
  status: string
  createdAt: string
  updatedAt: string
}

export type EnvironmentDTO = {
  id: number
  systemId: number
  key: string
  name: string
  baseUrl: string
  kind: string
  isProd: boolean
  allowedHosts: string[]
  loginCheck: { url: string; expectStatus: number[]; bodyContains: string } | null
  allowAutoLogin: boolean
  requireApproval: boolean
}

export type AppModelNodeDTO = {
  id: number
  systemId: number
  parentId: number | null
  kind: string
  key: string
  name: string
  path: string
  method: string
  source: string
  meta: unknown
}

export type AccountDTO = {
  id: number
  key: string
  label: string
  username: string
  kind: string
  status: string
  notes: string
}

export type AccessDTO = {
  id: number
  accountId: number
  systemId: number
  authType: string
  credentialSet: boolean
  usernameOverride: string
  loginUrl: string
  enabled: boolean
  concurrencyLimit: number
  sessionTtlSeconds: number | null
  permissionNote: string
  /** Bound auto-login recipe id (decimal string) or null (spec §3.4). */
  login_flow_id?: string | null
}

/** Minimal form login recipe definition (spec §3.4). */
export type AuthFlowDefinitionDTO = {
  login_url: string
  username_selector: string
  password_selector: string
  submit_selector: string
  success: { url_contains?: string; selector?: string }
  failure: { mfa_selector?: string; captcha_selector?: string; invalid_selector?: string }
  timeout_ms: number
}

/** Login recipe (auth_flow) of one system; ids are decimal strings (spec §6.1). */
export type AuthFlowDTO = {
  id: string
  system_id: string
  name: string
  kind: string
  definition: AuthFlowDefinitionDTO
  enabled: boolean
  created_at: string
  updated_at: string
}

export type SnapshotDTO = {
  id: number
  sessionId: number
  kind: string
  items: unknown
  summary: unknown
  permissionHash: string
  degraded: boolean
  capturedAt: string
}

export type SessionDTO = {
  id: number
  accountId: number
  systemId: number
  environmentId: number
  accessId: number
  state: string
  acquisition: string
  storageStateKind: string
  cookieCount: number
  originCount: number
  storageStateHash: string
  expiresAt: string | null
  lastValidatedAt: string | null
  lastUsedAt: string | null
  failureReason: string
  invalidatedReason: string
  expiring: boolean
  latestSnapshot?: SnapshotDTO | null
  activeLease?: { holder: string; expiresAt: string } | null
  latestTakeover?: { id: number; status: string; reason: string } | null
}

/** Normalized cursor page used by every resource list call. */
export type Page<T> = {
  items: T[]
  nextCursor: string | null
  hasMore: boolean
}
