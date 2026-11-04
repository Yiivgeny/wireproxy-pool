package wgbackend

import (
	"sync"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
)

// startupTUN keeps the first packet behind configuration and an explicit Up.
// Upstream snapshots S4 before its first blocking Read, so that Read must return
// zero packets once to refresh the offset after IpcSet:
// https://github.com/amnezia-vpn/amneziawg-go/issues/168
type startupTUN struct {
	tun.Device
	ready     chan struct{}
	events    chan tun.Event
	firstRead bool // Only the device's TUN reader accesses this field.
	release   func()
	close     func() error
}

func newStartupTUN(dev tun.Device) *startupTUN {
	t := &startupTUN{
		Device: dev, ready: make(chan struct{}), events: make(chan tun.Event), firstRead: true,
	}
	t.release = sync.OnceFunc(func() { close(t.ready) })
	t.close = sync.OnceValue(func() error {
		t.release()
		err := dev.Close()
		close(t.events)
		return err
	})
	return t
}

func (t *startupTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	if t.firstRead {
		<-t.ready
		t.firstRead = false
		return 0, nil
	}
	return t.Device.Read(bufs, sizes, offset)
}

func (t *startupTUN) Events() <-chan tun.Event {
	// The static netstack's automatic EventUp must not start peers before their
	// AWG parameters are committed. The owner calls Device.Up explicitly.
	return t.events
}

func (t *startupTUN) Close() error {
	return t.close()
}
