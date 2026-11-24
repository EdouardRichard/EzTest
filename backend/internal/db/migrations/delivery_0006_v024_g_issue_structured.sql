-- v0.2.4-G (T2, SC-G3 / P21): structured failure elements on the delivery issue.
-- failed_expected / failed_actual / failed_reason carry the representative run's
-- assertion expected / observed / reason for the five-element defect card; the
-- free-form summary narrative is retained. Append-only (GD-05): existing rows
-- default to empty strings, no backfill. The candidate-derivation fill that
-- populates them from assertion_result belongs to G T8, not here.
ALTER TABLE delivery_issue
    ADD COLUMN failed_expected TEXT NOT NULL DEFAULT '',
    ADD COLUMN failed_actual   TEXT NOT NULL DEFAULT '',
    ADD COLUMN failed_reason   TEXT NOT NULL DEFAULT '';