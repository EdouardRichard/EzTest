package executionapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// VersionBinding is the execution context's read projection of one version and
// its SUT bindings. It is produced by the version adapter from the version
// application service; disabled bindings are filtered out there.
//
// Stage is the deterministic version-stage projection (Task 78). Initial
// execution intents may only be refreshed at executing (or completed, where an
// environment change reopens the version), so a case confirmation while the
// version is still at the case gate cannot auto-create a wave.
//
// Status is the version lifecycle projection the adapter fills from the version
// read model. H-A10 verified it has NO production reader (the intent gate reads
// Stage only); its consumers today are the application test fakes, which use it
// as their channel for emulating the version table's completed-version guard.
// It is registered as a write-only projection residual in the H debt ledger
// (task-3 report, H-A10) with a cleanup plan: move the fake's version-status
// state into the fake itself and drop this field. Kept until that cleanup so
// the 65+ task6Binding fixture call sites are not churned inside the H-A10
// literal-parameterization change set.
type VersionBinding struct {
	VersionID int64
	Status    string
	Stage     string
	SUTs      []SUTBinding
}

// SUTBinding is one active SUT binding of the version's environment with its
// resolved fingerprint inputs and code-repository baselines.
//
// v0.2.4-B: the per-version binding row is gone, so SUTID is the only key. It
// used to be carried twice (binding-row id + SUT id) with both legs hashed; the
// dropped leg is why every fingerprint hash value changes with this release
// (GD-05 discards the pre-upgrade intents).
//
// v0.2.4-F (V-0024): the code baseline is the union of the environment's
// repositories, each paired with its OWN resolved commit (RepositoryBaselines).
// The former single CodeRepoID/Branch/Commit triple carried a branch fallback and
// allowed an empty commit; both are gone — a baseline entry must be a resolved
// 40-hex commit. CodeRepoID remains the deterministic lowest-ID baseline
// repository for the wave scope snapshot's legacy single-repository fallback.
type SUTBinding struct {
	SUTID           int64
	EnvironmentID   int64
	SUTName         string
	EnvironmentName string
	BaseURL         string
	PageIDs         []int64
	APIIDs          []int64
	DatabaseIDs     []int64
	AccountRoles    []string
	CodeRepoID      *int64
	// RepositoryIDs is every effective code repository of the binding
	// (versionapp.BindingView.Repositories), deduplicated ascending. It is what
	// the wave scope snapshot freezes as repository_ids.
	RepositoryIDs []int64
	// RepositoryBaselines pairs every effective repository of the environment
	// code pool with its own resolved sut_repository.code_baseline_commit. The
	// code baseline hashes this union; an unresolved or malformed commit is a
	// typed rejection (V-0024), never a branch fallback or a borrowed commit.
	RepositoryBaselines []RepositoryBaseline
}

// RepositoryBaseline is one (repository, resolved commit) pair of the environment
// code pool. Commit is the pinned sut_repository.code_baseline_commit; it must be
// a lowercase 40-hex SHA for the pair to enter the code baseline (V-0024).
type RepositoryBaseline struct {
	RepoID int64
	Commit string
}

// fingerprintPayload is the canonical JSON shape of §5.1. Field order is
// fixed by the struct; slices and bindings are normalized before encoding.
type fingerprintPayload struct {
	SUTs []fingerprintSUT `json:"suts"`
}

type fingerprintSUT struct {
	SUTID         int64    `json:"sut_id"`
	EnvironmentID int64    `json:"environment_id"`
	BaseURL       string   `json:"base_url"`
	PageIDs       []int64  `json:"page_ids"`
	APIIDs        []int64  `json:"api_ids"`
	DatabaseIDs   []int64  `json:"database_ids"`
	AccountRoles  []string `json:"account_roles"`
}

type codeBaselinePayload struct {
	Repositories []codeBaselineRepo `json:"repositories"`
}

// codeBaselineRepo is one resolved (repository, commit) pair of the code baseline
// (§5.5). V-0024 removed the branch-fallback shape: every entry is a resolved
// 40-hex commit, so the canonical JSON is always {repo_id, commit_sha}.
type codeBaselineRepo struct {
	RepoID    int64  `json:"repo_id"`
	CommitSHA string `json:"commit_sha"`
}

