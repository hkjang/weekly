#!/bin/sh
# Every version tag has a release with an asset behind it.
#
# v0.150.0 was tagged and pushed, and its release never appeared: GitHub held
# the run in a queue for over an hour while the tag it was built from sat there
# looking finished. Nothing noticed. It was found by hand, two releases later,
# while checking something else.
#
# A tag with no release is a version somebody can be told to install and cannot.
# This is the cheap check that says so.
#
# Tags created in the last hour are skipped: the release workflow runs beside
# CI, not before it, so the newest tag is legitimately still building.
#
# It also asks whether CI was green for the recent tags. That question went
# unasked for a day: the release workflow and CI are two workflows, the release
# one kept succeeding, and the asset kept appearing — so every iteration looked
# finished while CI had been failing for nineteen commits straight. A release
# whose tests did not pass is a release nobody checked.
#
# Run: scripts/release-check.sh
set -eu

command -v gh >/dev/null 2>&1 || { echo "gh 명령이 필요합니다."; exit 2; }

grace=$(( $(date +%s) - 3600 ))
missing=0
checked=0
young=0

releases=$(gh release list --limit 500 --json tagName -q '.[].tagName' 2>/dev/null | sort)

for tag in $(git tag -l 'v*' | sort); do
  stamp=$(git log -1 --format=%ct "$tag" 2>/dev/null || echo 0)
  if [ "$stamp" -gt "$grace" ]; then
    young=$(( young + 1 ))
    continue
  fi
  checked=$(( checked + 1 ))
  if ! printf '%s\n' "$releases" | grep -qx "$tag"; then
    printf '  %s — 태그는 있는데 릴리스가 없습니다\n' "$tag"
    missing=$(( missing + 1 ))
  fi
done

if [ "$missing" -gt 0 ]; then
  printf '릴리스 검사: %d개 태그 중 %d개에 릴리스가 없습니다.\n' "$checked" "$missing"
  printf '  다시 만들려면: gh workflow run "Offline Docker Release" --ref main -f tag=<태그>\n'
  exit 1
fi

# Whether CI passed on the newest tag it has finished running for.
#
# Only the newest: a check that stays red for a dozen releases while the backlog
# rolls out of its window is a check people learn to walk past, which is the
# failure this one exists to prevent. Older reds are counted and printed, not
# failed on — the history is visible, the gate is about now.
runs=$(gh run list --workflow CI --limit 120 --json headSha,conclusion \
  -q '.[] | "\(.headSha) \(.conclusion)"' 2>/dev/null || true)
newest=""
newest_verdict=""
older_red=0
for tag in $(git tag -l 'v*' | sort -V | tail -15); do
  sha=$(git rev-list -n1 "$tag")
  verdict=$(printf '%s\n' "$runs" | grep "^$sha " | head -1 | cut -d' ' -f2)
  [ -z "$verdict" ] && continue
  [ "$verdict" = "null" ] && continue
  if [ -n "$newest" ]; then
    case "$newest_verdict" in
      failure|cancelled|timed_out) older_red=$(( older_red + 1 )) ;;
    esac
  fi
  newest="$tag"
  newest_verdict="$verdict"
done

if [ "$older_red" -gt 0 ]; then
  printf '  앞선 태그 %d개가 CI 실패 위에서 나갔습니다 (지난 일이라 여기서 막지는 않습니다).\n' "$older_red"
fi
case "$newest_verdict" in
  failure|cancelled|timed_out)
    printf '릴리스 검사: 가장 최근 태그 %s 의 CI 가 %s 입니다.\n' "$newest" "$newest_verdict"
    printf '  시험이 통과하지 않은 릴리스는 아무도 확인하지 않은 릴리스입니다.\n'
    printf '  무엇이 깨졌는지: gh run list --workflow CI --limit 5\n'
    exit 1
    ;;
esac

printf '릴리스 검사: 통과 — 태그 %d개가 모두 릴리스를 가지고 있습니다' "$checked"
if [ "$young" -gt 0 ]; then
  printf ' (최근 1시간 내 태그 %d개는 아직 만들어지는 중일 수 있어 건너뜀)' "$young"
fi
printf '.\n'
