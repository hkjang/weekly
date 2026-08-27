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
-- Every seeded account shares one password, and that is the point: this seed
-- exists to be loaded against, and scripts/load-check.py signs 300 people in
-- at once to reproduce Monday morning. Its own instructions said the accounts
-- had a shared password; they did not, so the load check could not be run as
-- documented without somebody setting three hundred passwords by hand.
--
-- The hash below is Argon2id over the same string the load check already
-- carries in plain sight, so nothing here is a secret that was not already in
-- the repository. The seed refuses to run against a database that holds
-- reports, which is the guard that matters.
INSERT INTO users(username, display_name, role, organization_id, password_hash)
SELECT 'u' || g, '사용자 ' || g,
       CASE WHEN g % 25 = 0 THEN 'TEAM_LEADER' ELSE 'USER' END,
       (SELECT id FROM organizations WHERE code = 'TM' || (1 + (g % 32))),
       '$argon2id$v=19$m=65536,t=3,p=2$qgo+q3ViRTgJYWc6v1nNfA$1kNBwOuW87XVWxPlNm8Pwu1OaU9xthF9Bq3EAlq5mxo'
FROM generate_series(1, 300) g;

INSERT INTO users(username, display_name, role, organization_id, password_hash)
SELECT 'hq' || g, '본부장 ' || g, 'ORG_MANAGER',
       (SELECT id FROM organizations WHERE code = 'HQ' || g),
       '$argon2id$v=19$m=65536,t=3,p=2$qgo+q3ViRTgJYWc6v1nNfA$1kNBwOuW87XVWxPlNm8Pwu1OaU9xthF9Bq3EAlq5mxo'
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
--
-- Every report was APPROVED, and submitted_at, reviewed_at and reviewed_by were
-- all null. So 보고서 상태 분포 had four of its five rows at zero, 제출률 read
-- exactly 100.0% on every deployment, and nothing that asks who has not handed
-- theirs in yet had anybody to name. The review workflow is a third of what
-- this product does and the fixture had it in one state.
--
-- Only the current week varies. Every earlier week stays APPROVED so the
-- year of history the rollups and forecasts read is unchanged.
INSERT INTO weekly_reports(user_id, week_start, summary, status, submitted_at, reviewed_at, reviewed_by)
SELECT u.id, wk.day, '주간 요약 ' || w, st.status,
       CASE WHEN st.status = 'DRAFT' THEN NULL
            ELSE wk.day + interval '4 days 15 hours' END,
       CASE WHEN st.status IN ('DRAFT', 'SUBMITTED') THEN NULL
            ELSE wk.day + interval '5 days 10 hours' END,
       CASE WHEN st.status IN ('DRAFT', 'SUBMITTED') THEN NULL
            ELSE rev.id END
FROM users u CROSS JOIN generate_series(0, 51) w
CROSS JOIN LATERAL (SELECT (date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - ((51 - w) * 7)) AS day) wk
CROSS JOIN LATERAL (SELECT CASE
         WHEN wk.day <> date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date THEN 'APPROVED'
         WHEN u.id % 11 = 0 THEN 'DRAFT'
         WHEN u.id % 11 = 1 THEN 'SUBMITTED'
         WHEN u.id % 11 = 2 THEN 'REVISION_REQUESTED'
         WHEN u.id % 11 = 3 THEN 'CLOSED'
         ELSE 'APPROVED' END AS status) st
-- Somebody in the same organisation who is allowed to review. Left join, so a
-- writer with no leader above them still gets a report.
LEFT JOIN LATERAL (SELECT l.id FROM users l
                   WHERE l.organization_id = u.organization_id
                     AND l.role IN ('TEAM_LEADER', 'ORG_MANAGER')
                     AND l.id <> u.id
                   ORDER BY l.id LIMIT 1) rev ON TRUE
