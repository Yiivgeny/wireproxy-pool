package metrics_test

import (
	"context"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	poolmetrics "github.com/yiivgeny/wireproxy-pool/internal/metrics"
	"github.com/yiivgeny/wireproxy-pool/internal/pool"
)

type metricBackend struct {
	id     string
	config string
}

func (b metricBackend) ID() string         { return b.id }
func (b metricBackend) ConfigPath() string { return b.config }
func (metricBackend) Resolve(context.Context, string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("192.0.2.1")}, nil
}
func (metricBackend) DialContext(context.Context, string, string) (net.Conn, error) {
	client, peer := net.Pipe()
	_ = peer.Close()
	return client, nil
}
func (metricBackend) Close() error { return nil }

func TestMetricsExposePerBackendCurrentStateAndCounters(t *testing.T) {
	backends := []pool.Backend{
		metricBackend{id: "primary", config: "/configs/primary.conf"},
		metricBackend{id: "fallback", config: "/configs/fallback.conf"},
	}
	p, err := pool.New(backends, time.Second, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	p.RecordHealth(0, true, 25*time.Millisecond)
	p.RecordHealth(1, false, 50*time.Millisecond)

	registry := prometheus.NewRegistry()
	m, err := poolmetrics.New(registry, p)
	if err != nil {
		t.Fatal(err)
	}
	p.SetObserver(m)
	m.Failover("primary", "fallback")
	m.DialError("primary", "connect")

	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("metrics status = %d", response.Code)
	}
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	output := string(body)
	assertContains(t, output, `wireproxy_backend_up{backend="primary",config="/configs/primary.conf"} 1`)
	assertContains(t, output, `wireproxy_backend_up{backend="fallback",config="/configs/fallback.conf"} 0`)
	assertContains(t, output, `wireproxy_backend_active{backend="primary",config="/configs/primary.conf"} 1`)
	assertContains(t, output, `wireproxy_backend_last_check_success{backend="fallback",config="/configs/fallback.conf"} 0`)
	assertContains(t, output, `wireproxy_backend_failovers_total{from_backend="primary",to_backend="fallback"} 1`)
	assertContains(t, output, `wireproxy_backend_dial_errors_total{backend="primary",stage="connect"} 1`)
}

func assertContains(t *testing.T, output, value string) {
	t.Helper()
	if !strings.Contains(output, value) {
		t.Fatalf("metrics output does not contain %q:\n%s", value, output)
	}
}
