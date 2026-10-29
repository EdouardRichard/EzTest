package domain_test

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestParseBatchKind(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want domain.BatchKind
	}{
		{"", domain.BatchKindBaseline},
		{"baseline", domain.BatchKindBaseline},
		{"incremental", domain.BatchKindIncremental},
		{"supplement", domain.BatchKindSupplement},
	} {
		got, err := domain.ParseBatchKind(tc.raw)
		if err != nil {
			t.Fatalf("ParseBatchKind(%q) error = %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ParseBatchKind(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	if _, err := domain.ParseBatchKind("replay"); err == nil {
		t.Fatal("ParseBatchKind(replay) error = nil, want a validation error")
	}
}

func TestParseCaseSourceDefaultsToBaseline(t *testing.T) {
	if got := domain.CaseSourceBaseline; got != "baseline" {
		t.Fatalf("CaseSourceBaseline = %q", got)
	}
	if got := domain.CaseSourceAISupplement; got != "ai_supplement" {
		t.Fatalf("CaseSourceAISupplement = %q", got)
	}
	if got := domain.ParseCaseSource(""); got != domain.CaseSourceBaseline {
		t.Fatalf("ParseCaseSource(\"\") = %q, want baseline", got)
	}
	if got := domain.ParseCaseSource("ai_supplement"); got != domain.CaseSourceAISupplement {
		t.Fatalf("ParseCaseSource(ai_supplement) = %q", got)
	}
	if got := domain.ParseCaseSource("other"); got != domain.CaseSourceBaseline {
		t.Fatalf("ParseCaseSource(other) = %q, want baseline", got)
	}
}
