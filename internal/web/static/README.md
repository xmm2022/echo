# Vendored static assets

These files are vendored (committed to the repo), not loaded from a CDN — Echo
serves them itself so the admin UI works in an isolated/air-gapped deployment and
its behavior is pinned (spec §12 / plan Phase 9).

| File | Version | Source |
|---|---|---|
| `htmx.min.js` | htmx 2.0.4 | https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js |
| `app.js` | — (first-party) | Echo dashboard glue: injects the admin bearer token into htmx requests |

To upgrade htmx, replace `htmx.min.js` with the new release's `dist/htmx.min.js`
and update the version + URL above.
