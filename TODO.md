# TPA Hub Production Readiness Checklist

This checklist tracks the remediation work required after the architecture, correctness, commerce, payment, migration, authorization, and repository audit.

The goal is to make every item independently verifiable.

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
| P0.1.1  | TODO   | A successful purchase activates exactly the grants purchased by that offer          | Persist immutable purchased grant specifications                        | Inspect persisted purchase snapshot          |          |
| P0.1.2  | TODO   | Purchased grant snapshots are scoped to the exact purchase                          | Key snapshots by purchase + capability or equivalent immutable identity | Schema/invariant test                        |          |
| P0.1.3  | TODO   | Grant activation must not depend on whatever grants happen to exist at webhook time | Materialize grants exclusively from purchase snapshots                  | End-to-end payment test                      |          |
| P0.1.4  | TODO   | Unrelated grants for the same product must remain untouched                         | Restrict activation to purchased capability set                         | Test with unrelated capability               |          |
| P0.1.5  | TODO   | Purchased limits must match the purchased offer exactly                             | Snapshot and activate storage, egress, operation limits and metrics     | Exact-value assertions                       |          |
| P0.1.6  | TODO   | Purchased offer identity must be preserved                                          | Persist and materialize exact `offer_id`                                | Purchase/grant assertion                     |          |
| P0.1.7  | TODO   | Purchased cadence must be preserved                                                 | Persist and materialize exact cadence                                   | Monthly/annual tests                         |          |
| P0.1.8  | TODO   | Partial grant activation is forbidden                                               | Validate expected grant count/set before commit                         | Missing-capability transaction rollback test |          |
| P0.1.9  | TODO   | Payment success and grant activation are atomic                                     | Perform state transition and materialization in one transaction         | Failure injection inside transaction         |          |
| P0.1.10 | TODO   | Existing Standard/Pro state cannot leak into a Nano purchase                        | Replace inherited state with exact purchased snapshot semantics         | Legacy Standard/Pro → Nano test              |          |

---

# P0.2 — Checkout Supersession and Changed Intent

| ID     | Status | Requirement / Invariant                                                              | Required Work                                            | Required Verification            | Evidence |
| ------ | ------ | ------------------------------------------------------------------------------------ | -------------------------------------------------------- | -------------------------------- | -------- |
| P0.2.1 | TODO   | A provider-payable checkout must always remain locally reconcilable                  | Define explicit checkout supersession policy             | Architecture review              |          |
| P0.2.2 | TODO   | Changed purchase intent must not create paid-without-grant states                    | Reconcile, cancel, expire, or retain old checkout safely | Changed-intent E2E test          |          |
| P0.2.3 | TODO   | Provider checkout and local purchase state must not silently diverge                 | Enforce provider/local lifecycle relationship            | State-machine test               |          |
| P0.2.4 | TODO   | `UpdatePurchaseCheckout` must update exactly one expected pending purchase           | Check affected row count                                 | Unit/integration test            |          |
| P0.2.5 | TODO   | Success after local `failed` must have defined behavior                              | Define event precedence                                  | Out-of-order event test          |          |
| P0.2.6 | TODO   | Success after local `expired` must have defined behavior                             | Define event precedence                                  | Out-of-order event test          |          |
| P0.2.7 | TODO   | Terminal event after success must not revoke paid grants unless explicitly supported | Protect paid terminal state                              | Success-then-expired/failed test |          |
| P0.2.8 | TODO   | Concurrent changed-intent requests must have deterministic results                   | Serialize/reconcile intent replacement                   | Concurrent purchase test         |          |
| P0.2.9 | TODO   | Paying any provider-returned valid checkout must never lose customer money           | Add full adversarial checkout race test                  | E2E provider simulation          |          |

---

# P0.3 — Hosted Account Provisioning

