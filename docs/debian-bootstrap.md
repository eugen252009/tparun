# Debian repository bootstrap

TPA.run uses two Debian package identities:

- `tparun-release` (`Architecture: all`) establishes the repository source and
  archive keyring;
- `tparun` is the application package delivered by that repository.

The bootstrap package contains:

```text
/usr/share/keyrings/tparun-archive-keyring.gpg
/etc/apt/sources.list.d/tparun.sources
```

The source uses Debian's deb822 format and scopes trust with:

```text
Signed-By: /usr/share/keyrings/tparun-archive-keyring.gpg
```

It contains no application binary, private key, Hub state, credentials or
runtime dependency. It has no dependency on `tparun` and does not invoke APT
from a maintainer script. Removing it removes only the source configuration
and keyring; it never removes `tparun` or application data. Purging has the
same package-owned cleanup semantics.

## Build

The project-owned build is:

```sh
SOURCE_DATE_EPOCH=0 packaging/tparun-release/build.sh \
  dist/tparun-release_0.1.5_all.deb
```

The version changes when repository bootstrap configuration changes, such as
the repository endpoint, suite/component or archive key. It need not track each
application release. The default package is built from `tpahub.asc`, the
canonical repository endpoint and the `stable/main` suite/component. Candidate
qualification can override the public key and repository URL without changing
the default package definition.

## Installation

Download and install the bootstrap package, then let APT discover the
application:

```sh
curl -fL https://tpa.run/tparun-bootstrap.deb -o tparun-bootstrap.deb
sudo apt install ./tparun-bootstrap.deb
sudo apt update
apt policy tparun
sudo apt install tparun
```

Afterwards, application upgrades use normal Debian behavior:

```sh
sudo apt update
sudo apt upgrade
```

The stable bootstrap alias follows the explicitly promoted immutable
repository artifact. For reproducible installation, verify the promoted
artifact SHA-256 shown on the website. The advanced alternative is the manual
deb822 source at `https://tpa.run/tparun.sources`.

`tests/bootstrap-package-qualification.sh` builds a disposable candidate
repository and uses real Debian APT to verify package discovery, installation,
upgrade, reinstall idempotence, and bootstrap removal. This is candidate
qualification; no public deployment is implied.
