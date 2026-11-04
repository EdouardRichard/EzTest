-- Task 96: requirement source identity.
--
-- Two requirement curators can create drafts for the same source requirement
-- (for example FR-011) in the same version concurrently. The application
-- derives a deterministic key from the leading source ID in the draft title and
-- this partial unique index makes the database the final arbiter: exactly one
-- active requirement per (version_id, source_key).
--
-- Legacy rows keep the empty default and are excluded from the index, so
-- existing duplicate rows are retained (the manual requirement.dedupe scan can
-- still collapse them). Deleted (obsolete) requirements also leave the index,
-- so a source can be re-created after an explicit delete.
ALTER TABLE requirement ADD COLUMN source_key TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX uk_requirement_version_id_source_key
    ON requirement (version_id, source_key)
    WHERE source_key <> '' AND status <> 'obsolete';
