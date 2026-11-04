package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// CodeForbidden is the role-whitelist denial code (0.2.1 spec §5.2/§5.8). A
// denied call is a policy failure with HTTP 403 semantics, not an argument
// error: the model asked for a tool outside its role.
const CodeForbidden = "forbidden"

// Forbidden builds the typed denied-tool error. It never includes tool
// arguments, so a denial cannot leak payload content into logs or transcripts.
func Forbidden(role, tool string) *Error {
	return &Error{
		Code:    CodeForbidden,
		Message: fmt.Sprintf("role %s may not call tool %s", role, tool),
		Details: map[string]any{"role": role, "tool": tool},
	}
}

// ToolSet is an immutable role tool whitelist. Entries are exact tool names or
// namespace wildcards in the form "namespace.*" (for example "explore.*" for
// tools that later phases register). A ToolSet can never grant a tool that is
// not also present in the runtime registry: Definitions/Execute still consult
// the registry, so a wildcard only permits future namespace members, it does
// not invent them.
type ToolSet struct {
	role     string
	exact    map[string]struct{}
	prefixes map[string]struct{}
}

// NewToolSet builds a whitelist. Empty and duplicate names are ignored; the
// input slice is never retained.
func NewToolSet(role string, names []string) ToolSet {
	set := ToolSet{
		role:     strings.TrimSpace(role),
		exact:    make(map[string]struct{}, len(names)),
		prefixes: make(map[string]struct{}),
	}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if prefix, ok := strings.CutSuffix(name, ".*"); ok {
			if prefix = strings.TrimSpace(prefix); prefix != "" {
				set.prefixes[prefix] = struct{}{}
			}
			continue
		}
		set.exact[name] = struct{}{}
	}
	return set
}

// Role returns the role key this whitelist belongs to.
func (s ToolSet) Role() string { return s.role }

// Allows reports whether the role may see and call the tool. Unknown names are
// denied unless a namespace wildcard covers them; the registry remains the
// final authority on whether such a name exists.
func (s ToolSet) Allows(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if _, ok := s.exact[name]; ok {
		return true
	}
	for prefix := range s.prefixes {
		if len(name) > len(prefix)+1 && strings.HasPrefix(name, prefix+".") {
			return true
		}
	}
	return false
}

// Names returns the registered allowed tool names in the registry's sorted
// order, so a role can only ever see its intersection with the tool registry.
func (s ToolSet) Names(registry *Registry) []string {
	if registry == nil {
		return nil
	}
	all := registry.Names()
	names := make([]string, 0, len(all))
	for _, name := range all {
		if s.Allows(name) {
			names = append(names, name)
		}
	}
	return names
}

// Definitions renders the tool definitions a role may see. Denied and
// unregistered tools are never exposed to the model.
func (s ToolSet) Definitions(registry *Registry) []llmapp.ToolDefinition {
	return registry.Definitions(s.Names(registry))
}

// Execute enforces the whitelist before delegating to the registry. A tool
// outside the whitelist returns a typed forbidden error and never executes; an
// allowed but unregistered name falls through to the registry's typed
// invalid_argument error.
func (s ToolSet) Execute(ctx context.Context, registry *Registry, scope Scope, name string, raw json.RawMessage) (any, error) {
	if !s.Allows(name) {
		return nil, Forbidden(s.role, name)
	}
	return registry.Execute(ctx, scope, name, raw)
}
