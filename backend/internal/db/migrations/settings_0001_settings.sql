CREATE TABLE system_setting (
    key TEXT PRIMARY KEY,
    value_json JSONB NOT NULL,
    category TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('string', 'number', 'boolean', 'json')),
    is_secret BOOLEAN NOT NULL DEFAULT false,
    restart_required BOOLEAN NOT NULL DEFAULT false,
    description TEXT NOT NULL DEFAULT '',
    version INT NOT NULL DEFAULT 1,
    updated_by BIGINT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE system_setting_revision (
    id BIGINT PRIMARY KEY,
    setting_key TEXT NOT NULL REFERENCES system_setting(key) ON DELETE CASCADE,
    old_value JSONB NULL,
    new_value JSONB NOT NULL,
    actor_id BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_system_setting_revision_key_created
    ON system_setting_revision (setting_key, created_at DESC);
