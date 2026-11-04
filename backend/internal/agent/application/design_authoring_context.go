package agentapp

import (
	"strings"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// Task 79 design-authoring context scoping.
//
// A background design run is one of the per-requirement case authors, the round
// executor, the critic or the data planner. They all share the version's design
// conversation and post their final summary both to that conversation and to
// the design team room. Reading those shared, append-only artifacts back into
// every author run makes the prompt grow with the number of authors: in the
// live 67-requirement S3 version the design thread reached 178 messages and the
// design room 10 multi-KB posts, so each later author replayed 300k-670k
// conservative tokens. The design token budget was consumed by the read-only
// turns and case.graph_propose never ran.
//
// A background author does not need the running design chat: its work item
// carries the requirement scope, and requirement.read + design.graph_read give
// it the current facts. It only needs the human instructions (the latest design
// decisions) and the newest team-room signal. The design Host chat.turn is
// deliberately excluded: it is the conversation and keeps its full thread.
const (
	maxDesignAuthoringHistoryMessages  = 3
	maxDesignAuthoringTeamRoomPosts    = 2
	maxDesignAuthoringTeamRoomPostRune = 400
	// The design Host chat (S4 case modification) keeps its own recent replies
	// in addition to the human instructions, still bounded.
	maxDesignHostHistoryMessages = 6
)

// designAuthoringWorkTypes are the background design work types whose context
// is scoped by Task 79.
func isDesignAuthoringWorkType(workType string) bool {
	switch workType {
	case WorkTypeDesignRequirement, WorkTypeDesignGap, WorkTypeDesignRound,
		WorkTypeDesignCritic, WorkTypeDesignDataPlan:
		return true
	default:
		return false
	}
}

// designAuthoringHistory keeps the human turns of the design thread (the latest
// instruction window) and drops the background authors' own UI projections and
// the design Host's replies. The result is bounded by
// maxDesignAuthoringHistoryMessages.
func designAuthoringHistory(messages []Message) []Message {
	kept := make([]Message, 0, maxDesignAuthoringHistoryMessages)
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			continue
		}
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		kept = append(kept, message)
	}
	if len(kept) > maxDesignAuthoringHistoryMessages {
		kept = kept[len(kept)-maxDesignAuthoringHistoryMessages:]
	}
	return kept
}

// boundDesignAuthoringTeamRoom keeps the newest posts only and clips each post
// body, so the shared design room cannot flood a background author prompt. The
// digest is kept: it is already bounded by the digest contract.
func boundDesignAuthoringTeamRoom(room TeamRoomContext) TeamRoomContext {
	if len(room.Posts) > maxDesignAuthoringTeamRoomPosts {
		room.Posts = room.Posts[:maxDesignAuthoringTeamRoomPosts]
	}
	posts := make([]agent.TeamRoomPost, 0, len(room.Posts))
	for _, post := range room.Posts {
		post.Content = clipDesignAuthoringText(post.Content, maxDesignAuthoringTeamRoomPostRune)
		posts = append(posts, post)
	}
	room.Posts = posts
	return room
}

// designHostHistory bounds the design Host chat transcript: the newest human
// instructions plus the newest assistant turns (the Host's own replies or the
// latest background projection), in original order. The Host reads the current
// graph with design.graph_read for the case state; replaying the whole design
// thread (which every background author appends to) is not needed and grows
// with the round.
func designHostHistory(messages []Message) []Message {
	keep := make(map[int]struct{}, maxDesignHostHistoryMessages)
	humans, assistants := 0, 0
	for index := len(messages) - 1; index >= 0; index-- {
		switch strings.ToLower(strings.TrimSpace(messages[index].Role)) {
		case "user":
			if humans < maxDesignHostHistoryMessages/2 {
				keep[index] = struct{}{}
				humans++
			}
		case "assistant":
			if assistants < maxDesignHostHistoryMessages/2 {
				keep[index] = struct{}{}
				assistants++
			}
		}
		if humans >= maxDesignHostHistoryMessages/2 && assistants >= maxDesignHostHistoryMessages/2 {
			break
		}
	}
	kept := make([]Message, 0, len(keep))
	for index, message := range messages {
		if _, ok := keep[index]; ok {
			kept = append(kept, message)
		}
	}
	return kept
}

// clipDesignAuthoringText truncates text to limit runes without splitting a
// UTF-8 rune.
func clipDesignAuthoringText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