| ID     | Status | Requirement / Invariant                                                            | Required Work                                            | Required Verification        | Evidence |
| ------ | ------ | ---------------------------------------------------------------------------------- | -------------------------------------------------------- | ---------------------------- | -------- |
| P0.3.1 | TODO   | Hosted production accounts must not receive accidental permanent free grants       | Remove implicit permanent Nano provisioning              | Registration test            |          |
| P0.3.2 | TODO   | New hosted accounts must not be labeled `legacy`                                   | Introduce explicit provisioning source/policy            | Persistence assertion        |          |
| P0.3.3 | TODO   | Development/local mode provisioning must be explicit                               | Define development/self-host policy                      | Config test                  |          |
| P0.3.4 | TODO   | Historical migrated accounts must have explicit grandfathering semantics           | Separate legacy migration policy                         | Migration test               |          |
| P0.3.5 | TODO   | Buying a product must not reduce pre-existing grandfathered rights unintentionally | Define precedence/merge behavior                         | Grandfathered purchase test  |          |
| P0.3.6 | TODO   | Production access policy must be explicit                                          | Choose no grant, disabled grant, or bounded trial        | Product policy documentation |          |
| P0.3.7 | TODO   | Paying customer access must not be less durable than non-paying access by accident | Validate post-purchase state against provisioning policy | Lifecycle test               |          |

---

# P0.4 — Crash-Consistent Repository Publication

| ID      | Status | Requirement / Invariant                                                    | Required Work                              | Required Verification | Evidence |
| ------- | ------ | -------------------------------------------------------------------------- | ------------------------------------------ | --------------------- | -------- |
| P0.4.1  | TODO   | Publication artifacts must be immutable per generation                     | Introduce versioned artifact generations   | Source inspection     |          |
| P0.4.2  | TODO   | Candidate generation must be complete before publication                   | Build full candidate generation first      | Publish test          |          |
| P0.4.3  | TODO   | Candidate repository must be verified before live swap                     | Add explicit verification boundary         | Failure test          |          |
| P0.4.4  | TODO   | Live repository switch must remain atomic                                  | Preserve atomic directory exchange         | Qualification test    |          |
| P0.4.5  | TODO   | Active artifact generation must be durably recorded                        | Persist active generation identity         | DB assertion          |          |
| P0.4.6  | TODO   | Job success must correspond to the same committed generation               | Bind job completion to active generation   | Crash/restart test    |          |
| P0.4.7  | TODO   | Old authoritative artifacts must not be deleted before durable replacement | Reorder publication cleanup                | Fault injection       |          |
| P0.4.8  | TODO   | Old generations may be garbage-collected only after durable success        | Add post-commit GC                         | Source/test evidence  |          |
| P0.4.9  | TODO   | Startup must reconcile incomplete publication generations                  | Implement restart reconciliation           | Restart fault test    |          |
| P0.4.10 | TODO   | Startup must detect stale staging/work directories                         | Scan and reconcile stale state             | Startup cleanup test  |          |
| P0.4.11 | TODO   | Crash after candidate build must be recoverable                            | Fault-injection point                      | Restart assertion     |          |
| P0.4.12 | TODO   | Crash before live swap must preserve previous repo                         | Fault-injection point                      | Restart assertion     |          |
| P0.4.13 | TODO   | Crash immediately after live swap must be recoverable                      | Fault-injection point                      | Restart assertion     |          |
| P0.4.14 | TODO   | Crash before DB success commit must be recoverable                         | Fault-injection point                      | Restart assertion     |          |
| P0.4.15 | TODO   | Crash after DB success commit must leave consistent state                  | Fault-injection point                      | Restart assertion     |          |
| P0.4.16 | TODO   | Rename failure must not lose previously published packages                 | Inject rename failure                      | Recovery test         |          |
| P0.4.17 | TODO   | Disk-full failure must not corrupt live or authoritative state             | Inject ENOSPC                              | Recovery test         |          |
| P0.4.18 | TODO   | Signing failure must leave previous live generation intact                 | Break signing configuration during publish | Publish test          |          |
| P0.4.19 | TODO   | Next publish after recovery must preserve all previously valid packages    | Publish after crash matrix                 | Qualification test    |          |

---

# P0.5 — Suspended Account Enforcement

