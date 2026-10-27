package gitx

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

const (
	// defaultMaxDiffBytes mirrors the design spec §4.2 default of
	// EZTEST_CODEINTEL_MAX_DIFF_BYTES.
	defaultMaxDiffBytes = 256 << 10
	// hardMaxDiffBytes is the highest configured diff bound of design spec §4.2.
	hardMaxDiffBytes = 1 << 20
)

// Diff builds the deterministic file-level change set between base and head by
// merging `git diff --raw -z` and `git diff --numstat -z` on path (design spec
// §6.1). Files are sorted by path and cut at MaxChangedFilesStored.
func (r *GitReader) Diff(ctx context.Context, localPath, base, head string) (domain.ChangeSetData, error) {
	if !validCommit(base) || !validCommit(head) {
		return domain.ChangeSetData{}, commitNotFound()
	}
	dir, err := r.openRepo(ctx, localPath)
	if err != nil {
		return domain.ChangeSetData{}, err
	}
	return r.diffAt(ctx, dir, base, head)
}

// FileDiff returns the on-demand unified diff of one changed file. The change
// set lookup supplies the authoritative status (so a rename keeps R<score>)
// and rejects paths outside the change set. Binary and deleted files return an
// empty patch, and the patch is cut at maxBytes.
func (r *GitReader) FileDiff(ctx context.Context, localPath, base, head, path string, maxBytes int) (domain.FileDiff, error) {
	if !validCommit(base) || !validCommit(head) {
		return domain.FileDiff{}, commitNotFound()
	}
	normalized, err := normalizeRepoPath(path)
	if err != nil {
		return domain.FileDiff{}, err
	}
	limit := maxBytes
	if limit <= 0 {
		limit = defaultMaxDiffBytes
	}
	if limit > hardMaxDiffBytes {
		limit = hardMaxDiffBytes
	}
	dir, err := r.openRepo(ctx, localPath)
	if err != nil {
		return domain.FileDiff{}, err
	}
	changes, err := r.diffAt(ctx, dir, base, head)
	if err != nil {
		return domain.FileDiff{}, err
	}
	var changed *domain.ChangedFile
	for index := range changes.Files {
		if changes.Files[index].Path == normalized {
			changed = &changes.Files[index]
			break
		}
	}
	if changed == nil {
		return domain.FileDiff{}, notChanged()
	}
	result := domain.FileDiff{
		BaseCommit: base,
		HeadCommit: head,
		Path:       normalized,
		Status:     changed.Status,
		Binary:     changed.Binary,
	}
	if changed.Status == "D" || changed.Binary {
		return result, nil
	}

	output, err := r.run(ctx, dir, "diff", "--no-ext-diff", "--find-renames", "--unified=3",
		base+".."+head, "--", literalPathspec(normalized))
	if err != nil {
		if operationalError(err) {
			return domain.FileDiff{}, err
		}
		if missingObject(err) {
			return domain.FileDiff{}, commitNotFound()
		}
		return domain.FileDiff{}, fmt.Errorf("gitx: file diff: %w", err)
	}
	patch := string(output)
	if strings.Contains(patch, "Binary files ") || strings.Contains(patch, "GIT binary patch") {
		result.Binary = true
		result.Patch = ""
		return result, nil
	}
	if len(patch) > limit {
		patch = patch[:limit]
		for len(patch) > 0 && !utf8.ValidString(patch) {
			patch = patch[:len(patch)-1]
		}
		result.Truncated = true
	}
	result.Patch = patch
	return result, nil
}

// diffAt is the shared change-set implementation used by Diff and FileDiff.
func (r *GitReader) diffAt(ctx context.Context, dir, base, head string) (domain.ChangeSetData, error) {
	commitRange := base + ".." + head
	// --no-abbrev is required: raw diff abbreviates object ids by default, but
	// design spec §6.1 defines headBlobHash as "git-sha1:<40hex>".
	rawOutput, err := r.run(ctx, dir, "diff", "--raw", "-z", "--no-ext-diff", "--find-renames=50%", "--no-abbrev", commitRange)
	if err != nil {
		return domain.ChangeSetData{}, diffCommandError(err)
	}
	numstatOutput, err := r.run(ctx, dir, "diff", "--numstat", "-z", "--no-ext-diff", "--find-renames=50%", commitRange)
	if err != nil {
		return domain.ChangeSetData{}, diffCommandError(err)
	}
	rawChanges, err := parseRawDiff(rawOutput)
	if err != nil {
		return domain.ChangeSetData{}, err
	}
	numstat, err := parseNumstatDiff(numstatOutput)
	if err != nil {
		return domain.ChangeSetData{}, err
	}
	files := mergeChangedFiles(rawChanges, numstat)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	stats := changeSetStats{}
	for _, file := range files {
		stats.Additions += file.Additions
		stats.Deletions += file.Deletions
	}
	stats.ChangedFiles = len(files)
	if len(files) > domain.MaxChangedFilesStored {
		files = files[:domain.MaxChangedFilesStored]
		stats.ChangedFiles = len(files)
		stats.Truncated = true
	}
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return domain.ChangeSetData{}, fmt.Errorf("gitx: encode change set stats: %w", err)
	}
	return domain.ChangeSetData{
		Stats:     statsJSON,
		Files:     files,
		Truncated: stats.Truncated,
	}, nil
}

