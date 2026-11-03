package explore

// This file owns spec §11 capability #2: runtime page discovery. It crawls a
// bounded set of pages through the executor explore action family, normalizes
// URLs/query parameters/hash routes, derives route templates, inventories the
// interactive elements and writes one `page` version fact per page.
//
// It never captures network traffic: page discovery only consumes the DOM
// snapshot returned by explore.open/snapshot. Dynamic API observation is a 0.3
// source and is deliberately absent.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	resource "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// KindPage is the version_fact kind of one discovered page.
const KindPage = "page"

// Page discovery defaults. Every bound is independent so a chatty SUT cannot
// exhaust time, memory or fact storage.
const (
	DefaultMaxPages           = 40
	DefaultMaxPageDepth       = 3
	DefaultMaxLinksPerPage    = 50
	DefaultMaxElementsPerPage = 100
)

// PageDiscoveryConfig binds one crawl to a version SUT environment and a start
// location. StartURL is the normal crawl seed; an existing executor PageID or a
// captured StorageStateRef can be used for an already-authenticated session.
// Credentials never enter this config or the produced facts.
type PageDiscoveryConfig struct {
	VersionSUTID       int64
	EnvironmentID      int64
	StartURL           string
	SeedURLs           []string
	PageID             string
	StorageStateRef    string
	AllowedHosts       []string
	MaxPages           int
	MaxDepth           int
	MaxLinksPerPage    int
	MaxElementsPerPage int
}

// PageDiscoveryDeps are the narrow ports consumed by the crawl. Facts may be
// nil: the result is still useful to a caller that persists it elsewhere.
type PageDiscoveryDeps struct {
	Executor Executor
	Facts    FactRecorder
}