| ID     | Status | Requirement / Invariant                                     | Required Work                                    | Required Verification | Evidence |
| ------ | ------ | ----------------------------------------------------------- | ------------------------------------------------ | --------------------- | -------- |
| P0.5.1 | TODO   | Suspended accounts must fail bearer authentication          | Preserve/verify active-account join              | Test                  |          |
| P0.5.2 | TODO   | Suspended accounts must fail browser session authentication | Preserve/verify active-account join              | Test                  |          |
| P0.5.3 | TODO   | Suspended accounts must fail reader-token private access    | Join reader token → repository → account status  | Test                  |          |
| P0.5.4 | TODO   | Reader authorization must verify token is not revoked       | Preserve token revocation validation             | Test                  |          |
| P0.5.5 | TODO   | Reader authorization must verify repository ownership       | Validate ownership/customer relationship         | Test                  |          |
| P0.5.6 | TODO   | Reader authorization must verify required grant             | Check active private/read grant                  | Test                  |          |
| P0.5.7 | TODO   | Queued jobs must not publish after account suspension       | Recheck account immediately before publication   | Worker test           |          |
| P0.5.8 | TODO   | Re-activating account must follow explicit policy           | Verify reader/worker behavior after reactivation | Test                  |          |

---

# P0.6 — Fail-Closed Legacy Migration

| ID      | Status | Requirement / Invariant                               | Required Work                                | Required Verification | Evidence |
| ------- | ------ | ----------------------------------------------------- | -------------------------------------------- | --------------------- | -------- |
| P0.6.1  | TODO   | Only known legacy plans may migrate                   | Explicitly map Nano, Standard, Pro           | Migration test        |          |
| P0.6.2  | TODO   | Unknown plan must not become unlimited                | Fail migration or quarantine row             | Unknown-plan test     |          |
| P0.6.3  | TODO   | Purchase-backed grant requires authoritative purchase | Validate linked purchase exists              | Migration test        |          |
| P0.6.4  | TODO   | Purchase-backed grant requires paid purchase state    | Validate purchase state                      | Migration test        |          |
| P0.6.5  | TODO   | Purchase-backed grant requires bounded validity       | Reject empty `valid_until`                   | Migration test        |          |
| P0.6.6  | TODO   | Grant validity must satisfy end > start               | Add validation                               | Boundary test         |          |
| P0.6.7  | TODO   | Malformed rows must not silently become active grants | Fail closed or explicitly disable/quarantine | Migration test        |          |
| P0.6.8  | TODO   | Rejected rows must be diagnosable                     | Log/report migration rejection               | Operational evidence  |          |
| P0.6.9  | TODO   | Migration must remain transactional                   | Verify migration transaction boundary        | Fault-injection test  |          |
| P0.6.10 | TODO   | Migration must remain idempotent                      | Re-run migration repeatedly                  | Restart test          |          |
| P0.6.11 | TODO   | Mid-migration crash must recover safely               | Kill between migration steps                 | Restart test          |          |

---

# P0.7 — Upload and Staging Capacity

| ID      | Status | Requirement / Invariant                                        | Required Work                        | Required Verification | Evidence |
| ------- | ------ | -------------------------------------------------------------- | ------------------------------------ | --------------------- | -------- |
| P0.7.1  | TODO   | Small storage grants must not allow unbounded staging usage    | Add staging quota                    | Test                  |          |
| P0.7.2  | TODO   | Outstanding incoming bytes must be bounded per account         | Track pending incoming bytes         | Test                  |          |
| P0.7.3  | TODO   | Outstanding work bytes must be bounded per account             | Track work bytes                     | Test                  |          |
| P0.7.4  | TODO   | Clearly impossible uploads should be rejected before queueing  | Compare upload size against policy   | Request test          |          |
| P0.7.5  | TODO   | Pending jobs must count toward staging pressure                | Add queued-byte accounting           | Queue saturation test |          |
| P0.7.6  | TODO   | Global staging usage must be bounded                           | Add global configured limit          | Test                  |          |
| P0.7.7  | TODO   | Global disk headroom must be protected                         | Add disk-free threshold              | ENOSPC/headroom test  |          |
| P0.7.8  | TODO   | Terminal jobs must remove incoming data                        | Cleanup success/failure/cancel paths | Test                  |          |
| P0.7.9  | TODO   | Terminal jobs must remove work data                            | Cleanup success/failure/cancel paths | Test                  |          |
| P0.7.10 | TODO   | Startup must clean abandoned job directories                   | Startup reconciliation               | Restart test          |          |
| P0.7.11 | TODO   | Many queued large jobs cannot exhaust filesystem beyond policy | Adversarial queue test               | Disk-usage assertion  |          |

