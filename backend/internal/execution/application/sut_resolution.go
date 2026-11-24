package executionapp

import (
	"context"
	"net/url"
	"sort"
	"strings"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// P16 SUT autonomous resolution (v0.2.4-F, SC-F2; design v1.3.1 §6.2).
//
// The execution team resolves a case's natural-language sut_description to the
// SUT(s) it targets. This service is the SERVER-SIDE truth of that resolution:
// it reads the version's EFFECTIVE SUT catalog (approved SUTs and their approved
// assets only), matches the description deterministically, verifies the matched
// SUTs' assets, and appends the sut_resolution fact (case_run_id -> sut_ids +
// resolution_method + the page/API asset ids the run touched/verified valid).
//
// Capability-first / GD-21: the service never narrows the agent's operation
// surface and never gates execution. A catalog hit records the verified assets
// (the knowledge-dimension truth source, SC-F10, even for a run that never
// explores); a missing match OR an asset-level gap (R-E8: a catalog SUT with no
// indexed code repo has no code-fact proposals, hence empty assets) records
// method=exploration and leaves the agent to explore autonomously through the
// general channels (browser.cli + http + exec). On ambiguity the resolution
// SELECTS the top-ranked SUT (controller ruling P19; design §6.2 "歧义按上下文
// 选择"), records the Ambiguous flag, and keeps only the selected SUT's validated
// assets; human.request stays an optional escape, not a gate (§6.6 full autonomy).
// The model only submits candidates elsewhere (T10 asset registration); this fact
// is computed and validated server-side.

// Resolution methods recorded on sut_resolution.resolution_method. Free text
// owned by this service (the sut_registration.resolution_method precedent).
const (
	// ResolutionMethodCatalogHit: the description matched a catalog SUT whose
	// effective assets were verified valid; the selected SUT's asset ids are
	// recorded as the validated (touched) set.
	ResolutionMethodCatalogHit = "catalog_hit"
	// ResolutionMethodExploration: no catalog match, or the selected SUT has no
	// effective assets (R-E8 asset-level gap); the agent must explore.
	ResolutionMethodExploration = "exploration"
)

// EventSUTResolved announces one recorded SUT resolution on the version channel.
// The payload carries the decimal case_run_id, the resolved sut_ids, the
// resolution_method, the touched page/API asset ids (decimal strings), and the
// ambiguous / needs_exploration booleans (P19/Minor-2 production readers).
const EventSUTResolved = "sut.resolved"

// ResolvedSUT is one EFFECTIVE SUT of the version's bound environment as the
// resolution service consumes it: identity/labels (name/intro/base_url/source/
// status) plus the EFFECTIVE page/API asset ids (approved only). The production
// adapter derives it from versionapp.EnvironmentSUTCatalog.EffectiveSUTs — the
// single effective-scope projection — so a proposed/rejected SUT never enters the
// resolution range (R-B4 trust boundary). The service trusts this projection and
// does NOT re-implement the SUT-status gate (constitution art.5: one authority).
type ResolvedSUT struct {
	SUTID         int64
	EnvironmentID int64
	Name          string
	Intro         string
	BaseURL       string
	Source        string
	Status        string
	PageIDs       []int64
	APIIDs        []int64
}

func (s ResolvedSUT) hasAssets() bool { return len(s.PageIDs) > 0 || len(s.APIIDs) > 0 }

// SUTCatalogReader reads the effective SUT catalog of a version's bound
// environment. The execution adapter implements it over the version binding
// reader's EffectiveSUTs projection (approved SUTs + approved/active assets).
type SUTCatalogReader interface {
	EffectiveSUTCatalog(ctx context.Context, versionID int64) ([]ResolvedSUT, error)
}

// SUTResolutionOutcome is the result of resolving one case run. NeedsExploration
// tells the caller (the task-provider decorator) that the agent must explore;
// Ambiguous reports that more than one catalog SUT matched (resolved by context).
// Recorded reports whether a NEW fact was appended (false on an idempotent
// re-resolution whose latest fact already matches).
type SUTResolutionOutcome struct {
	SUTIDs           []int64
	ResolutionMethod string
	TouchedPageIDs   []int64
	TouchedAPIIDs    []int64
	NeedsExploration bool
	Ambiguous        bool
	Recorded         bool
}

// SUTResolutionDeps wires the resolution service. Catalog and Resolutions are
// required; Events/Clock are optional (a nil publisher skips the event, a nil
// clock falls back to the domain normalize/DB default).
type SUTResolutionDeps struct {
	Catalog     SUTCatalogReader
	Resolutions execution.SUTResolutionRepository
	Events      EventPublisher
	Clock       Clock
}

// SUTResolutionService computes and records the P16 SUT resolution fact.
type SUTResolutionService struct {
	catalog     SUTCatalogReader
	resolutions execution.SUTResolutionRepository
	events      EventPublisher
	clock       Clock
}

// NewSUTResolutionService wires the resolution service.
func NewSUTResolutionService(deps SUTResolutionDeps) *SUTResolutionService {
	return &SUTResolutionService{
		catalog:     deps.Catalog,
		resolutions: deps.Resolutions,
		events:      deps.Events,
		clock:       deps.Clock,
	}
}

// ResolveForRun resolves the case run's sut_description against the version's
// effective SUT catalog, then appends the sut_resolution fact and publishes
// sut.resolved. It is idempotent: when the run's latest stored fact already
// equals the computed resolution it appends/publishes nothing (Recorded=false).
// A catalog-read or fact-write failure is returned (observable, retryable via the
// work queue) — never a silent empty resolution (constitution art.7.6). An
// exploration outcome is NOT an error: it is a valid resolution that leaves the
// agent to explore.
func (s *SUTResolutionService) ResolveForRun(ctx context.Context, versionID, caseRunID int64, sutDescription string) (SUTResolutionOutcome, error) {
	if s.catalog == nil || s.resolutions == nil {
		return SUTResolutionOutcome{}, platform.Unavailable("sut resolution service is not configured")
	}
	if caseRunID <= 0 {
		return SUTResolutionOutcome{}, platform.Invalid("case_run_id", "must be a positive decimal ID")
	}
	catalog, err := s.catalog.EffectiveSUTCatalog(ctx, versionID)
	if err != nil {
		return SUTResolutionOutcome{}, err
	}
	out := resolveDescription(sutDescription, catalog)

	fact := execution.SUTResolution{
		CaseRunID:        caseRunID,
		SUTIDs:           out.SUTIDs,
		ResolutionMethod: out.ResolutionMethod,
		TouchedPageIDs:   out.TouchedPageIDs,
		TouchedAPIIDs:    out.TouchedAPIIDs,
		Ambiguous:        out.Ambiguous,
	}
	fact.Normalize()
	if err := fact.Validate(); err != nil {
		return SUTResolutionOutcome{}, err
	}
	// Idempotency: a resumed dispatch re-resolves the same run; if the latest
	// stored fact already matches, do not append a duplicate (the production read
	// path of the append-only fact).
	existing, err := s.resolutions.ListSUTResolutions(ctx, caseRunID)
	if err != nil {
		return SUTResolutionOutcome{}, err
	}
	if len(existing) > 0 && sameResolution(existing[len(existing)-1], fact) {
		return out, nil
	}
	if s.clock != nil {
		fact.CreatedAt = s.clock.Now()
	}
	written, err := s.resolutions.AppendSUTResolution(ctx, fact)
	if err != nil {
		return SUTResolutionOutcome{}, err
	}
	out.Recorded = true
	s.publishResolved(ctx, versionID, written)
	return out, nil
}

// publishResolved emits sut.resolved on the version channel (best-effort, the
// execution publishEvent precedent: the fact is already persisted).
func (s *SUTResolutionService) publishResolved(ctx context.Context, versionID int64, fact execution.SUTResolution) {
	if s.events == nil || versionID <= 0 {
		return
	}
	payload := map[string]any{
		"case_run_id":       platformid.Format(fact.CaseRunID),
		"sut_ids":           idsToStrings(fact.SUTIDs),
		"resolution_method": fact.ResolutionMethod,
		"touched_page_ids":  idsToStrings(fact.TouchedPageIDs),
		"touched_api_ids":   idsToStrings(fact.TouchedAPIIDs),
		// P19/Minor-2: the ambiguity flag and the exploration need are part of the
		// event so downstream has a production reader (DoD no unwired).
		// needs_exploration is derived from the method (single source).
		"ambiguous":         fact.Ambiguous,
		"needs_exploration": fact.ResolutionMethod == ResolutionMethodExploration,
	}
	publishEvent(ctx, s.events, EventSUTResolved, versionID, map[string]any{
		"case_run_id": platformid.Format(fact.CaseRunID),
	}, payload, nil)
}

// sameResolution reports whether two facts carry the same resolved set, method
// and touched assets (order-independent). It is the idempotency comparison.
func sameResolution(a, b execution.SUTResolution) bool {
	return a.ResolutionMethod == b.ResolutionMethod &&
		a.Ambiguous == b.Ambiguous &&
		equalIDSets(a.SUTIDs, b.SUTIDs) &&
		equalIDSets(a.TouchedPageIDs, b.TouchedPageIDs) &&
		equalIDSets(a.TouchedAPIIDs, b.TouchedAPIIDs)
}

func equalIDSets(a, b []int64) bool {
	as, bs := sortedUniqueInt64(a), sortedUniqueInt64(b)
	if len(as) != len(bs) {
		return false
	}
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// resolveDescription is the deterministic matcher + asset verification. It is a
// pure function of the description and the effective catalog so it is unit-
// testable without ports.
//
// Touched semantics (controller ruling P20): resolution runs at Prepare, BEFORE
// the run executes, so "touched" is the resolved/selected SUT's VALIDATED asset
// set (the honest available true source — no fabrication). T14's knowledge
// projection consumes this validated-set; post-execution refinement to the
// actually-used assets is a future enhancement, not this version.
func resolveDescription(description string, catalog []ResolvedSUT) SUTResolutionOutcome {
	matches := matchCatalog(description, catalog)
	// Context fallback: an empty description with a single effective SUT is
	// unambiguous (the executionSUTBindings single-binding precedent).
	if len(matches) == 0 && strings.TrimSpace(description) == "" && len(catalog) == 1 {
		matches = []sutMatch{{sut: catalog[0]}}
	}
	if len(matches) == 0 {
		return SUTResolutionOutcome{
			ResolutionMethod: ResolutionMethodExploration,
			NeedsExploration: true,
			SUTIDs:           []int64{},
			TouchedPageIDs:   []int64{},
			TouchedAPIIDs:    []int64{},
		}
	}
	// Controller ruling P19 (design §6.2 "歧义按上下文选择" means SELECT, not
	// union): pick the TOP-RANKED SUT. matchCatalog's ranking already prefers an
	// asset-bearing candidate (the actionable target), then the lowest SUT id as
	// the deterministic tie-break. The touched/validated asset set is the
	// SELECTED SUT's only — an unselected candidate's assets never enter the
	// knowledge-dimension true source (Review Focus 2). Ambiguous records that
	// the resolution selected from more than one match.
	selected := matches[0].sut
	out := SUTResolutionOutcome{
		SUTIDs:         []int64{selected.SUTID},
		TouchedPageIDs: sortedUniqueInt64(selected.PageIDs),
		TouchedAPIIDs:  sortedUniqueInt64(selected.APIIDs),
		Ambiguous:      len(matches) > 1,
	}
	// R-E8 asset-level gap: matched SUT(s) but no effective page/API asset to
	// verify (e.g. a no-indexed-repo SUT) -> the agent must explore.
	if len(out.TouchedPageIDs) == 0 && len(out.TouchedAPIIDs) == 0 {
		out.ResolutionMethod = ResolutionMethodExploration
		out.NeedsExploration = true
		return out
	}
	out.ResolutionMethod = ResolutionMethodCatalogHit
	return out
}

// sutMatch is one catalog SUT that matched the description on a PRIMARY criterion
// (R-B4: base_url/domain + semantic). parentPath is the auxiliary same-origin
// signal that boosts ranking but never creates a match on its own.
type sutMatch struct {
	sut        ResolvedSUT
	score      int
	parentPath bool
}

// matchCatalog returns the primary-matched SUTs ranked by context (score, then
// asset presence, then ascending SUT id). A SUT matches only on a primary
// criterion: semantic (name/intro), a domain that uniquely identifies it, or its
// base_url path. A mere parent-path same-origin mention (shared host, no path or
// semantic confirmation) is auxiliary and never the sole criterion (R-B4).
func matchCatalog(description string, catalog []ResolvedSUT) []sutMatch {
	desc := strings.ToLower(strings.TrimSpace(description))
	urls := extractURLs(description)
	hostCount := make(map[string]int, len(catalog))
	for _, sut := range catalog {
		if h := sutHostname(sut.BaseURL); h != "" {
			hostCount[h]++
		}
	}
	matches := make([]sutMatch, 0)
	for _, sut := range catalog {
		host := sutHostname(sut.BaseURL)
		origin := sutOrigin(sut.BaseURL)
		basePath := sutBasePath(sut.BaseURL)
		hostMentioned := host != "" && strings.Contains(desc, host)
		pathMentioned := basePath != "" && strings.Contains(desc, basePath)
		semantic := semanticMatch(desc, sut)
		domainPrimary := hostMentioned && hostCount[host] == 1
		baseURLPrimary := hostMentioned && pathMentioned
		if !semantic && !domainPrimary && !baseURLPrimary {
			continue
		}
		parentPath := false
		if origin != "" {
			for _, u := range urls {
				if urlOrigin(u) == origin {
					parentPath = true
					break
				}
			}
		}
		score := 0
		if semantic {
			score += 2
		}
		if domainPrimary {
			score += 2
		}
		if baseURLPrimary {
			score += 2
		}
		if parentPath {
			score++
		}
		matches = append(matches, sutMatch{sut: sut, score: score, parentPath: parentPath})
	}
	// Choose by context: higher score first, then a SUT with effective assets
	// (the actionable target), then the deterministic lowest SUT id.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if matches[i].sut.hasAssets() != matches[j].sut.hasAssets() {
			return matches[i].sut.hasAssets()
		}
		return matches[i].sut.SUTID < matches[j].sut.SUTID
	})
	return matches
}

