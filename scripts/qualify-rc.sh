#!/usr/bin/env bash
set -Eeuo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
CANDIDATE=${1:-}
if [[ -z "$CANDIDATE" || ! -f "$CANDIDATE" || "${CANDIDATE##*.}" != deb ]]; then
  echo "usage: $0 <candidate.deb>" >&2
  exit 2
fi
CANDIDATE=$(readlink -f "$CANDIDATE")
command -v docker >/dev/null || { echo 'docker is required' >&2; exit 2; }
command -v dpkg-deb >/dev/null || { echo 'dpkg-deb is required' >&2; exit 2; }
command -v sha256sum >/dev/null || { echo 'sha256sum is required' >&2; exit 2; }

RUN_ID="$(date -u +%Y%m%d%H%M%S)-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
OUT="$ROOT/qualification/$RUN_ID"
mkdir -p "$OUT"
cp -- "$CANDIDATE" "$OUT/candidate.deb"
SIZE=$(stat -c '%s' "$CANDIDATE")
SHA256=$(sha256sum "$CANDIDATE" | awk '{print $1}')
PKG_NAME=$(dpkg-deb -f "$CANDIDATE" Package)
PKG_VERSION=$(dpkg-deb -f "$CANDIDATE" Version)
PKG_ARCH=$(dpkg-deb -f "$CANDIDATE" Architecture)
cat >"$OUT/candidate-metadata.txt" <<EOF
path=$CANDIDATE
size=$SIZE
sha256=$SHA256
package=$PKG_NAME
version=$PKG_VERSION
architecture=$PKG_ARCH
EOF

TPA_BIN=${TPA_BIN:-}
TPA_BUILD_DIR=$(mktemp -d)
cleanup_host() { rm -rf "$TPA_BUILD_DIR"; }
trap cleanup_host EXIT
if [[ -z "$TPA_BIN" ]]; then
  TPA_DIR=${TPA_DIR:-"$ROOT/../tpa"}
  [[ -f "$TPA_DIR/go.mod" ]] || { echo 'set TPA_BIN or provide the sibling TPA checkout' >&2; exit 2; }
  (cd "$TPA_DIR" && go build -trimpath -o "$TPA_BUILD_DIR/tpa" .)
  TPA_BIN="$TPA_BUILD_DIR/tpa"
fi
TPA_BIN=$(readlink -f "$TPA_BIN")
[[ -x "$TPA_BIN" ]] || { echo "TPA_BIN is not executable: $TPA_BIN" >&2; exit 2; }

# The container receives only the candidate, the external TPA dependency, and
# the run output directory. TPA Hub itself is never rebuilt in the container.
docker pull debian:bookworm-slim >/dev/null
set +e
docker run --rm -i --privileged --name "tpahub-rc-$RUN_ID" \
  -v "$CANDIDATE:/candidate.deb:ro" \
  -v "$TPA_BIN:/tpa:ro" \
  -v "$OUT:/out" \
  debian:bookworm-slim bash -s -- "$PKG_VERSION" "$PKG_NAME" "$PKG_ARCH" "$RUN_ID" "$SHA256" "$SIZE" >"$OUT/qualification.log" 2>&1 <<'CONTAINER_SCRIPT'
set -Eeuo pipefail
PKG_VERSION=$1; PKG_NAME=$2; PKG_ARCH=$3; RUN_ID=$4; SHA256=$5; SIZE=$6
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq ca-certificates curl dpkg-dev gnupg python3 >/dev/null

