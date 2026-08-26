#!/usr/bin/env python3
"""Break the code a guard claims to protect, and see whether the guard notices.

guard-check.py answers "did this test run its subject". It cannot answer the
harder question, and v0.81 showed why that matters: two tests ran their subject,
asserted the right things, and still could not fail. One searched for a word
that matched nothing, so removing the whole feature left the result unchanged.
The other exercised a disabled gateway that was also unconfigured, so loosening
the check made no difference.

Nothing about coverage distinguishes those from real guards. Changing the code
does.

For every `// guards: F` marker this applies small edits to F — flipping a
comparison, inverting a boolean operator, turning a returned constant around —
and runs that one test. A mutation the test does not catch is reported. It is
not proof the guard is weak; some mutations are semantically harmless. It is a
list of places to look, and every entry is a question worth answering.

This is slow — one compile and one test run per mutation — so it is not a CI
step. Run it after writing guards, the way scripts/scale-check.py is run after
changing a list endpoint.

Usage:
    python3 -u scripts/mutation-check.py [--package ./internal/app] [--test TestName]
                                         [--limit N] [--list]

Use -u, or redirect to a file and watch nothing appear for twenty minutes:
Python buffers stdout when it is not a terminal, and this run is long enough
that the difference matters.

It edits files under --package in place and restores them, so nothing else may
build or edit that tree while it runs.

Exit code is 1 when a mutation survives.
"""
import argparse
import atexit
import os
import pathlib
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time

GUARD_MARKER = re.compile(r"^\s*//\s*guards:\s*(.+?)\s*$")
TEST_FUNC = re.compile(r"^func (Test\w+)\(")

# (pattern, replacement, description). Applied one occurrence at a time, inside
# the guarded function only.
MUTATIONS = [
    (r"(?<![<>=!])<=(?!=)", "<", "<= → <"),
    (r"(?<![<>=!])>=(?!=)", ">", ">= → >"),
    (r"(?<![<>=!+-])<(?![=-])", ">", "< → >"),
    (r"(?<![<>=!])>(?![=])", "<", "> → <"),
    (r"(?<![&])&&(?![&])", "||", "&& → ||"),
    (r"(?<![|])\|\|(?![|])", "&&", "|| → &&"),
    (r"(?<![<>=!])==(?!=)", "!=", "== → !="),
    (r"(?<![<>=!])!=(?!=)", "==", "!= → =="),
    (r"\breturn true\b", "return false", "return true → false"),
    (r"\breturn false\b", "return true", "return false → true"),
]


# A run that is interrupted must not leave the tree mutated. The mutation is a
# plausible-looking one-character edit in a file nobody expects to have changed,
# and the next commit carries it. Ctrl-C and a CI timeout both arrive as signals
# that skip every finally block, so the restore is registered here instead.
_IN_FLIGHT = {}


def _restore_all(*_):
    for path, original in list(_IN_FLIGHT.items()):
        try:
            pathlib.Path(path).write_text(original, encoding="utf-8")
        except OSError:
            pass
    _IN_FLIGHT.clear()


atexit.register(_restore_all)
for _signal in (signal.SIGINT, signal.SIGTERM):
    signal.signal(_signal, lambda number, frame: (_restore_all(), sys.exit(130)))


def resolve_base(base):
    """A base that git cannot resolve is not an error worth stopping for.

    The first push of a branch reports an all-zero previous commit, and a
    shallow clone may not carry the one it names. Falling back to the previous
    commit checks something rather than nothing."""
    if base and set(base) != {"0"}:
        probe = subprocess.run(["git", "rev-parse", "--verify", "--quiet", base + "^{commit}"],
                               capture_output=True, text=True)
        if probe.returncode == 0:
            return base
        print(f"기준 커밋 {base} 을(를) 찾을 수 없어 HEAD~1 로 대신합니다.")
    return "HEAD~1"


