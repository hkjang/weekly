-- A silent OIDC attempt has a different failure path from a login the user
-- explicitly started. The provider's "login_required" answer must return to
-- the ordinary login screen instead of replacing the SPA with an error page.
ALTER TABLE oidc_login_states
  ADD COLUMN IF NOT EXISTS silent boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS return_to text NOT NULL DEFAULT '';
