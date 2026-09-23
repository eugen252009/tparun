#!/usr/bin/env bash
# Disposable qualification for the production deployment contract.
# It exercises the real deployment script against a disposable Schema-11
# instance; no production paths are used.
set -Eeuo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d /tmp/tparun-deploy-qualification-XXXXXX)
PIDS=()
cleanup() { set +e; [[ -s "${TMP_PID:-}" ]] && kill "$(cat "$TMP_PID")" 2>/dev/null || true; for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done; rm -rf "$TMP"; }
trap cleanup EXIT INT TERM

script="$ROOT/deploy/deploy-tpahub"
[[ -x "$script" ]] || { echo 'deployment script is not executable' >&2; exit 1; }
bash -n "$script"
for required in \
  'CURRENT_SCHEMA=11' \
  'candidate embedded revision mismatch' \
  'candidate would downgrade' \
  'requires TPARUN_ALLOW_SCHEMA_MIGRATION=true' \
  'PRAGMA integrity_check' \
  'PRAGMA foreign_key_check' \
  "v == {'ready': True" \
  'public artifact validation failed' \
  'rollback state is unreadable'; do
  grep -Fq "$required" "$script" || { echo "missing deployment guard: $required" >&2; exit 1; }
done

HOME_DIR="$TMP/home"
PROD_ROOT="$HOME_DIR/vserver-tpahub-qualification"
mkdir -p "$HOME_DIR/.config/tparun" "$PROD_ROOT/bin" "$PROD_ROOT/data" "$PROD_ROOT/gnupg" "$PROD_ROOT/releases"
chmod 700 "$PROD_ROOT/gnupg"
export HOME_DIR PROD_ROOT
export GNUPGHOME="$PROD_ROOT/gnupg"
gpg --batch --passphrase '' --quick-gen-key 'TPA.run deployment qualification <qualification@example.invalid>' rsa2048 sign 0 >/dev/null 2>&1
KEY=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr"{print $10;exit}')
export TPARUN_DATA_DIR="$PROD_ROOT/data" TPARUN_PROFILE=self-hosted
export TPARUN_LOCAL_PROVISIONING_ENABLED=true TPARUN_REGISTRATION_ENABLED=true
export TPARUN_CAPABILITY_SOURCE=local TPARUN_BILLING=none TPARUN_BILLING_PROVIDER=none
export TPARUN_DELIVERY_MODE=metrics-only TPARUN_EGRESS_ENFORCEMENT=none
export TPARUN_GPG_FINGERPRINT="$KEY" TPA_BIN=/usr/local/bin/tpa
export TPARUN_OPERATOR_RP_ID=localhost TPARUN_OPERATOR_ORIGIN=http://127.0.0.1:18082
export TPARUN_LISTEN=127.0.0.1:18082

# The old process is deliberately a different binary revision, while retaining
# the same application/schema. The deployment must replace it with HEAD.
go build -trimpath -ldflags='-s -w -X github.com/eugen252009/tpahub/internal/version.BuildRevision=1111111111111111111111111111111111111111' -o "$PROD_ROOT/bin/tparun" "$ROOT/cmd/tparun"
"$PROD_ROOT/bin/tparun" init >/dev/null

cat >"$TMP/systemctl" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
case "${2:-}" in
cat) printf 'ExecStart=%s serve\nWorkingDirectory=%s\nTPARUN_DATA_DIR=%s\nTPARUN_RELEASE_DIR=%s/releases\n' "$PROD_ROOT/bin/tparun" "$PROD_ROOT" "$PROD_ROOT/data" "$PROD_ROOT" ;;
stop) if [[ -s "$TMP_PID" ]]; then kill "$(cat "$TMP_PID")" 2>/dev/null || true; rm -f "$TMP_PID"; fi ;;
start) n=0; [[ -f "$TMP_START_COUNT" ]] && n=$(cat "$TMP_START_COUNT"); n=$((n+1)); printf '%s\n' "$n" >"$TMP_START_COUNT"; if [[ "${TPARUN_TEST_FAIL_START:-}" == 1 && "$n" == 2 ]]; then exit 1; fi; "$PROD_ROOT/bin/tparun" serve >"$TMP_LOG" 2>&1 & echo $! >"$TMP_PID" ;;
is-active) [[ -s "$TMP_PID" ]] && kill -0 "$(cat "$TMP_PID")" 2>/dev/null ;;
*) exit 2 ;;
esac
EOF
chmod +x "$TMP/systemctl"
export TMP_PID="$TMP/service.pid" TMP_LOG="$TMP/service.log" TMP_START_COUNT="$TMP/start-count" SYSTEMCTL="$TMP/systemctl"
"$TMP/systemctl" --user start tparun.service
PIDS+=("$(cat "$TMP_PID")")
for _ in $(seq 1 100); do curl -fsS http://127.0.0.1:18082/version >/dev/null 2>&1 && break; sleep .1; done
curl -fsS http://127.0.0.1:18082/ready | grep -Fq '"schema":11'

