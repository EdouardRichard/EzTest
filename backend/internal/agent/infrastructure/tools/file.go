package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// Tool error codes used by the file and HTTP tools. The executor maps them to
// the execution status reported in the tool_result activity.
const (
	toolErrorCodeInvalidArguments   = "invalid_arguments"
	toolErrorCodeDenied             = "denied"
	toolErrorCodeNotFound           = "not_found"
	toolErrorCodeTooLarge           = "too_large"
	toolErrorCodeUnsupportedContent = "unsupported_content"
	toolErrorCodeTimeout            = "timeout"
	toolErrorCodeInternal           = "internal"
)

// Limits of the read-only file tools (design spec §8.3–§8.4).
const (
	maxFileReadBytes   = 256 * 1024
	maxFileReadLimit   = 2000
	binaryProbeBytes   = 8 * 1024
	maxFileListEntries = 200
	maxFileListDepth   = 3
)

// Input schemas of the file tools.
const (
	fileListSchema = `{"type":"object","properties":{"path":{"type":"string"},"depth":{"type":"integer","minimum":1,"maximum":3}}}`
	fileReadSchema = `{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1,"maximum":2000}},"required":["path"]}`
)

// fileDeniedSegments are directory or file names the agent may never read. The
// comparison is case-insensitive because the runtime also targets Windows.
var fileDeniedSegments = map[string]struct{}{
	".git":         {},
	".tools":       {},
	".data":        {},
	".tmp":         {},
	"node_modules": {},
	".next":        {},
	".secrets":     {},
}

// newToolError builds a typed, model-facing tool failure.
func newToolError(code, message string) *domain.ToolError {
	return &domain.ToolError{Code: code, Message: message}
}

// NewFileTools returns the read-only file tools rooted at root. It returns
// (nil, nil) when root is empty, which disables the file tools entirely.
//
// The root is resolved through filepath.Abs and filepath.EvalSymlinks at
// construction time, so every later access is checked against the resolved
// directory and the configured path cannot be swapped by a symlink.
func NewFileTools(root string) ([]domain.Tool, error) {
	if root == "" {
		return nil, nil
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("file tools: resolve root %q: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("file tools: resolve root %q: %w", root, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("file tools: stat root %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("file tools: root %q is not a directory", root)
	}

	r := fileRoot{root: filepath.Clean(resolved)}
	r.prefix = r.root
	if !strings.HasSuffix(r.prefix, string(os.PathSeparator)) {
		r.prefix += string(os.PathSeparator)
	}
	return []domain.Tool{fileList{root: r}, fileRead{root: r}}, nil
}

// fileRoot owns the resolved root directory and the containment checks shared
// by file.list and file.read.
type fileRoot struct {
	// root is the symlink-resolved absolute root directory.
	root string
	// prefix is root with exactly one trailing separator, used for the prefix
	// check (filepath.Rel is not enough on its own: "C:\root2" must not pass
	// for a root of "C:\root").
	prefix string
}

// within reports whether path, which must be absolute, is root or below it.
func (r fileRoot) within(path string) bool {
	return path == r.root || strings.HasPrefix(path, r.prefix)
}

// resolve validates a model-supplied relative path and returns its
// symlink-resolved absolute path plus the slash-separated path used in results.
func (r fileRoot) resolve(raw string) (resolved, display string, err error) {
	if raw == "" {
		return "", "", newToolError(toolErrorCodeInvalidArguments, "path must not be empty")
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", "", newToolError(toolErrorCodeInvalidArguments, "path must not contain NUL bytes")
	}
	if absolutePath(raw) {
		return "", "", newToolError(toolErrorCodeDenied, fmt.Sprintf("path %q must be relative to the tool root", raw))
	}

	rel := filepath.Clean(raw)
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", "", newToolError(toolErrorCodeDenied, fmt.Sprintf("path %q escapes the tool root", raw))
	}
	if deniedFilePath(rel) {
		return "", "", newToolError(toolErrorCodeDenied, fmt.Sprintf("path %q is not readable", raw))
	}

	resolved, err = filepath.EvalSymlinks(filepath.Join(r.root, rel))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", newToolError(toolErrorCodeNotFound, fmt.Sprintf("path %q does not exist", raw))
		}
		return "", "", newToolError(toolErrorCodeInternal, fmt.Sprintf("cannot resolve path %q: %v", raw, err))
	}
	if !r.within(resolved) {
		return "", "", newToolError(toolErrorCodeDenied, fmt.Sprintf("path %q escapes the tool root", raw))
	}

	// A symlink inside the root may still point into a denied location; check
	// the resolved path too.
	resolvedRel, relErr := filepath.Rel(r.root, resolved)
	if relErr != nil {
		return "", "", newToolError(toolErrorCodeInternal, fmt.Sprintf("cannot inspect resolved path %q: %v", raw, relErr))
	}
	if deniedFilePath(resolvedRel) {
		return "", "", newToolError(toolErrorCodeDenied, fmt.Sprintf("path %q resolves into a denied location", raw))
	}
	return resolved, filepath.ToSlash(rel), nil
}

