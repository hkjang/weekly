-- Dependencies between work items.
--
-- The report says a task is stalled. It cannot say the task is stalled because
-- another team has not finished something, which is the most common reason work
-- stops in an organisation of any size and the one a status meeting rediscovers
-- every week by asking around the room.
--
-- One edge, not two. "A depends on B" and "B blocks A" are the same fact seen
-- from two ends, so storing both kinds would let the two disagree — a graph that
-- contradicts itself is worse than no graph. The row records the blocker and the
-- blocked, and each screen reads it from where it stands.

CREATE TABLE IF NOT EXISTS work_item_links (
  id          BIGSERIAL PRIMARY KEY,
  -- blocker_id must finish (or move) before blocked_id can proceed.
  blocker_id  BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
  blocked_id  BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
  -- Why one waits on the other. A dependency without a reason cannot be
  -- disputed, and the whole value of declaring it is that the other team can.
  note        TEXT NOT NULL DEFAULT '',
  created_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- A task cannot wait on itself, and the same pair cannot be declared twice.
  CONSTRAINT work_item_links_not_self CHECK (blocker_id <> blocked_id),
  CONSTRAINT work_item_links_unique UNIQUE (blocker_id, blocked_id)
);

-- "무엇이 이 업무를 막고 있나" — read from the waiting end, which is how the
-- work item screen and the stalled-task explanation both ask.
CREATE INDEX IF NOT EXISTS idx_work_item_links_blocked
  ON work_item_links(blocked_id, id);

-- "이 업무가 무엇을 막고 있나" — read from the other end, which is how a
-- bottleneck is counted: one task with many rows here is the bottleneck.
CREATE INDEX IF NOT EXISTS idx_work_item_links_blocker
  ON work_item_links(blocker_id, id);