// changeSetStats is the deterministic change-set summary (design spec §6.9
// event stats shape).
type changeSetStats struct {
	Additions    int  `json:"additions"`
	ChangedFiles int  `json:"changedFiles"`
	Deletions    int  `json:"deletions"`
	Truncated    bool `json:"truncated"`
}

// rawChange is one parsed record of `git diff --raw -z`.
type rawChange struct {
	path    string
	oldPath string
	status  string
	newSHA  string
}

// numstatChange is one parsed record of `git diff --numstat -z`.
type numstatChange struct {
	additions int
	deletions int
	binary    bool
}

// parseRawDiff parses the NUL-separated raw diff records:
// ":<oldmode> <newmode> <oldsha> <newsha> <status>\x00<path>\x00" for ordinary
// changes and two path records for renames and copies.
func parseRawDiff(output []byte) ([]rawChange, error) {
	records := strings.Split(string(output), "\x00")
	changes := make([]rawChange, 0, len(records))
	for index := 0; index < len(records); {
		header := records[index]
		index++
		if header == "" {
			continue
		}
		if !strings.HasPrefix(header, ":") {
			return nil, fmt.Errorf("%w: raw diff record does not start with ':'", errMalformedOutput)
		}
		fields := strings.Fields(header[1:])
		if len(fields) != 5 || fields[4] == "" {
			return nil, fmt.Errorf("%w: raw diff record has %d fields", errMalformedOutput, len(fields))
		}
		change := rawChange{status: fields[4], newSHA: fields[3]}
		switch change.status[0] {
		case 'R', 'C':
			if index+1 >= len(records) {
				return nil, fmt.Errorf("%w: raw rename record without two paths", errMalformedOutput)
			}
			change.oldPath = records[index]
			change.path = records[index+1]
			index += 2
		default:
			if index >= len(records) {
				return nil, fmt.Errorf("%w: raw record without a path", errMalformedOutput)
			}
			change.path = records[index]
			index++
		}
		if change.path == "" {
			return nil, fmt.Errorf("%w: raw record with an empty path", errMalformedOutput)
		}
		changes = append(changes, change)
	}
	return changes, nil
}

// parseNumstatDiff parses the NUL-separated numstat records. A rename record
// carries an empty path field followed by the old and new path records.
func parseNumstatDiff(output []byte) (map[string]numstatChange, error) {
	records := strings.Split(string(output), "\x00")
	changes := make(map[string]numstatChange, len(records))
	for index := 0; index < len(records); {
		record := records[index]
		index++
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("%w: numstat record has %d fields", errMalformedOutput, len(fields))
		}
		path := fields[2]
		if path == "" {
			if index+1 >= len(records) {
				return nil, fmt.Errorf("%w: numstat rename without two paths", errMalformedOutput)
			}
			path = records[index+1]
			index += 2
		}
		if path == "" {
			return nil, fmt.Errorf("%w: numstat record with an empty path", errMalformedOutput)
		}
		change := numstatChange{}
		if fields[0] == "-" && fields[1] == "-" {
			change.binary = true
		} else {
			additions, err := strconv.Atoi(fields[0])
			if err != nil {
				return nil, fmt.Errorf("%w: numstat additions %q", errMalformedOutput, fields[0])
			}
			deletions, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, fmt.Errorf("%w: numstat deletions %q", errMalformedOutput, fields[1])
			}
			change.additions = additions
			change.deletions = deletions
		}
		changes[path] = change
	}
	return changes, nil
}

// mergeChangedFiles joins the raw statuses and the numstat line counts by path.
func mergeChangedFiles(raw []rawChange, numstat map[string]numstatChange) []domain.ChangedFile {
	files := make([]domain.ChangedFile, 0, len(raw))
	for _, change := range raw {
		file := domain.ChangedFile{Path: change.path, Status: change.status}
		if change.oldPath != "" {
			oldPath := change.oldPath
			file.OldPath = &oldPath
		}
		if !isZeroSHA(change.newSHA) {
			file.HeadBlobHash = "git-sha1:" + change.newSHA
		}
		if stats, ok := numstat[change.path]; ok {
			file.Additions = stats.additions
			file.Deletions = stats.deletions
			file.Binary = stats.binary
		}
		files = append(files, file)
	}
	return files
}

// isZeroSHA reports the all-zero object id git prints for the absent side of an
// added, deleted or type-changed entry.
func isZeroSHA(sha string) bool {
	return strings.Trim(sha, "0") == ""
}

// diffCommandError maps a failed diff command to the domain vocabulary.
func diffCommandError(err error) error {
	if operationalError(err) {
		return err
	}
	if missingObject(err) {
		return commitNotFound()
	}
	return fmt.Errorf("gitx: diff: %w", err)
}
