# Opt-in SSH publication worker

The default publication backend remains local. SSH is an opt-in transport for the existing unsigned TPA build step only. The authoritative TPA.run server remains responsible for account/quota policy, candidate verification, signing, signed verification, and atomic activation. The SSH worker receives package inputs and bounded build policy only; it must not receive repository signing keys, server configuration, database state, or credentials other than its dedicated SSH key.

## Server configuration

Set the following environment variables for the TPA.run service:

```ini
TPARUN_PUBLICATION_BACKEND=ssh
TPARUN_SSH_WORKER_TARGET=tparun-worker@builder.example
TPARUN_SSH_IDENTITY_FILE=/etc/tparun/worker_ed25519
TPARUN_SSH_KNOWN_HOSTS_FILE=/etc/tparun/worker_known_hosts
TPARUN_SSH_PORT=22
TPARUN_SSH_CONNECT_TIMEOUT=15s
```

The identity and known-hosts files must be regular files readable by the service account. The SSH client is invoked with a clean configuration (`-F /dev/null`), batch mode, strict host-key checking, no password or keyboard-interactive authentication, no agent forwarding, no port/X11 forwarding, no TTY, and the configured pinned known-hosts file as the only host-key source. Provision and rotate the worker host key out-of-band. Do not use `StrictHostKeyChecking=accept-new`, an unpinned host key, or a broad operator SSH key. `TPARUN_SSH_BINARY` can select an absolute SSH client path.

The request uses protocol version 1 and sends a deterministic archive containing only regular `.deb` inputs plus their names, sizes, and SHA-256 digests. Job ID, per-invocation attempt ID, backend, start time, and output byte/inode limits are bound to the request and echoed in the response. The worker result is untrusted. TPA.run checks response identity, bounds, paths, file types, and digests, then runs its existing independent unsigned-candidate verification and signing/activation path. A failed SSH attempt is a failed publication; it is not resumed or silently replayed. A later client publication creates a new job. The current attempt ID is per-process (not a durable lease); running jobs are failed during service-start recovery, so disconnected attempts are never adopted by a later process.

## Worker account and host

Use a dedicated, unprivileged account and a dedicated key restricted to this worker. On OpenSSH servers, force the command regardless of the client's requested command and disable interactive/forwarding features. For example, an `authorized_keys` entry can use:

```text
restrict,command="/usr/bin/tparun worker" ssh-ed25519 AAAA... tparun-publication-server
```

Where supported, also configure `sshd` with `ForceCommand /usr/bin/tparun worker`, `PermitTTY no`, `AllowTcpForwarding no`, `AllowAgentForwarding no`, `X11Forwarding no`, `PermitTunnel no`, and `PermitUserRC no` for this account. Verify the effective `sshd -T` configuration and test the key from a disposable client. The forced worker ignores `SSH_ORIGINAL_COMMAND`; it does not offer a shell protocol. Ensure the account cannot use other keys, sudo, or unrelated services.

Install a compatible TPA binary and its required sandbox utilities on the worker. The same `unshare`/mount-namespace requirements as the local bounded runner apply: worker failure to create the sandbox is a hard failure. Do not weaken the namespace, capability, or process limits to make a worker succeed. Allocate a dedicated worker temp filesystem with an operator-enforced size/inode ceiling sufficient for the maximum admitted input, candidate, and transfer overhead; set `TMPDIR` for the forced-command account (for example with an `sshd Match User` `SetEnv TMPDIR=/srv/tparun-worker/tmp`) to a private directory on that filesystem. The worker uses a private, per-attempt temporary directory there and removes it after normal protocol completion. It holds a lease while working; on a later valid worker invocation, it removes only unlocked, current-format workspaces owned by that account. A hard kill can therefore leave data until the next invocation. Legacy or incomplete workspaces without a lease are deliberately not auto-deleted; inspect and clean those as the worker administrator. If the worker is idle after a failure, stale data remains until an operator cleanup or reset of the dedicated temporary filesystem. Limit the worker account's CPU, memory, process count, and disk usage with host controls; protocol-supplied limits are not a substitute for worker-host resource controls.

## Security and operations

- Keep the signing key, database, user credentials, capacity policy, and active repository only on the authoritative server.
- Use network policy allowing only the server-to-worker SSH connection. Do not expose the worker to arbitrary users.
- Treat all returned files and diagnostics as untrusted. Never activate worker output directly or configure it to sign.
- A network loss is reported as failure. The synchronous SSH channel and one-shot job model prevent a later process from accepting a delayed result; inspect the durable job record before asking a client to retry.
- The local backend remains the default and can be restored with `TPARUN_PUBLICATION_BACKEND=local`.
- This implementation does not provide a remote queue, durable remote lease, worker pool, or automatic retry. Validate the exact worker image, OpenSSH forced-command policy, TPA binary, sandbox, and host resource limits in an isolated environment before enabling the backend.

## Qualification status

The SSH backend has been end-to-end qualified in disposable Debian Trixie infrastructure against OpenSSH, the real `tparun worker`, bounded TPA publication, server-side signing/activation, and an APT client. Qualification also exercised restricted forced-command behavior, pinned host keys, transport/worker failures, a controlled timeout, and service restart with an in-flight attempt. This does not qualify any particular production or NAS host, package, network policy, resource limit, or worker-image deployment; repeat qualification against the exact intended environment before enabling it.
