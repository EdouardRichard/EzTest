package versionapi_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// fakeFactArbitrationService records one arbitration call and serves canned
// outcomes. It stands in for *versionapp.FactService.
type fakeFactArbitrationService struct {
	results []versionapp.FactArbitration
	err     error
	calls   []factArbitrationCall
}

type factArbitrationCall struct {
	versionID int64
	factID    int64
	decision  versionapp.FactArbitrationDecision
	reason    string
	actorID   int64
}

func (f *fakeFactArbitrationService) ArbitrateFact(
	_ context.Context, versionID, factID int64, decision versionapp.FactArbitrationDecision,
	reason string, actorID int64,
) (versionapp.FactArbitration, error) {
	f.calls = append(f.calls, factArbitrationCall{
		versionID: versionID, factID: factID, decision: decision, reason: reason, actorID: actorID,
	})
	if f.err != nil {
		return versionapp.FactArbitration{}, f.err
	}
	index := len(f.calls) - 1
	if index >= len(f.results) {
		index = len(f.results) - 1
	}
	return f.results[index], nil
}

var _ versionapi.FactArbitrationService = (*fakeFactArbitrationService)(nil)

func factConfidencePtr(value float64) *float64 { return &value }

func newFactArbitrationServer(svc versionapi.FactArbitrationService) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/versions/{id}/facts/{fid}/arbitrate",
		versionapi.NewFactHandler(svc).Arbitrate)
	return mux
}

func factArbitrationResult(decision versionapp.FactArbitrationDecision) versionapp.FactArbitration {
	keepID := int64(2)
	dropped := int64(1)
	result := versionapp.FactArbitration{
		VersionID:  7,
		FactID:     2,
		Kind:       "decision",
		Key:        "refund",
		Decision:   decision,
		Reason:     "host prefers T+3",
		KeepID:     keepID,
		DroppedIDs: []int64{dropped},
		Changed:    true,
		Facts: []version.Fact{{
			ID: keepID, VersionID: 7, Kind: "decision", Key: "refund",
			Confidence: factConfidencePtr(0.9), ContentHash: "keep-hash",
		}},
	}
	if decision == versionapp.FactArbitrationDrop {
		result.FactID = dropped
		result.KeepID = keepID
	}
	return result
}

// TestFactArbitrateHandler pins the 200 contract of API-13: the handler parses
// both path IDs, the decision and reason, requires the authenticated actor, and
// renders the resolution plus the resulting current row.
func TestFactArbitrateHandler(t *testing.T) {
	cases := []struct {
		name        string
		decision    versionapp.FactArbitrationDecision
		factID      string
		wantKeepID  string
		wantDropIDs int
	}{
		{name: "keep", decision: versionapp.FactArbitrationKeep, factID: "2", wantKeepID: "2", wantDropIDs: 1},
		{name: "drop", decision: versionapp.FactArbitrationDrop, factID: "1", wantKeepID: "2", wantDropIDs: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeFactArbitrationService{results: []versionapp.FactArbitration{factArbitrationResult(tc.decision)}}
			recorder := call(t, newFactArbitrationServer(svc), http.MethodPost,
				"/api/versions/7/facts/"+tc.factID+"/arbitrate",
				`{"decision":"`+string(tc.decision)+`","reason":"host prefers T+3"}`, actor())
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if len(svc.calls) != 1 {
				t.Fatalf("service calls = %d, want 1", len(svc.calls))
			}
			callArgs := svc.calls[0]
			if callArgs.versionID != 7 || strconv.FormatInt(callArgs.factID, 10) != tc.factID {
				t.Fatalf("service args = %+v", callArgs)
			}
			if callArgs.decision != tc.decision || callArgs.reason != "host prefers T+3" || callArgs.actorID != 42 {
				t.Fatalf("service args = %+v", callArgs)
			}

			body := dataBody(t, recorder)
			if body["version_id"] != "7" || body["fact_id"] != tc.factID {
				t.Fatalf("response identity = %#v", body)
			}
			if body["decision"] != string(tc.decision) || body["reason"] != "host prefers T+3" {
				t.Fatalf("response decision = %#v", body)
			}
			if body["keep_id"] != tc.wantKeepID || body["changed"] != true {
				t.Fatalf("response outcome = %#v", body)
			}
			drops, ok := body["drop_ids"].([]any)
			if !ok || len(drops) != tc.wantDropIDs {
				t.Fatalf("response drop_ids = %#v", body["drop_ids"])
			}
			current, ok := body["current"].([]any)
			if !ok || len(current) != 1 {
				t.Fatalf("response current = %#v", body["current"])
			}
			first, _ := current[0].(map[string]any)
			if first["id"] != tc.wantKeepID || first["superseded_by"] != nil {
				t.Fatalf("response current row = %#v", first)
			}
		})
	}
}

