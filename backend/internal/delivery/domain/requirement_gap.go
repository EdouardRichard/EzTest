package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// RequirementGapDedupeKey computes the §5.4 deterministic requirement-gap
// dedupe key: sha256(version_id | 'requirement_gap' | requirement id set). The
// id set is sorted and deduplicated so the same gap escalated twice (or with a
// different requirement order) hard-dedupes to one issue.
func RequirementGapDedupeKey(versionID int64, requirementIDs []int64) string {
	normalized := NormalizeRequirementGapIDs(requirementIDs)
	parts := make([]string, 0, len(normalized))
	for _, requirementID := range normalized {
		parts = append(parts, strconv.FormatInt(requirementID, 10))
	}
	payload := fmt.Sprintf("%d|%s|%s", versionID, CategoryRequirementGap, strings.Join(parts, ","))
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// NormalizeRequirementGapIDs returns the positive requirement ids sorted
// ascending and deduplicated. Non-positive ids are dropped; the caller rejects
// an escalation that normalizes to an empty set.
func NormalizeRequirementGapIDs(requirementIDs []int64) []int64 {
	if len(requirementIDs) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(requirementIDs))
	unique := make([]int64, 0, len(requirementIDs))
	for _, requirementID := range requirementIDs {
		if requirementID <= 0 {
			continue
		}
		if _, duplicate := seen[requirementID]; duplicate {
			continue
		}
		seen[requirementID] = struct{}{}
		unique = append(unique, requirementID)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	return unique
}
