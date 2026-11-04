-- v0.22 B structured questions: human_request gains its conversation/message
-- binding and the versioned question payload, while kind/status move to the
-- v0.22 question state machine (spec DB-16, §4.11, B §6.1).
--
-- The 0.21 checks were inline column constraints, so PostgreSQL auto-named them
-- human_request_kind_check / human_request_status_check. Drop them before
-- remapping the legacy status values and installing the v0.22 definitions.
-- v0.22 rebuilds from a fresh database; the UPDATE is still kept so the
-- migration is safe on a copied 0.21 dataset.
-- conversation_id/message_id stay logical BIGINT references (no cross-context
-- FK, Agent.md §6.9.7).
-- See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md §5 DB-16 and
-- docs/version-V0.22/B-clarification-messaging.md §6.1.

ALTER TABLE human_request
    ADD COLUMN conversation_id BIGINT NULL,
    ADD COLUMN message_id      BIGINT NULL,
    ADD COLUMN question        JSONB NOT NULL DEFAULT '{}'::jsonb
        CONSTRAINT chk_human_request_question
        CHECK (jsonb_typeof(question) = 'object');

ALTER TABLE human_request
    DROP CONSTRAINT human_request_kind_check,
    DROP CONSTRAINT human_request_status_check;

-- Map the 0.21 status vocabulary onto the v0.22 one. The other legacy values
-- (cancelled/expired) already match.
UPDATE human_request
SET status = CASE status
    WHEN 'open' THEN 'pending'
    WHEN 'resolved' THEN 'answered'
    ELSE status
END
WHERE status IN ('open', 'resolved');

ALTER TABLE human_request
    ALTER COLUMN status SET DEFAULT 'pending',
    ADD CONSTRAINT chk_human_request_kind
        CHECK (kind IN ('clarification_question', 'approval', 'blocked_recovery',
                        'login_takeover', 'conflict', 'other')),
    ADD CONSTRAINT chk_human_request_status
        CHECK (status IN ('pending', 'answered', 'expired', 'cancelled'));

-- The question card is read by conversation, newest first.
CREATE INDEX idx_human_request_conversation
    ON human_request (conversation_id, id);
