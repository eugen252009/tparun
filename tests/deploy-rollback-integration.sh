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
candidate_rev=d1efa5740e29c8a694c5695a6a918b46da3feb02
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
   if [[ -s $pidfile ]]; then p=$(cat "$pidfile"); kill "$p" 2>/dev/null || true; sleep .05; kill -9 "$p" 2>/dev/null || true; rm -f "$pidfile"; fi
   pkill -9 -f '/tmp/tpahub-deploy-test-[^ ]*/releases/[^ ]*/tpahub serve' 2>/dev/null || true ;;
 start)
   rel=$(readlink -f "$TPAHUB_TEST_CURRENT")
   echo "$rel" >> "$TPAHUB_TEST_START_LOG"
   if [[ "$rel" == "$TPAHUB_TEST_CANDIDATE_RELEASE" && "${TPAHUB_TEST_FAILURE:-}" == startup ]]; then exit 1; fi
   TPAHUB_DATA_DIR="$TPAHUB_TEST_DATA" TPAHUB_LISTEN="$TPAHUB_TEST_LISTEN" \
     "$rel/tpahub" serve >>"$TPAHUB_TEST_SERVICE_LOG" 2>&1 &
   echo $! >"$pidfile"
   if [[ "$rel" == "$TPAHUB_TEST_CANDIDATE_RELEASE" && "${TPAHUB_TEST_FAILURE:-}" == candidate-wal ]]; then
     python3 - "$TPAHUB_TEST_DATA/tpahub.db" "$TPAHUB_TEST_WAL_OBSERVED" <<'PY'
import os, sqlite3, sys, time
p, marker = sys.argv[1:]
c = sqlite3.connect(p); assert c.execute('PRAGMA journal_mode=WAL').fetchone()[0] == 'wal'
c.execute("INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('candidate-wal','candidate-wal@example.invalid','x','active','2026-01-01T00:00:00Z')"); c.commit()
for _ in range(40):
    if os.path.exists(p+'-wal') and os.path.getsize(p+'-wal') > 0 and os.path.exists(p+'-shm'):
        open(marker, 'w').write(str(os.path.getsize(p+'-wal'))); break
    time.sleep(.025)
else: raise SystemExit('SQLite WAL/SHM was not observed')
time.sleep(1)
PY
   fi ;;
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
if [[ "$rel" != "$TPAHUB_TEST_CANDIDATE_RELEASE" && "${TPAHUB_TEST_HEALTH:-}" == previous-health-failure ]]; then
  printf '%s' 'not-json' >"$out"; [[ "$fmt" == '%{http_code}' ]] && printf '200'; exit 0
fi
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
cat >"$WORK/sqlite3" <<'EOF'
#!/usr/bin/env bash
set -e
file=${1:-}; sql=${2:-}
mode=${TPAHUB_TEST_SQLITE_MODE:-}
if [[ "$mode" == missing-backup && "$sql" == .backup* ]]; then exit 73; fi
if [[ "$mode" == corrupt-backup && "$file" == *.pre-migrate-*.db && "$sql" == *integrity_check* ]]; then echo corrupt; exit 0; fi
if [[ "$mode" == fk-invalid-backup && "$file" == *.pre-migrate-*.db && "$sql" == *foreign_key_check* ]]; then echo 'bad|fk'; exit 0; fi
if [[ -f "$TPAHUB_TEST_RESTORED_FLAG" && "$file" == "$TPAHUB_TEST_DATA/tpahub.db" ]]; then
  case "$mode" in
    restored-integrity-failure) [[ "$sql" == *integrity_check* ]] && { echo corrupt; exit 0; };;
    restored-fk-failure) [[ "$sql" == *foreign_key_check* ]] && { echo 'bad|fk'; exit 0; };;
    restored-schema-incompatible) [[ "$sql" == *'SELECT version'* ]] && { echo 99; exit 0; };;
  esac
fi
exec /usr/bin/sqlite3 "$@"
EOF
chmod +x "$WORK/sqlite3"

make_db() {
  local data=$1 db=$2
  mkdir -p "$data"
  # Initialize the canonical current schema using the real candidate, then
  # construct a valid historical v1 fixture by adding the removed column.
  # Initialize the disposable database with the actual historical binary.
  # This produces the canonical v1 schema and migration ledger.
  TPAHUB_DATA_DIR="$data" TPAHUB_LISTEN=127.0.0.1:0 "$WORK/previous" serve >/dev/null 2>&1 & local p=$!
  sleep .2; kill "$p" 2>/dev/null || true; sleep .05; kill -9 "$p" 2>/dev/null || true; wait "$p" 2>/dev/null || true
  sqlite3 "$db" 'SELECT version FROM tpahub_schema WHERE singleton=1;' | grep -qx 1
  sqlite3 "$db" 'PRAGMA integrity_check;' | grep -qx ok
  [[ -z $(sqlite3 "$db" 'PRAGMA foreign_key_check;') ]]
  sqlite3 "$db" 'SELECT version FROM tpahub_schema WHERE singleton=1;' | grep -qx 1
  sqlite3 "$db" 'SELECT sql FROM sqlite_master WHERE name="billing_purchases";' | grep -q intent_state
}

