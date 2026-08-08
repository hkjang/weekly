#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
version=$(cat "$project_dir/VERSION")
commit=$(git -C "$project_dir" rev-parse --short HEAD 2>/dev/null || echo unknown)
build_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)

cd "$project_dir/frontend"
npm ci
npm run build

rm -rf "$project_dir/cmd/weekly/web"
mkdir -p "$project_dir/cmd/weekly/web" "$project_dir/dist"
cp -R "$project_dir/frontend/dist/." "$project_dir/cmd/weekly/web/"
if [ -f "$project_dir/1월5주간업무보고_AI엔지니어링.pptx" ]; then
  cp "$project_dir/1월5주간업무보고_AI엔지니어링.pptx" "$project_dir/cmd/weekly/templates/reference.pptx"
fi

cd "$project_dir"
CGO_ENABLED=0 go build -trimpath \
  -ldflags="-s -w -X main.version=$version -X main.commit=$commit -X main.buildTime=$build_time" \
  -o "$project_dir/dist/weekly" ./cmd/weekly

echo "Weekly v$version built: $project_dir/dist/weekly"
