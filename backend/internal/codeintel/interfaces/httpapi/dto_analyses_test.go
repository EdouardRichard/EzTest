package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// TestAnalysisStatsUnmappedTruncated pins the stats projection of the stored
// unmappedTruncated flag: a server-truncated unmapped list must reach the wire
// even when exactly 200 entries are persisted, and a stats document without the
// field must project false.
func TestAnalysisStatsUnmappedTruncated(t *testing.T) {
	serveLatest := func(t *testing.T, stats string) (analysisStatsWire, map[string]json.RawMessage) {
		t.Helper()
		analysis := testAnalysis()
		analysis.Stats = []byte(stats)
		analyses := &fakeAnalysisService{
			latestFn: func(context.Context, int64) (domain.CodeAnalysis, error) {
				return analysis, nil
			},
		}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/analyses/latest", userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data analysisWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.Stats == nil {
			t.Fatal("stats = null, want an object")
		}
		// The raw object also proves the field is always emitted, not merely
		// defaulted to false by the test decoder.
		var raw struct {
			Data struct {
				Stats map[string]json.RawMessage `json:"stats"`
			} `json:"data"`
		}
		decodeBody(t, recorder, &raw)
		return *body.Data.Stats, raw.Data.Stats
	}

	t.Run("a truncated unmapped list is carried by the response", func(t *testing.T) {
		typed, raw := serveLatest(t, `{"unmapped":200,"unmappedTruncated":true}`)

		if !typed.UnmappedTruncated {
			t.Fatal("stats.unmappedTruncated = false, want true")
		}
		field, ok := raw["unmappedTruncated"]
		if !ok {
			t.Fatalf("stats = %v, want the unmappedTruncated field", raw)
		}
		if string(field) != "true" {
			t.Fatalf("stats.unmappedTruncated = %s, want true", field)
		}
	})

	t.Run("an absent flag defaults to false", func(t *testing.T) {
		typed, raw := serveLatest(t, `{"unmapped":0}`)

		if typed.UnmappedTruncated {
			t.Fatal("stats.unmappedTruncated = true, want false")
		}
		field, ok := raw["unmappedTruncated"]
		if !ok {
			t.Fatalf("stats = %v, want the unmappedTruncated field always emitted", raw)
		}
		if string(field) != "false" {
			t.Fatalf("stats.unmappedTruncated = %s, want false when the document omits it", field)
		}
	})
}
