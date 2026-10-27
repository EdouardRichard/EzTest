package application_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// svcCallLog records cross-fake call order for the deletion test.
type svcCallLog struct {
	mu    sync.Mutex
	calls []string
}

// add appends one call name.
func (l *svcCallLog) add(call string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

// snapshot returns a copy of the recorded calls.
func (l *svcCallLog) snapshot() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// svcFakeSessionRepository is an in-memory domain.SessionRepository with
// programmable list/create/get/title/delete behavior.
type svcFakeSessionRepository struct {
	log *svcCallLog

	summaries []domain.SessionSummary
	hasMore   bool
	listErr   error
	listCalls int
	cursor    int64
	limit     int
	profileID *int64

	getSession domain.Session
	getErr     error

	created          domain.Session
	createErr        error
	createCalls      int
	createdProfileID int64
	createdTitle     string
	createdBy        *int64

	createWithOptionsCalls int
	createdChannel         string
	createdVersionID       *string

	updateTitleSession domain.Session
	updateTitleErr     error
	updateTitleID      int64
	updateTitleValue   string

	deleteErr   error
	deleteCalls int
	deletedID   int64
}

// List records the pagination arguments and returns the configured page.
func (r *svcFakeSessionRepository) List(_ context.Context, cursor int64, limit int, profileID *int64) ([]domain.SessionSummary, bool, error) {
	r.listCalls++
	r.cursor = cursor
	r.limit = limit
	r.profileID = profileID
	if r.listErr != nil {
		return nil, false, r.listErr
	}
	return r.summaries, r.hasMore, nil
}

// Create records the arguments and returns the configured session.
func (r *svcFakeSessionRepository) Create(_ context.Context, profileID int64, title string, createdBy *int64) (domain.Session, error) {
	r.createCalls++
	r.createdProfileID = profileID
	r.createdTitle = title
	r.createdBy = createdBy
	if r.createErr != nil {
		return domain.Session{}, r.createErr
	}
	return r.created, nil
}

// CreateWithOptions records the P3 channel stamp. The service reaches it
// through its optional repository extension.
func (r *svcFakeSessionRepository) CreateWithOptions(ctx context.Context, profileID int64, title string, createdBy *int64, channel string, versionID *string) (domain.Session, error) {
	r.createWithOptionsCalls++
	r.createdChannel = channel
	r.createdVersionID = versionID
	return r.Create(ctx, profileID, title, createdBy)
}

// Get returns the configured session or ErrNotFound.
func (r *svcFakeSessionRepository) Get(context.Context, int64) (domain.Session, error) {
	if r.getErr != nil {
		return domain.Session{}, r.getErr
	}
	if r.getSession.ID == 0 {
		return domain.Session{}, domain.ErrNotFound
	}
	return r.getSession, nil
}

// UpdateTitle records the arguments and returns the configured session.
func (r *svcFakeSessionRepository) UpdateTitle(_ context.Context, id int64, title string) (domain.Session, error) {
	r.updateTitleID = id
	r.updateTitleValue = title
	if r.updateTitleErr != nil {
		return domain.Session{}, r.updateTitleErr
	}
	return r.updateTitleSession, nil
}

// Delete records the call. The fake does not consult getSession: the service
// must have loaded the session before deleting.
func (r *svcFakeSessionRepository) Delete(_ context.Context, id int64) error {
	r.deleteCalls++
	r.deletedID = id
	r.log.add("delete")
	if r.deleteErr != nil {
		return r.deleteErr
	}
	return nil
}

// svcFakeRunRepository records the two lookups SessionService.Get needs.
type svcFakeRunRepository struct {
	active    domain.Run
	activeErr error
	latest    domain.Run
	latestErr error

	activeCalls int
	latestCalls int
}

// CreateTurn is unused by SessionService.
func (r *svcFakeRunRepository) CreateTurn(context.Context, int64, []domain.Part, string) (domain.TurnIDs, error) {
	return domain.TurnIDs{}, nil
}

// Get is unused by SessionService.
func (r *svcFakeRunRepository) Get(context.Context, int64) (domain.Run, error) {
	return domain.Run{}, domain.ErrNotFound
}

// Active returns the configured active run.
func (r *svcFakeRunRepository) Active(context.Context, int64) (domain.Run, error) {
	r.activeCalls++
	if r.activeErr != nil {
		return domain.Run{}, r.activeErr
	}
	return r.active, nil
}

// Latest returns the configured newest terminal run.
func (r *svcFakeRunRepository) Latest(context.Context, int64) (domain.Run, error) {
	r.latestCalls++
	if r.latestErr != nil {
		return domain.Run{}, r.latestErr
	}
	return r.latest, nil
}

// UpdateProgress is unused by SessionService.
func (r *svcFakeRunRepository) UpdateProgress(context.Context, int64, int, domain.Usage) error {
	return nil
}

// Finish is unused by SessionService.
func (r *svcFakeRunRepository) Finish(context.Context, int64, domain.RunResult) error {
	return nil
}

// MarkRunningInterrupted is unused by SessionService.
func (r *svcFakeRunRepository) MarkRunningInterrupted(context.Context) (int64, error) {
	return 0, nil
}

// svcFakeMessageRepository returns one configured message.
type svcFakeMessageRepository struct {
	message domain.Message
	getErr  error
}

// ListBySession is unused by SessionService.
func (r *svcFakeMessageRepository) ListBySession(context.Context, int64) ([]domain.Message, error) {
	return nil, nil
}

// Get returns the configured message.
func (r *svcFakeMessageRepository) Get(context.Context, int64) (domain.Message, error) {
	if r.getErr != nil {
		return domain.Message{}, r.getErr
	}
	return r.message, nil
}

// Append is unused by SessionService.
func (r *svcFakeMessageRepository) Append(context.Context, domain.Message) (domain.Message, error) {
	return domain.Message{}, nil
}

// AppendAssistantPlaceholder is unused by SessionService.
func (r *svcFakeMessageRepository) AppendAssistantPlaceholder(context.Context, int64, int64) (int64, error) {
	return 0, nil
}

// CompleteAssistant is unused by SessionService.
func (r *svcFakeMessageRepository) CompleteAssistant(context.Context, int64, []domain.Part, string, []domain.ToolCall, *domain.Usage) error {
	return nil
}

// svcFakeRunController records the abort/idle calls of the deletion flow.
type svcFakeRunController struct {
	log *svcCallLog

	startCalls int
	abortCalls int
	waitCalls  int

	abortResult domain.AbortResult
	abortErr    error
	waitIdle    bool
	waitTimeout time.Duration
}

// Start is unused by SessionService.
func (c *svcFakeRunController) Start(context.Context, int64, string, *int64) (domain.StartResult, error) {
	c.startCalls++
	return domain.StartResult{}, nil
}

// Abort records the call and returns the configured result.
func (c *svcFakeRunController) Abort(context.Context, int64, *int64) (domain.AbortResult, error) {
	c.abortCalls++
	c.log.add("abort")
	return c.abortResult, c.abortErr
}

// WaitIdle records the call and returns the configured idleness.
func (c *svcFakeRunController) WaitIdle(_ context.Context, _ int64, timeout time.Duration) bool {
	c.waitCalls++
	c.log.add("wait")
	c.waitTimeout = timeout
	return c.waitIdle
}

// svcTestSessionService wires the session service with the given fakes.
func svcTestSessionService(
	profiles *svcFakeProfileRepository,
	sessions *svcFakeSessionRepository,
	runs *svcFakeRunRepository,
	messages *svcFakeMessageRepository,
	control *svcFakeRunController,
	audit *svcFakeAuditSink,
) *application.SessionService {
	return application.NewSessionService(profiles, sessions, runs, messages, control, audit)
}

func TestSessionListUsesCursorAndLimit(t *testing.T) {
	summaries := []domain.SessionSummary{
		{Session: domain.Session{ID: 9, ProfileKey: "runtime-diagnostic"}},
		{Session: domain.Session{ID: 8, ProfileKey: "runtime-diagnostic"}},
	}
	sessions := &svcFakeSessionRepository{summaries: summaries, hasMore: true}
	svc := svcTestSessionService(
		&svcFakeProfileRepository{},
		sessions,
		&svcFakeRunRepository{},
		&svcFakeMessageRepository{},
		&svcFakeRunController{},
		&svcFakeAuditSink{},
	)
	profileID := int64(3)

	got, hasMore, err := svc.List(context.Background(), 10, 2, &profileID)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if sessions.listCalls != 1 {
		t.Fatalf("repository List called %d times, want 1", sessions.listCalls)
	}
	if sessions.cursor != 10 {
		t.Fatalf("repository cursor=%d want 10 (the decoded cursor must pass through)", sessions.cursor)
	}
	if sessions.limit != 2 {
		t.Fatalf("repository limit=%d want 2 (the handler limit must pass through)", sessions.limit)
	}
	if sessions.profileID == nil || *sessions.profileID != 3 {
		t.Fatalf("repository profileID=%v want 3", sessions.profileID)
	}
	if len(got) != 2 || !hasMore {
		t.Fatalf("List returned len=%d hasMore=%v want 2,true", len(got), hasMore)
	}
}

func TestSessionDeleteAbortsThenDeletes(t *testing.T) {
	actor := int64(5)

	t.Run("an idle run is deleted and audited after abort and wait", func(t *testing.T) {
		log := &svcCallLog{}
		sessions := &svcFakeSessionRepository{
			log:        log,
			getSession: domain.Session{ID: 4},
		}
		control := &svcFakeRunController{
			log:         log,
			abortResult: domain.AbortResult{Status: "stopping"},
			waitIdle:    true,
		}
		audit := &svcFakeAuditSink{}
		svc := svcTestSessionService(&svcFakeProfileRepository{}, sessions,
			&svcFakeRunRepository{}, &svcFakeMessageRepository{}, control, audit)

		if err := svc.Delete(context.Background(), 4, &actor); err != nil {
			t.Fatalf("Delete returned error: %v", err)
		}
		if control.abortCalls != 1 {
			t.Fatalf("Abort called %d times, want 1", control.abortCalls)
		}
		if control.waitCalls != 1 {
			t.Fatalf("WaitIdle called %d times, want 1", control.waitCalls)
		}
		if control.waitTimeout != 5*time.Second {
			t.Fatalf("WaitIdle timeout=%v want 5s", control.waitTimeout)
		}
		if sessions.deleteCalls != 1 || sessions.deletedID != 4 {
			t.Fatalf("repository Delete calls=%d id=%d want 1/4", sessions.deleteCalls, sessions.deletedID)
		}
		calls := log.snapshot()
		want := []string{"abort", "wait", "delete"}
		if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] || calls[2] != want[2] {
			t.Fatalf("call order=%v want %v", calls, want)
		}

		events := audit.recorded()
		if len(events) != 1 {
			t.Fatalf("audit recorded %d events, want 1", len(events))
		}
		if events[0].Action != "agent.session.deleted" {
			t.Fatalf("audit action=%q want agent.session.deleted", events[0].Action)
		}
		if got := events[0].Metadata["session_id"]; got != int64(4) {
			t.Fatalf("audit metadata[session_id]=%v want 4", got)
		}
		if events[0].ActorUserID == nil || *events[0].ActorUserID != actor {
			t.Fatalf("audit actor=%v want %d", events[0].ActorUserID, actor)
		}
	})

	t.Run("a run that does not stop within the deadline keeps the session", func(t *testing.T) {
		log := &svcCallLog{}
		sessions := &svcFakeSessionRepository{
			log:        log,
			getSession: domain.Session{ID: 4},
		}
		control := &svcFakeRunController{
			log:         log,
			abortResult: domain.AbortResult{Status: "stopping"},
			waitIdle:    false,
		}
		audit := &svcFakeAuditSink{}
		svc := svcTestSessionService(&svcFakeProfileRepository{}, sessions,
			&svcFakeRunRepository{}, &svcFakeMessageRepository{}, control, audit)

		if err := svc.Delete(context.Background(), 4, &actor); !errors.Is(err, domain.ErrRunActive) {
			t.Fatalf("Delete error=%v want domain.ErrRunActive", err)
		}
		if sessions.deleteCalls != 0 {
			t.Fatalf("repository Delete called %d times, want 0", sessions.deleteCalls)
		}
		if events := audit.recorded(); len(events) != 0 {
			t.Fatalf("audit recorded %d events, want 0", len(events))
		}
	})

	t.Run("missing session is reported before aborting", func(t *testing.T) {
		control := &svcFakeRunController{waitIdle: true}
		svc := svcTestSessionService(&svcFakeProfileRepository{}, &svcFakeSessionRepository{},
			&svcFakeRunRepository{}, &svcFakeMessageRepository{}, control, &svcFakeAuditSink{})

		if err := svc.Delete(context.Background(), 404, nil); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Delete(missing) error=%v want domain.ErrNotFound", err)
		}
		if control.abortCalls != 0 {
			t.Fatalf("Abort called %d times for a missing session, want 0", control.abortCalls)
		}
	})
}

