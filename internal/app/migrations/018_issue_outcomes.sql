-- What happened to an obstacle, which the product has never known.
--
-- An issue appears in a weekly report and, some weeks later, stops appearing.
-- Everything up to now measures the middle: 이슈 3주 지속, and the digest
-- scores it. Nothing records the ending. Resolved and abandoned look identical
-- in the data, so the one question an organisation would most want answered —
-- how long does this kind of obstacle take us to clear — cannot be asked.
--
-- The middle is already derivable from report_items, so nothing here duplicates
-- it. Only the ending needs a person, because only a person knows whether the
-- blank issue field means the problem went away or the reporter gave up on
-- writing it down.
CREATE TABLE IF NOT EXISTS work_item_issue_outcomes (
  id            bigserial PRIMARY KEY,
  work_item_id  bigint NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
  -- The week the issue stopped being reported, and the week it first was. The
  -- span between them is the answer this table exists to collect.
  ended_week    date NOT NULL,
  started_week  date NOT NULL,
  weeks         integer NOT NULL,
  outcome       varchar(20) NOT NULL,
  -- The wording at the end, kept so a reader can tell what was cleared without
  -- walking back through the weekly rows.
  issue_text    text NOT NULL DEFAULT '',
  recorded_by   bigint REFERENCES users(id) ON DELETE SET NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);

-- One ending per work item per week. Saving the same report twice is ordinary
-- and must not record the same resolution twice.
CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_outcomes_item_week
  ON work_item_issue_outcomes(work_item_id, ended_week);
CREATE INDEX IF NOT EXISTS idx_issue_outcomes_week
  ON work_item_issue_outcomes(ended_week);
