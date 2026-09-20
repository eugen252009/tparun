# Effective grant aggregation

Runtime authorization uses only enabled grant rows active at the evaluation
time. Historical `legacy` rows are audit-only after the bounded migration and
never authorize TPA.run access. `migration`, `purchase`, and explicit `local`
grants use the same activity rule.

For one customer/product/capability:

* Boolean permission is available when at least one active contributor exists.
* Storage and egress limits are the component-wise maximum, not a sum.
* Non-zero operation counts may combine only when their metric IDs are equal.
  Incompatible metrics fail closed with
  `ErrIncompatibleGrantMetrics`; unrelated metric namespaces are never compared.
* Expiry or disabling one contributor removes only that contribution.

A single-row result preserves that row's commercial metadata. A multi-row
result uses `source=effective`, clears offer, purchase, cadence, validity, and
creation metadata, and carries an explicit `EffectiveGrant` containing every
unaggregated authoritative contributor. It therefore cannot masquerade as one
purchased grant.

`PeriodAt` delegates to the contributor supplying the effective egress limit,
then the operation limit, then the storage limit. Equal limits use a stable
source/purchase/offer ordering, so SQL row order cannot change the result. This
period-basis index is explicit in `EffectiveGrant`; no contributor's commercial
identity is copied onto the aggregate object.
