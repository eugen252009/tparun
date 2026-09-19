# Account SSH login

TPA Hub supports SSH public-key login as an additional customer authentication method:

```text
tpahub login --ssh
tpahub login --ssh ~/.ssh/id_ed25519
```

The optional identity-file argument takes precedence over automatic selection. Without
it, the CLI first uses identities available through `SSH_AUTH_SOCK`, then supported
unprotected default files (`id_ed25519`, `id_ecdsa`, and `id_rsa`). Encrypted or
unsupported keys must be supplied through an agent or a usable identity file.

A public key must first be registered to the existing account through the authenticated
`/v1/account/ssh-credentials` endpoint (DELETE the credential URL to revoke it). New credentials start disabled with no capabilities. PATCH the credential URL with named `enabled`, `list`, `read`, and `write` booleans to configure it. Registration stores only the public key,
fingerprint, label, and timestamps. Private-key material is never sent to or stored by
TPA Hub.

Login is challenge-response. The server issues a short-lived, single-use random
challenge. The client signs a domain-separated payload containing protocol version,
purpose, challenge ID and nonce, account identity, and service identity using the
OpenSSH signing format. The server verifies the signature against the registered key,
consumes the challenge, and creates the same normal account session as password login.

Use `tpahub account credentials` for interactive inspection and configuration, or `tpahub account credentials add <identity-file>` to register a public key. SSH account credentials are not operator credentials. SSH authentication does not alter
product grants, quotas, repository ownership, repository capabilities, bootstrap access,
or `/ops` access. Operator WebAuthn remains a separate trust domain.

```text
credential recovery/change
!= entitlement mutation
!= repository ownership mutation
!= operator authority
```
