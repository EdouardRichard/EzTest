-- P8 visual assertions (design spec §9.3 / D7): a case assertion may be of kind
-- visual. Its comparison parameters live in a dedicated table keyed by the
-- assertion id, so the frozen case_assertion shape stays stable and the baseline
-- approval metadata never leaks into the graph write path.
--
-- The kind CHECK is an inline column constraint, so PostgreSQL named it
-- case_assertion_kind_check; drop that exact name and re-add every legacy kind.
ALTER TABLE case_assertion DROP CONSTRAINT IF EXISTS case_assertion_kind_check;
ALTER TABLE case_assertion ADD CONSTRAINT ck_case_assertion_kind
  CHECK (kind IN ('ui','api','db','text','visual'));

CREATE TABLE IF NOT EXISTS case_visual_assertion (
  assertion_id         BIGINT NOT NULL
                         REFERENCES case_assertion(id) ON DELETE CASCADE,
  -- Empty selector means the whole page.
  selector             TEXT NOT NULL DEFAULT '',
  -- Normalized {x,y,width,height} crop of the viewport; '{}' means no crop.
  region               JSONB NOT NULL DEFAULT '{}'::jsonb,
  -- Fraction of differing pixels allowed, 0..0.1 (spec §9.3).
  threshold            NUMERIC(6,5) NOT NULL DEFAULT 0.005
                         CHECK (threshold >= 0 AND threshold <= 0.1),
  baseline_evidence_id BIGINT,
  viewport_width       INTEGER NOT NULL DEFAULT 1280 CHECK (viewport_width > 0),
  viewport_height      INTEGER NOT NULL DEFAULT 720 CHECK (viewport_height > 0),
  device_scale_factor  NUMERIC(4,2) NOT NULL DEFAULT 1.0
                         CHECK (device_scale_factor > 0),
  approved_by          BIGINT,
  approved_at          TIMESTAMPTZ,
  revision             INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_case_visual_assertion PRIMARY KEY (assertion_id)
);
CREATE INDEX IF NOT EXISTS idx_case_visual_assertion_baseline
  ON case_visual_assertion(baseline_evidence_id)
  WHERE baseline_evidence_id IS NOT NULL;
