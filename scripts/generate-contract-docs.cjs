#!/usr/bin/env node
/*
 * Contract-docs generator / staleness gate (v0.2.4-H T20, R-H3r).
 *
 * Generates two committed contract docs from their Go single sources
 * (constitution art.5: derive, never hand-maintain a second truth):
 *
 *   - docs/contracts/events.md      — event types derived from `events.Publish(...)`
 *                                      call sites and event-type constants across
 *                                      backend/internal (non-test).
 *   - docs/contracts/task-types.md  — task types derived from the `WorkType*`
 *                                      string constants in
 *                                      backend/internal/agent/application.
 *     The agent package is the authority. The execution and codeintel contexts
 *     redeclare a subset of the same names; their copies are scanned and must
 *     carry the exact authority value, and a copy-only name fails. Every other
 *     `backend/internal/<context>/application` directory is scanned too: any
 *     WorkType* constant there is an unregistered copy context and fails (a
 *     future context cannot escape the gate). The gate fails closed (an
 *     authority-only name is allowed — copies validate only the types they
 *     declare). Constants are not moved by this generator.
 *
 *   node scripts/generate-contract-docs.cjs          # check (non-zero exit on drift)
 *   node scripts/generate-contract-docs.cjs --write  # regenerate the committed docs
 *
 * This is the standalone generator (no dependency on any historical v022 script).
 * It mirrors the repository's generator convention (scripts/generate-capability-list.cjs /
 * scripts/generate-evidence-kinds.cjs: default = check, `--write` rewrites) and is
 * appended to the scripts/check-docs.ps1 guard list so the docs cannot silently
 * drift (method discipline ⑪: in git AND invoked AND blocking).
 *
 * The event scanner is a compact port of the proven scan rules in the historical
 * inventory script (string-literal / constant / struct-field projection / helper
 * parameter following). It also attributes `for _, t := range []string{...}`
 * loops: when the published `Event{Type: ...}` is exactly the loop variable and
 * that variable is not reassigned in the loop body, every literal element is an
 * event type. Loop variables shadow same-named package constants (and local
 * declarations shadow package constants in general), so a name collision cannot
 * fabricate an attribution.
 *
 * Fail closed: every `.Publish(...)` call site is either attributed or listed in
 * the "未静态派生的发布点" inventory (file:line + truncated arguments). Publish
 * sites whose event type cannot be resolved are never silently dropped.
 *
 * Resolves the repo root from its own location (cwd-independent). Exits 0 when the
 * committed docs equal the derived content (or after writing); 1 on drift.
 */
'use strict'

const fs = require('node:fs')
const path = require('node:path')

const ROOT = path.resolve(__dirname, '..')
const WRITE = process.argv.includes('--write')

const EVENTS_REL = 'docs/contracts/events.md'
const TASK_TYPES_REL = 'docs/contracts/task-types.md'

const EVENT_TYPE_RE = /^[a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+$/

// Event-looking names that are NOT publishable event types. Each exclusion is a
// work-item kind (or like) that the scanner reaches through a struct Type field
// projection but that is never a persisted event type. Keeping this list
// explicit (with a reason) beats target-fitting the regex.
const NON_EVENT_TYPES = new Map([
  [
    'chat.turn',
    'work item kind, not an event type: WorkTypeChatTurn = "chat.turn" in ' +
      'backend/internal/agent/application/ports_agent.go; reached via a work-item ' +
      'Type field projection, never assigned to a persisted event.',
  ],
])

// ---------------------------------------------------------------------------
// generic Go source helpers (comment stripping, file walking, line lookup)
// ---------------------------------------------------------------------------

function lineOf(src, index) {
  let line = 1
  for (let i = 0; i < index && i < src.length; i++) {
    if (src[i] === '\n') line++
  }
  return line
}

// Strip // and /* */ comments while preserving newlines and the byte offsets of
// everything else (so line numbers stay accurate). String/char/raw literals are
// preserved verbatim — the scanners read literal values from this text.
function stripGoComments(src) {
  let out = ''
  let i = 0
  const n = src.length
  let state = 'code' // code | line | block | sq | dq | raw
  while (i < n) {
    const ch = src[i]
    const next = src[i + 1]
    if (state === 'code') {
      if (ch === '/' && next === '/') {
        state = 'line'
        i += 2
        continue
      }
      if (ch === '/' && next === '*') {
        state = 'block'
        i += 2
        out += '  '
        continue
      }
      if (ch === '"') {
        state = 'dq'
      } else if (ch === "'") {
        state = 'sq'
      } else if (ch === '`') {
        state = 'raw'
      }
      out += ch
      i++
      continue
    }
    if (state === 'line') {
      if (ch === '\n') {
        state = 'code'
        out += ch
      }
      i++
      continue
    }
    if (state === 'block') {
      if (ch === '*' && next === '/') {
        state = 'code'
        i += 2
        out += '  '
        continue
      }
      out += ch === '\n' ? '\n' : ' '
      i++
      continue
    }
    // inside a string/char/raw literal
    out += ch
    if (state === 'dq' && ch === '\\') {
      out += next === undefined ? '' : next
      i += 2
      continue
    }
    if (state === 'sq' && ch === '\\') {
      out += next === undefined ? '' : next
      i += 2
      continue
    }
    if (state === 'dq' && ch === '"') state = 'code'
    else if (state === 'sq' && ch === "'") state = 'code'
    else if (state === 'raw' && ch === '`') state = 'code'
    i++
  }
  return out
}

function walkGoFiles(dir, acc) {
  let entries
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true })
  } catch {
    return acc
  }
  // Byte-order sort for locale/filesystem-independent traversal: the "first
  // publish point" evidence column must be deterministic across machines.
  entries.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
  for (const e of entries) {
    const full = path.join(dir, e.name)
    if (e.isDirectory()) {
      walkGoFiles(full, acc)
    } else if (e.isFile() && e.name.endsWith('.go')) {
      acc.push(full)
    }
  }
  return acc
}

