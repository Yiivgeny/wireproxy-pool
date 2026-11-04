package pool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

var ErrNoAvailableBackend = errors.New("no healthy backend is available")

// Backend is one isolated userspace network stack and WireGuard/AWG device.
type Backend interface {
	ID() string
	ConfigPath() string
	Resolve(context.Context, string) ([]net.IP, error)
	DialContext(context.Context, string, string) (net.Conn, error)
	Close() error
}

// Observer receives request-path events. Health gauges are read from Snapshot.
type Observer interface {
	DialError(backend, stage string)
	Failover(fromBackend, toBackend string)
}

type noopObserver struct{}

func (noopObserver) DialError(string, string) {}
func (noopObserver) Failover(string, string)  {}

type healthState struct {
	checked              bool
	up                   bool
	consecutiveSuccesses int
	consecutiveFailures  int
	lastCheck            time.Time
	lastCheckDuration    time.Duration
	lastCheckSuccess     bool
}

// Snapshot is the current, race-free state used by metrics and diagnostics.
type Snapshot struct {
	Index                int
	BackendID            string
	ConfigPath           string
	Checked              bool
	Up                   bool
	Active               bool
	ConsecutiveSuccesses int
	ConsecutiveFailures  int
	LastCheck            time.Time
	LastCheckDuration    time.Duration
	LastCheckSuccess     bool
}

// DialSelection identifies the backend and resolved endpoint that actually
// established a destination connection.
type DialSelection struct {
	BackendID string
	Endpoint  string
}

// Transition describes the result of recording one health probe.
type Transition struct {
	BackendID string
	Checked   bool
	Changed   bool
	Up        bool
}

type entry struct {
	backend Backend
	health  healthState
}

// Pool chooses the first healthy backend in a stable configuration order.
type Pool struct {
	mu               sync.RWMutex
	entries          []entry
	connectTimeout   time.Duration
	failureThreshold int
	successThreshold int
	observer         Observer
	now              func() time.Time
}

func New(backends []Backend, connectTimeout time.Duration, failureThreshold, successThreshold int) (*Pool, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("create pool: no backends")
	}
	if connectTimeout <= 0 {
		return nil, fmt.Errorf("connect timeout must be positive")
	}
	if failureThreshold < 1 {
		return nil, fmt.Errorf("failure threshold must be at least 1")
	}
	if successThreshold < 1 {
		return nil, fmt.Errorf("success threshold must be at least 1")
	}

	entries := make([]entry, len(backends))
	for i, backend := range backends {
		if backend == nil {
			return nil, fmt.Errorf("backend %d is nil", i)
		}
		entries[i].backend = backend
	}

	return &Pool{
		entries:          entries,
		connectTimeout:   connectTimeout,
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
		observer:         noopObserver{},
		now:              time.Now,
	}, nil
}

func (p *Pool) SetObserver(observer Observer) {
	if observer == nil {
		observer = noopObserver{}
	}
	p.mu.Lock()
	p.observer = observer
	p.mu.Unlock()
}

func (p *Pool) Backends() []Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()

	backends := make([]Backend, len(p.entries))
	for i := range p.entries {
		backends[i] = p.entries[i].backend
	}
	return backends
}

func (p *Pool) RecordHealth(index int, success bool, duration time.Duration) Transition {
	p.mu.Lock()
	defer p.mu.Unlock()

	if index < 0 || index >= len(p.entries) {
		return Transition{}
	}

	state := &p.entries[index].health
	wasChecked := state.checked
	wasUp := state.up
	state.checked = true
	state.lastCheck = p.now()
	state.lastCheckDuration = duration
	state.lastCheckSuccess = success

	if success {
		state.consecutiveFailures = 0
		state.consecutiveSuccesses++
		if !wasChecked || (!state.up && state.consecutiveSuccesses >= p.successThreshold) {
			state.up = true
		}
	} else {
		state.consecutiveSuccesses = 0
		state.consecutiveFailures++
		if !wasChecked || (state.up && state.consecutiveFailures >= p.failureThreshold) {
			state.up = false
		}
	}

	return Transition{
		BackendID: p.entries[index].backend.ID(),
		Checked:   true,
		Changed:   !wasChecked || wasUp != state.up,
		Up:        state.up,
	}
}

func (p *Pool) Snapshots() []Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	active := -1
	for i := range p.entries {
		if p.entries[i].health.up {
			active = i
			break
		}
	}

	snapshots := make([]Snapshot, len(p.entries))
	for i := range p.entries {
		backend := p.entries[i].backend
		state := p.entries[i].health
		snapshots[i] = Snapshot{
			Index:                i,
			BackendID:            backend.ID(),
			ConfigPath:           backend.ConfigPath(),
			Checked:              state.checked,
			Up:                   state.up,
			Active:               i == active,
			ConsecutiveSuccesses: state.consecutiveSuccesses,
			ConsecutiveFailures:  state.consecutiveFailures,
			LastCheck:            state.lastCheck,
			LastCheckDuration:    state.lastCheckDuration,
			LastCheckSuccess:     state.lastCheckSuccess,
		}
	}
	return snapshots
}

type selectedBackend struct {
	index     int
	attempted []int
	ip        net.IP
}

type selectionContextKey struct{}