// EnvironmentFingerprint computes the deterministic environment fingerprint of
// §5.1: sha256 of the canonical JSON of every active SUT binding of the
// version's environment, sorted by sut_id with sorted ID/role arrays. Any change
// to the environment, target resources or account roles produces a new
// fingerprint, and therefore a new intent.
func EnvironmentFingerprint(binding VersionBinding) (string, error) {
	if len(binding.SUTs) == 0 {
		return "", platform.Invalid("sut_ids", "the version has no active SUT bindings")
	}
	payload := fingerprintPayload{SUTs: make([]fingerprintSUT, 0, len(binding.SUTs))}
	for _, sut := range binding.SUTs {
		if sut.SUTID <= 0 || sut.EnvironmentID <= 0 {
			return "", platform.Invalid("sut_ids", "SUT bindings require positive IDs")
		}
		payload.SUTs = append(payload.SUTs, fingerprintSUT{
			SUTID:         sut.SUTID,
			EnvironmentID: sut.EnvironmentID,
			BaseURL:       sut.BaseURL,
			PageIDs:       sortedUniqueInt64(sut.PageIDs),
			APIIDs:        sortedUniqueInt64(sut.APIIDs),
			DatabaseIDs:   sortedUniqueInt64(sut.DatabaseIDs),
			AccountRoles:  sortedUniqueStrings(sut.AccountRoles),
		})
	}
	sort.SliceStable(payload.SUTs, func(i, j int) bool {
		return payload.SUTs[i].SUTID < payload.SUTs[j].SUTID
	})
	return hashCanonical(payload)
}

// CodeBaseline returns the code baseline (V-0024 strong binding): empty when the
// version binds no code repository, otherwise sha256 of the canonical JSON of the
// distinct repositories of the environment code pool in ascending repo_id order
// (§5.5), each paired with its OWN resolved commit. Every entry must be a resolved
// lowercase 40-hex commit — an unresolved (empty) or malformed commit is a typed
// rejection, never a branch fallback, a bare {repo_id}, or a commit borrowed from
// another repository. Multiple SUT bindings that share the same repository are
// deduplicated; conflicting commits settle on the lexicographically smallest SHA
// so the fingerprint never depends on binding order.
func CodeBaseline(binding VersionBinding) (string, error) {
	byRepo := map[int64]codeBaselineRepo{}
	for _, sut := range binding.SUTs {
		for _, baseline := range sut.RepositoryBaselines {
			if baseline.RepoID <= 0 {
				return "", platform.Invalid("code_baseline", "repository baselines require positive repository IDs")
			}
			// GitCommitSHA short-circuits nil on the empty string (the nullable
			// column's "not resolved yet"), so the non-empty guard is explicit: a
			// baseline entry must be a RESOLVED commit (V-0024).
			if baseline.Commit == "" {
				return "", unresolvedBaselineError(baseline.RepoID)
			}
			if err := validation.GitCommitSHA("code_baseline_commit", baseline.Commit); err != nil {
				return "", err
			}
			candidate := codeBaselineRepo{RepoID: baseline.RepoID, CommitSHA: baseline.Commit}
			current, ok := byRepo[candidate.RepoID]
			if !ok {
				byRepo[candidate.RepoID] = candidate
				continue
			}
			byRepo[candidate.RepoID] = mergeCodeBaselineRepo(current, candidate)
		}
	}
	if len(byRepo) == 0 {
		return "", nil
	}
	repos := make([]codeBaselineRepo, 0, len(byRepo))
	for _, repo := range byRepo {
		repos = append(repos, repo)
	}
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].RepoID < repos[j].RepoID })
	return hashCanonical(codeBaselinePayload{Repositories: repos})
}

// unresolvedBaselineError is the typed 409 for a repository of the environment
// code pool whose baseline commit is not resolved. It names the repository and the
// next action so the failure is attributable (constitution art.7.7), never a
// silent branch fallback or a narrowed baseline.
func unresolvedBaselineError(repoID int64) error {
	return &platform.Error{
		Status: http.StatusConflict,
		Code:   "code_baseline_unresolved",
		Message: fmt.Sprintf(
			"repository %d has no resolved code baseline commit; execution requires a pinned 40-hex commit for every repository of the environment code pool",
			repoID),
		Details: map[string]any{
			"repo_id":     platformid.Format(repoID),
			"next_action": "index the repository (or pin its baseline commit) so the code baseline resolves, then execute again",
		},
	}
}

// mergeCodeBaselineRepo picks the canonical entry for one repository: both
// candidates carry a validated resolved commit, so the tie-break is the
// lexicographically smallest SHA and the fingerprint never depends on binding
// order (§5.5).
func mergeCodeBaselineRepo(current, candidate codeBaselineRepo) codeBaselineRepo {
	if candidate.CommitSHA < current.CommitSHA {
		return candidate
	}
	return current
}

// Wave frozen fingerprint failure codes (SC-F7; v0.2.4-G SC-G6 report leg).
// WaveFrozenFingerprint fails closed for three distinct reasons, and a wave-level
// consumer must tell them apart to report the truth to a human (art.7.6): a
// genuine legacy wave (no carrier) is NOT the same fact as an integrity failure
// (the recorded carrier disagrees with the frozen inputs it claims to summarize —
// a tamper/corruption signal). The codes keep the same fail-closed behavior (no
// live-read fallback) while making the reason classifiable.
const (
	// WaveFrozenSnapshotMalformedCode marks a scope snapshot that is not a
	// decodable JSON object (parse failure).
	WaveFrozenSnapshotMalformedCode = "wave_frozen_snapshot_malformed"
	// WaveFrozenNoCarrierCode marks a genuine legacy wave whose frozen snapshot
	// carries no environment fingerprint carrier.
	WaveFrozenNoCarrierCode = "wave_frozen_no_carrier"
	// WaveFrozenFingerprintMismatchCode marks an integrity failure: the recorded
	// environment fingerprint carrier disagrees with the frozen scope inputs.
	WaveFrozenFingerprintMismatchCode = "wave_frozen_fingerprint_mismatch"
	// WaveFrozenBaselineMismatchCode marks an integrity failure: the recorded
	// code baseline carrier disagrees with the frozen scope inputs.
	WaveFrozenBaselineMismatchCode = "wave_frozen_baseline_mismatch"
)

