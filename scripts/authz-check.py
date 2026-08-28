#!/usr/bin/env python3
"""Remove each authorisation refusal in turn and see whether anything notices.

A test named after a rule is not the same as a test of that rule. v0.117 found
one that had been guarding nothing for its whole life: it asked to edit somebody
else's report without a version, was refused for the missing version, and
concluded the ownership check worked. Deleting `if ownerID != p.ID` entirely
left it green.

That was found by hand. This asks the same question of every refusal at once:
for each `writeError(..., 403, ...)` followed by a `return`, take both lines out
so the handler carries on as though the caller were allowed, and run the whole
suite. A refusal nobody misses is a rule nobody is keeping.

Not part of CI: one full suite per site is far too slow. Run it when the
authorisation surface changes.

**While this runs, the working tree is deliberately broken.** Each site is
edited out, the suite is run, and the file is put back — for forty minutes or
more. Anything else that builds or tests the same checkout in that window is
compiling sabotaged code, and what it sees is authorisation refusals that do not
refuse. That happened twice: a full suite run beside this one reported three
role gates answering 200 to non-administrators, and another reported a stranger
deleting somebody else's link. Both were read as intermittent product bugs and
one was written into the roadmap as an unexplained open question. Neither was
real; both were this tool, mid-mutation, in the same directory.

So it now says so, refuses to run twice at once, leaves a marker naming the file
it is holding open, and puts the file back even when it is killed.

Run: WEEKLY_TEST_POSTGRES_DSN=... python3 scripts/authz-check.py [--limit N]
"""
import argparse
import atexit
import os
import pathlib
import re
import signal
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
PACKAGE = ROOT / "internal" / "app"
REFUSAL = re.compile(r'writeError\(\s*w,\s*(?:http\.StatusForbidden|403)\s*,')
HANDLER = re.compile(r'^func \(a \*App\) (\w+)\(')
# A second layer can also live in middleware: apiKeyRequestAllowed turns an API
# key away from /mcp before the handler's own scope check is reached, so that
# check survives removal too. Detecting every such layer statically is not
# worth the machinery — when a refusal survives, ask first whether anything
# else already refuses, exactly as with an equivalent mutation.
#
# A route wrapped in requireRole refuses before its handler runs, so a role
# check inside that handler is a second layer. Removing it changes nothing
# anybody can observe, and reporting that as "nobody is keeping this rule"
# teaches the reader to distrust the tool — the same failure this project keeps
# fixing in the product.
ROUTER_GATE = re.compile(r'requireRole\(([^)]*)\)\(http\.HandlerFunc\(a\.(\w+)\)')


def router_gated_handlers():
    source = (ROOT / "internal" / "app" / "app.go").read_text(encoding="utf-8")
    return {handler for _, handler in ROUTER_GATE.findall(source)}


def enclosing_handler(lines, index):
    for number in range(index, -1, -1):
        match = HANDLER.match(lines[number])
        if match:
            return match.group(1)
    return ""


# Refusals that cannot be reached today, with the reason each one stays.
#
# Keyed by file and error code rather than line number, because a line number
# is wrong after the next edit and a silent exception is worse than none.
# Listed rather than pattern-matched, for the reason reachability_test.go gives
# about its own list: "probably fine" is how the last three got in.
#
# A sweep reports these as unguarded correctly and every time — nothing can
# reach them, so nothing can notice them going. Without the list, whoever runs
# this next does the same investigation again and reaches the same answer.
SECOND_LAYER = {
    ("mcp.go", "MCP_SCOPE_REQUIRED"):
        "apiKeyRequestAllowed 가 /mcp 를 부르는 키에 이미 mcp:read 를 요구합니다. "
        "두 규칙은 바뀔 이유가 달라 남겨 둡니다.",
}


