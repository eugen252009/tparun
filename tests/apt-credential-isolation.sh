#!/bin/sh
# Called against an existing disposable Hub. Actual Debian APT + observed HTTP.
# Args: binary, origin, private A ID, token A, private B ID, token B, public ID.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BIN=$1; SERVER=$2; A=$3; READER_A=$4; B=$5; READER_B=$6; PUBLIC=$7
TMP=$(mktemp -d); CONTAINER="tpahub-apt-isolation-$$"
cleanup(){ docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; [ -z "${PROXY_PID:-}" ] || kill "$PROXY_PID" 2>/dev/null || true; [ -z "${PROXY_PID:-}" ] || wait "$PROXY_PID" 2>/dev/null || true; rm -rf "$TMP"; }
trap cleanup EXIT INT TERM
printf '%s\n' a-only >"$TMP/phase"
READER_A="$READER_A" READER_B="$READER_B" python3 "$ROOT/tests/testdata/apt-scope-proxy.py" "$SERVER" "$TMP" >"$TMP/proxy.log" 2>&1 & PROXY_PID=$!
i=0; while [ ! -s "$TMP/origin" ]; do i=$((i+1)); [ "$i" -lt 100 ] || exit 1; sleep .05; done
ORIGIN=$(python3 -c 'import pathlib,sys;print(pathlib.Path(sys.argv[1]).read_text())' "$TMP/origin")
docker run -d --name "$CONTAINER" --network=host \
 -e ORIGIN="$ORIGIN" -e A="$A" -e B="$B" -e PUBLIC="$PUBLIC" \
 -e READER_A="$READER_A" -e READER_B="$READER_B" debian:bookworm-slim sleep infinity >/dev/null
docker cp "$BIN" "$CONTAINER:/usr/local/bin/tparun"
docker exec "$CONTAINER" sh -eu -c '
 chmod 755 /usr/local/bin/tparun
 rm -f /etc/apt/sources.list /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources
 tparun add "$A" --server "$ORIGIN" --token "$READER_A"
 if tparun add "$B" --server "$ORIGIN" --token unavailable-reader >/tmp/add-b-wrong.log 2>&1; then
   echo "wrong B credential was accepted by tparun add" >&2
   exit 1
 fi
 test ! -e /etc/apt/auth.conf.d/tpahub-"$B"
 test ! -e /etc/apt/sources.list.d/tpahub-"$B".list
 apt-get update -o APT::Update::Error-Mode=any >/tmp/apt.log 2>&1
'
python3 - "$TMP/requests.jsonl" "$A" "$B" <<'PY'
import json,sys
rows=[json.loads(x) for x in open(sys.argv[1])]
a=[r for r in rows if r['path'].startswith('/r/'+sys.argv[2]+'/')]
b=[r for r in rows if r['path'].startswith('/r/'+sys.argv[3]+'/')]
assert any(r['a'] and r['status']==200 for r in a),a
assert any(r['auth'] and not r['a'] and not r['b'] and r['status'] in (401,403) for r in b),b
PY
printf '%s\n' both >"$TMP/phase"
docker exec "$CONTAINER" sh -eu -c '
 tparun add "$B" --server "$ORIGIN" --token "$READER_B"
 rm -rf /var/lib/apt/lists/*
 apt-get update -o APT::Update::Error-Mode=any
 cd /tmp
 apt-get download tpa private-b-probe
'
python3 - "$TMP/requests.jsonl" "$A" "$B" <<'PY'
import json,sys
rows=[json.loads(x) for x in open(sys.argv[1]) if json.loads(x)['phase']=='both']
for key,repo in [('a',sys.argv[2]),('b',sys.argv[3])]:
    observed=[r for r in rows if r['path'].startswith('/r/'+repo+'/')]
    assert observed and all(r[key] and r['status']==200 for r in observed),observed
    assert any(r['path'].endswith('.deb') for r in observed),observed
PY
printf '%s\n' missing >"$TMP/phase"
docker exec "$CONTAINER" sh -eu -c '
 rm /etc/apt/auth.conf.d/tpahub-"$B"
 if apt-get update -o APT::Update::Error-Mode=any >/tmp/apt-missing.log 2>&1; then echo "B authenticated without its auth file" >&2; exit 1; fi
 rm /etc/apt/sources.list.d/tpahub-"$B".list
'
python3 - "$TMP/requests.jsonl" "$B" <<'PY'
import json,sys
rows=[json.loads(x) for x in open(sys.argv[1]) if json.loads(x)['phase']=='missing']
b=[r for r in rows if r['path'].startswith('/r/'+sys.argv[2]+'/')]
assert b and all(not r['auth'] and r['status'] in (401,403) for r in b),b
PY
printf '%s\n' public >"$TMP/phase"
docker exec "$CONTAINER" sh -eu -c '
 tparun add "$PUBLIC" --server "$ORIGIN"
 rm -rf /var/lib/apt/lists/*
 apt-get update -o APT::Update::Error-Mode=any
'
python3 - "$TMP/requests.jsonl" "$PUBLIC" <<'PY'
import json,sys
rows=[json.loads(x) for x in open(sys.argv[1])]
public=[r for r in rows if r['path'].startswith('/r/'+sys.argv[2]+'/')]
assert public and all(not r['auth'] and r['status']==200 for r in public),public
PY
printf '%s\n' 'REAL APT PATH-SCOPED CREDENTIAL ISOLATION QUALIFIED'
