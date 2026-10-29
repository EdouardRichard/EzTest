package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// fileListEntry mirrors the JSON objects file.list returns.
type fileListEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// fileSchemaProp is the subset of a property schema the file tools pin.
type fileSchemaProp struct {
	Type    string `json:"type"`
	Minimum *int   `json:"minimum"`
	Maximum *int   `json:"maximum"`
}

// fileToolSchema is the subset of the file tools' input schema tested here.
type fileToolSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]fileSchemaProp `json:"properties"`
	Required   []string                  `json:"required"`
}

// executeTool runs one tool with raw JSON arguments.
func executeTool(t *testing.T, tool domain.Tool, args string) (domain.ToolResult, error) {
	t.Helper()
	return tool.Execute(context.Background(), json.RawMessage(args))
}

// requireToolErrorCode asserts Execute returned a *domain.ToolError with code.
func requireToolErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Execute() error=nil want *domain.ToolError with code %q", want)
	}
	var toolErr *domain.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("Execute() err=%v want *domain.ToolError", err)
	}
	if toolErr.Code != want {
		t.Fatalf("ToolError.Code=%q want %q (message %q)", toolErr.Code, want, toolErr.Message)
	}
}

// writeTestFile creates rel (slash separated) under root with content.
func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent of %q: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", rel, err)
	}
}

// writeTestPath creates content at an absolute path outside any tool root.
func writeTestPath(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent of %q: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

// newTestFileTools builds the file tools over root and returns list and read.
func newTestFileTools(t *testing.T, root string) (domain.Tool, domain.Tool) {
	t.Helper()
	tools, err := NewFileTools(root)
	if err != nil {
		t.Fatalf("NewFileTools(%q) error: %v", root, err)
	}
	if len(tools) != 2 {
		t.Fatalf("NewFileTools(%q) returned %d tools, want 2", root, len(tools))
	}
	var list, read domain.Tool
	for _, tool := range tools {
		switch tool.Name() {
		case "file.list":
			list = tool
		case "file.read":
			read = tool
		}
	}
	if list == nil || read == nil {
		t.Fatalf("NewFileTools(%q) tools=%v want file.list and file.read", root, tools)
	}
	return list, read
}

// decodeFileList decodes a successful file.list result.
func decodeFileList(t *testing.T, result domain.ToolResult) []fileListEntry {
	t.Helper()
	if result.IsError {
		t.Fatalf("file.list result is an error: %q", result.Content)
	}
	var entries []fileListEntry
	if err := json.Unmarshal([]byte(result.Content), &entries); err != nil {
		t.Fatalf("file.list content %q is not a JSON array: %v", result.Content, err)
	}
	return entries
}

// fileListNames projects the entry names preserving order.
func fileListNames(entries []fileListEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name
	}
	return names
}

func TestFileToolsUnavailableWithoutRoot(t *testing.T) {
	tools, err := NewFileTools("")
	if err != nil {
		t.Fatalf("NewFileTools(\"\") error: %v, want nil (file tools disabled)", err)
	}
	if tools != nil {
		t.Fatalf("NewFileTools(\"\") tools=%v want nil (file tools disabled)", tools)
	}
}

func TestFileToolsRequireAnExistingDirectory(t *testing.T) {
	base := t.TempDir()

	if _, err := NewFileTools(filepath.Join(base, "missing")); err == nil {
		t.Fatal("NewFileTools accepted a missing root")
	}

	file := filepath.Join(base, "not-a-dir.txt")
	writeTestPath(t, file, "x")
	if _, err := NewFileTools(file); err == nil {
		t.Fatal("NewFileTools accepted a regular file as root")
	}
}

