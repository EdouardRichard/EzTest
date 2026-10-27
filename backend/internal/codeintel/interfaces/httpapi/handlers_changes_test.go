package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// treePageWire is the tree endpoint's page-envelope shape (frontend apiGetPage:
// data is the entry array, next_cursor/has_more are siblings).
type treePageWire struct {
	Data       []treeEntryWire `json:"data"`
	NextCursor string          `json:"next_cursor"`
	HasMore    bool            `json:"has_more"`
}

// fileWindowWire is the FileWindow DTO shape.
type fileWindowWire struct {
	Commit     string `json:"commit"`
	Path       string `json:"path"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	TotalLines int    `json:"totalLines"`
	Truncated  bool   `json:"truncated"`
	Content    string `json:"content"`
}

// fileDiffWire is the FileDiff DTO shape.
type fileDiffWire struct {
	BaseCommit string `json:"baseCommit"`
	HeadCommit string `json:"headCommit"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Binary     bool   `json:"binary"`
	Truncated  bool   `json:"truncated"`
	Patch      string `json:"patch"`
}

// changeStatsWire is the translated ChangeList stats shape (gitx writes
// changedFiles; the API contract is files).
type changeStatsWire struct {
	Files     int  `json:"files"`
	Additions int  `json:"additions"`
	Deletions int  `json:"deletions"`
	Truncated bool `json:"truncated"`
}

// changedFileWire is one ChangeList file entry.
type changedFileWire struct {
	Path         string  `json:"path"`
	OldPath      *string `json:"oldPath"`
	Status       string  `json:"status"`
	Additions    int     `json:"additions"`
	Deletions    int     `json:"deletions"`
	Binary       bool    `json:"binary"`
	HeadBlobHash string  `json:"headBlobHash"`
}

// changeListWire is the ChangeListDTO embedded in the data field (R7).
type changeListWire struct {
	ChangeSetID *string           `json:"changeSetId"`
	BaseCommit  string            `json:"baseCommit"`
	HeadCommit  string            `json:"headCommit"`
	Stats       changeStatsWire   `json:"stats"`
	Files       []changedFileWire `json:"files"`
	NextCursor  *string           `json:"nextCursor"`
	HasMore     bool              `json:"hasMore"`
}

// evidenceWire is one CodeEntity evidence entry.
type evidenceWire struct {
	File        string `json:"file"`
	StartLine   int    `json:"startLine"`
	EndLine     int    `json:"endLine"`
	ExcerptHash string `json:"excerptHash"`
}

// codeEntityWire is the CodeEntity DTO shape: codeintel ids are decimal strings,
// repoId is a number (R6/D29).
type codeEntityWire struct {
	ID          string         `json:"id"`
	RepoID      int64          `json:"repoId"`
	Commit      string         `json:"commit"`
	Kind        string         `json:"kind"`
	Path        string         `json:"path"`
	Name        string         `json:"name"`
	StartLine   *int           `json:"startLine"`
	EndLine     *int           `json:"endLine"`
	Signature   string         `json:"signature"`
	BlobHash    string         `json:"blobHash"`
	Source      string         `json:"source"`
	Confidence  string         `json:"confidence"`
	Evidence    []evidenceWire `json:"evidence"`
	FactKey     *string        `json:"factKey"`
	FactVersion *int           `json:"factVersion"`
	AnalysisID  *string        `json:"analysisId"`
	CreatedAt   *string        `json:"createdAt"`
	UpdatedAt   *string        `json:"updatedAt"`
}

