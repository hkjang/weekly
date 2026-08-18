-- pg_trgm and pgvector are optional. A site that cannot install them, or whose
-- database role cannot create extensions, must still start, so every step here
-- is attempted and skipped rather than allowed to fail the migration.
DO $$
BEGIN
  CREATE EXTENSION IF NOT EXISTS pg_trgm;
EXCEPTION WHEN OTHERS THEN
  RAISE NOTICE 'pg_trgm is unavailable; content search falls back to a sequential scan';
END $$;

DO $$
BEGIN
  CREATE EXTENSION IF NOT EXISTS vector;
EXCEPTION WHEN OTHERS THEN
  RAISE NOTICE 'pgvector is unavailable; semantic search stays disabled';
END $$;

-- Trigram indexes turn the existing case-insensitive substring search from a
-- full scan into an index lookup, which is what the search was missing.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
    CREATE INDEX IF NOT EXISTS idx_report_items_title_trgm
      ON report_items USING gin (title gin_trgm_ops);
    CREATE INDEX IF NOT EXISTS idx_report_items_current_trgm
      ON report_items USING gin (current_result gin_trgm_ops);
    CREATE INDEX IF NOT EXISTS idx_report_items_next_trgm
      ON report_items USING gin (next_plan gin_trgm_ops);
    CREATE INDEX IF NOT EXISTS idx_report_items_issue_trgm
      ON report_items USING gin (issue gin_trgm_ops);
    CREATE INDEX IF NOT EXISTS idx_report_items_ask_trgm
      ON report_items USING gin (management_ask gin_trgm_ops);
    CREATE INDEX IF NOT EXISTS idx_weekly_reports_summary_trgm
      ON weekly_reports USING gin (summary gin_trgm_ops);
    CREATE INDEX IF NOT EXISTS idx_work_items_title_trgm
      ON work_items USING gin (title gin_trgm_ops);
  END IF;
END $$;

-- Embeddings are stored without a fixed dimension so the model can change
-- without a schema migration. Rows are filtered by model and dimension at query
-- time, because pgvector rejects comparisons between different dimensions.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector') THEN
    CREATE TABLE IF NOT EXISTS report_item_embeddings (
      report_item_id bigint PRIMARY KEY REFERENCES report_items(id) ON DELETE CASCADE,
      embedding vector NOT NULL,
      model varchar(120) NOT NULL,
      dimensions integer NOT NULL,
      content_hash char(64) NOT NULL,
      updated_at timestamptz NOT NULL DEFAULT now()
    );
    CREATE INDEX IF NOT EXISTS idx_report_item_embeddings_model
      ON report_item_embeddings(model, dimensions);
  END IF;
END $$;

INSERT INTO app_settings(key, value, secret) VALUES
 ('ai.embedding_enabled', 'false', false),
 ('ai.embedding_model', '', false),
 ('ai.embedding_endpoint', '', false),
 ('search.similarity_threshold', '35', false),
 ('search.semantic_threshold', '25', false)
ON CONFLICT (key) DO NOTHING;
