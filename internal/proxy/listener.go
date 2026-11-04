package proxy

import (
	"errors"
	"net"
	"sync"
)

// TrackingListener allows shutdown to close both the listener and established
// client-side SOCKS connections managed by go-socks5.
type TrackingListener struct {
	net.Listener

	mu      sync.Mutex
	closing bool
	conns   map[*trackedConn]struct{}
}

type trackedConn struct {
	net.Conn
	once   sync.Once
	remove func(*trackedConn)
}

func NewTrackingListener(listener net.Listener) *TrackingListener {
	return &TrackingListener{Listener: listener, conns: make(map[*trackedConn]struct{})}
}

func (l *TrackingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	tracked := &trackedConn{Conn: conn, remove: l.remove}
	l.mu.Lock()
	if l.closing {
		l.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	l.conns[tracked] = struct{}{}
	l.mu.Unlock()
	return tracked, nil
}

func (l *TrackingListener) Shutdown() error {
	l.mu.Lock()
	l.closing = true
	connections := make([]*trackedConn, 0, len(l.conns))
	for conn := range l.conns {
		connections = append(connections, conn)
	}
	l.mu.Unlock()

	var errs []error
	if err := l.Listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		errs = append(errs, err)
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (l *TrackingListener) remove(conn *trackedConn) {
	l.mu.Lock()
	delete(l.conns, conn)
	l.mu.Unlock()
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.remove(c) })
	return err
}
