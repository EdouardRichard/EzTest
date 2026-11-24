//go:build integration

package versionadapter

// H-B1b (T7-R1 routed): readBriefingKnowledgeScope mirrors
// resourceapp.SUTView.IsApproved as SQL in FOUR places — the knowledge family
// projection, the SUT/environment binding projection, the effective page keys and
// the effective API keys — each `(s.status = $n OR s.status = '')`.
// TestBriefingKnowledgeScopeExcludesProposedSUT already covers three hand-picked
// statuses with hand-written expectations; that is a VALUE test, not a RULE pin:
// change the predicate shape (drop the empty-status branch, invert it, gate the
// wrong column, gate only some of the four projections) and the hand-picked cases
// can stay green while the behaviour diverges from the authority.
//
// This pin closes that gap the way T7's
// version/infrastructure/sut_gate_isapproved_pin_integration_test.go does: for
// EVERY SUT status the resource domain declares (DERIVED from the source, so a new
// status joins automatically), seed a real version binding whose SUT owns an
// approved page and an approved API, and assert each of the four projections'
// behaviour equals resourceapp.SUTView.IsApproved for that status. The expectation
// comes from the AUTHORITY, never from the SQL's own output.
//
// Why a pin and not a port (the brief's option ②): the briefing reads every
// section inside ONE repeatable-read, read-only transaction (sqlBriefingSource.Load)
// so a concurrent write cannot mix two snapshots. resourceapp.HandoffResourceReader
// resolves through its own pool connections, outside that transaction, and its
// read model carries no SUT family at all (HandoffSUT has ID/Name/BaseURL/Pages/
// APIs/Databases) — the family projection is the first thing this scope needs.
// Routing through it would trade a pinned mirror for a lost snapshot invariant
// plus a port contract change plus a new cross-context wiring edge. Registered
// choice: option ① (behaviour pin), see task-11b-report.md.
//
// The empty-status branch is provably unreachable from the database
// (chk_sut_status, resource_0005); the last sub-test pins that claim instead of
// pretending to cover it.

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resource "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// briefingGateRepoRoot walks up from this test's source file to the repository
// root (the directory containing backend/go.mod).
func briefingGateRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the briefing SUT gate pin source")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "backend", "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("repo root containing backend/go.mod not found from " + file)
	return ""
}

// deriveBriefingSUTStatuses derives every SUT registration status from the
// resource domain source (the SUTStatus* declarations), so a NEW status constant
// is exercised without editing a list here. A declaration whose value is not a
// plain string literal is a LOUD failure, never a silent skip: dropping it would
// blind the pin to a status defined by reference (the govocab I1 / H-B1c shape).
//
// T11e-0: the walk now goes through platform/vocablint's shared elision-aware
// const walk. It used to be a LOCAL go/ast walk with the comment "that resolver
// lives under the version context's internal/ directory and Go forbids importing
// it from the agent context" — the visibility limit that forced this second copy
// is gone now that the machinery lives in the platform shared kernel
// (constitution 五.1/五.3 存量违例清偿). The AUTHORITY stays single
// (resource/domain/sut.go).
func deriveBriefingSUTStatuses(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(briefingGateRepoRoot(t), "backend", "internal", "resource", "domain", "sut.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse SUT status authority: %v", err)
	}
	var values []string
	for _, spec := range vocablint.ConstSpecs(file, token.CONST) {
		for i, name := range spec.Names {
			if !strings.HasPrefix(name, "SUTStatus") {
				continue
			}
			if i >= len(spec.Exprs) {
				t.Fatalf("SUT status declaration %s has no value expression; the pin cannot derive it", name)
			}
			value, isString, unquoteErr := vocablint.StringValue(spec.Exprs[i])
			if unquoteErr != nil {
				t.Fatalf("unquote SUT status %s: %v", name, unquoteErr)
			}
			if !isString {
				t.Fatalf("SUT status declaration %s is not a string literal; define it as one or classify it explicitly, otherwise the pin goes blind to it", name)
			}
			values = append(values, value)
		}
	}
	if len(values) < 3 {
		t.Fatalf("derived SUT status set %v is suspiciously small; the derivation is broken and the pin would go vacuous", values)
	}
	return values
}

