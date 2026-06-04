# Echo Release Closeout Record

Date: 2026-06-04

## Current State

- Echo main branch is locally green through the standard verification commands.
- v0.3 discovery is implemented and documented.
- The real 115 gate was rerun successfully on 2026-06-04.
- The Telegram MTProto discovery gate is optional/deferred for this release: the
  configured Telegram session ref exists but is not authorized.
- The previous 115 `sig invalid` blocker is resolved by a published OpenList
  sidecar hotfix git ref. A local hotfix sidecar binary remains the runtime
  artifact used by the recorded real gate.

## Sidecar Hotfix Reproducibility

Baseline sidecar source:

- Repository: `github.com/xmm2022/openlist-guangyapan-src`
- Baseline commit: `3324d78d9de9a060bc6830d0f4b2d012ea47576b`

Patch artifact:

- `docs/superpowers/release-gates/2026-06-04-openlist-115cas-hotfix.patch`

Published hotfix ref:

- Branch: `echo/115cas-hotfix-20260604`
- Tag: `echo-115cas-hotfix-20260604`
- Commit: `814736c203e2115bb2dfda597f625c676a5cda74`

Patch check performed on 2026-06-04:

```bash
git clone https://github.com/xmm2022/openlist-guangyapan-src.git /tmp/openlist-guangyapan-src-3324d78
git -C /tmp/openlist-guangyapan-src-3324d78 checkout 3324d78d9de9a060bc6830d0f4b2d012ea47576b
cp -a /tmp/openlist-guangyapan-src-3324d78 /tmp/openlist-guangyapan-src-3324d78-patchcheck
patch -d /tmp/openlist-guangyapan-src-3324d78-patchcheck -p1 < docs/superpowers/release-gates/2026-06-04-openlist-115cas-hotfix.patch
GOTMPDIR=/home/nax/.cache/echo-gotmp go test -count=1 ./drivers/115 ./cmd/115share2cas
```

Result:

- Patch applied cleanly.
- `github.com/OpenListTeam/OpenList/v4/drivers/115` passed.
- `github.com/OpenListTeam/OpenList/v4/cmd/115share2cas` passed.

## Completed Release Blockers

- Published the OpenList 115 hotfix as a real git commit/tag.
- Updated `docker/Dockerfile` `SIDECAR_TOOLS_REF` from `3324d78` to
  `814736c203e2115bb2dfda597f625c676a5cda74`.
- Built the Echo release Docker image with `make docker VERSION=v0.1.0`.
  Resulting `echo:dev` image id/local digest:
  `sha256:f0eac711db15e5114a057bd5f2d60cdf524e26399ed97b0ff8a3037a7dcf9dfd`.
- Reran `TestReal115` with the local hotfix sidecar served by transient unit
  `echo-openlist-hotfix.service`: passed and streamed `4096` bytes through Echo.
- Accepted the existing v0.2 real Emby manual gate as sufficient for this tag:
  no Emby proxy code or deployment contract changed during this closeout, and
  local Emby proxy package coverage remains part of `go test ./...`.
- Demoted the Telegram MTProto discovery gate to optional/deferred for this
  release. The release still covers discovery through fake full-path integration
  and covers 115 ingest/stream through the real 115 gate.
- Kept `sidecar.default.min_version` empty in the default config for this
  release because the current local hotfix runtime sidecar reports `Version:
  dev` and `Commit ID: unknown`. Exact version gating can be enabled by
  operators who build the runtime sidecar with `build.sh release` from
  `echo-115cas-hotfix-20260604`.

## Remaining Release Blockers

Before cutting a final release tag:

- Ensure `git status --short` is clean for release-owned files before the
  release commit/tag.

## Deferred

- `TestDiscoveryRealTelegram115Gate` remains optional/deferred until the real
  discovery Telegram session ref in
  `docs/superpowers/release-gates/2026-06-02-echo-v0.3-discovery.md` is
  authorized. The 2026-06-04 attempt reached Telegram and failed with
  `session is not authorized`; this does not block the current tag.
