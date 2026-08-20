-- Indexes on the report-scoped foreign keys.
--
-- PostgreSQL indexes the referenced side of a foreign key, never the
-- referencing side, so report_id had no index at all. Every report load, every
-- save, every PPTX export and every period rollup filters on it, and each of
-- those was a sequential scan of the whole table.
--
-- Measured on 80,000 report items (200 people, 80 weeks, 5 items each): the
-- lookup behind one report load went from 5.07ms and 1,144 buffers to 0.074ms
-- and 11. The cost is paid on every request, which is why this is worth a
-- migration on its own.
--
-- Built without CONCURRENTLY because the migration runner wraps each file in a
-- transaction, and because a table of this size builds in well under a second.
-- A deployment large enough for that to matter should create these by hand with
-- CONCURRENTLY before upgrading; the IF NOT EXISTS then makes this a no-op.

-- sort_order and id are included because every read of this table is ordered by
-- them, which lets the index answer the ordering as well as the filter.
CREATE INDEX IF NOT EXISTS idx_report_items_report
  ON report_items(report_id, sort_order, id);

CREATE INDEX IF NOT EXISTS idx_report_comments_report
  ON report_comments(report_id, created_at);

CREATE INDEX IF NOT EXISTS idx_report_status_history_report
  ON report_status_history(report_id, id);

-- actor_id is ON DELETE RESTRICT, so removing a user makes PostgreSQL check
-- this table for referencing rows. Without an index that check is a sequential
-- scan, and it is the reason the integration test could not delete its own
-- fixture user until the history rows were removed first.
CREATE INDEX IF NOT EXISTS idx_report_status_history_actor
  ON report_status_history(actor_id);

-- The submission deadline, which until now was a constant in a SQL expression:
-- "submitted_at::date <= week_start + 7". Two problems. It is an organizational
-- policy that no operator could change, and the cast ran in the database session
-- timezone (UTC) while every other week boundary in the product is computed in
-- service.timezone, so a report handed in at 08:00 on the day after the deadline
-- was dated to the previous day and counted as on time.
--
-- Expressed as an offset from the week start so it reads the way a policy is
-- stated: "주차 시작일로부터 N일 뒤 H시까지". The defaults reproduce the old rule
-- exactly (end of week_start + 7 days), so no deployment's numbers move except
-- the ones that were wrong.
INSERT INTO app_settings(key, value, secret) VALUES
 ('workflow.deadline_days', '7', false),
 ('workflow.deadline_hour', '24', false)
ON CONFLICT (key) DO NOTHING;