def sites():
    """Every refusal whose next statement is a bare return, in file order."""
    gated = router_gated_handlers()
    found, skipped, layered, second = [], [], [], []
    for path in sorted(PACKAGE.glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        lines = path.read_text(encoding="utf-8").split("\n")
        for index, line in enumerate(lines):
            if not REFUSAL.search(line):
                continue
            handler = enclosing_handler(lines, index)
            if handler in gated:
                layered.append((path, index, line.strip(), handler))
                continue
            code = re.search(r'"([A-Z_]+)"', line)
            if code and (path.name, code.group(1)) in SECOND_LAYER:
                second.append((path, index, line.strip(), SECOND_LAYER[(path.name, code.group(1))]))
                continue
            # The shape this can remove safely: the refusal, then a return. A
            # site that continues some other way is left alone and counted, so
            # a partial sweep never reads as a complete one.
            if index + 1 < len(lines) and lines[index + 1].strip() == "return":
                found.append((path, index, line.strip()))
            else:
                skipped.append((path, index, line.strip()))
    return found, skipped, layered, second


FAILED_TEST = re.compile(r"^--- FAIL: (\w+)", re.M)


def run_suite(dsn_present):
    """Whether the suite passed, its output, and which tests failed by name.

    The names matter. Judging a refusal by pass/fail alone means **any** failure
    reads as "a test is keeping this rule", and a flake anywhere in the suite
    turns an unguarded refusal into a guarded one — the exact false negative
    this check exists to prevent. Measured: mcp.go:56 came back guarded in one
    run and unguarded in the next, and removing it by hand leaves the suite
    green. It is a second line behind the middleware's scope table and nothing
    can reach it; the first run had simply failed for its own reasons.
    """
    result = subprocess.run(
        ["go", "test", "./internal/app/", "-count=1"],
        cwd=ROOT, capture_output=True, text=True)
    output = result.stdout + result.stderr
    return result.returncode == 0, output, sorted(set(FAILED_TEST.findall(output)))


MARKER = ROOT / ".authz-check-running"


def claim_the_tree():
    """Refuse to start beside another run, and say the tree is being mutated.

    The marker is the part that matters. It is not for this process — it is for
    whoever is about to run `go test` in the same directory and wonder why an
    authorisation test just failed.
    """
    if MARKER.exists():
        owner = MARKER.read_text(encoding="utf-8").strip()
        print(f"이미 돌고 있습니다: {MARKER}\n  {owner}\n"
              "  같은 체크아웃에서 둘이 동시에 파일을 고치면 서로의 원본을 덮어씁니다.\n"
              f"  그 실행이 죽어서 남은 파일이라면 지우고 다시 시작하세요: rm {MARKER}",
              file=sys.stderr)
        return False
    MARKER.write_text(f"pid {os.getpid()} · 권한 검사가 작업 트리를 고치는 중입니다\n", encoding="utf-8")
    print("─" * 72)
    print("이 검사가 도는 동안 작업 트리는 **일부러 망가진 상태**입니다.")
    print("한 번에 한 곳씩 권한 거부를 지웠다가 되돌립니다. 수십 분 걸립니다.")
    print("그동안 같은 디렉터리에서 build 나 test 를 돌리지 마십시오 —")
    print("거부하지 않는 권한 검사를 보게 되고, 그것은 제품의 결함이 아닙니다.")
    print("─" * 72)
    return True


def release_the_tree():
    MARKER.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--limit", type=int, default=0, help="only the first N sites")
    args = parser.parse_args()

    if not claim_the_tree():
        return 2
    atexit.register(release_the_tree)

    found, skipped, layered, second = sites()
    if args.limit:
        found = found[: args.limit]
    print(f"권한 거부 {len(found)}곳을 하나씩 없애 봅니다"
          + (f" · {len(skipped)}곳은 모양이 달라 건너뜁니다" if skipped else "")
          + (f" · {len(layered)}곳은 라우터가 이미 막는 두 번째 층입니다" if layered else ""))
    for path, index, line in skipped:
        print(f"  건너뜀 {path.name}:{index + 1}  {line[:70]}")
    for path, index, line, handler in layered:
        print(f"  두 번째 층 {path.name}:{index + 1}  {handler}  {line[:52]}")
    for path, index, line, why in second:
        print(f"  닿지 않는 두 번째 층 {path.name}:{index + 1}  {line[:48]}\n      {why}")
    print()

    ok, _, _ = run_suite(True)
    if not ok:
        print("먼저 손대지 않은 상태에서 시험이 통과해야 합니다.", file=sys.stderr)
        return 2

    unguarded = []
    # Which tests failed for each site that was judged guarded. A test that
    # turns up as the only keeper of many unrelated refusals is far more likely
    # to be flaky than to be guarding all of them.
    keepers = {}
    # Sites whose removal did not compile. Nothing was learned about those, and
    # counting them as kept is the very confusion this check exists to catch:
    # "a test named after a rule is not the same as a test of that rule", and a
    # site nobody could test is not a site somebody is watching.
    unlearned = []
    for number, (path, index, line) in enumerate(found, start=1):
        original = path.read_text(encoding="utf-8")
        lines = original.split("\n")
        removed = lines[:index] + lines[index + 2:]

        # A finally covers an exception and not a kill. Ctrl-C, a timeout, or
        # the runner deciding it no longer needs this run all leave the file
        # edited, and the next person to build finds a check that is simply
        # gone. So the same restore is armed on the signals too, and the marker
        # names the file while it is open.
        def put_it_back(*_):
            path.write_text(original, encoding="utf-8")
            release_the_tree()
            sys.exit(130)

        previous = {number: signal.signal(number, put_it_back)
                    for number in (signal.SIGINT, signal.SIGTERM)}
        MARKER.write_text(f"pid {os.getpid()} · {path.name}:{index + 1} 을 지운 상태입니다\n", encoding="utf-8")
        path.write_text("\n".join(removed), encoding="utf-8")
        try:
            passed, output, failures = run_suite(True)
        finally:
            path.write_text(original, encoding="utf-8")
            for number, handler in previous.items():
                signal.signal(number, handler)
            MARKER.write_text(f"pid {os.getpid()} · 권한 검사가 작업 트리를 고치는 중입니다\n", encoding="utf-8")
        if not passed:
            keepers[(path.name, index)] = failures
        if passed:
            unguarded.append((path, index, line))
            mark = "!"
        elif "build failed" in output or "cannot use" in output:
            mark = "?"   # removing it did not compile; nothing was learned
            unlearned.append((path, index, line))
        else:
            mark = "."
        names = keepers.get((path.name, index), [])
        kept_by = ("  ← " + ", ".join(names[:2]) + ("…" if len(names) > 2 else "")) if names else ""
        print(f"  {mark}  [{number}/{len(found)}] {path.name}:{index + 1}  {line[:52]}{kept_by}")

    print()
    if unlearned:
        print(f"{len(unlearned)}곳은 지워도 컴파일되지 않아 이 방법으로는 알 수 없습니다:")
        for path, index, line in unlearned:
            print(f"  {path.relative_to(ROOT)}:{index + 1}\n    {line}")
        print("  이 자리들은 다른 방법으로 확인해야 합니다.\n")
    # One test standing as the sole keeper of many refusals is a flake
    # signature, not a guard: a refusal in one handler does not usually decide
    # whether an unrelated test passes.
    sole = {}
    for site, names in keepers.items():
        if len(names) == 1:
            sole.setdefault(names[0], []).append(site)
    for name, held in sole.items():
        if len(held) > max(3, len(found) // 3):
            print(f"⚠ {name} 하나가 {len(held)}곳의 유일한 가드로 나옵니다. "
                  f"가드가 아니라 흔들리는 시험일 수 있으니 그 자리들을 손으로 확인하십시오.")
            for path_name, index in held[:5]:
                print(f"    {path_name}:{index + 1}")
            print()
    if not unguarded:
        confirmed = len(found) - len(unlearned)
        # The last line is the one people quote, so it carries the whole
        # picture: what was proved, what could not be, and what was never
        # swept. A number that describes only the swept sites reads as if it
        # described every refusal.
        tail = ""
        if unlearned:
            tail += f" · {len(unlearned)}곳은 알 수 없습니다"
        if skipped:
            tail += f" · {len(skipped)}곳은 모양이 달라 손으로 확인해야 합니다"
        if layered:
            tail += f" · {len(layered)}곳은 라우터가 막는 두 번째 층입니다"
        if second:
            tail += f" · {len(second)}곳은 닿지 않는 두 번째 층으로 이유가 적혀 있습니다"
        print(f"권한 검사: {confirmed}곳은 없애면 시험이 알아차립니다{tail}.")
        return 0
    print(f"{len(unguarded)}곳은 없애도 아무도 알아차리지 못합니다:")
    for path, index, line in unguarded:
        print(f"  {path.relative_to(ROOT)}:{index + 1}\n    {line}")
    print("\n이름이 규칙을 말하는 시험이 있어도, 그 규칙을 시험하는 것은 아닙니다.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