-- Including the four 본부장. They were left out, so the most senior seat in the
-- deployment owned no work at all: 내 보고서, 인수인계, 미결 후속 조치 and the
-- self-scoped half of every screen answered empty from the account most likely
-- to be used to look at the thing. An empty answer from a real account is the
-- hardest kind of gap to notice, because it looks like a fast one.
WHERE u.username LIKE 'u%' OR u.username LIKE 'hq%';

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
       subject.word || ' ' || action.word || CASE WHEN slot = 8 THEN ' 신규 착수' ELSE '' END,
       -- 지난주에 계획한 일인데 이번 주 실적이 비어 있습니다 is one of the four
       -- checks a writer sees before submitting, and it could never fire: the
       -- seed wrote a result on every line of every week. One line in twenty-
       -- nine is now left blank in the current week.
       CASE WHEN r.week_start = date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date AND ((r.user_id + slot) % 29) = 7 THEN ''
            ELSE subject.word || ' ' || action.word || ' 진행 상황을 정리했습니다' END,
       -- The plan moves. It used to be the same sentence for fifty-two weeks on
       -- every task, so repeatedPlan came back 51 or 52 for **100%** of them and
       -- the 계획 N회 반복 badge sat on every row of the work list. A mark that
       -- is on everything points at nothing.
       --
       -- A plan now covers two weeks, which is below the badge's threshold of
       -- three, and one task in eight keeps restating the same one — those are
       -- the rows the badge is for.
       CASE WHEN ((r.user_id + slot) % 8) = 0
            THEN subject.word || ' ' || action.word || ' 다음 단계'
            ELSE subject.word || ' ' || action.word || ' ' ||
                 (ARRAY['설계 검토','시험 환경 구성','1차 적용','결과 점검',
                        '유관 부서 협의','운영 이관 준비','문서 정리','마무리 점검'])[1 + ((p.wk / 2) % 8)]
       END,
       -- Two kinds of issue, because the product distinguishes them. The
       -- scattered one below never lands twice in a row for the same task, so
       -- before this the seed had no open issue at all and the risk register
       -- was empty however it was configured. One task in nine now carries an
       -- unanswered issue from week 30 to the end, which is what "이슈가 N주째
       -- 지속되고 있습니다" is supposed to describe.
       --
       -- Two sentences used to cover 110,000 rows, so every long-running issue
       -- in the deployment read the same. 경영 요약 picks the ten items a leader
       -- must look at and all ten came back saying "벤더 회신 지연으로 규격 확정을
       -- 못 하고 있습니다" — ten rows that teach one thing. The obstacle is the
       -- part of a weekly report a reader actually reads.
       CASE
         WHEN ((r.user_id + slot) % 9) = 0
              AND ((r.week_start - (date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - 357)) / 7) >= 30
           THEN (ARRAY[
             '벤더 회신 지연으로 규격 확정을 못 하고 있습니다',
             '보안 검토 결과를 기다리는 중이라 배포를 못 잡습니다',
             '검증 환경에 데이터가 없어 재현이 안 됩니다',
             '유관 부서 담당자가 공석이라 협의가 멈췄습니다',
             '예산 집행 승인이 나지 않아 계약을 못 합니다',
             '기존 시스템 정지 시간을 잡지 못하고 있습니다',
             '요건이 두 번 바뀌어 설계를 다시 잡고 있습니다',
             '테스트 장비 반입 허가가 지연되고 있습니다',
             '이관 대상 데이터 정합성이 맞지 않습니다'])[1 + ((r.user_id * 3 + slot) % 9)]
         WHEN (r.id + slot) % 7 = 0
           THEN (ARRAY['외부 연동 지연', '담당자 일정 조율 중', '사양 확인 대기',
                       '테스트 데이터 준비 중', '검토 의견 반영 중'])[1 + ((r.id + slot) % 5)]
         ELSE ''
       END,
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
       -- The last week is where the change summary is read, and until now every
       -- task in it did exactly one of two things: climb a little, or sit
       -- still. Five of the seven change kinds could never occur on a seeded
       -- deployment — 완료, 신규, 재개, 진척도 역행 and 보고 누락 all came back
       -- 0 — so the dashboard's main visual used two of its seven slots and
       -- the classifications behind the other five were never seen with data.
       CASE
         -- A task that starts this week: no prior report, first week is now.
         WHEN slot = 8 THEN 5
         -- Finished this week. Only from below 90, so the week before is
         -- certainly under 100 and this reads as crossing the line rather than
         -- as a task that was already done.
         WHEN r.week_start = date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date AND ((r.user_id + slot) % 23) = 3 AND p.base < 90
           THEN 100
         -- Reported lower than last week. The weekly step is one or two points,
         -- so fifteen below is unambiguously backwards however the task moved.
         WHEN r.week_start = date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date AND ((r.user_id + slot) % 23) = 2 AND p.base < 90
           THEN GREATEST(0, p.base - 15)
         -- A week where something actually landed. Every other task moves one
         -- or two points, so before this every 진척 row carried the same delta
         -- and the ordering that decides which forty of a thousand rows a
         -- reader sees had nothing to tell them apart — the cap looked right
         -- on a fixture that could not show it working.
         WHEN r.week_start = date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date AND ((r.user_id + slot) % 23) IN (5, 11) AND p.base < 80
           THEN p.base + 8 + ((r.user_id + slot) % 13)
         ELSE p.base
       END,
       slot
