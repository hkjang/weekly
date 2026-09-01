-- Which team members a leader wants beside their own weekly report.
--
-- This is a preference, not copied report content. The source report remains
-- owned by its writer and is read again for the same week whenever the
-- leader's report is opened. That keeps work-item ownership and organisation
-- analytics from counting the same work twice.
CREATE TABLE IF NOT EXISTS user_report_inclusions (
  owner_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  member_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(owner_user_id, member_user_id),
  CONSTRAINT user_report_inclusions_not_self CHECK (owner_user_id <> member_user_id)
);

-- PostgreSQL does not index the referencing side of a foreign key. User
-- removal checks member_user_id too, while the primary key begins with owner.
CREATE INDEX IF NOT EXISTS idx_user_report_inclusions_member
  ON user_report_inclusions(member_user_id, owner_user_id);
