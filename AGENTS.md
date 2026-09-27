# Agent Instructions

These rules govern AI-assisted implementation, review, planning, and refactoring in this repository and its Bloom-related work.

## Bloom North Star

Turn temporary agent reasoning into tested, reusable executable capability. Prefer accumulated contracts, plans, artifacts, and validated execution paths over repeated one-off code generation.

## Mandatory Discovery Before Creation

Before implementing, proposing, or manually recreating any non-trivial capability:

1. Search Bloom's capability discovery interface first:
   ```bash
   bloom search "<semantic capability need>"
   ```
2. If a plausible result exists, inspect it with:
   ```bash
   bloom describe <capability>
   ```
3. Prefer reuse, then composition, then coherent generalization.
4. Only after those fail, record the genuine gap and consider the smallest general-purpose addition.

Search semantic needs, not predetermined implementation names. Do not spam queries or include secrets/payloads. A search miss is useful telemetry and does not by itself justify creating a primitive. If discovery is unavailable, report that explicitly.

## Architecture Rules

- Prefer composition over domain-specific executables and one-off helpers.
- Keep primitives coherent, orthogonal, typed, deterministic where practical, and independently testable.
- Keep capability identity separate from artifact/package identity.
- Keep vBuf-compatible data in vBuf form through internal pipelines where practical; avoid unnecessary parsing, serialization, copying, and materialization.
- Distinguish streamable, bounded-lookahead, seek-dependent, materializing, and global operations honestly.
- Keep vBuf Core minimal; policy belongs above Core.
- Maintain one canonical persistent authority; do not persist cheaply derivable duplicates.
- Separate persistent, runtime, backend, source, artifact, and active-runtime representations.
- Declare external effects explicitly: filesystem, network, process, clock, randomness, and external services.
- Prefer deterministic behavior and reproducible validation.
- Prevent semantically duplicate capabilities and capability accretion.
- Treat generated code as untrusted until isolated build, validation, tests, immutable artifact, registry publication, and explicit activation.
- Do not claim roadmap or proposed behavior is implemented.
- Preserve invariants over implementation convenience; stop and document conflicts instead of weakening contracts.

## Evidence and Testing

Tests must exercise the real production path. When a reusable invariant or guard exists:

```text
positive scenario → same predicate → accept
negative scenario → same predicate → reject
```

Do not use helper-only copies of validation, planning, ABI, registry, or runtime logic as evidence. Effects and ordering must be machine-observed. PASS labels are valid only when all promised primitive assertions have succeeded.

## Package Distribution and Local Beta Staging

### Release channels

TPA.run has two separate release channels:

```text
Internal beta: http://repo.home.arpa/beta
Public stable: public TPA.run repository
```

Use internal beta for development and pre-release builds, upgrade and fresh-install qualification, package lifecycle and systemd/unit validation, local API/UI and reverse-proxy testing, real APT-client testing, and migration/adoption/rollback qualification. Public stable is for released versions only. Never publish beta or test builds to public stable unless explicitly instructed.

When `repo.home.arpa` is available on the LAN, prefer beta for staging and qualification. If it is unavailable, report that; do not silently switch to public stable or substitute another package.

### Local APT qualification

Prefer qualifying TPA.run packages through the actual beta APT repository rather than manually copying binaries where practical. A fresh staging machine can be configured with:

```bash
wget http://repo.home.arpa/lupricht-beta-repo_1.0_all.deb
sudo apt install ./lupricht-beta-repo_1.0_all.deb
sudo apt update
apt-cache policy tparun
sudo apt install tparun
```

The bootstrap installs `/etc/apt/sources.list.d/lupricht-beta.sources` and the beta public key at `/usr/share/keyrings/lupricht-beta.gpg`. The deb822 source is:

```deb822
Types: deb
URIs: http://repo.home.arpa/beta
Suites: beta
Components: main
Architectures: amd64
Signed-By: /usr/share/keyrings/lupricht-beta.gpg
```

Inspect `apt-cache policy tparun` before qualification. When stable and beta sources are both configured, install an explicit version if needed to ensure the intended beta candidate is selected.

For later beta updates, refresh APT and install the intended package/version:

```bash
sudo apt update
sudo apt install tparun
# Or pin the qualification target:
sudo apt install tparun=<version>
```

`sudo apt upgrade` updates all eligible packages on the machine; use it only when a host-wide upgrade is intended, not as the default TPA.run-only qualification step.

