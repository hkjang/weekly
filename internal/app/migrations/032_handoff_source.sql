-- 서비스 간 문서 넘기기: 넘겨받은 파일에 어디서 왔는지를 남긴다.
--
-- A deck that arrived from ptium through a handoff claim is otherwise
-- indistinguishable from one a person uploaded by hand. The standard asks that
-- the receiving side keep the origin, so that "which canvas did this slide
-- come from" can be answered later. NULL is the direct upload.
--
-- Numbered 032 rather than 031 on purpose: the tracking branch in flight uses
-- 031, and two files with the same number would leave one of them silently
-- unapplied.
ALTER TABLE import_files
  ADD COLUMN IF NOT EXISTS handoff_source text;
