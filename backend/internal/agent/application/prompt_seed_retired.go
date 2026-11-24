package agentapp

// Retired seed prompt bodies registered by v0.2.4-C T14 (R-C1 wire rename).
//
// prompt_seed.go and prompt_seed_legacy_test.go are byte-exact historical
// fixtures (B invariant 9) and are NEVER modified. The seed-2 case_executor
// prompt body — the one that still names the retired browser.* atomic actions,
// the lifted password prohibition and read-only SELECT — shipped at
// ProfileSeedVersion 2 but was never registered in legacySeedPromptHashes. T13's
// backfill therefore reconciled the stored whitelist to the capability domains
// while leaving the prompt naming browser.* (now denied by the whitelist → wasted
// turns), and marked the row seed_version=3 so a later pass skipped it.
//
// T14 registers that historical body here so the EXISTING frozen append-only
// upgrade policy (backfillSeedPrompt / isLegacySeedPrompt) recognizes and replaces
// it. This extends the single allow-list rather than duplicating the upgrade
// mechanism: the frozen fixture file is untouched, and the frozen legacy test
// still passes (it pins the four upgradable KEYS and the v0.2.2 bodies, not the
// per-key digest count). ProfileSeedVersion is bumped to 4 in profile_backfill.go
// so rows T13 already marked seed_version=3 are reconciled once more and their
// stale seed-2 prompt is upgraded.
//
// The digest is the normalized (LF) sha256 of the exact historical body, computed
// from the pre-T13 source (git 96d4a00c^). prompt_seed_retired_test.go pins it
// against the reconstructed body so a drift in either fails loudly.
const retiredSeed2CaseExecutorPromptHash = "0e810ea0e6e60e85ae4651904782fa1f38618b09c6353b4b316be4adf3bdeaa7"

func init() {
	legacySeedPromptHashes[ProfileKeyCaseExecutor] = append(
		legacySeedPromptHashes[ProfileKeyCaseExecutor],
		retiredSeed2CaseExecutorPromptHash,
	)
}
