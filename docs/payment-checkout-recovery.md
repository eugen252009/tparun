# Provider checkout recovery authority

TPA Hub persists the purchase ID, customer, product, offer identity, rate card,
tax classification, amount, currency, provider/environment, entitlement
validity, checkout request version, and grant snapshot set before calling the
external checkout provider. The provider idempotency identity is derived
deterministically from that durable purchase identity.

If the provider creates a checkout and the Hub stops before persisting its
reference, a signed webhook may recover the exact provider reference for that
purchase. Recovery finalizes the commercial terms already stored on the
purchase; provider event time, webhook receive time, replay time, and current
wall-clock time never define or extend validity.

A late payment can therefore mark a purchase paid while its original immutable
entitlement is already expired. It does not create a replacement interval.
Duplicate or delayed events cannot move grant dates.

Before automatic recovery, the Hub validates the complete local commercial
authority using the same canonical snapshot count and digest rules as normal
activation. This includes purchase/customer/product/offer/rate-card identity,
amount and currency, exact validity, snapshot finalization state, snapshot-set
metadata, every snapshot row, supported cadence and limits, and product-owned
capability identity. Snapshot rows are selected only by the exact purchase key.
Validation and recovery finalization share one SQLite transaction; finalized
snapshot triggers prevent later changes.

If a historical/incomplete purchase lacks a valid durable interval or complete
activatable snapshot authority, the provider reference is retained and the
purchase enters `reconciliation_required` without being falsely finalized.
The operation-ID webhook persists the purchase state, provider correlation,
provider event ID/type, positive payment state, and specific authority failure
in the same SQLite transaction. No grant instance or compatibility projection
is created, and new checkout creation for the same account/product/provider
environment is blocked. Positive payment evidence wins over later failure or
expiry notifications and survives restart.

Provider references are unique for each provider/environment pair when non-empty.
Database opening explicitly rejects historical duplicate non-empty references;
it never picks, merges, or deletes one. Empty references remain available to
multiple unlinked pending purchases.

`reconciliation_required` has no automatic repair path. An operator can inspect
`billing_purchases.reconciliation_reason` together with the correlated
`billing_provider_events` purchase/reference columns, then restore trustworthy
local commercial authority only through a separately audited recovery procedure.
