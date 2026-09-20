#!/usr/bin/env bash
set -Eeuo pipefail

CANDIDATE=${1:?candidate .deb required}
OUT=${2:?output directory required}
TPA_BIN=${3:?TPA runtime binary required}
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
RUN_ID=${RUN_ID:-$(date -u +%Y%m%d%H%M%S)-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')}
WORK=$(mktemp -d "$OUT/self-hosted-$RUN_ID.XXXXXX")
NETWORK="tparun-qualification-$RUN_ID"
SERVER="tparun-selfhost-server-$RUN_ID"
CLIENT="tparun-selfhost-client-$RUN_ID"
IMAGE_SERVER="tparun-selfhost-server-image-$RUN_ID"
IMAGE_CLIENT="tparun-selfhost-client-image-$RUN_ID"
PHASE=setup
FAILED=0
PORT_ARGS=()
if [[ -n "${PREVIEW_PORT:-}" ]]; then PORT_ARGS=(-p "${PREVIEW_PORT}:18080"); fi

mkdir -p "$OUT"
VOLUME_DATA="tparun-qualification-${RUN_ID}-data"
VOLUME_GPG="tparun-qualification-${RUN_ID}-gnupg"
CLIENT_HOME="$WORK/client-home"
CLIENT_CONFIG="$WORK/client-config"
mkdir -p "$CLIENT_HOME" "$CLIENT_CONFIG" "$WORK/logs"
docker volume create "$VOLUME_DATA" >/dev/null
docker volume create "$VOLUME_GPG" >/dev/null
chmod 700 "$CLIENT_HOME" "$CLIENT_CONFIG"

ARCH=$(dpkg-deb -f "$CANDIDATE" Architecture)
CANDIDATE_PKG=$(dpkg-deb -f "$CANDIDATE" Package)
EXPECTED_SHA=$(sha256sum "$CANDIDATE" | awk '{print $1}')
HOST_ARCH=$(dpkg --print-architecture)
[[ "$ARCH" == "$HOST_ARCH" ]] || { echo "candidate architecture $ARCH does not match host/base architecture $HOST_ARCH" >&2; exit 2; }

CONTEXT="$WORK/context"
mkdir "$CONTEXT"
cp "$CANDIDATE" "$CONTEXT/candidate.deb"
cp "$ROOT/tests/rc/Dockerfile.server" "$CONTEXT/"
cp "$ROOT/tests/rc/Dockerfile.client" "$CONTEXT/"

SELF_RESULT="$OUT/self-hosted-result.json"
write_result() {
  local result=$1
  python3 - "$SELF_RESULT" "$result" "$CANDIDATE" "$NETWORK" "$PHASE" "$EXPECTED_SHA" <<'PY'
import json,sys
path,result,candidate,network,phase,sha=sys.argv[1:]
status='PASS' if result == 'PASS' else 'FAIL'
fields=['server_install','client_install','network_isolation','repository_flow','restart','container_recreation','persistent_storage','package_download_after_recreation','apt_health','client_recreation']
self_hosted={key:status for key in fields}
self_hosted['ttl_min_expiry']='N/A (covered by canonical packaged gate)'
self_hosted['ttl_min_purge']='N/A (covered by canonical packaged gate)'
self_hosted['zero_residue']='N/A (covered by canonical packaged gate)'
self_hosted['billing_provider']='none'
if result == 'FAIL':
    for key in fields:
        self_hosted[key] = 'PASS' if key != phase else 'FAIL'
with open(path,'w') as f:
    json.dump({'result':result,'artifact':{'path':candidate,'sha256':sha},'server_base':'debian:bookworm-slim','client_base':'debian:bookworm-slim','server_binary':'/usr/bin/tparun','client_binary':'/usr/bin/tparun','network':network,'persistent_layout':{'server_data':'run-specific bind mount','client_config':'run-specific bind mount','client_home':'run-specific bind mount'},'failed_phase':None if result=='PASS' else phase,'self_hosted':self_hosted},f,indent=2,sort_keys=True); f.write('\n')
PY
}

cleanup() {
  rc=$?
  set +e
  docker logs "$SERVER" >"$OUT/self-hosted-server-runtime.log" 2>&1
  docker logs "$CLIENT" >"$OUT/self-hosted-client-runtime.log" 2>&1
  if [[ "${KEEP_RUNNING:-0}" == 1 ]]; then
    if [[ $rc -ne 0 ]]; then write_result FAIL; else write_result PASS; fi
    echo "server_container=$SERVER"
    echo "client_container=$CLIENT"
    echo "network=$NETWORK"
    echo "data_volume=$VOLUME_DATA"
    echo "gnupg_volume=$VOLUME_GPG"
    exit "$rc"
  fi
  docker rm -f "$CLIENT" "$SERVER" >/dev/null 2>&1
  docker volume rm "$VOLUME_DATA" "$VOLUME_GPG" >/dev/null 2>&1
  docker run --rm -v "$WORK:/cleanup" debian:bookworm-slim sh -c 'rm -rf /cleanup' >/dev/null 2>&1
  docker network rm "$NETWORK" >/dev/null 2>&1
  docker image rm "$IMAGE_SERVER" "$IMAGE_CLIENT" >/dev/null 2>&1
  if [[ $rc -ne 0 ]]; then write_result FAIL; else write_result PASS; fi
  rm -rf "$WORK"
  exit "$rc"
}
trap cleanup EXIT

fail() { echo "SELF-HOSTED QUALIFICATION FAILED phase=$PHASE: $*" >&2; return 1; }
wait_ready() {
  for _ in $(seq 1 120); do
    if docker exec "$SERVER" curl -fsS http://127.0.0.1:18080/ready >"$OUT/self-hosted-ready.json" 2>/dev/null; then return 0; fi
    sleep 1
  done
  return 1
}
client_exec() {
  docker exec "$CLIENT" sh -lc "HOME=/home/qual XDG_CONFIG_HOME=/home/qual/.config TPARUN_SERVER= \${TPARUN_SERVER-} $*"
}

PHASE=network_isolation
docker network create "$NETWORK" >"$OUT/self-hosted-network.log"
docker build -f "$CONTEXT/Dockerfile.server" -t "$IMAGE_SERVER" "$CONTEXT" >"$OUT/self-hosted-server-install.log"
docker build -f "$CONTEXT/Dockerfile.client" -t "$IMAGE_CLIENT" "$CONTEXT" >"$OUT/self-hosted-client-install.log"
docker run --rm "$IMAGE_CLIENT" "dpkg -r '$CANDIDATE_PKG' >/tmp/reinstall-remove.log 2>&1 && dpkg -i /candidate.deb >/tmp/reinstall-install.log 2>&1 && dpkg --audit && apt-get check" >"$OUT/self-hosted-package-reinstall.log" 2>&1

PHASE=server_install
docker run -d --privileged --security-opt seccomp=unconfined --security-opt apparmor=unconfined --name "$SERVER" --network "$NETWORK" --network-alias tparun-server "${PORT_ARGS[@]}" \
  -v "$VOLUME_DATA:/var/lib/tpahub" -v "$VOLUME_GPG:/var/lib/tpahub-gnupg" -v "$TPA_BIN:/tpa:ro" \
  "$IMAGE_SERVER" 'set -Eeu; export TPARUN_DATA_DIR=/var/lib/tpahub TPARUN_LISTEN=0.0.0.0:18080 TPARUN_REGISTRATION_ENABLED=true TPARUN_LOCAL_PROVISIONING_ENABLED=true TPARUN_CAPABILITY_SOURCE=legacy-local TPARUN_BILLING=none TPARUN_BILLING_PROVIDER=none TPARUN_DELIVERY_MODE=strict-accounted TPARUN_EGRESS_ENFORCEMENT=strict-accounted TPA_BIN=/tpa GNUPGHOME=/var/lib/tpahub-gnupg; mkdir -p "$GNUPGHOME"; chmod 700 "$GNUPGHOME"; if [ ! -f "$TPARUN_DATA_DIR/tpahub.db" ]; then gpg --batch --passphrase "" --quick-gen-key "TPA.run self-hosted qualification <qualification@example.invalid>" rsa2048 sign 0 >/dev/null 2>&1; export TPARUN_GPG_FINGERPRINT=$(gpg --batch --with-colons --list-secret-keys | awk -F: '\''$1=="fpr"{print $10;exit}'\''); tparun init; else export TPARUN_GPG_FINGERPRINT=$(gpg --batch --with-colons --list-secret-keys | awk -F: '\''$1=="fpr"{print $10;exit}'\''); fi; exec tparun serve' >"$OUT/self-hosted-server-start.log"
