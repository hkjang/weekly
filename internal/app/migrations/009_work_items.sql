-- WorkItem gives a task an identity that outlives the week it was reported in.
-- ReportItem becomes the weekly snapshot of a WorkItem rather than the only
-- record of it, which is what lets progress, ageing and carryover be tracked
-- across weeks instead of being re-guessed from the title every time.
CREATE TABLE IF NOT EXISTS work_items (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title varchar(240) NOT NULL,
  -- Lowercased letters and digits only, produced by the same normalizer the
  -- period rollup uses, so both features agree on what "the same task" means.
  normalized_key varchar(240) NOT NULL,
  category varchar(80) NOT NULL DEFAULT '',
  due_date date,
  parent_work_item_id bigint REFERENCES work_items(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- One task per owner per normalized title. An owner who genuinely runs two
-- tasks under one name renames one of them to split the history.
CREATE UNIQUE INDEX IF NOT EXISTS idx_work_items_owner_key
  ON work_items(user_id, normalized_key);
CREATE INDEX IF NOT EXISTS idx_work_items_owner ON work_items(user_id, id);

ALTER TABLE report_items
  ADD COLUMN IF NOT EXISTS work_item_id bigint REFERENCES work_items(id) ON DELETE SET NULL,
  -- Management ask is deliberately separate from issue: an issue describes what
  -- is wrong, an ask states the decision or support the reporting line needs.
  ADD COLUMN IF NOT EXISTS management_ask text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_report_items_work_item
  ON report_items(work_item_id) WHERE work_item_id IS NOT NULL;
