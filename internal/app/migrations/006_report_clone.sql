DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'weekly_reports_source_type_check') THEN
    ALTER TABLE weekly_reports DROP CONSTRAINT weekly_reports_source_type_check;
  END IF;
  ALTER TABLE weekly_reports ADD CONSTRAINT weekly_reports_source_type_check
    CHECK (source_type IN ('MANUAL','AI_TEXT','PPTX_IMPORT','CONFLUENCE_AI','CLONED','API','JIRA'));
END $$;
