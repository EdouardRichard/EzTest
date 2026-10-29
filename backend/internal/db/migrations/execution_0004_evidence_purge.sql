-- P8 evidence GC metadata (design spec 10.2/4.2): the artifact bytes are removed
-- by the GC while the evidence row is retained as metadata. purged_at is the
-- durable marker; purged_reason records why it was removed. Both default to the
-- unpurged state so every existing row stays untouched.
ALTER TABLE evidence ADD COLUMN IF NOT EXISTS purged_at TIMESTAMPTZ;
ALTER TABLE evidence ADD COLUMN IF NOT EXISTS purged_reason TEXT NOT NULL DEFAULT '';

-- Candidate scan: unpurged evidence older than the retention window.
CREATE INDEX IF NOT EXISTS idx_evidence_unpurged_created
  ON evidence(created_at, id) WHERE purged_at IS NULL;
