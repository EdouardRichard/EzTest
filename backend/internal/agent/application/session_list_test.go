package application_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// channelSessionRepo extends the in-memory session repository with the P6
// channel query. Embedding the P1 fake keeps every other domain.SessionRepository
// method available, so the service sees one implementation.
type channelSessionRepo struct {
	*svcFakeSessionRepository

	sessions         map[string][]domain.Session
	listByChannelErr error
	nextID           int64

	listByChannelCalls int
	lastChannel        string
	lastCursor         int64
	lastLimit          int
}

// seedChannel appends count sessions stamped with channel, newest first.
func (r *channelSessionRepo) seedChannel(channel string, count int) []domain.Session {
	if r.sessions == nil {
		r.sessions = make(map[string][]domain.Session)
	}
	base := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		r.nextID++
		r.sessions[channel] = append(r.sessions[channel], domain.Session{
			ID:              r.nextID,
			Title:           "case run " + channel,
			ActivityChannel: channel,
			UpdatedAt:       base.Add(time.Duration(-len(r.sessions[channel])) * time.Minute),
		})
	}
	return r.sessions[channel]
}

// ListByChannel implements the P6 repository extension.
func (r *channelSessionRepo) ListByChannel(_ context.Context, channel string, beforeSeq int64, limit int) ([]domain.Session, bool, error) {
	r.listByChannelCalls++
	r.lastChannel = channel
	r.lastCursor = beforeSeq
	r.lastLimit = limit
	if r.listByChannelErr != nil {
		return nil, false, r.listByChannelErr
	}
	all := r.sessions[channel]
	start := 0
	if beforeSeq > 0 {
		for i, session := range all {
			if session.ID == beforeSeq {
				start = i + 1
				break
			}
		}
	}
	page := all[start:]
	hasMore := len(page) > limit
	if hasMore {
		page = page[:limit]
	}
	return append([]domain.Session(nil), page...), hasMore, nil
}

// newChannelSessionService returns a SessionService backed by the channel fake.
func newChannelSessionService(t *testing.T) (*application.SessionService, *channelSessionRepo) {
	t.Helper()
	repo := &channelSessionRepo{svcFakeSessionRepository: &svcFakeSessionRepository{}}
	svc := application.NewSessionService(nil, repo, nil, nil, nil, nil)
	return svc, repo
}

// TestListSessionsByActivityChannel pins the P6 session query: only sessions
// stamped with the requested activity channel are returned and the pagination
// arguments reach the repository unchanged.
func TestListSessionsByActivityChannel(t *testing.T) {
	svc, repo := newChannelSessionService(t)
	repo.seedChannel("version:9", 2)
	repo.seedChannel("version:8", 1)

	got, more, err := svc.ListByChannel(context.Background(), "version:9", 0, 10)
	if err != nil {
		t.Fatalf("ListByChannel: %v", err)
	}
	if len(got) != 2 || more {
		t.Fatalf("sessions=%d has_more=%v want 2/false", len(got), more)
	}
	if repo.lastChannel != "version:9" || repo.lastCursor != 0 || repo.lastLimit != 10 {
		t.Fatalf("repository args channel=%q cursor=%d limit=%d want version:9/0/10",
			repo.lastChannel, repo.lastCursor, repo.lastLimit)
	}
	for _, session := range got {
		if session.ActivityChannel != "version:9" {
			t.Fatalf("session %d channel=%q want version:9", session.ID, session.ActivityChannel)
		}
	}
}

