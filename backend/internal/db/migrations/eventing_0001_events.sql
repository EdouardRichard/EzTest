CREATE TABLE event_channels (
  channel TEXT PRIMARY KEY,
  last_seq BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE events (
  id BIGSERIAL PRIMARY KEY,
  channel TEXT NOT NULL REFERENCES event_channels(channel) ON DELETE RESTRICT,
  seq BIGINT NOT NULL,
  type TEXT NOT NULL,
  version_id TEXT,
  batch_id TEXT,
  case_id TEXT,
  step_id TEXT,
  agent_role TEXT,
  session_id TEXT,
  ts TIMESTAMPTZ NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  detail JSONB NOT NULL DEFAULT '{}',
  refs JSONB NOT NULL DEFAULT '{"evidence":[],"case":[],"requirement":[],"code":[]}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(channel, seq)
);
-- UNIQUE(channel, seq) already serves the replay path (channel, seq); these two
-- support time- and type-filtered queries.
CREATE INDEX events_channel_ts_idx ON events(channel, ts);
CREATE INDEX events_channel_type_idx ON events(channel, type);