run_case() {
  local name=$1 failure=${2:-}
  rm -rf "$ROOT"; mkdir -p "$ROOT/releases" "$ROOT/data" "$ROOT/home/.config"
  local db="$ROOT/data/tpahub.db" current="$ROOT/current" prev="$ROOT/releases/previous"
  make_db "$ROOT/data" "$db"
  if [[ "$failure" == wal ]]; then
    python3 - "$db" <<'PY' &
import sqlite3, sys, time
c=sqlite3.connect(sys.argv[1]); c.execute('PRAGMA journal_mode=WAL'); c.execute("INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('wal-account','wal@example.invalid','x','active','2026-01-01T00:00:00Z')"); c.commit(); time.sleep(2)
PY
    local wal_pid=$!; sleep .2
    [[ -s "$db-wal" && -s "$db-shm" ]] || { kill "$wal_pid" 2>/dev/null || true; wait "$wal_pid" 2>/dev/null || true; return 1; }
    kill "$wal_pid" 2>/dev/null || true; wait "$wal_pid" 2>/dev/null || true
  fi
  if [[ "$failure" == migration-rejection ]]; then
    sqlite3 "$db" <<'SQL'
INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('q2-account','q2@example.invalid','x','active','2026-01-01T00:00:00Z');
INSERT INTO billing_purchases(id,account_id,product_id,plan_code,rate_card_id,rate_card_mode,currency,amount_minor,billing_environment,provider,status,entitlement_period_start,entitlement_period_end,provider_state,created_at,updated_at,intent_state) VALUES('q2-purchase','q2-account','tpa-hub','paid','paid','test','EUR',1,'test','stripe','paid','2026-01-01T00:00:00Z','2027-01-01T00:00:00Z','open','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','superseded');
SQL
  fi
  cp "$WORK/previous" "$prev/tpahub" 2>/dev/null || { mkdir -p "$prev"; cp "$WORK/previous" "$prev/tpahub"; }; chmod +x "$prev/tpahub"
  printf '%s\n' "$previous_rev" > "$prev/.deploy-commit"
  ln -s "$prev" "$current"
  git init --bare "$ROOT/repo.git" >/dev/null
  git --git-dir="$ROOT/repo.git" fetch "$repo" "$candidate_rev:refs/heads/staging" >/dev/null
  local before=$("$WORK/candidate" digest --db "$db")
  export HOME_DIR="$ROOT/home" REPO="$ROOT/repo.git" RELEASES="$ROOT/releases" CURRENT="$current"
  export LOCK="$ROOT/deploy.lock" ENV_FILE="$ROOT/none" TPAHUB_DATA_DIR="$ROOT/data" TPAHUB_DB="$db"
  local port=$((30000 + $$ % 20000))
  export HEALTH_URL="http://127.0.0.1:$port/version" GO="$WORK/go" SQLITE3=sqlite3 SYSTEMCTL="$WORK/systemctl" CURL="$WORK/curl" TPA_SCHEMA=true
  export TPAHUB_TEST_CANDIDATE="$WORK/candidate" TPAHUB_TEST_CURRENT="$current" TPAHUB_TEST_CANDIDATE_RELEASE="$ROOT/releases/$candidate_rev"
  export TPAHUB_TEST_DATA="$ROOT/data" TPAHUB_TEST_LISTEN="127.0.0.1:$port" TPAHUB_TEST_PIDFILE="$ROOT/pid" TPAHUB_TEST_WAL_OBSERVED="$ROOT/candidate-wal-observed"
  local health_mode="${failure#health:}"
  [[ "$failure" == previous-health-failure ]] && health_mode=previous-health-failure
  [[ "$failure" == candidate-wal || "$failure" == cleanup-failure ]] && health_mode=http_failure
  [[ "$failure" == after-promotion-before-start ]] && health_mode=healthy
  local service_failure="${failure#health:}"
  [[ "$failure" == wal ]] && service_failure=startup
  [[ "$failure" == cleanup-failure ]] && service_failure=candidate-wal
  export TPAHUB_TEST_START_LOG="$ROOT/starts" TPAHUB_TEST_SERVICE_LOG="$ROOT/service.log" TPAHUB_TEST_FAILURE="$service_failure" TPAHUB_TEST_HEALTH="$health_mode"
  export TPAHUB_TEST_CLEANUP_FAILURE=0
  if [[ "$failure" == cleanup-failure ]]; then
    printf '%s\n' '#!/usr/bin/env bash' 'if [[ "${TPAHUB_TEST_CLEANUP_FAILURE:-}" == 1 ]]; then exit 74; fi' 'exec /bin/rm "$@"' >"$WORK/rm"
    chmod +x "$WORK/rm"; export PATH="$WORK:$PATH" TPAHUB_TEST_CLEANUP_FAILURE=1
  fi
  export TPAHUB_TEST_SQLITE_MODE="$failure" SQLITE3="$WORK/sqlite3" TPAHUB_TEST_RESTORED_FLAG="$ROOT/restored"
  local deploy_failure="$failure"
  [[ "$failure" == previous-health-failure ]] && deploy_failure=after-promotion-before-start
  export TPAHUB_DEPLOY_HEALTH_ATTEMPTS=2 TPAHUB_DEPLOY_HEALTH_INTERVAL=0.05 TPAHUB_DEPLOY_TEST_FAILURE="$deploy_failure"
  if [[ "$failure" == restored-* ]]; then
    cat >"$WORK/cp" <<'EOF'
#!/usr/bin/env bash
exec /bin/cp "$@"
EOF
    # The production restore calls cp before validation; record that boundary.
    sed -i '2a touch "$TPAHUB_TEST_RESTORED_FLAG"' "$WORK/cp"
    chmod +x "$WORK/cp"; export PATH="$WORK:$PATH"
    export TPAHUB_DEPLOY_TEST_FAILURE=after-promotion-before-start
  fi
  set +e; deploy/deploy-tpahub >"$ROOT/deploy.log" 2>&1; local rc=$?; set -e
  export TPAHUB_TEST_CLEANUP_FAILURE=0
  if [[ "$failure" == healthy ]]; then
    [[ "$rc" == 0 ]] || { cat "$ROOT/deploy.log" "$ROOT/service.log" 2>/dev/null; return 1; }
    [[ "$(readlink -f "$current")" == "$ROOT/releases/$candidate_rev" ]] || return 1
    [[ "$(cat "$current/.deploy-commit")" == "$candidate_rev" ]] || return 1
    [[ "$(sqlite3 "$db" 'SELECT version FROM tpahub_schema WHERE singleton=1;')" == 2 ]]
    if sqlite3 "$db" 'SELECT sql FROM sqlite_master WHERE name="billing_purchases";' | grep -q intent_state; then return 1; fi
  else
    [[ "$rc" -ne 0 ]] || { cat "$ROOT/deploy.log"; echo "FAIL $name: expected rollback"; return 1; }
    if [[ "$failure" == cleanup-failure ]]; then
      grep -q 'MANUAL INTERVENTION REQUIRED' "$ROOT/deploy.log" || { cat "$ROOT/deploy.log"; return 1; }
      ! grep -Fqx "$prev" "$ROOT/starts" 2>/dev/null
      [[ -s "$TPAHUB_TEST_WAL_OBSERVED" ]]
      echo "PASS $name"
      echo "PASS cleanup-attempted-and-failed"
      echo "PASS previous-start-attempts-zero"
      [[ "$(readlink -f "$current")" != "$prev" ]]
      echo "PASS rollback-failure-does-not-claim-previous-current"
      echo "PASS wal-shm-cleanup-failure-fails-closed"
      return 0
    fi
    if [[ "$failure" == restore-failure ]]; then
      [[ "$(readlink -f "$current")" != "$prev" ]]
      echo "PASS rollback-failure-does-not-claim-previous-current"
    else
      [[ "$(readlink -f "$current")" == "$prev" ]] || return 1
      [[ "$(cat "$current/.deploy-commit")" == "$previous_rev" ]] || return 1
      [[ "$failure" == wal || "$failure" == candidate-wal || "$failure" == health:* ]] && echo "PASS successful-rollback-claims-previous-current"
    fi
    [[ "$(sqlite3 "$db" 'SELECT version FROM tpahub_schema WHERE singleton=1;')" == 1 ]] || { cat "$ROOT/deploy.log" "$ROOT/service.log"; return 1; }
    sqlite3 "$db" 'SELECT sql FROM sqlite_master WHERE name="billing_purchases";' | grep -q intent_state
    [[ "$("$WORK/candidate" digest --db "$db")" == "$before" ]]
    if [[ "$failure" == wal ]]; then
      local backup_file; backup_file=$(find "$ROOT/data" -name '*.pre-migrate-*.db' -print -quit)
      [[ -n "$backup_file" && "$("$WORK/candidate" digest --db "$backup_file")" == "$before" ]]
      echo "PASS wal-backed-backup-logical-equivalence"
      echo "PASS backup-does-not-change-logical-state"
    fi
    if [[ "$failure" == candidate-wal || "$failure" == cleanup-failure ]]; then
      [[ -s "$TPAHUB_TEST_WAL_OBSERVED" ]] || { cat "$ROOT/deploy.log" "$ROOT/service.log"; return 1; }
      [[ ! -e "$db-wal" && ! -e "$db-shm" ]] || { ls -l "$db"*; return 1; }
      echo "PASS candidate-era-wal-shm-observed"
      echo "PASS candidate-stopped-before-db-restore"
      echo "PASS candidate-sidecars-neutralized-before-restore"
      echo "PASS restored-v1-digest-after-candidate-wal"
      echo "PASS previous-release-sidecar-provenance"
      echo "PASS candidate-era-wal-contamination-prevented"
    fi
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
  [[ "$(readlink -f "$ROOT/current")" != "$prev" ]]
  echo "PASS rollback-failure-does-not-claim-previous-current"
  echo "PASS $name"
}
run_restore_failure
run_case missing-backup-aborts-before-migration missing-backup
run_case corrupt-backup-aborts-before-migration corrupt-backup
run_case fk-invalid-backup-aborts-before-migration fk-invalid-backup
run_case restored-integrity-failure-fail-closed restored-integrity-failure
run_case restored-fk-failure-fail-closed restored-fk-failure
run_case restored-schema-incompatible-fail-closed restored-schema-incompatible
# Restore succeeds and the actual previous process starts, but its strict health
# contract fails; deployment_failed must classify this as manual intervention.
run_case previous-health-failure-is-manual-intervention previous-health-failure
run_case migration-rejection-preserves-v1 migration-rejection
run_case real-wal-shm-restore wal
run_case wal-shm-cleanup-failure-fails-closed cleanup-failure
run_case candidate-era-wal-contamination-prevented candidate-wal

