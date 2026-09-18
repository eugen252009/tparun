#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d)
CONTAINER="tpahub-release-qualification-$$"
cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  [[ -z "${HTTP_PID:-}" ]] || kill "$HTTP_PID" 2>/dev/null || true
  [[ -z "${HTTP_PID:-}" ]] || wait "$HTTP_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT INT TERM

mkdir -p "$TMP/gnupg" "$TMP/repo/pool" "$TMP/repo/dists/stable/main/binary-amd64"
chmod 700 "$TMP/gnupg"
cat >"$TMP/key.conf" <<'EOF'
%no-protection
Key-Type: RSA
Key-Length: 2048
Name-Real: TPA Hub bootstrap qualification
Name-Email: bootstrap@example.invalid
Expire-Date: 0
%commit
EOF
GNUPGHOME="$TMP/gnupg" gpg --batch --generate-key "$TMP/key.conf" >/dev/null 2>&1
KEY=$(GNUPGHOME="$TMP/gnupg" gpg --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr"{print $10;exit}')
GNUPGHOME="$TMP/gnupg" gpg --batch --armor --export "$KEY" >"$TMP/public.asc"

HUB_BINARY=${TPAHUB_APPLICATION_BINARY:-$TMP/tpahub}
if [[ ! -x "$HUB_BINARY" ]]; then
  go build -trimpath -o "$HUB_BINARY" ./cmd/tpahub
fi
HUB_SHA=$(sha256sum "$HUB_BINARY" | awk '{print $1}')
make_app() {
  local version=$1 work="$TMP/app-$1"
  rm -rf "$work"
  mkdir -p "$work/DEBIAN" "$work/usr/bin"
  cp "$HUB_BINARY" "$work/usr/bin/tpahub"
  chmod 0755 "$work/usr/bin/tpahub"
  cat >"$work/DEBIAN/control" <<EOF
Package: tpahub
Version: $version
Section: net
Priority: optional
Architecture: amd64
Maintainer: TPA Hub qualification <qualification@example.invalid>
Description: TPA Hub application qualification package
EOF
  dpkg-deb --build --root-owner-group "$work" "$TMP/repo/pool/tpahub_${version}_amd64.deb" >/dev/null
}
make_app 0.1.1

refresh_repo() {
  (cd "$TMP/repo" && dpkg-scanpackages pool /dev/null >dists/stable/main/binary-amd64/Packages 2>/dev/null)
  gzip -n -f -k "$TMP/repo/dists/stable/main/binary-amd64/Packages"
  {
    printf 'Origin: TPA Hub qualification\nLabel: TPA Hub qualification\nSuite: stable\nCodename: stable\nArchitectures: amd64\nComponents: main\n'
    (cd "$TMP/repo" && apt-ftparchive release dists/stable)
  } >"$TMP/repo/dists/stable/Release"
  GNUPGHOME="$TMP/gnupg" gpg --batch --yes --pinentry-mode loopback --passphrase '' --clearsign --digest-algo SHA256 \
    -o "$TMP/repo/dists/stable/InRelease" "$TMP/repo/dists/stable/Release"
}
refresh_repo
python3 - "$TMP/repo" "$TMP/port" <<'PY' >"$TMP/http.log" 2>&1 &
import http.server, pathlib, socketserver, sys
root = pathlib.Path(sys.argv[1]).resolve()
with socketserver.TCPServer(("127.0.0.1", 0), lambda *a, **k: http.server.SimpleHTTPRequestHandler(*a, directory=str(root), **k)) as server:
    pathlib.Path(sys.argv[2]).write_text(str(server.server_address[1]))
    server.serve_forever()
PY
HTTP_PID=$!
for _ in $(seq 1 100); do [[ -s "$TMP/port" ]] && break; sleep .05; done
PORT=$(cat "$TMP/port")
SOURCE_URL="http://127.0.0.1:$PORT"
SOURCE_DATE_EPOCH=0 TPAHUB_RELEASE_REPOSITORY_URL="$SOURCE_URL" TPAHUB_RELEASE_PUBLIC_KEY="$TMP/public.asc" \
  "$ROOT/packaging/tpahub-release/build.sh" "$TMP/tpahub-release.deb" >/dev/null
BOOTSTRAP_SHA=$(sha256sum "$TMP/tpahub-release.deb" | awk '{print $1}')
[[ "$(dpkg-deb -f "$TMP/tpahub-release.deb" Package)" == tpahub-release ]]
[[ "$(dpkg-deb -f "$TMP/tpahub-release.deb" Architecture)" == all ]]
! dpkg-deb -c "$TMP/tpahub-release.deb" | grep -Eq '/usr/bin/tpahub|private|tpahub\.db'
dpkg-deb -c "$TMP/tpahub-release.deb" | grep -q '/usr/share/keyrings/tpahub-archive-keyring.gpg'
dpkg-deb -c "$TMP/tpahub-release.deb" | grep -q '/etc/apt/sources.list.d/tpahub.sources'
rm -rf "$TMP/control"
dpkg-deb -e "$TMP/tpahub-release.deb" "$TMP/control"
find "$TMP/control" -maxdepth 1 -type f -printf '%f\n' | grep -qx postrm
! grep -Eiq 'apt(-get)?|apt-cache' "$TMP/control/postrm"
GNUPGHOME="$TMP/gnupg" gpg --batch --show-keys "$TMP/public.asc" >/dev/null

docker run -d --name "$CONTAINER" --network=host debian:bookworm-slim sleep 600 >/dev/null
docker cp "$TMP/tpahub-release.deb" "$CONTAINER:/tmp/tpahub-release.deb"
docker cp "$ROOT/install.sh" "$CONTAINER:/tmp/install.sh"
docker exec "$CONTAINER" sh -eu -c '
  rm -f /etc/apt/sources.list.d/tpahub* /etc/apt/auth.conf.d/tpahub*
  TPAHUB_RELEASE_PACKAGE_PATH=/tmp/tpahub-release.deb TPAHUB_RELEASE_PACKAGE_SHA256='"$BOOTSTRAP_SHA"' bash /tmp/install.sh >/tmp/install.log
  test -f /usr/share/keyrings/tpahub-archive-keyring.gpg
  test -f /etc/apt/sources.list.d/tpahub.sources
  test "$(find /etc/apt/sources.list.d -name "tpahub.sources" | wc -l)" = 1
  grep -F "Signed-By: /usr/share/keyrings/tpahub-archive-keyring.gpg" /etc/apt/sources.list.d/tpahub.sources
  apt-get update >/dev/null
  apt-cache policy tpahub | grep -F "Candidate: 0.1.1"
  apt-get install -y tpahub=0.1.1 >/dev/null
  test "$(dpkg-query -W -f=\${Version} tpahub)" = 0.1.1
  test -x /usr/bin/tpahub
  mkdir -p /var/lib/tpahub-qualification
  printf retained >/var/lib/tpahub-qualification/sentinel
  apt-get install --reinstall -y /tmp/tpahub-release.deb >/dev/null
  test "$(find /etc/apt/sources.list.d -name "tpahub.sources" | wc -l)" = 1
'

make_app 0.1.2
refresh_repo
docker exec "$CONTAINER" sh -eu -c '
  apt-get update >/dev/null
  apt-get upgrade -y >/dev/null
  test "$(dpkg-query -W -f=\${Version} tpahub)" = 0.1.2
  apt-get remove -y tpahub-release >/dev/null
  test ! -e /etc/apt/sources.list.d/tpahub.sources
  test -x /usr/bin/tpahub
  test -f /var/lib/tpahub-qualification/sentinel
'
printf '%s\n' "TPA HUB RELEASE BOOTSTRAP QUALIFIED ($BOOTSTRAP_SHA)"
