"""Check that the numbers a period report shows are true.

Everything else in scripts/ measures a shape: how big a response is, how long it
takes, whether a guard runs, whether a table is read, whether the shipped files
install. Nothing asked whether a number on the screen is the number in the
database — and this product's numbers go into somebody's management report.

Two things this had to learn the hard way, both of them the checker being wrong
rather than the product:

1. A period covers every week that OVERLAPS it, not every week that STARTS in
   it. Counting `week_start BETWEEN start AND end` reports the product as one
   report short in every period, every time.

2. In organisation scope, totalItems is not a DISTINCT of anything. Within one
   owner the stored work item is the identity; across owners nothing stored
   spans people, so the report merges on the title and then fuzzily on titles
   close enough to be the same work. Measured on one organisation: 543 distinct
   work items, 267 distinct normalised titles, 256 after the fuzzy pass. Only a
   bound can be checked from SQL, so only a bound is.
"""
import argparse
import datetime
import http.cookiejar
import json
import subprocess
import sys
import urllib.request

PERIODS = [("MONTH", "2026-08"), ("QUARTER", "2026-Q3"), ("HALF", "2026-H1"), ("YEAR", "2026")]


def parse_args():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--base", default="http://127.0.0.1:8080")
    p.add_argument("--password", required=True)
    p.add_argument("--database", default="weekly", help="psql -d, read through the weekly-pg container")
    p.add_argument("--container", default="weekly-pg")
    p.add_argument("--periods", default=",".join(f"{k}:{v}" for k, v in PERIODS))
    p.add_argument("who", nargs="+", help="username, or username:TEAM for organisation scope")
    return p.parse_args()


def sign_in(base, user, password):
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    body = json.dumps({"username": user, "password": password}).encode()
    request = urllib.request.Request(base + "/api/v1/auth/login", body,
                                     {"Content-Type": "application/json", "Origin": base})
    opener.open(request).read()
    return opener


def fetch(opener, base, path):
    return json.loads(opener.open(urllib.request.Request(base + path, headers={"Origin": base})).read())


def query(container, database, statement):
    done = subprocess.run(["docker", "exec", container, "psql", "-U", "postgres", "-d", database, "-tAc", statement],
                          capture_output=True, text=True)
    if done.returncode != 0:
        raise SystemExit(f"psql: {done.stderr.strip()}")
    return [line.split("|") for line in done.stdout.strip().split("\n") if line]


def weeks_overlapping(start, end):
    """How many week starts produce a week that touches this period."""
    first = datetime.date.fromisoformat(start)
    last = datetime.date.fromisoformat(end)
    cursor = first - datetime.timedelta(days=first.weekday())
    count = 0
    while cursor <= last:
        if cursor + datetime.timedelta(days=6) >= first:
            count += 1
        cursor += datetime.timedelta(days=7)
    return count


