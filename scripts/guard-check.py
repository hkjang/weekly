#!/usr/bin/env python3
"""Check that every guard test executes the code it claims to guard.

Three times in a row a test was written to catch a specific defect, passed, and
could not have failed:

  * v0.64 — a reachability guard matched loosely enough that an unrelated CSS
    class satisfied it. Deleting the only caller still passed.
  * v0.68 — every case in a message test hit an earlier branch, so the fallback
    the test existed for was never executed.
  * v0.72 — a ranking test ran SQL it had written itself instead of calling the
    product's function. Deleting that function entirely still passed.

The last two share one mechanical signature: the test never ran the code it was
guarding. That is what per-test coverage measures, so it does not have to be
caught by remembering to check.

A guard test declares its subject in a comment directly above it:

    // guards: searchScan, appendSearchMatches
    func TestATitleMatchOutranksAFloodOfRecentBodies(t *testing.T) {

This runs each such test alone under coverage and reports any named function the
test never reached.

Reaching a function is not the same as reaching the part of it that matters —
the v0.68 test above ran its subject and still missed the branch it existed for.
A guard can demand more by naming a percentage:

    // guards: safeConfluenceError=100

which fails unless the test covers every branch of that function. Use it where
the guard's whole claim is "each case is handled differently", because there the
uncovered branch is the case nobody checked.

A subject that is not a function — a constant like `mcpRollupContributors`, or
the embedded `migrations` ledger — has no coverage reading and never will. For
those the check is that the test mentions the name it claims to guard, which is
what catches a marker left behind after its test moved on.

It does not check that the assertions are strong. Reverting a guard to confirm
it fails is still the real check; this catches the case where there was nothing
to revert.

Usage:
    python3 scripts/guard-check.py [--package ./internal/app] [--list]

Exit code is 1 when a guard names a function it does not execute, so it can gate
a release. Skipped tests are reported and do not pass silently.
"""
import argparse
import os
import pathlib
import re
import subprocess
import sys
import tempfile

GUARD_MARKER = re.compile(r"^\s*//\s*guards:\s*(.+?)\s*$")
TEST_FUNC = re.compile(r"^func (Test\w+)\(")
COVER_LINE = re.compile(r"^(\S+):(\d+):\s+(\S+)\s+([\d.]+)%$")
FUNC_DECL = re.compile(r"^func\s+(?:\([^)]*\)\s*)?(\w+)\s*[(\[]", re.M)
DECL_OPEN = re.compile(r"^(?:const|var)\s*\($")
DECL_ONE = re.compile(r"^(?:const|var)\s+(\w+)")
DECL_IN_BLOCK = re.compile(r"^\s+(\w+)")
TEST_BODY = re.compile(r"^func (Test\w+)\(.*?^}", re.M | re.S)


def describe(subjects):
    return [name if required <= 0 else f"{name}={required:.0f}%" for name, required in subjects]


