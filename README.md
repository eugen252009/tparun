# TPA Hub

**Persistent release baseline: Schema 5.** [Schema and upgrade contract](docs/schema-upgrades.md). Schemas 1–4 are disposable pre-release state and are rejected without automatic migration or deletion.

TPA Hub is a CLI-first service for publishing and serving public or private APT repositories. **TPA Hub uses TPA; it does not implement APT repository generation itself.** TPA remains a separate external executable invoked across a process boundary.

TPA Hub now has a small public website at `https://tpa.run`. It explains the CLI-first workflow and Nano availability; repository and package management remain in `tpahub` plus normal `apt` commands.

## Install TPA Hub

The public deployment marker is available at `https://tpa.run/version` and returns the service version, startup time, and revision marker. Repository trust is configured per repository by the client.

The Debian-native installation path is a small repository bootstrap package followed by ordinary APT:

```sh
curl -LO https://tpa.run/tpahub-release_0.1.3_all.deb
sudo apt install ./tpahub-release_0.1.3_all.deb
sudo apt update
sudo apt install tpahub
```

The `tpahub-release` package contains only the public repository keyring and
scoped `.sources` definition. It does not contain the application, initialize
Hub state, or invoke APT recursively. Future application updates use ordinary
`sudo apt update && sudo apt upgrade`. The convenience `install.sh` wrapper
uses the same bootstrap path and verifies the bootstrap package checksum before
installing it. Candidate qualification may override the package path and
checksum with `TPAHUB_RELEASE_PACKAGE_PATH` and
`TPAHUB_RELEASE_PACKAGE_SHA256`; both are required together. See
[Debian bootstrap packaging](docs/debian-bootstrap.md). The public bootstrap
repository also carries the qualified `mema` and `bloom` packages used by the
[clean Debian qualification](docs/debian-bootstrap-qualification.md).

## Build and run

Build TPA separately and make it available on `PATH`, or set `TPA_BIN`:

```sh
(cd ../tpa && go build -o /tmp/tpa .)
go build -o tpahub ./cmd/tpahub
# Once, for a NEW empty data directory:
TPAHUB_DATA_DIR=./data ./tpahub init
# Configure your existing repository signing identity as usual, then:
TPA_BIN=/tmp/tpa TPAHUB_DATA_DIR=./data ./tpahub serve
```

The initialized self-hosted profile selects **operator-local capabilities +
metrics-only delivery + enforcement none + billing none**. Registration is enabled
on the default loopback listener. Local grants come from operator policy, not a
Nano offer. No Stripe account, rate card, payment provider or billing endpoint is
needed for provisioning. Existing signing/TPA prerequisites still apply.

Local finite defaults preserve the historical bounds: 5,000,000 storage bytes,
12,000,000,000 annual egress bytes and private-read enabled. Operators can select
`TPAHUB_CAPABILITY_SOURCE=local|hosted-commercial|legacy-local` and configure
`TPAHUB_LOCAL_STORAGE_BYTES`, `TPAHUB_LOCAL_EGRESS_BYTES` and
`TPAHUB_LOCAL_PRIVATE_READ_ENABLED=true|false`. Changes affect new accounts only;
existing grants are never silently replaced. Zero is not unlimited. See
[local capability provisioning](docs/local-capability-provisioning.md).

**Upgrade compatibility:** without an initialized profile, `serve` retains legacy
strict-accounted/mock defaults. `init` refuses existing state and never migrates a
schema. See [commercial composition](docs/commercial-composition.md) for explicit
profile overrides, billing selection and authority boundaries.

Local publication currently requires Linux, the util-linux `unshare`, `mount`,
and `prlimit` commands, and enabled unprivileged user namespaces. TPA Hub runs
each TPA publication in a private user/mount namespace with a fixed-size
`tmpfs`; publication fails closed if that sandbox cannot be created. The data
directory and its work/candidate paths must remain on the same filesystem so a
verified candidate can become an immutable generation by rename.

Configuration uses environment variables. The table below lists legacy defaults;
initialized self-hosted defaults are described above. Explicit environment values
override profile defaults:

```text
TPAHUB_LISTEN                 127.0.0.1:8080
TPAHUB_DATA_DIR               ./data
TPA_BIN                       tpa
GNUPGHOME                     optional GPG home
TPAHUB_GPG_FINGERPRINT        full signing fingerprint
TPAHUB_PUBLISH_TIMEOUT        10m
TPAHUB_UPLOAD_READ_TIMEOUT    15m
TPAHUB_MAX_UPLOAD_BYTES       512 MiB
TPAHUB_MAX_STAGING_BYTES      4 GiB
TPAHUB_MAX_CANDIDATE_INODES   65536 (filesystem inode count)
TPAHUB_MIN_DISK_FREE_BYTES    100 MiB
TPAHUB_MAX_GENERATIONS        5
TPAHUB_REGISTRATION_ENABLED   false (set true to enable with billing or local provisioning)
TPAHUB_LOCAL_PROVISIONING_ENABLED false (set true only for an intentional local deployment)
```

