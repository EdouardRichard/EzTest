package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// dbTargetTestClock is a fixed clock for the db target service tests.
type dbTargetTestClock struct {
	now time.Time
}

// Now returns the fixed instant.
func (c dbTargetTestClock) Now() time.Time { return c.now }

// dbTargetTestIDs is a deterministic id generator.
type dbTargetTestIDs struct {
	next int64
}

// Next returns the next id.
func (g *dbTargetTestIDs) Next() int64 {
	g.next++
	return g.next
}

// dbTargetTestEnvRepo is an in-memory domain.EnvironmentRepository.
type dbTargetTestEnvRepo struct {
	environments map[int64]domain.Environment
}

// newDBTargetTestEnvRepo seeds one environment.
func newDBTargetTestEnvRepo() *dbTargetTestEnvRepo {
	return &dbTargetTestEnvRepo{environments: map[int64]domain.Environment{
		7: {ID: 7, SystemID: 1, Key: "staging", Name: "Staging", Kind: domain.EnvironmentKindStaging},
	}}
}

func (r *dbTargetTestEnvRepo) CreateEnvironment(context.Context, *domain.Environment) error {
	return errors.New("not implemented")
}

func (r *dbTargetTestEnvRepo) GetEnvironment(_ context.Context, id int64) (*domain.Environment, error) {
	environment, ok := r.environments[id]
	if !ok {
		return nil, domain.NewNotFound("environment", id)
	}
	return &environment, nil
}

func (r *dbTargetTestEnvRepo) ListEnvironments(context.Context, int64) ([]domain.Environment, error) {
	return nil, errors.New("not implemented")
}

func (r *dbTargetTestEnvRepo) UpdateEnvironment(context.Context, *domain.Environment) error {
	return errors.New("not implemented")
}

func (r *dbTargetTestEnvRepo) DeleteEnvironment(context.Context, int64) error {
	return errors.New("not implemented")
}

func (r *dbTargetTestEnvRepo) CountSessions(context.Context, int64) (int, error) {
	return 0, nil
}

// dbTargetTestRepo is an in-memory DBTargetRepository.
type dbTargetTestRepo struct {
	targets     map[int64]domain.DBTarget
	upsertCalls int
	deleteCalls int
	testUpdates []dbTargetTestUpdate
	upsertErr   error
}

// dbTargetTestUpdate captures one last_test_* write.
type dbTargetTestUpdate struct {
	targetID int64
	testedAt time.Time
	status   string
	message  string
}

func newDBTargetTestRepo() *dbTargetTestRepo {
	return &dbTargetTestRepo{targets: map[int64]domain.DBTarget{}}
}

func (r *dbTargetTestRepo) GetDBTarget(_ context.Context, environmentID int64) (*domain.DBTarget, error) {
	target, ok := r.targets[environmentID]
	if !ok {
		return nil, nil
	}
	copied := target
	return &copied, nil
}

func (r *dbTargetTestRepo) UpsertDBTarget(_ context.Context, target *domain.DBTarget) error {
	r.upsertCalls++
	if r.upsertErr != nil {
		return r.upsertErr
	}
	r.targets[target.EnvironmentID] = *target
	return nil
}

func (r *dbTargetTestRepo) UpdateDBTargetTestResult(_ context.Context, targetID int64, testedAt time.Time, status, message string) error {
	r.testUpdates = append(r.testUpdates, dbTargetTestUpdate{targetID: targetID, testedAt: testedAt, status: status, message: message})
	for environmentID, target := range r.targets {
		if target.ID != targetID {
			continue
		}
		target.LastTestedAt = &testedAt
		target.LastTestStatus = status
		target.LastTestMessage = message
		r.targets[environmentID] = target
	}
	return nil
}

func (r *dbTargetTestRepo) DeleteDBTarget(_ context.Context, environmentID int64) error {
	r.deleteCalls++
	delete(r.targets, environmentID)
	return nil
}

