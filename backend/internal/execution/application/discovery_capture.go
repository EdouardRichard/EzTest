package executionapp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// discoveryNetworkTimeout bounds one terminal-state network read of one browser
// context. The capture is best effort: a slow/dead context is skipped and never
// delays the terminal write for longer than this bound.
const discoveryNetworkTimeout = 10 * time.Second

// DiscoveryObservation is one execution-time discovery observation of a
// terminal case_run (spec DB-29, R-EX-1). Kind is "page" (document navigation)
// or "api" (xhr/fetch/eventsource/websocket/preflight/manifest). Host/Path are
// already redacted: query values, fragments outside a hash route, userinfo,
// headers, cookies and bodies are never carried. StatusCode is nil for a
// request whose response was lost. ResourceType is the executor's Playwright
// resource_type; the empty string means the executor did not report one (the
// MIME/extension fallback decided the kind). ResourceTemplate is the normalized
// DB-29 template (page: route/path, api: path only; the method is carried by
// Method). The transactional identity (VersionSUTID/WaveID/CaseRunID) is
// attached by the capture; VersionID and EnvironmentID may stay zero and are
// resolved by the repository from run_wave/version_sut. Occurrences aggregates
// same-batch duplicates.
type DiscoveryObservation struct {
	Kind             string
	Method           string
	Host             string
	Path             string
	ResourceTemplate string
	StatusCode       *int
	MIME             string
	DurationMS       int
	ResourceType     string
	VersionID        int64
	VersionSUTID     int64
	EnvironmentID    int64
	WaveID           int64
	CaseRunID        int64
	Occurrences      int
}

// networkObservationItem is one browser.network item as the executor emits it.
type networkObservationItem struct {
	Method       string `json:"method"`
	URL          string `json:"url"`
	Status       *int   `json:"status"`
	MIME         string `json:"mime"`
	DurationMS   int    `json:"duration_ms"`
	ResourceType string `json:"resource_type"`
}

// discoveryEvidenceItem is the redacted shape persisted as network evidence. It
// deliberately has no URL/query/header/cookie/body fields.
type discoveryEvidenceItem struct {
	Method       string `json:"method"`
	Host         string `json:"host"`
	Path         string `json:"path"`
	Status       *int   `json:"status"`
	MIME         string `json:"mime"`
	DurationMS   int    `json:"duration_ms"`
	ResourceType string `json:"resource_type"`
}

// captureTerminalObservations implements R-EX-1: at every terminal state
// (passed/failed/blocked/inconclusive), before the SUT contexts are closed,
// read the network log of every active browser context directly from the
// executor and persist the page/api observations plus one redacted network
// evidence row per context. It never goes through CaseRunner.Browser, whose
// running-scope guard would reject the terminal call, and every failure is
// best effort: the verdict is already aggregated and is never touched.
func (s *CaseRunner) captureTerminalObservations(ctx context.Context, caseRun execution.CaseRun, status execution.CaseRunStatus) {
	if s.browser == nil || s.sutContexts == nil {
		return
	}
	switch status {
	case execution.CaseRunPassed, execution.CaseRunFailed, execution.CaseRunBlocked, execution.CaseRunInconclusive:
	default:
		return
	}
	rows, err := s.sutContexts.ListByCaseRun(ctx, caseRun.ID)
	if err != nil {
		slog.Warn("execution discovery: list SUT contexts failed",
			"case_run_id", caseRun.ID, "error", err)
		return
	}
	for i := range rows {
		row := rows[i]
		if row.Status != execution.SUTContextActive || row.BrowserContextID == "" {
			continue
		}
		observations, evidence, versionSUTID := s.captureContextObservations(row)
		if len(observations) == 0 {
			continue
		}
		// Attach the transactional identity the capture knows. VersionID and
		// EnvironmentID stay zero: the ingest/repository layer resolves them from
		// run_wave and version_sut without leaking a second lookup into the
		// application layer.
		for index := range observations {
			observations[index].VersionSUTID = versionSUTID
			observations[index].WaveID = caseRun.WaveID
			observations[index].CaseRunID = caseRun.ID
		}
		// The DB-29 observation write is independent of the evidence row: a
		// reached evidence cap or an observation-store failure must not lose the
		// other one.
		if s.discovery != nil {
			if err := s.discovery.WriteObservations(ctx, observations); err != nil {
				slog.Warn("execution discovery: write observations failed",
					"case_run_id", caseRun.ID, "version_sut_id", versionSUTID,
					"observations", len(observations), "error", err)
			}
		}
		s.storeDiscoveryNetworkEvidence(ctx, caseRun, versionSUTID, evidence)
	}
}

