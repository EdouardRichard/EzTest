package agenttools

// explore.http is the read-only probe tool of the app-exploration profile
// (design spec §6.9/§8.2). It only allows GET/HEAD, forwards the URL and the
// calling run's per-environment host allowlist to the injected
// application.Prober (which enforces the SSRF rules) and returns a bounded
// status/title/links/forms summary. The page body is never returned, and a
// refused probe is a typed explore_not_allowed error.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// exploreHTTPSchema is the frozen input schema of explore.http.
const exploreHTTPSchema = `{"type":"object","properties":{"url":{"type":"string","minLength":1},"method":{"type":"string","enum":["GET","HEAD"]}},"required":["url"],"additionalProperties":false}`

// exploreHTTPInput is the explore.http argument document.
type exploreHTTPInput struct {
	URL    string `json:"url"`
	Method string `json:"method"`
}

// exploreHTTPOutput is the model-facing probe summary. It carries no body.
type exploreHTTPOutput struct {
	URL         string   `json:"url"`
	Method      string   `json:"method"`
	Status      int      `json:"status"`
	ContentType string   `json:"contentType,omitempty"`
	Title       string   `json:"title,omitempty"`
	Links       []string `json:"links"`
	Forms       []string `json:"forms"`
	Truncated   bool     `json:"truncated"`
}

// runExploreHTTP executes explore.http.
func runExploreHTTP(ctx context.Context, deps ToolDeps, args json.RawMessage) (agentdomain.ToolResult, error) {
	var input exploreHTTPInput
	if failure := decodeToolArguments(args, &input); failure != nil {
		return agentdomain.ToolResult{}, failure
	}
	rawURL := strings.TrimSpace(input.URL)
	if rawURL == "" {
		return agentdomain.ToolResult{}, invalidToolArguments("url must not be empty")
	}
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = "GET"
	}
	if method != "GET" && method != "HEAD" {
		return agentdomain.ToolResult{}, toolFailure(toolErrorExploreNotAllow,
			"method %q is not allowed: exploration is read-only (GET or HEAD only)", method)
	}
	allowedHosts, err := exploreRunAllowedHosts(ctx, deps)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	probe, err := probeURL(ctx, deps, method, rawURL, allowedHosts)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}

	output := exploreHTTPOutput{
		URL:         truncateBytes(rawURL, maxExploreFieldBytes),
		Method:      method,
		Status:      probe.Status,
		ContentType: truncateBytes(strings.TrimSpace(probe.ContentType), maxExploreFieldBytes),
		Title:       truncateRunes(strings.TrimSpace(probe.Title), maxExploreTitleRunes),
		Links:       make([]string, 0, len(probe.Links)),
		Forms:       make([]string, 0, len(probe.Forms)),
		Truncated:   probe.Truncated,
	}
	if resolved := strings.TrimSpace(probe.URL); resolved != "" {
		output.URL = truncateBytes(resolved, maxExploreFieldBytes)
	}
	for _, link := range probe.Links {
		if len(output.Links) >= maxExploreLinks {
			output.Truncated = true
			break
		}
		output.Links = append(output.Links, truncateBytes(strings.TrimSpace(link), maxExploreFieldBytes))
	}
	for _, form := range probe.Forms {
		if len(output.Forms) >= maxExploreForms {
			output.Truncated = true
			break
		}
		output.Forms = append(output.Forms, truncateBytes(strings.TrimSpace(form), maxExploreFieldBytes))
	}
	result, err := encodeExploreSummary(output)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	recordToolAudit(ctx, deps, toolNameExploreHTTP, 0, map[string]any{
		"status": probe.Status,
		"links":  len(output.Links),
		"forms":  len(output.Forms),
	})
	return result, nil
}

// exploreRunAllowedHosts returns the per-environment SSRF allowlist of the
// calling exploration run. The run's stored input names its environment; the
// catalog supplies that environment's base host and configured allowed_hosts. A
// tool call outside an explore run (or without an environment) returns nil, in
// which case the prober falls back to its own configured allowlist.
func exploreRunAllowedHosts(ctx context.Context, deps ToolDeps) ([]string, error) {
	invocation, ok := agentapp.InvocationFromContext(ctx)
	if !ok || invocation.SessionID <= 0 {
		return nil, nil
	}
	runs, err := deps.Runs.ListActive(ctx, maxRunScan)
	if err != nil {
		return nil, fmt.Errorf("mindmap: list active runs: %w", err)
	}
	for _, run := range runs {
		if run.AgentSessionID == nil || *run.AgentSessionID != invocation.SessionID {
			continue
		}
		if run.Kind != mindmapdomain.RunKindExplore {
			continue
		}
		return exploreEnvironmentHosts(ctx, deps, run)
	}
	return nil, nil
}