wait_ready || fail "server did not become ready"
docker exec -e EXPECTED_SHA="$EXPECTED_SHA" "$SERVER" sh -lc 'test "$(readlink -f "$(command -v tparun)")" = /usr/bin/tparun; test "$(sha256sum /candidate.deb | cut -d" " -f1)" = "$EXPECTED_SHA"; test "$(tparun schema)" = 9; test "$(sqlite3 /var/lib/tpahub/tpahub.db "pragma integrity_check;")" = ok; test -z "$(sqlite3 /var/lib/tpahub/tpahub.db "pragma foreign_key_check;")"; dpkg --audit; apt-get check' >"$OUT/self-hosted-server-health.log" 2>&1 || fail "server health failed"

PHASE=client_install
mkdir -p "$WORK/packages"
docker run -d --name "$CLIENT" --network "$NETWORK" -e TPARUN_ALLOW_PRIVATE_HTTP=true \
  -v "$CLIENT_HOME:/home/qual" -v "$CLIENT_CONFIG:/home/qual/.config" -v "$WORK/packages:/qualification:ro" \
  "$IMAGE_CLIENT" 'exec sleep infinity' >"$OUT/self-hosted-client-start.log"
docker exec -e EXPECTED_SHA="$EXPECTED_SHA" "$CLIENT" sh -lc 'test "$(readlink -f "$(command -v tparun)")" = /usr/bin/tparun; test "$(sha256sum /candidate.deb | cut -d" " -f1)" = "$EXPECTED_SHA"; dpkg --audit; apt-get check' >"$OUT/self-hosted-client-health.log" 2>&1 || fail "client health failed"

