package executor

import (
	"sync"
	"time"
)

// Resolver caches one HTTPClient per executor endpoint. P8 multi-executor
// routing needs a client for each endpoint that owns a browser context; the
// token/timeout are shared by every client. A missing/empty endpoint returns
// nil so the caller can fail with a stable wiring error instead of dialing "".
type Resolver struct {
	token   string
	timeout time.Duration

	mu      sync.Mutex
	clients map[string]*HTTPClient
}

// NewResolver builds an endpoint-keyed client cache.
func NewResolver(token string, timeout time.Duration) *Resolver {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Resolver{
		token:   token,
		timeout: timeout,
		clients: map[string]*HTTPClient{},
	}
}

// Client returns the cached client for endpoint, creating it on first use.
func (r *Resolver) Client(endpoint string) *HTTPClient {
	if r == nil || endpoint == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if client, ok := r.clients[endpoint]; ok {
		return client
	}
	client := NewClient(endpoint, r.token, r.timeout)
	r.clients[endpoint] = client
	return client
}
