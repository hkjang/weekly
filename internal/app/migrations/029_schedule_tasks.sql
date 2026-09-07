-- 부서 월간 업무 상황판: 날짜가 있는 할 일과 그 체크박스.
--
-- work_items already holds a task with a due_date, and it is the wrong shape
-- for this. A work item is derived from what somebody wrote in a weekly report
-- — one row per owner per normalized title — and it carries exactly one
-- deadline. A department that runs on a schedule has many dated items under one
-- task ("감사 준비" has a kickoff, a dry run, a submission), items that belong
-- to nobody's weekly report yet, and items a team leader lays out for the month
-- before anyone has reported anything at all. None of those can be expressed as
-- a due_date on a derived row.
--
-- So this is its own table, and it points at work_items rather than replacing
-- them: a scheduled item that belongs to a tracked task carries the link, and
-- the board and 업무 추적 then talk about the same work instead of two.
CREATE TABLE IF NOT EXISTS schedule_tasks (
  id bigserial PRIMARY KEY,
  -- The assignee. A team leader planning the month writes rows owned by other
  -- people, which is why created_by is separate.
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_by bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title varchar(200) NOT NULL,
  category varchar(80) NOT NULL DEFAULT '',
  -- A span, not a day. Department work is "9/7 ~ 9/11 감사 준비" as often as it
  -- is a single date, and a board that can only place one square per item draws
  -- the week-long ones as a dot on their first day.
  start_date date NOT NULL,
  end_date date NOT NULL,
  -- The checkbox. A timestamp rather than a boolean because a status board is
  -- asked "when was this done, and by whom" the moment anyone disagrees with
  -- it, and a boolean cannot answer either.
  done_at timestamptz,
  done_by bigint REFERENCES users(id) ON DELETE SET NULL,
  work_item_id bigint REFERENCES work_items(id) ON DELETE SET NULL,
  -- The ITSM service request this line came from, stored as the number and
  -- never as a URL: the address a person clicks is a setting, so an ITSM that
  -- moves to a new portal is one setting away from every row pointing at the
  -- right place rather than a data migration.
  sr_id varchar(64) NOT NULL DEFAULT '',
  -- URGENT / IMPORTANT / NEEDED / NORMAL, drawn red / green / blue / black.
  -- A wall board is read from three metres away, where colour is the only
  -- thing that carries at all; the word is kept beside it for the people who
  -- cannot tell those four colours apart.
  priority varchar(16) NOT NULL DEFAULT 'NORMAL',
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT schedule_tasks_span CHECK (end_date >= start_date)
);

-- The board reads one month at a time for one organisation, so the range is the
-- filter and the owner is the grouping.
CREATE INDEX IF NOT EXISTS idx_schedule_tasks_range ON schedule_tasks(start_date, end_date);
CREATE INDEX IF NOT EXISTS idx_schedule_tasks_owner ON schedule_tasks(user_id, start_date);
-- Open items, which is what "지연" and "오늘" are counted from.
CREATE INDEX IF NOT EXISTS idx_schedule_tasks_open ON schedule_tasks(end_date) WHERE done_at IS NULL;
-- "이 SR이 이미 달력에 있나" is asked every time somebody adds one.
CREATE INDEX IF NOT EXISTS idx_schedule_tasks_sr ON schedule_tasks(sr_id) WHERE sr_id <> '';
