-- D34: version context table names become singular (spec §3.2/§3.3).
-- Rename only: columns, constraints, index names and data stay untouched.
ALTER TABLE IF EXISTS versions RENAME TO version;
ALTER TABLE IF EXISTS requirements RENAME TO requirement;
ALTER TABLE IF EXISTS requirement_relations RENAME TO requirement_relation;
ALTER TABLE IF EXISTS requirement_attachments RENAME TO requirement_attachment;
ALTER TABLE IF EXISTS clarification_sessions RENAME TO clarification_session;
ALTER TABLE IF EXISTS clarification_questions RENAME TO clarification_question;
ALTER TABLE IF EXISTS decisions RENAME TO decision;

-- P3 ids are application-generated Snowflake values and the tables above have
-- no sequences today; these defensive renames keep any externally created
-- sequence in step with its renamed table (spec §3.3).
ALTER SEQUENCE IF EXISTS versions_id_seq RENAME TO version_id_seq;
ALTER SEQUENCE IF EXISTS requirements_id_seq RENAME TO requirement_id_seq;
ALTER SEQUENCE IF EXISTS requirement_relations_id_seq RENAME TO requirement_relation_id_seq;
ALTER SEQUENCE IF EXISTS requirement_attachments_id_seq RENAME TO requirement_attachment_id_seq;
ALTER SEQUENCE IF EXISTS clarification_sessions_id_seq RENAME TO clarification_session_id_seq;
ALTER SEQUENCE IF EXISTS clarification_questions_id_seq RENAME TO clarification_question_id_seq;
ALTER SEQUENCE IF EXISTS decisions_id_seq RENAME TO decision_id_seq;
