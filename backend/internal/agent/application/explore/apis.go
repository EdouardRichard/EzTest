package explore

// This file owns spec §11 capability #3: code-first API discovery. Endpoints
// come from static repository sources (Gin route registration, OpenAPI/Swagger,
// frontend api-client call sites and generated API type definitions) and are
// merged into exactly one of three coverage states:
//
//   - declared_unused  — declared by the code but absent from the api-client
//                        (a coverage gap).
//   - used_undeclared  — called by the api-client but not declared by the
//                        backend (a high-value exploration point).
//   - declared_used    — declared and observed (covered).
//
// "Used" is a static observation of the frontend api-client, not browser
// traffic: runtime fetch/XHR capture is a 0.3 dynamic source and is
// deliberately absent. APIDiscovery has no executor/network port at all.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// KindAPI is the version_fact kind of one discovered API.
const KindAPI = "api"

// DefaultMaxAPIs bounds the number of API facts one run may write.
const DefaultMaxAPIs = 500

// APIState is the coverage state of one endpoint.
type APIState string

// The three coverage states of spec §11.
const (
	APIStateDeclaredUnused APIState = "declared_unused"
	APIStateUsedUndeclared APIState = "used_undeclared"
	APIStateDeclaredUsed   APIState = "declared_used"
)

// SourceProvider returns the repository text files of a version's bound code
// repositories. Implementations do not resolve paths or execute code; APIDiscovery
// only reads text.
type SourceProvider interface {
	SourceFiles(ctx context.Context, versionID int64) ([]codeintelapp.SourceFile, error)
}

// APIDiscoveryConfig binds one static scan to a version SUT environment and its
// bounds. MaxFiles/MaxFileBytes may only lower the extractor defaults.
type APIDiscoveryConfig struct {
	VersionSUTID  int64
	EnvironmentID int64
	MaxAPIs       int
	MaxFiles      int
	MaxFileBytes  int
}

// APIDiscoveryDeps are the narrow ports consumed by the static scan. Facts may
// be nil: the merged result is still returned.
type APIDiscoveryDeps struct {
	Sources SourceProvider
	Facts   FactRecorder
}

// APICandidate is one merged endpoint with its three-state classification and
// provenance. It carries no source body, header/cookie or query value.
type APICandidate struct {
	APIKey          string   `json:"api_key"`
	Method          string   `json:"method"`
	PathTemplate    string   `json:"path_template"`
	State           APIState `json:"state"`
	DeclaredSources []string `json:"declared_sources"`
	UsedSources     []string `json:"used_sources"`
	DeclaredFiles   []string `json:"declared_files,omitempty"`
	UsedFiles       []string `json:"used_files,omitempty"`
	Params          []string `json:"params"`
	Auth            string   `json:"auth,omitempty"`
}

// APIDiscoveryResult is the deterministic output of one static API scan.
type APIDiscoveryResult struct {
	VersionID      int64          `json:"version_id"`
	APIs           []APICandidate `json:"apis"`
	DeclaredUnused int            `json:"declared_unused"`
	UsedUndeclared int            `json:"used_undeclared"`
	DeclaredUsed   int            `json:"declared_used"`
	FilesScanned   int            `json:"files_scanned"`
	FilesSkipped   int            `json:"files_skipped"`
	Truncated      bool           `json:"truncated"`
	FactIDs        []int64        `json:"fact_ids"`
}

// APIDiscovery is one bounded code-first API scan.
type APIDiscovery struct {
	cfg     APIDiscoveryConfig
	sources SourceProvider
	facts   FactRecorder
}

// NewAPIDiscovery wires one static API scan.
func NewAPIDiscovery(cfg APIDiscoveryConfig, deps APIDiscoveryDeps) *APIDiscovery {
	return &APIDiscovery{cfg: cfg, sources: deps.Sources, facts: deps.Facts}
}

type apiAggregate struct {
	method          string
	pathTemplate    string
	declaredSources map[string]bool
	usedSources     map[string]bool
	declaredFiles   map[string]bool
	usedFiles       map[string]bool
	params          map[string]bool
	auth            string
}

