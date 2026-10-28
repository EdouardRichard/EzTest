-- D34: table naming normalization (plural -> singular). Fields/constraints/indexes unchanged.
ALTER TABLE IF EXISTS audit_logs RENAME TO audit_log;
ALTER SEQUENCE IF EXISTS audit_logs_id_seq RENAME TO audit_log_id_seq;
