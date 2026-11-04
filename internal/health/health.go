package health

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/yiivgeny/wireproxy-pool/internal/pool"
)

type Target struct {
	Host string
	Port int
}

func ParseTarget(value string) (Target, error) {
	host, portValue, err := net.SplitHostPort(value)
	if err != nil {
		return Target{}, fmt.Errorf("health target must be host:port: %w", err)
	}
	if host == "" {
		return Target{}, fmt.Errorf("health target host is empty")
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		return Target{}, fmt.Errorf("health target port must be between 1 and 65535")
	}
	return Target{Host: host, Port: port}, nil
}

func (t Target) String() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

type Prober interface {
	Probe(context.Context, pool.Backend, Target) error
}

// TCPProber resolves the target through the backend and establishes a TCP
// connection through that same backend's userspace network stack.
type TCPProber struct{}

func (TCPProber) Probe(ctx context.Context, backend pool.Backend, target Target) error {
	ip := net.ParseIP(target.Host)
	if ip == nil {
		addresses, err := backend.Resolve(ctx, target.Host)
		if err != nil {
			return fmt.Errorf("resolve health target: %w", err)
		}
		if len(addresses) == 0 {
			return fmt.Errorf("resolve health target: no addresses")
		}
		ip = addresses[0]
	}

	conn, err := backend.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(target.Port)))
	if err != nil {
		return fmt.Errorf("connect health target: %w", err)
	}
	_ = conn.Close()
	return nil
}

type Monitor struct {
	pool     *pool.Pool
	prober   Prober
	target   Target
	interval time.Duration
	timeout  time.Duration
	logger   *log.Logger
	wg       sync.WaitGroup
}

func NewMonitor(p *pool.Pool, prober Prober, target Target, interval, timeout time.Duration, logger *log.Logger) (*Monitor, error) {
	if p == nil {
		return nil, fmt.Errorf("health monitor pool is nil")
	}
	if prober == nil {
		return nil, fmt.Errorf("health prober is nil")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("health interval must be positive")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("health timeout must be positive")
	}
	return &Monitor{
		pool:     p,
		prober:   prober,
		target:   target,
		interval: interval,
		timeout:  timeout,
		logger:   logger,
	}, nil
}

// CheckAll performs one concurrent probe of every concrete backend.
func (m *Monitor) CheckAll(ctx context.Context) {
	backends := m.pool.Backends()
	var checks sync.WaitGroup
	checks.Add(len(backends))
	for index, backend := range backends {
		go func() {
			defer checks.Done()
			started := time.Now()
			probeCtx, cancel := context.WithTimeout(ctx, m.timeout)
			err := m.prober.Probe(probeCtx, backend, m.target)
			cancel()
			if ctx.Err() != nil {
				return
			}
			transition := m.pool.RecordHealth(index, err == nil, time.Since(started))
			if transition.Changed && m.logger != nil {
				if err != nil {
					m.logger.Printf("backend %s is unavailable after health check: %v", transition.BackendID, err)
				} else {
					m.logger.Printf("backend %s is available", transition.BackendID)
				}
			}
		}()
	}
	checks.Wait()
}

func (m *Monitor) Start(ctx context.Context) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.CheckAll(ctx)
			}
		}
	}()
}

func (m *Monitor) Wait() {
	m.wg.Wait()
}