FROM weekly_reports r
CROSS JOIN generate_series(1, 8) slot
JOIN subject ON subject.i = ((r.user_id * 7 + slot) % 32)
JOIN action ON action.i = (((r.user_id * 7 + slot) / 32) % 8)
CROSS JOIN LATERAL (SELECT ((r.week_start - (date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - 357)) / 7) AS wk,
                  LEAST(100, ((r.user_id * 7 + slot) % 9) * 4
                  + (LEAST(
                       ((r.week_start - (date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - 357)) / 7),
                       CASE WHEN ((r.user_id + slot) % 6) = 0 THEN 20 ELSE 999 END)
                     * (1 + ((r.user_id + slot) % 3)) / 2)) AS base) p
-- Which rows exist at all. Dropping a row is how the two kinds that are
-- defined by absence get made: a task missing from this week's report after
-- being in last week's is 보고 누락, and one missing from last week's but
-- present in this week's is 재개.
WHERE (slot <= 7 OR (r.week_start = date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date AND r.user_id % 7 = 0))
  AND NOT (slot <= 7 AND r.week_start = date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date
           AND ((r.user_id + slot) % 23) = 1 AND p.base < 90)
  AND NOT (slot <= 7 AND r.week_start = date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - 7
           AND ((r.user_id + slot) % 23) = 4)
  -- 그리고 끝나는 업무. 열한 쌍에 하나는 18~47주차 어딘가에서 보고를 멈춥니다.
  -- 폭이 좁으면 가까운 두 창이 여전히 같은 답을 냅니다. 처음 18~37주로 두었더니
  -- 끝난 업무가 전부 15주 넘게 전이라 4주와 12주가 똑같이 걸러 냈고, 18~43주로
  -- 넓혔더니 이번에는 4주와 8주가 같았습니다. 화면이 내주는 가장 좁은 두 창까지
  -- 갈리려면 최근 4~7주 사이에 끝난 업무도 있어야 합니다.
  --
  -- 이것이 없으면 2,171건이 전부 최근 4주 안에 보고된 상태가 되고, 그러면
  -- 화면의 기간 선택이 아무것도 가르지 못합니다. 실제로 재 보니 업무
  -- 인사이트의 4·12·26·52주가 업무 2171·중복 5692·협업 328 로 **똑같은 수**를
  -- 냈습니다. 기간을 통째로 무시하는 회귀가 생겨도 같은 답이 나옵니다.
  --
  -- 일은 끝납니다. 끝난 일이 다음 주 보고서에 없는 것이 정상이고, 그 부재가
  -- 있어야 '최근 N주' 라는 말에 뜻이 생깁니다.
  AND NOT (slot <= 7 AND ((r.user_id + slot) % 11) = 0
           AND p.wk > 18 + ((r.user_id * 3 + slot) % 30));

-- A rejected report carries its reason. reviewReport() writes the reason into
-- report_comments as well as the status history, and that comment is what the
-- writer actually reads on the screen — a 반려 with nothing beside it is a
-- state the product never produces.
INSERT INTO report_comments(report_id, user_id, content)
SELECT r.id, coalesce(r.reviewed_by, r.user_id),
       (ARRAY['지난주 대비 무엇이 달라졌는지가 없습니다. 진척 근거를 적어 주세요.',
              '이슈에 담당과 기한이 없습니다. 다음 주 계획과 함께 보완해 주세요.',
              '업무 항목이 지난주와 제목만 다릅니다. 같은 일이면 하나로 합쳐 주세요.'])[1 + (r.id % 3)]
FROM weekly_reports r
WHERE r.status = 'REVISION_REQUESTED'
ON CONFLICT DO NOTHING;

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