// entityPageWire is the facts/search page-envelope shape.
type entityPageWire struct {
	Data       []codeEntityWire `json:"data"`
	NextCursor string           `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

// commitWire is the commit metadata DTO shape.
type commitWire struct {
	SHA         string `json:"sha"`
	Message     string `json:"message"`
	Author      string `json:"author"`
	CommittedAt string `json:"committedAt"`
}

// TestTreePagesWithPathCursor proves the tree endpoint follows the ApiPage wire
// shape, forwards the live-Git query and keeps the last path as the cursor.
func TestTreePagesWithPathCursor(t *testing.T) {
	page := domain.TreePage{
		Commit: commitA,
		Entries: []domain.TreeEntry{
			{Path: "src/a.go", Type: "blob", GitObjectID: commitA, Mode: "100644", Size: 120},
			{Path: "src/b", Type: "tree", GitObjectID: commitB, Mode: "040000", Size: 0},
		},
		NextCursor: "src/b",
		HasMore:    true,
	}
	query := &fakeQueryService{treeFn: func(context.Context, int64, string, string, string, int) (domain.TreePage, error) {
		return page, nil
	}}
	handler := newTestHandler(t, HandlerDeps{Query: query})

	recorder := serve(t, handler, http.MethodGet,
		"/api/code/repos/7/tree?commit="+commitA+"&path=src&cursor=src%2Fa.go&limit=2", userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body treePageWire
	decodeBody(t, recorder, &body)
	if len(body.Data) != 2 {
		t.Fatalf("entries = %d, want 2", len(body.Data))
	}
	if body.Data[0].Path != "src/a.go" || body.Data[0].GitObjectID != commitA || body.Data[0].Mode != "100644" {
		t.Fatalf("first entry = %+v", body.Data[0])
	}
	if body.Data[1].Type != "tree" {
		t.Fatalf("second entry type = %q, want tree", body.Data[1].Type)
	}
	if body.NextCursor != "src/b" || !body.HasMore {
		t.Fatalf("page = cursor %q hasMore %v, want src/b/true", body.NextCursor, body.HasMore)
	}
	if len(query.treeCalls) != 1 {
		t.Fatalf("Tree calls = %+v, want one", query.treeCalls)
	}
	if got := query.treeCalls[0]; got != (treeCall{RepoID: 7, Prefix: "src", Commit: commitA, Cursor: "src/a.go", Limit: 2}) {
		t.Fatalf("Tree call = %+v", got)
	}
}

// TestTreeDefaultsPageLimit proves the API's 20-entry default is what the
// application service receives when the caller omits limit.
func TestTreeDefaultsPageLimit(t *testing.T) {
	query := &fakeQueryService{treeFn: func(context.Context, int64, string, string, string, int) (domain.TreePage, error) {
		return domain.TreePage{}, nil
	}}
	handler := newTestHandler(t, HandlerDeps{Query: query})
	recorder := serve(t, handler, http.MethodGet, "/api/code/repos/7/tree", userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	if got := query.treeCalls[0].Limit; got != platform.DefaultLimit {
		t.Fatalf("limit = %d, want the API default %d", got, platform.DefaultLimit)
	}
}

// TestTreeRejectsInvalidLimit proves the shared limit bound is enforced before
// the service is called.
func TestTreeRejectsInvalidLimit(t *testing.T) {
	query := &fakeQueryService{}
	handler := newTestHandler(t, HandlerDeps{Query: query})
	recorder := serve(t, handler, http.MethodGet, "/api/code/repos/7/tree?limit=101", userToken)
	assertErrorResponse(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_limit")
	if len(query.treeCalls) != 0 {
		t.Fatalf("Tree calls = %+v, want none", query.treeCalls)
	}
}

// TestFileForwardsActorAndWindow proves the file endpoint forwards the
// authenticated actor (the QueryService records code.read with it) and renders
// the partial window as 200 (the service clamps start/end).
func TestFileForwardsActorAndWindow(t *testing.T) {
	window := domain.FileWindow{
		Commit:     commitA,
		Path:       "src/main.go",
		StartLine:  2,
		EndLine:    7,
		TotalLines: 40,
		Truncated:  true,
		Content:    "line2\nline3",
	}
	query := &fakeQueryService{fileFn: func(context.Context, int64, int64, string, string, int, int) (domain.FileWindow, error) {
		return window, nil
	}}
	handler := newTestHandler(t, HandlerDeps{Query: query})

	recorder := serve(t, handler, http.MethodGet,
		"/api/code/repos/7/file?commit="+commitA+"&path=src%2Fmain.go&startLine=2&endLine=7", userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data fileWindowWire `json:"data"`
	}
	decodeBody(t, recorder, &body)
	if body.Data.Commit != commitA || body.Data.Path != "src/main.go" {
		t.Fatalf("window = %+v", body.Data)
	}
	if body.Data.StartLine != 2 || body.Data.EndLine != 7 || body.Data.TotalLines != 40 || !body.Data.Truncated {
		t.Fatalf("window bounds = %+v", body.Data)
	}
	if len(query.fileCalls) != 1 {
		t.Fatalf("File calls = %+v, want one", query.fileCalls)
	}
	if got := query.fileCalls[0]; got != (fileCall{ActorID: testUserID, RepoID: 7, Commit: commitA, Path: "src/main.go", StartLine: 2, EndLine: 7}) {
		t.Fatalf("File call = %+v, want the authenticated actor and parsed window", got)
	}
}

// TestFileRejectsMissingPathAndMalformedLines proves query validation is strict
// and happens before the application service.
func TestFileRejectsMissingPathAndMalformedLines(t *testing.T) {
	t.Run("missing path is a 400", func(t *testing.T) {
		query := &fakeQueryService{}
		handler := newTestHandler(t, HandlerDeps{Query: query})
		recorder := serve(t, handler, http.MethodGet, "/api/code/repos/7/file?commit="+commitA, userToken)
		body := assertErrorResponse(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_request")
		if got, _ := body.Error.Details["field"].(string); got != "path" {
			t.Fatalf("details.field = %q, want path", got)
		}
		if len(query.fileCalls) != 0 {
			t.Fatalf("File calls = %+v, want none", query.fileCalls)
		}
	})

	t.Run("non-numeric startLine is a 400", func(t *testing.T) {
		query := &fakeQueryService{}
		handler := newTestHandler(t, HandlerDeps{Query: query})
		recorder := serve(t, handler, http.MethodGet,
			"/api/code/repos/7/file?path=src%2Fmain.go&startLine=first", userToken)
		body := assertErrorResponse(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_line")
		if got, _ := body.Error.Details["field"].(string); got != "startLine" {
			t.Fatalf("details.field = %q, want startLine", got)
		}
	})
}

// TestFileReasonMapping pins the 422 reasons the file read can surface and the
// 503 git_unavailable operational mapping (R19).
func TestFileReasonMapping(t *testing.T) {
	testCases := []struct {
		name   string
		err    error
		status int
		code   string
		reason string
	}{
		{
			name:   "path escape",
			err:    domain.NewInvalid("path", "path_escape", "codeintel: path escapes the repository root"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "path_escape",
		},
		{
			name:   "unsupported content",
			err:    domain.NewInvalid("path", "unsupported_content", "codeintel: content is binary"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "unsupported_content",
		},
		{
			name:   "path not found",
			err:    domain.NewInvalid("path", "path_not_found", "codeintel: path does not exist"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "path_not_found",
		},
		{
			name:   "invalid line range",
			err:    domain.NewInvalid("startLine", "invalid_line_range", "codeintel: invalid line window"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "invalid_line_range",
		},
		{
			name:   "operational git failure",
			err:    errors.New("gitx: git show failed with exit code 128"),
			status: http.StatusServiceUnavailable,
			code:   platform.CodeUnavailable,
			reason: "git_unavailable",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			query := &fakeQueryService{fileFn: func(context.Context, int64, int64, string, string, int, int) (domain.FileWindow, error) {
				return domain.FileWindow{}, testCase.err
			}}
			handler := newTestHandler(t, HandlerDeps{Query: query})
			recorder := serve(t, handler, http.MethodGet, "/api/code/repos/7/file?path=src%2Fmain.go", userToken)
			assertErrorResponse(t, recorder, testCase.status, testCase.code, testCase.reason)
		})
	}
}

// TestDiffChangeListTranslatesStats proves R7: the ChangeList is embedded in the
// data field, the gitx changedFiles stats key translates to files, the cursor is
// the last path and hasMore is derived from len(files)==limit (not Truncated).
func TestDiffChangeListTranslatesStats(t *testing.T) {
	oldPath := "src/old.go"
	files := []domain.ChangedFile{
		{Path: "src/a.go", Status: "M", Additions: 3, Deletions: 1, HeadBlobHash: "git-sha1:" + commitA},
		{Path: "src/b.go", Status: "R100", OldPath: &oldPath, Additions: 2, HeadBlobHash: "git-sha1:" + commitB},
	}
	query := &fakeQueryService{diffFn: func(context.Context, int64, string, string, int, string) (domain.ChangeSetData, error) {
		return domain.ChangeSetData{
			Stats:     []byte(`{"additions":5,"changedFiles":2,"deletions":1,"truncated":false}`),
			Files:     files,
			Truncated: false,
		}, nil
	}}
	handler := newTestHandler(t, HandlerDeps{Query: query})

	recorder := serve(t, handler, http.MethodGet,
		"/api/code/repos/7/diff?base="+commitA+"&head="+commitB+"&limit=2", userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data changeListWire `json:"data"`
	}
	decodeBody(t, recorder, &body)
	if body.Data.ChangeSetID != nil {
		t.Fatalf("changeSetId = %v, want null for the read-only preview", *body.Data.ChangeSetID)
	}
	if body.Data.BaseCommit != commitA || body.Data.HeadCommit != commitB {
		t.Fatalf("commits = %q..%q", body.Data.BaseCommit, body.Data.HeadCommit)
	}
	if body.Data.Stats.Files != 2 {
		t.Fatalf("stats.files = %d, want the translated changedFiles=2", body.Data.Stats.Files)
	}
	if body.Data.Stats.Additions != 5 || body.Data.Stats.Deletions != 1 || body.Data.Stats.Truncated {
		t.Fatalf("stats = %+v", body.Data.Stats)
	}
	if len(body.Data.Files) != 2 || body.Data.Files[1].OldPath == nil || *body.Data.Files[1].OldPath != "src/old.go" {
		t.Fatalf("files = %+v, want the rename oldPath", body.Data.Files)
	}
	if body.Data.NextCursor == nil || *body.Data.NextCursor != "src/b.go" {
		t.Fatalf("nextCursor = %v, want the last page path", body.Data.NextCursor)
	}
	if !body.Data.HasMore {
		t.Fatal("hasMore = false, want true when len(files)==limit")
	}
	if got := query.diffCalls; len(got) != 1 || got[0] != (diffCall{RepoID: 7, Base: commitA, Head: commitB, Limit: 2}) {
		t.Fatalf("Diff call = %+v", got)
	}
}

// TestDiffHasMoreIgnoresTruncated proves hasMore is derived from the page size,
// not from the change-set truncation flag, and that a short page has no cursor.
func TestDiffHasMoreIgnoresTruncated(t *testing.T) {
	query := &fakeQueryService{diffFn: func(context.Context, int64, string, string, int, string) (domain.ChangeSetData, error) {
		return domain.ChangeSetData{
			Stats:     []byte(`{"changedFiles":1,"truncated":true}`),
			Files:     []domain.ChangedFile{{Path: "src/a.go", Status: "M"}},
			Truncated: true,
		}, nil
	}}
	handler := newTestHandler(t, HandlerDeps{Query: query})
	recorder := serve(t, handler, http.MethodGet,
		"/api/code/repos/7/diff?base="+commitA+"&head="+commitB+"&limit=20", userToken)
	var body struct {
		Data changeListWire `json:"data"`
	}
	decodeBody(t, recorder, &body)
	if body.Data.HasMore {
		t.Fatal("hasMore = true, want false for a short page (Truncated alone must not drive it)")
	}
	if body.Data.NextCursor != nil {
		t.Fatalf("nextCursor = %v, want null for the last page", *body.Data.NextCursor)
	}
	if !body.Data.Stats.Truncated {
		t.Fatal("stats.truncated = false, want the change-set truncation flag preserved")
	}
	if body.Data.Stats.Files != 1 {
		t.Fatalf("stats.files = %d, want 1", body.Data.Stats.Files)
	}
}

// TestDiffRequiresBaseAndHead proves the change list requires both commits (the
// DTO must report the resolved head, which the read-only preview cannot infer
// when head is omitted).
func TestDiffRequiresBaseAndHead(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		target string
		field  string
	}{
		{name: "missing base", target: "/api/code/repos/7/diff?head=" + commitB, field: "base"},
		{name: "missing head", target: "/api/code/repos/7/diff?base=" + commitA, field: "head"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			query := &fakeQueryService{}
			handler := newTestHandler(t, HandlerDeps{Query: query})
			recorder := serve(t, handler, http.MethodGet, testCase.target, userToken)
			body := assertErrorResponse(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_request")
			if got, _ := body.Error.Details["field"].(string); got != testCase.field {
				t.Fatalf("details.field = %q, want %q", got, testCase.field)
			}
			if len(query.diffCalls) != 0 {
				t.Fatalf("Diff calls = %+v, want none", query.diffCalls)
			}
		})
	}
}

// TestFileDiffBinaryReturnsEmptyPatch200 pins R18: a binary changed file is a
// 200 with binary=true and an empty patch, never a 422, so the UI can render the
// marker and keep working.
func TestFileDiffBinaryReturnsEmptyPatch200(t *testing.T) {
	diff := domain.FileDiff{
		BaseCommit: commitA,
		HeadCommit: commitB,
		Path:       "assets/logo.png",
		Status:     "M",
		Binary:     true,
		Patch:      "",
	}
	query := &fakeQueryService{fileDiffFn: func(context.Context, int64, string, string, string) (domain.FileDiff, error) {
		return diff, nil
	}}
	handler := newTestHandler(t, HandlerDeps{Query: query})

	recorder := serve(t, handler, http.MethodGet,
		"/api/code/repos/7/diff/file?base="+commitA+"&head="+commitB+"&path=assets%2Flogo.png", userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a binary file (body %s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data fileDiffWire `json:"data"`
	}
	decodeBody(t, recorder, &body)
	if !body.Data.Binary || body.Data.Patch != "" {
		t.Fatalf("diff = %+v, want binary=true with an empty patch", body.Data)
	}
	if body.Data.BaseCommit != commitA || body.Data.HeadCommit != commitB {
		t.Fatalf("commit pair = %q..%q", body.Data.BaseCommit, body.Data.HeadCommit)
	}
	if got := query.fileDiffCalls; len(got) != 1 || got[0] != (fileDiffCall{RepoID: 7, Base: commitA, Head: commitB, Path: "assets/logo.png"}) {
		t.Fatalf("FileDiff call = %+v", got)
	}
}

// TestFileDiffReasonMapping pins not_changed/path_escape 422 reasons and the
// strict query validation.
func TestFileDiffReasonMapping(t *testing.T) {
	t.Run("path outside the change set is 422 not_changed", func(t *testing.T) {
		query := &fakeQueryService{fileDiffFn: func(context.Context, int64, string, string, string) (domain.FileDiff, error) {
			return domain.FileDiff{}, domain.NewInvalid("path", "not_changed", "codeintel: path is not part of the change set")
		}}
		handler := newTestHandler(t, HandlerDeps{Query: query})
		recorder := serve(t, handler, http.MethodGet,
			"/api/code/repos/7/diff/file?base="+commitA+"&head="+commitB+"&path=src%2Fa.go", userToken)
		assertErrorResponse(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "not_changed")
	})

	t.Run("path escape is 422", func(t *testing.T) {
		query := &fakeQueryService{fileDiffFn: func(context.Context, int64, string, string, string) (domain.FileDiff, error) {
			return domain.FileDiff{}, domain.NewInvalid("path", "path_escape", "codeintel: path escapes the repository root")
		}}
		handler := newTestHandler(t, HandlerDeps{Query: query})
		recorder := serve(t, handler, http.MethodGet,
			"/api/code/repos/7/diff/file?base="+commitA+"&head="+commitB+"&path=..%2Fsecret", userToken)
		assertErrorResponse(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "path_escape")
	})

	t.Run("missing path is a 400", func(t *testing.T) {
		query := &fakeQueryService{}
		handler := newTestHandler(t, HandlerDeps{Query: query})
		recorder := serve(t, handler, http.MethodGet,
			"/api/code/repos/7/diff/file?base="+commitA+"&head="+commitB, userToken)
		body := assertErrorResponse(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_request")
		if got, _ := body.Error.Details["field"].(string); got != "path" {
			t.Fatalf("details.field = %q, want path", got)
		}
	})
}

// TestFactsHonorsKindAndLimit proves the facts endpoint forwards kind and
// limit=50 (the T11 FactsPanel contract), validates the opaque cursor, and
// serializes CodeEntity ids as decimal strings while repoId stays a number (R6).
func TestFactsHonorsKindAndLimit(t *testing.T) {
	startLine, endLine := 10, 20
	factKey := "api:POST /api/refund"
	factVersion := 3
	analysisID := int64(7002)
	entity := domain.CodeEntity{
		ID:          5001,
		RepoID:      3,
		Commit:      commitA,
		Kind:        domain.KindAPI,
		Path:        "src/router.ts",
		Name:        "POST /api/refund",
		StartLine:   &startLine,
		EndLine:     &endLine,
		Signature:   "router.post('/api/refund')",
		BlobHash:    "git-sha1:" + commitA,
		Source:      domain.SourceAI,
		Confidence:  domain.ConfidenceHigh,
		Evidence:    []domain.Evidence{{File: "src/router.ts", StartLine: 10, EndLine: 12, ExcerptHash: "sha256:abc"}},
		FactKey:     &factKey,
		FactVersion: &factVersion,
		AnalysisID:  &analysisID,
		CreatedAt:   startedAt,
		UpdatedAt:   finishedAt,
	}
	cursor := platform.EncodeCursor(5001)
	query := &fakeQueryService{factsFn: func(context.Context, int64, domain.EntityFilter) ([]domain.CodeEntity, string, bool, error) {
		return []domain.CodeEntity{entity}, "next-cursor", true, nil
	}}
	handler := newTestHandler(t, HandlerDeps{Query: query})

	recorder := serve(t, handler, http.MethodGet,
		"/api/code/repos/3/facts?commit="+commitA+"&kind=api&path=src&limit=50&cursor="+url.QueryEscape(cursor), userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body entityPageWire
	decodeBody(t, recorder, &body)
	if len(body.Data) != 1 {
		t.Fatalf("entities = %d, want 1", len(body.Data))
	}
	got := body.Data[0]
	if got.ID != "5001" {
		t.Fatalf("id = %q, want the decimal string \"5001\"", got.ID)
	}
	if got.RepoID != 3 {
		t.Fatalf("repoId = %d, want the P2 numeric id", got.RepoID)
	}
	if got.AnalysisID == nil || *got.AnalysisID != "7002" {
		t.Fatalf("analysisId = %v, want the decimal string \"7002\"", got.AnalysisID)
	}
	if got.FactVersion == nil || *got.FactVersion != 3 || got.FactKey == nil || *got.FactKey != factKey {
		t.Fatalf("fact identity = %+v", got)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].File != "src/router.ts" ||
		got.Evidence[0].StartLine != 10 || got.Evidence[0].ExcerptHash != "sha256:abc" {
		t.Fatalf("evidence = %+v, want the camelCase evidence range", got.Evidence)
	}
	if body.NextCursor != "next-cursor" || !body.HasMore {
		t.Fatalf("page = cursor %q hasMore %v", body.NextCursor, body.HasMore)
	}
	if got := query.factsCalls; len(got) != 1 {
		t.Fatalf("Facts calls = %+v, want one", got)
	}
	wantFilter := domain.EntityFilter{Commit: commitA, Kind: "api", Path: "src", Limit: 50, Cursor: cursor}
	if got := query.factsCalls[0]; got.RepoID != 3 || got.Filter != wantFilter {
		t.Fatalf("Facts call = %+v, want %+v", got, wantFilter)
	}
}

// TestFactsDefaultsLimitAndRejectsMalformedCursor pins the API default limit and
// the opaque cursor validation.
func TestFactsDefaultsLimitAndRejectsMalformedCursor(t *testing.T) {
	t.Run("default limit is 20", func(t *testing.T) {
		query := &fakeQueryService{factsFn: func(context.Context, int64, domain.EntityFilter) ([]domain.CodeEntity, string, bool, error) {
			return nil, "", false, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Query: query})
		recorder := serve(t, handler, http.MethodGet, "/api/code/repos/3/facts", userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		if got := query.factsCalls[0].Filter.Limit; got != platform.DefaultLimit {
			t.Fatalf("limit = %d, want %d", got, platform.DefaultLimit)
		}
	})

	t.Run("malformed cursor is a 400", func(t *testing.T) {
		query := &fakeQueryService{}
		handler := newTestHandler(t, HandlerDeps{Query: query})
		recorder := serve(t, handler, http.MethodGet, "/api/code/repos/3/facts?cursor=not%20base64", userToken)
		assertErrorResponse(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_cursor")
		if len(query.factsCalls) != 0 {
			t.Fatalf("Facts calls = %+v, want none", query.factsCalls)
		}
	})
}

// TestSearchForwardsSubstringQuery proves the search endpoint forwards q/kind/
// cursor as a path-or-name substring query and renders the ApiPage.
func TestSearchForwardsSubstringQuery(t *testing.T) {
	cursor := platform.EncodeCursor(77)
	query := &fakeQueryService{searchFn: func(context.Context, int64, string, string, string, int, string) ([]domain.CodeEntity, string, bool, error) {
		return []domain.CodeEntity{{ID: 5001, RepoID: 3, Commit: commitA, Kind: domain.KindRoute, Path: "src/router.ts", Name: "/orders"}}, "", false, nil
	}}
	handler := newTestHandler(t, HandlerDeps{Query: query})

	recorder := serve(t, handler, http.MethodGet,
		"/api/code/repos/3/search?commit="+commitA+"&q=refund%20api&kind=api&limit=10&cursor="+url.QueryEscape(cursor), userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body entityPageWire
	decodeBody(t, recorder, &body)
	if len(body.Data) != 1 || body.Data[0].ID != "5001" {
		t.Fatalf("data = %+v, want one entity with a string id", body.Data)
	}
	if body.HasMore || body.NextCursor != "" {
		t.Fatalf("page = cursor %q hasMore %v, want empty/false", body.NextCursor, body.HasMore)
	}
	want := searchCall{RepoID: 3, Commit: commitA, Query: "refund api", Kind: "api", Limit: 10, Cursor: cursor}
	if got := query.searchCalls; len(got) != 1 || got[0] != want {
		t.Fatalf("Search call = %+v, want %+v", got, want)
	}
}

// TestCommitMetaAndReasonMapping covers GET /commits/{sha}: the DTO shape and
// the documented 422 invalid_head / commit_not_found plus 503 git_unavailable
// mappings.
func TestCommitMetaAndReasonMapping(t *testing.T) {
	t.Run("known commit returns sha/message/author/committedAt", func(t *testing.T) {
		info := domain.CommitInfo{
			SHA:         commitA,
			Message:     "fix refund reason",
			Author:      "QA Bot",
			CommittedAt: startedAt,
		}
		query := &fakeQueryService{commitFn: func(context.Context, int64, string) (domain.CommitInfo, error) {
			return info, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Query: query})
		recorder := serve(t, handler, http.MethodGet, "/api/code/repos/3/commits/"+commitA, userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data commitWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.SHA != commitA || body.Data.Message != "fix refund reason" || body.Data.Author != "QA Bot" {
			t.Fatalf("commit = %+v", body.Data)
		}
		if body.Data.CommittedAt == "" {
			t.Fatal("committedAt is empty, want an ISO timestamp")
		}
		if got := query.commitCalls; len(got) != 1 || got[0] != (commitCall{RepoID: 3, SHA: commitA}) {
			t.Fatalf("Commit call = %+v", got)
		}
	})

	testCases := []struct {
		name   string
		sha    string
		err    error
		status int
		code   string
		reason string
	}{
		{
			name:   "malformed sha maps to invalid_head",
			sha:    "not-a-sha",
			err:    domain.NewInvalid("sha", "invalid_head", "codeintel: commit sha must be a lowercase 40-hex commit id"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "invalid_head",
		},
		{
			name:   "unknown commit maps to commit_not_found",
			sha:    commitB,
			err:    domain.NewInvalid("commit", "commit_not_found", "codeintel: commit does not resolve in this repository"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "commit_not_found",
		},
		{
			name:   "operational failure maps to 503 git_unavailable",
			sha:    commitB,
			err:    errors.New("gitx: git show failed with exit code 128"),
			status: http.StatusServiceUnavailable,
			code:   platform.CodeUnavailable,
			reason: "git_unavailable",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			query := &fakeQueryService{commitFn: func(context.Context, int64, string) (domain.CommitInfo, error) {
				return domain.CommitInfo{}, testCase.err
			}}
			handler := newTestHandler(t, HandlerDeps{Query: query})
			recorder := serve(t, handler, http.MethodGet, "/api/code/repos/3/commits/"+testCase.sha, userToken)
			assertErrorResponse(t, recorder, testCase.status, testCase.code, testCase.reason)
		})
	}
}

// TestQueryRoutesRequireAuthentication proves the read routes are behind the
// login middleware.
func TestQueryRoutesRequireAuthentication(t *testing.T) {
	handler := newTestHandler(t, HandlerDeps{Query: &fakeQueryService{}})
	targets := []string{
		"/api/code/repos/3/tree",
		"/api/code/repos/3/file?path=src%2Fmain.go",
		"/api/code/repos/3/diff?base=" + commitA + "&head=" + commitB,
		"/api/code/repos/3/diff/file?base=" + commitA + "&head=" + commitB + "&path=src%2Fmain.go",
		"/api/code/repos/3/facts",
		"/api/code/repos/3/search?q=refund",
		"/api/code/repos/3/commits/" + commitA,
	}
	for _, target := range targets {
		recorder := serve(t, handler, http.MethodGet, target, "")
		assertErrorResponse(t, recorder, http.StatusUnauthorized, platform.CodeUnauthenticated, "")
	}
}
