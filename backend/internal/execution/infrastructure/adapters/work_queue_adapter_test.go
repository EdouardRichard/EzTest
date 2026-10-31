package adapters

import (
	"context"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

type fakeWorkItemEnsurer struct {
	ensured       []agent.NewWorkItem
	waitingHuman  []int64
	finished      []int64
	finishReasons []string
	nextID        int64
	created       bool
	err           error
}

func (f *fakeWorkItemEnsurer) Ensure(_ context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error) {
	f.ensured = append(f.ensured, in)
	f.nextID++
	return agent.WorkItem{ID: f.nextID, VersionID: in.VersionID, Type: in.Type}, f.created, f.err
}

func (f *fakeWorkItemEnsurer) MarkWaitingHuman(_ context.Context, id, _ int64) error {
	f.waitingHuman = append(f.waitingHuman, id)
	return f.err
}

func (f *fakeWorkItemEnsurer) Finish(_ context.Context, id int64, reason string) error {
	f.finished = append(f.finished, id)
	f.finishReasons = append(f.finishReasons, reason)
	return f.err
}

type fakeRequirementReader struct {
	currentVersionID int64
	err              error
}

func (f *fakeRequirementReader) Get(context.Context, int64, int64) (versionapp.RequirementDetail, error) {
	if f.err != nil {
		return versionapp.RequirementDetail{}, f.err
	}
	return versionapp.RequirementDetail{
		RequirementView: versionapp.RequirementView{CurrentVersionID: f.currentVersionID},
	}, nil
}

type fakeTeamRoomPoster struct {
	posts []agent.TeamRoomPost
}

func (f *fakeTeamRoomPoster) Post(_ context.Context, post agent.TeamRoomPost) (agent.TeamRoomPost, error) {
	f.posts = append(f.posts, post)
	return post, nil
}

func TestWorkQueueAdapterMapsExecutionTask(t *testing.T) {
	queue := &fakeWorkItemEnsurer{created: true}
	adapter := NewWorkQueueAdapter(queue)
	id, created, err := adapter.Ensure(context.Background(), executionapp.WorkItemInput{
		VersionID: 10, Type: agentapp.WorkTypeExecuteCase, Team: "execution",
		DedupeKey: "execute:1001:3",
		Payload:   map[string]any{"intent_id": "1001", "dispatch_epoch": "3"},
	})
	if err != nil || !created || id != 1 {
		t.Fatalf("Ensure = %d, %v, %v", id, created, err)
	}
	if len(queue.ensured) != 1 {
		t.Fatalf("ensured = %+v", queue.ensured)
	}
	item := queue.ensured[0]
	if item.VersionID != 10 || item.Type != agentapp.WorkTypeExecuteCase || item.Team != "execution" ||
		item.DedupeKey != "execute:1001:3" || item.Payload.(map[string]any)["dispatch_epoch"] != "3" {
		t.Fatalf("ensured item = %+v", item)
	}
	if err := adapter.MarkWaitingHuman(context.Background(), 9001, 3); err != nil {
		t.Fatalf("MarkWaitingHuman: %v", err)
	}
	if err := adapter.Finish(context.Background(), 9001, "waiting_human_resumed"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if len(queue.waitingHuman) != 1 || queue.waitingHuman[0] != 9001 {
		t.Fatalf("waiting human = %v", queue.waitingHuman)
	}
	if len(queue.finished) != 1 || queue.finishReasons[0] != "waiting_human_resumed" {
		t.Fatalf("finished = %v / %v", queue.finished, queue.finishReasons)
	}
}

func TestGapEnsurerMatchesP2Dedupe(t *testing.T) {
	queue := &fakeWorkItemEnsurer{}
	adapter := NewGapEnsurer(queue, &fakeRequirementReader{currentVersionID: 601})
	if err := adapter.EnsureGap(context.Background(), 10, 501, 0, "execution_not_run"); err != nil {
		t.Fatalf("EnsureGap(requirement): %v", err)
	}
	if len(queue.ensured) != 1 {
		t.Fatalf("ensured = %+v", queue.ensured)
	}
	item := queue.ensured[0]
	if item.Type != agentapp.WorkTypeDesignGap || item.Team != "design" {
		t.Fatalf("gap item = %+v", item)
	}
	if item.DedupeKey != "gap:501:601" {
		t.Fatalf("dedupe = %q, want gap:501:601", item.DedupeKey)
	}
	payload := item.Payload.(map[string]any)
	if payload["requirement_id"] != "501" || payload["requirement_version_id"] != "601" ||
		payload["reason"] != "execution_not_run" {
		t.Fatalf("payload = %+v", payload)
	}

	if err := adapter.EnsureGap(context.Background(), 10, 501, 41, "execution_failed_coverage"); err != nil {
		t.Fatalf("EnsureGap(case): %v", err)
	}
	caseItem := queue.ensured[1]
	if caseItem.DedupeKey != "gap:501:41" {
		t.Fatalf("dedupe = %q, want gap:501:41", caseItem.DedupeKey)
	}
	if caseItem.Payload.(map[string]any)["case_version_id"] != "41" {
		t.Fatalf("case payload = %+v", caseItem.Payload)
	}
}

func TestTeamRoomAdapterPostsExecutionFinding(t *testing.T) {
	room := &fakeTeamRoomPoster{}
	adapter := NewTeamRoomAdapter(room)
	actor := int64(7)
	if err := adapter.Post(context.Background(), executionapp.TeamPost{
		VersionID: 10, Team: "execution", Kind: "finding",
		Content: "登录按钮文案不符", CreatedBy: &actor,
		Refs: []executionapp.TeamPostRef{{Kind: "case_run", ID: "3001"}},
	}); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(room.posts) != 1 {
		t.Fatalf("posts = %+v", room.posts)
	}
	post := room.posts[0]
	if post.VersionID != 10 || post.Team != agent.TeamExecution || post.Kind != agent.PostKindFinding ||
		post.Content != "登录按钮文案不符" || len(post.Refs) != 1 ||
		post.Refs[0].Kind != "case_run" || post.Refs[0].ID != "3001" {
		t.Fatalf("post = %+v", post)
	}
	if post.CreatedBy == nil || *post.CreatedBy != 7 {
		t.Fatalf("created by = %v", post.CreatedBy)
	}
}
