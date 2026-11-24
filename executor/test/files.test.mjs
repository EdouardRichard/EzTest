// Task 8 (v0.2.4-C): workspace files — upload/download/list with configurable
// transfer limits (JSON body 4MiB / artifact 200MiB protocol constants) and
// workspace-anchored path confinement (functional workspace integrity, NOT a
// GD-01 security gate — there is no host isolation).
import { describe, it } from "node:test"
import assert from "node:assert/strict"
import crypto from "node:crypto"
import fsp from "node:fs/promises"
import os from "node:os"
import path from "node:path"

import {
  DEFAULT_ARTIFACT_LIMIT_BYTES,
  DEFAULT_JSON_BODY_LIMIT_BYTES,
  WorkspaceFiles,
} from "../src/files.mjs"
import { ExecutorError } from "../src/protocol.mjs"
import { SessionManager } from "../src/sessions.mjs"
import { createExecutorServer } from "../src/server.mjs"
import { httpClient } from "./helpers.mjs"

const TOKEN = "test-executor-token"

function sha256(buffer) {
  return crypto.createHash("sha256").update(buffer).digest("hex")
}

async function makeWorkspace(t) {
  const dir = await fsp.mkdtemp(path.join(os.tmpdir(), "eztest-t8-files-"))
  t.after(() => fsp.rm(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }))
  return dir
}

function makeManager(t) {
  const manager = new SessionManager({ logger: null })
  t.after(() => manager.close())
  return manager
}

