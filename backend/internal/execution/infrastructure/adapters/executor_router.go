package adapters

import (
	"context"
	"fmt"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executorinfra "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
)

// ExecutorLookup resolves the endpoint of one registered executor (Registry
// implements it). Its List is used to enumerate every live endpoint for
// context/orphan cleanup.
type ExecutorLookup interface {
	Get(ctx context.Context, id int64) (execution.ExecutorInstance, error)
	List(ctx context.Context) ([]execution.ExecutorInstance, error)
}

// ExecutorRouter routes browser/control calls to the executor endpoint that
// owns a context. executorID 0 (historical rows or single-endpoint mode) uses
// the primary endpoint.
type ExecutorRouter struct {
	resolver *executorinfra.Resolver
	lookup   ExecutorLookup
	primary  string
}

// NewExecutorRouter wires the multi-executor router. lookup may be nil in
// single-endpoint tests; resolver must be non-nil to serve calls.
func NewExecutorRouter(resolver *executorinfra.Resolver, lookup ExecutorLookup, primary string) *ExecutorRouter {
	return &ExecutorRouter{resolver: resolver, lookup: lookup, primary: primary}
}

// ExecutorRouteDiagnostic describes how an executor id was resolved. It is
// attached to executor call diagnostics so a failure names the owner, the
// endpoint that was actually used, and whether the registry supplied it.
type ExecutorRouteDiagnostic struct {
	OwnerExecutorID int64
	Endpoint        string
	RegistryHit     bool
}

// clientFor routes one call. Unknown or offline owners fall back to the primary
// endpoint; the action then fails with a typed transport error, which the
// adapter annotates with registry_hit=false.
func (r *ExecutorRouter) clientFor(ctx context.Context, executorID int64) (executorinfra.Client, error) {
	client, _, err := r.clientForDiagnostics(ctx, executorID)
	return client, err
}

// clientForDiagnostics is clientFor plus the route diagnostic. An offline or
// unknown owner is never dialed: the primary endpoint is used instead and
// registry_hit stays false.
func (r *ExecutorRouter) clientForDiagnostics(ctx context.Context, executorID int64) (executorinfra.Client, ExecutorRouteDiagnostic, error) {
	diagnostic := ExecutorRouteDiagnostic{OwnerExecutorID: executorID}
	if r == nil || r.resolver == nil {
		return nil, diagnostic, errNotConfigured("executor client")
	}
	endpoint := r.primary
	if executorID > 0 && r.lookup != nil {
		instance, err := r.lookup.Get(ctx, executorID)
		if err == nil && instance.Endpoint != "" && instance.Status != execution.ExecutorOffline {
			endpoint = instance.Endpoint
			diagnostic.RegistryHit = true
		}
		// Unknown/offline ids fall back to the primary endpoint; they are never
		// routed to a stale/offline endpoint.
	}
	if endpoint == "" {
		return nil, diagnostic, errNotConfigured("executor endpoint")
	}
	client := r.resolver.Client(endpoint)
	if client == nil {
		return nil, diagnostic, errNotConfigured("executor client")
	}
	diagnostic.Endpoint = endpoint
	return client, diagnostic, nil
}

type routedClient struct {
	ExecutorID int64
	Client     executorinfra.Client
}

// clients returns one client per live executor endpoint. The primary endpoint
// is always included (with id 0 if no row owns it) so single-endpoint
// deployments keep P3 semantics.
func (r *ExecutorRouter) clients(ctx context.Context) ([]routedClient, error) {
	if r == nil || r.resolver == nil {
		return nil, errNotConfigured("executor client")
	}
	routed := make([]routedClient, 0)
	seen := map[string]struct{}{}
	add := func(id int64, endpoint string) {
		if endpoint == "" {
			return
		}
		if _, duplicate := seen[endpoint]; duplicate {
			return
		}
		client := r.resolver.Client(endpoint)
		if client == nil {
			return
		}
		seen[endpoint] = struct{}{}
		routed = append(routed, routedClient{ExecutorID: id, Client: client})
	}
	if r.lookup != nil {
		instances, err := r.lookup.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list executors: %w", err)
		}
		for _, instance := range instances {
			if instance.Status == execution.ExecutorOffline {
				continue
			}
			add(instance.ID, instance.Endpoint)
		}
	}
	add(0, r.primary)
	if len(routed) == 0 {
		return nil, errNotConfigured("executor endpoint")
	}
	return routed, nil
}
