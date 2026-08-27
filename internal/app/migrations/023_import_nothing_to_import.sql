-- A job that has nothing to review has to say so.
--
-- Re-uploading a deck that is already in the system marks every file
-- DUPLICATE, so nothing is queued and nothing fails. Both places that decide a
-- job's status read that as READY — 검토 가능 on the history list — and the
-- operator opens it to find no file they can select. Every route out was
-- refused: confirming answered IMPORT_FILE_NOT_READY, confirming nothing
-- answered IMPORT_SELECTION_REQUIRED, and re-analysing answered
-- NO_RETRYABLE_IMPORT. The row stayed on the list, labelled as work, forever.
--
-- Somebody who drags the same folder in twice a month accumulates them, and
-- they sit among the jobs that really are waiting.
ALTER TABLE import_jobs DROP CONSTRAINT IF EXISTS import_jobs_status_check;
ALTER TABLE import_jobs ADD CONSTRAINT import_jobs_status_check
	CHECK (status IN ('PENDING','PROCESSING','READY','PARTIAL','FAILED','CONFIRMED','NOTHING_TO_IMPORT'));

-- Jobs already stuck in the state this names.
UPDATE import_jobs j SET status = 'NOTHING_TO_IMPORT'
WHERE j.status = 'READY'
  AND NOT EXISTS (SELECT 1 FROM import_files f WHERE f.import_job_id = j.id AND f.status IN ('READY','NEEDS_REVIEW'))
  AND NOT EXISTS (SELECT 1 FROM import_files f WHERE f.import_job_id = j.id AND f.status = 'FAILED');
