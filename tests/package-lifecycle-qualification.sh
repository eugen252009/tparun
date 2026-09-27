#!/usr/bin/env bash
set -Eeuo pipefail

# Disposable local-Docker package qualification. This never targets NAS Docker:
# the selected context must be a local Unix socket. It builds a signed temporary
# APT repository containing the previous beta and candidate packages, then
# qualifies fresh no-systemd install, beta upgrade, and systemd integration.

BETA1_DEB=${1:?usage: TPARUN_BUILD_REVISION=... TPA_BIN=/path/to/tpa $0 beta1.deb beta2.deb}
BETA2_DEB=${2:?usage: TPARUN_BUILD_REVISION=... TPA_BIN=/path/to/tpa $0 beta1.deb beta2.deb}
EXPECTED_REVISION=${TPARUN_BUILD_REVISION:?set TPARUN_BUILD_REVISION to the exact package build identity}
TPA_BIN=${TPA_BIN:-$(command -v tpa || true)}
STABLE_KEY=${TPARUN_STABLE_KEY:-/usr/share/keyrings/tparun-archive-keyring.gpg}
DOCKER=${DOCKER:-docker}

[[ -f "$BETA1_DEB" && -f "$BETA2_DEB" && -r "$STABLE_KEY" && -x "$TPA_BIN" ]]
[[ "$(dpkg-deb -f "$BETA1_DEB" Package)" == tparun ]]
[[ "$(dpkg-deb -f "$BETA1_DEB" Version)" == '0.1.5~beta1' ]]
[[ "$(dpkg-deb -f "$BETA2_DEB" Package)" == tparun ]]
[[ "$(dpkg-deb -f "$BETA2_DEB" Version)" == '0.1.5~beta2' ]]
[[ "$(dpkg-deb -f "$BETA2_DEB" Architecture)" == amd64 ]]

context=$($DOCKER context show)
endpoint=$($DOCKER context inspect --format '{{.Endpoints.docker.Host}}' "$context")
case "$endpoint" in
  unix:///var/run/docker.sock|unix:///run/docker.sock) ;;
  *) echo "refusing non-local Docker context: $endpoint" >&2; exit 2 ;;
esac
if [[ ${TPARUN_QUALIFICATION_HOST:-} != local ]]; then
  echo 'set TPARUN_QUALIFICATION_HOST=local to confirm disposable local Docker use (not NAS)' >&2
  exit 2
fi

work=$(mktemp -d /tmp/tparun-package-qualification.XXXXXX)
repo_in="$work/input"
mkdir -m 0755 -p "$repo_in"
containers=()
images=()
cleanup() {
  local name image
  for name in "${containers[@]:-}"; do "$DOCKER" rm -f "$name" >/dev/null 2>&1 || true; done
  for image in "${images[@]:-}"; do "$DOCKER" image rm "$image" >/dev/null 2>&1 || true; done
  rm -rf -- "$work"
}
trap cleanup EXIT

install -m 0644 "$BETA1_DEB" "$repo_in/tparun_0.1.5~beta1_amd64.deb"
install -m 0644 "$BETA2_DEB" "$repo_in/tparun_0.1.5~beta2_amd64.deb"
control="$work/package-control"
root="$work/package-root"
mkdir -m 0755 "$control" "$root"
dpkg-deb -e "$BETA2_DEB" "$control"
dpkg-deb -x "$BETA2_DEB" "$root"
depends=$(dpkg-deb -f "$BETA2_DEB" Depends)
if grep -Eq '(^|[,[:space:]])systemd([,[:space:]]|$)' <<<"$depends"; then
  echo "tparun has a hard systemd dependency: $depends" >&2
  exit 1
fi
for maintainer_script in "$control"/preinst "$control"/postinst "$control"/prerm "$control"/postrm; do
  [[ -f "$maintainer_script" ]] || continue
  if grep -Eq 'systemd-sysusers|systemd-tmpfiles|systemctl|deb-systemd-invoke' "$maintainer_script"; then
    echo "unexpected systemd lifecycle call in $maintainer_script" >&2
    exit 1
  fi
