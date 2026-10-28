// Package httpclient is the read-only HTTP probe client of the P5 application
// exploration service (spec §6.9/D36). It fetches one URL with GET or HEAD,
// enforces the SSRF allowlist, refuses cross-host redirects, caps the parsed
// body at 512 KiB and extracts the title, links, forms and scripts of an HTML
// response. It never sends a state-changing request and never injects a token:
// the only credential it may carry is the explicit Cookie header built from a
// P2 session storage state.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
)

// Defaults of one read-only probe (spec §6.9: 5s per request, 512 KiB parse
// limit).
const (
	DefaultTimeout     = 5 * time.Second
	DefaultMaxBody     = int64(512 * 1024)
	DefaultMaxRedirect = 5
)

// Sentinel errors. The exploration service maps a connection failure of the
// environment base URL to environment_unreachable; the allowlist errors are
// reported as environment_not_allowed and never retried.
var (
	// ErrMethodNotAllowed rejects everything but GET and HEAD.
	ErrMethodNotAllowed = errors.New("httpclient: method must be GET or HEAD")
	// ErrURLNotAllowed rejects a malformed URL, a non-http(s) scheme or an
	// embedded credential.
	ErrURLNotAllowed = errors.New("httpclient: url is not allowed")
	// ErrHostNotAllowed rejects a host outside the environment allowlist.
	ErrHostNotAllowed = errors.New("httpclient: host is not allowlisted")
	// ErrPrivateIP rejects a private/reserved address that is not explicitly
	// allowlisted by its IP literal.
	ErrPrivateIP = errors.New("httpclient: private or reserved address is not allowlisted")
	// ErrCrossHostRedirect rejects a 3xx that leaves the starting host.
	ErrCrossHostRedirect = errors.New("httpclient: cross-host redirect is not allowed")
	// ErrTooManyRedirects bounds the same-host redirect chain.
	ErrTooManyRedirects = errors.New("httpclient: too many redirects")
)

// Form is one <form> element of a probed page. Action is the raw action
// attribute (possibly relative and empty for a self-targeting form); Method is
// the lowercase form method (HTML defaults to GET).
type Form struct {
	Action string
	Method string
}

// ProbeResult is the parsed projection of one GET/HEAD response.
type ProbeResult struct {
	URL         string
	Status      int
	ContentType string
	Title       string
	Links       []string
	Forms       []Form
	Scripts     []string
	// Body is capped at Options.MaxBodyBytes; Truncated reports that the
	// response was longer and the body was cut.
	Body      []byte
	Truncated bool
}

// Options configures one probe client. AllowedHosts is the environment host
// allowlist; an empty allowlist denies every host (fail closed). A literal IP
// entry additionally allows a private/loopback destination.
type Options struct {
	Timeout      time.Duration
	MaxBodyBytes int64
	MaxRedirects int
	AllowedHosts []string
}

// Client is a safe-for-concurrent-use read-only probe client.
type Client struct {
	timeout      time.Duration
	maxBody      int64
	maxRedirects int
	allowedHosts []string
	allowedIPs   []net.IP
	transport    http.RoundTripper
}

// New builds a probe client with the documented defaults for the zero-valued
// options.
func New(opts Options) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = DefaultMaxBody
	}
	if opts.MaxRedirects <= 0 {
		opts.MaxRedirects = DefaultMaxRedirect
	}
	client := &Client{
		timeout:      opts.Timeout,
		maxBody:      opts.MaxBodyBytes,
		maxRedirects: opts.MaxRedirects,
		transport:    http.DefaultTransport,
	}
	for _, raw := range opts.AllowedHosts {
		host := normalizeHost(raw)
		if host == "" {
			continue
		}
		client.allowedHosts = append(client.allowedHosts, host)
		if ip := net.ParseIP(host); ip != nil {
			client.allowedIPs = append(client.allowedIPs, ip)
		}
	}
	return client
}

// Fetch probes rawURL with GET.
func (c *Client) Fetch(ctx context.Context, rawURL, cookieHeader string) (ProbeResult, error) {
	return c.FetchMethod(ctx, http.MethodGet, rawURL, cookieHeader)
}

