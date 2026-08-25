#!/bin/sh
# Prove that weekly-backup.sh can actually bring a deployment back.
#
# Every release note since v0.15 tells operators to run `backup` and `verify`
# before upgrading. Nothing ever ran them. A recovery script is the one piece of
# software whose first real execution happens on the worst day of the year, so
# this exercises the whole round trip — dump, archive, verify, restore — and
# compares what came back against what went in.
#
# It also checks the failure it exists to report. A backup that is missing
# attachment files must exit non-zero: a broken restore point recorded as a
# success is worse than no backup at all, because it is trusted.
#
# Run: scripts/backup-check.sh [DSN]
#   DSN defaults to $WEEKLY_TEST_POSTGRES_DSN. The database in it is not
#   touched; two scratch databases are created beside it.
set -eu

dsn=${1:-${WEEKLY_TEST_POSTGRES_DSN:-}}
[ -n "$dsn" ] || { echo "no DSN: pass one or set WEEKLY_TEST_POSTGRES_DSN" >&2; exit 2; }

for tool in psql pg_dump pg_restore tar; do
  command -v "$tool" >/dev/null 2>&1 || { echo "required tool not found: $tool" >&2; exit 3; }
done

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Swap the database name, keeping host, credentials and query string intact.
with_database() {
  printf '%s\n' "$dsn" | sed -e "s#/[^/?]*\(?\|$\)#/$1\1#"
}
admin=$(with_database postgres)
src=$(with_database weekly_backup_src)
dst=$(with_database weekly_backup_dst)

say() { printf '%s\n' "$*"; }
fail() { printf 'backup-check: %s\n' "$*" >&2; exit 1; }

say "1/7 scratch databases"
drop_scratch() {
  psql "$admin" -q -o /dev/null -c 'SET client_min_messages TO warning' \
    -c 'DROP DATABASE IF EXISTS weekly_backup_src' -c 'DROP DATABASE IF EXISTS weekly_backup_dst'
}
drop_scratch
psql "$admin" -q -o /dev/null -c 'CREATE DATABASE weekly_backup_src' -c 'CREATE DATABASE weekly_backup_dst'

say "2/7 migrations"
for file in "$root"/internal/app/migrations/*.sql; do
  psql "$src" -q -o /dev/null -v ON_ERROR_STOP=1 -f "$file"
done

say "3/7 seeding a report with attachments"
# Two rows share one stored path, which is what a duplicate upload produces.
# The counting in weekly-backup.sh is built around that difference, so a check
# that only ever sees distinct files would not exercise it.
one=1111111111111111111111111111111111111111111111111111111111111111
two=2222222222222222222222222222222222222222222222222222222222222222
psql "$src" -q -o /dev/null -v ON_ERROR_STOP=1 <<SQL
INSERT INTO organizations(name, code) VALUES ('복구 시험 조직', 'DRTEST');
INSERT INTO users(username, display_name) VALUES ('drcheck', '복구 시험');
INSERT INTO weekly_reports(user_id, week_start)
  SELECT id, DATE '2026-08-24' FROM users WHERE username = 'drcheck';
INSERT INTO report_attachments(report_id, user_id, original_filename, stored_path, content_type, extension, size_bytes, width, height, sha256)
  SELECT r.id, r.user_id, f.name, '1/' || f.digest || '.png', 'image/png', 'png', 12, 4, 4, f.digest
  FROM weekly_reports r, (VALUES ('a.png', '$one'), ('b.png', '$one'), ('c.png', '$two')) AS f(name, digest);
SQL

state="$work/state"
mkdir -p "$state/attachments/1"
printf 'first file\n'  > "$state/attachments/1/$one.png"
printf 'second file\n' > "$state/attachments/1/$two.png"
printf 'not a real key\n' > "$state/instance.key"

say "4/7 backup"
mkdir -p "$work/out"
sh "$root/scripts/weekly-backup.sh" backup -o "$work/out" -d "$src" -s "$state" > "$work/backup.log" 2>&1 \
  || { cat "$work/backup.log"; fail "backup exited non-zero on a complete deployment"; }
grep -q 'complete' "$work/backup.log" || { cat "$work/backup.log"; fail "backup did not report the attachments as complete"; }
archive=$(find "$work/out" -maxdepth 1 -type d -name 'weekly-backup-*' | head -1)
[ -n "$archive" ] || fail "backup produced no archive directory"

grep -q '^attachment_rows=3$' "$archive/manifest.txt" || fail "manifest lost the attachment row count"
grep -q '^attachment_files_expected=2$' "$archive/manifest.txt" || fail "manifest miscounted distinct stored paths"
grep -q '@' "$archive/manifest.txt" && grep -q 'dsn=.*\*\*\*' "$archive/manifest.txt" \
  || fail "manifest did not redact the password"

say "5/7 verify"
sh "$root/scripts/weekly-backup.sh" verify -i "$archive" > "$work/verify.log" 2>&1 \
  || { cat "$work/verify.log"; fail "verify rejected an archive it had just written"; }

say "6/7 restore into an empty deployment"
restored="$work/restored"
sh "$root/scripts/weekly-backup.sh" restore -i "$archive" -d "$dst" -s "$restored" --force > "$work/restore.log" 2>&1 \
  || { cat "$work/restore.log"; fail "restore exited non-zero" ; }

rows=$(psql "$dst" -tAc 'SELECT count(*) FROM report_attachments')
[ "$rows" = 3 ] && say "  attachment rows: $rows" || fail "restored $rows attachment rows, expected 3"
reports=$(psql "$dst" -tAc 'SELECT count(*) FROM weekly_reports')
[ "$reports" = 1 ] || fail "restored $reports reports, expected 1"

for digest in "$one" "$two"; do
  original="$state/attachments/1/$digest.png"
  came_back="$restored/attachments/1/$digest.png"
  [ -f "$came_back" ] || fail "attachment file $digest.png did not come back"
  cmp -s "$original" "$came_back" || fail "attachment file $digest.png came back with different bytes"
done
[ -f "$restored/instance.key" ] || fail "instance.key did not come back"
say "  attachment files: identical to the originals"

# Nothing the backup did not contain may be left behind pretending to be data.
stale="$restored/attachments/1/stale.png"
mkdir -p "$restored/attachments/1"; printf 'stale\n' > "$stale"
sh "$root/scripts/weekly-backup.sh" restore -i "$archive" -d "$dst" -s "$restored" --force > /dev/null 2>&1 \
  || fail "second restore exited non-zero"
[ -f "$stale" ] && fail "restore left a stale file behind" || say "  stale files: cleared"

say "7/7 a backup with a missing file must fail loudly"
rm -f "$state/attachments/1/$two.png"
if sh "$root/scripts/weekly-backup.sh" backup -o "$work/out" -d "$src" -s "$state" > "$work/gap.log" 2>&1; then
  cat "$work/gap.log"
  fail "backup reported success while an attachment file was missing"
fi
grep -q 'WARNING' "$work/gap.log" || { cat "$work/gap.log"; fail "backup failed without saying why"; }
say "  reported: $(grep WARNING "$work/gap.log" | head -1)"

drop_scratch
say "복구 검사: 통과 — 백업·검증·복구가 왕복하고, 빠진 첨부는 실패로 보고합니다."
