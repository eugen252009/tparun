# Authorization separation

TPA.run keeps three authorities independent:

```text
account identity/session  -> who is authenticated
account entitlement       -> which resource limits apply
repository policy         -> which repository operations are allowed
operator session          -> which administrative policy changes are allowed
```

A quota is not a permission. In particular, a finite or very large storage
limit does not grant access to another account's repository, and it does not
make the account an operator.

## Accounts and entitlements

An account is identified internally by its immutable account ID and currently
uses its normalized email as its login identifier. Its password and sessions
are replaceable credentials. Product grants in `product_grants` represent
entitlements such as repository publication storage and download egress.
Those grants are policy data; they do not establish repository ownership or
operator authority.

The historical phrase “god account” is not a product role. It described a
normal account whose limits had been adjustable by an operator. An operator
changing that account from a finite quota to a larger or unlimited quota does
not change its account class, create an operator session, or grant access to
other repositories.

## Repository authority

Repository mutation and publication are authorized by `RepositoryPolicy`:

1. the repository must have a valid owner;
2. the authenticated account must be that owner; and
3. the owner must have the relevant product capability and active entitlement.

The checks are deliberately ordered so a large entitlement cannot bypass
ownership. The bootstrap repository is separately owned by its internal
bootstrap account:

```text
https://tpa.run/r/tpahub-bootstrap/tpahub-bootstrap
```

Recovering or enlarging a personal account does not grant it bootstrap
publication authority.

## Operator authority

`/ops` uses a separate WebAuthn-backed operator session. In the current
implementation the bootstrap operations can:

- designate or remove the global bootstrap repository;
- change the bootstrap account's finite storage and egress capacity; and
- record those changes in `admin_audit_log`.

They do not turn the target account into an operator and do not provide a
generic self-service account quota or cross-repository capability endpoint.
Normal account sessions cannot use `/ops` routes.

Bootstrap capacity changes currently accept positive finite byte limits. There
is no separate customer-facing “unlimited” mutation path in this UI. Any
future unlimited representation must remain entitlement data and must not
become an authority signal.

## Recovery boundary

Credential recovery, if added, must preserve the existing account ID,
repositories, ownership, entitlements, and accounting identity. It must not
implicitly change repository authority or operator authority. The current
product has no customer password-reset flow; operator recovery is for operator
WebAuthn enrollment, not customer account recovery.

## Qualification invariant

The required matrix is:

```text
large/unlimited entitlement + foreign repository -> reject
repository owner + active publish grant       -> accept
normal account session + /ops                  -> reject
operator session + bootstrap capacity change  -> policy mutation only
operator mutation                             -> target remains non-operator
```
