# Deployed Stripe Test Checkout Evidence

Date: 2026-09-11.

The deployed `tpahub.service` was inspected through `ssh vserver` without
reading billing secrets. It was `active (running)` and its local `/version`
endpoint reported version `0.1.1`, startup `2026-09-08T15:51:52Z`, and revision
`unknown`.

The non-secret runtime billing settings were:

```text
TPAHUB_BILLING_PROVIDER=stripe
TPAHUB_BILLING_ENVIRONMENT=test
TPAHUB_BILLING_RATE_CARD=production-nano-v1
TPAHUB_BILLING_PURCHASES_ENABLED=true
```

An isolated account was registered against the loopback service, then used to
create one Nano purchase through `POST /v1/billing/purchases/nano`. The probe
discarded the account token and Checkout URL and recorded only:

```text
provider_host=checkout.stripe.com
purchase_status=pending
billing_environment=test
```

This verifies deployed Stripe **test-mode checkout creation**, including durable
pending purchase creation. It does not verify hosted test-card completion,
Stripe webhook delivery, paid activation, refunds, disputes, or live payments.
Those claims remain unchecked until independently observed.
