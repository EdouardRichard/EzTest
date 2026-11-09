package platform

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// RequestMeta is the transport provenance of one HTTP request. It exists so
// audit records can fall back to the request that triggered a business action
// without every caller re-parsing the request headers.
//
// IP is derived from RemoteAddr only. Forwarded headers are deliberately not
// trusted: deployments that terminate a reverse proxy would otherwise let any
// client spoof the recorded address without a trusted-proxy policy.
type RequestMeta struct {
	IP        string
	UserAgent string
}

type requestMetaKey struct{}

// WithRequestMeta stores the request provenance in the context.
func WithRequestMeta(ctx context.Context, meta RequestMeta) context.Context {
	return context.WithValue(ctx, requestMetaKey{}, meta)
}

// RequestMetaFrom reads the request provenance, if any middleware stored it.
func RequestMetaFrom(ctx context.Context) (RequestMeta, bool) {
	meta, ok := ctx.Value(requestMetaKey{}).(RequestMeta)
	return meta, ok
}

// RequestMetaFor builds the provenance of r from its RemoteAddr and User-Agent.
func RequestMetaFor(r *http.Request) RequestMeta {
	if r == nil {
		return RequestMeta{}
	}
	return RequestMeta{
		IP:        remoteHost(r.RemoteAddr),
		UserAgent: strings.TrimSpace(r.Header.Get("User-Agent")),
	}
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
