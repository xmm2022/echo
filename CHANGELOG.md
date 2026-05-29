# Changelog

## v0.1.0 - pending tag

Release hardening is in progress. The `v0.1.0` tag is blocked until the real
sidecar version string is measured, `sidecar.default.min_version` is pinned to
that exact string, and the real 115 acceptance test passes.

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

### Validation

- `go test ./... -count=1`: `PASS`
- `go vet ./...`: `PASS`
- `go test -tags=integration -race ./integration/... -count=1`: `PASS`
- `make build VERSION=v0.1.0`: `PASS`
- `make docker VERSION=v0.1.0`: `PASS` (`echo:dev` image id `sha256:37bfc60afe63d2e527c87031e67603fe6581875db67939978fe4ff7df9f128b6`)
- `integration_real`: `TBD-pending-operator-run`
