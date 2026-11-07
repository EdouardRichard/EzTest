package executionapp

import (
	"context"
	"log/slog"
	"net"
	"net/url"
	"strings"
)

// DiscoveryObservationStore is the application-side port of the DB-29 upsert
// (implemented by the postgres adapter). It accepts already normalized rows and
// is idempotent on the natural key (version_id, version_sut_id, kind, method,
// resource_template).
type DiscoveryObservationStore interface {
	UpsertObservations(ctx context.Context, observations []DiscoveryObservation) error
}

// SUTHostResolver returns the hosts of one version_sut binding: the SUT
// environment base URL plus the hosts of its registered pages. It lets the
// ingest service filter third-party CDN/analytics traffic. A nil resolver means
// no host filtering.
type SUTHostResolver interface {
	AllowedHosts(ctx context.Context, versionSUTID int64) (map[string]bool, error)
}

// DiscoveryIngestDeps wires the discovery ingest service. Store is mandatory;
// Hosts is optional.
type DiscoveryIngestDeps struct {
	Store  DiscoveryObservationStore
	Hosts  SUTHostResolver
	Logger *slog.Logger
}

// DiscoveryIngest implements DiscoveryObservationWriter: it normalizes every
// observation, drops static resources, applies the third-party host filter and
// aggregates same-batch duplicates before the repository upsert. Failures are
// returned to the caller, which records them best effort (the case verdict was
// already decided).
type DiscoveryIngest struct {
	store  DiscoveryObservationStore
	hosts  SUTHostResolver
	logger *slog.Logger
}

var _ DiscoveryObservationWriter = (*DiscoveryIngest)(nil)

// NewDiscoveryIngest wires one ingest service.
func NewDiscoveryIngest(deps DiscoveryIngestDeps) *DiscoveryIngest {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &DiscoveryIngest{store: deps.Store, hosts: deps.Hosts, logger: logger}
}

// WriteObservations normalizes, filters, dedupes and persists one batch. It is
// safe on nil receivers/repositories (the capture is best effort).
func (s *DiscoveryIngest) WriteObservations(ctx context.Context, observations []DiscoveryObservation) error {
	if s == nil || s.store == nil || len(observations) == 0 {
		return nil
	}
	rows := s.normalize(ctx, observations)
	if len(rows) == 0 {
		return nil
	}
	return s.store.UpsertObservations(ctx, rows)
}

type discoveryObservationKey struct {
	versionSUTID int64
	kind         string
	method       string
	template     string
}

type discoveryHostCacheEntry struct {
	hosts  map[string]bool
	filter bool
}

// normalize applies the R-EX-2 pipeline and aggregates duplicate keys. The
// aggregate carries the sum of occurrences and the first non-empty metadata, so
// one request seen three times is one row with occurrences=3.
func (s *DiscoveryIngest) normalize(ctx context.Context, observations []DiscoveryObservation) []DiscoveryObservation {
	rows := make([]DiscoveryObservation, 0, len(observations))
	index := make(map[discoveryObservationKey]int, len(observations))
	hostCache := make(map[int64]discoveryHostCacheEntry)
	for _, observation := range observations {
		kind := strings.ToLower(strings.TrimSpace(observation.Kind))
		method := strings.ToUpper(strings.TrimSpace(observation.Method))
		host, template, ok := NormalizeResourceTemplate(kind, method, discoveryObservationURL(observation))
		if !ok {
			continue
		}
		if isStaticDiscoveryMIME(observation.MIME) {
			continue
		}
		if s.hosts != nil && !s.hostAllowed(ctx, observation.VersionSUTID, host, hostCache) {
			continue
		}
		pathTemplate := template
		if kind == "page" {
			method = ""
		} else {
			if method == "" {
				method = "GET"
			}
			pathTemplate = strings.TrimPrefix(template, method+" ")
		}
		row := observation
		row.Kind = kind
		row.Method = method
		row.Host = host
		row.ResourceTemplate = pathTemplate
		if row.Occurrences <= 0 {
			row.Occurrences = 1
		}
		key := discoveryObservationKey{
			versionSUTID: observation.VersionSUTID,
			kind:         kind,
			method:       method,
			template:     pathTemplate,
		}
		if at, found := index[key]; found {
			rows[at].Occurrences += row.Occurrences
			if rows[at].StatusCode == nil {
				rows[at].StatusCode = row.StatusCode
			}
			if rows[at].MIME == "" {
				rows[at].MIME = row.MIME
			}
			if rows[at].ResourceType == "" {
				rows[at].ResourceType = row.ResourceType
			}
			continue
		}
		index[key] = len(rows)
		rows = append(rows, row)
	}
	return rows
}

