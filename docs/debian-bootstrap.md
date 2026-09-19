# Debian repository bootstrap

TPA Hub uses two Debian package identities:

- `tpahub-release` (`Architecture: all`) establishes the repository source and
  archive keyring;
- `tpahub` is the application package delivered by that repository.

The bootstrap package contains:

```text
/usr/share/keyrings/tpahub-archive-keyring.gpg
/etc/apt/sources.list.d/tpahub.sources
```

The source uses Debian's deb822 format and scopes trust with:

```text
Signed-By: /usr/share/keyrings/tpahub-archive-keyring.gpg
```

It contains no application binary, private key, Hub state, credentials or
runtime dependency. It has no dependency on `tpahub` and does not invoke APT
from a maintainer script. Removing it removes only the source configuration
and keyring; it never removes `tpahub` or application data. Purging has the
same package-owned cleanup semantics.

## Build

The project-owned build is:

```sh
SOURCE_DATE_EPOCH=0 packaging/tpahub-release/build.sh \
  dist/tpahub-release_0.1.3_all.deb
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
curl -LO https://tpa.run/tpahub-release_0.1.3_all.deb
sudo apt install ./tpahub-release_0.1.3_all.deb
sudo apt update
apt policy tpahub
sudo apt install tpahub
```

Afterwards, application upgrades use normal Debian behavior:

```sh
sudo apt update
sudo apt upgrade
```

`install.sh` is only a convenience wrapper around this flow. It downloads or
accepts a candidate-local `tpahub-release` package, verifies its SHA-256, then
runs the two explicit APT steps. It never recursively installs the application
from the bootstrap package.

`tests/bootstrap-package-qualification.sh` builds a disposable candidate
repository and uses real Debian APT to verify package discovery, installation,
upgrade, reinstall idempotence, and bootstrap removal. This is candidate
qualification; no public deployment is implied.