// semanticMatch reports whether the SUT's name or intro shares meaningful content
// with the (lowercased) description. The full name as a substring covers CJK
// names (no word separators); significant tokens cover multi-word Latin names and
// intros that are referenced piecewise.
func semanticMatch(desc string, sut ResolvedSUT) bool {
	name := strings.ToLower(strings.TrimSpace(sut.Name))
	if name != "" && strings.Contains(desc, name) {
		return true
	}
	for _, tok := range significantTokens(sut.Name) {
		if strings.Contains(desc, tok) {
			return true
		}
	}
	for _, tok := range significantTokens(sut.Intro) {
		if strings.Contains(desc, tok) {
			return true
		}
	}
	return false
}

// significantTokens splits text into lowercased tokens worth matching: runs of
// letters/digits (Latin words) and runs of CJK characters. A CJK run is kept whole
// (splitting it per character would match almost any description); a Latin token
// must be at least 3 runes to avoid matching ubiquitous short words.
func significantTokens(text string) []string {
	var tokens []string
	var cur []rune
	var curCJK bool
	flush := func() {
		if len(cur) == 0 {
			return
		}
		tok := string(cur)
		if curCJK || len(cur) >= 3 {
			tokens = append(tokens, tok)
		}
		cur = cur[:0]
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case isCJK(r):
			if !curCJK {
				flush()
				curCJK = true
			}
			cur = append(cur, r)
		case isWordRune(r):
			if curCJK {
				flush()
				curCJK = false
			}
			cur = append(cur, r)
		default:
			flush()
			curCJK = false
		}
	}
	flush()
	return tokens
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

func isWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// extractURLs finds the http(s) URLs mentioned in a description. It is a loose
// scanner (the description is prose, not markup): each http(s):// token is parsed
// with net/url; unparseable tokens are skipped.
func extractURLs(text string) []*url.URL {
	var urls []*url.URL
	lower := strings.ToLower(text)
	for _, scheme := range []string{"https://", "http://"} {
		rest := lower
		for {
			idx := strings.Index(rest, scheme)
			if idx < 0 {
				break
			}
			rest = rest[idx:]
			end := strings.IndexAny(rest, " \t\r\n\"'<>）)】，,；;")
			token := rest
			if end >= 0 {
				token = rest[:end]
				rest = rest[end:]
			} else {
				rest = ""
			}
			if u, err := url.Parse(token); err == nil && u.Host != "" {
				urls = append(urls, u)
			}
			if rest == "" {
				break
			}
		}
	}
	return urls
}

func sutHostname(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func sutOrigin(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return urlOrigin(u)
}

func urlOrigin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// sutBasePath returns the normalized path identity of a base_url (no trailing
// slash). A root/empty path carries no identity (""), so it cannot discriminate
// shared-host SUTs by path.
func sutBasePath(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	if p == "" || p == "/" {
		return ""
	}
	return strings.ToLower(p)
}
