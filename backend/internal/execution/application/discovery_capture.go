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
// normalized to the route identity (no query, no userinfo, no fragment outside a
// hash route) for fact-key/route-template dedup — functional normalization, not
// credential redaction; the network evidence row keeps the raw URL verbatim
// (GD-01 Q05=B). StatusCode is nil for a
// request whose response was lost. ResourceType is the executor's Playwright
// resource_type; the empty string means the executor did not report one (the
// MIME/extension fallback decided the kind). ResourceTemplate is the normalized
// DB-29 template (page: route/path, api: path only; the method is carried by
// Method). The transactional identity (SUTID/WaveID/CaseRunID) is
// attached by the capture; VersionID and EnvironmentID may stay zero and are
// resolved by the repository from run_wave/sut. Occurrences aggregates
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
	SUTID            int64
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

// captureTerminalObservations implements R-EX-1: at every terminal state
// (passed/failed/blocked/inconclusive), before the SUT contexts are closed,
// read the recorded network log of every active executor session directly from
// the executor's passive traffic recorder and persist the page/api observations
// plus one verbatim network evidence row per context. The read is a best-effort
// side channel (no running-scope guard): the verdict is already aggregated and
// is never touched.
func (s *CaseRunner) captureTerminalObservations(ctx context.Context, caseRun execution.CaseRun, status execution.CaseRunStatus) {
	if s.execution == nil || s.sutContexts == nil {
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
		if row.Status != execution.SUTContextActive || row.SessionID == "" {
			continue
		}
		observations, evidence, sutID := s.captureContextObservations(row)
		if len(observations) == 0 {
			continue
		}
		// Attach the transactional identity the capture knows. VersionID and
		// EnvironmentID stay zero: the ingest/repository layer resolves them from
		// run_wave and sut without leaking a second lookup into the
		// application layer.
		for index := range observations {
			observations[index].SUTID = sutID
			observations[index].WaveID = caseRun.WaveID
			observations[index].CaseRunID = caseRun.ID
		}
		// The DB-29 observation write is independent of the evidence row: a
		// reached evidence cap or an observation-store failure must not lose the
		// other one.
		if s.discovery != nil {
			if err := s.discovery.WriteObservations(ctx, observations); err != nil {
				slog.Warn("execution discovery: write observations failed",
					"case_run_id", caseRun.ID, "sut_id", sutID,
					"observations", len(observations), "error", err)
			}
		}
		s.storeDiscoveryNetworkEvidence(ctx, caseRun, sutID, evidence)
	}
}

// captureContextObservations reads the recorded network log of one executor
// session through the passive traffic recorder (T16, design §4.3 pure CDP
// recording — the retired Playwright-library bridge's BrowserAction("network")
// is replaced by the NetworkLog read surface). It returns the normalized
// observations (DB-29 fact/route identity) and the raw recorder payload as
// network evidence. GD-01 (Q05=B): evidence is plaintext by design, so the legacy
// query/userinfo erasure on the evidence row is removed. Any read failure yields
// no observations (best effort, never touches the verdict).
func (s *CaseRunner) captureContextObservations(row execution.CaseRunSUTContext) ([]DiscoveryObservation, []byte, int64) {
	executorID := int64(0)
	if row.ExecutorID != nil {
		executorID = *row.ExecutorID
	}
	// An executor without the traffic-recording surface simply has no network
	// log to read; the capture skips best-effort (the GeneralExecutionPort
	// type-assertion precedent).
	reader, ok := s.execution.(NetworkLogReader)
	if !ok {
		return nil, nil, row.SUTID
	}
	// A terminal write may run on a canceled request context; the capture owns
	// its own short-lived context like the terminal screenshot does.
	actionCtx, cancel := context.WithTimeout(context.Background(), discoveryNetworkTimeout)
	defer cancel()
	// Ask for the 200-item maximum the recorder's read tail supports. A normal
	// SPA page load puts the document request first (the W1 SUT home page makes
	// 52 requests), so a smaller default window would silently lose the page
	// observation; the recorder still bounds memory with its 500-entry ring.
	payload, err := reader.NetworkLog(actionCtx, executorID, row.SessionID, 200)
	if err != nil {
		slog.Warn("execution discovery: read recorded network log failed",
			"case_run_id", row.CaseRunID, "sut_id", row.SUTID,
			"session_id", row.SessionID, "error", err)
		return nil, nil, row.SUTID
	}
	observations := parseDiscoveryNetworkItems(payload)
	if len(observations) == 0 {
		return nil, nil, row.SUTID
	}
	// The evidence row carries the recorder payload verbatim (same contract as
	// the retired browser.network evidence path); only the observation above is
	// normalized for fact-key/route-template dedup.
	return observations, payload, row.SUTID
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
	if kind == execution.ObservationKindPage {
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

// discoveryPath returns the normalized path of a URL (the route identity): no
// query, no userinfo, no fragment except the path part of a hash route (SPA
// routes stay discoverable, their query values are not part of the route
// identity). Functional normalization for fact-key dedup, not credential
// redaction — the raw network evidence keeps the full URL verbatim (GD-01).
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
		return execution.ObservationKindPage, true
	case "xhr", "fetch", "eventsource", "websocket", "preflight", "manifest":
		return execution.ObservationKindAPI, true
	case "stylesheet", "image", "media", "font", "script", "texttrack":
		return "", false
	case "", "other":
		if isHTMLDiscoveryMIME(mime) {
			return execution.ObservationKindPage, true
		}
		return execution.ObservationKindAPI, true
	default:
		if isHTMLDiscoveryMIME(mime) {
			return execution.ObservationKindPage, true
		}
		return execution.ObservationKindAPI, true
	}
}

func isHTMLDiscoveryMIME(mime string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "text/html")
}

// storeDiscoveryNetworkEvidence stores the raw network payload verbatim through
// the existing evidence path (GD-01 Q05=B: evidence is plaintext). The evidence
// cap may drop the row; observations are written independently, so a full
// evidence table never loses discovery data.
func (s *CaseRunner) storeDiscoveryNetworkEvidence(ctx context.Context, caseRun execution.CaseRun, sutID int64, payload []byte) {
	if s.evidence == nil || s.content == nil || len(payload) == 0 {
		return
	}
	versionRef := sutID
	if _, err := s.addEvidenceForRun(ctx, caseRun, 0, EvidenceInput{
		Kind:  execution.EvidenceNetwork,
		Data:  payload,
		SUTID: &versionRef,
		Auto:  true,
	}); err != nil {
		slog.Warn("execution discovery: store network evidence failed",
			"case_run_id", caseRun.ID, "sut_id", sutID, "error", err)
	}
}
