#!/usr/bin/env python3
"""Call every read endpoint against a large deployment and report what comes back.

Three defects in a row were invisible until the data was big: a 23.6 MB work
item list, a collaboration ranking that reshuffled on every refresh, and 522 KB
of weekly text in a period report that no chart draws. None of them showed on a
nine-item development database, and all three appeared within minutes of putting
300 people and a year of reports behind the same screens.

That is a method, and doing it by hand means the next feature reintroduces the
same fault with nobody watching. This runs it: every endpoint, five times each,
reporting size and latency and whether the answer was the same every time.

It checks three things, and each one is a rule this project already adopted:

  * Size. A response larger than --max-bytes is reported. There is no correct
    number here — the point is that somebody has to look at a list that grew,
    not that 2 MB is a law.
  * Stability. The same request, five times, must produce the same bytes. A
    ranked list that reorders between refreshes is not a ranking. Fields that
    are meant to differ (generatedAt, traceId) are excluded by name rather than
    by guessing which differences matter.
  * Caps. A response holding a list must say how many exist. Reporting a page
    as the whole set is how a team leader undercounts their own team.

Usage:
    python3 scripts/scale-check.py --base http://127.0.0.1:8080 \
        --username admin --password '...'

Exit code is 1 when something is reported, so it can gate a release.
"""
import argparse
import http.cookiejar
import json
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

# Read paths worth measuring at scale. Endpoints that fold a whole organisation
# into one answer come first, because those are the ones that have gone wrong.
PATHS = [
    "/api/v1/work-items?scope=TEAM",
    "/api/v1/work-items?scope=SELF",
    "/api/v1/meeting?scope=TEAM",
    "/api/v1/digest",
    "/api/v1/insights/work-graph",
    "/api/v1/rollups?kind=YEAR&period={year}&scope=TEAM",
    "/api/v1/rollups?kind=QUARTER&period={year}-Q1&scope=TEAM",
    "/api/v1/rollups?kind=MONTH&period={year}-03&scope=TEAM",
    "/api/v1/rollups/export.csv?kind=YEAR&period={year}&scope=TEAM",
    "/api/v1/team/reports",
    "/api/v1/team/members",
    "/api/v1/reports",
    "/api/v1/handover",
    "/api/v1/changes",
    "/api/v1/analytics/overview",
    "/api/v1/admin/analytics/keywords",
    "/api/v1/admin/analytics/organizations",
    "/api/v1/admin/analytics/participation",
    "/api/v1/admin/users",
    "/api/v1/admin/audit",
    "/api/v1/search?q=인증",
    "/api/v1/decisions/open",
]

# Values that are supposed to change between two identical requests. Listed by
# name: deciding at comparison time which differences are acceptable is how a
# real instability gets waved through as noise.
VOLATILE = ("generatedAt", "traceId")


def normalise(body: bytes) -> str:
    """A comparable form of a response, with the deliberately-varying parts out."""
    try:
        parsed = json.loads(body)
    except ValueError:
        return body.decode("utf-8", "replace")
    if isinstance(parsed, dict):
        parsed.pop("traceId", None)
    text = json.dumps(parsed, sort_keys=True, ensure_ascii=False)
    for name in VOLATILE:
        text = re.sub(rf'"{name}": "[^"]*"', f'"{name}": "X"', text)
    return text


LIST_SIZE_TO_QUESTION = 200


def _count_names(key: str):
    """Every spelling a count for `key` might reasonably use.

    Written out because guessing one form and reporting everything else as a
    violation is how a check stops being read. `duplicates` is counted by
    `duplicateTotal` in this codebase — singular — and an earlier version of
    this script reported that as a defect five runs in a row.
    """
    singular = key[:-1] if key.endswith("s") else key
    return {name.lower() for name in (
        "total", f"{key}Total", f"{singular}Total", f"total{key}", f"total{singular}",
        f"{key}Count", f"{singular}Count",
    )}


