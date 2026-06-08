# Changelog

## Unreleased

### v0.5 browser auth foundation

- Added browser username/password login backed by existing Echo users.
- Added server-side web sessions, HttpOnly session cookies, and CSRF enforcement for cookie-auth mutations.
- Added bootstrap admin password setup while preserving DB-backed Bearer API tokens for automation.
- Added a full-path fake browser auth integration gate (`TestBrowserAuthFakeFullPath`) covering browser login/session, CSRF success and missing-token rejection, and media request creation.
- Removed browser localStorage token inputs from the admin and user shells.

### v0.4 media requests

- Added media request schema for access policies, policy targets, request
  ledger, user subscription links, request events, and user audit events.
- Added user-facing `/api/me/discovery/*` APIs and `/app` media request shell
  for TMDB search, catalog, request creation/cancelation, and redacted user
  subscription status.
- Added admin policy/target management and request approval/rejection under the
  existing admin-only `/api/discovery/*` boundary.
- Added canonical discovery subscription reuse across multiple users requesting
  the same media target, keeping user request creation separate from producer
  job dispatch.
- Added a fake full-path gate covering user request creation, admin approval,
  discovery subscription reuse, existing v0.3 match review, concurrent accept,
  one `ingest_producer` job, and redacted user projections.

### v0.3 discovery

- Added TMDB-backed subscriptions, Telegram/poster/manual discovery sources,
  deterministic rule scoring, admin candidate/match review, discovery scheduler
  jobs, and 115-only dispatch into the existing `ingest_producer` pipeline.
- Added admin JSON/UI surfaces under `/api/discovery/*` and `/ui/discovery/*`.
- Added fake full-path integration coverage, an optional environment-gated
  Telegram MTProto operator gate, and real 115 discovery/ingest validation.
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
The hotfix is now available as a real git ref. The current release does not
block on Telegram login or MTProto session authorization; Telegram real
discovery remains an optional operator gate.

### Reproducibility tuple

- Echo version: `v0.1.0`
- Echo source commit: release tag target (`v0.1.0`)
- Echo Docker build arg: `ECHO_VERSION=v0.1.0`
- Echo Docker image tag: `echo:dev`
- Echo Docker image id/local digest:
  `sha256:f0eac711db15e5114a057bd5f2d60cdf524e26399ed97b0ff8a3037a7dcf9dfd`
- Sidecar tools source: `github.com/xmm2022/openlist-guangyapan-src`
- Sidecar tools baseline ref: `3324d78d9de9a060bc6830d0f4b2d012ea47576b`
- Sidecar tools release ref: `814736c203e2115bb2dfda597f625c676a5cda74`
- Sidecar tools release tag: `echo-115cas-hotfix-20260604`
- Echo Docker build arg: `SIDECAR_TOOLS_REF=814736c203e2115bb2dfda597f625c676a5cda74`
- Sidecar runtime image tag: not published by this Echo release; operators can
  build a runtime sidecar from `echo-115cas-hotfix-20260604`
- Local hotfix sidecar binary: `/home/nax/.cache/openlist-115cas/openlist-guangyapan`
- Local hotfix sidecar binary sha256: `a827c1f52cdc88a2ba3dedb740d5565b569aef170f3f77d3bd47b068403e6445`
- Local hotfix patch:
  `docs/superpowers/release-gates/2026-06-04-openlist-115cas-hotfix.patch`
- Sidecar runtime image digest: not published by this Echo release; the local
  real gate used the hotfix binary recorded below
- `sidecar.default.min_version`: remains empty in the default config for this
  release because the current local hotfix runtime sidecar reports `Version:
  dev` and `Commit ID: unknown`. Operators who build the runtime sidecar with
  `build.sh release` from `echo-115cas-hotfix-20260604` can pin the exact
  reported version in their deployment config.
- `115share2cas` binary sha256: `6ec60458d24a31d89ebc4ed1d1f2bd2ac6921c48e46f7bc7d7a21cc9bed21acd`
- Observed real sidecar version string: `dev (Commit: unknown) - Frontend: rolling - Build at: unknown`

### Validation

- `go test ./... -count=1`: `PASS`
- `go vet ./...`: `PASS`
- `go test -tags=integration -race ./integration/... -count=1`: `PASS`
- `make build VERSION=v0.1.0`: `PASS`
- `make docker VERSION=v0.1.0`: `PASS` (`echo:dev` image id/local digest
  `sha256:f0eac711db15e5114a057bd5f2d60cdf524e26399ed97b0ff8a3037a7dcf9dfd`)
- Historical result with pinned `115share2cas@3324d78`,
  `ECHO_TEST_115_LIMIT=1`: `FAIL`
  (`sidecar api error: code=500 message=sig invalid: sig invalid`)
- Historical result with pre-generated source-aware 115 CAS sample, size
  `1837819460`: `FAIL`
  (`sidecar api error: code=500 message=sig invalid: sig invalid`)
- `TestReal115` with the local Task 14 hotfix sidecar binary: `PASS`, streamed
  `4096` bytes through Echo.
- `TestReal115` rerun on 2026-06-04 with the local hotfix sidecar served by
  transient unit `echo-openlist-hotfix.service`: `PASS`, streamed `4096` bytes
  through Echo.
- `TestDiscoveryRealTelegram115Gate` attempted on 2026-06-04 with real gate env:
  `FAIL` / optional-deferred at Telegram crawl because the configured session
  ref exists but is not authorized (`session is not authorized`). This gate is
  not release-blocking for the current tag.
- Hotfix patch apply/check against `3324d78d9de9a060bc6830d0f4b2d012ea47576b`:
  `PASS` for `./drivers/115` and `./cmd/115share2cas`.
- Hotfix published to `github.com/xmm2022/openlist-guangyapan-src` as branch
  `echo/115cas-hotfix-20260604`, tag `echo-115cas-hotfix-20260604`, commit
  `814736c203e2115bb2dfda597f625c676a5cda74`.