func TestSessionCreateDefaultsProfile(t *testing.T) {
	builtin := svcTestProfile(2)
	other := svcTestProfile(1)
	other.Key = "other-profile"
	other.Builtin = false
	profiles := &svcFakeProfileRepository{profiles: []domain.AgentProfile{other, builtin}}

	t.Run("profile id zero selects the builtin profile", func(t *testing.T) {
		sessions := &svcFakeSessionRepository{created: domain.Session{ID: 11, ProfileID: 2}}
		svc := svcTestSessionService(profiles, sessions, &svcFakeRunRepository{},
			&svcFakeMessageRepository{}, &svcFakeRunController{}, &svcFakeAuditSink{})
		actor := int64(1)

		session, err := svc.Create(context.Background(), 0, "检查运行时健康", &actor)
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		if session.ID != 11 {
			t.Fatalf("Create returned session %+v, want the repository result", session)
		}
		if sessions.createdProfileID != 2 {
			t.Fatalf("repository profile_id=%d want 2 (the builtin profile)", sessions.createdProfileID)
		}
		if sessions.createdTitle != "检查运行时健康" {
			t.Fatalf("repository title=%q want the requested title", sessions.createdTitle)
		}
		if sessions.createdBy == nil || *sessions.createdBy != actor {
			t.Fatalf("repository created_by=%v want %d", sessions.createdBy, actor)
		}
	})

	t.Run("explicit profile id is used as is", func(t *testing.T) {
		sessions := &svcFakeSessionRepository{created: domain.Session{ID: 12, ProfileID: 1}}
		svc := svcTestSessionService(profiles, sessions, &svcFakeRunRepository{},
			&svcFakeMessageRepository{}, &svcFakeRunController{}, &svcFakeAuditSink{})

		if _, err := svc.Create(context.Background(), 1, "", nil); err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		if sessions.createdProfileID != 1 {
			t.Fatalf("repository profile_id=%d want 1", sessions.createdProfileID)
		}
	})

	t.Run("disabled profile is rejected", func(t *testing.T) {
		disabled := builtin
		disabled.Enabled = false
		disabledProfiles := &svcFakeProfileRepository{profiles: []domain.AgentProfile{disabled}}
		sessions := &svcFakeSessionRepository{}
		svc := svcTestSessionService(disabledProfiles, sessions, &svcFakeRunRepository{},
			&svcFakeMessageRepository{}, &svcFakeRunController{}, &svcFakeAuditSink{})

		if _, err := svc.Create(context.Background(), 2, "", nil); !errors.Is(err, domain.ErrProfileDisabled) {
			t.Fatalf("Create(disabled) error=%v want domain.ErrProfileDisabled", err)
		}
		if sessions.createCalls != 0 {
			t.Fatalf("repository Create called %d times, want 0", sessions.createCalls)
		}
	})

	t.Run("missing profile is reported as not found", func(t *testing.T) {
		sessions := &svcFakeSessionRepository{}
		svc := svcTestSessionService(profiles, sessions, &svcFakeRunRepository{},
			&svcFakeMessageRepository{}, &svcFakeRunController{}, &svcFakeAuditSink{})

		if _, err := svc.Create(context.Background(), 99, "", nil); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Create(missing) error=%v want domain.ErrNotFound", err)
		}
		if sessions.createCalls != 0 {
			t.Fatalf("repository Create called %d times, want 0", sessions.createCalls)
		}
	})

	t.Run("title longer than 120 characters is rejected", func(t *testing.T) {
		sessions := &svcFakeSessionRepository{}
		svc := svcTestSessionService(profiles, sessions, &svcFakeRunRepository{},
			&svcFakeMessageRepository{}, &svcFakeRunController{}, &svcFakeAuditSink{})

		title := strings.Repeat("题", 121)
		if _, err := svc.Create(context.Background(), 0, title, nil); err == nil {
			t.Fatal("Create(overlong title) returned nil error, want a validation error")
		} else if !strings.Contains(err.Error(), "title") {
			t.Fatalf("Create error=%q, want it to name title", err)
		}
		if sessions.createCalls != 0 {
			t.Fatalf("repository Create called %d times, want 0", sessions.createCalls)
		}
	})
}

