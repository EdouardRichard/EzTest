-- P7 resource context: optional SUT family (#38).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p7-p9-design.md §5 (#38) and
-- P7-D-01. scope=system knowledge matches versions through sut.family, so the
-- family is stored lowercase (<=64 chars) and indexed only when present.

ALTER TABLE sut
    ADD COLUMN family TEXT NULL
        CHECK (family IS NULL OR (family = lower(family) AND char_length(family) BETWEEN 1 AND 64));

CREATE INDEX idx_sut_family ON sut (family) WHERE family IS NOT NULL;
