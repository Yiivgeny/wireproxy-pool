package wgbackend

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

var testKey = base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))

func testConfig(extra string) string {
	return "[Interface]\nPrivateKey=" + testKey + "\nAddress=10.0.0.2/32\n" + extra +
		"\n[Peer]\nPublicKey=" + testKey + "\n"
}

func TestParseAWG3Parameters(t *testing.T) {
	conf, err := parseConfig([]byte(testConfig(`
DNS=10.0.0.1, fd00::1
Address=fd00::2/128
MTU=1280
ListenPort=0
Jc=0
Jmin=40
Jmax=70
S1=12
S2=12
S3=12
S4=12
H1=100-199
H2=200-299
H3=300-399
H4=400-499
I1=<b 0x1234><r 4>
I2=<rd 4>
I3=<rc 4>
I4=<t>
I5=<r 8>
HeaderProtectionKey=`+testKey+`
ContentPaddingAddition=10-20
RekeyAfterTime=120-180
RekeyTimeout=5-10
RejectAfterTime=240-300
KeepaliveTimeout=10-15
MaxHandshakeAttempts=18-20
RandomTrailers=on
DisableCookies=off
`) + "PersistentKeepalive=22-30\nEndpoint=[::1]:51820\nAllowedIPs=192.0.2.12/24, ::/0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if conf.mtu != 1280 || len(conf.addresses) != 2 || conf.addresses[0].String() != "10.0.0.2" ||
		conf.addresses[1].String() != "fd00::2" || len(conf.dns) != 2 {
		t.Fatal("interface addresses, DNS or MTU were not preserved")
	}
	for _, line := range []string{
		"listen_port=0", "jc=0", "jmin=40", "jmax=70",
		"s1=12", "s2=12", "s3=12", "s4=12",
		"h1=100-199", "h2=200-299", "h3=300-399", "h4=400-499",
		"i1=<b 0x1234><r 4>", "i2=<rd 4>", "i3=<rc 4>", "i4=<t>", "i5=<r 8>",
		"header_protection_key=" + hex.EncodeToString([]byte("01234567890123456789012345678901")),
		"content_padding_addition=10-20", "rekey_after_time=120-180", "rekey_timeout=5-10",
		"reject_after_time=240-300", "keepalive_timeout=10-15", "max_handshake_attempts=18-20",
		"random_trailers=true", "disable_cookies=false",
		"persistent_keepalive_interval=22-30", "endpoint=[::1]:51820",
		"allowed_ip=192.0.2.0/24", "allowed_ip=::/0",
	} {
		if !strings.Contains(conf.ipc, line+"\n") {
			t.Fatalf("missing setting %s", strings.SplitN(line, "=", 2)[0])
		}
	}
	if strings.Index(conf.ipc, "header_protection_key=") > strings.Index(conf.ipc, "public_key=") {
		t.Fatal("device parameters must precede peer parameters")
	}
	if !strings.HasSuffix(conf.ipc, "\n\n") {
		t.Fatal("UAPI transaction is missing its terminator")
	}
}

func TestParseLegacyConfigDefaultsAndPeers(t *testing.T) {
	conf, err := parseConfig([]byte(strings.ToLower("[INTERFACE]\nAddress=10.0.0.2\n") +
		"privatekey=" + testKey + "\n[peer]\npublickey=" + testKey +
		"\n[peer]\npublickey=" + testKey + "\nAllowedIPs=10.0.0.0/8\nAllowedIPs=fd00::/64\n"))
	if err != nil {
		t.Fatal(err)
	}
	if conf.mtu != 1420 || len(conf.dns) != 0 || strings.Count(conf.ipc, "public_key=") != 2 {
		t.Fatal("legacy defaults or multiple peer sections were not preserved")
	}
	for _, prefix := range []string{"0.0.0.0/0", "::/0", "10.0.0.0/8", "fd00::/64"} {
		if !strings.Contains(conf.ipc, "allowed_ip="+prefix+"\n") {
			t.Fatalf("missing allowed IP %s", prefix)
		}
	}
}

func TestParseEnvironmentAndRejectUAPIInjection(t *testing.T) {
	t.Setenv("POOL_TEST_KEY", testKey)
	data := strings.ReplaceAll(testConfig("HeaderProtectionKey=$POOL_TEST_KEY"), testKey, "$POOL_TEST_KEY")
	if _, err := parseConfig([]byte(data)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_KEY", testKey+"\npublic_key="+testKey)
	if _, err := parseConfig([]byte(data)); err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatal("multiline environment values must be rejected without exposing keys")
	}
}

func TestInvalidConfigDoesNotExposeValues(t *testing.T) {
	const secret = "SENSITIVE_VALUE_DO_NOT_LOG"
	t.Setenv("POOL_TEST_SECRET", secret)
	for _, tt := range []struct {
		name string
		data string
	}{
		{"INI syntax", secret},
		{"private key", strings.Replace(testConfig(""), testKey, secret, 1)},
		{"public key", testConfig("") + "PublicKey=" + secret},
		{"preshared key", testConfig("") + "PresharedKey=" + secret},
		{"header protection key", testConfig("HeaderProtectionKey=" + secret)},
		{"MTU", testConfig("MTU=" + secret)},
		{"address", testConfig("Address=" + secret)},
		{"DNS", testConfig("DNS=" + secret)},
		{"endpoint", testConfig("") + "Endpoint=" + secret},
		{"allowed IP", testConfig("") + "AllowedIPs=" + secret},
		{"unknown field", testConfig("Unknown=" + secret)},
		{"unknown section", testConfig("") + "[" + secret + "]\n"},
		{"duplicate interface", testConfig("") + "[Interface]\nPrivateKey=" + secret},
		{"missing peer", "[Interface]\nPrivateKey=" + testKey + "\nAddress=10.0.0.2"},
		{"missing address", strings.Replace(testConfig(""), "Address=10.0.0.2/32", "", 1)},
		{"missing private key", "[Interface]\nAddress=10.0.0.2\n[Peer]\nPublicKey=" + testKey},
		{"missing public key", "[Interface]\nAddress=10.0.0.2\nPrivateKey=" + testKey + "\n[Peer]\n"},
		{"duplicate scalar", testConfig("MTU=1400\nMTU=1200")},
		{"reversed junk range", testConfig("Jc=4\nJmin=80\nJmax=40")},
		{"missing junk max", testConfig("Jc=4\nJmin=40")},
		{"negative junk size", testConfig("Jmin=-1")},
		{"excessive junk count", testConfig("Jc=4294967295")},
		{"negative signature size", testConfig("I1=<r -1>")},
		{"negative character count", testConfig("I1=<rc -1>")},
		{"negative digit count", testConfig("I1=<rd -1>")},
		{"negative data size", testConfig("I1=<dz -1>")},
		{"signature size overflow", testConfig("I1=<r 65500><r 65500>")},
		{"unset environment", testConfig("HeaderProtectionKey=$POOL_TEST_ENV_THAT_IS_NOT_SET")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseConfig([]byte(tt.data))
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), testKey) {
				t.Fatal("config error exposes key or config content")
			}
		})
	}
}
