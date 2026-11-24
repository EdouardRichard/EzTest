package agentapp

// Retired seed prompt bodies registered by v0.2.4-F T15 (autonomous-loop
// whitelist + prompt consolidation). prompt_seed.go and
// prompt_seed_legacy_test.go are byte-exact historical fixtures (B invariant 9)
// and are NEVER modified; the append-only allow-list is extended here
// (prompt_seed_skills.go precedent).
//
// Two historical case_executor bodies are registered:
//
//   - The seed-6/7 era body (C T19 skill progressive disclosure, before F T12
//     taught the natural-language contract). F T12 changed the seed prompt but
//     did not bump ProfileSeedVersion or register the prior body, so rows
//     reconciled to seed 7 before T12 kept the pre-T12 prompt; registering it
//     here closes that upgrade path together with the T15 bump.
//   - The F T12 body (natural-language contract + data-preparation loop, shipped
//     at ProfileSeedVersion 7 before T15). T15 adds the autonomous-loop guidance
//     (sut.read resolution, evidence-gated sut.register / sut.asset_register,
//     fixture.upload / fixture.download login-state reuse) and bumps
//     ProfileSeedVersion to 8; registering the pre-T15 body lets the frozen
//     upgrade policy (backfillSeedPrompt / isLegacySeedPrompt) replace it on
//     rows whose latest revision is still the untouched seed body.
//
// Administrator edits are never overwritten (source=admin/reset always wins).
// The digests are the normalized (LF) sha256 of the exact historical bodies
// (git 27af7f28 and ea12b0ef); prompt_seed_autonomy_test.go pins them against
// the reconstructed bodies so a drift in either fails loudly.
const retiredSeed7PreT12CaseExecutorPromptHash = "6346cd50d630bd23b48f5f99aad0b62017800bb81c6b4a4f9a4ec561e7609aca"
const retiredSeed7T12CaseExecutorPromptHash = "36c1096f94a0f13325dd07c83c9cf027eb90ebcd66132fc8eb9af97ee5cb3979"

func init() {
	legacySeedPromptHashes[ProfileKeyCaseExecutor] = append(
		legacySeedPromptHashes[ProfileKeyCaseExecutor],
		retiredSeed7PreT12CaseExecutorPromptHash,
		retiredSeed7T12CaseExecutorPromptHash,
	)
}
