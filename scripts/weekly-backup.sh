#!/bin/sh
# Back up, verify and restore a Weekly deployment.
#
# Weekly keeps its data in two places: PostgreSQL holds every row, and the state
# volume holds the attachment images and, in the legacy key mode, instance.key.
# Restoring one without the other leaves rows pointing at files that are gone,
# which shows up much later as a 404 on a capture that the list screen still
# offers. The whole point of this script is to make the pair travel together.
#
# Order matters, and it is not arbitrary. The database is dumped first and the
# files are copied second, because an upload writes the file before it inserts
# the row: anything the dump can see was already on disk when the dump ran, so
# copying afterwards can only add files, never miss one. The reverse order would
# capture rows whose files had not been written yet. The one window this leaves
# is a delete during the backup, which removes the row first and the file after;
# `verify` reports that as a count mismatch rather than hiding it. Quiesce the
# service if a byte-exact point in time is required.
set -eu

usage() {
  cat >&2 <<'USAGE'
usage:
  weekly-backup.sh backup  -o OUT_DIR  [-d DSN] [-s STATE_DIR]
  weekly-backup.sh verify  -i ARCHIVE_DIR
  weekly-backup.sh restore -i ARCHIVE_DIR [-d DSN] [-s STATE_DIR] [--force]

  -d DSN        PostgreSQL connection URI. Default: $WEEKLY_POSTGRES_DSN
  -s STATE_DIR  Weekly state volume. Default: $WEEKLY_STATE_DIR or /var/lib/weekly
  -o OUT_DIR    Directory to create the backup under
  -i ARCHIVE_DIR  A directory previously produced by `backup`
  --force       Skip the typed confirmation on restore. Restore is destructive.

Run this where PostgreSQL is reachable and the state volume is readable. With
Compose, that is typically:
  docker run --rm -v weekly-data:/var/lib/weekly -v "$PWD/backups:/backups" \
    --network <weekly-network> -e WEEKLY_POSTGRES_DSN=... postgres:16-alpine \
    sh /backups/weekly-backup.sh backup -o /backups
USAGE
  exit 2
}

command=${1:-}
[ -n "$command" ] || usage
shift || true

dsn=${WEEKLY_POSTGRES_DSN:-}
state=${WEEKLY_STATE_DIR:-/var/lib/weekly}
out=""
archive=""
force=no

while [ "$#" -gt 0 ]; do
  case "$1" in
    -d) dsn=${2:?-d needs a DSN}; shift 2 ;;
    -s) state=${2:?-s needs a path}; shift 2 ;;
    -o) out=${2:?-o needs a path}; shift 2 ;;
    -i) archive=${2:?-i needs a path}; shift 2 ;;
    --force) force=yes; shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "required tool not found: $1" >&2; exit 3; }
}

# redact keeps the password out of the manifest and the terminal. The manifest
# is stored next to the dump and is read by whoever is doing the restore, which
# is exactly the audience that should not be handed a credential.
redact() {
  printf '%s\n' "$1" | sed -e 's#://[^@/]*@#://***@#'
}

sha_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

# expected_files counts distinct stored paths, not rows: a duplicate upload is
# deduplicated to one file backing two rows, so rows would overstate the file
# count and make every healthy backup look broken.
query_counts() {
  psql "$dsn" -tAF' ' -c "SELECT
    (SELECT count(*) FROM report_attachments),
    (SELECT count(DISTINCT stored_path) FROM report_attachments),
    (SELECT coalesce(max(version),0) FROM schema_migrations)"
}

count_archived_files() {
  tar tzf "$1" | grep -c '^\(\./\)\?attachments/[^/]\+/[^/]\+$' || true
}

