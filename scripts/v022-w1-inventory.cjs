#!/usr/bin/env node
/**
 * v0.22 W1 inventory (W1-1).
 *
 * Produces four deterministic inventory groups from source:
 *   1. constructors  - `func New[A-Z]...` in `backend/internal/**` non-test files,
 *                      with import-aware production/test reference counts.
 *   2. settings      - settings catalog keys with no production reader.
 *   3. routes        - `mux.Handle("METHOD /path", ...)` registrations with no
 *                      production frontend call (method-aware), split into
 *                      business API-only / infra / other classifications.
 *   4. events        - publishable event type set (eventingapp.Event{Type:...},
 *                      publish-call arguments, constants, same-package
 *                      literals, for-range []string).
 *
 * No third-party dependencies: node:fs and node:path only.
 *
 * Exports scanConstructors() and scanPublishedEvents() so later W1 gates
 * (scripts/check-constructor-refs.cjs, W1-5 event consumer registry) can reuse
 * exactly the same scan rules.
 */
'use strict'

const fs = require('node:fs')
const path = require('node:path')

const ROOT = path.resolve(__dirname, '..')
const EVIDENCE_DIR = path.join(ROOT, 'docs', 'version-V0.22', 'acceptance', 'evidence', 'w1')

const EVENT_TYPE_RE = /^[a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+$/

// Event-looking names that are not publishable event types for the v0.22
// baseline counts. Each exclusion carries a source file:line proof and is
// reported in inventory.md, so the baseline is auditable instead of
// target-fitted. Do not add an exclusion without that evidence.
const NON_EVENT_TYPES = new Map([
  [
    'chat.turn',
    'work item kind, not an event type: WorkTypeChatTurn = "chat.turn" at ' +
      'backend/internal/agent/application/ports_agent.go:242. An Event type is never ' +
      'assigned this value; the scanner reaches it through NewWorkItem{Type: workType} ' +
      '(backend/internal/agent/application/reconciler.go:668-673) fed by the ensure(...) ' +
      'call at reconciler.go:627.',
  ],
])

// The zero-production-reference constructors measured on the W1 baseline
// (02bce00d). W1-1 moved each into a same-package _test.go helper, so the live
// scan no longer sees them; the baseline list is kept here so inventory.md
// records the disposition instead of silently losing the evidence.
const DISPOSED_CONSTRUCTORS = [
  {
    name: 'NewBrowserAdapter',
    file: 'backend/internal/execution/infrastructure/adapters/executor_adapter.go',
    line: 41,
    testRefs: 1,
    disposition: 'moved to same-package _test.go helper (adapters_test.go)',
  },
  {
    name: 'NewExecutorControl',
    file: 'backend/internal/execution/infrastructure/adapters/executor_adapter.go',
    line: 209,
    testRefs: 1,
    disposition: 'moved to same-package _test.go helper (adapters_test.go)',
  },
  {
    name: 'NewAdapter',
    file: 'backend/internal/llm/infrastructure/openai_chat.go',
    line: 30,
    testRefs: 16,
    disposition: 'moved to same-package _test.go helper (export_test.go)',
  },
  {
    name: 'NewLLMSummarizer',
    file: 'backend/internal/agent/application/summary.go',
    line: 47,
    testRefs: 5,
    disposition: 'moved to same-package _test.go helper (export_test.go)',
  },
  {
    name: 'NewCoveredMessageID',
    file: 'backend/internal/agent/application/summary.go',
    line: 1364,
    testRefs: 5,
    disposition: 'moved to same-package _test.go helper (export_test.go)',
  },
  {
    name: 'NewSummaryLedger',
    file: 'backend/internal/agent/application/summary.go',
    line: 1918,
    testRefs: 2,
    disposition: 'moved to same-package _test.go helper (export_test.go)',
  },
  {
    name: 'NewError',
    file: 'backend/internal/agent/application/tools/registry.go',
    line: 158,
    testRefs: 3,
    disposition: 'moved to same-package _test.go helper (export_test.go)',
  },
  {
    name: 'NewP1Registry',
    file: 'backend/internal/agent/application/tools/registry.go',
    line: 498,
    testRefs: 30,
    disposition: 'moved to same-package _test.go helper (export_test.go); cross-package tests use NewP2Registry/struct literals',
  },
  {
    name: 'NewExtractionCompletionValidator',
    file: 'backend/internal/agent/application/tools/task_complete.go',
    line: 592,
    testRefs: 5,
    disposition: 'moved to same-package _test.go helper (export_test.go)',
  },
  {
    name: 'NewReportFinalizer',
    file: 'backend/internal/delivery/application/report_tools.go',
    line: 433,
    testRefs: 1,
    disposition: 'moved to same-package _test.go helper (report_tools_test.go)',
  },
].map((c) => ({
  ...c,
  // Plan step 3 requires the disposition *and* the reason. The reason is
  // derived from the measured reference counts so it cannot drift from them.
  reason:
    c.testRefs > 0
      ? `zero production references at the 02bce00d baseline; test-only references (testRefs=${c.testRefs}) -> moved into a same-package _test.go helper so the test seam is kept`
      : 'zero references (production and test) -> deleted',
}))

// ---------------------------------------------------------------------------
// generic helpers
// ---------------------------------------------------------------------------

function walk(dir, out = []) {
  if (!fs.existsSync(dir)) return out
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) walk(full, out)
    else out.push(full)
  }
  return out
}

function rel(file) {
  return path.relative(ROOT, file).split(path.sep).join('/')
}

function read(file) {
  return fs.readFileSync(file, 'utf8')
}

/** Strip Go comments while preserving string/rune literals and line numbers. */
function stripGoComments(src) {
  let out = ''
  for (let i = 0; i < src.length; i++) {
    const c = src[i]
    if (c === '"' || c === "'" || c === '`') {
      const quote = c
      out += c
      i++
      while (i < src.length) {
        out += src[i]
        if (src[i] === '\\') {
          i++
          if (i < src.length) out += src[i]
        } else if (src[i] === quote) {
          break
        }
        i++
      }
    } else if (c === '/' && src[i + 1] === '/') {
      while (i < src.length && src[i] !== '\n') i++
      out += '\n'
    } else if (c === '/' && src[i + 1] === '*') {
      i += 2
      while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) i++
      i++
    } else {
      out += c
    }
  }
  return out
}