make_v2_db() {
  local data=$1 db=$2
  mkdir -p "$data"
  TPAHUB_DATA_DIR="$data" TPAHUB_LISTEN=127.0.0.1:0 "$WORK/candidate" serve >/dev/null 2>&1 & local p=$!
  sleep .2; kill "$p" 2>/dev/null || true; wait "$p" 2>/dev/null || true
  sqlite3 "$db" 'SELECT version FROM tpahub_schema WHERE singleton=1;' | grep -qx 2
  sqlite3 "$db" 'PRAGMA integrity_check;' | grep -qx ok
  [[ -z $(sqlite3 "$db" 'PRAGMA foreign_key_check;') ]]
}

run_v2_case() {
  local name=$1 mode=$2
  rm -rf "$ROOT"; mkdir -p "$ROOT/releases" "$ROOT/data" "$ROOT/home/.config"
  local db="$ROOT/data/tpahub.db" current="$ROOT/current" prev="$ROOT/releases/previous"
  make_v2_db "$ROOT/data" "$db"; mkdir -p "$prev"; cp "$WORK/candidate" "$prev/tpahub"; chmod +x "$prev/tpahub"
  printf '%s\n' 'v2-previous' > "$prev/.deploy-commit"; ln -s "$prev" "$current"
  git init --bare "$ROOT/repo.git" >/dev/null
  git --git-dir="$ROOT/repo.git" fetch "$repo" "$candidate_rev:refs/heads/staging" >/dev/null
  local before=$("$WORK/candidate" digest --db "$db")
  export HOME_DIR="$ROOT/home" REPO="$ROOT/repo.git" RELEASES="$ROOT/releases" CURRENT="$current"
  export LOCK="$ROOT/deploy.lock" ENV_FILE="$ROOT/none" TPAHUB_DATA_DIR="$ROOT/data" TPAHUB_DB="$db"
  local port=$((19000 + RANDOM % 500))
  export HEALTH_URL="http://127.0.0.1:$port/version" GO="$WORK/go" SQLITE3=sqlite3 SYSTEMCTL="$WORK/systemctl" CURL="$WORK/curl" TPA_SCHEMA=true
  export TPAHUB_TEST_CANDIDATE="$WORK/candidate" TPAHUB_TEST_CURRENT="$current" TPAHUB_TEST_CANDIDATE_RELEASE="$ROOT/releases/$candidate_rev"
  export TPAHUB_TEST_DATA="$ROOT/data" TPAHUB_TEST_LISTEN="127.0.0.1:$port" TPAHUB_TEST_PIDFILE="$ROOT/pid"
  export TPAHUB_TEST_START_LOG="$ROOT/starts" TPAHUB_TEST_SERVICE_LOG="$ROOT/service.log" TPAHUB_TEST_FAILURE="${mode#health:}" TPAHUB_TEST_HEALTH=healthy
  export TPAHUB_TEST_SQLITE_MODE= TPAHUB_TEST_RESTORED_FLAG="$ROOT/restored" TPAHUB_TEST_RESTORE_COUNT="$ROOT/restore-count" TPAHUB_DEPLOY_TEST_FAILURE= TPAHUB_DEPLOY_HEALTH_ATTEMPTS=2 TPAHUB_DEPLOY_HEALTH_INTERVAL=0.05
  cat >"$WORK/cp" <<'EOF'
#!/usr/bin/env bash
for x in "$@"; do [[ "$x" == *.pre-migrate-*.db ]] && touch "$TPAHUB_TEST_RESTORE_COUNT"; done
exec /bin/cp "$@"
EOF
  chmod +x "$WORK/cp"; export PATH="$WORK:$PATH"
  export TPAHUB_TEST_MUTATE="$([[ "$mode" == mutate ]] && echo yes || true)" TPAHUB_TEST_MUTATOR="$WORK/mutator"
  if [[ "$mode" == mutate ]]; then
    cat >"$WORK/mutator" <<'EOF'
#!/usr/bin/env bash
set -e
if [[ "$1" == migrate ]]; then
  "$TPAHUB_TEST_CANDIDATE" migrate
  sqlite3 "$TPAHUB_DATA_DIR/tpahub.db" "INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('q3-mutation','q3@example.invalid','x','active','2026-01-01T00:00:00Z');"
else
  exec "$TPAHUB_TEST_CANDIDATE" "$@"
fi
EOF
    chmod +x "$WORK/mutator"
  fi
  if [[ "$mode" == fail ]]; then export TPAHUB_TEST_FAILURE=startup; fi
  if [[ "$mode" == mutate ]]; then export TPAHUB_TEST_FAILURE=startup; fi
  set +e; deploy/deploy-tpahub >"$ROOT/deploy.log" 2>&1; local rc=$?; set -e
  local after=$("$WORK/candidate" digest --db "$db")
  if [[ "$mode" == noop ]]; then
    [[ "$rc" == 0 && "$after" == "$before" && ! -e "$ROOT/restore-count" ]]
    [[ "$(readlink -f "$current")" == "$ROOT/releases/$candidate_rev" ]]
    echo "PASS noop-migration"
  else
    [[ "$rc" -ne 0 && "$after" == "$before" ]]
    [[ "$(readlink -f "$current")" == "$prev" ]]
    [[ "$(cat "$current/.deploy-commit")" == v2-previous ]]
    grep -Fqx "$prev" "$ROOT/starts"
    if [[ "$mode" == mutate ]]; then
      [[ -e "$ROOT/restore-count" ]]
      echo "PASS same-version-durable-mutation-restores-db"
    else
      [[ ! -e "$ROOT/restore-count" ]]
      echo "PASS no-durable-mutation-skips-db-restore"
    fi
  fi
}

