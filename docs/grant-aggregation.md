# Effective grant aggregation

Runtime authorization derives one effective grant per customer, product, and
capability from independently persisted active grant instances.

* A boolean capability is enabled when any applicable active instance enables
  it.
* Storage and egress limits use the component-wise maximum across applicable
  active instances. They are not summed.
* Operation counts use the maximum. The operation metric comes from the
  instance that supplies that maximum count.
* Expired or disabled purchase instances do not contribute.
* An open-ended grandfathered `nano`, `standard`, or `pro` grant participates
  without losing its independent `legacy` provenance. A weaker purchase can
  therefore never reduce a grandfathered limit.
* Offer identity, cadence, validity, and purchase linkage remain properties of
  each authoritative instance. The derived effective grant does not replace or
  erase those rows.

Payment arrival order and SQL row order do not alter these combination rules.
There is currently no product operation that revokes or disables a paid grant;
expiry is the supported lifecycle transition.
