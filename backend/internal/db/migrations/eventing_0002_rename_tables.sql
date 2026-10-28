-- D34: table naming normalization (plural -> singular). Fields/constraints/indexes unchanged.
-- event_channel has a TEXT primary key, so there is no owned sequence to rename.
ALTER TABLE IF EXISTS event_channels RENAME TO event_channel;
ALTER TABLE IF EXISTS events RENAME TO event;
ALTER SEQUENCE IF EXISTS events_id_seq RENAME TO event_id_seq;
