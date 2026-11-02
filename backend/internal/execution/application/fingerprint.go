package executionapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// VersionBinding is the execution context's read projection of one version and
// its SUT bindings. It is produced by the version adapter from the version
// application service; disabled bindings are filtered out there.
type VersionBinding struct {
	VersionID int64
	Status    string
	SUTs      []VersionSUTBinding
}

// VersionSUTBinding is one active version_sut binding with its resolved
// fingerprint inputs and code-repository baseline. Commit is the resolved
// version_sut.code_baseline_commit; empty means the branch fallback is used.
type VersionSUTBinding struct {
	VersionSUTID    int64
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
	Branch          string
	Commit          string
}

// fingerprintPayload is the canonical JSON shape of §5.1. Field order is
// fixed by the struct; slices and bindings are normalized before encoding.
type fingerprintPayload struct {
	VersionSUTs []fingerprintSUT `json:"version_suts"`
}

type fingerprintSUT struct {
	VersionSUTID  int64    `json:"version_sut_id"`
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

// codeBaselineRepo is either a resolved commit ({repo_id, commit_sha}) or, when
// the binding's baseline could not be resolved, the branch fallback
// ({repo_id, branch}). The two shapes are mutually exclusive so the canonical
// JSON matches spec §5.5.
type codeBaselineRepo struct {
	RepoID    int64  `json:"repo_id"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

// EnvironmentFingerprint computes the deterministic environment fingerprint of
// §5.1: sha256 of the canonical JSON of every active version_sut binding,
// sorted by version_sut_id with sorted ID/role arrays. Any change to the
// environment, target resources or account roles produces a new fingerprint,
// and therefore a new intent.
func EnvironmentFingerprint(binding VersionBinding) (string, error) {
	if len(binding.SUTs) == 0 {
		return "", platform.Invalid("version_sut_ids", "the version has no active SUT bindings")
	}
	payload := fingerprintPayload{VersionSUTs: make([]fingerprintSUT, 0, len(binding.SUTs))}
	for _, sut := range binding.SUTs {
		if sut.VersionSUTID <= 0 || sut.SUTID <= 0 || sut.EnvironmentID <= 0 {
			return "", platform.Invalid("version_sut_ids", "version SUT bindings require positive IDs")
		}
		payload.VersionSUTs = append(payload.VersionSUTs, fingerprintSUT{
			VersionSUTID:  sut.VersionSUTID,
			SUTID:         sut.SUTID,
			EnvironmentID: sut.EnvironmentID,
			BaseURL:       sut.BaseURL,
			PageIDs:       sortedUniqueInt64(sut.PageIDs),
			APIIDs:        sortedUniqueInt64(sut.APIIDs),
			DatabaseIDs:   sortedUniqueInt64(sut.DatabaseIDs),
			AccountRoles:  sortedUniqueStrings(sut.AccountRoles),
		})
	}
	sort.SliceStable(payload.VersionSUTs, func(i, j int) bool {
		return payload.VersionSUTs[i].VersionSUTID < payload.VersionSUTs[j].VersionSUTID
	})
	return hashCanonical(payload)
}

// CodeBaseline returns the P5 code baseline: empty when the version binds no
// code repository, otherwise sha256 of the canonical JSON of the distinct
// repositories in ascending repo_id order (§5.5). A repository with a resolved
// commit contributes {repo_id, commit_sha}; a repository without one falls back
// to {repo_id, branch}. Multiple version_sut bindings of the same repository
// are deduplicated: a resolved commit wins over the branch fallback and
// conflicting commits settle on the lexicographically smallest SHA so the
// fingerprint never depends on binding order.
func CodeBaseline(binding VersionBinding) string {
	byRepo := map[int64]codeBaselineRepo{}
	for _, sut := range binding.SUTs {
		if sut.CodeRepoID == nil || *sut.CodeRepoID <= 0 {
			continue
		}
		candidate := codeBaselineRepo{RepoID: *sut.CodeRepoID}
		if sut.Commit != "" {
			candidate.CommitSHA = sut.Commit
		} else {
			candidate.Branch = sut.Branch
		}
		current, ok := byRepo[candidate.RepoID]
		if !ok {
			byRepo[candidate.RepoID] = candidate
			continue
		}
		byRepo[candidate.RepoID] = mergeCodeBaselineRepo(current, candidate)
	}
	if len(byRepo) == 0 {
		return ""
	}
	repos := make([]codeBaselineRepo, 0, len(byRepo))
	for _, repo := range byRepo {
		repos = append(repos, repo)
	}
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].RepoID < repos[j].RepoID })
	hash, err := hashCanonical(codeBaselinePayload{Repositories: repos})
	if err != nil {
		return ""
	}
	return hash
}

// mergeCodeBaselineRepo picks the canonical entry for one repository: a
// resolved commit beats the branch fallback, and ties between two commits or
// two branches settle on the lexicographically smallest value so the
// fingerprint never depends on binding order.
func mergeCodeBaselineRepo(current, candidate codeBaselineRepo) codeBaselineRepo {
	switch {
	case current.CommitSHA != "" && candidate.CommitSHA != "":
		if candidate.CommitSHA < current.CommitSHA {
			return candidate
		}
		return current
	case candidate.CommitSHA != "":
		return candidate
	case current.CommitSHA != "":
		return current
	case candidate.Branch < current.Branch:
		return candidate
	default:
		return current
	}
}

// DedupeKey builds the display/work-item correlation key of §5.1. It is not a
// uniqueness constraint: the partial unique index owns idempotency.
func DedupeKey(caseVersionID int64, fingerprint, baseline string, purpose execution.Purpose) string {
	sum := sha256.Sum256([]byte(
		strconv.FormatInt(caseVersionID, 10) + "|" + fingerprint + "|" + baseline + "|" + string(purpose),
	))
	return "exec:" + hex.EncodeToString(sum[:])[:32]
}

// VersionSUTIDs returns the sorted unique version_sut IDs of a version binding.
func VersionSUTIDs(binding VersionBinding) []int64 {
	ids := make([]int64, 0, len(binding.SUTs))
	for _, sut := range binding.SUTs {
		if sut.VersionSUTID > 0 {
			ids = append(ids, sut.VersionSUTID)
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
