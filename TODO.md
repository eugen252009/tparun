# TPA Hub Production Readiness Checklist

## Schema 5 release-cut status

The [Schema 5 contract](docs/schema-upgrades.md) and [qualification report](docs/schema5-qualification.md)
supersede historical schema migration, lineage and installer-test blockers below.
Schemas 1–4 are unsupported development state, not migration sources. Historical
`DONE` entries are not current implementation claims. This local qualification
is not a public release, deployment authorization or unrestricted paid-launch approval.

This checklist tracks the remediation work required after the architecture, correctness, commerce, payment, migration, authorization, and repository audit.

The goal is to make every item independently verifiable.

## Mini-SaaS Launch Review: 2026-09-11

Reviewed source: `a962a8ddbd8d45b2dfda200ef14ec3ab29d92b73`.

**Verdict: not ready for an unrestricted paid public launch.** A controlled,
explicitly test-mode pilot is a separate decision, not production qualification.
This section records launch gates at that historical revision; older `DONE` and `VERIFIED`
entries below remain historical evidence, not proof that these gates are closed.
In particular, persisted-grant tests do not establish effective runtime access
policy when the default grant service supplies fallback grants.

All unchecked items below are `TODO`. Checking an item requires recorded evidence,
not just an implementation claim. External operational controls may satisfy a
requirement, but must be documented and verified rather than assumed absent or
present. Before implementation, follow capability discovery and reuse rules in
`AGENTS.md`; the review's Bloom search returned no capability result.

### Review Baseline

- [ ] Restore a green current-revision test baseline. `go vet ./...` passed in this review; `go test -race ./...` failed on `TestPublicBootstrapAndVersionEndpoints`, and an isolated rerun reproduced the failure.
- [ ] Resolve the installer/test repository identity mismatch: `cmd/tpahub/public_test.go:20` expects `748316a73ce0eff46f8888f956c5eeb0`, while `install.sh:9` selects `a0d8fd0f82235ee431c41581aa253ec8`. Establish the intended published repository and trust material before changing either side; a mismatch alone does not prove that the installer is broken.
- [ ] Record qualification gaps explicitly: full Docker/APT qualification, historical-binary rollback qualification, deployed proxy/TLS settings, off-host backups, monitoring, and live Stripe configuration were not verified in this review.
- [ ] Attach subsequent test and operational evidence to an exact source SHA, artifact identity, and environment. Do not reuse historical PASS claims as current release approval.

### L0.1 - Decide and Enforce the Nano Access Policy

Priority: launch blocker. Depends on an explicit product-policy decision.
References: `cmd/tpahub/plan.go:82-132`, `cmd/tpahub/auth.go:41-45`,
`docs/migration-access-policy.md`, and historical P0.3 provisioning claims below.

- [x] Decide whether Nano is permanently free, payment-gated, or a bounded trial. Hosted Nano is payment-gated after any active migration grant; local provisioning is an explicit persisted-grant mode. Document the decision before changing authorization; do not silently weaken an existing contract.
- [ ] If Nano is free, define what a payment actually purchases and align the offer, expiry, and migration policies with that value proposition.
- [x] If Nano is paid or time-bounded, prevent the default runtime service from converting missing, disabled, or inactive grants into permanent Nano access. `grantService` now uses the persisted store by default; `TestHostedRegistrationDoesNotProvisionPermanentTPAGrants` and `TestDefaultGrantServiceRequiresActivePersistedGrant` cover hosted creation, active purchase, exact expiry, disabled grants, and egress lookup.
- [x] Ensure raising a resource floor cannot erase a purchased or migration grant's validity boundary. The runtime floor has been removed; only stored limits and validity contribute.
- [x] Keep account suspension, product access, and resource limits distinct; preserving one must not bypass another. Repository policy checks active account status and persisted grants independently; `TestReactivationRestoresReaderAndNewWorkerAuthorization` verifies reactivation does not restore disabled grant access.
- [ ] Test the production server's default grant wiring, without injecting a stricter test-only grant service: fresh unpaid account, active purchase, exact expiry boundary, expired migration access, disabled grant, and reconciliation-required purchase.
- [ ] For each applicable case, exercise repository creation, publication admission, queued publication execution, private reads, and download/egress admission. Assert both permitted and denied operations using the same production predicates.
- [ ] Reconcile earlier store-only `VERIFIED` claims with effective runtime results, preserving the historical evidence and documenting its narrower scope.

Acceptance: effective access matches the chosen policy across real request and
worker paths; a database-only grant assertion is insufficient.

### L0.2 - Protect All Customer Authentication Entry Points

Priority: launch blocker for open signup/login.
References: `cmd/tpahub/auth.go:48-104`, `cmd/tpahub/web.go:210-264`.

- [x] Apply consistent rate admission to API registration, API login, browser registration, and browser login before expensive password work. All four routes share the server-owned customer limiter.
- [x] Define bounded limiter storage and concurrency behavior so an attacker cannot exhaust memory by varying identities or addresses. `authLimiter` serializes access and evicts old keys at its configured bound.
- [x] Define the trusted client-address policy for the actual reverse proxy. Limiting uses the direct TCP peer address and does not trust forwarded headers; deployment must retain that transport boundary.
- [x] Preserve generic authentication failures and ensure rejection responses do not disclose credentials or account details.
- [x] Exercise every public auth route with accepted attempts, excessive attempts, and recovery after the limit window. `TestCustomerAuthenticationRoutesShareBoundedLimiter` exercises each route; `TestCustomerAuthenticationRoutesCannotBypassSharedLimiter` proves API/browser switching cannot bypass the shared limiter. The limiter mutex is covered by race validation.
- [ ] Verify deployed edge limits if they are part of the protection; record sanitized configuration and observed behavior.

Acceptance: unauthenticated callers cannot trigger unbounded parallel bcrypt work
or unrestricted account creation through an alternate entry point.

### L0.3 - Bound Upload Time and Shared Resource Use

Priority: launch blocker for untrusted multi-tenant publication.
References: `cmd/tpahub/main.go:151`, `cmd/tpahub/main.go:425-553`,
`cmd/tpahub/main.go:786-788`, `internal/tpa/tpa.go:110-134`.

- [x] Avoid holding the global staging mutex while reading an untrusted request body; preserve atomic capacity reservation and cleanup invariants when shortening the critical section. The body stream runs after physical reservation/incoming-directory creation; incomplete multipart reservations contribute observed partial bytes to same-account admission. `TestStalledUploadDoesNotBlockAnotherTenantUpload` proves a stalled tenant does not block another tenant upload.
- [x] Define and enforce upload duration/body-read limits appropriate to supported package sizes, including cancellation and stalled clients. `TPAHUB_UPLOAD_READ_TIMEOUT` defaults to 15 minutes and is applied through the production HTTP-server constructor; `TestUploadReadTimeoutReleasesReservation` drives a stalled TCP body and verifies cleanup.
- [ ] Test a real slow/stalled upload concurrently with another tenant's upload, repository creation, and publication worker. Observe that unrelated work progresses within a bounded time.
- [ ] Verify timeout/disconnect cleanup releases reservations and temporary files without releasing authority for work still in use.
- [x] Reduce multipart in-memory buffering from the configured maximum upload size; verify large admitted uploads do not require hundreds of MiB of application heap by default. Multipart parsing now has an 8 MiB memory threshold independent of `TPAHUB_MAX_UPLOAD_BYTES`; `TestMultipartUploadSpoolsFilesPastMemoryLimit` exercises the production upload path and verifies a larger file is spooled.
- [ ] Bound subprocess diagnostic capture and document process memory/CPU limits. Disk/tmpfs limits alone are not memory limits.
- [ ] Review the package-processing trust boundary: the current namespace/tmpfs setup does not isolate all host files, environment secrets, or network access. Document and validate the intended containment rather than describing output limits as a complete security sandbox.

Acceptance: one admitted tenant cannot indefinitely stall shared publication or
exhaust host resources within the documented service limits.

### L0.4 - Make Deployment and Rollback Preserve Accepted State

Priority: launch blocker before relying on automated production deployments.
References: `deploy/deploy-tpahub:47-56`, `deploy/deploy-tpahub:98-120`,
`deploy/deploy-tpahub:143-214`, `cmd/tpahub/main.go:151-158`.

- [ ] Establish a write-quiescence boundary before the final rollback snapshot. Account for active HTTP requests, publication workers, payment callbacks, and usage accounting.
- [ ] Prevent unaccounted customer/provider effects while a candidate is running but not yet accepted, or define a rollback strategy that preserves those effects.
- [ ] Coordinate database rollback with authoritative package/generation state; do not restore SQLite into an incompatible filesystem history.
- [ ] Explicitly propagate failure from every restore copy, permission change, rename, integrity check, foreign-key check, and schema assertion. Do not rely on `set -e` inside a function invoked as an `if` condition.
- [ ] Await bounded HTTP/worker shutdown before closing the database and releasing the data-directory lock.
- [ ] Capture previous and candidate build identities independently; do not validate the restored binary against the candidate's version.
- [ ] Test through the real deploy script: writes around quiescence, failed migration, post-promotion failure, failed restore operations, integrity/FK failure with matching schema, and rollback across different application versions.
- [ ] Observe that acknowledged writes survive, failed validation is never reported as successful restoration, and unsafe rollback leaves a clear fail-closed/manual-intervention state.

Acceptance: the tested deployment procedure preserves accepted authority and
reports every failed recovery assertion honestly.

### L0.5 - Qualify Real-Money Checkout and Activation

Priority: paid-launch blocker. Depends on L0.1 and safe operational recovery.
References: `cmd/tpahub/provider.go:49-72`, `cmd/tpahub/purchase.go`,
`internal/payments/stripe/stripe.go:33-45`.

- [ ] Implement an explicitly supported production provider path; the existing code rejects live mode at multiple layers, so changing secrets is not sufficient.
- [ ] Separate test/live checkout, webhook verification, provider correlation, and persisted environment identities. Reject cross-environment events without creating access.
- [ ] Preserve immutable purchase terms, amount/currency checks, paid-status verification, event deduplication, and transactional activation.
- [ ] Exercise successful payment, unpaid completion, invalid signature, wrong amount/currency/environment, duplicate events, out-of-order events, lost responses, and process restart through actual checkout/webhook handlers.
- [ ] Verify effective customer access after activation using L0.1's runtime paths, not only purchase and grant rows.
- [ ] Validate deployment-specific return URLs and webhook endpoints rather than hardcoding the public homepage for every environment.
- [ ] Obtain explicit authorization before activating live billing or performing real-money validation. Record the controlled checkout, receipt, activation, and refund evidence without recording secrets.

Deployed test evidence: `docs/deployed-test-billing-evidence.md` records an
active service and an isolated Stripe test Checkout creation on 2026-09-11. It
does not close the unchecked activation or live-billing gates above.

Acceptance: authorized live payment qualification proves that exactly the paid
terms activate once and invalid/unpaid evidence does not activate access.

### L1.1 - Support Uninterrupted Annual Renewal

Priority: required before selling annual access. Depends on L0.1 and L0.5.
References: `internal/store/store.go:1826-1830`,
`internal/store/store.go:1871-1897`, `internal/payments/stripe/stripe.go:75-82`.

- [ ] Implement manual advance renewal or an explicitly chosen recurring model. Automatic subscriptions are not required for a minimal launch.
- [ ] Define renewal start/end boundaries, calendar/leap-year behavior, late renewal, overlapping purchase handling, and which offer terms are snapshotted.
- [ ] Permit payment before the current term expires without overwriting immutable current-purchase authority.
- [ ] Make repeated/concurrent renewal requests idempotent and keep future paid access distinct from current access.
- [ ] Test renewal before expiry, exact term transition, renewal after expiry, duplicate webhook delivery, failed payment, and restart between payment and term activation.
- [ ] Show the current paid-through date and renewal availability, and document any reminder/support process.

Acceptance: customers can pay ahead and continue using the service across the
term boundary without an avoidable access gap or duplicate charge.

### L1.2 - Complete Customer Purchase and Support Flows

Priority: required before paid self-service launch.
References: `cmd/tpahub/purchase.go:81`, `cmd/tpahub/web.go:96-134`,
`docs/payment-checkout-recovery.md:28-46`.