func TestFileToolsExposeReadOnlySchemas(t *testing.T) {
	tools, err := NewFileTools(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileTools() error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("NewFileTools() returned %d tools, want 2", len(tools))
	}
	if tools[0].Name() != "file.list" || tools[1].Name() != "file.read" {
		t.Fatalf("tool order=%q,%q want file.list,file.read", tools[0].Name(), tools[1].Name())
	}
	for _, tool := range tools {
		if tool.Description() == "" {
			t.Fatalf("%s Description() is empty", tool.Name())
		}
	}

	var listSchema fileToolSchema
	if err := json.Unmarshal(tools[0].InputSchema(), &listSchema); err != nil {
		t.Fatalf("file.list schema is not JSON: %v", err)
	}
	if listSchema.Type != "object" {
		t.Fatalf("file.list schema type=%q want object", listSchema.Type)
	}
	if got := listSchema.Properties["path"].Type; got != "string" {
		t.Fatalf("file.list path type=%q want string", got)
	}
	depth := listSchema.Properties["depth"]
	if depth.Type != "integer" || depth.Minimum == nil || *depth.Minimum != 1 || depth.Maximum == nil || *depth.Maximum != 3 {
		t.Fatalf("file.list depth schema=%+v want integer 1..3", depth)
	}
	if len(listSchema.Required) != 0 {
		t.Fatalf("file.list required=%v want none", listSchema.Required)
	}

	var readSchema fileToolSchema
	if err := json.Unmarshal(tools[1].InputSchema(), &readSchema); err != nil {
		t.Fatalf("file.read schema is not JSON: %v", err)
	}
	if readSchema.Type != "object" {
		t.Fatalf("file.read schema type=%q want object", readSchema.Type)
	}
	if got := readSchema.Properties["path"].Type; got != "string" {
		t.Fatalf("file.read path type=%q want string", got)
	}
	offset := readSchema.Properties["offset"]
	if offset.Type != "integer" || offset.Minimum == nil || *offset.Minimum != 1 {
		t.Fatalf("file.read offset schema=%+v want integer >=1", offset)
	}
	limit := readSchema.Properties["limit"]
	if limit.Type != "integer" || limit.Minimum == nil || *limit.Minimum != 1 || limit.Maximum == nil || *limit.Maximum != 2000 {
		t.Fatalf("file.read limit schema=%+v want integer 1..2000", limit)
	}
	if !slices.Equal(readSchema.Required, []string{"path"}) {
		t.Fatalf("file.read required=%v want [path]", readSchema.Required)
	}
}

func TestFileReadReadsTextWithLineHeader(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "hello.txt", "line one\nline two\nline three\n")
	writeTestFile(t, root, "empty.txt", "")
	_, read := newTestFileTools(t, root)

	result, err := executeTool(t, read, `{"path":"hello.txt"}`)
	if err != nil {
		t.Fatalf("file.read hello.txt error: %v", err)
	}
	if result.IsError {
		t.Fatalf("file.read hello.txt IsError=true content=%q", result.Content)
	}
	want := "hello.txt (lines 1-3 of 3)\nline one\nline two\nline three"
	if result.Content != want {
		t.Fatalf("file.read hello.txt content=%q want %q", result.Content, want)
	}

	// offset/limit select a 1-based inclusive line window.
	result, err = executeTool(t, read, `{"path":"hello.txt","offset":2,"limit":1}`)
	if err != nil {
		t.Fatalf("file.read offset/limit error: %v", err)
	}
	want = "hello.txt (lines 2-2 of 3)\nline two"
	if result.Content != want {
		t.Fatalf("file.read offset/limit content=%q want %q", result.Content, want)
	}

	// An empty file reports an empty line window rather than a fake line 1.
	result, err = executeTool(t, read, `{"path":"empty.txt"}`)
	if err != nil {
		t.Fatalf("file.read empty.txt error: %v", err)
	}
	if want := "empty.txt (lines 0-0 of 0)"; result.Content != want {
		t.Fatalf("file.read empty.txt content=%q want %q", result.Content, want)
	}

	// An offset past the last line yields an empty window, not a crash.
	result, err = executeTool(t, read, `{"path":"hello.txt","offset":10}`)
	if err != nil {
		t.Fatalf("file.read offset past end error: %v", err)
	}
	if want := "hello.txt (lines 0-0 of 3)"; result.Content != want {
		t.Fatalf("file.read offset past end content=%q want %q", result.Content, want)
	}

	// The limit is capped at the remaining lines.
	result, err = executeTool(t, read, `{"path":"hello.txt","offset":3,"limit":2000}`)
	if err != nil {
		t.Fatalf("file.read tail error: %v", err)
	}
	if want := "hello.txt (lines 3-3 of 3)\nline three"; result.Content != want {
		t.Fatalf("file.read tail content=%q want %q", result.Content, want)
	}
}

