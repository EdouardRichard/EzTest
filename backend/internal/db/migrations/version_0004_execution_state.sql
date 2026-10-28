-- P6 execution state of a version (spec §3.3). The table was renamed to the
-- singular `version` by version_0003 (D34); the index name keeps the spec's
-- frozen `idx_versions_paused` name.
ALTER TABLE version ADD COLUMN IF NOT EXISTS paused BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE version ADD COLUMN IF NOT EXISTS pause_mode TEXT NOT NULL DEFAULT 'graceful'
  CHECK (pause_mode IN ('graceful','immediate'));
ALTER TABLE version ADD COLUMN IF NOT EXISTS batch_no INT NOT NULL DEFAULT 0
  CHECK (batch_no >= 0);
CREATE INDEX IF NOT EXISTS idx_versions_paused ON version(paused) WHERE paused = true;