// TestCreateWithOptionsPersistsChannel pins the P3 session stamp: Create keeps
// the P1 empty channel, while CreateWithOptions forwards the version channel
// and version id to the repository.
func TestCreateWithOptionsPersistsChannel(t *testing.T) {
	builtin := svcTestProfile(2)
	profiles := &svcFakeProfileRepository{profiles: []domain.AgentProfile{builtin}}

	t.Run("P1 Create keeps the default empty channel", func(t *testing.T) {
		sessions := &svcFakeSessionRepository{created: domain.Session{ID: 11, ProfileID: 2}}
		svc := svcTestSessionService(profiles, sessions, &svcFakeRunRepository{},
			&svcFakeMessageRepository{}, &svcFakeRunController{}, &svcFakeAuditSink{})

		if _, err := svc.Create(context.Background(), 0, "检查运行时健康", nil); err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		if sessions.createWithOptionsCalls != 1 {
			t.Fatalf("repository CreateWithOptions calls=%d want 1 (Create must delegate)", sessions.createWithOptionsCalls)
		}
		if sessions.createdChannel != "" {
			t.Fatalf("repository activity_channel=%q want the empty P1 default", sessions.createdChannel)
		}
		if sessions.createdVersionID != nil {
			t.Fatalf("repository activity_version_id=%v want nil", sessions.createdVersionID)
		}
	})

	t.Run("P3 CreateWithOptions persists the version channel and version id", func(t *testing.T) {
		versionID := "42"
		sessions := &svcFakeSessionRepository{created: domain.Session{ID: 12, ProfileID: 2}}
		svc := svcTestSessionService(profiles, sessions, &svcFakeRunRepository{},
			&svcFakeMessageRepository{}, &svcFakeRunController{}, &svcFakeAuditSink{})
		actor := int64(3)

		session, err := svc.CreateWithOptions(context.Background(), 0, "需求评审", &actor, application.SessionOptions{
			ActivityChannel:   "version:42",
			ActivityVersionID: &versionID,
		})
		if err != nil {
			t.Fatalf("CreateWithOptions returned error: %v", err)
		}
		if session.ID != 12 {
			t.Fatalf("CreateWithOptions returned session %+v want the repository result", session)
		}
		if sessions.createdChannel != "version:42" {
			t.Fatalf("repository activity_channel=%q want version:42", sessions.createdChannel)
		}
		if sessions.createdVersionID == nil || *sessions.createdVersionID != versionID {
			t.Fatalf("repository activity_version_id=%v want %q", sessions.createdVersionID, versionID)
		}
		if sessions.createdProfileID != 2 || sessions.createdTitle != "需求评审" {
			t.Fatalf("repository profile/title=%d/%q want 2/需求评审", sessions.createdProfileID, sessions.createdTitle)
		}
		if sessions.createdBy == nil || *sessions.createdBy != actor {
			t.Fatalf("repository created_by=%v want %d", sessions.createdBy, actor)
		}
	})
}

