package agentapp_test

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func agentP6Int64Ptr(value int64) *int64 { return &value }

// fakeP6RetryReader is the agent-context P6 retry read source used by the query
// port tests.
type fakeP6RetryReader struct {
	num   int
	denom int
	err   error
	calls int
	scope *int64
}

func (f *fakeP6RetryReader) CountTaskRetries(_ context.Context, versionID *int64) (int, int, error) {
	f.calls++
	f.scope = versionID
	return f.num, f.denom, f.err
}

func TestP6CountTaskRetries(t *testing.T) {
	t.Run("empty store returns zero counts", func(t *testing.T) {
		reader := &fakeP6RetryReader{}
		service := agentapp.NewQueryService(reader)

		num, denom, err := service.CountTaskRetries(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountTaskRetries: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
		if reader.calls != 1 || reader.scope != nil {
			t.Fatalf("reader calls = %d scope = %v, want one global call", reader.calls, reader.scope)
		}
	})

	t.Run("version filter is forwarded", func(t *testing.T) {
		reader := &fakeP6RetryReader{num: 2, denom: 9}
		service := agentapp.NewQueryService(reader)

		num, denom, err := service.CountTaskRetries(context.Background(), agentP6Int64Ptr(7))
		if err != nil {
			t.Fatalf("CountTaskRetries: %v", err)
		}
		if num != 2 || denom != 9 {
			t.Fatalf("counts = %d/%d, want 2/9", num, denom)
		}
		if reader.scope == nil || *reader.scope != 7 {
			t.Fatalf("scope = %v, want 7", reader.scope)
		}
	})

	t.Run("zero denominator is reported without error", func(t *testing.T) {
		service := agentapp.NewQueryService(&fakeP6RetryReader{})

		num, denom, err := service.CountTaskRetries(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountTaskRetries: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
	})

	t.Run("non-positive version is rejected", func(t *testing.T) {
		reader := &fakeP6RetryReader{}
		service := agentapp.NewQueryService(reader)

		if _, _, err := service.CountTaskRetries(context.Background(), agentP6Int64Ptr(0)); err == nil {
			t.Fatal("expected invalid_argument for version_id=0")
		}
		if reader.calls != 0 {
			t.Fatalf("reader calls = %d, want 0", reader.calls)
		}
	})

	t.Run("reader error is propagated", func(t *testing.T) {
		want := errors.New("boom")
		service := agentapp.NewQueryService(&fakeP6RetryReader{err: want})

		if _, _, err := service.CountTaskRetries(context.Background(), nil); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	})

	t.Run("nil reader is unavailable", func(t *testing.T) {
		service := agentapp.NewQueryService(nil)
		_, _, err := service.CountTaskRetries(context.Background(), nil)
		if err == nil {
			t.Fatal("expected unavailable error without a reader")
		}
		if platform.AsError(err).Status != 503 {
			t.Fatalf("status = %d, want 503", platform.AsError(err).Status)
		}
	})
}