// dbTargetTestProber is a scripted DBTargetProber.
type dbTargetTestProber struct {
	calls    int
	last     domain.DBTarget
	result   domain.DBTargetTestResult
	err      error
	onTarget func(domain.DBTarget)
}

func (p *dbTargetTestProber) TestConnection(_ context.Context, target domain.DBTarget) (domain.DBTargetTestResult, error) {
	p.calls++
	p.last = target
	if p.onTarget != nil {
		p.onTarget(target)
	}
	return p.result, p.err
}

// dbTargetFixture bundles the service and its fakes.
type dbTargetFixture struct {
	service *DBTargetService
	envs    *dbTargetTestEnvRepo
	targets *dbTargetTestRepo
	prober  *dbTargetTestProber
	ids     *dbTargetTestIDs
	now     time.Time
}

// newDBTargetFixture builds a service over the in-memory fakes.
func newDBTargetFixture() *dbTargetFixture {
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	f := &dbTargetFixture{
		envs:    newDBTargetTestEnvRepo(),
		targets: newDBTargetTestRepo(),
		prober:  &dbTargetTestProber{result: domain.DBTargetTestResult{OK: true}},
		ids:     &dbTargetTestIDs{},
		now:     now,
	}
	return f
}

// build constructs the service (separated so tests can mutate fakes first).
func (f *dbTargetFixture) build() *DBTargetService {
	f.service = NewDBTargetService(f.targets, f.envs, f.prober, f.ids, dbTargetTestClock{now: f.now})
	return f.service
}

// fullCommand returns a create/update command with every optional field set.
func fullDBTargetCommand(password string) UpsertDBTargetCommand {
	sslMode := domain.DBTargetSSLModeRequire
	enabled := true
	cmd := UpsertDBTargetCommand{
		Name:               "orders",
		Host:               "127.0.0.1",
		Port:               5432,
		Database:           "eztest_target",
		Username:           "eztest_ro",
		SSLMode:            &sslMode,
		ConnectTimeoutMS:   intPointer(2000),
		StatementTimeoutMS: intPointer(4000),
		MaxRows:            intPointer(50),
		Enabled:            &enabled,
	}
	if password != "" {
		cmd.Password = &password
	}
	return cmd
}

// intPointer returns a pointer to value.
func intPointer[T any](value T) *T { return &value }

// seed stores one valid target for environment 7.
func (f *dbTargetFixture) seed() domain.DBTarget {
	target := domain.DBTarget{
		ID: 100, EnvironmentID: 7, Name: "orders", Host: "db.internal", Port: 5433,
		DatabaseName: "orders_ro", Username: "ro", Password: "stored-secret",
		SSLMode: domain.DBTargetSSLModeRequire, ConnectTimeoutMS: 1500,
		StatementTimeoutMS: 2500, MaxRows: 25, Enabled: true,
		CreatedBy: 11, UpdatedBy: 11, CreatedAt: f.now.Add(-time.Hour), UpdatedAt: f.now.Add(-time.Hour),
	}
	f.targets.targets[7] = target
	return target
}

// assertDomainReason asserts a *domain.Error with kind and reason.
func assertDomainReason(t *testing.T, err error, kind domain.ErrorKind, reason string) *domain.Error {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("err=%v (%T), want *domain.Error", err, err)
	}
	if domainErr.Kind != kind || domainErr.Reason != reason {
		t.Fatalf("kind=%q reason=%q want %q/%q", domainErr.Kind, domainErr.Reason, kind, reason)
	}
	return domainErr
}

func TestDBTargetServiceGetMissing(t *testing.T) {
	f := newDBTargetFixture()
	service := f.build()
	target, err := service.Get(context.Background(), 7)
	if err != nil || target != nil {
		t.Fatalf("Get missing = (%v, %v), want (nil, nil)", target, err)
	}
}

func TestDBTargetServiceGetReturnsStoredPasswordInternally(t *testing.T) {
	f := newDBTargetFixture()
	f.seed()
	service := f.build()
	target, err := service.Get(context.Background(), 7)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if target == nil || target.Password != "stored-secret" {
		t.Fatalf("target=%+v want the stored password for internal use", target)
	}
}