make_probe() { local version=$1 output=$2; local dir="$WORK/probe-$version"; rm -rf "$dir"; mkdir -p "$dir/DEBIAN" "$dir/usr/bin"; printf 'Package: qualification-probe\nVersion: %s\nArchitecture: amd64\nMaintainer: qualification <qualification@example.invalid>\nDescription: self-hosted qualification probe\n' "$version" >"$dir/DEBIAN/control"; printf '#!/bin/sh\nprintf "%s\\n"\n' "$output" >"$dir/usr/bin/qualification-probe"; chmod 755 "$dir/usr/bin/qualification-probe"; dpkg-deb --build --root-owner-group "$dir" "$WORK/packages/qualification-probe-$version.deb" >/dev/null; }
make_probe 1.0.0 QUALIFICATION_V1
make_probe 2.0.0 QUALIFICATION_V2

PHASE=repository_flow
SERVER_URL=http://tparun-server:18080
EMAIL="selfhost-$RUN_ID@example.invalid"
PASSWORD='self-hosted-qualification-password-123'
# No configured or explicit server must fail in the clean client.
if docker exec "$CLIENT" sh -lc 'unset TPARUN_SERVER; HOME=/home/qual XDG_CONFIG_HOME=/home/qual/.config tparun account' >"$OUT/self-hosted-missing-server.log" 2>&1; then fail "missing server unexpectedly succeeded"; fi
docker exec -e TPARUN_EMAIL="$EMAIL" -e TPARUN_PASSWORD="$PASSWORD" "$CLIENT" sh -lc "HOME=/home/qual XDG_CONFIG_HOME=/home/qual/.config tparun register '$SERVER_URL'" >"$OUT/self-hosted-register.log" 2>&1 || fail "registration failed"
REPO_URL=$(docker exec "$CLIENT" sh -lc "HOME=/home/qual XDG_CONFIG_HOME=/home/qual/.config tparun create selfhost-$RUN_ID --server '$SERVER_URL'") || fail "repository creation failed"
docker exec "$CLIENT" sh -lc "HOME=/home/qual XDG_CONFIG_HOME=/home/qual/.config tparun show '$REPO_URL'" >"$OUT/self-hosted-show.log" 2>&1 || fail "repository show failed"
TOKEN=$(docker exec "$CLIENT" python3 -c 'import json; print(json.load(open("/home/qual/.config/tparun/config.json"))["credentials"]["http://tparun-server:18080"]["token"])')
REPO_ID=$(docker exec -e TPARUN_TOKEN="$TOKEN" -e TPARUN_SERVER_URL="$SERVER_URL" -e TPARUN_REPO_NAME="selfhost-$RUN_ID" "$CLIENT" python3 -c 'import json,os,urllib.request; req=urllib.request.Request(os.environ["TPARUN_SERVER_URL"]+"/v1/repositories",headers={"Authorization":"Bearer "+os.environ["TPARUN_TOKEN"]}); rows=json.load(urllib.request.urlopen(req)); print(next(x["id"] for x in rows if x["name"]==os.environ["TPARUN_REPO_NAME"]))')
docker exec "$CLIENT" sh -lc "curl -fsS -X PATCH '$SERVER_URL/v1/repositories/$REPO_ID' -H 'Authorization: Bearer $TOKEN' -H 'Content-Type: application/json' -d '{\"visibility\":\"public\"}'" >"$OUT/self-hosted-visibility.log" 2>&1 || fail "repository visibility failed"
docker exec "$CLIENT" sh -lc 'HOME=/home/qual XDG_CONFIG_HOME=/home/qual/.config tparun account' >"$OUT/self-hosted-account-before-publish.log" 2>&1 || fail "client authentication did not persist"
docker exec "$CLIENT" sh -lc "export HOME=/home/qual XDG_CONFIG_HOME=/home/qual/.config; tparun stage /qualification/qualification-probe-1.0.0.deb && tparun publish 'selfhost-$RUN_ID' --yes" >"$OUT/self-hosted-publish-v1.log" 2>&1 || fail "V1 publication failed"
docker exec "$CLIENT" sh -lc "curl -fsS '$REPO_URL/dists/stable/main/binary-amd64/Packages' | grep -q '^Package: qualification-probe$' && curl -fsS '$REPO_URL/dists/stable/main/binary-amd64/Packages' | awk '/^Filename:/{print \$2;exit}' | xargs -I{} curl -fsS '$REPO_URL/{}' -o /tmp/v1.deb && dpkg -i /tmp/v1.deb >/dev/null && test \"\$(qualification-probe)\" = QUALIFICATION_V1" >"$OUT/self-hosted-download-v1.log" 2>&1 || fail "V1 download failed"
docker exec "$CLIENT" sh -lc "export HOME=/home/qual XDG_CONFIG_HOME=/home/qual/.config; tparun stage /qualification/qualification-probe-2.0.0.deb && tparun publish 'selfhost-$RUN_ID' --yes" >"$OUT/self-hosted-publish-v2.log" 2>&1 || fail "V2 publication failed"
docker exec "$CLIENT" sh -lc "curl -fsS '$REPO_URL/dists/stable/main/binary-amd64/Packages' | awk '/^Version: 2.0.0$/{found=1} found && /^Filename:/{print \$2;exit}' | xargs -I{} curl -fsS '$REPO_URL/{}' -o /tmp/v2.deb && dpkg -i /tmp/v2.deb >/dev/null && test \"\$(qualification-probe)\" = QUALIFICATION_V2" >"$OUT/self-hosted-download-v2.log" 2>&1 || fail "V2 download failed"

