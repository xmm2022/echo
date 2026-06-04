# Changelog

## Unreleased

### v0.3 discovery

- Added TMDB-backed subscriptions, Telegram/poster/manual discovery sources,
  deterministic rule scoring, admin candidate/match review, discovery scheduler
  jobs, and 115-only dispatch into the existing `ingest_producer` pipeline.
- Added admin JSON/UI surfaces under `/api/discovery/*` and `/ui/discovery/*`.
- Added fake full-path integration coverage and an environment-gated real
  Telegram + 115 discovery release gate.
- Local verification recorded on 2026-06-04:
  - `go test ./... -count=1`: `PASS`
  - `go test ./integration -run 'TestDiscoveryFakeFullPath|TestDiscoveryRealTelegram115Gate' -v`:
    fake gate `PASS`, real gate `SKIP` without `ECHO_REAL_DISCOVERY_GATE=1`
  - `make migrate`: `PASS`
  - `make build`: `PASS`

## v0.1.0 - pending tag

Release hardening is in progress. The earlier 115 real-gate `sig invalid`
blocker was resolved by the Task 14 sidecar hotfix recorded in
`docs/superpowers/release-gates/2026-06-03-echo-v0.3-task14-sidecar-hotfix.md`.
The hotfix is now reproducible as a patch, but the tag is still blocked until
that sidecar change is available as a real git ref/image and the final release
metadata is pinned.

### Reproducibility tuple

- Echo version: `v0.1.0`
- Echo source commit: `TBD-final-release-commit`
- Echo Docker build arg: `ECHO_VERSION=v0.1.0`
- Sidecar tools source: `github.com/xmm2022/openlist-guangyapan-src`
- Sidecar tools ref: `3324d78d9de9a060bc6830d0f4b2d012ea47576b`
- Echo Docker build arg: `SIDECAR_TOOLS_REF=3324d78`
- Sidecar image tag: `ghcr.io/xmm2022/openlist-guangyapan:feat-cas-tools-3324d78`
- Local hotfix sidecar binary: `/home/nax/.cache/openlist-115cas/openlist-guangyapan`
- Local hotfix sidecar binary sha256: `a827c1f52cdc88a2ba3dedb740d5565b569aef170f3f77d3bd47b068403e6445`
- Local hotfix patch:
  `docs/superpowers/release-gates/2026-06-04-openlist-115cas-hotfix.patch`
- Sidecar image digest: pending until the hotfix is published as a release image
- `sidecar.default.min_version`: pending; the local hotfix binary reports
  `Version: dev` and `Commit ID: unknown`
- `115share2cas` binary sha256: `6ec60458d24a31d89ebc4ed1d1f2bd2ac6921c48e46f7bc7d7a21cc9bed21acd`
- Observed real sidecar version string: `dev (Commit: unknown) - Frontend: rolling - Build at: unknown`

### Validation

- `go test ./... -count=1`: `PASS`
- `go vet ./...`: `PASS`
- `go test -tags=integration -race ./integration/... -count=1`: `PASS`
- `make build VERSION=v0.1.0`: `PASS`
- `make docker VERSION=v0.1.0`: `PASS` (`echo:dev` image id `sha256:a171e29ecfcef0aac86ea0fca85a35aef800abc7bd192c0b9cb2b79e916e4e6f`)
- Historical result with pinned `115share2cas@3324d78`,
  `ECHO_TEST_115_LIMIT=1`: `FAIL`
  (`sidecar api error: code=500 message=sig invalid: sig invalid`)
- Historical result with pre-generated source-aware 115 CAS sample, size
  `1837819460`: `FAIL`
  (`sidecar api error: code=500 message=sig invalid: sig invalid`)
- `TestReal115` with the local Task 14 hotfix sidecar binary: `PASS`, streamed
  `4096` bytes through Echo.
- Hotfix patch apply/check against `3324d78d9de9a060bc6830d0f4b2d012ea47576b`:
  `PASS` for `./drivers/115` and `./cmd/115share2cas`.