func TestDBTargetServiceUpsertCreatesWithDefaults(t *testing.T) {
	f := newDBTargetFixture()
	service := f.build()
	cmd := UpsertDBTargetCommand{
		Host:     " 127.0.0.1 ",
		Port:     5432,
		Database: " eztest_target ",
		Username: " eztest_ro ",
		Password: strPointer("s3cret"),
	}
	target, err := service.Upsert(context.Background(), 7, 42, cmd)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if target.ID <= 0 || target.EnvironmentID != 7 {
		t.Fatalf("target=%+v want a generated id and the environment", target)
	}
	if target.Host != "127.0.0.1" || target.DatabaseName != "eztest_target" || target.Username != "eztest_ro" {
		t.Fatalf("target=%+v want trimmed connection fields", target)
	}
	if target.SSLMode != domain.DBTargetSSLModeDisable ||
		target.ConnectTimeoutMS != domain.DBTargetDefaultConnectTimeoutMS ||
		target.StatementTimeoutMS != domain.DBTargetDefaultStatementTimeoutMS ||
		target.MaxRows != domain.DBTargetDefaultMaxRows || !target.Enabled {
		t.Fatalf("target=%+v want the DDL defaults", target)
	}
	if target.Password != "s3cret" {
		t.Fatal("stored target lost the password")
	}
	if target.CreatedBy != 42 || target.UpdatedBy != 42 {
		t.Fatalf("createdBy/updatedBy=%d/%d want 42/42", target.CreatedBy, target.UpdatedBy)
	}
	if f.targets.upsertCalls != 1 {
		t.Fatalf("upsert calls=%d want 1", f.targets.upsertCalls)
	}
}

func TestDBTargetServiceUpsertFirstPasswordRequired(t *testing.T) {
	f := newDBTargetFixture()
	service := f.build()
	_, err := service.Upsert(context.Background(), 7, 42, UpsertDBTargetCommand{
		Host: "127.0.0.1", Port: 5432, Database: "db", Username: "ro",
	})
	assertDomainReason(t, err, domain.KindInvalid, domain.ReasonDBTargetInvalid)
	if f.targets.upsertCalls != 0 {
		t.Fatal("invalid create reached the repository")
	}
}