-- 마감일. 씨앗은 여태 하나도 넣지 않았고, 그래서 업무 2,171건 전부가
-- dueOutlook NONE 이었습니다. 마감일 화면과 전망, 마감 초과 배지, 경영
-- 요약의 마감 관련 가중치 셋이 어느 배포에서도 켜지지 않았습니다.
--
-- 여섯 갈래가 모두 나와야 합니다. 한 갈래라도 비면 그 갈래를 그리는 코드가
-- 배포에서 한 번도 실행되지 않습니다. 그런데 갈래는 날짜만으로 정해지지
-- 않고 **그 업무의 진척과 두 속도**에 달려 있습니다.
--
-- 이 씨앗의 속도를 실측했습니다: 전체 평균 0.5~0.8%/주, 최근 8%/주.
-- 남은 주 W 에 대해 낮은 추정은 진척 + 0.8W, 높은 추정은 진척 + 8W 이므로
--
--   닿음   : 낮은 추정도 100 — 진척 60 이상이면 W ≥ 50
--   갈림   : 높은 추정만 100 — W 는 5~44 사이
--   위태   : 둘 다 못 닿음 — W 가 작을 때
--
-- 처음에는 id 로 날짜를 흩뿌렸고 셋만 나왔습니다. 그다음 진척만 보고
-- 놓았더니 넷이었습니다. 속도를 재고 나서야 여섯이 다 나옵니다.
WITH state AS (
  SELECT DISTINCT ON (i.work_item_id)
         i.work_item_id AS id, i.progress, count(*) OVER (PARTITION BY i.work_item_id) AS weeks
  FROM report_items i JOIN weekly_reports r ON r.id = i.report_id
  WHERE i.work_item_id IS NOT NULL
  ORDER BY i.work_item_id, r.week_start DESC
), wk AS (SELECT date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date AS d)
UPDATE work_items w SET due_date =
  CASE
    -- 완료: 끝난 일에 지난 마감일. 완료는 날짜가 지났어도 초과가 아닙니다.
    WHEN s.progress >= 100 AND (w.id % 3) = 0 THEN wk.d - 35
    -- 알 수 없음: 보고가 한 주뿐이면 추정할 근거가 없습니다.
    WHEN s.weeks <= 1 THEN wk.d + 42
    -- 지남: 아직 한참 남았는데 날짜가 지났습니다.
    WHEN s.progress < 90 AND (w.id % 11) = 1 THEN wk.d - 28
    -- 닿음: 진척 60 이상에 쉰다섯 주. 느린 쪽 속도로도 100 에 닿습니다.
    WHEN s.progress >= 60 AND (w.id % 5) <= 1 THEN wk.d + 385
    -- 갈림: 스무 주. 최근 속도로는 닿고 전체 평균으로는 못 닿는 자리입니다.
    WHEN s.progress BETWEEN 30 AND 99 AND (w.id % 7) <= 2 THEN wk.d + 140
    -- 위태: 이제 시작인데 2주.
    WHEN s.progress < 45 AND (w.id % 4) = 0 THEN wk.d + 14
    ELSE NULL
  END
FROM state s, wk
WHERE s.id = w.id;


-- Dependencies, because without them a whole feature is dark.
--
-- The seed built reports, tasks, issues and stalls, and no task ever waited on
-- another. So the bottleneck screen read zero however the data grew, the
-- meeting's 타 조직 대기 section could not appear, and the dependency views had
-- nothing to draw. Same lesson as the stalls this file learned in v0.134 and the
-- open issues in v0.137, now for the last dark corner.
--
-- Two shapes, because the product distinguishes them. Ordinary pairs inside one
-- organisation, and a handful of blockers holding several tasks up across
-- organisations — which is the case the meeting exists to surface, and the only
-- one that makes a bottleneck.
WITH running AS (
  SELECT w.id, w.user_id, u.organization_id,
         row_number() OVER (ORDER BY w.id) AS n
  FROM work_items w
  JOIN users u ON u.id = w.user_id
  WHERE EXISTS (SELECT 1 FROM report_items i WHERE i.work_item_id = w.id)
),
-- Six blockers, each in a different organisation from the work waiting on it.
blockers AS (SELECT id, organization_id, row_number() OVER (ORDER BY id) AS b FROM running WHERE n % 331 = 7 LIMIT 6),
waiting AS (
  SELECT r.id AS blocked_id, b.id AS blocker_id,
         row_number() OVER (PARTITION BY b.id ORDER BY r.id) AS seq
  FROM running r JOIN blockers b
    ON b.organization_id IS DISTINCT FROM r.organization_id
  WHERE r.n % 37 = 3
)
INSERT INTO work_item_links(blocker_id, blocked_id, note)
SELECT blocker_id, blocked_id, '선행 작업이 끝나야 진행할 수 있습니다'
FROM waiting WHERE seq <= 4
ON CONFLICT DO NOTHING;

