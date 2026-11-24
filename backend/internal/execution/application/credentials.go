package executionapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// AccountCredential is one test-account secret resolved for a (SUT,
// role) pair. v0.2.4-C T12 (GD-01/Q05=B): credentials are plaintext by design and
// MAY enter prompt, evidence and logs; the old "never written to evidence"
// prohibition is lifted. Only the API output mask remains (a functional boundary,
// not a scrub).
type AccountCredential struct {
	AccountID int64
	Username  string
	Password  string
}

// DatabaseCredential is one target-database secret resolved for a bound SUT.
type DatabaseCredential struct {
	DatabaseID int64
	DSN        string
	Username   string
	Password   string
	ReadOnly   bool
}

// CredentialReader resolves version-SUT-bound credentials through the version
// binding + resource secret readers. It is implemented by the credential
// adapter; the application layer never sees a DSN/password outside this struct.
type CredentialReader interface {
	Account(ctx context.Context, versionID, sutID int64, role string) (AccountCredential, error)
	Database(ctx context.Context, versionID, sutID int64, databaseID *int64) (DatabaseCredential, error)
}

// AccountCredentials is the account.credentials tool. Reading a credential is
// audited with the agent run ID. v0.2.4-C T12 (GD-01/Q05=B): the returned
// password is evidence-eligible (the old "never written to evidence" prohibition
// is lifted). The audit METADATA inherently carries no plaintext password — that
// is the audit record's shape, not a scrub mechanism, and is retained.
func (s *CaseRunner) AccountCredentials(ctx context.Context, scope tools.Scope, in tools.AccountCredentialsInput) (any, error) {
	if s.credentials == nil {
		return nil, errors.New("case runner has no credential reader")
	}
	// in.VersionSUTID is the agent-owned tool DTO Go field
	// (agent/application/tools/case_execution_tools.go AccountCredentialsInput); its value is
	// a SUT id. The model-facing WIRE key is sut_id since v0.2.4-C T14 (R-C1);
	// the Go field name is internal and kept. See the lockstep note in tools.go.
	sutID, err := s.resolveSUTID(ctx, scope, in.VersionSUTID)
	if err != nil {
		return nil, err
	}
	credential, err := s.credentials.Account(ctx, scope.VersionID, sutID, in.Role)
	if err != nil {
		return nil, mapExecutionError(err, "test_account")
	}
	s.recordCredentialAudit(scope, "account", sutID, credential.AccountID, in.Role)
	return map[string]any{
		"sut_id":     platformid.Format(sutID),
		"account_id": platformid.Format(credential.AccountID),
		"role":       in.Role,
		"username":   credential.Username,
		"password":   credential.Password,
	}, nil
}

// resolveSUTID resolves the optional tool sut_id: an explicit ID must belong to
// the version; otherwise the run's own SUT is inferred.
//
// v0.2.4-B (T9 design contract): the inference can no longer read a case default
// or a per-step target, because a case step carries prose instead of ids. The
// case-scoped source is now the intent's SUT set, which is frozen when the
// intent is created; when it is unambiguous and still bound, it wins. Otherwise
// a version binding exactly one SUT is unambiguous the same way, and a
// multi-SUT version must be told explicitly.
func (s *CaseRunner) resolveSUTID(ctx context.Context, scope tools.Scope, explicit *int64) (int64, error) {
	bindings, err := s.versionBindings(ctx, scope.VersionID)
	if err != nil {
		return 0, err
	}
	ids := make(map[int64]struct{}, len(bindings))
	for _, binding := range bindings {
		ids[binding.SUTID] = struct{}{}
	}
	if explicit != nil {
		if _, ok := ids[*explicit]; !ok {
			return 0, platform.Invalid("sut_id", "is not bound to the version")
		}
		return *explicit, nil
	}
	if scope.CaseRunID > 0 && s.caseRuns != nil {
		if caseRun, getErr := s.caseRuns.Get(ctx, scope.CaseRunID); getErr == nil {
			if intent, intentErr := s.intents.Get(ctx, caseRun.IntentID); intentErr == nil &&
				len(intent.SUTIDs) == 1 && intent.SUTIDs[0] > 0 {
				if _, ok := ids[intent.SUTIDs[0]]; ok {
					return intent.SUTIDs[0], nil
				}
			}
		}
	}
	if len(bindings) == 1 {
		return bindings[0].SUTID, nil
	}
	return 0, platform.Invalid("sut_id", "is required when the version has multiple SUT bindings")
}

// versionBindings returns the resolved SUT bindings of a version.
func (s *CaseRunner) versionBindings(ctx context.Context, versionID int64) ([]execution.SUTBinding, error) {
	if s.bindings == nil {
		return nil, errors.New("case runner has no SUT binding resolver")
	}
	bindings, err := s.bindings.Resolve(ctx, versionID)
	if err != nil {
		return nil, mapExecutionError(err, "version")
	}
	if len(bindings) == 0 {
		return nil, platform.StateConflict("the version has no active SUT bindings")
	}
	return bindings, nil
}

// bindingFor returns one bound SUT, validating ownership.
func (s *CaseRunner) bindingFor(ctx context.Context, versionID, sutID int64) (execution.SUTBinding, error) {
	bindings, err := s.versionBindings(ctx, versionID)
	if err != nil {
		return execution.SUTBinding{}, err
	}
	for _, binding := range bindings {
		if binding.SUTID == sutID {
			return binding, nil
		}
	}
	return execution.SUTBinding{}, platform.Invalid("sut_id", fmt.Sprintf("SUT %s is not bound to the version", platformid.Format(sutID)))
}

func (s *CaseRunner) recordCredentialAudit(scope tools.Scope, kind string, sutID, accountID int64, role string) {
	if s.audit == nil {
		return
	}
	metadata := map[string]any{
		"sut_id":       platformid.Format(sutID),
		"case_run_id":  platformid.Format(scope.CaseRunID),
		"agent_run_id": platformid.Format(scope.AgentRunID),
		"credential":   kind,
		"account_id":   platformid.Format(accountID),
	}
	if role != "" {
		metadata["role"] = role
	}
	actor := int64(0)
	_ = s.audit.Record(context.Background(), auditapp.Event{
		ActorID: &actor,
		Action:  "execution.credentials_read",
		// v0.2.4-B: the audit target is a SUT, not the dropped binding row.
		TargetKind: "sut",
		TargetID:   platformid.Format(sutID),
		Metadata:   metadata,
	})
}
