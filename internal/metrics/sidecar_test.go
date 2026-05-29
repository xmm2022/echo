package metrics_test

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/xmm2022/echo/internal/metrics"
	"github.com/xmm2022/echo/internal/sidecarclient"
)

func TestInstrumentSidecarRecordsCallsAndLatency(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	fake := &countingSidecar{linkErr: errors.New("boom")}

	sc := metrics.InstrumentSidecar(fake, m, "default")
	if err := sc.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, err := sc.Link(context.Background(), "mount", "p"); err == nil {
		t.Fatal("expected link error to propagate")
	}

	counts := counterValues(t, reg)
	if got := counts[`echo_sidecar_calls_total{method="ping",sidecar="default",status="ok"}`]; got != 1 {
		t.Errorf("ping ok counter = %v, want 1", got)
	}
	if got := counts[`echo_sidecar_calls_total{method="link",sidecar="default",status="error"}`]; got != 1 {
		t.Errorf("link error counter = %v, want 1", got)
	}
	if got := histogramCount(t, reg, "echo_sidecar_call_latency_seconds",
		map[string]string{"sidecar": "default", "method": "ping"}); got != 1 {
		t.Errorf("ping latency sample count = %v, want 1", got)
	}
}

func TestInstrumentSidecarNilMetricsReturnsInner(t *testing.T) {
	fake := &countingSidecar{}
	sc := metrics.InstrumentSidecar(fake, nil, "default")
	if _, ok := sc.(*countingSidecar); !ok {
		t.Fatal("nil metrics should return the inner sidecar unchanged")
	}
}

func counterValues(t *testing.T, g prometheus.Gatherer) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for name, mf := range gather(t, g) {
		for _, metric := range mf.GetMetric() {
			out[name+labelKey(metric)] = metric.GetCounter().GetValue()
		}
	}
	return out
}

func histogramCount(t *testing.T, g prometheus.Gatherer, name string, labels map[string]string) uint64 {
	t.Helper()
	mf, ok := gather(t, g)[name]
	if !ok {
		return 0
	}
	for _, metric := range mf.GetMetric() {
		if labelsMatch(metric, labels) {
			return metric.GetHistogram().GetSampleCount()
		}
	}
	return 0
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(m.GetLabel()))
	for _, lp := range m.GetLabel() {
		got[lp.GetName()] = lp.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

// countingSidecar is a no-op sidecarclient.Sidecar whose Link can be forced to err.
type countingSidecar struct {
	linkErr error
}

func (c *countingSidecar) Ping(context.Context) error              { return nil }
func (c *countingSidecar) Version(context.Context) (string, error) { return "v", nil }
func (c *countingSidecar) ListStorages(context.Context) ([]sidecarclient.Storage, error) {
	return nil, nil
}
func (c *countingSidecar) PutCAS(context.Context, sidecarclient.PutCASRequest) (*sidecarclient.ItemResult, error) {
	return &sidecarclient.ItemResult{}, nil
}
func (c *countingSidecar) Link(context.Context, string, string) (*sidecarclient.DirectLink, error) {
	if c.linkErr != nil {
		return nil, c.linkErr
	}
	return &sidecarclient.DirectLink{}, nil
}
func (c *countingSidecar) Stream(context.Context, sidecarclient.StreamRequest) (*sidecarclient.StreamResult, error) {
	return &sidecarclient.StreamResult{}, nil
}
