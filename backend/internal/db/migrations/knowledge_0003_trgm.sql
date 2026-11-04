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
-- Index columns: title, content and the tag array rendered as text. These are
-- the three fields the Go candidate scorer reads (retrievalTextScore: title,
-- content, tags); a tag-only match must not be silently dropped just because
-- another row has a title/content hit. The multicolumn GIN index answers the
-- retrieval predicate with a BitmapOr of one index scan per constrained field.
-- eztest_text_array_to_string() (created by knowledge_0001) is IMMUTABLE, so
-- PostgreSQL accepts it as an index expression; the retrieval SQL calls the
-- same expression, so its parsed form matches the index.
--
-- Rollback (the migration runner is forward-only; run by hand if needed):
--   DELETE FROM schema_migrations WHERE version = 'knowledge_0003_trgm.sql';
--   DROP INDEX IF EXISTS idx_knowledge_item_trgm;
--   DROP EXTENSION IF EXISTS pg_trgm;
-- The schema_migrations row must be deleted too: without it the forward-only
-- runner still treats this migration as applied and never recreates the
-- objects. Dropping the extension removes trigram support still used by the
-- retrieval SQL, so roll it back only together with the code change.
--
-- No in-migration advisory lock is needed: db.Migrate holds its session
-- advisory lock (0x455A5430) for the whole run, so two schema migrations in the
-- same test database cannot reach this CREATE EXTENSION concurrently. The
-- earlier transaction-scoped lock (0x455A5431) was redundant.
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE INDEX idx_knowledge_item_trgm ON knowledge_item
    USING GIN (title public.gin_trgm_ops, content public.gin_trgm_ops,
        eztest_text_array_to_string(tags) public.gin_trgm_ops);
