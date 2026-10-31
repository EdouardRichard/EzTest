-- P2 storage context: export artifact metadata. The bytes live in the P0
-- content ledger (content_object/content_ref); this table only records the
-- export revision and its content-object SHA-256.
-- See docs/superpowers/specs/2026-10-31-eztest-v02-p2-case-design-design.md §3.2.

CREATE TABLE export_artifact (
    id                  BIGINT NOT NULL,
    version_id          BIGINT NOT NULL,
    owner_kind          TEXT NOT NULL CHECK (owner_kind IN ('case', 'report', 'chain')),
    owner_id            BIGINT NOT NULL,
    revision            INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    file_format         TEXT NOT NULL
                        CHECK (file_format IN ('markdown', 'mermaid', 'opml', 'csv', 'html', 'pdf')),
    content_object_id   TEXT NOT NULL CHECK (content_object_id ~ '^[0-9a-f]{64}$'),
    size_bytes          BIGINT NOT NULL CHECK (size_bytes >= 0),
    mime                TEXT NOT NULL,
    filename            TEXT NOT NULL CHECK (char_length(filename) BETWEEN 1 AND 255),
    status              TEXT NOT NULL DEFAULT 'ready'
                        CHECK (status IN ('ready', 'failed')),
    created_by          BIGINT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_export_artifact PRIMARY KEY (id),
    CONSTRAINT uk_export_artifact_version_owner_file_format_revision
        UNIQUE (version_id, owner_kind, owner_id, file_format, revision)
);

CREATE INDEX idx_export_artifact_version ON export_artifact (version_id, id DESC);
CREATE INDEX idx_export_artifact_created_at ON export_artifact (created_at);