def package_names(package_dir):
    """What the package declares, split into what coverage can see and what it cannot.

    Coverage reports functions. A guard whose subject is a constant or a ledger
    variable — `mcpRollupContributors`, the embedded `migrations` — can never
    show a percentage, so the "never executed" rule condemned it no matter what
    the test did. Three such guards sat red in CI for a day while the releases
    went out anyway, which is worse than not checking: a check that is always
    red stops being read.

    Those subjects still have a checkable claim, just a different one: the test
    has to actually mention the thing it says it guards. That is what catches
    the marker left behind after the test moved on.
    """
    functions, data = set(), set()
    for path in sorted(pathlib.Path(package_dir).glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        source = path.read_text(encoding="utf-8")
        functions.update(FUNC_DECL.findall(source))
        # Package-level const/var only. An assignment inside a function is not a
        # declaration, and reading one as a subject would let any local name
        # pass as a guarded thing.
        in_block = False
        for line in source.splitlines():
            if in_block:
                if line.startswith(")"):
                    in_block = False
                elif not line.lstrip().startswith("//"):
                    named = DECL_IN_BLOCK.match(line)
                    if named:
                        data.add(named.group(1))
                continue
            if DECL_OPEN.match(line):
                in_block = True
                continue
            named = DECL_ONE.match(line)
            if named:
                data.add(named.group(1))
    return functions, data - functions


def test_bodies(package_dir):
    """Each test function's own source, for guards coverage cannot speak about."""
    bodies = {}
    for path in sorted(pathlib.Path(package_dir).glob("*_test.go")):
        for match in TEST_BODY.finditer(path.read_text(encoding="utf-8")):
            bodies[match.group(1)] = match.group(0)
    return bodies


def collect_guards(package_dir):
    """Pair each `// guards:` marker with the test function it precedes."""
    guards = []
    for path in sorted(pathlib.Path(package_dir).glob("*_test.go")):
        pending = None
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            marker = GUARD_MARKER.match(line)
            if marker:
                subjects = []
                for entry in marker.group(1).split(","):
                    entry = entry.strip()
                    if not entry:
                        continue
                    name, _, threshold = entry.partition("=")
                    subjects.append((name.strip(), float(threshold) if threshold.strip() else 0.0))
                pending = (subjects, number)
                continue
            test = TEST_FUNC.match(line)
            if test:
                if pending:
                    guards.append({"test": test.group(1), "subjects": pending[0],
                                   "file": str(path), "line": pending[1]})
                pending = None
                continue
            # The marker has to sit in the comment block immediately above the
            # test, or it is describing something else.
            if pending and not line.lstrip().startswith("//"):
                pending = None
    return guards


def resolve_base(reference):
    """The commit to diff against, or None when there is nothing to compare."""
    candidates = [reference] if reference else ["HEAD~1", "HEAD"]
    for candidate in candidates:
        result = subprocess.run(["git", "rev-parse", "--verify", "--quiet", candidate + "^{commit}"],
                                capture_output=True, text=True)
        if result.returncode == 0:
            return result.stdout.strip()
    return None


def changed_text(base):
    """Everything the working tree adds or changes against base, as one string.

    Untracked files count: a guard added in a new file is exactly the case this
    is for, and it is the case that put a wrong marker into a release.
    """
    pieces = []
    diff = subprocess.run(["git", "diff", "--unified=0", base, "--"], capture_output=True, text=True)
    pieces.append(diff.stdout)
    untracked = subprocess.run(["git", "ls-files", "--others", "--exclude-standard"],
                               capture_output=True, text=True)
    for name in untracked.stdout.split():
        try:
            pieces.append(pathlib.Path(name).read_text(encoding="utf-8"))
        except OSError:
            continue
    return "\n".join(pieces)


def touched_guards(guards, base):
    """Guards whose test or named subject appears in what changed.

    A marker can only become a lie two ways: the test moves away from what it
    names, or the subject does. Both show up in the diff by name.
    """
    text = changed_text(base)
    if not text.strip():
        return []
    picked = []
    for guard in guards:
        names = [guard["test"]] + [name for name, _ in guard["subjects"]]
        if any(name in text for name in names):
            picked.append(guard)
    return picked

def covered_functions(package, test_name, profile):
    """Run one test under coverage; return {function: percent} and whether it ran."""
    result = subprocess.run(
        ["go", "test", package, "-run", f"^{test_name}$", "-count=1", "-coverprofile", profile, "-v"],
        capture_output=True, text=True)
    if result.returncode != 0:
        return None, result.stdout + result.stderr, "failed"
    if f"--- SKIP: {test_name}" in result.stdout:
        return None, result.stdout, "skipped"
    if f"--- PASS: {test_name}" not in result.stdout:
        return None, result.stdout, "did not run"
    report = subprocess.run(["go", "tool", "cover", "-func", profile], capture_output=True, text=True)
    percentages = {}
    for line in report.stdout.splitlines():
        match = COVER_LINE.match(line.replace("\t", " ").strip())
        if match:
            # A name can appear once per file; keep the highest reading so a
            # method and a same-named helper elsewhere do not mask each other.
            name, percent = match.group(3), float(match.group(4))
            percentages[name] = max(percentages.get(name, 0.0), percent)
    return percentages, "", "ok"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--package", default="./internal/app")
    parser.add_argument("--list", action="store_true", help="print the guards and exit")
    parser.add_argument("--test", help="check only this guard test")
    # Whole-run cost is one `go test` per guard, which is minutes. That is the
    # right price for CI and the wrong one for the moment somebody adds a
    # marker — so it went unrun there, and a marker naming two functions the
    # test never calls reached a release. This makes the check affordable
    # exactly when it is needed.
    parser.add_argument("--changed", nargs="?", const="", metavar="BASE",
                        help="only guards whose test or subject the diff against BASE touched "
                             "(default base HEAD~1; untracked files count)")
    options = parser.parse_args()

    package_dir = options.package.lstrip("./") or "."
    guards = collect_guards(package_dir)
    if not guards:
        print("no guard markers found; add `// guards: <function>` above a test")
        return 0
    if options.list:
        for guard in guards:
            print(f"{guard['test']:<60} guards {', '.join(describe(guard['subjects']))}")
        return 0

    if options.test:
        guards = [g for g in guards if g["test"] == options.test]
        if not guards:
            print(f"no guard named {options.test}")
            return 1
    elif options.changed is not None:
        base = resolve_base(options.changed)
        if base is None:
            print("nothing to diff against; checking every guard")
        else:
            guards = touched_guards(guards, base)
            if not guards:
                print("no guard's test or subject appears in the diff — nothing to check")
                return 0

    function_names, data_names = package_names(package_dir)
    bodies = test_bodies(package_dir)

    print(f"checking {len(guards)} guard test(s) in {options.package}\n")
    findings, skipped = [], []
    with tempfile.TemporaryDirectory() as workspace:
        for guard in guards:
            profile = os.path.join(workspace, guard["test"] + ".out")
            percentages, output, state = covered_functions(options.package, guard["test"], profile)
            if state == "skipped":
                skipped.append(guard["test"])
                print(f"  ?  {guard['test']} — skipped, not verified")
                continue
            if state != "ok":
                findings.append((guard, f"the test {state}", output.strip()[-400:]))
                print(f"  !  {guard['test']} — {state}")
                continue
            missed, notes = [], []
            for name, required in guard["subjects"]:
                reached = percentages.get(name, 0.0)
                if name not in function_names and name not in data_names:
                    # The marker names something the package does not declare.
                    # A subject that does not exist cannot be guarded, and the
                    # old message called that "never executed", which reads
                    # like a weak test rather than a wrong name.
                    missed.append(f"{name} 라는 것을 이 꾸러미가 선언하지 않습니다")
                    continue
                if name in data_names and name not in function_names:
                    # Not a function: coverage will never have a reading for it.
                    # The claim it can still be held to is that the test names it.
                    if required > 0.0:
                        missed.append(f"{name} is not a function; a percentage cannot be measured for it")
                    elif name not in bodies.get(guard["test"], ""):
                        missed.append(f"never mentions {name}")
                    else:
                        notes.append(f"{name} (상수/원장) 참조")
                    continue
                if required <= 0.0 and reached <= 0.0:
                    missed.append(f"never executed {name}")
                elif required > 0.0 and reached < required:
                    missed.append(f"{name} covered {reached:.0f}%, below the {required:.0f}% it claims")
            if missed:
                findings.append((guard, "; ".join(missed), ""))
                print(f"  !  {guard['test']} — {'; '.join(missed)}")
            else:
                summary = ", ".join(
                    f"{name} {percentages.get(name, 0.0):.0f}%"
                    for name, _ in guard["subjects"] if name not in data_names or name in function_names)
                summary = ", ".join(part for part in [summary, ", ".join(notes)] if part)
                print(f"  .  {guard['test']} — {summary}")

    print()
    if skipped:
        print(f"{len(skipped)} guard(s) skipped and therefore unverified: {', '.join(skipped)}")
    if not findings:
        # The last line is the one people read. Saying "reached what they name"
        # and stopping there reads as a clean run even when most of the suite
        # never ran: a database-backed guard skips without WEEKLY_TEST_POSTGRES_DSN,
        # and shell exports do not survive between commands. A mispointed marker
        # shipped exactly that way — checked locally, caught in CI.
        checked = len(guards) - len(skipped)
        if skipped:
            print(f"{checked} guard(s) reached what they name — "
                  f"{len(skipped)} were NOT checked. Set WEEKLY_TEST_POSTGRES_DSN to check them all.")
        else:
            print(f"{checked} guard(s) reached what they name.")
        return 0
    print(f"{len(findings)} guard(s) cannot fail:\n")
    for guard, reason, detail in findings:
        print(f"  {guard['file']}:{guard['line']}  {guard['test']}")
        print(f"    {reason}")
        if detail:
            print(f"    {detail}")
    print("\nA test that never runs its subject passes whatever the subject does.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
