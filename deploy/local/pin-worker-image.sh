#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
env_file="$root/deploy/local/.env"
image_id=$(docker image inspect switchback-worker:local --format '{{.Id}}')
case "$image_id" in
  sha256:[0-9a-f][0-9a-f]*) ;;
  *) printf '%s\n' "Worker build returned an invalid immutable image ID" >&2; exit 1 ;;
esac
if [ "${#image_id}" -ne 71 ]; then
  printf '%s\n' "Worker build returned an invalid immutable image ID" >&2
  exit 1
fi
temporary="$env_file.tmp"
awk -v image="$image_id" '
  /^SWITCHBACK_WORKER_IMAGE=/ { print "SWITCHBACK_WORKER_IMAGE=" image; replaced=1; next }
  { print }
  END { if (!replaced) print "SWITCHBACK_WORKER_IMAGE=" image }
' "$env_file" > "$temporary"
chmod 600 "$temporary"
mv "$temporary" "$env_file"
printf '%s\n' "Pinned local Worker image: $image_id"
