package versionapp

// W1-12 (WP-3): deterministic execution-time discovery delta. The service
// classifies every DB-29 observation against the environment resource catalog
// (new/proposed/approved/rejected/out_of_scope), proposes genuinely new pages and
// APIs, records one version_fact per new resource with wave/case_run/observation
// provenance, persists the observation decision and publishes one wave-level
// resource.delta event.
//
// The version context does not read or write the execution table directly
// (Agent.md §6.2): observations and decision updates cross the boundary through
// the DiscoveryObservationReader/DiscoveryObservationDecider ports implemented by
// the execution infrastructure adapter.

import (
	"context"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resource "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// DiscoveryObservation is the version context's view of one execution
// discovery observation (spec DB-29). It is a neutral projection so the version
// application never imports the execution application/domain.
type DiscoveryObservation struct {
	ObservationID    int64
	VersionID        int64
	WaveID           int64
	CaseRunID        int64
	VersionSUTID     int64
	EnvironmentID    int64
	Kind             string
	Method           string
	Host             string
	ResourceTemplate string
	Decision         string
	ResourceID       int64
	FactID           int64
}

// DiscoveryObservationReader is the read port into execution discovery
// observations. The execution infrastructure adapter implements it.
type DiscoveryObservationReader interface {
	ListForDelta(ctx context.Context, versionID, waveID int64) ([]DiscoveryObservation, error)
}

// DiscoveryObservationDecider persists the delta classification back on the
// observation row. It is separate from the reader so tests/degraded assemblies
// can run read-only; a nil decider skips the write.
type DiscoveryObservationDecider interface {
	MarkDecision(ctx context.Context, observationID int64, decision string, resourceID, factID int64) error
}

// DiscoveryResourceCatalog is the resource-context surface the delta service
// uses. *resourceapp.Service implements it: template lookups are status-agnostic
// (the W1-12 registered/rejected/proposed distinction) and proposal creation
// goes through the existing proposed-resource path.
type DiscoveryResourceCatalog interface {
	FindPageByRouteTemplate(ctx context.Context, environmentID int64, routeTemplate string) (resourceapp.PageView, bool, error)
	FindAPIByTemplate(ctx context.Context, environmentID int64, method, pathTemplate string) (resourceapp.APIView, bool, error)
	CreateProposedPage(ctx context.Context, input resourceapp.ProposedPageInput) (resourceapp.PageView, error)
	CreateProposedAPI(ctx context.Context, input resourceapp.ProposedAPIInput) (resourceapp.APIView, error)
}

// DiscoveryFactRecorder is the append-only fact surface the delta service uses
// (*FactService implements it). Current is used to avoid duplicating an existing
// P9 (or earlier execution) fact of the same resource.
type DiscoveryFactRecorder interface {
	Record(ctx context.Context, fact version.Fact) (version.Fact, bool, error)
	Current(ctx context.Context, versionID int64, kind string) ([]version.Fact, error)
}

// DeltaItem is one classified observation. IDs are decimal int64 values; the
// HTTP/event projections format them as decimal strings.
type DeltaItem struct {
	Kind             string `json:"kind"`
	Method           string `json:"method"`
	ResourceTemplate string `json:"resource_template"`
	Host             string `json:"host"`
	VersionSUTID     int64  `json:"version_sut_id"`
	EnvironmentID    int64  `json:"environment_id"`
	WaveID           int64  `json:"wave_id"`
	CaseRunID        int64  `json:"case_run_id"`
	ObservationID    int64  `json:"observation_id"`
	ResourceID       int64  `json:"resource_id"`
	FactID           int64  `json:"fact_id"`
	Decision         string `json:"decision"`
}

// DeltaResult is the five-way classification of one wave (spec R-EX-3, API-37).
// Every slice is non-nil.
type DeltaResult struct {
	New        []DeltaItem `json:"new"`
	Proposed   []DeltaItem `json:"proposed"`
	Approved   []DeltaItem `json:"approved"`
	Rejected   []DeltaItem `json:"rejected"`
	OutOfScope []DeltaItem `json:"out_of_scope"`
}

// DiscoveryDeltaDeps wires one delta evaluator. Observations and Resources are
// mandatory; Decisions, Facts and Events may be nil in degraded assemblies.
type DiscoveryDeltaDeps struct {
	Observations DiscoveryObservationReader
	Decisions    DiscoveryObservationDecider
	Resources    DiscoveryResourceCatalog
	Facts        DiscoveryFactRecorder
	Events       eventingapp.Publisher
	Logger       *slog.Logger
}

// DiscoveryDelta evaluates execution observations against the resource catalog
// (W1-12). It is idempotent per wave: re-evaluating creates no second
// resource/fact/event, and approved/rejected resources are never re-proposed.
type DiscoveryDelta struct {
	observations DiscoveryObservationReader
	decisions    DiscoveryObservationDecider
	resources    DiscoveryResourceCatalog
	facts        DiscoveryFactRecorder
	events       eventingapp.Publisher
	logger       *slog.Logger
}

// NewDiscoveryDelta wires one delta evaluator.
func NewDiscoveryDelta(deps DiscoveryDeltaDeps) *DiscoveryDelta {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &DiscoveryDelta{
		observations: deps.Observations,
		decisions:    deps.Decisions,
		resources:    deps.Resources,
		facts:        deps.Facts,
		events:       deps.Events,
		logger:       logger,
	}
}

// Evaluate classifies every observation of the wave and records the genuinely
// new resources. A resource that already exists (approved/proposed/rejected) is
// classified from its stored status and never re-proposed. Only a genuinely new
// resource records a fact and contributes to the single resource.delta event.
func (d *DiscoveryDelta) Evaluate(ctx context.Context, versionID, waveID int64) (DeltaResult, error) {
	result := newDeltaResult()
	if versionID <= 0 {
		return result, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if d == nil || d.observations == nil || d.resources == nil {
		return result, platform.Unavailable("discovery delta is not configured")
	}
	observations, err := d.observations.ListForDelta(ctx, versionID, waveID)
	if err != nil {
		return result, err
	}

	facts := d.loadCurrentFacts(ctx, versionID)
	newItems := make([]DeltaItem, 0)
	for _, observation := range observations {
		item := deltaItemFromObservation(observation, waveID)
		if status := strings.TrimSpace(observation.Kind); status != "page" && status != "api" {
			d.logger.WarnContext(ctx, "discovery delta: skip observation with unsupported kind",
				"observation_id", observation.ObservationID, "kind", observation.Kind)
			continue
		}
		if observation.EnvironmentID <= 0 {
			d.logger.WarnContext(ctx, "discovery delta: skip observation without an environment",
				"observation_id", observation.ObservationID, "version_sut_id", observation.VersionSUTID)
			continue
		}
		classified, created, err := d.evaluateObservation(ctx, versionID, facts, observation, &item)
		if err != nil {
			return result, err
		}
		appendDeltaItem(&result, classified, item)
		if created {
			newItems = append(newItems, item)
		}
	}

	sortDeltaResult(&result)
	d.publishDelta(ctx, versionID, waveID, newItems)
	return result, nil
}

// evaluateObservation classifies one observation, creating/proposing a resource
// when it is genuinely new. The returned classification is the target slice name.
func (d *DiscoveryDelta) evaluateObservation(ctx context.Context, versionID int64, facts map[string][]version.Fact, observation DiscoveryObservation, item *DeltaItem) (string, bool, error) {
	switch observation.Kind {
	case "page":
		view, found, err := d.resources.FindPageByRouteTemplate(ctx, observation.EnvironmentID, observation.ResourceTemplate)
		if err != nil {
			return "", false, err
		}
		if found {
			item.ResourceID = parseResourceID(view.ID)
			item.FactID = observation.FactID
			switch view.Status {
			case resource.PageStatusRejected:
				return "rejected", false, d.markDecision(ctx, observation, "rejected", item.ResourceID, 0)
			case resource.PageStatusProposed:
				return "proposed", false, d.markDecision(ctx, observation, "proposed", item.ResourceID, item.FactID)
			case resource.PageStatusApproved:
				if observation.Decision == "approved" {
					return "approved", false, d.markDecision(ctx, observation, "approved", item.ResourceID, 0)
				}
				return "out_of_scope", false, d.markDecision(ctx, observation, "out_of_scope", item.ResourceID, 0)
			default:
				return "out_of_scope", false, d.markDecision(ctx, observation, "out_of_scope", item.ResourceID, 0)
			}
		}
		// No catalog row: an observation that was already classified in a prior
		// pass is never silently re-proposed.
		if classified, handled := classifiedWithoutResource(observation); handled {
			return classified, false, nil
		}
		created, err := d.resources.CreateProposedPage(ctx, resourceapp.ProposedPageInput{
			EnvironmentID: observation.EnvironmentID,
			Name:          observation.ResourceTemplate,
			URL:           discoveryProposedURL(*item),
			RouteTemplate: observation.ResourceTemplate,
			PageType:      "page",
			Source:        resource.PageSourceAI,
		})
		if err != nil {
			return "", false, err
		}
		item.ResourceID = parseResourceID(created.ID)
		return d.finalizeProposedResource(ctx, versionID, facts, observation, item, created.Status)
	case "api":
		view, found, err := d.resources.FindAPIByTemplate(ctx, observation.EnvironmentID, observation.Method, observation.ResourceTemplate)
		if err != nil {
			return "", false, err
		}
		if found {
			item.ResourceID = parseResourceID(view.ID)
			item.FactID = observation.FactID
			switch view.Status {
			case resource.APIStatusRejected:
				return "rejected", false, d.markDecision(ctx, observation, "rejected", item.ResourceID, 0)
			case resource.APIStatusProposed:
				return "proposed", false, d.markDecision(ctx, observation, "proposed", item.ResourceID, item.FactID)
			case resource.APIStatusApproved:
				if observation.Decision == "approved" {
					return "approved", false, d.markDecision(ctx, observation, "approved", item.ResourceID, 0)
				}
				return "out_of_scope", false, d.markDecision(ctx, observation, "out_of_scope", item.ResourceID, 0)
			default:
				return "out_of_scope", false, d.markDecision(ctx, observation, "out_of_scope", item.ResourceID, 0)
			}
		}
		if classified, handled := classifiedWithoutResource(observation); handled {
			return classified, false, nil
		}
		created, err := d.resources.CreateProposedAPI(ctx, resourceapp.ProposedAPIInput{
			EnvironmentID: observation.EnvironmentID,
			Method:        observation.Method,
			PathTemplate:  observation.ResourceTemplate,
			Name:          strings.TrimSpace(observation.Method + " " + observation.ResourceTemplate),
			Source:        resource.APISourceAI,
		})
		if err != nil {
			return "", false, err
		}
		item.ResourceID = parseResourceID(created.ID)
		return d.finalizeProposedResource(ctx, versionID, facts, observation, item, created.Status)
	}
	return "", false, nil
}

// finalizeProposedResource resolves the status returned by the proposal write.
// The proposal path may return an already-proposed row (idempotent refresh) or
// an approved/rejected row (the normalized_url lookup found one), so the fact
// and the event are only written for a proposed row.
func (d *DiscoveryDelta) finalizeProposedResource(ctx context.Context, versionID int64, facts map[string][]version.Fact, observation DiscoveryObservation, item *DeltaItem, status string) (string, bool, error) {
	switch status {
	case "proposed":
		factID, err := d.ensureFact(ctx, versionID, facts, observation, item)
		if err != nil {
			return "", false, err
		}
		item.FactID = factID
		if err := d.markDecision(ctx, observation, "proposed", item.ResourceID, factID); err != nil {
			return "", false, err
		}
		return "new", true, nil
	case "rejected":
		return "rejected", false, d.markDecision(ctx, observation, "rejected", item.ResourceID, 0)
	default:
		return "out_of_scope", false, d.markDecision(ctx, observation, "out_of_scope", item.ResourceID, 0)
	}
}

// classifiedWithoutResource handles an observation whose catalog row is missing
// but which already carries a terminal decision. It prevents a deleted/rejected
// resource from being re-proposed by a later wave.
func classifiedWithoutResource(observation DiscoveryObservation) (string, bool) {
	switch observation.Decision {
	case "rejected":
		return "rejected", true
	case "proposed":
		return "proposed", true
	case "approved":
		return "approved", true
	case "out_of_scope":
		return "out_of_scope", true
	default:
		return "", false
	}
}

// ensureFact records one page/api fact for a newly proposed resource unless a
// current fact of the same environment/template already exists (the P9 fact is
// never duplicated; the observation carries this execution's provenance). The
// returned id is the existing or newly stored fact id.
func (d *DiscoveryDelta) ensureFact(ctx context.Context, versionID int64, facts map[string][]version.Fact, observation DiscoveryObservation, item *DeltaItem) (int64, error) {
	if d.facts == nil {
		return 0, nil
	}
	if existing := matchingDiscoveryFact(facts[item.Kind], *item); existing != nil {
		return existing.ID, nil
	}
	fact := discoveryDeltaFact(versionID, observation, *item)
	stored, _, err := d.facts.Record(ctx, fact)
	if err != nil {
		return 0, err
	}
	facts[item.Kind] = append(facts[item.Kind], stored)
	return stored.ID, nil
}

// loadCurrentFacts caches the current page/api facts once per evaluation. A
// fact-read failure is non-fatal: the proposal still succeeds, but the dedupe
// check is skipped (best effort knowledge write).
func (d *DiscoveryDelta) loadCurrentFacts(ctx context.Context, versionID int64) map[string][]version.Fact {
	facts := map[string][]version.Fact{"page": {}, "api": {}}
	if d.facts == nil {
		return facts
	}
	for _, kind := range []string{"page", "api"} {
		current, err := d.facts.Current(ctx, versionID, kind)
		if err != nil {
			d.logger.WarnContext(ctx, "discovery delta: read current facts failed; P9 fact dedupe skipped",
				"version_id", versionID, "kind", kind, "error", err)
			continue
		}
		facts[kind] = current
	}
	return facts
}

// markDecision persists the observation classification. A nil decider (or a
// missing observation id in a degraded fake) is a no-op.
func (d *DiscoveryDelta) markDecision(ctx context.Context, observation DiscoveryObservation, decision string, resourceID, factID int64) error {
	if d.decisions == nil || observation.ObservationID <= 0 {
		return nil
	}
	return d.decisions.MarkDecision(ctx, observation.ObservationID, decision, resourceID, factID)
}

// publishDelta emits exactly one resource.delta per evaluation that produced at
// least one new resource. The payload carries the wave/version identity and the
// per-resource provenance; a single new resource is also flattened to the
// top-level kind/resource_template/resource_id/fact_id/observation_id fields.
func (d *DiscoveryDelta) publishDelta(ctx context.Context, versionID, waveID int64, items []DeltaItem) {
	if d.events == nil || len(items) == 0 {
		return
	}
	eventItems := make([]any, 0, len(items))
	for _, item := range items {
		eventItems = append(eventItems, map[string]any{
			"kind":              item.Kind,
			"method":            item.Method,
			"resource_template": item.ResourceTemplate,
			"host":              item.Host,
			"version_sut_id":    platformid.Format(item.VersionSUTID),
			"environment_id":    platformid.Format(item.EnvironmentID),
			"resource_id":       platformid.Format(item.ResourceID),
			"fact_id":           platformid.Format(item.FactID),
			"observation_id":    platformid.Format(item.ObservationID),
			"case_run_id":       platformid.Format(item.CaseRunID),
		})
	}
	payload := map[string]any{
		"version_id": platformid.Format(versionID),
		"wave_id":    platformid.Format(waveID),
		"source":     "execution",
		"items":      eventItems,
	}
	if len(items) == 1 {
		item := items[0]
		payload["kind"] = item.Kind
		payload["method"] = item.Method
		payload["resource_template"] = item.ResourceTemplate
		payload["host"] = item.Host
		payload["version_sut_id"] = platformid.Format(item.VersionSUTID)
		payload["environment_id"] = platformid.Format(item.EnvironmentID)
		payload["resource_id"] = platformid.Format(item.ResourceID)
		payload["fact_id"] = platformid.Format(item.FactID)
		payload["observation_id"] = platformid.Format(item.ObservationID)
		payload["case_run_id"] = platformid.Format(item.CaseRunID)
	}
	_ = d.events.Publish(ctx, eventingapp.Event{
		Channel:   "version:" + platformid.Format(versionID),
		Type:      "resource.delta",
		Scope:     map[string]any{"version_id": platformid.Format(versionID)},
		Payload:   payload,
		RequestID: requestid.From(ctx),
	})
}

// discoveryDeltaFact builds the version_fact of one newly proposed resource. The
// key is the normalized resource_template (task W1-12); the provenance ids are
// decimal strings in Payload["provenance"] (spec R-EX-3: no typed columns).
func discoveryDeltaFact(versionID int64, observation DiscoveryObservation, item DeltaItem) version.Fact {
	confidence := 0.9
	payload := map[string]any{
		"kind":              item.Kind,
		"method":            item.Method,
		"resource_template": item.ResourceTemplate,
		"host":              item.Host,
		"source":            "execution",
		"environment_id":    platformid.Format(item.EnvironmentID),
		"version_sut_id":    platformid.Format(item.VersionSUTID),
		"resource_id":       platformid.Format(item.ResourceID),
		"provenance": map[string]any{
			"wave_id":        platformid.Format(item.WaveID),
			"case_run_id":    platformid.Format(item.CaseRunID),
			"observation_id": platformid.Format(observation.ObservationID),
		},
	}
	if item.Kind == "page" {
		payload["route_template"] = item.ResourceTemplate
	} else {
		payload["path_template"] = item.ResourceTemplate
	}
	return version.Fact{
		VersionID:  versionID,
		Kind:       item.Kind,
		Key:        item.ResourceTemplate,
		Payload:    payload,
		Confidence: &confidence,
		Source:     "execution",
		SourceRef:  "execution_discovery_observation:" + platformid.Format(observation.ObservationID),
	}
}

// matchingDiscoveryFact finds a current fact describing the same resource. It
// matches on kind + environment + template (method for APIs) so a P9 fact with
// a different key convention is reused instead of duplicated.
func matchingDiscoveryFact(facts []version.Fact, item DeltaItem) *version.Fact {
	for index := range facts {
		fact := &facts[index]
		if fact.Kind != item.Kind {
			continue
		}
		if environmentID := factPayloadInt(fact.Payload, "environment_id"); environmentID != 0 && environmentID != item.EnvironmentID {
			continue
		}
		if item.Kind == "api" {
			if method := strings.ToUpper(factPayloadString(fact.Payload, "method")); method != "" && method != strings.ToUpper(item.Method) {
				continue
			}
		}
		template := factPayloadString(fact.Payload, "route_template")
		if item.Kind == "api" {
			template = factPayloadString(fact.Payload, "path_template")
		}
		if template != "" {
			if discoveryTemplateKey(template) == discoveryTemplateKey(item.ResourceTemplate) {
				return fact
			}
			continue
		}
		if discoveryTemplateKey(fact.Key) == discoveryTemplateKey(item.ResourceTemplate) ||
			discoveryKeySuffix(fact.Key) == discoveryTemplateKey(item.ResourceTemplate) {
			return fact
		}
	}
	return nil
}

// factPayloadString reads a string payload value (P9 stores plain strings).
func factPayloadString(payload map[string]any, key string) string {
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

// factPayloadInt reads a numeric payload value. JSONB decoding yields float64;
// direct Go fixtures may use int/int64/string.
func factPayloadInt(payload map[string]any, key string) int64 {
	switch value := payload[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// discoveryTemplateKey normalizes a route/path template for equality: trimmed,
// with trailing slashes removed (root stays "/").
func discoveryTemplateKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	trimmed := strings.TrimRight(value, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

// discoveryKeySuffix extracts the template-shaped tail of a P9 fact key
// (`page:env:9:https://host/orders/{id}`, `api:env:9:GET /orders/{id}`).
func discoveryKeySuffix(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if index := strings.IndexByte(key, ' '); index >= 0 && strings.HasPrefix(key, "api:") {
		key = key[index+1:]
	}
	if strings.HasPrefix(key, "page:") || strings.HasPrefix(key, "api:") {
		if index := strings.Index(key, "://"); index >= 0 {
			rest := key[index+3:]
			if slash := strings.IndexByte(rest, '/'); slash >= 0 {
				return discoveryTemplateKey(rest[slash:])
			}
		}
		for _, separator := range []int{strings.LastIndex(key, ":"), strings.LastIndex(key, " ")} {
			if separator >= 0 && separator+1 < len(key) {
				candidate := key[separator+1:]
				if strings.HasPrefix(candidate, "/") {
					return discoveryTemplateKey(candidate)
				}
			}
		}
	}
	return discoveryTemplateKey(key)
}

// discoveryProposedURL rebuilds a page URL for the proposal service from the
// redacted host + route template. It is only used as the proposal's identity
// input; the route template column remains the catalog key.
func discoveryProposedURL(item DeltaItem) string {
	host := strings.TrimSpace(item.Host)
	if host == "" {
		return ""
	}
	if bare := strings.Trim(host, "[]"); net.ParseIP(bare) != nil && strings.Contains(bare, ":") {
		host = "[" + bare + "]"
	}
	path := item.ResourceTemplate
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "http://" + host + path
}

// parseResourceID reads the decimal string ids of the resource views.
func parseResourceID(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func newDeltaResult() DeltaResult {
	return DeltaResult{
		New:        []DeltaItem{},
		Proposed:   []DeltaItem{},
		Approved:   []DeltaItem{},
		Rejected:   []DeltaItem{},
		OutOfScope: []DeltaItem{},
	}
}

func deltaItemFromObservation(observation DiscoveryObservation, waveID int64) DeltaItem {
	item := DeltaItem{
		Kind:             observation.Kind,
		Method:           observation.Method,
		ResourceTemplate: discoveryTemplateKey(observation.ResourceTemplate),
		Host:             observation.Host,
		VersionSUTID:     observation.VersionSUTID,
		EnvironmentID:    observation.EnvironmentID,
		WaveID:           observation.WaveID,
		CaseRunID:        observation.CaseRunID,
		ObservationID:    observation.ObservationID,
		ResourceID:       observation.ResourceID,
		FactID:           observation.FactID,
		Decision:         observation.Decision,
	}
	if item.WaveID == 0 {
		item.WaveID = waveID
	}
	return item
}

func appendDeltaItem(result *DeltaResult, classification string, item DeltaItem) {
	item.Decision = classification
	switch classification {
	case "new":
		result.New = append(result.New, item)
	case "proposed":
		result.Proposed = append(result.Proposed, item)
	case "approved":
		result.Approved = append(result.Approved, item)
	case "rejected":
		result.Rejected = append(result.Rejected, item)
	case "out_of_scope":
		result.OutOfScope = append(result.OutOfScope, item)
	}
}

// sortDeltaResult makes the five slices deterministic regardless of the
// observation read order.
func sortDeltaResult(result *DeltaResult) {
	for _, items := range [][]DeltaItem{result.New, result.Proposed, result.Approved, result.Rejected, result.OutOfScope} {
		sort.SliceStable(items, func(i, j int) bool {
			left, right := items[i], items[j]
			if left.Kind != right.Kind {
				return left.Kind < right.Kind
			}
			if left.ResourceTemplate != right.ResourceTemplate {
				return left.ResourceTemplate < right.ResourceTemplate
			}
			if left.Method != right.Method {
				return left.Method < right.Method
			}
			if left.EnvironmentID != right.EnvironmentID {
				return left.EnvironmentID < right.EnvironmentID
			}
			return left.VersionSUTID < right.VersionSUTID
		})
	}
}