function modulePath() {
  const gomod = path.join(ROOT, 'backend', 'go.mod')
  if (!fs.existsSync(gomod)) return ''
  const m = /^module\s+(\S+)/m.exec(read(gomod))
  return m ? m[1] : ''
}

function goFiles(dir) {
  return walk(dir)
    .filter((file) => file.endsWith('.go'))
    .map((file) => ({ file, rel: rel(file), test: file.endsWith('_test.go'), src: read(file) }))
}

function packageName(src) {
  const m = /^package\s+([A-Za-z_]\w*)/m.exec(src)
  return m ? m[1] : ''
}

/** Parse imports: returns Map(localAlias -> importPath). */
function parseImports(src, pkgByPath = new Map()) {
  const imports = new Map()
  const block = /import\s*\(([\s\S]*?)\)/.exec(src)
  const lines = []
  if (block) lines.push(...block[1].split(/\r?\n/))
  const single = /import\s+(?:([A-Za-z_.]\w*)\s+)?"([^"]+)"/g
  let m
  while ((m = single.exec(src))) {
    imports.set(m[1] || pkgByPath.get(m[2]) || path.posix.basename(m[2]), m[2])
  }
  for (const line of lines) {
    const im = /^\s*(?:([A-Za-z_.]\w*)\s+)?"([^"]+)"/.exec(line)
    if (im) imports.set(im[1] || pkgByPath.get(im[2]) || path.posix.basename(im[2]), im[2])
  }
  return imports
}

function lineOf(src, index) {
  return src.slice(0, index).split('\n').length
}

// ---------------------------------------------------------------------------
// 1. constructors
// ---------------------------------------------------------------------------

