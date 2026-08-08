ALTER TABLE weekly_reports
  ADD COLUMN IF NOT EXISTS source_type varchar(30) NOT NULL DEFAULT 'MANUAL',
  ADD COLUMN IF NOT EXISTS source_ref varchar(120);

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'weekly_reports_source_type_check') THEN
    ALTER TABLE weekly_reports ADD CONSTRAINT weekly_reports_source_type_check
      CHECK (source_type IN ('MANUAL','AI_TEXT','PPTX_IMPORT','API','JIRA'));
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS import_jobs (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  status varchar(30) NOT NULL DEFAULT 'PENDING'
    CHECK (status IN ('PENDING','PROCESSING','READY','PARTIAL','FAILED','CONFIRMED')),
  total_files integer NOT NULL DEFAULT 0,
  processed_files integer NOT NULL DEFAULT 0,
  failed_files integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  confirmed_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_import_jobs_user_created ON import_jobs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_import_jobs_status ON import_jobs(status, created_at);

CREATE TABLE IF NOT EXISTS import_files (
  id bigserial PRIMARY KEY,
  import_job_id bigint NOT NULL REFERENCES import_jobs(id) ON DELETE CASCADE,
  original_filename varchar(255) NOT NULL,
  stored_path text,
  file_hash char(64) NOT NULL,
  size_bytes bigint NOT NULL,
  status varchar(30) NOT NULL DEFAULT 'QUEUED'
    CHECK (status IN ('QUEUED','PROCESSING','READY','NEEDS_REVIEW','FAILED','DUPLICATE','CONFIRMED','SKIPPED')),
  detected_week_start date,
  detected_week_end date,
  confidence numeric(5,4),
  date_source varchar(40),
  raw_text text NOT NULL DEFAULT '',
  parsed_result jsonb NOT NULL DEFAULT '{}'::jsonb,
  ai_response text NOT NULL DEFAULT '',
  error_message text NOT NULL DEFAULT '',
  duplicate_of bigint REFERENCES import_files(id) ON DELETE SET NULL,
  report_id bigint REFERENCES weekly_reports(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  analyzed_at timestamptz,
  confirmed_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_import_files_job ON import_files(import_job_id, id);
CREATE INDEX IF NOT EXISTS idx_import_files_hash ON import_files(file_hash);

INSERT INTO app_settings(key, value, secret) VALUES
 ('ai.enabled', 'false', false),
 ('ai.endpoint', '', false),
 ('ai.api_key', '', true),
 ('ai.model', '', false),
 ('ai.timeout_seconds', '90', false),
 ('ai.max_input_chars', '50000', false),
 ('import.max_files', '20', false),
 ('import.max_file_mb', '25', false),
 ('import.retention_days', '365', false)
ON CONFLICT (key) DO NOTHING;
