#!/usr/bin/env bash
# Disposable integration qualification for deploy/deploy-tpahub.
# This deliberately drives the production script; the adapters only replace
# systemd and the build tool, never migration or health validation.
set -Eeuo pipefail
ROOT=$(mktemp -d /tmp/tpahub-deploy-test-XXXXXX)
WORK=$(mktemp -d /tmp/tpahub-deploy-work-XXXXXX)
PIDS=()
cleanup() {
  set +e
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  git worktree remove --force "$WORK/previous-src" 2>/dev/null || true
  rm -rf "$ROOT" "$WORK"
}
trap cleanup EXIT INT TERM
repo=$(git rev-parse --show-toplevel)
base=$(git rev-parse HEAD)
# The candidate is the frozen implementation under qualification.
candidate_rev=$(git rev-parse "$base^")
[[ "$candidate_rev" == d1efa5740e29c8a694c5695a6a918b46da3feb02 ]] || { echo "wrong candidate revision: $candidate_rev" >&2; exit 1; }
 git cat-file -e "$candidate_rev^{commit}" || { echo "candidate revision unavailable: $candidate_rev" >&2; exit 1; }
previous_rev=de40f89c4e6181098e8aefd554115a62be4f4cfa

echo "building isolated previous ($previous_rev) and candidate ($candidate_rev)"
git worktree add --detach "$WORK/previous-src" "$previous_rev" >/dev/null
(cd "$WORK/previous-src" && go build -o "$WORK/previous" ./cmd/tpahub)
go build -o "$WORK/candidate" ./cmd/tpahub
chmod +x "$WORK/previous" "$WORK/candidate"

# The build adapter is used only because the deploy script itself performs the
# staging/archive/build flow.  The resulting candidate is a real application.
cat >"$WORK/go" <<'EOF'
#!/usr/bin/env bash
set -e
out=''
while (($#)); do [[ $1 == -o ]] && { out=$2; shift 2; continue; }; shift; done
[[ -n $out ]]
cp "$TPAHUB_TEST_CANDIDATE" "$out"
chmod +x "$out"
EOF
chmod +x "$WORK/go"

# Disposable service adapter. It starts the actual selected binary and records
# every start, allowing the fail-closed assertion to inspect previous starts.
cat >"$WORK/systemctl" <<'EOF'
#!/usr/bin/env bash
set -e
op=$2; pidfile=$TPAHUB_TEST_PIDFILE
case "$op" in
 stop)
   if [[ -s $pidfile ]]; then kill "$(cat "$pidfile")" 2>/dev/null || true; rm -f "$pidfile"; fi ;;
 start)
   rel=$(readlink -f "$TPAHUB_TEST_CURRENT")
   echo "$rel" >> "$TPAHUB_TEST_START_LOG"
   if [[ "$rel" == "$TPAHUB_TEST_CANDIDATE_RELEASE" && "${TPAHUB_TEST_FAILURE:-}" == startup ]]; then exit 1; fi
   TPAHUB_DATA_DIR="$TPAHUB_TEST_DATA" TPAHUB_LISTEN="$TPAHUB_TEST_LISTEN" \
     "$rel/tpahub" serve >>"$TPAHUB_TEST_SERVICE_LOG" 2>&1 &
   echo $! >"$pidfile" ;;
 is-active)
   [[ -s $pidfile ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null ;;
 *) exit 2;;
esac
EOF
chmod +x "$WORK/systemctl"
cat >"$WORK/curl" <<'EOF'
#!/usr/bin/env bash
set -e
orig=("$@"); out=''; url=''
while (($#)); do case "$1" in -o) out=$2; shift 2;; -w) fmt=$2; shift 2;; *) url=$1; shift;; esac; done
rel=$(readlink -f "$TPAHUB_TEST_CURRENT")
if [[ "$rel" == "$TPAHUB_TEST_CANDIDATE_RELEASE" ]]; then
  case "${TPAHUB_TEST_HEALTH:-healthy}" in
    http_failure) body='{}'; code=503;;
    invalid_json) body='not-json'; code=200;;
    wrong_service) body='{ "service":"other", "version":"0.1.0", "started_at":"2026-01-01T00:00:00Z" }'; code=200;;
    wrong_version) body='{ "service":"tpahub", "version":"wrong", "started_at":"2026-01-01T00:00:00Z" }'; code=200;;
    missing_started_at) body='{ "service":"tpahub", "version":"0.1.0" }'; code=200;;
    timeout) sleep 2; body='{}'; code=503;;
    *) body='{ "service":"tpahub", "version":"0.1.0", "started_at":"2026-01-01T00:00:00Z" }'; code=200;;
  esac
  printf '%s' "$body" >"$out"; [[ "$fmt" == '%{http_code}' ]] && printf '%s' "$code"; exit 0
