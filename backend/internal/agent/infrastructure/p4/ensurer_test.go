package p4

import (
	"context"
	"errors"
	"testing"
)

type fakeScanner struct {
	calls []int64
	err   error
	count int
}

func (f *fakeScanner) ScanFailures(_ context.Context, versionID int64) (int, error) {
	f.calls = append(f.calls, versionID)
	return f.count, f.err
}

type fakeEnsure struct {
	name  string
	calls []int64
	err   error
}

func (f *fakeEnsure) EnsureReports(_ context.Context, versionID int64) error {
	f.calls = append(f.calls, versionID)
	return f.err
}

type fakeClosures struct {
	calls []int64
	err   error
}

func (f *fakeClosures) EnsureClosures(_ context.Context, versionID int64) error {
	f.calls = append(f.calls, versionID)
	return f.err
}

type fakeVersionClose struct {
	calls []int64
	err   error
}

func (f *fakeVersionClose) EnsureVersionClose(_ context.Context, versionID int64) error {
	f.calls = append(f.calls, versionID)
	return f.err
}

func TestEnsurerAdaptsTheFourRules(t *testing.T) {
	scanner := &fakeScanner{count: 3}
	reports := &fakeEnsure{name: "reports"}
	closures := &fakeClosures{}
	versionClose := &fakeVersionClose{}
	ensurer := NewEnsurer(scanner, reports, closures, versionClose)
	ctx := context.Background()

	if err := ensurer.ScanFailures(ctx, 7); err != nil {
		t.Fatalf("ScanFailures: %v", err)
	}
	if err := ensurer.EnsureReports(ctx, 7); err != nil {
		t.Fatalf("EnsureReports: %v", err)
	}
	if err := ensurer.EnsureClosures(ctx, 7); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	if err := ensurer.EnsureVersionClose(ctx, 7); err != nil {
		t.Fatalf("EnsureVersionClose: %v", err)
	}
	if len(scanner.calls) != 1 || scanner.calls[0] != 7 {
		t.Fatalf("scanner calls = %v", scanner.calls)
	}
	if len(reports.calls) != 1 || len(closures.calls) != 1 || len(versionClose.calls) != 1 {
		t.Fatalf("ensure calls = %v/%v/%v", reports.calls, closures.calls, versionClose.calls)
	}
}

func TestEnsurerPropagatesErrors(t *testing.T) {
	cause := errors.New("boom")
	ensurer := NewEnsurer(&fakeScanner{err: cause}, &fakeEnsure{err: cause}, &fakeClosures{err: cause}, &fakeVersionClose{err: cause})
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"ScanFailures":       func() error { return ensurer.ScanFailures(ctx, 7) },
		"EnsureReports":      func() error { return ensurer.EnsureReports(ctx, 7) },
		"EnsureClosures":     func() error { return ensurer.EnsureClosures(ctx, 7) },
		"EnsureVersionClose": func() error { return ensurer.EnsureVersionClose(ctx, 7) },
	} {
		if err := call(); !errors.Is(err, cause) {
			t.Fatalf("%s error = %v, want the wrapped cause", name, err)
		}
	}
}
