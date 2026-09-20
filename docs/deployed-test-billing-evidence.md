# Deployed Stripe Test Checkout Evidence

Date: 2026-09-11.

The deployed `tpahub.service` was inspected through `ssh vserver` without
reading billing secrets. It was `active (running)` and its local `/version`
endpoint reported version `0.1.1`, startup `2026-09-08T15:51:52Z`, and revision
`unknown`.

The non-secret runtime billing settings were:

```text
TPARUN_BILLING_PROVIDER=stripe
TPARUN_BILLING_ENVIRONMENT=test
TPARUN_BILLING_RATE_CARD=production-nano-v1
TPARUN_BILLING_PURCHASES_ENABLED=true
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

## Historical Webhook Diagnosis

On 2026-08-30, two Stripe test `checkout.session.completed` deliveries returned
HTTP 400. Stripe records show both sessions were paid with a 500 EUR subtotal
and 95 EUR automatic tax, for a 595 EUR total. The then-deployed webhook
compared the 595 EUR `amount_total` with the immutable 500 EUR quoted purchase
amount and rejected the event. The current source verifies Stripe's
`amount_subtotal` against that quoted amount instead; its production-handler
test covers a tax-inclusive 595 EUR total. This is source-level correction
evidence only and does not establish deployed activation until a fresh test
checkout is completed after an authorized deployment.