---

# P0.8 — Conservative Egress Enforcement

| ID     | Status | Requirement / Invariant                                                  | Required Work                     | Required Verification | Evidence |
| ------ | ------ | ------------------------------------------------------------------------ | --------------------------------- | --------------------- | -------- |
| P0.8.1 | TODO   | Grant lookup failure must not result in unrestricted download            | Conservative deny/throttle policy | Inject error test     |          |
| P0.8.2 | TODO   | Usage lookup failure must not result in unrestricted download            | Conservative deny/throttle policy | Inject DB error test  |          |
| P0.8.3 | TODO   | Accounting persistence failure must be observable                        | Structured failure logging        | Test/log evidence     |          |
| P0.8.4 | TODO   | Missing grant behavior must be explicit                                  | Document deny/throttle policy     | Test                  |          |
| P0.8.5 | TODO   | Disabled grant behavior must be explicit                                 | Document deny/throttle policy     | Test                  |          |
| P0.8.6 | TODO   | Expired grant behavior must be explicit                                  | Document deny/throttle policy     | Test                  |          |
| P0.8.7 | TODO   | Malformed period must fail conservatively                                | Validate period                   | Test                  |          |
| P0.8.8 | TODO   | Private repository behavior on egress-authority failure must be explicit | Decide deny vs throttle           | Test                  |          |

---

# P1 — Architecture Hardening

## P1.1 — Remove TPA Identity from Shared Commerce

| ID     | Status | Requirement / Invariant                                        | Required Work                                            | Required Verification     | Evidence |
| ------ | ------ | -------------------------------------------------------------- | -------------------------------------------------------- | ------------------------- | -------- |
| P1.1.1 | TODO   | `internal/commerce` must not define a concrete TPA Hub product | Move `ProductTPAHub` into product-owned package          | Dependency inspection     |          |
| P1.1.2 | TODO   | Generic APIs must reject empty product IDs                     | Add validation                                           | Unit test                 |          |
| P1.1.3 | TODO   | Missing product ID must never silently default to TPA Hub      | Remove fallback behavior                                 | Product #2 test           |          |
| P1.1.4 | TODO   | Legacy TPA behavior must be explicitly named                   | Restrict compatibility wrappers to migration/legacy code | Source inspection         |          |
| P1.1.5 | TODO   | Shared persistence must not contain runtime TPA defaults       | Remove/search generic store defaults                     | Repository search + tests |          |

---

## P1.2 — Single Authoritative Offer Definition