func TestFileReadRejectsEscapesAndSecrets(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeTestFile(t, root, "hello.txt", "hi\n")
	writeTestFile(t, base, "escape.txt", "outside secret")
	writeTestFile(t, root, ".env", "TOKEN=1")
	writeTestFile(t, root, ".env.local", "TOKEN=2")
	writeTestFile(t, root, ".secrets/token", "secret")
	writeTestFile(t, root, "notes.local.md", "private")
	writeTestFile(t, root, "sub/.git/config", "[core]")
	writeTestFile(t, root, "subdir/keep.txt", "keep")
	writeTestFile(t, root, "big.txt", strings.Repeat("a", 256*1024+1))
	writeTestFile(t, root, "nul.txt", "abc\x00def")
	writeTestFile(t, root, "invalid-utf8.txt", string([]byte{0xff, 0xfe, 0xfd}))

	_, read := newTestFileTools(t, root)
	absHello := filepath.Join(root, "hello.txt")

	cases := []struct {
		name string
		args string
		code string
	}{
		{name: "parent escape", args: `{"path":"../escape.txt"}`, code: "denied"},
		{name: "absolute path", args: fmt.Sprintf(`{"path":%q}`, absHello), code: "denied"},
		{name: "doubled escape", args: `{"path":"sub/../../escape.txt"}`, code: "denied"},
		{name: "env file", args: `{"path":".env"}`, code: "denied"},
		{name: "env variant", args: `{"path":".env.local"}`, code: "denied"},
		{name: "secrets directory", args: `{"path":".secrets/token"}`, code: "denied"},
		{name: "local notes", args: `{"path":"notes.local.md"}`, code: "denied"},
		{name: "nested git", args: `{"path":"sub/.git/config"}`, code: "denied"},
		{name: "nul in path", args: `{"path":"hel\u0000lo.txt"}`, code: "invalid_arguments"},
		{name: "missing file", args: `{"path":"missing.txt"}`, code: "not_found"},
		{name: "too large", args: `{"path":"big.txt"}`, code: "too_large"},
		{name: "nul bytes", args: `{"path":"nul.txt"}`, code: "invalid_arguments"},
		{name: "invalid utf8", args: `{"path":"invalid-utf8.txt"}`, code: "invalid_arguments"},
		{name: "directory", args: `{"path":"subdir"}`, code: "invalid_arguments"},
		{name: "empty path", args: `{"path":""}`, code: "invalid_arguments"},
		{name: "missing path", args: `{}`, code: "invalid_arguments"},
		{name: "offset zero", args: `{"path":"hello.txt","offset":0}`, code: "invalid_arguments"},
		{name: "negative offset", args: `{"path":"hello.txt","offset":-1}`, code: "invalid_arguments"},
		{name: "limit zero", args: `{"path":"hello.txt","limit":0}`, code: "invalid_arguments"},
		{name: "limit too high", args: `{"path":"hello.txt","limit":2001}`, code: "invalid_arguments"},
		{name: "malformed arguments", args: `{"path":`, code: "invalid_arguments"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := executeTool(t, read, tc.args)
			requireToolErrorCode(t, err, tc.code)
		})
	}
}

func TestFileReadRejectsSymlinkEscapes(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeTestFile(t, root, "hello.txt", "hello\n")
	writeTestFile(t, base, "outside.txt", "top secret")
	writeTestFile(t, base, "outside-dir/leak.txt", "leaked")

	escapeLink := filepath.Join(root, "escape.txt")
	if err := os.Symlink(filepath.Join(base, "outside.txt"), escapeLink); err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}
	dirLink := filepath.Join(root, "dirlink")
	if err := os.Symlink(filepath.Join(base, "outside-dir"), dirLink); err != nil {
		t.Skipf("cannot create a directory symlink on this platform: %v", err)
	}
	insideLink := filepath.Join(root, "inside.txt")
	if err := os.Symlink(filepath.Join(root, "hello.txt"), insideLink); err != nil {
		t.Skipf("cannot create a file symlink on this platform: %v", err)
	}

	_, read := newTestFileTools(t, root)

	if _, err := executeTool(t, read, `{"path":"escape.txt"}`); err == nil {
		t.Fatal("file.read followed a symlink out of the root")
	} else {
		requireToolErrorCode(t, err, "denied")
	}
	if _, err := executeTool(t, read, `{"path":"dirlink/leak.txt"}`); err == nil {
		t.Fatal("file.read followed a directory symlink out of the root")
	} else {
		requireToolErrorCode(t, err, "denied")
	}

	// A symlink that stays inside the root is still readable.
	result, err := executeTool(t, read, `{"path":"inside.txt"}`)
	if err != nil {
		t.Fatalf("file.read inside symlink error: %v", err)
	}
	if !strings.Contains(result.Content, "hello") {
		t.Fatalf("file.read inside symlink content=%q want the hello.txt contents", result.Content)
	}
}