// ---------------------------------------------------------------------------
// event scan (port of the proven v022 inventory rules)
// ---------------------------------------------------------------------------

function scanPublishedEvents(root) {
  const internal = path.join(root, 'backend', 'internal')
  const units = walkGoFiles(internal, [])
    .filter((f) => !f.endsWith('_test.go'))
    .map((file) => ({
      file,
      rel: path.relative(root, file).split(path.sep).join('/'),
      dir: path.dirname(file),
      src: stripGoComments(fs.readFileSync(file, 'utf8')),
    }))

  // Per-directory and global maps of identifier -> "a.b.c" string value.
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

  // Struct definitions: type name -> field names.
  const structTypes = new Map()
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

  // -------------------------------------------------------------------------
  // range-loop analysis
  //
  // The scanner only attributes a loop variable when it can prove the binding:
  // `for _, t := range []string{"a", "b"} { ... Event{Type: t} ... }`. Every
  // loop variable is nevertheless registered as a local shadow, so an unrelated
  // package constant with the same name cannot fabricate an event type.
  // -------------------------------------------------------------------------

  // Skip a Go string / char / raw literal starting at `i`; returns the index
  // after the closing quote (or src.length when unterminated).
  function skipQuoted(src, i) {
    const quote = src[i]
    let j = i + 1
    while (j < src.length) {
      if (quote !== '`' && src[j] === '\\') {
        j += 2
        continue
      }
      if (src[j] === quote) return j + 1
      j++
    }
    return j
  }

  // Find the `{` that opens a loop body, starting right after `range`. Braces
  // belonging to composite literals in the range expression (`[]string{...}`,
  // `map[K]V{...}`) are skipped; the next top-level `{` is the body.
  function findLoopBodyBrace(src, from) {
    let j = from
    while (j < src.length) {
      const ch = src[j]
      if (ch === '"' || ch === "'" || ch === '`') {
        j = skipQuoted(src, j)
        continue
      }
      if (ch === '{') {
        const before = src.slice(Math.max(0, j - 60), j)
        if (
          /\[\][A-Za-z_][\w.]*\s*$/.test(before) ||
          /map\s*\[[^\]]*\]\s*[A-Za-z_][\w.]*\s*$/.test(before)
        ) {
          // Composite literal: skip to its matching close brace.
          let depth = 0
          let k = j
          while (k < src.length) {
            const c = src[k]
            if (c === '"' || c === "'" || c === '`') {
              k = skipQuoted(src, k)
              continue
            }
            if (c === '{') depth++
            else if (c === '}') {
              depth--
              if (depth === 0) {
                k++
                break
              }
            }
            k++
          }
          j = k
          continue
        }
        return j
      }
      j++
    }
    return -1
  }

  function matchingBrace(src, openIndex) {
    let depth = 0
    let j = openIndex
    while (j < src.length) {
      const ch = src[j]
      if (ch === '"' || ch === "'" || ch === '`') {
        j = skipQuoted(src, j)
        continue
      }
      if (ch === '{') depth++
      else if (ch === '}') {
        depth--
        if (depth === 0) return j
      }
      j++
    }
    return -1
  }

  function stringLiteralValue(expr) {
    const m = /^"([^"\\]*(?:\\.[^"\\]*)*)"$/.exec(expr.trim())
    return m ? m[1] : null
  }

  // Parse `for ... range ... {` loops in a unit. For each loop we record the
  // variables it shadows and, when provable, the value variable's literal set.
  function parseRangeLoops(unit) {
    const src = unit.src
    const loops = []
    const forRe = /\bfor\b/g
    let m
    while ((m = forRe.exec(src))) {
      const headerStart = m.index
      let rangeAt = -1
      let j = headerStart + 3
      while (j < src.length) {
        const ch = src[j]
        if (ch === '"' || ch === "'" || ch === '`') {
          j = skipQuoted(src, j)
          continue
        }
        if (ch === '{') break
        if (
          src.startsWith('range', j) &&
          !/\w/.test(src[j - 1] || '') &&
          !/\w/.test(src[j + 'range'.length] || '')
        ) {
          rangeAt = j
          break
        }
        j++
      }
      if (rangeAt < 0) continue
      const bodyBrace = findLoopBodyBrace(src, rangeAt + 'range'.length)
      if (bodyBrace < 0) continue
      const bodyEnd = matchingBrace(src, bodyBrace)
      if (bodyEnd < 0) continue

      const header = src.slice(headerStart + 3, rangeAt)
      const assignAt = header.indexOf(':=')
      if (assignAt < 0) continue
      const vars = splitTopLevel(header.slice(0, assignAt))
        .map((v) => v.trim())
        .filter(Boolean)
      if (vars.length === 0 || !vars.every((v) => v === '_' || /^[A-Za-z_]\w*$/.test(v))) continue

      // Go range: one variable is the index; with two variables the second is
      // the value. Only the value variable can carry an event type.
      const valueVar = vars.length >= 2 ? vars[vars.length - 1] : ''
      const shadowVars = vars.filter((v) => v !== '_')
      const body = src.slice(bodyBrace + 1, bodyEnd)

      let values = null
      if (valueVar && valueVar !== '_') {
        const expr = src.slice(rangeAt + 'range'.length, bodyBrace)
        const literal = /^\s*\[\]string\s*\{([\s\S]*)\}\s*$/.exec(expr)
        if (literal) {
          const elements = splitTopLevel(literal[1])
          const parsed = elements.map(stringLiteralValue)
          if (parsed.length > 0 && parsed.every((v) => v !== null)) values = parsed
        }
        // Any assignment/redeclaration of the loop variable makes the binding
        // unprovable — never guess.
        const reassigned = new RegExp(
          '\\b' + valueVar + '\\s*(?::=|[+\\-*/%&|^]?=)(?!=)|\\bvar\\s+' + valueVar + '\\b|\\b' + valueVar + '\\s*(?:\\+\\+|--)',
        ).test(body)
        if (reassigned) values = null
      }

      loops.push({ shadowVars, valueVar, values, bodyStart: bodyBrace, bodyEnd })
    }
    return loops
  }

  const loopsByUnit = new Map()
  function loopsOf(unit) {
    if (!loopsByUnit.has(unit)) loopsByUnit.set(unit, parseRangeLoops(unit))
    return loopsByUnit.get(unit)
  }

  // Locals visible at `index`, including enclosing loop variables. Loop
  // variables shadow same-named declarations/constants; an unprovable binding
  // is shadowed with no value (fail closed, never fall back to a global const).
  function scopeAt(unit, index, caller) {
    const locals = caller ? localsOf(caller) : new Map()
    const shadowed = new Set()
    const loopVars = new Set()
    const enclosingLoops = loopsOf(unit)
      .filter((loop) => loop.bodyStart <= index && index <= loop.bodyEnd)
      .sort((a, b) => a.bodyStart - b.bodyStart || a.bodyEnd - b.bodyEnd)
    for (const loop of enclosingLoops) {
      for (const v of loop.shadowVars) {
        shadowed.add(v)
        loopVars.add(v)
      }
      if (loop.valueVar && loop.valueVar !== '_') {
        shadowed.add(loop.valueVar)
        loopVars.add(loop.valueVar)
        if (loop.values) locals.set(loop.valueVar, loop.values.slice())
        else locals.delete(loop.valueVar)
      }
    }
    return { locals, shadowed, loopVars }
  }

  // Parse functions/methods with bodies so event-type parameters can be followed
  // to call sites (e.g. publishEvent(ctx, eventType, ...)).
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

  function baseTypeName(type) {
    return type.trim().replace(/^\*/, '').replace(/^\[\]/, '')
  }

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

  function resolveSimple(expr, locals, varTypes, shadowed) {
    expr = expr.trim().replace(/^\(|\)$/g, '')
    let m
    if ((m = /^"([^"\\]*(?:\\.[^"\\]*)*)"$/.exec(expr))) return [m[1]]
    if ((m = /^([A-Za-z_]\w*)\.([A-Za-z_]\w*)$/.exec(expr))) {
      const structType = varTypes && varTypes.get(m[1])
      if (structType) {
        const projected = structFieldValues.get(structType + '.' + m[2])
        if (projected && projected.size > 0) return [...projected]
      }
      // A shadowed/unresolved local must not borrow a package constant.
      if (shadowed && shadowed.has(m[1])) return []
      const v = globalConsts.get(m[2])
      return v ? [v] : []
    }
    if ((m = /^([A-Za-z_]\w*)$/.exec(expr))) {
      // Local bindings shadow package constants; an explicitly shadowed name
      // with no proven value resolves to nothing (never guessed).
      if (shadowed && shadowed.has(m[1])) {
        return locals && locals.has(m[1]) ? [...locals.get(m[1])] : []
      }
      if (locals && locals.has(m[1])) return [...locals.get(m[1])]
      return globalConsts.has(m[1]) ? [globalConsts.get(m[1])] : []
    }
    if ((m = /^"([^"]*)"\s*\+\s*(.+)$/.exec(expr))) {
      return resolveSimple(m[2], locals, varTypes, shadowed).map((suffix) => m[1] + suffix)
    }
    return []
  }

  // Local declarations shadow package-level constants (Go scoping). Every
  // assignment/declaration is recorded, not just event/action-shaped names:
  // otherwise a local variable that happens to share a constant's name would be
  // misattributed to that constant.
  function localsOf(fn) {
    const locals = new Map()
    const varTypes = varTypesOf(fn)
    const re = /\b([A-Za-z_]\w*)\s*:?=\s*([^\n;]+)/g
    let m
    while ((m = re.exec(fn.body))) {
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

  // Field values seen in struct literals: "Type.field" -> Set(values).
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
      const scope = scopeAt(unit, m.index, caller)
      const locals = scope.locals
      const vTypes = caller ? varTypesOf(caller) : new Map()
      for (const pair of splitTopLevel(body)) {
        const fm = /^([A-Za-z_]\w*)\s*:\s*([\s\S]+)$/.exec(pair)
        if (!fm) continue
        if (type === 'Event' && fm[1] === 'Type') continue
        for (const value of resolveSimple(fm[2], locals, vTypes, scope.shadowed)) {
          if (!EVENT_TYPE_RE.test(value)) continue
          const key = type + '.' + fm[1]
          if (!structFieldValues.has(key)) structFieldValues.set(key, new Set())
          structFieldValues.get(key).add(value)
        }
      }
      structLitRe.lastIndex = j
    }
  }

  // Function event-type parameter positions (follow param -> call site).
  const fnSpecs = new Map()
  for (const fn of functions) {
    const specs = []
    const typeExprs = []
    const typeRe = /\bType:\s*([^,\n}]+)/g
    let m
    while ((m = typeRe.exec(fn.body))) typeExprs.push(m[1])
    for (const raw of typeExprs) {
      const expr = raw.trim()
      const index = fn.params.indexOf(expr)
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
    const pubRe = /\b(?:[A-Za-z_]\w*\.)?(publish[A-Za-z_]*|Publish[A-Za-z_]*)\s*\(/g
    while ((m = pubRe.exec(fn.body))) {
      let depth = 1
      let j = pubRe.lastIndex
      while (j < fn.body.length && depth > 0) {
        if (fn.body[j] === '(') depth++
        else if (fn.body[j] === ')') depth--
        j++
      }
      const args = fn.body.slice(pubRe.lastIndex, j - 1)
      for (const arg of splitTopLevel(args)) {
        const pi = fn.params.indexOf(arg.trim())
        if (pi >= 0) specs.push({ index: pi, prefix: '' })
      }
      pubRe.lastIndex = j
    }
    if (specs.length > 0) fnSpecs.set(fn.unit.dir + '::' + fn.name, specs)
  }

  const found = new Map() // type -> { evidence:file:line, via }
  function add(type, evidence, via) {
    if (typeof type !== 'string' || !EVENT_TYPE_RE.test(type)) return
    if (NON_EVENT_TYPES.has(type)) return
    if (!found.has(type)) found.set(type, { evidence, via: via || '' })
  }
  const payloadKeys = new Map() // type -> Set(string keys) from the defining Event literal

  // Follow helper event-type params to call sites within the same directory.
  // Function specs are keyed by dir::name to avoid cross-package name collisions.
  // A helper that resolves ≥1 concrete event type via its callers is recorded in
  // resolvedHelpers so its own internal publish site is not miscounted as
  // unresolved (the type is pinned via the callers, not at the forwarding site).
  const resolvedHelpers = new Set()
  for (const fn of functions) {
    const helperKey = fn.unit.dir + '::' + fn.name
    const specs = fnSpecs.get(helperKey)
    if (!specs) continue
    let helperResolved = false
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
        const scope = scopeAt(unit, m.index, caller)
        const locals = scope.locals
        const vTypes = caller ? varTypesOf(caller) : new Map()
        for (const spec of specs) {
          const arg = args[spec.index]
          if (!arg) continue
          const via = projectionVia(arg, vTypes)
          for (const value of resolveSimple(arg, locals, vTypes, scope.shadowed)) {
            const full = spec.prefix + value
            if (EVENT_TYPE_RE.test(full) && !NON_EVENT_TYPES.has(full)) helperResolved = true
            add(full, unit.rel + ':' + lineOf(unit.src, m.index), via)
          }
        }
        re.lastIndex = j
      }
    }
    if (helperResolved) resolvedHelpers.add(helperKey)
  }

  // Direct publish*/Publish call arguments + Event{Type:...} struct literals.
  const publishSites = [] // {file,line,expr,produced,fnKey} — for the unresolved inventory
  for (const unit of units) {
    const pubRe = /\b(?:[A-Za-z_]\w*\.)?(publish[A-Za-z_]*|Publish[A-Za-z_]*)\s*\(/g
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
      // Skip function/method declarations (`func publish(...)`, `func (s S) publish(...)`);
      // those are not call sites.
      const declPre = unit.src.slice(Math.max(0, m.index - 60), m.index)
      if (/func\s+$/.test(declPre) || /func\s*\([^)]*\)\s+$/.test(declPre)) {
        pubRe.lastIndex = j
        continue
      }
      const caller = enclosing(unit, m.index)
      const scope = scopeAt(unit, m.index, caller)
      const locals = scope.locals
      const vTypes = caller ? varTypesOf(caller) : new Map()
      const site = unit.rel + ':' + lineOf(unit.src, m.index)
      // Bare identifiers resolved from an enclosing range loop carry the event
      // type only when they are exactly the value of this call's `Type:` field.
      // The same loop variable in Channel/Payload/... is not an event type.
      const typeValueNames = new Set()
      const typeValueRe = /\bType\s*:\s*([A-Za-z_]\w*)\b/g
      let tvm
      while ((tvm = typeValueRe.exec(args))) typeValueNames.add(tvm[1])
      const tokenRe = /"([^"\\]*(?:\\.[^"\\]*)*)"|([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?)/g
      let tm
      let producedAny = false
      while ((tm = tokenRe.exec(args))) {
        if (tm[1] !== undefined) {
          if (EVENT_TYPE_RE.test(tm[1])) producedAny = true
          add(tm[1], site)
        } else {
          if (
            !tm[2].includes('.') &&
            scope.loopVars.has(tm[2]) &&
            !typeValueNames.has(tm[2])
          ) {
            continue
          }
          const via = projectionVia(tm[2], vTypes)
          for (const value of resolveSimple(tm[2], locals, vTypes, scope.shadowed)) {
            if (EVENT_TYPE_RE.test(value)) producedAny = true
            add(value, site, via)
          }
        }
      }
      publishSites.push({
        file: unit.rel,
        line: lineOf(unit.src, m.index),
        name: m[1],
        expr: args.replace(/\s+/g, ' ').trim().slice(0, 90),
        produced: producedAny,
        fnKey: caller ? caller.unit.dir + '::' + caller.name : '',
      })
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
      let resolvedTypes = []
      if (tm) {
        const caller = enclosing(unit, em.index)
        const scope = scopeAt(unit, em.index, caller)
        const locals = scope.locals
        const vTypes = caller ? varTypesOf(caller) : new Map()
        const via = projectionVia(tm[1], vTypes)
        resolvedTypes = resolveSimple(tm[1], locals, vTypes, scope.shadowed)
        for (const value of resolvedTypes) {
          add(value, unit.rel + ':' + lineOf(unit.src, em.index), via)
        }
      }
      // Payload keys from this literal's `Payload: <expr>{ "k": ... }`.
      const pm = /\bPayload:\s*([A-Za-z_][\w.]*)?\s*\{/.exec(body)
      if (pm && resolvedTypes.length > 0) {
        const mapStart = evRe.lastIndex + pm.index + pm[0].length
        let md = 1
        let mk = mapStart
        while (mk < unit.src.length && md > 0) {
          if (unit.src[mk] === '{') md++
          else if (unit.src[mk] === '}') md--
          mk++
        }
        const mapBody = unit.src.slice(mapStart, mk - 1)
        const keyRe = /"([A-Za-z0-9_]+)"\s*:/g
        let km
        const keys = new Set()
        while ((km = keyRe.exec(mapBody))) keys.add(km[1])
        for (const value of resolvedTypes) {
          if (!EVENT_TYPE_RE.test(value) || NON_EVENT_TYPES.has(value)) continue
          if (!payloadKeys.has(value)) payloadKeys.set(value, new Set())
          for (const kk of keys) payloadKeys.get(value).add(kk)
        }
      }
      evRe.lastIndex = j
    }

    const constRe = /\b(Event[A-Za-z0-9_]*)\s*(?::=|=)\s*"([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)"/g
    let cm
    while ((cm = constRe.exec(unit.src))) add(cm[2], unit.rel + ':' + lineOf(unit.src, cm.index))
  }

  // Struct-field projections that are consumed by a publish call but never
  // defined as an Event{Type:...} literal: fold in the projected values.
  for (const [type, detail] of [...found.entries()]) {
    if (!detail.via) continue
    const projected = structFieldValues.get(detail.via)
    if (projected) for (const value of projected) add(value, detail.evidence, detail.via)
  }

  const events = [...found.entries()]
    .map(([type, detail]) => ({
      type,
      evidence: detail.evidence,
      via: detail.via,
      payload: payloadKeys.has(type) ? [...payloadKeys.get(type)].sort() : [],
    }))
    .sort((a, b) => a.type.localeCompare(b.type))

  // The event-publisher call sites are the calls to the `.Publish(` interface method
  // (capital P). Lowercase `publish*` are internal domain helpers whose event types
  // are built inside them (captured by the Event{Type:...} scan) — not publish points.
  const eventPublishSites = publishSites.filter(
    (s) => s.name === 'Publish' && !/context\.Context/.test(s.expr),
  )
  // Unresolved = an actual `.Publish(` call whose event type cannot be attributed at
  // the site and whose enclosing helper was not followed to a resolvable caller.
  // Listed in events.md and thus staleness-gated (no silent false-negative).
  const unresolved = eventPublishSites
    .filter((s) => !s.produced && !(s.fnKey && resolvedHelpers.has(s.fnKey)))
    .map((s) => ({ site: s.file + ':' + s.line, expr: s.expr }))
    .sort((a, b) => (a.site < b.site ? -1 : a.site > b.site ? 1 : 0))

  return { events, unresolved, publishSiteCount: eventPublishSites.length, unitCount: units.length }
}

// ---------------------------------------------------------------------------
// task-type scan
// ---------------------------------------------------------------------------

function scanTaskTypes(root) {
  const constants = readWorkTypeConstants(root, WORK_TYPE_AUTHORITY_DIR) || []
  const found = new Map() // value -> { name, file }
  for (const c of constants) {
    if (!found.has(c.value)) found.set(c.value, c)
  }
  return [...found.entries()]
    .map(([value, info]) => ({ value, name: info.name, file: info.file }))
    .sort((a, b) => a.value.localeCompare(b.value))
}

// ---------------------------------------------------------------------------
// cross-context WorkType* consistency
//
// The task-types authority is backend/internal/agent/application. A few contexts
// redeclare a subset of the same names. Redeclaring is allowed (this gate does
// not move constants), but copies must never drift silently:
//
//   - a same-named copy must carry the exact authority value (verbatim);
//   - a copy-only name is an unregistered work type and fails;
//   - an authority-only name is allowed: copies validate only the types they
//     declare, so the authority may add a type without copies following;
//   - any OTHER `backend/internal/<context>/application` directory that declares
//     a WorkType* constant is an unregistered copy context and fails — the scan
//     is over all contexts, not just the known list below.
//
// Nothing is guessed or auto-corrected. The gate fails closed: a known copy
// directory that went missing (e.g. an unnoticed context rename) and an empty
// authority derivation are violations, not silent no-ops.
// ---------------------------------------------------------------------------

const WORK_TYPE_AUTHORITY_DIR = 'backend/internal/agent/application'
// Contexts that are expected to redeclare WorkType* constants. Each must exist;
// they are validated value-for-value against the authority. Any other context
// that starts declaring WorkType* constants is rejected as unregistered.
const WORK_TYPE_COPY_DIRS = [
  'backend/internal/execution/application',
  'backend/internal/codeintel/application',
]

// Read every non-test `WorkType* = "a.b"` constant in `dirRel` (relative to
// root). Returns null when the directory itself is missing.
function readWorkTypeConstants(root, dirRel) {
  const dir = path.join(root, dirRel)
  let entries
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true })
  } catch {
    return null
  }
  const files = entries
    .filter((e) => e.isFile() && e.name.endsWith('.go') && !e.name.endsWith('_test.go'))
    .map((e) => e.name)
    .sort()
  const constants = []
  for (const name of files) {
    const file = path.join(dir, name)
    const src = stripGoComments(fs.readFileSync(file, 'utf8'))
    // Matches a bare const-block entry, a single-line `const WorkTypeX = "a.b"`,
    // and a typed `WorkTypeX WorkType = "a.b"`.
    const re =
      /^\s*(?:const\s+)?(WorkType[A-Za-z0-9_]+)(?:\s+WorkType)?\s*=\s*"([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)"\s*$/gm
    let m
    while ((m = re.exec(src))) {
      constants.push({
        name: m[1],
        value: m[2],
        file: path.relative(root, file).split(path.sep).join('/'),
        line: lineOf(src, m.index),
      })
    }
  }
  return constants
}

