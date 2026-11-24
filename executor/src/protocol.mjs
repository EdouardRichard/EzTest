/**
 * Executor protocol primitives: the typed protocol error and the JSON request
 * size bound (protocol v2 contract, docs/contracts/executor-protocol-v2.md).
 *
 * GD-01 (single-admin internal trusted deployment): the host allow list, scheme
 * gate and secret-registration set that used to live here are removed. The
 * v1-era artifact/snapshot/timeout/ring helpers with no v2 consumer are deleted
 * (DoD: no unwired code); the live transfer bounds are owned by files.mjs
 * (DEFAULT_JSON_BODY_LIMIT_BYTES / DEFAULT_ARTIFACT_LIMIT_BYTES) and the ring
 * buffer by traffic_recorder.mjs.
 */

export const MAX_REQUEST_BYTES = 1024 * 1024

/** Executor error with a protocol error code (see the protocol contract). */
export class ExecutorError extends Error {
  constructor(code, message, details) {
    super(message)
    this.name = "ExecutorError"
    this.code = code
    if (details !== undefined) {
      this.details = details
    }
  }
}

export function invalidArguments(message) {
  return new ExecutorError("invalid_arguments", message)
}