CREATE TABLE IF NOT EXISTS schema_migrations (
  version integer PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS organizations (
  id bigserial PRIMARY KEY,
  parent_id bigint REFERENCES organizations(id) ON DELETE SET NULL,
  name varchar(120) NOT NULL,
  code varchar(60) NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS users (
  id bigserial PRIMARY KEY,
  username varchar(120) NOT NULL UNIQUE,
  display_name varchar(120) NOT NULL,
  email varchar(255),
  password_hash text,
  role varchar(30) NOT NULL DEFAULT 'USER' CHECK (role IN ('USER','TEAM_LEADER','ORG_MANAGER','ADMIN')),
  organization_id bigint REFERENCES organizations(id) ON DELETE SET NULL,
  manager_id bigint REFERENCES users(id) ON DELETE SET NULL,
  oidc_subject varchar(255) UNIQUE,
  active boolean NOT NULL DEFAULT true,
  key_version integer NOT NULL DEFAULT 0,
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS app_settings (
  key varchar(120) PRIMARY KEY,
  value text NOT NULL,
  secret boolean NOT NULL DEFAULT false,
  updated_by bigint REFERENCES users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_sessions (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash char(64) NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  ip_address inet,
  user_agent text,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON user_sessions(expires_at);

CREATE TABLE IF NOT EXISTS oidc_login_states (
  state_hash char(64) PRIMARY KEY,
  nonce varchar(128) NOT NULL,
  pkce_verifier varchar(128) NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS weekly_reports (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  week_start date NOT NULL,
  status varchar(30) NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT','SUBMITTED','REVISION_REQUESTED','APPROVED','CLOSED')),
  summary text NOT NULL DEFAULT '',
  version integer NOT NULL DEFAULT 1,
  submitted_at timestamptz,
  reviewed_at timestamptz,
  reviewed_by bigint REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(user_id, week_start)
);
CREATE INDEX IF NOT EXISTS idx_reports_week_status ON weekly_reports(week_start, status);

CREATE TABLE IF NOT EXISTS report_items (
  id bigserial PRIMARY KEY,
  report_id bigint NOT NULL REFERENCES weekly_reports(id) ON DELETE CASCADE,
  category varchar(80) NOT NULL DEFAULT '',
  title varchar(240) NOT NULL,
  current_result text NOT NULL DEFAULT '',
  next_plan text NOT NULL DEFAULT '',
  issue text NOT NULL DEFAULT '',
  progress integer NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
  sort_order integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS report_comments (
  id bigserial PRIMARY KEY,
  report_id bigint NOT NULL REFERENCES weekly_reports(id) ON DELETE CASCADE,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  content text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS report_status_history (
  id bigserial PRIMARY KEY,
  report_id bigint NOT NULL REFERENCES weekly_reports(id) ON DELETE CASCADE,
  actor_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  from_status varchar(30),
  to_status varchar(30) NOT NULL,
  comment text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS personal_api_keys (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name varchar(120) NOT NULL,
  prefix varchar(20) NOT NULL,
  token_hash char(64) NOT NULL UNIQUE,
  key_version integer NOT NULL,
  scopes text[] NOT NULL DEFAULT ARRAY['reports:read']::text[],
  last_used_at timestamptz,
  expires_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_personal_keys_user ON personal_api_keys(user_id);

CREATE TABLE IF NOT EXISTS audit_logs (
  id bigserial PRIMARY KEY,
  actor_id bigint REFERENCES users(id) ON DELETE SET NULL,
  action varchar(120) NOT NULL,
  resource_type varchar(80) NOT NULL,
  resource_id varchar(120),
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  ip_address inet,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at DESC);

CREATE TABLE IF NOT EXISTS api_request_metrics (
  bucket timestamptz NOT NULL,
  method varchar(10) NOT NULL,
  route varchar(180) NOT NULL,
  status integer NOT NULL,
  request_count bigint NOT NULL DEFAULT 0,
  duration_ms_sum bigint NOT NULL DEFAULT 0,
  duration_ms_max bigint NOT NULL DEFAULT 0,
  PRIMARY KEY(bucket, method, route, status)
);
CREATE INDEX IF NOT EXISTS idx_metrics_bucket ON api_request_metrics(bucket DESC);

INSERT INTO organizations(name, code)
VALUES ('기본 조직', 'DEFAULT')
ON CONFLICT (code) DO NOTHING;

INSERT INTO app_settings(key, value, secret) VALUES
 ('service.name', 'Weekly', false),
 ('service.notice', '', false),
 ('workflow.enabled', 'false', false),
 ('workflow.week_start', 'MONDAY', false),
 ('auth.local_enabled', 'true', false),
 ('auth.session_hours', '12', false),
 ('oidc.enabled', 'false', false),
 ('oidc.issuer_url', '', false),
 ('oidc.client_id', '', false),
 ('oidc.client_secret', '', true),
 ('oidc.redirect_url', '', false),
 ('oidc.scopes', 'openid profile email', false),
 ('oidc.username_claim', 'preferred_username', false),
 ('oidc.groups_claim', 'groups', false),
 ('oidc.admin_group', '', false),
 ('oidc.auto_provision', 'true', false),
 ('security.api_key_max_days', '365', false),
 ('analytics.retention_days', '90', false)
ON CONFLICT (key) DO NOTHING;
