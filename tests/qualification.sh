#!/bin/sh
# Complete local qualification. Requires: Go, TPA checkout, gpg, curl, dpkg-deb, docker.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TPA_DIR=${TPA_DIR:-"$ROOT/../tpa"}
TMP=$(mktemp -d); PORT=${TPAHUB_TEST_PORT:-18080}; SERVER="http://127.0.0.1:$PORT"; GNUPGHOME="$TMP/gnupg"; DATA="$TMP/data"
export GNUPGHOME
# Qualify the strict durable path explicitly; init otherwise defaults to metrics-only.
export TPAHUB_DELIVERY_MODE=strict-accounted TPAHUB_EGRESS_ENFORCEMENT=strict-accounted
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
TPAHUB_DATA_DIR="$DATA" "$TMP/tpahub" init
[ "$("$TMP/tpahub" schema)" = 5 ]
TPAHUB_LISTEN="127.0.0.1:$PORT" TPAHUB_DATA_DIR="$DATA" TPAHUB_REGISTRATION_ENABLED=true TPAHUB_LOCAL_PROVISIONING_ENABLED=true TPA_BIN="$TMP/bootstrap-tpa" TPAHUB_GPG_FINGERPRINT="$KEY" GNUPGHOME="$GNUPGHOME" "$TMP/tpahub" serve >"$TMP/server.log" 2>&1 & HUB_PID=$!
i=0; while ! curl -sS "$SERVER/v1/repositories/nope" >/dev/null 2>&1; do i=$((i+1)); [ "$i" -lt 100 ] || { cat "$TMP/server.log"; exit 1; }; sleep .1; done
client(){ HOME="$TMP/client-home" XDG_CONFIG_HOME="$TMP/client-config" TPAHUB_EMAIL=qualification@example.invalid TPAHUB_PASSWORD=qualification-password "$TMP/tpahub" "$@" --server "$SERVER"; }
client register
client login
AUTH=$(python3 - "$TMP/client-config/tpahub/config.json" "$SERVER" <<'PY'
import json,sys
with open(sys.argv[1]) as f: print(json.load(f)['credentials'][sys.argv[2]]['Token'])
PY
)
python3 - "$DATA/tpahub.db" <<'PY'
import sqlite3,sys
with sqlite3.connect(sys.argv[1]) as db:
    assert db.execute('SELECT singleton,version FROM tpahub_schema').fetchall() == [(1,5)]
    assert db.execute("SELECT DISTINCT storage_limit_bytes FROM product_grants WHERE capability_id='repository.publish'").fetchall() == [(5000000,)]
