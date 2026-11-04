package proxy

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"

	"github.com/things-go/go-socks5"
	"github.com/yiivgeny/wireproxy-pool/internal/pool"
)

type SOCKSServer struct {
	server *socks5.Server
}

func NewSOCKSServer(p *pool.Pool, logger *log.Logger, debug bool) (*SOCKSServer, error) {
	if p == nil {
		return nil, fmt.Errorf("SOCKS pool is nil")
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}

	server := socks5.NewServer(
		socks5.WithResolver(p),
		socks5.WithRule(&socks5.PermitCommand{EnableConnect: true}),
		socks5.WithDialAndRequest(func(ctx context.Context, network, _ string, request *socks5.Request) (net.Conn, error) {
			host := request.RawDestAddr.FQDN
			if host == "" {
				host = request.RawDestAddr.IP.String()
			}
			conn, selection, err := p.DialDestinationWithSelection(ctx, network, host, request.RawDestAddr.Port)
			if err != nil {
				return nil, err
			}
			logSuccessfulConnect(
				logger,
				debug,
				request.RemoteAddr,
				net.JoinHostPort(host, strconv.Itoa(request.RawDestAddr.Port)),
				selection,
			)
			return conn, nil
		}),
	)
	return &SOCKSServer{server: server}, nil
}

func (s *SOCKSServer) Serve(listener net.Listener) error {
	return s.server.Serve(listener)
}

func logSuccessfulConnect(
	logger *log.Logger,
	debug bool,
	client net.Addr,
	requestedEndpoint string,
	selection pool.DialSelection,
) {
	if !debug {
		return
	}
	clientAddress := "unknown"
	if client != nil {
		clientAddress = client.String()
	}
	logger.Printf(
		"DEBUG SOCKS CONNECT client=%q requested_endpoint=%q selected_endpoint=%q backend=%q",
		clientAddress,
		requestedEndpoint,
		selection.Endpoint,
		selection.BackendID,
	)
}