done
unit="$root/lib/systemd/system/tparun.service"
for setting in 'User=tparun' 'Group=tparun' 'WorkingDirectory=/var/lib/tparun' 'EnvironmentFile=/etc/tparun/tparun.env' 'ExecStart=/usr/bin/tparun serve' 'Restart=on-failure'; do
  grep -Fxq "$setting" "$unit"
done
[[ "$(stat -c %a "$root/etc/tparun/tparun.env")" == 640 ]]
grep -Fxq '/etc/tparun/tparun.env' "$control/conffiles"
printf 'PACKAGE_METADATA_AND_OPTIONAL_UNIT=PASS\n'
mkdir -m 0700 "$work/gnupg"
gpg --batch --homedir "$work/gnupg" --pinentry-mode loopback --passphrase '' \
  --quick-generate-key 'TPA.run disposable package qualification <package-qualification@example.invalid>' rsa3072 sign 0 >/dev/null
test_fpr=$(gpg --batch --homedir "$work/gnupg" --with-colons --list-secret-keys | awk -F: '$1=="fpr" {print $10; exit}')
GNUPGHOME="$work/gnupg" "$TPA_BIN" pack -in="$repo_in" -out="$work/repo" \
  -origin='TPA.run Qualification' -label='Disposable package qualification' \
  -suite=beta -components=main -codename=beta -gpg="$test_fpr"
gpg --batch --homedir "$work/gnupg" --export "$test_fpr" > "$work/beta-qualification.gpg"

"$DOCKER" pull debian:trixie >/dev/null
new_container() {
  local name=$1 image=${2:-debian:trixie} privileged=${3:-no}
  local -a extra=()
  if [[ $privileged == yes ]]; then
    extra+=(--privileged --cgroupns=host --tmpfs /run --tmpfs /run/lock -v /sys/fs/cgroup:/sys/fs/cgroup:rw)
  fi
  containers+=("$name")
  if [[ $privileged == yes ]]; then
    "$DOCKER" run -d --name "$name" --label tpahub.tparun.package-qualification=true \
      "${extra[@]}" \
      -v "$work/repo:/qualification/repo:ro" \
      -v "$work/beta-qualification.gpg:/qualification/beta-qualification.gpg:ro" \
      -v "$STABLE_KEY:/qualification/stable-key.gpg:ro" \
      "$image" >/dev/null
  else
    "$DOCKER" run -d --name "$name" --label tpahub.tparun.package-qualification=true \
      -v "$work/repo:/qualification/repo:ro" \
      -v "$work/beta-qualification.gpg:/qualification/beta-qualification.gpg:ro" \
      -v "$STABLE_KEY:/qualification/stable-key.gpg:ro" \
      --entrypoint /bin/sh "$image" -c 'exec sleep infinity' >/dev/null
  fi
}

prepare_apt() {
  local name=$1
  "$DOCKER" exec "$name" sh -ec 'apt-get update; DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl procps'
  "$DOCKER" exec "$name" sh -ec 'install -m 0644 /qualification/beta-qualification.gpg /usr/share/keyrings/tparun-qualification.gpg; install -m 0644 /qualification/stable-key.gpg /usr/share/keyrings/tparun-archive-keyring.gpg; cat > /etc/apt/sources.list.d/tparun-qualification.sources <<"EOF"
Types: deb
URIs: file:/qualification/repo
Suites: beta
Components: main
Signed-By: /usr/share/keyrings/tparun-qualification.gpg

Types: deb
URIs: https://tpa.run/r/tparun/bootstrap
Suites: stable
Components: main
Signed-By: /usr/share/keyrings/tparun-archive-keyring.gpg
EOF
apt-get update'
  local policy
  policy=$("$DOCKER" exec "$name" apt-cache policy tparun)
  printf '%s\n' "$policy"
  grep -Fq '0.1.5~beta2' <<<"$policy" || { echo 'APT policy lacks beta2' >&2; return 1; }
}

