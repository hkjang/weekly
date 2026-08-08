DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'weekly_reports_source_type_check') THEN
    ALTER TABLE weekly_reports DROP CONSTRAINT weekly_reports_source_type_check;
  END IF;
  ALTER TABLE weekly_reports ADD CONSTRAINT weekly_reports_source_type_check
    CHECK (source_type IN ('MANUAL','AI_TEXT','PPTX_IMPORT','CONFLUENCE_AI','API','JIRA'));
END $$;

CREATE TABLE IF NOT EXISTS user_external_accounts (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  system_type varchar(30) NOT NULL CHECK (system_type IN ('CONFLUENCE')),
  external_username varchar(255) NOT NULL,
  mapping_source varchar(30) NOT NULL DEFAULT 'EXPLICIT'
    CHECK (mapping_source IN ('EXPLICIT','EMAIL_LOCALPART','USERNAME')),
  active boolean NOT NULL DEFAULT true,
  updated_by bigint REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(user_id, system_type)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_external_account_system_username
  ON user_external_accounts(system_type, lower(external_username));

CREATE TABLE IF NOT EXISTS confluence_sync_state (
  system_type varchar(30) PRIMARY KEY CHECK (system_type = 'CONFLUENCE'),
  status varchar(30) NOT NULL DEFAULT 'IDLE'
    CHECK (status IN ('IDLE','RUNNING','SUCCESS','PARTIAL','FAILED')),
  last_success_at timestamptz,
  last_attempt_at timestamptz,
  current_started_at timestamptz,
  error_message text NOT NULL DEFAULT '',
  pages_scanned integer NOT NULL DEFAULT 0,
  pages_changed integer NOT NULL DEFAULT 0,
  candidates_created integer NOT NULL DEFAULT 0,
  pages_failed integer NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO confluence_sync_state(system_type) VALUES ('CONFLUENCE')
ON CONFLICT (system_type) DO NOTHING;

CREATE TABLE IF NOT EXISTS confluence_pages (
  id bigserial PRIMARY KEY,
  page_id varchar(120) NOT NULL UNIQUE,
  content_type varchar(30) NOT NULL DEFAULT 'PAGE'
    CHECK (content_type IN ('PAGE','BLOGPOST')),
  status varchar(30) NOT NULL DEFAULT 'CURRENT',
  space_key varchar(255) NOT NULL DEFAULT '',
  title text NOT NULL,
  creator_username varchar(255) NOT NULL DEFAULT '',
  last_modifier_username varchar(255) NOT NULL DEFAULT '',
  created_at_source timestamptz,
  updated_at_source timestamptz,
  page_url text NOT NULL DEFAULT '',
  body_hash char(64),
  title_hash char(64) NOT NULL,
  page_version integer NOT NULL DEFAULT 0,
  last_error text NOT NULL DEFAULT '',
  last_synced_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_confluence_pages_updated
  ON confluence_pages(updated_at_source DESC);

CREATE TABLE IF NOT EXISTS report_candidates (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  week_start date NOT NULL,
  normalized_title varchar(240) NOT NULL,
  category varchar(80) NOT NULL DEFAULT '',
  current_result text NOT NULL DEFAULT '',
  next_plan text NOT NULL DEFAULT '',
  issue text NOT NULL DEFAULT '',
  confidence numeric(5,4) NOT NULL DEFAULT 0,
  rule_score integer NOT NULL DEFAULT 0,
  status varchar(30) NOT NULL DEFAULT 'DETECTED'
    CHECK (status IN ('DETECTED','ACCEPTED','IGNORED','MERGED','REMOVED')),
  created_by varchar(30) NOT NULL DEFAULT 'CONFLUENCE_AI'
    CHECK (created_by = 'CONFLUENCE_AI'),
  user_edited boolean NOT NULL DEFAULT false,
  accepted_report_id bigint REFERENCES weekly_reports(id) ON DELETE SET NULL,
  merged_into_id bigint REFERENCES report_candidates(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_report_candidates_user_week
  ON report_candidates(user_id, week_start, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS candidate_sources (
  candidate_id bigint NOT NULL REFERENCES report_candidates(id) ON DELETE CASCADE,
  confluence_page_id bigint NOT NULL REFERENCES confluence_pages(id) ON DELETE CASCADE,
  page_version integer NOT NULL DEFAULT 0,
  activity_type varchar(30) NOT NULL DEFAULT 'MODIFIED'
    CHECK (activity_type IN ('CREATED','MODIFIED','CREATED_AND_MODIFIED')),
  source_updated_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(candidate_id, confluence_page_id)
);
CREATE INDEX IF NOT EXISTS idx_candidate_sources_page
  ON candidate_sources(confluence_page_id, page_version);

CREATE TABLE IF NOT EXISTS confluence_sync_errors (
  id bigserial PRIMARY KEY,
  page_id varchar(120),
  phase varchar(40) NOT NULL,
  status_code integer,
  error_message text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_confluence_sync_errors_created
  ON confluence_sync_errors(created_at DESC);

INSERT INTO app_settings(key, value, secret) VALUES
 ('confluence.enabled', 'false', false),
 ('confluence.base_url', '', false),
 ('confluence.auth_mode', 'BASIC', false),
 ('confluence.username', '', false),
 ('confluence.password', '', true),
 ('confluence.include_spaces', '', false),
 ('confluence.exclude_spaces', '', false),
 ('confluence.sync_interval_minutes', '60', false),
 ('confluence.ai_enabled', 'true', false),
 ('confluence.minimum_candidate_score', '50', false),
 ('confluence.ai_review_min_score', '20', false),
 ('confluence.analyze_body', 'true', false),
 ('confluence.lookback_days', '7', false),
 ('confluence.batch_size', '50', false),
 ('confluence.include_blogs', 'false', false),
 ('confluence.auto_map_email_localpart', 'true', false),
 ('confluence.auto_map_username', 'true', false),
 ('confluence.work_keywords', '개발,검토,테스트,적용,구축,설계,분석,개선,배포,연계,구현,PoC,개발완료,테스트결과,develop,test,deploy,review,design,implement', false),
 ('confluence.personal_space_prefixes', '~,PERSONAL', false),
 ('confluence.score_project_space', '20', false),
 ('confluence.score_creator', '20', false),
 ('confluence.score_modifier', '20', false),
 ('confluence.score_work_keyword', '20', false),
 ('confluence.score_meeting', '-10', false),
 ('confluence.score_notice', '-20', false),
 ('confluence.score_leave', '-50', false),
 ('confluence.score_personal_space', '-30', false)
ON CONFLICT (key) DO NOTHING;