// PageElement is one interactive DOM element of a discovered page. Values are
// never read; only the selector/name/type/href inventory is persisted.
type PageElement struct {
	Index    int    `json:"index"`
	Tag      string `json:"tag"`
	Selector string `json:"selector"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Text     string `json:"text"`
	Href     string `json:"href,omitempty"`
	Visible  bool   `json:"visible"`
	Disabled bool   `json:"disabled"`
}

// DiscoveredPage is one normalized page of the crawl. PageKey is the
// origin-qualified route template (`http://sut/orders/{id}`), so it is stable
// across concrete IDs while remaining unique per host. RouteTemplate is the
// knowledge-facing key (`/orders/{id}`); HashRoute carries a hash-router route.
type DiscoveredPage struct {
	PageKey       string        `json:"page_key"`
	URL           string        `json:"url"`
	RouteTemplate string        `json:"route_template"`
	HashRoute     string        `json:"hash_route,omitempty"`
	Title         string        `json:"title"`
	QueryKeys     []string      `json:"query_keys"`
	Depth         int           `json:"depth"`
	ParentKey     string        `json:"parent_page_key,omitempty"`
	Elements      []PageElement `json:"elements"`
	Children      []string      `json:"children"`
}

// PageDiscoveryResult is the deterministic output of one crawl.
type PageDiscoveryResult struct {
	VersionID      int64            `json:"version_id"`
	Pages          []DiscoveredPage `json:"pages"`
	RouteTemplates []string         `json:"route_templates"`
	Truncated      bool             `json:"truncated"`
	SkippedLinks   int              `json:"skipped_links"`
	FactIDs        []int64          `json:"fact_ids"`
}

// PageDiscovery is one bounded runtime page crawl.
type PageDiscovery struct {
	cfg   PageDiscoveryConfig
	exec  Executor
	facts FactRecorder
}

// NewPageDiscovery wires one crawl.
func NewPageDiscovery(cfg PageDiscoveryConfig, deps PageDiscoveryDeps) *PageDiscovery {
	return &PageDiscovery{cfg: cfg, exec: deps.Executor, facts: deps.Facts}
}

type pageTarget struct {
	URL           string
	PageKey       string
	RouteTemplate string
	HashRoute     string
	QueryKeys     []string
	Host          string
	Path          string
}

type pageCrawlItem struct {
	target     pageTarget
	depth      int
	parentKey  string
	pageID     string
	storageRef string
}

// Run crawls the configured start location and records one page fact per
// discovered page. A page that cannot be opened is skipped unless it is the
// crawl seed; only configuration errors, a broken seed or a canceled context
// fail the run. Facts are written after the crawl, so a failed run never leaves
// partial page facts.
func (d *PageDiscovery) Run(ctx context.Context, versionID int64) (PageDiscoveryResult, error) {
	result := PageDiscoveryResult{VersionID: versionID, Pages: []DiscoveredPage{}, RouteTemplates: []string{}}
	if d == nil || d.exec == nil {
		return result, errors.New("page discovery has no executor")
	}
	if versionID <= 0 {
		return result, errors.New("page discovery requires a positive version id")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	cfg := d.cfg
	cfg.MaxPages = positiveInt(cfg.MaxPages, DefaultMaxPages)
	cfg.MaxDepth = positiveInt(cfg.MaxDepth, DefaultMaxPageDepth)
	cfg.MaxLinksPerPage = positiveInt(cfg.MaxLinksPerPage, DefaultMaxLinksPerPage)
	cfg.MaxElementsPerPage = positiveInt(cfg.MaxElementsPerPage, DefaultMaxElementsPerPage)

	queue, allowedHosts, err := d.seedQueue(ctx, cfg)
	if err != nil {
		return result, err
	}
	if len(queue) == 0 {
		return result, errors.New("page discovery requires a start URL, seed URL or page id")
	}

	visited := make(map[string]bool)
	queued := make(map[string]bool)
	for _, item := range queue {
		queued[item.target.PageKey] = true
	}

	// The executor caps a runtime at 8 open pages, so the crawl navigates one
	// dedicated page instead of opening a page per URL. A caller-supplied page
	// belongs to the caller and is never closed here.
	activePageID := strings.TrimSpace(cfg.PageID)
	pages := make([]DiscoveredPage, 0, cfg.MaxPages)
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if len(pages) >= cfg.MaxPages {
			result.Truncated = true
			break
		}
		item := queue[0]
		queue = queue[1:]
		item.pageID = activePageID

		snapshot, openErr := d.openPage(ctx, cfg, item, allowedHosts)
		if openErr != nil {
			if len(pages) == 0 {
				return result, fmt.Errorf("open page discovery seed %s: %w", item.target.URL, openErr)
			}
			result.SkippedLinks++
			continue
		}
		if pageID := strings.TrimSpace(snapshot.PageID); pageID != "" {
			activePageID = pageID
		}
		finalTarget, targetErr := pageTargetFromSnapshot(snapshot, item.target)
		if targetErr != nil {
			if len(pages) == 0 {
				return result, targetErr
			}
			result.SkippedLinks++
			continue
		}
		if visited[finalTarget.PageKey] || finalTarget.PageKey == item.parentKey {
			continue
		}
		// A redirect must not pull the crawl off the configured SUT host.
		if !hostAllowed(finalTarget.Host, allowedHosts) {
			result.SkippedLinks++
			continue
		}
		visited[finalTarget.PageKey] = true

		elements, links, elementsTruncated := inventoryPage(snapshot, finalTarget, cfg)
		if elementsTruncated {
			result.Truncated = true
		}
		page := DiscoveredPage{
			PageKey:       finalTarget.PageKey,
			URL:           finalTarget.URL,
			RouteTemplate: finalTarget.RouteTemplate,
			HashRoute:     finalTarget.HashRoute,
			Title:         firstNonEmptyString(snapshot.DOM.Title, finalTarget.RouteTemplate),
			QueryKeys:     finalTarget.QueryKeys,
			Depth:         item.depth,
			ParentKey:     item.parentKey,
			Elements:      elements,
			Children:      []string{},
		}

		// Breadth-first, link-sorted crawl: same template is queued once, which
		// keeps both the page order and the page tree deterministic.
		if item.depth < cfg.MaxDepth {
			sort.Slice(links, func(i, j int) bool { return links[i].PageKey < links[j].PageKey })
			accepted := 0
			for _, link := range links {
				if accepted >= cfg.MaxLinksPerPage {
					result.Truncated = true
					break
				}
				if !hostAllowed(link.Host, allowedHosts) {
					result.SkippedLinks++
					continue
				}
				if queued[link.PageKey] || visited[link.PageKey] {
					continue
				}
				queued[link.PageKey] = true
				accepted++
				page.Children = append(page.Children, link.PageKey)
				queue = append(queue, pageCrawlItem{
					target:     link,
					depth:      item.depth + 1,
					parentKey:  finalTarget.PageKey,
					storageRef: cfg.StorageStateRef,
				})
			}
		}
		pages = append(pages, page)
	}

	result.Pages = pages
	// Prune tree edges whose child was never discovered (open failure or a
	// redirect that collapsed onto an already visited template), so the page
	// tree never points at a page outside the result.
	discovered := make(map[string]bool, len(pages))
	for _, page := range pages {
		discovered[page.PageKey] = true
	}
	for index := range result.Pages {
		kept := make([]string, 0, len(result.Pages[index].Children))
		for _, child := range result.Pages[index].Children {
			if discovered[child] {
				kept = append(kept, child)
			}
		}
		result.Pages[index].Children = kept
	}
	templates := make(map[string]struct{}, len(pages))
	for _, page := range pages {
		templates[page.RouteTemplate] = struct{}{}
	}
	result.RouteTemplates = make([]string, 0, len(templates))
	for template := range templates {
		result.RouteTemplates = append(result.RouteTemplates, template)
	}
	sort.Strings(result.RouteTemplates)

	if d.facts != nil {
		for index := range result.Pages {
			fact := pageFact(versionID, cfg, result.Pages[index])
			stored, _, recordErr := d.facts.Record(ctx, fact)
			if recordErr != nil {
				return result, fmt.Errorf("record %s fact: %w", KindPage, recordErr)
			}
			result.FactIDs = append(result.FactIDs, stored.ID)
		}
	}
	return result, nil
}

/* --------------------------------- seeding -------------------------------- */

// seedQueue resolves the crawl roots and the host allowlist. The configured
// StartURL keeps the first position; extra seeds are normalized and sorted.
func (d *PageDiscovery) seedQueue(ctx context.Context, cfg PageDiscoveryConfig) ([]pageCrawlItem, []string, error) {
	start := strings.TrimSpace(cfg.StartURL)
	rawSeeds := make([]string, 0, len(cfg.SeedURLs)+1)
	if start != "" {
		rawSeeds = append(rawSeeds, start)
	}
	for _, seed := range cfg.SeedURLs {
		if trimmed := strings.TrimSpace(seed); trimmed != "" {
			rawSeeds = append(rawSeeds, trimmed)
		}
	}
	// A page-id-only caller has no URL yet: snapshot it once to learn the
	// current location. This is still page discovery, never network capture.
	if len(rawSeeds) == 0 && strings.TrimSpace(cfg.PageID) != "" {
		snapshot, err := d.snapshotPage(ctx, strings.TrimSpace(cfg.PageID))
		if err != nil {
			return nil, nil, fmt.Errorf("snapshot page discovery seed: %w", err)
		}
		seedURL := firstNonEmptyString(snapshot.DOM.URL, snapshot.URL)
		if seedURL == "" {
			return nil, nil, errors.New("page discovery seed page has no URL")
		}
		rawSeeds = append(rawSeeds, seedURL)
	}

	targets := make([]pageTarget, 0, len(rawSeeds))
	seen := make(map[string]bool, len(rawSeeds))
	for index, rawSeed := range rawSeeds {
		target, err := analyzePageURL(rawSeed)
		if err != nil {
			if index == 0 || start == "" {
				return nil, nil, fmt.Errorf("invalid page discovery seed %q: %w", rawSeed, err)
			}
			continue
		}
		if seen[target.PageKey] {
			continue
		}
		seen[target.PageKey] = true
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return nil, nil, errors.New("page discovery has no usable seed URL")
	}

	allowedHosts := normalizedHosts(cfg.AllowedHosts)
	if len(allowedHosts) == 0 {
		allowedHosts = []string{targets[0].Host}
	}

	// StartURL stays first; additional seeds are deterministic.
	extra := targets[1:]
	sort.Slice(extra, func(i, j int) bool { return extra[i].PageKey < extra[j].PageKey })
	ordered := append([]pageTarget{targets[0]}, extra...)

	queue := make([]pageCrawlItem, 0, len(ordered))
	for index, target := range ordered {
		item := pageCrawlItem{target: target, storageRef: cfg.StorageStateRef}
		if index == 0 && strings.TrimSpace(cfg.PageID) != "" {
			item.pageID = strings.TrimSpace(cfg.PageID)
		}
		queue = append(queue, item)
	}
	return queue, allowedHosts, nil
}

/* --------------------------------- opening -------------------------------- */

type discoverySnapshot struct {
	PageID string       `json:"page_id"`
	URL    string       `json:"url"`
	Route  string       `json:"route"`
	DOM    discoveryDOM `json:"dom_snapshot"`
}

type discoveryDOM struct {
	URL      string             `json:"url"`
	Title    string             `json:"title"`
	ARIA     string             `json:"aria"`
	Route    string             `json:"route"`
	Elements []discoveryElement `json:"elements"`
}

type discoveryElement struct {
	Index    int    `json:"index"`
	Tag      string `json:"tag"`
	Selector string `json:"selector"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Text     string `json:"text"`
	Href     string `json:"href"`
	Disabled bool   `json:"disabled"`
	Visible  *bool  `json:"visible"`
}

func decodeDiscoverySnapshot(result map[string]any) (discoverySnapshot, error) {
	if result == nil {
		return discoverySnapshot{}, errors.New("executor returned an empty explore result")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return discoverySnapshot{}, fmt.Errorf("encode explore result: %w", err)
	}
	var snapshot discoverySnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return discoverySnapshot{}, fmt.Errorf("decode explore result: %w", err)
	}
	if snapshot.DOM.URL == "" {
		snapshot.DOM.URL = snapshot.URL
	}
	return snapshot, nil
}

func (d *PageDiscovery) openPage(ctx context.Context, cfg PageDiscoveryConfig, item pageCrawlItem, allowedHosts []string) (discoverySnapshot, error) {
	args := map[string]any{"url": item.target.URL}
	if len(allowedHosts) > 0 {
		args["allowed_hosts"] = append([]string(nil), allowedHosts...)
	}
	pageID := strings.TrimSpace(item.pageID)
	// storage_state_ref is only valid when a new page is created; a navigation
	// inherits the already-authenticated context.
	if pageID == "" {
		storageRef := strings.TrimSpace(item.storageRef)
		if storageRef == "" {
			storageRef = strings.TrimSpace(cfg.StorageStateRef)
		}
		if storageRef != "" {
			args["storage_state_ref"] = storageRef
		}
	} else {
		args["page_id"] = pageID
	}
	result, err := d.exec.Action(ctx, actionOpen, args)
	if err != nil {
		return discoverySnapshot{}, err
	}
	return decodeDiscoverySnapshot(result)
}

func (d *PageDiscovery) snapshotPage(ctx context.Context, pageID string) (discoverySnapshot, error) {
	result, err := d.exec.Action(ctx, actionSnapshot, map[string]any{"page_id": pageID})
	if err != nil {
		return discoverySnapshot{}, err
	}
	return decodeDiscoverySnapshot(result)
}

// pageTargetFromSnapshot follows an executor redirect: the final DOM URL wins
// over the requested URL when it is a usable http(s) page. Query keys from both
// locations are merged so the normalized page does not lose a parameter name.
func pageTargetFromSnapshot(snapshot discoverySnapshot, requested pageTarget) (pageTarget, error) {
	raw := strings.TrimSpace(snapshot.DOM.URL)
	if raw == "" {
		raw = strings.TrimSpace(snapshot.URL)
	}
	if raw == "" {
		return requested, nil
	}
	target, err := analyzePageURL(raw)
	if err != nil {
		return requested, nil
	}
	keys := append([]string(nil), target.QueryKeys...)
	keys = append(keys, requested.QueryKeys...)
	target.QueryKeys = uniqueSorted(keys)
	return target, nil
}

/* ------------------------------ URL analysis ------------------------------ */

var (
	// logoutPathPattern keeps destructive session links out of the crawl.
	logoutPathPattern = regexp.MustCompile(`(?i)(^|/)(log-?out|sign-?out|logout)(/|$)`)
	// sensitiveQueryKey names are recorded never, even though values are always
	// dropped: a key name alone can reveal the credential mechanism.
	sensitiveQueryKeys = map[string]bool{
		"token": true, "accesstoken": true, "refreshtoken": true, "idtoken": true,
		"password": true, "passwd": true, "secret": true, "apikey": true,
		"key": true, "auth": true, "authorization": true, "session": true,
		"sessionid": true, "sid": true, "sig": true, "signature": true,
		"otp": true,
	}
	staticAssetExtensions = map[string]bool{
		".js": true, ".mjs": true, ".cjs": true, ".css": true, ".map": true,
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
		".ico": true, ".webp": true, ".avif": true, ".bmp": true, ".woff": true,
		".woff2": true, ".ttf": true, ".otf": true, ".eot": true, ".pdf": true,
		".zip": true, ".gz": true, ".tar": true, ".mp3": true, ".mp4": true,
		".webm": true, ".wav": true, ".json": true, ".xml": true, ".txt": true,
	}
)

// analyzePageURL canonicalizes one absolute page URL and derives its route
// template. Query values are always dropped; only non-sensitive parameter
// names are kept. A hash-router path is preserved as part of the template
// (`/app#/reports/{id}`), while a plain anchor (`#section`) is ignored.
func analyzePageURL(raw string) (pageTarget, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return pageTarget{}, fmt.Errorf("parse page URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return pageTarget{}, fmt.Errorf("page URL %q is not http(s)", raw)
	}
	if parsed.Host == "" {
		return pageTarget{}, fmt.Errorf("page URL %q has no host", raw)
	}
	if parsed.User != nil {
		return pageTarget{}, errors.New("page URL must not contain credentials")
	}

	queryKeys := queryKeyNames(parsed.RawQuery)
	hashPath := ""
	fragment := parsed.Fragment
	if strings.HasPrefix(fragment, "!") {
		fragment = strings.TrimPrefix(fragment, "!")
	}
	if strings.HasPrefix(fragment, "/") {
		hashPath = fragment
		if cut := strings.IndexByte(hashPath, '?'); cut >= 0 {
			queryKeys = append(queryKeys, queryKeyNames(hashPath[cut+1:])...)
			hashPath = hashPath[:cut]
		}
		if cut := strings.IndexByte(hashPath, '#'); cut >= 0 {
			hashPath = hashPath[:cut]
		}
	}

	pathTemplate, ok := normalizePageRoute(parsed.Path)
	if !ok {
		return pageTarget{}, fmt.Errorf("page URL %q has an unsupported path", raw)
	}
	routeTemplate := pathTemplate
	hashRoute := ""
	if hashPath != "" {
		hashTemplate, hashOK := normalizePageRoute(hashPath)
		if hashOK {
			hashRoute = hashPath
			if routeTemplate == "/" {
				routeTemplate = "/#" + hashTemplate
			} else {
				routeTemplate += "#" + hashTemplate
			}
		}
	}

	normalizedURL, err := resource.NormalizePageURL(raw)
	if err != nil {
		return pageTarget{}, fmt.Errorf("normalize page URL: %w", err)
	}
	normalized, err := url.Parse(normalizedURL)
	if err != nil {
		return pageTarget{}, fmt.Errorf("parse normalized page URL: %w", err)
	}
	fragment = normalized.Fragment
	if cut := strings.IndexByte(fragment, '?'); cut >= 0 {
		fragment = fragment[:cut]
	}
	normalized.Fragment = fragment
	normalizedURL = normalized.String()

	scheme := strings.ToLower(normalized.Scheme)
	host := strings.ToLower(normalized.Host)
	return pageTarget{
		URL:           normalizedURL,
		PageKey:       scheme + "://" + host + routeTemplate,
		RouteTemplate: routeTemplate,
		HashRoute:     hashRoute,
		QueryKeys:     uniqueSorted(queryKeys),
		Host:          host,
		Path:          normalized.Path,
	}, nil
}

// normalizePageRoute collapses concrete identifier segments of a route to
// `{id}`. It deliberately mirrors the codeintel API path identity so page route
// templates and API path templates stay recognizable to the same consumers.
func normalizePageRoute(rawPath string) (string, bool) {
	if rawPath == "" {
		return "/", true
	}
	segments := strings.Split(rawPath, "/")
	normalized := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		if segment == ".." {
			return "", false
		}
		if isPageIDSegment(segment) {
			normalized = append(normalized, "{id}")
			continue
		}
		for _, r := range segment {
			if r <= 0x20 || r == 0x7f || r == '\\' {
				return "", false
			}
		}
		normalized = append(normalized, segment)
	}
	return "/" + strings.Join(normalized, "/"), true
}

func isPageIDSegment(segment string) bool {
	if segment == "" {
		return false
	}
	numeric := true
	for _, r := range segment {
		if r < '0' || r > '9' {
			numeric = false
			break
		}
	}
	if numeric {
		return true
	}
	if len(segment) == 36 {
		uuid := true
		for index, r := range segment {
			switch index {
			case 8, 13, 18, 23:
				if r != '-' {
					uuid = false
				}
			default:
				if !isHexRune(r) {
					uuid = false
				}
			}
			if !uuid {
				break
			}
		}
		if uuid {
			return true
		}
	}
	if len(segment) < 16 {
		return false
	}
	for _, r := range segment {
		if !isHexRune(r) {
			return false
		}
	}
	return true
}

func isHexRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func queryKeyNames(rawQuery string) []string {
	if strings.TrimSpace(rawQuery) == "" {
		return nil
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if isSensitiveQueryKey(key) {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}

func isSensitiveQueryKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "_", ""), "-", ""))
	return sensitiveQueryKeys[normalized]
}