declare -A TESTS=()
CURRENT=setup
PASS=0
fail() { echo "RC QUALIFICATION: FAILED PHASE=$CURRENT: $*" >&2; exit 1; }
write_result() { python3 - "$@" <<'PY'
import json,sys
out,result,run_id,name,version,arch,sha,size,tests=sys.argv[1:]
data={"result":result,"run_id":run_id,"artifact":{"path":"/candidate.deb","sha256":sha,"package":name,"version":version,"architecture":arch,"size":int(size)},"tests":json.loads(tests),"limitations":["customer lifecycle email delivery: NOT IN CURRENT RC GATE SCOPE"]}
with open(out,"w") as f: json.dump(data,f,indent=2,sort_keys=True); f.write("\n")
PY
}
record() { TESTS["$1"]=$2; printf '%-24s %s\n' "$1" "$2" | tee -a /out/test-markers.log; }
collect_tests() { python3 - <<'PY'
import json,os
out={}
try:
 for line in open('/out/test-markers.log'):
  parts=line.split()
  if len(parts)>=2: out[parts[0]] = parts[-1]
except FileNotFoundError: pass
print(json.dumps(out,sort_keys=True))
PY
}
on_exit() { rc=$?; if [[ -n "${SERVER_PID:-}" ]]; then kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; fi; write_result /out/rc-result.json FAIL "$RUN_ID" "$PKG_NAME" "$PKG_VERSION" "$PKG_ARCH" "$SHA256" "$SIZE" "$(collect_tests)"; exit "$rc"; }
trap on_exit EXIT
CURRENT=install
# Install and verify the exact candidate artifact.
dpkg -i /candidate.deb >/out/install.log 2>&1 || fail "candidate installation failed"
record install PASS
command -v tpahub >/out/candidate-path.txt
readlink -f "$(command -v tpahub)" >>/out/candidate-path.txt
[[ "$(readlink -f "$(command -v tpahub)")" == /usr/bin/tpahub ]] || fail "candidate binary was shadowed"
tpahub --help >/out/help.log 2>&1 || fail "candidate help failed"
APP_VERSION=$(tpahub version)
EXPECTED_APP_VERSION=${PKG_VERSION%-*}
[[ "$APP_VERSION" == "$EXPECTED_APP_VERSION" ]] || fail "version mismatch: $APP_VERSION != $EXPECTED_APP_VERSION"
tpahub version >/out/version.txt
record version PASS

CURRENT=server
export GNUPGHOME=/tmp/gnupg
mkdir -m 700 "$GNUPGHOME"
gpg --batch --passphrase '' --quick-gen-key 'TPA Hub RC qualification <qualification@example.invalid>' rsa2048 sign 0 >/dev/null 2>&1
GPG_KEY=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr"{print $10;exit}')
export TPAHUB_DATA_DIR=/tmp/tpahub-data TPAHUB_LISTEN=127.0.0.1:18080 TPAHUB_REGISTRATION_ENABLED=true
export TPAHUB_LOCAL_PROVISIONING_ENABLED=true TPAHUB_CAPABILITY_SOURCE=legacy-local TPAHUB_BILLING=none
export TPAHUB_DELIVERY_MODE=strict-accounted TPAHUB_EGRESS_ENFORCEMENT=strict-accounted TPA_BIN=/tpa TPAHUB_GPG_FINGERPRINT=$GPG_KEY
mkdir -p "$TPAHUB_DATA_DIR"
tpahub init >/out/init.log 2>&1 || fail "server initialization failed"
[[ "$(tpahub schema)" == 8 ]] || fail "schema is not 8"
tpahub serve >/out/server.log 2>&1 & SERVER_PID=$!
cleanup_server() { kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; }
cleanup_server() { kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; }
trap on_exit EXIT
for _ in $(seq 1 100); do curl -fsS http://127.0.0.1:18080/ready >/out/ready.json 2>/dev/null && break; sleep .1; done
curl -fsS http://127.0.0.1:18080/ready >/out/ready.json || fail "server not ready"
python3 - <<'PY'
import json
x=json.load(open('/out/ready.json'))
assert x.get('ready') is True and x.get('schema')==8
PY
python3 - <<'PY'
import sqlite3
with sqlite3.connect('/tmp/tpahub-data/tpahub.db') as d:
 assert d.execute('pragma integrity_check').fetchone()[0]=='ok'
 assert list(d.execute('pragma foreign_key_check'))==[]
PY
record server_init PASS

CURRENT=happy-path
export HOME=/tmp/rc-home XDG_CONFIG_HOME=/tmp/rc-config TPAHUB_SERVER=http://127.0.0.1:18080
export TPAHUB_EMAIL="rc-${RUN_ID}@example.invalid" TPAHUB_PASSWORD='qualification-password-123'
tpahub register --server "$TPAHUB_SERVER" >/out/register.log 2>&1 || fail "registration failed"
REPO_URL=$(tpahub create "rc-$RUN_ID" --server "$TPAHUB_SERVER") || fail "repository creation failed"
REPO_ID=$(python3 - "$REPO_URL" <<'PY'
import json,os,sys,urllib.request
origin=os.environ['TPAHUB_SERVER']; token=json.load(open('/tmp/rc-config/tpahub/config.json'))['credentials'][origin]['token']
req=urllib.request.Request(origin+'/v1/repositories',headers={'Authorization':'Bearer '+token})
with urllib.request.urlopen(req) as r: rows=json.load(r)
print(next(x['id'] for x in rows if x['name'].startswith('rc-')))
PY
)
TOKEN=$(python3 - <<'PY'
import json,os
print(json.load(open('/tmp/rc-config/tpahub/config.json'))['credentials'][os.environ['TPAHUB_SERVER']]['token'])
PY
)
curl -fsS -X PATCH "$TPAHUB_SERVER/v1/repositories/$REPO_ID" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"visibility":"public"}' >/out/repository-public.log || fail "repository visibility failed"
tpahub show "$REPO_URL" --server "$TPAHUB_SERVER" >/out/repository-show.log || fail "repository inspection failed"
tpahub add "$REPO_ID" --server "$TPAHUB_SERVER" >/out/repository-add.log 2>&1 || fail "repository apt registration failed"
record authentication PASS; record repository_create PASS