A local TPA.run installation from beta is a preferred staging path. Keep it clearly separate from production, with separate data, signing material, credentials, and user/customer data. Do not reuse production state or secrets unless the task explicitly requires a controlled copy and authorizes it.

For NAS staging, keep three qualification layers distinct:

- **Runtime:** TPA.run must run directly as `/usr/bin/tparun serve`; application operation must not require systemd.
- **Package:** install and upgrade through APT/dpkg in the same long-lived staging container. Persist staging-only `/var/lib/tparun` and `/etc/tparun`; normal package updates must not recreate the container.
- **systemd:** qualify the packaged unit, service account, paths, and lifecycle separately in a disposable environment. Do not install or run systemd in the persistent runtime container.

Report runtime, package, and systemd results independently. A failure in the optional systemd integration is not by itself evidence that the portable runtime is broken. Never describe a proposed or unexecuted staging path as validated. The APT-based NAS canary bundle is documented in `docs/nas-staging-apt.md` and `ops/tparun-staging/`; it is prepared but not deployed until authorized Docker access is available.

**Published-package compatibility note:** `0.1.5~beta1` predates portable package setup and has a hard `systemd` dependency. `0.1.5~beta2` removed that dependency but rejects a fresh root-owned named volume. `0.1.5~beta3` safely adopts only an empty `root:root` `/var/lib/tparun` mountpoint by changing ownership of that directory alone. The optional systemd unit and Mema systemd target remain host integrations; the Mema target is usable only where its systemd adapter is available.

State-root ownership contract: absent root is created for `tparun`; compatible `tparun:tparun` state is preserved; empty root-owned Docker mountpoint is adopted non-recursively; non-empty incompatible state, foreign ownership, symlinks, and non-directories fail without modifying user data. Never recursively chown state.

Package `0.1.5~beta3` (amd64; SHA-256 `f33129151cffa8a92c4a4061ef18a7da6528b8523825222f64011a1b34cb0fbb`; embedded source-input identity `bb0992303625853d7a3affdfb114e1441009c812+src.96da62a1695630c66f1aa3f2eeb7de5903d5f352c18e46fc13599325244b9aab`) passed disposable local-Docker qualification using a real named volume: fresh no-systemd install/runtime, root-owned empty-volume adoption, data/database/signing-marker persistence after container removal and replacement, compatible non-empty state with a foreign-owned descendant (proving no recursive chown), and unchanged rejection of non-empty root-owned, foreign-owned, dangling-symlink content, state-root symlink, and non-directory cases. Beta2-to-beta3 APT upgrades passed with no systemd package/PID 1 and separately under systemd; data/config and service behavior were preserved. Tests used a signed local APT repo and prebuilt Trixie test images. The NAS staging files were prepared, but Docker socket access prevented container inspection/creation. No NAS staging container was created, and local named-volume results do not qualify the final NAS bind mounts.

Package `0.1.5~beta4` (amd64; SHA-256 `ee567c1c3f4f610db70c3898f289ebaeb41ceba67e111c6f62ca788d9115f731`; embedded source-input identity `bb0992303625853d7a3affdfb114e1441009c812+src.db06c844be479e25b87a176e2b736a07caa8e9a3dac9dc37ce424248368bf9ce`) adds authenticated `tparun list` for repositories owned by the account logged into the selected server. Go tests cover the real authenticated `/v1/repositories` CLI path, private/public output, owner scoping, and login-required behavior. Beta3-to-beta4 local-Docker APT qualification passed: fresh no-systemd install/runtime, named-volume adoption and replacement persistence, state-conflict rejection/no-recursive-chown cases, and Beta3-to-Beta4 upgrades with and without systemd. A signed temporary APT repository selected Beta4. The live LAN repo signature, candidate ordering/download, and exact package hash were verified after publication. The prior Beta3 repository snapshot is retained; no stable publication or NAS container activation occurred.

On 2026-09-27, after Beta4 publication, the current Debian host (`debian`) was updated via `mayi apt update` and `mayi apt upgrade -y`; APT upgraded only `tparun` to `0.1.5~beta4`. The installed binary reports Beta4. The service was then restarted through `mayi /usr/bin/systemctl restart tparun.service`; verification showed it active, `/ready` true, and `/version` reporting `0.1.5~beta4` with the expected embedded source-input revision `bb0992303625853d7a3affdfb114e1441009c812+src.db06c844be479e25b87a176e2b736a07caa8e9a3dac9dc37ce424248368bf9ce`. All privileged commands were routed through Mayi; no direct sudo was used.

