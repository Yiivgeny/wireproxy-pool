package pool_test

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/yiivgeny/wireproxy-pool/internal/pool"
)

type fakeBackend struct {
	id         string
	configPath string
	addresses  []net.IP
	resolveErr error
	dialErr    error

	mu           sync.Mutex
	resolveCalls []string
	dialCalls    []string
}

func (b *fakeBackend) ID() string         { return b.id }
func (b *fakeBackend) ConfigPath() string { return b.configPath }
func (b *fakeBackend) Close() error       { return nil }

func (b *fakeBackend) Resolve(_ context.Context, host string) ([]net.IP, error) {
	b.mu.Lock()
	b.resolveCalls = append(b.resolveCalls, host)
	b.mu.Unlock()
	if b.resolveErr != nil {
		return nil, b.resolveErr
	}
	return b.addresses, nil
}

func (b *fakeBackend) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	b.mu.Lock()
	b.dialCalls = append(b.dialCalls, address)
	b.mu.Unlock()
	if b.dialErr != nil {
		return nil, b.dialErr
	}
	client, peer := net.Pipe()
	_ = peer.Close()
	return client, nil
}

func (b *fakeBackend) calls() ([]string, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.resolveCalls...), append([]string(nil), b.dialCalls...)
}

type observedFailover struct {
	from string
	to   string
}

type fakeObserver struct {
	mu        sync.Mutex
	failovers []observedFailover
}

func (*fakeObserver) DialError(string, string) {}
func (o *fakeObserver) Failover(from, to string) {
	o.mu.Lock()
	o.failovers = append(o.failovers, observedFailover{from: from, to: to})
	o.mu.Unlock()
}

func TestPrimaryBackendIsSelectedFirst(t *testing.T) {
	primary := newFakeBackend("primary", "10.0.0.1")
	fallback := newFakeBackend("fallback", "10.0.0.2")
	p := newHealthyPool(t, primary, fallback)

	ctx, ip, err := p.Resolve(context.Background(), "service.test")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got, want := ip.String(), "10.0.0.1"; got != want {
		t.Fatalf("Resolve() = %s, want %s", got, want)
	}
	conn, selection, err := p.DialDestinationWithSelection(ctx, "tcp", "service.test", 443)
	if err != nil {
		t.Fatalf("DialDestinationWithSelection() error = %v", err)
	}
	_ = conn.Close()
	if selection.BackendID != "primary" || selection.Endpoint != "10.0.0.1:443" {
		t.Fatalf("selection = %+v", selection)
	}

	primaryResolves, primaryDials := primary.calls()
	fallbackResolves, fallbackDials := fallback.calls()
	if !reflect.DeepEqual(primaryResolves, []string{"service.test"}) {
		t.Fatalf("primary resolve calls = %v", primaryResolves)
	}
	if !reflect.DeepEqual(primaryDials, []string{"10.0.0.1:443"}) {
		t.Fatalf("primary dial calls = %v", primaryDials)
	}
	if len(fallbackResolves)+len(fallbackDials) != 0 {
		t.Fatalf("fallback unexpectedly used: resolve=%v dial=%v", fallbackResolves, fallbackDials)
	}
}

func TestDialFailoverRepeatsDNSOnNextBackend(t *testing.T) {
	primary := newFakeBackend("primary", "10.0.0.1")
	primary.dialErr = errors.New("destination refused")
	fallback := newFakeBackend("fallback", "10.0.0.2")
	p := newHealthyPool(t, primary, fallback)
	observer := &fakeObserver{}
	p.SetObserver(observer)

	ctx, _, err := p.Resolve(context.Background(), "service.test")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	conn, selection, err := p.DialDestinationWithSelection(ctx, "tcp", "service.test", 443)
	if err != nil {
		t.Fatalf("DialDestinationWithSelection() error = %v", err)
	}
	_ = conn.Close()
	if selection.BackendID != "fallback" || selection.Endpoint != "10.0.0.2:443" {
		t.Fatalf("selection = %+v", selection)
	}

	_, primaryDials := primary.calls()
	fallbackResolves, fallbackDials := fallback.calls()
	if !reflect.DeepEqual(primaryDials, []string{"10.0.0.1:443"}) {
		t.Fatalf("primary dial calls = %v", primaryDials)
	}
	if !reflect.DeepEqual(fallbackResolves, []string{"service.test"}) {
		t.Fatalf("fallback resolve calls = %v", fallbackResolves)
	}
	if !reflect.DeepEqual(fallbackDials, []string{"10.0.0.2:443"}) {
		t.Fatalf("fallback dial calls = %v", fallbackDials)
	}
	if got, want := observer.failovers, []observedFailover{{from: "primary", to: "fallback"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failovers = %v, want %v", got, want)
	}

	// A destination-specific refusal triggers per-request fallback but does not
	// alter health. Only RecordHealth can change backend availability.
	snapshots := p.Snapshots()
	if !snapshots[0].Up || !snapshots[1].Up {
		t.Fatalf("destination error changed health: %+v", snapshots)
	}
}

func TestRecoveryRestoresPrimaryPriority(t *testing.T) {
	primary := newFakeBackend("primary", "10.0.0.1")
	fallback := newFakeBackend("fallback", "10.0.0.2")
	p, err := pool.New([]pool.Backend{primary, fallback}, time.Second, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	p.RecordHealth(0, true, time.Millisecond)
	p.RecordHealth(1, true, time.Millisecond)

	p.RecordHealth(0, false, time.Millisecond)
	if !p.Snapshots()[0].Up {
		t.Fatal("primary went down before failure threshold")
	}
	p.RecordHealth(0, false, time.Millisecond)
	if p.Snapshots()[0].Up {
		t.Fatal("primary stayed up after failure threshold")
	}

	_, ip, err := p.Resolve(context.Background(), "service.test")
	if err != nil || ip.String() != "10.0.0.2" {
		t.Fatalf("resolution while primary down = %v, %v", ip, err)
	}

	p.RecordHealth(0, true, time.Millisecond)
	if p.Snapshots()[0].Up {
		t.Fatal("primary recovered before success threshold")
	}
	p.RecordHealth(0, true, time.Millisecond)
	if !p.Snapshots()[0].Up {
		t.Fatal("primary did not recover at success threshold")
	}

	_, ip, err = p.Resolve(context.Background(), "service.test")
	if err != nil || ip.String() != "10.0.0.1" {
		t.Fatalf("resolution after primary recovery = %v, %v", ip, err)
	}
	if !p.Snapshots()[0].Active {
		t.Fatal("recovered primary is not active")
	}
}

func TestInitialProbeResultSetsAvailabilityImmediately(t *testing.T) {
	backend := newFakeBackend("primary", "10.0.0.1")
	p, err := pool.New([]pool.Backend{backend}, time.Second, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if p.Snapshots()[0].Up {
		t.Fatal("unchecked backend starts up")
	}
	p.RecordHealth(0, true, time.Millisecond)
	if !p.Snapshots()[0].Up {
		t.Fatal("first successful probe did not make backend available")
	}
}

func newFakeBackend(id, address string) *fakeBackend {
	return &fakeBackend{
		id:         id,
		configPath: "/configs/" + id + ".conf",
		addresses:  []net.IP{net.ParseIP(address)},
	}
}

func newHealthyPool(t *testing.T, backends ...pool.Backend) *pool.Pool {
	t.Helper()
	p, err := pool.New(backends, time.Second, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	for index := range backends {
		p.RecordHealth(index, true, time.Millisecond)
	}
	return p
}