/* ------------------------------ DOM inventory ----------------------------- */

// inventoryPage builds the interactive-element inventory and the crawl links of
// one page. Only visible, enabled anchors contribute links; hidden nodes stay
// in the inventory (with visible=false) so callers can reason about them.
func inventoryPage(snapshot discoverySnapshot, page pageTarget, cfg PageDiscoveryConfig) ([]PageElement, []pageTarget, bool) {
	elements := make([]PageElement, 0, len(snapshot.DOM.Elements))
	links := make([]pageTarget, 0)
	seenLinks := make(map[string]bool)
	truncated := false
	for _, rawElement := range snapshot.DOM.Elements {
		if len(elements) >= cfg.MaxElementsPerPage {
			truncated = true
			break
		}
		selector := strings.TrimSpace(rawElement.Selector)
		if selector == "" {
			continue
		}
		visible := rawElement.Visible == nil || *rawElement.Visible
		element := PageElement{
			Index:    rawElement.Index,
			Tag:      strings.ToLower(strings.TrimSpace(rawElement.Tag)),
			Selector: clipString(selector, 500),
			Role:     clipString(rawElement.Role, 100),
			Name:     clipString(rawElement.Name, 200),
			Type:     clipString(rawElement.Type, 50),
			Text:     clipString(rawElement.Text, 200),
			Visible:  visible,
			Disabled: rawElement.Disabled,
		}
		var resolved *pageTarget
		if strings.TrimSpace(rawElement.Href) != "" {
			if target, err := resolvePageLink(rawElement.Href, page.URL); err == nil {
				element.Href = clipString(target.URL, 500)
				resolved = &target
			}
		}
		elements = append(elements, element)
		if !visible || rawElement.Disabled || resolved == nil {
			continue
		}
		isAnchor := element.Tag == "a" || strings.EqualFold(element.Role, "link")
		if !isAnchor {
			continue
		}
		if !isCrawlablePageLink(*resolved) {
			continue
		}
		if seenLinks[resolved.PageKey] {
			continue
		}
		seenLinks[resolved.PageKey] = true
		links = append(links, *resolved)
	}
	return elements, links, truncated
}

