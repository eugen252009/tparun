# Startup cleanup and recovery policy

Startup reconciliation is fail-closed. A failure to read durable publication
state, validate the authoritative generation, reconcile `current`, scan
staging, or remove terminal job input causes startup to fail; the process must
not report a healthy service while publication or capacity state is unknown.

The active generation, the durable publication target, generations needed for
recovery, and data belonging to nonterminal executable jobs are never removed
by reconciliation. Terminal incoming payloads, terminal work trees, abandoned
candidate/work trees, and stale temporary publication pointers are transient
and are removed or retried on the next restart. Once startup reconciliation
succeeds, terminal job cleanup failures remain durably pending with attempt
counts and a capped exponential `cleanup_next_retry` deadline; the live cleanup
worker retries them without releasing the corresponding in-process capacity
reservation until cleanup succeeds.

A harmless old non-authoritative artifact may be retained when its deletion
fails, with a structured warning, provided it cannot affect serving,
authorization, publication selection, or capacity accounting. Reconciliation
is idempotent: a later healthy restart retries remaining cleanup without
repeating destructive actions against authoritative state.