function scanConstructors() {
  const mod = modulePath()
  const internal = path.join(ROOT, 'backend', 'internal')
  const backend = path.join(ROOT, 'backend')
  const all = goFiles(backend)
  const internalFiles = all.filter((f) => f.rel.startsWith('backend/internal/'))
  const prod = internalFiles.filter((f) => !f.test)
  const prodUnitsAll = all.filter((f) => !f.test)

  const pkgNameByDir = new Map()
  const importPathByDir = new Map()
  for (const f of prodUnitsAll) {
    const dir = path.dirname(f.file)
    if (!pkgNameByDir.has(dir)) pkgNameByDir.set(dir, packageName(f.src))
    if (!importPathByDir.has(dir)) {
      const relDir = path.relative(path.join(ROOT, 'backend'), dir).split(path.sep).join('/')
      importPathByDir.set(dir, mod ? `${mod}/${relDir}` : relDir)
    }
  }

  const pkgByPath = new Map()
  for (const [dir, name] of pkgNameByDir) {
    const importPath = importPathByDir.get(dir)
    if (importPath) pkgByPath.set(importPath, name)
  }

  const defs = []
  for (const f of prod) {
    const dir = path.dirname(f.file)
    const code = stripGoComments(f.src)
    const re = /^func\s+(New[A-Z][A-Za-z0-9_]*)\s*\(/gm
    let m
    while ((m = re.exec(code))) {
      defs.push({
        name: m[1],
        file: f.rel,
        line: lineOf(code, m.index),
        dir,
        packageName: pkgNameByDir.get(dir) || '',
        importPath: importPathByDir.get(dir) || '',
        testutil: f.rel.startsWith('backend/internal/testutil/'),
      })
    }
  }

  const prodUnits = prodUnitsAll.map((f) => ({
    rel: f.rel,
    dir: path.dirname(f.file),
    code: stripGoComments(f.src),
    imports: parseImports(f.src, pkgByPath),
  }))
  const testUnits = all
    .filter((f) => f.test)
    .map((f) => ({
      rel: f.rel,
      dir: path.dirname(f.file),
      code: stripGoComments(f.src),
      imports: parseImports(f.src, pkgByPath),
    }))

  function countsFor(def) {
    let prodRefs = 0
    let testRefs = 0
    const samePkgBare = new RegExp('\\b' + def.name + '\\b', 'g')
    for (const f of prodUnits) {
      const isDefFile = f.rel === def.file
      if (f.dir === def.dir) {
        const re = new RegExp(samePkgBare.source, 'g')
        let m
        while ((m = re.exec(f.code))) {
          if (isDefFile && lineOf(f.code, m.index) === def.line) continue
          prodRefs++
        }
        continue
      }
      // Import-aware qualified references from another production package.
      for (const [alias, importPath] of f.imports) {
        if (importPath !== def.importPath) continue
        const re = new RegExp('\\b' + alias.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\s*\\.\\s*' + def.name + '\\b', 'g')
        let m
        while ((m = re.exec(f.code))) prodRefs++
      }
    }
    for (const f of testUnits) {
      if (f.dir === def.dir) {
        const re = new RegExp('\\b' + def.name + '\\b', 'g')
        let m
        while ((m = re.exec(f.code))) testRefs++
        continue
      }
      for (const [alias, importPath] of f.imports) {
        if (importPath !== def.importPath) continue
        const re = new RegExp('\\b' + alias.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\s*\\.\\s*' + def.name + '\\b', 'g')
        let m
        while ((m = re.exec(f.code))) testRefs++
      }
    }
    return { prodRefs, testRefs }
  }

  return defs
    .map((def) => {
      const { prodRefs, testRefs } = countsFor(def)
      return { ...def, prodRefs, testRefs, totalRefs: prodRefs + testRefs }
    })
    .sort((a, b) => a.file.localeCompare(b.file) || a.line - b.line)
}

// ---------------------------------------------------------------------------
// 2. settings
// ---------------------------------------------------------------------------

function scanSettings() {
  const catalogFile = path.join(ROOT, 'backend', 'internal', 'settings', 'application', 'catalog.go')
  const catalog = read(catalogFile)
  const keys = []
  const re = /Key:\s*"([^"]+)"/g
  let m
  while ((m = re.exec(catalog))) keys.push(m[1])

  const readers = goFiles(path.join(ROOT, 'backend')).filter(
    (f) => !f.test && f.rel !== 'backend/internal/settings/application/catalog.go',
  )
  return keys.map((key) => {
    const needle = '"' + key + '"'
    const hits = []
    for (const f of readers) {
      if (!f.src.includes(needle)) continue
      for (const line of f.src.split(/\r?\n/)) {
        if (line.includes(needle)) hits.push({ file: f.rel, line: line.trim() })
      }
    }
    return { key, readers: hits.length, sites: hits.slice(0, 5) }
  })
}

// ---------------------------------------------------------------------------
// 3. routes
// ---------------------------------------------------------------------------

function scanRoutes() {
  const registered = []
  const files = goFiles(path.join(ROOT, 'backend', 'internal')).filter((f) => !f.test)
  for (const f of files) {
    const lines = f.src.split(/\r?\n/)
    lines.forEach((line, index) => {
      const m = /mux\.Handle(?:Func)?\("([A-Z]+)\s+([^"]+)"\s*,(.*)$/.exec(line)
      if (!m) return
      const handler = /h\.([A-Za-z_]\w*)\b/.exec(m[3])
      registered.push({
        method: m[1],
        path: m[2],
        handler: handler ? handler[1] : '',
        file: f.rel,
        line: index + 1,
      })
    })
  }

  const frontendDir = path.join(ROOT, 'frontend', 'src')
  const frontendFiles = walk(frontendDir).filter(
    (file) => /\.(ts|tsx|js|jsx)$/.test(file) && !/\.(test|spec)\./.test(path.basename(file)),
  )
  const originals = frontendFiles.map((file) => read(file))

  // Resolve simple path constants and helper functions so frontend path
  // composition (versionPath(), itemPath(), extractionPath(), ...) is visible.
  const defs = new Map()
  for (const text of originals) {
    let m
    const constRe = /const\s+([A-Za-z_]\w*)\s*(?::\s*[^=]+)?=\s*`([^`]*)`/g
    while ((m = constRe.exec(text))) defs.set(m[1], m[2])
    const strConstRe = /const\s+([A-Za-z_]\w*)\s*(?::\s*[^=]+)?=\s*"([^"]*)"/g
    while ((m = strConstRe.exec(text))) defs.set(m[1], m[2])
    const fnRe = /function\s+([A-Za-z_]\w*)\s*\([^)]*\)\s*(?::\s*[^{]+)?\{/g
    while ((m = fnRe.exec(text))) {
      let depth = 1
      let j = fnRe.lastIndex
      while (j < text.length && depth > 0) {
        if (text[j] === '{') depth++
        else if (text[j] === '}') depth--
        j++
      }
      const body = text.slice(fnRe.lastIndex, j - 1)
      let body2 = body
      const localRe = /const\s+([A-Za-z_]\w*)\s*(?::\s*[^=]+)?=\s*`([^`]*)`/g
      const locals = []
      let lm
      while ((lm = localRe.exec(body))) locals.push([lm[1], lm[2]])
      for (const [name, value] of locals) {
        body2 = body2.replace(new RegExp('\\b' + name + '\\b', 'g'), '\u0001' + value + '\u0001')
      }
      const returns = []
      const retRe = /return\s+([^;\n]+)/g
      while ((lm = retRe.exec(body2))) returns.push(lm[1])
      if (returns.length > 0) {
        const parts = []
        for (const expr of returns) {
          const templates = [...expr.matchAll(/`([^`]*)`/g)].map((t) => t[1])
          if (templates.length > 0) parts.push(...templates)
          else if (expr.includes('/api/')) parts.push(expr)
        }
        for (const marker of body2.matchAll(/\u0001([\s\S]*?)\u0001/g)) {
          if (marker[1].includes('/api/')) parts.push(marker[1])
        }
        if (parts.length > 0) defs.set(m[1], '\u0002' + parts.join('\u0002') + '\u0002')
      }
    }
  }

  const interpRe = (name) =>
    new RegExp('\\$\\{\\s*' + name + '\\([^()]*(?:\\([^()]*\\)[^()]*)*\\)\\s*\\}', 'g')
  const callRe = (name) =>
    new RegExp('(?<!function\\s)\\b' + name + '\\([^()]*(?:\\([^()]*\\)[^()]*)*\\)', 'g')

  const rawConsts = new Map()
  for (const [name, body] of defs) {
    if (!body.includes('\u0002') && body.includes('/api/')) rawConsts.set(name, body)
  }

  function normalizeInterpolations(text) {
    let out = ''
    for (let i = 0; i < text.length; i++) {
      if (text[i] === '$' && text[i + 1] === '{') {
        let depth = 1
        i += 2
        while (i < text.length && depth > 0) {
          if (text[i] === '{') depth++
          else if (text[i] === '}') depth--
          i++
        }
        i--
        out += '{}'
      } else {
        out += text[i]
      }
    }
    return out
  }

  function stripMarkers(text) {
    return text.replace(/[\u0000\u0001]/g, '')
  }

  // `${/api/versions/{}/issues/{}}` arises when a helper template is inlined
  // inside another template interpolation; unwrap it so the path is preserved.
  function unwrapApiInterpolations(text) {
    let out = ''
    for (let i = 0; i < text.length; i++) {
      if (text[i] === '$' && text[i + 1] === '{') {
        let depth = 1
        let j = i + 2
        while (j < text.length && depth > 0) {
          if (text[j] === '{') depth++
          else if (text[j] === '}') depth--
          j++
        }
        const inner = text.slice(i + 2, j - 1)
        if (inner.includes('/api/')) {
          out += inner
          i = j - 1
          continue
        }
      }
      out += text[i]
    }
    return out
  }

  function clean(text) {
    return normalizeInterpolations(stripMarkers(text))
  }

  const patterns = new Map()
  const helpers = [...defs.entries()].filter(([, body]) => body.includes('\u0002'))
  for (let pass = 0; pass < 8; pass++) {
    let changed = false
    for (const [name, body] of helpers) {
      const rawParts = body
        .split('\u0002')
        .map((part) => stripMarkers(part).trim())
        .filter(Boolean)
      const out = []
      for (let part of rawParts) {
        for (const [inner, innerParts] of patterns) {
          if (inner === name) continue
          for (const innerPart of [...innerParts].sort((a, b) => b.length - a.length)) {
            part = part.replace(interpRe(inner), innerPart).replace(callRe(inner), innerPart)
          }
        }
        for (const [constName, constValue] of rawConsts) {
          part = part
            .replace(new RegExp('\\$\\{\\s*' + constName + '\\s*\\}', 'g'), constValue)
            .replace(new RegExp('\\b' + constName + '\\b', 'g'), constValue)
        }
        part = normalizeInterpolations(unwrapApiInterpolations(part))
          .trim()
          .replace(/^`+/, '')
          .replace(/`+$/, '')
          .trim()
        if (part.startsWith('/api/') && part.includes('/api/')) out.push(part)
      }
      const next = out.join('\u0002')
      const previous = (patterns.get(name) || []).join('\u0002')
      if (next !== previous) {
        patterns.set(name, out)
        changed = true
      }
    }
    if (!changed) break
  }

  const texts = originals.map((text) => {
    let out = text
    for (const [constName, constValue] of rawConsts) {
      out = out
        .replace(new RegExp('\\$\\{\\s*' + constName + '\\s*\\}', 'g'), constValue)
        .replace(new RegExp('\\b' + constName + '\\b', 'g'), constValue)
    }
    for (const [name, parts] of patterns) {
      for (const part of [...parts].sort((a, b) => b.length - a.length)) {
        out = out.replace(interpRe(name), part).replace(callRe(name), part)
      }
    }
    return clean(out)
  })

  function routeRe(routePath) {
    let p = routePath.replace(/\{[^}]+\}/g, '{}')
    p = p.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/\\\{\\\}/g, '[^/`"\']+')
    return new RegExp(p + '(?=$|[?{`"\'\\s,)\\]])', 'g')
  }

  const methodMap = {
    apiGet: 'GET',
    apiGetPage: 'GET',
    apiDownload: 'GET',
    apiPost: 'POST',
    apiPatch: 'PATCH',
    apiPut: 'PUT',
    apiDelete: 'DELETE',
    apiUpload: 'POST',
  }
  const used = new Set()
  for (const route of registered) {
    const re = routeRe(route.path)
    for (const text of texts) {
      re.lastIndex = 0
      let m
      while ((m = re.exec(text))) {
        const before = text.slice(Math.max(0, m.index - 400), m.index)
        let method = ''
        const mr = /\bapi(GetPage|Get|Post|Patch|Put|Delete|Download|Upload|Fetch)\b(?:<[^>]*>)?\s*\(/g
        let mm
        while ((mm = mr.exec(before))) method = methodMap['api' + mm[1]] || ''
        if (method === route.method) {
          used.add(route.method + ' ' + route.path)
          break
        }
      }
      if (used.has(route.method + ' ' + route.path)) break
    }
  }

  // Legacy aliases/duplicate registrations share one handler: if any of its
  // mounts is called by the frontend, the handler is considered reachable.
  const usedHandlers = new Set()
  for (const route of registered) {
    if (route.handler && used.has(route.method + ' ' + route.path)) {
      const tail = route.path.split('/').pop()
      usedHandlers.add(path.posix.dirname(route.file) + '|' + route.method + '|' + route.handler + '|' + tail)
    }
  }

  const nonBusiness = [
    [/^GET \/healthz$/, 'probe', 'liveness probe (non-API)'],
    [/^(GET|POST) \/api\/internal\/executors\//, 'infra', 'executor protocol (internal)'],
    [/^GET \/api\/health$/, 'infra', 'health/readiness probe'],
    [/^GET \/api\/health\/ready$/, 'infra', 'health/readiness probe'],
    [/^GET \/api\/stream$/, 'infra', 'SSE transport endpoint (EventSource, not apiGet)'],
    [/^POST \/api\/jobs\/[^ ]*\/(cancel|retry)$/, 'legacy-alias', 'legacy job-control mount; the frontend calls the versioned alias'],
    [/^POST \/api\/versions\/[^ ]*\/jobs\/[^ ]*\/(cancel|retry)$/, 'dynamic-action', 'frontend builds the action suffix from a union literal'],
    [/^(POST) \/api\/versions\/[^ ]*\/conversations\/[^ ]*\/questions$/, 'agent-internal', 'structured questions are created by the Agent runtime'],
    [/^POST \/api\/versions\/[^ ]*\/questions\/[^ ]*\/reopen$/, 'agent-internal', 'question reopen is agent-assisted; not wired to a W1 UI'],
    [/^POST \/api\/admin\/executors\/[^ ]*\/(drain|activate)$/, 'admin-lifecycle', 'executor lifecycle control belongs to W2 executor management'],
    [/^(GET|POST) \/api\/versions\/[^ ]*\/accounts$/, 'account-management', 'version SUT account endpoints are consumed by the resources settings flow'],
    [/^POST \/api\/quality\/eval-runs$/, 'admin-write', 'eval runs are created by admin/system tooling, not by the W1 quality UI'],
    [/^GET \/api\/search$/, 'ui-indirect', 'the search UI passes a buildSearchPath() result to apiGet'],
    [/^GET \/api\/resources\//, 'catalog-embedded', 'resource catalog data is embedded in the SUT aggregate read'],
    [/^POST \/api\/resources\/environments\/[^ ]*\/(pages|apis)\/import$/, 'catalog-embedded', 'resource catalog import is invoked from the SUT aggregate editor'],
    [/^GET \/api\/versions\/[^ ]*\/requirements\/extractions$/, 'ui-indirect', 'extraction status is read through the extractionPath() helper'],
    [/^POST \/api\/versions\/[^ ]*\/requirements\/extractions$/, 'ui-indirect', 'extraction start is invoked through the extractionPath() helper'],
    [/^PATCH \/api\/versions\/[^ ]*\/binding$/, 'w1-8-scope', 'D6 binding edit (PATCH) is added by W1-8; the current UI uses PUT'],
  ]

  const noFrontend = []
  for (const route of registered) {
    if (used.has(route.method + ' ' + route.path)) continue
    const tail = route.path.split('/').pop()
    if (
      route.handler &&
      usedHandlers.has(path.posix.dirname(route.file) + '|' + route.method + '|' + route.handler + '|' + tail)
    ) {
      continue
    }
    const key = route.method + ' ' + route.path
    const classification = nonBusiness.find(([re]) => re.test(key))
    noFrontend.push({
      ...route,
      category: classification ? classification[1] : 'business',
      reason: classification ? classification[2] : 'no production frontend call',
    })
  }
  return { registered, noFrontend }
}

// ---------------------------------------------------------------------------
// 4. publishable events
// ---------------------------------------------------------------------------

function scanPublishedEvents() {
  const internal = path.join(ROOT, 'backend', 'internal')
  const units = goFiles(internal)
    .filter((f) => !f.test)
    .map((f) => ({
      rel: f.rel,
      dir: path.dirname(f.file),
      src: stripGoComments(f.src),
    }))

  const dirConsts = new Map()
  const globalConsts = new Map()
  for (const unit of units) {
    if (!dirConsts.has(unit.dir)) dirConsts.set(unit.dir, new Map())
    const dm = dirConsts.get(unit.dir)
    const re = /\b([A-Za-z_]\w*)\s*(?::=|=)\s*"([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)"/g
    let m
    while ((m = re.exec(unit.src))) {
      dm.set(m[1], m[2])
      if (!globalConsts.has(m[1])) globalConsts.set(m[1], m[2])
    }
  }

  const found = new Map()
  function add(type, evidence, via) {
    if (typeof type !== 'string' || !EVENT_TYPE_RE.test(type)) return
    if (NON_EVENT_TYPES.has(type)) return
    if (!found.has(type)) found.set(type, { evidence, via: via || '' })
  }

  function resolveSimple(expr, locals, varTypes) {
    expr = expr.trim().replace(/^\(|\)$/g, '')
    let m
    if ((m = /^"([^"\\]*(?:\\.[^"\\]*)*)"$/.exec(expr))) return [m[1]]
    if ((m = /^([A-Za-z_]\w*)\.([A-Za-z_]\w*)$/.exec(expr))) {
      const structType = varTypes && varTypes.get(m[1])
      if (structType) {
        const projected = structFieldValues.get(structType + '.' + m[2])
        if (projected && projected.size > 0) return [...projected]
      }
      const v = globalConsts.get(m[2])
      return v ? [v] : []
    }
    if ((m = /^([A-Za-z_]\w*)$/.exec(expr))) {
      const values = []
      if (locals && locals.has(m[1])) values.push(...locals.get(m[1]))
      if (globalConsts.has(m[1])) values.push(globalConsts.get(m[1]))
      return values
    }
    if ((m = /^"([^"]*)"\s*\+\s*(.+)$/.exec(expr))) {
      return resolveSimple(m[2], locals, varTypes).map((suffix) => m[1] + suffix)
    }
    return []
  }

  // Parse functions/methods so event-type parameters can be followed to call
  // sites (resource.record, orchestration.transition, ...).
  const functions = []
  for (const unit of units) {
    const re = /func\s+(?:\([^)]*\)\s+)?([A-Za-z_]\w*)\s*\(/g
    let m
    while ((m = re.exec(unit.src))) {
      const name = m[1]
      let depth = 1
      let j = re.lastIndex
      while (j < unit.src.length && depth > 0) {
        if (unit.src[j] === '(') depth++
        else if (unit.src[j] === ')') depth--
        j++
      }
      const paramsText = unit.src.slice(re.lastIndex, j - 1)
      let b = j
      while (b < unit.src.length && unit.src[b] !== '{') b++
      let bd = 1
      let k = b + 1
      while (k < unit.src.length && bd > 0) {
        if (unit.src[k] === '{') bd++
        else if (unit.src[k] === '}') bd--
        k++
      }
      const body = unit.src.slice(b + 1, k - 1)
      const params = paramsText
        .split(',')
        .map((p) => p.trim())
        .filter(Boolean)
        .map((p) => (p.match(/^([A-Za-z_]\w*)/) || [])[1])
        .filter(Boolean)
      functions.push({ name, params, paramsText, body, unit, start: m.index, end: k })
      re.lastIndex = k
    }
  }

  /** Split a Go argument/parameter list at top-level commas. */
  function splitTopLevel(text) {
    const args = []
    let cur = ''
    let depth = 0
    let quote = ''
    for (let x = 0; x < text.length; x++) {
      const ch = text[x]
      if (quote) {
        cur += ch
        if (ch === '\\') cur += text[++x]
        else if (ch === quote) quote = ''
        continue
      }
      if (ch === '"' || ch === "'" || ch === '`') {
        quote = ch
        cur += ch
        continue
      }
      if (ch === '(' || ch === '{' || ch === '[') depth++
      if (ch === ')' || ch === '}' || ch === ']') depth--
      if (ch === ',' && depth === 0) {
        args.push(cur.trim())
        cur = ''
      } else {
        cur += ch
      }
    }
    if (cur.trim()) args.push(cur.trim())
    return args
  }

  // Struct definitions and literals: a package constant can be projected into a
  // struct field and published later through that field (for example
  // issueEventUpdated -> issueTransition.eventType -> publishIssueEvent). Parse
  // the projection chain generically instead of hard-coding the event name.
  const structTypes = new Map() // type name -> Set(field names)
  for (const unit of units) {
    const re = /\btype\s+([A-Za-z_]\w*)\s+struct\s*\{/g
    let m
    while ((m = re.exec(unit.src))) {
      let depth = 1
      let j = re.lastIndex
      while (j < unit.src.length && depth > 0) {
        if (unit.src[j] === '{') depth++
        else if (unit.src[j] === '}') depth--
        j++
      }
      const body = unit.src.slice(re.lastIndex, j - 1)
      const fields = new Set()
      for (const line of body.split(/\r?\n/)) {
        const fm = /^\s*([A-Za-z_]\w*)\s+[A-Za-z_][\w.]*(?:\[[^\]]*\])?/.exec(line)
        if (fm) fields.add(fm[1])
      }
      structTypes.set(m[1], fields)
      re.lastIndex = j
    }
  }

  function baseTypeName(type) {
    return type.trim().replace(/^\*/, '').replace(/^\[\]/, '')
  }

  // Variables known to hold one of the local struct types: named parameters and
  // local struct declarations.
  function varTypesOf(fn) {
    const types = new Map()
    for (const part of splitTopLevel(fn.paramsText)) {
      const pm = /^([A-Za-z_]\w*)\s+(.+)$/.exec(part)
      if (!pm) continue
      const base = baseTypeName(pm[2])
      if (/^[A-Za-z_]\w*$/.test(base) && structTypes.has(base)) types.set(pm[1], base)
    }
    const declRe = /\b([A-Za-z_]\w*)\s*:?=\s*([A-Za-z_]\w*)\s*\{/g
    let dm
    while ((dm = declRe.exec(fn.body))) {
      if (structTypes.has(dm[2])) types.set(dm[1], dm[2])
    }
    return types
  }

  function projectionVia(expr, varTypes) {
    const m = /^([A-Za-z_]\w*)\.([A-Za-z_]\w*)$/.exec(expr.trim().replace(/^\(|\)$/g, ''))
    if (!m || !varTypes || !varTypes.has(m[1])) return ''
    return varTypes.get(m[1]) + '.' + m[2]
  }

  function localsOf(fn) {
    const locals = new Map()
    const varTypes = varTypesOf(fn)
    const re = /\b([A-Za-z_]\w*)\s*:?=\s*([^\n;]+)/g
    let m
    while ((m = re.exec(fn.body))) {
      if (!/^(event|action)/i.test(m[1]) && !/\+\s*[A-Za-z_]/.test(m[2])) continue
      const values = resolveSimple(m[2], locals, varTypes)
      if (!locals.has(m[1])) locals.set(m[1], [])
      for (const value of values) {
        if (!locals.get(m[1]).includes(value)) locals.get(m[1]).push(value)
      }
    }
    return locals
  }

  function enclosing(unit, index) {
    return functions.find((fn) => fn.unit === unit && fn.start <= index && index < fn.end)
  }

  // Field values seen in struct literals: "Type.field" -> Set(event types).
  const structFieldValues = new Map()
  const structLitRe = /\b([A-Za-z_]\w*)\s*\{/g
  for (const unit of units) {
    let m
    while ((m = structLitRe.exec(unit.src))) {
      const type = m[1]
      if (!structTypes.has(type)) continue
      let depth = 1
      let j = structLitRe.lastIndex
      while (j < unit.src.length && depth > 0) {
        if (unit.src[j] === '{') depth++
        else if (unit.src[j] === '}') depth--
        j++
      }
      const body = unit.src.slice(structLitRe.lastIndex, j - 1)
      const caller = enclosing(unit, m.index)
      const locals = caller ? localsOf(caller) : new Map()
      const varTypes = caller ? varTypesOf(caller) : new Map()
      for (const pair of splitTopLevel(body)) {
        const fm = /^([A-Za-z_]\w*)\s*:\s*([\s\S]+)$/.exec(pair)
        if (!fm) continue
        // The event envelope's Type field is already covered by the dedicated
        // Event{Type: ...} scan. Projecting it back would be circular (the
        // forwarding Publish/Append would resolve through values it seeded
        // itself) and would shadow the originating publish site.
        if (type === 'Event' && fm[1] === 'Type') continue
        for (const value of resolveSimple(fm[2], locals, varTypes)) {
          if (!EVENT_TYPE_RE.test(value)) continue
          const key = type + '.' + fm[1]
          if (!structFieldValues.has(key)) structFieldValues.set(key, new Set())
          structFieldValues.get(key).add(value)
        }
      }
      structLitRe.lastIndex = j
    }
  }

  // Function event-type parameter positions.
  const fnSpecs = new Map()
  for (const fn of functions) {
    const specs = []
    const typeExprs = []
    const typeRe = /\bType:\s*([^,\n}]+)/g
    let m
    while ((m = typeRe.exec(fn.body))) typeExprs.push(m[1])
    for (const raw of typeExprs) {
      const expr = raw.trim()
      let index = fn.params.indexOf(expr)
      if (index >= 0) {
        specs.push({ index, prefix: '' })
        continue
      }
      if (!/^[A-Za-z_]\w*$/.test(expr)) continue
      const assign = new RegExp('\\b' + expr + '\\s*:?=\\s*"([^"]*)"\\s*\\+\\s*([A-Za-z_]\\w*)', 'g')
      let am
      while ((am = assign.exec(fn.body))) {
        const pi = fn.params.indexOf(am[2])
        if (pi >= 0) specs.push({ index: pi, prefix: am[1] })
      }
    }
    const pubRe = /\b(?:[A-Za-z_]\w*\.)?(publish[A-Za-z_]*|Publish[A-Za-z_]*|Append)\s*\(/g
    while ((m = pubRe.exec(fn.body))) {
      let depth = 1
      let j = pubRe.lastIndex
      while (j < fn.body.length && depth > 0) {
        if (fn.body[j] === '(') depth++
        else if (fn.body[j] === ')') depth--
        j++
      }
      const args = fn.body.slice(pubRe.lastIndex, j - 1)
      args.split(',').forEach((arg) => {
        const pi = fn.params.indexOf(arg.trim())
        if (pi >= 0) specs.push({ index: pi, prefix: '' })
      })
      pubRe.lastIndex = j
    }
    if (specs.length > 0) fnSpecs.set(fn.name, specs)
  }

  for (const fn of functions) {
    const specs = fnSpecs.get(fn.name)
    if (!specs) continue
    for (const unit of units.filter((u) => u.dir === fn.unit.dir)) {
      const re = new RegExp('(?:\\.|\\b)' + fn.name + '\\s*\\(', 'g')
      let m
      while ((m = re.exec(unit.src))) {
        const pre = unit.src.slice(Math.max(0, m.index - 10), m.index)
        if (/func\s+$/.test(pre)) continue
        let depth = 1
        let j = re.lastIndex
        while (j < unit.src.length && depth > 0) {
          if (unit.src[j] === '(') depth++
          else if (unit.src[j] === ')') depth--
          j++
        }
        const args = splitTopLevel(unit.src.slice(re.lastIndex, j - 1))
        const caller = enclosing(unit, m.index)
        const locals = caller ? localsOf(caller) : new Map()
        const varTypes = caller ? varTypesOf(caller) : new Map()
        for (const spec of specs) {
          const arg = args[spec.index]
          if (!arg) continue
          const via = projectionVia(arg, varTypes)
          for (const value of resolveSimple(arg, locals, varTypes)) {
            add(spec.prefix + value, unit.rel + ':' + lineOf(unit.src, m.index), via)
          }
        }
      }
    }
  }

  // Direct publish*/Append call arguments and Event{Type: ...} literals.
  for (const unit of units) {
    const pubRe = /\b(?:[A-Za-z_]\w*\.)?(publish[A-Za-z_]*|Publish[A-Za-z_]*|Append)\s*\(/g
    let m
    while ((m = pubRe.exec(unit.src))) {
      let depth = 1
      let j = pubRe.lastIndex
      while (j < unit.src.length && depth > 0) {
        if (unit.src[j] === '(') depth++
        else if (unit.src[j] === ')') depth--
        j++
      }
      const args = unit.src.slice(pubRe.lastIndex, j - 1)
      const caller = enclosing(unit, m.index)
      const locals = caller ? localsOf(caller) : new Map()
      const varTypes = caller ? varTypesOf(caller) : new Map()
      const site = unit.rel + ':' + lineOf(unit.src, m.index)
      const tokenRe = /"([^"\\]*(?:\\.[^"\\]*)*)"|([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?)/g
      let tm
      while ((tm = tokenRe.exec(args))) {
        if (tm[1] !== undefined) add(tm[1], site)
        else {
          const via = projectionVia(tm[2], varTypes)
          for (const value of resolveSimple(tm[2], locals, varTypes)) add(value, site, via)
        }
      }
      pubRe.lastIndex = j
    }

    const evRe = /(?:[A-Za-z_]\w*\.)?Event\{/g
    let em
    while ((em = evRe.exec(unit.src))) {
      let depth = 1
      let j = evRe.lastIndex
      while (j < unit.src.length && depth > 0) {
        if (unit.src[j] === '{') depth++
        else if (unit.src[j] === '}') depth--
        j++
      }
      const body = unit.src.slice(evRe.lastIndex, j - 1)
      const tm = /\bType:\s*([^,\n}]+)/.exec(body)
      if (tm) {
        const caller = enclosing(unit, em.index)
        const locals = caller ? localsOf(caller) : new Map()
        const varTypes = caller ? varTypesOf(caller) : new Map()
        const via = projectionVia(tm[1], varTypes)
        for (const value of resolveSimple(tm[1], locals, varTypes)) {
          add(value, unit.rel + ':' + lineOf(unit.src, em.index), via)
        }
      }
      evRe.lastIndex = j
    }

    const constRe = /\b(Event[A-Za-z0-9_]*)\s*(?::=|=)\s*"([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)"/g
    let cm
    while ((cm = constRe.exec(unit.src))) add(cm[2], unit.rel + ':' + lineOf(unit.src, cm.index))

    const rangeRe = /for\s+_,\s+(\w+)\s*:=\s*range\s+\[\]string\{/g
    let rm
    while ((rm = rangeRe.exec(unit.src))) {
      let depth = 1
      let j = rangeRe.lastIndex
      while (j < unit.src.length && depth > 0) {
        if (unit.src[j] === '{') depth++
        else if (unit.src[j] === '}') depth--
        j++
      }
      const body = unit.src.slice(rangeRe.lastIndex, j - 1)
      const tail = unit.src.slice(j, j + 3000)
      const usedByPublish = new RegExp('\\b(?:publish[A-Za-z_]*|Publish[A-Za-z_]*)\\s*\\([^)]*\\b' + rm[1] + '\\b').test(tail)
      if (usedByPublish || /event/i.test(rm[1])) {
        const litRe = /"([^"\\]*(?:\\.[^"\\]*)*)"/g
        let lm
        while ((lm = litRe.exec(body))) add(lm[1], unit.rel + ':' + lineOf(unit.src, rm.index))
      }
      rangeRe.lastIndex = j
    }
  }

  return [...found.entries()]
    .map(([type, detail]) => (detail.via ? { type, evidence: detail.evidence, via: detail.via } : { type, evidence: detail.evidence }))
    .sort((a, b) => a.type.localeCompare(b.type))
}

// ---------------------------------------------------------------------------
// inventory render
// ---------------------------------------------------------------------------

function renderInventory({ constructors, settings, routeScan, events }) {
  const currentZero = constructors.filter((c) => c.prodRefs === 0 && !c.testutil)
  const zero = [...currentZero, ...DISPOSED_CONSTRUCTORS]
  const noReaders = settings.filter((s) => s.readers === 0)
  const business = routeScan.noFrontend.filter((r) => r.category === 'business')
  const others = routeScan.noFrontend.filter((r) => r.category !== 'business')

  const lines = []
  lines.push('# W1-1 inventory (HEAD baseline)')
  lines.push('')
  lines.push('Generated by `node scripts/v022-w1-inventory.cjs`. Counts are the W1 acceptance baseline.')
  lines.push('')
  lines.push('## Summary')
  lines.push('')
  lines.push('| group | count |')
  lines.push('|---|---:|')
  lines.push(`| constructors scanned | ${constructors.length} |`)
  lines.push(`| constructors with zero production references | ${zero.length} |`)
  lines.push(`| settings keys | ${settings.length} |`)
  lines.push(`| settings without production reader | ${noReaders.length} |`)
  lines.push(`| mux routes scanned | ${routeScan.registered.length} |`)
  lines.push(`| business API-only routes | ${business.length} |`)
  lines.push(`| non-business routes without frontend call | ${others.length} |`)
  lines.push(`| publishable event types | ${events.length} |`)
  lines.push('')

  lines.push('## Constructors')
  lines.push('')
  lines.push('| constructor | file:line | production refs | test refs | testutil | disposition |')
  lines.push('|---|---|---:|---:|:---:|---|')
  for (const c of constructors) {
    const disposition = c.testutil
      ? 'testutil whitelist'
      : c.prodRefs > 0
        ? 'wired'
        : c.testRefs > 0
          ? 'test-only: move into same-package `_test.go` helper'
          : 'delete (no references)'
    lines.push(
      `| \`${c.name}\` | \`${c.file}:${c.line}\` | ${c.prodRefs} | ${c.testRefs} | ${c.testutil ? 'yes' : 'no'} | ${disposition} |`,
    )
  }
  lines.push('')
  lines.push('### Disposed zero-production-reference constructors (W1-1 baseline `02bce00d`)')
  lines.push('')
  lines.push('| constructor | file:line | test refs | disposition | reason |')
  lines.push('|---|---|---:|---|---|')
  for (const c of DISPOSED_CONSTRUCTORS) {
    lines.push(`| \`${c.name}\` | \`${c.file}:${c.line}\` | ${c.testRefs} | ${c.disposition} | ${c.reason} |`)
  }
  lines.push('')
  lines.push('## Settings without production reader')
  lines.push('')
  lines.push('| key | reader sites |')
  lines.push('|---|---:|')
  for (const s of noReaders) lines.push(`| \`${s.key}\` | ${s.readers} |`)
  lines.push('')
  lines.push('## Business API-only routes (no production frontend call)')
  lines.push('')
  lines.push('| route | registration |')
  lines.push('|---|---|')
  for (const r of business) lines.push(`| \`${r.method} ${r.path}\` | \`${r.file}:${r.line}\` |`)
  lines.push('')
  lines.push('## Non-business routes without frontend call')
  lines.push('')
  lines.push('| route | category | reason | registration |')
  lines.push('|---|---|---|---|')
  for (const r of others) lines.push(`| \`${r.method} ${r.path}\` | ${r.category} | ${r.reason} | \`${r.file}:${r.line}\` |`)
  lines.push('')
  lines.push('## Publishable event types')
  lines.push('')
  lines.push('| event type | first publish site | resolution |')
  lines.push('|---|---|---|')
  for (const e of events) {
    const resolution = e.via ? `struct field \`${e.via}\`` : 'direct publish/Append argument'
    lines.push(`| \`${e.type}\` | \`${e.evidence}\` | ${resolution} |`)
  }
  lines.push('')
  lines.push('### Struct-field projection resolution')
  lines.push('')
  const projected = events.filter((e) => e.via)
  if (projected.length === 0) {
    lines.push('No event type is reached through a struct-field projection.')
  } else {
    lines.push(
      'These event types are carried by a value projected into a struct field and read at the publish site, not passed as a literal publish argument:',
    )
    lines.push('')
    for (const e of projected) lines.push(`- \`${e.type}\` -> \`${e.via}\` -> \`${e.evidence}\``)
    lines.push('')
    lines.push(
      '`issue.updated` chain: package constant `issueEventUpdated = "issue.updated"` (`backend/internal/delivery/application/issue_service.go:19`) -> `issueTransition{eventType: issueEventUpdated}` (`issue_service.go:264`, `issue_service.go:279`) -> `spec.eventType` (`issue_service.go:387`) -> `publishIssueEvent` -> `s.events.Publish` (`issue_service.go:670-674`). The scanner resolves this from struct definitions and literals; no event name is hard-coded as an exception.',
    )
  }
  lines.push('')
  lines.push('### Event scan exclusions')
  lines.push('')
  lines.push('Only non-event string values reached by the publish/Append scan are excluded. Each exclusion needs `file:line` evidence:')
  lines.push('')
  lines.push('| excluded value | reason and file:line evidence |')
  lines.push('|---|---|')
  for (const [type, reason] of NON_EVENT_TYPES) lines.push(`| \`${type}\` | ${reason} |`)
  lines.push('')
  lines.push('## Notes')
  lines.push('')
  lines.push('- Constructors: `func New[A-Z]...` in `backend/internal/**` non-test files; production references are import-aware and exclude comments and the definition line. The only whitelist is `backend/internal/testutil/**`.')
  lines.push('- Settings: keys from `backend/internal/settings/application/catalog.go`; readers are string-literal uses in production Go files outside the catalog and `_test.go`.')
  lines.push('- Routes: `mux.Handle`/`mux.HandleFunc` registrations; a route is frontend-used only by a production `frontend/src` call with the same method and a resolved path helper. Duplicate/legacy registrations that share a handler are treated as one reachable handler.')
  lines.push('- Events: publishable types found from `Event{Type:...}`, `publish*`/`Publish`/`Append` arguments, constants, same-package literals, function event-type parameters followed to call sites, struct definitions/literals for field projection, and `for range []string{...}`. Only evidenced non-event strings are excluded (`NON_EVENT_TYPES`).')
  lines.push('')
  return { markdown: lines.join('\n'), zero, currentZero, noReaders, business, others }
}

function main() {
  const constructors = scanConstructors()
  const settings = scanSettings()
  const routeScan = scanRoutes()
  const events = scanPublishedEvents()
  const rendered = renderInventory({ constructors, settings, routeScan, events })

  fs.mkdirSync(EVIDENCE_DIR, { recursive: true })
  fs.writeFileSync(path.join(EVIDENCE_DIR, 'inventory.md'), rendered.markdown + '\n')

  const json = {
    generated_at: new Date().toISOString(),
    baseline: '02bce00d',
    counts: {
      constructors: constructors.length,
      constructor_zero_prod_refs: rendered.zero.length,
      constructor_zero_prod_refs_current: rendered.currentZero.length,
      settings: settings.length,
      settings_without_reader: rendered.noReaders.length,
      routes: routeScan.registered.length,
      business_api_only: rendered.business.length,
      non_business_no_frontend: rendered.others.length,
      publishable_events: events.length,
    },
    constructors: constructors.map((c) => ({
      name: c.name,
      file: c.file,
      line: c.line,
      package: c.packageName,
      testutil: c.testutil,
      prodRefs: c.prodRefs,
      testRefs: c.testRefs,
      disposition: c.testutil
        ? 'testutil'
        : c.prodRefs > 0
          ? 'wired'
          : c.testRefs > 0
            ? 'test-only'
            : 'delete',
    })),
    disposed_constructors: DISPOSED_CONSTRUCTORS,
    settings_without_reader: rendered.noReaders.map((s) => s.key),
    business_api_only: rendered.business.map((r) => ({ method: r.method, path: r.path, file: r.file, line: r.line })),
    non_business_no_frontend: rendered.others.map((r) => ({ method: r.method, path: r.path, category: r.category, reason: r.reason })),
    publishable_events: events,
    non_event_exclusions: [...NON_EVENT_TYPES.entries()].map(([type, reason]) => ({ type, reason })),
  }
  fs.writeFileSync(path.join(EVIDENCE_DIR, 'inventory.json'), JSON.stringify(json, null, 2) + '\n')

  console.log('W1-1 inventory')
  console.log(
    `  constructors: ${constructors.length} scanned, ${rendered.zero.length} zero production refs (baseline disposed; ${rendered.currentZero.length} remaining)`,
  )
  console.log(`  settings: ${settings.length} keys, ${rendered.noReaders.length} without production reader`)
  console.log(`  routes: ${routeScan.registered.length} scanned, ${rendered.business.length} business API-only, ${rendered.others.length} non-business without frontend call`)
  console.log(`  events: ${events.length} publishable types`)
  console.log(`  evidence: ${rel(EVIDENCE_DIR)}/inventory.md, inventory.json`)
}

module.exports = { scanConstructors, scanPublishedEvents, scanSettings, scanRoutes }

if (require.main === module) main()