make_probe() { local version=$1 output=$2; local file=/tmp/qualification-probe-$version; rm -rf "$file"; mkdir -p "$file/DEBIAN" "$file/usr/bin"; printf 'Package: qualification-probe\nVersion: %s\nArchitecture: amd64\nMaintainer: qualification <qualification@example.invalid>\nDescription: deterministic qualification probe\n' "$version" >"$file/DEBIAN/control"; printf '#!/bin/sh\nprintf "%s\\n"\n' "$output" >"$file/usr/bin/qualification-probe"; chmod 0755 "$file/usr/bin/qualification-probe"; dpkg-deb --build --root-owner-group "$file" "/tmp/qualification-probe-$version.deb" >/dev/null; }
make_probe 1.0.0 QUALIFICATION_V1
make_probe 2.0.0 QUALIFICATION_V2
tpahub stage /tmp/qualification-probe-1.0.0.deb >/out/stage-v1.log 2>&1 || fail "stage v1 failed"
tpahub publish "$REPO_URL" --yes --server "$TPAHUB_SERVER" >/out/publish-v1.log 2>&1 || fail "publish v1 failed"
record publish_v1 PASS
find "/tmp/tpahub-data/repos/$REPO_ID" -maxdepth 5 -type f -print >/out/repo-files-v1.txt || true
curl -i "$REPO_URL/dists/stable/main/binary-amd64/Packages" >/out/packages-v1-http.txt 2>&1 || fail "v1 metadata unavailable"
cp /out/packages-v1-http.txt /out/packages-v1.txt
grep -q '^Package: qualification-probe$' /out/packages-v1.txt || fail "v1 package missing"
apt-get update -qq >/out/apt-v1.log 2>&1 || fail "apt update v1 failed"
apt-get install -y -qq qualification-probe=1.0.0 >/out/install-v1.log 2>&1 || fail "install v1 failed"
[[ "$(qualification-probe)" == QUALIFICATION_V1 ]] || fail "v1 execution mismatch"
record install_v1 PASS

tpahub stage /tmp/qualification-probe-2.0.0.deb >/out/stage-v2.log 2>&1 || fail "stage v2 failed"
tpahub publish "$REPO_URL" --yes --server "$TPAHUB_SERVER" >/out/publish-v2.log 2>&1 || fail "publish v2 failed"
record publish_v2 PASS
apt-get update -qq >/out/apt-v2.log 2>&1 || fail "apt update v2 failed"
apt-get install -y -qq --allow-downgrades qualification-probe=2.0.0 >/out/upgrade-v2.log 2>&1 || fail "upgrade v2 failed"
[[ "$(qualification-probe)" == QUALIFICATION_V2 ]] || fail "v2 execution mismatch"
apt-get install -y -qq --allow-downgrades qualification-probe=1.0.0 >/out/select-v1.log 2>&1 || fail "explicit v1 selection failed"
[[ "$(qualification-probe)" == QUALIFICATION_V1 ]] || fail "explicit v1 output mismatch"
apt-get install -y -qq qualification-probe=2.0.0 >/out/select-v2.log 2>&1 || fail "explicit v2 selection failed"
[[ "$(qualification-probe)" == QUALIFICATION_V2 ]] || fail "explicit v2 output mismatch"
apt-get remove -y -qq qualification-probe >/out/remove.log 2>&1 || fail "remove failed"
[[ ! -e /usr/bin/qualification-probe ]] || fail "remove left executable"
apt-get install -y -qq qualification-probe=2.0.0 >/out/reinstall.log 2>&1 || fail "reinstall failed"
[[ "$(qualification-probe)" == QUALIFICATION_V2 ]] || fail "reinstall output mismatch"
record upgrade_v2 PASS; record explicit_versions PASS; record remove_reinstall PASS

