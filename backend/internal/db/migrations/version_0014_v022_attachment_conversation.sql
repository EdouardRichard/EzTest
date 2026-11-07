-- v0.22 Task 20 follow-up (Important): scope pre-registered message attachments
-- to the conversation that owns them. A message post preflight stores the
-- requirement_attachment row before the conversation_message insert and starts
-- attachment.ingest, so the row has no conversation_message_id for a while
-- (R-E4-1). Without an owning conversation that draft row was visible/readable
-- from every conversation of the same version through ListByConversation /
-- attachment.read_text, which leaked one conversation's pending upload into the
-- others. conversation_id records the owner from pre-registration onwards; it is
-- NULL for requirement-scoped attachment rows and for legacy/unattributable
-- drafts, which the conversation read path deliberately excludes (never return
-- an unscoped draft to a conversation).
--
-- Additive column + index only. Bound rows are backfilled from their message so
-- existing data has the same scope as the read path (which resolves bound rows
-- through conversation_message).

ALTER TABLE requirement_attachment
    ADD COLUMN conversation_id BIGINT NULL;

UPDATE requirement_attachment a
SET conversation_id = m.conversation_id
FROM conversation_message m
WHERE m.id = a.conversation_message_id
  AND a.conversation_id IS NULL;

CREATE INDEX idx_requirement_attachment_conversation
    ON requirement_attachment (conversation_id);