- [ ] Render persisted pending, active, failed/cancelled, expired, and reconciliation-required states on the account page, including useful next actions and validity dates.
- [ ] Distinguish checkout success and cancellation returns. A redirect must display webhook-backed state, never grant authority itself.
- [ ] Handle delayed webhooks and already-active purchases without leaving customers at an unexplained homepage or plain conflict error.
- [ ] Document an authorized manual reconciliation/refund/escalation procedure for customers who paid but lack usable access; avoid undocumented SQL surgery.
- [ ] Define refund, dispute, and late-payment handling, including the case where immutable purchased validity has already expired. Automation may be deferred if the manual process is safe and usable.
- [ ] Test the support procedure on incomplete authority, replay/restart, and already-resolved cases; preserve original purchase and provider evidence with an audit trail.
- [ ] Verify purchase, cancellation, delayed activation, and support messaging in a browser on desktop and mobile.

Acceptance: a customer can understand what was purchased, whether access is
active, and how a payment problem will be resolved.

### L1.3 - Make Customer Credentials Recoverable and Revocable

Priority: required before broad public launch.
References: `internal/store/store.go:2331-2341`, `README.md:132`,
`cmd/tpahub/credentials.go:19-64`.

- [ ] Add server-enforced customer-session expiry with an explicit browser/CLI policy. Cookie expiry alone must not leave copied tokens valid indefinitely.
- [ ] Test expiry at the exact boundary, logout, suspension, and reuse of a copied expired cookie as a bearer token.
- [ ] Provide a safe customer password/account recovery process, automated or documented manual, with identity checks, bounded one-time recovery authority, and appropriate session revocation.
- [ ] Choose and enforce email verification, invitation, or another controlled registration policy before unrestricted signup.
- [ ] Permit authenticated owners to revoke existing repository reader credentials even when their private-read entitlement has lapsed.
- [ ] Test that a leaked reader token revoked during a lapse remains rejected after access is renewed.

Acceptance: customers can recover access safely, stolen sessions have bounded
lifetime, and security-reducing operations do not require continued payment.

### L1.4 - Prove Whole-Service Disaster Recovery

Priority: required before promising durable customer hosting.
References: `README.md:47-49`, `README.md:124-138`,
`deploy/deploy-tpahub:143-149`. External backups were not inspected.

- [ ] Inventory all required recovery state: SQLite, authoritative artifacts/generations, signing material, runtime configuration, and exact application/TPA artifacts.
- Deferred TODO: document and verify coordinated off-host backup consistency, scheduling, retention, encryption/access controls, and recovery credentials. The owner explicitly deferred this requirement; it remains a launch-risk exception rather than a completed gate.
- [ ] Define acceptable recovery point and recovery time objectives and customer-facing expectations.
- [ ] Alert on failed or stale backups; verify access to recovery material without depending solely on the failed host.
- [ ] Perform a clean-host restore drill and measure data loss window and restore time.
- [ ] Verify restored authentication, tenant isolation, public/private APT access, repository signatures, publishing, purchase authority, and pending/reconciliation state.
- [ ] Document how data deletion and backup expiry interact, including restoring a backup containing previously deleted customer data.

Acceptance: independently recorded restore evidence demonstrates recovery of the
service, not merely an integrity check on a local SQLite copy.

### L1.5 - Align Pricing, Terms, Tax, and Privacy

Priority: required before collecting real money.
References: `cmd/tpahub/templates/pricing.html`,
`cmd/tpahub/templates/account.html`, `cmd/tpahub/templates/docs.html`,
`privacy.html`, `legal.html`.

- [ ] Publish one pre-signup offer describing price/currency, capacity, payment cadence, renewal, expiry, and test versus live availability.
- [ ] Align onboarding with the selected access policy: registration alone must not imply paid activation if payment is required.
- [ ] Remove stale claims about unavailable checkout or unfinished enforcement where implementation now differs; keep mock billing previews distinct from actual bills.
- [ ] Confirm tax-inclusive/exclusive pricing, tax calculation, business/customer classification, and receipt/invoice handling with the responsible business/tax adviser. A Stripe product tax code alone does not establish a complete tax flow.
- [ ] Review applicable service terms, cancellation/refund policy, consumer disclosures, acceptable use, and support contact before live sales.
- [ ] Ensure the actually linked legal/privacy pages on `lupricht.net` contain the intended disclosures; local HTML files are not proof of published content.
- [ ] Align cookie, operator WebAuthn, processor, logging, and retention disclosures with actual operations. The local one-cookie claim does not describe the operator cookies.
- [ ] Establish an operable data access/export/deletion request process; self-service automation is optional if the manual process is verified.

Acceptance: the actual published offer and operational/legal procedures describe
the service customers receive. Engineering review is not legal certification.

### L1.6 - Make Releases and Operations Observable

Priority: required for a supportable public service.
References: `cmd/tpahub/main.go:253-260`, `deploy/deploy-tpahub:194-204`,
`deploy/deploy-tpahub:26-28`, `README.md:136-138`.

- [ ] Expose immutable build/revision identity instead of `revision: unknown`, and validate the expected candidate during deployment.
- [ ] Separate liveness from readiness; define checks for required database/storage/worker conditions without making probes destructive or expensive.
- [ ] Add external HTTPS and representative repository probes plus actionable alerts for disk/inodes, publication failures, cleanup backlog, and payment/egress reconciliation.
- [ ] Verify physical disk reporting in the operator UI; its production construction currently does not wire the test-injected `diskFree` function.
- [ ] Provide sanitized, reproducible systemd/proxy/deploy configuration, including TLS renewal, restart/boot behavior, resource limits, namespace compatibility, and secret permissions.
- [ ] Verify concurrent-push handling: the deploy script must queue/recheck new revisions or truthfully report a skipped deployment rather than claiming the running invocation will deploy them.
- [ ] Establish an exact-SHA release gate, through CI or a reproducible manual approval process. Building successfully is not equivalent to qualification.

Acceptance: the operator can identify the running artifact, detect user-visible
failure, and reproduce the supported deployment configuration.

### Final Release Acceptance

- [ ] Close all applicable L0 and L1 gates with evidence; record owner-approved rationale for any `N/A` decision rather than silently dropping a requirement.
- [ ] Run `go test -race ./...` and `go vet ./...` successfully on the release revision.
- [ ] Run `./tests/qualification.sh` and the applicable clean-client bootstrap qualification against the intended package and signing identity.
- [ ] Run `./tests/deploy-rollback-integration.sh` with its required exact historical binary/checksum and inspect every promised negative assertion, not only a final PASS label.
- [ ] Execute the runtime paid/unpaid/expired authorization matrix, checkout/renewal flow, two-tenant abuse tests, and clean-host restore drill on the supported configuration.
- [ ] Freeze the reviewed source SHA and immutable build artifacts; record publication and runtime activation separately. Requalify relevant changes made after the review freeze.
- [ ] Obtain explicit approval for publication, deployment, and live billing activation. Keep signup/payment gates closed until the intended launch mode is approved.

Recommended order: access-policy decision -> authentication/upload and rollback
fixes -> backup/restore proof -> live payment and renewal qualification -> customer
flows and commercial disclosures -> exact-revision release approval. Independent
documentation and operational-evidence work can proceed in parallel.

## Status Legend

| Status        | Meaning                                                |
| ------------- | ------------------------------------------------------ |
| `TODO`        | Not started                                            |
| `IN PROGRESS` | Currently being implemented                            |
| `BLOCKED`     | Cannot proceed because another item is required first  |
| `DONE`        | Implemented but not yet independently verified         |
| `VERIFIED`    | Implementation and evidence were independently checked |
| `N/A`         | Explicitly determined not to apply                     |

## Evidence Guidelines

Evidence should preferably reference one or more of:

* commit hash
* source file and function/type
* migration file
* schema definition
* test name
* qualification test
* fault-injection test
* race/concurrency test
* command output
* architecture guardrail
* documentation section
* reproducible manual verification procedure

A checklist entry should only be marked `VERIFIED` if the evidence demonstrates the stated invariant rather than merely showing that unrelated tests pass.

---

# P0 — Production Correctness Blockers

| ID      | Status | Requirement / Invariant                                                             | Required Work                                                           | Required Verification                        | Evidence |
| ------- | ------ | ----------------------------------------------------------------------------------- | ----------------------------------------------------------------------- | -------------------------------------------- | -------- |
| P0.1.1  | DONE | A successful purchase activates exactly the grants purchased by that offer          | Persist immutable purchased grant specifications                        | Inspect persisted purchase snapshot          | Recovery and activation share complete authority preflight; malformed sets retain payment evidence without grants: `TestRecoveryIncompleteCommercialAuthorityMatrix`; complete recovery remains exact: `TestRecoveredCommercialTermsIgnoreProviderAndReceiveTimes`; `TestRecoveryUsesPersistedSnapshotAfterLiveOfferMutation`. |
| P0.1.2  | VERIFIED | Purchased grant snapshots are scoped to the exact purchase                          | Key snapshots by purchase + capability or equivalent immutable identity | Schema/invariant test                        | `internal/store/store.go`: primary key `(purchase_id, capability_id)`; `internal/store/store_test.go`: `TestPurchaseGrantSnapshotsAreScopedToPurchase`; `go test ./...` PASS |
| P0.1.3  | VERIFIED | Grant activation must not depend on whatever grants happen to exist at webhook time | Materialize grants exclusively from purchase snapshots                  | End-to-end payment test                      | `internal/store/store.go`: success activation reads only `purchase_grant_snapshots`; `internal/store/store_test.go`: `TestPurchaseGrantSnapshotIsPersistedAndActivatesExactTerms` mutates the live grant before payment and verifies snapshot terms win; `go test ./...` PASS |
| P0.1.4  | VERIFIED | Unrelated grants for the same product must remain untouched                         | Restrict activation to purchased capability set                         | Test with unrelated capability               | `internal/store/store.go`: activation iterates only snapshot capabilities; `internal/store/store_test.go`: `TestPurchaseGrantSnapshotIsPersistedAndActivatesExactTerms` compares the complete unrelated grant before/after activation; `go test ./...` PASS |
| P0.1.5  | VERIFIED | Purchased limits must match the purchased offer exactly                             | Snapshot and activate storage, egress, operation limits and metrics     | Exact-value assertions                       | `internal/store/store.go`: snapshot schema and activation persist all typed limits/metrics; `internal/store/store_test.go`: `TestPurchaseGrantSnapshotIsPersistedAndActivatesExactTerms` asserts storage=123, egress=456, metric=`test_operations`, count=7; `go test ./...` PASS |
| P0.1.6  | DONE | Purchased offer identity must be preserved                                          | Persist and materialize exact `offer_id`                                | Purchase/grant assertion                     | Finalized purchase terms are immutable and activation materializes the exact offer; `internal/store/finalization_r3_test.go`. |
| P0.1.7  | VERIFIED | Purchased cadence must be preserved                                                 | Persist and materialize exact cadence                                   | Monthly/annual tests                         | `internal/store/store.go`: `purchase_grant_snapshots.period_cadence` is persisted and materialized into grants; `internal/store/store_test.go`: `TestPurchaseGrantSnapshotIsPersistedAndActivatesExactTerms` asserts monthly cadence; `TestPurchaseGrantSnapshotsAreScopedToPurchase` asserts distinct monthly/annual cadence; `go test ./...` PASS |
| P0.1.8  | DONE | Partial grant activation is forbidden                                               | Validate expected grant count/set before commit                         | Missing-capability transaction rollback test | Shared preflight checks exact count/digest and structural authority before recovery/activation; incomplete sets enter zero-access reconciliation: `TestRecoveryIncompleteCommercialAuthorityMatrix`; `TestCorruptedFinalizedSnapshotCannotActivatePartially`. |
| P0.1.9  | VERIFIED | Payment success and grant activation are atomic                                     | Perform state transition and materialization in one transaction         | Failure injection inside transaction         | `internal/store/store.go`: `applyBillingProviderEvent` updates purchase, grants, compatibility projection, and provider-event state in one transaction; `internal/store/store_test.go`: `TestPaymentGrantActivationRollsBackPartialGrantSet` verifies an injected grant failure leaves the purchase pending and no partial grant; `go test ./...` PASS |
| P0.1.10 | VERIFIED | Existing Standard/Pro state cannot leak into a Nano purchase                        | Replace inherited state with exact purchased snapshot semantics         | Legacy Standard/Pro → Nano test              | `cmd/tpahub/purchase_test.go`: `TestStripeTestNanoPurchaseAndWebhookActivatesOnce` seeds Standard-like storage/egress grants before webhook processing and asserts Nano offer, annual cadence, 5 MB storage, and 12 GB egress; `go test ./...` PASS |

---

# P0.2 — Checkout Supersession and Changed Intent

