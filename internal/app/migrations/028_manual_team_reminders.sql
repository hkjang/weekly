-- A queued reminder may come from the weekly schedule or from an explicit
-- click in personal settings. Delivery revalidates both kinds against the
-- requester's current role and organisation, but only scheduled mail depends
-- on the automatic-reminder preference still being enabled.
ALTER TABLE team_reminder_deliveries
  ADD COLUMN origin varchar(10) NOT NULL DEFAULT 'AUTO'
  CHECK (origin IN ('AUTO', 'MANUAL'));
