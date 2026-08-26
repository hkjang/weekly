#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 IMAGE[:TAG]" >&2
  exit 2
fi

image=$1
project_dir=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
image_tag=${image##*:}
case "$image_tag" in
  v*) release_tag=$image_tag ;;
  *) release_tag="v$image_tag" ;;
esac
output_dir="$project_dir/release"
output="$output_dir/weekly-${release_tag}.tar.gz"

mkdir -p "$output_dir"
docker image inspect "$image" >/dev/null
docker save "$image" | gzip -9 -n > "$output"

echo "$output"
sha256sum "$output"

# The archive hash answers "did this file arrive whole". It cannot answer
# "is this the image that was built": an archive unpacked and repacked on
# the way in — ordinary on a network with no route out — comes back with
# different bytes and the same image inside. Docker rewrites its own
# packaging metadata on a load-and-save round trip; the layer and config
# digests do not move. So print the identity that survives the journey.
# Read it out of the archive rather than asking Docker, because Docker does not
# answer the same way on both ends: `docker image inspect --format {{.Id}}` on a
# freshly built image and on the same image after a load give different values.
# The digest written inside the archive does not move.
identity_dir=$(mktemp -d)
gzip -dc "$output" | tar -x -C "$identity_dir" manifest.json 2>/dev/null || true
identity=$(grep -o 'blobs/sha256/[0-9a-f]\{64\}' "$identity_dir/manifest.json" 2>/dev/null | head -1)
rm -rf "$identity_dir"
if [ -n "$identity" ]; then
  printf 'image digest: sha256:%s\n' "${identity##*/}"
fi

