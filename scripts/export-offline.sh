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
printf "image id: "
docker image inspect "$image" --format '{{.Id}}'

