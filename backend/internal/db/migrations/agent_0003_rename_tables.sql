-- D34: table naming normalization (plural -> singular). Fields/constraints/index names unchanged.
ALTER TABLE IF EXISTS agent_profiles RENAME TO agent_profile;
ALTER SEQUENCE IF EXISTS agent_profiles_id_seq RENAME TO agent_profile_id_seq;
ALTER TABLE IF EXISTS agent_sessions RENAME TO agent_session;
ALTER SEQUENCE IF EXISTS agent_sessions_id_seq RENAME TO agent_session_id_seq;
ALTER TABLE IF EXISTS agent_runs RENAME TO agent_run;
ALTER SEQUENCE IF EXISTS agent_runs_id_seq RENAME TO agent_run_id_seq;
ALTER TABLE IF EXISTS agent_messages RENAME TO agent_message;
ALTER SEQUENCE IF EXISTS agent_messages_id_seq RENAME TO agent_message_id_seq;