CURRENT=ttl-min
# Create the fixed public ttl=min profile through the real API.
export MIN_CONFIG=/tmp/min-config MIN_EMAIL="min-$RUN_ID@example.invalid" MIN_PASSWORD='min-profile-password-123'
mkdir -p "$MIN_CONFIG/tpahub"
python3 - <<'PY'
import json,os,urllib.request
origin=os.environ['TPAHUB_SERVER']; body=json.dumps({'email':os.environ['MIN_EMAIL'],'password':os.environ['MIN_PASSWORD'],'ttl':'min'}).encode()
req=urllib.request.Request(origin+'/v1/accounts',data=body,headers={'Content-Type':'application/json'})
with urllib.request.urlopen(req) as r: out=json.load(r)
state={'version':2,'server':origin,'credentials':{origin:{'email':out['email'],'token':out['token']}}}
json.dump(state,open('/tmp/min-config/tpahub/config.json','w'),indent=2)
print(out['account_id'])
PY
MIN_ID=$(python3 - <<'PY'
import sqlite3,os
with sqlite3.connect('/tmp/tpahub-data/tpahub.db') as d: print(d.execute('select id from accounts where email=?',(os.environ['MIN_EMAIL'],)).fetchone()[0])
PY
)
python3 - "$MIN_ID" <<'PY'
import sqlite3,sys,datetime
with sqlite3.connect('/tmp/tpahub-data/tpahub.db') as d:
 row=d.execute('select lifecycle_kind,membership_expires_at,purge_after from accounts where id=?',(sys.argv[1],)).fetchone()
 assert row[0]=='ephemeral'
 a=datetime.datetime.fromisoformat(row[1].replace('Z','+00:00')); b=datetime.datetime.fromisoformat(row[2].replace('Z','+00:00'))
 assert (b-a).total_seconds()==300
 rows=d.execute('select capability_id,storage_limit_bytes,egress_limit_bytes from product_grants where customer_id=? order by capability_id',(sys.argv[1],)).fetchall()
 assert rows==[('repository.create',0,0),('repository.download',0,20480),('repository.publish',10240,0),('repository.read.private',0,0)]
PY
record ttl_min_profile PASS

export XDG_CONFIG_HOME=$MIN_CONFIG TPAHUB_EMAIL=$MIN_EMAIL TPAHUB_PASSWORD=$MIN_PASSWORD
MIN_REPO_URL=$(tpahub create "min-$RUN_ID" --server "$TPAHUB_SERVER") || fail "min repository creation failed"
MIN_REPO_ID=$(python3 - "$MIN_REPO_URL" <<'PY'
import json,os,urllib.request
origin=os.environ['TPAHUB_SERVER']; token=json.load(open('/tmp/min-config/tpahub/config.json'))['credentials'][origin]['token']
req=urllib.request.Request(origin+'/v1/repositories',headers={'Authorization':'Bearer '+token})
with urllib.request.urlopen(req) as r: rows=json.load(r)
print(next(x['id'] for x in rows if x['name'].startswith('min-')))
PY
)
MIN_TOKEN=$(python3 - <<'PY'
import json,os
print(json.load(open('/tmp/min-config/tpahub/config.json'))['credentials'][os.environ['TPAHUB_SERVER']]['token'])
PY
)
curl -fsS -X PATCH "$TPAHUB_SERVER/v1/repositories/$MIN_REPO_ID" -H "Authorization: Bearer $MIN_TOKEN" -H 'Content-Type: application/json' -d '{"visibility":"public"}' >/out/min-public.log || fail "min repository visibility failed"
make_quota() { local name=$1 size=$2; local file=/tmp/$name; rm -rf "$file"; mkdir -p "$file/DEBIAN" "$file/usr/share/$name"; printf 'Package: %s\nVersion: 1.0.0\nArchitecture: amd64\nMaintainer: qualification <qualification@example.invalid>\nDescription: quota probe\n' "$name" >"$file/DEBIAN/control"; python3 - "$file/usr/share/$name/payload" "$size" <<'PY'
import hashlib,sys
path,size=sys.argv[1],int(sys.argv[2]); data=b''; i=0
while len(data)<size: data+=hashlib.sha256(f'qualification-{i}'.encode()).digest(); i+=1
open(path,'wb').write(data[:size])
PY
 dpkg-deb --build --root-owner-group "$file" "/tmp/$name.deb" >/dev/null; }
