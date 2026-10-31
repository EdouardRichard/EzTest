package agent_test

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func TestTeamValidity(t *testing.T) {
	for _, team := range []agent.Team{
		agent.TeamClarification,
		agent.TeamDesign,
		agent.TeamExecution,
		agent.TeamDelivery,
		agent.TeamSteward,
	} {
		if !team.Valid() {
			t.Fatalf("team %q should be valid", team)
		}
	}
	if agent.Team("ops").Valid() {
		t.Fatal("unknown team must be invalid")
	}
	if _, err := agent.ParseTeam("design"); err != nil {
		t.Fatalf("ParseTeam(design): %v", err)
	}
	if _, err := agent.ParseTeam("unknown"); err == nil {
		t.Fatal("ParseTeam should reject unknown teams")
	} else if field := platform.AsError(err).Details["field"]; field != "team" {
		t.Fatalf("field = %#v", field)
	}
}

func TestPostKindValidity(t *testing.T) {
	for _, kind := range []agent.PostKind{
		agent.PostKindFinding,
		agent.PostKindDecision,
		agent.PostKindBlocker,
		agent.PostKindHandoff,
		agent.PostKindStatus,
	} {
		if !kind.Valid() {
			t.Fatalf("kind %q should be valid", kind)
		}
	}
	if agent.PostKind("chat").Valid() {
		t.Fatal("unknown kind must be invalid")
	}
}

func TestTeamRoomPostValidate(t *testing.T) {
	valid := agent.TeamRoomPost{
		VersionID: 10,
		Team:      agent.TeamClarification,
		Kind:      agent.PostKindDecision,
		Content:   "采用方案 A",
		Refs:      []agent.PostRef{{Kind: "requirement", ID: "100"}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid post rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*agent.TeamRoomPost)
		field  string
	}{
		{"missing version", func(p *agent.TeamRoomPost) { p.VersionID = 0 }, "version_id"},
		{"unknown team", func(p *agent.TeamRoomPost) { p.Team = "unknown" }, "team"},
		{"unknown kind", func(p *agent.TeamRoomPost) { p.Kind = "chat" }, "kind"},
		{"empty content", func(p *agent.TeamRoomPost) { p.Content = "   " }, "content"},
		{"too much content", func(p *agent.TeamRoomPost) { p.Content = string(make([]byte, agent.MaxPostContentLength+1)) }, "content"},
		{"bad ref", func(p *agent.TeamRoomPost) { p.Refs = []agent.PostRef{{Kind: "", ID: "1"}} }, "refs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			post := valid
			tc.mutate(&post)
			err := post.Validate()
			if err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
			if field := platform.AsError(err).Details["field"]; field != tc.field {
				t.Fatalf("field = %#v, want %q", field, tc.field)
			}
		})
	}
}

func TestTeamRoomDigestValidate(t *testing.T) {
	valid := agent.TeamRoomDigest{
		VersionID:     10,
		Team:          agent.TeamClarification,
		Summary:       "决定：方案 A",
		PinnedPostIDs: []int64{1, 2},
		TokenEstimate: 30,
		Version:       1,
		UpdatedAt:     testTime,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
	bad := valid
	bad.Version = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("digest version 0 must be invalid")
	}
	empty := valid
	empty.Summary = " "
	if err := empty.Validate(); err == nil {
		t.Fatal("empty digest summary must be invalid")
	}
}