-- And ordinary pairs inside one organisation: one task waiting on a colleague's.
INSERT INTO work_item_links(blocker_id, blocked_id, note)
SELECT b.id, a.id, '같은 팀의 선행 작업입니다'
FROM (
  SELECT w.id, u.organization_id,
         row_number() OVER (PARTITION BY u.organization_id ORDER BY w.id) AS seat
  FROM work_items w JOIN users u ON u.id = w.user_id
  WHERE EXISTS (SELECT 1 FROM report_items i WHERE i.work_item_id = w.id)
) a
JOIN (
  SELECT w.id, u.organization_id,
         row_number() OVER (PARTITION BY u.organization_id ORDER BY w.id) AS seat
  FROM work_items w JOIN users u ON u.id = w.user_id
  WHERE EXISTS (SELECT 1 FROM report_items i WHERE i.work_item_id = w.id)
) b ON b.organization_id = a.organization_id AND b.seat = a.seat + 1
WHERE a.seat % 9 = 1 AND a.id <> b.id
ON CONFLICT DO NOTHING;

-- Decisions, resolved issues and review comments.
--
-- Counting every table on a seeded deployment turned up five that stayed empty
-- however much the seed grew. Three of them are whole screens: the decision
-- register and the meeting's 결정 필요 section, the issue-clearance report that
-- v0.131 built, and the review conversation on a report. The other two need
-- files on disk and the submit flow, and are noted rather than faked.
--
-- Found by counting rather than by walking into them one at a time, which is
-- how the stalls, the open issues and the dependencies were each found.

-- One decision on every ninth task, a third of them still open with a deadline
-- the meeting agreed — which is the only way a task with no deadline of its own
-- is offered one.
INSERT INTO decisions(work_item_id, recorded_by, decided_by, decided_on, title, rationale, follow_up, due_date, status)
SELECT w.id, w.user_id,
       CASE WHEN (w.id / 9) % 3 = 0 THEN '본부 회의' ELSE '팀 회의' END,
       (date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - 357 + ((w.id % 40)::int) * 7),
       w.title || ' 방향 결정',
       '대안보다 위험이 낮고 기존 구성과 맞물립니다.',
       CASE WHEN (w.id / 9) % 3 = 0 THEN '다음 회의까지 설계안 확정' ELSE '담당자가 일정 재확인' END,
       CASE WHEN (w.id / 9) % 3 = 0
            THEN (date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date + 14)
            ELSE NULL END,
       CASE WHEN (w.id / 9) % 3 = 0 THEN 'OPEN' ELSE 'DONE' END
FROM work_items w
WHERE w.id % 9 = 0 AND EXISTS (SELECT 1 FROM report_items i WHERE i.work_item_id = w.id)
ON CONFLICT DO NOTHING;

-- 뒤집힌 결정. 결정 241건 중 대체된 것이 0건이었고 상태도 OPEN 과 DONE
-- 둘뿐이라, SUPERSEDED 갈래 — 목록에서 빠지는 규칙, '이전 결정 #N 대체'
-- 표시, 인수인계의 열린 결정 세기 — 가 어느 배포에서도 켜지지 않았습니다.
--
-- 결정은 뒤집힙니다. 지난달 정한 것을 이번 달에 바꾸는 일이 흔하고, 그때
-- 옛 기록이 남아야 왜 바뀌었는지 읽을 수 있습니다.
INSERT INTO decisions(work_item_id, recorded_by, decided_by, decided_on, title, rationale, follow_up, due_date, status, supersedes_id)
SELECT d.work_item_id, d.recorded_by, '본부 회의',
       LEAST(d.decided_on + 35, date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date),
       replace(d.title, '방향 결정', '방향 재결정'),
       '앞선 결정 뒤에 제약이 바뀌어 방향을 다시 잡았습니다.',
       '바뀐 방향으로 일정 다시 세우기',
       date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date + 21,
       'OPEN', d.id
