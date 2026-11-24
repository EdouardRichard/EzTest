package delivery

// Report display vocabulary (v0.2.4-G T10 fix round, 五.1 single source). The
// renderer and read projections compare against these constants instead of
// scattering raw literals. Run-status values are DERIVED from the execution
// domain (the authoritative status vocabulary, already a delivery-domain
// dependency). Closure-kind values mirror the version domain's ClosureKinds
// list; the delivery domain must not import another context's domain, so the
// equality is pinned by an adapters-side consistency test
// (TestReportDisplayVocabularyMatchesAuthority) rather than a cross-context
// import.

import (
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Report closure-kind display values (mirror version.ClosureKindNormal/Waived).
const (
	ReportClosureKindNormal = "normal"
	ReportClosureKindWaived = "waived"
)

// Report run-status display values (derived from the execution domain).
const (
	ReportRunStatusPassed       = string(execution.CaseRunPassed)
	ReportRunStatusFailed       = string(execution.CaseRunFailed)
	ReportRunStatusBlocked      = string(execution.CaseRunBlocked)
	ReportRunStatusInconclusive = string(execution.CaseRunInconclusive)
	ReportRunStatusSkipped      = string(execution.CaseRunSkipped)
)

// Report failure-class display values (derived from the execution domain, plus
// the delivery triage classifications). They are the keys the report's Chinese
// failure-class glossary translates.
const (
	ReportFailureClassApplication     = string(execution.FailureClassApplication)
	ReportFailureClassEnvironment     = string(execution.FailureClassEnvironment)
	ReportFailureClassSession         = string(execution.FailureClassSession)
	ReportFailureClassModel           = string(execution.FailureClassModel)
	ReportFailureClassTimeout         = string(execution.FailureClassTimeout)
	ReportFailureClassExecutorLost    = string(execution.FailureClassExecutorLost)
	ReportFailureClassUnknown         = string(execution.FailureClassUnknown)
	ReportFailureClassAutomation      = FailureClassAutomation
	ReportFailureClassExpectedFailure = FailureClassExpectedFailure
	ReportFailureClassBlocked         = FailureClassBlocked
)
