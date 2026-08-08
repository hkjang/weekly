CREATE TABLE IF NOT EXISTS pptx_templates (
  id smallint PRIMARY KEY CHECK (id = 1),
  original_name varchar(255) NOT NULL,
  file_name varchar(255) NOT NULL,
  size_bytes bigint NOT NULL,
  sha256 char(64) NOT NULL,
  placeholders text[] NOT NULL DEFAULT '{}'::text[],
  uploaded_by bigint REFERENCES users(id) ON DELETE SET NULL,
  uploaded_at timestamptz NOT NULL DEFAULT now()
);
