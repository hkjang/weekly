-- Failed local logins, so repeated guessing can be slowed down and stopped.
--
-- Only failures are recorded, and only for the throttling window: this is a
-- counter, not a second audit log. Successful logins already land in audit_logs
-- and a successful login clears the account's failures.
CREATE TABLE IF NOT EXISTS login_attempts (
  id bigserial PRIMARY KEY,
  username varchar(120) NOT NULL,
  ip_address inet,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Stored in PostgreSQL rather than in process memory so the limit survives a
-- restart. An in-memory counter would hand an attacker a reset for free and
-- would count separately in each replica.
CREATE INDEX IF NOT EXISTS idx_login_attempts_username
  ON login_attempts(lower(username), created_at DESC);
CREATE INDEX IF NOT EXISTS idx_login_attempts_ip
  ON login_attempts(ip_address, created_at DESC) WHERE ip_address IS NOT NULL;

-- A work item that was merged into another keeps its row and its normalized
-- key. That is what makes the merge survive: the key is how a title is resolved
-- to an identity on every save, so deleting the row would let the next report
-- recreate the task and quietly undo the merge.
ALTER TABLE work_items
  ADD COLUMN IF NOT EXISTS merged_into_id bigint REFERENCES work_items(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_work_items_merged
  ON work_items(merged_into_id) WHERE merged_into_id IS NOT NULL;

-- A pinned snapshot keeps the identity its author chose, instead of the one the
-- title normalizer would derive. Without it a split would be undone by the next
-- save of the same report, because that save re-derives every link from titles.
ALTER TABLE report_items
  ADD COLUMN IF NOT EXISTS work_item_pinned boolean NOT NULL DEFAULT false;

INSERT INTO app_settings(key, value, secret) VALUES
 ('auth.max_login_attempts', '10', false),
 ('auth.lockout_minutes', '15', false),
 ('auth.max_login_attempts_per_ip', '0', false)
ON CONFLICT (key) DO NOTHING;
