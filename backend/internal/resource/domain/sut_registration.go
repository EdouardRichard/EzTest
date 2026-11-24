package resource

import (
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// SUTRegistration is one append-only fact recording how a SUT came to be
// registered (v0.2.4-B R-B4). Every registration — the admin creation path wired
// by subproject B, and the Agent proposal path that subproject F will add —
// appends a new row; an existing row is never rewritten or deleted. The fact is
// independent of the SUT's current status so the registration history survives
// later approve/reject transitions (constitution 五.7: a fact that affects audit
// must exist independently of events or current state).
//
// Source uses the same vocabulary as SUT.Source. EvidenceRefs carries the
// content-addressed evidence the registration relied on (empty for the admin
// path, which needs no AI evidence). ResolutionMethod records how the SUT was
// resolved/created and GateResult the registration gate outcome; their concrete
// vocabulary for the Agent path is owned by subproject F, so they are free text
// here rather than a checked enum.
type SUTRegistration struct {
	ID               int64
	SUTID            int64
	Source           string
	EvidenceRefs     []string
	ResolutionMethod string
	GateResult       string
	CreatedAt        time.Time
	CreatedBy        int64
}

// MaxRegistrationMethodRunes caps the resolution_method and gate_result labels.
const MaxRegistrationMethodRunes = 100

// SUTResolutionAdminCreate is the resolution_method of the birth registration fact
// written by the admin SUT-creation path (v0.2.4-B R-B4). Because registration IDs
// are independent of SUT IDs, the birth record is identified by its content —
// sut_id + this resolution_method — so the value is load-bearing and must be
// referenced from this single constant, not hand-written. Subproject F's Agent
// proposal path appends with its own resolution methods.
const SUTResolutionAdminCreate = "admin_create"

// SUTResolutionAgentRegister is the resolution_method of the birth registration
// fact written by the Agent new-SUT path (v0.2.4-F R-B4/R-F1, SC-F4): the execution
// team explored a resource that matched no existing SUT and registered a new one
// (source=ai) through the server-side evidence gate. Like the admin birth record,
// it is identified by content (sut_id + this resolution_method), not by id equality
// with the SUT. The gate outcome (approved on proof, proposed otherwise) is recorded
// in gate_result; the fact is append-only and survives later admin reject.
const SUTResolutionAgentRegister = "agent_register"

// Validate checks one registration fact.
func (r SUTRegistration) Validate() error {
	if r.SUTID <= 0 {
		return platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	switch r.Source {
	case SUTSourceAdmin, SUTSourceAI, SUTSourceImport:
	default:
		return invalidEnum("source", r.Source)
	}
	if err := validation.String("resolution_method", r.ResolutionMethod, 1, MaxRegistrationMethodRunes); err != nil {
		return err
	}
	if err := validation.String("gate_result", r.GateResult, 1, MaxRegistrationMethodRunes); err != nil {
		return err
	}
	return nil
}
