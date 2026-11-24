-- v0.2.4-H Q06 (H-A11): human_request.kind "other" had zero production creators
-- (execution/domain/human.go enum + Valid only; discovery_capture.go's "other"
-- is resource-type normalization, not a human_request writer). Design 13 §2.3
-- Q06 removes it. The member is dropped from the Go enum, the published OpenAPI
-- enum (docs/openapi/api.yaml HumanRequestView.kind) and the frontend union/labels
-- in the same change set; the deletion is announced in the v0.2.4 release notes
-- (H chapter, constitution VI.6 notification period; GD-05 destructive-release
-- precedent). Append-only: this tightens the existing chk_human_request_kind
-- installed by execution_0007, never edits an applied file.
--
-- No production row ever carried kind='other' (zero creators). The DELETE keeps
-- the migration safe on a copied legacy dataset (GD-05 old data may be discarded)
-- so the tightened CHECK cannot fail on an orphan row.

DELETE FROM human_request WHERE kind = 'other';

ALTER TABLE human_request
    DROP CONSTRAINT chk_human_request_kind,
    ADD CONSTRAINT chk_human_request_kind
        CHECK (kind IN ('clarification_question', 'approval', 'blocked_recovery',
                        'login_takeover', 'conflict'));