describe("WorkspaceFiles core", () => {
  it("protocol constants: JSON 4MiB, artifact 200MiB", () => {
    assert.equal(DEFAULT_JSON_BODY_LIMIT_BYTES, 4 * 1024 * 1024)
    assert.equal(DEFAULT_ARTIFACT_LIMIT_BYTES, 200 * 1024 * 1024)
  })

  it("upload/download round-trips binary content with matching sha256", async (t) => {
    const workspace = await makeWorkspace(t)
    const manager = makeManager(t)
    const files = new WorkspaceFiles()
    const created = await manager.create({ workspace })
    const session = manager.sessions.get(created.sessionId)
    const content = Buffer.from([0, 1, 2, 255, 254, 128, 65, 66, 0, 10, 13, 26])
    const uploaded = await files.upload(session, {
      path: "data/blob.bin",
      base64: content.toString("base64"),
    })
    assert.equal(uploaded.path, "data/blob.bin")
    assert.equal(uploaded.bytes, content.length)
    assert.equal(uploaded.sha256, sha256(content))
    // The file really landed inside the workspace anchor.
    const onDisk = await fsp.readFile(path.join(workspace, "data", "blob.bin"))
    assert.deepEqual(onDisk, content)

    const downloaded = await files.download(session, { path: "data/blob.bin" })
    assert.equal(downloaded.bytes, content.length)
    assert.equal(downloaded.sha256, sha256(content))
    assert.equal(downloaded.mime, "application/octet-stream")
    assert.deepEqual(Buffer.from(downloaded.base64, "base64"), content)
  })

  it("list walks nested directories with workspace-relative posix paths", async (t) => {
    const workspace = await makeWorkspace(t)
    const manager = makeManager(t)
    const files = new WorkspaceFiles()
    const created = await manager.create({ workspace })
    const session = manager.sessions.get(created.sessionId)
    await files.upload(session, { path: "a.txt", base64: Buffer.from("a").toString("base64") })
    await files.upload(session, {
      path: "sub/dir/b.json",
      base64: Buffer.from("{}").toString("base64"),
    })
    const listed = await files.list(session, {})
    const byPath = new Map(listed.files.map((f) => [f.path, f]))
    assert.ok(byPath.has("a.txt"))
    assert.ok(byPath.has("sub/dir/b.json"))
    assert.equal(byPath.get("a.txt").bytes, 1)
    assert.equal(byPath.get("sub/dir/b.json").bytes, 2)
    assert.equal(typeof byPath.get("a.txt").modified_at, "string")
    assert.equal(listed.truncated, false)
  })

  it("path traversal is confined to the workspace anchor (typed invalid_arguments)", async (t) => {
    const root = await makeWorkspace(t)
    const workspace = path.join(root, "ws")
    const manager = makeManager(t)
    const files = new WorkspaceFiles()
    const created = await manager.create({ workspace })
    const session = manager.sessions.get(created.sessionId)
    const escapes = [
      "../evil.txt",
      "a/../../evil.txt",
      path.join(root, "absolute-evil.txt"),
      "..",
    ]
    for (const evil of escapes) {
      await assert.rejects(
        () => files.upload(session, { path: evil, base64: Buffer.from("x").toString("base64") }),
        (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
        `upload must reject ${evil}`,
      )
      await assert.rejects(
        () => files.download(session, { path: evil }),
        (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
        `download must reject ${evil}`,
      )
    }
    // Nothing was created outside the anchor.
    const siblings = await fsp.readdir(root)
    assert.deepEqual(siblings.sort(), ["ws"])
  })

  it("artifact limit is configurable and rejects oversized content with typed too_large", async (t) => {
    const workspace = await makeWorkspace(t)
    const manager = makeManager(t)
    const files = new WorkspaceFiles({ artifactLimitBytes: 2048 })
    const created = await manager.create({ workspace })
    const session = manager.sessions.get(created.sessionId)
    const big = Buffer.alloc(3000, 0x61)
    await assert.rejects(
      () => files.upload(session, { path: "big.bin", base64: big.toString("base64") }),
      (error) => error instanceof ExecutorError && error.code === "too_large",
    )
    // A file that appeared in the workspace by other means (exec) is also
    // bounded on download — stat before read, never buffered.
    await fsp.writeFile(path.join(workspace, "grown.bin"), big)
    await assert.rejects(
      () => files.download(session, { path: "grown.bin" }),
      (error) => error instanceof ExecutorError && error.code === "too_large",
    )
    const small = Buffer.alloc(2048, 0x62)
    const ok = await files.upload(session, { path: "ok.bin", base64: small.toString("base64") })
    assert.equal(ok.bytes, 2048)
  })

  it("symlink/junction components cannot escape the anchor (upload/download/list)", async (t) => {
    const root = await makeWorkspace(t)
    const workspace = path.join(root, "ws")
    const outside = path.join(root, "outside")
    const manager = makeManager(t)
    const files = new WorkspaceFiles()
    const created = await manager.create({ workspace })
    const session = manager.sessions.get(created.sessionId)
    await fsp.mkdir(outside, { recursive: true })
    await fsp.writeFile(path.join(outside, "secret.txt"), "outside-secret")
    // A junction (directory reparse point, creatable without privilege on
    // Windows) inside the workspace pointing OUTSIDE the anchor. lstat treats
    // junctions as symlinks; POSIX dir symlinks behave identically here.
    await fsp.symlink(outside, path.join(workspace, "linkdir"), "junction")

    const isSymlinkRejection = (error) =>
      error instanceof ExecutorError &&
      error.code === "invalid_arguments" &&
      /symlink/i.test(error.message)

    // Upload through a symlinked PARENT must not write outside.
    await assert.rejects(
      () => files.upload(session, { path: "linkdir/evil.txt", base64: Buffer.from("x").toString("base64") }),
      isSymlinkRejection,
    )
    assert.deepEqual(await fsp.readdir(outside), ["secret.txt"])
    // Download through a symlinked PARENT must not read outside.
    await assert.rejects(
      () => files.download(session, { path: "linkdir/secret.txt" }),
      isSymlinkRejection,
    )
    // Final-component link: upload naming the link itself, download naming it.
    await assert.rejects(
      () => files.upload(session, { path: "linkdir", base64: Buffer.from("x").toString("base64") }),
      isSymlinkRejection,
    )
    await assert.rejects(
      () => files.download(session, { path: "linkdir" }),
      isSymlinkRejection,
    )
    // List rooted at a symlinked directory must not walk outside.
    await assert.rejects(
      () => files.list(session, { path: "linkdir" }),
      isSymlinkRejection,
    )
    // A normal in-workspace file still round-trips with the guard in place.
    const ok = await files.upload(session, { path: "ok.txt", base64: Buffer.from("inside").toString("base64") })
    assert.equal(ok.bytes, 6)
    const back = await files.download(session, { path: "ok.txt" })
    assert.equal(Buffer.from(back.base64, "base64").toString("utf8"), "inside")
  })

  it("final-component file symlink is rejected where the host can create one", async (t) => {
    const root = await makeWorkspace(t)
    const workspace = path.join(root, "ws")
    const manager = makeManager(t)
    const files = new WorkspaceFiles()
    const created = await manager.create({ workspace })
    const session = manager.sessions.get(created.sessionId)
    const outsideFile = path.join(root, "outside.txt")
    await fsp.writeFile(outsideFile, "outside-secret")
    try {
      await fsp.symlink(outsideFile, path.join(workspace, "filelink.txt"), "file")
    } catch (error) {
      // Windows without SeCreateSymbolicLinkPrivilege/Developer Mode cannot
      // create FILE symlinks (junctions are covered by the test above). The
      // guard logic is component-uniform; this leg runs on capable hosts.
      if (error?.code === "EPERM") {
        t.skip("host cannot create file symlinks (EPERM); junction coverage applies")
        return
      }
      throw error
    }
    const isSymlinkRejection = (error) =>
      error instanceof ExecutorError &&
      error.code === "invalid_arguments" &&
      /symlink/i.test(error.message)
    await assert.rejects(
      () => files.upload(session, { path: "filelink.txt", base64: Buffer.from("x").toString("base64") }),
      isSymlinkRejection,
    )
    await assert.rejects(
      () => files.download(session, { path: "filelink.txt" }),
      isSymlinkRejection,
    )
    assert.equal(await fsp.readFile(outsideFile, "utf8"), "outside-secret")
  })

  it("missing files are typed file_not_found; directories and bad input are invalid_arguments", async (t) => {
    const workspace = await makeWorkspace(t)
    const manager = makeManager(t)
    const files = new WorkspaceFiles()
    const created = await manager.create({ workspace })
    const session = manager.sessions.get(created.sessionId)
    await assert.rejects(
      () => files.download(session, { path: "nope.txt" }),
      (error) => error instanceof ExecutorError && error.code === "file_not_found",
    )
    await fsp.mkdir(path.join(workspace, "adir"))
    await assert.rejects(
      () => files.download(session, { path: "adir" }),
      (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
    )
    for (const params of [{}, { path: "" }, { path: "x.txt" }, { path: "x.txt", base64: 5 }]) {
      await assert.rejects(
        () => files.upload(session, params),
        (error) => error instanceof ExecutorError && error.code === "invalid_arguments",
        `expected invalid_arguments for ${JSON.stringify(params)}`,
      )
    }
  })
})

describe("files HTTP routes (v2)", () => {
  async function startServer(t, { files } = {}) {
    const sessions = new SessionManager({ logger: null })
    const server = createExecutorServer({
      token: TOKEN,
      sessions,
      files,
      logger: null,
      version: "test",
    })
    server.listen(0, "127.0.0.1")
    await new Promise((resolve) => server.once("listening", resolve))
    const { port } = server.address()
    const request = httpClient(`http://127.0.0.1:${port}`, TOKEN)
    t.after(async () => {
      // Delete sessions through the protocol first: stops their watchers
      // deterministically before workspace directories are removed.
      for (const session of sessions.list()) {
        await request(`/v1/sessions/${session.sessionId}`, { method: "DELETE" })
      }
      await sessions.close()
      await new Promise((resolve) => server.close(resolve))
    })
    return { sessions, request }
  }

  async function createSession(request, t) {
    const workspace = await makeWorkspace(t)
    const created = await request("/v1/sessions", { method: "POST", body: { workspace } })
    assert.equal(created.status, 200, created.text)
    return { sessionId: created.json.sessionId, workspace }
  }

  it("POST/GET files round-trip over HTTP with sha256 integrity", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    const content = Buffer.from("hello t8 files\r\nline two", "utf8")
    const uploaded = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "notes/hello.txt", base64: content.toString("base64") },
    })
    assert.equal(uploaded.status, 200, uploaded.text)
    assert.equal(uploaded.json.sha256, sha256(content))
    assert.equal(uploaded.json.bytes, content.length)

    const downloaded = await request(
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("notes/hello.txt")}`,
    )
    assert.equal(downloaded.status, 200, downloaded.text)
    assert.equal(downloaded.json.sha256, sha256(content))
    assert.equal(downloaded.json.mime, "text/plain")
    assert.deepEqual(Buffer.from(downloaded.json.base64, "base64"), content)

    const listed = await request(`/v1/sessions/${sessionId}/files`)
    assert.equal(listed.status, 200)
    assert.ok(listed.json.files.some((f) => f.path === "notes/hello.txt"))
  })

  it("upload bodies over the 4MiB JSON limit are rejected with typed too_large", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    // 3.5MiB binary -> ~4.67MiB base64 body: above the 4MiB JSON protocol cap.
    const over = Buffer.alloc(3.5 * 1024 * 1024, 0x61)
    const rejected = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "over.bin", base64: over.toString("base64") },
    })
    assert.equal(rejected.status, 413, rejected.text)
    assert.equal(rejected.json.error.code, "too_large")
    // 2MiB binary -> ~2.67MiB base64 body: below the cap, accepted.
    const under = Buffer.alloc(2 * 1024 * 1024, 0x62)
    const accepted = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "under.bin", base64: under.toString("base64") },
    })
    assert.equal(accepted.status, 200, accepted.text)
    assert.equal(accepted.json.bytes, under.length)
  })

  it("JSON body limit is configurable (small limit rejects small bodies)", async (t) => {
    const files = new WorkspaceFiles({ jsonBodyLimitBytes: 1024 })
    const { request } = await startServer(t, { files })
    const { sessionId } = await createSession(request, t)
    const rejected = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "x.bin", base64: Buffer.alloc(800, 0x61).toString("base64") },
    })
    assert.equal(rejected.status, 413)
    assert.equal(rejected.json.error.code, "too_large")
    const accepted = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "small.bin", base64: Buffer.alloc(16, 0x61).toString("base64") },
    })
    assert.equal(accepted.status, 200, accepted.text)
  })

  it("artifact limit is configurable end-to-end (upload and download)", async (t) => {
    const files = new WorkspaceFiles({ artifactLimitBytes: 2048 })
    const { request } = await startServer(t, { files })
    const { sessionId, workspace } = await createSession(request, t)
    // Decoded content above the artifact limit is typed too_large even though
    // the JSON body itself is under the (default 4MiB) cap.
    const overArtifact = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "y.bin", base64: Buffer.alloc(3000, 0x61).toString("base64") },
    })
    assert.equal(overArtifact.status, 413)
    assert.equal(overArtifact.json.error.code, "too_large")
    // Download of an oversized workspace file is bounded by the artifact limit.
    await fsp.writeFile(path.join(workspace, "grown.bin"), Buffer.alloc(3000, 0x62))
    const download = await request(
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("grown.bin")}`,
    )
    assert.equal(download.status, 413)
    assert.equal(download.json.error.code, "too_large")
  })

  it("traversal over HTTP is 400 and never writes outside the workspace", async (t) => {
    const { request } = await startServer(t)
    const { sessionId, workspace } = await createSession(request, t)
    const evil = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "../evil.txt", base64: Buffer.from("x").toString("base64") },
    })
    assert.equal(evil.status, 400)
    assert.equal(evil.json.error.code, "invalid_arguments")
    const evilRead = await request(
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("../evil.txt")}`,
    )
    assert.equal(evilRead.status, 400)
    const parent = await fsp.readdir(path.dirname(workspace))
    assert.ok(!parent.includes("evil.txt"))
  })

  it("symlink escape over HTTP is 400 for upload and download", async (t) => {
    const { request } = await startServer(t)
    const { sessionId, workspace } = await createSession(request, t)
    const outside = path.join(path.dirname(workspace), `outside-http-${path.basename(workspace)}`)
    await fsp.mkdir(outside, { recursive: true })
    t.after(() => fsp.rm(outside, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }))
    await fsp.writeFile(path.join(outside, "secret.txt"), "outside-secret")
    await fsp.symlink(outside, path.join(workspace, "linkdir"), "junction")
    const uploaded = await request(`/v1/sessions/${sessionId}/files`, {
      method: "POST",
      body: { path: "linkdir/evil.txt", base64: Buffer.from("x").toString("base64") },
    })
    assert.equal(uploaded.status, 400, uploaded.text)
    assert.equal(uploaded.json.error.code, "invalid_arguments")
    assert.ok(/symlink/i.test(uploaded.json.error.message))
    assert.deepEqual(await fsp.readdir(outside), ["secret.txt"])
    const downloaded = await request(
      `/v1/sessions/${sessionId}/files?path=${encodeURIComponent("linkdir/secret.txt")}`,
    )
    assert.equal(downloaded.status, 400, downloaded.text)
    assert.ok(/symlink/i.test(downloaded.json.error.message))
  })

  it("missing file download is 404 file_not_found; routes require the token", async (t) => {
    const { request } = await startServer(t)
    const { sessionId } = await createSession(request, t)
    const missing = await request(`/v1/sessions/${sessionId}/files?path=nope.txt`)
    assert.equal(missing.status, 404)
    assert.equal(missing.json.error.code, "file_not_found")
    const noToken = await request(`/v1/sessions/${sessionId}/files`, { token: undefined })
    assert.equal(noToken.status, 401)
    const unknownSession = await request("/v1/sessions/sess_missing/files")
    assert.equal(unknownSession.status, 404)
    assert.equal(unknownSession.json.error.code, "session_not_found")
  })
})
