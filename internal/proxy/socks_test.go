package proxy

import (
	"bytes"
	"log"
	"net"
	"strings"
	"testing"

	"github.com/yiivgeny/wireproxy-pool/internal/pool"
)

func TestLogSuccessfulConnectAtDebugLevel(t *testing.T) {
	var output bytes.Buffer
	logger := log.New(&output, "", 0)
	client := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321}
	selection := pool.DialSelection{BackendID: "fallback", Endpoint: "192.0.2.10:443"}

	logSuccessfulConnect(logger, true, client, "service.test:443", selection)
	want := `DEBUG SOCKS CONNECT client="127.0.0.1:54321" ` +
		`requested_endpoint="service.test:443" selected_endpoint="192.0.2.10:443" backend="fallback"`
	if got := strings.TrimSpace(output.String()); got != want {
		t.Fatalf("debug log = %q, want %q", got, want)
	}
}

func TestLogSuccessfulConnectIsSilentAtInfoLevel(t *testing.T) {
	var output bytes.Buffer
	logger := log.New(&output, "", 0)
	logSuccessfulConnect(
		logger,
		false,
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321},
		"service.test:443",
		pool.DialSelection{BackendID: "primary", Endpoint: "192.0.2.10:443"},
	)
	if output.Len() != 0 {
		t.Fatalf("info level emitted debug log: %q", output.String())
	}
}