// List every existing `backend/internal/<context>/application` directory
// (relative path), excluding the authority.
function listApplicationContextDirs(root) {
  const internal = path.join(root, 'backend', 'internal')
  let entries
  try {
    entries = fs.readdirSync(internal, { withFileTypes: true })
  } catch {
    return []
  }
  return entries
    .filter((e) => e.isDirectory())
    .map((e) => 'backend/internal/' + e.name + '/application')
    .filter((rel) => rel !== WORK_TYPE_AUTHORITY_DIR)
    .filter((rel) => fs.existsSync(path.join(root, rel)))
    .sort()
}

function verifyWorkTypeCopies(root) {
  const violations = []
  const authority = readWorkTypeConstants(root, WORK_TYPE_AUTHORITY_DIR) || []
  if (authority.length === 0) {
    violations.push(
      WORK_TYPE_AUTHORITY_DIR + ': derived 0 WorkType* constants — refusing to verify copies against an empty authority',
    )
  }
  const authorityByName = new Map()
  for (const c of authority) {
    if (!authorityByName.has(c.name)) authorityByName.set(c.name, c)
  }

  // The known copy contexts must still exist (fail closed on rename/removal).
  const knownCopyDirs = new Set(WORK_TYPE_COPY_DIRS)
  for (const dirRel of WORK_TYPE_COPY_DIRS) {
    if (!fs.existsSync(path.join(root, dirRel))) {
      violations.push(dirRel + ': expected cross-context WorkType* copy directory not found')
    }
  }

  const copies = []
  // Scan every context/application directory, not just the known list: a future
  // context that starts redeclaring WorkType* must fail loudly instead of
  // silently escaping the gate.
  for (const dirRel of listApplicationContextDirs(root)) {
    const constants = readWorkTypeConstants(root, dirRel) || []
    if (!knownCopyDirs.has(dirRel)) {
      if (constants.length > 0) {
        violations.push(
          'unregistered WorkType copy context: ' +
            dirRel +
            ' (WorkType* constants: ' +
            constants
              .map((c) => c.name)
              .sort()
              .join(', ') +
            ')',
        )
      }
      continue
    }
    for (const c of constants) {
      copies.push(c)
      const authoritative = authorityByName.get(c.name)
      if (!authoritative) {
        violations.push(
          c.file +
            ':' +
            c.line +
            ': copy declares ' +
            c.name +
            ' = "' +
            c.value +
            '" but the authority (' +
            WORK_TYPE_AUTHORITY_DIR +
            ') has no such WorkType* name',
        )
        continue
      }
      if (authoritative.value !== c.value) {
        violations.push(
          c.file +
            ':' +
            c.line +
            ': ' +
            c.name +
            ' = "' +
            c.value +
            '" drifts from authority ' +
            authoritative.file +
            ':' +
            authoritative.line +
            ' value "' +
            authoritative.value +
            '"',
        )
      }
    }
  }

  return { authority, copies, violations }
}