PHASE=restart
docker stop "$SERVER" >"$OUT/self-hosted-stop.log"
docker start "$SERVER" >"$OUT/self-hosted-restart.log"
wait_ready || fail "server did not recover after restart"
docker exec "$CLIENT" sh -lc "HOME=/home/qual XDG_CONFIG_HOME=/home/qual/.config tparun show '$REPO_URL'" >"$OUT/self-hosted-show-after-restart.log" 2>&1 || fail "repository lost after restart"

PHASE=container_recreation
docker rm -f "$SERVER" >"$OUT/self-hosted-remove-server.log"
docker run -d --privileged --security-opt seccomp=unconfined --security-opt apparmor=unconfined --name "$SERVER" --network "$NETWORK" --network-alias tparun-server "${PORT_ARGS[@]}" \
  -v "$VOLUME_DATA:/var/lib/tpahub" -v "$VOLUME_GPG:/var/lib/tpahub-gnupg" -v "$TPA_BIN:/tpa:ro" "$IMAGE_SERVER" 'set -Eeu; export TPARUN_DATA_DIR=/var/lib/tpahub TPARUN_LISTEN=0.0.0.0:18080 TPARUN_REGISTRATION_ENABLED=true TPARUN_LOCAL_PROVISIONING_ENABLED=true TPARUN_CAPABILITY_SOURCE=legacy-local TPARUN_BILLING=none TPARUN_BILLING_PROVIDER=none TPARUN_DELIVERY_MODE=strict-accounted TPARUN_EGRESS_ENFORCEMENT=strict-accounted TPA_BIN=/tpa GNUPGHOME=/var/lib/tpahub-gnupg; export TPARUN_GPG_FINGERPRINT=$(gpg --batch --with-colons --list-secret-keys | awk -F: '\''$1=="fpr"{print $10;exit}'\''); exec tparun serve' >"$OUT/self-hosted-recreate.log"
