# R3/R4: client origins and upload admission

## Historical R3/R4 baseline

Worktree: `tpahub-r34`, branch `fix/client-origin-upload-admission`.
Git base: `94c226dae1999d27895d75fc14d27d790b0b5f2f` plus all accumulated
uncommitted delivery/usage, optional-commercial, enforcement and provisioning work.
Input archive SHA-256:
`0220eaf9340b5ae7c023ea50c3daaf119cb54400ed86690453e1c1d93fec6b02`.
This historical refinement used Schema 3. The current [Schema 5 contract](schema-upgrades.md)
supersedes its lineage limitation; historical qualification below is not Schema 5 evidence.
No deployment, production modification, TPA modification or commit was performed.
Pre-change `go test ./cmd/tparun ./internal/config` failed only the inherited
`TestPublicBootstrapAndVersionEndpoints` installer-ID assertion.

## Client origin invariant

`client_origin.go` stores version-2 client state: selected server plus credentials
keyed by canonical scheme/hostname/effective port. Default ports collapse, but
ports, schemes and subdomains otherwise remain distinct. DNS/IP equivalence is
not used. Server base URLs must be origins, not paths, userinfo, queries or fragments.
Remote requests require HTTPS; explicitly selected localhost/loopback HTTP works.
No TLS-verification bypass is introduced.

Legacy credentials bind to the server in the file before environment or CLI
selection. Missing-owner legacy credentials are retained under `unbound_legacy`
and never selected. Invalid/unsupported configuration fails closed without
rewriting the original file. Successful login updates only the selected slot;
logout revokes/removes only that slot. Failed login leaves the file unchanged.
Writes use a private temporary file, fsync, and rename; configuration directory
permissions are tightened to 0700 and the new file is 0600. Concurrent independent
CLI writers are not serialized; use one credential-mutating CLI operation at a time.

`doCLIRequest` guards every CLI HTTP path, including anonymous discovery, key
retrieval, logout, reader management, password POSTs and multipart publication.
Cross-origin redirects are refused before transport, even when another saved
credential exists. This also prevents 307/308 replay of sensitive bodies. Cookie
jar inheritance is disabled for these bearer-oriented requests. Same-origin
redirects retain the existing redirect-count bound.

Explicit reader-based APT registration performs no publisher-authenticated
metadata discovery. The separate existing APT machine-path scoping limitation
is not redesigned here. Browser cookie scoping is unchanged.

Optional future challenge/machine-key authentication is not implemented. Any
session it eventually issues must still obey origin/identity binding; challenge
responses do not by themselves establish first-contact trust.

## Upload invariant

For multipart and unknown-length raw requests, post-copy input already appears
in the in-flight incoming directory. The existing logical capacity check now adds
zero prospective bytes instead of adding the completed payload again:

```
before: published + pending + in-flight (including N) + N
after:  published + pending + in-flight (including N) + 0
```

Known-length raw uploads continue using their existing logical reservation.
The final check, reservation removal and durable job insertion remain protected
by `stagingMu`. No new exclusion path, persistent reservation authority or quota
engine exists. The 5,000,000-byte policy and grant semantics are unchanged.

Physical spool/work/candidate accounting is not redefined. Concurrent tests
observed an independent existing `507 capacity accounting underflow`: physical
usage can grow between the global scan and the in-flight subtraction scan.
It fails closed; an exact-fit retry after the competing copy completes succeeds.
Tests disclose this response rather than weaken the physical predicate. This
physical scanning limitation remains outside R4's logical double-count fix.

## Evidence

New tests exercise real production client paths with actual Hub-issued credentials,
separate receiving origins, flag/environment overrides, legacy import, coexistence,
logout, failed login, redirects (including 307/308), scheme/port/host boundaries,
configuration permissions and incomplete input. Secrets are compared without
printing them.

Upload tests exercise real HTTP admission with stored grants at below/equal/above
limits for known-length raw, unknown-length raw and multipart; nonempty canonical
published generations; 6+6, 4+6 and 5+5 MB concurrent inputs under a disposable
10 MB test grant; job-insert failure, read failure, retry and restart/cleanup
ownership. They inspect reservations, incoming files, durable jobs and grants.
Existing disconnect/read-timeout and publication/recovery tests remain intact.

The formerly failing 3,000,000-byte multipart payload under the unchanged
5,000,000-byte allowance is admitted. Above-limit work still rejects. Synthetic
admission tests alone are not publication evidence.

The complete existing `tests/qualification.sh` passed using disposable state,
real TPA/GPG, and a Debian bookworm container. It uses HTTP uploads and the real
CLI for APT registration/removal; covers signed public/private access, revoked and
wrong readers, restart, APT install/upgrade/downgrade/dependencies, repeatable
registration and invalid-package authority preservation. No quotas were raised.
This is not a fresh VM qualification or validation of the public deployed installer.

Final `go test ./...` and `go test -race ./... -count=1 -timeout=60s` retain
only the independent stale installer-ID assertion. A prior full race run also
observed one fresh-server test failure: TPA reported no `.deb` packages in its
work directory. Ten isolated race repetitions passed, as did the subsequent full
race run apart from the installer assertion. This intermittent observation is
not claimed fixed or proven inherited.

The filtered race command excluding exactly the known installer test passed 342
top-level tests/examples, with no additional test skips. Twelve new top-level
tests were added. Vet, resource formatter tests and diff checks pass. The full
APT qualification passed twice without quota changes. All unrelated baseline
files were byte-compared against the input archive; server architecture/packages,
TPA, schema and 5 MB policy remain unchanged.