// absolutePath reports whether p addresses an absolute location. It rejects
// drive-relative Windows paths ("C:foo") and separator-rooted paths ("\foo")
// that filepath.IsAbs alone does not treat as absolute.
func absolutePath(p string) bool {
	return filepath.IsAbs(p) ||
		filepath.VolumeName(p) != "" ||
		strings.HasPrefix(p, string(os.PathSeparator)) ||
		strings.HasPrefix(p, "/")
}

// deniedFilePath reports whether any segment of a slash- or OS-separated
// relative path is denied.
func deniedFilePath(rel string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
		if segment == "" || segment == "." || segment == ".." {
			continue
		}
		if deniedFileName(segment) {
			return true
		}
	}
	return false
}

// deniedFileName applies the denied-name rules to one path segment.
func deniedFileName(name string) bool {
	lower := strings.ToLower(name)
	if _, denied := fileDeniedSegments[lower]; denied {
		return true
	}
	if lower == ".env" || strings.HasPrefix(lower, ".env.") {
		return true
	}
	return strings.HasSuffix(lower, ".local.md")
}

// fileEntry is one entry of the file.list JSON result. Size is the file size
// in bytes; directories and symlinks report 0.
type fileEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// fileListArguments is the model-supplied argument shape of file.list.
type fileListArguments struct {
	Path  string `json:"path"`
	Depth *int   `json:"depth"`
}

// fileList implements the file.list tool.
type fileList struct {
	root fileRoot
}

// Name implements domain.Tool.
func (fileList) Name() string { return "file.list" }

// Description implements domain.Tool.
func (fileList) Description() string {
	return "List readable files below the workspace root as a JSON array of {name,type,size}. " +
		"path is relative to the root (default \".\"); depth is 1..3 (default 1); at most 200 entries are returned. " +
		"Paths under .git, .tools, .data, .tmp, node_modules, .next, .secrets, .env*, *.local.md are omitted."
}

// InputSchema implements domain.Tool.
func (fileList) InputSchema() json.RawMessage { return json.RawMessage(fileListSchema) }

// Execute implements domain.Tool.
func (t fileList) Execute(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.ToolResult{}, err
	}

	var in fileListArguments
	if err := json.Unmarshal(args, &in); err != nil {
		return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments,
			"file.list: arguments must be an object with an optional path and depth")
	}
	depth := 1
	if in.Depth != nil {
		depth = *in.Depth
	}
	if depth < 1 || depth > maxFileListDepth {
		return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments,
			fmt.Sprintf("file.list: depth must be between 1 and %d", maxFileListDepth))
	}
	if in.Path == "" {
		in.Path = "."
	}

	resolved, _, err := t.root.resolve(in.Path)
	if err != nil {
		return domain.ToolResult{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.ToolResult{}, newToolError(toolErrorCodeNotFound, fmt.Sprintf("path %q does not exist", in.Path))
		}
		return domain.ToolResult{}, newToolError(toolErrorCodeInternal, fmt.Sprintf("cannot stat path %q: %v", in.Path, err))
	}
	if !info.IsDir() {
		return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments,
			fmt.Sprintf("file.list: path %q is not a directory", in.Path))
	}

	entries := make([]fileEntry, 0, maxFileListEntries)
	if err := t.appendEntries(resolved, "", depth, &entries, true); err != nil {
		return domain.ToolResult{}, err
	}
	content, err := json.Marshal(entries)
	if err != nil {
		return domain.ToolResult{}, newToolError(toolErrorCodeInternal, fmt.Sprintf("file.list: encode result: %v", err))
	}
	return domain.ToolResult{Content: string(content)}, nil
}

