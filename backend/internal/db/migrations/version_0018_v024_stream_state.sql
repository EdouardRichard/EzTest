-- v0.2.4-D streaming: conversation_message.stream_state marks an incremental
-- Host reply's lifecycle (streaming -> complete | interrupted). Append-only:
-- existing rows default to 'complete' so every batch-path/legacy message stays
-- compatible. The CHECK enum is a persisted projection of the Go domain
-- authority version.MessageStreamStates() (streaming/complete/interrupted); the
-- single-source gates in db/stream_state_enum_test.go and the version
-- infrastructure integration test derive their expected set from that authority.
ALTER TABLE conversation_message
    ADD COLUMN stream_state TEXT NOT NULL DEFAULT 'complete'
        CONSTRAINT chk_conversation_message_stream_state
        CHECK (stream_state IN ('streaming', 'complete', 'interrupted'));
