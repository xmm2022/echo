// Package metrics defines Echo's Prometheus collectors (spec §8) and nil-safe
// instrumentation helpers.
//
// Push-style counters and histograms live on *Metrics; a nil *Metrics is a
// no-op so call sites in unwired tests need not guard every observation. The
// three pull-style gauges (open connections / account status / open hash
// conflicts) are sampled at scrape time by StateCollector.
//
// New zero-initialises the closed-label-set series (jobs, producer runs) so they
// appear on a cold /metrics scrape and rate() sees no gaps. Provider-keyed series
// (ingest items, restore requests/latency) are data-driven and appear on first
// request; the sidecar series are seeded by InstrumentSidecar.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// jobKinds and jobStatuses enumerate the closed label space for echo_jobs_total.
var (
	jobKinds      = []string{"ingest_manual", "ingest_producer"}
	jobStatuses   = []string{"done", "failed"}
	producerTools = []string{"115share2cas"}
	// producerResults mirrors spec §8: success / exit_failed / timeout / unauthorized.
	producerResults = []string{"success", "exit_failed", "timeout", "unauthorized"}
)

// Metrics holds Echo's push-style collectors. Construct with New.
type Metrics struct {
	jobs            *prometheus.CounterVec
	ingestItems     *prometheus.CounterVec
	restoreRequests *prometheus.CounterVec
	restoreLatency  *prometheus.HistogramVec
	sidecarCalls    *prometheus.CounterVec
	sidecarLatency  *prometheus.HistogramVec
	producerRuns    *prometheus.CounterVec
	buildInfo       *prometheus.GaugeVec

	// v0.2 Emby reverse-proxy + playback observability (spec §8). Labels are
	// route templates and safe enums ONLY: never a token, item id, raw path,
	// raw user id, or raw error text.
	embyProxyRequests      *prometheus.CounterVec
	playbackInfoRewrite    *prometheus.CounterVec
	playbackSessionsActive prometheus.Gauge
	playbackStreamBytes    *prometheus.CounterVec
	copyFailures           *prometheus.CounterVec
	accountCooldown        *prometheus.GaugeVec
	quotaDenied            *prometheus.CounterVec
}

// New registers every push collector on reg and returns the handle. It panics on
// duplicate registration (promauto semantics): build exactly one per registry.
func New(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)
	m := &Metrics{
		jobs: f.NewCounterVec(prometheus.CounterOpts{
			Name: "echo_jobs_total",
			Help: "Ingest jobs by kind and terminal status.",
		}, []string{"kind", "status"}),
		ingestItems: f.NewCounterVec(prometheus.CounterOpts{
			Name: "echo_ingest_items_total",
			Help: "Ingest items by provider and result (restored/skipped_dup/failed/parse_error).",
		}, []string{"provider", "result"}),
		restoreRequests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "echo_restore_requests_total",
			Help: "Restore requests by provider, endpoint (json/stream) and result.",
		}, []string{"provider", "endpoint", "result"}),
		restoreLatency: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "echo_restore_latency_seconds",
			Help:    "Restore request latency by provider and endpoint.",
			Buckets: prometheus.DefBuckets,
		}, []string{"provider", "endpoint"}),
		sidecarCalls: f.NewCounterVec(prometheus.CounterOpts{
			Name: "echo_sidecar_calls_total",
			Help: "Sidecar calls by sidecar, method and status (ok/error).",
		}, []string{"sidecar", "method", "status"}),
		sidecarLatency: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "echo_sidecar_call_latency_seconds",
			Help:    "Sidecar call latency by sidecar and method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"sidecar", "method"}),
		producerRuns: f.NewCounterVec(prometheus.CounterOpts{
			Name: "echo_producer_runs_total",
			Help: "Producer tool runs by tool and result (success/exit_failed/timeout/unauthorized).",
		}, []string{"tool", "result"}),
		buildInfo: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "echo_build_info",
			Help: "Echo build info; constant 1 labelled by version and configured sidecar min_version.",
		}, []string{"version", "sidecar_min_version"}),
		embyProxyRequests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "echo_emby_proxy_requests_total",
			Help: "Emby reverse-proxy requests by route template and safe result enum.",
		}, []string{"route_template", "result"}),
		playbackInfoRewrite: f.NewCounterVec(prometheus.CounterOpts{
			Name: "echo_playbackinfo_rewrite_total",
			Help: "PlaybackInfo rewrite outcomes by safe result enum (rewritten/error_url/passthrough/fail_closed).",
		}, []string{"result"}),
		playbackSessionsActive: f.NewGauge(prometheus.GaugeOpts{
			Name: "echo_playback_sessions_active",
			Help: "Active playback stream leases. Pushed (inc/dec) per accepted GET stream and corrected to the DB count at startup.",
		}),
		playbackStreamBytes: f.NewCounterVec(prometheus.CounterOpts{
			Name: "echo_playback_stream_bytes_total",
			Help: "Bytes streamed to clients by provider and safe result enum.",
		}, []string{"provider", "result"}),
		copyFailures: f.NewCounterVec(prometheus.CounterOpts{
			Name: "echo_copy_failures_total",
			Help: "File-copy failures by safe reason, confidence (confirmed/suspect) and provider.",
		}, []string{"reason", "confidence", "provider"}),
		accountCooldown: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "echo_account_cooldown",
			Help: "Account cooldown state by provider and safe reason: 1 = currently in cooldown, 0 = cleared.",
		}, []string{"provider", "reason"}),
		quotaDenied: f.NewCounterVec(prometheus.CounterOpts{
			Name: "echo_quota_denied_total",
			Help: "Playback requests denied by quota, by safe reason enum (streams/bytes).",
		}, []string{"reason"}),
	}
	m.preInit()
	return m
}

