CREATE TABLE storage_objects (
  sha256 TEXT PRIMARY KEY,
  size BIGINT NOT NULL,
  content_type TEXT NOT NULL,
  relative_path TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
