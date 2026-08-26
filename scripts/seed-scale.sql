-- A deployment large enough for the failures that only appear at scale.
--
-- The nine-item development database hid a 23.6 MB list response, a ranking
-- that reshuffled between refreshes, and half a megabyte of weekly text no
-- chart draws. All three surfaced within minutes of putting a real
-- organisation behind the same screens, so having one on demand matters more
-- than the exact numbers below.
--
-- Titles matter as much as the row count. An earlier version of this seed named
-- every task "업무 N 처리", which made every pair of titles share two tokens and
-- turned a similarity pass into 2.2 million full comparisons — a measurement of
-- the fixture, not of the product. The vocabulary here is spread so that titles
-- overlap the way real ones do: some pairs share a word, most share none, and
-- the same task recurs week after week for its owner.
--
-- Run against an EMPTY database that the application has already migrated:
--   docker exec -i <pg> psql -U postgres -d weeklyscale -f - < scripts/seed-scale.sql
-- Then restart the application so the work item backfill runs.

\set ON_ERROR_STOP on

DO $$
BEGIN
  IF (SELECT count(*) FROM weekly_reports) > 0 THEN
    RAISE EXCEPTION '이 데이터베이스에는 이미 주간보고가 있습니다. 빈 데이터베이스에만 실행하십시오.';
  END IF;
END $$;

-- Four levels, not two.
--
-- The visibility predicate walks this tree with a recursive lookup, and the
-- earlier fixture was a root plus one row of children — so every scale run
-- exercised exactly one hop and "a manager sees the whole subtree beneath them"
-- was never measured at the shape a real deployment has
-- (회사 → 본부 → 실 → 팀).
INSERT INTO organizations(name, code, parent_id) VALUES ('회사', 'CORP', NULL);

INSERT INTO organizations(name, code, parent_id)
SELECT '본부 ' || g, 'HQ' || g, (SELECT id FROM organizations WHERE code = 'CORP')
FROM generate_series(1, 4) g;

INSERT INTO organizations(name, code, parent_id)
SELECT '실 ' || g, 'OF' || g, (SELECT id FROM organizations WHERE code = 'HQ' || (1 + (g % 4)))
FROM generate_series(1, 8) g;

INSERT INTO organizations(name, code, parent_id)
SELECT '팀 ' || g, 'TM' || g, (SELECT id FROM organizations WHERE code = 'OF' || (1 + (g % 8)))
FROM generate_series(1, 32) g;

-- Leaders sit at every level, so a scale run has somebody whose scope is one
-- team and somebody whose scope is a quarter of the company.
INSERT INTO users(username, display_name, role, organization_id)
SELECT 'u' || g, '사용자 ' || g,
       CASE WHEN g % 25 = 0 THEN 'TEAM_LEADER' ELSE 'USER' END,
       (SELECT id FROM organizations WHERE code = 'TM' || (1 + (g % 32)))
FROM generate_series(1, 300) g;

INSERT INTO users(username, display_name, role, organization_id)
SELECT 'hq' || g, '본부장 ' || g, 'ORG_MANAGER',
       (SELECT id FROM organizations WHERE code = 'HQ' || g)
FROM generate_series(1, 4) g;

-- 52 weeks ending on the week the application calls current.
--
-- In the service timezone, not the database's. PostgreSQL runs in UTC here, so
-- on a Sunday evening in Seoul CURRENT_DATE is still the previous week and the
-- seed stopped one Monday short of where the application was looking. Every
-- task then read as "reported last week, gone this week": the agenda came back
-- with 1,866 rows under 보고 누락 and nothing under 변경점, which looks exactly
-- like a bug in change detection and was a bug in the fixture.
--
-- service.timezone defaults to Asia/Seoul; change the zone below to match a
-- deployment that has set it to something else.
INSERT INTO weekly_reports(user_id, week_start, summary, status)
SELECT u.id,
       (date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - ((51 - w) * 7)),
       '주간 요약 ' || w, 'APPROVED'
FROM users u CROSS JOIN generate_series(0, 51) w
WHERE u.username LIKE 'u%';