// ---------------------------------------------------------------------------
// render
// ---------------------------------------------------------------------------

function renderEventsMd({ events, unresolved, publishSiteCount, unitCount }) {
  const lines = []
  lines.push('# EzTest 事件契约（从 Go 源码生成）')
  lines.push('')
  lines.push('> **生成产物，请勿手改。** 权威 = Go 发布点（`events.Publish(...)` 调用）+ 事件类型常量（单一事实源，宪法第五条）。')
  lines.push('> 重新生成：`node scripts/generate-contract-docs.cjs --write`；校验：`node scripts/generate-contract-docs.cjs`。')
  lines.push('> 调用链（方法纪律⑪）：`scripts/check-docs.ps1` → `node scripts/generate-contract-docs.cjs`（默认 check，漂移即非零退出）。')
  lines.push('> 事件信封：`eventing.Event{Channel, Seq, Type, Scope, Payload, ActorID, RequestID, CreatedAt}`（`backend/internal/eventing/domain/event.go`）。')
  lines.push('>')
  lines.push(
    '> 派生范围：扫描 `backend/internal/**`（非测试）`' +
      unitCount +
      '` 个 Go 文件中的 `Publish`/`publish*` 调用点（共 ' +
      publishSiteCount +
      ' 个调用站点）与事件常量，键为可静态解析的事件类型；无法静态归属的发布点**具名列于**下文「未静态派生的发布点」一节（该节亦受 staleness 门覆盖，不静默跳过）。',
  )
  lines.push('')
  lines.push('## 事件清单（' + events.length + ' 类）')
  lines.push('')
  lines.push('| 事件类型 | 首个发布点 | 静态 payload 键 |')
  lines.push('|---|---|---|')
  for (const ev of events) {
    const payload = ev.payload.length > 0 ? ev.payload.map((k) => '`' + k + '`').join(', ') : '—'
    lines.push('| `' + ev.type + '` | `' + ev.evidence + '` | ' + payload + ' |')
  }
  lines.push('')
  lines.push('## 未静态派生的发布点（' + unresolved.length + '）')
  lines.push('')
  if (unresolved.length === 0) {
    lines.push('0（全部发布点均可静态派生出事件类型）。')
  } else {
    lines.push('> 扫描到 `.Publish(` 调用，但其事件类型无法在此调用点静态归属（参数为变量 / 函数返回 / 转发 `Event` 值）。')
    lines.push('> 注：按语法同名匹配的少量业务方法（如报告发布的 `Publish`）也会列入——**刻意包含以免漏报**，逐条 `file:line` 可核对。')
    lines.push('> 本节参与 staleness 门：名单新增/减少/变化即漂移，须 `--write` 重生成——不会静默跳过。')
    lines.push('')
    lines.push('| 发布点 | 调用参数（截断） |')
    lines.push('|---|---|')
    for (const u of unresolved) {
      lines.push('| `' + u.site + '` | `' + u.expr.replace(/\|/g, '\\|') + '` |')
    }
  }
  lines.push('')
  return lines.join('\n')
}

