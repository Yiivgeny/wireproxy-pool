package wgbackend

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
)

type Backend struct {
	id         string
	configPath string
	net        *netstack.Net
	dev        *device.Device
	systemDNS  bool
	close      func()
}

func Start(configPath, id string) (*Backend, error) {
	configuration, err := loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return start(configuration, configPath, id)
}

func start(configuration *configuration, configPath, id string) (*Backend, error) {
	tun, tnet, err := netstack.CreateNetTUN(configuration.addresses, configuration.dns, configuration.mtu)
	if err != nil {
		return nil, fmt.Errorf("create AmneziaWG netstack: %w", err)
	}
	// UAPI errors can contain config values. Keep them out of process logs.
	startup := newStartupTUN(tun)
	dev := device.NewDevice(startup, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	if err := dev.IpcSet(configuration.ipc); err != nil {
		dev.Close()
		return nil, fmt.Errorf("invalid AmneziaWG device configuration: check AWG parameters, header ranges and S1-S4 padding")
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("start AmneziaWG device: %w", err)
	}
	startup.release()
	return &Backend{
		id: id, configPath: configPath, net: tnet, dev: dev,
		systemDNS: len(configuration.dns) == 0,
		close:     sync.OnceFunc(dev.Close),
	}, nil
}

func (b *Backend) ID() string {
	return b.id
}

func (b *Backend) ConfigPath() string {
	return b.configPath
}

func (b *Backend) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	var addresses []string
	var err error
	if b.systemDNS {
		addresses, err = net.DefaultResolver.LookupHost(ctx, host)
	} else {
		addresses, err = b.net.LookupContextHost(ctx, host)
	}
	if err != nil {
		return nil, err
	}

	result := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ip := net.ParseIP(address)
		if ip != nil {
			result = append(result, ip)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("resolver returned no usable IP addresses")
	}
	return result, nil
}

func (b *Backend) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return b.net.DialContext(ctx, network, address)
}

func (b *Backend) Close() error {
	b.close()
	return nil
}
