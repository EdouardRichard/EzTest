-- 0.2.1 knowledge retrieval: pg_trgm similarity for Chinese queries (spec §10.2).
--
-- The 0.2.0 search vector uses the 'simple' full-text configuration, which has
-- no dictionary or word breaker for Chinese: a free-text query only matches an
-- identical whole token, so near-miss Chinese (登陆 vs 登录页) and long CJK
-- sentences never recall. This migration adds the trigram extension and the GIN
-- trigram index the knowledge search top-K query is built on.
--
-- The extension is installed into public on purpose. Extensions are
-- database-scoped, but their functions/operator classes live in one schema;
-- installing into public keeps public.similarity() and public.gin_trgm_ops
-- deterministic for every session (including the integration harness, which
-- runs each test in its own schema with a narrowed search_path). pg_trgm is a
-- standard PostgreSQL contrib module (available in the official postgres
-- image); creating it needs superuser/a trusted-extension privilege. If it is
-- already installed in another schema, CREATE EXTENSION ... IF NOT EXISTS
-- leaves it there and this migration fails loudly on the qualified opclass
-- instead of silently deploying an unindexed search.
--
-- Index columns: title and content only. They are the two free-text columns the
-- retrieval top-K ranks with similarity(); tags stay in the Go-side score and
-- in the ts_rank fallback window and are deliberately not a third trigram
-- column (they are short, low-cardinality identifiers and would only widen the
-- index). A multicolumn GIN index answers the title/content OR predicate with a
-- BitmapOr of two index scans.
--
-- Rollback (the migration runner is forward-only; run by hand if needed):
--   DROP INDEX IF EXISTS idx_knowledge_item_trgm;
--   DROP EXTENSION IF EXISTS pg_trgm;
-- Dropping the extension removes trigram support still used by the retrieval
-- SQL, so roll it back only together with the code change.
--
-- Serialize the extension creation. CREATE EXTENSION IF NOT EXISTS is not
-- race-free: two sessions can both pass the existence check and the loser gets
-- a duplicate key error on pg_extension. The integration harness migrates one
-- schema per test and runs test packages in parallel against the same database,
-- so a transaction-scoped advisory lock makes the winner's commit visible
-- before the next schema runs this migration. The key (0x455A5431) is distinct
-- from the migration runner's session lock (0x455A5430).
SELECT pg_advisory_xact_lock(1163547697);

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE INDEX idx_knowledge_item_trgm ON knowledge_item
    USING GIN (title public.gin_trgm_ops, content public.gin_trgm_ops);