func TestDBTargetServiceUpsertUpdateKeepsOmittedFields(t *testing.T) {
	f := newDBTargetFixture()
	seeded := f.seed()
	service := f.build()
	target, err := service.Upsert(context.Background(), 7, 43, UpsertDBTargetCommand{
		Host: "db2.internal", Port: 5434, Database: "orders_ro2", Username: "ro2",
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if target.ID != seeded.ID {
		t.Fatalf("id=%d want the stored id %d", target.ID, seeded.ID)
	}
	if target.Password != "stored-secret" {
		t.Fatal("an omitted password must keep the stored value")
	}
	if target.SSLMode != seeded.SSLMode || target.ConnectTimeoutMS != seeded.ConnectTimeoutMS ||
		target.StatementTimeoutMS != seeded.StatementTimeoutMS || target.MaxRows != seeded.MaxRows ||
		!target.Enabled {
		t.Fatalf("target=%+v want omitted options preserved", target)
	}
	if target.UpdatedBy != 43 {
		t.Fatalf("updatedBy=%d want 43", target.UpdatedBy)
	}
}

func TestDBTargetServiceUpsertUpdateReplacesPassword(t *testing.T) {
	f := newDBTargetFixture()
	f.seed()
	service := f.build()
	target, err := service.Upsert(context.Background(), 7, 43, UpsertDBTargetCommand{
		Host: "db.internal", Port: 5433, Database: "orders_ro", Username: "ro",
		Password: strPointer("new-secret"), Enabled: boolPointer(false),
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if target.Password != "new-secret" || target.Enabled {
		t.Fatalf("target=%+v want the replaced password and disabled flag", target)
	}
}

func TestDBTargetServiceUpsertRejectsMissingEnvironment(t *testing.T) {
	f := newDBTargetFixture()
	service := f.build()
	_, err := service.Upsert(context.Background(), 99, 42, fullDBTargetCommand("s3cret"))
	domainErr := assertDomainReason(t, err, domain.KindInvalid, domain.ReasonDBTargetInvalid)
	if domainErr.Field != "environmentId" {
		t.Fatalf("field=%q want environmentId", domainErr.Field)
	}
	if f.targets.upsertCalls != 0 {
		t.Fatal("missing environment reached the repository")
	}
}

func TestDBTargetServiceUpsertValidatesFields(t *testing.T) {
	cases := []struct {
		name, field string
		mutate      func(*UpsertDBTargetCommand)
	}{
		{"port", "port", func(cmd *UpsertDBTargetCommand) { cmd.Port = 70000 }},
		{"host", "host", func(cmd *UpsertDBTargetCommand) { cmd.Host = " " }},
		{"database", "database", func(cmd *UpsertDBTargetCommand) { cmd.Database = "" }},
		{"username", "username", func(cmd *UpsertDBTargetCommand) { cmd.Username = "" }},
		{"sslmode", "sslmode", func(cmd *UpsertDBTargetCommand) { cmd.SSLMode = strPointer("allow") }},
		{"max rows", "maxRows", func(cmd *UpsertDBTargetCommand) { cmd.MaxRows = intPointer(0) }},
		{"connect timeout", "connectTimeoutMs", func(cmd *UpsertDBTargetCommand) { cmd.ConnectTimeoutMS = intPointer(1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDBTargetFixture()
			service := f.build()
			cmd := fullDBTargetCommand("s3cret")
			tc.mutate(&cmd)
			_, err := service.Upsert(context.Background(), 7, 42, cmd)
			domainErr := assertDomainReason(t, err, domain.KindInvalid, domain.ReasonDBTargetInvalid)
			if domainErr.Field != tc.field {
				t.Fatalf("field=%q want %q", domainErr.Field, tc.field)
			}
			if f.targets.upsertCalls != 0 {
				t.Fatal("invalid upsert reached the repository")
			}
		})
	}
}

func TestDBTargetServiceUpsertRequiresIDGenerator(t *testing.T) {
	f := newDBTargetFixture()
	f.service = NewDBTargetService(f.targets, f.envs, f.prober, nil, dbTargetTestClock{now: f.now})
	_, err := f.service.Upsert(context.Background(), 7, 42, fullDBTargetCommand("s3cret"))
	if err == nil || !strings.Contains(err.Error(), "id generator") {
		t.Fatalf("err=%v want an id generator configuration failure", err)
	}
	if f.targets.upsertCalls != 0 {
		t.Fatal("create without an id generator reached the repository")
	}
}

func TestDBTargetServiceDelete(t *testing.T) {
	f := newDBTargetFixture()
	f.seed()
	service := f.build()
	if err := service.Delete(context.Background(), 7, 42); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if f.targets.deleteCalls != 1 || len(f.targets.targets) != 0 {
		t.Fatalf("delete calls=%d targets=%d want one delete", f.targets.deleteCalls, len(f.targets.targets))
	}
}

func TestDBTargetServiceDeleteMissing(t *testing.T) {
	f := newDBTargetFixture()
	service := f.build()
	err := service.Delete(context.Background(), 7, 42)
	assertDomainReason(t, err, domain.KindConflict, domain.ReasonDBTargetMissing)
	if f.targets.deleteCalls != 0 {
		t.Fatal("missing target delete reached the repository")
	}
}

func TestDBTargetServiceTestConnectionPersistsStoredResult(t *testing.T) {
	f := newDBTargetFixture()
	seeded := f.seed()
	f.prober.result = domain.DBTargetTestResult{
		OK: true, LatencyMS: 12, ServerVersion: "PostgreSQL 17", CurrentUser: "ro",
		TransactionReadOnly: true, Warnings: []string{"role_has_write_privileges"},
	}
	service := f.build()
	result, err := service.Test(context.Background(), 7, nil)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK || result.LatencyMS != 12 || result.Warnings[0] != "role_has_write_privileges" {
		t.Fatalf("result=%+v", result)
	}
	if f.prober.last.Password != "stored-secret" || f.prober.last.Host != seeded.Host {
		t.Fatalf("prober target=%+v want the stored target", f.prober.last)
	}
	if len(f.targets.testUpdates) != 1 {
		t.Fatalf("test result writes=%d want 1", len(f.targets.testUpdates))
	}
	update := f.targets.testUpdates[0]
	if update.targetID != seeded.ID || update.status != domain.DBTargetTestStatusOK || update.message != "" ||
		!update.testedAt.Equal(f.now) {
		t.Fatalf("persisted test result=%+v", update)
	}
}

func TestDBTargetServiceTestConnectionPersistsFailure(t *testing.T) {
	f := newDBTargetFixture()
	f.seed()
	f.prober.result = domain.DBTargetTestResult{
		OK: false, LatencyMS: 5,
		Error: &domain.DBTargetTestError{Reason: domain.ReasonDBConnectionFailed, Message: "connection refused"},
	}
	service := f.build()
	result, err := service.Test(context.Background(), 7, nil)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if result.OK || result.Error == nil || result.Error.Reason != domain.ReasonDBConnectionFailed {
		t.Fatalf("result=%+v want a classified failure", result)
	}
	update := f.targets.testUpdates[0]
	if update.status != domain.DBTargetTestStatusFailed || update.message != "connection refused" {
		t.Fatalf("persisted test result=%+v", update)
	}
}

func TestDBTargetServiceTestConnectionWithOverrideDoesNotPersist(t *testing.T) {
	f := newDBTargetFixture()
	f.seed()
	f.prober.result = domain.DBTargetTestResult{OK: true}
	service := f.build()
	result, err := service.Test(context.Background(), 7, &UpsertDBTargetCommand{
		Host: "override.internal", Port: 6000, Database: "other", Username: "other_ro",
		Password: strPointer("other-secret"), Enabled: boolPointer(true),
	})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if f.prober.last.Host != "override.internal" || f.prober.last.Password != "other-secret" {
		t.Fatalf("prober target=%+v want the form override", f.prober.last)
	}
	if len(f.targets.testUpdates) != 0 {
		t.Fatal("an unsaved override must not rewrite the stored last_test_* fields")
	}
}

func TestDBTargetServiceTestConnectionRequiresTarget(t *testing.T) {
	f := newDBTargetFixture()
	service := f.build()
	_, err := service.Test(context.Background(), 7, nil)
	assertDomainReason(t, err, domain.KindConflict, domain.ReasonDBTargetMissing)
	if f.prober.calls != 0 {
		t.Fatal("missing target reached the prober")
	}
}

func TestDBTargetServiceTestConnectionOverrideFirstPasswordRequired(t *testing.T) {
	f := newDBTargetFixture()
	service := f.build()
	_, err := service.Test(context.Background(), 7, &UpsertDBTargetCommand{
		Host: "127.0.0.1", Port: 5432, Database: "db", Username: "ro",
	})
	assertDomainReason(t, err, domain.KindInvalid, domain.ReasonDBTargetInvalid)
	if f.prober.calls != 0 {
		t.Fatal("invalid override reached the prober")
	}
}

func TestDBTargetServiceTestConnectionPropagatesProberFailure(t *testing.T) {
	f := newDBTargetFixture()
	f.seed()
	f.prober.err = errors.New("prober exploded")
	service := f.build()
	_, err := service.Test(context.Background(), 7, nil)
	if err == nil || !strings.Contains(err.Error(), "prober exploded") {
		t.Fatalf("err=%v want the prober failure", err)
	}
}

// strPointer returns a pointer to a string value.
func strPointer(value string) *string { return &value }

// boolPointer returns a pointer to a bool value.
func boolPointer(value bool) *bool { return &value }