## Website and onboarding

Start at `https://tpa.run/` and read the practical guide at `/docs`. The installer is available at `/install.sh`. Nano is available now; Standard and Pro are displayed as coming soon and have no activation or billing flow.

The website registration and login forms use the same account/session API as the CLI. A successful registration leads to `/account` with the first-user workflow. Production registration is deliberately controlled by `TPAHUB_REGISTRATION_ENABLED`; public registration requires enabled purchases or a local capability source (including the legacy `TPAHUB_LOCAL_PROVISIONING_ENABLED` compatibility selection).

## CLI workflow

Registration and login store origin-scoped credentials in `$XDG_CONFIG_HOME/tpahub/config.json`, or `~/.config/tpahub/config.json`, mode `0600`. `--server` overrides `TPAHUB_SERVER`, which overrides the saved server. Selecting another origin never carries the previous origin's token; credentials for multiple Hubs coexist. Remote servers require verified HTTPS; explicit loopback HTTP is supported for local use. Cross-origin redirects are refused, including redirects of passwords and publication bodies.

Legacy single-server credentials bind to their stored server before overrides. A token without a stored server is retained as unbound local recovery data and is never transmitted; log in explicitly to establish a scoped credential. Invalid legacy server URLs fail closed without overwriting the original file. The version-2 client configuration is not intended for older clients. See [R3/R4 qualification](docs/client-origin-upload-admission.md).

```sh
tpahub register
tpahub login
tpahub create private-tools
tpahub list
tpahub stage ./tool_1.0.0_amd64.deb
tpahub status
tpahub publish https://tpa.run/REPOSITORY_ID
tpahub status JOB_ID
tpahub repo token create private-tools workstation
```

For automated use, `TPAHUB_EMAIL` and `TPAHUB_PASSWORD` avoid interactive prompts. Passwords are bcrypt-hashed server-side and never logged or stored locally as passwords.

Create a repository with `tpahub create name`; repositories default to hidden/private. Use `tpahub show https://tpa.run/REPOSITORY_ID` to inspect one repository. Repository IDs are opaque and filesystem paths never use names. The legacy `repo` namespace remains a compatibility alias.

## APT registration

Public repository:

```sh
sudo tpahub add https://tpa.run/REPOSITORY_ID
apt update
apt install package-name
```

Private repository registration uses a repository reader credential, not the publisher session token:

```sh
sudo tpahub add https://tpa.run/REPOSITORY_ID
apt update
apt install package-name
```

Private repositories prompt for `Repository password:` without echoing the input. For automation, retain an existing reader token with `--token`; do not place it in a command shared with other users.

`add` is an installation-side operation: it writes system APT configuration
and therefore must be run as root (normally with `sudo`). It is not needed to
register, publish, or inspect repositories. Unprivileged users can preview the
exact network-fetched key and files without changing the machine:

```sh
tpahub add https://tpa.run/REPOSITORY_ID --dry-run
```

`tpahub list` lists repositories owned by the selected remote account/server; it is not a local APT-subscription inventory.

The CLI writes:

```text
/etc/apt/keyrings/tpahub-<id>.gpg                 0644, public key
/etc/apt/sources.list.d/tpahub-<id>.list          0644, signed-by source
/etc/apt/auth.conf.d/tpahub-<id>                  0600, private repos only
```

The auth file uses standard APT `machine ... login reader password ...` syntax. `add` is repeatable without duplicate source entries. `tpahub remove https://tpa.run/REPOSITORY_ID` removes only these local APT files; it never uninstalls packages or deletes the remote repository. Remote deletion is explicit and destructive: `tpahub delete https://tpa.run/REPOSITORY_ID`.

Reader credentials are scoped to one repository, stored server-side only as hashes, and displayed only at creation:

```sh
tpahub repo token create REPOSITORY_ID workstation
tpahub repo token list REPOSITORY_ID
tpahub repo token revoke REPOSITORY_ID TOKEN_ID
```

## Local delivery and usage

`TPAHUB_DELIVERY_MODE` selects `strict-accounted` (the legacy default),
`metrics-only`, or `unmetered`. Selection is fixed when the server is composed;
restart to change it. Private authorization and publication/storage safety apply
in every mode.

For a local dashboard without billing:

```sh
TPAHUB_DELIVERY_MODE=metrics-only TPAHUB_BILLING_PROVIDER=none tpahub serve
```

