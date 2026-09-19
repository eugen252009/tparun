# Clean Debian bootstrap qualification

**Qualification date:** 2026-09-19  
**Verdict:** `TPA STACK CLEAN DEBIAN BOOTSTRAP QUALIFIED`

This records the final consumer qualification of the public TPA stack. The
qualified path is deliberately separate from Docker qualification and from
production deployment work.

## Authoritative baseline

```text
Canonical/production revision: e197f9c1ad59b8d9e69befee2cadb2f6ae974633
TPA Hub version:              0.1.1
Schema:                       5
Production readiness:         true
Bootstrap repository:         f102d36c2eca9bbaa8ddcff08b3c645f
Repository authority:         internal-owned, public, global bootstrap
Signing fingerprint:          EDCEE63AA803737C8ABBC1E4A34190A2829D5E54
Bootstrap package:            tpahub-release_0.1.3_all.deb
Bootstrap SHA-256:            420d7886545f52cf2a8c5ce987bba55a2ddd0e9888a3c71caddd4670c0d6f18c
```

The public `InRelease`, package indexes, installer, and release package were
read-only verified before the guest qualification.

## Final VM

```text
Domain:       tpahub-bootstrap-debian12-final
Guest:        Debian GNU/Linux 12 (bookworm), amd64
Virtualizer:  KVM
Resources:    2 vCPU, 4096 MiB RAM, 25 GiB qcow2 overlay
User:         tpatest
Network:      libvirt qemu:///system default NAT
```

The immutable base is:

```text
/home/eugen/vms/tpahub-bootstrap-debian12/base/debian-12-generic-amd64.qcow2
```

Its verified SHA-512 is:

```text
84946d6e2f55b1e8dcc114efc059f13882bd2ccdf36852126a13a0aa4a4ce0efee945406b78280e5942096639edc2a3ced20b70c12d9c7c7366ce364ba47a0ff
```

The preserved final overlay is:

```text
/var/tmp/tpahub-bootstrap-debian12-final/tpahub-bootstrap-final.qcow2
```

Cloud-init was delivered by `virt-install --cloud-init` using a fresh NoCloud
instance ID and seed. The first boot reached `cloud-init status: done`; later
boots correctly report cloud-init disabled because first-boot provisioning is
complete.

The previous manual reset was not reused: it had an unreliable seed/cloud-init
lifecycle and a backing-file accessibility problem for system libvirt. A fresh
libvirt-readable base copy, overlay, domain, seed, and instance ID fixed that
harness failure.

## Clean consumer path

The pre-bootstrap guest had no `tpahub`, `tpa`, `mema`, or `bloom` commands and
no TPA APT source. The minimal qualified path was:

```sh
sudo apt-get update
sudo apt-get install -y ca-certificates curl gnupg

curl -fsSL https://tpa.run/install.sh -o /tmp/tpahub-install.sh
sudo bash /tmp/tpahub-install.sh

sudo apt-get update
sudo apt-get install -y mema bloom mema-go

sudo mema install go 1.26.5

go version

bloom search "math min u32"
bloom describe math.min.u32
```

The resulting package/runtime baseline was:

```text
tpahub:       0.1.1
mema:         0.2
bloom:        0.1.0~bootstrap.7
mema-go:      1.26.5
Go:           go1.26.5 at /usr/local/bin/go
```

`mema-go` is the APT recipe/provider package. `sudo mema install go 1.26.5`
is the Mema installation and activation step; it produced the persistent
`/usr/local/bin/go` link.

Fresh-login PATH, `bash -lc`, and functional Bloom discovery/description all
passed. After a real VM reboot, APT update, the signing key, all three core
binaries, Go, login PATH, and the same Bloom operation continued to work.

## VM reuse and clean recreation

Start, stop, and inspect the preserved VM with:

```sh
virsh -c qemu:///system start tpahub-bootstrap-debian12-final
virsh -c qemu:///system shutdown tpahub-bootstrap-debian12-final
virsh -c qemu:///system domifaddr tpahub-bootstrap-debian12-final --source lease
```

Connect using the disposable qualification key (never copy host production
keys into the guest):

```sh
ssh -i /home/eugen/vms/tpahub-bootstrap-debian12/final/ssh/id_ed25519 \
  tpatest@<guest-ip>
```

A clean recreation is not just deleting an overlay. It must create:

```text
new libvirt-readable base copy if needed
new qcow2 overlay
new cloud-init user-data and meta-data
new unique instance-id
new seed attached by virt-install --cloud-init
new domain definition
```

Then wait for the DHCP lease, SSH, and first-boot cloud-init completion before
starting product qualification. The canonical final configuration is kept
under:

```text
/home/eugen/vms/tpahub-bootstrap-debian12/final/
```

## Known issue and boundary

The optional `mema-bun` path is a known non-blocking issue. Its recipe/archive
download path was exercised, but activation produced an invalid executable path;
the failed optional installation was removed from the final qualification VM.
This is recorded only for a separate future Mema/Bun task. It does not weaken
the qualified Go path or the core bootstrap verdict.

The earlier Docker DNS failure and the old VM reset failure were test-harness
issues, not evidence of a public TPA bootstrap failure.

**Core bootstrap qualification: PASS.**  
**Mema/Bun repair: intentionally deferred.**
