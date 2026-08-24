-- The search's first pass asks only for the fields that outrank a paragraph:
-- a work item's title and its category. Every other searchable column already
-- had a trigram index; category did not, so that pass fell back to a sequential
-- scan of report_items no matter how selective the query was.
--
-- Measured on 109,175 items: a four-character query went from an 84 ms full
-- scan to a 0.45 ms index lookup. Two-character queries stay slow either way —
-- trigrams cannot be selective below three characters — and that is intrinsic,
-- not something another index fixes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
    CREATE INDEX IF NOT EXISTS idx_report_items_category_trgm
      ON report_items USING gin (category gin_trgm_ops);
  ELSE
    RAISE NOTICE 'pg_trgm is unavailable; category search stays a full scan';
  END IF;
END $$;
