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
printf '릴리스 검사: 통과 — 태그 %d개가 모두 릴리스를 가지고 있습니다' "$checked"
if [ "$young" -gt 0 ]; then
  printf ' (최근 1시간 내 태그 %d개는 아직 만들어지는 중일 수 있어 건너뜀)' "$young"
fi
printf '.\n'
