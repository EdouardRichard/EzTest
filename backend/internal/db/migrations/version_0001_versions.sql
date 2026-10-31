CREATE TABLE version (
    id BIGINT PRIMARY KEY,
    title TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'initial' CHECK (status IN ('initial', 'running', 'completed')),
    auto_design_new_requirements BOOLEAN NOT NULL DEFAULT false,
    closed_at TIMESTAMPTZ NULL,
    close_reason TEXT NOT NULL DEFAULT '',
    close_run_id BIGINT NULL,
    created_by BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_version_created_at ON version (created_at DESC);

CREATE TABLE version_sut (
    id BIGINT PRIMARY KEY,
    version_id BIGINT NOT NULL REFERENCES version(id) ON DELETE CASCADE,
    sut_id BIGINT NOT NULL,
    environment_id BIGINT NOT NULL,
    code_repo_id BIGINT NULL,
    scope JSONB NOT NULL DEFAULT '{"pages":[],"apis":[]}'::jsonb,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_version_sut ON version_sut (version_id, sut_id);
CREATE INDEX idx_version_sut_sut ON version_sut (sut_id);
CREATE INDEX idx_version_sut_environment ON version_sut (environment_id);
CREATE INDEX idx_version_sut_repo ON version_sut (code_repo_id);

CREATE TABLE version_sut_resource (
    id BIGINT PRIMARY KEY,
    version_sut_id BIGINT NOT NULL REFERENCES version_sut(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('database', 'account')),
    resource_id BIGINT NOT NULL,
    role TEXT NOT NULL DEFAULT '',
    selected BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_version_sut_resource
    ON version_sut_resource (version_sut_id, kind, resource_id, role);