// WaveFrozenFingerprint is the wave-level fingerprint consumer (SC-F7). It rebuilds
// the environment fingerprint and code baseline from the wave's FROZEN scope
// snapshot through the same canonical hash functions used at plan time, and
// cross-checks them against the recorded carriers. Because the inputs are the
// frozen document — not the live catalog — a mid-wave catalog change cannot move
// the value: the in-flight wave keeps its fingerprint and the next wave freezes a
// new one. A legacy wave without a carrier, or a snapshot whose recorded carrier
// disagrees with its frozen inputs, is a typed error (fail-closed; never a silent
// live-read fallback, which would reintroduce the drift the freeze prevents).
//
// Intent creation and the replay drift gate deliberately do NOT use this: they
// keep the live read (change detection and same-fingerprint replay safety).
func WaveFrozenFingerprint(wave execution.RunWave) (string, string, error) {
	payload, err := execution.ParseWaveScopeSnapshot(wave.ScopeSnapshot)
	if err != nil {
		// Re-wrap under the classifiable wave-frozen code, but KEEP the domain
		// parser's message and field detail (platform.Invalid("scope_snapshot")):
		// dropping them would hide which input was malformed (H-G4 T9, 七.6/七.7).
		cause := platform.AsError(err)
		wrapped := platform.NewError(http.StatusBadRequest, WaveFrozenSnapshotMalformedCode, cause.Message)
		wrapped.Details = cause.Details
		return "", "", wrapped
	}
	if payload.EnvironmentFingerprint == "" {
		return "", "", platform.NewError(http.StatusConflict, WaveFrozenNoCarrierCode,
			"the wave has no frozen environment fingerprint carrier; wave-level consumers require a frozen scope snapshot")
	}
	binding := VersionBinding{
		VersionID: payload.VersionID,
		SUTs:      make([]SUTBinding, 0, len(payload.SUTs)),
	}
	for _, sut := range payload.SUTs {
		baselines := make([]RepositoryBaseline, 0, len(sut.RepositoryBaselines))
		for _, repo := range sut.RepositoryBaselines {
			baselines = append(baselines, RepositoryBaseline{RepoID: repo.RepoID, Commit: repo.CommitSHA})
		}
		binding.SUTs = append(binding.SUTs, SUTBinding{
			SUTID:               sut.SUTID,
			EnvironmentID:       sut.EnvironmentID,
			BaseURL:             sut.BaseURL,
			PageIDs:             sut.PageIDs,
			APIIDs:              sut.APIIDs,
			DatabaseIDs:         sut.DatabaseIDs,
			AccountRoles:        sut.AccountRoles,
			RepositoryIDs:       sut.RepositoryIDs,
			RepositoryBaselines: baselines,
		})
	}
	fingerprint, err := EnvironmentFingerprint(binding)
	if err != nil {
		return "", "", err
	}
	if fingerprint != payload.EnvironmentFingerprint {
		return "", "", platform.NewError(http.StatusConflict, WaveFrozenFingerprintMismatchCode,
			"the wave's frozen environment fingerprint carrier disagrees with its frozen scope inputs")
	}
	baseline, err := CodeBaseline(binding)
	if err != nil {
		return "", "", err
	}
	if baseline != payload.CodeBaseline {
		return "", "", platform.NewError(http.StatusConflict, WaveFrozenBaselineMismatchCode,
			"the wave's frozen code baseline carrier disagrees with its frozen scope inputs")
	}
	return fingerprint, baseline, nil
}

// DedupeKey builds the display/work-item correlation key of §5.1. It is not a
// uniqueness constraint: the partial unique index owns idempotency.
func DedupeKey(caseVersionID int64, fingerprint, baseline string, purpose execution.Purpose) string {
	sum := sha256.Sum256([]byte(
		strconv.FormatInt(caseVersionID, 10) + "|" + fingerprint + "|" + baseline + "|" + string(purpose),
	))
	return "exec:" + hex.EncodeToString(sum[:])[:32]
}

// SUTIDs returns the sorted unique SUT ids of a version binding.
func SUTIDs(binding VersionBinding) []int64 {
	ids := make([]int64, 0, len(binding.SUTs))
	for _, sut := range binding.SUTs {
		if sut.SUTID > 0 {
			ids = append(ids, sut.SUTID)
		}
	}
	return sortedUniqueInt64(ids)
}

func hashCanonical(payload any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", platform.Invalid("fingerprint", "could not encode the environment fingerprint")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func sortedUniqueInt64(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func sortedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
