-- v0.2.4-C skill usage ledger (T19, SC-C12): append-only found/miss facts of
-- every skill.read. (run_id, seq) is the idempotency key: a replayed write of
-- the same fact is a no-op. run_id is a real same-context FK to agent_run.
-- There is no UPDATE/DELETE path; rows are facts, never current state.

CREATE TABLE skill_usage (
    id         BIGINT PRIMARY KEY,
    run_id     BIGINT NOT NULL REFERENCES agent_run (id),
    seq        INTEGER NOT NULL CHECK (seq >= 1),
    skill_name TEXT NOT NULL CHECK (length(skill_name) > 0),
    found      BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_skill_usage_run_seq UNIQUE (run_id, seq)
);