do_backup() {
  [ -n "$dsn" ] || { echo "no DSN: set WEEKLY_POSTGRES_DSN or pass -d" >&2; exit 2; }
  [ -n "$out" ] || usage
  [ -d "$state" ] || { echo "state directory not found: $state" >&2; exit 3; }
  need pg_dump; need psql; need tar

  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  target="$out/weekly-backup-$stamp"
  mkdir -p "$target"

  echo "1/4 dumping PostgreSQL"
  pg_dump --format=custom --no-owner --no-privileges --file="$target/database.dump" "$dsn"

  echo "2/4 reading expected attachment counts"
  counts=$(query_counts)
  rows=$(echo "$counts" | cut -d' ' -f1)
  files_expected=$(echo "$counts" | cut -d' ' -f2)
  migration=$(echo "$counts" | cut -d' ' -f3)

  echo "3/4 archiving state volume"
  tar czf "$target/state.tar.gz" -C "$state" .

  files_archived=$(count_archived_files "$target/state.tar.gz")

  cat > "$target/manifest.txt" <<MANIFEST
created_at=$stamp
dsn=$(redact "$dsn")
state_dir=$state
schema_migration=$migration
attachment_rows=$rows
attachment_files_expected=$files_expected
attachment_files_archived=$files_archived
MANIFEST

  echo "4/4 writing checksums"
  (cd "$target" && sha_of database.dump state.tar.gz manifest.txt > SHA256SUMS)

  echo "backup written to $target"
  # Exits non-zero on a gap so a scheduled backup fails loudly instead of
  # recording a broken restore point as a success.
  status=0
  report_attachment_gap "$files_expected" "$files_archived" || status=$?
  return $status
}

report_attachment_gap() {
  expected=$1
  archived=$2
  if [ "$expected" -eq "$archived" ]; then
    echo "attachments: $archived files for $expected referenced paths — complete"
    return 0
  fi
  if [ "$archived" -lt "$expected" ]; then
    echo "WARNING: $((expected - archived)) referenced attachment files are missing." >&2
    echo "  Rows restored from this backup will 404 on those captures. Check that" >&2
    echo "  the state volume is the one the service actually writes to, and that" >&2
    echo "  earlier upgrades kept it mounted." >&2
    return 1
  fi
  echo "note: $((archived - expected)) archived files are no longer referenced; harmless."
  return 0
}

do_verify() {
  [ -n "$archive" ] || usage
  [ -f "$archive/manifest.txt" ] || { echo "not a Weekly backup: $archive" >&2; exit 3; }
  need tar

  echo "1/3 checksums"
  (cd "$archive" && sha_of -c SHA256SUMS)

  echo "2/3 archive contents"
  if command -v pg_restore >/dev/null 2>&1; then
    pg_restore --list "$archive/database.dump" >/dev/null
    echo "  database.dump readable"
  else
    echo "  pg_restore not available; skipped dump inspection"
  fi
  tar tzf "$archive/state.tar.gz" >/dev/null
  echo "  state.tar.gz readable"

  echo "3/3 attachment completeness"
  expected=$(sed -n 's/^attachment_files_expected=//p' "$archive/manifest.txt")
  archived=$(count_archived_files "$archive/state.tar.gz")
  status=0
  report_attachment_gap "${expected:-0}" "$archived" || status=$?
  return $status
}

do_restore() {
  [ -n "$dsn" ] || { echo "no DSN: set WEEKLY_POSTGRES_DSN or pass -d" >&2; exit 2; }
  [ -n "$archive" ] || usage
  [ -f "$archive/manifest.txt" ] || { echo "not a Weekly backup: $archive" >&2; exit 3; }
  need pg_restore; need tar

  database=$(printf '%s\n' "$dsn" | sed -e 's#[?].*##' -e 's#.*/##')
  if [ "$force" != yes ]; then
    echo "This DROPS and rewrites every table in '$database' and replaces $state."
    printf "Type the database name to continue: "
    read -r typed
    [ "$typed" = "$database" ] || { echo "aborted" >&2; exit 4; }
  fi

  echo "1/3 restoring PostgreSQL"
  pg_restore --clean --if-exists --no-owner --no-privileges --dbname="$dsn" "$archive/database.dump"

  echo "2/3 replacing state volume"
  mkdir -p "$state"
  # Emptied rather than merged: leaving stale files behind would let a file the
  # backup does not contain masquerade as restored data.
  find "$state" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
  tar xzf "$archive/state.tar.gz" -C "$state"

  echo "3/3 done. Check, in this order:"
  echo "  - /readyz returns ok and the boot log has no 'attachment files are missing'"
  echo "  - local admin login, and admin secret settings show 안전하게 설정됨"
  echo "  - one report with a capture opens and exports to PPTX"
  echo "  If secrets read as 복호화할 수 없음, WEEKLY_ENCRYPTION_KEY does not match"
  echo "  the one in use when this backup was taken."
}

case "$command" in
  backup) do_backup ;;
  verify) do_verify ;;
  restore) do_restore ;;
  *) usage ;;
esac
