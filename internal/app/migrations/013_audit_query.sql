-- Making the audit trail answerable.
--
-- The only way to read it was the most recent 500 rows with no filter, no date
-- range and no paging, while the table itself grew without limit. A few hundred
-- people generate that many entries in days, so "who approved this report, and
-- when" became unanswerable almost immediately even though the answer was
-- stored.

-- The action filter is a prefix match, so the index needs pattern operators.
-- A default btree on a varchar column only answers LIKE 'x%' when the database
-- collation is C; under the usual en_US.utf8 or ko_KR.utf8 it falls back to a
-- sequential scan, which is exactly what this index exists to avoid.
CREATE INDEX IF NOT EXISTS idx_audit_action_created
  ON audit_logs(action varchar_pattern_ops, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_audit_resource
  ON audit_logs(resource_type, resource_id, created_at DESC);

-- Not for the API, which filters the actor by name: this one is for the
-- referential check when a user row is deleted, since actor_id is ON DELETE SET
-- NULL and would otherwise scan the whole table for every removed account.
CREATE INDEX IF NOT EXISTS idx_audit_actor
  ON audit_logs(actor_id) WHERE actor_id IS NOT NULL;

-- Kept for a year by default, which is the shortest retention most internal
-- audit policies accept. Zero keeps everything, for deployments whose policy
-- says so; growth is then the operator's deliberate choice rather than an
-- accident of there being no setting.
INSERT INTO app_settings(key, value, secret) VALUES
 ('audit.retention_days', '365', false)
ON CONFLICT (key) DO NOTHING;