func TestSessionGetAssemblesActiveAndLastRun(t *testing.T) {
	session := domain.Session{ID: 4, ProfileID: 2, ProfileKey: "runtime-diagnostic"}
	active := domain.Run{ID: 9, SessionID: 4, Status: domain.RunRunning, Turns: 2}
	last := domain.Run{ID: 8, SessionID: 4, Status: domain.RunDone, TerminalReason: domain.ReasonCompleted}

	t.Run("detail carries the running and the newest terminal run", func(t *testing.T) {
		runs := &svcFakeRunRepository{active: active, latest: last}
		sessions := &svcFakeSessionRepository{getSession: session}
		svc := svcTestSessionService(&svcFakeProfileRepository{}, sessions, runs,
			&svcFakeMessageRepository{}, &svcFakeRunController{}, &svcFakeAuditSink{})

		detail, err := svc.Get(context.Background(), 4)
		if err != nil {
			t.Fatalf("Get returned error: %v", err)
		}
		if !detail.Running {
			t.Fatal("detail.Running=false want true")
		}
		if detail.ActiveRun == nil || detail.ActiveRun.ID != 9 {
			t.Fatalf("detail.ActiveRun=%+v want run 9", detail.ActiveRun)
		}
		if detail.LastRun == nil || detail.LastRun.ID != 8 {
			t.Fatalf("detail.LastRun=%+v want run 8", detail.LastRun)
		}
		if runs.activeCalls != 1 || runs.latestCalls != 1 {
			t.Fatalf("run lookups active=%d latest=%d want 1/1", runs.activeCalls, runs.latestCalls)
		}
	})

	t.Run("a session without runs has neither", func(t *testing.T) {
		runs := &svcFakeRunRepository{activeErr: domain.ErrNotFound, latestErr: domain.ErrNotFound}
		sessions := &svcFakeSessionRepository{getSession: session}
		svc := svcTestSessionService(&svcFakeProfileRepository{}, sessions, runs,
			&svcFakeMessageRepository{}, &svcFakeRunController{}, &svcFakeAuditSink{})

		detail, err := svc.Get(context.Background(), 4)
		if err != nil {
			t.Fatalf("Get returned error: %v", err)
		}
		if detail.Running || detail.ActiveRun != nil || detail.LastRun != nil {
			t.Fatalf("detail=%+v want no runs", detail)
		}
	})
}

