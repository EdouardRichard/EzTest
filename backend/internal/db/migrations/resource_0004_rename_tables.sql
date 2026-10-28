-- D34 rename (spec §3.2): singular resource table names. Only relation names
-- change; columns, constraints and existing index names are left untouched.
-- Every table below was created with BIGSERIAL, so its owned id sequence is
-- renamed with the table.

ALTER TABLE IF EXISTS companies RENAME TO company;
ALTER SEQUENCE IF EXISTS companies_id_seq RENAME TO company_id_seq;

ALTER TABLE IF EXISTS systems RENAME TO system;
ALTER SEQUENCE IF EXISTS systems_id_seq RENAME TO system_id_seq;

ALTER TABLE IF EXISTS environments RENAME TO environment;
ALTER SEQUENCE IF EXISTS environments_id_seq RENAME TO environment_id_seq;

ALTER TABLE IF EXISTS app_model_nodes RENAME TO app_model_node;
ALTER SEQUENCE IF EXISTS app_model_nodes_id_seq RENAME TO app_model_node_id_seq;

ALTER TABLE IF EXISTS accounts RENAME TO account;
ALTER SEQUENCE IF EXISTS accounts_id_seq RENAME TO account_id_seq;

ALTER TABLE IF EXISTS account_system_accesses RENAME TO account_system_access;
ALTER SEQUENCE IF EXISTS account_system_accesses_id_seq RENAME TO account_system_access_id_seq;

ALTER TABLE IF EXISTS sessions RENAME TO session;
ALTER SEQUENCE IF EXISTS sessions_id_seq RENAME TO session_id_seq;

ALTER TABLE IF EXISTS session_snapshots RENAME TO session_snapshot;
ALTER SEQUENCE IF EXISTS session_snapshots_id_seq RENAME TO session_snapshot_id_seq;

ALTER TABLE IF EXISTS session_leases RENAME TO session_lease;
ALTER SEQUENCE IF EXISTS session_leases_id_seq RENAME TO session_lease_id_seq;

ALTER TABLE IF EXISTS manual_takeovers RENAME TO manual_takeover;
ALTER SEQUENCE IF EXISTS manual_takeovers_id_seq RENAME TO manual_takeover_id_seq;

ALTER TABLE IF EXISTS code_repositories RENAME TO code_repository;
ALTER SEQUENCE IF EXISTS code_repositories_id_seq RENAME TO code_repository_id_seq;
