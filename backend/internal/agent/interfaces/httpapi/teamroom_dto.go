package agentapi

// teamRoomDTO is the read-only team-room envelope from spec §8.3:
//
//	{"data":{"team":...,"digest":...|null,"posts":[...]}}
type teamRoomDTO struct {
	Team   string             `json:"team"`
	Digest *teamRoomDigestDTO `json:"digest"`
	Posts  []teamRoomPostDTO  `json:"posts"`
}

type teamRoomDigestDTO struct {
	Summary       string   `json:"summary"`
	PinnedPostIDs []string `json:"pinned_post_ids"`
	TokenEstimate int      `json:"token_estimate"`
	Version       int      `json:"version"`
	UpdatedAt     string   `json:"updated_at"`
}

type teamRoomPostDTO struct {
	ID         string               `json:"id"`
	Kind       string               `json:"kind"`
	Content    string               `json:"content"`
	Refs       []teamRoomPostRefDTO `json:"refs"`
	AgentRunID *string              `json:"agent_run_id"`
	CreatedAt  string               `json:"created_at"`
}

type teamRoomPostRefDTO struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}
