package executionapp

import (
	"context"
	"log/slog"
	"sync"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// SettingMaxConcurrentCaseRuns is the single global concurrent-case-run limit
// (design P5, v0.2.4-F): 0 = unlimited (the catalog default). It replaces the
// retired execution.global_concurrency / per_sut_concurrency /
// cross_system_concurrency trio. The scheduler reads it on every dispatch pass
// and the REST quotas projection reads the same key through the same QuotaResolver
// implementation -- but the gate and the handler hold INDEPENDENT resolver
// instances with separate last-known-good state. The gate is authoritative; after
// a settings update they converge, and on a read-failure the projection may
// momentarily differ (each retains its own last-known-good value).
const SettingMaxConcurrentCaseRuns = "execution.max_concurrent_case_runs"

// settingAgentMaxConcurrentRuns is the agent worker's claim concurrency
// (settings catalog "agent.max_concurrent_runs", read by agent
// Worker.concurrency). It is the provable claim headroom for dispatch
// admission: a dispatched-but-unclaimed case_run occupies a work-queue slot
// until a worker claims it, and nothing renews its dispatch lease while it
// waits. Keep the key string in lockstep with the settings catalog and the
// agent worker reader.
const settingAgentMaxConcurrentRuns = "agent.max_concurrent_runs"

// minProvableClaimHeadroom is the admission bound used when the worker claim
// concurrency has never been read successfully. The settings catalog enforces
// Min 1 on agent.max_concurrent_runs, so 1 is the provable MINIMUM of real
// claim capacity -- never an artificial cap in normal operation, and never a
// fail-open to unbounded queueing.
const minProvableClaimHeadroom = 1

// QuotaResolver reads the concurrency settings with last-known-good retention
// (controller ruling P12, fix round 1). It is the ONE implementation of the
// read-failure semantics shared by the scheduler gate and the REST quotas
// projection: a failed read logs a WARN and retains the last successfully-read
// value; it never silently widens admission to the zero value. Before the
// first successful read the fallback is the catalog default (quota 0 =
// unlimited; claim headroom = the provable minimum), and the failure is still
// logged. The zero value is ready to use; a resolver must not be copied after
// first use (it holds a mutex).
type QuotaResolver struct {
	mu         sync.Mutex
	lastQuota  execution.Quota
	quotaKnown bool
	lastClaim  int
	claimKnown bool
}

// Resolve returns the admin-configured global limit (0 = unlimited). A nil
// settings source is a degraded assembly (tests / half-wired processes) and
// yields the catalog default without logging.
func (r *QuotaResolver) Resolve(ctx context.Context, settings SettingsReader) execution.Quota {
	if settings == nil {
		return execution.Quota{}
	}
	value, err := settings.Int(ctx, SettingMaxConcurrentCaseRuns)
	if err == nil {
		quota := execution.QuotaFromSettings(int(value))
		r.mu.Lock()
		r.lastQuota = quota
		r.quotaKnown = true
		r.mu.Unlock()
		return quota
	}
	r.mu.Lock()
	fallback := r.lastQuota
	known := r.quotaKnown
	r.mu.Unlock()
	slog.WarnContext(ctx,
		"execution: failed to read the global concurrency quota setting; retaining the last-known-good limit",
		"setting", SettingMaxConcurrentCaseRuns,
		"error", err,
		"fallback_global", fallback.Global,
		"last_known_good", known)
	return fallback
}

// claimHeadroom returns the worker claim concurrency with the same retention
// semantics. An out-of-contract value (<= 0; the catalog enforces Min 1) is
// treated as a failed read, mirroring the agent worker's value > 0 guard.
func (r *QuotaResolver) claimHeadroom(ctx context.Context, settings SettingsReader) int {
	value, err := settings.Int(ctx, settingAgentMaxConcurrentRuns)
	if err == nil && value > 0 {
		headroom := int(value)
		r.mu.Lock()
		r.lastClaim = headroom
		r.claimKnown = true
		r.mu.Unlock()
		return headroom
	}
	r.mu.Lock()
	fallback := r.lastClaim
	known := r.claimKnown
	r.mu.Unlock()
	if !known {
		fallback = minProvableClaimHeadroom
	}
	slog.WarnContext(ctx,
		"execution: failed to read the worker claim concurrency setting; retaining the last-known-good claim headroom",
		"setting", settingAgentMaxConcurrentRuns,
		"error", err,
		"fallback_claim_headroom", fallback,
		"last_known_good", known)
	return fallback
}

// DispatchLimit composes the effective admission bound for one dispatch pass
// (controller ruling P13, fix round 1): the admin limit AND the provable claim
// headroom, whichever is tighter. Design P5's "0 = unlimited" means no
// artificial cap -- dispatch still follows real resources. Without the claim
// bound, an unlimited default would stamp a whole large wave running+enqueued
// at once; queued runs past the dispatch lease are then FALSE-recovered by
// Recovery as environment/dispatch_timeout failures (executor.ActiveContexts
// only refreshes on the health poll, and nothing renews a queued run's lease).
// Bounding in-flight dispatched runs by claim headroom keeps every dispatched
// run promptly claimable while the rest wait as pending (no lease, no attempts).
// A nil settings source keeps the legacy unbounded behavior for degraded
// assemblies; production always wires the settings service.
func (r *QuotaResolver) DispatchLimit(ctx context.Context, settings SettingsReader) int {
	quota := r.Resolve(ctx, settings)
	if settings == nil {
		return quota.Global
	}
	headroom := r.claimHeadroom(ctx, settings)
	if quota.Global > 0 && quota.Global < headroom {
		return quota.Global
	}
	return headroom
}