// Resolve implements the go-socks5 NameResolver contract. It records the
// backend and IP in the returned context so DialDestination uses the same pair.
func (p *Pool) Resolve(ctx context.Context, host string) (context.Context, net.IP, error) {
	candidates := p.healthyIndices()
	if len(candidates) == 0 {
		p.observerSnapshot().DialError("none", "no_backend")
		return ctx, nil, ErrNoAvailableBackend
	}

	var errs []error
	for position, index := range candidates {
		backend := p.backend(index)
		attemptCtx, cancel := context.WithTimeout(ctx, p.connectTimeout)
		addresses, err := backend.Resolve(attemptCtx, host)
		cancel()

		ip := firstIP(addresses)
		if err == nil && ip == nil {
			err = fmt.Errorf("resolver returned no addresses")
		}
		if err == nil {
			selection := selectedBackend{
				index:     index,
				attempted: append([]int(nil), candidates[:position+1]...),
				ip:        append(net.IP(nil), ip...),
			}
			return context.WithValue(ctx, selectionContextKey{}, selection), selection.ip, nil
		}

		p.observerSnapshot().DialError(backend.ID(), "resolve")
		errs = append(errs, fmt.Errorf("backend %s resolve: %w", backend.ID(), err))
		if position+1 < len(candidates) {
			p.observerSnapshot().Failover(backend.ID(), p.backend(candidates[position+1]).ID())
		}
	}

	return ctx, nil, fmt.Errorf("resolve %q through all healthy backends: %w", host, errors.Join(errs...))
}

// DialDestination dials a SOCKS CONNECT destination. For a hostname, ctx should
// be the context returned by Resolve. Every fallback backend resolves the
// original hostname again through its own network stack.
func (p *Pool) DialDestination(ctx context.Context, network, host string, port int) (net.Conn, error) {
	conn, _, err := p.DialDestinationWithSelection(ctx, network, host, port)
	return conn, err
}

// DialDestinationWithSelection is DialDestination with metadata about the
// successful backend attempt for diagnostics such as debug connection logs.
func (p *Pool) DialDestinationWithSelection(
	ctx context.Context,
	network, host string,
	port int,
) (net.Conn, DialSelection, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, DialSelection{}, fmt.Errorf("unsupported network %q", network)
	}
	if port < 1 || port > 65535 {
		return nil, DialSelection{}, fmt.Errorf("invalid destination port %d", port)
	}

	selection, hasSelection := ctx.Value(selectionContextKey{}).(selectedBackend)
	indices := p.dialIndices(selection, hasSelection)
	if len(indices) == 0 {
		p.observerSnapshot().DialError("none", "no_backend")
		return nil, DialSelection{}, ErrNoAvailableBackend
	}

	parsedIP := net.ParseIP(host)
	var errs []error
	for position, index := range indices {
		backend := p.backend(index)
		attemptCtx, cancel := context.WithTimeout(ctx, p.connectTimeout)

		ip := parsedIP
		if hasSelection && index == selection.index && selection.ip != nil {
			ip = selection.ip
		} else if parsedIP == nil {
			addresses, err := backend.Resolve(attemptCtx, host)
			if err != nil {
				cancel()
				p.observerSnapshot().DialError(backend.ID(), "resolve")
				errs = append(errs, fmt.Errorf("backend %s resolve: %w", backend.ID(), err))
				p.recordDialFailover(indices, position)
				continue
			}
			ip = firstIP(addresses)
			if ip == nil {
				cancel()
				p.observerSnapshot().DialError(backend.ID(), "resolve")
				errs = append(errs, fmt.Errorf("backend %s resolve returned no addresses", backend.ID()))
				p.recordDialFailover(indices, position)
				continue
			}
		}

		address := net.JoinHostPort(ip.String(), strconv.Itoa(port))
		conn, err := backend.DialContext(attemptCtx, network, address)
		cancel()
		if err == nil {
			return conn, DialSelection{BackendID: backend.ID(), Endpoint: address}, nil
		}

		p.observerSnapshot().DialError(backend.ID(), "connect")
		errs = append(errs, fmt.Errorf("backend %s connect: %w", backend.ID(), err))
		p.recordDialFailover(indices, position)
	}

	return nil, DialSelection{}, fmt.Errorf(
		"dial %s:%d through all healthy backends: %w",
		host,
		port,
		errors.Join(errs...),
	)
}

func (p *Pool) dialIndices(selection selectedBackend, hasSelection bool) []int {
	healthy := p.healthyIndices()
	if !hasSelection {
		return healthy
	}

	indices := []int{selection.index}
	alreadyAttempted := make(map[int]struct{}, len(selection.attempted)+1)
	for _, index := range selection.attempted {
		alreadyAttempted[index] = struct{}{}
	}
	alreadyAttempted[selection.index] = struct{}{}
	for _, index := range healthy {
		if _, ok := alreadyAttempted[index]; !ok {
			indices = append(indices, index)
		}
	}
	return indices
}

func (p *Pool) healthyIndices() []int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	indices := make([]int, 0, len(p.entries))
	for i := range p.entries {
		if p.entries[i].health.up {
			indices = append(indices, i)
		}
	}
	return indices
}

func (p *Pool) backend(index int) Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.entries[index].backend
}

func (p *Pool) observerSnapshot() Observer {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.observer
}

func (p *Pool) recordDialFailover(indices []int, position int) {
	if position+1 >= len(indices) {
		return
	}
	from := p.backend(indices[position]).ID()
	to := p.backend(indices[position+1]).ID()
	p.observerSnapshot().Failover(from, to)
}

func firstIP(addresses []net.IP) net.IP {
	for _, address := range addresses {
		if address != nil {
			return address
		}
	}
	return nil
}

func CloseAll(backends []Backend) error {
	var errs []error
	for i := len(backends) - 1; i >= 0; i-- {
		if err := backends[i].Close(); err != nil {
			errs = append(errs, fmt.Errorf("close backend %s: %w", backends[i].ID(), err))
		}
	}
	return errors.Join(errs...)
}
