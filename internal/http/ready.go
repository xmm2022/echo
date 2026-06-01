package httpserver

import (
	"context"
	"net/http"
	"time"
)

// defaultReadyTimeout bounds the readiness probes so /readyz cannot hang on a
// slow database or sidecar.
const defaultReadyTimeout = 5 * time.Second

// Pinger is the database readiness probe; *sql.DB satisfies it.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// SidecarHealth is the sidecar readiness surface: reachability (Ping) plus
// version compatibility (Version returns ErrSidecarVersionTooOld on mismatch).
type SidecarHealth interface {
	Ping(ctx context.Context) error
	Version(ctx context.Context) (string, error)
}

// ReadyConfig flags which v0.2 dependencies are hard (block readiness) vs soft
// (degrade only). MappedOnlyPlayback promotes both sidecar dependencies to hard
// because mapped-only playback cannot serve anything without the sidecar.
type ReadyConfig struct {
	RequireSidecarContract     bool
	RequireSidecarConnectivity bool
	RequireEmbyConnectivity    bool
	MappedOnlyPlayback         bool
}

// ProbeResult is one v0.2 dependency probe outcome. Status is one of ok, unknown,
// unreachable, incompatible.
type ProbeResult struct {
	Status string
	Detail string
}

// Probe is a v0.2 dependency readiness probe.
type Probe interface {
	Check(ctx context.Context) ProbeResult
}

// ReadyDeps wires the v0.2 config-driven readiness checker: a DB pinger, the
// sidecar contract + Emby connectivity probes, and the soft/hard config.
type ReadyDeps struct {
	DB              Pinger
	SidecarContract Probe
	Emby            Probe
	Config          ReadyConfig
}

// ReadyChecker backs GET /readyz. It runs in one of two modes:
//
//   - Legacy (spec §8): DB ping + sidecar ping + sidecar version compatibility,
//     every check hard (any failure => 503). Used by cmd/echo/main.go and the
//     existing readyz / integration tests. Populate DB/Sidecar/Version directly.
//   - v0.2 config-driven: DB ping + sidecar contract probe + Emby probe with
//     soft/hard rules from ReadyConfig. Constructed via NewReadyChecker.
//
// The handler branches on whether the v0.2 probes were populated (usesV02).
type ReadyChecker struct {
	DB      Pinger
	Sidecar SidecarHealth
	Version string
	Timeout time.Duration

	// v0.2 config-driven fields (set only via NewReadyChecker).
	sidecarContract Probe
	emby            Probe
	config          ReadyConfig
}

// NewReadyChecker builds a v0.2 config-driven readiness checker. The returned
// checker's handler applies the soft/hard dependency rules from deps.Config.
func NewReadyChecker(deps ReadyDeps) *ReadyChecker {
	return &ReadyChecker{
		DB:              deps.DB,
		sidecarContract: deps.SidecarContract,
		emby:            deps.Emby,
		config:          deps.Config,
	}
}

// usesV02 reports whether this checker was built for the v0.2 config-driven path.
func (rc *ReadyChecker) usesV02() bool {
	return rc.sidecarContract != nil || rc.emby != nil
}

func (rc *ReadyChecker) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		timeout := rc.Timeout
		if timeout <= 0 {
			timeout = defaultReadyTimeout
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		if rc.usesV02() {
			rc.serveV02(ctx, w)
			return
		}
		rc.serveLegacy(ctx, w)
	}
}

// serveLegacy is the v0.1 all-hard readiness path (spec §8). Its checks-map keys
// (db / sidecar / sidecar_version) and 503-on-any-failure behavior are relied on by
// existing unit + integration tests and MUST NOT change.
func (rc *ReadyChecker) serveLegacy(ctx context.Context, w http.ResponseWriter) {
	checks := make(map[string]string, 3)
	ready := true

	if err := rc.DB.PingContext(ctx); err != nil {
		checks["db"] = err.Error()
		ready = false
	} else {
		checks["db"] = "ok"
	}

	if err := rc.Sidecar.Ping(ctx); err != nil {
		checks["sidecar"] = err.Error()
		ready = false
	} else {
		checks["sidecar"] = "ok"
	}

	if version, err := rc.Sidecar.Version(ctx); err != nil {
		checks["sidecar_version"] = err.Error()
		ready = false
	} else {
		checks["sidecar_version"] = version
	}

	status := "ok"
	code := http.StatusOK
	if !ready {
		status = "not_ready"
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{
		"status":  status,
		"version": rc.Version,
		"checks":  checks,
	})
}

// serveV02 applies the v0.2 soft/hard dependency rules:
//   - DB failure is always hard (not_ready, 503).
//   - Sidecar contract "incompatible" is always hard.
//   - Sidecar contract "unknown" is hard only when RequireSidecarContract or
//     MappedOnlyPlayback.
//   - Sidecar connectivity "unreachable" is hard only when
//     RequireSidecarConnectivity or MappedOnlyPlayback.
//   - Emby "unreachable" is hard only when RequireEmbyConnectivity.
//   - Any non-hard, non-ok dependency degrades (200, "degraded").
func (rc *ReadyChecker) serveV02(ctx context.Context, w http.ResponseWriter) {
	checks := make(map[string]string, 3)
	hard := false
	degraded := false

	// DB: always hard.
	if err := rc.DB.PingContext(ctx); err != nil {
		checks["db"] = err.Error()
		hard = true
	} else {
		checks["db"] = "ok"
	}

	// Sidecar contract.
	if rc.sidecarContract != nil {
		res := rc.sidecarContract.Check(ctx)
		checks["sidecar_contract"] = res.Status
		switch res.Status {
		case "ok":
		case "incompatible":
			hard = true
		default: // unknown / unreachable / anything non-ok
			if rc.config.RequireSidecarContract || rc.config.MappedOnlyPlayback {
				hard = true
			} else {
				degraded = true
			}
		}
		// Sidecar connectivity is read from the same probe's status when it
		// reports unreachable (no separate connectivity probe in v0.2 deps).
		if res.Status == "unreachable" && (rc.config.RequireSidecarConnectivity || rc.config.MappedOnlyPlayback) {
			hard = true
		}
	}

	// Emby connectivity.
	if rc.emby != nil {
		res := rc.emby.Check(ctx)
		checks["emby"] = res.Status
		if res.Status != "ok" {
			if rc.config.RequireEmbyConnectivity {
				hard = true
			} else {
				degraded = true
			}
		}
	}

	status := "ok"
	code := http.StatusOK
	switch {
	case hard:
		status = "not_ready"
		code = http.StatusServiceUnavailable
	case degraded:
		status = "degraded"
	}
	writeJSON(w, code, map[string]any{
		"status":  status,
		"version": rc.Version,
		"checks":  checks,
	})
}
