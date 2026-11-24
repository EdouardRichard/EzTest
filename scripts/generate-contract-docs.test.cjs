#!/usr/bin/env node
/*
 * Tests for the contract-docs event scanner (v0.2.4-H gapfix, R1).
 *
 * These tests build a minimal `backend/internal/<ctx>/application/x.go` tree in
 * a temp directory and call the exported scanner directly, so they do not
 * depend on the real repository's Go sources.
 *
 * Covered regressions:
 *   1. `for _, t := range []string{...}` + `Publish(..., Event{Type: t})` must
 *      attribute every literal element as an event type.
 *   2. A range loop whose element source is not a string-literal slice must be
 *      unresolved (variable / function return — never guessed).
 *   3. An `Event{Type: someVar}` whose variable cannot be resolved must be
 *      unresolved even when a same-named package constant exists (the variable
 *      shadows the constant) — the historical silent-drop defect.
 *   4. Non-vacuity: a direct string-literal publish site must keep being
 *      attributed (guards against breaking the existing path).
 *
 * Cross-context WorkType* consistency (v0.2.4-H gapfix, R2 second half):
 *   5. Same-named copies of the authority's WorkType* constants must carry the
 *      exact authority value (verbatim).
 *   6. A drifted copy value fails and names the file + constant.
 *   7. A copy-only WorkType* name (not declared by the authority) fails.
 *   8. An authority-only WorkType* name is allowed: copies validate only the
 *      types they declare; the authority may add types without copies following.
 *   9. Zero derived authority constants still fails the non-vacuous guard.
 *  10. A WorkType* constant in any context's application dir OUTSIDE the known
 *      copy list fails as an unregistered copy context; a context directory
 *      without WorkType* constants is fine.
 *  11. Single-line `const WorkTypeX = "..."` and typed `WorkTypeX WorkType = "..."`
 *      declarations are scanned.
 *  12. A missing authority directory yields clean violations, not a crash.
 */
'use strict'

const { test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

const {
  scanPublishedEvents,
  scanTaskTypes,
  verifyWorkTypeCopies,
} = require('./generate-contract-docs.cjs')

const GO_PREFIX = `package application

import "context"

type Event struct {
\tChannel string
\tType    string
}

type sink struct{}

func (s *sink) Publish(ctx context.Context, e Event) error { return nil }
`

function makeTree(files) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'eztest-contract-docs-'))
  for (const [rel, content] of Object.entries(files)) {
    const full = path.join(root, rel)
    fs.mkdirSync(path.dirname(full), { recursive: true })
    fs.writeFileSync(full, content, 'utf8')
  }
  return root
}

