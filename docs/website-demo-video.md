# Website terminal demo storyboard

Status: storyboard only; no video asset is published yet.

## Goal

Record a truthful 50–60 second terminal walkthrough showing the qualified
public path from clean Debian 12 to a real project build. Use a disposable
Debian 12 amd64 VM, not the developer host or a prepared worktree.

Keep the terminal large, crop unrelated desktop content, and never show keys,
tokens, credentials, or private repository URLs.

## Recording sequence

| Time | Scene | Commands / screen |
|---|---|---|
| 0–5s | Clean machine | `cat /etc/os-release`; `command -v tparun || true` |
| 5–15s | Public bootstrap | `curl -fL https://tpa.run/tparun-bootstrap.deb -o tparun-bootstrap.deb`; `sudo apt install ./tparun-bootstrap.deb`; `sudo apt update`; `sudo apt install tparun` |
| 15–23s | Ecosystem packages | `sudo apt install -y mema bloom mema-go mema-bun` |
| 23–32s | Runtimes | `sudo mema install go 1.26.5`; `sudo mema install bun 1.3.14`; `go version`; `bun --version` |
| 32–48s | Real project | Clone `https://github.com/rs/zerolog.git`, enter it, run `go mod download`, `go test ./...`, and `go build ./...` |
| 48–54s | Bloom | `bloom search "math min u32"` or `bloom describe math.min.u32` |
| 54–60s | Close | `Fresh Debian → working development environment` / `tpa.run` |

The demo repository is `rs/zerolog`: it is small enough for a short recording,
uses external Go modules, and visibly exercises dependency resolution, tests,
and a build. The landing page separately shows the qualified templ and Hono
workflows without making the short video wait for a large JavaScript install.

The recording script should also print the repository commit with:

```sh
git rev-parse HEAD
```

Do not claim a fixed timing until the actual recording is measured. If network
waits are edited, use a clear small jump cut or an “download omitted” note.

## Clean VM preparation

Use the known Debian KVM reset procedure rather than cleaning the existing
qualification workspace in place:

```text
immutable Debian base
→ new qcow2 overlay
→ new cloud-init user-data and meta-data
→ new instance-id
→ new seed
→ new libvirt domain
```

Wait for DHCP, SSH, and first-boot cloud-init completion. Confirm the VM is
clean before recording. The existing qualified VM remains preserved separately.

## Exact project segment

```sh
set -e
mkdir -p "$HOME/demo"
cd "$HOME/demo"
git clone --depth 1 https://github.com/rs/zerolog.git
cd zerolog
git rev-parse HEAD
go mod download
go test ./...
go build ./...
```

Expected result: successful tests and build. If the current upstream checkout
is no longer the qualified revision, record the actual commit and do not imply
that the older qualification commit was tested in that footage.

## Title card and accessibility

Closing text:

```text
Fresh Debian → working development environment
TPA.run + Mema + Bloom
https://tpa.run
```

When the video is recorded, add captions and a transcript. The site currently
renders an intentional “Terminal demo coming soon” placeholder rather than a
fake poster or broken embed.