def lists_without_a_total(body: bytes):
    """Lists big enough to be a page, with nothing saying how many exist.

    A reader cannot tell a complete list from a truncated one unless the
    response says. That is the rule this project adopted in v0.25, and it has
    been broken twice since by endpoints written after it.

    Counts nested one level down are accepted: a period report keeps its
    figures under `insights`, and demanding they be lifted to the top would be
    a demand about layout, not about whether the reader can tell.
    """
    try:
        parsed = json.loads(body)
    except ValueError:
        return []
    data = parsed.get("data") if isinstance(parsed, dict) else parsed
    if isinstance(data, list):
        return ["(최상위 배열)"] if len(data) >= LIST_SIZE_TO_QUESTION else []
    if not isinstance(data, dict):
        return []
    available = {key.lower() for key in data}
    for value in data.values():
        if isinstance(value, dict):
            available |= {key.lower() for key in value}
    missing = []
    for key, value in data.items():
        if not isinstance(value, list) or len(value) < LIST_SIZE_TO_QUESTION:
            continue
        if _count_names(key) & available:
            continue
        missing.append(key)
    return missing


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base", default="http://127.0.0.1:8080")
    parser.add_argument("--username", required=True)
    parser.add_argument("--password", required=True)
    parser.add_argument("--year", default=str(time.gmtime().tm_year))
    parser.add_argument("--repeat", type=int, default=5,
                        help="calls per endpoint; one call cannot show instability")
    parser.add_argument("--max-bytes", type=int, default=2_000_000)
    parser.add_argument("--max-ms", type=int, default=3000)
    args = parser.parse_args()

    opener = urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def call(method, path, payload=None):
        body = json.dumps(payload).encode() if payload is not None else None
        request = urllib.request.Request(
            args.base + urllib.parse.quote(path, safe="/?&=%"), data=body, method=method,
            headers={"Content-Type": "application/json", "Origin": args.base})
        started = time.time()
        try:
            with opener.open(request) as response:
                return response.status, response.read(), (time.time() - started) * 1000
        except urllib.error.HTTPError as error:
            return error.code, error.read(), (time.time() - started) * 1000

    status, body, _ = call("POST", "/api/v1/auth/login",
                           {"username": args.username, "password": args.password})
    if status != 200:
        print(f"로그인 실패 {status}: {body[:200].decode('utf-8', 'replace')}", file=sys.stderr)
        return 2

    findings = []
    print(f"{'상태':>4} {'크기':>12} {'지연':>9}  경로")
    for template in PATHS:
        path = template.format(year=args.year)
        seen, status, body, worst = set(), None, b"", 0.0
        for _ in range(args.repeat):
            status, body, took = call("GET", path)
            seen.add(normalise(body))
            worst = max(worst, took)
        notes = []
        if status != 200:
            notes.append(f"{status}")
        if len(seen) > 1:
            notes.append(f"호출마다 다름({len(seen)}가지)")
        if len(body) > args.max_bytes:
            notes.append(f"{len(body):,}B > 상한 {args.max_bytes:,}B")
        if worst > args.max_ms:
            notes.append(f"{worst:.0f}ms > 상한 {args.max_ms}ms")
        for name in lists_without_a_total(body):
            notes.append(f"목록 {name}({len(json.loads(body).get('data', {}).get(name, [])) if isinstance(json.loads(body).get('data'), dict) else '?'}건) 옆에 전체 건수가 없어 완전한 목록인지 알 수 없음")
        mark = "  <== " + " · ".join(notes) if notes else ""
        print(f"{status:>4} {len(body):>11,}B {worst:>7.0f}ms  {path}{mark}")
        if notes:
            findings.append((path, notes))

    print()
    if not findings:
        print(f"확인 {len(PATHS)}개 경로, 지적 사항 없음")
        return 0
    print(f"지적 사항 {len(findings)}건")
    for path, notes in findings:
        print(f"  {path}")
        for note in notes:
            print(f"    - {note}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
