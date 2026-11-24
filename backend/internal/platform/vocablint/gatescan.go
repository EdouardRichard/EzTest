package vocablint

// The gate driver: one scan of a derived surface against a derived value set and
// an adjudicated exemption table. Four gates used to keep their own copy of this
// loop (read file -> ScanLiterals -> accumulate hits -> stale check -> sort
// findings); the algorithm is shared, the SCOPE stays at the gate.

import (
	"os"
	"path/filepath"
	"sort"
)

// SurfaceScan is the result of scanning a whole guarded surface: the unexempted
// occurrences in (file, line, value) order and the exemption-table problems
// (ill-shaped entries, stale fragments, fragments that are not 1:1 with the sites
// they sanction). A read failure is a problem too — a gate must never look green
// because it could not read a file.
type SurfaceScan struct {
	Findings []Finding
	Problems []string
}

// ScanSurface scans every repository-relative file of files under root. allowlist
// fragments are file-bound, so a fragment can only sanction occurrences in its own
// file (T11e-5 Part A1), and one fragment must adjudicate exactly one OCCURRENCE
// (T11e-6 M5 + review Minor 3).
func ScanSurface(root string, files []string, values map[string][]string, allowlist map[string]AllowEntry) SurfaceScan {
	out := SurfaceScan{}
	out.Problems = append(out.Problems, AllowlistShapeProblems(allowlist)...)
	hits := map[FragmentKey]int{}
	sites := map[FragmentKey][]OccurrenceSite{}
	for _, rel := range files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			out.Problems = append(out.Problems, "read "+rel+": "+err.Error())
			continue
		}
		scan := ScanLiterals(rel, string(src), values, allowlist)
		out.Findings = append(out.Findings, scan.Findings...)
		for key, count := range scan.FragmentHits {
			hits[key] += count
		}
		for key, lines := range scan.FragmentSites {
			sites[key] = append(sites[key], lines...)
		}
	}
	out.Problems = append(out.Problems, StaleFragmentProblems(allowlist, hits)...)
	out.Problems = append(out.Problems, FragmentSiteProblems(allowlist, sites)...)
	sort.Slice(out.Findings, func(i, j int) bool {
		if out.Findings[i].File != out.Findings[j].File {
			return out.Findings[i].File < out.Findings[j].File
		}
		if out.Findings[i].Line != out.Findings[j].Line {
			return out.Findings[i].Line < out.Findings[j].Line
		}
		return out.Findings[i].Value < out.Findings[j].Value
	})
	return out
}
