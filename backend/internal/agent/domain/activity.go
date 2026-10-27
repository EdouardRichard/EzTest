package domain

import "strconv"

// EventType is the P0 events.type value used for every agent activity event.
const EventType = "agent.activity"

// Activity kinds published in detail.kind. The set is frozen in the activity
// event contract V1 (design spec §9.2).
const (
	KindRunStarted  = "run_started"
	KindUserMessage = "user_message"
	KindThinking    = "thinking"
	KindText        = "text"
	KindToolUse     = "tool_use"
	KindToolResult  = "tool_result"
	KindUsage       = "usage"
	KindDecision    = "decision"
	KindRunFinished = "run_finished"
)

// ChannelForSession returns the P0 event channel of a session. The events
// table limits channels to 64 characters and the numeric suffix keeps the
// channel well below that.
func ChannelForSession(sessionID int64) string {
	return "agent:" + strconv.FormatInt(sessionID, 10)
}