For meaningful package changes, qualify fresh install, older-version-to-beta and beta-to-newer-beta upgrades, remove/reinstall, restart, and mounted-volume persistence through container replacement. Before stable release, qualify beta-to-final-stable through APT ordering with both sources enabled. Current ordering is `0.1.4 < 0.1.5~beta1 < 0.1.5~beta2 < 0.1.5~beta3 < 0.1.5~beta4 < 0.1.5`.

### Building and publishing beta

Use TPA to generate and sign the static APT repository where its workflow supports the required output. Prefer:

```text
build package
→ verify package metadata and hash
→ generate/sign beta repository with TPA
→ verify repository locally
→ sync static output to NAS
→ validate through an APT client
```

Do not reimplement repository metadata generation when TPA supports it. The beta repository is `/volume1/repo/beta` (suite `beta`, component `main`, architecture `amd64`); the LAN bootstrap package is `http://repo.home.arpa/lupricht-beta-repo_1.0_all.deb`. Avoid destructive sync options such as `rsync --delete` unless the destination is conclusively verified as the dedicated beta repository root.

The beta signing identity is separate from public stable:

- Beta fingerprint: `0B2A5DEC8B89EE702B4AF7B920E1029F252912FB`.
- Public stable fingerprint: `F2341718A2BE045B9DB1589CEC9584A267A60717`.
- The beta private key is not stored on the NAS; the NAS should contain only public keys and signed repository artifacts.
- Do not copy or reuse the public stable private key for routine beta work, and never expose private signing material in logs.
- Clients must use `Signed-By`, not deprecated `apt-key`.

The LAN beta repository contains `tparun` `0.1.4~local1` (SHA-256 `5063d5fd6bf73f4f0a0d4ec48befa720e90d70c3f581552e339817dd693a3a34`), `0.1.5~beta1` (SHA-256 `d986524be72d7abb4a5d34920ffc91d663008ae3d28a429098e6019b1d354388`), `0.1.5~beta2` (SHA-256 `ee46ae0410a34f46666a3cb60dbef6917b3750b746ffed4ea41d729c82739d3b`), `0.1.5~beta3` (SHA-256 `f33129151cffa8a92c4a4061ef18a7da6528b8523825222f64011a1b34cb0fbb`), and `0.1.5~beta4` (SHA-256 `ee567c1c3f4f610db70c3898f289ebaeb41ceba67e111c6f62ca788d9115f731`), all `amd64`, under `pool/main/t/tparun/`. Beta4 was generated and signed with the dedicated beta key; live signature, APT ordering/candidate, download, and package hash were verified. The Beta3 snapshot is retained at `/volume1/repo/.beta-previous-0.1.5beta4-20260927T072014Z`; the prior Beta2 snapshot remains `/volume1/repo/.beta-previous-0.1.5beta3-20260926T215910Z`. Public stable and the bootstrap package were not changed. Publishing Beta4 did not install/start it on the NAS or create a NAS staging container, and does not imply production adoption.

### Production boundary

Treat local beta/staging, production deployment, and public stable publication as separate actions. Authorization to build a package, publish to local beta, install on a staging host, or qualify an upgrade does not authorize production deployment, public stable publication, or production data migration. Production deployment remains an explicit step.

## Privileged operations

- If `mayi` is available on the current machine, use it for privileged operations; run unprivileged commands normally.
- If Mayi is unavailable, follow the machine/project's existing documented privilege workflow.
- Do not bypass Mayi with `sudo`, `su`, `pkexec`, an improvised root shell, or another escalation path.
- For current syntax and behavior, consult `man mayi` and `man mayi-workflow`.
- If Mayi rejects an operation, do not evade the restriction with shell wrappers or by moving/copying scripts or executables. Report the blocker or find a narrower legitimate operation.
- After an approved privileged operation, verify the result with unprivileged/read-only commands where possible.

## Change Discipline

Before non-trivial changes, ask:

- Does this already exist?
- Can it be composed or generalized?
- Is it a capability, plan, or artifact?
- Does it add duplicated authority or unnecessary data movement?
- Are effects and determinism explicit?
- Does it belong in Core, a superset, Bloom, or runtime?
- Does the test use the same production predicate?
- Is the feature implemented now rather than merely proposed?

Keep source revision, build artifact, publication, and runtime activation separate. Do not push, deploy, or mutate production unless explicitly authorized. Freeze exact review SHAs and do not create commits afterward when a review handoff requires immutability.
