# Egress accounting uncertainty

Every repository delivery creates a durable `egress_accounting_uncertain` row
before response bytes are served. Completion records the delivered bytes and
removes that row in one SQLite transaction. A write failure or process crash
therefore leaves durable uncertainty.

Any uncertainty for an account fails closed with HTTP 503, including private
repositories and after restart. It is not automatically cleared merely because
SQLite becomes writable. Recovery is an explicit administrative reconciliation
for one exact marker:

```text
tparun admin egress list-uncertain --data-dir <data-dir>
tparun admin egress inspect <delivery-id> --data-dir <data-dir>
tparun admin egress recover <delivery-id> --version <version> \
  --usage-date <YYYY-MM-DD> --bytes <measured-bytes> --data-dir <data-dir>
```

Recovery requires the marker's exact repository/account scope and version,
rejects active `streaming` markers, atomically records the supplied usage and
removes only that marker, and writes an administrative audit record. There is
no account-wide clear operation. The administrative CLI opens the durable store
without applying normal service-startup marker reconciliation, so it can safely
inspect a live service.
