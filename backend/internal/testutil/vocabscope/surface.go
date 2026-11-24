package vocabscope

// The REPOSITORY scan surface: every bounded context's infrastructure layer,
// derived from the directory tree. This is what the composition-root gate guards
// (H-A25). A hand-written context list is forbidden for the same reason a
// hand-written file list is: a new context, or a new subdirectory of an existing
// one, would silently escape the net.
//
// The per-context floors below are anti-vacuity bounds, NOT a coverage list: the
// file set inside each context is always derived. A context that appears in the
// walk with no floor entry is itself reported (SurfaceFloorProblems), so a
// twentieth context cannot join the repository unprotected and unnoticed.

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// Surface is a derived scan surface: the repository-relative slash paths in sorted
// order, and the same paths grouped by bounded context (for the per-context floors).
type Surface struct {
	Files     []string
	ByContext map[string][]string
}

// Counts returns the per-context file counts the surface floors bound.
func (s Surface) Counts() map[string]int {
	out := make(map[string]int, len(s.ByContext))
	for name, files := range s.ByContext {
		out[name] = len(files)
	}
	return out
}

// InfrastructureSurface walks backend/internal and collects every non-test .go file
// whose path contains an /infrastructure/ segment, keyed by the bounded context
// (the first path segment under backend/internal). Platform facilities
// (platform / db / server / config / testutil) have no infrastructure layer, so they
// contribute nothing; if one ever grows one, SurfaceFloorProblems reports it.
func InfrastructureSurface(root string) (Surface, error) {
	internal := filepath.Join(root, "backend", "internal")
	all, err := vocablint.GoFilesUnder(internal)
	if err != nil {
		return Surface{}, err
	}
	out := Surface{ByContext: map[string][]string{}}
	for _, file := range all {
		rel, relErr := filepath.Rel(root, file)
		if relErr != nil {
			return Surface{}, relErr
		}
		slash := filepath.ToSlash(rel)
		parts := strings.Split(slash, "/")
		if len(parts) < 4 || !strings.Contains(slash, "/infrastructure/") {
			continue
		}
		out.Files = append(out.Files, slash)
		out.ByContext[parts[2]] = append(out.ByContext[parts[2]], slash)
	}
	return out, nil
}

// GateContexts is the SCOPE of each context gate: which bounded contexts'
// infrastructure it guards. The file set inside each context is always derived.
//
// This is the single authority for those scopes (T11e-6). Before it, each gate
// carried its own list AND its own per-context file-count floor, duplicating the
// numbers in InfrastructureSurfaceFloors — and they had already drifted:
// the orchestration gate's floor for codeintel said 13 while the tree holds 15.
// A floor two gates state differently is a second truth (constitution 五.1/五.3),
// the same defect T11e-5 closed for the per-TYPE floors.
func GateContexts() map[string][]string {
	return map[string][]string{
		"delivery":      {"delivery"},
		"version":       {"version"},
		"orchestration": {"orchestration", "codeintel", "knowledge", "design", "storage"},
		"agent":         {"agent", "chain", "execution", "llm", "search", "eventing"},
	}
}

// GuardedFiles derives one gate's scan surface: every non-test .go file under each
// named context's infrastructure directory, repository-relative and sorted, with
// the per-context anti-vacuity floor taken from the ONE shared table. A context
// with no floor entry is an error, not a silently unprotected scope.
func GuardedFiles(root string, contexts []string) ([]string, error) {
	floors := InfrastructureSurfaceFloors()
	var out []string
	for _, name := range contexts {
		floor, ok := floors[name]
		if !ok {
			return nil, fmt.Errorf("guarded context %q has no scan-surface floor entry; register it in InfrastructureSurfaceFloors", name)
		}
		files, err := vocablint.GoFilesUnder(filepath.Join(root, "backend", "internal", name, "infrastructure"))
		if err != nil {
			return nil, fmt.Errorf("derive %s infrastructure scan surface: %w", name, err)
		}
		if problem := vocablint.CountFloorProblem(name+" infrastructure production files", len(files), floor); problem != "" {
			return nil, errors.New(problem)
		}
		for _, file := range files {
			rel, relErr := filepath.Rel(root, file)
			if relErr != nil {
				return nil, relErr
			}
			out = append(out, filepath.ToSlash(rel))
		}
	}
	sort.Strings(out)
	return out, nil
}

// InfrastructureSurfaceFloors are the per-context anti-vacuity floors, measured on
// the T11e-6 tree: 172 production files across 19 bounded contexts. The six
// contexts no context gate had ever covered (audit / identity / quality / resource /
// settings / workspace, 12 files) are registered here too -- that is the whole point
// of H-A25: they were measured, found clean, and are now permanently inside the net.
func InfrastructureSurfaceFloors() map[string]int {
	return map[string]int{
		"agent":         31,
		"audit":         1,
		"chain":         6,
		"codeintel":     15,
		"delivery":      15,
		"design":        12,
		"eventing":      4,
		"execution":     35,
		"identity":      2,
		"knowledge":     1,
		"llm":           5,
		"orchestration": 3,
		"quality":       3,
		"resource":      1,
		"search":        4,
		"settings":      1,
		"storage":       5,
		"version":       24,
		"workspace":     4,
	}
}