function renderTaskTypesMd(taskTypes) {
  const lines = []
  lines.push('# EzTest 任务类型契约（从 Go 源码生成）')
  lines.push('')
  lines.push('> **生成产物，请勿手改。** 权威 = `backend/internal/agent/application` 的 `WorkType*` 字符串常量（单一事实源，宪法第五条）。')
  lines.push('> 重新生成：`node scripts/generate-contract-docs.cjs --write`；校验：`node scripts/generate-contract-docs.cjs`。')
  lines.push('> 调用链（方法纪律⑪）：`scripts/check-docs.ps1` → `node scripts/generate-contract-docs.cjs`（默认 check，漂移即非零退出）。')
  lines.push('')
  lines.push('## 任务类型（' + taskTypes.length + '）')
  lines.push('')
  lines.push('| 任务类型 | 常量 | 源文件 |')
  lines.push('|---|---|---|')
  for (const t of taskTypes) {
    lines.push('| `' + t.value + '` | `' + t.name + '` | `' + t.file + '` |')
  }
  lines.push('')
  return lines.join('\n')
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

function readCommitted(rel) {
  return fs.readFileSync(path.join(ROOT, rel), 'utf8').replace(/\r\n/g, '\n')
}

function main() {
  const scan = scanPublishedEvents(ROOT)
  const taskTypes = scanTaskTypes(ROOT)
  const workTypeCopies = verifyWorkTypeCopies(ROOT)

  // Non-vacuous guards: an empty derivation would make the contract trivially
  // "in sync" and silently useless.
  if (scan.events.length === 0) {
    console.error('generate-contract-docs: derived 0 event types (scan broke?) — refusing')
    process.exit(1)
  }
  if (taskTypes.length === 0) {
    console.error('generate-contract-docs: derived 0 task types (scan broke?) — refusing')
    process.exit(1)
  }
  // Cross-context copies must never drift silently. This fails in both check and
  // --write mode: the generator never "fixes" a drifted copy by regenerating.
  if (workTypeCopies.violations.length > 0) {
    console.error('generate-contract-docs: cross-context WorkType* copy drift:')
    for (const violation of workTypeCopies.violations) {
      console.error('  - ' + violation)
    }
    process.exit(1)
  }

  const eventsMd = renderEventsMd(scan)
  const taskTypesMd = renderTaskTypesMd(taskTypes)

  const outputs = [
    { rel: EVENTS_REL, content: eventsMd },
    { rel: TASK_TYPES_REL, content: taskTypesMd },
  ]

  if (WRITE) {
    for (const out of outputs) {
      fs.mkdirSync(path.dirname(path.join(ROOT, out.rel)), { recursive: true })
      fs.writeFileSync(path.join(ROOT, out.rel), out.content, 'utf8')
      console.log('wrote ' + out.rel)
    }
    console.log(
      'generate-contract-docs: wrote ' +
        scan.events.length +
        ' event types, ' +
        taskTypes.length +
        ' task types',
    )
    return
  }

  let drifted = false
  for (const out of outputs) {
    let committed
    try {
      committed = readCommitted(out.rel)
    } catch {
      console.error('generate-contract-docs: missing committed ' + out.rel + ' — run with --write')
      drifted = true
      continue
    }
    if (committed !== out.content) {
      console.error(
        'generate-contract-docs: drift in ' +
          out.rel +
          ' — committed content != regenerated content; run: node scripts/generate-contract-docs.cjs --write',
      )
      drifted = true
    } else {
      console.log('OK ' + out.rel)
    }
  }
  if (drifted) process.exit(1)
}

if (require.main === module) {
  main()
}

module.exports = {
  scanPublishedEvents,
  scanTaskTypes,
  verifyWorkTypeCopies,
  renderEventsMd,
  renderTaskTypesMd,
}