// TestListSessionsByActivityChannelPaginates pins the updated_at/id cursor: the
// second page resumes after the last id of the first page.
func TestListSessionsByActivityChannelPaginates(t *testing.T) {
	svc, repo := newChannelSessionService(t)
	repo.seedChannel("version:9", 3)

	first, more, err := svc.ListByChannel(context.Background(), "version:9", 0, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 2 || !more {
		t.Fatalf("first page sessions=%d has_more=%v want 2/true", len(first), more)
	}

	second, more, err := svc.ListByChannel(context.Background(), "version:9", first[len(first)-1].ID, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second) != 1 || more {
		t.Fatalf("second page sessions=%d has_more=%v want 1/false", len(second), more)
	}
	if second[0].ID == first[0].ID || second[0].ID == first[1].ID {
		t.Fatalf("second page repeats a first-page session: %+v", second)
	}
}

// TestListSessionsByActivityChannelRejectsBlankChannel pins the service-side
// validation: an empty or whitespace-only channel is never queried. The HTTP
// layer turns this into a 422/400 for the caller.
func TestListSessionsByActivityChannelRejectsBlankChannel(t *testing.T) {
	svc, repo := newChannelSessionService(t)
	for _, channel := range []string{"", "   "} {
		if _, _, err := svc.ListByChannel(context.Background(), channel, 0, 10); err == nil {
			t.Fatalf("ListByChannel(%q) returned no error", channel)
		}
	}
	if repo.listByChannelCalls != 0 {
		t.Fatalf("repository calls=%d want 0 for a blank channel", repo.listByChannelCalls)
	}
}

// TestListSessionsByActivityChannelWithoutRepositoryExtension pins the failure
// mode of a repository that lacks the P6 extension: it must fail loudly instead
// of silently falling back to the unfiltered P1 list.
func TestListSessionsByActivityChannelWithoutRepositoryExtension(t *testing.T) {
	repo := &svcFakeSessionRepository{}
	svc := application.NewSessionService(nil, repo, nil, nil, nil, nil)
	if _, _, err := svc.ListByChannel(context.Background(), "version:9", 0, 10); err == nil {
		t.Fatal("ListByChannel returned no error for a repository without ListByChannel")
	}
}

// fakeChannelHTTPPayloadService is an httpapi.SessionService whose channel list
// is programmable. The P1 methods are stubs the channel route never calls.
type fakeChannelHTTPPayloadService struct {
	sessions []domain.Session
	details  map[int64]domain.SessionDetail
	hasMore  bool

	listByChannelCalls int
	lastChannel        string
	lastCursor         int64
	lastLimit          int
}

// List implements httpapi.SessionService.
func (f *fakeChannelHTTPPayloadService) List(context.Context, int64, int, *int64) ([]domain.SessionSummary, bool, error) {
	return nil, false, nil
}

// Create implements httpapi.SessionService.
func (f *fakeChannelHTTPPayloadService) Create(context.Context, int64, string, *int64) (domain.Session, error) {
	return domain.Session{}, nil
}

// Get implements httpapi.SessionService.
func (f *fakeChannelHTTPPayloadService) Get(_ context.Context, id int64) (domain.SessionDetail, error) {
	if detail, ok := f.details[id]; ok {
		return detail, nil
	}
	return domain.SessionDetail{}, domain.ErrNotFound
}

// UpdateTitle implements httpapi.SessionService.
func (f *fakeChannelHTTPPayloadService) UpdateTitle(context.Context, int64, string) (domain.Session, error) {
	return domain.Session{}, nil
}

// Delete implements httpapi.SessionService.
func (f *fakeChannelHTTPPayloadService) Delete(context.Context, int64, *int64) error { return nil }

// Message implements httpapi.SessionService.
func (f *fakeChannelHTTPPayloadService) Message(context.Context, int64, int64) (domain.Message, error) {
	return domain.Message{}, nil
}

// ListByChannel is the P6 extension the channel route type-asserts.
func (f *fakeChannelHTTPPayloadService) ListByChannel(_ context.Context, channel string, beforeSeq int64, limit int) ([]domain.Session, bool, error) {
	f.listByChannelCalls++
	f.lastChannel = channel
	f.lastCursor = beforeSeq
	f.lastLimit = limit
	return f.sessions, f.hasMore, nil
}

// TestAgentSessionsChannelRouteIsAdminGuarded pins that the channel query stays
// behind the P1 admin guard: the guard rejects the request before the service is
// reached.
func TestAgentSessionsChannelRouteIsAdminGuarded(t *testing.T) {
	svc := &fakeChannelHTTPPayloadService{}
	guardCalls := 0
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			guardCalls++
			http.Error(w, "forbidden", http.StatusForbidden)
		})
	}
	mux := http.NewServeMux()
	httpapi.RegisterRoutes(mux, httpapi.NewHandler(nil, svc, nil, nil), guard)

	request := httptest.NewRequest(http.MethodGet, "/api/agents/sessions?channel=version%3A9", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 from the admin guard", recorder.Code)
	}
	if guardCalls != 1 {
		t.Fatalf("guard calls=%d want 1", guardCalls)
	}
	if svc.listByChannelCalls != 0 {
		t.Fatalf("service calls=%d want 0 when the guard rejects the request", svc.listByChannelCalls)
	}
}

