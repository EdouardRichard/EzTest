package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// DBTargetService implements the environment read-only database target use
// cases (spec §5.7/§7.8). The plaintext password is only ever handed to the
// repository and the connection prober; the API layer builds its DTO from the
// domain target and never copies the password.
type DBTargetService struct {
	targets DBTargetRepository
	envs    domain.EnvironmentRepository
	prober  DBTargetProber
	ids     domain.IDGenerator
	clock   Clock
}

// UpsertDBTargetCommand is the PUT /api/resources/environments/{id}/db-target
// body. The required fields are plain values (an omitted one fails validation);
// a nil optional field keeps the stored value on update and applies the DDL
// default on create; a nil Password keeps the stored password and is required on
// create.
type UpsertDBTargetCommand struct {
	Name               string
	Host               string
	Port               int
	Database           string
	Username           string
	Password           *string
	SSLMode            *string
	ConnectTimeoutMS   *int
	StatementTimeoutMS *int
	MaxRows            *int
	Enabled            *bool
}

// NewDBTargetService wires the db target use cases to their repository, the
// environment repository, the connection prober, the snowflake id generator and
// the injectable clock. The id generator is required for the first Upsert of an
// environment and must be the process-wide generator (D29).
func NewDBTargetService(
	targets DBTargetRepository,
	envs domain.EnvironmentRepository,
	prober DBTargetProber,
	ids domain.IDGenerator,
	clock Clock,
) *DBTargetService {
	return &DBTargetService{targets: targets, envs: envs, prober: prober, ids: ids, clock: clock}
}

// Get returns the environment target, or (nil, nil) when the environment has
// none configured (spec §7.8 returns data:null).
func (s *DBTargetService) Get(ctx context.Context, environmentID int64) (*domain.DBTarget, error) {
	if environmentID <= 0 {
		return nil, domain.NewInvalid("id", "invalid_id", "id must be a positive integer")
	}
	return s.targets.GetDBTarget(ctx, environmentID)
}

// Upsert creates or replaces the single target of environmentID. The
// environment must exist (resource configuration is visible to admins);
// environment_id stays a logical cross-context reference (D34).
func (s *DBTargetService) Upsert(ctx context.Context, environmentID, actor int64, cmd UpsertDBTargetCommand) (domain.DBTarget, error) {
	if environmentID <= 0 {
		return domain.DBTarget{}, domain.NewInvalid("id", "invalid_id", "id must be a positive integer")
	}
	if err := s.checkEnvironment(ctx, environmentID); err != nil {
		return domain.DBTarget{}, err
	}
	existing, err := s.targets.GetDBTarget(ctx, environmentID)
	if err != nil {
		return domain.DBTarget{}, err
	}

	target := domain.DBTarget{EnvironmentID: environmentID, Enabled: true}
	if existing != nil {
		target = *existing
		target.EnvironmentID = environmentID
	}
	applyDBTargetCommand(&target, cmd, existing == nil)

	if existing == nil {
		if target.Password == "" {
			return domain.DBTarget{}, domain.NewInvalid("password", domain.ReasonDBTargetInvalid,
				"password is required the first time the target is configured")
		}
		if s.ids == nil {
			return domain.DBTarget{}, errors.New("application: db target id generator is not configured")
		}
		id := s.ids.Next()
		if id <= 0 {
			return domain.DBTarget{}, fmt.Errorf("application: db target id generator returned a non-positive id %d", id)
		}
		target.ID = id
		target.CreatedBy = actor
		target.LastTestStatus = domain.DBTargetTestStatusNone
		target.LastTestMessage = ""
	}
	target.UpdatedBy = actor

	if err := domain.ValidateDBTarget(&target); err != nil {
		return domain.DBTarget{}, err
	}
	if err := s.targets.UpsertDBTarget(ctx, &target); err != nil {
		return domain.DBTarget{}, err
	}
	return target, nil
}

// Delete removes the configured target of environmentID. A missing target is
// the stable db_target_missing 409 (spec §7.11).
func (s *DBTargetService) Delete(ctx context.Context, environmentID, actor int64) error {
	if environmentID <= 0 {
		return domain.NewInvalid("id", "invalid_id", "id must be a positive integer")
	}
	existing, err := s.targets.GetDBTarget(ctx, environmentID)
	if err != nil {
		return err
	}
	if existing == nil {
		return domain.NewDBTargetMissing(environmentID)
	}
	return s.targets.DeleteDBTarget(ctx, environmentID)
}

