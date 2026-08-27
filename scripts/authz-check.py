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

Run: WEEKLY_TEST_POSTGRES_DSN=... python3 scripts/authz-check.py [--limit N]
"""
import argparse
import pathlib
import re
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


def sites():
    """Every refusal whose next statement is a bare return, in file order."""
    gated = router_gated_handlers()
    found, skipped, layered = [], [], []
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
            # The shape this can remove safely: the refusal, then a return. A
            # site that continues some other way is left alone and counted, so
            # a partial sweep never reads as a complete one.
            if index + 1 < len(lines) and lines[index + 1].strip() == "return":
                found.append((path, index, line.strip()))
            else:
                skipped.append((path, index, line.strip()))
    return found, skipped, layered


def run_suite(dsn_present):
    result = subprocess.run(
        ["go", "test", "./internal/app/", "-count=1"],
        cwd=ROOT, capture_output=True, text=True)
    return result.returncode == 0, result.stdout + result.stderr


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--limit", type=int, default=0, help="only the first N sites")
    args = parser.parse_args()

    found, skipped, layered = sites()
    if args.limit:
        found = found[: args.limit]
    print(f"권한 거부 {len(found)}곳을 하나씩 없애 봅니다"
          + (f" · {len(skipped)}곳은 모양이 달라 건너뜁니다" if skipped else "")
          + (f" · {len(layered)}곳은 라우터가 이미 막는 두 번째 층입니다" if layered else ""))
    for path, index, line in skipped:
        print(f"  건너뜀 {path.name}:{index + 1}  {line[:70]}")
    for path, index, line, handler in layered:
        print(f"  두 번째 층 {path.name}:{index + 1}  {handler}  {line[:52]}")
    print()

    ok, _ = run_suite(True)
    if not ok:
        print("먼저 손대지 않은 상태에서 시험이 통과해야 합니다.", file=sys.stderr)
        return 2

    unguarded = []
    # Sites whose removal did not compile. Nothing was learned about those, and
    # counting them as kept is the very confusion this check exists to catch:
    # "a test named after a rule is not the same as a test of that rule", and a
    # site nobody could test is not a site somebody is watching.
    unlearned = []
    for number, (path, index, line) in enumerate(found, start=1):
        original = path.read_text(encoding="utf-8")
        lines = original.split("\n")
        removed = lines[:index] + lines[index + 2:]
        path.write_text("\n".join(removed), encoding="utf-8")
        try:
            passed, output = run_suite(True)
        finally:
            path.write_text(original, encoding="utf-8")
        if passed:
            unguarded.append((path, index, line))
            mark = "!"
        elif "build failed" in output or "cannot use" in output:
            mark = "?"   # removing it did not compile; nothing was learned
            unlearned.append((path, index, line))
        else:
            mark = "."
        print(f"  {mark}  [{number}/{len(found)}] {path.name}:{index + 1}  {line[:66]}")

    print()
    if unlearned:
        print(f"{len(unlearned)}곳은 지워도 컴파일되지 않아 이 방법으로는 알 수 없습니다:")
        for path, index, line in unlearned:
            print(f"  {path.relative_to(ROOT)}:{index + 1}\n    {line}")
        print("  이 자리들은 다른 방법으로 확인해야 합니다.\n")
    if not unguarded:
        confirmed = len(found) - len(unlearned)
        print(f"권한 검사: {confirmed}곳은 없애면 시험이 알아차립니다"
              + (f". 나머지 {len(unlearned)}곳은 알 수 없습니다." if unlearned else "."))
        return 0
    print(f"{len(unguarded)}곳은 없애도 아무도 알아차리지 못합니다:")
    for path, index, line in unguarded:
        print(f"  {path.relative_to(ROOT)}:{index + 1}\n    {line}")
    print("\n이름이 규칙을 말하는 시험이 있어도, 그 규칙을 시험하는 것은 아닙니다.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
