#!/usr/bin/env bash
set -Eeuo pipefail

# Local-only qualification for Beta2 -> Beta3 and fresh Docker named volumes.
# Containers, volumes, and prebuilt images are disposable and never target NAS.
BETA2_DEB=${1:?usage: TPARUN_BUILD_REVISION=... TPA_BIN=/path/to/tpa $0 beta2.deb beta3.deb}
BETA3_DEB=${2:?usage: TPARUN_BUILD_REVISION=... TPA_BIN=/path/to/tpa $0 beta2.deb beta3.deb}
EXPECTED_REVISION=${TPARUN_BUILD_REVISION:?set the exact Beta3 build identity}
TPA_BIN=${TPA_BIN:-$(command -v tpa || true)}
DOCKER=${DOCKER:-docker}
[[ -f "$BETA2_DEB" && -f "$BETA3_DEB" && -x "$TPA_BIN" ]]
TPA_BIN=$(realpath "$TPA_BIN")
[[ "$(dpkg-deb -f "$BETA2_DEB" Package)" == tparun ]]
[[ "$(dpkg-deb -f "$BETA2_DEB" Version)" == '0.1.5~beta2' ]]
[[ "$(dpkg-deb -f "$BETA3_DEB" Package)" == tparun ]]
[[ "$(dpkg-deb -f "$BETA3_DEB" Version)" == '0.1.5~beta3' ]]
[[ "$(dpkg-deb -f "$BETA3_DEB" Architecture)" == amd64 ]]

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

work=$(mktemp -d /var/tmp/tparun-beta3-qualification.XXXXXX)
input="$work/packages"
mkdir -m 0755 "$input"
containers=()
volumes=()
images=()
cleanup() {
  local item
  for item in "${containers[@]:-}"; do "$DOCKER" rm -f "$item" >/dev/null 2>&1 || true; done
  for item in "${volumes[@]:-}"; do "$DOCKER" volume rm "$item" >/dev/null 2>&1 || true; done
  for item in "${images[@]:-}"; do "$DOCKER" image rm "$item" >/dev/null 2>&1 || true; done
  rm -rf -- "$work"
}
trap cleanup EXIT

install -m 0644 "$BETA2_DEB" "$input/tparun_0.1.5~beta2_amd64.deb"
install -m 0644 "$BETA3_DEB" "$input/tparun_0.1.5~beta3_amd64.deb"
control="$work/control"
root="$work/root"
mkdir -m 0755 "$control" "$root"
dpkg-deb -e "$BETA3_DEB" "$control"
dpkg-deb -x "$BETA3_DEB" "$root"
depends=$(dpkg-deb -f "$BETA3_DEB" Depends)
if grep -Eq '(^|[,[:space:]])systemd([,[:space:]]|$)' <<<"$depends"; then
  echo "beta3 has a hard systemd dependency: $depends" >&2
  exit 1
fi
for script in "$control"/preinst "$control"/postinst "$control"/prerm "$control"/postrm; do
  [[ -f "$script" ]] || continue
  if grep -Eq 'systemd-sysusers|systemd-tmpfiles|systemctl|deb-systemd-invoke|chown[[:space:]]+-R' "$script"; then
    echo "unsafe/ init-system maintainer operation in $script" >&2
    exit 1
  fi
done
unit="$root/lib/systemd/system/tparun.service"
for setting in 'User=tparun' 'Group=tparun' 'ExecStart=/usr/bin/tparun serve' 'Restart=on-failure'; do grep -Fxq "$setting" "$unit"; done
[[ "$(stat -c %a "$root/etc/tparun/tparun.env")" == 640 ]]
printf 'BETA3_PACKAGE_METADATA_AND_OPTIONAL_UNIT=PASS\n'

mkdir -m 0700 "$work/gnupg"
gpg --batch --homedir "$work/gnupg" --pinentry-mode loopback --passphrase '' \
  --quick-generate-key 'TPA.run disposable Beta3 volume qualification <volume-qualification@example.invalid>' rsa3072 sign 0 >/dev/null