def changed_line_numbers(base, package_dir):
    """Map path → set of line numbers the diff against base touched.

    The comparison is against the working tree, not against HEAD, so this
    answers the question before a commit as well as after one."""
    result = subprocess.run(
        ["git", "diff", "--unified=0", base, "--", package_dir],
        capture_output=True, text=True)
    if result.returncode != 0:
        raise SystemExit(f"git diff {base} 실패: {result.stderr.strip()}")
    touched, path = {}, None
    # A file git has never seen is not in the diff, and a brand new guard is
    # exactly the one worth checking. Count every line of it as changed.
    untracked = subprocess.run(
        ["git", "ls-files", "--others", "--exclude-standard", "--", package_dir],
        capture_output=True, text=True)
    for name in untracked.stdout.split():
        if name.endswith(".go"):
            count = len(pathlib.Path(name).read_text(encoding="utf-8").splitlines())
            touched[name] = set(range(1, count + 1))
    for line in result.stdout.splitlines():
        if line.startswith("+++ b/"):
            path = line[6:]
            continue
        if line.startswith("@@") and path:
            # @@ -old,count +new,count @@
            span = line.split("+", 1)[1].split(" ", 1)[0]
            start, _, count = span.partition(",")
            start, count = int(start), int(count or 1)
            touched.setdefault(path, set()).update(range(start, start + max(count, 1)))
    return touched


def functions_touched(package_dir, base):
    """Names of top-level funcs whose body the diff changed, plus the guard
    tests the diff itself added or edited.

    A weak guard is created two ways: the subject grows a branch nobody checks,
    or a new guard is written that cannot fail. Both are in the diff, and both
    are worth the minute this costs — the whole suite is not."""
    touched = changed_line_numbers(base, package_dir)
    subjects, tests = set(), set()
    for path, lines in touched.items():
        file = pathlib.Path(path)
        if not file.exists() or file.suffix != ".go":
            continue
        source = file.read_text(encoding="utf-8").splitlines()
        bounds = []
        for index, line in enumerate(source):
            match = re.match(r"^func (?:\([^)]*\) )?([A-Za-z0-9_]+)", line)
            if match:
                bounds.append((index + 1, match.group(1)))
        for position, name in bounds:
            end = len(source)
            for later, _ in bounds:
                if later > position:
                    end = later - 1
                    break
            if any(position <= number <= end for number in lines):
                (tests if file.name.endswith("_test.go") else subjects).add(name)
    return subjects, tests


def collect_guards(package_dir):
    guards = []
    for path in sorted(pathlib.Path(package_dir).glob("*_test.go")):
        pending = None
        for line in path.read_text(encoding="utf-8").splitlines():
            marker = GUARD_MARKER.match(line)
            if marker:
                names = []
                for entry in marker.group(1).split(","):
                    name = entry.partition("=")[0].strip()
                    if name:
                        names.append(name)
                pending = names
                continue
            test = TEST_FUNC.match(line)
            if test:
                if pending:
                    guards.append({"test": test.group(1), "subjects": pending})
                pending = None
                continue
            if pending and not line.lstrip().startswith("//"):
                pending = None
    return guards