FROM decisions d
WHERE (d.work_item_id % 4) = 0;

-- 그리고 뒤집힌 쪽은 대체됨이 됩니다. 열린 목록에 영영 남지 않으면서도
-- 뒤집혔다는 사실은 지워지지 않습니다.
UPDATE decisions SET status = 'SUPERSEDED', updated_at = now()
WHERE id IN (SELECT supersedes_id FROM decisions WHERE supersedes_id IS NOT NULL);

-- Issues that were raised and then cleared, so the clearance report has spans
-- to measure rather than an empty window. The spans differ on purpose: a report
-- of identical numbers cannot show whether it is measuring anything.
INSERT INTO work_item_issue_outcomes(work_item_id, ended_week, started_week, weeks, outcome, issue_text, recorded_by)
SELECT w.id,
       (date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - ((w.id % 11)::int) * 7),
       (date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - (((w.id % 11) + 1 + (w.id % 7))::int) * 7),
       1 + (w.id % 7),
       'RESOLVED',
       '외부 연동 지연이 해소되었습니다.',
       w.user_id
FROM work_items w
WHERE w.id % 13 = 0 AND EXISTS (SELECT 1 FROM report_items i WHERE i.work_item_id = w.id)
ON CONFLICT DO NOTHING;

-- A reviewer's note on some submitted weeks. The comment comes from somebody
-- other than the author where the organisation has anybody else, because a
-- review conversation with one voice is not one.
INSERT INTO report_comments(report_id, user_id, content)
SELECT r.id,
       coalesce((SELECT other.id FROM users other
                 WHERE other.organization_id = u.organization_id AND other.id <> r.user_id
                 ORDER BY other.id LIMIT 1), r.user_id),
       '진척 근거를 한 줄만 더 적어 주세요. 다음 주 회의에서 그대로 씁니다.'
FROM weekly_reports r
JOIN users u ON u.id = r.user_id
WHERE r.id % 41 = 0;

-- Who asked for their weekly report by mail, and what happened to it.
--
-- Both tables were empty on a seeded deployment, so 개인 설정 → 주간보고 메일
-- 발송 only ever showed its empty state: no address saved, no delivery, no
-- failure to read a reason from. That is the shape of gap this cycle found six
-- times, and each time it was hiding a defect in the screen behind it.
--
-- One writer in five asks for it. Enough that the card is populated for anybody
-- an operator happens to log in as, not so many that it looks like everybody.
INSERT INTO user_mail_settings(user_id, address, on_submit)
SELECT u.id, lower(u.username) || '@example.internal', true
FROM users u
WHERE (u.username LIKE 'u%' OR u.username LIKE 'hq%') AND (u.id % 5) = 0
ON CONFLICT (user_id) DO NOTHING;

-- The three states a delivery can be in, because the screen prints each one
-- differently and only one of them was ever reachable from a fixture: sent,
-- waiting with a reason and a time to try again, and given up on.
--
-- The reasons are the ones a relay actually gives. A row that says only 실패
-- teaches the reader nothing, which is the whole argument for keeping the
-- relay's own words.
INSERT INTO report_mail_deliveries(report_id, user_id, address, status, attempts,
                                   error_message, created_at, sent_at, next_attempt_at)