func TestSessionMessageRejectsMessageFromAnotherSession(t *testing.T) {
	message := domain.Message{ID: 42, SessionID: 2, Role: "assistant"}

	t.Run("a message from another session is not found", func(t *testing.T) {
		svc := svcTestSessionService(&svcFakeProfileRepository{}, &svcFakeSessionRepository{},
			&svcFakeRunRepository{}, &svcFakeMessageRepository{message: message},
			&svcFakeRunController{}, &svcFakeAuditSink{})

		if _, err := svc.Message(context.Background(), 1, 42); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Message(other session) error=%v want domain.ErrNotFound", err)
		}
	})

	t.Run("a message from the requested session is returned", func(t *testing.T) {
		svc := svcTestSessionService(&svcFakeProfileRepository{}, &svcFakeSessionRepository{},
			&svcFakeRunRepository{}, &svcFakeMessageRepository{message: message},
			&svcFakeRunController{}, &svcFakeAuditSink{})

		got, err := svc.Message(context.Background(), 2, 42)
		if err != nil {
			t.Fatalf("Message returned error: %v", err)
		}
		if got.ID != 42 {
			t.Fatalf("Message returned %+v want message 42", got)
		}
	})
}

func TestSessionUpdateTitleRejectsTooLongTitle(t *testing.T) {
	sessions := &svcFakeSessionRepository{}
	svc := svcTestSessionService(&svcFakeProfileRepository{}, sessions,
		&svcFakeRunRepository{}, &svcFakeMessageRepository{}, &svcFakeRunController{}, &svcFakeAuditSink{})

	if _, err := svc.UpdateTitle(context.Background(), 1, strings.Repeat("题", 121)); err == nil {
		t.Fatal("UpdateTitle(121 runes) returned nil error, want a validation error")
	}
	if sessions.updateTitleID != 0 {
		t.Fatalf("repository UpdateTitle called for an invalid title (id=%d)", sessions.updateTitleID)
	}
}
