#!/bin/sh
# Disposable pre-release fixtures are rejection inputs, never migration sources.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
(cd "$ROOT" && go build -o "$TMP/candidate" ./cmd/tparun)
test "$("$TMP/candidate" schema)" = 5
check_rejection() {
  state=$1 expected=$2
  before=$("$TMP/candidate" digest --db "$state/tpahub.db")
  bytes_before=$(sha256sum "$state/tpahub.db")
  ddl_before=$(python3 - "$state/tpahub.db" <<'PY'
import sqlite3,sys,json
print(json.dumps(sqlite3.connect(sys.argv[1]).execute('SELECT type,name,sql FROM sqlite_master ORDER BY type,name').fetchall()))
PY
)
  for command in serve migrate; do
    if env -i PATH="$PATH" HOME="$TMP" TPARUN_DATA_DIR="$state" "$TMP/candidate" "$command" > "$TMP/rejection.log" 2>&1; then
      echo "unexpected $command acceptance: $state" >&2; exit 1
    fi
    grep -q "$expected" "$TMP/rejection.log"
    test "$before" = "$("$TMP/candidate" digest --db "$state/tpahub.db")"
    test "$bytes_before" = "$(sha256sum "$state/tpahub.db")"
  done
  ddl_after=$(python3 - "$state/tpahub.db" <<'PY'
import sqlite3,sys,json
print(json.dumps(sqlite3.connect(sys.argv[1]).execute('SELECT type,name,sql FROM sqlite_master ORDER BY type,name').fetchall()))
PY
)
  test "$ddl_before" = "$ddl_after"
}
for pair in \
  623adb1369407ebe0e13313f6f6f7afe67f93d75:1 \
  4ff5a1e0ae433cb08ba15faa35cedfea3d9099d0:2 \
  94c226dae1999d27895d75fc14d27d790b0b5f2f:3 \
  1b226391c6beeaa1dbf3ab99e1dfbe8a82ef2b73:4
do
  revision=$(git -C "$ROOT" rev-parse "${pair%:*}")
  version=${pair#*:}
  source="$TMP/schema$version"
  mkdir -p "$source"
  git -C "$ROOT" archive "$revision" | tar -x -C "$source"
  mkdir -p "$source/cmd/lineage-probe"
  cp "$ROOT/tests/testdata/lineage/probe.go" "$source/cmd/lineage-probe/main.go"
  actual=$(cd "$source" && go run ./cmd/lineage-probe "$source/fixture")
  test "$actual" = "$version"
  check_rejection "$source/fixture" "unsupported pre-release database schema $version"
  printf 'PASS pre-release Schema %s rejection: serve + migrate, bytes + DDL + rows (%s)\n' "$version" "$revision"
done
(cd "$ROOT" && go run ./tests/testdata/lineage/probe.go "$TMP/current") > "$TMP/current-version"
test "$(cat "$TMP/current-version")" = 5
before=$(sha256sum "$TMP/current/tpahub.db")
for iteration in 1 2 3; do
 env -i PATH="$PATH" HOME="$TMP" TPARUN_DATA_DIR="$TMP/current" "$TMP/candidate" migrate
 test "$before" = "$(sha256sum "$TMP/current/tpahub.db")"
done
printf 'PASS Schema 5 repeated public migrate no-op\n'
python3 - "$TMP/current/tpahub.db" <<'PY'
import sqlite3,sys
with sqlite3.connect(sys.argv[1]) as db: db.execute('UPDATE tpahub_schema SET version=6')
PY
check_rejection "$TMP/current" 'newer than this binary'
printf 'PASS future Schema 6 rejection: serve + migrate, bytes + DDL + rows\n'