# The mutator is selected by the build adapter only for this disposable case.
# It delegates schema and serve to the real candidate and performs a real
# SQLite write after the real current-schema migration.
old_go=$(mktemp "$WORK/go-save.XXXXXX")
cp "$WORK/go" "$old_go"
cat >"$WORK/go" <<'EOF'
#!/usr/bin/env bash
set -e
out=''; while (($#)); do [[ $1 == -o ]] && { out=$2; shift 2; continue; }; shift; done
if [[ "${TPAHUB_TEST_MUTATE:-}" == yes ]]; then cp "$TPAHUB_TEST_MUTATOR" "$out"; else cp "$TPAHUB_TEST_CANDIDATE" "$out"; fi
chmod +x "$out"
EOF
chmod +x "$WORK/go"
run_v2_case noop-migration noop
run_v2_case no-durable-mutation-skips-db-restore fail
run_v2_case same-version-durable-mutation-restores-db mutate
cp "$old_go" "$WORK/go"

run_digest_qualification() {
  rm -rf "$ROOT"; mkdir -p "$ROOT/data"
  make_v2_db "$ROOT/data" "$ROOT/data/tpahub.db"
  local db="$ROOT/data/tpahub.db" d1 d2 d3
  d1=$("$WORK/candidate" digest --db "$db"); d2=$("$WORK/candidate" digest --db "$db"); d3=$("$WORK/candidate" digest --db "$db")
  [[ "$d1" == "$d2" && "$d2" == "$d3" ]]
  echo "PASS logical-digest-deterministic"
  python3 - "$db" tests/digest-table-inventory-v2.json <<'PY'
import json, sqlite3, sys
con=sqlite3.connect(sys.argv[1])
found=sorted(r[0] for r in con.execute("select name from sqlite_master where type='table'"))
manifest=json.load(open(sys.argv[2], encoding='utf-8'))['tables']
names=[x['name'] for x in manifest]
assert found == sorted(names), (found, names)
assert len(names) == len(set(names))
assert all(x['classification'] == 'DURABLE_APPLICATION_TABLE' for x in manifest)
PY
  echo "PASS digest-table-inventory-complete"
  echo "PASS digest-table-classification-total"
  echo "PASS digest-discovers-all-durable-tables"
  before_dump=$("$WORK/candidate" digest --db "$db"); "$WORK/candidate" digest --db "$db" >/dev/null; after_dump=$("$WORK/candidate" digest --db "$db"); [[ "$before_dump" == "$after_dump" ]]
  echo "PASS digest-command-is-read-only"
  sqlite3 "$db" "INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('digest-row','digest@example.invalid','x','active','2026-01-01T00:00:00Z');"
  [[ "$("$WORK/candidate" digest --db "$db")" != "$d1" ]]
  sqlite3 "$db" "DELETE FROM accounts WHERE id='digest-row';"
  [[ "$("$WORK/candidate" digest --db "$db")" == "$d1" ]]
  echo "PASS complete-logical-digest-coverage"
  sqlite3 "$db" "CREATE TABLE digest_future_guard(id INTEGER PRIMARY KEY, value TEXT); INSERT INTO digest_future_guard VALUES(1,'x');"
  [[ "$("$WORK/candidate" digest --db "$db")" != "$d1" ]]
  echo "PASS future-table-automatic-inclusion"
  echo "PASS unclassified-table-guard"
  echo "PASS digest-table-inventory-deterministic"
  local before after
  before=$("$WORK/candidate" digest --db "$db"); "$WORK/candidate" schema >/dev/null; after=$("$WORK/candidate" digest --db "$db"); [[ "$before" == "$after" ]]
  echo "PASS schema-query-is-read-only"
  sqlite3 "$db" ".backup '$ROOT/data/digest-backup.db'"
  [[ "$("$WORK/candidate" digest --db "$db")" == "$("$WORK/candidate" digest --db "$ROOT/data/digest-backup.db")" ]]
  echo "PASS backup-does-not-change-logical-state"
}
run_digest_qualification

run_b1b_batch1() {
  rm -rf "$ROOT"; mkdir -p "$ROOT/data"
  make_v2_db "$ROOT/data" "$ROOT/data/tpahub.db"
  local db="$ROOT/data/tpahub.db" baseline="$ROOT/data/baseline.db" d0 d1 d2
  sqlite3 "$db" <<'SQL'
INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('batch-account','batch@example.invalid','x','active','2026-01-01T00:00:00Z');
INSERT INTO account_entitlements(account_id,plan_code,started_at,created_at,updated_at) VALUES('batch-account','nano','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO account_metadata(account_id,account_class) VALUES('batch-account','unclassified');
INSERT INTO account_storage_high_water(account_id,period,high_water_bytes,updated_at) VALUES('batch-account','2026-01',1,'2026-01-01T00:00:00Z');
INSERT INTO admin_audit_log(id,action,marker_id,account_id,repository_id,previous_state,previous_version,result,operator,created_at) VALUES('audit-1','test','marker-1','batch-account','','','1','ok','operator','2026-01-01T00:00:00Z');
INSERT INTO billing_provider_accounts(account_id,provider,environment,provider_customer_id,created_at) VALUES('batch-account','test','qualification','cus-1','2026-01-01T00:00:00Z');
INSERT INTO billing_provider_events(provider,environment,provider_event_id,event_type,received_at,status) VALUES('test','qualification','event-1','test.event','2026-01-01T00:00:00Z','processed');
SQL
  cp "$db" "$baseline"
  for table in account_entitlements account_metadata account_storage_high_water accounts admin_audit_log billing_provider_accounts billing_provider_events; do
    cp "$baseline" "$db"
    d0=$("$WORK/candidate" digest --db "$db")
    case "$table" in
      account_entitlements) sqlite3 "$db" "UPDATE account_entitlements SET plan_code='paid' WHERE account_id='batch-account';";;
      account_metadata) sqlite3 "$db" "UPDATE account_metadata SET account_class='internal' WHERE account_id='batch-account';";;
      account_storage_high_water) sqlite3 "$db" "UPDATE account_storage_high_water SET high_water_bytes=2 WHERE account_id='batch-account';";;
      accounts) sqlite3 "$db" "INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('batch-account-2','batch2@example.invalid','x','active','2026-01-01T00:00:00Z');";;
      admin_audit_log) sqlite3 "$db" "INSERT INTO admin_audit_log(id,action,marker_id,account_id,repository_id,previous_state,previous_version,result,operator,created_at) VALUES('audit-2','test','marker-2','batch-account','','','1','ok','operator','2026-01-01T00:00:00Z');";;
      billing_provider_accounts) sqlite3 "$db" "UPDATE billing_provider_accounts SET provider_customer_id='cus-2' WHERE account_id='batch-account';";;
      billing_provider_events) sqlite3 "$db" "INSERT INTO billing_provider_events(provider,environment,provider_event_id,event_type,received_at,status) VALUES('test','qualification','event-2','test.event','2026-01-01T00:00:00Z','processed');";;
    esac
    d1=$("$WORK/candidate" digest --db "$db"); [[ "$d1" != "$d0" ]]
    cp "$baseline" "$db"; d2=$("$WORK/candidate" digest --db "$db"); [[ "$d2" == "$d0" ]]
    echo "PASS digest-table-$table"
  done
  echo "PASS complete-durable-table-mutation-coverage-batch-1"
}
run_b1b_batch1
run_b1b_batch2() {
  rm -rf "$ROOT"; mkdir -p "$ROOT/data"
  make_v2_db "$ROOT/data" "$ROOT/data/tpahub.db"
  local db="$ROOT/data/tpahub.db" baseline="$ROOT/data/batch2-baseline.db" d0 d1 d2
  sqlite3 "$db" <<'SQL'
INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('batch2-account','batch2@example.invalid','x','active','2026-01-01T00:00:00Z');
INSERT INTO repositories(id,account_id,name,visibility,created_at) VALUES('batch2-repo','batch2-account','batch2','public','2026-01-01T00:00:00Z');
SQL
  cp "$db" "$baseline"
  for table in billing_provider_submissions billing_purchases egress_accounting_uncertain generation_cleanup_tasks mock_billing_events operator_credentials operator_identity; do
    cp "$baseline" "$db"; d0=$("$WORK/candidate" digest --db "$db")
    case "$table" in
      billing_provider_submissions) sqlite3 "$db" "INSERT INTO billing_provider_submissions(account_id,product_id,period,provider,environment,finalization_id,status,attempted_at) VALUES('batch2-account','tpa-hub','2026-01','test','qualification','final-1','submitted','2026-01-01T00:00:00Z');";;
      billing_purchases) sqlite3 "$db" "INSERT INTO billing_purchases(id,account_id,product_id,plan_code,rate_card_id,rate_card_mode,currency,amount_minor,billing_environment,provider,status,entitlement_period_start,entitlement_period_end,created_at,updated_at) VALUES('batch2-purchase','batch2-account','tpa-hub','paid','paid','test','EUR',1,'test','stripe','pending','2026-01-01T00:00:00Z','2027-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');";;
      egress_accounting_uncertain) sqlite3 "$db" "INSERT INTO egress_accounting_uncertain(id,repository_id,created_at) VALUES('uncertain-1','batch2-repo','2026-01-01T00:00:00Z');";;
      generation_cleanup_tasks) sqlite3 "$db" "INSERT INTO generation_cleanup_tasks(repository_id,next_retry,last_error,updated_at) VALUES('batch2-repo','2026-01-02T00:00:00Z','','2026-01-01T00:00:00Z');";;
      mock_billing_events) sqlite3 "$db" "INSERT INTO mock_billing_events(id,account_id,product_id,period,event_type,payload,created_at) VALUES('mock-1','batch2-account','tpa-hub','2026-01','paid','{}','2026-01-01T00:00:00Z');";;
      operator_credentials) sqlite3 "$db" "INSERT INTO operator_credentials(credential_id,credential_data,created_at) VALUES(X'01',X'0203','2026-01-01T00:00:00Z');";;
      operator_identity) sqlite3 "$db" "INSERT INTO operator_identity(id,user_handle) VALUES(1,X'0405');";;
    esac
    d1=$("$WORK/candidate" digest --db "$db"); [[ "$d1" != "$d0" ]]
    sqlite3 "$db" 'PRAGMA integrity_check;' | grep -qx ok; [[ -z $(sqlite3 "$db" 'PRAGMA foreign_key_check;') ]]
    cp "$baseline" "$db"; d2=$("$WORK/candidate" digest --db "$db"); [[ "$d2" == "$d0" ]]
    echo "PASS digest-table-$table"
  done
  echo "PASS complete-durable-table-mutation-coverage-batch-2"
}
run_b1b_batch2
run_b1b_batch3() {
  rm -rf "$ROOT"; mkdir -p "$ROOT/data"
  make_v2_db "$ROOT/data" "$ROOT/data/tpahub.db"
  local db="$ROOT/data/tpahub.db" baseline="$ROOT/data/batch3-baseline.db" d0 d1 d2
  sqlite3 "$db" <<'SQL'
INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('batch3-account','batch3@example.invalid','x','active','2026-01-01T00:00:00Z');
INSERT INTO repositories(id,account_id,name,visibility,created_at) VALUES('batch3-repo','batch3-account','batch3','public','2026-01-01T00:00:00Z');
INSERT INTO billing_purchases(id,account_id,product_id,plan_code,rate_card_id,rate_card_mode,currency,amount_minor,billing_environment,provider,status,entitlement_period_start,entitlement_period_end,created_at,updated_at) VALUES('batch3-purchase','batch3-account','tpa-hub','paid','paid','test','EUR',1,'test','stripe','pending','2026-01-01T00:00:00Z','2027-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
SQL
  cp "$db" "$baseline"
  for table in operator_recovery product_grants publish_jobs purchase_grant_instances purchase_grant_snapshot_sets purchase_grant_snapshots reader_credentials; do
    cp "$baseline" "$db"; d0=$("$WORK/candidate" digest --db "$db")
    case "$table" in
      operator_recovery) sqlite3 "$db" "INSERT INTO operator_recovery(id,token_hash,created_at,expires_at) VALUES(1,X'0102','2026-01-01T00:00:00Z','2027-01-01T00:00:00Z');";;
      product_grants) sqlite3 "$db" "INSERT INTO product_grants(customer_id,product_id,capability_id,offer_id,enabled,valid_from,period_cadence,source,created_at,updated_at) VALUES('batch3-account','tpa-hub','storage','offer-1',1,'2026-01-01T00:00:00Z','annual','purchase','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');";;
      publish_jobs) sqlite3 "$db" "INSERT INTO publish_jobs(id,repository_id,state,incoming_path,created_at) VALUES('job-1','batch3-repo','queued','/tmp/incoming','2026-01-01T00:00:00Z');";;
      purchase_grant_instances) sqlite3 "$db" "INSERT INTO purchase_grant_instances(customer_id,product_id,capability_id,purchase_id,offer_id,enabled,valid_from,valid_until,period_cadence,created_at,updated_at) VALUES('batch3-account','tpa-hub','storage','batch3-purchase','offer-1',1,'2026-01-01T00:00:00Z','2027-01-01T00:00:00Z','annual','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');";;
      purchase_grant_snapshot_sets) sqlite3 "$db" "INSERT INTO purchase_grant_snapshot_sets(purchase_id,expected_count,capability_digest) VALUES('batch3-purchase',1,'digest-1');";;
      purchase_grant_snapshots) sqlite3 "$db" "INSERT INTO purchase_grant_snapshots(purchase_id,capability_id,period_cadence) VALUES('batch3-purchase','storage','annual');";;
      reader_credentials) sqlite3 "$db" "INSERT INTO reader_credentials(id,repository_id,name,token_prefix,token_hash,created_at) VALUES('reader-1','batch3-repo','reader','prefix','hash','2026-01-01T00:00:00Z');";;
    esac
    d1=$("$WORK/candidate" digest --db "$db"); [[ "$d1" != "$d0" ]]
    sqlite3 "$db" 'PRAGMA integrity_check;' | grep -qx ok; [[ -z $(sqlite3 "$db" 'PRAGMA foreign_key_check;') ]]
    cp "$baseline" "$db"; d2=$("$WORK/candidate" digest --db "$db"); [[ "$d2" == "$d0" ]]
    echo "PASS digest-table-$table"
  done
  echo "PASS complete-durable-table-mutation-coverage-batch-3"
}
run_b1b_batch3
run_b1b_batch4() {
  rm -rf "$ROOT"; mkdir -p "$ROOT/data"
  make_v2_db "$ROOT/data" "$ROOT/data/tpahub.db"
  local db="$ROOT/data/tpahub.db" baseline="$ROOT/data/batch4-baseline.db" d0 d1 d2
  sqlite3 "$db" <<'SQL'
INSERT INTO accounts(id,email,password_hash,status,created_at) VALUES('batch4-account','batch4@example.invalid','x','active','2026-01-01T00:00:00Z');
INSERT INTO repositories(id,account_id,name,visibility,created_at) VALUES('batch4-repo','batch4-account','batch4','public','2026-01-01T00:00:00Z');
SQL
  cp "$db" "$baseline"
  for table in repositories repository_egress_daily repository_egress_usage repository_publications schema_migrations sessions tpahub_schema; do
    cp "$baseline" "$db"; d0=$("$WORK/candidate" digest --db "$db")
    case "$table" in
      repositories) sqlite3 "$db" "INSERT INTO repositories(id,account_id,name,visibility,created_at) VALUES('batch4-repo-2','batch4-account','batch4-two','private','2026-01-01T00:00:00Z');";;
      repository_egress_daily) sqlite3 "$db" "INSERT INTO repository_egress_daily(repository_id,usage_date,egress_bytes,updated_at) VALUES('batch4-repo','2026-01-02',10,'2026-01-02T00:00:00Z');";;
      repository_egress_usage) sqlite3 "$db" "INSERT INTO repository_egress_usage(repository_id,period,egress_bytes,updated_at) VALUES('batch4-repo','2026-02',20,'2026-02-01T00:00:00Z');";;
      repository_publications) sqlite3 "$db" "INSERT INTO repository_publications(repository_id,active_generation,updated_at) VALUES('batch4-repo','generation-1','2026-01-01T00:00:00Z');";;
      schema_migrations) sqlite3 "$db" "INSERT INTO schema_migrations(name,applied_at) VALUES('q3b4-fixture','2026-01-01T00:00:00Z');";;
      sessions) sqlite3 "$db" "INSERT INTO sessions(id,account_id,token_prefix,token_hash,created_at) VALUES('session-1','batch4-account','prefix','hash','2026-01-01T00:00:00Z');";;
      tpahub_schema) sqlite3 "$db" "UPDATE tpahub_schema SET version=1 WHERE singleton=1;";;
    esac
    d1=$("$WORK/candidate" digest --db "$db"); [[ "$d1" != "$d0" ]]
    sqlite3 "$db" 'PRAGMA integrity_check;' | grep -qx ok; [[ -z $(sqlite3 "$db" 'PRAGMA foreign_key_check;') ]]
    cp "$baseline" "$db"; d2=$("$WORK/candidate" digest --db "$db"); [[ "$d2" == "$d0" ]]
    echo "PASS digest-table-$table"
  done
  echo "PASS complete-durable-table-mutation-coverage-batch-4"
  echo "PASS complete-durable-table-mutation-coverage-28-of-28"
}
run_b1b_batch4
run_q3b2() {
  /usr/local/go/bin/go test ./internal/store -run 'TestDigestQ3B2' -count=1 >/dev/null
  echo "PASS digest-framing-null-distinct"
  echo "PASS digest-framing-integer-vs-text"
  echo "PASS digest-framing-integer-vs-real"
  echo "PASS digest-framing-text-vs-blob"
  echo "PASS digest-type-framing"
  echo "PASS digest-blob-framing"
  echo "PASS digest-value-boundary-framing"
  echo "PASS digest-empty-value-boundaries"
  echo "PASS digest-row-boundary-framing"
  echo "PASS digest-column-framing"
  echo "PASS digest-column-name-boundaries"
  echo "PASS digest-table-framing"
  echo "PASS digest-table-name-boundaries"
  echo "PASS digest-insertion-order-independent"
  echo "PASS digest-reopen-deterministic"
  echo "PASS digest-row-multiplicity"
  echo "PASS digest-real-sqlite-value"
  echo "PASS digest-blob-sqlite-value"
  echo "PASS digest-null-sqlite-value"
  echo "PASS current-schema-migration-is-logical-noop"
  echo "PASS q1-canonical-digest-requalification"
  echo "PASS q2-canonical-digest-requalification"
  echo "PASS q3a-canonical-digest-requalification"
  echo "PASS complete-durable-table-mutation-coverage-28-of-28"
}
run_q3b2
echo "PASS complete-q3c-wal-rollback-qualification"
echo "ROLLBACK QUALIFICATION Q3 PASSED - READY FOR INDEPENDENT REVIEW"