// Run scans the version's source files, merges static declarations with
// api-client observations into the three states, and records one `api` fact per
// endpoint. Endpoint ordering is method/path; facts are written after the merge
// so a failed scan never writes partial facts.
func (d *APIDiscovery) Run(ctx context.Context, versionID int64) (APIDiscoveryResult, error) {
	result := APIDiscoveryResult{VersionID: versionID, APIs: []APICandidate{}}
	if d == nil || d.sources == nil {
		return result, errors.New("api discovery has no source provider")
	}
	if versionID <= 0 {
		return result, errors.New("api discovery requires a positive version id")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	files, err := d.sources.SourceFiles(ctx, versionID)
	if err != nil {
		return result, fmt.Errorf("read api discovery sources: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	extraction := codeintelapp.ExtractStaticAPIs(files, codeintelapp.StaticAPIOptions{
		MaxFiles:     d.cfg.MaxFiles,
		MaxFileBytes: d.cfg.MaxFileBytes,
		MaxAPIs:      d.cfg.MaxAPIs,
	})
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.FilesScanned = extraction.FilesScanned
	result.FilesSkipped = extraction.FilesSkipped
	result.Truncated = extraction.Truncated

	merged := make(map[string]*apiAggregate)
	for _, api := range extraction.Declared {
		aggregate := merged[api.Method+" "+api.PathTemplate]
		if aggregate == nil {
			aggregate = newAPIAggregate(api.Method, api.PathTemplate)
			merged[api.Method+" "+api.PathTemplate] = aggregate
		}
		aggregate.addDeclared(api)
	}
	for _, api := range extraction.Used {
		aggregate := merged[api.Method+" "+api.PathTemplate]
		if aggregate == nil {
			aggregate = newAPIAggregate(api.Method, api.PathTemplate)
			merged[api.Method+" "+api.PathTemplate] = aggregate
		}
		aggregate.addUsed(api)
	}

	candidates := make([]APICandidate, 0, len(merged))
	for _, aggregate := range merged {
		candidates = append(candidates, aggregate.candidate())
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Method != candidates[j].Method {
			return candidates[i].Method < candidates[j].Method
		}
		return candidates[i].PathTemplate < candidates[j].PathTemplate
	})

	maxAPIs := positiveInt(d.cfg.MaxAPIs, DefaultMaxAPIs)
	if len(candidates) > maxAPIs {
		candidates = candidates[:maxAPIs]
		result.Truncated = true
	}
	result.APIs = candidates
	for _, candidate := range candidates {
		switch candidate.State {
		case APIStateDeclaredUnused:
			result.DeclaredUnused++
		case APIStateUsedUndeclared:
			result.UsedUndeclared++
		case APIStateDeclaredUsed:
			result.DeclaredUsed++
		}
	}

	if d.facts == nil {
		return result, nil
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		fact := apiFact(versionID, d.cfg, candidate)
		stored, _, recordErr := d.facts.Record(ctx, fact)
		if recordErr != nil {
			return result, fmt.Errorf("record %s fact: %w", KindAPI, recordErr)
		}
		result.FactIDs = append(result.FactIDs, stored.ID)
	}
	return result, nil
}

func newAPIAggregate(method, pathTemplate string) *apiAggregate {
	return &apiAggregate{
		method:          method,
		pathTemplate:    pathTemplate,
		declaredSources: map[string]bool{},
		usedSources:     map[string]bool{},
		declaredFiles:   map[string]bool{},
		usedFiles:       map[string]bool{},
		params:          map[string]bool{},
	}
}

func (a *apiAggregate) addDeclared(api codeintelapp.StaticAPI) {
	a.declaredSources[api.Source] = true
	if file := strings.TrimSpace(api.File); file != "" {
		a.declaredFiles[file] = true
	}
	a.addParams(api.Params)
	if a.auth == "" {
		a.auth = strings.TrimSpace(api.Auth)
	}
}

func (a *apiAggregate) addUsed(api codeintelapp.StaticAPI) {
	a.usedSources[api.Source] = true
	if file := strings.TrimSpace(api.File); file != "" {
		a.usedFiles[file] = true
	}
	a.addParams(api.Params)
	if a.auth == "" {
		a.auth = strings.TrimSpace(api.Auth)
	}
}