func TestFileToolsResolveRootSymlinks(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real-root")
	writeTestFile(t, realRoot, "hello.txt", "hello\n")
	writeTestFile(t, base, "escape.txt", "outside")

	link := filepath.Join(base, "root-link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}

	_, read := newTestFileTools(t, link)
	result, err := executeTool(t, read, `{"path":"hello.txt"}`)
	if err != nil {
		t.Fatalf("file.read through a symlinked root error: %v", err)
	}
	if !strings.Contains(result.Content, "hello") {
		t.Fatalf("file.read through a symlinked root content=%q want hello", result.Content)
	}

	// The resolved root, not the unresolved link path, is the escape boundary.
	if _, err := executeTool(t, read, `{"path":"../escape.txt"}`); err == nil {
		t.Fatal("file.read escaped through the symlinked root")
	} else {
		requireToolErrorCode(t, err, "denied")
	}
}

func TestFileListStopsAtDeniedEntries(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "aaa")
	writeTestFile(t, root, ".env", "TOKEN=1")
	writeTestFile(t, root, "sub/b.txt", "bb")
	writeTestFile(t, root, "sub/.env", "TOKEN=2")
	writeTestFile(t, root, ".secrets/token", "secret")
	writeTestFile(t, root, ".git/config", "[core]")
	writeTestFile(t, root, "node_modules/pkg/index.js", "module.exports = 1")

	list, _ := newTestFileTools(t, root)

	result, err := executeTool(t, list, `{"depth":2}`)
	if err != nil {
		t.Fatalf("file.list depth=2 error: %v", err)
	}
	entries := decodeFileList(t, result)
	if want := []string{"a.txt", "sub", "sub/b.txt"}; !slices.Equal(fileListNames(entries), want) {
		t.Fatalf("file.list depth=2 names=%v want %v", fileListNames(entries), want)
	}
	if entries[0].Type != "file" || entries[0].Size != 3 {
		t.Fatalf("a.txt entry=%+v want type=file size=3", entries[0])
	}
	if entries[1].Type != "dir" {
		t.Fatalf("sub entry=%+v want type=dir", entries[1])
	}

	// depth defaults to 1.
	for _, args := range []string{`{}`, `{"depth":1}`, `{"path":".","depth":1}`} {
		result, err := executeTool(t, list, args)
		if err != nil {
			t.Fatalf("file.list %s error: %v", args, err)
		}
		if want := []string{"a.txt", "sub"}; !slices.Equal(fileListNames(decodeFileList(t, result)), want) {
			t.Fatalf("file.list %s names=%v want %v", args, fileListNames(decodeFileList(t, result)), want)
		}
	}

	for _, args := range []string{`{"depth":0}`, `{"depth":4}`, `{"depth":-1}`, `{"depth":"2"}`} {
		if _, err := executeTool(t, list, args); err == nil {
			t.Fatalf("file.list %s returned no error", args)
		} else {
			requireToolErrorCode(t, err, "invalid_arguments")
		}
	}
}

func TestFileListScopesToRequestedPath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "top.txt", "top")
	writeTestFile(t, root, "docs/a.md", "a")
	writeTestFile(t, root, "docs/nested/b.md", "b")
	writeTestFile(t, root, "docs/nested/deep/c.md", "c")

	list, _ := newTestFileTools(t, root)

	result, err := executeTool(t, list, `{"path":"docs","depth":3}`)
	if err != nil {
		t.Fatalf("file.list docs depth=3 error: %v", err)
	}
	want := []string{"a.md", "nested", "nested/b.md", "nested/deep", "nested/deep/c.md"}
	if got := fileListNames(decodeFileList(t, result)); !slices.Equal(got, want) {
		t.Fatalf("file.list docs depth=3 names=%v want %v", got, want)
	}

	result, err = executeTool(t, list, `{"path":"docs","depth":2}`)
	if err != nil {
		t.Fatalf("file.list docs depth=2 error: %v", err)
	}
	want = []string{"a.md", "nested", "nested/b.md", "nested/deep"}
	if got := fileListNames(decodeFileList(t, result)); !slices.Equal(got, want) {
		t.Fatalf("file.list docs depth=2 names=%v want %v", got, want)
	}
}