// TestAgentSessionsChannelListEnvelope pins the channel page contract: the
// existing {"data":{items,has_more,next_cursor}} envelope with decimal string
// session ids and the P6 id/channel/title/status/running payload.
func TestAgentSessionsChannelListEnvelope(t *testing.T) {
	updated := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	svc := &fakeChannelHTTPPayloadService{
		sessions: []domain.Session{
			{ID: 11, Title: "case 11", ActivityChannel: "version:9", UpdatedAt: updated},
			{ID: 12, Title: "case 12", ActivityChannel: "version:9", UpdatedAt: updated},
		},
		details: map[int64]domain.SessionDetail{
			11: {
				Session:   domain.Session{ID: 11, Title: "case 11", ActivityChannel: "version:9"},
				Running:   true,
				ActiveRun: &domain.Run{ID: 91, SessionID: 11, Status: domain.RunRunning},
			},
		},
		hasMore: true,
	}
	mux := http.NewServeMux()
	httpapi.RegisterRoutes(mux, httpapi.NewHandler(nil, svc, nil, nil), nil)

	cursor := platform.EncodeCursor(11)
	target := "/api/agents/sessions?channel=" + url.QueryEscape("version:9") + "&limit=5&cursor=" + url.QueryEscape(cursor)
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			Items []struct {
				ID      string `json:"id"`
				Channel string `json:"channel"`
				Title   string `json:"title"`
				Status  string `json:"status"`
				Running bool   `json:"running"`
			} `json:"items"`
			HasMore    bool    `json:"has_more"`
			NextCursor *string `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data.Items) != 2 {
		t.Fatalf("items=%d want 2", len(response.Data.Items))
	}
	first := response.Data.Items[0]
	if first.ID != "11" || first.Channel != "version:9" || first.Title != "case 11" {
		t.Fatalf("first item=%+v want id=11 channel=version:9 title=case 11", first)
	}
	if !first.Running || first.Status != "running" {
		t.Fatalf("first item running=%v status=%q want true/running", first.Running, first.Status)
	}
	if response.Data.Items[1].Status != "idle" || response.Data.Items[1].Running {
		t.Fatalf("second item=%+v want idle/not running", response.Data.Items[1])
	}
	if !response.Data.HasMore || response.Data.NextCursor == nil || *response.Data.NextCursor != platform.EncodeCursor(12) {
		t.Fatalf("has_more=%v next_cursor=%v want true/%q",
			response.Data.HasMore, response.Data.NextCursor, platform.EncodeCursor(12))
	}
	if svc.lastChannel != "version:9" || svc.lastLimit != 5 || svc.lastCursor != 11 {
		t.Fatalf("service args channel=%q limit=%d cursor=%d want version:9/5/11",
			svc.lastChannel, svc.lastLimit, svc.lastCursor)
	}
}

// TestAgentSessionsEmptyChannelRejected pins the request validation: an explicit
// but empty channel is a client error, not the unfiltered P1 list.
func TestAgentSessionsEmptyChannelRejected(t *testing.T) {
	svc := &fakeChannelHTTPPayloadService{}
	mux := http.NewServeMux()
	httpapi.RegisterRoutes(mux, httpapi.NewHandler(nil, svc, nil, nil), nil)

	request := httptest.NewRequest(http.MethodGet, "/api/agents/sessions?channel=", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity && recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 422 or 400 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Code != platform.CodeInvalidArgument {
		t.Fatalf("error code=%q want %q", response.Error.Code, platform.CodeInvalidArgument)
	}
	if svc.listByChannelCalls != 0 {
		t.Fatalf("service calls=%d want 0 for an empty channel", svc.listByChannelCalls)
	}
}
