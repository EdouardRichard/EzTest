CREATE TABLE content_object (
    sha256 TEXT PRIMARY KEY,
    size BIGINT NOT NULL CHECK (size >= 0),
    mime TEXT NOT NULL DEFAULT 'application/octet-stream',
    relative_path TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE content_ref (
    id BIGINT PRIMARY KEY,
    object_id TEXT NOT NULL REFERENCES content_object(sha256),
    owner_kind TEXT NOT NULL,
    owner_id BIGINT NOT NULL,
    role TEXT NOT NULL DEFAULT 'attachment',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ NULL
);

CREATE INDEX idx_content_ref_object ON content_ref (object_id);
CREATE INDEX idx_content_ref_owner ON content_ref (owner_kind, owner_id);