| ID     | Status | Requirement / Invariant                                                                    | Required Work                           | Required Verification    | Evidence |
| ------ | ------ | ------------------------------------------------------------------------------------------ | --------------------------------------- | ------------------------ | -------- |
| P1.2.1 | TODO   | Price must have one authoritative source                                                   | Consolidate rate card/offer price       | Checkout amount test     |          |
| P1.2.2 | TODO   | Cadence must have one authoritative source                                                 | Consolidate cadence                     | Offer snapshot test      |          |
| P1.2.3 | TODO   | Grant limits must have one authoritative source                                            | Consolidate limits                      | Grant assertion          |          |
| P1.2.4 | TODO   | Offer versions must be immutable once purchased                                            | Introduce stable offer/version identity | Historical purchase test |          |
| P1.2.5 | TODO   | Purchase must snapshot offer terms                                                         | Persist immutable terms                 | DB assertion             |          |
| P1.2.6 | TODO   | Checkout must charge exactly snapshotted amount                                            | Remove split amount logic               | Payment test             |          |
| P1.2.7 | TODO   | Grant activation must use snapshotted terms                                                | Link to P0.1                            | E2E test                 |          |
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
| P1.5.1 | TODO   | Grants are the runtime source of truth                                               | Verify all runtime reads          | Repository search         |          |
| P1.5.2 | TODO   | Missing entitlement projection must not block valid payment activation               | Remove veto dependency            | Payment test              |          |
| P1.5.3 | TODO   | Projection divergence must not change authorization                                  | Make projection derived-only      | Divergence test           |          |
| P1.5.4 | TODO   | Projection writes must not succeed while authoritative grant mutation silently fails | Tighten/update or remove path     | Test                      |          |
| P1.5.5 | TODO   | Removal path must be documented                                                      | Define migration/removal plan     | Documentation             |          |
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
| P2.PAY.1  | TODO   | Exact purchased grants              | Only exact offer snapshot activates        |          |
| P2.PAY.2  | TODO   | Partial expected grant set          | Transaction rejects; no partial activation |          |
| P2.PAY.3  | TODO   | Changed checkout intent             | Never paid without grants                  |          |
| P2.PAY.4  | TODO   | Duplicate success webhook           | Exactly-once semantic mutation             |          |
| P2.PAY.5  | TODO   | Expired/failed before success       | Defined deterministic precedence           |          |
| P2.PAY.6  | TODO   | Success before later terminal event | Paid grants remain stable                  |          |
| P2.PAY.7  | TODO   | Concurrent identical checkout       | One logical purchase/provider operation    |          |
| P2.PAY.8  | TODO   | Provider-customer creation race     | One stable customer mapping                |          |
| P2.PAY.9  | TODO   | Crash before checkout persistence   | Retry safely reconciles                    |          |
| P2.PAY.10 | TODO   | Missing compatibility projection    | Authoritative grants still activate        |          |

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
| P2.MIG.1 | TODO   | Missing purchase       | No active purchase grant              |          |
| P2.MIG.2 | TODO   | Non-paid purchase      | No active purchase grant              |          |
| P2.MIG.3 | TODO   | Missing `valid_until`  | Migration rejects/fails closed        |          |
| P2.MIG.4 | TODO   | End before start       | Migration rejects                     |          |
| P2.MIG.5 | TODO   | Unknown plan           | Migration rejects; no unlimited grant |          |
| P2.MIG.6 | TODO   | Known Pro plan         | Deterministic documented mapping      |          |
| P2.MIG.7 | TODO   | Migration interruption | Restart is safe                       |          |
| P2.MIG.8 | TODO   | Repeated migration     | Idempotent result                     |          |

## Authorization

| ID        | Status | Test                            | Expected Invariant | Evidence |
| --------- | ------ | ------------------------------- | ------------------ | -------- |
| P2.AUTH.1 | TODO   | Disabled bearer session         | Denied             |          |
| P2.AUTH.2 | TODO   | Disabled browser session        | Denied             |          |
| P2.AUTH.3 | TODO   | Disabled account reader token   | Denied             |          |
| P2.AUTH.4 | TODO   | Disabled account queued publish | Denied             |          |
| P2.AUTH.5 | TODO   | Revoked reader token            | Denied             |          |
| P2.AUTH.6 | TODO   | Expired private-read grant      | Denied             |          |

## Repository Publication

| ID         | Status | Test                                    | Expected Invariant                         | Evidence |
| ---------- | ------ | --------------------------------------- | ------------------------------------------ | -------- |
| P2.REPO.1  | TODO   | Crash before live swap                  | Old repository intact                      |          |
| P2.REPO.2  | TODO   | Crash immediately after live swap       | Recoverable consistent generation          |          |
| P2.REPO.3  | TODO   | Crash before DB commit                  | Reconciliation restores consistent state   |          |
| P2.REPO.4  | TODO   | Crash after DB commit                   | Committed generation remains authoritative |          |
| P2.REPO.5  | TODO   | Rename failure                          | No package loss                            |          |
| P2.REPO.6  | TODO   | Disk full                               | Old live repo preserved                    |          |
| P2.REPO.7  | TODO   | Signing failure                         | Old live repo preserved                    |          |
| P2.REPO.8  | TODO   | Grant expires during publish            | No unauthorized final swap                 |          |
| P2.REPO.9  | TODO   | Account suspended during queued publish | No final swap                              |          |
| P2.REPO.10 | TODO   | Next publish after crash recovery       | All prior valid packages remain            |          |

## Staging and Storage

| ID        | Status | Test                                      | Expected Invariant                          | Evidence |
| --------- | ------ | ----------------------------------------- | ------------------------------------------- | -------- |
| P2.STOR.1 | TODO   | Many oversized queued uploads             | Staging remains bounded                     |          |
| P2.STOR.2 | TODO   | Per-account staging exhaustion            | New uploads rejected safely                 |          |
| P2.STOR.3 | TODO   | Global staging exhaustion                 | Service protects disk headroom              |          |
| P2.STOR.4 | TODO   | Interrupted upload                        | Orphan cleaned                              |          |
| P2.STOR.5 | TODO   | Interrupted worker                        | Work directory reconciled                   |          |
| P2.STOR.6 | TODO   | Concurrent/multi-instance publish attempt | Explicitly rejected or correctly serialized |          |

