CREATE TABLE sut (
    id BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    intro TEXT NOT NULL DEFAULT '',
    created_by BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_sut_name ON sut (lower(name));

CREATE TABLE sut_environment (
    id BIGINT PRIMARY KEY,
    sut_id BIGINT NOT NULL REFERENCES sut(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    base_url TEXT NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT false,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_sut_environment_name ON sut_environment (sut_id, lower(name));
CREATE UNIQUE INDEX uk_sut_environment_default ON sut_environment (sut_id) WHERE is_default;

CREATE TABLE sut_page (
    id BIGINT PRIMARY KEY,
    sut_id BIGINT NOT NULL REFERENCES sut(id) ON DELETE CASCADE,
    environment_id BIGINT NOT NULL REFERENCES sut_environment(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    route_template TEXT NOT NULL,
    page_type TEXT NOT NULL DEFAULT 'page',
    auth_role TEXT NOT NULL DEFAULT '',
    module TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'approved' CHECK (status IN ('approved', 'proposed', 'rejected')),
    source TEXT NOT NULL DEFAULT 'admin' CHECK (source IN ('admin', 'ai', 'import')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_sut_page_template ON sut_page (environment_id, route_template);
CREATE INDEX idx_sut_page_sut ON sut_page (sut_id);

CREATE TABLE sut_api (
    id BIGINT PRIMARY KEY,
    sut_id BIGINT NOT NULL REFERENCES sut(id) ON DELETE CASCADE,
    environment_id BIGINT NOT NULL REFERENCES sut_environment(id) ON DELETE CASCADE,
    method TEXT NOT NULL CHECK (method IN ('GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS')),
    path_template TEXT NOT NULL,
    name TEXT NOT NULL,
    auth_role TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('code', 'openapi', 'ai', 'manual', 'import')),
    status TEXT NOT NULL DEFAULT 'approved' CHECK (status IN ('approved', 'proposed', 'rejected')),
    schema_ref TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_sut_api_route ON sut_api (environment_id, method, path_template);
CREATE INDEX idx_sut_api_sut ON sut_api (sut_id);

CREATE TABLE target_database (
    id BIGINT PRIMARY KEY,
    sut_id BIGINT NOT NULL REFERENCES sut(id) ON DELETE CASCADE,
    environment_id BIGINT NOT NULL REFERENCES sut_environment(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'postgres' CHECK (kind IN ('postgres')),
    dsn TEXT NOT NULL,
    username TEXT NOT NULL,
    password TEXT NOT NULL DEFAULT '',
    read_only BOOLEAN NOT NULL DEFAULT true,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_target_database_name ON target_database (environment_id, lower(name));

CREATE TABLE test_account (
    id BIGINT PRIMARY KEY,
    sut_id BIGINT NOT NULL REFERENCES sut(id) ON DELETE CASCADE,
    environment_id BIGINT NOT NULL REFERENCES sut_environment(id) ON DELETE CASCADE,
    username TEXT NOT NULL,
    password TEXT NOT NULL,
    roles JSONB NOT NULL DEFAULT '[]'::jsonb,
    notes TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_test_account_username ON test_account (environment_id, lower(username));

CREATE TABLE code_repository (
    id BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    branch TEXT NOT NULL DEFAULT '',
    include JSONB NOT NULL DEFAULT '[]'::jsonb,
    exclude JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_by BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_code_repository_name ON code_repository (lower(name));