// captureContextObservations reads and redacts the network log of one browser
// context. It returns the observations, the redacted evidence payload and the
// context's version_sut_id. Any executor failure yields no observations.
func (s *CaseRunner) captureContextObservations(row execution.CaseRunSUTContext) ([]DiscoveryObservation, []byte, int64) {
	executorID := int64(0)
	if row.ExecutorID != nil {
		executorID = *row.ExecutorID
	}
	// A terminal write may run on a canceled request context; the capture owns
	// its own short-lived context like the terminal screenshot does.
	actionCtx, cancel := context.WithTimeout(context.Background(), discoveryNetworkTimeout)
	defer cancel()
	// The executor's network action defaults to the last 50 items. A normal SPA
	// page load puts the document request first (the W1 SUT home page makes 52
	// requests), so the default window would silently lose the page observation.
	// Ask for the 200-item maximum the executor supports; the executor still
	// bounds memory with its 500-entry ring (W1-15 fix).
	result, err := s.browser.Action(actionCtx, executorID, row.BrowserContextID, "network", json.RawMessage(`{"tail":200}`))
	if err != nil {
		slog.Warn("execution discovery: read browser network log failed",
			"case_run_id", row.CaseRunID, "version_sut_id", row.VersionSUTID,
			"browser_context_id", row.BrowserContextID, "error", err)
		return nil, nil, row.VersionSUTID
	}
	if result.Error != nil {
		slog.Warn("execution discovery: executor rejected the network action",
			"case_run_id", row.CaseRunID, "version_sut_id", row.VersionSUTID,
			"browser_context_id", row.BrowserContextID, "code", result.Error.Code)
		return nil, nil, row.VersionSUTID
	}
	observations := parseDiscoveryNetworkItems(result.Result)
	if len(observations) == 0 {
		return nil, nil, row.VersionSUTID
	}
	return observations, discoveryEvidencePayload(observations), row.VersionSUTID
}

// parseDiscoveryNetworkItems decodes the executor payload. The executor wraps
// the item list in {"items":[...]} while direct executor clients and tests may
// hand over the bare array; both shapes are accepted.
func parseDiscoveryNetworkItems(raw json.RawMessage) []DiscoveryObservation {
	if len(raw) == 0 {
		return nil
	}
	items := decodeDiscoveryNetworkItems(raw)
	if len(items) == 0 {
		return nil
	}
	observations := make([]DiscoveryObservation, 0, len(items))
	for _, rawItem := range items {
		// Per-item decoding (M6): one malformed executor entry must never drop
		// the rest of the batch.
		var item networkObservationItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			slog.Warn("execution discovery: skip malformed network item", "error", err)
			continue
		}
		observation, ok := discoveryObservationFromItem(item)
		if !ok {
			continue
		}
		observations = append(observations, observation)
	}
	return observations
}

func decodeDiscoveryNetworkItems(raw json.RawMessage) []json.RawMessage {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err == nil && items != nil {
		return items
	}
	var envelope struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		return envelope.Items
	}
	return nil
}