// TestFactArbitrateHandlerErrors pins the typed error mapping: an unknown fact
// is 404, an invalid decision/reason is 400, and a repeated request is a 200
// idempotent success rather than a second mutation.
func TestFactArbitrateHandlerErrors(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		svc := &fakeFactArbitrationService{err: platform.NotFound("fact")}
		recorder := call(t, newFactArbitrationServer(svc), http.MethodPost,
			"/api/versions/7/facts/999/arbitrate", `{"decision":"keep","reason":"keep it"}`, actor())
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if code := errorBody(t, recorder)["code"]; code != "not_found" {
			t.Fatalf("code = %#v", code)
		}
	})

	t.Run("invalid decision", func(t *testing.T) {
		svc := &fakeFactArbitrationService{err: platform.Invalid("decision", "must be one of keep, drop")}
		recorder := call(t, newFactArbitrationServer(svc), http.MethodPost,
			"/api/versions/7/facts/2/arbitrate", `{"decision":"maybe","reason":"because"}`, actor())
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if code := errorBody(t, recorder)["code"]; code != "invalid_argument" {
			t.Fatalf("code = %#v", code)
		}
	})

	t.Run("missing reason", func(t *testing.T) {
		svc := &fakeFactArbitrationService{err: platform.Invalid("reason", "must not be empty")}
		recorder := call(t, newFactArbitrationServer(svc), http.MethodPost,
			"/api/versions/7/facts/2/arbitrate", `{"decision":"keep"}`, actor())
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		svc := &fakeFactArbitrationService{results: []versionapp.FactArbitration{factArbitrationResult(versionapp.FactArbitrationKeep)}}
		recorder := call(t, newFactArbitrationServer(svc), http.MethodPost,
			"/api/versions/7/facts/2/arbitrate", `{`, actor())
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if len(svc.calls) != 0 {
			t.Fatalf("malformed body reached the service: %+v", svc.calls)
		}
	})

	t.Run("invalid path id", func(t *testing.T) {
		svc := &fakeFactArbitrationService{results: []versionapp.FactArbitration{factArbitrationResult(versionapp.FactArbitrationKeep)}}
		recorder := call(t, newFactArbitrationServer(svc), http.MethodPost,
			"/api/versions/abc/facts/2/arbitrate", `{"decision":"keep","reason":"keep it"}`, actor())
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if len(svc.calls) != 0 {
			t.Fatalf("invalid path reached the service: %+v", svc.calls)
		}
	})

	t.Run("idempotent repeat", func(t *testing.T) {
		first := factArbitrationResult(versionapp.FactArbitrationKeep)
		repeated := first
		repeated.Changed = false
		svc := &fakeFactArbitrationService{results: []versionapp.FactArbitration{first, repeated}}
		recorder := call(t, newFactArbitrationServer(svc), http.MethodPost,
			"/api/versions/7/facts/2/arbitrate", `{"decision":"keep","reason":"host prefers T+3"}`, actor())
		if recorder.Code != http.StatusOK || dataBody(t, recorder)["changed"] != true {
			t.Fatalf("first status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		recorder = call(t, newFactArbitrationServer(svc), http.MethodPost,
			"/api/versions/7/facts/2/arbitrate", `{"decision":"keep","reason":"host prefers T+3"}`, actor())
		if recorder.Code != http.StatusOK {
			t.Fatalf("repeat status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if dataBody(t, recorder)["changed"] != false {
			t.Fatalf("repeat body = %s, want changed false", recorder.Body.String())
		}
		if len(svc.calls) != 2 {
			t.Fatalf("service calls = %d, want 2", len(svc.calls))
		}
	})
}

// TestFactArbitrateHandlerActorAndUnavailable pins the auth guard and the
// degraded-assembly answer.
func TestFactArbitrateHandlerActorAndUnavailable(t *testing.T) {
	t.Run("missing actor", func(t *testing.T) {
		svc := &fakeFactArbitrationService{results: []versionapp.FactArbitration{factArbitrationResult(versionapp.FactArbitrationKeep)}}
		recorder := call(t, newFactArbitrationServer(svc), http.MethodPost,
			"/api/versions/7/facts/2/arbitrate", `{"decision":"keep","reason":"keep it"}`, nil)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if len(svc.calls) != 0 {
			t.Fatalf("unauthenticated call reached the service: %+v", svc.calls)
		}
	})

	t.Run("unconfigured service", func(t *testing.T) {
		recorder := call(t, newFactArbitrationServer(nil), http.MethodPost,
			"/api/versions/7/facts/2/arbitrate", `{"decision":"keep","reason":"keep it"}`, actor())
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})
}
