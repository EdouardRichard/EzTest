-- D34: table naming normalization (plural -> singular). Fields/constraints/indexes unchanged.
-- storage_object has a TEXT primary key, so there is no owned sequence to rename.
ALTER TABLE IF EXISTS storage_objects RENAME TO storage_object;