// hostAllowed caches the allowed-host set per version_sut. A resolver failure or
// an empty set disables the filter for that binding (fail open: a missing
// binding must not silently hide every observation). An exact host match always
// wins; otherwise a host in the same registrable domain is first-party too, so
// the SUT's own api./static. subdomains are not mistaken for third parties
// (W1-11).
func (s *DiscoveryIngest) hostAllowed(ctx context.Context, versionSUTID int64, host string, cache map[int64]discoveryHostCacheEntry) bool {
	entry, cached := cache[versionSUTID]
	if !cached {
		hosts, err := s.hosts.AllowedHosts(ctx, versionSUTID)
		if err != nil {
			s.logger.Warn("execution discovery: resolve SUT hosts failed; host filter skipped",
				"version_sut_id", versionSUTID, "error", err)
			entry = discoveryHostCacheEntry{}
		} else {
			entry = discoveryHostCacheEntry{hosts: hosts, filter: len(hosts) > 0}
		}
		cache[versionSUTID] = entry
	}
	if !entry.filter {
		return true
	}
	if entry.hosts[host] {
		return true
	}
	target := discoveryRegistrableDomain(host)
	if target == "" {
		return false
	}
	for allowed := range entry.hosts {
		if discoveryRegistrableDomain(allowed) == target {
			return true
		}
	}
	return false
}

// discoveryRegistrableDomain returns the eTLD+1 approximation used for the
// first-party host check. It deliberately handles only the common multi-label
// public suffixes (the project does not vendor a public-suffix list); IP
// literals compare exactly.
func discoveryRegistrableDomain(host string) string {
	host = discoveryNormalizedHost(host)
	if host == "" {
		return ""
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return host
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return host
	}
	if len(labels) >= 3 {
		suffix := strings.Join(labels[len(labels)-2:], ".")
		if _, ok := discoveryMultiLabelSuffixes[suffix]; ok {
			return strings.Join(labels[len(labels)-3:], ".")
		}
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

// discoveryMultiLabelSuffixes is the pinned subset of multi-label public
// suffixes needed to avoid treating two unrelated registrable domains as the
// same first party.
var discoveryMultiLabelSuffixes = map[string]struct{}{
	"co.uk": {}, "org.uk": {}, "ac.uk": {}, "gov.uk": {},
	"com.cn": {}, "net.cn": {}, "org.cn": {},
	"com.au": {}, "net.au": {}, "org.au": {},
	"co.jp": {}, "co.kr": {}, "co.nz": {},
	"com.br": {}, "com.tw": {}, "com.hk": {}, "com.sg": {},
}

// discoveryNormalizedHost lowercases one host and strips brackets/port. A bare
// IPv6 literal is recognized before url.Parse (which splits on the first colon).
func discoveryNormalizedHost(host string) string {
	value := strings.ToLower(strings.TrimSpace(host))
	if value == "" {
		return ""
	}
	if bare := strings.Trim(value, "[]"); net.ParseIP(bare) != nil {
		return bare
	}
	if parsed, err := url.Parse("http://" + value); err == nil && parsed.Hostname() != "" {
		return strings.ToLower(parsed.Hostname())
	}
	return strings.Trim(value, "[]")
}

// discoveryObservationURL rebuilds the redacted URL the normalizer consumes.
// Query/userinfo were already dropped by the capture, but the ingest repeats the
// normalization so a caller that stores a raw-ish Path still cannot leak it. An
// IPv6 literal must be bracketed before it is concatenated, otherwise the URL
// parser reads only the first colon as the host (W1-11).
func discoveryObservationURL(observation DiscoveryObservation) string {
	path := observation.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	host := strings.TrimSpace(observation.Host)
	if bare := strings.Trim(host, "[]"); net.ParseIP(bare) != nil && strings.Contains(bare, ":") {
		host = "[" + bare + "]"
	}
	return "http://" + host + path
}