// resolvePageLink resolves an href against the current page and rejects
// non-http(s) schemes. The returned target has values stripped.
func resolvePageLink(rawHref, pageURL string) (pageTarget, error) {
	base, err := url.Parse(pageURL)
	if err != nil {
		return pageTarget{}, err
	}
	reference, err := url.Parse(strings.TrimSpace(rawHref))
	if err != nil {
		return pageTarget{}, err
	}
	switch strings.ToLower(reference.Scheme) {
	case "", "http", "https":
	default:
		return pageTarget{}, errors.New("unsupported link scheme")
	}
	absolute := base.ResolveReference(reference)
	return analyzePageURL(absolute.String())
}

// isCrawlablePageLink drops static/logout targets. Host allowlisting is
// enforced by the caller before opening a page.
func isCrawlablePageLink(target pageTarget) bool {
	if strings.TrimSpace(target.URL) == "" {
		return false
	}
	if isStaticAssetPath(target.Path) || isStaticAssetPath(target.HashRoute) {
		return false
	}
	if logoutPathPattern.MatchString(target.Path) || logoutPathPattern.MatchString(target.HashRoute) {
		return false
	}
	return true
}

func isStaticAssetPath(rawPath string) bool {
	lower := strings.ToLower(rawPath)
	if cut := strings.IndexByte(lower, '#'); cut >= 0 {
		lower = lower[:cut]
	}
	return staticAssetExtensions[path.Ext(lower)]
}

