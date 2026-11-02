package designapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func newReviewFixture(t *testing.T) (*GraphService, *fakeRepo, *fakePublisher, *fakeTeamRoom) {
	t.Helper()
	repo := newFakeRepo()
	publisher := &fakePublisher{}
	teamRoom := &fakeTeamRoom{}
	service := NewGraphService(repo, &fakeIDs{}, newFixedClock(), nil, publisher, teamRoom)
	return service, repo, publisher, teamRoom
}

func TestSubmitReviewPostsFinding(t *testing.T) {
	service, repo, publisher, teamRoom := newReviewFixture(t)
	repo.latestRound = &design.Round{
		ID: 8001, VersionID: 1001, ConversationID: 9001,
		Activation: design.ActivationManual, Status: design.RoundStatusActive,
	}

	result, err := service.SubmitReview(context.Background(), 1001, 3001, 77, ReviewInput{
		Summary: "覆盖不足",
		Findings: []ReviewFinding{
			{Severity: "important", Message: "缺少异常路径"},
			{Severity: "minor", Message: "标题不够具体"},
		},
	})
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if !result.Posted || result.Count != 2 {
		t.Fatalf("result = %+v", result)
	}
	if len(teamRoom.posts) != 2 {
		t.Fatalf("team posts = %d", len(teamRoom.posts))
	}
	for _, post := range teamRoom.posts {
		if post.Team != "design" || post.Kind != "finding" {
			t.Fatalf("post = %+v", post)
		}
		if post.AgentRunID == nil || *post.AgentRunID != 77 {
			t.Fatalf("post run id = %v", post.AgentRunID)
		}
	}
	events := publisher.byType("design.review_submitted")
	if len(events) != 1 {
		t.Fatalf("review events = %v", publisher.types())
	}
	if events[0].Payload["count"] != 2 {
		t.Fatalf("event payload = %v", events[0].Payload)
	}
	if events[0].Payload["round_id"] != "8001" {
		t.Fatalf("round id payload = %v", events[0].Payload["round_id"])
	}
}

func TestSubmitReviewValidation(t *testing.T) {
	service, _, _, _ := newReviewFixture(t)
	cases := []ReviewInput{
		{},
		{Findings: []ReviewFinding{{Severity: "critical", Message: "x"}}},
		{Findings: []ReviewFinding{{Severity: "minor", Message: ""}}},
		{Findings: []ReviewFinding{{Severity: "minor", Message: strings.Repeat("x", 2001)}}},
		{Summary: strings.Repeat("x", 4001), Findings: []ReviewFinding{{Severity: "minor", Message: "x"}}},
	}
	for _, input := range cases {
		_, err := service.SubmitReview(context.Background(), 1001, 3001, 1, input)
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Code != "invalid_argument" {
			t.Fatalf("input %+v err = %v, want invalid_argument", input, err)
		}
	}
}
