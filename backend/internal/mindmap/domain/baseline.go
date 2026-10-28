package domain

import "time"

// CaseBaseline is the immutable snapshot created when a review is approved.
// VersionID is unique: a version can be frozen only once (D5). GraphHash is the
// lowercase hex sha256 of the canonical graph (spec §6.4). IDs are
// application-generated Snowflakes.
type CaseBaseline struct {
	ID                 int64
	VersionID          int64
	ReviewID           int64
	NodeCount          int
	CaseCount          int
	GraphHash          string
	CodeBaselineCommit *string
	FrozenBy           int64
	FrozenAt           time.Time
}