/* --------------------------------- facts ---------------------------------- */

func pageFact(versionID int64, cfg PageDiscoveryConfig, page DiscoveredPage) version.Fact {
	confidence := 0.9
	payload := map[string]any{
		"page_key":        page.PageKey,
		"url":             page.URL,
		"route_template":  page.RouteTemplate,
		"hash_route":      page.HashRoute,
		"title":           page.Title,
		"query_keys":      copyStrings(page.QueryKeys),
		"depth":           page.Depth,
		"parent_page_key": page.ParentKey,
		"children":        copyStrings(page.Children),
		"elements":        pageElementPayloads(page.Elements),
		"source":          "runtime",
	}
	if cfg.VersionSUTID > 0 {
		payload["version_sut_id"] = cfg.VersionSUTID
	}
	if cfg.EnvironmentID > 0 {
		payload["environment_id"] = cfg.EnvironmentID
	}
	return version.Fact{
		VersionID:  versionID,
		Kind:       KindPage,
		Key:        pageFactKey(cfg.EnvironmentID, page.PageKey),
		Payload:    payload,
		Confidence: &confidence,
		Source:     "explorer",
		SourceRef:  page.URL,
	}
}

func pageFactKey(environmentID int64, pageKey string) string {
	if environmentID > 0 {
		return fmt.Sprintf("page:env:%d:%s", environmentID, pageKey)
	}
	return "page:" + pageKey
}

