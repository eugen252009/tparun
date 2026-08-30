#!/bin/sh
# Complete local qualification. Requires: Go, TPA checkout, gpg, curl, dpkg-deb, docker.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TPA_DIR=${TPA_DIR:-"$ROOT/../tpa"}
TMP=$(mktemp -d); PORT=${TPAHUB_TEST_PORT:-18080}; SERVER="http://127.0.0.1:$PORT"; GNUPGHOME="$TMP/gnupg"; DATA="$TMP/data"
export GNUPGHOME
cleanup(){ [ -n "${HUB_PID:-}" ] && kill "$HUB_PID" 2>/dev/null || true; wait "${HUB_PID:-}" 2>/dev/null || true; rm -rf "$TMP"; }
trap cleanup EXIT INT TERM
mkdir -m 700 "$GNUPGHOME"
cat >"$TMP/key.conf" <<'EOF'
%no-protection
Key-Type: RSA
Key-Length: 2048
Name-Real: TPA Hub qualification
Name-Email: qualification@example.invalid
Expire-Date: 0
%commit
EOF
gpg --batch --generate-key "$TMP/key.conf" >/dev/null 2>&1
KEY=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr"{print $10;exit}')
[ -n "$KEY" ]
cp -a "$TPA_DIR" "$TMP/tpa-source"
(cd "$TMP/tpa-source" && go build -o "$TMP/bootstrap-tpa" .)
(cd "$TMP/tpa-source" && TPA_VERSION=1.0.0 ./build.sh >/dev/null)
cp "$TMP/tpa-source/dist/tpa-amd64.deb" "$TMP/tpa_1.0.0_amd64.deb"
go build -o "$TMP/tpahub" ./cmd/tpahub
TPAHUB_LISTEN="127.0.0.1:$PORT" TPAHUB_DATA_DIR="$DATA" TPA_BIN="$TMP/bootstrap-tpa" TPAHUB_GPG_FINGERPRINT="$KEY" GNUPGHOME="$GNUPGHOME" "$TMP/tpahub" serve >"$TMP/server.log" 2>&1 & HUB_PID=$!
i=0; while ! curl -sS "$SERVER/v1/repositories/nope" >/dev/null 2>&1; do i=$((i+1)); [ "$i" -lt 100 ] || { cat "$TMP/server.log"; exit 1; }; sleep .1; done
AUTH=$(curl -fsS -X POST "$SERVER/v1/accounts" -H 'content-type: application/json' -d '{"email":"qualification@example.invalid","password":"qualification-password"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
REPO=$(curl -fsS -X POST "$SERVER/v1/repositories" -H 'content-type: application/json' -H "Authorization: Bearer $AUTH" -d '{"name":"tpa","visibility":"public"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
publish(){ curl -fsS -X POST "$SERVER/v1/repositories/$REPO/publishes" -H "Authorization: Bearer $AUTH" -F "files=@$1"; }
JOB=$(publish "$TMP/tpa_1.0.0_amd64.deb" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
wait_job(){ i=0; while :; do state=$(curl -fsS "$SERVER/v1/publish-jobs/$1" -H "Authorization: Bearer $AUTH"|python3 -c 'import json,sys; print(json.load(sys.stdin)["State"])'); case "$state" in succeeded)return;; failed) curl -fsS "$SERVER/v1/publish-jobs/$1" -H "Authorization: Bearer $AUTH"; exit 1;; esac; i=$((i+1)); [ "$i" -lt 300 ] || exit 1; sleep .1; done; }
wait_job "$JOB"
curl -fsS "$SERVER/r/$REPO/dists/stable/InRelease" >/dev/null
curl -fsS "$SERVER/r/$REPO/dists/stable/main/binary-amd64/Packages" | grep -q '^Package: tpa$'
TPA_PATH=$(curl -fsS "$SERVER/r/$REPO/dists/stable/main/binary-amd64/Packages" | awk '/^Filename:/{print $2;exit}')
curl -fsS "$SERVER/r/$REPO/$TPA_PATH" >/dev/null
make_deb(){
 name=$1; version=$2; depends=${3:-}; root="$TMP/$name-$version"; mkdir -p "$root/DEBIAN" "$root/usr/share/$name"
 { printf 'Package: %s\nVersion: %s\nArchitecture: all\nMaintainer: qualification <qualification@example.invalid>\nDescription: qualification package\n' "$name" "$version"; [ -z "$depends" ] || printf 'Depends: %s\n' "$depends"; } >"$root/DEBIAN/control"
 printf '%s\n' "$version" >"$root/usr/share/$name/version"; dpkg-deb --build "$root" "$TMP/$name-$version.deb" >/dev/null
}
make_deb hub-qualification 1.0.0; make_deb hub-qualification 2.0.0
make_deb dep-base 1.0.0; make_deb dep-meta 1.0.0 dep-base; make_deb dep-chain 1.0.0 dep-meta
JOB=$(curl -fsS -X POST "$SERVER/v1/repositories/$REPO/publishes" -H "Authorization: Bearer $AUTH" -F files=@"$TMP/hub-qualification-1.0.0.deb" -F files=@"$TMP/hub-qualification-2.0.0.deb" -F files=@"$TMP/dep-base-1.0.0.deb" -F files=@"$TMP/dep-meta-1.0.0.deb" -F files=@"$TMP/dep-chain-1.0.0.deb" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
wait_job "$JOB"
# An invalid artifact must fail without replacing the live repository.
BEFORE=$(curl -fsS "$SERVER/r/$REPO/dists/stable/InRelease" | sha256sum)
printf 'not a deb' >"$TMP/bad.deb"
JOB=$(curl -fsS -X POST "$SERVER/v1/repositories/$REPO/publishes" -H "Authorization: Bearer $AUTH" -F files=@"$TMP/bad.deb" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
i=0; while :; do state=$(curl -fsS "$SERVER/v1/publish-jobs/$JOB" -H "Authorization: Bearer $AUTH"|python3 -c 'import json,sys; print(json.load(sys.stdin)["State"])'); [ "$state" = failed ] && break; i=$((i+1)); [ "$i" -lt 300 ] || exit 1; sleep .1; done
[ "$BEFORE" = "$(curl -fsS "$SERVER/r/$REPO/dists/stable/InRelease" | sha256sum)" ]
# Private repository and reader-credential qualification.
PRIVATE=$(curl -fsS -X POST "$SERVER/v1/repositories" -H "Authorization: Bearer $AUTH" -H 'content-type: application/json' -d '{"name":"private-tools"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
JOB=$(curl -fsS -X POST "$SERVER/v1/repositories/$PRIVATE/publishes" -H "Authorization: Bearer $AUTH" -F files=@"$TMP/tpa_1.0.0_amd64.deb" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])'); wait_job "$JOB"
READER=$(curl -fsS -X POST "$SERVER/v1/repositories/$PRIVATE/reader-credentials" -H "Authorization: Bearer $AUTH" -H 'content-type: application/json' -d '{"name":"qualification"}'); READER_TOKEN=$(echo "$READER"|python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])'); READER_ID=$(echo "$READER"|python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
[ "$(curl -s -o /dev/null -w '%{http_code}' -u reader:wrong "$SERVER/r/$PRIVATE/dists/stable/InRelease")" = 401 ]
[ "$(curl -s -o /dev/null -w '%{http_code}' -u reader:$READER_TOKEN "$SERVER/r/$PRIVATE/dists/stable/InRelease")" = 200 ]
AUTHB=$(curl -fsS -X POST "$SERVER/v1/accounts" -H 'content-type: application/json' -d '{"email":"qualification-b@example.invalid","password":"qualification-password"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
[ "$(curl -s -o /dev/null -w '%{http_code}' "$SERVER/v1/repositories/$PRIVATE" -H "Authorization: Bearer $AUTHB")" = 404 ]
[ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$SERVER/v1/repositories/$PRIVATE/publishes" -H "Authorization: Bearer $AUTHB")" = 404 ]
curl -fsS -X DELETE "$SERVER/v1/repositories/$PRIVATE/reader-credentials/$READER_ID" -H "Authorization: Bearer $AUTH" >/dev/null
[ "$(curl -s -o /dev/null -w '%{http_code}' -u reader:$READER_TOKEN "$SERVER/r/$PRIVATE/dists/stable/InRelease")" = 401 ]
READER_TOKEN=$(curl -fsS -X POST "$SERVER/v1/repositories/$PRIVATE/reader-credentials" -H "Authorization: Bearer $AUTH" -H 'content-type: application/json' -d '{"name":"qualification-new"}'|python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
# Restart persistence is tested before the client container starts.
kill "$HUB_PID"; wait "$HUB_PID" || true; unset HUB_PID
TPAHUB_LISTEN="127.0.0.1:$PORT" TPAHUB_DATA_DIR="$DATA" TPA_BIN="$TMP/bootstrap-tpa" TPAHUB_GPG_FINGERPRINT="$KEY" GNUPGHOME="$GNUPGHOME" "$TMP/tpahub" serve >"$TMP/server.log" 2>&1 & HUB_PID=$!
i=0; while ! curl -fsS "$SERVER/r/$REPO/dists/stable/InRelease" >/dev/null 2>&1; do i=$((i+1)); [ "$i" -lt 100 ] || exit 1; sleep .1; done
# The Debian client uses the real CLI, not hand-written apt configuration.
cat >"$TMP/Dockerfile" <<EOF
FROM debian:bookworm-slim
COPY tpahub /usr/local/bin/tpahub
COPY bootstrap-tpa /usr/local/bin/bootstrap-tpa
RUN chmod 755 /usr/local/bin/tpahub /usr/local/bin/bootstrap-tpa
RUN rm -f /etc/apt/sources.list /etc/apt/sources.list.d/tpahub-*.list
RUN tpahub repo add $REPO --server $SERVER && tpahub repo add $REPO --server $SERVER && test "\$(grep -c '^deb ' /etc/apt/sources.list.d/tpahub-$REPO.list)" = 1
RUN test -s /etc/apt/keyrings/tpahub-$REPO.gpg && grep -F 'signed-by=/etc/apt/keyrings/tpahub-$REPO.gpg' /etc/apt/sources.list.d/tpahub-$REPO.list
RUN tpahub repo add $PRIVATE --server $SERVER --token $READER_TOKEN && test -f /etc/apt/auth.conf.d/tpahub-$PRIVATE && test "\$(stat -c %a /etc/apt/auth.conf.d/tpahub-$PRIVATE)" = 600
RUN apt-get update && apt-get install -y --no-install-recommends tpa && command -v tpa && dpkg-query -W tpa && tpa schema >/dev/null
RUN apt-get install -y hub-qualification=1.0.0 && test "\$(cat /usr/share/hub-qualification/version)" = 1.0.0
RUN apt-get upgrade -y && test "\$(cat /usr/share/hub-qualification/version)" = 2.0.0
RUN apt-get install -y --allow-downgrades hub-qualification=1.0.0 && test "\$(cat /usr/share/hub-qualification/version)" = 1.0.0
RUN apt-get install -y dep-chain && test -f /usr/share/dep-base/version && test -f /usr/share/dep-meta/version && test -f /usr/share/dep-chain/version
RUN tpahub repo remove $REPO && test ! -e /etc/apt/sources.list.d/tpahub-$REPO.list && test ! -e /etc/apt/keyrings/tpahub-$REPO.gpg
EOF
docker build --network=host --build-arg REPO="$REPO" -f "$TMP/Dockerfile" "$TMP"
printf '%s\n' 'REAL APT SELF-HOSTING QUALIFIED'