# The public-artifact verifier gets a disposable, honest public surface.
mkdir -p "$TMP/public/r/eugen/demo/dists/stable/main/binary-amd64" "$TMP/public/r/eugen/demo/pool"
printf 'Package: demo\nVersion: 1.0\nFilename: pool/demo.deb\n' >"$TMP/public/r/eugen/demo/dists/stable/main/binary-amd64/Packages"
printf 'qualified\n' >"$TMP/public/r/eugen/demo/dists/stable/InRelease"
printf 'artifact\n' >"$TMP/public/r/eugen/demo/pool/demo.deb"
cat >"$TMP/public.py" <<'PY'
import http.server, json, os
root=os.environ['PUBLIC_ROOT']
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        path=self.path.split('?',1)[0]
        if path == '/v1/public/repositories':
            body=json.dumps([{'username':'eugen','name':'demo'}]).encode()
            self.send_response(200); self.send_header('Content-Length',str(len(body))); self.end_headers(); self.wfile.write(body); return
        if path in ('/account','/ops'):
            self.send_response(303); self.send_header('Location','/login'); self.end_headers(); return
        if path in ('/tparun-bootstrap.deb','/r/tparun/bootstrap'):
            self.send_response(404); self.end_headers(); return
        if path == '/tparun.sources' or path in ('/','/pricing','/docs','/how-to','/nano','/login','/register'):
            self.send_response(200); self.end_headers(); self.wfile.write(b'ok'); return
        file=os.path.join(root, path.lstrip('/'))
        if os.path.isfile(file):
            body=open(file,'rb').read(); self.send_response(200); self.send_header('Content-Length',str(len(body))); self.end_headers(); self.wfile.write(body); return
        self.send_response(404); self.end_headers()
    def log_message(self, *args): pass
http.server.ThreadingHTTPServer(('127.0.0.1',18083), Handler).serve_forever()
PY
PUBLIC_ROOT="$TMP/public" python3 "$TMP/public.py" & PIDS+=("$!")

# Use the exact checked-in source revision as the receive repository.
git init --bare "$TMP/repo.git" >/dev/null
git --git-dir="$TMP/repo.git" fetch "$ROOT" "HEAD:refs/heads/staging" >/dev/null

set +e
output=$(HOME_DIR="$HOME_DIR" PROD_ROOT="$PROD_ROOT" REPO="$TMP/repo.git" \
  PUBLIC_ORIGIN=http://127.0.0.1:18083 HEALTH_ORIGIN=http://127.0.0.1:18082 \
  GO="$(command -v go)" SQLITE3="$(command -v sqlite3 || echo /home/eugen/Android/Sdk/platform-tools/sqlite3)" \
  SYSTEMCTL="$TMP/systemctl" bash "$script" 2>&1)
status=$?
set -e
if [[ $status -ne 0 ]]; then printf '%s\n' "$output"; cat "$TMP/service.log" >&2 || true; exit $status; fi
printf '%s\n' "$output"
grep -Fq 'schema_before=11 schema_after=11 migration=NO' <<<"$output"
[[ "$("$PROD_ROOT/bin/tparun" diagnostics | python3 -c 'import json,sys; print(json.load(sys.stdin)["revision"])')" == "$(git --git-dir="$TMP/repo.git" rev-parse refs/heads/staging)" ]]
curl -fsS http://127.0.0.1:18082/ready | grep -Fq '"schema":11'
curl -fsS http://127.0.0.1:18082/version | grep -Fq '"schema":"11"'
# Force a disposable candidate-start failure. The script must restore the
# prior binary/data snapshot and bring the previous service back.
printf '1\n' > "$TMP_START_COUNT"
set +e
rollback_output=$(TPARUN_TEST_FAIL_START=1 LOCK="$TMP/rollback.lock" HOME_DIR="$HOME_DIR" PROD_ROOT="$PROD_ROOT" REPO="$TMP/repo.git" \
  PUBLIC_ORIGIN=http://127.0.0.1:18083 HEALTH_ORIGIN=http://127.0.0.1:18082 \
  GO="$(command -v go)" SQLITE3="$(command -v sqlite3 || echo /home/eugen/Android/Sdk/platform-tools/sqlite3)" \
  SYSTEMCTL="$TMP/systemctl" bash "$script" 2>&1)
rollback_status=$?
set -e
[[ "$rollback_status" -ne 0 ]] || { printf '%s\n' "$rollback_output"; echo 'startup failure was not rejected' >&2; exit 1; }
grep -Fq 'rollback restored' <<<"$rollback_output"
curl -fsS http://127.0.0.1:18082/ready | grep -Fq '"schema":11'
printf '%s\n' "$rollback_output"
printf 'DEPLOYMENT SCHEMA-11 QUALIFICATION: PASS\n'
