# Egress accounting uncertainty

Every repository delivery creates a durable `egress_accounting_uncertain` row
before response bytes are served. Completion records the delivered bytes and
removes that row in one SQLite transaction. A write failure or process crash
therefore leaves durable uncertainty.

Any uncertainty for an account fails closed with HTTP 503, including private
repositories and after restart. It is not automatically cleared merely because
SQLite becomes writable. Recovery is an explicit administrative reconciliation:
account for the uncertain delivery conservatively, then call the store recovery
operation that clears the account marker. This is currently a store-level
administrative integration point, not a public HTTP or CLI command. Tests use
it only after simulating this explicit decision; an operator-facing recovery
command remains separate operational work.
