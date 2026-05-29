package metrics_test

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/xmm2022/echo/internal/metrics"
)

// TestNewExposesPushMetrics asserts every push-style collector (counters +
// histograms + build_info) is registered and visible after the helpers fire.
// Together with TestStateCollectorGauges (the 3 pull gauges) this covers all 11
// metrics in the spec §8 inventory (+ build_info).
func TestNewExposesPushMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	m.IncJob("ingest_manual", "done")
	m.IncIngestItem("115", "restored")
	m.ObserveRestore("115", "json", "ok", 0.01)
	m.ObserveSidecarCall("default", "link", "ok", 0.02)
	m.IncProducerRun("115share2cas", "success")
	m.SetBuildInfo("v0.1.0-test", "feat-cas-tools@abc123")

	want := []string{
		"echo_jobs_total",
		"echo_ingest_items_total",
		"echo_restore_requests_total",
		"echo_restore_latency_seconds",
		"echo_sidecar_calls_total",
		"echo_sidecar_call_latency_seconds",
		"echo_producer_runs_total",
		"echo_build_info",
	}
	families := gather(t, reg)
	for _, name := range want {
		if _, ok := families[name]; !ok {
			t.Errorf("metric %q not exposed", name)
		}
	}
}

// TestNilMetricsHelpersAreNoops guarantees instrumentation call sites can hold a
// nil *Metrics (unwired) without panicking.
func TestNilMetricsHelpersAreNoops(t *testing.T) {
	var m *metrics.Metrics
	m.IncJob("ingest_manual", "done")
	m.IncIngestItem("115", "restored")
	m.ObserveRestore("115", "json", "ok", 0.01)
	m.ObserveSidecarCall("default", "link", "ok", 0.02)
	m.IncProducerRun("115share2cas", "success")
	m.SetBuildInfo("v", "s")
}

// TestStateCollectorGauges verifies the pull-based gauges sample db + store state
// at scrape time and map account status strings to the spec's numeric encoding.
func TestStateCollectorGauges(t *testing.T) {
	reg := prometheus.NewRegistry()
	src := &fakeState{
		accounts: []metrics.AccountStatus{
			{Provider: "115", AccountID: "acc1", Status: "ok"},
			{Provider: "139", AccountID: "acc2", Status: "banned"},
			{Provider: "189pc", AccountID: "acc3", Status: "weird"},
		},
		openConflicts: 3,
	}
	reg.MustRegister(metrics.NewStateCollector(fakeDBStats{open: 5}, src, nil))

	values := gaugeValues(t, reg)
	cases := map[string]float64{
		`echo_db_open_connections`:                                5,
		`echo_hash_conflicts_open`:                                3,
		`echo_account_status{account_id="acc1",provider="115"}`:   1,
		`echo_account_status{account_id="acc2",provider="139"}`:   -1,
		`echo_account_status{account_id="acc3",provider="189pc"}`: 2,
	}
	for key, want := range cases {
		got, ok := values[key]
		if !ok {
			t.Errorf("gauge %q not exposed", key)
			continue
		}
		if got != want {
			t.Errorf("gauge %q = %v, want %v", key, got, want)
		}
	}
}

func gather(t *testing.T, g prometheus.Gatherer) map[string]*dto.MetricFamily {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := make(map[string]*dto.MetricFamily, len(mfs))
	for _, mf := range mfs {
		out[mf.GetName()] = mf
	}
	return out
}

// gaugeValues flattens gathered gauges to a "name{k=v,...}" → value map.
func gaugeValues(t *testing.T, g prometheus.Gatherer) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for name, mf := range gather(t, g) {
		for _, metric := range mf.GetMetric() {
			out[name+labelKey(metric)] = metric.GetGauge().GetValue()
		}
	}
	return out
}

func labelKey(m *dto.Metric) string {
	if len(m.GetLabel()) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m.GetLabel()))
	for _, lp := range m.GetLabel() {
		parts = append(parts, fmt.Sprintf("%s=%q", lp.GetName(), lp.GetValue()))
	}
	sort.Strings(parts)
	return "{" + strings.Join(parts, ",") + "}"
}

type fakeDBStats struct{ open int }

func (f fakeDBStats) Stats() sql.DBStats { return sql.DBStats{OpenConnections: f.open} }

type fakeState struct {
	accounts      []metrics.AccountStatus
	openConflicts int64
}

func (f *fakeState) AccountStatuses(context.Context) ([]metrics.AccountStatus, error) {
	return f.accounts, nil
}

func (f *fakeState) OpenHashConflicts(context.Context) (int64, error) {
	return f.openConflicts, nil
}
