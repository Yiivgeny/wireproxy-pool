package health_test

import (
	"context"
	"net"
	"reflect"
	"testing"

	"github.com/yiivgeny/wireproxy-pool/internal/health"
)

type probeBackend struct {
	resolved []string
	dialed   []string
}

func (*probeBackend) ID() string         { return "backend" }
func (*probeBackend) ConfigPath() string { return "/configs/backend.conf" }
func (*probeBackend) Close() error       { return nil }

func (b *probeBackend) Resolve(_ context.Context, host string) ([]net.IP, error) {
	b.resolved = append(b.resolved, host)
	return []net.IP{net.ParseIP("192.0.2.10")}, nil
}

func (b *probeBackend) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	b.dialed = append(b.dialed, address)
	client, peer := net.Pipe()
	_ = peer.Close()
	return client, nil
}

func TestTCPProberResolvesAndDialsThroughSameBackend(t *testing.T) {
	backend := &probeBackend{}
	err := (health.TCPProber{}).Probe(
		context.Background(),
		backend,
		health.Target{Host: "health.test", Port: 443},
	)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if !reflect.DeepEqual(backend.resolved, []string{"health.test"}) {
		t.Fatalf("resolve calls = %v", backend.resolved)
	}
	if !reflect.DeepEqual(backend.dialed, []string{"192.0.2.10:443"}) {
		t.Fatalf("dial calls = %v", backend.dialed)
	}
}

func TestParseTarget(t *testing.T) {
	target, err := health.ParseTarget("example.test:8443")
	if err != nil {
		t.Fatal(err)
	}
	if target.Host != "example.test" || target.Port != 8443 {
		t.Fatalf("target = %+v", target)
	}
	if _, err := health.ParseTarget("missing-port"); err == nil {
		t.Fatal("ParseTarget() accepted a target without port")
	}
}