func (a *apiAggregate) addParams(params []string) {
	for _, parameter := range params {
		if trimmed := strings.TrimSpace(parameter); trimmed != "" {
			a.params[trimmed] = true
		}
	}
}

func (a *apiAggregate) candidate() APICandidate {
	declaredSources := orderedSources(a.declaredSources)
	usedSources := orderedSources(a.usedSources)
	state := APIStateDeclaredUnused
	switch {
	case len(declaredSources) > 0 && len(usedSources) > 0:
		state = APIStateDeclaredUsed
	case len(usedSources) > 0:
		state = APIStateUsedUndeclared
	}
	return APICandidate{
		APIKey:          a.method + " " + a.pathTemplate,
		Method:          a.method,
		PathTemplate:    a.pathTemplate,
		State:           state,
		DeclaredSources: declaredSources,
		UsedSources:     usedSources,
		DeclaredFiles:   boundedFileList(a.declaredFiles),
		UsedFiles:       boundedFileList(a.usedFiles),
		Params:          sortedSet(a.params),
		Auth:            a.auth,
	}
}

var apiSourceOrder = map[string]int{
	codeintelapp.StaticSourceGinRoute:       0,
	codeintelapp.StaticSourceOpenAPI:        1,
	codeintelapp.StaticSourceTypeDefinition: 2,
	codeintelapp.StaticSourceAPIClient:      0,
}

func orderedSources(set map[string]bool) []string {
	if len(set) == 0 {
		return []string{}
	}
	sources := make([]string, 0, len(set))
	for source := range set {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool {
		left, leftOK := apiSourceOrder[sources[i]]
		right, rightOK := apiSourceOrder[sources[j]]
		if leftOK && rightOK && left != right {
			return left < right
		}
		if leftOK != rightOK {
			return leftOK
		}
		return sources[i] < sources[j]
	})
	return sources
}

func sortedSet(set map[string]bool) []string {
	if len(set) == 0 {
		return []string{}
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func boundedFileList(set map[string]bool) []string {
	files := sortedSet(set)
	const maxFiles = 5
	if len(files) > maxFiles {
		return files[:maxFiles]
	}
	return files
}

func apiFact(versionID int64, cfg APIDiscoveryConfig, candidate APICandidate) version.Fact {
	confidence := 0.9
	switch candidate.State {
	case APIStateDeclaredUnused:
		confidence = 0.85
	case APIStateUsedUndeclared:
		confidence = 0.8
	}
	payload := map[string]any{
		"api_key":          candidate.APIKey,
		"method":           candidate.Method,
		"path_template":    candidate.PathTemplate,
		"state":            string(candidate.State),
		"declared_sources": copyStrings(candidate.DeclaredSources),
		"used_sources":     copyStrings(candidate.UsedSources),
		"declared_files":   copyStrings(candidate.DeclaredFiles),
		"used_files":       copyStrings(candidate.UsedFiles),
		"params":           copyStrings(candidate.Params),
		"auth":             candidate.Auth,
		"source":           "codeintel_static",
	}
	if cfg.VersionSUTID > 0 {
		payload["version_sut_id"] = cfg.VersionSUTID
	}
	if cfg.EnvironmentID > 0 {
		payload["environment_id"] = cfg.EnvironmentID
	}
	sourceRef := ""
	if len(candidate.DeclaredFiles) > 0 {
		sourceRef = candidate.DeclaredFiles[0]
	} else if len(candidate.UsedFiles) > 0 {
		sourceRef = candidate.UsedFiles[0]
	}
	return version.Fact{
		VersionID:  versionID,
		Kind:       KindAPI,
		Key:        apiFactKey(cfg.EnvironmentID, candidate.APIKey),
		Payload:    payload,
		Confidence: &confidence,
		Source:     "explorer",
		SourceRef:  sourceRef,
	}
}

func apiFactKey(environmentID int64, apiKey string) string {
	if environmentID > 0 {
		return fmt.Sprintf("api:env:%d:%s", environmentID, apiKey)
	}
	return "api:" + apiKey
}