// seedBriefingSUTGateFixture seeds one environment, one version bound to it and
// one SUT with the given registration status owning an APPROVED page and an
// APPROVED API. Every asset is in scope by its own status, so the only variable
// under test is the SUT's registration status. The family/route/API keys are
// unique per status so a leak is attributable.
func seedBriefingSUTGateFixture(t *testing.T, pool *pgxpool.Pool, base int64, status string) (versionID, sutID int64, family, pageKey, apiKey string) {
	t.Helper()
	ctx := context.Background()
	environmentID := base + 1
	versionID, sutID = base+2, base+3
	pageID, apiID := base+4, base+5
	family = "family-" + status
	pageKey = "/page-" + status
	apiKey = "POST /api/" + status
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed briefing SUT gate fixture (%s): %v", status, err)
		}
	}
	exec(`INSERT INTO environment (id, name, status) VALUES ($1, $2, $3)`, environmentID, "briefing gate env "+status, resource.StatusActive)
	exec(`INSERT INTO version (id, title, created_by, environment_id) VALUES ($1, 'briefing gate version', 1, $2)`, versionID, environmentID)
	exec(`INSERT INTO sut (id, environment_id, name, base_url, created_by, family, status)
		VALUES ($1, $2, 'gate sut', 'https://gate.example.test', 1, $3, $4)`, sutID, environmentID, family, status)
	exec(`INSERT INTO sut_page (id, sut_id, name, url, route_template, status)
		VALUES ($1, $2, 'gate page', 'https://gate.example.test/p', $3, $4)`, pageID, sutID, pageKey, resource.PageStatusApproved)
	exec(`INSERT INTO sut_api (id, sut_id, method, path_template, name, status)
		VALUES ($1, $2, 'POST', $3, 'gate api', $4)`, apiID, sutID, "/api/"+status, resource.APIStatusApproved)
	return versionID, sutID, family, pageKey, apiKey
}

func TestBriefingKnowledgeScopeMirrorsIsApproved(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	statuses := deriveBriefingSUTStatuses(t)

	for index, status := range statuses {
		t.Run(status, func(t *testing.T) {
			base := int64(720000 + 100*index)
			versionID, sutID, family, pageKey, apiKey := seedBriefingSUTGateFixture(t, pool, base, status)
			// The expectation comes from the AUTHORITY, not from the SQL's output.
			want := resourceapp.SUTView{Status: status}.IsApproved()

			tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
			if err != nil {
				t.Fatalf("begin briefing read tx: %v", err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			scope, err := readBriefingKnowledgeScope(ctx, tx, versionID)
			if err != nil {
				t.Fatalf("readBriefingKnowledgeScope: %v", err)
			}

			if _, ok := scope.families[family]; ok != want {
				t.Errorf("SUT status %q: knowledge family in scope = %v, but resourceapp.SUTView.IsApproved = %v -- the family projection must mirror the authority for every status",
					status, ok, want)
			}
			if _, ok := scope.systemIDs[sutID]; ok != want {
				t.Errorf("SUT status %q: system id in scope = %v, but IsApproved = %v -- the binding projection must mirror the authority",
					status, ok, want)
			}
			if _, ok := scope.pageKeys[pageKey]; ok != want {
				t.Errorf("SUT status %q: approved page key in scope = %v, but IsApproved = %v -- the page projection must mirror the authority",
					status, ok, want)
			}
			if _, ok := scope.apiKeys[apiKey]; ok != want {
				t.Errorf("SUT status %q: approved API key in scope = %v, but IsApproved = %v -- the API projection must mirror the authority",
					status, ok, want)
			}
			// The environment dimension rides the SAME binding projection as the
			// system id, so it must agree with it (a gate on one column only would
			// split them).
			_, envInScope := scope.environmentID[base+1]
			if envInScope != want {
				t.Errorf("SUT status %q: environment id in scope = %v, but IsApproved = %v", status, envInScope, want)
			}
		})
	}

	// The empty-status branch of the four SQL mirrors is UNREACHABLE: the column
	// is NOT NULL DEFAULT 'approved' with chk_sut_status (resource_0005). Pin that
	// claim rather than asserting a behaviour the database cannot produce.
	t.Run("empty_status_is_db_unreachable", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO sut (id, environment_id, name, base_url, created_by, status)
			VALUES (729001, 720001, 'empty status sut', 'https://empty.example.test', 1, '')`)
		if err == nil {
			t.Fatal("an empty sut.status was accepted; the mirrors' empty-status branch is reachable and needs its own behaviour pin")
		}
		if !strings.Contains(err.Error(), "chk_sut_status") {
			t.Fatalf("empty sut.status was rejected by something other than chk_sut_status: %v", err)
		}
		if !(resourceapp.SUTView{Status: ""}).IsApproved() {
			t.Fatal("IsApproved no longer treats the legacy empty status as approved; the SQL mirrors' empty branch must be revisited")
		}
	})
}

// TestBriefingSUTGateMirrorCountIsPinned keeps the pin's scope honest: the four
// IsApproved mirrors this test exercises must all still be present in the
// production reader. A mirror that silently disappears (a projection rewritten
// without the gate) would leave the pin asserting nothing about it.
func TestBriefingSUTGateMirrorCountIsPinned(t *testing.T) {
	path := filepath.Join(briefingGateRepoRoot(t), "backend", "internal", "agent", "infrastructure", "versionadapter", "briefing.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read briefing reader: %v", err)
	}
	src := strings.ReplaceAll(string(raw), "\r\n", "\n")
	// The empty-status branch is the mirror's shape marker, independent of which
	// placeholder index the approved constant is bound to.
	got := strings.Count(src, "OR s.status = ''")
	if got != 4 {
		t.Fatalf("briefing.go carries %d IsApproved SQL mirrors, want 4 (family / binding / page / API projections); "+
			"a projection changed shape and TestBriefingKnowledgeScopeMirrorsIsApproved must be re-aimed at it", got)
	}
}