// exploreEnvironmentHosts reads the explore run's environment and returns its
// allowlist (base host plus configured hosts).
func exploreEnvironmentHosts(ctx context.Context, deps ToolDeps, run mindmapdomain.CaseDesignRun) ([]string, error) {
	var input struct {
		EnvironmentID int64 `json:"environmentId"`
	}
	if len(run.Input) > 0 {
		_ = json.Unmarshal(run.Input, &input)
	}
	if input.EnvironmentID <= 0 || deps.Catalog == nil {
		return nil, nil
	}
	environment, err := deps.Catalog.GetEnvironment(ctx, input.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("mindmap: read explore environment %d: %w", input.EnvironmentID, err)
	}
	hosts := make([]string, 0, len(environment.AllowedHosts)+1)
	seen := make(map[string]struct{}, len(environment.AllowedHosts)+1)
	add := func(raw string) {
		host := strings.ToLower(strings.TrimSpace(raw))
		if host == "" {
			return
		}
		if _, duplicate := seen[host]; duplicate {
			return
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	if base, err := url.Parse(strings.TrimSpace(environment.BaseURL)); err == nil {
		add(base.Hostname())
	}
	for _, host := range environment.AllowedHosts {
		add(host)
	}
	return hosts, nil
}

// probeURL performs one read-only probe. Any prober error (host outside the
// allowlist, private address, too many redirects, transport failure) is a
// fail-closed explore_not_allowed tool error; a cancelled/timed-out context is
// returned to the executor so it can report timeout/cancelled.
func probeURL(ctx context.Context, deps ToolDeps, method, rawURL string, allowedHosts []string) (mindmapapp.ProbeResult, error) {
	var (
		probe mindmapapp.ProbeResult
		err   error
	)
	if method == "HEAD" {
		methodProber, ok := deps.Prober.(MethodProber)
		if !ok {
			return mindmapapp.ProbeResult{}, toolFailure(toolErrorExploreNotAllow,
				"HEAD is not supported by the configured prober; use GET")
		}
		probe, err = methodProber.ProbeMethod(ctx, method, rawURL, "", allowedHosts)
	} else {
		probe, err = deps.Prober.Probe(ctx, rawURL, "", allowedHosts)
	}
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return mindmapapp.ProbeResult{}, contextErr
		}
		return mindmapapp.ProbeResult{}, toolFailure(toolErrorExploreNotAllow,
			"the URL is not allowed: %v", err)
	}
	return probe, nil
}

// encodeExploreSummary marshals the probe summary and enforces the 32 KiB cap by
// dropping links then forms from the tail and finally shortening the scalar
// fields. The reply is always a valid JSON object.
func encodeExploreSummary(output exploreHTTPOutput) (agentdomain.ToolResult, error) {
	for {
		encoded, err := json.Marshal(output)
		if err != nil {
			return agentdomain.ToolResult{}, fmt.Errorf("mindmap: encode explore summary: %w", err)
		}
		if len(encoded) <= maxExploreSummaryBytes {
			return agentdomain.ToolResult{Content: string(encoded)}, nil
		}
		output.Truncated = true
		switch {
		case len(output.Links) > 0:
			output.Links = output.Links[:len(output.Links)-1]
		case len(output.Forms) > 0:
			output.Forms = output.Forms[:len(output.Forms)-1]
		default:
			output.Title = truncateRunes(output.Title, 100)
			output.URL = truncateBytes(output.URL, 256)
			output.ContentType = truncateBytes(output.ContentType, 256)
			encoded, err := json.Marshal(output)
			if err != nil {
				return agentdomain.ToolResult{}, fmt.Errorf("mindmap: encode explore summary: %w", err)
			}
			return agentdomain.ToolResult{Content: string(encoded)}, nil
		}
	}
}