PY
REPO=$(client repo create tpa --public | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
client repo show tpa >/dev/null
client repo show "$REPO" >/dev/null
publish(){ client publish tpa "$1"; }
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
make_deb restored-probe 1.0.0
make_deb dep-base 1.0.0; make_deb dep-meta 1.0.0 dep-base; make_deb dep-chain 1.0.0 dep-meta
JOB=$(curl -fsS -X POST "$SERVER/v1/repositories/$REPO/publishes" -H "Authorization: Bearer $AUTH" -F files=@"$TMP/hub-qualification-1.0.0.deb" -F files=@"$TMP/hub-qualification-2.0.0.deb" -F files=@"$TMP/restored-probe-1.0.0.deb" -F files=@"$TMP/dep-base-1.0.0.deb" -F files=@"$TMP/dep-meta-1.0.0.deb" -F files=@"$TMP/dep-chain-1.0.0.deb" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
wait_job "$JOB"
# An invalid artifact must fail without replacing the live repository.
BEFORE=$(curl -fsS "$SERVER/r/$REPO/dists/stable/InRelease" | sha256sum)
printf 'not a deb' >"$TMP/bad.deb"
JOB=$(curl -fsS -X POST "$SERVER/v1/repositories/$REPO/publishes" -H "Authorization: Bearer $AUTH" -F files=@"$TMP/bad.deb" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
i=0; while :; do state=$(curl -fsS "$SERVER/v1/publish-jobs/$JOB" -H "Authorization: Bearer $AUTH"|python3 -c 'import json,sys; print(json.load(sys.stdin)["State"])'); [ "$state" = failed ] && break; i=$((i+1)); [ "$i" -lt 300 ] || exit 1; sleep .1; done
[ "$BEFORE" = "$(curl -fsS "$SERVER/r/$REPO/dists/stable/InRelease" | sha256sum)" ]
# Private repository and reader-credential qualification.
PRIVATE=$(client repo create private-tools | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
client repo show private-tools >/dev/null
JOB=$(client publish private-tools "$TMP/tpa_1.0.0_amd64.deb" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])'); wait_job "$JOB"
READER_TOKEN=$(client repo token create private-tools qualification | awk '/^Token:/{print $2}')
READER_ID=$(client repo token list "$PRIVATE" | python3 -c 'import json,sys;print(next(x["ID"] for x in json.load(sys.stdin) if x["Name"]=="qualification"))')
make_deb private-b-probe 1.0.0
PRIVATE_B=$(client repo create private-b | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
JOB=$(client publish "$PRIVATE_B" "$TMP/private-b-probe-1.0.0.deb" | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])'); wait_job "$JOB"
READER_B=$(client repo token create private-b reader-b | awk '/^Token:/{print $2}')
[ "$(curl -s -o /dev/null -w '%{http_code}' -u reader:$READER_TOKEN "$SERVER/r/$PRIVATE_B/dists/stable/InRelease")" = 401 ]
[ "$(curl -s -o /dev/null -w '%{http_code}' -u reader:$READER_B "$SERVER/r/$PRIVATE_B/dists/stable/InRelease")" = 200 ]
[ "$(curl -s -o /dev/null -w '%{http_code}' -u reader:wrong "$SERVER/r/$PRIVATE/dists/stable/InRelease")" = 401 ]
[ "$(curl -s -o /dev/null -w '%{http_code}' -u reader:$READER_TOKEN "$SERVER/r/$PRIVATE/dists/stable/InRelease")" = 200 ]
AUTHB=$(curl -fsS -X POST "$SERVER/v1/accounts" -H 'content-type: application/json' -d '{"email":"qualification-b@example.invalid","password":"qualification-password"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
[ "$(curl -s -o /dev/null -w '%{http_code}' "$SERVER/v1/repositories/$PRIVATE" -H "Authorization: Bearer $AUTHB")" = 404 ]
[ "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$SERVER/v1/repositories/$PRIVATE/publishes" -H "Authorization: Bearer $AUTHB")" = 404 ]
client repo token revoke private-tools "$READER_ID" >/dev/null
[ "$(curl -s -o /dev/null -w '%{http_code}' -u reader:$READER_TOKEN "$SERVER/r/$PRIVATE/dists/stable/InRelease")" = 401 ]
[ "$(curl -s -o /dev/null -w '%{http_code}' -u reader:$READER_B "$SERVER/r/$PRIVATE_B/dists/stable/InRelease")" = 200 ]
REVOKED_READER_TOKEN=$READER_TOKEN
READER_TOKEN=$(client repo token create "$PRIVATE" qualification-new | awk '/^Token:/{print $2}')
client repo token list private-tools >/dev/null
# Restart persistence is tested before the client container starts.
PRIVATE_BEFORE=$(curl -fsS -u reader:$READER_TOKEN "$SERVER/r/$PRIVATE/dists/stable/InRelease" | sha256sum)
kill "$HUB_PID"; wait "$HUB_PID" || true; unset HUB_PID
DURABLE_BEFORE=$("$TMP/tpahub" digest --db "$DATA/tpahub.db")
# Destructive whole-instance backup/restore drill. The restored binary, TPA,
# signing home and Schema 5 data are used below; the original state is removed.
BACKUP="$TMP/tpahub-backup.tar.gz"
printf 'schema=5\nhub_version=0.1.1\ntpa_fingerprint=%s\ndelivery=strict-accounted\nenforcement=strict-accounted\n' "$KEY" >"$TMP/backup-manifest.txt"
tar -czf "$BACKUP" -C "$TMP" data gnupg bootstrap-tpa tpahub backup-manifest.txt
for required in data/tpahub.db data/server-profile.json gnupg bootstrap-tpa tpahub backup-manifest.txt; do tar -tzf "$BACKUP" | grep -Eq "^${required}(/|$)" || { echo "backup missing $required" >&2; exit 1; }; done
BACKUP_SHA=$(sha256sum "$BACKUP" | awk '{print $1}')
rm -rf "$DATA" "$GNUPGHOME"
RESTORE="$TMP/restored"
mkdir -m 700 "$RESTORE"
tar -xzf "$BACKUP" -C "$RESTORE"
DATA="$RESTORE/data"
GNUPGHOME="$RESTORE/gnupg"
RESTORED_HUB="$RESTORE/tpahub"
RESTORED_TPA="$RESTORE/bootstrap-tpa"
chmod 700 "$DATA" "$GNUPGHOME"; chmod 600 "$DATA/server-profile.json"
python3 - "$BACKUP_SHA" "$BACKUP" <<'PY'
import hashlib,sys
want=sys.argv[1]
h=hashlib.sha256(open(sys.argv[2],'rb').read()).hexdigest()
assert h == want, (h,want)
PY
python3 - "$DATA/tpahub.db" <<'PY'
import sqlite3,sys
with sqlite3.connect(sys.argv[1]) as db:
    for table in ['accounts','product_grants','sessions','repositories','reader_credentials','repository_publications','publish_jobs','repository_egress_daily']:
        assert db.execute('SELECT COUNT(*) FROM '+table).fetchone()[0] > 0,table
    assert db.execute('SELECT SUM(egress_bytes) FROM repository_egress_daily').fetchone()[0] > 0
PY
TPAHUB_LISTEN="127.0.0.1:$PORT" TPAHUB_DATA_DIR="$DATA" TPAHUB_REGISTRATION_ENABLED=true TPAHUB_LOCAL_PROVISIONING_ENABLED=true TPA_BIN="$RESTORED_TPA" TPAHUB_GPG_FINGERPRINT="$KEY" GNUPGHOME="$GNUPGHOME" "$RESTORED_HUB" serve >"$TMP/server.log" 2>&1 & HUB_PID=$!
i=0; while ! curl -sS "$SERVER/v1/repositories/nope" >/dev/null 2>&1; do i=$((i+1)); [ "$i" -lt 100 ] || exit 1; sleep .1; done
[ "$DURABLE_BEFORE" = "$("$RESTORED_HUB" digest --db "$DATA/tpahub.db")" ]
[ "$BEFORE" = "$(curl -fsS "$SERVER/r/$REPO/dists/stable/InRelease" | sha256sum)" ]
make_deb restored-probe 2.0.0
JOB=$(curl -fsS -X POST "$SERVER/v1/repositories/$REPO/publishes" -H "Authorization: Bearer $AUTH" -F files=@"$TMP/restored-probe-2.0.0.deb" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
wait_job "$JOB"
[ "$PRIVATE_BEFORE" = "$(curl -fsS -u reader:$READER_TOKEN "$SERVER/r/$PRIVATE/dists/stable/InRelease" | sha256sum)" ]
[ "$(curl -s -o /dev/null -w '%{http_code}' -u reader:$REVOKED_READER_TOKEN "$SERVER/r/$PRIVATE/dists/stable/InRelease")" = 401 ]
curl -fsS "$SERVER/v1/repositories/$REPO" -H "Authorization: Bearer $AUTH" >/dev/null
printf '%s\n' 'SCHEMA 5 RESTART AUTHORITY QUALIFIED'
sh "$ROOT/tests/apt-credential-isolation.sh" "$RESTORED_HUB" "$SERVER" "$PRIVATE" "$READER_TOKEN" "$PRIVATE_B" "$READER_B" "$REPO"
# The Debian client uses the real CLI, not hand-written apt configuration.
cat >"$TMP/Dockerfile" <<EOF
FROM debian:bookworm-slim
COPY restored/tpahub /usr/local/bin/tpahub
COPY restored/bootstrap-tpa /usr/local/bin/bootstrap-tpa
RUN chmod 755 /usr/local/bin/tpahub /usr/local/bin/bootstrap-tpa
RUN rm -f /etc/apt/sources.list /etc/apt/sources.list.d/tpahub-*.list
RUN tpahub --server $SERVER repo add tpa && tpahub repo add $REPO --server=$SERVER && test "\$(grep -c '^deb ' /etc/apt/sources.list.d/tpahub-$REPO.list)" = 1
RUN test -s /etc/apt/keyrings/tpahub-$REPO.gpg && grep -F 'signed-by=/etc/apt/keyrings/tpahub-$REPO.gpg' /etc/apt/sources.list.d/tpahub-$REPO.list
RUN tpahub repo add $PRIVATE --server $SERVER --token $READER_TOKEN && test -f /etc/apt/auth.conf.d/tpahub-$PRIVATE && test "\$(stat -c %a /etc/apt/auth.conf.d/tpahub-$PRIVATE)" = 600
RUN apt-get update && apt-get install -y --no-install-recommends tpa && command -v tpa && dpkg-query -W tpa && tpa schema >/dev/null
RUN apt-get install -y hub-qualification=1.0.0 && test "\$(cat /usr/share/hub-qualification/version)" = 1.0.0
RUN apt-get upgrade -y && test "\$(cat /usr/share/hub-qualification/version)" = 2.0.0
RUN apt-get install -y restored-probe=1.0.0 && test "\$(cat /usr/share/restored-probe/version)" = 1.0.0
RUN apt-get upgrade -y && test "\$(cat /usr/share/restored-probe/version)" = 2.0.0
RUN apt-get install -y --allow-downgrades hub-qualification=1.0.0 && test "\$(cat /usr/share/hub-qualification/version)" = 1.0.0
RUN apt-get install -y dep-chain && test -f /usr/share/dep-base/version && test -f /usr/share/dep-meta/version && test -f /usr/share/dep-chain/version
RUN tpahub repo remove tpa --server $SERVER && test ! -e /etc/apt/sources.list.d/tpahub-$REPO.list && test ! -e /etc/apt/keyrings/tpahub-$REPO.gpg
EOF
docker build --network=host --build-arg REPO="$REPO" -f "$TMP/Dockerfile" "$TMP"
printf '%s\n' 'REAL APT SELF-HOSTING QUALIFIED'