// FetchMethod probes rawURL with GET or HEAD. Any other method is rejected
// before a connection is opened.
func (c *Client) FetchMethod(ctx context.Context, method, rawURL, cookieHeader string) (ProbeResult, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method != http.MethodGet && method != http.MethodHead {
		return ProbeResult{}, ErrMethodNotAllowed
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ProbeResult{}, fmt.Errorf("%w: %v", ErrURLNotAllowed, err)
	}
	if err := c.validateURL(ctx, parsed); err != nil {
		return ProbeResult{}, err
	}

	request, err := http.NewRequestWithContext(ctx, method, parsed.String(), nil)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("%w: %v", ErrURLNotAllowed, err)
	}
	request.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	request.Header.Set("User-Agent", "EzTest-Explorer/1.0")
	if cookieHeader != "" {
		request.Header.Set("Cookie", cookieHeader)
	}

	client := &http.Client{
		Transport:     c.transport,
		Timeout:       c.timeout,
		CheckRedirect: c.redirectPolicy(parsed),
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, ErrCrossHostRedirect) {
			return ProbeResult{}, ErrCrossHostRedirect
		}
		if errors.Is(err, ErrTooManyRedirects) {
			return ProbeResult{}, ErrTooManyRedirects
		}
		return ProbeResult{}, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, c.maxBody+1))
	if err != nil {
		return ProbeResult{}, fmt.Errorf("httpclient: read response body: %w", err)
	}
	truncated := int64(len(body)) > c.maxBody
	if truncated {
		body = body[:c.maxBody]
	}

	result := ProbeResult{
		URL:         response.Request.URL.String(),
		Status:      response.StatusCode,
		ContentType: response.Header.Get("Content-Type"),
		Body:        body,
		Truncated:   truncated,
	}
	result.Title, result.Links, result.Forms, result.Scripts = parseHTML(body)
	return result, nil
}

// Probe implements application.Prober: it performs the same read-only GET as
// Fetch and projects the result onto the application port. The port carries
// forms as strings, so each entry is encoded as "<METHOD> <action>" (uppercase
// method, raw action, action omitted for a self-targeting form); the application
// service decodes it with splitExploreForm. A target rejected by the read-only
// URL rules is reported as application.ErrProbeTargetNotAllowed so the service
// can keep its environment_not_allowed mapping without importing this package.
func (c *Client) Probe(ctx context.Context, rawURL, cookieHeader string) (application.ProbeResult, error) {
	result, err := c.Fetch(ctx, rawURL, cookieHeader)
	if err != nil {
		if errors.Is(err, ErrURLNotAllowed) || errors.Is(err, ErrHostNotAllowed) || errors.Is(err, ErrPrivateIP) {
			return application.ProbeResult{}, fmt.Errorf("%w: %w", application.ErrProbeTargetNotAllowed, err)
		}
		return application.ProbeResult{}, err
	}
	var forms []string
	if len(result.Forms) > 0 {
		forms = make([]string, 0, len(result.Forms))
		for _, form := range result.Forms {
			method := strings.ToUpper(strings.TrimSpace(form.Method))
			if method == "" {
				method = http.MethodGet
			}
			if form.Action == "" {
				forms = append(forms, method)
				continue
			}
			forms = append(forms, method+" "+form.Action)
		}
	}
	return application.ProbeResult{
		URL:         result.URL,
		Status:      result.Status,
		ContentType: result.ContentType,
		Title:       result.Title,
		Links:       result.Links,
		Forms:       forms,
		Scripts:     result.Scripts,
		Body:        string(result.Body),
		Truncated:   result.Truncated,
	}, nil
}

// redirectPolicy validates every redirect target. A redirect that leaves the
// starting host is refused; the remaining checks (scheme, allowlist, private
// IP) are the same as for the initial URL.
func (c *Client) redirectPolicy(initial *url.URL) func(*http.Request, []*http.Request) error {
	initialHost := canonicalHost(initial)
	return func(request *http.Request, via []*http.Request) error {
		if len(via) >= c.maxRedirects {
			return ErrTooManyRedirects
		}
		if canonicalHost(request.URL) != initialHost {
			return ErrCrossHostRedirect
		}
		return c.validateURL(request.Context(), request.URL)
	}
}

// validateURL applies the SSRF rules of spec §6.9 point 8: http(s) only, no
// embedded credentials, host inside the allowlist, and no private/reserved
// resolved address unless that exact IP was explicitly allowlisted.
func (c *Client) validateURL(ctx context.Context, target *url.URL) error {
	if target == nil {
		return ErrURLNotAllowed
	}
	scheme := strings.ToLower(target.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrURLNotAllowed, target.Scheme)
	}
	if target.User != nil {
		return fmt.Errorf("%w: embedded credentials are not allowed", ErrURLNotAllowed)
	}
	host := strings.ToLower(target.Hostname())
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrURLNotAllowed)
	}
	if !c.hostAllowed(host) {
		return fmt.Errorf("%w: %s", ErrHostNotAllowed, host)
	}
	if isLoopbackHostname(host) && !c.hasAllowlistedLoopback() {
		return fmt.Errorf("%w: %s is a loopback name", ErrPrivateIP, host)
	}
	addresses, err := resolveIPs(ctx, host)
	if err != nil {
		return fmt.Errorf("%w: resolve %s: %v", ErrURLNotAllowed, host, err)
	}
	for _, address := range addresses {
		if isReservedIP(address) && !c.ipAllowed(address) {
			return fmt.Errorf("%w: %s resolves to %s", ErrPrivateIP, host, address)
		}
	}
	return nil
}

