-- D34: table naming normalization (plural -> singular). Fields/constraints/indexes unchanged.
ALTER TABLE IF EXISTS idempotency_keys RENAME TO idempotency_key;
ALTER SEQUENCE IF EXISTS idempotency_keys_id_seq RENAME TO idempotency_key_id_seq;
