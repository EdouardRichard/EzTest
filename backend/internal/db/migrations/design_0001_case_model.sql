-- P2 design context: scenario / logical case / immutable case version /
-- steps / assertions / sources / confirmations / design rounds / design scope.
-- See docs/superpowers/specs/2026-10-31-eztest-v02-p2-case-design-design.md §3.1.
-- Naming (Agent.md §6.9/D34, scheme A): singular tables, pk_/uk_/idx_ names.

CREATE TABLE case_scenario (
    id                      BIGINT NOT NULL,
    version_id              BIGINT NOT NULL,
    requirement_id          BIGINT NOT NULL,
    requirement_version_id  BIGINT NOT NULL,
    name                    TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    description             TEXT NOT NULL DEFAULT '',
    version_sut_ids         JSONB NOT NULL DEFAULT '[]'::jsonb
                            CHECK (jsonb_typeof(version_sut_ids) = 'array'),
    page_ids                JSONB NOT NULL DEFAULT '[]'::jsonb
                            CHECK (jsonb_typeof(page_ids) = 'array'),
    api_ids                 JSONB NOT NULL DEFAULT '[]'::jsonb
                            CHECK (jsonb_typeof(api_ids) = 'array'),
    sort_order              INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    created_by              BIGINT NULL,
    created_by_run_id       BIGINT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_case_scenario PRIMARY KEY (id),
    CONSTRAINT uk_case_scenario_requirement_version_name
        UNIQUE (requirement_version_id, name)
);

CREATE INDEX idx_case_scenario_requirement
    ON case_scenario (version_id, requirement_id, sort_order, id);
CREATE INDEX idx_case_scenario_requirement_version
    ON case_scenario (requirement_version_id);

CREATE TABLE case_entity (
    id                  BIGINT NOT NULL,
    scenario_id         BIGINT NOT NULL REFERENCES case_scenario (id),
    current_version_id  BIGINT NULL,
    status              TEXT NOT NULL DEFAULT 'draft'
                        CHECK (status IN ('draft', 'confirmed', 'stale')),
    created_by          BIGINT NULL,
    created_by_run_id   BIGINT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_case_entity PRIMARY KEY (id)
);

CREATE INDEX idx_case_entity_scenario ON case_entity (scenario_id, id);
CREATE INDEX idx_case_entity_current ON case_entity (current_version_id);
CREATE INDEX idx_case_entity_status ON case_entity (status);

CREATE TABLE case_version (
    id                      BIGINT NOT NULL,
    case_id                 BIGINT NOT NULL REFERENCES case_entity (id),
    revision                INTEGER NOT NULL CHECK (revision > 0),
    title                   TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    description             TEXT NOT NULL DEFAULT '',
    preconditions           TEXT NOT NULL DEFAULT '',
    test_data               JSONB NOT NULL DEFAULT '{}'::jsonb
                            CHECK (jsonb_typeof(test_data) = 'object'),
    priority                TEXT NOT NULL DEFAULT 'P2'
                            CHECK (priority IN ('P0', 'P1', 'P2', 'P3')),
    default_version_sut_id  BIGINT NULL,
    version_sut_ids         JSONB NOT NULL DEFAULT '[]'::jsonb
                            CHECK (jsonb_typeof(version_sut_ids) = 'array'),
    page_ids                JSONB NOT NULL DEFAULT '[]'::jsonb
                            CHECK (jsonb_typeof(page_ids) = 'array'),
    api_ids                 JSONB NOT NULL DEFAULT '[]'::jsonb
                            CHECK (jsonb_typeof(api_ids) = 'array'),
    status                  TEXT NOT NULL DEFAULT 'draft'
                            CHECK (status IN ('draft', 'confirmed', 'stale', 'superseded')),
    content_hash            TEXT NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    created_by              BIGINT NULL,
    created_by_run_id       BIGINT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_case_version PRIMARY KEY (id),
    CONSTRAINT uk_case_version_case_revision UNIQUE (case_id, revision)
);

CREATE INDEX idx_case_version_case ON case_version (case_id, id DESC);
CREATE INDEX idx_case_version_status ON case_version (status);

CREATE TABLE case_step (
    id                      BIGINT NOT NULL,
    case_version_id         BIGINT NOT NULL REFERENCES case_version (id),
    seq                     INTEGER NOT NULL CHECK (seq > 0),
    action                  TEXT NOT NULL CHECK (char_length(action) BETWEEN 1 AND 2000),
    expected                TEXT NOT NULL DEFAULT '',
    target_version_sut_id   BIGINT NULL,
    target_page_id          BIGINT NULL,
    target_api_id           BIGINT NULL,
    account_role            TEXT NOT NULL DEFAULT '' CHECK (char_length(account_role) <= 64),
    data                    JSONB NOT NULL DEFAULT '{}'::jsonb
                            CHECK (jsonb_typeof(data) = 'object'),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_case_step PRIMARY KEY (id),
    CONSTRAINT uk_case_step_version_seq UNIQUE (case_version_id, seq)
);