// Test probes the stored target (override nil) or an unsaved form override. A
// probe failure is reported in the result with the classified reason; the error
// return is reserved for configuration and storage failures. Only a test of the
// stored configuration refreshes the last_test_* fields; an override is never
// persisted ("body 可选覆盖当前表单（不落库）").
func (s *DBTargetService) Test(ctx context.Context, environmentID int64, override *UpsertDBTargetCommand) (domain.DBTargetTestResult, error) {
	if environmentID <= 0 {
		return domain.DBTargetTestResult{}, domain.NewInvalid("id", "invalid_id", "id must be a positive integer")
	}
	stored, err := s.targets.GetDBTarget(ctx, environmentID)
	if err != nil {
		return domain.DBTargetTestResult{}, err
	}

	var target domain.DBTarget
	if override != nil {
		if err := s.checkEnvironment(ctx, environmentID); err != nil {
			return domain.DBTargetTestResult{}, err
		}
		target = domain.DBTarget{EnvironmentID: environmentID, Enabled: true}
		if stored != nil {
			target = *stored
			target.EnvironmentID = environmentID
		}
		applyDBTargetCommand(&target, *override, stored == nil)
		if target.Password == "" {
			return domain.DBTargetTestResult{}, domain.NewInvalid("password", domain.ReasonDBTargetInvalid,
				"password is required the first time the target is configured")
		}
	} else {
		if stored == nil {
			return domain.DBTargetTestResult{}, domain.NewDBTargetMissing(environmentID)
		}
		target = *stored
	}

	if err := domain.ValidateDBTarget(&target); err != nil {
		return domain.DBTargetTestResult{}, err
	}
	if s.prober == nil {
		return domain.DBTargetTestResult{}, errors.New("application: db target prober is not configured")
	}
	result, err := s.prober.TestConnection(ctx, target)
	if err != nil {
		return domain.DBTargetTestResult{}, err
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	if override == nil {
		status := domain.DBTargetTestStatusOK
		message := ""
		if !result.OK {
			status = domain.DBTargetTestStatusFailed
			if result.Error != nil {
				message = result.Error.Message
			}
		}
		testedAt := s.clock.Now()
		if err := s.targets.UpdateDBTargetTestResult(ctx, stored.ID, testedAt, status, message); err != nil {
			return result, err
		}
	}
	return result, nil
}

// checkEnvironment maps a missing environment onto db_target_invalid, the 422
// of spec §7.11 for "配置字段非法/环境缺失".
func (s *DBTargetService) checkEnvironment(ctx context.Context, environmentID int64) error {
	if _, err := s.envs.GetEnvironment(ctx, environmentID); err != nil {
		if isNotFound(err) {
			return domain.NewInvalid("environmentId", domain.ReasonDBTargetInvalid, "environment does not exist")
		}
		return err
	}
	return nil
}

// applyDBTargetCommand merges cmd onto target and fills the DDL defaults for a
// new row. creating controls the enabled default only: the other optional fields
// keep their stored value when the request omits them.
func applyDBTargetCommand(target *domain.DBTarget, cmd UpsertDBTargetCommand, creating bool) {
	target.Name = cmd.Name
	target.Host = strings.TrimSpace(cmd.Host)
	target.Port = cmd.Port
	target.DatabaseName = strings.TrimSpace(cmd.Database)
	target.Username = strings.TrimSpace(cmd.Username)
	if cmd.Password != nil {
		target.Password = *cmd.Password
	}
	if cmd.SSLMode != nil {
		target.SSLMode = *cmd.SSLMode
	} else if creating {
		target.SSLMode = domain.DBTargetSSLModeDisable
	}
	if cmd.ConnectTimeoutMS != nil {
		target.ConnectTimeoutMS = *cmd.ConnectTimeoutMS
	} else if creating {
		target.ConnectTimeoutMS = domain.DBTargetDefaultConnectTimeoutMS
	}
	if cmd.StatementTimeoutMS != nil {
		target.StatementTimeoutMS = *cmd.StatementTimeoutMS
	} else if creating {
		target.StatementTimeoutMS = domain.DBTargetDefaultStatementTimeoutMS
	}
	if cmd.MaxRows != nil {
		target.MaxRows = *cmd.MaxRows
	} else if creating {
		target.MaxRows = domain.DBTargetDefaultMaxRows
	}
	if cmd.Enabled != nil {
		target.Enabled = *cmd.Enabled
	} else if creating {
		target.Enabled = true
	}
}