func pageElementPayloads(elements []PageElement) []any {
	payloads := make([]any, 0, len(elements))
	for _, element := range elements {
		payload := map[string]any{
			"index":    element.Index,
			"tag":      element.Tag,
			"selector": element.Selector,
			"role":     element.Role,
			"name":     element.Name,
			"type":     element.Type,
			"text":     element.Text,
			"visible":  element.Visible,
			"disabled": element.Disabled,
		}
		if element.Href != "" {
			payload["href"] = element.Href
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

/* --------------------------------- helpers -------------------------------- */

func normalizedHosts(values []string) []string {
	hosts := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		host := strings.ToLower(strings.TrimSpace(value))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

// hostAllowed matches either the exact host:port authority or the bare host
// name, so an allowlist entry of "sut.test" also covers "sut.test:8080".
func hostAllowed(host string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	hostname := strings.ToLower(strings.TrimSpace(host))
	if parsed, err := url.Parse("http://" + hostname); err == nil && parsed.Hostname() != "" {
		hostname = strings.ToLower(parsed.Hostname())
	}
	for _, entry := range allowed {
		if entry = strings.ToLower(strings.TrimSpace(entry)); entry == "" {
			continue
		}
		if entry == strings.ToLower(strings.TrimSpace(host)) || entry == hostname {
			return true
		}
	}
	return false
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]bool, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		unique = append(unique, trimmed)
	}
	sort.Strings(unique)
	return unique
}

// copyStrings returns a non-nil copy so a fact payload keeps array shapes
// ([] rather than null) for consumers that decode the payload structurally.
func copyStrings(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func clipString(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= limit {
		return trimmed
	}
	return string(runes[:limit])
}

func positiveInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
