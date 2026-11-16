-- Keycloak 부서(department)로 소속 조직을 자동 지정.
--
-- An account's organisation decides which team leader sees its reports, and
-- until now the only way to set it was an administrator choosing it by hand for
-- every person SSO created — in the default organisation, where no team leader
-- looks. The directory already knows the answer: Keycloak carries a department
-- attribute on each person, and a User Attribute mapper puts it in the token.
--
-- sso_department is what the provider last said, kept whether or not the
-- administrator has switched the mapping on. It is the evidence for every later
-- question — why is this person here, which departments have no organisation —
-- and without it the screen could only guess.
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS sso_department varchar(255),
  ADD COLUMN IF NOT EXISTS sso_department_at timestamptz,
  -- An administrator's own choice of organisation, which the department sync
  -- must not undo at the person's next sign-in.
  ADD COLUMN IF NOT EXISTS organization_locked boolean NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS idx_users_sso_department
  ON users (lower(btrim(sso_department)))
  WHERE sso_department IS NOT NULL;

-- A department value the administrator has tied to an organisation by hand.
--
-- Names are not unique — two divisions can each have a 기획팀 — and the value
-- Keycloak sends is often neither the organisation's name nor its code. This is
-- the answer for both: the administrator says which organisation a value means,
-- once, and it wins over every automatic match.
--
-- department is the normalised key (trimmed, inner spaces collapsed, lower
-- case) so the same value typed two ways is one mapping.
CREATE TABLE IF NOT EXISTS organization_department_aliases (
  department varchar(255) PRIMARY KEY,
  organization_id bigint NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  updated_by bigint REFERENCES users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Seeded so the settings screen has a valid value to send back: it saves every
-- box it drew, and an empty department_sync is not one of the three modes.
INSERT INTO app_settings(key, value, secret) VALUES
 ('oidc.department_claim', 'department', false),
 ('oidc.department_sync', 'OFF', false),
 ('oidc.department_create', 'false', false)
ON CONFLICT (key) DO NOTHING;
