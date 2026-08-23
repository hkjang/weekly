#!/usr/bin/env python3
"""Monday morning: everybody saves a report at once while the leaders read.

Every measurement before this one sent a single request at a time, and that
missed the failure that actually takes a deployment down. Argon2id reserves
64 MiB for the length of one password check, so 250 people signing in at nine
on Monday reserved sixteen gigabytes between them and the container was killed
on the busiest minute of the week — with the response sizes and the query plans
all perfectly healthy.

So this makes the shape of that minute: N people opening a session and saving
their week, M leaders opening the rollup, the meeting and the digest, all at the
same time. Latency percentiles come out of it, but the number to watch is the
resident memory of the container while it runs.

Usage:
    python3 scripts/load-check.py http://127.0.0.1:8080 250 50 4

    (base URL, concurrent writers, concurrent readers, rounds each)

The accounts it signs in as are the ones scripts/seed-scale.sql creates —
u1..u300 with a shared password, and the every-25th user is a team leader,
which is who the read side pretends to be. Point it at a scale seed, never at a
deployment holding real work.
"""
import json, statistics, sys, threading, time, urllib.request, http.cookiejar, urllib.parse
BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:19076"
WRITERS = int(sys.argv[2]) if len(sys.argv) > 2 else 100
READERS = int(sys.argv[3]) if len(sys.argv) > 3 else 20
ROUNDS  = int(sys.argv[4]) if len(sys.argv) > 4 else 3

def session():
    return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

def call(op, m, p, b=None):
    d = json.dumps(b).encode() if b is not None else None
    r = urllib.request.Request(BASE + urllib.parse.quote(p, safe="/?&=%"), data=d, method=m,
                               headers={"Content-Type": "application/json", "Origin": BASE})
    t = time.time()
    try:
        with op.open(r) as x:
            return x.status, x.read(), (time.time() - t) * 1000
    except urllib.error.HTTPError as e:
        return e.code, e.read(), (time.time() - t) * 1000
    except Exception as e:
        return 0, str(e).encode(), (time.time() - t) * 1000

results = {"write": [], "read": []}
errors = []
lock = threading.Lock()

def writer(index):
    op = session()
    code, body, _ = call(op, "POST", "/api/v1/auth/login",
                         {"username": f"u{index+1}", "password": "WeeklyVerify1234"})
    if code != 200:
        with lock: errors.append(("login", code, body[:120].decode("utf-8", "replace")))
        return
    code, body, _ = call(op, "GET", "/api/v1/me")
    week = json.loads(body)["data"]["currentWeekStart"] if code == 200 else "2026-08-24"
    # 실제 월요일 패턴은 새 보고서를 만드는 것이 아니라 이번 주 초안을 고치는 것이다.
    code, body, _ = call(op, "GET", "/api/v1/reports/current")
    report = json.loads(body).get("data") if code == 200 else None
    if not report or not report.get("id"):
        code, body, ms = call(op, "POST", "/api/v1/reports",
                              {"weekStart": week, "items": [{"title": "부하 업무 0", "currentResult": "시작",
                                                             "nextPlan": "계속", "progress": 10, "sortOrder": 0}]})
        with lock:
            results["write"].append(ms)
            if code >= 300:
                errors.append(("create", code, body[:160].decode("utf-8", "replace")))
                return
        report = json.loads(body).get("data")
    report_id = report["id"]
    version = report.get("version", 1)
    for round_index in range(ROUNDS):
        items = [{"title": f"부하 업무 {slot}", "currentResult": f"{round_index}회차 진행",
                  "nextPlan": "계속", "progress": 10 + slot, "sortOrder": slot} for slot in range(7)]
        code, body, ms = call(op, "PUT", f"/api/v1/reports/{report_id}",
                              {"summary": f"{round_index}회차", "version": version, "items": items})
        with lock:
            results["write"].append(ms)
            if code >= 300:
                errors.append(("save", code, body[:160].decode("utf-8", "replace")))
        if code == 200:
            try:
                version = json.loads(body)["data"]["version"]
            except Exception:
                version += 1

def reader(index):
    op = session()
    code, body, _ = call(op, "POST", "/api/v1/auth/login",
                         {"username": f"u{((index % 12) + 1) * 25}", "password": "WeeklyVerify1234"})
    if code != 200:
        with lock: errors.append(("login-reader", code, body[:120].decode("utf-8", "replace")))
        return
    paths = ["/api/v1/rollups?kind=MONTH&period=2026-08&scope=TEAM",
             "/api/v1/meeting?scope=TEAM", "/api/v1/digest", "/api/v1/team/reports"]
    for round_index in range(ROUNDS):
        for path in paths:
            code, body, ms = call(op, "GET", path)
            with lock:
                results["read"].append(ms)
                if code >= 300:
                    errors.append(("read", code, path))

threads = [threading.Thread(target=writer, args=(i,)) for i in range(WRITERS)]
threads += [threading.Thread(target=reader, args=(i,)) for i in range(READERS)]
started = time.time()
for t in threads: t.start()
for t in threads: t.join()
elapsed = time.time() - started

def report(name, values):
    if not values:
        print(f"{name}: 없음"); return
    values.sort()
    def pct(p): return values[min(len(values)-1, int(len(values)*p))]
    print(f"{name:6} n={len(values):>5}  중앙 {statistics.median(values):7.0f}ms  "
          f"p90 {pct(0.90):7.0f}ms  p99 {pct(0.99):7.0f}ms  최대 {values[-1]:7.0f}ms")

print(f"동시 쓰기 {WRITERS}명 · 읽기 {READERS}명 · {ROUNDS}회전 · 총 {elapsed:.1f}초")
report("저장", results["write"])
report("조회", results["read"])
print(f"오류 {len(errors)}건")
for kind, code, detail in errors[:8]:
    print(f"  {kind} {code} {detail}")
