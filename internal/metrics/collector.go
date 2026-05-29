package metrics

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// scrapeTimeout bounds the store queries a scrape triggers so /metrics never
// hangs on a slow database.
const scrapeTimeout = 5 * time.Second

// AccountStatus is the minimal account view the gauge collector samples.
type AccountStatus struct {
	Provider  string
	AccountID string
	Status    string
}

// DBStatsSource exposes connection-pool statistics; *sql.DB satisfies it.
type DBStatsSource interface {
	Stats() sql.DBStats
}

// StateSource is the read side the collector samples at scrape time.
type StateSource interface {
	AccountStatuses(ctx context.Context) ([]AccountStatus, error)
	OpenHashConflicts(ctx context.Context) (int64, error)
}

// StateCollector emits the three pull-style gauges (spec §8) by sampling the DB
// pool and the store at scrape time, avoiding instrumentation of every account
// or conflict mutation path.
type StateCollector struct {
	db     DBStatsSource
	src    StateSource
	logger *slog.Logger

	dbConns       *prometheus.Desc
	accountStatus *prometheus.Desc
	conflictsOpen *prometheus.Desc
}

// NewStateCollector builds the gauge collector. logger may be nil (errors during
// a scrape are then silently skipped rather than logged).
func NewStateCollector(db DBStatsSource, src StateSource, logger *slog.Logger) *StateCollector {
	return &StateCollector{
		db:     db,
		src:    src,
		logger: logger,
		dbConns: prometheus.NewDesc(
			"echo_db_open_connections",
			"Open database connections.",
			nil, nil,
		),
		accountStatus: prometheus.NewDesc(
			"echo_account_status",
			"Account health (ok=1 expired=0 banned=-1 unknown=2).",
			[]string{"provider", "account_id"}, nil,
		),
		conflictsOpen: prometheus.NewDesc(
			"echo_hash_conflicts_open",
			"Open (unresolved) hash conflicts.",
			nil, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *StateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.dbConns
	ch <- c.accountStatus
	ch <- c.conflictsOpen
}

// Collect implements prometheus.Collector. A failing store query skips only its
// own gauge so the scrape still reports the others.
func (c *StateCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(c.dbConns, prometheus.GaugeValue, float64(c.db.Stats().OpenConnections))

	ctx, cancel := context.WithTimeout(context.Background(), scrapeTimeout)
	defer cancel()

	if accounts, err := c.src.AccountStatuses(ctx); err != nil {
		c.warn("collect account statuses", err)
	} else {
		for _, a := range accounts {
			ch <- prometheus.MustNewConstMetric(c.accountStatus, prometheus.GaugeValue,
				accountStatusValue(a.Status), a.Provider, a.AccountID)
		}
	}

	if n, err := c.src.OpenHashConflicts(ctx); err != nil {
		c.warn("collect open hash conflicts", err)
	} else {
		ch <- prometheus.MustNewConstMetric(c.conflictsOpen, prometheus.GaugeValue, float64(n))
	}
}

func (c *StateCollector) warn(msg string, err error) {
	if c.logger != nil {
		c.logger.Warn("metrics: "+msg, "err", err)
	}
}

// accountStatusValue maps a status string to spec §8's numeric encoding.
func accountStatusValue(status string) float64 {
	switch status {
	case "ok":
		return 1
	case "expired":
		return 0
	case "banned":
		return -1
	default:
		return 2 // unknown
	}
}