SELECT r.id, r.user_id, s.address,
       CASE WHEN (r.id % 17) = 0 THEN 'FAILED'
            WHEN (r.id % 11) = 0 THEN 'QUEUED'
            ELSE 'SENT' END,
       CASE WHEN (r.id % 17) = 0 THEN 5
            WHEN (r.id % 11) = 0 THEN 1 + (r.id % 3)
            ELSE 1 END,
       -- What a writer is actually shown. The first version stored the raw Go
       -- error, relay address and all, and that is exactly what the product
       -- used to do — so the seed reproduced the leak and made it look normal.
       -- These are the sentences mailUserMessage produces.
       -- What a writer is actually shown: the relay's own words where it
       -- answered, and a plain sentence where nothing did. The first version
       -- stored the raw dial error, relay address and all — which is what the
       -- product used to do, so the seed reproduced the leak and made it look
       -- like the normal shape.
       CASE WHEN (r.id % 17) = 0
              THEN (ARRAY['받는 주소를 릴레이가 거부했습니다: 550 5.1.1 mailbox unavailable',
                          '릴레이에 연결하지 못했습니다. 잠시 뒤 다시 시도합니다.'])[1 + (r.id % 2)]
            WHEN (r.id % 11) = 0
              THEN (ARRAY['릴레이와 암호화 연결을 맺지 못했습니다. 관리자에게 알려 주세요.',
                          '릴레이가 본문을 받지 않았습니다: 451 4.3.0 temporary local problem'])[1 + (r.id % 2)]
            ELSE '' END,
       -- Never later than now. The first version dated the current week's
       -- delivery from the week's Friday, which has not happened yet, so the
       -- screen said a report had been sent tomorrow.
       least(r.week_start + interval '4 days 16 hours', now() - interval '3 hours'),
       CASE WHEN (r.id % 17) = 0 OR (r.id % 11) = 0 THEN NULL
            ELSE least(r.week_start + interval '4 days 16 hours 2 minutes', now() - interval '2 hours') END,
       CASE WHEN (r.id % 11) = 0 THEN now() + interval '16 minutes'
            ELSE least(r.week_start + interval '4 days 16 hours', now() - interval '3 hours') END
FROM weekly_reports r
JOIN user_mail_settings s ON s.user_id = r.user_id
-- Only reports that were handed in. A delivery is queued by the submission, so
-- one attached to a 작성 중 report is a row the product cannot produce.
WHERE r.week_start > date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - 84 AND r.status <> 'DRAFT'
ON CONFLICT DO NOTHING;

-- ---------------------------------------------------------------------------
-- What this fixture is worth, in one row.
--
-- Six defects in a row this cycle were hidden by a fixture where everything
-- had the same value: every report APPROVED, every plan the same sentence, every
-- weekly movement one point, two issue sentences across 110,000 rows. Each time
-- the uniformity hid a product defect, and each time it was found by counting
-- rather than by walking the screens.
--
-- So the seed says what spread it produced. A formula edited later that quietly
-- collapses one of these back to a single value shows up here, on the run that
-- caused it, instead of years later as a screen nobody could read.
SELECT '다양성 점검' AS check,
       (SELECT count(DISTINCT status) FROM weekly_reports
         WHERE week_start = date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date) AS report_statuses,
       (SELECT count(DISTINCT issue) FROM report_items WHERE issue <> '') AS issue_sentences,
       (SELECT count(DISTINCT next_plan) FROM report_items) AS plan_sentences,
       (SELECT count(*) FROM report_items i JOIN weekly_reports r ON r.id = i.report_id
         WHERE r.week_start = date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date AND i.current_result = '') AS blank_results,
       (SELECT count(*) FROM report_items i JOIN weekly_reports r ON r.id = i.report_id
         WHERE r.week_start = date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date) AS rows_this_week,
       (SELECT count(*) FROM report_items i JOIN weekly_reports r ON r.id = i.report_id
         WHERE r.week_start = date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date - 7) AS rows_last_week,
       (SELECT count(DISTINCT status) FROM report_mail_deliveries) AS mail_states;

SELECT (SELECT count(*) FROM organizations) AS organizations,
       (SELECT count(*) FROM users) AS users,
       (SELECT count(*) FROM weekly_reports) AS reports,
       (SELECT count(*) FROM report_items) AS items,
       (SELECT count(*) FROM work_items) AS work_items,
       (SELECT count(*) FROM report_items WHERE work_item_id IS NULL) AS unlinked,
       (SELECT count(*) FROM work_item_links) AS dependencies,
       (SELECT count(*) FROM decisions) AS decisions,
       (SELECT count(*) FROM work_item_issue_outcomes) AS cleared_issues,
       (SELECT count(*) FROM report_comments) AS comments;

-- And it fails if one of them collapsed.
--
-- Printing the spread is not enough: a number in a wall of output is exactly
-- the kind of quiet signal this cycle kept finding people had walked past. The
-- thresholds are far below what the formulas above produce, so this fires only
-- when a dimension has genuinely gone flat — which is the state that makes the
-- whole fixture unable to show the feature it was built for.
DO $$
DECLARE
  statuses int; issues int; plans int; blanks int; mail_states int; wk date;
  ended_4w int; ended_12w int; due_buckets int; decision_states int;