| ID     | Status | Requirement / Invariant                                                              | Required Work                                            | Required Verification            | Evidence |
| ------ | ------ | ------------------------------------------------------------------------------------ | -------------------------------------------------------- | -------------------------------- | -------- |
| P0.2.1 | DONE    | A provider-payable checkout must always remain locally reconcilable                  | Define explicit checkout supersession policy             | Architecture review              | Complete authority activates; incomplete authority atomically retains correlated provider evidence and blocks another checkout: `TestRecoveryIncompleteCommercialAuthorityMatrix`; `TestStripeWebhookPersistsIncompleteAuthorityEvidenceWithoutActivation`; supersession remains covered by `TestPendingPurchaseIntentChangeGetsNewIdentity`. |
| P0.2.2 | VERIFIED    | Changed purchase intent must not create paid-without-grant states                    | Reconcile, cancel, expire, or retain old checkout safely | Changed-intent E2E test          | `internal/store/store.go`: superseded purchases retain provider references and successful events materialize their snapshots; `cmd/tpahub/purchase_idempotency_test.go`: `TestPendingPurchaseIntentChangeGetsNewIdentity` processes the superseded checkout successfully and asserts no unreconcilable payment error; `go test ./...` PASS |
| P0.2.3 | DONE | Provider checkout and local purchase state must not silently diverge                 | Enforce provider/local lifecycle relationship            | State-machine test               | Complete commercial authority is preflighted before recovery finalization; every intrinsic snapshot defect enters durable `reconciliation_required` with correlated evidence and no grants: `TestRecoveryIncompleteCommercialAuthorityMatrix`; `TestReconciliationEvidencePrecedenceReplayAndRestart`; `TestProviderCheckoutProcessDeathRecoversExactPurchase`. |
| P0.2.4 | VERIFIED | `UpdatePurchaseCheckout` must update exactly one expected pending purchase           | Check affected row count                                 | Unit/integration test            | `internal/store/store.go`: `UpdatePurchaseCheckout` checks `RowsAffected() == 1`; `cmd/tpahub/purchase_idempotency_test.go`: verifies a superseded purchase cannot be updated; `go test ./...` PASS |
| P0.2.5 | DONE | Success after local `failed` must have defined behavior                              | Define event precedence                                  | Out-of-order event test          | Success wins only for a provider-linked purchase with finalized validity/snapshots: `internal/store/store.go`; `internal/store/finalization_r3_test.go`. |
| P0.2.6 | DONE | Success after local `expired` must have defined behavior                             | Define event precedence                                  | Out-of-order event test          | Provider-referenced expired purchases use the same finalized-term late-success rule; `TestPaymentSuccessWinsOverEarlierFailedEventForPayablePurchase`. |
| P0.2.7 | VERIFIED | Terminal event after success must not revoke paid grants unless explicitly supported | Protect paid terminal state                              | Success-then-expired/failed test | `internal/store/store.go`: terminal events mutate only pending purchases and paid purchases are marked processed without state changes; `internal/store/store_test.go`: `TestPaymentSuccessWinsOverEarlierFailedEventForPayablePurchase` sends a failed event after success and verifies the purchase remains paid; `go test ./...` PASS |
| P0.2.8 | VERIFIED | Concurrent changed-intent requests must have deterministic results                   | Serialize/reconcile intent replacement                   | Concurrent purchase test         | `cmd/tpahub/main.go`/`cmd/tpahub/purchase.go`: per-server `purchaseMu` serializes the complete local/provider checkout transition; `cmd/tpahub/purchase_idempotency_test.go`: `TestConcurrentChangedIntentLeavesDeterministicPurchaseStates` verifies two concurrent intents produce one pending and one superseded purchase; `go test ./...` PASS |
| P0.2.9 | DONE    | Paying any provider-returned valid checkout must never lose customer money           | Add full adversarial checkout race test                  | E2E provider simulation          | Provider success is either atomically activated from complete authority or durably correlated to zero-access reconciliation; unresolved reconciliation blocks duplicate checkout: `TestStripeWebhookPersistsIncompleteAuthorityEvidenceWithoutActivation`; `TestReconciliationEvidencePrecedenceReplayAndRestart`; valid superseded purchases remain independent in `TestReverseOrderPurchasesAreIndependentAndDeterministic`. |

---

# P0.3 — Hosted Account Provisioning

| ID     | Status | Requirement / Invariant                                                            | Required Work                                            | Required Verification        | Evidence |
| ------ | ------ | ---------------------------------------------------------------------------------- | -------------------------------------------------------- | ---------------------------- | -------- |
| P0.3.1 | VERIFIED | Hosted production accounts must not receive accidental permanent free grants       | Remove implicit permanent Nano provisioning              | Registration test            | `cmd/tpahub/auth.go`: `initialTPAGrants` returns no grants when billing purchases are enabled; `cmd/tpahub/web.go` uses the same policy; `cmd/tpahub/auth_test.go`: `TestHostedRegistrationDoesNotProvisionPermanentTPAGrants`; `go test ./...` PASS |
| P0.3.2 | VERIFIED | New hosted accounts must not be labeled `legacy`                                   | Introduce explicit provisioning source/policy            | Persistence assertion        | `cmd/tpahub/auth.go`: hosted provisioning passes no legacy grants; `internal/store/store.go`: no entitlement row is created when no grants are supplied; `cmd/tpahub/auth_test.go`: `TestHostedRegistrationDoesNotProvisionPermanentTPAGrants` asserts no active grant and `sql.ErrNoRows` for entitlement; `go test ./...` PASS |
| P0.3.3 | VERIFIED | Development/local mode provisioning must be explicit                               | Define development/self-host policy                      | Config test                  | `cmd/tpahub/auth.go`: `initialTPAGrants` explicitly selects local defaults only when billing is disabled; `cmd/tpahub/auth_test.go`: `TestLocalRegistrationExplicitlyProvisionsTPAGrants` verifies the local legacy grant policy; `go test ./...` PASS |
| P0.3.4 | DONE | Historical migrated accounts must have explicit grandfathering semantics           | Separate legacy migration policy                         | Migration test               | Policy is a one-time six-calendar-month `migration` grant, never permanent legacy authorization: `docs/migration-access-policy.md`; `TestHistoricalAccountsReceiveOneBoundedNanoMigrationGrant`; `TestBoundedMigrationEligibilityIsTPAScopedAndActive`. |
| P0.3.5 | DONE | Buying a product must not reduce pre-existing grandfathered rights unintentionally | Define precedence/merge behavior                         | Grandfathered purchase test  | Migration and purchase grant instances remain independent contributors in both orders; `TestMigrationAndPurchasesUseOnlyActiveEnabledContributors`; `TestPurchaseExpiryDoesNotResetOrRemoveMigration`; `TestPurchaseBeforeBoundedMigrationRemainsIndependent`. |
| P0.3.6 | VERIFIED    | Production access policy must be explicit                                          | Choose no grant, disabled grant, or bounded trial        | Product policy documentation | Policy: hosted production accounts receive no product grant by default; access begins only after successful purchase activation. `cmd/tpahub/auth.go`: billing-enabled provisioning is fail-closed; `cmd/tpahub/auth_test.go`: `TestHostedRegistrationDoesNotProvisionPermanentTPAGrants`; `go test ./...` PASS |
| P0.3.7 | DONE | Paying customer access must not be less durable than non-paying access by accident | Validate post-purchase state against provisioning policy | Lifecycle test               | Historical paid grants are atomically materialized as immutable purchase instances before bounded migration replaces compatibility rows; active instances block duplicate checkout: `TestHistoricalPaidGrantSurvivesOverlappingBoundedMigration`. |

---

# P0.4 — Crash-Consistent Repository Publication

| ID      | Status | Requirement / Invariant                                                    | Required Work                              | Required Verification | Evidence |
| ------- | ------ | -------------------------------------------------------------------------- | ------------------------------------------ | --------------------- | -------- |
| P0.4.1  | VERIFIED | Publication artifacts must be immutable per generation                     | Introduce versioned artifact generations   | Source inspection     | `cmd/tpahub/main.go`: successful publication writes to `repos/<id>/generations/<job-id>` and readers resolve the active generation through `current`; prior generations are retained; `cmd/tpahub/main_test.go`: `TestRepositoryGenerationsUseActivePointer`; `go test ./...` PASS |
| P0.4.2  | DONE | Candidate generation must be complete before publication                   | Build full candidate generation first      | Publish test          | `verifyPublicationCandidate` requires signed Release architectures to exactly match `binary-*` trees and package stanzas (`Architecture: all` is accepted only in a valid declared index), in addition to indexes, checksums, and package history; `TestPublicationCandidateSemanticVerification`. |
| P0.4.3  | DONE | Candidate repository must be verified before live swap                     | Add explicit verification boundary         | Failure test          | Verification checks the configured signer fingerprint and is time-bounded; `TestSuccessfulRunnerMalformedCandidatesNeverActivate`. |
| P0.4.4  | VERIFIED | Live repository switch must remain atomic                                  | Preserve atomic directory exchange         | Qualification test    | `cmd/tpahub/main.go`: `atomicReplaceSymlink` uses Linux `renameat2(RENAME_EXCHANGE)` when replacing an active generation pointer and a single rename for first publication; readers resolve `current`; `go test ./...` PASS |
| P0.4.5  | DONE | Active artifact generation must be durably recorded                        | Persist active generation identity         | DB assertion          | First-publication repository/generation directory entries and pointer swaps are fsynced before `Store.CommitPublication`; generation deletion is fsynced before persisted cleanup retry state is cleared. |
| P0.4.6  | DONE | Job success must correspond to the same committed generation               | Bind job completion to active generation   | Crash/restart test    | Generation authority and job success share one SQLite transaction; `TestPostCommitFailureLeavesActiveGenerationAndJobConsistent`; `TestPublicationProcessDeathRecoveryMatrix`. |
| P0.4.7  | VERIFIED | Old authoritative artifacts must not be deleted before durable replacement | Reorder publication cleanup                | Fault injection       | `cmd/tpahub/main.go`: old artifact cleanup runs only after candidate verification, active-pointer replacement, durable generation recording, and job-success binding; `go test ./...` PASS |
| P0.4.8  | VERIFIED | Old generations may be garbage-collected only after durable success        | Add post-commit GC                         | Source/test evidence  | `cmd/tpahub/main.go`: `collectOldGenerations` runs only after durable activation/job-success binding and excludes the active generation; `cmd/tpahub/main_test.go`: `TestGenerationRetentionPreservesActiveAndRecentHistory`; `generation_cleanup_failure_test.go`: failure/retry and partial-GC tests preserve active authority |
| P0.4.9  | VERIFIED    | Startup must reconcile incomplete publication generations                  | Implement restart reconciliation           | Restart fault test    | Startup enumerates durable repository rows before filesystem roots, reconstructs unpublished roots, fails closed on missing recorded generations, and resumes persisted old-generation cleanup: `TestPublicationReconciliationEnumeratesDurableRepositoriesBeforeFilesystem`, `TestGenerationCleanupRetriesAutonomouslyAndAfterReopen`. |
| P0.4.10 | VERIFIED | Startup must detect stale staging/work directories                         | Scan and reconcile stale state             | Startup cleanup test  | `cmd/tpahub/main.go`: startup removes transient entries under `work` while preserving durable incoming/job state; `cmd/tpahub/main_test.go`: `TestStartupCleansAbandonedStagingWorkspaces`; `go test ./...` PASS |
| P0.4.11 | DONE | Crash after candidate build must be recoverable                            | Fault-injection point                      | Restart assertion     | `TestPublicationProcessDeathRecoveryMatrix/candidate-built` uses subprocess `os.Exit`, reopens, reconciles, and publishes a successful retry. |
| P0.4.12 | DONE | Crash before live swap must preserve previous repo                         | Fault-injection point                      | Restart assertion     | `TestPublicationProcessDeathRecoveryMatrix/before-live-swap` proves old durable authority and removes the failed orphan generation. |
| P0.4.13 | DONE | Crash immediately after live swap must be recoverable                      | Fault-injection point                      | Restart assertion     | `TestPublicationProcessDeathRecoveryMatrix/after-live-swap`; `TestDurableGenerationControlsReadsDuringPointerSwapWindow`. |
| P0.4.14 | DONE | Crash before DB success commit must be recoverable                         | Fault-injection point                      | Restart assertion     | `TestPublicationProcessDeathRecoveryMatrix/after-live-swap` proves restart restores old authority and permits retry. |
| P0.4.15 | DONE | Crash after DB success commit must leave consistent state                  | Fault-injection point                      | Restart assertion     | `TestPublicationProcessDeathRecoveryMatrix/after-durable-record` proves active generation and job success remain atomically consistent. |
| P0.4.16 | DONE | Rename failure must not lose previously published packages                 | Inject rename failure                      | Recovery test         | `TestWorkerPointerRenameFailureRollsBackAndRetrySucceeds` exercises worker failure state, preserved authority, and retry. |
| P0.4.17 | DONE | Disk-full failure must not corrupt live or authoritative state             | Inject ENOSPC                              | Recovery test         | `internal/tpa/bounded_test.go` proves hard-bound ENOSPC; `TestWorkerCandidateHardBoundENOSPCPreservesAuthorityAndRetry` proves worker recovery. |
| P0.4.18 | DONE | Signing failure must leave previous live generation intact                 | Break signing configuration during publish | Publish test          | `TestWorkerSigningFailurePreservesAuthorityAndRetry` uses the production TPA execution path. |
| P0.4.19 | DONE | Next publish after recovery must preserve all previously valid packages    | Publish after crash matrix                 | Qualification test    | Worker rename, ENOSPC, signing, and post-swap failure tests each prove a subsequent successful retry. |