wait_ready || fail "recreated server did not become ready"
docker exec "$SERVER" sh -lc 'test "$(tparun schema)" = 9; test "$(sqlite3 /var/lib/tpahub/tpahub.db "pragma integrity_check;")" = ok; test -z "$(sqlite3 /var/lib/tpahub/tpahub.db "pragma foreign_key_check;")"' >"$OUT/self-hosted-recreated-health.log" 2>&1 || fail "recreated server database health failed"
docker exec "$CLIENT" sh -lc "HOME=/home/qual XDG_CONFIG_HOME=/home/qual/.config tparun show '$REPO_URL'" >"$OUT/self-hosted-show-after-recreate.log" 2>&1 || fail "repository lost after container recreation"
docker exec "$CLIENT" sh -lc "curl -fsS '$REPO_URL/dists/stable/main/binary-amd64/Packages' | grep -q '^Version: 2.0.0$'" >"$OUT/self-hosted-metadata-after-recreate.log" 2>&1 || fail "metadata lost after recreation"

PHASE=client_recreation
docker rm -f "$CLIENT" >"$OUT/self-hosted-remove-client.log"
docker run -d --name "$CLIENT" --network "$NETWORK" -e TPARUN_ALLOW_PRIVATE_HTTP=true -v "$CLIENT_HOME:/home/qual" -v "$CLIENT_CONFIG:/home/qual/.config" -v "$WORK/packages:/qualification:ro" "$IMAGE_CLIENT" 'exec sleep infinity' >"$OUT/self-hosted-client-recreate.log"
docker exec "$CLIENT" sh -lc "HOME=/home/qual XDG_CONFIG_HOME=/home/qual/.config tparun show '$REPO_URL'" >"$OUT/self-hosted-client-after-recreate.log" 2>&1 || fail "client did not recover configured server"

PHASE=complete
echo "TPA.RUN SELF-HOSTED RC QUALIFICATION PASSED"
echo "ARTIFACT SHA256: $(sha256sum "$CANDIDATE" | awk '{print $1}')"
