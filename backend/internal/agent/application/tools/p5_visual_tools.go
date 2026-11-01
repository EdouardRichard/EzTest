package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// sut.page_propose bounds (spec §4.4 / contracts/agent-profiles-tools.md §8.3).
const (
	p5ProposalNameMax   = 200
	p5ProposalURLMax    = 4096
	p5ProposalRouteMax  = 2000
	p5ProposalReasonMax = 2000
)

var p5HTTPMethodPattern = regexp.MustCompile(`^[A-Z]{1,10}$`)

// sutPageProposeTool writes exactly one candidate page/API proposal. It never
// sets approved/rejected: the writer owns status=proposed/source=ai|code and
// the admin confirmation stays a user API (spec §8.1).
type sutPageProposeTool struct{ deps Deps }

func (t *sutPageProposeTool) Name() string { return ToolSUTPagePropose }

func (t *sutPageProposeTool) Description() string {
	return "Propose one page or API resource for the current version (status=proposed only; an admin confirms it later). Returns created/skipped with the resource ID."
}

func (t *sutPageProposeTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"kind":          map[string]any{"type": "string", "enum": []string{"page", "api"}},
		"name":          map[string]any{"type": "string", "description": "resource name (1..200 characters)"},
		"url":           map[string]any{"type": "string", "description": "page URL (page proposals)"},
		"routeTemplate": map[string]any{"type": "string", "description": "page route template such as /orders", "pattern": "^/"},
		"method":        map[string]any{"type": "string", "description": "HTTP method (api proposals)", "pattern": "^[A-Z]{1,10}$"},
		"pathTemplate":  map[string]any{"type": "string", "description": "API path template such as /api/orders/{id}", "pattern": "^/"},
		"reason":        map[string]any{"type": "string", "description": "why the resource is proposed (max 2000 characters)"},
	}, "kind", "name")
}

func (t *sutPageProposeTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var args struct {
		Kind          string `json:"kind"`
		Name          string `json:"name"`
		URL           string `json:"url"`
		RouteTemplate string `json:"routeTemplate"`
		Method        string `json:"method"`
		PathTemplate  string `json:"pathTemplate"`
		Reason        string `json:"reason"`
	}
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	if scope.VersionID <= 0 {
		return nil, Invalid("scope", "the version scope is required")
	}
	kind := strings.TrimSpace(args.Kind)
	name := strings.TrimSpace(args.Name)
	if err := validateText("name", name, 1, p5ProposalNameMax); err != nil {
		return nil, err
	}
	routeTemplate := strings.TrimSpace(args.RouteTemplate)
	pathTemplate := strings.TrimSpace(args.PathTemplate)
	method := strings.ToUpper(strings.TrimSpace(args.Method))
	url := strings.TrimSpace(args.URL)
	reason := strings.TrimSpace(args.Reason)
	if reason != "" {
		if err := validateText("reason", reason, 1, p5ProposalReasonMax); err != nil {
			return nil, err
		}
	}
	if url != "" {
		if err := validateText("url", url, 1, p5ProposalURLMax); err != nil {
			return nil, err
		}
	}
	if routeTemplate != "" {
		if err := validateText("routeTemplate", routeTemplate, 1, p5ProposalRouteMax); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(routeTemplate, "/") {
			return nil, Invalid("routeTemplate", "must start with /")
		}
	}
	switch kind {
	case "page":
		if routeTemplate == "" && url == "" {
			return nil, Invalid("routeTemplate", "a page proposal requires routeTemplate or url")
		}
	case "api":
		if pathTemplate == "" {
			return nil, Invalid("pathTemplate", "an api proposal requires pathTemplate")
		}
		if method == "" {
			return nil, Invalid("method", "an api proposal requires method")
		}
		if !p5HTTPMethodPattern.MatchString(method) {
			return nil, Invalid("method", "method must be an uppercase HTTP method")
		}
		if err := validateText("pathTemplate", pathTemplate, 1, p5ProposalRouteMax); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(pathTemplate, "/") {
			return nil, Invalid("pathTemplate", "must start with /")
		}
	default:
		return nil, Invalid("kind", `kind must be "page" or "api"`)
	}
	if t.deps.P5.Propose == nil {
		return nil, missingDep("resource proposal writer")
	}
	result, err := t.deps.P5.Propose.ProposeSUTResource(ctx, scope.VersionID, SutProposalInput{
		Kind: kind, Name: name, URL: url, RouteTemplate: routeTemplate,
		Method: method, PathTemplate: pathTemplate, Reason: reason,
	})
	if err != nil {
		return nil, p5MapError(err, "sut resource proposal")
	}
	return map[string]any{
		"kind":    kind,
		"created": result.Created,
		"skipped": !result.Created,
		"id":      idString(result.ID),
		"status":  result.Status,
	}, nil
}