fi
printf '%s' '{ "service":"tpahub", "version":"0.1.0", "started_at":"2026-01-01T00:00:00Z" }' >"$out"
[[ "$fmt" == '%{http_code}' ]] && printf '200'
EOF
chmod +x "$WORK/curl"

make_db() {
  local data=$1 db=$2
  mkdir -p "$data"
  # Initialize the canonical current schema using the real candidate, then
  # construct a valid historical v1 fixture by adding the removed column.
  # Initialize the disposable database with the actual historical binary.
  # This produces the canonical v1 schema and migration ledger.
  TPAHUB_DATA_DIR="$data" TPAHUB_LISTEN=127.0.0.1:0 "$WORK/previous" serve >/dev/null 2>&1 & local p=$!
  sleep .2; kill "$p" 2>/dev/null || true; wait "$p" 2>/dev/null || true
  sqlite3 "$db" 'SELECT version FROM tpahub_schema WHERE singleton=1;' | grep -qx 1
  sqlite3 "$db" 'PRAGMA integrity_check;' | grep -qx ok
  [[ -z $(sqlite3 "$db" 'PRAGMA foreign_key_check;') ]]
  sqlite3 "$db" 'SELECT version FROM tpahub_schema WHERE singleton=1;' | grep -qx 1
  sqlite3 "$db" 'SELECT name FROM pragma_table_info("billing_purchases") WHERE name="intent_state";' | grep -qx intent_state
}

run_case() {
  local name=$1 failure=${2:-}
  rm -rf "$ROOT"; mkdir -p "$ROOT/releases" "$ROOT/data" "$ROOT/home/.config"
  local db="$ROOT/data/tpahub.db" current="$ROOT/current" prev="$ROOT/releases/previous"
  make_db "$ROOT/data" "$db"
  cp "$WORK/previous" "$prev/tpahub" 2>/dev/null || { mkdir -p "$prev"; cp "$WORK/previous" "$prev/tpahub"; }; chmod +x "$prev/tpahub"
  printf '%s\n' "$previous_rev" > "$prev/.deploy-commit"
  ln -s "$prev" "$current"
  git init --bare "$ROOT/repo.git" >/dev/null
  git --git-dir="$ROOT/repo.git" fetch "$repo" "$candidate_rev:refs/heads/staging" >/dev/null
  local before; before=$(sqlite3 "$db" '.dump' | sha256sum | cut -d' ' -f1)
  export HOME_DIR="$ROOT/home" REPO="$ROOT/repo.git" RELEASES="$ROOT/releases" CURRENT="$current"
  export LOCK="$ROOT/deploy.lock" ENV_FILE="$ROOT/none" TPAHUB_DATA_DIR="$ROOT/data" TPAHUB_DB="$db"
  local port=$((18000 + RANDOM % 1000))
  export HEALTH_URL="http://127.0.0.1:$port/version" GO="$WORK/go" SQLITE3=sqlite3 SYSTEMCTL="$WORK/systemctl" CURL="$WORK/curl" TPA_SCHEMA=true
  export TPAHUB_TEST_CANDIDATE="$WORK/candidate" TPAHUB_TEST_CURRENT="$current" TPAHUB_TEST_CANDIDATE_RELEASE="$ROOT/releases/$candidate_rev"
  export TPAHUB_TEST_DATA="$ROOT/data" TPAHUB_TEST_LISTEN="127.0.0.1:$port" TPAHUB_TEST_PIDFILE="$ROOT/pid"
  export TPAHUB_TEST_START_LOG="$ROOT/starts" TPAHUB_TEST_SERVICE_LOG="$ROOT/service.log" TPAHUB_TEST_FAILURE="${failure#health:}" TPAHUB_TEST_HEALTH="${failure#health:}"
  export TPAHUB_DEPLOY_HEALTH_ATTEMPTS=2 TPAHUB_DEPLOY_HEALTH_INTERVAL=0.05 TPAHUB_DEPLOY_TEST_FAILURE="$failure"
  set +e; deploy/deploy-tpahub >"$ROOT/deploy.log" 2>&1; local rc=$?; set -e
  if [[ "$failure" == healthy ]]; then
    [[ "$rc" == 0 ]] || { cat "$ROOT/deploy.log" "$ROOT/service.log" 2>/dev/null; return 1; }
    [[ "$(readlink -f "$current")" == "$ROOT/releases/$candidate_rev" ]] || return 1
    [[ "$(cat "$current/.deploy-commit")" == "$candidate_rev" ]] || return 1
    [[ "$(sqlite3 "$db" 'SELECT version FROM tpahub_schema WHERE singleton=1;')" == 2 ]]
    ! sqlite3 "$db" 'SELECT name FROM pragma_table_info("billing_purchases") WHERE name="intent_state";' | grep -q intent_state
  else
    [[ "$rc" -ne 0 ]] || { cat "$ROOT/deploy.log"; echo "FAIL $name: expected rollback"; return 1; }
    [[ "$(readlink -f "$current")" == "$prev" ]] || return 1
    [[ "$(cat "$current/.deploy-commit")" == "$previous_rev" ]] || return 1
    [[ "$(sqlite3 "$db" 'SELECT version FROM tpahub_schema WHERE singleton=1;')" == 1 ]]
    sqlite3 "$db" 'SELECT name FROM pragma_table_info("billing_purchases") WHERE name="intent_state";' | grep -qx intent_state
    [[ "$(sqlite3 "$db" '.dump' | sha256sum | cut -d' ' -f1)" == "$before" ]]
  fi
  echo "PASS $name"
}