// hostAllowed reports whether host exactly matches one allowlist entry.
func (c *Client) hostAllowed(host string) bool {
	for _, allowed := range c.allowedHosts {
		if allowed == host {
			return true
		}
	}
	return false
}

// ipAllowed reports whether ip exactly matches one allowlisted IP literal.
func (c *Client) ipAllowed(ip net.IP) bool {
	for _, allowed := range c.allowedIPs {
		if allowed.Equal(ip) {
			return true
		}
	}
	return false
}

// hasAllowlistedLoopback reports whether the allowlist contains an explicit
// loopback IP; only that unlock a loopback hostname such as localhost.
func (c *Client) hasAllowlistedLoopback() bool {
	for _, ip := range c.allowedIPs {
		if ip.IsLoopback() {
			return true
		}
	}
	return false
}

// resolveIPs resolves host, accepting an IP literal without a DNS lookup.
func resolveIPs(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ips = append(ips, address.IP)
	}
	if len(ips) == 0 {
		return nil, errors.New("no address")
	}
	return ips, nil
}

// isReservedIP reports whether ip is loopback, private, unspecified, link-local,
// multicast or carrier-grade NAT: the destination classes an SSRF probe must
// not reach unless explicitly allowlisted.
func isReservedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 0.0.0.0/8 ("this network") and 100.64.0.0/10 (CGNAT).
		if ip4[0] == 0 {
			return true
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
	}
	return false
}

// isLoopbackHostname reports whether host is localhost or a .localhost name.
func isLoopbackHostname(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

// canonicalHost normalizes a URL authority to host:port for origin comparison.
func canonicalHost(target *url.URL) string {
	if target == nil {
		return ""
	}
	host := strings.ToLower(target.Hostname())
	port := target.Port()
	if port == "" {
		switch strings.ToLower(target.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return net.JoinHostPort(host, port)
}

// normalizeHost turns an allowlist entry into a bare lowercase host. Entries may
// be host, host:port, a URL or a bracketed IPv6 literal.
func normalizeHost(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil {
			return strings.Trim(parsed.Hostname(), "[]")
		}
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(value, "[]")
}

// HTML extraction patterns. The fixture pages are small and well formed; a
// regular-expression pass keeps the client dependency free (no golang.org/x/net
// in go.mod) and is bounded by the 512 KiB body cap.
var (
	titlePattern  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	anchorPattern = regexp.MustCompile(`(?is)<a\b[^>]*>`)
	formPattern   = regexp.MustCompile(`(?is)<form\b[^>]*>`)
	scriptPattern = regexp.MustCompile(`(?is)<script\b[^>]*>`)
	hrefPattern   = regexp.MustCompile(`(?is)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	actionPattern = regexp.MustCompile(`(?is)\baction\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	methodPattern = regexp.MustCompile(`(?is)\bmethod\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	srcPattern    = regexp.MustCompile(`(?is)\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
)

// parseHTML extracts the title, links, forms and scripts of one body.
func parseHTML(body []byte) (title string, links []string, forms []Form, scripts []string) {
	text := string(body)
	if match := titlePattern.FindStringSubmatch(text); match != nil {
		title = strings.Join(strings.Fields(html.UnescapeString(match[1])), " ")
	}
	for _, tag := range anchorPattern.FindAllString(text, -1) {
		if href := attributeValue(hrefPattern, tag); href != "" {
			links = append(links, href)
		}
	}
	for _, tag := range formPattern.FindAllString(text, -1) {
		method := strings.ToLower(attributeValue(methodPattern, tag))
		if method == "" {
			method = "get"
		}
		forms = append(forms, Form{Action: attributeValue(actionPattern, tag), Method: method})
	}
	for _, tag := range scriptPattern.FindAllString(text, -1) {
		if src := attributeValue(srcPattern, tag); src != "" {
			scripts = append(scripts, src)
		}
	}
	return title, links, forms, scripts
}

// attributeValue returns the first non-empty quoted or bare attribute value of
// one tag, HTML-unescaped.
func attributeValue(pattern *regexp.Regexp, tag string) string {
	match := pattern.FindStringSubmatch(tag)
	if match == nil {
		return ""
	}
	for index := 1; index < len(match); index++ {
		if match[index] != "" {
			return html.UnescapeString(strings.TrimSpace(match[index]))
		}
	}
	return ""
}
