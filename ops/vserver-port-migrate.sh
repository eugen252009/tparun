#!/usr/bin/env bash
# vserver port standardization and TPA.run adoption. Review before privileged use.
set -Eeuo pipefail
umask 077

readonly PACKAGE=/tmp/tparun_0.1.4~local1_amd64.deb
readonly PACKAGE_SHA=5063d5fd6bf73f4f0a0d4ec48befa720e90d70c3f581552e339817dd693a3a34
readonly PACKAGE_REV=4b0e9ec49388153e3265908c17b2ec4415da5d32
readonly OLD_TPA_REV=044e0d18a562e1d6974500bf8c957cebb9e5aba6
readonly BACKUP_ROOT=/root/vserver-port-migration-rollback
readonly TPA_DATA=/home/eugen/vserver-tpahub-qualification/data
readonly TPA_GPG=/home/eugen/vserver-tpahub-qualification/gnupg
readonly TPA_OLD_BIN=/home/eugen/vserver-tpahub-qualification/bin/tparun
readonly TPA_OLD_UNIT=tparun.service
readonly TPA_NEW_PORT=9050
readonly CONTACT_OLD_PORT=8083 CONTACT_NEW_PORT=8060
readonly KORE_OLD_PORT=4001 KORE_NEW_PORT=8070
readonly LLAMA_PORT=8003
readonly EXPECTED_HOST=crimson-okapi-31849

MODE=${1:-}
BACKUP_DIR=${2:-}
PHASE=initialization
MUTATION_STARTED=0
USER_NAME=eugen
USER_UID=
TPA_PID= CONTACT_PID= KORE_PID=
TPA_PIDS=() CONTACT_PIDS=() KORE_PIDS=() PACKAGED_TPA_PIDS=() KORE_MIGRATED_PIDS=()
PACKAGED_TPA_SCOPE= PACKAGED_TPA_UID= PACKAGED_TPA_USER= PACKAGED_TPA_UNIT=
KORE_MIGRATED_SCOPE= KORE_MIGRATED_UID= KORE_MIGRATED_USER= KORE_MIGRATED_UNIT=
TPA_SCOPE= TPA_UID= TPA_USER= TPA_UNIT=
CONTACT_SCOPE= CONTACT_UID= CONTACT_USER= CONTACT_UNIT=
KORE_SCOPE= KORE_UID= KORE_USER= KORE_UNIT=
TPA_UNIT_FILE= TPA_ENV_FILE= CONTACT_UNIT_FILE= KORE_UNIT_FILE=
CONTACT_ENV_FILES=() KORE_ENV_FILES=()
NGINX_FILES=()
BACKED_FILES=()

usage() {
  cat <<'EOF'
Usage (when Mayi is installed):
  mayi ./ops/vserver-port-migrate.sh --check
  mayi ./ops/vserver-port-migrate.sh --apply
  mayi ./ops/vserver-port-migrate.sh --rollback /root/vserver-port-migration-rollback/<timestamp>

Otherwise use the host's documented authorized privilege workflow. No mode
defaults to usage only. --apply performs a coordinated, backed-up migration.
--rollback restores the recorded pre-migration files and user service.
EOF
}

fail() { printf 'ERROR [%s]: %s\n' "$PHASE" "$*" >&2; return 1; }

on_err() {
  local rc=$?
  trap - ERR
  if (( MUTATION_STARTED )); then
    printf '\nFAILED during phase: %s (exit %s)\n' "$PHASE" "$rc" >&2
    printf 'Rollback directory: %s\n' "${BACKUP_DIR:-not created}" >&2
    printf 'Manual rollback (through the approved privilege workflow): %q --rollback %q\n' "$0" "${BACKUP_DIR:-<backup-directory>}" >&2
    printf 'No automatic destructive rollback was attempted.\n' >&2
  fi
  exit "$rc"
}
trap on_err ERR

need() { command -v "$1" >/dev/null 2>&1 || fail "required command missing: $1"; }

if [[ "$MODE" == "" || "$MODE" == "--help" || "$MODE" == "-h" ]]; then usage; exit 0; fi
if (( EUID != 0 )); then fail 'run through the authorized privilege workflow (effective UID must be 0)'; fi
[[ "$(hostname -s)" == "$EXPECTED_HOST" ]] || fail "wrong host: expected $EXPECTED_HOST, found $(hostname -s)"
case "$MODE" in --check|--apply) [[ $# -eq 1 ]] || { usage >&2; exit 2; };; --rollback) [[ $# -eq 2 ]] || { usage >&2; exit 2; };; *) usage >&2; exit 2;; esac
for c in systemctl loginctl runuser ss sha256sum dpkg dpkg-deb dpkg-query install cp tar find grep sed awk nginx curl python3 flock id stat getent systemd-sysusers systemd-tmpfiles realpath readlink hostname touch chmod rm date mktemp seq chown cut sort head cat cmp df du gpg; do need "$c"; done
if [[ "$MODE" != --check ]]; then
  exec 9>/run/lock/vserver-port-migrate.lock
  flock -n 9 || fail 'another migration or rollback is already running'
fi

# A root-run script must address the existing lingering user manager explicitly.
USER_UID=$(id -u "$USER_NAME")
readonly USER_RUNTIME=/run/user/$USER_UID
[[ -S "$USER_RUNTIME/bus" ]] || fail "user systemd bus is unavailable: $USER_RUNTIME/bus"
userctl() { # username followed by systemctl --user arguments; never falls back to system scope
  local username=$1 uid runtime; shift
  uid=$(id -u "$username") || fail "unknown user-systemd owner: $username"
  runtime=/run/user/$uid
  [[ -S "$runtime/bus" ]] || fail "user systemd bus unavailable for $username (uid $uid)"
  runuser -u "$username" -- env XDG_RUNTIME_DIR="$runtime" DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime/bus" systemctl --user "$@"
}
u() { userctl "$USER_NAME" "$@"; }
scoped_systemctl() { # scope, username-or-dash, systemctl arguments
  local scope=$1 username=$2; shift 2
  case "$scope" in user) userctl "$username" "$@";; system) systemctl "$@";; *) fail "invalid systemd scope: $scope";; esac
}
port_free() { ! ss -H -ltn "sport = :$1" | grep -q .; }
manifest_value() { # key from the selected root-owned rollback manifest
  awk -F= -v key="$1" '$1==key {sub(/^[^=]*=/, ""); print; n++} END {if(n!=1) exit 1}' "$BACKUP_DIR/manifest.txt"
}

