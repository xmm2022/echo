# Echo Release Closeout Record

Date: 2026-06-04

## Current State

- Echo main branch is locally green through the standard verification commands.
- v0.3 discovery is implemented and documented.
- The real discovery gate is intentionally deferred until Telegram/TMDB/115 gate
  credentials are configured.
- The previous 115 `sig invalid` blocker is resolved only by a local OpenList
  sidecar hotfix binary for now.

## Sidecar Hotfix Reproducibility

Baseline sidecar source:

- Repository: `github.com/xmm2022/openlist-guangyapan-src`
- Baseline commit: `3324d78d9de9a060bc6830d0f4b2d012ea47576b`

Patch artifact:

- `docs/superpowers/release-gates/2026-06-04-openlist-115cas-hotfix.patch`

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

## Release Blockers

Before cutting a final release tag:

- Upstream or publish the OpenList 115 hotfix as a real git commit/tag.
- Update `docker/Dockerfile` `SIDECAR_TOOLS_REF` from `3324d78` to that
  commit/tag.
- Build the release Docker image and record the image id/digest.
- Decide whether `sidecar.default.min_version` should remain empty for the
  current `Version: dev / Commit ID: unknown` sidecar, or whether the sidecar
  build must be fixed to report an exact version string.
- Run the final real gates that are in scope for the tag.

## Deferred

- `TestDiscoveryRealTelegram115Gate` remains deferred until the real discovery
  environment variables in
  `docs/superpowers/release-gates/2026-06-02-echo-v0.3-discovery.md` are
  configured.