def main():
    args = parse_args()
    periods = [tuple(entry.split(":", 1)) for entry in args.periods.split(",")]
    wrong = []
    checked = 0

    for entry in args.who:
        user, _, scope = entry.partition(":")
        scope = scope or "SELF"
        opener = sign_in(args.base, user, args.password)
        user_id = query(args.container, args.database, f"SELECT id FROM users WHERE username='{user}'")[0][0]
        if scope == "TEAM":
            who = f"""r.user_id IN (SELECT id FROM users WHERE organization_id IN (
                     WITH RECURSIVE t(id,depth) AS (
                       SELECT organization_id,0 FROM users WHERE id={user_id}
                       UNION ALL SELECT o.id,t.depth+1 FROM organizations o JOIN t ON o.parent_id=t.id
                       WHERE t.depth<16)
                     SELECT id FROM t))"""
        else:
            who = f"r.user_id={user_id}"

        for kind, period in periods:
            answer = fetch(opener, args.base, f"/api/v1/rollups?kind={kind}&period={period}&scope={scope}")
            if not answer.get("success"):
                wrong.append((user, scope, kind, period, "응답", answer["error"]["code"], "성공"))
                continue
            view = answer["data"]
            insights = view["insights"]
            start, end = view["start"], view["end"]
            checked += 1

            # Weeks that overlap the period, not weeks that start in it.
            rows = query(args.container, args.database, f"""
                SELECT count(*), count(DISTINCT it.work_item_id), count(DISTINCT r.id), count(DISTINCT r.week_start)
                FROM report_items it JOIN weekly_reports r ON r.id=it.report_id
                WHERE {who} AND r.week_start <= '{end}' AND r.week_start + 6 >= '{start}'""")[0]
            source_rows, work_items, reports, weeks = (int(value) for value in rows)

            def bad(what, got, want):
                wrong.append((user, scope, kind, period, what, got, want))

            total = insights["totalItems"]
            if insights["sourceItems"] != source_rows:
                bad("sourceItems", insights["sourceItems"], source_rows)
            if insights["sourceReports"] != reports:
                bad("sourceReports", insights["sourceReports"], reports)
            if insights["reportedWeeks"] != weeks:
                bad("reportedWeeks", insights["reportedWeeks"], weeks)
            if insights["expectedWeeks"] != weeks_overlapping(start, end):
                bad("expectedWeeks", insights["expectedWeeks"], weeks_overlapping(start, end))
            # Merging only ever folds rows together, so this is a bound in both
            # directions and an equality in neither.
            if total > work_items:
                bad("totalItems ≤ 업무 수", total, work_items)
            if source_rows > 0 and total == 0:
                bad("행이 있는데 업무가 0", total, "1 이상")

            # Every item lands in exactly one bucket of each partition.
            parts = insights["completedItems"] + insights["inProgressItems"] + insights["notStartedItems"]
            if parts != total:
                bad("완료+진행+미착수", parts, total)
            if insights["continuingItems"] + insights["oneOffItems"] != total:
                bad("연속+단발", insights["continuingItems"] + insights["oneOffItems"], total)
            for name in ("stalledItems", "carryoverItems", "issueItems", "askItems",
                         "persistentIssues", "noLandingItems", "missesPeriod"):
                if insights[name] > total:
                    bad(f"{name} ≤ 총건수", insights[name], total)

            # Rates are the counts they claim to be made of.
            if total > 0:
                want = round(insights["completedItems"] * 100 / total, 1)
                if abs(insights["completionRate"] - want) > 0.05:
                    bad("completionRate", insights["completionRate"], want)
            if insights["expectedWeeks"] > 0:
                want = round(insights["reportedWeeks"] * 100 / insights["expectedWeeks"], 1)
                if abs(insights["reportCoverage"] - want) > 0.05:
                    bad("reportCoverage", insights["reportCoverage"], want)
            if insights["reportedWeeks"] > insights["expectedWeeks"]:
                bad("보고한 주 ≤ 기대 주", insights["reportedWeeks"], insights["expectedWeeks"])

            # The arrays beside the headline have to describe the same period.
            if len(view["trend"]) != len(view["weeks"]):
                bad("trend 길이 = weeks 길이", len(view["trend"]), len(view["weeks"]))
            reported = sum(1 for week in view["trend"] if week["reports"] > 0)
            if reported != insights["reportedWeeks"]:
                bad("보고가 있는 주 = reportedWeeks", reported, insights["reportedWeeks"])
            if sum(week["reports"] for week in view["trend"]) != insights["sourceReports"]:
                bad("추이의 보고서 합", sum(week["reports"] for week in view["trend"]), insights["sourceReports"])
            if view["categories"]:
                if sum(row["items"] for row in view["categories"]) != total:
                    bad("분류 건수 합", sum(row["items"] for row in view["categories"]), total)
                share = round(sum(row["share"] for row in view["categories"]), 1)
                if total > 0 and abs(share - 100) > 0.6:
                    bad("분류 지분 합", share, 100)
            if view["contributors"]:
                people = sum(row["reports"] for row in view["contributors"])
                if people != insights["sourceReports"]:
                    bad("참여자 보고서 합", people, insights["sourceReports"])

    print("숫자 검사")
    for user, scope, kind, period, what, got, want in wrong:
        print(f"  실패 {user}/{scope} {kind} {period}: {what} — 화면 {got}, 실제 {want}")
    if wrong:
        print(f"숫자 검사: {checked}개 조합 중 {len(wrong)}개 값이 어긋납니다")
        sys.exit(1)
    print(f"숫자 검사: 통과 — {checked}개 조합의 값이 데이터베이스와 같습니다.")


if __name__ == "__main__":
    main()