// appendEntries walks dir recursively up to depth levels. Entries are emitted
// in directory order, directory before its children. Unreadable nested
// directories are skipped so one inaccessible subtree cannot fail the listing.
func (t fileList) appendEntries(dir, prefix string, depth int, out *[]fileEntry, top bool) error {
	if depth < 1 || len(*out) >= maxFileListEntries {
		return nil
	}
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if !top {
			return nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			return newToolError(toolErrorCodeNotFound, fmt.Sprintf("directory %q does not exist", dir))
		}
		return newToolError(toolErrorCodeInternal, fmt.Sprintf("cannot read directory %q: %v", dir, err))
	}

	for _, entry := range dirEntries {
		if len(*out) >= maxFileListEntries {
			return nil
		}
		name := entry.Name()
		if deniedFileName(name) {
			continue
		}
		rel := name
		if prefix != "" {
			rel = prefix + "/" + name
		}
		if deniedFilePath(rel) {
			continue
		}

		item := fileEntry{Name: rel, Type: "file"}
		if info, infoErr := entry.Info(); infoErr == nil {
			item.Size = info.Size()
		}
		switch {
		case entry.IsDir():
			item.Type = "dir"
			item.Size = 0
		case entry.Type()&fs.ModeSymlink != 0:
			// Never traverse symlinks: they are reported so the model knows
			// they exist, and file.read re-checks containment if it follows one.
			item.Type = "symlink"
			item.Size = 0
		}
		*out = append(*out, item)

		if entry.IsDir() && depth > 1 {
			if err := t.appendEntries(filepath.Join(dir, name), rel, depth-1, out, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// fileReadArguments is the model-supplied argument shape of file.read.
type fileReadArguments struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

// fileRead implements the file.read tool.
type fileRead struct {
	root fileRoot
}

// Name implements domain.Tool.
func (fileRead) Name() string { return "file.read" }

// Description implements domain.Tool.
func (fileRead) Description() string {
	return "Read a UTF-8 text file below the workspace root. path is relative to the root; " +
		"offset is the first 1-based line (default 1) and limit is the number of lines (default and maximum 2000). " +
		"Files over 256 KB or containing binary data are refused. The result starts with \"path (lines a-b of n)\"."
}

// InputSchema implements domain.Tool.
func (fileRead) InputSchema() json.RawMessage { return json.RawMessage(fileReadSchema) }

// Execute implements domain.Tool.
func (t fileRead) Execute(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.ToolResult{}, err
	}

	var in fileReadArguments
	if err := json.Unmarshal(args, &in); err != nil || in.Path == "" {
		return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments,
			"file.read: arguments must be an object with a non-empty path")
	}
	offset := 1
	if in.Offset != nil {
		offset = *in.Offset
		if offset < 1 {
			return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments, "file.read: offset must be >= 1")
		}
	}
	limit := maxFileReadLimit
	if in.Limit != nil {
		limit = *in.Limit
		if limit < 1 || limit > maxFileReadLimit {
			return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments,
				fmt.Sprintf("file.read: limit must be between 1 and %d", maxFileReadLimit))
		}
	}

	resolved, display, err := t.root.resolve(in.Path)
	if err != nil {
		return domain.ToolResult{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.ToolResult{}, newToolError(toolErrorCodeNotFound, fmt.Sprintf("path %q does not exist", in.Path))
		}
		return domain.ToolResult{}, newToolError(toolErrorCodeInternal, fmt.Sprintf("cannot stat path %q: %v", in.Path, err))
	}
	if info.IsDir() {
		return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments,
			fmt.Sprintf("file.read: path %q is a directory", in.Path))
	}
	if info.Size() > maxFileReadBytes {
		return domain.ToolResult{}, newToolError(toolErrorCodeTooLarge,
			fmt.Sprintf("file.read: %q is %d bytes, the limit is %d", in.Path, info.Size(), maxFileReadBytes))
	}

	file, err := os.Open(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.ToolResult{}, newToolError(toolErrorCodeNotFound, fmt.Sprintf("path %q does not exist", in.Path))
		}
		return domain.ToolResult{}, newToolError(toolErrorCodeInternal, fmt.Sprintf("cannot open path %q: %v", in.Path, err))
	}
	defer file.Close()

	// Re-apply the size limit to the bytes actually read so a growing file
	// cannot exceed it between Stat and Open.
	data, err := io.ReadAll(io.LimitReader(file, maxFileReadBytes+1))
	if err != nil {
		return domain.ToolResult{}, newToolError(toolErrorCodeInternal, fmt.Sprintf("cannot read path %q: %v", in.Path, err))
	}
	if len(data) > maxFileReadBytes {
		return domain.ToolResult{}, newToolError(toolErrorCodeTooLarge,
			fmt.Sprintf("file.read: %q is larger than %d bytes", in.Path, maxFileReadBytes))
	}
	if !utf8.Valid(data) {
		return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments,
			fmt.Sprintf("file.read: %q is not valid UTF-8 text", in.Path))
	}
	probe := data
	if len(probe) > binaryProbeBytes {
		probe = probe[:binaryProbeBytes]
	}
	if bytes.IndexByte(probe, 0x00) >= 0 {
		return domain.ToolResult{}, newToolError(toolErrorCodeInvalidArguments,
			fmt.Sprintf("file.read: %q contains NUL bytes and looks binary", in.Path))
	}

	lines := splitFileLines(data)
	total := len(lines)
	start, end := offset, 0
	if start > total {
		start = 0
	} else {
		end = start + limit - 1
		if end > total {
			end = total
		}
	}
	header := fmt.Sprintf("%s (lines %d-%d of %d)", display, start, end, total)
	if start == 0 || end == 0 {
		return domain.ToolResult{Content: header}, nil
	}
	return domain.ToolResult{
		Content: header + "\n" + strings.Join(lines[start-1:end], "\n"),
	}, nil
}

// splitFileLines splits file contents into lines. A trailing newline does not
// create an extra empty line, so a three-line file reports "of 3" whether or
// not it ends with a newline.
func splitFileLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
