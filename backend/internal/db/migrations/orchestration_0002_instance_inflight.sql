-- P8 hardening: per-replica in-flight counters used by drain/rolling upgrade.
-- active_requests counts HTTP requests currently being served by the replica;
-- active_work counts agent runs currently executing in the replica process.
-- Written by the heartbeat loop and shown by GET /api/admin/instances and
-- GET /api/health/ready; never used for scheduling decisions.

ALTER TABLE api_instance
    ADD COLUMN active_requests INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN active_work     INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT ck_api_instance_active_requests CHECK (active_requests >= 0),
    ADD CONSTRAINT ck_api_instance_active_work CHECK (active_work >= 0);
