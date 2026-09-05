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

If a historical/incomplete purchase lacks a valid durable local interval, the
provider reference is retained and the purchase enters
`reconciliation_required`. Provider events are recorded with that state, no
grants activate, and new checkout creation for the same account/product/provider
environment is blocked. Positive payment evidence wins over later failure or
expiry notifications. This state has no automatic repair path: an operator must
inspect the purchase and provider event evidence and restore trustworthy local
commercial authority through a separately audited recovery procedure.
