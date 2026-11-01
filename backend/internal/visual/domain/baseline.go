package visual

import "time"

// BaselineStatus is the visual_baseline.status enum. Baselines are historical:
// approving a new one supersedes the previous approved row instead of deleting
// it (spec §7.2).
type BaselineStatus string

const (
	BaselineApproved   BaselineStatus = "approved"
	BaselineSuperseded BaselineStatus = "superseded"
)

// Valid reports whether status is a persisted visual_baseline status.
func (s BaselineStatus) Valid() bool {
	return s == BaselineApproved || s == BaselineSuperseded
}

// Baseline is one approved visual baseline. Bytes are not copied: evidence_id
// is the logical reference to the immutable visual_actual evidence in the
// execution context (spec §7.4).
type Baseline struct {
	ID                     int64
	VisualAssertionID      int64
	CaseVersionID          int64
	VersionSUTID           int64
	EvidenceID             int64
	SHA256                 string
	SizeBytes              int64
	Width                  int
	Height                 int
	ViewportWidth          int
	ViewportHeight         int
	DeviceScaleFactor      float64
	ConfigRevision         int
	EnvironmentFingerprint string
	Status                 BaselineStatus
	ApprovedBy             int64
	ApprovedAt             time.Time
	SupersededAt           *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// Active reports whether the baseline is the current approved one.
func (b Baseline) Active() bool { return b.Status == BaselineApproved }
