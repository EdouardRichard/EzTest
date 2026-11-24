-- v0.2.4-D streaming: agent_message.stream_state marks an incrementally written
-- agent transcript row's lifecycle (streaming -> complete | interrupted) for
-- EVERY profile, including the non-Host runs shown in the admin-only Agent
-- Center transcript. Append-only: existing rows default to 'complete' so every
-- batch-path/legacy transcript row stays compatible and frozen.
--
-- The enum is NOT a second authority: it is the persisted projection of the
-- project's single stream-state authority version.MessageStreamStates(), which
-- agent.MessageStreamStates() derives from. The gates in
-- db/agent_stream_state_enum_test.go and the agent infrastructure integration
-- test derive their expected set from that authority.
ALTER TABLE agent_message
    ADD COLUMN stream_state TEXT NOT NULL DEFAULT 'complete'
        CONSTRAINT chk_agent_message_stream_state
        CHECK (stream_state IN ('streaming', 'complete', 'interrupted'));