---

# P0.5 — Suspended Account Enforcement

| ID     | Status | Requirement / Invariant                                     | Required Work                                    | Required Verification | Evidence |
| ------ | ------ | ----------------------------------------------------------- | ------------------------------------------------ | --------------------- | -------- |
| P0.5.1 | VERIFIED | Suspended accounts must fail bearer authentication          | Preserve/verify active-account join              | Test                  | `internal/store/store.go`: bearer token lookup joins active accounts; `cmd/tpahub/auth_test.go`: `TestAccountStatusEnforcedAcrossAuthenticationPaths`; `go test ./...` PASS |
| P0.5.2 | VERIFIED | Suspended accounts must fail browser session authentication | Preserve/verify active-account join              | Test                  | `internal/store/store.go`: session lookup joins active accounts; `cmd/tpahub/auth_test.go`: `TestAccountStatusEnforcedAcrossAuthenticationPaths`; `go test ./...` PASS |
| P0.5.3 | VERIFIED | Suspended accounts must fail reader-token private access    | Join reader token → repository → account status  | Test                  | `internal/store/store.go`: `ReaderRepository` joins credentials through repository ownership and requires `accounts.status='active'`; `cmd/tpahub/auth_test.go`: reader access is denied after account suspension; `go test ./...` PASS |
| P0.5.4 | VERIFIED | Reader authorization must verify token is not revoked       | Preserve token revocation validation             | Test                  | `internal/store/store.go`: reader query requires `revoked_at IS NULL`; `cmd/tpahub/auth_test.go`: `TestAuthenticationAndPrivateReader` revokes a credential and verifies 401; `go test ./...` PASS |
| P0.5.5 | VERIFIED | Reader authorization must verify repository ownership       | Validate ownership/customer relationship         | Test                  | `internal/store/store.go`: reader lookup joins credential to its repository and account; credential management uses ownership-scoped queries; `go test ./...` PASS |
| P0.5.6 | VERIFIED | Reader authorization must verify required grant             | Check active private/read grant                  | Test                  | `cmd/tpahub/main.go`/`cmd/tpahub/credentials.go`: private delivery and credential management require the active `repository.read.private` grant; existing private-reader integration coverage; `go test ./...` PASS |
| P0.5.7 | VERIFIED    | Queued jobs must not publish after account suspension       | Recheck account immediately before publication   | Worker test           | `cmd/tpahub/main.go`: `ensurePublishAuthorized` runs immediately before pointer activation; `cmd/tpahub/main_test.go`: `TestSuspendedAccountCannotRunQueuedPublication`, `TestSuspensionImmediatelyBeforeLiveSwapBlocksPublication`, and `TestGrantRevocationImmediatelyBeforeLiveSwapBlocksPublication`. |
| P0.5.8 | DONE | Re-activating account must follow explicit policy           | Verify reader/worker behavior after reactivation | Test                  | Reactivation restores reader and queued-worker behavior only when grants remain authoritative; `cmd/tpahub/reactivation_r3_test.go`. |

---

# P0.6 — Fail-Closed Legacy Migration

| ID      | Status | Requirement / Invariant                               | Required Work                                | Required Verification | Evidence |
| ------- | ------ | ----------------------------------------------------- | -------------------------------------------- | --------------------- | -------- |
| P0.6.1  | VERIFIED | Only known legacy plans may migrate                   | Explicitly map Nano, Standard, Pro           | Migration test        | `internal/store/store.go`: migration accepts only the explicit `nano`, `standard`, and `pro` plan set before grant materialization; `go test ./...` PASS |
| P0.6.2  | VERIFIED | Unknown plan must not become unlimited                | Fail migration or quarantine row             | Unknown-plan test     | `internal/store/store.go`: unknown legacy plans fail migration before inserts; `internal/store/store_test.go`: `TestUnknownLegacyPlanFailsClosedDuringMigration`; `go test ./...` PASS |
| P0.6.3  | VERIFIED | Purchase-backed grant requires authoritative purchase | Validate linked purchase exists              | Migration test        | Historical migration requires exact account, product, offer, paid status, and validity-window agreement with the authoritative purchase before immutable instance materialization; `TestMalformedHistoricalMigrationMatrixFailsClosedAndRepeats`. |
| P0.6.4  | VERIFIED | Purchase-backed grant requires paid purchase state    | Validate purchase state                      | Migration test        | `internal/store/store.go`: purchase-backed grants require `billing_purchases.status='paid'`; orphan/unpaid rows abort migration; `go test ./...` PASS |
| P0.6.5  | VERIFIED | Purchase-backed grant requires bounded validity       | Reject empty `valid_until`                   | Migration test        | `internal/store/store.go`: purchase-backed grants require parseable non-empty validity bounds; `go test ./...` PASS |
| P0.6.6  | VERIFIED | Grant validity must satisfy end > start               | Add validation                               | Boundary test         | `internal/store/store.go`: migration rejects purchase-backed validity where `valid_until` is not strictly after `valid_from`; `go test ./...` PASS |
| P0.6.7  | VERIFIED | Malformed rows must not silently become active grants | Fail closed or explicitly disable/quarantine | Migration test        | `internal/store/store.go`: malformed plan, purchase linkage, status, timestamp, and validity rows return migration errors before materialization; `internal/store/store_test.go`: unknown-plan and orphan-grant fail-closed tests; `go test ./...` PASS |
| P0.6.8  | VERIFIED | Rejected rows must be diagnosable                     | Log/report migration rejection               | Operational evidence  | `internal/store/store.go`: migration errors include rejected plan, purchase ID, status, and validation category; `Open` returns migration errors instead of activating invalid rows; `go test ./...` PASS |
| P0.6.9  | VERIFIED | Migration must remain transactional                   | Verify migration transaction boundary        | Fault-injection test  | `internal/store/store.go`: validation and grant materialization execute within one SQLite transaction with rollback on every error; `go test ./...` PASS |
| P0.6.10 | DONE | Migration must remain idempotent                      | Re-run migration repeatedly                  | Restart test          | `legacy-six-month-nano-v1` assigns one immutable six-month term transactionally; `TestHistoricalAccountsReceiveOneBoundedNanoMigrationGrant`; `TestBoundedMigrationInterruptionReopensIdempotently`; `TestNewHostedAccountDoesNotReceiveMigrationGrant`. |
| P0.6.11 | VERIFIED    | Mid-migration crash must recover safely               | Kill between migration steps                 | Restart test          | `internal/store/store.go`: validation, materialization, marker and commit are one transaction with fault checkpoints; `internal/store/migration_matrix_test.go`: `TestMigrationInterruptionReopenMatrix` interrupts four boundaries, proves no partial state/marker, then reopens twice idempotently. |

---

# P0.7 — Upload and Staging Capacity

| ID      | Status | Requirement / Invariant                                        | Required Work                        | Required Verification | Evidence |
| ------- | ------ | -------------------------------------------------------------- | ------------------------------------ | --------------------- | -------- |
| P0.7.1  | VERIFIED    | Small storage grants must not allow unbounded staging usage    | Add staging quota                    | Test                  | `cmd/tpahub/main.go`: upload staging is checked against the active publish storage grant before queueing; `cmd/tpahub/main_test.go`: `TestUploadStagingRespectsStorageGrant`; `go test ./...` PASS |
| P0.7.2  | DONE | Outstanding incoming bytes must be bounded per account         | Track pending incoming bytes         | Test                  | Incoming reservations round files and directory/file-count metadata to physical filesystem allocation; `TestConcurrentUploadsUseRealReservationPath`. |
| P0.7.3  | DONE | Outstanding work bytes must be bounded per account             | Track work bytes                     | Test                  | Commercial storage admission counts exact logical active/incoming payload bytes, while global and per-account transient staging use physical block/inode reservations; `TestHTTPUploadUsesLogicalBytesForCommercialQuota`, `TestPerAccountLogicalQuotaAndPhysicalTransientLimitAreIndependent`. |
| P0.7.4  | VERIFIED | Clearly impossible uploads should be rejected before queueing  | Compare upload size against policy   | Request test          | `cmd/tpahub/main.go`: upload is rejected with 413 before `CreateJob` when grant capacity cannot contain it; `go test ./...` PASS |
| P0.7.5  | VERIFIED | Pending jobs must count toward staging pressure                | Add queued-byte accounting           | Queue saturation test | `cmd/tpahub/main.go`: all bytes present under `incoming` are counted, including queued jobs, while the staging mutex prevents concurrent bypass; `go test ./...` PASS |
| P0.7.6  | DONE | Global staging usage must be bounded                           | Add global configured limit          | Test                  | Global admission uses physical filesystem allocation plus reservations; `TestConcurrentUploadsUseRealReservationPath`; `TestCapacityReservationReconstructsFromFilesystemAfterRestart`; `TestPerAccountStagingUsesPhysicalAllocation`. |
| P0.7.7  | VERIFIED    | Global disk headroom must be protected                         | Add disk-free threshold              | ENOSPC/headroom test  | Upload, candidate, and repository creation reserve disk and inode resources; failed repository DB insertion removes and fsyncs its root, with DB-orphaned roots retried live/startup: `TestRepositoryCreationUsesResourceAdmissionAndRollsBackRoot`. |
| P0.7.8  | VERIFIED    | Terminal jobs must remove incoming data                        | Cleanup success/failure/cancel paths | Test                  | `cmd/tpahub/main.go`: success/failure remove incoming; `cmd/tpahub/cleanup_recovery_test.go`: `TestStartupCleanupMatrixIsIdempotentAndProtectsAuthoritativeState`; `cleanup_failure_test.go`: terminal-removal failure and retry coverage. |
| P0.7.9  | VERIFIED    | Terminal jobs must remove work data                            | Cleanup success/failure/cancel paths | Test                  | `cmd/tpahub/main.go`: `process` defers work/candidate removal; `cmd/tpahub/cleanup_failure_test.go`: `TestPartialCleanupResumesAfterRestart` proves interrupted cleanup retries idempotently. |
| P0.7.10 | VERIFIED    | Startup must clean abandoned job directories                   | Startup reconciliation               | Restart test          | `cmd/tpahub/main.go`: `reconcileStagingState`; `cmd/tpahub/main_test.go`: `TestStartupCleansAbandonedStagingWorkspaces`; `cmd/tpahub/cleanup_failure_test.go`: `TestPartialCleanupResumesAfterRestart` proves retry after partial removal |
| P0.7.11 | DONE | Many queued large jobs cannot exhaust filesystem beyond policy | Adversarial queue test               | Disk-usage assertion  | Physical reservations, bounded multipart file count, and hard-bounded candidate tmpfs prevent sparse/metadata/candidate bypass; direct concurrency and ENOSPC tests cover denial. |

---

# P0.8 — Conservative Egress Enforcement

