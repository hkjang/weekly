-- Retries that outlive an ordinary outage.
--
-- Measured on a deployment: with the attempts thirty seconds apart, a relay
-- that was unreachable for two and a half minutes used the whole budget and
-- the report was marked failed for good. A relay restart takes longer than
-- that. The budget has to be spent over hours, not minutes, or it is not a
-- retry policy — it is a slightly delayed give-up.
--
-- It also unblocks the queue. The claim took the oldest queued row every tick,
-- so one address the relay refuses held up everybody else's mail until it had
-- burned through its attempts. A row that has just failed is no longer due, so
-- the next tick reaches past it.
ALTER TABLE report_mail_deliveries
  ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NOT NULL DEFAULT now();

DROP INDEX IF EXISTS idx_report_mail_queue;
CREATE INDEX IF NOT EXISTS idx_report_mail_due
  ON report_mail_deliveries(next_attempt_at, created_at) WHERE status = 'QUEUED';
