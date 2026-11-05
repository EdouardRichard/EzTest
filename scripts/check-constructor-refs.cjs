#!/usr/bin/env node
/**
 * v0.22 W1 constructor guard.
 *
 * Reuses scanConstructors() from scripts/v022-w1-inventory.cjs and fails when a
 * production constructor has zero import-aware production references. The only
 * whitelist is backend/internal/testutil/** (already marked by the scanner).
 */
'use strict'

const { scanConstructors } = require('./v022-w1-inventory.cjs')

const zero = scanConstructors().filter((c) => c.prodRefs === 0 && !c.testutil)
if (zero.length > 0) {
  console.error(
    'zero production-reference constructors:\n' +
      zero.map((c) => `  ${c.name} @ ${c.file}:${c.line} (test refs: ${c.testRefs})`).join('\n'),
  )
  process.exit(1)
}

console.log('constructor guard: OK (no zero production-reference constructor outside testutil)')
