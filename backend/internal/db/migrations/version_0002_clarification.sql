CREATE TABLE IF NOT EXISTS clarification_sessions (
  id BIGINT PRIMARY KEY,
  version_id BIGINT NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','completed','cancelled')),
  agent_session_id BIGINT,
  context_summary TEXT NOT NULL DEFAULT '',
  summary_updated_at TIMESTAMPTZ,
  started_by BIGINT NOT NULL,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ended_at TIMESTAMPTZ,
  ended_reason TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_clarification_active
  ON clarification_sessions(version_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_clarification_version_started
  ON clarification_sessions(version_id, started_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS clarification_questions (
  id BIGINT PRIMARY KEY,
  version_id BIGINT NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
  requirement_id BIGINT REFERENCES requirements(id) ON DELETE CASCADE,
  session_id BIGINT REFERENCES clarification_sessions(id) ON DELETE SET NULL,
  question TEXT NOT NULL CHECK (question <> ''),
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','answered','dismissed')),
  answer TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'ai' CHECK (source IN ('ai','user')),
  answered_via TEXT NOT NULL DEFAULT 'ui' CHECK (answered_via IN ('ui','agent')),
  answered_by BIGINT,
  answered_at TIMESTAMPTZ,
  created_by BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (status <> 'answered' OR answer <> '')
);
CREATE INDEX IF NOT EXISTS idx_questions_version_status
  ON clarification_questions(version_id, status, id);
CREATE INDEX IF NOT EXISTS idx_questions_requirement_status
  ON clarification_questions(requirement_id, status, id);

CREATE TABLE IF NOT EXISTS decisions (
  id BIGINT PRIMARY KEY,
  version_id BIGINT NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
  requirement_id BIGINT REFERENCES requirements(id) ON DELETE CASCADE,
  question_id BIGINT REFERENCES clarification_questions(id) ON DELETE SET NULL,
  topic TEXT NOT NULL CHECK (topic <> ''),
  decision TEXT NOT NULL CHECK (decision <> ''),
  rationale TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed','accepted','rejected')),
  source TEXT NOT NULL DEFAULT 'ai' CHECK (source IN ('ai','user')),
  proposed_by BIGINT NOT NULL,
  decided_by BIGINT,
  decided_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_decisions_version_status
  ON decisions(version_id, status, id);
CREATE INDEX IF NOT EXISTS idx_decisions_requirement ON decisions(requirement_id, id);
