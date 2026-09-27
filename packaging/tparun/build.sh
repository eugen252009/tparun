#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
VERSION=${TPARUN_VERSION:-0.1.5~beta5}
ARCH=${TPARUN_ARCH:-amd64}
OUTPUT=${1:-$ROOT/dist/tparun_${VERSION}_${ARCH}.deb}
EPOCH=${SOURCE_DATE_EPOCH:-0}

[[ "$ARCH" == amd64 ]] || { echo 'tparun currently supports amd64 packaging only' >&2; exit 1; }
command -v go >/dev/null || { echo 'go is required' >&2; exit 1; }
command -v dpkg-deb >/dev/null || { echo 'dpkg-deb is required' >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
install -d -m 0755 "$work/DEBIAN" "$work/usr/bin" "$work/usr/lib/systemd/system" \
  "$work/usr/lib/sysusers.d" "$work/usr/lib/tmpfiles.d" "$work/etc/tparun" "$work/etc/mema/recipe/manage"
revision=${TPARUN_BUILD_REVISION:-$(git -C "$ROOT" rev-parse HEAD)}
[[ "$revision" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$ ]] || { echo 'TPARUN_BUILD_REVISION must be a safe build identifier' >&2; exit 1; }
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/eugen252009/tpahub/internal/version.Current=$VERSION -X github.com/eugen252009/tpahub/internal/version.BuildRevision=$revision" \
  -o "$work/usr/bin/tparun" "$ROOT/cmd/tparun"
chmod 0755 "$work/usr/bin/tparun"
schema=$("$work/usr/bin/tparun" schema)
[[ "$schema" =~ ^[1-9][0-9]*$ ]] || { echo "built tparun reported invalid schema: $schema" >&2; exit 1; }
runtime_version=$("$work/usr/bin/tparun" version)
[[ "$runtime_version" == "$VERSION" ]] || { echo "package version $VERSION does not match runtime version $runtime_version" >&2; exit 1; }
install -m 0644 "$ROOT/packaging/tparun/tparun.service" "$work/usr/lib/systemd/system/tparun.service"
install -m 0644 "$ROOT/packaging/tparun/tparun.sysusers" "$work/usr/lib/sysusers.d/tparun.conf"
install -m 0644 "$ROOT/packaging/tparun/tparun.tmpfiles" "$work/usr/lib/tmpfiles.d/tparun.conf"
install -m 0640 "$ROOT/packaging/tparun/tparun.env" "$work/etc/tparun/tparun.env"
install -m 0644 "$ROOT/packaging/tparun/tparun.json" "$work/etc/mema/recipe/manage/tparun.json"
printf '/etc/tparun/tparun.env\n' > "$work/DEBIAN/conffiles"
install -m 0755 "$ROOT/packaging/tparun/postinst" "$work/DEBIAN/postinst"
cat > "$work/DEBIAN/control" <<EOF
Package: tparun
Version: $VERSION
Section: net
Priority: optional
Architecture: $ARCH
Depends: adduser, ca-certificates, gnupg, tpa, mema (>= 0.4.1)
Replaces: tparun (<< 0.1.5)
Maintainer: TPA.run <tparun@lupricht.net>
Homepage: https://tpa.run
Mema-Type: binary
Mema-Service: tparun
Mema-Schema: $schema
Description: TPA.run self-hosted application
 Persistent APT repository and package publishing service.
 This package owns the release binary and host integration files; instance
 data, repository contents, publication state, and signing identity remain
 outside the package.
EOF
find "$work" -exec touch -h -d "@$EPOCH" {} +
mkdir -p "$(dirname "$OUTPUT")"
dpkg-deb --build --root-owner-group "$work" "$OUTPUT" >/dev/null
packaged_schema=$(dpkg-deb -f "$OUTPUT" Mema-Schema)
[[ "$packaged_schema" == "$schema" ]] || { echo "package schema metadata $packaged_schema does not match binary schema $schema" >&2; exit 1; }
packaged_version=$(dpkg-deb -f "$OUTPUT" Version)
[[ "$packaged_version" == "$runtime_version" ]] || { echo "package version $packaged_version does not match runtime version $runtime_version" >&2; exit 1; }
printf '%s\n' "$OUTPUT"
