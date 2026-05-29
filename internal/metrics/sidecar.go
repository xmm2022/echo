package metrics

import (
	"context"
	"time"

	"github.com/xmm2022/echo/internal/sidecarclient"
)

// sidecarMethods enumerates the method labels recorded for sidecar calls (spec §8).
var sidecarMethods = []string{"ping", "version", "list_storages", "restore_from_cas", "link", "stream"}

// InstrumentSidecar wraps sc so each call records echo_sidecar_calls_total and
// echo_sidecar_call_latency_seconds under the given sidecar name. When m is nil
// it returns sc unchanged (no wrapping overhead for unwired callers).
func InstrumentSidecar(sc sidecarclient.Sidecar, m *Metrics, name string) sidecarclient.Sidecar {
	if m == nil {
		return sc
	}
	m.seedSidecar(name)
	return &instrumentedSidecar{inner: sc, m: m, name: name}
}

// seedSidecar zero-initialises the sidecar series so they appear on a cold scrape.
func (m *Metrics) seedSidecar(name string) {
	for _, method := range sidecarMethods {
		m.sidecarLatency.WithLabelValues(name, method)
		m.sidecarCalls.WithLabelValues(name, method, "ok")
		m.sidecarCalls.WithLabelValues(name, method, "error")
	}
}

type instrumentedSidecar struct {
	inner sidecarclient.Sidecar
	m     *Metrics
	name  string
}

func (s *instrumentedSidecar) observe(method string, start time.Time, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	s.m.ObserveSidecarCall(s.name, method, status, time.Since(start).Seconds())
}

func (s *instrumentedSidecar) Ping(ctx context.Context) error {
	start := time.Now()
	err := s.inner.Ping(ctx)
	s.observe("ping", start, err)
	return err
}

func (s *instrumentedSidecar) Version(ctx context.Context) (string, error) {
	start := time.Now()
	v, err := s.inner.Version(ctx)
	s.observe("version", start, err)
	return v, err
}

func (s *instrumentedSidecar) ListStorages(ctx context.Context) ([]sidecarclient.Storage, error) {
	start := time.Now()
	storages, err := s.inner.ListStorages(ctx)
	s.observe("list_storages", start, err)
	return storages, err
}

func (s *instrumentedSidecar) PutCAS(ctx context.Context, req sidecarclient.PutCASRequest) (*sidecarclient.ItemResult, error) {
	start := time.Now()
	result, err := s.inner.PutCAS(ctx, req)
	s.observe("restore_from_cas", start, err)
	return result, err
}

func (s *instrumentedSidecar) Link(ctx context.Context, storageMount, remotePath string) (*sidecarclient.DirectLink, error) {
	start := time.Now()
	link, err := s.inner.Link(ctx, storageMount, remotePath)
	s.observe("link", start, err)
	return link, err
}

func (s *instrumentedSidecar) Stream(ctx context.Context, req sidecarclient.StreamRequest) (*sidecarclient.StreamResult, error) {
	start := time.Now()
	result, err := s.inner.Stream(ctx, req)
	s.observe("stream", start, err)
	return result, err
}