func TestFileListRejectsInvalidPaths(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeTestFile(t, root, "a.txt", "a")
	writeTestFile(t, base, "outside.txt", "outside")
	writeTestFile(t, root, ".git/config", "[core]")

	list, _ := newTestFileTools(t, root)
	absRoot := filepath.Join(root, "sub")
	absRoot, err := filepath.Abs(absRoot)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}

	cases := []struct {
		name string
		args string
		code string
	}{
		{name: "parent escape", args: `{"path":".."}`, code: "denied"},
		{name: "absolute path", args: fmt.Sprintf(`{"path":%q}`, absRoot), code: "denied"},
		{name: "denied directory", args: `{"path":".git"}`, code: "denied"},
		{name: "missing directory", args: `{"path":"missing"}`, code: "not_found"},
		{name: "file instead of directory", args: `{"path":"a.txt"}`, code: "invalid_arguments"},
		{name: "malformed arguments", args: `{"path":`, code: "invalid_arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := executeTool(t, list, tc.args); err == nil {
				t.Fatalf("file.list %s returned no error", tc.args)
			} else {
				requireToolErrorCode(t, err, tc.code)
			}
		})
	}
}

func TestFileListCapsEntriesAt200(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 205; i++ {
		writeTestFile(t, root, fmt.Sprintf("f%03d.txt", i), "x")
	}

	list, _ := newTestFileTools(t, root)
	result, err := executeTool(t, list, `{"depth":1}`)
	if err != nil {
		t.Fatalf("file.list error: %v", err)
	}
	entries := decodeFileList(t, result)
	if len(entries) != 200 {
		t.Fatalf("file.list returned %d entries, want the 200 entry cap", len(entries))
	}
	if entries[0].Name != "f000.txt" || entries[199].Name != "f199.txt" {
		t.Fatalf("file.list first=%q last=%q want f000.txt..f199.txt", entries[0].Name, entries[199].Name)
	}
}

// TestFileRootContainmentPredicates covers the post-EvalSymlinks containment
// boundary and the denied-name rules directly. The end-to-end symlink tests
// skip on hosts that refuse symlink creation (the Windows test host does), so
// this keeps the prefix boundary and case-insensitive deny list exercised.
func TestFileRootContainmentPredicates(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create root: %v", err)
	}
	tools, err := NewFileTools(root)
	if err != nil {
		t.Fatalf("NewFileTools() error: %v", err)
	}
	list, ok := tools[0].(fileList)
	if !ok {
		t.Fatalf("first tool is %T, want fileList", tools[0])
	}
	r := list.root

	if !r.within(r.root) {
		t.Fatal("within(root) = false, want true")
	}
	if !r.within(filepath.Join(r.root, "a", "b.txt")) {
		t.Fatal("within(root/a/b.txt) = false, want true")
	}
	// A sibling whose name merely starts with the root's name is not inside.
	if r.within(filepath.Join(base, "root2", "x")) {
		t.Fatal("within(root2/x) = true, want false: prefix check must respect the separator")
	}
	if r.within(filepath.Join(base, "other")) {
		t.Fatal("within(other) = true, want false")
	}

	denied := []string{
		"sub/.git/config",
		"SUB/.ENV.LOCAL",
		"docs/.env.example",
		"src/NOTES.local.md",
		"node_modules/pkg/index.js",
		".secrets/token",
	}
	for _, rel := range denied {
		if !deniedFilePath(rel) {
			t.Fatalf("deniedFilePath(%q) = false, want true", rel)
		}
	}
	allowed := []string{"src/main.go", "docs/README.md", "notes/local.md", "tmp/file.txt", "env.example"}
	for _, rel := range allowed {
		if deniedFilePath(rel) {
			t.Fatalf("deniedFilePath(%q) = true, want false", rel)
		}
	}
}
