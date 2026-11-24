-- v0.2.4-C recording layer: extend evidence.kind with the command/file/trace
-- kinds produced by the executor v2 recording layer (command execution records,
-- workspace file snapshots, browser/execution traces). Append-only: this file
-- supersedes the chk_evidence_kind value set of visual_0002_v024_drop_visual
-- instead of editing it; the eight existing kinds and the GD-22 visual removal
-- are preserved.
ALTER TABLE evidence DROP CONSTRAINT IF EXISTS chk_evidence_kind;
ALTER TABLE evidence ADD CONSTRAINT chk_evidence_kind
    CHECK (kind IN ('screenshot', 'snapshot', 'console', 'network',
                    'http', 'db', 'download', 'text',
                    'command', 'file', 'trace'));