if [[ "$MODE" == --rollback ]]; then
  BACKUP_DIR=$(realpath -e "$BACKUP_DIR")
  [[ "$BACKUP_DIR" == "$BACKUP_ROOT"/* && "$(stat -c '%U:%a' "$BACKUP_DIR")" == root:700 && -f "$BACKUP_DIR/manifest.txt" && -f "$BACKUP_DIR/files.tsv" && -f "$BACKUP_DIR/units.tsv" ]] || fail 'rollback directory is not a valid migration backup'
  grep -Eq '^migration_state=(started|complete)$' "$BACKUP_DIR/manifest.txt" || fail 'backup has no recognized migration state'
  if grep -qx 'contact_changed_by_apply=false' "$BACKUP_DIR/manifest.txt"; then
    ! awk -F '\t' '$4=="contact-form.service" {found=1} END {exit !found}' "$BACKUP_DIR/units.tsv" || fail 'rollback metadata unexpectedly includes Contact service state'
    ! awk -F '\t' '$1 ~ /contact-form\.service|\/\.config\/contact\/contact\.env/ {found=1} END {exit !found}' "$BACKUP_DIR/files.tsv" || fail 'rollback metadata unexpectedly includes mutable Contact files'
  elif ! grep -qx 'contact_changed_by_apply=true' "$BACKUP_DIR/manifest.txt"; then
    fail 'rollback backup does not explicitly record whether this apply changed Contact; refusing ambiguous rollback'
  fi
  [[ "$(manifest_value kore_rollback_version)" == 1 ]] || fail 'rollback directory lacks the Kore/Nginx consistency manifest'
  KORE_PREVIOUS_PORT=$(manifest_value kore_previous_port)
  KORE_TARGET_PORT=$(manifest_value kore_target_port)
  KORE_ROLLBACK_BIND=$(manifest_value kore_bind)
  KORE_ROLLBACK_SCOPE=$(manifest_value kore_scope)
  KORE_ROLLBACK_UID=$(manifest_value kore_uid)
  KORE_ROLLBACK_USER=$(manifest_value kore_user)
  KORE_ROLLBACK_UNIT=$(manifest_value kore_unit)
  KORE_ROLLBACK_UNIT_CONFIG=$(manifest_value kore_unit_config)
  KORE_ROLLBACK_NGINX_MANIFEST="$BACKUP_DIR/$(manifest_value kore_nginx_manifest)"
  [[ "$KORE_PREVIOUS_PORT" =~ ^[0-9]+$ && "$KORE_TARGET_PORT" =~ ^[0-9]+$ && "$KORE_PREVIOUS_PORT" != "$KORE_TARGET_PORT" && -n "$KORE_ROLLBACK_BIND" ]] || fail 'rollback Kore ports/bind are invalid'
  [[ "$(manifest_value kore_local_health_before)" == 200 && "$(manifest_value kore_public_health_before)" == 200 ]] || fail 'rollback manifest does not prove healthy Kore baseline endpoints'
  [[ "$KORE_ROLLBACK_NGINX_MANIFEST" == "$BACKUP_DIR/kore-nginx.tsv" && -s "$KORE_ROLLBACK_NGINX_MANIFEST" ]] || fail 'Kore Nginx hash manifest is missing or invalid'
  [[ "$KORE_ROLLBACK_UNIT_CONFIG" == /* ]] || fail 'Kore unit config path is not absolute'
  awk -F '\t' -v p="$KORE_ROLLBACK_UNIT_CONFIG" '$1==p {n++} END {exit n==1?0:1}' "$BACKUP_DIR/files.tsv" || fail 'Kore unit config is not covered by the selected rollback backup'
  KORE_UNIT_BACKUP=$(awk -F '\t' -v p="$KORE_ROLLBACK_UNIT_CONFIG" '$1==p {print $2}' "$BACKUP_DIR/files.tsv")
  [[ -f "$KORE_UNIT_BACKUP" ]] || fail 'recorded Kore unit config backup is missing'
  awk -F '\t' -v s="$KORE_ROLLBACK_SCOPE" -v id="$KORE_ROLLBACK_UID" -v u="$KORE_ROLLBACK_USER" -v unit="$KORE_ROLLBACK_UNIT" '$1==s && $2==id && $3==u && $4==unit {n++} END {exit n==1?0:1}' "$BACKUP_DIR/units.tsv" || fail 'Kore owner tuple is not recorded in the selected rollback backup'
  while IFS=$'\t' read -r nginx_file nginx_hash; do
    [[ -n "$nginx_file" && "$nginx_hash" =~ ^[0-9a-f]{64}$ ]] || fail 'invalid Kore Nginx hash entry'
    awk -F '\t' -v p="$nginx_file" '$1==p {n++} END {exit n==1?0:1}' "$BACKUP_DIR/files.tsv" || fail "Kore Nginx file is not covered by rollback files: $nginx_file"
    saved_nginx=$(awk -F '\t' -v p="$nginx_file" '$1==p {print $2}' "$BACKUP_DIR/files.tsv")
    [[ -f "$saved_nginx" && "$(sha256sum "$saved_nginx" | awk '{print $1}')" == "$nginx_hash" ]] || fail "saved Kore Nginx backup is missing or hash-mismatched: $nginx_file"
  done < "$KORE_ROLLBACK_NGINX_MANIFEST"
  printf 'Rolling back from %s\n' "$BACKUP_DIR"
  MUTATION_STARTED=1; PHASE=rollback
  if systemctl is-active --quiet tparun.service; then systemctl stop tparun.service; fi
  systemctl disable tparun.service >/dev/null 2>&1 || true
  if [[ -f /etc/tparun/tparun.env ]]; then cp -a /etc/tparun/tparun.env "$BACKUP_DIR/tparun.env.migration"; chmod 0600 "$BACKUP_DIR/tparun.env.migration"; fi
  if dpkg-query -W -f='${db:Status-Abbrev}' tparun >/dev/null 2>&1; then DEBIAN_FRONTEND=noninteractive dpkg --purge --force-confold tparun; fi
  if [[ -f "$BACKUP_DIR/tparun.env.migration" && -f /etc/tparun/tparun.env ]] && cmp -s "$BACKUP_DIR/tparun.env.migration" /etc/tparun/tparun.env; then rm -f /etc/tparun/tparun.env; fi
  if [[ -f "$BACKUP_DIR/policy-rc.d.absent" ]]; then rm -f /usr/sbin/policy-rc.d; elif [[ -f "$BACKUP_DIR/policy-rc.d.present" ]]; then cp -a "$BACKUP_DIR/policy-rc.d.present" /usr/sbin/policy-rc.d; fi
  while IFS=$'\t' read -r original saved; do
    [[ -n "$original" && -f "$saved" ]] || continue
    cp -a -- "$saved" "$original"
  done < "$BACKUP_DIR/files.tsv"
  systemctl daemon-reload
  while IFS=$'\t' read -r scope uid username unit was_enabled was_active; do
    [[ -n "$unit" ]] || continue
    if [[ "$scope" == user ]]; then
      [[ "$uid" =~ ^[0-9]+$ && "$(id -u "$username")" == "$uid" ]] || fail "rollback owner identity mismatch for $unit"
      userctl "$username" daemon-reload
    elif [[ "$scope" != system ]]; then fail "invalid recorded service scope: $scope"; fi
    if [[ "$was_enabled" == enabled ]]; then
      scoped_systemctl "$scope" "$username" enable "$unit" >/dev/null
    else
      scoped_systemctl "$scope" "$username" disable "$unit" >/dev/null 2>&1 || true
    fi
    if [[ "$was_active" == active ]]; then scoped_systemctl "$scope" "$username" restart "$unit"; fi
  done < "$BACKUP_DIR/units.tsv"
  port_free "$TPA_NEW_PORT" || fail "rollback left TPA target port $TPA_NEW_PORT listening"
  port_free "$KORE_TARGET_PORT" || fail "rollback left Kore target port $KORE_TARGET_PORT listening"
  ss -H -ltn 'sport = :8060' | awk '$4=="127.0.0.1:8060" {ok=1} END {exit !ok}' || fail 'rollback did not preserve pre-existing Contact port 8060'
  ss -H -ltn 'sport = :8082' | grep -q . || fail 'rollback did not restore TPA port 8082'
  KORE_EXPECTED_BIND=${KORE_ROLLBACK_BIND%:*}:$KORE_PREVIOUS_PORT
  ss -H -ltn "sport = :$KORE_PREVIOUS_PORT" | awk -v b="$KORE_EXPECTED_BIND" '$4==b {ok=1} END {exit !ok}' || fail "rollback Kore backend is not listening on recorded bind $KORE_EXPECTED_BIND"
  scoped_systemctl "$KORE_ROLLBACK_SCOPE" "$KORE_ROLLBACK_USER" is-active --quiet "$KORE_ROLLBACK_UNIT" || fail 'rollback Kore unit is not active in its recorded manager scope'
  KORE_ROLLBACK_LOCAL_CODE=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$KORE_PREVIOUS_PORT/" || true)
  [[ "$KORE_ROLLBACK_LOCAL_CODE" == 200 ]] || fail "rollback Kore backend on port $KORE_PREVIOUS_PORT returned HTTP $KORE_ROLLBACK_LOCAL_CODE (expected 200)"
  while IFS=$'\t' read -r nginx_file nginx_hash; do
    [[ "$(sha256sum "$nginx_file" | awk '{print $1}')" == "$nginx_hash" ]] || fail "rollback did not restore original Kore Nginx file hash: $nginx_file"
    grep -Eq "^[[:space:]]*proxy_pass[[:space:]]+http://127\\.0\\.0\\.1:$KORE_PREVIOUS_PORT([;/[:space:]]|$)" "$nginx_file" || fail "restored Kore Nginx file does not proxy to recorded port $KORE_PREVIOUS_PORT: $nginx_file"
    ! grep -Eq "^[[:space:]]*proxy_pass[[:space:]]+http://127\\.0\\.0\\.1:$KORE_TARGET_PORT([;/[:space:]]|$)" "$nginx_file" || fail "restored Kore Nginx file still proxies to migrated port $KORE_TARGET_PORT: $nginx_file"
  done < "$KORE_ROLLBACK_NGINX_MANIFEST"
  nginx -t || fail 'nginx -t failed after Kore rollback restoration'
  systemctl reload nginx
  KORE_ROLLBACK_PUBLIC_CODE=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 https://kore.lupricht.net/ || true)
  [[ "$KORE_ROLLBACK_PUBLIC_CODE" == 200 ]] || fail "restored public Kore endpoint returned HTTP $KORE_ROLLBACK_PUBLIC_CODE (expected 200)"
  curl -fsS --max-time 5 http://127.0.0.1:8082/version > "$BACKUP_DIR/rollback-version.json"
  python3 - "$BACKUP_DIR/rollback-version.json" "$OLD_TPA_REV" <<'PY'
import json,sys
assert json.load(open(sys.argv[1])).get('revision')==sys.argv[2]
PY
  printf 'ROLLBACK PASS. Kore backend and Nginx both restored to port %s; local/public HTTP 200. Adopted TPA data preserved.\n' "$KORE_PREVIOUS_PORT"
  exit 0
fi

socket_has() { # port, exact address (127.0.0.1, *, etc.)
  local port=$1 address=$2
  ss -H -ltnp "sport = :$port" | awk -v a="$address" -v p="$port" '$4 == a ":" p || (a == "*" && $4 ~ (":" p "$")) {found=1} END {exit !found}'
}
listener_pids() { ss -H -ltnp "sport = :$1" | grep -oE 'pid=[0-9]+' | cut -d= -f2 | sort -u; }

resolve_cgroup_owners() { # PID -> every uniquely identifiable candidate; caller rejects zero/multiple
  python3 - "$1" <<'PY'
import pwd,re,sys
pid=sys.argv[1]
try: lines=open(f'/proc/{pid}/cgroup',encoding='ascii').read().splitlines()
except OSError as e: raise SystemExit(f'cannot inspect /proc/{pid}/cgroup: {e}')
owners=set()
for line in lines:
    parts=line.split(':',2)
    if len(parts)!=3: continue
    comps=[c for c in parts[2].split('/') if c]
    for i,c in enumerate(comps):
        if c=='system.slice':
            unit=next((x for x in comps[i+1:] if x.endswith('.service')),None)
            if unit: owners.add(('system','-','-',unit))
        m=re.fullmatch(r'user-(\d+)\.slice',c)
        if m:
            uid=m.group(1)
            if i+1>=len(comps) or comps[i+1]!=f'user@{uid}.service': continue
            try: after=comps.index('app.slice',i+2)
            except ValueError: continue
            unit=next((x for x in comps[after+1:] if x.endswith('.service')),None)
            if unit:
                try: name=pwd.getpwuid(int(uid)).pw_name
                except KeyError: continue
                owners.add(('user',uid,name,unit))
for owner in sorted(owners): print('\t'.join(owner))
PY
}

discover_listener() { # service prefix, port -> verify every ss-owned PID resolves to one active owner
  local service=$1 port=$2 pid records scope uid username unit active key desc
  local candidate_count detail='' problem='' owner_key=''
  local -n pids_ref="${service}_PIDS" scope_ref="${service}_SCOPE" uid_ref="${service}_UID" user_ref="${service}_USER" unit_ref="${service}_UNIT"
  mapfile -t pids_ref < <(listener_pids "$port")
  if ((${#pids_ref[@]}==0)); then fail "$service port $port: listener PIDs=<none>; resolved units=<none>; scope/user/UID=<unknown>"; return 1; fi
  declare -A all_owners=()
  for pid in "${pids_ref[@]}"; do
    records=$(resolve_cgroup_owners "$pid") || records=''
    candidate_count=0
    if [[ -z "$records" ]]; then
      problem+=" PID=$pid has no resolvable service cgroup;"
      continue
    fi
    while IFS=$'\t' read -r scope uid username unit; do
      [[ -n "$unit" ]] || continue
      candidate_count=$((candidate_count+1))
      key="$scope|$uid|$username|$unit"
      all_owners["$key"]+="$pid "
      if [[ "$scope" == user ]]; then
        active=$(userctl "$username" show "$unit" -p ActiveState --value 2>/dev/null || true)
      else
        active=$(systemctl show "$unit" -p ActiveState --value 2>/dev/null || true)
      fi
      desc="scope=$scope uid=$uid user=$username unit=$unit pid=$pid state=${active:-unavailable}"
      detail+=" [$desc]"
      [[ "$active" == active ]] || problem+=" inactive/unavailable owner for PID=$pid ($desc);"
    done <<< "$records"
    ((candidate_count==1)) || problem+=" PID=$pid maps to $candidate_count cgroup owners;"
  done
  if [[ ${#all_owners[@]} -ne 1 || -n "$problem" ]]; then
    fail "$service port $port: listener PIDs=${pids_ref[*]}; resolved owner(s):${detail:- <none>}; issues:${problem:- multiple units/scopes/users}"
    return 1
  fi
  owner_key=${!all_owners[@]}
  IFS='|' read -r scope_ref uid_ref user_ref unit_ref <<< "$owner_key"
}

unit_prop() { # scope, username-or-dash, unit, property
  if [[ "$1" == user ]]; then userctl "$2" show "$3" -p "$4" --value; else systemctl show "$3" -p "$4" --value; fi
}

read_env_value() { # systemd EnvironmentFile key, don't print file or unrelated values
  local file=$1 key=$2
  python3 - "$file" "$key" <<'PY'
import re,sys
p,k=sys.argv[1:]
for line in open(p,encoding='utf-8'):
    m=re.match(r'^\s*(?:export\s+)?'+re.escape(k)+r'\s*=\s*(.*?)\s*$',line)
    if m:
        v=m.group(1)
        if len(v)>=2 and v[0]==v[-1] and v[0] in "\"'": v=v[1:-1]
        print(v)
        break
PY
}

active_tpa_version() { curl -fsS --max-time 4 http://127.0.0.1:8082/version; }

# Discover the exact serving processes from the sockets, then their owning unit.
PHASE=discovery
discover_listener TPA 8082 || exit 1
discover_listener CONTACT 8060 || exit 1
discover_listener KORE 4001 || exit 1
TPA_PID=${TPA_PIDS[0]}; CONTACT_PID=${CONTACT_PIDS[0]}; KORE_PID=${KORE_PIDS[0]}
KORE_BIND=$(ss -H -ltnp 'sport = :4001' | awk 'NR==1 {print $4; exit}')
[[ -n "$KORE_BIND" ]] || fail "Kore port 4001: listener PIDs=${KORE_PIDS[*]}; resolved unit=$KORE_UNIT scope=$KORE_SCOPE user=$KORE_USER UID=$KORE_UID; bind address unavailable"


[[ "$TPA_SCOPE" == user && "$TPA_USER" == "$USER_NAME" && "$TPA_UID" == "$USER_UID" ]] || fail "TPA port 8082: listener PIDs=${TPA_PIDS[*]}; resolved unit=$TPA_UNIT scope=$TPA_SCOPE user=$TPA_USER UID=$TPA_UID, expected the eugen user service"
printf 'Cgroup-verified owners:\n'
printf ' TPA port=8082 PIDs=%s unit=%s scope=%s uid=%s user=%s\n' "${TPA_PIDS[*]}" "$TPA_UNIT" "$TPA_SCOPE" "$TPA_UID" "$TPA_USER"
printf ' Contact port=8060 PIDs=%s unit=%s scope=%s uid=%s user=%s\n' "${CONTACT_PIDS[*]}" "$CONTACT_UNIT" "$CONTACT_SCOPE" "$CONTACT_UID" "$CONTACT_USER"
printf ' Kore port=4001 PIDs=%s unit=%s scope=%s uid=%s user=%s\n' "${KORE_PIDS[*]}" "$KORE_UNIT" "$KORE_SCOPE" "$KORE_UID" "$KORE_USER"

TPA_UNIT_FILE=$(unit_prop "$TPA_SCOPE" "$TPA_USER" "$TPA_UNIT" FragmentPath)
CONTACT_UNIT_FILE=$(unit_prop "$CONTACT_SCOPE" "$CONTACT_USER" "$CONTACT_UNIT" FragmentPath)
KORE_UNIT_FILE=$(unit_prop "$KORE_SCOPE" "$KORE_USER" "$KORE_UNIT" FragmentPath)
[[ -f "$TPA_UNIT_FILE" && -f "$CONTACT_UNIT_FILE" && -f "$KORE_UNIT_FILE" ]] || fail 'could not resolve all active unit files'
for spec in "$TPA_SCOPE:$TPA_USER:$TPA_UNIT" "$CONTACT_SCOPE:$CONTACT_USER:$CONTACT_UNIT" "$KORE_SCOPE:$KORE_USER:$KORE_UNIT"; do
  IFS=: read -r scope username unit <<< "$spec"
  [[ -z "$(unit_prop "$scope" "$username" "$unit" DropInPaths)" ]] || fail "systemd drop-ins found for $unit; inspect and incorporate them before migration"
done
TPA_INLINE_ENV=$(unit_prop "$TPA_SCOPE" "$TPA_USER" "$TPA_UNIT" Environment)
if [[ "$TPA_INLINE_ENV" =~ (^|[[:space:]])(TPARUN_DATA_DIR|GNUPGHOME|TPARUN_LISTEN|TPA_BIN)= ]]; then fail 'critical TPA settings are also inline in the unit; resolve precedence before migration'; fi
CONTACT_ENV_SPEC=$(unit_prop "$CONTACT_SCOPE" "$CONTACT_USER" "$CONTACT_UNIT" EnvironmentFiles)
mapfile -t CONTACT_ENV_FILES < <(printf '%s\n' "$CONTACT_ENV_SPEC" | grep -oE '/[^ ]+' )
mapfile -t KORE_ENV_FILES < <(unit_prop "$KORE_SCOPE" "$KORE_USER" "$KORE_UNIT" EnvironmentFiles | grep -oE '/[^ ]+' )
for f in "${CONTACT_ENV_FILES[@]}" "${KORE_ENV_FILES[@]}"; do [[ -f "$f" && -r "$f" ]] || fail "service EnvironmentFile missing/unreadable: $f"; done
[[ "$CONTACT_SCOPE" == user && "$CONTACT_USER" == eugen && "$CONTACT_UID" == 1001 ]] || fail "Contact port 8060: listener PIDs=${CONTACT_PIDS[*]}; resolved unit=$CONTACT_UNIT scope=$CONTACT_SCOPE user=$CONTACT_USER UID=$CONTACT_UID, expected eugen UID 1001"
[[ "$KORE_SCOPE" == user && "$KORE_USER" == eugen && "$KORE_UID" == 1001 && "$KORE_UNIT" == kore.service ]] || fail "Kore port 4001: listener PIDs=${KORE_PIDS[*]}; resolved unit=$KORE_UNIT scope=$KORE_SCOPE user=$KORE_USER UID=$KORE_UID, expected user=eugen UID=1001 unit=kore.service"
CONTACT_HOME=$(getent passwd "$CONTACT_USER" | cut -d: -f6)
CONTACT_ENV_FILE="$CONTACT_HOME/.config/contact/contact.env"
[[ -f "$CONTACT_ENV_FILE" && -r "$CONTACT_ENV_FILE" ]] || fail 'Contact configuration file is missing/unreadable'
[[ "$CONTACT_ENV_SPEC" == *"$CONTACT_ENV_FILE"* ]] || fail 'Contact service does not load the expected contact.env file'
[[ "$(read_env_value "$CONTACT_ENV_FILE" ADDR)" == 127.0.0.1:8060 ]] || fail 'Contact contact.env ADDR is not the expected 127.0.0.1:8060'

# Resolve the authoritative EnvironmentFile by querying systemd metadata. Refuse
# ambiguous/multiple files; the current deployment uses one protected file.
mapfile -t tpa_envs < <(unit_prop "$TPA_SCOPE" "$TPA_USER" "$TPA_UNIT" EnvironmentFiles | grep -oE '/[^ ]+ \(ignore_errors=no\)' | sed 's/ (ignore_errors=no)$//')
[[ ${#tpa_envs[@]} -eq 1 && -f ${tpa_envs[0]} ]] || fail 'could not resolve exactly one readable TPA.run EnvironmentFile'
TPA_ENV_FILE=${tpa_envs[0]}

# Ensure the executable/process identity still matches the documented production state.
for pid in "${TPA_PIDS[@]}"; do
  [[ "$(readlink -f "/proc/$pid/exe")" == "$(readlink -f "$TPA_OLD_BIN")" ]] || fail "TPA port 8082: listener PID=$pid resolved to $TPA_UNIT but executable differs from expected production binary"
done
TPA_VERSION_JSON=$(active_tpa_version) || fail 'current TPA.run version endpoint is unavailable'
python3 - "$TPA_VERSION_JSON" "$OLD_TPA_REV" <<'PY'
import json,sys
v=json.loads(sys.argv[1]); assert v.get('revision')==sys.argv[2] and v.get('schema')=='11' and v.get('service')=='tparun'
PY
BASELINE_PUBLIC_READY=$(curl -fsS --max-time 10 https://tpa.run/ready) || fail 'public TPA.run readiness failed before migration'
python3 - "$BASELINE_PUBLIC_READY" <<'PY'
import json,sys
assert json.loads(sys.argv[1])=={'ready':True,'schema':11}
PY
contact_public_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 https://lupricht.net/contact || true)
[[ "$contact_public_code" =~ ^[1-4][0-9][0-9]$ ]] || fail "baseline Contact endpoint returned HTTP $contact_public_code"
KORE_LOCAL_BASELINE_CODE=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:4001/ || true)
[[ "$KORE_LOCAL_BASELINE_CODE" == 200 ]] || fail "Kore baseline http://127.0.0.1:4001/ returned HTTP $KORE_LOCAL_BASELINE_CODE (expected 200)"
KORE_PUBLIC_BASELINE_CODE=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 https://kore.lupricht.net/ || true)
[[ "$KORE_PUBLIC_BASELINE_CODE" == 200 ]] || fail "Kore baseline https://kore.lupricht.net/ returned HTTP $KORE_PUBLIC_BASELINE_CODE (expected 200)"
[[ "$(read_env_value "$TPA_ENV_FILE" TPARUN_DATA_DIR)" == "$TPA_DATA" ]] || fail 'live TPA environment data path differs from expected production data'
[[ "$(read_env_value "$TPA_ENV_FILE" GNUPGHOME)" == "$TPA_GPG" ]] || fail 'live TPA environment signing path differs from expected production signing state'
[[ "$(read_env_value "$TPA_ENV_FILE" TPARUN_LISTEN)" == 127.0.0.1:8082 ]] || fail 'live TPA environment is not bound to 127.0.0.1:8082'

# Positively associate the other live processes with their units and port settings.
if [[ "$CONTACT_SCOPE" == user ]]; then
  for pid in "${CONTACT_PIDS[@]}"; do [[ "$(stat -c %u "/proc/$pid")" == "$CONTACT_UID" ]] || fail "Contact port 8060: listener PID=$pid does not match unit=$CONTACT_UNIT user=$CONTACT_USER UID=$CONTACT_UID"; done
fi
if [[ "$KORE_SCOPE" == user ]]; then
  for pid in "${KORE_PIDS[@]}"; do [[ "$(stat -c %u "/proc/$pid")" == "$KORE_UID" ]] || fail "Kore port 4001: listener PID=$pid does not match unit=$KORE_UNIT user=$KORE_USER UID=$KORE_UID"; done
fi
CONTACT_START=$(unit_prop "$CONTACT_SCOPE" "$CONTACT_USER" "$CONTACT_UNIT" ExecStart)
[[ "$CONTACT_START" == *contact-form* ]] || fail "Contact port 8060: listener PIDs=${CONTACT_PIDS[*]}; resolved unit=$CONTACT_UNIT scope=$CONTACT_SCOPE user=$CONTACT_USER UID=$CONTACT_UID; ExecStart does not identify Contact form"
KORE_START=$(unit_prop "$KORE_SCOPE" "$KORE_USER" "$KORE_UNIT" ExecStart)
[[ "$KORE_START" == *kore* || "$KORE_START" == *bun* ]] || fail "Kore port 4001: listener PIDs=${KORE_PIDS[*]}; resolved unit=$KORE_UNIT scope=$KORE_SCOPE user=$KORE_USER UID=$KORE_UID; ExecStart does not identify Kore"

# Parse active Nginx configuration, not disabled sites or arbitrary files.
NGINX_DUMP=$(mktemp)
trap 'rm -f "${NGINX_DUMP:-}"' EXIT
nginx -T >"$NGINX_DUMP" 2>&1 || fail 'nginx -T failed during discovery'
mapfile -t NGINX_FILES < <(awk '/^# configuration file / {p=$4; sub(/:$/, "", p); print p}' "$NGINX_DUMP" | sort -u | while IFS= read -r f; do [[ -f "$f" ]] && printf '%s\n' "$f"; done)
TPA_NGINX_FILES=(); CONTACT_NGINX_FILES=(); KORE_NGINX_FILES=(); KORE_TARGET_NGINX_FILES=()
for f in "${NGINX_FILES[@]}"; do
  grep -Eq 'proxy_pass[[:space:]]+http://127\.0\.0\.1:8082([;/[:space:]]|$)' "$f" && TPA_NGINX_FILES+=("$f") || true
  grep -Eq 'proxy_pass[[:space:]]+http://127\.0\.0\.1:8060/contact([;/[:space:]]|$)' "$f" && CONTACT_NGINX_FILES+=("$f") || true
  grep -Eq 'proxy_pass[[:space:]]+http://127\.0\.0\.1:4001([;/[:space:]]|$)' "$f" && KORE_NGINX_FILES+=("$f") || true
  grep -Ev '^[[:space:]]*#' "$f" | grep -Eq '127\.0\.0\.1:8070([^0-9]|$)' && KORE_TARGET_NGINX_FILES+=("$f") || true
done
((${#TPA_NGINX_FILES[@]} > 0)) || fail 'active Nginx config has no verified TPA upstream 127.0.0.1:8082'
((${#CONTACT_NGINX_FILES[@]} > 0)) || fail 'active Nginx config has no verified pre-migrated Contact upstream 127.0.0.1:8060/contact'
((${#KORE_NGINX_FILES[@]} > 0)) || fail 'active Nginx config has no verified Kore upstream 127.0.0.1:4001'
((${#KORE_TARGET_NGINX_FILES[@]} == 0)) || fail "inconsistent Kore baseline: backend is still on port 4001, but active Nginx config already references target 8070 in: ${KORE_TARGET_NGINX_FILES[*]}"

# Live listener and target-port preflight, including the administrative path.
socket_has 8082 127.0.0.1 || fail 'TPA not listening on 127.0.0.1:8082'
socket_has 8060 127.0.0.1 || fail 'Contact not listening on pre-migrated 127.0.0.1:8060'
ss -H -ltnp 'sport = :4001' | grep -q . || fail 'Kore port 4001 has no live TCP listener'
port_free 9050 || fail 'TPA target port 9050 is occupied'
port_free 8070 || fail 'Kore target port 8070 is occupied'
ss -H -ltn 'sport = :8085' | grep -q . && fail 'legacy llama port 8085 is unexpectedly occupied'
ss -H -ltn 'sport = :22' | grep -q . || fail 'SSH port 22 is not listening; refusing migration'
ss -H -ltn "sport = :$LLAMA_PORT" | grep -q . || fail 'llama.cpp port 8003 is not listening as expected'
UNKNOWN_8080=$(ss -H -ltn 'sport = :8080' | awk '{print $4}' | sort)
UNKNOWN_8081=$(ss -H -ltn 'sport = :8081' | awk '{print $4}' | sort)
nginx -t || fail 'nginx -t failed before mutation'

# Validate package and adoption contract before any change.
[[ -f "$PACKAGE" ]] || fail "package missing: $PACKAGE"
[[ "$(sha256sum "$PACKAGE" | awk '{print $1}')" == "$PACKAGE_SHA" ]] || fail 'prepared Debian package SHA-256 mismatch'
if dpkg-query -W -f='${db:Status-Abbrev}' tparun >/dev/null 2>&1; then fail 'system package tparun is already installed; rollback assumptions require it absent'; fi
PACKAGE_INFO=$(dpkg-deb -f "$PACKAGE" Package Version Architecture)
[[ "$(dpkg-deb -f "$PACKAGE" Package)" == tparun ]] || fail 'package name mismatch'
[[ "$(dpkg-deb -f "$PACKAGE" Architecture)" == amd64 && "$(dpkg --print-architecture)" == amd64 ]] || fail 'package/host architecture mismatch'
PKG_TMP=$(mktemp -d)
trap 'rm -rf "${PKG_TMP:-}"; rm -f "${NGINX_DUMP:-}"' EXIT
dpkg-deb -e "$PACKAGE" "$PKG_TMP/control"
dpkg-deb -x "$PACKAGE" "$PKG_TMP/root"
PACKAGED_UNIT="$PKG_TMP/root/lib/systemd/system/tparun.service"
[[ -f "$PACKAGED_UNIT" ]] || fail 'package does not contain expected system service unit'
grep -Fxq 'User=tparun' "$PACKAGED_UNIT" && grep -Fxq 'Group=tparun' "$PACKAGED_UNIT" || fail 'packaged service account/group differs from adoption assumptions'
grep -Fxq 'WorkingDirectory=/var/lib/tparun' "$PACKAGED_UNIT" || fail 'packaged working directory differs from adoption assumptions'
grep -Fxq 'EnvironmentFile=/etc/tparun/tparun.env' "$PACKAGED_UNIT" || fail 'packaged EnvironmentFile differs from adoption assumptions'
grep -Fq 'ExecStart=/usr/bin/tparun serve' "$PACKAGED_UNIT" || fail 'packaged unit does not explicitly execute tparun serve'
[[ -f "$PKG_TMP/root/usr/lib/sysusers.d/tparun.conf" && -f "$PKG_TMP/root/usr/lib/tmpfiles.d/tparun.conf" ]] || fail 'package is missing expected account/state directory definitions'
grep -Fxq 'u tparun - "TPA.run service" /var/lib/tparun' "$PKG_TMP/root/usr/lib/sysusers.d/tparun.conf" || fail 'package system account definition differs from assumptions'
grep -Fxq 'd /var/lib/tparun/data 0700 tparun tparun -' "$PKG_TMP/root/usr/lib/tmpfiles.d/tparun.conf" && grep -Fxq 'd /var/lib/tparun/gnupg 0700 tparun tparun -' "$PKG_TMP/root/usr/lib/tmpfiles.d/tparun.conf" || fail 'package state/signing directory definitions differ from assumptions'
for script in preinst postinst prerm postrm; do [[ ! -e "$PKG_TMP/control/$script" ]] || fail "package contains maintainer script $script; inspect before proceeding"; done
[[ -d "$TPA_DATA" && -f "$TPA_DATA/tpahub.db" && -d "$TPA_GPG" ]] || fail 'live TPA data/database/signing tree is incomplete'
[[ ! -e /var/lib/tparun/data && ! -e /var/lib/tparun/gnupg ]] || fail 'packaged TPA data/signing destination already exists; refusing adoption over existing state'
[[ ! -e /etc/tparun/tparun.env ]] || fail 'packaged TPA EnvironmentFile already exists; refusing to overwrite it'
if [[ -e /var/lib/tparun ]]; then [[ -z "$(find /var/lib/tparun -mindepth 1 -maxdepth 1 -print -quit)" ]] || fail '/var/lib/tparun contains existing state; refusing adoption'; fi
! getent passwd tparun >/dev/null || fail 'system account tparun already exists outside this package; inspect before adoption'
[[ "$(systemctl show tparun.service -p LoadState --value 2>/dev/null || true)" == not-found ]] || fail 'system tparun.service already exists; refusing to replace an unmanaged unit'
[[ -x "$TPA_OLD_BIN" && -r "$TPA_ENV_FILE" ]] || fail 'old TPA binary/environment is not readable'
source_bytes=$(du -sb "$TPA_DATA" "$TPA_GPG" | awk '{sum += $1} END {print sum+0}')
free_bytes=$(df -PB1 / | awk 'NR==2 {print $4}')
needed_bytes=$((2 * source_bytes + 536870912))
(( free_bytes > needed_bytes )) || fail "insufficient root filesystem space for rollback/adoption snapshots (need about $needed_bytes bytes)"

printf '\nDISCOVERY SUMMARY (no secrets):\n'
printf ' host=%s user=%s uid=%s\n' "$(hostname)" "$USER_NAME" "$USER_UID"
printf ' TPA=%s scope=%s uid=%s user=%s pid=%s revision=%s bind=127.0.0.1:8082 data=%s gpg=%s\n' "$TPA_UNIT" "$TPA_SCOPE" "$TPA_UID" "$TPA_USER" "$TPA_PID" "$OLD_TPA_REV" "$TPA_DATA" "$TPA_GPG"
printf ' Contact: 8083 -> 8060; status: already migrated before this apply run; unit=%s scope=%s uid=%s user=%s pid=%s\n' "$CONTACT_UNIT" "$CONTACT_SCOPE" "$CONTACT_UID" "$CONTACT_USER" "$CONTACT_PID"
printf ' Kore=%s scope=%s uid=%s user=%s pid=%s bind=%s target-port=8070\n' "$KORE_UNIT" "$KORE_SCOPE" "$KORE_UID" "$KORE_USER" "$KORE_PID" "$KORE_BIND"
printf ' Nginx TPA files: %s\n' "${TPA_NGINX_FILES[*]}"
printf ' Nginx Contact files: %s\n' "${CONTACT_NGINX_FILES[*]}"
printf ' Nginx Kore files: %s\n' "${KORE_NGINX_FILES[*]}"
printf ' package: %s\n SHA-256: %s\n expected revision: %s\n' "$PACKAGE_INFO" "$PACKAGE_SHA" "$PACKAGE_REV"
printf ' owner states: TPA=%s Contact=%s Kore=%s\n' "$(scoped_systemctl "$TPA_SCOPE" "$TPA_USER" is-active "$TPA_UNIT")" "$(scoped_systemctl "$CONTACT_SCOPE" "$CONTACT_USER" is-active "$CONTACT_UNIT")" "$(scoped_systemctl "$KORE_SCOPE" "$KORE_USER" is-active "$KORE_UNIT")"
printf ' packaged system unit: user=%s working-directory=%s EnvironmentFile=%s ExecStart=%s\n' "$(sed -n 's/^User=//p' "$PACKAGED_UNIT" | head -n1)" "$(sed -n 's/^WorkingDirectory=//p' "$PACKAGED_UNIT" | head -n1)" "$(sed -n 's/^EnvironmentFile=//p' "$PACKAGED_UNIT" | head -n1)" "$(sed -n 's/^ExecStart=//p' "$PACKAGED_UNIT" | head -n1)"
printf ' Kore bind scope: %s; baseline local/public HTTP=%s/%s\n' "$KORE_BIND" "$KORE_LOCAL_BASELINE_CODE" "$KORE_PUBLIC_BASELINE_CODE"
printf ' standard ports and unknown 8080/8081 will not be modified.\n\n'

if [[ "$MODE" == --check ]]; then
  printf 'CHECK PASS: all preconditions passed; no changes made.\n'
  exit 0
fi

# Create durable backup before first mutation. Never reuse an existing directory.
PHASE=backup
[[ ! -L "$BACKUP_ROOT" ]] || fail 'backup root must not be a symlink'
install -d -o root -g root -m 0700 "$BACKUP_ROOT"
[[ "$(stat -c '%U:%a' "$BACKUP_ROOT")" == root:700 ]] || fail 'backup root must be root-owned mode 0700'
BACKUP_DIR="$BACKUP_ROOT/$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -m 0700 "$BACKUP_DIR"
mkdir -p "$BACKUP_DIR/files"
printf '%s\n' "$UNKNOWN_8080" > "$BACKUP_DIR/unknown-8080-before.txt"
printf '%s\n' "$UNKNOWN_8081" > "$BACKUP_DIR/unknown-8081-before.txt"
: > "$BACKUP_DIR/files.tsv"
: > "$BACKUP_DIR/units.tsv"
: > "$BACKUP_DIR/created-paths.txt"

backup_file() {
  local source=$1 index=${#BACKED_FILES[@]} dest
  [[ -f "$source" ]] || fail "cannot back up missing file: $source"
  dest="$BACKUP_DIR/files/$index-$(basename "$source")"
  cp -a -- "$source" "$dest"
  printf '%s\t%s\n' "$source" "$dest" >> "$BACKUP_DIR/files.tsv"
  printf '%s\n' "$source" >> "$BACKUP_DIR/manifest.txt"
  printf '%s\t%s\n' "$source" "$(sha256sum "$source" | awk '{print $1}')" >> "$BACKUP_DIR/hashes.tsv"
  BACKED_FILES+=("$source")
}

for f in "${TPA_NGINX_FILES[@]}" "${CONTACT_NGINX_FILES[@]}" "${KORE_NGINX_FILES[@]}" "$TPA_UNIT_FILE" "$TPA_ENV_FILE" "$KORE_UNIT_FILE" "${KORE_ENV_FILES[@]}"; do
  already=false; for prev in "${BACKED_FILES[@]}"; do [[ "$prev" == "$f" ]] && already=true; done
  [[ "$already" == true ]] || backup_file "$f"
done
: > "$BACKUP_DIR/kore-nginx.tsv"
for f in "${KORE_NGINX_FILES[@]}"; do printf '%s\t%s\n' "$f" "$(sha256sum "$f" | awk '{print $1}')" >> "$BACKUP_DIR/kore-nginx.tsv"; done
# Preserve old binary separately, and the complete source trees separately from
# the package-owned adoption copy. Do not remove or rewrite these source trees.
cp -a "$TPA_OLD_BIN" "$BACKUP_DIR/tparun.old"
sha256sum "$TPA_OLD_BIN" > "$BACKUP_DIR/tparun.old.sha256"
runuser -u "$USER_NAME" -- env GNUPGHOME="$TPA_GPG" gpg --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr" {print $10}' | sort -u > "$BACKUP_DIR/signing-fingerprints-before.txt"
[[ -s "$BACKUP_DIR/signing-fingerprints-before.txt" ]] || fail 'no production signing fingerprints found; refusing identity migration'
python3 - "$TPA_DATA" "$BACKUP_DIR/data.pre-stop" "$TPA_GPG" "$BACKUP_DIR/gnupg.pre-stop" <<'PY'
import os,shutil,stat,sys
for src,dst in ((sys.argv[1],sys.argv[2]),(sys.argv[3],sys.argv[4])):
    def ignore(directory,names):
        return [name for name in names if stat.S_ISSOCK(os.lstat(os.path.join(directory,name)).st_mode)]
    shutil.copytree(src,dst,symlinks=True,ignore=ignore)
PY

TPA_ENABLED=$(userctl "$TPA_USER" is-enabled "$TPA_UNIT" 2>/dev/null || true); TPA_ACTIVE=$(userctl "$TPA_USER" is-active "$TPA_UNIT" 2>/dev/null || true)
KORE_ENABLED=$(scoped_systemctl "$KORE_SCOPE" "$KORE_USER" is-enabled "$KORE_UNIT" 2>/dev/null || true); KORE_ACTIVE=$(scoped_systemctl "$KORE_SCOPE" "$KORE_USER" is-active "$KORE_UNIT" 2>/dev/null || true)
printf 'user\t%s\t%s\t%s\t%s\t%s\n' "$TPA_UID" "$TPA_USER" "$TPA_UNIT" "$TPA_ENABLED" "$TPA_ACTIVE" >> "$BACKUP_DIR/units.tsv"
printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$KORE_SCOPE" "$KORE_UID" "$KORE_USER" "$KORE_UNIT" "$KORE_ENABLED" "$KORE_ACTIVE" >> "$BACKUP_DIR/units.tsv"
printf 'migration_state=started\nhost=%s\npackage_sha256=%s\npackage_revision=%s\nold_revision=%s\ntpa_scope=%s\ntpa_uid=%s\ntpa_user=%s\ntpa_unit=%s\ntpa_env=%s\ntpa_data=%s\ntpa_gpg=%s\ncontact_scope=%s\ncontact_uid=%s\ncontact_user=%s\ncontact_unit=%s\nkore_scope=%s\nkore_uid=%s\nkore_user=%s\nkore_unit=%s\n' \
  "$(hostname)" "$PACKAGE_SHA" "$PACKAGE_REV" "$OLD_TPA_REV" "$TPA_SCOPE" "$TPA_UID" "$TPA_USER" "$TPA_UNIT" "$TPA_ENV_FILE" "$TPA_DATA" "$TPA_GPG" "$CONTACT_SCOPE" "$CONTACT_UID" "$CONTACT_USER" "$CONTACT_UNIT" "$KORE_SCOPE" "$KORE_UID" "$KORE_USER" "$KORE_UNIT" > "$BACKUP_DIR/manifest.txt"
printf 'manifest_version=3\ncontact_state=already-migrated-before-this-apply\ncontact_changed_by_apply=false\ncontact_old_port=8083\ncontact_current_port=8060\ncontact_env=%s\n' "$CONTACT_ENV_FILE" >> "$BACKUP_DIR/manifest.txt"
printf 'Contact: 8083 -> 8060; status: already migrated before this apply run\nADDR=127.0.0.1:8060\n' > "$BACKUP_DIR/contact-state-before.txt"
sha256sum "$CONTACT_ENV_FILE" >> "$BACKUP_DIR/contact-state-before.txt"
printf 'kore_rollback_version=1\nkore_previous_port=%s\nkore_target_port=%s\nkore_bind=%s\nkore_unit_config=%s\nkore_env_files=%s\nkore_nginx_manifest=kore-nginx.tsv\nkore_nginx_files=%s\nkore_local_health_before=%s\nkore_public_health_before=%s\n' "$KORE_OLD_PORT" "$KORE_NEW_PORT" "$KORE_BIND" "$KORE_UNIT_FILE" "${KORE_ENV_FILES[*]}" "${KORE_NGINX_FILES[*]}" "$KORE_LOCAL_BASELINE_CODE" "$KORE_PUBLIC_BASELINE_CODE" >> "$BACKUP_DIR/manifest.txt"
printf 'listeners_before:\n' >> "$BACKUP_DIR/manifest.txt"
ss -lntup >> "$BACKUP_DIR/manifest.txt"
active_tpa_version > "$BACKUP_DIR/tpa-version-before.json"
printf '%s\n' "$PACKAGE_INFO" > "$BACKUP_DIR/package-info.txt"
dpkg-deb -c "$PACKAGE" > "$BACKUP_DIR/package-contents.txt"
printf 'package maintainer scripts: none (preflight verified)\n' > "$BACKUP_DIR/package-maintainer-scripts.txt"
{
  printf 'TPA filesystem ownership/modes (no file contents):\n'
  stat -c '%U:%G %a %n' "$TPA_DATA" "$TPA_DATA/tpahub.db" "$TPA_GPG" "$TPA_ENV_FILE" "$TPA_OLD_BIN"
  printf '\nSYSTEMD SERVICES\n'; systemctl list-units --all --no-pager --plain --type=service
  printf '\nUSER SERVICES (owner managers)\n'
  for manager_user in "$TPA_USER" "$CONTACT_USER" "$KORE_USER"; do
    [[ -n "$manager_user" && "$manager_user" != - ]] || continue
    printf '\nmanager_user=%s\n' "$manager_user"
    userctl "$manager_user" list-units --all --no-pager --plain --type=service
  done
} > "$BACKUP_DIR/service-state-before.txt"
if command -v docker >/dev/null 2>&1; then
  { docker ps --no-trunc --format '{{.Names}} {{.Ports}}' 2>&1 || true; docker compose ls --all --format json 2>&1 || true; } > "$BACKUP_DIR/docker-before.txt"
else printf 'docker command unavailable\n' > "$BACKUP_DIR/docker-before.txt"; fi
if command -v nft >/dev/null 2>&1; then nft list ruleset > "$BACKUP_DIR/firewall-before.txt" 2>&1 || true
elif command -v iptables-save >/dev/null 2>&1; then iptables-save > "$BACKUP_DIR/firewall-before.txt" 2>&1 || true
elif command -v ufw >/dev/null 2>&1; then ufw status verbose > "$BACKUP_DIR/firewall-before.txt" 2>&1 || true
else printf 'no nft/iptables/ufw inspection tool available\n' > "$BACKUP_DIR/firewall-before.txt"; fi
chmod 0700 "$BACKUP_DIR" "$BACKUP_DIR/files" "$BACKUP_DIR/data.pre-stop" "$BACKUP_DIR/gnupg.pre-stop"
printf 'BACKUP COMPLETE: %s\n' "$BACKUP_DIR"

# From here failures are reported with the explicit rollback command. Do not
# automatically revert production state on an ambiguous partial cutover.
MUTATION_STARTED=1

replace_exact() { # file old literal new literal; replace only verified references
  python3 - "$1" "$2" "$3" <<'PY'
import pathlib,re,sys
p=pathlib.Path(sys.argv[1]);old=sys.argv[2];new=sys.argv[3]
lines=p.read_text().splitlines(keepends=True);count=0;out=[]
for line in lines:
    if old not in line or line.lstrip().startswith('#'):
        out.append(line);continue
    if line.lstrip().split(None,1)[0] != 'proxy_pass':
        raise SystemExit(f'{p}: refusing non-proxy reference to {old!r}')
    count+=line.count(old);out.append(line.replace(old,new))
if count < 1: raise SystemExit(f'{p}: expected active proxy literal not found: {old!r}')
p.write_text(''.join(out))
PY
}

# Contact is verified as already migrated. No Contact unit/env edits or restart
# are performed by this apply run. Only Kore remains in this service phase.
PHASE=service-configuration
python3 - "$KORE_UNIT_FILE" <<'PY'
import pathlib,re,sys
p=pathlib.Path(sys.argv[1]);s=p.read_text()
s,n=re.subn(r'(?m)^Environment="PORT=4001"$', 'Environment="PORT=8070"', s)
if n!=1: raise SystemExit(f'Kore unit expected one exact PORT=4001 setting, got {n}')
p.write_text(s)
PY
replace_env_port() {
  python3 - "$1" "$2" "$3" <<'PY'
import pathlib,re,sys
p=pathlib.Path(sys.argv[1]);old,new=sys.argv[2:]
s=p.read_text();out=[];changed=0
for line in s.splitlines(keepends=True):
    if line.lstrip().startswith('#') or '=' not in line:
        out.append(line);continue
    key,value=line.split('=',1)
    if re.search(r'(?:PORT|LISTEN|BIND|ADDR)$',key.strip(),re.I):
        updated,n=re.subn(r'(?<![0-9])'+re.escape(old)+r'(?![0-9])',new,value)
        if n: line=key+'='+updated;changed+=n
    out.append(line)
if changed: p.write_text(''.join(out))
PY
}
for f in "${KORE_ENV_FILES[@]}"; do replace_env_port "$f" "$KORE_OLD_PORT" "$KORE_NEW_PORT"; done
# The TPA Nginx cutover remains later, after Kore has independently passed
# backend ownership, local health, proxy reload, and public health verification.

PHASE=kore-restart
if [[ "$KORE_SCOPE" == user ]]; then userctl "$KORE_USER" daemon-reload; else systemctl daemon-reload; fi
scoped_systemctl "$KORE_SCOPE" "$KORE_USER" restart "$KORE_UNIT"
scoped_systemctl "$KORE_SCOPE" "$KORE_USER" is-active --quiet "$KORE_UNIT" || fail 'Kore service is not active'
contact_local_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:8060/contact || true)
[[ "$contact_local_code" =~ ^[1-4][0-9][0-9]$ ]] || fail "Contact local non-destructive request returned HTTP $contact_local_code"
new_kore_bind=${KORE_BIND%:$KORE_OLD_PORT}:$KORE_NEW_PORT
ss -H -ltn "sport = :$KORE_NEW_PORT" | awk -v b="$new_kore_bind" '$4==b {ok=1} END {exit !ok}' || fail "Kore target $new_kore_bind is not listening with its original bind scope"
port_free "$KORE_OLD_PORT" || fail 'Kore old port 4001 remains in use'
KORE_LOCAL_AFTER_CODE=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:8070/ || true)
[[ "$KORE_LOCAL_AFTER_CODE" == 200 ]] || fail "Kore post-restart backend http://127.0.0.1:8070/ returned HTTP $KORE_LOCAL_AFTER_CODE (expected 200)"
discover_listener KORE_MIGRATED 8070 || exit 1
[[ "$KORE_MIGRATED_SCOPE" == "$KORE_SCOPE" && "$KORE_MIGRATED_UID" == "$KORE_UID" && "$KORE_MIGRATED_USER" == "$KORE_USER" && "$KORE_MIGRATED_UNIT" == "$KORE_UNIT" ]] || fail "Kore port 8070: listener PIDs=${KORE_MIGRATED_PIDS[*]}; owner scope/user/UID/unit differs from preflight owner $KORE_SCOPE/$KORE_USER/$KORE_UID/$KORE_UNIT"

# Only after the new backend passes port, ownership, and HTTP checks do we switch Kore's proxy.
PHASE=kore-nginx-switch
for f in "${KORE_NGINX_FILES[@]}"; do replace_exact "$f" 'http://127.0.0.1:4001' 'http://127.0.0.1:8070'; done
nginx -t || fail 'nginx -t failed after Kore upstream update'
systemctl reload nginx
KORE_PUBLIC_AFTER_CODE=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 https://kore.lupricht.net/ || true)
[[ "$KORE_PUBLIC_AFTER_CODE" == 200 ]] || fail "Kore post-switch endpoint https://kore.lupricht.net/ returned HTTP $KORE_PUBLIC_AFTER_CODE (expected 200)"

PHASE=package-install
# Suppress any package-triggered service start. Restore the previous policy file
# byte-for-byte (or remove ours) regardless of dpkg outcome.
POLICY=/usr/sbin/policy-rc.d
POLICY_BACKUP="$BACKUP_DIR/policy-rc.d"
if [[ -e "$POLICY" ]]; then cp -a "$POLICY" "$BACKUP_DIR/policy-rc.d.present"; else : > "$BACKUP_DIR/policy-rc.d.absent"; fi
POLICY_TMP="$BACKUP_DIR/policy-rc.d.new"
cat > "$POLICY_TMP" <<'EOF'
#!/bin/sh
# Migration script: never start packaged daemons during dpkg installation.
exit 101
EOF
chmod 0755 "$POLICY_TMP"
install -o root -g root -m 0755 "$POLICY_TMP" "$POLICY"
restore_policy() {
  if [[ -f "$BACKUP_DIR/policy-rc.d.absent" ]]; then rm -f "$POLICY"; else cp -a "$BACKUP_DIR/policy-rc.d.present" "$POLICY"; fi
}
if ! dpkg -i "$PACKAGE"; then
  restore_policy
  fail 'dpkg installation failed; use recorded rollback after reviewing state'
fi
restore_policy
if [[ -f "$BACKUP_DIR/policy-rc.d.absent" ]]; then
  [[ ! -e "$POLICY" ]] || fail 'temporary policy-rc.d was not removed'
else
  cmp -s "$POLICY" "$BACKUP_DIR/policy-rc.d.present" || fail 'original policy-rc.d was not restored byte-for-byte'
fi
systemd-sysusers /usr/lib/sysusers.d/tparun.conf
systemd-tmpfiles --create /usr/lib/tmpfiles.d/tparun.conf

# Package service account and packaged paths are distinct. Copy while old TPA
# is stopped so SQLite/WAL and repository/signing state form one coherent set.
PHASE=tparun-data-adoption
scoped_systemctl "$TPA_SCOPE" "$TPA_USER" stop "$TPA_UNIT"
ss -H -ltn 'sport = :8082' | grep -q . && fail 'old TPA service did not release port 8082'
python3 - "$TPA_DATA/tpahub.db" "$BACKUP_DIR/database-state-before.json" <<'PY'
import sqlite3,json,sys
src,out=sys.argv[1:]
db=sqlite3.connect('file:'+src+'?mode=ro',uri=True)
assert db.execute('PRAGMA integrity_check').fetchone()[0]=='ok'
assert not db.execute('PRAGMA foreign_key_check').fetchall()
tables=['accounts','repositories','publish_jobs','repository_publications','product_grants']
counts={t:db.execute('SELECT COUNT(*) FROM "'+t+'"').fetchone()[0] for t in tables}
schema=db.execute('SELECT version FROM tpahub_schema WHERE singleton=1').fetchone()[0]
json.dump({'schema':schema,'counts':counts},open(out,'w'),sort_keys=True);db.close()
PY
[[ -d /var/lib/tparun/data && -d /var/lib/tparun/gnupg ]] || fail 'packaged tmpfiles did not create expected state directories'
[[ -z "$(find /var/lib/tparun/data -mindepth 1 -maxdepth 1 -print -quit)" && -z "$(find /var/lib/tparun/gnupg -mindepth 1 -maxdepth 1 -print -quit)" ]] || fail 'packaged state directories are not empty; refusing overwrite'
python3 - "$TPA_DATA" /var/lib/tparun/data "$TPA_GPG" /var/lib/tparun/gnupg <<'PY'
import os,shutil,stat,sys
for src,dst in ((sys.argv[1],sys.argv[2]),(sys.argv[3],sys.argv[4])):
    def ignore(directory,names):
        return [name for name in names if stat.S_ISSOCK(os.lstat(os.path.join(directory,name)).st_mode)]
    shutil.copytree(src,dst,symlinks=True,dirs_exist_ok=True,ignore=ignore)
PY
chown -R tparun:tparun /var/lib/tparun/data /var/lib/tparun/gnupg
runuser -u tparun -- env GNUPGHOME=/var/lib/tparun/gnupg gpg --batch --with-colons --list-secret-keys | awk -F: '$1=="fpr" {print $10}' | sort -u > "$BACKUP_DIR/signing-fingerprints-after-copy.txt"
cmp -s "$BACKUP_DIR/signing-fingerprints-before.txt" "$BACKUP_DIR/signing-fingerprints-after-copy.txt" || fail 'packaged signing key fingerprints differ from the live production identity'
# Refresh the pre-change rollback snapshot now that the old writer is stopped.
rm -rf "$BACKUP_DIR/data.pre-stop" "$BACKUP_DIR/gnupg.pre-stop"
python3 - "$TPA_DATA" "$BACKUP_DIR/data.pre-stop" "$TPA_GPG" "$BACKUP_DIR/gnupg.pre-stop" <<'PY'
import os,shutil,stat,sys
for src,dst in ((sys.argv[1],sys.argv[2]),(sys.argv[3],sys.argv[4])):
    def ignore(directory,names):
        return [name for name in names if stat.S_ISSOCK(os.lstat(os.path.join(directory,name)).st_mode)]
    shutil.copytree(src,dst,symlinks=True,ignore=ignore)
PY
# Keep only application configuration from the known live env; rewrite paths and
# bind, and discard login-session variables that do not belong in a system unit.
python3 - "$TPA_ENV_FILE" /etc/tparun/tparun.env /var/lib/tparun/data /var/lib/tparun/gnupg <<'PY'
import pathlib,re,sys,os
src,dst,data,gpg=sys.argv[1:]
keep=[]
for line in pathlib.Path(src).read_text().splitlines():
 s=line.strip()
 if not s or s.startswith('#') or '=' not in s: continue
 key=s.split('=',1)[0].strip().removeprefix('export ').strip()
 if key in {'HOME','USER','LOGNAME','PWD','SHELL','PATH','XDG_RUNTIME_DIR'}: continue
 if key.startswith('TPARUN_') or key in {'GNUPGHOME','TPA_BIN'}: keep.append(line)
values={}
for line in keep:
 m=re.match(r'^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$',line)
 if m: values[m.group(1)]=m.group(2)
values['TPARUN_DATA_DIR']=data
values['GNUPGHOME']=gpg
values['TPARUN_LISTEN']='127.0.0.1:9050'
# TPA_BIN must be readable/executable by the packaged identity; refuse implicit substitution.
tpa_bin=values.get('TPA_BIN','tpa')
if len(tpa_bin)>=2 and tpa_bin[0]==tpa_bin[-1] and tpa_bin[0] in "\\\"'": tpa_bin=tpa_bin[1:-1]
if tpa_bin.startswith('/') and not (os.path.isfile(tpa_bin) and os.access(tpa_bin,os.X_OK)):
 raise SystemExit('configured TPA_BIN is not executable; install/configure the matching TPA binary first')
pathlib.Path(dst).write_text(''.join(f'{k}={v}\n' for k,v in sorted(values.items())))
os.chmod(dst,0o600)
PY
chown root:tparun /etc/tparun/tparun.env
chmod 0640 /etc/tparun/tparun.env

PHASE=tparun-service-cutover
NEW_TPA_BIN=$(read_env_value /etc/tparun/tparun.env TPA_BIN)
if [[ "$NEW_TPA_BIN" == /* ]]; then
  runuser -u tparun -- test -x "$NEW_TPA_BIN" && runuser -u tparun -- test -r "$NEW_TPA_BIN" || fail 'packaged tparun account cannot execute/read configured TPA_BIN'
else
  runuser -u tparun -- sh -c 'command -v "$1" >/dev/null 2>&1' sh "$NEW_TPA_BIN" || fail 'configured TPA_BIN is not available in packaged service PATH'
fi
systemctl daemon-reload
# Candidate must be the declared revision before it can receive production state.
[[ "$(sha256sum /usr/bin/tparun | awk '{print $1}')" != "" ]] || fail 'packaged tparun binary missing'
TPARUN_DATA_DIR=/var/lib/tparun/data /usr/bin/tparun diagnostics > "$BACKUP_DIR/tparun-candidate-diagnostics.json"
python3 - "$BACKUP_DIR/tparun-candidate-diagnostics.json" "$PACKAGE_REV" <<'PY'
import json,sys
x=json.load(open(sys.argv[1]));assert x.get('revision')==sys.argv[2] and x.get('schema')==11
PY
systemctl enable tparun.service >/dev/null
systemctl start tparun.service
for _ in $(seq 1 60); do
  if curl -fsS --max-time 2 http://127.0.0.1:9050/ready > "$BACKUP_DIR/ready-local.json" 2>/dev/null; then break; fi
  sleep 1
done
python3 - "$BACKUP_DIR/ready-local.json" <<'PY'
import json,sys
assert json.load(open(sys.argv[1]))=={'ready':True,'schema':11}
PY
curl -fsS --max-time 4 http://127.0.0.1:9050/version > "$BACKUP_DIR/version-local.json"
python3 - "$BACKUP_DIR/version-local.json" "$PACKAGE_REV" <<'PY'
import json,sys
x=json.load(open(sys.argv[1]));assert x.get('revision')==sys.argv[2] and x.get('schema')=='11' and x.get('service')=='tparun'
PY
python3 - /var/lib/tparun/data/tpahub.db "$BACKUP_DIR/database-state-before.json" "$BACKUP_DIR/database-state-after.json" <<'PY'
import sqlite3,json,sys
path,baseline,out=sys.argv[1:];old=json.load(open(baseline))
db=sqlite3.connect('file:'+path+'?mode=ro',uri=True)
assert db.execute('PRAGMA integrity_check').fetchone()[0]=='ok'
assert not db.execute('PRAGMA foreign_key_check').fetchall()
counts={t:db.execute('SELECT COUNT(*) FROM "'+t+'"').fetchone()[0] for t in old['counts']}
schema=db.execute('SELECT version FROM tpahub_schema WHERE singleton=1').fetchone()[0]
assert schema==old['schema'] and counts==old['counts'],(schema,counts,old)
json.dump({'schema':schema,'counts':counts},open(out,'w'),sort_keys=True)
db.close()
PY

# Change TPA proxy only after the new backend is ready, then validate before reload.
PHASE=nginx-tparun-switch
for f in "${TPA_NGINX_FILES[@]}"; do replace_exact "$f" 'http://127.0.0.1:8082' 'http://127.0.0.1:9050'; done
nginx -t
systemctl reload nginx

PHASE=production-verification
curl -fsS --max-time 10 https://tpa.run/ready > "$BACKUP_DIR/ready-public.json"
python3 - "$BACKUP_DIR/ready-public.json" <<'PY'
import json,sys
assert json.load(open(sys.argv[1]))=={'ready':True,'schema':11}
PY
curl -fsS --max-time 10 https://tpa.run/version > "$BACKUP_DIR/version-public.json"
python3 - "$BACKUP_DIR/version-public.json" "$PACKAGE_REV" <<'PY'
import json,sys
x=json.load(open(sys.argv[1]));assert x.get('revision')==sys.argv[2] and x.get('schema')=='11'
PY
# Exercise the exact installed CLI behavior without touching production config.
/usr/bin/tparun > "$BACKUP_DIR/no-args-help.txt"
grep -q 'TPA.run CLI (tparun)' "$BACKUP_DIR/no-args-help.txt"
grep -Fq 'ExecStart=/usr/bin/tparun serve' /lib/systemd/system/tparun.service
[[ "$(systemctl is-active tparun.service)" == active ]]
socket_has 9050 127.0.0.1 || fail 'packaged TPA is not listening on loopback 9050'
discover_listener PACKAGED_TPA 9050 || exit 1
[[ "$PACKAGED_TPA_SCOPE" == system && "$PACKAGED_TPA_UNIT" == tparun.service ]] || fail "packaged TPA port 9050: listener PIDs=${PACKAGED_TPA_PIDS[*]}; resolved unit=$PACKAGED_TPA_UNIT scope=$PACKAGED_TPA_SCOPE user=$PACKAGED_TPA_USER UID=$PACKAGED_TPA_UID, expected system/tparun.service"
ss -H -ltn 'sport = :22' | grep -q . || fail 'SSH port 22 is no longer listening'
ss -H -ltn "sport = :$LLAMA_PORT" | grep -q . || fail 'llama.cpp listener 8003 was lost'
port_free 8085 || fail 'legacy llama port 8085 became occupied'
[[ "$(ss -H -ltn 'sport = :8080' | awk '{print $4}' | sort)" == "$UNKNOWN_8080" ]] || fail 'unknown port 8080 listener changed'
[[ "$(ss -H -ltn 'sport = :8081' | awk '{print $4}' | sort)" == "$UNKNOWN_8081" ]] || fail 'unknown port 8081 listener changed'
port_free 8082 || fail 'old TPA port 8082 remains in use'
# Non-destructive endpoint checks. No contact submission is sent.
contact_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 https://lupricht.net/contact || true)
[[ "$contact_code" =~ ^[1-4][0-9][0-9]$ ]] || fail "Contact proxy returned HTTP $contact_code"
kore_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 https://kore.lupricht.net/ || true)
[[ "$kore_code" == 200 ]] || fail "Kore proxy https://kore.lupricht.net/ returned HTTP $kore_code (expected 200)"

# Disable the old service only after all production endpoints and process identity pass.
scoped_systemctl "$TPA_SCOPE" "$TPA_USER" disable "$TPA_UNIT"
userctl "$TPA_USER" daemon-reload

PHASE=final-audit
{
  printf 'migration_state=complete\ncompleted_at=%s\n' "$(date -u +%FT%TZ)"
  printf '\nTCP/UDP LISTENERS\n'; ss -lntup
  printf '\nSYSTEM SERVICES\n'; systemctl list-units --no-pager --plain --type=service --state=running
  printf '\nUSER SERVICES (owner managers)\n'
  for manager_user in "$TPA_USER" "$CONTACT_USER" "$KORE_USER"; do
    [[ -n "$manager_user" && "$manager_user" != - ]] || continue
    printf '\nmanager_user=%s\n' "$manager_user"
    userctl "$manager_user" list-units --no-pager --plain --type=service --state=running
  done
  printf '\nTPA VERSION\n'; cat "$BACKUP_DIR/version-public.json"
  printf '\nCONTACT\n'; cat "$BACKUP_DIR/contact-state-before.txt"
  printf '\nPACKAGE\n'; dpkg-query -W -f='${Package} ${Version} ${db:Status-Abbrev}\n' tparun
  printf '\nNGINX UPSTREAMS\n'; nginx -T 2>&1 | grep -E 'proxy_pass.*(9050|8060|8070|8080|8081|3000|4000)'
  printf '\nDOCKER PUBLISHED PORTS\n'
  if command -v docker >/dev/null 2>&1; then docker ps --no-trunc --format '{{.Names}} {{.Ports}}' 2>&1 || true; docker compose ls --all --format json 2>&1 || true; else printf 'docker command unavailable\n'; fi
  printf '\nFIREWALL PORT RULES\n'
  if command -v nft >/dev/null 2>&1; then nft list ruleset 2>&1 || true; elif command -v iptables-save >/dev/null 2>&1; then iptables-save 2>&1 || true; elif command -v ufw >/dev/null 2>&1; then ufw status verbose 2>&1 || true; else printf 'no nft/iptables/ufw inspection tool available\n'; fi
} > "$BACKUP_DIR/final-audit.txt"
sed -i 's/^migration_state=started$/migration_state=complete/' "$BACKUP_DIR/manifest.txt"
printf '\nAPPLY PASS\nRollback directory: %s\n' "$BACKUP_DIR"
printf 'Contact: 8083 -> 8060; status: already migrated before this apply run\n'
printf 'Rollback command (through the approved privilege workflow): %q --rollback %q\n' "$0" "$BACKUP_DIR"
