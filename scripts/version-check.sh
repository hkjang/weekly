#!/bin/sh
# Every place that names the version has to name the same one.
#
# A release touches seven files: VERSION, the frontend package, the OpenAPI
# document, three deployment files and the README's docker load line. Miss one
# and the mistake is invisible here and expensive there — an operator follows
# the README and loads an image that is not the one this tree builds, or a
# Compose file pins a tag that was never published.
#
# It also refuses to bless a tree that another tool is holding open.
#
# authz-check.py deletes one authorisation refusal at a time and puts it back;
# for the forty minutes that takes, the checkout does not compile the product it
# claims to. `git add -A` beside it swept a half-removed check into a release,
# and v0.259.0 shipped a handover endpoint that would answer for somebody else's
# organisation. This is the last gate before a version is stamped, so it is
# where that has to stop.
#
# Run: scripts/version-check.sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ -e "$root/.authz-check-running" ]; then
  echo "권한 검사가 이 작업 트리를 고치는 중입니다:"
  sed 's/^/  /' "$root/.authz-check-running"
  echo "  지금 커밋하면 지워진 권한 검사가 그대로 나갑니다. 끝나기를 기다리십시오."
  exit 1
fi

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=$(tr -d ' \n' < "$root/VERSION")
[ -n "$version" ] || { echo "VERSION 이 비어 있습니다" >&2; exit 1; }

problems=0

# Report what is missing rather than what was found. An early version guessed
# the current value by taking the first x.y.z in the file, and README.md answered
# with Confluence's 6.9.1 — a number that has nothing to do with this and sends
# the reader to the wrong line.
expect() {
  file=$1
  pattern=$2
  if [ ! -f "$root/$file" ]; then
    echo "  - $file 이 없습니다" >&2
    problems=$((problems + 1))
    return
  fi
  if ! grep -q "$pattern" "$root/$file"; then
    echo "  - $file 에 '$pattern' 이(가) 없습니다" >&2
    problems=$((problems + 1))
  fi
}

expect frontend/package.json "\"version\": \"$version\""
expect docs/openapi.yaml "version: $version"
expect deploy/.env.example "WEEKLY_VERSION=$version"
expect deploy/compose.yaml "WEEKLY_VERSION:-$version"
expect deploy/kubernetes.yaml "image: weekly:v$version"
expect README.md "weekly-v$version.tar.gz"
# The guides ship inside the release, so their version is the release's. Both
# said v0.7.0-ENTERPRISE while the product was v0.272.0 — 265 releases apart —
# and a reader had no way to tell whether the manual described their deployment.
expect docs/USER_GUIDE.md "문서 버전\*\*: v$version"
expect docs/ADMIN_GUIDE.md "문서 버전\*\*: v$version"

# The notes are what an operator reads before upgrading; a tag without them is
# a release nobody can find out about.
if [ ! -f "$root/.github/release-notes/v$version.md" ]; then
  echo "  - .github/release-notes/v$version.md 이 없습니다" >&2
  problems=$((problems + 1))
fi

if [ "$problems" -gt 0 ]; then
  echo "버전 검사: $problems 곳이 $version 과 어긋납니다." >&2
  exit 1
fi
echo "버전 검사: 통과 — 아홉 곳이 모두 $version 입니다."
