package agentapp

// Retired seed prompt body registered by v0.2.4-C T19 (skill progressive
// disclosure). prompt_seed.go and prompt_seed_legacy_test.go are byte-exact
// historical fixtures (B invariant 9) and are NEVER modified; the append-only
// allow-list is extended here (prompt_seed_retired.go precedent).
//
// The seed-5 era case_executor prompt body (T14 capability-domain rewrite,
// shipped at ProfileSeedVersion 4/5) does not name skill.read. T19 adds the
// progressive-disclosure line to the seed prompt and bumps ProfileSeedVersion
// to 6; registering the pre-T19 body here lets the frozen upgrade policy
// (backfillSeedPrompt / isLegacySeedPrompt) replace it on rows whose latest
// revision is still the untouched seed body. Administrator edits are never
// overwritten (source=admin/reset always wins).
//
// The digest is the normalized (LF) sha256 of the exact pre-T19 body (git
// 9e1f9cfe). skill_context_test.go pins it against the reconstructed body so a
// drift in either fails loudly.
const retiredSeed5CaseExecutorPromptHash = "2542f266860736b2c02b663e27d6510a8a9001e9c62f001ff80c01249e5b7201"

func init() {
	legacySeedPromptHashes[ProfileKeyCaseExecutor] = append(
		legacySeedPromptHashes[ProfileKeyCaseExecutor],
		retiredSeed5CaseExecutorPromptHash,
	)
}
