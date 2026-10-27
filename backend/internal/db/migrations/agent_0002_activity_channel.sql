ALTER TABLE agent_sessions
  ADD COLUMN IF NOT EXISTS activity_channel TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions
  ADD COLUMN IF NOT EXISTS activity_version_id TEXT;
CREATE INDEX IF NOT EXISTS idx_agent_sessions_activity_channel
  ON agent_sessions(activity_channel) WHERE activity_channel <> '';
