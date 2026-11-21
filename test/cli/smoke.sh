#!/bin/sh
# End-to-end check of the CLI against the local Docker daemon, as a CI step
# would use it. Needs nginx:1.27-alpine and a built layve binary:
#
#   go build -o bin/layve ./cmd/layve && test/cli/smoke.sh bin/layve
set -eu
layve=${1:-bin/layve}
here=$(cd "$(dirname "$0")" && pwd)
img=layve-smoke:test
trap 'docker rmi -f "$img" layve-smoke:squashed >/dev/null 2>&1 || true' EXIT

step() { printf '\n== %s\n' "$*"; }
expect() { # expect CODE command...
  want=$1; shift
  set +e; "$@"; got=$?; set -e
  [ "$got" -eq "$want" ] || { echo "expected exit $want, got $got: $*" >&2; exit 1; }
}

docker build -q -t "$img" "$here" >/dev/null

step "inspect"
"$layve" inspect "$img"
"$layve" inspect "$img" --json | grep -q '"wasted_files"'

step "check fails on wasted space"
expect 2 "$layve" check "$img" --max-wasted 1MB

step "fix --dry-run changes nothing"
before=$(docker image inspect -f '{{.Id}}' "$img")
"$layve" fix "$img" --wasted --purge /root/.npmrc --dry-run
[ "$(docker image inspect -f '{{.Id}}' "$img")" = "$before" ]

step "fix overwrites the tag"
"$layve" fix "$img" --wasted --purge /root/.npmrc
[ "$(docker image inspect -f '{{.Id}}' "$img")" != "$before" ]
docker image inspect "$before" >/dev/null # the previous image is still there
docker rmi "$before" >/dev/null

step "check passes"
expect 0 "$layve" check "$img" --max-wasted 1MB

step "the secret is gone from every layer"
if docker save "$img" | tar -xO 2>/dev/null | grep -aq npm_SECRET; then
  echo "secret still in the image" >&2; exit 1
fi

step "the image still works"
docker run --rm --entrypoint sh "$img" -c 'cat /usr/share/nginx/html/index.html; nginx -v'

step "squash into a new tag"
"$layve" fix "$img" --squash -o layve-smoke:squashed --no-mark
[ "$(docker image inspect -f '{{len .RootFS.Layers}}' layve-smoke:squashed)" = 1 ]

printf '\nsmoke test passed\n'
