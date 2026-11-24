package execution

import (
	"context"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// SUTResolution is one append-only fact recording how a case_run resolved its
// target SUTs (v0.2.4-F P16, SC-F2). Rows are only ever inserted, never
// rewritten or deleted; resolving the same run again appends a second row
// (latest wins). The fact is independent of current state so the resolution
// history survives later changes (constitution 五.7).
//
// TouchedPageIDs/TouchedAPIIDs carry the page/API asset ids the run actually
// touched/verified valid — including a directory-hit run that never explores.
// They are the server-truth source for the knowledge page/API dimension
// (SC-F10): the projection reads resolved ids from this fact and never
// fabricates an id. SUTIDs/TouchedPageIDs/TouchedAPIIDs are logical BIGINT
// references into the resource context (no cross-context FK); CaseRunID is a
// NOT NULL within-context reference backed by a real foreign key to case_run
// (evidence/step_result precedent). ResolutionMethod is free text (catalog_hit,
// exploration, ...), owned by the P16 resolution service, like
// sut_registration.resolution_method.
type SUTResolution struct {
	ID               int64
	CaseRunID        int64
	SUTIDs           []int64
	ResolutionMethod string
	TouchedPageIDs   []int64
	TouchedAPIIDs    []int64
	// Ambiguous records that more than one catalog SUT matched the description
	// and the resolution SELECTED the top-ranked one (controller ruling P19,
	// design §6.2 "歧义按上下文选择"). It lets downstream (the SC-F10 knowledge
	// projection) tell a selection-from-ambiguity apart from an unambiguous
	// resolution. The touched assets are the SELECTED SUT's validated set only.
	Ambiguous bool
	CreatedAt time.Time
	CreatedBy int64
}

// MaxResolutionMethodRunes caps the resolution_method label.
const MaxResolutionMethodRunes = 100

// Normalize trims the method and installs the empty id slices a nil Go value
// cannot express (the DB columns default to '{}'). It is idempotent.
func (r *SUTResolution) Normalize() {
	r.ResolutionMethod = strings.TrimSpace(r.ResolutionMethod)
	if r.SUTIDs == nil {
		r.SUTIDs = []int64{}
	}
	if r.TouchedPageIDs == nil {
		r.TouchedPageIDs = []int64{}
	}
	if r.TouchedAPIIDs == nil {
		r.TouchedAPIIDs = []int64{}
	}
}

// Validate checks one resolution fact before it is appended.
func (r SUTResolution) Validate() error {
	if r.CaseRunID <= 0 {
		return platform.Invalid("case_run_id", "must be a positive decimal ID")
	}
	if err := validation.String("resolution_method", r.ResolutionMethod, 1, MaxResolutionMethodRunes); err != nil {
		return err
	}
	for _, id := range r.SUTIDs {
		if id <= 0 {
			return platform.Invalid("sut_ids", "must contain positive decimal IDs")
		}
	}
	for _, id := range r.TouchedPageIDs {
		if id <= 0 {
			return platform.Invalid("touched_page_ids", "must contain positive decimal IDs")
		}
	}
	for _, id := range r.TouchedAPIIDs {
		if id <= 0 {
			return platform.Invalid("touched_api_ids", "must contain positive decimal IDs")
		}
	}
	return nil
}

// SUTResolutionRepository persists the append-only sut_resolution fact store.
// It exposes only Append/List — never Update/Delete (append-only fact,
// constitution 五.7). ListSUTResolutions returns one case_run's facts in append
// order (id ascending); the caller takes the latest as the effective resolution.
type SUTResolutionRepository interface {
	AppendSUTResolution(ctx context.Context, resolution SUTResolution) (SUTResolution, error)
	ListSUTResolutions(ctx context.Context, caseRunID int64) ([]SUTResolution, error)
}
