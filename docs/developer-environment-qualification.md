# Developer Environment Qualification

Date: 2026-09-19

This qualification used the preserved Debian 12 amd64 VM
`tpahub-bootstrap-debian12-final` after a fresh SSH login and after reboot.
The VM consumed Mema `0.2-2` from the existing signed bootstrap repository.

## Environment

```text
Go:       go1.26.5 at /usr/local/bin/go
Bun:      1.3.14 at /usr/local/bin/bun
templ:    v0.3.1020 at /home/tpatest/go/bin/templ
live-server: /home/tpatest/.bun/bin/live-server
GOROOT:   /opt/mema/go/1.26.5
GOPATH:   /home/tpatest/go
GOBIN:    (empty; Go default)
GOMODCACHE: /home/tpatest/go/pkg/mod
GOCACHE:    /home/tpatest/.cache/go-build
GOENV:      /home/tpatest/.config/go/env
GOPROXY:   https://proxy.golang.org,direct
```

The post-fix login PATH was:

```text
/home/tpatest/.local/bin:/home/tpatest/.bun/bin:/home/tpatest/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/local/games:/usr/games
```

`HOME`, `GOPATH`, `GOMODCACHE`, and `GOCACHE` were writable.

## Repositories

All repositories were cloned over HTTPS into
`~/qualification-workspace`; no host worktrees were copied or mounted.

| Repository | Commit | Workflow | Result |
|---|---|---|---|
| `github.com/rs/zerolog` | `5eb5f3a76c09eeadc1f2464baf4a4e3f1037c830` | `go mod download`, `go test ./...`, `go build ./...` | PASS |
| `github.com/a-h/templ` | `04abee5364c6fab2bde8c00d215fdcb630ad6a94` | `templ generate` in `examples/counter`, Go tests/build | PASS |
| `github.com/honojs/hono` | `098e11912ab244c5c33931de007f04dc8e3c2929` | `bun install`, `bun run test:bun`, `bun run build` | PASS |

Hono installed 781 packages in 18.92 seconds on the initial run. Its Bun
workflow ran 26 tests with 0 failures. The build exercised project-local
`publint` and completed successfully.

## Secondary tools

`templ` was installed normally:

```sh
go install github.com/a-h/templ/cmd/templ@v0.3.1020
```

After the Mema login-path fix, it was visible and executable from the current
shell, `bash -lc`, a fresh SSH login, and after reboot. Its generator produced
code for `examples/counter`; the generated result passed `go test ./...` and
`go build ./...`.

Bun's global `live-server@1.2.2` was installed with:

```sh
bun add --global live-server
```

The binary was visible after fresh login and reboot. The package's direct
launcher requires Node, which is not part of this Go/Bun qualification, so the
expected direct command reports `node: No such file or directory`. The
supported Bun execution path was qualified functionally:

```sh
bun x --bun live-server <directory> --port=8123 --no-browser
curl http://127.0.0.1:8123/index.html
```

The request succeeded before the server was stopped. No server was left
running.

## Login and reboot evidence

Both non-interactive workflows passed from `bash -lc`:

```sh
cd templ/examples/counter && templ generate && go test ./... && go build ./...
cd zerolog && go mod download && go test ./... && go build ./...
cd hono && bun install && bun run test:bun && bun run build
```

After reboot, the same templ, zerolog, and Hono workflows passed again. A
fresh post-reboot clone of `zerolog` at commit
`5eb5f3a76c09eeadc1f2464baf4a4e3f1037c830` completed in approximately one
second, followed by dependency download, tests, and build successfully.

## Environment defect found and fixed

Initially, Mema's login loader exposed `/usr/local/bin` but not the standard
user binary locations used by runtime package managers:

```text
$HOME/go/bin
$HOME/.bun/bin
```

Consequently, `templ` and Bun-global `live-server` were installed but not
reachable by command name in a fresh login. The smallest Mema fix adds both
standard user-tool directories to the login PATH, without shell-local exports
or runtime redesign.

Mema commits:

```text
bce47f9 Expose Go user tools in fresh Mema logins
8fbdcf6 Expose Bun user tools in fresh Mema logins
```

Regression coverage is in `Mema/tests/test_environment_loader.sh`. The fixed
Mema package is `0.2-2`, published through the existing signed repository and
installed through normal APT.

## Timing samples

```text
Repository clone:             ~1s each
zerolog module download:      ~1s
zerolog tests:                ~7s
Hono bun install:             ~19s
Hono Bun tests:               ~1s
Hono build:                   ~6s
fresh post-reboot clone:      ~1s
```

## Verdict

```text
TPA STACK CLEAN DEBIAN BOOTSTRAP QUALIFIED
MEMA GO AND BUN RUNTIMES QUALIFIED
REAL DEVELOPMENT WORKFLOW QUALIFIED
```
