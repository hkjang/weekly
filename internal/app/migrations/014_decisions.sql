-- The decision log.
--
-- The record a weekly report cannot hold. A report says what happened; it does
-- not say who decided what, on what grounds, or what was supposed to follow. So
-- six months later the question "왜 이렇게 하기로 했더라" is answered by asking
-- whoever still remembers, and the handover screen — built precisely for the
-- moment nobody remembers — has nothing to show.
--
-- The roadmap settled the order of construction: an explicit record first, AI
-- suggestion afterwards. An audit trail whose completeness depends on a model's
-- recall is not an audit trail. This migration is the explicit half.

CREATE TABLE IF NOT EXISTS decisions (
  id            BIGSERIAL PRIMARY KEY,
  -- The work the decision is about. A decision always attaches to a task,
  -- because a decision with nothing to change is a note, and notes belong in
  -- the report. ON DELETE CASCADE: a task that never existed cannot have been
  -- decided about.
  work_item_id  BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
  -- Who recorded it, which is not necessarily who decided. The two differ often
  -- enough — a team lead writes down what a director said — that collapsing
  -- them would make the log wrong in exactly the cases it matters.
  recorded_by   BIGINT REFERENCES users(id) ON DELETE SET NULL,
  decided_by    VARCHAR(120) NOT NULL,
  decided_on    DATE NOT NULL,
  title         VARCHAR(240) NOT NULL,
  -- Why. The field this table exists for: a decision without its grounds is a
  -- fact nobody can revisit, and revisiting is the whole point.
  rationale     TEXT NOT NULL DEFAULT '',
  -- What was supposed to happen next, and by when. Kept as text rather than a
  -- task of its own: an action item that becomes a tracked task belongs in a
  -- weekly report, and one that does not is a sentence.
  follow_up     TEXT NOT NULL DEFAULT '',
  due_date      DATE,
  -- OPEN while the follow-up is outstanding, DONE when it has been carried out,
  -- SUPERSEDED when a later decision replaced this one. Superseded rows are
  -- never deleted: a decision that was reversed is more informative than one
  -- that was never made.
  status        VARCHAR(20) NOT NULL DEFAULT 'OPEN',
  supersedes_id BIGINT REFERENCES decisions(id) ON DELETE SET NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT decisions_status_check CHECK (status IN ('OPEN','DONE','SUPERSEDED')),
  CONSTRAINT decisions_no_self_supersede CHECK (supersedes_id IS NULL OR supersedes_id <> id)
);

-- The work item timeline reads this every time a task is opened, newest first.
CREATE INDEX IF NOT EXISTS idx_decisions_work_item
  ON decisions(work_item_id, decided_on DESC, id DESC);

-- "무엇이 아직 미해결인가" across a whole organisation, which is the question
-- the digest and the meeting agenda will ask of this table.
CREATE INDEX IF NOT EXISTS idx_decisions_open_due
  ON decisions(status, due_date) WHERE status = 'OPEN';

CREATE INDEX IF NOT EXISTS idx_decisions_supersedes
  ON decisions(supersedes_id) WHERE supersedes_id IS NOT NULL;