New initialized self-hosted installations select `metrics-only` with
`TPAHUB_BILLING=none`. Hosted composition explicitly selects
`TPAHUB_BILLING=hosted` and the current `TPAHUB_BILLING_PROVIDER=stripe` test adapter;
`simulator` retains the local mock development tool. No remote transport is implemented.

Metrics-only counters are **in-memory and reset on restart**. `/account` reads
usage without creating a billing preview; when billing is enabled, its explicit
**Billing preview** link retains the existing preview functionality. No remote
reporting or collection service is required or implemented.

Egress enforcement is independently selectable with
`TPAHUB_EGRESS_ENFORCEMENT=none|strict-accounted`. When unset, delivery presets
retain their previous behavior: strict-accounted selects strict enforcement;
metrics-only/unmetered select none. An explicit override does not change whether
bytes are measured. Unmetered + strict enforcement is rejected.

For measured delivery with strict local enforcement but no billing:

```sh
TPAHUB_DELIVERY_MODE=metrics-only TPAHUB_EGRESS_ENFORCEMENT=strict-accounted \
  TPAHUB_BILLING=none tpahub serve
```

See [enforcement strategies and lifecycle guarantees](docs/enforcement-strategies.md)
and [local delivery architecture and measurement semantics](docs/local-delivery-usage.md).
Schema 5 supersedes the pre-release lineage conflict through an explicit compatibility
cut, not a historical upgrade. See the [schema contract](docs/schema-upgrades.md).
No deployment or production upgrade is authorized by local qualification. Offline
recovery state and the destructive restore gate are documented in
[backup and restore](docs/backup-restore.md). The public clean Debian consumer
path is recorded in the [final bootstrap qualification](docs/debian-bootstrap-qualification.md).
The follow-on real developer workflow qualification covers fresh-login repository
clones, dependency resolution, templ generation, Bun project tooling, reboot, and
post-reboot rebuilds: [developer environment qualification](docs/developer-environment-qualification.md).

## API and storage

Management endpoints require `Authorization: Bearer tph_s_...`: account registration (`POST /v1/accounts`), login (`POST /v1/login`), logout (`POST /v1/logout`), repository management, publishing, jobs, and reader credentials. The signing-key endpoint remains public. Public repository files are anonymous; private files require HTTP Basic credentials using the reader token as the password.

State is SQLite. `.deb` artifacts remain authoritative and are stored on the local filesystem; TPA creates and verifies signed atomic repository snapshots.

```text
data/{tpahub.db,incoming/<job>,work/<job>,artifacts/<repo-id>,repos/<repo-id>}
```

## Security and deployment status

This is a small single-node foundation. Registration is enabled for development but should be gated in a public deployment until verification or invitation policy exists. Nano checkout and the account web UI are available; there is no email verification, password reset, OAuth, or customer WebAuthn. HTTP local development is supported; production deployment should use TLS.

Operator operations use passwordless WebAuthn passkeys at `/ops/login`. Passkeys are discoverable and require user verification; multiple credentials are supported. To bootstrap or recover access, use SSH and run `sudo tpahub admin operator recovery issue --data-dir /home/eugen/data/tpahub`, then enter the one-time 15-minute code at `/ops/recover` and register a new passkey. The code is printed once, stored only as a SHA-256 hash, consumed before enrollment, and issuing a new code invalidates the previous one. A recovery session authorizes one registration ceremony only; failed or concurrent enrollment attempts require a newly issued code. Never send recovery codes through chat or email or place them in URLs. The former `TPAHUB_OPERATOR_TOKEN` compatibility path has been removed; it grants no operator authority. Authentication rate limits use the direct TCP peer address and do not trust forwarded headers; customer authentication, passkey login, and recovery have separate bounded limiters.

## VServer deployment

The production deployment uses the live Schema 5 root `/home/eugen/vserver-tpahub-qualification`: the binary is `bin/tpahub`, persistent state is `data/`, and signing state is `gnupg/`. The bootstrap release package is published from `releases/` and served at `https://tpa.run/tpahub-release_0.1.3_all.deb`. The deployment script validates Schema 5, stages the binary and package, creates rollback material, and checks the public installer/package pair. Push the `staging` branch to the `vserver` remote to trigger the deployment service. Runtime configuration is kept outside Git in `/home/eugen/.config/tpahub/tpahub.env`. The old `/home/eugen/tpahub_current` and `/home/eugen/data/tpahub` paths are legacy and are not migration sources.

## Development and qualification

```sh
go test -race ./...
go vet ./...
./tests/qualification.sh
```

The qualification builds a real TPA `.deb`, creates a temporary signing identity, exercises authenticated publishing, verifies public and private access, and runs APT install/upgrade/downgrade/dependency and failure-preservation checks in Debian.