| ID     | Status | Requirement / Invariant                                                  | Required Work                     | Required Verification | Evidence |
| ------ | ------ | ------------------------------------------------------------------------ | --------------------------------- | --------------------- | -------- |
| P0.8.1 | VERIFIED | Grant lookup failure must not result in unrestricted download            | Conservative deny/throttle policy | Inject error test     | `cmd/tpahub/main.go`: known grant denial is rate-limited and unexpected grant-authority failure returns 503; `cmd/tpahub/egress_enforcement_test.go`: injected authority failure is denied; `go test ./...` PASS |
| P0.8.2 | DONE | Usage lookup failure must not result in unrestricted download            | Conservative deny/throttle policy | Inject DB error test  | Durable delivery uncertainty blocks future authorization across restart until explicit recovery; `TestEgressUncertaintySurvivesRestartUntilExplicitRecovery`; `TestUnownedRepositoryUncertaintyFailsClosedAfterRestart`. |
| P0.8.3 | DONE | Accounting persistence failure must be observable                        | Structured failure logging        | Test/log evidence     | Failed completion records durable uncertainty and logs repository/bytes/date context; `docs/egress-recovery.md`. |
| P0.8.4 | VERIFIED | Missing grant behavior must be explicit                                  | Document deny/throttle policy     | Test                  | Policy: missing grant is conservatively rate-limited; `cmd/tpahub/egress_enforcement_test.go`: `ErrGrantNotFound` coverage; `go test ./...` PASS |
| P0.8.5 | VERIFIED    | Disabled grant behavior must be explicit                                 | Document deny/throttle policy     | Test                  | Policy: disabled grants are conservatively limited; `cmd/tpahub/egress_enforcement_test.go`: `TestEgressEnforcementUsesGrantLimit` injects `commerce.ErrGrantDisabled` and asserts the limited response. |
| P0.8.6 | VERIFIED | Expired grant behavior must be explicit                                  | Document deny/throttle policy     | Test                  | Expired/inactive grants follow the same conservative limited path through `grantDenied`; `cmd/tpahub/egress_enforcement_test.go`; `go test ./...` PASS |
| P0.8.7 | VERIFIED    | Malformed period must fail conservatively                                | Validate period                   | Test                  | Bounded egress markers, settlement, recovery, and aggregation retain fixed-width nanosecond UTC instants with exact half-open comparisons; legacy daily buckets are counted conservatively at non-midnight boundaries: `TestEgressReservationUsesExactHalfOpenInstants`, `TestEgressWindowSortsFractionalInstantsAndConservativelyCountsLegacyDays`. |
| P0.8.8 | VERIFIED | Private repository behavior on egress-authority failure must be explicit | Decide deny vs throttle           | Test                  | Policy: private repository delivery returns 503 when egress authority is unavailable; `cmd/tpahub/egress_enforcement_test.go` verifies this with valid private credentials; `go test ./...` PASS |

---

# P0-R4 — Remediation Re-evaluation

The following P0 invariants were reopened after the independent R3 audit. They
are implemented but remain `DONE`, not `VERIFIED`, until the independent
re-audit confirms the production diff: purchase ownership and instance
immutability, migration eligibility and timestamp parsing, package identity and
history preservation, process/inode capacity and incoming-orphan cleanup,
marker-specific egress recovery, superseded terminal events, commit-boundary
expiry, and reactivation behavior. Direct evidence is in
`internal/store/r4_regression_test.go`, `cmd/tpahub/r4_regression_test.go`, the
package identity case in `cmd/tpahub/main_test.go`, and the updated egress
recovery test.

# P0-R10 — Provider Recovery Commercial Authority

Checkout recovery now reconstructs exact provider linkage while preserving the
product-owned purchase validity and grant snapshots persisted before the
provider side effect. Provider event, receive, replay, and wall-clock times do
not define validity. Incomplete historical authority enters fail-closed
`reconciliation_required`; provider evidence remains durable, grants do not
activate, and another checkout is blocked. Direct evidence is in
`internal/store/recovery_r10_test.go`, the corrected finalization regression,
and the provider process-death test. Independent re-audit remains required.

# P0-R11 — Complete Recovery Authority and Provider Correlation

Checkout recovery and successful payment processing now share deterministic,
read-only commercial-authority preflight covering purchase terms, validity,
snapshot finalization, snapshot-set count/digest, exact purchase-scoped rows,
structural cadence/limit validity, and product-owned TPA capability identity.
Incomplete authority atomically retains correlated provider evidence in
`reconciliation_required`, creates no authorization contributor, remains
blocked across retries/restart, and is not falsely finalized. Non-empty provider
references are SQLite-unique by provider and environment; opening a historical
DB with duplicates fails explicitly without changing either purchase. Direct
evidence is in `internal/store/recovery_r11_test.go`, updated corruption tests,
and `docs/payment-checkout-recovery.md`. Independent re-audit remains required.

# P0-R12 — Atomic Incomplete-Authority Provider Evidence

The production operation-ID webhook now sends provider correlation, complete
commercial-authority validation, reconciliation, and provider-event evidence
through one store transaction. Incomplete authority therefore commits
`reconciliation_required`, provider linkage, immutable failure reason,
`provider_success_unreconciled`, and event identity/type together, while
remaining zero-access and checkout-blocked. A test-only store checkpoint proves
rollback at the former recovery/payment transaction boundary and deterministic
restart replay. Direct evidence is in
`internal/store/recovery_r11_test.go`; independent re-audit remains required.

# P1 — Architecture Hardening

## P1.1 — Remove TPA Identity from Shared Commerce

| ID     | Status | Requirement / Invariant                                        | Required Work                                            | Required Verification     | Evidence |
| ------ | ------ | -------------------------------------------------------------- | -------------------------------------------------------- | ------------------------- | -------- |
| P1.1.1 | DONE — READY FOR INDEPENDENT REVIEW | `internal/commerce` must not define a concrete TPA Hub product | Move `ProductTPAHub` into product-owned package | Dependency inspection | `internal/tpahubproduct/product.go` owns `ProductID`; `internal/commerce/commerce.go` defines no concrete product identity; repository search confirms no `commerce.ProductTPAHub` references. |
| P1.1.2 | DONE — READY FOR INDEPENDENT REVIEW | Generic APIs must reject empty product IDs | Add validation | Unit test | `internal/store/store.go`: product-aware account/grant, purchase, provider-submission, and mock-event APIs reject empty product IDs; `internal/store/p1_architecture_test.go`: focused missing-identity, atomic account, and mock round-trip tests. |
| P1.1.3 | DONE — READY FOR INDEPENDENT REVIEW | Missing product ID must never silently default to TPA Hub | Remove fallback behavior | Product #2 test | Generic account/grant, purchase, provider-submission, and mock-event persistence no longer supplies a TPA Hub fallback or persists an empty identity; missing identity is rejected by the focused architecture tests. |
| P1.1.4 | DONE — READY FOR INDEPENDENT REVIEW | Legacy TPA behavior must be explicitly named | Restrict compatibility wrappers to migration/legacy code | Source inspection | TPA-specific migration and compatibility projection logic references `tpahubproduct.ProductID`; generic product-aware APIs are explicit; legacy mock and entitlement wrappers are explicitly named (`LegacyTPA*`) and delegate to product-aware methods. |
| P1.1.5 | DONE — READY FOR INDEPENDENT REVIEW | Shared persistence must not contain runtime TPA defaults | Remove/search generic store defaults | Repository search + tests | `CreateAccount`, `CreatePendingPurchase`, `SaveProviderSubmission`, and product-aware mock-event APIs no longer default or persist missing product identity; `SetLegacyTPAEntitlement` explicitly owns the TPA compatibility path, with current product selection otherwise explicit. |

---

## P1.2 — Single Authoritative Offer Definition

