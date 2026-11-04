package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/yiivgeny/wireproxy-pool/internal/pool"
)

type Metrics struct {
	provider *pool.Pool

	backendUp                  *prometheus.Desc
	backendActive              *prometheus.Desc
	lastCheckTimestamp         *prometheus.Desc
	lastCheckSuccess           *prometheus.Desc
	lastCheckDuration          *prometheus.Desc
	backendConsecutiveFailures *prometheus.Desc

	failovers  *prometheus.CounterVec
	dialErrors *prometheus.CounterVec
}

func New(registerer prometheus.Registerer, provider *pool.Pool) (*Metrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("metrics registerer is nil")
	}
	if provider == nil {
		return nil, fmt.Errorf("metrics pool is nil")
	}

	labels := []string{"backend", "config"}
	m := &Metrics{
		provider: provider,
		backendUp: prometheus.NewDesc(
			"wireproxy_backend_up",
			"Whether the backend is currently considered available (1) or unavailable (0).",
			labels, nil,
		),
		backendActive: prometheus.NewDesc(
			"wireproxy_backend_active",
			"Whether the backend is the first currently available backend (1) or not (0).",
			labels, nil,
		),
		lastCheckTimestamp: prometheus.NewDesc(
			"wireproxy_backend_last_check_timestamp_seconds",
			"Unix timestamp of the latest completed backend health check.",
			labels, nil,
		),
		lastCheckSuccess: prometheus.NewDesc(
			"wireproxy_backend_last_check_success",
			"Whether the latest backend health check succeeded (1) or failed (0).",
			labels, nil,
		),
		lastCheckDuration: prometheus.NewDesc(
			"wireproxy_backend_last_check_duration_seconds",
			"Duration of the latest backend health check in seconds.",
			labels, nil,
		),
		backendConsecutiveFailures: prometheus.NewDesc(
			"wireproxy_backend_consecutive_failures",
			"Number of consecutive failed health checks for the backend.",
			labels, nil,
		),
		failovers: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "wireproxy_backend_failovers_total",
				Help: "Number of request attempts moved from one backend to another.",
			},
			[]string{"from_backend", "to_backend"},
		),
		dialErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "wireproxy_backend_dial_errors_total",
				Help: "Number of request-path errors by backend and stage.",
			},
			[]string{"backend", "stage"},
		),
	}

	if err := registerer.Register(m); err != nil {
		return nil, fmt.Errorf("register backend metrics: %w", err)
	}
	if err := registerer.Register(m.failovers); err != nil {
		return nil, fmt.Errorf("register failover metric: %w", err)
	}
	if err := registerer.Register(m.dialErrors); err != nil {
		return nil, fmt.Errorf("register dial error metric: %w", err)
	}
	return m, nil
}

func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- m.backendUp
	ch <- m.backendActive
	ch <- m.lastCheckTimestamp
	ch <- m.lastCheckSuccess
	ch <- m.lastCheckDuration
	ch <- m.backendConsecutiveFailures
}

func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	for _, snapshot := range m.provider.Snapshots() {
		labels := []string{snapshot.BackendID, snapshot.ConfigPath}
		lastCheckTimestamp := 0.0
		if snapshot.Checked {
			lastCheckTimestamp = float64(snapshot.LastCheck.UnixNano()) / 1e9
		}
		ch <- prometheus.MustNewConstMetric(m.backendUp, prometheus.GaugeValue, boolFloat(snapshot.Up), labels...)
		ch <- prometheus.MustNewConstMetric(m.backendActive, prometheus.GaugeValue, boolFloat(snapshot.Active), labels...)
		ch <- prometheus.MustNewConstMetric(
			m.lastCheckTimestamp,
			prometheus.GaugeValue,
			lastCheckTimestamp,
			labels...,
		)
		ch <- prometheus.MustNewConstMetric(
			m.lastCheckSuccess,
			prometheus.GaugeValue,
			boolFloat(snapshot.Checked && snapshot.LastCheckSuccess),
			labels...,
		)
		ch <- prometheus.MustNewConstMetric(
			m.lastCheckDuration,
			prometheus.GaugeValue,
			snapshot.LastCheckDuration.Seconds(),
			labels...,
		)
		ch <- prometheus.MustNewConstMetric(
			m.backendConsecutiveFailures,
			prometheus.GaugeValue,
			float64(snapshot.ConsecutiveFailures),
			labels...,
		)
	}
}

func (m *Metrics) DialError(backend, stage string) {
	m.dialErrors.WithLabelValues(backend, stage).Inc()
}

func (m *Metrics) Failover(fromBackend, toBackend string) {
	m.failovers.WithLabelValues(fromBackend, toBackend).Inc()
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
