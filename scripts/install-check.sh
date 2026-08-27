#!/bin/sh
# The install a customer actually performs, checked before it ships.
#
# Everything else in scripts/ measures the product. This measures the two files
# a customer copies before the product has ever run: deploy/compose.yaml and
# deploy/.env.example. Following them verbatim once failed at the very first
# command, because compose demanded a variable the example ships empty, the
# README calls recommended, and the binary treats as optional — three artefacts
# disagreeing about the same value, and nothing looked at all three.
set -eu

project_dir=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
version=$(cat "$project_dir/VERSION")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0
note() { printf '  %s\n' "$1"; }
fail() { printf '  실패: %s\n' "$1"; failures=$((failures + 1)); }

cp "$project_dir/deploy/compose.yaml" "$work/compose.yaml"
cp "$project_dir/deploy/.env.example" "$work/.env"

# What the README tells a reader to change, and nothing else. Anything still
# needed after this is something the shipped files failed to say.
sed -i "s|^WEEKLY_POSTGRES_DSN=.*|WEEKLY_POSTGRES_DSN=postgres://weekly:pw@db.internal:5432/weekly?sslmode=require|" "$work/.env"
sed -i "s|^WEEKLY_BOOTSTRAP_ADMIN_PASSWORD=.*|WEEKLY_BOOTSTRAP_ADMIN_PASSWORD=FirstInstall1234|" "$work/.env"

echo "설치 검사"
if rendered=$(cd "$work" && docker compose --env-file .env -f compose.yaml config 2>&1); then
  note "문서대로 채운 .env 로 compose 가 렌더됩니다"
else
  fail "문서대로 채웠는데 compose 가 렌더되지 않습니다:"
  printf '    %s\n' "$(printf '%s' "$rendered" | head -3)"
  rendered=""
fi

# The image the customer would pull has to be the one this tree builds.
if [ -n "$rendered" ]; then
  image=$(printf '%s' "$rendered" | sed -n 's/^ *image: *//p' | head -1)
  if [ "$image" = "weekly:v$version" ]; then
    note "이미지 $image 가 VERSION 과 같습니다"
  else
    fail "compose 가 가리키는 이미지는 $image, VERSION 은 $version"
  fi
fi

# Every WEEKLY_* the binary reads has to be passed by both deployment files, or
# be one the product is documented to run without. A variable added to the code
# and to neither file reaches the customer as a default nobody chose.
optional="WEEKLY_ENCRYPTION_KEY WEEKLY_ALLOW_SECRET_RESET"
# Tests read their own DSN, and no deployment should carry it.
read_names=$(grep -rho --include='*.go' --exclude='*_test.go' 'os.Getenv("WEEKLY_[A-Z_]*"' \
  "$project_dir/internal" "$project_dir/cmd" \
  | sed 's/.*"\(WEEKLY_[A-Z_]*\)".*/\1/' | sort -u)
for name in $read_names; do
  for file in deploy/compose.yaml deploy/kubernetes.yaml; do
    if grep -q "$name" "$project_dir/$file"; then continue; fi
    case " $optional " in
      *" $name "*) note "$name 은 $file 에 없지만 없이도 도는 값입니다" ;;
      *) fail "$name 을 코드가 읽는데 $file 이 전달하지 않습니다" ;;
    esac
  done
done

# The example must not ship a value that only looks filled in.
if grep -q '^WEEKLY_BOOTSTRAP_ADMIN_PASSWORD=CHANGE_ME' "$project_dir/deploy/.env.example"; then
  note ".env.example 의 비밀번호는 바꿔야 한다고 눈에 보입니다"
else
  fail ".env.example 이 그대로 써도 될 것처럼 보이는 비밀번호를 담고 있습니다"
fi

if [ "$failures" -eq 0 ]; then
  echo "설치 검사: 통과 — 받은 파일 그대로 설치가 시작됩니다."
else
  echo "설치 검사: $failures 건 실패"
  exit 1
fi
