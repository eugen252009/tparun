#!/usr/bin/env bash
set -Eeuo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TPA_DIR=${TPA_DIR:-$ROOT/../tpa}
IMAGE=${TPAHUB_IMAGE:-tpahub}
VERSION=$(awk -F'"' '/const Current =/{print $2}' "$ROOT/internal/version/version.go")
REVISION=$(git -C "$ROOT" rev-parse HEAD)

if [[ -n "$(git -C "$ROOT" status --porcelain)" ]]; then
  echo "source worktree must be clean before building the image" >&2
  exit 1
fi
if [[ ! -d "$TPA_DIR" ]]; then
  echo "TPA source directory not found: $TPA_DIR (set TPA_DIR explicitly)" >&2
  exit 1
fi

context=$(mktemp -d)
cleanup() { rm -rf "$context"; }
trap cleanup EXIT
mkdir -p "$context/tpahub" "$context/tpa"
tar -C "$ROOT" --exclude=.git --exclude=.cocoindex_code -cf - . | tar -C "$context/tpahub" -xf -
tar -C "$TPA_DIR" --exclude=.git --exclude=.cocoindex_code -cf - . | tar -C "$context/tpa" -xf -

short=${REVISION:0:12}
docker build --pull \
  --build-arg "TPAHUB_REVISION=$REVISION" \
  --build-arg "TPAHUB_VERSION=$VERSION" \
  -f "$ROOT/deploy/container/Dockerfile" \
  -t "$IMAGE:$short" -t "$IMAGE:selfhost-$VERSION" "$context"

docker image inspect "$IMAGE:$short" --format 'image={{.Id}}\ncreated={{.Created}}\nrepo_digests={{json .RepoDigests}}\nrevision={{index .Config.Labels "org.opencontainers.image.revision"}}\nversion={{index .Config.Labels "org.opencontainers.image.version"}}'
printf 'tags=%s:%s,%s:selfhost-%s\n' "$IMAGE" "$short" "$IMAGE" "$VERSION"
inspect_container="tpahub-image-inspect-$$"
inspect_file=$(mktemp)
trap 'rm -f "$inspect_file"; cleanup' EXIT
docker create --name "$inspect_container" "$IMAGE:$short" >/dev/null
docker cp "$inspect_container:/usr/local/bin/tpahub" "$inspect_file"
printf 'hub_sha256='; sha256sum "$inspect_file" | cut -d' ' -f1
docker rm "$inspect_container" >/dev/null
