/**
 * Workspace file transfer (executor v2): upload (base64) / download (base64) /
 * list, anchored at the session workspace.
 *
 * Transfer limits are configurable protocol constants: the JSON request body
 * cap (default 4MiB, enforced by the HTTP layer) and the artifact content cap
 * (default 200MiB, enforced here on decoded upload content and on download
 * stat-before-read). Oversized transfers reject with typed `too_large`.
 *
 * Path confinement to the workspace anchor is FUNCTIONAL workspace integrity
 * (a session's files live in its workspace), NOT a GD-01 security gate — there
 * is no host isolation and no read-only root; exec/shell run natively. Both
 * lexical traversal and symlink/junction components are rejected (resolveWithin).
 */

import crypto from "node:crypto"
import fsp from "node:fs/promises"
import path from "node:path"

import { mimeForPath } from "./recording.mjs"
import { ExecutorError, invalidArguments } from "./protocol.mjs"

// Protocol transfer constants (design §4.1: JSON 4MiB / artifact 200MiB),
// configurable operational parameters — not capability restrictions.
export const DEFAULT_JSON_BODY_LIMIT_BYTES = 4 * 1024 * 1024
export const DEFAULT_ARTIFACT_LIMIT_BYTES = 200 * 1024 * 1024
// Operational listing bound; hitting it sets an explicit truncated flag.
export const DEFAULT_LIST_LIMIT = 10000

function positiveInt(value, fallback) {
  return Number.isInteger(value) && value > 0 ? value : fallback
}

function sha256(buffer) {
  return crypto.createHash("sha256").update(buffer).digest("hex")
}

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

export class WorkspaceFiles {
  constructor({
    jsonBodyLimitBytes = DEFAULT_JSON_BODY_LIMIT_BYTES,
    artifactLimitBytes = DEFAULT_ARTIFACT_LIMIT_BYTES,
    listLimit = DEFAULT_LIST_LIMIT,
  } = {}) {
    this.jsonBodyLimitBytes = positiveInt(jsonBodyLimitBytes, DEFAULT_JSON_BODY_LIMIT_BYTES)
    this.artifactLimitBytes = positiveInt(artifactLimitBytes, DEFAULT_ARTIFACT_LIMIT_BYTES)
    this.listLimit = positiveInt(listLimit, DEFAULT_LIST_LIMIT)
  }

  /**
   * Resolves a workspace-relative path inside the anchor. Absolute paths and
   * any resolution escaping the anchor are typed invalid_arguments; nothing
   * is read or written outside the session workspace through this channel.
   *
   * Symlink confinement (functional workspace integrity): every EXISTING
   * component below the anchor is lstat-checked; a symlink/junction anywhere
   * in the chain (parent directories or the final component) is rejected,
   * because following it would read/write outside the anchor. Non-existing
   * components are fine (upload creates them as regular dirs/files). This
   * complements the lexical check — lexical alone cannot see reparse points.
   */
  async resolveWithin(workspace, relativePath, { allowRoot = false } = {}) {
    if (typeof relativePath !== "string" || relativePath.length === 0) {
      throw invalidArguments("path must be a non-empty workspace-relative string")
    }
    if (relativePath.includes("\0")) {
      throw invalidArguments("path must not contain NUL bytes")
    }
    if (path.isAbsolute(relativePath)) {
      throw invalidArguments("path must be workspace-relative, not absolute")
    }
    const anchor = path.resolve(workspace)
    const resolved = path.resolve(anchor, relativePath)
    if (resolved !== anchor && !resolved.startsWith(anchor + path.sep)) {
      throw invalidArguments("path escapes the session workspace")
    }
    const relative = path.relative(anchor, resolved).split(path.sep).join("/")
    if (relative === "" && !allowRoot) {
      throw invalidArguments("path must name a file inside the workspace")
    }
    // Walk existing components below the anchor; reject any symlink/junction.
    let current = anchor
    for (const part of path.relative(anchor, resolved).split(path.sep)) {
      if (part === "") {
        continue
      }
      current = path.join(current, part)
      let stat
      try {
        stat = await fsp.lstat(current)
      } catch (error) {
        if (error?.code === "ENOENT") {
          // Nothing deeper can exist either; upload will create regular dirs.
          break
        }
        throw error
      }
      if (stat.isSymbolicLink()) {
        throw invalidArguments(
          "path must not traverse a symlink (workspace paths stay inside the anchor)",
        )
      }
    }
    return { absolute: resolved, relative }
  }

