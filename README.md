# TPA Hub

TPA Hub is a small local service for publishing and serving private/public APT repositories. **TPA Hub uses TPA; it does not implement APT repository generation itself.** TPA is an external executable and is never vendored or imported.

## Build and run

Build TPA separately (`cd ../tpa && go build -o tpa .`) and either put it on `PATH` or set `TPA_BIN`.

```sh
go build -o tpahub ./cmd/tpahub
TPA_BIN=/path/to/tpa TPAHUB_DATA_DIR=./data ./tpahub serve
```

Configuration: `TPAHUB_LISTEN` (default `127.0.0.1:8080`), `TPAHUB_DATA_DIR` (default `./data`), `TPA_BIN` (default `tpa`), `GNUPGHOME`, `TPAHUB_GPG_FINGERPRINT` (full fingerprint), `TPAHUB_PUBLISH_TIMEOUT` (default `10m`), and `TPAHUB_MAX_UPLOAD_BYTES` (default 512 MiB). A signing fingerprint is required for the APT client flow.

## Local publishing

```sh
curl -X POST localhost:8080/v1/repositories \
  -H 'content-type: application/json' -d '{"name":"tpa"}'
# use the returned id
curl -X POST 'localhost:8080/v1/repositories/ID/publishes?filename=tpa_1_amd64.deb' \
  --data-binary @tpa_1_amd64.deb
curl localhost:8080/v1/publish-jobs/JOB
```

Uploads are complete snapshots accumulated from retained `.deb` files: each successful publish retains all prior artifacts and adds the new upload. TPA receives that complete artifact set and atomically replaces `data/repos/ID`. Thus 1.0.0 and 2.0.0 coexist; no package metadata database is maintained.

Layout:

```text
data/{tpahub.db,incoming/<job>,work/<job>,artifacts/<repo-id>,repos/<repo-id>}
```

Repositories are served at `http://127.0.0.1:8080/r/ID/`. State is SQLite (`repositories` and `publish_jobs`). Failed TPA invocations mark the job failed and leave the previously published repository untouched.

## Client registration

The same binary has a deliberately minimal client:

```sh
tpahub repo add ID --server http://127.0.0.1:8080
apt update
apt install tpa
# remove only Hub's APT configuration:
tpahub repo remove ID
```

It fetches the public key from `/v1/repositories/ID/signing-key`, writes `/etc/apt/keyrings/tpahub-ID.gpg`, and writes `/etc/apt/sources.list.d/tpahub-ID.list` using `signed-by`. Repeating `repo add` does not duplicate entries. Authentication/login is intentionally not implemented yet.

## Development

```sh
go test -race ./...
go vet ./...
```

The real TPA and Debian APT qualification requires the separately built TPA executable, GPG, dpkg-deb, and a Debian test environment. Bootstrap TPA locally, publish its real `.deb` through the normal API, then register the resulting repository with `tpahub repo add`.
