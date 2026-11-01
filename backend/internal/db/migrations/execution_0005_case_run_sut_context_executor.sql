-- P8 hardening: remember which executor owns one browser context so actions,
-- cancellation, close and orphan reconciliation are routed to the same
-- executor endpoint that created the context. Logical reference to
-- executor_instance.id (no cross-context FK). NULL for historical rows.

ALTER TABLE case_run_sut_context
    ADD COLUMN executor_id BIGINT,
    ADD CONSTRAINT ck_case_run_sut_context_executor CHECK (executor_id IS NULL OR executor_id > 0);