## Egress

| ID       | Status | Test                            | Expected Invariant                                 | Evidence |
| -------- | ------ | ------------------------------- | -------------------------------------------------- | -------- |
| P2.EGR.1 | TODO   | Egress DB lookup failure        | Conservative behavior                              |          |
| P2.EGR.2 | TODO   | Egress accounting write failure | Observable and conservative behavior               |          |
| P2.EGR.3 | TODO   | Grant begins mid-day            | Only defined in-window usage counts                |          |
| P2.EGR.4 | TODO   | Grant ends mid-day              | Exact documented boundary                          |          |
| P2.EGR.5 | TODO   | Concurrent downloads near limit | Bounded/documented overshoot or strict reservation |          |
| P2.EGR.6 | TODO   | HTTP Range requests             | Correct byte accounting                            |          |
| P2.EGR.7 | TODO   | Aborted response                | Actual delivered bytes counted consistently        |          |
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
| P9.1  | TODO   | `go test ./...`                                               | PASS                             |          |
| P9.2  | TODO   | `go test -race ./...`                                         | PASS                             |          |
| P9.3  | TODO   | `go vet ./...`                                                | PASS                             |          |
| P9.4  | TODO   | `git diff --check`                                            | PASS                             |          |
| P9.5  | TODO   | TPA Hub qualification suite                                   | PASS                             |          |
| P9.6  | TODO   | Local bootstrap qualification                                 | PASS                             |          |
| P9.7  | TODO   | Bootstrap key qualification                                   | PASS                             |          |
| P9.8  | TODO   | Bootstrap qualification                                       | PASS                             |          |
| P9.9  | TODO   | TPA `go test -race ./...`                                     | PASS                             |          |
| P9.10 | TODO   | TPA `go vet ./...`                                            | PASS                             |          |
| P9.11 | TODO   | TPA qualification suite                                       | PASS                             |          |
| P9.12 | TODO   | Architecture/import guardrails                                | PASS                             |          |
| P9.13 | TODO   | Publication crash matrix                                      | PASS                             |          |
| P9.14 | TODO   | Payment adversarial matrix                                    | PASS                             |          |
| P9.15 | TODO   | Migration fault matrix                                        | PASS                             |          |
| P9.16 | TODO   | Product #2 E2E proof                                          | PASS                             |          |
| P9.17 | TODO   | Provider replacement contract proof                           | PASS                             |          |
| P9.18 | TODO   | Self-hosting independence proof                               | PASS                             |          |
| P9.19 | TODO   | Original full audit prompt rerun from scratch                 | No unresolved production blocker |          |
| P9.20 | TODO   | Each original Critical finding C1–C8 individually rechecked   | RESOLVED                         |          |
| P9.21 | TODO   | Each original Important finding I1–I19 individually rechecked | RESOLVED / explicitly accepted   |          |
| P9.22 | TODO   | Working tree clean after final verification                   | CLEAN                            |          |

---

# Original Critical Finding Closure

| Finding                                            | Status | Resolution Summary | Evidence |
| -------------------------------------------------- | ------ | ------------------ | -------- |
| C1 — Payment success activates wrong grant set     | TODO   |                    |          |
| C2 — Changed intent / out-of-order payment events  | TODO   |                    |          |
| C3 — Permanent free Nano vs expiring paid access   | TODO   |                    |          |
| C4 — Publication not crash-consistent              | TODO   |                    |          |
| C5 — Suspended accounts retain reader-token access | TODO   |                    |          |
| C6 — Unsafe legacy migration                       | TODO   |                    |          |
| C7 — Upload staging bypasses storage grant         | TODO   |                    |          |
| C8 — Egress enforcement fails open                 | TODO   |                    |          |

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
| I13 — `account_entitlements` can block activation               | TODO                |                                                       |          |
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
| All qualification suites pass                                                  | TODO   |          |
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
