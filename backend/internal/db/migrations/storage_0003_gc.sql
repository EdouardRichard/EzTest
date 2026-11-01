-- P7 storage context: unified content-ledger GC runs and tombstones (#35).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p7-p9-design.md §5 (#35) and
-- §4.4. A single gc_run may be running at a time; apply mode writes a
-- tombstone before deleting refs/objects/files. Report revisions' HTML/PDF are
-- protected by the application and never deleted here. Cross-context actor /
-- content references stay logical BIGINT only.

CREATE TABLE gc_run (
    id              BIGINT NOT NULL,
    mode            TEXT NOT NULL CHECK (mode IN ('dry_run', 'apply')),
    status          TEXT NOT NULL DEFAULT 'running'
                    CHECK (status IN ('running', 'completed', 'failed')),
    actor_id        BIGINT NULL,
    actor_kind      TEXT NOT NULL DEFAULT 'system' CHECK (actor_kind IN ('user', 'system')),
    lease_owner     TEXT NULL,
    lease_until     TIMESTAMPTZ NULL,
    lease_epoch     BIGINT NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ NULL,
    scanned_objects INTEGER NOT NULL DEFAULT 0 CHECK (scanned_objects >= 0),
    scanned_refs    INTEGER NOT NULL DEFAULT 0 CHECK (scanned_refs >= 0),
    deleted_objects INTEGER NOT NULL DEFAULT 0 CHECK (deleted_objects >= 0),
    deleted_refs    INTEGER NOT NULL DEFAULT 0 CHECK (deleted_refs >= 0),
    deleted_bytes   BIGINT NOT NULL DEFAULT 0 CHECK (deleted_bytes >= 0),
    report          JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(report) = 'object'),
    error           TEXT NULL CHECK (error IS NULL OR char_length(error) <= 4000),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_gc_run PRIMARY KEY (id)
);

-- One active GC run at a time (partial unique index on a constant expression).
CREATE UNIQUE INDEX uk_gc_run_running ON gc_run ((true)) WHERE status = 'running';
CREATE INDEX idx_gc_run_started_at ON gc_run (started_at DESC, id DESC);

CREATE TABLE gc_tombstone (
    id            BIGINT NOT NULL,
    run_id        BIGINT NOT NULL REFERENCES gc_run (id) ON DELETE CASCADE,
    object_sha    TEXT NOT NULL CHECK (object_sha ~ '^[0-9a-f]{64}$'),
    size_bytes    BIGINT NOT NULL CHECK (size_bytes >= 0),
    mime          TEXT NOT NULL DEFAULT 'application/octet-stream',
    relative_path TEXT NOT NULL,
    reason        TEXT NOT NULL
                  CHECK (reason IN ('unreferenced', 'temp', 'superseded_export', 'orphan_file')),
    deleted_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_gc_tombstone PRIMARY KEY (id)
);

CREATE INDEX idx_gc_tombstone_run_id ON gc_tombstone (run_id, id DESC);

-- GC scan accelerators.
CREATE INDEX idx_content_ref_live_object ON content_ref (object_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_content_object_created_at ON content_object (created_at);