make_quota quota-first 7000
make_quota quota-second 7000
(tpahub stage /tmp/quota-first.deb >/out/quota-stage1.log 2>&1 && tpahub publish "$MIN_REPO_URL" --yes --server "$TPAHUB_SERVER" >/out/quota-publish1.log 2>&1) || fail "min first publication failed"
if tpahub stage /tmp/quota-second.deb >/out/quota-stage2.log 2>&1 && tpahub publish "$MIN_REPO_URL" --yes --server "$TPAHUB_SERVER" >/out/quota-publish2.log 2>&1; then fail "min storage overflow unexpectedly succeeded"; fi
curl -fsS "$MIN_REPO_URL/dists/stable/InRelease" >/out/min-inrelease || fail "min first publication was lost"
record storage_quota PASS

PKG_PATH=$(curl -fsS "$MIN_REPO_URL/dists/stable/main/binary-amd64/Packages" | awk '/^Filename:/{print $2;exit}')
for _ in $(seq 1 40); do curl -fsS -o /dev/null "$MIN_REPO_URL/$PKG_PATH" || fail "egress download failed"; done
MIN_EGRESS=$(python3 - "$MIN_ID" <<'PY'
import sqlite3,sys
with sqlite3.connect('/tmp/tpahub-data/tpahub.db') as d: print(d.execute('select coalesce(sum(egress_bytes),0) from repository_egress_daily u join repositories r on r.id=u.repository_id where r.account_id=?',(sys.argv[1],)).fetchone()[0])
PY
)
[[ "$MIN_EGRESS" -gt 20480 ]] || fail "egress accounting did not exceed min allowance: $MIN_EGRESS"
record egress PASS

# The min account has a five-minute authority lifetime and a ten-minute purge deadline.
for _ in $(seq 1 80); do
  if ! curl -fsS -o /dev/null -H "Authorization: Bearer $MIN_TOKEN" "$TPAHUB_SERVER/v1/account"; then break; fi
  sleep 5
done
if curl -fsS -o /dev/null -H "Authorization: Bearer $MIN_TOKEN" "$TPAHUB_SERVER/v1/account"; then fail "expired min session remained authorized"; fi
record ttl_expiry PASS
for _ in $(seq 1 180); do
  count=$(python3 - "$MIN_ID" <<'PY'
import sqlite3,sys
with sqlite3.connect('/tmp/tpahub-data/tpahub.db') as d: print(d.execute('select count(*) from accounts where id=?',(sys.argv[1],)).fetchone()[0])
PY
)
  [[ "$count" == 0 ]] && break
  sleep 5
done
[[ "$count" == 0 ]] || fail "min account was not purged"
for table in product_grants sessions account_ssh_credentials repositories; do
  n=$(python3 - "$MIN_ID" "$table" <<'PY'
import sqlite3,sys
column='customer_id' if sys.argv[2]=='product_grants' else ('account_id' if sys.argv[2] != 'repositories' else 'account_id')
with sqlite3.connect('/tmp/tpahub-data/tpahub.db') as d: print(d.execute('select count(*) from '+sys.argv[2]+' where '+column+'=?',(sys.argv[1],)).fetchone()[0])
PY
)
  [[ "$n" == 0 ]] || fail "purge residue in $table"
done
[[ ! -e "/tmp/tpahub-data/repos/$MIN_REPO_ID" && ! -e "/tmp/tpahub-data/artifacts/$MIN_REPO_ID" ]] || fail "purge filesystem residue"
record ttl_purge PASS; record zero_residue PASS

CURRENT=authorization
# Bounded authorization regressions.
[[ "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$TPAHUB_SERVER/ops")" != 200 ]] || fail "ordinary account reached ops"
record authorization_negative PASS
record migration N/A
record ssh_machine_auth N/A
record customer_email_delivery N/A

# Persist the complete result before the successful trap exits.
TEST_JSON=$(collect_tests)
write_result /out/rc-result.json PASS "$RUN_ID" "$PKG_NAME" "$PKG_VERSION" "$PKG_ARCH" "$SHA256" "$SIZE" "$TEST_JSON"
trap - EXIT
cleanup_server
printf '\nRC QUALIFICATION: PASS\nartifact: %s\nsha256: %s\nversion: %s\n' "$PKG_NAME" "$SHA256" "$PKG_VERSION"
CONTAINER_SCRIPT
RC=$?
set -e
if [[ $RC -ne 0 ]]; then
  echo "TPA HUB RC RELEASE GATE FAILED"
  echo "logs: $OUT"
  exit $RC
fi
cat "$OUT/rc-result.json"
echo "TPA HUB RC RELEASE GATE QUALIFIED"
echo "ARTIFACT SHA256: $SHA256"