assert_account_and_paths() {
  local name=$1
  "$DOCKER" exec "$name" sh -ec '
    uid=$(id -u tparun); gid=$(getent group tparun | cut -d: -f3)
    test "$(getent passwd tparun | cut -d: -f4)" = "$gid"
    test "$(stat -c %u:%g:%a /var/lib/tparun)" = "$uid:$gid:750"
    test "$(stat -c %u:%g:%a /var/lib/tparun/data)" = "$uid:$gid:700"
    test "$(stat -c %u:%g:%a /var/lib/tparun/gnupg)" = "$uid:$gid:700"
    test "$(stat -c %u:%g:%a /etc/tparun)" = "0:$gid:750"
    test "$(stat -c %u:%g:%a /etc/tparun/tparun.env)" = "0:$gid:640"
  '
}

assert_legacy_account_and_state() {
  local name=$1
  "$DOCKER" exec "$name" sh -ec '
    uid=$(id -u tparun); gid=$(getent group tparun | cut -d: -f3)
    test "$(getent passwd tparun | cut -d: -f4)" = "$gid"
    test "$(stat -c %u:%g:%a /var/lib/tparun)" = "$uid:$gid:750"
    test "$(stat -c %u:%g:%a /var/lib/tparun/data)" = "$uid:$gid:700"
    test "$(stat -c %u:%g:%a /var/lib/tparun/gnupg)" = "$uid:$gid:700"
    test -f /etc/tparun/tparun.env
  '
}

