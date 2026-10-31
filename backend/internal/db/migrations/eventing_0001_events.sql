CREATE TABLE event_channel (
    channel TEXT PRIMARY KEY,
    last_seq BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE event (
    channel TEXT NOT NULL REFERENCES event_channel(channel),
    seq BIGINT NOT NULL,
    type TEXT NOT NULL,
    envelope JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel, seq)
);

CREATE INDEX idx_event_created_at ON event (created_at);