// discoveryObservationFromItem classifies and normalizes one network item.
// document is a page; xhr/fetch/eventsource/websocket/preflight/manifest are
// apis; the explicit static denylist (script/image/stylesheet/font/media/...)
// is filtered. A missing resource_type falls back deterministically: any
// text/html resource is a page (even with an extension, M10), everything else
// is an api (and the empty resource_type is kept on the row). A blocked
// WebSocket guard entry (method WS) is not a real resource and is skipped (M1).
func discoveryObservationFromItem(item networkObservationItem) (DiscoveryObservation, bool) {
	parsed, err := url.Parse(strings.TrimSpace(item.URL))
	if err != nil || parsed.Hostname() == "" {
		return DiscoveryObservation{}, false
	}
	if strings.EqualFold(strings.TrimSpace(item.Method), "WS") {
		return DiscoveryObservation{}, false
	}
	path := discoveryPath(parsed)
	kind, ok := classifyDiscoveryResource(item.ResourceType, item.MIME)
	if !ok {
		return DiscoveryObservation{}, false
	}
	if isStaticDiscoveryMIME(item.MIME) {
		return DiscoveryObservation{}, false
	}
	method := strings.ToUpper(strings.TrimSpace(item.Method))
	host, template, ok := NormalizeResourceTemplate(kind, method, item.URL)
	if !ok {
		return DiscoveryObservation{}, false
	}
	if kind == "page" {
		method = ""
	}
	return DiscoveryObservation{
		Kind:             kind,
		Method:           method,
		Host:             host,
		Path:             path,
		ResourceTemplate: template,
		StatusCode:       item.Status,
		MIME:             strings.TrimSpace(item.MIME),
		DurationMS:       item.DurationMS,
		ResourceType:     strings.ToLower(strings.TrimSpace(item.ResourceType)),
	}, true
}

// discoveryPath returns the redacted path of a URL: no query, no userinfo, no
// fragment except the path part of a hash route (SPA routes stay discoverable,
// their query values do not).
func discoveryPath(parsed *url.URL) string {
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	fragment := parsed.Fragment
	if strings.HasPrefix(fragment, "/") {
		if index := strings.IndexByte(fragment, '?'); index >= 0 {
			fragment = fragment[:index]
		}
		path += "#" + fragment
	}
	return path
}

// classifyDiscoveryResource maps a Playwright resource_type onto the DB-29 kind
// (M8): an explicit static denylist is filtered, non-static types such as
// eventsource/websocket/preflight/manifest are apis, and the missing/unknown
// fallback is deterministic — a text/html resource is a page (M10), everything
// else is an api. The extension/MIME static filter is applied by the normalizer
// and the MIME check, so a script-typed resource can never become an api.
func classifyDiscoveryResource(resourceType, mime string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(resourceType)) {
	case "document":
		return "page", true
	case "xhr", "fetch", "eventsource", "websocket", "preflight", "manifest":
		return "api", true
	case "stylesheet", "image", "media", "font", "script", "texttrack":
		return "", false
	case "", "other":
		if isHTMLDiscoveryMIME(mime) {
			return "page", true
		}
		return "api", true
	default:
		if isHTMLDiscoveryMIME(mime) {
			return "page", true
		}
		return "api", true
	}
}

func isHTMLDiscoveryMIME(mime string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "text/html")
}

func discoveryEvidencePayload(observations []DiscoveryObservation) []byte {
	items := make([]discoveryEvidenceItem, 0, len(observations))
	for _, observation := range observations {
		items = append(items, discoveryEvidenceItem{
			Method:       observation.Method,
			Host:         observation.Host,
			Path:         observation.Path,
			Status:       observation.StatusCode,
			MIME:         observation.MIME,
			DurationMS:   observation.DurationMS,
			ResourceType: observation.ResourceType,
		})
	}
	payload, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	return payload
}

// storeDiscoveryNetworkEvidence stores the redacted network payload through the
// existing evidence path. The evidence cap may drop the row; observations are
// written independently, so a full evidence table never loses discovery data.
func (s *CaseRunner) storeDiscoveryNetworkEvidence(ctx context.Context, caseRun execution.CaseRun, versionSUTID int64, payload []byte) {
	if s.evidence == nil || s.content == nil || len(payload) == 0 {
		return
	}
	versionRef := versionSUTID
	if _, err := s.addEvidenceForRun(ctx, caseRun, 0, EvidenceInput{
		Kind:         execution.EvidenceNetwork,
		Data:         payload,
		VersionSUTID: &versionRef,
		Auto:         true,
	}); err != nil {
		slog.Warn("execution discovery: store redacted network evidence failed",
			"case_run_id", caseRun.ID, "version_sut_id", versionSUTID, "error", err)
	}
}