BEGIN
  wk := date_trunc('week', (now() AT TIME ZONE 'Asia/Seoul')::date)::date;
  SELECT count(DISTINCT status) INTO statuses FROM weekly_reports WHERE week_start = wk;
  SELECT count(DISTINCT issue) INTO issues FROM report_items WHERE issue <> '';
  SELECT count(DISTINCT next_plan) INTO plans FROM report_items;
  SELECT count(*) INTO blanks FROM report_items i JOIN weekly_reports r ON r.id = i.report_id
    WHERE r.week_start = wk AND i.current_result = '';

  IF statuses < 4 THEN
    RAISE EXCEPTION '현재 주 보고서 상태가 %가지뿐입니다. 보고 분포와 제출률, 검토 대기 화면이 한 상태로 굳습니다.', statuses;
  END IF;
  IF issues < 5 THEN
    RAISE EXCEPTION '이슈 문장이 %가지뿐입니다. 경영 요약이 같은 문장 열 줄로 나옵니다.', issues;
  END IF;
  IF plans < 50 THEN
    RAISE EXCEPTION '차주 계획 문장이 %가지뿐입니다. 계획 반복 배지가 모든 줄에 붙어 아무것도 가리키지 못합니다.', plans;
  END IF;
  IF blanks = 0 THEN
    RAISE EXCEPTION '실적이 빈 줄이 하나도 없습니다. 보고 품질 점검의 네 규칙 중 하나가 실행되지 않습니다.';
  END IF;

  -- 끝난 업무가 있어야 '최근 N주' 가 뜻을 갖습니다. 이것이 0 이면 화면의
  -- 기간 선택이 4주든 52주든 같은 답을 냅니다. 실측한 적이 있습니다.
  SELECT count(*), count(*) FILTER (WHERE last_week < wk - 84) INTO ended_4w, ended_12w
    FROM (SELECT i.work_item_id, max(r.week_start) AS last_week
            FROM report_items i JOIN weekly_reports r ON r.id = i.report_id
           WHERE i.work_item_id IS NOT NULL
           GROUP BY i.work_item_id
          HAVING max(r.week_start) < wk - 28) t;
  IF ended_4w < 50 THEN
    RAISE EXCEPTION '최근 4주 안에 보고가 끊긴 업무가 %건뿐입니다. 기간 선택이 아무것도 가르지 못합니다.', ended_4w;
  END IF;
  IF ended_12w < 20 THEN
    RAISE EXCEPTION '12주 넘게 보고가 없는 업무가 %건뿐입니다. 넓은 기간과 좁은 기간이 같은 답을 냅니다.', ended_12w;
  END IF;

  -- 마감일 전망은 여섯 갈래이고 갈래마다 다른 문장과 배지를 그립니다. 날짜를
  -- 어디에 두느냐로 갈리므로, 서로 다른 자리 수가 줄면 어떤 갈래는 배포에서
  -- 한 번도 나오지 않습니다.
  SELECT count(DISTINCT due_date - wk) INTO due_buckets FROM work_items WHERE due_date IS NOT NULL;
  IF due_buckets < 5 THEN
    RAISE EXCEPTION '마감일이 %가지 자리에만 있습니다. 마감 전망 여섯 갈래 중 일부가 한 번도 나오지 않습니다.', due_buckets;
  END IF;

  -- 결정은 뒤집힙니다. 대체됨이 없으면 목록에서 빠지는 규칙도, 이전 결정을
  -- 가리키는 표시도, 열린 결정을 세는 자리도 확인할 수 없습니다.
  SELECT count(DISTINCT status) INTO decision_states FROM decisions;
  IF decision_states < 3 THEN
    RAISE EXCEPTION '결정 상태가 %가지뿐입니다. 대체된 결정이 없으면 뒤집힌 결정을 다루는 코드가 실행되지 않습니다.', decision_states;
  END IF;

  SELECT count(DISTINCT status) INTO mail_states FROM report_mail_deliveries;
  IF mail_states < 3 THEN
    RAISE EXCEPTION '메일 발송 상태가 %가지뿐입니다. 개인 설정의 발송 이력에서 보낸 것·기다리는 것·포기한 것을 구별할 수 없습니다.', mail_states;
  END IF;
END $$;
