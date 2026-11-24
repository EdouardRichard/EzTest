package versionpg

import (
	"testing"
	"time"
)

// summaryScanRow is a rowScanner that yields one conversation_summary row with
// caller-supplied pinned_facts / summary_meta JSONB bytes, in scanSummary's
// column order.
type summaryScanRow struct {
	pinned []byte
	meta   []byte
}

func (r summaryScanRow) Scan(dest ...any) error {
	*dest[0].(*int64) = 1
	*dest[1].(*int64) = 2
	*dest[2].(*string) = "summary"
	*dest[3].(*[]byte) = r.pinned
	*dest[4].(*int) = 0
	*dest[5].(*int) = 1
	*dest[6].(*int64) = 0
	*dest[7].(*string) = ""
	*dest[8].(*[]byte) = r.meta
	*dest[9].(*time.Time) = time.Time{}
	return nil
}

// TestScanSummaryFailsClosedOnCorruptMeta pins the I-2 correction: the
// summary_meta JSONB document is decoded fail-closed like scanMessage's three
// documents, not silently swallowed (constitution 七.6). A corrupt document is
// an error; a valid or absent one decodes normally.
func TestScanSummaryFailsClosedOnCorruptMeta(t *testing.T) {
	if _, err := scanSummary(summaryScanRow{meta: []byte(`{not json`)}); err == nil {
		t.Fatal("scanSummary accepted a corrupt summary_meta document; want an error, not a silent swallow")
	}
	got, err := scanSummary(summaryScanRow{meta: []byte(`{"trigger":"pressure"}`)})
	if err != nil {
		t.Fatalf("scanSummary(valid meta): %v", err)
	}
	if got.SummaryMeta["trigger"] != "pressure" {
		t.Fatalf("SummaryMeta = %v, want trigger=pressure", got.SummaryMeta)
	}
	if _, err := scanSummary(summaryScanRow{}); err != nil {
		t.Fatalf("scanSummary(absent meta): %v", err)
	}
}