def find_function(package_dir, name):
    """Return (path, start, end) line numbers for a top-level func by name."""
    declaration = re.compile(r"^func (?:\([^)]*\) )?" + re.escape(name) + r"\b")
    for path in sorted(pathlib.Path(package_dir).glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        lines = path.read_text(encoding="utf-8").splitlines()
        for index, line in enumerate(lines):
            if declaration.match(line):
                end = len(lines)
                for later in range(index + 1, len(lines)):
                    if lines[later].startswith("func "):
                        end = later
                        break
                return path, index, end
    return None, 0, 0


def mutate(lines, start, end):
    """Yield (mutated_lines, description) for one change at a time."""
    for pattern, replacement, description in MUTATIONS:
        compiled = re.compile(pattern)
        for number in range(start, end):
            line = lines[number]
            if line.lstrip().startswith("//"):
                continue
            for match in list(compiled.finditer(line)):
                changed = list(lines)
                changed[number] = line[:match.start()] + replacement + line[match.end():]
                yield changed, f"{description} at line {number + 1}"


def run_test(package, test=None):
    """True when the run fails — which is what a mutation should cause."""
    command = ["go", "test", package, "-count=1"]
    if test:
        command += ["-run", f"^{test}$"]
    result = subprocess.run(command, capture_output=True, text=True)
    if "build failed" in result.stdout + result.stderr or "cannot use" in result.stderr:
        return None  # not a valid mutation
    return result.returncode != 0


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--package", default="./internal/app")
    parser.add_argument("--test", help="only this guard test")
    parser.add_argument("--limit", type=int, default=6,
                        help="mutations to try per guarded function (default 6)")
    parser.add_argument("--list", action="store_true")
    parser.add_argument("--budget", type=int, metavar="SECONDS",
                        help="stop after roughly this long and say what was "
                             "not reached, rather than running to the end")
    parser.add_argument("--changed", metavar="BASE", nargs="?", const="HEAD~1",
                        help="only guards whose subject the diff against BASE "
                             "touched, plus guard tests the diff itself changed "
                             "(default base HEAD~1)")
    options = parser.parse_args()

    package_dir = options.package.lstrip("./") or "."
    guards = collect_guards(package_dir)
    if options.test:
        guards = [g for g in guards if g["test"] == options.test]
    if options.changed:
        base = resolve_base(options.changed)
        subjects, tests = functions_touched(package_dir, base)
        guards = [g for g in guards
                  if g["test"] in tests or any(s in subjects for s in g["subjects"])]
        print(f"{base} 이후 바뀐 함수 {len(subjects)}개, 가드 시험 "
              f"{len(tests)}개 → 점검할 가드 {len(guards)}개")
        if not guards:
            print("바뀐 코드를 이름으로 지목하는 가드가 없습니다.")
            return 0
    if not guards:
        print("no guard markers matched")
        return 0
    if options.list:
        for guard in guards:
            print(f"{guard['test']:<60} {', '.join(guard['subjects'])}")
        return 0

    survivors, tried, skipped = [], 0, 0
    started = time.monotonic()
    unreached = []
    with tempfile.TemporaryDirectory() as workspace:
        for index, guard in enumerate(guards):
            if options.budget and time.monotonic() - started > options.budget:
                unreached = [g["test"] for g in guards[index:]]
                break
            for subject in guard["subjects"]:
                path, start, end = find_function(package_dir, subject)
                if path is None:
                    print(f"  ?  {guard['test']} — {subject} is not a function in {options.package}")
                    continue
                original = path.read_text(encoding="utf-8")
                backup = os.path.join(workspace, path.name)
                shutil.copy(path, backup)
                _IN_FLIGHT[str(path)] = original
                caught = 0
                try:
                    lines = original.splitlines()
                    for changed, description in mutate(lines, start, end):
                        if caught + len([s for s in survivors if s[0] == guard["test"] and s[1] == subject]) >= options.limit:
                            break
                        path.write_text("\n".join(changed) + "\n", encoding="utf-8")
                        tried += 1
                        outcome = run_test(options.package, guard["test"])
                        if outcome is None:
                            tried -= 1
                            skipped += 1
                            continue
                        if outcome:
                            caught += 1
                            continue
                        # The guard did not notice. Whether that is a hole in the
                        # product's tests or only a mispointed marker depends on
                        # whether anything else notices, so ask before reporting.
                        elsewhere = run_test(options.package)
                        survivors.append((guard["test"], subject, description,
                                          "caught elsewhere" if elsewhere else "nothing caught it"))
                finally:
                    shutil.copy(backup, path)
                    _IN_FLIGHT.pop(str(path), None)
                mark = "." if not [s for s in survivors if s[0] == guard["test"] and s[1] == subject] else "!"
                print(f"  {mark}  {guard['test']} — {subject}: caught {caught}, survived "
                      f"{len([s for s in survivors if s[0] == guard['test'] and s[1] == subject])}")

    print(f"\n{tried} mutation(s) applied, {skipped} skipped as uncompilable.")
    if unreached:
        # Saying nothing here would read as "all of these are guarded", which is
        # the one thing this tool must never imply.
        print(f"예산 {options.budget}초를 넘겨 가드 {len(unreached)}개는 확인하지 못했습니다: "
              + ", ".join(unreached[:6]) + ("…" if len(unreached) > 6 else ""))
    if not survivors:
        print("확인한 범위에서는 모든 변이가 잡혔습니다." if unreached else "Every mutation was caught.")
        return 0
    unguarded = [s for s in survivors if s[3] == "nothing caught it"]
    mispointed = [s for s in survivors if s[3] != "nothing caught it"]
    if mispointed:
        print(f"{len(mispointed)} mutation(s) the named guard missed but another test caught —\n"
              "the behaviour is covered; the marker points at the wrong test:\n")
        for test, subject, description, _ in mispointed:
            print(f"  {test}\n    {subject}: {description}")
        print()
    if unguarded:
        print(f"{len(unguarded)} mutation(s) NO test caught:\n")
        for test, subject, description, _ in unguarded:
            print(f"  {test}\n    {subject}: {description}")
    print("\nA surviving mutation is a question, not a verdict: either nothing checks\n"
          "that behaviour, or the change did not alter it.")
    return 1 if unguarded else 0


if __name__ == "__main__":
    sys.exit(main())
