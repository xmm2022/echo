# Changelog

## v0.1.0 - pending tag

Release hardening is in progress. The `v0.1.0` tag is blocked: the current real
sidecar returns `sig invalid` during 115 CAS restore, so the real acceptance test
does not pass yet.

### Reproducibility tuple

- Echo version: `v0.1.0`
- Echo source commit: `TBD-final-release-commit`
- Echo Docker build arg: `ECHO_VERSION=v0.1.0`
- Sidecar tools source: `github.com/xmm2022/openlist-guangyapan-src`
- Sidecar tools ref: `3324d78d9de9a060bc6830d0f4b2d012ea47576b`
- Echo Docker build arg: `SIDECAR_TOOLS_REF=3324d78`
- Sidecar image tag: `ghcr.io/xmm2022/openlist-guangyapan:feat-cas-tools-3324d78`
- Sidecar image digest: `TBD-pending-real-run`
- `sidecar.default.min_version`: `TBD-pending-real-sidecar-version`
- `115share2cas` binary sha256: `6ec60458d24a31d89ebc4ed1d1f2bd2ac6921c48e46f7bc7d7a21cc9bed21acd`
- Observed real sidecar version string: `dev (Commit: unknown) - Frontend: rolling - Build at: unknown`

### Validation

- `go test ./... -count=1`: `PASS`
- `go vet ./...`: `PASS`
- `go test -tags=integration -race ./integration/... -count=1`: `PASS`
- `make build VERSION=v0.1.0`: `PASS`
- `make docker VERSION=v0.1.0`: `PASS` (`echo:dev` image id `sha256:a171e29ecfcef0aac86ea0fca85a35aef800abc7bd192c0b9cb2b79e916e4e6f`)
- `integration_real` with pinned `115share2cas@3324d78`, `ECHO_TEST_115_LIMIT=1`: `FAIL` (`sidecar api error: code=500 message=sig invalid: sig invalid`)
- `integration_real` with pre-generated source-aware 115 CAS sample, size `1837819460`: `FAIL` (`sidecar api error: code=500 message=sig invalid: sig invalid`)