function scanFixture(files) {
  const root = makeTree(files)
  try {
    return scanPublishedEvents(root)
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
}

function eventTypes(result) {
  return result.events.map((e) => e.type).sort()
}

test('range over a string-literal slice attributes each element', () => {
  const result = scanFixture({
    'backend/internal/demo/application/x.go':
      GO_PREFIX +
      `
func emit(ctx context.Context, p *sink) {
\tfor _, t := range []string{"a.one", "a.two"} {
\t\tp.Publish(ctx, Event{Type: t})
\t}
}
`,
  })

  assert.deepEqual(eventTypes(result), ['a.one', 'a.two'])
  assert.deepEqual(result.unresolved, [])
})

test('range over a function return is unresolved (not guessed)', () => {
  const result = scanFixture({
    'backend/internal/demo/application/x.go':
      GO_PREFIX +
      `
func eventTypes() []string { return []string{"a.one"} }

func emit(ctx context.Context, p *sink) {
\tfor _, t := range eventTypes() {
\t\tp.Publish(ctx, Event{Type: t})
\t}
}
`,
  })

  assert.deepEqual(eventTypes(result), [])
  assert.equal(result.unresolved.length, 1)
  assert.match(result.unresolved[0].expr, /Type: t/)
})

test('unresolvable Event{Type: someVar} is unresolved even when a same-named constant exists', () => {
  const result = scanFixture({
    'backend/internal/demo/application/x.go':
      GO_PREFIX +
      `
const someVar = "fake.event"

func load() string { return "a.real" }

func emit(ctx context.Context, p *sink) {
\tsomeVar := load()
\tp.Publish(ctx, Event{Type: someVar})
}
`,
  })

  assert.deepEqual(eventTypes(result), [], 'a local variable must shadow the package constant')
  assert.equal(result.unresolved.length, 1)
  assert.match(result.unresolved[0].expr, /Type: someVar/)
})

test('a loop variable outside the Type field is not attributed', () => {
  const result = scanFixture({
    'backend/internal/demo/application/x.go':
      GO_PREFIX +
      `
func emit(ctx context.Context, p *sink) {
\tfor _, t := range []string{"a.one"} {
\t\tp.Publish(ctx, Event{Channel: t, Type: "fixed.event"})
\t}
}
`,
  })

  assert.deepEqual(eventTypes(result), ['fixed.event'])
  assert.deepEqual(result.unresolved, [])
})

test('a reassigned loop variable is unresolved (binding no longer provable)', () => {
  const result = scanFixture({
    'backend/internal/demo/application/x.go':
      GO_PREFIX +
      `
func load() string { return "a.real" }

func emit(ctx context.Context, p *sink) {
\tfor _, t := range []string{"a.one", "a.two"} {
\t\tt = load()
\t\tp.Publish(ctx, Event{Type: t})
\t}
}
`,
  })

  assert.deepEqual(eventTypes(result), [])
  assert.equal(result.unresolved.length, 1)
  assert.match(result.unresolved[0].expr, /Type: t/)
})

test('direct string-literal publish is still attributed (non-vacuous)', () => {
  const result = scanFixture({
    'backend/internal/demo/application/x.go':
      GO_PREFIX +
      `
func emit(ctx context.Context, p *sink) {
\tp.Publish(ctx, Event{Type: "x.y"})
}
`,
  })

  assert.deepEqual(eventTypes(result), ['x.y'])
  assert.deepEqual(result.unresolved, [])
})

// ---------------------------------------------------------------------------
// cross-context WorkType* consistency
// ---------------------------------------------------------------------------

const AUTHORITY_CONSTS = [
  'WorkTypeChatTurn          = "chat.turn"',
  'WorkTypeExecuteCase       = "execute.case"',
  'WorkTypeAnalyzeFailure    = "analyze.failure"',
  'WorkTypeAnalyzeCoverage   = "analyze.coverage"',
  'WorkTypeIndexRepo         = "index.repo"',
  'WorkTypeCodeAnalyze       = "code.analyze"',
]

const EXECUTION_CONSTS = [
  'WorkTypeExecuteCase     = "execute.case"',
  'WorkTypeAnalyzeFailure  = "analyze.failure"',
  'WorkTypeAnalyzeCoverage = "analyze.coverage"',
]

const CODEINTEL_CONSTS = [
  'WorkTypeIndexRepo   = "index.repo"',
  'WorkTypeCodeAnalyze = "code.analyze"',
]

function constBlockFile(lines) {
  if (lines.length === 0) return 'package application\n'
  return 'package application\n\nconst (\n' + lines.map((l) => '\t' + l).join('\n') + '\n)\n'
}

function workTypeFixture({
  authority = AUTHORITY_CONSTS,
  execution = EXECUTION_CONSTS,
  codeintel = CODEINTEL_CONSTS,
} = {}) {
  return {
    'backend/internal/agent/application/ports_agent.go': constBlockFile(authority),
    'backend/internal/execution/application/scheduler.go': constBlockFile(execution),
    'backend/internal/codeintel/application/index_service.go': constBlockFile(codeintel),
  }
}

function inspectWorkTypes(files) {
  const root = makeTree(files)
  try {
    return {
      verification: verifyWorkTypeCopies(root),
      taskTypes: scanTaskTypes(root),
    }
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
}

test('same-named WorkType* copies match the authority', () => {
  const { verification, taskTypes } = inspectWorkTypes(workTypeFixture())

  assert.deepEqual(verification.violations, [])
  assert.equal(verification.authority.length, AUTHORITY_CONSTS.length)
  assert.equal(taskTypes.length, verification.authority.length)
})

test('a drifted copy value fails and names the file + constant', () => {
  const execution = EXECUTION_CONSTS.map((line) =>
    line.includes('WorkTypeExecuteCase') ? 'WorkTypeExecuteCase = "execute.casex"' : line,
  )
  const { verification } = inspectWorkTypes(workTypeFixture({ execution }))

  assert.equal(verification.violations.length, 1)
  const message = verification.violations[0]
  assert.match(message, /scheduler\.go/)
  assert.match(message, /WorkTypeExecuteCase/)
  assert.match(message, /execute\.casex/)
  assert.match(message, /execute\.case"/)
})

test('a copy-only WorkType* name fails (unregistered work type)', () => {
  const codeintel = [...CODEINTEL_CONSTS, 'WorkTypeGhost = "ghost.run"']
  const { verification } = inspectWorkTypes(workTypeFixture({ codeintel }))

  assert.equal(verification.violations.length, 1)
  const message = verification.violations[0]
  assert.match(message, /index_service\.go/)
  assert.match(message, /WorkTypeGhost/)
  assert.match(message, /ghost\.run/)
  assert.match(message, /authority/)
})

test('an authority-only WorkType* name is allowed (copies validate declared types only)', () => {
  const authority = [...AUTHORITY_CONSTS, 'WorkTypeBrandNew = "brand.new"']
  const { verification, taskTypes } = inspectWorkTypes(workTypeFixture({ authority }))

  assert.deepEqual(verification.violations, [])
  assert.equal(taskTypes.length, AUTHORITY_CONSTS.length + 1)
})

test('zero derived authority WorkType* constants fails the non-vacuous guard', () => {
  const { verification, taskTypes } = inspectWorkTypes(workTypeFixture({ authority: [] }))

  assert.equal(taskTypes.length, 0)
  assert.ok(
    verification.violations.some((v) => /derived 0 WorkType\* constants/.test(v)),
    'expected an explicit empty-authority violation: ' + JSON.stringify(verification.violations),
  )
})

test('a WorkType* constant in an unregistered context fails', () => {
  const files = workTypeFixture()
  files['backend/internal/newctx/application/service.go'] =
    'package application\n\nconst WorkTypeGhost = "ghost.run"\n'

  const { verification } = inspectWorkTypes(files)

  assert.equal(verification.violations.length, 1)
  const message = verification.violations[0]
  assert.match(message, /^unregistered WorkType copy context: backend\/internal\/newctx\/application/)
  assert.match(message, /WorkTypeGhost/)
})

test('an unregistered context directory without WorkType* constants is allowed', () => {
  const files = workTypeFixture()
  files['backend/internal/otherctx/application/service.go'] =
    'package application\n\nconst OtherThing = "x.y"\n'

  const { verification } = inspectWorkTypes(files)

  assert.deepEqual(verification.violations, [])
})

test('single-line and typed WorkType declarations are scanned', () => {
  const files = workTypeFixture()
  files['backend/internal/agent/application/ports_agent.go'] =
    'package application\n\ntype WorkType string\n\nconst WorkTypeInline = "inline.type"\n\nconst WorkTypeTyped WorkType = "typed.type"\n'
  files['backend/internal/execution/application/scheduler.go'] =
    'package application\n\nconst WorkTypeInline = "inline.type"\n'
  files['backend/internal/codeintel/application/index_service.go'] = 'package application\n'

  const { verification, taskTypes } = inspectWorkTypes(files)

  assert.deepEqual(verification.violations, [])
  assert.deepEqual(
    taskTypes.map((t) => t.value),
    ['inline.type', 'typed.type'],
  )
})

test('a missing authority directory yields clean violations, not a crash', () => {
  const files = workTypeFixture()
  delete files['backend/internal/agent/application/ports_agent.go']

  const { verification, taskTypes } = inspectWorkTypes(files)

  assert.equal(taskTypes.length, 0)
  assert.ok(
    verification.violations.some((v) => /derived 0 WorkType\* constants/.test(v)),
    'expected an explicit empty-authority violation: ' + JSON.stringify(verification.violations),
  )
})