| ID     | Status | Requirement / Invariant                                                                    | Required Work                           | Required Verification    | Evidence |
| ------ | ------ | ------------------------------------------------------------------------------------------ | --------------------------------------- | ------------------------ | -------- |
| P1.2.1 | DONE — READY FOR INDEPENDENT REVIEW | Price must have one authoritative source | Consolidate rate card/offer price | Checkout amount test | `tpahubproduct.NanoOffer().AmountMinor` is the authoritative current Nano price; production rate-card and checkout amounts derive from it. Mock rate-card amounts remain explicitly virtual/test-only. `cmd/tpahub/production_rate_card_test.go` and `purchase_test.go` verify consistency. |
| P1.2.2 | DONE — READY FOR INDEPENDENT REVIEW | Cadence must have one authoritative source | Consolidate cadence | Offer snapshot test | `tpahubproduct.NanoCadence` is the current Nano cadence authority; `commerce.Offer.Cadence`, Nano grants, production rate-card cadence, fresh validity construction, and production finalization derive from or validate against it. Calendar cadence tests cover annual and monthly behavior; persisted purchase terms remain historical. |
| P1.2.3 | DONE — READY FOR INDEPENDENT REVIEW | Grant limits must have one authoritative source | Consolidate limits | Grant assertion | `tpahubproduct.NanoOffer().Grants` owns current Nano capability limits; local grants and the Nano plan catalog derive from those terms, purchase snapshots persist them, and activation/enforcement use persisted grant terms.`
| P1.2.4 | DONE — READY FOR INDEPENDENT RE-REVIEW | Offer versions must be immutable once purchased | Preserve captured version across historical lifecycle; add fixed version-keyed commercial snapshot | Historical purchase and drift-guard tests | `tpahubproduct.NanoOffer().Version` is captured on fresh Nano purchases and persisted as purchase authority; historical checkout/finalization preserves its captured version without comparing to today's offer; legacy rows remain explicitly `legacy-unversioned`, finalized version identity remains immutable; `TestHistoricalNanoPurchaseSurvivesCurrentOfferVersionChange` and the fixed `nano-v1` commercial snapshot guardrail cover the remediation.`
| P1.2.5 | DONE — READY FOR INDEPENDENT REVIEW | Purchase must snapshot offer terms | Verify complete typed purchase/grant snapshot authority | DB assertion | `CreatePendingPurchase` atomically persists ProductID, OfferID (`plan_code`), OfferVersion, amount, currency, validity, and purchase-scoped grant snapshots plus count/digest metadata; `TestFreshNanoPurchaseSnapshotSurvivesReopen` verifies round-trip persistence, and the production Nano purchase test verifies current offer capture. Historical recovery/activation continues to use persisted authority without current-offer reconstruction.`
| P1.2.6 | DONE — READY FOR INDEPENDENT REVIEW | Checkout must charge exactly snapshotted amount | Derive provider checkout requests directly from persisted purchase amount/currency; preserve current pricing only for fresh purchase construction | Production-path provider request and historical price-change retry test | `createPurchaseCheckout` forwards persisted `BillingPurchase.AmountMinor` and `Currency`; `TestHistoricalPurchaseCheckoutUsesPersistedAmountAfterPriceChange` records both failed/retried provider requests and verifies they remain 725 EUR while current Nano remains 500 EUR; fresh production checkout remains covered by `TestStripeTestNanoPurchaseAndWebhookActivatesOnce`.`
| P1.2.7 | DONE — READY FOR INDEPENDENT REVIEW | Grant activation must use snapshotted terms | Materialize paid purchase grants exclusively from persisted purchase snapshots with complete-set validation and atomic activation | Historical snapshot activation and exact grant-instance assertions | `ApplyProviderPaymentEvent` validates the persisted snapshot set and materializes `purchase_grant_instances` without current-offer reconstruction; `TestHistoricalNanoActivationUsesPersistedSnapshotAfterReopen` closes/reopens the store, changes historical cadence/limits/operation terms from current Nano, and verifies exact activation from SQLite rows; existing corruption, rollback, replay, and unrelated-contributor tests remain green.`
| P1.2.8 | TODO   | Monthly pricing must require product configuration change, not scattered application edits | Validate 5/10 EUR monthly offer change  | Product config test      |          |

---

## P1.3 — Provider-Neutral Payment Correlation

| ID     | Status | Requirement / Invariant                                                   | Required Work                          | Required Verification | Evidence |
| ------ | ------ | ------------------------------------------------------------------------- | -------------------------------------- | --------------------- | -------- |
| P1.3.1 | TODO   | Every provider operation must have provider-neutral correlation identity  | Use `OperationID` or equivalent        | Contract test         |          |
| P1.3.2 | TODO   | Adapter must persist reserved correlation metadata itself when applicable | Move correlation encoding into adapter | Stripe test           |          |
| P1.3.3 | TODO   | Product metadata must not overwrite reserved correlation fields           | Reject conflicts                       | Test                  |          |
| P1.3.4 | TODO   | New Stripe sessions must not require TPA-specific purchase metadata       | Use generic operation reference        | Webhook test          |          |
| P1.3.5 | TODO   | Legacy TPA metadata may remain read-only compatibility fallback           | Isolate fallback                       | Legacy test           |          |
| P1.3.6 | TODO   | Product #2 must correlate payment without TPA-specific code               | Add product #2 payment test            | E2E                   |          |

---

## P1.4 — Validity Derived from Purchase Terms

| ID     | Status | Requirement / Invariant                                     | Required Work                             | Required Verification        | Evidence |
| ------ | ------ | ----------------------------------------------------------- | ----------------------------------------- | ---------------------------- | -------- |
| P1.4.1 | TODO   | Webhook route must not invent commercial validity           | Remove caller-supplied arbitrary validity | Source inspection            |          |
| P1.4.2 | TODO   | Purchased cadence must be persisted                         | Purchase terms snapshot                   | DB assertion                 |          |
| P1.4.3 | TODO   | Activation validity must derive from purchased terms        | Centralize derivation                     | Payment test                 |          |
| P1.4.4 | TODO   | Delayed webhook behavior must be explicit                   | Define activation timestamp semantics     | Delayed-event test           |          |
| P1.4.5 | TODO   | Monthly offers must work without TPA-specific webhook logic | Add monthly test                          | E2E                          |          |
| P1.4.6 | TODO   | Annual offers must remain supported                         | Regression test                           | E2E                          |          |
| P1.4.7 | TODO   | Future scheduled renewal must be representable              | Validate terms model                      | Renewal test/design evidence |          |

---

## P1.5 — Make `account_entitlements` Non-Authoritative

| ID     | Status | Requirement / Invariant                                                              | Required Work                     | Required Verification     | Evidence |
| ------ | ------ | ------------------------------------------------------------------------------------ | --------------------------------- | ------------------------- | -------- |
| P1.5.1 | DONE   | Grants are the runtime source of truth                                               | Verify all runtime reads          | Repository search         | `GetGrant` and purchase admission no longer read the projection |
| P1.5.2 | DONE   | Missing entitlement projection must not block valid payment activation               | Remove veto dependency            | Payment test              | Payment activation writes grants and immutable evidence only |
| P1.5.3 | DONE   | Projection divergence must not change authorization                                  | Make projection derived-only      | Divergence test           | Stale/conflicting projection regression tests |
| P1.5.4 | DONE   | Projection writes must not succeed while authoritative grant mutation silently fails | Tighten/update or remove path     | Test                      | Runtime payment path no longer writes the projection |
| P1.5.5 | DONE   | Removal path must be documented                                                      | Define migration/removal plan     | Documentation             | Table retained; legacy reads/writes are migration compatibility only |
| P1.5.6 | TODO   | Projection should eventually be removed if no longer required                        | Remove after compatibility window | Commit/migration evidence |          |

---

## P1.6 — Separate Download Permission from Egress Limits

| ID     | Status | Requirement / Invariant                                                   | Required Work                             | Required Verification | Evidence |
| ------ | ------ | ------------------------------------------------------------------------- | ----------------------------------------- | --------------------- | -------- |
| P1.6.1 | TODO   | Capability semantics must distinguish permission from quantitative limits | Review `repository.download`              | Design evidence       |          |
| P1.6.2 | TODO   | Public repository download policy must be explicit                        | Define behavior                           | Test                  |          |
| P1.6.3 | TODO   | Private repository download policy must be explicit                       | Define behavior                           | Test                  |          |
| P1.6.4 | TODO   | Egress allowance must not ambiguously act as permission                   | Split or rename semantics                 | Capability tests      |          |
| P1.6.5 | TODO   | Expired product behavior must be explicit                                 | Define metadata vs `.deb` access behavior | Test                  |          |

---

## P1.7 — Single-Instance Publication Invariant

| ID     | Status | Requirement / Invariant                                                                        | Required Work                        | Required Verification  | Evidence |
| ------ | ------ | ---------------------------------------------------------------------------------------------- | ------------------------------------ | ---------------------- | -------- |
| P1.7.1 | TODO   | Current deployment model must explicitly be single-instance if multi-instance safety is absent | Document invariant                   | Documentation          |          |
| P1.7.2 | TODO   | One data directory may have only one active TPA Hub process                                    | Add process/data-dir lock            | Second-process test    |          |
| P1.7.3 | TODO   | Authorization must be rechecked immediately before live publish switch                         | Recheck grant/account status         | Expiry/suspension test |          |
| P1.7.4 | TODO   | Storage capacity must be rechecked immediately before live switch                              | Re-evaluate canonical storage policy | Race test              |          |
| P1.7.5 | TODO   | Multi-instance support must not be implied until explicitly implemented                        | Documentation/config guard           | Review                 |          |

---

## P1.8 — Minimal Renewal Support

| ID     | Status | Requirement / Invariant                                           | Required Work                        | Required Verification | Evidence |
| ------ | ------ | ----------------------------------------------------------------- | ------------------------------------ | --------------------- | -------- |
| P1.8.1 | TODO   | Customer must be able to renew before current paid period expires | Allow renewal purchase               | Renewal test          |          |
| P1.8.2 | TODO   | Renewal period must start at current valid grant end              | Schedule future validity             | Time-boundary test    |          |
| P1.8.3 | TODO   | Renewal must not activate new period early                        | Keep future grant inactive/scheduled | Test                  |          |
| P1.8.4 | TODO   | Renewal must avoid unavoidable service gap                        | Activate continuously at boundary    | Boundary test         |          |
| P1.8.5 | TODO   | Duplicate renewal purchases must be prevented/reconciled          | Add renewal uniqueness/idempotency   | Concurrency test      |          |
| P1.8.6 | TODO   | Full subscription infrastructure is not required                  | Keep solution minimal                | Architecture review   |          |

---

# P2 — Required Test Matrix

## Payment and Commerce

| ID        | Status | Test                                | Expected Invariant                         | Evidence |
| --------- | ------ | ----------------------------------- | ------------------------------------------ | -------- |
| P2.PAY.1  | DONE   | Exact purchased grants              | Only exact offer snapshot activates        | `TestPurchaseGrantSnapshotIsPersistedAndActivatesExactTerms`; finalization R3 tests |
| P2.PAY.2  | DONE   | Partial expected grant set          | Transaction rejects; no partial activation | `TestPaymentGrantActivationRollsBackPartialGrantSet`; `TestCorruptedFinalizedSnapshotCannotActivatePartially` |
| P2.PAY.3  | DONE   | Changed checkout intent             | Never paid without grants                  | `TestPendingPurchaseIntentChangeGetsNewIdentity`; `TestReverseOrderPurchasesAreIndependentAndDeterministic` |
| P2.PAY.4  | DONE   | Duplicate success webhook           | Exactly-once semantic mutation             | `TestStripeTestNanoPurchaseAndWebhookActivatesOnce` |
| P2.PAY.5  | DONE   | Expired/failed before success       | Defined deterministic precedence           | `TestPaymentSuccessWinsOverEarlierFailedEventForPayablePurchase` |
| P2.PAY.6  | DONE   | Success before later terminal event | Paid grants remain stable                  | `TestPaymentSuccessWinsOverEarlierFailedEventForPayablePurchase` |
| P2.PAY.7  | DONE   | Concurrent identical checkout       | One logical purchase/provider operation    | `TestConcurrentChangedIntentLeavesDeterministicPurchaseStates`; `TestIdenticalFrozenCheckoutUsesStableIdentityAndRequest` |
| P2.PAY.8  | TODO   | Provider-customer creation race     | One stable customer mapping                |          |
| P2.PAY.9  | TODO   | Crash before checkout persistence   | Retry safely reconciles                    |          |
| P2.PAY.10 | DONE   | Missing compatibility projection    | Authoritative grants still activate        | `TestHostedPurchaseActivatesWithoutCompatibilityEntitlement` |

## Product Isolation

| ID        | Status | Test                                     | Expected Invariant                           | Evidence |
| --------- | ------ | ---------------------------------------- | -------------------------------------------- | -------- |
| P2.PROD.1 | TODO   | TPA Hub + product #2 for same customer   | No cross-product mutation                    |          |
| P2.PROD.2 | TODO   | Product #2 with no storage/egress        | Paid lifecycle works without TPA semantics   |          |
| P2.PROD.3 | TODO   | Product #3 with different metric/cadence | Shared layers require no TPA-specific change |          |
| P2.PROD.4 | TODO   | Empty product ID                         | Generic API fails closed                     |          |
| P2.PROD.5 | TODO   | Product-scoped concurrent purchases      | Independent state and idempotency            |          |

## Migration

| ID       | Status | Test                   | Expected Invariant                    | Evidence |
| -------- | ------ | ---------------------- | ------------------------------------- | -------- |
| P2.MIG.1 | DONE   | Missing purchase       | No active purchase grant              | `TestOrphanedPurchaseGrantFailsClosedDuringMigration` |
| P2.MIG.2 | DONE   | Non-paid purchase      | No active purchase grant              | `TestMalformedHistoricalMigrationMatrixFailsClosedAndRepeats` |
| P2.MIG.3 | DONE   | Missing `valid_until`  | Migration rejects/fails closed        | `TestMalformedHistoricalMigrationMatrixFailsClosedAndRepeats` |
| P2.MIG.4 | DONE   | End before start       | Migration rejects                     | `TestMalformedHistoricalMigrationMatrixFailsClosedAndRepeats` |
| P2.MIG.5 | DONE   | Unknown plan           | Migration rejects; no unlimited grant | `TestUnknownLegacyPlanFailsClosedDuringMigration` |
| P2.MIG.6 | DONE   | Known Pro plan         | Deterministic documented mapping      | `TestHistoricalAccountsReceiveOneBoundedNanoMigrationGrant/pro` |
| P2.MIG.7 | DONE   | Migration interruption | Restart is safe                       | `TestMigrationInterruptionReopenMatrix`; `TestBoundedMigrationInterruptionReopensIdempotently` |
| P2.MIG.8 | DONE   | Repeated migration     | Idempotent result                     | `TestHistoricalAccountsReceiveOneBoundedNanoMigrationGrant` |

## Authorization

| ID        | Status | Test                            | Expected Invariant | Evidence |
| --------- | ------ | ------------------------------- | ------------------ | -------- |
| P2.AUTH.1 | DONE   | Disabled bearer session         | Denied             | `TestAccountStatusEnforcedAcrossAuthenticationPaths` |
| P2.AUTH.2 | DONE   | Disabled browser session        | Denied             | `TestAccountStatusEnforcedAcrossAuthenticationPaths` |
| P2.AUTH.3 | DONE   | Disabled account reader token   | Denied             | `TestReactivationRestoresReaderAndNewWorkerAuthorization` |
| P2.AUTH.4 | DONE   | Disabled account queued publish | Denied             | `TestSuspendedAccountCannotRunQueuedPublication`; final authorization tests |
| P2.AUTH.5 | DONE   | Revoked reader token            | Denied             | `TestAuthenticationAndPrivateReader` |
| P2.AUTH.6 | TODO   | Expired private-read grant      | Denied             |          |

## Repository Publication

| ID         | Status | Test                                    | Expected Invariant                         | Evidence |
| ---------- | ------ | --------------------------------------- | ------------------------------------------ | -------- |
| P2.REPO.1  | DONE   | Crash before live swap                  | Old repository intact                      | `TestPublicationProcessDeathRecoveryMatrix/before-live-swap` |
| P2.REPO.2  | DONE   | Crash immediately after live swap       | Recoverable consistent generation          | `TestPublicationProcessDeathRecoveryMatrix/after-live-swap` |
| P2.REPO.3  | DONE   | Crash before DB commit                  | Reconciliation restores consistent state   | `TestDurableGenerationControlsReadsDuringPointerSwapWindow`; subprocess death matrix |
| P2.REPO.4  | DONE   | Crash after DB commit                   | Committed generation remains authoritative | `TestPublicationProcessDeathRecoveryMatrix/after-durable-record` |
| P2.REPO.5  | DONE   | Rename failure                          | No package loss                            | `TestWorkerPointerRenameFailureRollsBackAndRetrySucceeds` |
| P2.REPO.6  | DONE   | Disk full                               | Old live repo preserved                    | `TestWorkerCandidateHardBoundENOSPCPreservesAuthorityAndRetry` |
| P2.REPO.7  | DONE   | Signing failure                         | Old live repo preserved                    | `TestWorkerSigningFailurePreservesAuthorityAndRetry` |
| P2.REPO.8  | TODO   | Grant expires during publish            | No unauthorized final swap                 |          |
| P2.REPO.9  | DONE   | Account suspended during queued publish | No final swap                              | `TestSuspendedAccountCannotRunQueuedPublication`; `TestSuspensionImmediatelyBeforeLiveSwapBlocksPublication` |
| P2.REPO.10 | DONE   | Next publish after crash recovery       | All prior valid packages remain            | `TestPublicationProcessDeathRecoveryMatrix`; worker retry tests |

## Staging and Storage

| ID        | Status | Test                                      | Expected Invariant                          | Evidence |
| --------- | ------ | ----------------------------------------- | ------------------------------------------- | -------- |
| P2.STOR.1 | DONE   | Many oversized queued uploads             | Staging remains bounded                     | `TestConcurrentUploadsUseRealReservationPath` |
| P2.STOR.2 | DONE   | Per-account staging exhaustion            | New uploads rejected safely                 | `TestUploadStagingRespectsStorageGrant`; `TestPerAccountStagingUsesPhysicalAllocation` |
| P2.STOR.3 | DONE   | Global staging exhaustion                 | Service protects disk headroom              | `TestCandidateOutputBeyondReservationCannotActivate`; `TestDiskHeadroomGuardFailsClosed` |
| P2.STOR.4 | DONE   | Interrupted upload                        | Orphan cleaned                              | `TestStartupCleanupMatrixIsIdempotentAndProtectsAuthoritativeState` |
| P2.STOR.5 | DONE   | Interrupted worker                        | Work directory reconciled                   | `TestPublicationProcessDeathRecoveryMatrix`; `TestPartialCleanupResumesAfterRestart` |
| P2.STOR.6 | TODO   | Concurrent/multi-instance publish attempt | Explicitly rejected or correctly serialized |          |

## Egress

| ID       | Status | Test                            | Expected Invariant                                 | Evidence |
| -------- | ------ | ------------------------------- | -------------------------------------------------- | -------- |
| P2.EGR.1 | DONE   | Egress DB lookup failure        | Conservative behavior                              | `TestEgressEnforcementUsesGrantLimit` |
| P2.EGR.2 | DONE   | Egress accounting write failure | Observable and conservative behavior               | `TestEgressBeginFailureServesNoBody`; completion rollback/restart uncertainty tests |
| P2.EGR.3 | TODO   | Grant begins mid-day            | Only defined in-window usage counts                |          |
| P2.EGR.4 | TODO   | Grant ends mid-day              | Exact documented boundary                          |          |
| P2.EGR.5 | TODO   | Concurrent downloads near limit | Bounded/documented overshoot or strict reservation |          |
| P2.EGR.6 | TODO   | HTTP Range requests             | Correct byte accounting                            |          |
| P2.EGR.7 | DONE   | Aborted response                | Actual delivered bytes counted consistently        | `TestRepositoryCountingWriterCountsSuccessfulPartialWrites` |
| P2.EGR.8 | TODO   | HEAD request                    | Defined/no accidental payload accounting           |          |

## Provider Independence

| ID            | Status | Test                                   | Expected Invariant                                        | Evidence |
| ------------- | ------ | -------------------------------------- | --------------------------------------------------------- | -------- |
| P2.PROVIDER.1 | TODO   | Non-Stripe redirect provider fake      | Commerce works without Stripe semantics                   |          |
| P2.PROVIDER.2 | TODO   | Provider-neutral operation correlation | Product code does not require provider metadata knowledge |          |
| P2.PROVIDER.3 | TODO   | Adapter contract test                  | Provider-specific verification stays inside adapter       |          |

---

# P3 — Cleanup and Architecture Guardrails

| ID    | Status | Requirement                                                                                  | Required Verification          | Evidence |
| ----- | ------ | -------------------------------------------------------------------------------------------- | ------------------------------ | -------- |
| P3.1  | TODO   | `internal/commerce` cannot import product/provider packages                                  | Automated import-boundary test |          |
| P3.2  | TODO   | `internal/payments` cannot import Stripe/product packages                                    | Automated import-boundary test |          |
| P3.3  | TODO   | Stripe imports allowed only in adapter/tests/composition wiring                              | Automated guardrail            |          |
| P3.4  | TODO   | TPA product constants forbidden in shared runtime code                                       | Automated/source guardrail     |          |
| P3.5  | TODO   | Remove or rename duplicate `BillingProvider` abstraction                                     | Source review/tests            |          |
| P3.6  | TODO   | Replace string-compared errors with typed/sentinel errors                                    | Repository search              |          |
| P3.7  | TODO   | Update README billing/web claims                                                             | Documentation review           |          |
| P3.8  | TODO   | Handle `crypto/rand` failures                                                                | Error-path tests               |          |
| P3.9  | TODO   | Remove orphan repository directory on DB insertion failure                                   | Failure test                   |          |
| P3.10 | TODO   | Remove stale legacy egress APIs if unused                                                    | Repository search/tests        |          |
| P3.11 | TODO   | Provider reference uniqueness defined/enforced                                               | Schema/test                    |          |
| P3.12 | TODO   | Sessions have server-side expiry semantics                                                   | Session expiry test            |          |
| P3.13 | TODO   | Payment failures include customer/product/purchase/event identifiers in safe structured logs | Log test/review                |          |
| P3.14 | TODO   | Authorization failures are diagnosable without exposing secrets                              | Log review                     |          |
| P3.15 | TODO   | HTTP read/write/idle limits are explicit                                                     | Config/integration test        |          |
| P3.16 | TODO   | Throttling respects request cancellation                                                     | Cancellation test              |          |

---

# P4 — Signing and Key Operations

| ID   | Status | Requirement / Invariant                                                       | Required Verification | Evidence |
| ---- | ------ | ----------------------------------------------------------------------------- | --------------------- | -------- |
| P4.1 | TODO   | Hosted signed-repo mode validates signing configuration at startup            | Startup test          |          |
| P4.2 | TODO   | Signing selector resolves uniquely                                            | Invalid-selector test |          |
| P4.3 | TODO   | Private key is usable before accepting production traffic                     | Startup signing probe |          |
| P4.4 | TODO   | Exported public key matches actual signer                                     | Key identity test     |          |
| P4.5 | TODO   | Hosted repositories cannot silently publish unsigned when signing is required | Publish test          |          |
| P4.6 | TODO   | Signing failure fails safely                                                  | Failure test          |          |
| P4.7 | TODO   | Key rotation procedure is documented                                          | Documentation         |          |
| P4.8 | TODO   | Key rotation supports overlap where existing clients require old key          | Rotation test         |          |
| P4.9 | TODO   | KMS integration remains deferred unless operational requirements justify it   | Architecture review   |          |

---

# P5 — Real Product #2 Proof

Use a materially different product, for example:

```text
Product: build-runner

