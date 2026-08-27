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
import hashlib
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
# Paths only an administrator may open. Running as anybody else used to report
# each of these as a defect, five findings in a row that were the product doing
# exactly what it should. A tool that cries wolf is one people stop reading, and
# the role-by-role sweep is the very thing that found a real defect in v0.101.
ADMIN_ONLY = (
    "/api/v1/admin/",
)

# Paths a plain writer cannot open either: these read across an organisation.
TEAM_ONLY = (
    "scope=TEAM",
    "/api/v1/team/",
    "/api/v1/insights/",
    "/api/v1/digest",
    "/api/v1/meeting?scope=TEAM",
    "/api/v1/rollups",
    "/api/v1/analytics/",
)


def reachable(path, role):
    """Whether this role is allowed to open this path at all."""
    if role == "ADMIN":
        return True
    if any(marker in path for marker in ADMIN_ONLY):
        return False
    if role in ("TEAM_LEADER", "ORG_MANAGER"):
        return True
    return not any(marker in path for marker in TEAM_ONLY)


def leaf_rows(body):
    """How many rows this answer actually carries, and whether it has any list.

    Counting bytes was the obvious rule and it was wrong: /api/v1/analytics/
    overview is 221 bytes of real numbers — 76 people, 126 open issues — and a
    byte threshold called it empty. A tool that cries wolf is one people stop
    reading.

    So count rows instead, in the deepest lists only. A change summary always
    has its seven groups whether or not anything happened, and the rows are one
    level below that; an answer whose innermost lists are all empty carries
    nothing however many wrappers it came in. An answer with no list at all is
    a summary object and is left alone.
    """
    counts = []

    def walk(node):
        """Fills counts; returns whether this subtree contains a list."""
        if isinstance(node, list):
            nested = False
            for element in node:
                if walk(element):
                    nested = True
            if not nested:
                counts.append(len(node))
            return True
        if isinstance(node, dict):
            return any([walk(value) for value in node.values()])
        return False

    try:
        payload = json.loads(body.decode("utf-8")).get("data")
    except Exception:
        return True, 1
    walk(payload)
    return bool(counts), sum(counts)


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
    "/api/v1/rollups/export.pptx?kind=YEAR&period={year}&scope=TEAM",
    "/api/v1/rollups/export.pptx?kind=MONTH&period={year}-03&scope=TEAM",
    "/api/v1/team/reports",
    "/api/v1/team/members",
    "/api/v1/reports",
    "/api/v1/handover",
    "/api/v1/changes?scope=TEAM",
    "/api/v1/changes?scope=SELF",
    "/api/v1/work-items/search?scope=TEAM&q=통합",
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
        # CSV and PPTX come through here. Decoding a .pptx as UTF-8 with
        # "replace" turns every byte it cannot read into the same character, so
        # two different decks could compare equal. A digest compares what was
        # actually sent.
        return hashlib.sha256(body).hexdigest()
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
    parser.add_argument("--settle", type=int, default=90,
                        help="seconds to wait for the deployment to stop changing before measuring (0 to skip)")
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

    # Ask the deployment who we are rather than being told. A --role flag would
    # be one more thing to get wrong, and the session already knows.
    status, body, _ = call("GET", "/api/v1/me")
    role = "ADMIN"
    if status == 200:
        try:
            role = json.loads(body)["data"]["user"]["role"]
        except (KeyError, ValueError):
            pass
    print(f"역할 {role} 로 확인합니다.\n")

    # Wait for the deployment to stop moving before measuring it.
    #
    # A freshly started service backfills work item links over every stored
    # report, and while that runs the derived lists genuinely change between
    # calls. Measuring then reports "호출마다 다름" for an endpoint that is
    # perfectly stable ten minutes later. That happened twice in one sitting,
    # and a tool that reports a defect which is not there is worse than one that
    # says nothing — it spends somebody's afternoon.
    findings, complete = [], []
    settle_path = "/api/v1/work-items?scope=TEAM&limit=1"
    if args.settle > 0:
        deadline = time.time() + args.settle
        previous, settled = None, False
        while time.time() < deadline:
            status, body, _ = call("GET", settle_path)
            if status != 200:
                break
            current = normalise(body)
            if previous is not None and current == previous:
                settled = True
                break
            previous = current
            time.sleep(3)
        if not settled and status == 200:
            print(f"아직 안정되지 않았습니다: {args.settle}초 동안 {settle_path} 의 답이 계속 바뀝니다.\n"
                  f"기동 직후라면 적재가 끝나기를 기다리십시오. 계속 진행하지만 아래 결과는 움직이는 대상을 잰 것입니다.\n",
                  file=sys.stderr)

    print(f"{'상태':>4} {'크기':>12} {'지연':>9}  경로")
    skipped = []
    hollow = []
    for template in PATHS:
        path = template.format(year=args.year)
        if not reachable(path, role):
            skipped.append(path)
            continue
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
            # No count beside the list. That is only a defect if the endpoint is
            # in fact returning a page, so ask it for one row and see. An
            # endpoint that ignores the parameter and returns the same bytes is
            # returning everything, which is complete and honest even without a
            # count; one that comes back shorter is paging without saying so,
            # and a reader has no way to know they are missing people.
            probe_status, probe_body, _ = call("GET", path + ("&" if "?" in path else "?") + "limit=1")
            if probe_status == 200 and normalise(probe_body) != normalise(body):
                findings_note = f"목록 {name} 이(가) limit 에 반응하는데 전체 건수를 말하지 않음 — 잘린 목록이 전부처럼 보임"
                notes.append(findings_note)
            else:
                complete.append(f"{path} · {name} (상한 없이 전량 반환)")
        # An answer with no rows in it is not a fast endpoint, it is an endpoint
        # that was asked about nothing. The bootstrap administrator owns no
        # work, so every screen scoped to the reader answers empty for them —
        # and this tool read /api/v1/changes that way while the same endpoint
        # returned 490 KB one query parameter over. Silence there looked like a
        # pass, and the byte figure beside it looked like a good one.
        if status == 200:
            has_list, rows = leaf_rows(body)
            if has_list and rows == 0:
                hollow.append(f"{path} ({len(body):,}B, 행 0)")
        mark = "  <== " + " · ".join(notes) if notes else ""
        print(f"{status:>4} {len(body):>11,}B {worst:>7.0f}ms  {path}{mark}")
        if notes:
            findings.append((path, notes))

    print()
    if hollow:
        # Not counted against the run: the endpoint did nothing wrong. What is
        # wrong is reading the number as evidence.
        print(f"잰 것이 없는 응답 {len(hollow)}건 — 아래 숫자는 무엇도 증명하지 않습니다")
        for line in hollow:
            print(f"  {line}")
        print(f"  이 계정({role})의 범위 안에 데이터가 없다는 뜻입니다. "
              f"업무를 가진 계정으로 다시 돌리거나 넓은 범위를 주십시오.\n")
    if complete:
        # Reported, not counted against the run. Returning everything is a
        # scaling question for whoever owns the screen, not a broken promise to
        # the reader.
        print(f"상한 없이 전량 반환 {len(complete)}건 (잘리지는 않음)")
        for line in complete:
            print(f"  {line}")
        print()
    if not findings:
        checked = len(PATHS) - len(skipped)
        if skipped:
            print(f"확인 {checked}개 경로, 지적 사항 없음 — {len(skipped)}개는 {role} 권한 밖이라 건너뜀: "
                  + ", ".join(skipped))
        else:
            print(f"확인 {len(PATHS)}개 경로, 지적 사항 없음")
        return 0
    if skipped:
        print(f"{len(skipped)}개 경로는 {role} 권한 밖이라 건너뜀: " + ", ".join(skipped) + "\n")
    print(f"지적 사항 {len(findings)}건 (확인 {len(PATHS) - len(skipped)}개 경로)")
    for path, notes in findings:
        print(f"  {path}")
        for note in notes:
            print(f"    - {note}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
