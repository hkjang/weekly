-- Sending a finished weekly report to an address the writer chose.
--
-- Two tables rather than columns on users, because the two have different
-- lifetimes: the preference is a setting the writer edits, and the delivery is
-- a record of one attempt that has to survive the preference being changed or
-- turned off. Reading "why did I not get last week's mail" needs the second.

CREATE TABLE IF NOT EXISTS user_mail_settings (
  user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  address varchar(320) NOT NULL DEFAULT '',
  on_submit boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- One row per attempt to deliver one report. Queued inside the submit
-- transaction so a relay that is down cannot fail a submission, and kept
-- afterwards so the writer can see what happened without asking an operator to
-- read a log.
CREATE TABLE IF NOT EXISTS report_mail_deliveries (
  id bigserial PRIMARY KEY,
  report_id bigint NOT NULL REFERENCES weekly_reports(id) ON DELETE CASCADE,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  address varchar(320) NOT NULL,
  status varchar(20) NOT NULL DEFAULT 'QUEUED' CHECK (status IN ('QUEUED','SENT','FAILED')),
  attempts integer NOT NULL DEFAULT 0,
  error_message text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz
);

-- The worker reads the head of the queue; the profile screen reads one writer's
-- recent attempts newest first.
CREATE INDEX IF NOT EXISTS idx_report_mail_queue
  ON report_mail_deliveries(status, created_at) WHERE status = 'QUEUED';
CREATE INDEX IF NOT EXISTS idx_report_mail_user
  ON report_mail_deliveries(user_id, created_at DESC);

-- mail.security is NONE, STARTTLS or TLS. An internal relay on port 25 with no
-- authentication is the common case in the networks this product runs in, so
-- NONE is the default and a username is what turns authentication on.
INSERT INTO app_settings(key, value, secret) VALUES
 ('mail.enabled', 'false', false),
 ('mail.host', '', false),
 ('mail.port', '25', false),
 ('mail.security', 'NONE', false),
 ('mail.username', '', false),
 ('mail.password', '', true),
 ('mail.from', '', false),
 ('mail.from_name', 'Weekly', false),
 ('mail.timeout_seconds', '20', false),
 ('mail.max_attempts', '5', false)
ON CONFLICT (key) DO NOTHING;
