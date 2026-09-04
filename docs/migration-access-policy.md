# Historical migration access policy

Historical eligible accounts receive 6 months of free TPA Nano. No permanent
plan grandfathering exists.

Eligibility comes from an authoritative pre-existing active `legacy`
entitlement or enabled, active TPA Hub `legacy` grant at the
`legacy-six-month-nano-v1` data migration. Legacy rows for other products and
disabled, expired, or future TPA Hub grants are ineligible. New hosted accounts
receive no grant; new self-hosted/local accounts use the separate `local`
source and are not migration-eligible.

Migration validates legacy plan and offer identities and parses validity
timestamps as timestamps before evaluating eligibility. Accounts without a
pre-existing legacy entitlement or TPA Hub legacy grant are not manufactured
during startup.

The first successful migration stores one `migration` grant set using the
current product-owned Nano offer. Its `valid_from` is the migration commit
anchor and `valid_until` is exactly six calendar months later. The marker and
stored timestamps make reopen idempotent: restart, purchase, suspension, and
reactivation cannot extend the window. Historical Standard/Pro limits are
removed from runtime authorization and replaced by Nano limits.

Migration and purchase grants remain independent. Only enabled grants active at
the evaluation time contribute. At migration expiry, paid instances continue
normally; without one, hosted product access ends.
