package wgbackend

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const awg2Settings = `
Jc=0
Jmin=40
Jmax=80
S1=12
S2=12
S3=12
S4=12
H1=100-199
H2=200-299
H3=300-399
H4=400-499
I1=<b 0x12345678><r 4>
`

func TestTunnelHandshakeDNSAndTCP(t *testing.T) {
	awg3 := awg2Settings + "HeaderProtectionKey=" + testKey + `
ContentPaddingAddition=10-20
RekeyAfterTime=120-180
RekeyTimeout=5-10
RejectAfterTime=240-300
KeepaliveTimeout=10-15
MaxHandshakeAttempts=18-20
`
	for _, mode := range []struct {
		name     string
		settings string
	}{
		{"WireGuard", ""},
		{"AWG2", awg2Settings},
		{"AWG3", awg3},
		{"AWG3.1", strings.Replace(awg3, "ContentPaddingAddition=10-20", "RandomTrailers=on\nDisableCookies=on", 1)},
	} {
		t.Run(mode.name, func(t *testing.T) {
			serverPrivate, serverPublic := testKeys(t)
			clientPrivate, clientPublic := testKeys(t)
			server := startTestBackend(t, "10.0.0.1/32", serverPrivate, clientPublic, mode.settings, "")
			state, err := server.dev.IpcGet()
			if err != nil {
				t.Fatal("could not read server listen port")
			}
			var port string
			for line := range strings.SplitSeq(state, "\n") {
				if value, ok := strings.CutPrefix(line, "listen_port="); ok {
					port = value
				}
			}
			if port == "" || port == "0" {
				t.Fatal("server has no UDP listen port")
			}
			serveTestDNS(t, server)
			listener, err := server.net.ListenTCP(&net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080})
			if err != nil {
				t.Fatal(err)
			}
			const payload = "AmneziaWG tunnel echo"
			result := make(chan error, 1)
			var workers sync.WaitGroup
			workers.Go(func() {
				conn, err := listener.Accept()
				if err != nil {
					result <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				data := make([]byte, len(payload))
				if _, err := io.ReadFull(conn, data); err != nil {
					result <- err
					return
				}
				_, err = conn.Write(data)
				result <- err
			})
			t.Cleanup(func() {
				_ = listener.Close()
				workers.Wait()
			})
			client := startTestBackend(t, "10.0.0.2/32", clientPrivate, serverPublic,
				mode.settings+"\nDNS=10.0.0.1\n", "Endpoint=127.0.0.1:"+port+"\nPersistentKeepalive=22-30\n")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			addresses, err := client.Resolve(ctx, "echo.test")
			if err != nil {
				t.Fatalf("resolve over tunnel: %v", err)
			}
			if len(addresses) != 1 || !addresses[0].Equal(net.ParseIP("10.0.0.1")) {
				t.Fatal("DNS response did not come from the tunnel DNS server")
			}
			conn, err := client.DialContext(ctx, "tcp", net.JoinHostPort(addresses[0].String(), "8080"))
			if err != nil {
				t.Fatalf("dial over tunnel: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.WriteString(conn, payload); err != nil {
				t.Fatal(err)
			}
			response := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, response); err != nil {
				t.Fatal(err)
			}
			if string(response) != payload {
				t.Fatal("TCP echo differs from the sent payload")
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInvalidDeviceConfigClosesUDPBind(t *testing.T) {
	private, public := testKeys(t)
	for _, settings := range []string{
		"H1=2",                           // Overlaps the default H2.
		"HeaderProtectionKey=" + testKey, // Missing required nonce padding.
		"I1=<SENSITIVE_SIGNATURE_DO_NOT_LOG>",
		"PersistentKeepalive=invalid", // Unknown device-level field.
	} {
		reservation, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := reservation.LocalAddr().(*net.UDPAddr).Port
		if err := reservation.Close(); err != nil {
			t.Fatal(err)
		}
		configPath := writeTestConfig(t, "10.0.0.1/32", private, public,
			fmt.Sprintf("ListenPort=%d\n%s\n", port, settings), "")
		backend, err := Start(configPath, "invalid")
		if backend != nil {
			_ = backend.Close()
			t.Fatal("invalid configuration created a backend")
		}
		if err == nil {
			t.Fatal("expected invalid device config error")
		}
		if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "SENSITIVE_SIGNATURE_DO_NOT_LOG") {
			t.Fatal("device error exposes config values")
		}
		check, err := net.ListenPacket("udp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("failed start leaked the UDP bind: %v", err)
		}
		_ = check.Close()
	}
}

func TestBackendIdentitySystemResolverAndConcurrentClose(t *testing.T) {
	private, public := testKeys(t)
	backend := startTestBackend(t, "10.0.0.1/32", private, public, "", "")
	if backend.ID() != "test" || backend.ConfigPath() == "" || !backend.systemDNS {
		t.Fatal("backend identity or resolver mode is incorrect")
	}
	addresses, err := backend.Resolve(t.Context(), "127.0.0.1")
	if err != nil || len(addresses) != 1 || !addresses[0].IsLoopback() {
		t.Fatal("system resolver fallback did not resolve a literal IP")
	}
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() { _ = backend.Close() })
	}
	workers.Wait()
}

func testKeys(t *testing.T) (private, public string) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key.Bytes()), base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
}

func writeTestConfig(t *testing.T, address, private, public, settings, peerSettings string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "awg.conf")
	data := fmt.Sprintf("[Interface]\nAddress=%s\nPrivateKey=%s\n%s\n[Peer]\nPublicKey=%s\n%s\n",
		address, private, settings, public, peerSettings)
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func startTestBackend(t *testing.T, address, private, public, settings, peerSettings string) *Backend {
	t.Helper()
	configPath := writeTestConfig(t, address, private, public, settings, peerSettings)
	backend, err := Start(configPath, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

func serveTestDNS(t *testing.T, backend *Backend) {
	t.Helper()
	listener, err := backend.net.ListenUDP(&net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 53})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Go(func() {
		buffer := make([]byte, 4096)
		for {
			n, address, err := listener.ReadFrom(buffer)
			if err != nil {
				return
			}
			var message dnsmessage.Message
			if err := message.Unpack(buffer[:n]); err != nil {
				return
			}
			message.Header.Response = true
			message.Header.Authoritative = true
			message.Header.RecursionAvailable = true
			for _, question := range message.Questions {
				if question.Type == dnsmessage.TypeA && question.Name.String() == "echo.test." {
					message.Answers = append(message.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
						Body:   &dnsmessage.AResource{A: [4]byte{10, 0, 0, 1}},
					})
				}
			}
			response, err := message.Pack()
			if err != nil {
				return
			}
			if _, err := listener.WriteTo(response, address); err != nil {
				return
			}
		}
	})
	t.Cleanup(func() {
		_ = listener.Close()
		workers.Wait()
	})
}
