package execution

import (
	"encoding/json"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// WaveScopeSnapshot is the authoritative shape of the frozen effective scope
// recorded once in run_wave.scope_snapshot when a wave is planned (R-C5-1/DB-19,
// v0.2.4-F SC-F7). It is the single source of truth for the document: the planner
// writes it, the wave repository reads it back, and the wave-level fingerprint
// consumer reconstructs the fingerprint inputs from it. Defining the shape here
// (the domain that owns RunWave) keeps those three legs from hand-maintaining
// separate copies of the same contract.
//
// EnvironmentFingerprint and CodeBaseline are the wave-level fingerprint carriers
// (SC-F7): the values computed from the frozen scope at plan time. They are
// derived facts — the wave-level consumer rebuilds them from the frozen SUT inputs
// via the canonical hash functions and cross-checks them against these recorded
// values (fail-closed on divergence), so a recorded carrier can never silently
// drift from the scope it claims to summarize.
type WaveScopeSnapshot struct {
	VersionID              int64          `json:"version_id"`
	FrozenAt               string         `json:"frozen_at"`
	EnvironmentFingerprint string         `json:"environment_fingerprint"`
	CodeBaseline           string         `json:"code_baseline"`
	SUTs                   []WaveScopeSUT `json:"suts"`
}

// WaveScopeSUT is one SUT's frozen effective scope. BaseURL is part of the
// environment fingerprint payload, so it is frozen here too (SC-F7): without it
// the snapshot could not rebuild the fingerprint input. RepositoryBaselines pairs
// every effective repository with its OWN resolved commit (V-0024), so the code
// baseline is likewise rebuildable from the frozen document.
type WaveScopeSUT struct {
	SUTID               int64                         `json:"sut_id"`
	EnvironmentID       int64                         `json:"environment_id"`
	BaseURL             string                        `json:"base_url"`
	PageIDs             []int64                       `json:"page_ids"`
	APIIDs              []int64                       `json:"api_ids"`
	DatabaseIDs         []int64                       `json:"database_ids"`
	AccountRoles        []string                      `json:"account_roles"`
	RepositoryIDs       []int64                       `json:"repository_ids"`
	RepositoryBaselines []WaveScopeRepositoryBaseline `json:"repository_baselines"`
}

// WaveScopeRepositoryBaseline is one frozen (repository, commit) pair of the
// environment code pool. CommitSHA is the resolved 40-hex commit pinned for that
// repository — never a branch name and never another repository's commit.
type WaveScopeRepositoryBaseline struct {
	RepoID    int64  `json:"repo_id"`
	CommitSHA string `json:"commit_sha"`
}

// ParseWaveScopeSnapshot decodes a frozen scope document. An empty or "{}"
// document (a legacy wave planned before the fingerprint carrier existed, or a
// degraded assembly that wrote no scope) yields a zero-value snapshot and no
// error; the wave-level consumer treats a missing carrier as a typed error rather
// than falling back to a live read. A malformed document is a typed 400.
func ParseWaveScopeSnapshot(raw string) (WaveScopeSnapshot, error) {
	var payload WaveScopeSnapshot
	if strings.TrimSpace(raw) == "" {
		return payload, nil
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return WaveScopeSnapshot{}, platform.Invalid("scope_snapshot", "the wave scope snapshot is not a decodable JSON object")
	}
	return payload, nil
}
