-- 마감 임박 알림: 상황판에 등록된 일정 중 종료일이 코앞인 것들을 담당자에게.
--
-- The board says what a department is doing this month. Nobody stands in front
-- of it every morning, and a line put there three weeks ago is a line nobody
-- has thought about since. What the assignee needs is the short list: what is
-- due in the next few days, grouped by how many days are left.
--
-- Off by default and per person, because a mail nobody asked for is the fastest
-- way to make every later mail from this product unread.
ALTER TABLE user_mail_settings
  ADD COLUMN IF NOT EXISTS schedule_reminder boolean NOT NULL DEFAULT false;

-- One digest per person per day, and the unique key is what makes that true.
--
-- The worker runs every minute and the service may be restarted or replicated;
-- without this, "send today's digest" would mean "send it again" on the next
-- tick. The row is the record that the day has been dealt with, whether the
-- relay took the message or refused it.
CREATE TABLE IF NOT EXISTS schedule_reminder_deliveries (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  -- The service-timezone day this digest speaks for. A board hanging in one
  -- country must not get two digests because somebody read it from another.
  reminder_on date NOT NULL,
  address varchar(320) NOT NULL,
  status varchar(20) NOT NULL DEFAULT 'QUEUED'
    CHECK (status IN ('QUEUED','SENT','FAILED')),
  attempts integer NOT NULL DEFAULT 0,
  error_message text NOT NULL DEFAULT '',
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz,
  UNIQUE(user_id, reminder_on)
);

CREATE INDEX IF NOT EXISTS idx_schedule_reminder_due
  ON schedule_reminder_deliveries(next_attempt_at, created_at)
  WHERE status = 'QUEUED';
