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
