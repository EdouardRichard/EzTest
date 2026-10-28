-- P7 version context (spec §4.2.5, §5.1): a completed version can be
-- continued by adding requirements. continue_phase tracks the sub-phase while
-- the version status is 'continuing'; continued_at records when it started.
ALTER TABLE version ADD COLUMN IF NOT EXISTS continue_phase TEXT
  CHECK (continue_phase IN ('clarifying','designing','executing'));
ALTER TABLE version ADD COLUMN IF NOT EXISTS continued_at TIMESTAMPTZ;