Capabilities:
- build.execute
- build.history.read

Metric:
- build_operations

Commercial shape:
- no repositories
- no storage
- no egress
- monthly or one-off operation allowance
```

| ID    | Status | Requirement / Invariant                                    | Required Verification          | Evidence |
| ----- | ------ | ---------------------------------------------------------- | ------------------------------ | -------- |
| P5.1  | TODO   | Product #2 has its own product package                     | Source inspection              |          |
| P5.2  | TODO   | Product #2 defines independent capabilities                | Grant tests                    |          |
| P5.3  | TODO   | Product #2 defines independent offer terms                 | Offer test                     |          |
| P5.4  | TODO   | Existing customer can purchase product #2                  | E2E purchase test              |          |
| P5.5  | TODO   | Product #2 checkout uses shared payment layer              | Dependency inspection          |          |
| P5.6  | TODO   | Product #2 payment activates exact product #2 grants       | E2E webhook test               |          |
| P5.7  | TODO   | Product #2 usage does not require storage/egress semantics | Runtime test                   |          |
| P5.8  | TODO   | TPA Hub grants remain untouched                            | Cross-product test             |          |
| P5.9  | TODO   | Stripe adapter requires no TPA/product-specific change     | Git diff / dependency evidence |          |
| P5.10 | TODO   | `internal/tpa` requires no change                          | Git diff evidence              |          |
| P5.11 | TODO   | TPA Hub repository runtime requires no change              | Git diff evidence              |          |
| P5.12 | TODO   | Shared commerce does not gain product #2 special cases     | Repository search              |          |

---

# P6 — Provider Replacement Proof

| ID    | Status | Requirement / Invariant                                                              | Required Verification             | Evidence |
| ----- | ------ | ------------------------------------------------------------------------------------ | --------------------------------- | -------- |
| P6.1  | TODO   | A second redirect-style provider can be added as an adapter                          | Fake/provider implementation test |          |
| P6.2  | TODO   | TPA Hub repository domain code requires no change                                    | Git diff evidence                 |          |
| P6.3  | TODO   | Product #2 domain code requires no provider-specific change                          | Git diff evidence                 |          |
| P6.4  | TODO   | Provider-specific webhook verification remains adapter-owned                         | Dependency review                 |          |
| P6.5  | TODO   | Provider-specific tax mapping remains adapter-owned                                  | Dependency review                 |          |
| P6.6  | TODO   | Provider-specific customer creation remains adapter-owned where applicable           | Contract test                     |          |
| P6.7  | TODO   | Product code does not hardcode `"stripe"`                                            | Repository search                 |          |
| P6.8  | TODO   | Product code does not hardcode Stripe environment names                              | Repository search                 |          |
| P6.9  | TODO   | Routing/composition may remain provider-aware                                        | Architecture review               |          |
| P6.10 | TODO   | Materially different payment semantics remain explicitly unsupported unless required | Architecture decision record      |          |

---

# P7 — Self-Hosting Independence Verification

| ID    | Status | Requirement / Invariant                                           | Required Verification          | Evidence |
| ----- | ------ | ----------------------------------------------------------------- | ------------------------------ | -------- |
| P7.1  | TODO   | TPA imports no Hub code                                           | Dependency inspection          |          |
| P7.2  | TODO   | TPA imports no commerce code                                      | Dependency inspection          |          |
| P7.3  | TODO   | TPA imports no payment code                                       | Dependency inspection          |          |
| P7.4  | TODO   | TPA requires no hosted authentication                             | Qualification test             |          |
| P7.5  | TODO   | TPA contains no mandatory Hub URL                                 | Repository search              |          |
| P7.6  | TODO   | TPA builds `.deb` packages independently                          | Qualification test             |          |
| P7.7  | TODO   | TPA creates APT repositories independently                        | Qualification test             |          |
| P7.8  | TODO   | TPA signs using user-controlled GPG keys                          | Qualification test             |          |
| P7.9  | TODO   | TPA performs atomic repository publication independently          | Qualification test             |          |
| P7.10 | TODO   | Standard APT client can install from self-hosted output           | Offline APT qualification      |          |
| P7.11 | TODO   | TPA Hub can disappear without breaking TPA workflows              | Architecture/repository review |          |
| P7.12 | TODO   | Generic future APT cache remains independent from TPA and TPA Hub | Architecture review            |          |

---

# P8 — Operational Recovery

| ID    | Status | Recovery Scenario                                                                  | Required Capability                                      | Verification           | Evidence |
| ----- | ------ | ---------------------------------------------------------------------------------- | -------------------------------------------------------- | ---------------------- | -------- |
| P8.1  | TODO   | Stuck pending checkout                                                             | Inspect/reconcile safely                                 | Recovery command/test  |          |
| P8.2  | TODO   | Provider paid but local purchase incomplete                                        | Reconcile provider/local state                           | Recovery test          |          |
| P8.3  | TODO   | Paid purchase but grants missing                                                   | Re-materialize exact purchase snapshot safely            | Recovery test          |          |
| P8.4  | TODO   | Compatibility projection missing                                                   | Rebuild projection without affecting authoritative state | Recovery test          |          |
| P8.5  | TODO   | Live repo/artifact generation mismatch                                             | Inspect and reconcile generation                         | Recovery test          |          |
| P8.6  | TODO   | Failed job after successful live swap                                              | Reconcile job with live generation                       | Recovery test          |          |
| P8.7  | TODO   | Orphan incoming/work directories                                                   | Detect and clean                                         | Startup/reconcile test |          |
| P8.8  | TODO   | Egress accounting inconsistency                                                    | Inspect/reset/rebuild explicitly                         | Operational procedure  |          |
| P8.9  | TODO   | Migration rejects malformed historical row                                         | Surface row for operator remediation                     | Recovery procedure     |          |
| P8.10 | TODO   | Recovery must not require undocumented manual SQL surgery for normal failure modes | Operational review                                       |                        |          |

---

# P9 — Final Verification and Re-Audit

| ID    | Status | Verification                                                  | Expected Result                  | Evidence |
| ----- | ------ | ------------------------------------------------------------- | -------------------------------- | -------- |
| P9.1  | DONE   | `go test ./...`                                               | PASS                             | 2026-09-04 local run PASS after P0-R3 changes; independent verification still required. |
| P9.2  | DONE   | `go test -race ./...`                                         | PASS                             | 2026-09-04 local run PASS after final P0-R3 code changes. |
| P9.3  | DONE   | `go vet ./...`                                                | PASS                             | 2026-09-04 local run PASS. |
| P9.4  | DONE   | `git diff --check`                                            | PASS                             | 2026-09-04 local run PASS. |
| P9.5  | DONE   | TPA Hub qualification suite                                   | PASS                             | `tests/qualification.sh`: `REAL APT SELF-HOSTING QUALIFIED`. |
| P9.6  | DONE   | Local bootstrap qualification                                 | PASS                             | `tests/local-bootstrap-qualification.sh`: PASS. |
| P9.7  | DONE   | Bootstrap key qualification                                   | PASS                             | Fingerprint `42313D9F5F1F319AB5A18F6122380CEE20D731C6` matched. |
| P9.8  | DONE   | Bootstrap qualification                                       | PASS                             | `tests/bootstrap-qualification.sh`: PASS. |
| P9.9  | DONE   | TPA `go test -race ./...`                                     | PASS                             | Sibling `../tpa` local run PASS. |
| P9.10 | DONE   | TPA `go vet ./...`                                            | PASS                             | Sibling `../tpa` local run PASS. |
| P9.11 | DONE   | TPA qualification suite                                       | PASS                             | Sibling signed APT and dependency qualification scripts PASS. |
| P9.12 | TODO   | Architecture/import guardrails                                | PASS                             |          |
| P9.13 | DONE   | Publication crash matrix                                      | PASS                             | `TestPublicationProcessDeathRecoveryMatrix`; worker rename/ENOSPC/signing and post-swap tests. |
| P9.14 | DONE   | Payment adversarial matrix                                    | PASS                             | Finalization, provider/environment/reference, reverse-order, corruption, and atomic rollback tests. |
| P9.15 | DONE   | Migration fault matrix                                        | PASS                             | `TestMigrationInterruptionReopenMatrix`; `TestBoundedMigrationInterruptionReopensIdempotently`; eligibility matrix. |
| P9.16 | TODO   | Product #2 E2E proof                                          | PASS                             |          |
| P9.17 | TODO   | Provider replacement contract proof                           | PASS                             |          |
| P9.18 | TODO   | Self-hosting independence proof                               | PASS                             |          |
| P9.19 | TODO   | Original full audit prompt rerun from scratch                 | No unresolved production blocker |          |
| P9.20 | DONE   | Each original Critical finding C1–C8 individually rechecked   | RESOLVED                         | Closure table below; independent re-audit remains required before `VERIFIED`. |
| P9.21 | TODO   | Each original Important finding I1–I19 individually rechecked | RESOLVED / explicitly accepted   |          |
| P9.22 | DONE   | Working tree clean after final verification                   | CLEAN                            | P0-R3 implementation `2cec6d8`; evidence documentation `d8de5fb`; final status checked after commit. |

---

# Original Critical Finding Closure

| Finding                                            | Status | Resolution Summary | Evidence |
| -------------------------------------------------- | ------ | ------------------ | -------- |
| C1 — Payment success activates wrong grant set     | DONE | Activation requires immutable finalized snapshots, validity, and provider linkage. | `TestDelayedSuccessUsesImmutableFinalizedValidity`; `TestFinalizedProviderReferenceCannotChange`; `TestFinalizedPurchaseRejectsWrongProviderAndEnvironment`; `TestUnfinalizedPurchaseCannotActivate` |
| C2 — Changed intent / out-of-order payment events  | DONE | Late success reconciles only provider-linked purchases using finalized local terms. | `TestPaymentSuccessWinsOverEarlierFailedEventForPayablePurchase`; `internal/store/finalization_r3_test.go` |
| C3 — Permanent free Nano vs expiring paid access   | DONE | Historical access is a one-time immutable six-calendar-month migration term; future hosted accounts receive none; purchase remains independent in both orders. | `docs/migration-access-policy.md`; `TestHistoricalAccountsReceiveOneBoundedNanoMigrationGrant`; `TestMigrationAndPurchasesUseOnlyActiveEnabledContributors`; `TestPurchaseBeforeBoundedMigrationRemainsIndependent`; `TestNewHostedAccountDoesNotReceiveMigrationGrant` |
| C4 — Publication not crash-consistent              | DONE | Candidates are semantically verified and hard-bounded; durable authority and job success commit atomically; real process death preserves or reconciles authority and permits retry. | `TestSuccessfulRunnerMalformedCandidatesNeverActivate`; `TestCommitPublicationAtomicallyRechecksAuthorizationAndJobState`; `TestPublicationProcessDeathRecoveryMatrix`; worker rename/ENOSPC/signing tests |
| C5 — Suspended accounts retain reader-token access | DONE | Reader and worker authorization recheck active account and grant state; reactivation does not revive disabled grants. | `TestReactivationRestoresReaderAndNewWorkerAuthorization`; `TestSuspensionImmediatelyBeforeLiveSwapBlocksPublication`; `TestDisabledGrantAtFinalAuthorizationCannotActivate` |
| C6 — Unsafe legacy migration                       | DONE | Migration is transactional, idempotent, bounded to six months, TPA-scoped, active-only, excludes future accounts, and never authorizes permanent legacy rows. | `TestBoundedMigrationEligibilityIsTPAScopedAndActive`; `TestBoundedMigrationInterruptionReopensIdempotently`; `TestMigrationInterruptionReopenMatrix` |
| C7 — Upload staging bypasses storage grant         | DONE | Admission counts physical allocation, metadata, baseline/work copies, and a hard-bounded candidate filesystem. | `TestConcurrentUploadsUseRealReservationPath`; `TestPerAccountStagingUsesPhysicalAllocation`; `TestLargeBaselineRejectedBeforeWorkCopy`; `TestWorkerCandidateHardBoundENOSPCPreservesAuthorityAndRetry`; `internal/tpa/bounded_test.go` |
| C8 — Egress enforcement fails open                 | DONE | Delivery uncertainty is durably recorded before streaming and blocks future requests after completion failure/restart until explicit recovery. | `TestEgressBeginFailureServesNoBody`; `TestCompleteEgressDeliveryRollsBackUsageAndPreservesUncertainty`; `TestEgressUncertaintySurvivesRestartUntilExplicitRecovery`; `TestUnownedRepositoryUncertaintyFailsClosedAfterRestart`; `docs/egress-recovery.md` |

---

# Original Important Finding Closure

| Finding                                                         | Status              | Resolution Summary                                    | Evidence |
| --------------------------------------------------------------- | ------------------- | ----------------------------------------------------- | -------- |
| I1 — Shared layers contain concrete TPA identity/defaults       | TODO                |                                                       |          |
| I2 — Provider-neutral operation correlation incomplete          | TODO                |                                                       |          |
| I3 — Generic webhook abstraction is Stripe-shaped               | TODO                |                                                       |          |
| I4 — Payment validity supplied by webhook route                 | TODO                |                                                       |          |
| I5 — Product pricing split/duplicated                           | TODO                |                                                       |          |
| I6 — Grant schema has ambiguous/invalid states                  | TODO                |                                                       |          |
| I7 — One grant row per capability limits stacking               | ACCEPTED / DEFERRED | Revisit only when stacking becomes a real requirement |          |
| I8 — `repository.download` mixes permission/quota semantics     | TODO                |                                                       |          |
| I9 — Egress windows lose timestamp precision                    | TODO                |                                                       |          |
| I10 — Concurrent downloads can overshoot limit                  | TODO / ACCEPTED     |                                                       |          |
| I11 — Storage safety assumes single process/worker              | TODO                |                                                       |          |
| I12 — Renewal impossible before expiry                          | TODO                |                                                       |          |
| I13 — `account_entitlements` can block activation               | DONE                | Purchase admission uses product-scoped grants; payment activation does not maintain projection | P0-A implementation |
| I14 — Payment event lacks customer/product/environment evidence | TODO                |                                                       |          |
| I15 — Provider customer creation race                           | TODO                |                                                       |          |
| I16 — Payment failures poorly diagnosable                       | TODO                |                                                       |          |
| I17 — No operational reconciliation path                        | TODO                |                                                       |          |
| I18 — Signing configuration insufficiently validated            | TODO                |                                                       |          |
| I19 — HTTP/throttling failure controls weak                     | TODO                |                                                       |          |

---

# Deferred Work

These items are intentionally not blockers unless a concrete requirement changes.

| Area                             | Status      | Decision                                                         | Evidence / Revisit Trigger |
| -------------------------------- | ----------- | ---------------------------------------------------------------- | -------------------------- |
| Credits                          | DEFERRED    | Correctly deferred                                               |                            |
| Wallets                          | DEFERRED    | Correctly deferred                                               |                            |
| Credit expiration                | DEFERRED    | Correctly deferred                                               |                            |
| Reserve / commit / release       | DEFERRED    | Correctly deferred                                               |                            |
| Subscriptions                    | DESIGN ONLY | Ensure current purchase terms do not block future implementation |                            |
| Renewals                         | REQUIRED    | Minimal advance renewal required before paid annual production   |                            |
| Refund orchestration             | DEFERRED    | Manual provider refunds acceptable initially                     |                            |
| Postpaid balances                | DEFERRED    | Correctly deferred                                               |                            |
| Cross-product invoicing          | DEFERRED    | Correctly deferred                                               |                            |
| Network control-plane extraction | DEFERRED    | Keep in-process                                                  |                            |
| Event bus                        | DEFERRED    | Not justified                                                    |                            |
| Kafka                            | DEFERRED    | Not justified                                                    |                            |
| Distributed transactions         | DEFERRED    | Not justified                                                    |                            |
| Dynamic product plugins          | DEFERRED    | Not justified                                                    |                            |
| Generic pricing DSL              | DEFERRED    | Not justified                                                    |                            |
| KMS integration                  | DEFERRED    | Revisit only when operational key custody requires it            |                            |
| Grant stacking                   | DEFERRED    | Revisit when promotions/add-on packs require it                  |                            |
| Multi-instance publication       | DEFERRED    | Revisit when deployment topology requires it                     |                            |
| Service extraction               | DEFERRED    | Revisit only under concrete operational/deployment pressure      |                            |

---

# Final Production Gate

TPA Hub must not be considered production-ready until all of the following are true:

| Gate                                                                           | Status | Evidence |
| ------------------------------------------------------------------------------ | ------ | -------- |
| All P0 items are `VERIFIED`                                                    | TODO   |          |
| No payment path can produce paid-without-grant state                           | TODO   |          |
| Purchased grants exactly match immutable purchase terms                        | TODO   |          |
| Suspended accounts are denied through every authentication path                | TODO   |          |
| Legacy migration fails closed                                                  | TODO   |          |
| Upload staging cannot exhaust storage outside defined limits                   | TODO   |          |
| Egress accounting failure does not silently become unrestricted delivery       | TODO   |          |
| Repository publication is crash-recoverable                                    | TODO   |          |
| Product #2 completes a full paid lifecycle without TPA-specific shared changes | TODO   |          |
| TPA remains fully independent and self-hostable                                | TODO   |          |
| All qualification suites pass                                                  | DONE   | TPA Hub and sibling TPA Go/race/vet, signed APT, dependency, bootstrap, key, and local-bootstrap runs passed 2026-09-04. |
| Final full re-audit finds no unresolved production blocker                     | TODO   |          |

---

# Final Acceptance Questions

Before declaring the architecture complete, answer each with concrete evidence.

|  # | Question                                                          | Answer | Evidence |
| -: | ----------------------------------------------------------------- | ------ | -------- |
|  1 | Is TPA Hub product #1 on a genuinely reusable commerce boundary?  |        |          |
|  2 | Can product #2 reuse commerce without knowing TPA Hub?            |        |          |
|  3 | Can product #2 use payments without knowing Stripe?               |        |          |
|  4 | Can Stripe be replaced without rewriting TPA Hub domain logic?    |        |          |
|  5 | Are product grants the actual runtime source of truth?            |        |          |
|  6 | Are storage and egress enforcement consistent with grants?        |        |          |
|  7 | Are product-scoped purchases and idempotency correct?             |        |          |
|  8 | Are migrations safe, fail-closed, and repeatable?                 |        |          |
|  9 | Is repository publication atomic and crash-recoverable?           |        |          |
| 10 | Can TPA function independently if TPA Hub disappears permanently? |        |          |
| 11 | Is self-hosting still a first-class supported path?               |        |          |
| 12 | Is the architecture still appropriate for a small real business?  |        |          |
| 13 | Are there any unresolved production blockers?                     |        |          |
| 14 | Are there any unresolved blockers before a real product #2?       |        |          |
