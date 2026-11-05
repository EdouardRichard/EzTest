package executionapp

import (
	"context"
	"encoding/json"
	"net/url"
	pathpkg "path"
	"strings"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// discoveryNetworkTimeout bounds one terminal-state network read of one browser
// context. The capture is best effort: a slow/dead context is skipped and never
// delays the terminal write for longer than this bound.
const discoveryNetworkTimeout = 10 * time.Second

// DiscoveryObservation is one normalized execution-time discovery observation
// of a terminal case_run (spec DB-29, R-EX-1). Kind is "page" (document
// navigation) or "api" (xhr/fetch). Host/Path are already redacted: query
// values, fragments outside a hash route, userinfo, headers, cookies and bodies
// are never carried. StatusCode is nil for a request whose response was lost.
// ResourceType is the executor's Playwright resource_type; the empty string
// means the executor did not report one (the MIME/extension fallback decided
// the kind).
type DiscoveryObservation struct {
	Kind         string
	Method       string
	Host         string
	Path         string
	StatusCode   *int
	MIME         string
	DurationMS   int
	ResourceType string
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
		// The DB-29 observation write is independent of the evidence row: a
		// reached evidence cap or an observation-store failure must not lose the
		// other one.
		if s.discovery != nil {
			_ = s.discovery.WriteObservations(ctx, observations)
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
	result, err := s.browser.Action(actionCtx, executorID, row.BrowserContextID, "network", json.RawMessage(`{}`))
	if err != nil || result.Error != nil {
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
	for _, item := range items {
		observation, ok := discoveryObservationFromItem(item)
		if !ok {
			continue
		}
		observations = append(observations, observation)
	}
	return observations
}

func decodeDiscoveryNetworkItems(raw json.RawMessage) []networkObservationItem {
	var items []networkObservationItem
	if err := json.Unmarshal(raw, &items); err == nil && items != nil {
		return items
	}
	var envelope struct {
		Items []networkObservationItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		return envelope.Items
	}
	return nil
}

// discoveryObservationFromItem classifies one network item. document is a page;
// xhr/fetch are apis; every static resource type (script/image/stylesheet/font/
// media and any other/unknown value) is filtered. A missing resource_type falls
// back deterministically: text/html with an extension-less path is a page,
// everything else is an api (and the empty resource_type is kept on the row).
func discoveryObservationFromItem(item networkObservationItem) (DiscoveryObservation, bool) {
	parsed, err := url.Parse(strings.TrimSpace(item.URL))
	if err != nil || parsed.Hostname() == "" {
		return DiscoveryObservation{}, false
	}
	path := discoveryPath(parsed)
	kind, ok := classifyDiscoveryResource(item.ResourceType, item.MIME, path)
	if !ok {
		return DiscoveryObservation{}, false
	}
	return DiscoveryObservation{
		Kind:         kind,
		Method:       strings.ToUpper(strings.TrimSpace(item.Method)),
		Host:         strings.ToLower(parsed.Hostname()),
		Path:         path,
		StatusCode:   item.Status,
		MIME:         strings.TrimSpace(item.MIME),
		DurationMS:   item.DurationMS,
		ResourceType: strings.ToLower(strings.TrimSpace(item.ResourceType)),
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

// classifyDiscoveryResource maps a Playwright resource_type onto the DB-29 kind.
func classifyDiscoveryResource(resourceType, mime, path string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(resourceType)) {
	case "document":
		return "page", true
	case "xhr", "fetch":
		return "api", true
	case "":
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "text/html") && !discoveryPathHasExtension(path) {
			return "page", true
		}
		return "api", true
	default:
		return "", false
	}
}

func discoveryPathHasExtension(path string) bool {
	if index := strings.IndexByte(path, '#'); index >= 0 {
		path = path[:index]
	}
	return pathpkg.Ext(path) != ""
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
	_, _ = s.addEvidenceForRun(ctx, caseRun, 0, EvidenceInput{
		Kind:         execution.EvidenceNetwork,
		Data:         payload,
		VersionSUTID: &versionRef,
		Auto:         true,
	})
}
