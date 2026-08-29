-- Stretch an already-seeded deployment backwards in time.
--
-- scripts/seed-scale.sql makes a wide deployment — 300 people, a year of
-- reports. It does not make an old one, and the two hide different faults. A
-- year of history never exercises the rule that decides which weeks somebody
-- was owed, and never puts a three-year window behind a period report.
--
-- This copies the existing year backwards, once per :years, keeping every
-- report attached to the same work item so a task genuinely continues for
-- years rather than restarting. 364 days rather than 365: a whole number of
-- weeks, so every copied report lands on the same weekday as the original and
-- the week grid stays intact.
--
-- Measured on a deployment stretched to three years — 47,424 reports, 331,590
-- items, 304 MB — every read stayed under 350 ms, 120 concurrent writers with
-- 25 readers finished with no errors and the container at 66 MiB, and the
-- participation rule answered correctly in all three directions: weeks outside
-- the window are not owed, the still-open week is not owed, and two weeks
-- deleted from the middle of the window named exactly the one person.
--
-- The one thing to know before running it: `created_at` on every account stays
-- where it was, so after this the accounts look younger than their own
-- reports. That is not an artefact to work around — it is exactly what a
-- deployment that imported its history looks like, and it is the case
-- expectedFromWeek exists for.
--
-- It copies whatever is there, including anything you added by hand. A test
-- report written while poking at the product comes back two more times, and a
-- probe task with a 상위 조직 요청 on it then turns up by name in a management
-- highlight for a year it was never written in — measured, on this database.
-- Clean the deployment before stretching it, or stretch it before you start.
--
-- Run against a database seeded by seed-scale.sql (or a real copy):
--   docker exec -i <pg> psql -U postgres -d <db> -v years=2 -f - < scripts/seed-years.sql
-- Then restart the application. Takes a few seconds per year.

\if :{?years} \else \set years 2 \endif

BEGIN;

-- Only the newest year is a source. Copying from everything looks equivalent
-- and is not: once a copy exists it becomes a source too, and then two
-- different (source, n) pairs land on the same week — 2025-05-05 with n=1 and
-- 2024-05-06 with n=0 name the same Monday. A NOT EXISTS cannot see rows the
-- same statement is inserting, so the second run died on the unique key and
-- rolled back. The row counts were unchanged, which read exactly like success.
CREATE TEMP TABLE seed_years_source ON COMMIT DROP AS
SELECT r.* FROM weekly_reports r
WHERE r.week_start > (SELECT max(week_start) - 364 FROM weekly_reports);

INSERT INTO weekly_reports(user_id, week_start, summary, status, source_type,
                           submitted_at, reviewed_at, reviewed_by, created_at, updated_at, version)
SELECT r.user_id,
       r.week_start - (n * 364),
       r.summary, r.status, r.source_type,
       r.submitted_at - make_interval(days => n * 364),
       r.reviewed_at  - make_interval(days => n * 364),
       r.reviewed_by,
       r.created_at   - make_interval(days => n * 364),
       r.updated_at   - make_interval(days => n * 364),
       r.version
FROM seed_years_source r, generate_series(1, :years) n
ON CONFLICT (user_id, week_start) DO NOTHING;

INSERT INTO report_items(report_id, work_item_id, title, category,
                         current_result, next_plan, issue, management_ask, progress, sort_order)
SELECT copy.id, i.work_item_id, i.title, i.category,
       i.current_result, i.next_plan, i.issue, i.management_ask, i.progress, i.sort_order
FROM report_items i
JOIN seed_years_source r ON r.id = i.report_id
JOIN generate_series(1, :years) n ON true
JOIN weekly_reports copy ON copy.user_id = r.user_id
 AND copy.week_start = r.week_start - (n * 364)
-- A copy that already has its items is left alone, so a second run adds
-- nothing rather than doubling every week.
WHERE NOT EXISTS (SELECT 1 FROM report_items already WHERE already.report_id = copy.id);

COMMIT;

SELECT 'weekly_reports' AS 표, count(*)::text AS 값 FROM weekly_reports
UNION ALL SELECT 'report_items', count(*)::text FROM report_items
UNION ALL SELECT '주차 범위', min(week_start)::text || ' ~ ' || max(week_start)::text FROM weekly_reports;