CREATE INDEX idx_case_step_version ON case_step (case_version_id, seq);

CREATE TABLE case_assertion (
    id                      BIGINT NOT NULL,
    case_version_id         BIGINT NOT NULL REFERENCES case_version (id),
    step_seq                INTEGER NOT NULL CHECK (step_seq > 0),
    kind                    TEXT NOT NULL
                            CHECK (kind IN ('ui', 'api', 'db', 'text', 'visual')),
    target                  TEXT NOT NULL DEFAULT '',
    expected                TEXT NOT NULL DEFAULT '',
    target_version_sut_id   BIGINT NULL,
    target_api_id           BIGINT NULL,
    database_id             BIGINT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_case_assertion PRIMARY KEY (id)
);

CREATE INDEX idx_case_assertion_version
    ON case_assertion (case_version_id, step_seq);

CREATE TABLE case_source (
    id                  BIGINT NOT NULL,
    case_version_id     BIGINT NOT NULL REFERENCES case_version (id),
    kind                TEXT NOT NULL
                        CHECK (kind IN ('requirement', 'code', 'page', 'api', 'knowledge')),
    ref_id              TEXT NOT NULL CHECK (char_length(ref_id) BETWEEN 1 AND 200),
    path                TEXT NOT NULL DEFAULT '',
    line_no             INTEGER NULL CHECK (line_no IS NULL OR line_no > 0),
    sha                 TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_case_source PRIMARY KEY (id)
);

CREATE INDEX idx_case_source_version ON case_source (case_version_id, kind);

CREATE TABLE case_confirmation (
    id                          BIGINT NOT NULL,
    case_version_id             BIGINT NOT NULL REFERENCES case_version (id),
    scope                       TEXT NOT NULL
                                CHECK (scope IN ('case', 'scenario', 'requirement')),
    target_scope_id             BIGINT NOT NULL,
    batch_id                    TEXT NOT NULL CHECK (char_length(batch_id) BETWEEN 1 AND 64),
    affected_case_version_ids   JSONB NOT NULL DEFAULT '[]'::jsonb
                                CHECK (jsonb_typeof(affected_case_version_ids) = 'array'),
    snapshot                    JSONB NOT NULL DEFAULT '{}'::jsonb
                                CHECK (jsonb_typeof(snapshot) = 'object'),
    confirmed_by                BIGINT NOT NULL,
    confirmed_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_case_confirmation PRIMARY KEY (id),
    CONSTRAINT uk_case_confirmation_version UNIQUE (case_version_id)
);

CREATE INDEX idx_case_confirmation_batch ON case_confirmation (batch_id);
CREATE INDEX idx_case_confirmation_actor
    ON case_confirmation (confirmed_by, confirmed_at DESC);

CREATE TABLE design_round (
    id                      BIGINT NOT NULL,
    version_id              BIGINT NOT NULL,
    conversation_id         BIGINT NOT NULL,
    activation              TEXT NOT NULL
                            CHECK (activation IN ('manual', 'auto_gap', 'auto_change', 'auto_new')),
    requirement_ids         JSONB NOT NULL DEFAULT '[]'::jsonb
                            CHECK (jsonb_typeof(requirement_ids) = 'array'),
    requirement_version_ids JSONB NOT NULL DEFAULT '[]'::jsonb
                            CHECK (jsonb_typeof(requirement_version_ids) = 'array'),
    scope                   JSONB NOT NULL DEFAULT '{}'::jsonb
                            CHECK (jsonb_typeof(scope) = 'object'),
    status                  TEXT NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'completed')),
    created_by              BIGINT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_design_round PRIMARY KEY (id),
    CONSTRAINT uk_design_round_conversation UNIQUE (conversation_id)
);

CREATE INDEX idx_design_round_version ON design_round (version_id, id DESC);
CREATE INDEX idx_design_round_status ON design_round (status);

CREATE TABLE design_scope (
    id                      BIGINT NOT NULL,
    version_id              BIGINT NOT NULL,
    requirement_id          BIGINT NOT NULL,
    activated_by_round_id   BIGINT NULL REFERENCES design_round (id),
    activated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    status                  TEXT NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'closed')),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_design_scope PRIMARY KEY (id),
    CONSTRAINT uk_design_scope_version_requirement UNIQUE (version_id, requirement_id)
);

CREATE INDEX idx_design_scope_requirement ON design_scope (requirement_id);
