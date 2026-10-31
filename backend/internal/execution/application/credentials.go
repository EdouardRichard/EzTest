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

// AccountCredential is one test-account secret resolved for a (version_sut,
// role) pair. The password only lives in memory and is never logged, published
// or written to evidence.
type AccountCredential struct {
	AccountID int64
	Username  string
	Password  string
}

// DatabaseCredential is one target-database secret resolved for a version SUT.
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
	Account(ctx context.Context, versionID, versionSUTID int64, role string) (AccountCredential, error)
	Database(ctx context.Context, versionID, versionSUTID int64, databaseID *int64) (DatabaseCredential, error)
}

// AccountCredentials is the account.credentials tool. Reading a credential is
// audited with the agent run ID; the returned password is never written to an
// event or evidence.
func (s *CaseRunner) AccountCredentials(ctx context.Context, scope tools.Scope, in tools.AccountCredentialsInput) (any, error) {
	if s.credentials == nil {
		return nil, errors.New("case runner has no credential reader")
	}
	versionSUTID, err := s.resolveVersionSUTID(ctx, scope, in.VersionSUTID)
	if err != nil {
		return nil, err
	}
	credential, err := s.credentials.Account(ctx, scope.VersionID, versionSUTID, in.Role)
	if err != nil {
		return nil, mapExecutionError(err, "test_account")
	}
	s.recordCredentialAudit(scope, "account", versionSUTID, credential.AccountID, in.Role)
	return map[string]any{
		"version_sut_id": platformid.Format(versionSUTID),
		"account_id":     platformid.Format(credential.AccountID),
		"role":           in.Role,
		"username":       credential.Username,
		"password":       credential.Password,
	}, nil
}

// resolveVersionSUTID resolves the optional tool version_sut_id: an explicit ID
// must belong to the version; otherwise the case's default version_sut is used.
func (s *CaseRunner) resolveVersionSUTID(ctx context.Context, scope tools.Scope, explicit *int64) (int64, error) {
	bindings, err := s.versionBindings(ctx, scope.VersionID)
	if err != nil {
		return 0, err
	}
	ids := make(map[int64]struct{}, len(bindings))
	for _, binding := range bindings {
		ids[binding.VersionSUTID] = struct{}{}
	}
	if explicit != nil {
		if _, ok := ids[*explicit]; !ok {
			return 0, platform.Invalid("version_sut_id", "is not bound to the version")
		}
		return *explicit, nil
	}
	if scope.CaseRunID > 0 && s.caseRuns != nil && s.cases != nil {
		caseRun, getErr := s.caseRuns.Get(ctx, scope.CaseRunID)
		if getErr == nil {
			intent, intentErr := s.intents.Get(ctx, caseRun.IntentID)
			if intentErr == nil {
				snapshot, snapshotErr := s.cases.GetCaseVersion(ctx, intent.VersionID, caseRun.CaseVersionID)
				if snapshotErr == nil {
					if snapshot.DefaultVersionSUTID > 0 {
						if _, ok := ids[snapshot.DefaultVersionSUTID]; ok {
							return snapshot.DefaultVersionSUTID, nil
						}
					}
					for _, step := range snapshot.Steps {
						if _, ok := ids[step.TargetVersionSUTID]; ok {
							return step.TargetVersionSUTID, nil
						}
					}
				}
			}
		}
	}
	if len(bindings) == 1 {
		return bindings[0].VersionSUTID, nil
	}
	return 0, platform.Invalid("version_sut_id", "is required when the version has multiple SUT bindings")
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

// bindingFor returns one version SUT binding, validating ownership.
func (s *CaseRunner) bindingFor(ctx context.Context, versionID, versionSUTID int64) (execution.SUTBinding, error) {
	bindings, err := s.versionBindings(ctx, versionID)
	if err != nil {
		return execution.SUTBinding{}, err
	}
	for _, binding := range bindings {
		if binding.VersionSUTID == versionSUTID {
			return binding, nil
		}
	}
	return execution.SUTBinding{}, platform.Invalid("version_sut_id", fmt.Sprintf("version_sut %s is not bound to the version", platformid.Format(versionSUTID)))
}

func (s *CaseRunner) recordCredentialAudit(scope tools.Scope, kind string, versionSUTID, accountID int64, role string) {
	if s.audit == nil {
		return
	}
	metadata := map[string]any{
		"version_sut_id": platformid.Format(versionSUTID),
		"case_run_id":    platformid.Format(scope.CaseRunID),
		"agent_run_id":   platformid.Format(scope.AgentRunID),
		"credential":     kind,
		"account_id":     platformid.Format(accountID),
	}
	if role != "" {
		metadata["role"] = role
	}
	actor := int64(0)
	_ = s.audit.Record(context.Background(), auditapp.Event{
		ActorID:    &actor,
		Action:     "execution.credentials_read",
		TargetKind: "version_sut",
		TargetID:   platformid.Format(versionSUTID),
		Metadata:   metadata,
	})
}