-- One task per (owner, slot), repeating every week, so a work item accumulates
-- a year of history the way a real one does.
WITH subject(i, word) AS (VALUES
 (0,'인증'),(1,'결제'),(2,'배치'),(3,'모니터링'),(4,'백업'),(5,'권한'),(6,'로그'),(7,'캐시'),
 (8,'검색'),(9,'알림'),(10,'대시보드'),(11,'리포트'),(12,'마이그레이션'),(13,'방화벽'),(14,'인덱스'),(15,'큐'),
 (16,'스케줄러'),(17,'게이트웨이'),(18,'세션'),(19,'암호화'),(20,'전표'),(21,'결산'),(22,'재고'),(23,'출고'),
 (24,'채용'),(25,'평가'),(26,'교육'),(27,'예산'),(28,'감사'),(29,'계약'),(30,'조달'),(31,'품질')),
 action(i, word) AS (VALUES
 (0,'개선'),(1,'구축'),(2,'이관'),(3,'점검'),(4,'자동화'),(5,'표준화'),(6,'통합'),(7,'분리'))
INSERT INTO report_items(report_id, category, title, current_result, next_plan, issue, progress, sort_order)
SELECT r.id,
       (ARRAY['개발','운영','기획','보안','인프라'])[1 + (slot % 5)],
       subject.word || ' ' || action.word,
       subject.word || ' ' || action.word || ' 진행 상황을 정리했습니다',
       subject.word || ' ' || action.word || ' 다음 단계',
       CASE WHEN (r.id + slot) % 7 = 0 THEN '외부 연동 지연' ELSE '' END,
       -- Different tasks at different stages, and most of them still running.
       -- An earlier version had every task climb to 100% by the last week, so
       -- nothing had changed since the week before and the meeting agenda came
       -- back empty on 109,200 rows — a fixture that hid the very screen it was
       -- built to exercise.
       -- One task in six stops climbing partway through and never moves again.
       -- Without this every task advanced every fortnight, so nothing was ever
       -- stalled: the stall rules, the meeting's 정체 entries and the forecast's
       -- "이 속도로는 끝나는 시점이 없습니다" branch were all unreachable on a
       -- fixture built to exercise them. The same mistake as the earlier version
       -- that drove everything to 100%, in the other direction.
       LEAST(100, ((r.user_id * 7 + slot) % 9) * 4
                  + (LEAST(
                       ((r.week_start - (date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - 357)) / 7),
                       CASE WHEN ((r.user_id + slot) % 6) = 0 THEN 20 ELSE 999 END)
                     * (1 + ((r.user_id + slot) % 3)) / 2)),
       slot
FROM weekly_reports r
CROSS JOIN generate_series(1, 7) slot
JOIN subject ON subject.i = ((r.user_id * 7 + slot) % 32)
JOIN action ON action.i = (((r.user_id * 7 + slot) / 32) % 8);

-- Give every reported title the task identity the application would have
-- created for it. resolveWorkItem() does this inside the save transaction, so
-- rows inserted straight into SQL never get linked: on a freshly seeded
-- deployment work_items was empty and the task list, stall rules, forecasts,
-- dependencies and decisions were all blank — the fixture looked populated
-- while every screen built on task identity had nothing to show.
--
-- The key must match candidateTitleKey(): lowercased, letters and digits only.
INSERT INTO work_items(user_id, title, normalized_key, category)
SELECT DISTINCT ON (r.user_id, k.key) r.user_id, i.title, k.key, i.category
FROM report_items i
JOIN weekly_reports r ON r.id = i.report_id
CROSS JOIN LATERAL (SELECT lower(regexp_replace(i.title, '[^0-9A-Za-z가-힣]', '', 'g')) AS key) k
WHERE k.key <> ''
-- Newest wording wins, the way the upsert leaves the most recent title.
ORDER BY r.user_id, k.key, r.week_start DESC;

UPDATE report_items i SET work_item_id = w.id
FROM weekly_reports r, work_items w
WHERE r.id = i.report_id
  AND w.user_id = r.user_id
  AND w.normalized_key = lower(regexp_replace(i.title, '[^0-9A-Za-z가-힣]', '', 'g'));

SELECT (SELECT count(*) FROM organizations) AS organizations,
       (SELECT count(*) FROM users) AS users,
       (SELECT count(*) FROM weekly_reports) AS reports,
       (SELECT count(*) FROM report_items) AS items,
       (SELECT count(*) FROM work_items) AS work_items,
       (SELECT count(*) FROM report_items WHERE work_item_id IS NULL) AS unlinked;
