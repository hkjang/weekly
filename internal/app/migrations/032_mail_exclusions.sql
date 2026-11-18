-- 주간보고 작성 권고 메일을 받을지 개인이 고르고, 메일을 받지 않는 조직을 둔다.
--
-- A team reminder was the one mail this product sent to somebody who had not
-- asked for it: a leader turned it on and every unsubmitted member received it,
-- under that leader's name. team_reminder is the member's own say, and it is
-- off until they turn it on — the same rule the deadline digest has followed
-- since 030, and for the same reason. Every existing account starts off too.
-- No row in user_mail_settings means the default, so every query reads a
-- missing row as false.
ALTER TABLE user_mail_settings
  ADD COLUMN IF NOT EXISTS team_reminder boolean NOT NULL DEFAULT false;

-- An organisation whose members receive no mail from this service at all:
-- not a reminder, not a deadline digest, not a copy of their own report. Asked
-- for 임원실 — executives are not chased by a weekly-report robot — and kept
-- as a flag rather than a name so a rename does not quietly lift it.
--
-- It covers the organisation and everything beneath it, the same way a
-- leader's scope does: a 비서실 under 임원실 is still 임원실.
ALTER TABLE organizations
  ADD COLUMN IF NOT EXISTS mail_blocked boolean NOT NULL DEFAULT false;

UPDATE organizations SET mail_blocked = true
WHERE regexp_replace(btrim(name), '\s+', '', 'g') = '임원실';

-- Whether mail to somebody in this organisation is refused, walking up the
-- chart. Bounded like every other walk over parent_id (orgtree.go): the column
-- can hold a loop and an unbounded walk over one never finishes.
CREATE OR REPLACE FUNCTION organization_mail_blocked(start_id bigint)
RETURNS boolean LANGUAGE sql STABLE AS $$
  WITH RECURSIVE up(id, depth) AS (
    SELECT start_id, 0 WHERE start_id IS NOT NULL
    UNION ALL
    SELECT o.parent_id, up.depth + 1 FROM organizations o JOIN up ON o.id = up.id
    WHERE o.parent_id IS NOT NULL AND up.depth < 16)
  SELECT EXISTS (SELECT 1 FROM up JOIN organizations o ON o.id = up.id WHERE o.mail_blocked)
$$;
