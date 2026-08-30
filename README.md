# TPA Hub

TPA Hub is a CLI-first service for publishing and serving public or private APT repositories. **TPA Hub uses TPA; it does not implement APT repository generation itself.** TPA remains a separate external executable invoked across a process boundary.

There is intentionally no web dashboard. The supported product interface is `tpahub` plus normal `apt` commands.

## Build and run

Build TPA separately and make it available on `PATH`, or set `TPA_BIN`:

```sh
(cd ../tpa && go build -o /tmp/tpa .)
go build -o tpahub ./cmd/tpahub
TPA_BIN=/tmp/tpa TPAHUB_DATA_DIR=./data ./tpahub serve
```

Configuration uses environment variables:

```text
TPAHUB_LISTEN                 127.0.0.1:8080
TPAHUB_DATA_DIR               ./data
TPA_BIN                       tpa
GNUPGHOME                     optional GPG home
TPAHUB_GPG_FINGERPRINT        full signing fingerprint
TPAHUB_PUBLISH_TIMEOUT        10m
TPAHUB_MAX_UPLOAD_BYTES       512 MiB
TPAHUB_REGISTRATION_ENABLED   true (set false to disable)
```

## CLI workflow

Registration and login store the selected server and session token in `$XDG_CONFIG_HOME/tpahub/config.json`, or `~/.config/tpahub/config.json`, mode `0600`.

```sh
tpahub register --server http://127.0.0.1:8080
tpahub login --server http://127.0.0.1:8080
tpahub repo create private-tools
tpahub repo list
tpahub publish private-tools ./tool_1.0.0_amd64.deb
tpahub status JOB_ID
tpahub repo token create private-tools workstation
```

For automated use, `TPAHUB_EMAIL` and `TPAHUB_PASSWORD` avoid interactive prompts. Passwords are bcrypt-hashed server-side and never logged or stored locally as passwords.

Create a public repository with `tpahub repo create name --public`. Repositories default to private. Repository IDs are opaque and filesystem paths never use names.

## APT registration

Public repository:

```sh
tpahub repo add REPOSITORY_ID --server http://127.0.0.1:8080
apt update
apt install package-name
```

Private repository registration uses a repository reader token, not the publisher session token:

```sh
tpahub repo add REPOSITORY_ID --server http://127.0.0.1:8080 --token tph_r_...
apt update
apt install package-name
```

The CLI writes:

```text
/etc/apt/keyrings/tpahub-<id>.gpg                 0644, public key
/etc/apt/sources.list.d/tpahub-<id>.list          0644, signed-by source
/etc/apt/auth.conf.d/tpahub-<id>                  0600, private repos only
```

The auth file uses standard APT `machine ... login reader password ...` syntax. `repo add` is repeatable without duplicate source entries. `tpahub repo remove` removes only these files; it never uninstalls packages.

Reader credentials are scoped to one repository, stored server-side only as hashes, and displayed only at creation:

```sh
tpahub repo token create REPOSITORY_ID workstation
tpahub repo token list REPOSITORY_ID
tpahub repo token revoke REPOSITORY_ID TOKEN_ID
```

## API and storage

Management endpoints require `Authorization: Bearer tph_s_...`: account registration (`POST /v1/accounts`), login (`POST /v1/login`), logout (`POST /v1/logout`), repository management, publishing, jobs, and reader credentials. The signing-key endpoint remains public. Public repository files are anonymous; private files require HTTP Basic credentials using the reader token as the password.

State is SQLite. `.deb` artifacts remain authoritative and are stored on the local filesystem; TPA creates and verifies signed atomic repository snapshots.

```text
data/{tpahub.db,incoming/<job>,work/<job>,artifacts/<repo-id>,repos/<repo-id>}
```

## Security and deployment status

This is a small single-node foundation. Registration is enabled for development but should be gated in a public deployment until verification or invitation policy exists. There is no email verification, password reset, billing, OAuth, or web UI yet. HTTP local development is supported; production deployment should use TLS.

## Development and qualification

```sh
go test -race ./...
go vet ./...
./tests/qualification.sh
```

The qualification builds a real TPA `.deb`, creates a temporary signing identity, exercises authenticated publishing, verifies public and private access, and runs APT install/upgrade/downgrade/dependency and failure-preservation checks in Debian.
