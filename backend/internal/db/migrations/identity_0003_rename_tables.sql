-- D34: table naming normalization (plural -> singular). Fields/constraints/indexes unchanged.
ALTER TABLE IF EXISTS users RENAME TO user_account;
ALTER SEQUENCE IF EXISTS users_id_seq RENAME TO user_account_id_seq;
ALTER TABLE IF EXISTS auth_tokens RENAME TO auth_token;
ALTER SEQUENCE IF EXISTS auth_tokens_id_seq RENAME TO auth_token_id_seq;