// preInit creates zero-valued children for the closed label sets.
func (m *Metrics) preInit() {
	for _, kind := range jobKinds {
		for _, status := range jobStatuses {
			m.jobs.WithLabelValues(kind, status)
		}
	}
	for _, tool := range producerTools {
		for _, result := range producerResults {
			m.producerRuns.WithLabelValues(tool, result)
		}
	}
}

// IncJob records a job reaching a terminal status.
func (m *Metrics) IncJob(kind, status string) {
	if m == nil {
		return
	}
	m.jobs.WithLabelValues(kind, status).Inc()
}

// IncIngestItem records one ingest item outcome.
func (m *Metrics) IncIngestItem(provider, result string) {
	if m == nil {
		return
	}
	m.ingestItems.WithLabelValues(provider, result).Inc()
}

// ObserveRestore records a restore request's result and latency.
func (m *Metrics) ObserveRestore(provider, endpoint, result string, seconds float64) {
	if m == nil {
		return
	}
	m.restoreRequests.WithLabelValues(provider, endpoint, result).Inc()
	m.restoreLatency.WithLabelValues(provider, endpoint).Observe(seconds)
}

// ObserveSidecarCall records a sidecar call's status and latency.
func (m *Metrics) ObserveSidecarCall(sidecar, method, status string, seconds float64) {
	if m == nil {
		return
	}
	m.sidecarCalls.WithLabelValues(sidecar, method, status).Inc()
	m.sidecarLatency.WithLabelValues(sidecar, method).Observe(seconds)
}

// IncProducerRun records a producer tool run outcome.
func (m *Metrics) IncProducerRun(tool, result string) {
	if m == nil {
		return
	}
	m.producerRuns.WithLabelValues(tool, result).Inc()
}

// SetBuildInfo publishes the build version + configured sidecar min_version as a
// constant-1 gauge. Call once at startup.
func (m *Metrics) SetBuildInfo(version, sidecarMinVersion string) {
	if m == nil {
		return
	}
	m.buildInfo.WithLabelValues(version, sidecarMinVersion).Set(1)
}

// EmbyProxyRequest records one Emby reverse-proxy request by route template and
// safe result enum. routeTemplate must be a low-cardinality template (e.g.
// "/emby/stream/{token}"), never a concrete path or token.
func (m *Metrics) EmbyProxyRequest(routeTemplate, result string) {
	if m == nil {
		return
	}
	m.embyProxyRequests.WithLabelValues(routeTemplate, result).Inc()
}

// PlaybackInfoRewrite records one PlaybackInfo rewrite outcome by safe result enum.
func (m *Metrics) PlaybackInfoRewrite(result string) {
	if m == nil {
		return
	}
	m.playbackInfoRewrite.WithLabelValues(result).Inc()
}

// PlaybackSessionsActive sets the active-lease gauge to an authoritative count.
// Used at startup to correct the gauge to the DB's active-session count. Negative
// counts are ignored.
func (m *Metrics) PlaybackSessionsActive(n int64) {
	if m == nil || n < 0 {
		return
	}
	m.playbackSessionsActive.Set(float64(n))
}

// PlaybackSessionStarted increments the active-lease gauge when an accepted GET
// stream lease begins.
func (m *Metrics) PlaybackSessionStarted() {
	if m == nil {
		return
	}
	m.playbackSessionsActive.Inc()
}

// PlaybackSessionEnded decrements the active-lease gauge when a stream lease ends.
func (m *Metrics) PlaybackSessionEnded() {
	if m == nil {
		return
	}
	m.playbackSessionsActive.Dec()
}

// PlaybackStreamBytes adds n bytes streamed to a client, keyed by provider and safe
// result enum. Non-positive byte counts are ignored.
func (m *Metrics) PlaybackStreamBytes(provider, result string, n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.playbackStreamBytes.WithLabelValues(provider, result).Add(float64(n))
}

// CopyFailure records one file-copy failure by safe reason, confidence
// (confirmed/suspect), and provider.
func (m *Metrics) CopyFailure(reason, confidence, provider string) {
	if m == nil {
		return
	}
	m.copyFailures.WithLabelValues(reason, confidence, provider).Inc()
}

// AccountCooldown marks an account (by provider + safe reason) as currently in
// cooldown (Set 1). Pass the matching clear via a sibling helper if/when cooldown
// is lifted; the gauge convention is 1 = in cooldown, 0 = cleared.
func (m *Metrics) AccountCooldown(provider, reason string) {
	if m == nil {
		return
	}
	m.accountCooldown.WithLabelValues(provider, reason).Set(1)
}

// QuotaDenied records one playback request denied by quota, by safe reason enum.
func (m *Metrics) QuotaDenied(reason string) {
	if m == nil {
		return
	}
	m.quotaDenied.WithLabelValues(reason).Inc()
}