test_fpr=$(gpg --batch --homedir "$work/gnupg" --with-colons --list-secret-keys | awk -F: '$1=="fpr" {print $10; exit}')
GNUPGHOME="$work/gnupg" "$TPA_BIN" pack -in="$input" -out="$work/repo" \
  -origin='TPA.run Qualification' -label='Disposable Beta3 volume qualification' \
  -suite=beta -components=main -codename=beta -gpg="$test_fpr"
gpg --batch --homedir "$work/gnupg" --export "$test_fpr" > "$work/beta.gpg"

no_systemd_image="tparun-beta3-qual-nosystemd:$$-$RANDOM"
systemd_image="tparun-beta3-qual-systemd:$$-$RANDOM"
images+=("$no_systemd_image" "$systemd_image")
mkdir -m 0755 "$work/no-systemd-image" "$work/systemd-image"
cat > "$work/no-systemd-image/Dockerfile" <<'EOF'
FROM debian:trixie
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends adduser ca-certificates gnupg curl procps && rm -rf /var/lib/apt/lists/*
CMD ["/bin/sh", "-c", "exec sleep infinity"]
EOF
cat > "$work/systemd-image/Dockerfile" <<'EOF'
FROM debian:trixie
ENV container=docker DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends adduser ca-certificates gnupg curl procps systemd systemd-sysv dbus && rm -rf /var/lib/apt/lists/*
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
EOF
# Prebuild known Trixie test environments once; test containers later use only
# the signed local package repo, so a mirror outage cannot skip qualification.
"$DOCKER" build --network=host -q -t "$no_systemd_image" "$work/no-systemd-image" >/dev/null
"$DOCKER" build --network=host -q -t "$systemd_image" "$work/systemd-image" >/dev/null

new_container() {
  local name=$1 image=${2:-$no_systemd_image} mode=${3:-sleep} volume=${4:-}
  local -a extra=()
  if [[ $mode == systemd ]]; then
    extra+=(--privileged --cgroupns=host --tmpfs /run --tmpfs /run/lock -v /sys/fs/cgroup:/sys/fs/cgroup:rw)
  fi
  if [[ -n $volume ]]; then extra+=(--mount "type=volume,source=$volume,target=/var/lib/tparun"); fi
  containers+=("$name")
  if [[ $mode == systemd ]]; then
    "$DOCKER" run -d --name "$name" --label tpahub.tparun.beta3-qualification=true \
      "${extra[@]}" -v "$work/repo:/qualification/repo:ro" -v "$work/beta.gpg:/qualification/beta.gpg:ro" \
      --mount "type=bind,source=$TPA_BIN,target=/usr/local/bin/tpa,readonly" \
      "$image" >/dev/null
  else
    "$DOCKER" run -d --name "$name" --label tpahub.tparun.beta3-qualification=true \
      "${extra[@]}" -v "$work/repo:/qualification/repo:ro" -v "$work/beta.gpg:/qualification/beta.gpg:ro" \
      --mount "type=bind,source=$TPA_BIN,target=/usr/local/bin/tpa,readonly" \
      --entrypoint /bin/sh "$image" -c 'exec sleep infinity' >/dev/null
  fi
}

prepare_apt() {
  local name=$1 policy
  "$DOCKER" exec "$name" sh -ec 'rm -f /etc/apt/sources.list /etc/apt/sources.list.d/*; install -m 0644 /qualification/beta.gpg /usr/share/keyrings/beta-qualification.gpg; cat > /etc/apt/sources.list.d/tparun-qualification.sources <<"EOF"
Types: deb
URIs: file:/qualification/repo
Suites: beta
Components: main
Signed-By: /usr/share/keyrings/beta-qualification.gpg
EOF
apt-get update'
  "$DOCKER" exec "$name" sh -ec 'command -v tpa >/dev/null; test -x /usr/local/bin/tpa'
  policy=$("$DOCKER" exec "$name" apt-cache policy tparun)
  printf '%s\n' "$policy"
  grep -Fq '0.1.5~beta3' <<<"$policy" || { echo 'APT candidate is not beta3' >&2; return 1; }
}

install_version() {
  local name=$1 version=$2
  "$DOCKER" exec "$name" sh -ec "DEBIAN_FRONTEND=noninteractive apt-get -o APT::Install-Recommends=false install -y tparun=$version"
}

assert_paths() {
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

assert_no_systemd_package() {
  local name=$1
  "$DOCKER" exec "$name" sh -ec 'test "$(cat /proc/1/comm)" != systemd; if dpkg-query -W -f="\${db:Status-Status}" systemd 2>/dev/null | grep -qx installed; then exit 1; fi; if command -v systemctl >/dev/null 2>&1; then exit 1; fi; test -z "$(dpkg --audit)"; apt-get check'
}

start_direct() {
  local name=$1 expected_version=$2 i body
  "$DOCKER" exec -d --user tparun "$name" sh -ec 'set -a; . /etc/tparun/tparun.env; set +a; exec /usr/bin/tparun serve >/tmp/tparun-beta3-qualification.log 2>&1'
  for i in $(seq 1 100); do
    body=$("$DOCKER" exec "$name" curl -fsS --max-time 2 http://127.0.0.1:8082/ready 2>/dev/null || true)
    if grep -Fq '"ready":true' <<<"$body" && grep -Fq '"schema":11' <<<"$body"; then break; fi
    sleep 0.25
  done
  grep -Fq '"ready":true' <<<"$body" || { "$DOCKER" exec "$name" cat /tmp/tparun-beta3-qualification.log >&2 || true; return 1; }
  body=$("$DOCKER" exec "$name" curl -fsS http://127.0.0.1:8082/version)
  grep -Fq "\"version\":\"$expected_version\"" <<<"$body"
  if [[ $expected_version == '0.1.5~beta3' ]]; then grep -Fq "\"revision\":\"$EXPECTED_REVISION\"" <<<"$body"; fi
}

stop_direct() {
  local name=$1 i
  "$DOCKER" exec "$name" pkill -TERM -x tparun >/dev/null 2>&1 || true
  for i in $(seq 1 40); do
    if ! "$DOCKER" exec "$name" curl -fsS --max-time 1 http://127.0.0.1:8082/ready >/dev/null 2>&1; then return 0; fi
    sleep 0.25
  done
  return 1
}

wait_systemd() {
  local name=$1 i state body
  for i in $(seq 1 120); do
    state=$("$DOCKER" exec "$name" systemctl is-system-running 2>/dev/null || true)
    if [[ $state == running || $state == degraded ]]; then break; fi
    sleep 0.5
  done
  [[ $state == running || $state == degraded ]]
  for i in $(seq 1 100); do
    body=$("$DOCKER" exec "$name" curl -fsS --max-time 2 http://127.0.0.1:8082/ready 2>/dev/null || true)
    if grep -Fq '"ready":true' <<<"$body"; then return 0; fi
    sleep 0.25
  done
  "$DOCKER" exec "$name" systemctl status --no-pager tparun.service >&2 || true
  "$DOCKER" exec "$name" journalctl -u tparun.service --no-pager -n 80 >&2 || true
  return 1
}

apt_upgrade() {
  local name=$1
  "$DOCKER" exec "$name" sh -ec 'DEBIAN_FRONTEND=noninteractive apt-get -o APT::Install-Recommends=false -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold install --only-upgrade -y tparun'
}

# Fresh normal package install without a systemd package or systemd PID 1.
fresh="tparun-beta3-fresh-$$-$RANDOM"
new_container "$fresh"
prepare_apt "$fresh"
install_version "$fresh" '0.1.5~beta3'
assert_no_systemd_package "$fresh"
assert_paths "$fresh"
start_direct "$fresh" '0.1.5~beta3'
printf 'FRESH_NO_SYSTEMD_INSTALL_AND_RUNTIME=PASS\n'

# Compatible non-empty state with a deliberately foreign-owned descendant proves
# setup never recursively chowns existing data.
compatible="tparun-beta3-compatible-state-$$-$RANDOM"
new_container "$compatible"
prepare_apt "$compatible"
"$DOCKER" exec "$compatible" sh -ec 'addgroup --system --quiet tparun; adduser --system --quiet --ingroup tparun --home /var/lib/tparun --no-create-home --shell /usr/sbin/nologin --disabled-login --disabled-password --gecos "compatible test account" tparun; uid=$(id -u tparun); gid=$(getent group tparun | cut -d: -f3); install -d -m 0755 -o "$uid" -g "$gid" /var/lib/tparun /var/lib/tparun/data; printf "keep-compatible\n" > /var/lib/tparun/data/foreign-descendant; chown 4242:4343 /var/lib/tparun/data/foreign-descendant; chmod 0644 /var/lib/tparun/data/foreign-descendant'
install_version "$compatible" '0.1.5~beta3'
assert_paths "$compatible"
"$DOCKER" exec "$compatible" sh -ec 'test "$(cat /var/lib/tparun/data/foreign-descendant)" = keep-compatible; test "$(stat -c %u:%g /var/lib/tparun/data/foreign-descendant)" = 4242:4343'
printf 'COMPATIBLE_NONEMPTY_STATE_AND_NONRECURSIVE_OWNERSHIP=PASS\n'

# An empty root-owned named volume is the only exceptional ownership adoption.
volume_name="tparun-beta3-state-$$-$RANDOM"
"$DOCKER" volume create --label tpahub.tparun.beta3-qualification=true "$volume_name" >/dev/null
volumes+=("$volume_name")
volume_first="tparun-beta3-volume-first-$$-$RANDOM"
new_container "$volume_first" "$no_systemd_image" sleep "$volume_name"
"$DOCKER" exec "$volume_first" sh -ec 'test "$(stat -c %u:%g /var/lib/tparun)" = 0:0; for entry in /var/lib/tparun/* /var/lib/tparun/.[!.]* /var/lib/tparun/..?*; do if [ -e "$entry" ] || [ -L "$entry" ]; then echo "fresh volume not empty: $entry" >&2; exit 1; fi; done'
printf 'NAMED_VOLUME_INITIAL_ROOT_OWNED_EMPTY=PASS\n'
prepare_apt "$volume_first"
install_version "$volume_first" '0.1.5~beta3'
assert_no_systemd_package "$volume_first"
assert_paths "$volume_first"
"$DOCKER" exec "$volume_first" stat -c 'adopted_volume=%u:%g:%a' /var/lib/tparun
start_direct "$volume_first" '0.1.5~beta3'
"$DOCKER" exec --user tparun "$volume_first" sh -ec 'printf "named-volume-data\n" > /var/lib/tparun/data/volume-marker; printf "test-only-signing-state\n" > /var/lib/tparun/gnupg/volume-marker'
"$DOCKER" exec "$volume_first" test -s /var/lib/tparun/data/tpahub.db
stop_direct "$volume_first"
"$DOCKER" rm -f "$volume_first" >/dev/null

# A replacement container gets the same named volume, installs Beta3 again,
# and reads the persisted data/identity markers before and after runtime start.
volume_second="tparun-beta3-volume-second-$$-$RANDOM"
new_container "$volume_second" "$no_systemd_image" sleep "$volume_name"
prepare_apt "$volume_second"
install_version "$volume_second" '0.1.5~beta3'
assert_no_systemd_package "$volume_second"
assert_paths "$volume_second"
"$DOCKER" exec "$volume_second" sh -ec 'test "$(cat /var/lib/tparun/data/volume-marker)" = named-volume-data; test "$(cat /var/lib/tparun/gnupg/volume-marker)" = test-only-signing-state'
start_direct "$volume_second" '0.1.5~beta3'
"$DOCKER" exec "$volume_second" sh -ec 'test "$(cat /var/lib/tparun/data/volume-marker)" = named-volume-data; test -s /var/lib/tparun/data/tpahub.db'
printf 'DOCKER_NAMED_VOLUME_INSTALL_AND_CONTAINER_REPLACEMENT_PERSISTENCE=PASS\n'

# Non-empty root:root state must fail without altering owner, mode, or bytes.
root_conflict="tparun-beta3-root-conflict-$$-$RANDOM"
new_container "$root_conflict"
prepare_apt "$root_conflict"
"$DOCKER" exec "$root_conflict" sh -ec 'mkdir -m 0755 /var/lib/tparun; printf "preserve-root-owned\n" > /var/lib/tparun/sentinel; chmod 0644 /var/lib/tparun/sentinel'
set +e
root_output=$("$DOCKER" exec "$root_conflict" sh -c 'DEBIAN_FRONTEND=noninteractive apt-get -o APT::Install-Recommends=false install -y tparun=0.1.5~beta3' 2>&1)
root_status=$?
set -e
[[ $root_status -ne 0 ]]
grep -Fq '/var/lib/tparun is owned 0:0 and is not empty' <<<"$root_output"
"$DOCKER" exec "$root_conflict" sh -ec 'test "$(cat /var/lib/tparun/sentinel)" = preserve-root-owned; test "$(stat -c %u:%g:%a /var/lib/tparun)" = 0:0:755; test "$(stat -c %u:%g /var/lib/tparun/sentinel)" = 0:0; test ! -e /var/lib/tparun/data'
printf 'NONEMPTY_ROOT_OWNED_STATE_REJECTED_UNCHANGED=PASS\n'

# A dangling symlink inside a root-owned mountpoint is content, not an empty
# volume; the -L check must reject it even though its target does not exist.
dangling_entry="tparun-beta3-dangling-entry-$$-$RANDOM"
new_container "$dangling_entry"
prepare_apt "$dangling_entry"
"$DOCKER" exec "$dangling_entry" sh -ec 'mkdir -m 0755 /var/lib/tparun; ln -s /missing/qualification-target /var/lib/tparun/.dangling'
set +e
dangling_output=$("$DOCKER" exec "$dangling_entry" sh -c 'DEBIAN_FRONTEND=noninteractive apt-get -o APT::Install-Recommends=false install -y tparun=0.1.5~beta3' 2>&1)
dangling_status=$?
set -e
[[ $dangling_status -ne 0 ]]
grep -Fq '/var/lib/tparun is owned 0:0 and is not empty' <<<"$dangling_output"
"$DOCKER" exec "$dangling_entry" sh -ec 'test -L /var/lib/tparun/.dangling; test "$(readlink /var/lib/tparun/.dangling)" = /missing/qualification-target; test "$(stat -c %u:%g /var/lib/tparun)" = 0:0'
printf 'DANGLING_SYMLINK_CONTENT_REJECTED_UNCHANGED=PASS\n'

# A foreign-owned state directory is rejected with its payload unchanged.
foreign="tparun-beta3-foreign-owner-$$-$RANDOM"
new_container "$foreign"
prepare_apt "$foreign"
"$DOCKER" exec "$foreign" sh -ec 'mkdir -m 0755 /var/lib/tparun; printf "preserve-foreign\n" > /var/lib/tparun/sentinel; chown 4242:4343 /var/lib/tparun /var/lib/tparun/sentinel'
set +e
foreign_output=$("$DOCKER" exec "$foreign" sh -c 'DEBIAN_FRONTEND=noninteractive apt-get -o APT::Install-Recommends=false install -y tparun=0.1.5~beta3' 2>&1)
foreign_status=$?
set -e
[[ $foreign_status -ne 0 ]]
grep -Fq 'owned 4242:4343, expected' <<<"$foreign_output"
"$DOCKER" exec "$foreign" sh -ec 'test "$(cat /var/lib/tparun/sentinel)" = preserve-foreign; test "$(stat -c %u:%g /var/lib/tparun)" = 4242:4343; test "$(stat -c %u:%g /var/lib/tparun/sentinel)" = 4242:4343'
printf 'FOREIGN_OWNED_STATE_REJECTED_UNCHANGED=PASS\n'

# Symlink and non-directory path conflicts are both rejected without touching
# the link target or replacing the conflicting object.
symlink_conflict="tparun-beta3-symlink-conflict-$$-$RANDOM"
new_container "$symlink_conflict"
prepare_apt "$symlink_conflict"
"$DOCKER" exec "$symlink_conflict" sh -ec 'mkdir -m 0755 /var/lib/tparun-target; printf "keep-target\n" > /var/lib/tparun-target/sentinel; ln -s /var/lib/tparun-target /var/lib/tparun'
set +e
symlink_output=$("$DOCKER" exec "$symlink_conflict" sh -c 'DEBIAN_FRONTEND=noninteractive apt-get -o APT::Install-Recommends=false install -y tparun=0.1.5~beta3' 2>&1)
symlink_status=$?
set -e
[[ $symlink_status -ne 0 ]]
grep -Fq '/var/lib/tparun is a symlink' <<<"$symlink_output"
"$DOCKER" exec "$symlink_conflict" sh -ec 'test -L /var/lib/tparun; test "$(readlink /var/lib/tparun)" = /var/lib/tparun-target; test "$(cat /var/lib/tparun-target/sentinel)" = keep-target'
printf 'STATE_ROOT_SYMLINK_REJECTED_UNCHANGED=PASS\n'

file_conflict="tparun-beta3-file-conflict-$$-$RANDOM"
new_container "$file_conflict"
prepare_apt "$file_conflict"
"$DOCKER" exec "$file_conflict" sh -ec 'printf "keep-file\n" > /var/lib/tparun'
set +e
file_output=$("$DOCKER" exec "$file_conflict" sh -c 'DEBIAN_FRONTEND=noninteractive apt-get -o APT::Install-Recommends=false install -y tparun=0.1.5~beta3' 2>&1)
file_status=$?
set -e
[[ $file_status -ne 0 ]]
grep -Fq '/var/lib/tparun exists but is not a directory' <<<"$file_output"
"$DOCKER" exec "$file_conflict" sh -ec 'test -f /var/lib/tparun; test "$(cat /var/lib/tparun)" = keep-file'
printf 'STATE_ROOT_NON_DIRECTORY_REJECTED_UNCHANGED=PASS\n'

# Beta2 -> Beta3 in a no-systemd-package container; PID 1 is sleep and the
# base image contains no systemd package.
nonsystemd_upgrade="tparun-beta2-beta3-nonsystemd-$$-$RANDOM"
new_container "$nonsystemd_upgrade" "$no_systemd_image"
prepare_apt "$nonsystemd_upgrade"
install_version "$nonsystemd_upgrade" '0.1.5~beta2'
assert_no_systemd_package "$nonsystemd_upgrade"
start_direct "$nonsystemd_upgrade" '0.1.5~beta2'
"$DOCKER" exec --user tparun "$nonsystemd_upgrade" sh -ec 'printf "upgrade-volume-data\n" > /var/lib/tparun/data/upgrade-marker'
"$DOCKER" exec "$nonsystemd_upgrade" sh -ec 'printf "\nTPARUN_BETA3_UPGRADE_MARKER=preserve\n" >> /etc/tparun/tparun.env; sha256sum /etc/tparun/tparun.env | cut -d" " -f1 > /tmp/config.sha'
non_config_hash=$("$DOCKER" exec "$nonsystemd_upgrade" cat /tmp/config.sha)
apt_upgrade "$nonsystemd_upgrade"
[[ "$("$DOCKER" exec "$nonsystemd_upgrade" dpkg-query -W -f='${Version}' tparun)" == '0.1.5~beta3' ]]
[[ "$("$DOCKER" exec "$nonsystemd_upgrade" cat /proc/1/comm)" != systemd ]]
assert_paths "$nonsystemd_upgrade"
"$DOCKER" exec "$nonsystemd_upgrade" sh -ec 'test "$(cat /var/lib/tparun/data/upgrade-marker)" = upgrade-volume-data; test "$(sha256sum /etc/tparun/tparun.env | cut -d" " -f1)" = "'"$non_config_hash"'"'
stop_direct "$nonsystemd_upgrade"
start_direct "$nonsystemd_upgrade" '0.1.5~beta3'
printf 'BETA2_TO_BETA3_NON_SYSTEMD_PID1_UPGRADE=PASS\n'

# Beta2 -> Beta3 upgrade under a real systemd PID 1, including restart and
# state/config preservation.
systemd_upgrade="tparun-beta2-beta3-systemd-$$-$RANDOM"
new_container "$systemd_upgrade" "$systemd_image" systemd
manager_ready=0
for _ in $(seq 1 120); do
  state=$("$DOCKER" exec "$systemd_upgrade" systemctl is-system-running 2>/dev/null || true)
  if [[ $state == running || $state == degraded ]]; then manager_ready=1; break; fi
  sleep 0.5
done
[[ $manager_ready == 1 ]]
prepare_apt "$systemd_upgrade"
install_version "$systemd_upgrade" '0.1.5~beta2'
"$DOCKER" exec "$systemd_upgrade" systemctl enable --now tparun.service
"$DOCKER" exec "$systemd_upgrade" systemctl is-enabled --quiet tparun.service
wait_systemd "$systemd_upgrade"
"$DOCKER" exec --user tparun "$systemd_upgrade" sh -ec 'printf "systemd-upgrade-data\n" > /var/lib/tparun/data/upgrade-marker'
"$DOCKER" exec "$systemd_upgrade" sh -ec 'printf "\nTPARUN_BETA3_SYSTEMD_MARKER=preserve\n" >> /etc/tparun/tparun.env; sha256sum /etc/tparun/tparun.env | cut -d" " -f1 > /tmp/config.sha'
systemd_config_hash=$("$DOCKER" exec "$systemd_upgrade" cat /tmp/config.sha)
apt_upgrade "$systemd_upgrade"
[[ "$("$DOCKER" exec "$systemd_upgrade" dpkg-query -W -f='${Version}' tparun)" == '0.1.5~beta3' ]]
"$DOCKER" exec "$systemd_upgrade" systemctl is-active --quiet tparun.service
"$DOCKER" exec "$systemd_upgrade" systemctl restart tparun.service
"$DOCKER" exec "$systemd_upgrade" systemctl is-active --quiet tparun.service
"$DOCKER" exec "$systemd_upgrade" systemctl is-enabled --quiet tparun.service
assert_paths "$systemd_upgrade"
wait_systemd "$systemd_upgrade"
"$DOCKER" exec "$systemd_upgrade" curl -fsS http://127.0.0.1:8082/version | grep -Fq "\"revision\":\"$EXPECTED_REVISION\""
"$DOCKER" exec "$systemd_upgrade" sh -ec 'test "$(cat /var/lib/tparun/data/upgrade-marker)" = systemd-upgrade-data; test "$(sha256sum /etc/tparun/tparun.env | cut -d" " -f1)" = "'"$systemd_config_hash"'"'
printf 'BETA2_TO_BETA3_SYSTEMD_UPGRADE_DATA_CONFIG=PASS\n'
printf 'BETA3_VOLUME_AND_UPGRADE_QUALIFICATION=PASS\n'