start_direct() {
  local name=$1 expected=$2 expected_version=${3:-0.1.5~beta2}
  "$DOCKER" exec -d --user tparun "$name" sh -c \
    'set -a; . /etc/tparun/tparun.env; set +a; exec /usr/bin/tparun serve >>/tmp/tparun-qualification.log 2>&1'
  local ready=0 i body
  for i in $(seq 1 100); do
    body=$("$DOCKER" exec "$name" curl -fsS --max-time 2 http://127.0.0.1:8082/ready 2>/dev/null || true)
    if grep -Fq '"ready":true' <<<"$body" && grep -Fq '"schema":11' <<<"$body"; then ready=1; break; fi
    sleep 0.25
  done
  [[ $ready == 1 ]] || { "$DOCKER" exec "$name" sh -c 'cat /tmp/tparun-qualification.log' >&2 || true; return 1; }
  body=$("$DOCKER" exec "$name" curl -fsS http://127.0.0.1:8082/version)
  grep -Fq "\"version\":\"$expected_version\"" <<<"$body"
  if [[ $expected_version == '0.1.5~beta2' ]]; then grep -Fq "\"revision\":\"$expected\"" <<<"$body"; fi
}

stop_direct() {
  local name=$1 i
  "$DOCKER" exec "$name" pkill -TERM -x tparun >/dev/null 2>&1 || true
  for i in $(seq 1 40); do
    if ! "$DOCKER" exec "$name" curl -fsS --max-time 1 http://127.0.0.1:8082/ready >/dev/null 2>&1; then return 0; fi
    sleep 0.25
  done
  echo "tparun did not stop in $name" >&2
  return 1
}

wait_ready_systemd() {
  local name=$1 i body
  for i in $(seq 1 100); do
    body=$("$DOCKER" exec "$name" curl -fsS --max-time 2 http://127.0.0.1:8082/ready 2>/dev/null || true)
    if grep -Fq '"ready":true' <<<"$body" && grep -Fq '"schema":11' <<<"$body"; then return 0; fi
    sleep 0.25
  done
  "$DOCKER" exec "$name" systemctl status --no-pager tparun.service >&2 || true
  "$DOCKER" exec "$name" journalctl -u tparun.service --no-pager -n 80 >&2 || true
  return 1
}

apt_upgrade() {
  local name=$1
  "$DOCKER" exec "$name" sh -ec 'apt-get update && DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade -y'
}

# Fresh APT install and runtime with neither systemd package nor systemd PID 1.
no_systemd="tparun-nonsystemd-qual-$$-$RANDOM"
new_container "$no_systemd"
prepare_apt "$no_systemd"
"$DOCKER" exec "$no_systemd" sh -ec 'apt-cache policy tparun | grep -F "0.1.5~beta2"; DEBIAN_FRONTEND=noninteractive apt-get install -y tparun'
[[ "$("$DOCKER" exec "$no_systemd" dpkg-query -W -f='${Version}' tparun)" == '0.1.5~beta2' ]]
"$DOCKER" exec "$no_systemd" sh -ec 'test "$(cat /proc/1/comm)" != systemd; if dpkg-query -W -f="\${db:Status-Status}" systemd 2>/dev/null | grep -qx installed; then exit 1; fi; if command -v systemctl >/dev/null 2>&1; then exit 1; fi; test -z "$(dpkg --audit)"; apt-get check'
assert_account_and_paths "$no_systemd"
start_direct "$no_systemd" "$EXPECTED_REVISION"
"$DOCKER" exec --user tparun "$no_systemd" sh -ec 'printf "persistent-data\n" > /var/lib/tparun/data/qualification-marker; printf "test-only-identity\n" > /var/lib/tparun/gnupg/qualification-marker'
"$DOCKER" exec "$no_systemd" sh -ec 'printf "\nTPARUN_QUALIFICATION_MARKER=preserve-me\n" >> /etc/tparun/tparun.env; sha256sum /etc/tparun/tparun.env | cut -d" " -f1 > /tmp/config.sha'
config_hash=$("$DOCKER" exec "$no_systemd" cat /tmp/config.sha)
stop_direct "$no_systemd"
"$DOCKER" restart "$no_systemd" >/dev/null
start_direct "$no_systemd" "$EXPECTED_REVISION"
"$DOCKER" exec "$no_systemd" sh -ec 'test "$(cat /var/lib/tparun/data/qualification-marker)" = persistent-data; test "$(cat /var/lib/tparun/gnupg/qualification-marker)" = test-only-identity'
"$DOCKER" exec "$no_systemd" pkill -TERM -x tparun >/dev/null 2>&1 || true
"$DOCKER" exec "$no_systemd" sh -ec 'DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold remove -y tparun; test -f /var/lib/tparun/data/qualification-marker; test -f /etc/tparun/tparun.env; test "$(sha256sum /etc/tparun/tparun.env | cut -d" " -f1)" = "'"$config_hash"'"; DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold install -y tparun=0.1.5~beta2; apt-get check'
assert_account_and_paths "$no_systemd"
start_direct "$no_systemd" "$EXPECTED_REVISION"
"$DOCKER" exec "$no_systemd" sh -ec 'test "$(cat /var/lib/tparun/data/qualification-marker)" = persistent-data; test "$(cat /var/lib/tparun/gnupg/qualification-marker)" = test-only-identity'
printf 'NON_SYSTEMD_FRESH_INSTALL=PASS\nNON_SYSTEMD_RUNTIME=PASS\nNON_SYSTEMD_REMOVE_REINSTALL_PERSISTENCE=PASS\n'

# Preserve an already-created compatible dynamic account.
compatible="tparun-compatible-account-qual-$$-$RANDOM"
new_container "$compatible"
prepare_apt "$compatible"
"$DOCKER" exec "$compatible" sh -ec 'DEBIAN_FRONTEND=noninteractive apt-get install -y adduser'
"$DOCKER" exec "$compatible" sh -ec 'addgroup --system --quiet tparun; adduser --system --quiet --ingroup tparun --home /var/lib/tparun --no-create-home --shell /usr/sbin/nologin --disabled-login --disabled-password --gecos "existing qualification account" tparun'
account_before=$("$DOCKER" exec "$compatible" sh -ec 'printf "%s:%s\\n" "$(id -u tparun)" "$(getent group tparun | cut -d: -f3)"')
"$DOCKER" exec "$compatible" sh -ec 'DEBIAN_FRONTEND=noninteractive apt-get install -y tparun=0.1.5~beta2'
account_after=$("$DOCKER" exec "$compatible" sh -ec 'printf "%s:%s\\n" "$(id -u tparun)" "$(getent group tparun | cut -d: -f3)"')
[[ "$account_before" == "$account_after" ]]
assert_account_and_paths "$compatible"
printf 'COMPATIBLE_ACCOUNT_PRESERVED=PASS\n'

# An account with a conflicting primary group is rejected, not rewritten.
incompatible="tparun-incompatible-account-qual-$$-$RANDOM"
new_container "$incompatible"
prepare_apt "$incompatible"
"$DOCKER" exec "$incompatible" sh -ec 'DEBIAN_FRONTEND=noninteractive apt-get install -y adduser'
"$DOCKER" exec "$incompatible" sh -ec 'addgroup --system --quiet tparun; adduser --system --quiet --ingroup nogroup --home /var/lib/tparun --no-create-home --shell /usr/sbin/nologin --disabled-login --disabled-password --gecos "incompatible qualification account" tparun'
set +e
conflict_output=$("$DOCKER" exec "$incompatible" sh -c 'DEBIAN_FRONTEND=noninteractive apt-get install -y tparun=0.1.5~beta2' 2>&1)
conflict_status=$?
set -e
[[ $conflict_status -ne 0 ]]
grep -Fq 'existing user tparun has primary GID' <<<"$conflict_output"
printf 'INCOMPATIBLE_ACCOUNT_REJECTED=PASS\n'

# A conflicting state root is rejected without changing ownership or contents.
state_conflict="tparun-incompatible-state-qual-$$-$RANDOM"
new_container "$state_conflict"
prepare_apt "$state_conflict"
"$DOCKER" exec "$state_conflict" sh -ec 'mkdir -m 0755 /var/lib/tparun; printf "keep\\n" > /var/lib/tparun/sentinel'
set +e
state_output=$("$DOCKER" exec "$state_conflict" sh -c 'DEBIAN_FRONTEND=noninteractive apt-get install -y tparun=0.1.5~beta2' 2>&1)
state_status=$?
set -e
[[ $state_status -ne 0 ]]
grep -Fq '/var/lib/tparun is owned 0:0' <<<"$state_output"
"$DOCKER" exec "$state_conflict" sh -ec 'test "$(cat /var/lib/tparun/sentinel)" = keep; test "$(stat -c %u:%g /var/lib/tparun)" = 0:0'
printf 'INCOMPATIBLE_STATE_OWNERSHIP_REJECTED=PASS\n'

# Legacy beta1 upgrade with a non-systemd PID 1. beta1 itself pulls the systemd
# package, so a separate fresh-install test above proves beta2 does not.
upgrade="tparun-beta-upgrade-qual-$$-$RANDOM"
new_container "$upgrade"
prepare_apt "$upgrade"
"$DOCKER" exec "$upgrade" sh -ec 'DEBIAN_FRONTEND=noninteractive apt-get install -y tparun=0.1.5~beta1 curl procps'
[[ "$("$DOCKER" exec "$upgrade" cat /proc/1/comm)" != systemd ]]
[[ "$("$DOCKER" exec "$upgrade" dpkg-query -W -f='${Version}' tparun)" == '0.1.5~beta1' ]]
assert_legacy_account_and_state "$upgrade"
start_direct "$upgrade" "$EXPECTED_REVISION" '0.1.5~beta1'
"$DOCKER" exec --user tparun "$upgrade" sh -ec 'printf "upgrade-data\n" > /var/lib/tparun/data/upgrade-marker; printf "test-only-identity\n" > /var/lib/tparun/gnupg/qualification-marker'
"$DOCKER" exec "$upgrade" sh -ec 'printf "\nTPARUN_UPGRADE_MARKER=preserve-me\n" >> /etc/tparun/tparun.env; sha256sum /etc/tparun/tparun.env | cut -d" " -f1 > /tmp/config.sha'
upgrade_config_hash=$("$DOCKER" exec "$upgrade" cat /tmp/config.sha)
apt_upgrade "$upgrade"
[[ "$("$DOCKER" exec "$upgrade" dpkg-query -W -f='${Version}' tparun)" == '0.1.5~beta2' ]]
[[ "$("$DOCKER" exec "$upgrade" cat /proc/1/comm)" != systemd ]]
# The already-running process may still be executing beta1's unlinked inode.
"$DOCKER" exec "$upgrade" sh -ec 'curl -fsS http://127.0.0.1:8082/ready >/dev/null; test "$(cat /var/lib/tparun/data/upgrade-marker)" = upgrade-data; test "$(cat /var/lib/tparun/gnupg/qualification-marker)" = test-only-identity; test "$(sha256sum /etc/tparun/tparun.env | cut -d" " -f1)" = "'"$upgrade_config_hash"'"'
legacy_version=$("$DOCKER" exec "$upgrade" curl -fsS http://127.0.0.1:8082/version)
grep -Fq '"version":"0.1.5~beta1"' <<<"$legacy_version"
stop_direct "$upgrade"
start_direct "$upgrade" "$EXPECTED_REVISION"
printf 'NON_SYSTEMD_PID1_BETA_UPGRADE=PASS\n'

# Disposable systemd integration. Build a separate image; it is not reused by
# the non-systemd path and is removed with the qualification run.
systemd_image="tparun-systemd-qual:$$-$RANDOM"
images+=("$systemd_image")
mkdir -m 0755 "$work/systemd-image"
cat > "$work/systemd-image/Dockerfile" <<'EOF'
FROM debian:trixie
ENV container=docker DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends systemd systemd-sysv dbus ca-certificates curl procps && rm -rf /var/lib/apt/lists/*
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
EOF
"$DOCKER" build -q -t "$systemd_image" "$work/systemd-image" >/dev/null
systemd="tparun-systemd-qual-$$-$RANDOM"
new_container "$systemd" "$systemd_image" yes
systemd_ready=0
for _ in $(seq 1 120); do
  state=$("$DOCKER" exec "$systemd" systemctl is-system-running 2>/dev/null || true)
  if [[ $state == running || $state == degraded ]]; then systemd_ready=1; break; fi
  sleep 0.5
done
[[ $systemd_ready == 1 && "$("$DOCKER" exec "$systemd" cat /proc/1/comm)" == systemd ]]
prepare_apt "$systemd"
"$DOCKER" exec "$systemd" sh -ec 'DEBIAN_FRONTEND=noninteractive apt-get install -y tparun=0.1.5~beta2'
assert_account_and_paths "$systemd"
"$DOCKER" exec "$systemd" systemctl enable --now tparun.service
"$DOCKER" exec "$systemd" systemctl is-active --quiet tparun.service
"$DOCKER" exec "$systemd" systemctl is-enabled --quiet tparun.service
"$DOCKER" exec "$systemd" systemctl show tparun.service -p User -p Group -p WorkingDirectory -p EnvironmentFiles --no-pager
wait_ready_systemd "$systemd"
"$DOCKER" exec "$systemd" curl -fsS http://127.0.0.1:8082/version | grep -Fq "\"revision\":\"$EXPECTED_REVISION\""
"$DOCKER" exec "$systemd" systemctl restart tparun.service
"$DOCKER" exec "$systemd" systemctl is-active --quiet tparun.service
wait_ready_systemd "$systemd"
"$DOCKER" exec --user tparun "$systemd" sh -ec 'printf "boot-persistent-data\\n" > /var/lib/tparun/data/boot-marker'
"$DOCKER" restart "$systemd" >/dev/null
systemd_ready=0
for _ in $(seq 1 120); do
  state=$("$DOCKER" exec "$systemd" systemctl is-system-running 2>/dev/null || true)
  if [[ $state == running || $state == degraded ]]; then systemd_ready=1; break; fi
  sleep 0.5
done
[[ $systemd_ready == 1 && "$("$DOCKER" exec "$systemd" cat /proc/1/comm)" == systemd ]]
"$DOCKER" exec "$systemd" systemctl is-active --quiet tparun.service
"$DOCKER" exec "$systemd" systemctl is-enabled --quiet tparun.service
wait_ready_systemd "$systemd"
"$DOCKER" exec "$systemd" sh -ec 'test "$(cat /var/lib/tparun/data/boot-marker)" = boot-persistent-data'
printf 'SYSTEMD_FRESH_INSTALL_ENABLE_START_RESTART_BOOT=PASS\n'

# Upgrade an active service from beta1 to beta2 with ordinary APT upgrade.
"$DOCKER" exec "$systemd" systemctl stop tparun.service
"$DOCKER" exec "$systemd" sh -ec 'DEBIAN_FRONTEND=noninteractive apt-get remove -y tparun; DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold install -y tparun=0.1.5~beta1'
"$DOCKER" exec "$systemd" systemctl daemon-reload
"$DOCKER" exec "$systemd" systemctl enable --now tparun.service
wait_ready_systemd "$systemd"
"$DOCKER" exec --user tparun "$systemd" sh -ec 'printf "systemd-upgrade-data\n" > /var/lib/tparun/data/upgrade-marker'
"$DOCKER" exec "$systemd" sh -ec 'printf "\nTPARUN_SYSTEMD_MARKER=preserve-me\n" >> /etc/tparun/tparun.env; sha256sum /etc/tparun/tparun.env | cut -d" " -f1 > /tmp/config.sha'
systemd_config_hash=$("$DOCKER" exec "$systemd" cat /tmp/config.sha)
apt_upgrade "$systemd"
[[ "$("$DOCKER" exec "$systemd" dpkg-query -W -f='${Version}' tparun)" == '0.1.5~beta2' ]]
"$DOCKER" exec "$systemd" systemctl is-active --quiet tparun.service
"$DOCKER" exec "$systemd" systemctl restart tparun.service
"$DOCKER" exec "$systemd" systemctl is-active --quiet tparun.service
"$DOCKER" exec "$systemd" systemctl is-enabled --quiet tparun.service
wait_ready_systemd "$systemd"
"$DOCKER" exec "$systemd" curl -fsS http://127.0.0.1:8082/version | grep -Fq "\"revision\":\"$EXPECTED_REVISION\""
"$DOCKER" exec "$systemd" sh -ec 'test "$(cat /var/lib/tparun/data/upgrade-marker)" = systemd-upgrade-data; test "$(sha256sum /etc/tparun/tparun.env | cut -d" " -f1)" = "'"$systemd_config_hash"'"'
printf 'SYSTEMD_BETA_UPGRADE_RESTART_PERSISTENCE=PASS\n'

# Removal/reinstall must preserve state and config; explicitly stop first.
"$DOCKER" exec "$systemd" systemctl stop tparun.service
"$DOCKER" exec "$systemd" sh -ec 'DEBIAN_FRONTEND=noninteractive apt-get remove -y tparun; test -f /var/lib/tparun/data/upgrade-marker; test -f /etc/tparun/tparun.env; DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold install -y tparun=0.1.5~beta2; systemctl daemon-reload; systemctl enable --now tparun.service; apt-get check'
"$DOCKER" exec "$systemd" systemctl is-active --quiet tparun.service
wait_ready_systemd "$systemd"
printf 'SYSTEMD_REMOVE_REINSTALL_PERSISTENCE=PASS\n'
printf 'PACKAGE_LIFECYCLE_QUALIFICATION=PASS\n'