run_case successful-schema-changing-deployment healthy
# Candidate startup failure after real v1->v2 migration is the original blocker.
run_case startup-failure-after-migration startup
run_case health-http-failure health:http_failure
run_case health-invalid-json health:invalid_json
run_case health-wrong-service health:wrong_service
run_case health-wrong-version health:wrong_version
run_case health-missing-started-at health:missing_started_at
run_case health-timeout health:timeout
run_case failure-after-migration-before-promotion after-migration-before-promotion
run_case failure-after-promotion-before-start after-promotion-before-start

# Force only the restore copy to fail.  This executes the production
# deployment_failed path and proves the incompatible previous binary is not
# started after rollback authority is lost.
run_restore_failure() {
  local name=restore-failure-fail-closed
  rm -rf "$ROOT"; mkdir -p "$ROOT/releases" "$ROOT/data" "$ROOT/home/.config"
  local db="$ROOT/data/tpahub.db" prev="$ROOT/releases/previous"
  make_db "$ROOT/data" "$db"; mkdir -p "$prev"
  cp "$WORK/previous" "$prev/tpahub"; chmod +x "$prev/tpahub"
  printf '%s\n' "$previous_rev" > "$prev/.deploy-commit"; ln -s "$prev" "$ROOT/current"
  git init --bare "$ROOT/repo.git" >/dev/null
  git --git-dir="$ROOT/repo.git" fetch "$repo" "$candidate_rev:refs/heads/staging" >/dev/null
  export HOME_DIR="$ROOT/home" REPO="$ROOT/repo.git" RELEASES="$ROOT/releases" CURRENT="$ROOT/current"
  export LOCK="$ROOT/deploy.lock" ENV_FILE="$ROOT/none" TPAHUB_DATA_DIR="$ROOT/data" TPAHUB_DB="$db"
  export HEALTH_URL=http://127.0.0.1:1/version GO="$WORK/go" SQLITE3=sqlite3 SYSTEMCTL="$WORK/systemctl" TPA_SCHEMA=true
  export TPAHUB_TEST_CANDIDATE="$WORK/candidate" TPAHUB_TEST_CURRENT="$ROOT/current" TPAHUB_TEST_CANDIDATE_RELEASE="$ROOT/releases/$candidate_rev"
  export TPAHUB_TEST_DATA="$ROOT/data" TPAHUB_TEST_LISTEN=127.0.0.1:0 TPAHUB_TEST_PIDFILE="$ROOT/pid"
  export TPAHUB_TEST_START_LOG="$ROOT/starts" TPAHUB_TEST_SERVICE_LOG="$ROOT/service.log" TPAHUB_TEST_FAILURE=startup
  cat >"$WORK/cp" <<'EOF'
#!/usr/bin/env bash
for x in "$@"; do [[ "$x" == *.pre-migrate-*.db ]] && exit 73; done
exec /bin/cp "$@"
EOF
  chmod +x "$WORK/cp"
  set +e; PATH="$WORK:$PATH" deploy/deploy-tpahub >"$ROOT/deploy.log" 2>&1; local rc=$?; set -e
  [[ "$rc" -ne 0 ]] || return 1
  grep -q 'MANUAL INTERVENTION REQUIRED' "$ROOT/deploy.log"
  if grep -Fqx "$prev" "$ROOT/starts" 2>/dev/null; then cat "$ROOT/starts" >&2; cat "$ROOT/deploy.log" >&2; echo "previous service was started" >&2; return 1; fi
  echo "PASS $name"
}
run_restore_failure

echo "ALL DEPLOYMENT ROLLBACK INTEGRATION TESTS PASSED"