  async upload(session, params = {}) {
    if (!isPlainObject(params)) {
      throw invalidArguments("upload parameters must be a JSON object")
    }
    const { path: relativePath, base64 } = params
    if (typeof base64 !== "string") {
      throw invalidArguments("base64 must be a string payload")
    }
    const { absolute, relative } = await this.resolveWithin(session.workspace, relativePath)
    const content = Buffer.from(base64, "base64")
    if (content.length > this.artifactLimitBytes) {
      throw new ExecutorError(
        "too_large",
        `upload is ${content.length} bytes, exceeding the ${this.artifactLimitBytes} byte artifact limit`,
      )
    }
    await fsp.mkdir(path.dirname(absolute), { recursive: true })
    await fsp.writeFile(absolute, content)
    return { path: relative, bytes: content.length, sha256: sha256(content) }
  }

  async download(session, params = {}) {
    if (!isPlainObject(params)) {
      throw invalidArguments("download parameters must be a JSON object")
    }
    const { absolute, relative } = await this.resolveWithin(session.workspace, params.path)
    let stat
    try {
      stat = await fsp.stat(absolute)
    } catch (error) {
      if (error?.code === "ENOENT") {
        throw new ExecutorError("file_not_found", `workspace file ${relative} does not exist`)
      }
      throw error
    }
    if (!stat.isFile()) {
      throw invalidArguments("path must name a regular file")
    }
    // Stat before read: oversized files are rejected without buffering them.
    if (stat.size > this.artifactLimitBytes) {
      throw new ExecutorError(
        "too_large",
        `file is ${stat.size} bytes, exceeding the ${this.artifactLimitBytes} byte artifact limit`,
      )
    }
    const content = await fsp.readFile(absolute)
    return {
      path: relative,
      bytes: content.length,
      sha256: sha256(content),
      mime: mimeForPath(relative),
      base64: content.toString("base64"),
    }
  }

  async list(session, params = {}) {
    if (!isPlainObject(params)) {
      throw invalidArguments("list parameters must be a JSON object")
    }
    const { absolute } = await this.resolveWithin(session.workspace, params.path ?? ".", {
      allowRoot: true,
    })
    let stat
    try {
      stat = await fsp.stat(absolute)
    } catch (error) {
      if (error?.code === "ENOENT") {
        throw new ExecutorError("file_not_found", "workspace path does not exist")
      }
      throw error
    }
    if (!stat.isDirectory()) {
      throw invalidArguments("list path must name a directory")
    }
    const files = []
    let truncated = false
    const stack = [absolute]
    while (stack.length > 0 && !truncated) {
      const dir = stack.pop()
      let entries
      try {
        entries = await fsp.readdir(dir, { withFileTypes: true })
      } catch (error) {
        if (error?.code === "ENOENT") {
          continue
        }
        throw error
      }
      for (const entry of entries) {
        const absoluteChild = path.join(dir, entry.name)
        // Symlinks are not followed (workspace integrity: the listing stays
        // inside the anchor); they are not listed as regular files either.
        if (entry.isSymbolicLink()) {
          continue
        }
        if (entry.isDirectory()) {
          stack.push(absoluteChild)
          continue
        }
        if (!entry.isFile()) {
          continue
        }
        if (files.length >= this.listLimit) {
          truncated = true
          break
        }
        try {
          const childStat = await fsp.stat(absoluteChild)
          files.push({
            path: path.relative(session.workspace, absoluteChild).split(path.sep).join("/"),
            bytes: childStat.size,
            modified_at: childStat.mtime.toISOString(),
          })
        } catch (error) {
          if (error?.code !== "ENOENT") {
            throw error
          }
        }
      }
    }
    files.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0))
    return { files, truncated }
  }
}
