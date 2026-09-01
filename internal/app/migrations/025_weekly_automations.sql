-- Personal weekly-report automations.
--
-- Kept apart from user_mail_settings: cloning a report is not a mail
-- preference, and a deployment may use it without configuring SMTP at all.
CREATE TABLE IF NOT EXISTS user_weekly_preferences (
  user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  auto_clone_previous boolean NOT NULL DEFAULT false,
  -- The automatic action is handled once per reporting week. Keeping the
  -- marker after a generated draft is deleted prevents the scheduler from
  -- recreating it every minute against the writer's explicit deletion.
  auto_clone_processed_week date,
  team_reminder_enabled boolean NOT NULL DEFAULT false,
  team_reminder_weekday varchar(9) NOT NULL DEFAULT 'FRIDAY'
    CHECK (team_reminder_weekday IN
      ('SUNDAY','MONDAY','TUESDAY','WEDNESDAY','THURSDAY','FRIDAY','SATURDAY')),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- A reminder is not attached to a report: its recipient may not have created
-- one yet. It therefore has its own durable queue rather than a nullable fake
-- report in report_mail_deliveries.
CREATE TABLE IF NOT EXISTS team_reminder_deliveries (
  id bigserial PRIMARY KEY,
  requested_by bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  recipient_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  week_start date NOT NULL,
  address varchar(320) NOT NULL,
  status varchar(20) NOT NULL DEFAULT 'QUEUED'
    CHECK (status IN ('QUEUED','SENT','FAILED')),
  attempts integer NOT NULL DEFAULT 0,
  error_message text NOT NULL DEFAULT '',
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz,
  -- An organisation manager and a team leader may cover the same person. The
  -- service sends one useful reminder, not one copy per level in the tree.
  UNIQUE(recipient_user_id, week_start)
);

CREATE INDEX IF NOT EXISTS idx_team_reminder_due
  ON team_reminder_deliveries(next_attempt_at, created_at)
  WHERE status = 'QUEUED';
CREATE INDEX IF NOT EXISTS idx_team_reminder_requester
  ON team_reminder_deliveries(requested_by, created_at DESC);
