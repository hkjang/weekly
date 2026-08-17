CREATE TABLE IF NOT EXISTS report_attachments (
  id bigserial PRIMARY KEY,
  report_id bigint NOT NULL REFERENCES weekly_reports(id) ON DELETE CASCADE,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  original_filename varchar(255) NOT NULL,
  stored_path text NOT NULL,
  content_type varchar(80) NOT NULL,
  extension varchar(8) NOT NULL,
  size_bytes bigint NOT NULL,
  width integer NOT NULL,
  height integer NOT NULL,
  sha256 char(64) NOT NULL,
  caption varchar(240) NOT NULL DEFAULT '',
  placement varchar(20) NOT NULL DEFAULT 'AFTER'
    CHECK (placement IN ('BEFORE', 'AFTER')),
  sort_order integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_report_attachments_report
  ON report_attachments(report_id, placement, sort_order, id);

INSERT INTO app_settings(key, value, secret) VALUES
 ('attachment.max_per_report', '20', false),
 ('attachment.max_file_mb', '10', false)
ON CONFLICT (key) DO NOTHING;
