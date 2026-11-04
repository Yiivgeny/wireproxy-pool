package wgbackend

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/go-ini/ini"
)

type configuration struct {
	addresses []netip.Addr
	dns       []netip.Addr
	mtu       int
	ipc       string
}

type parameter struct {
	name string
	uapi string
}

var interfaceParameters = []parameter{
	{"PrivateKey", "private_key"},
	{"ListenPort", "listen_port"},
	{"Jc", "jc"}, {"Jmin", "jmin"}, {"Jmax", "jmax"},
	{"S1", "s1"}, {"S2", "s2"}, {"S3", "s3"}, {"S4", "s4"},
	{"H1", "h1"}, {"H2", "h2"}, {"H3", "h3"}, {"H4", "h4"},
	{"I1", "i1"}, {"I2", "i2"}, {"I3", "i3"}, {"I4", "i4"}, {"I5", "i5"},
	{"HeaderProtectionKey", "header_protection_key"},
	{"ContentPaddingAddition", "content_padding_addition"},
	{"RekeyAfterTime", "rekey_after_time"},
	{"RekeyTimeout", "rekey_timeout"},
	{"RejectAfterTime", "reject_after_time"},
	{"KeepaliveTimeout", "keepalive_timeout"},
	{"MaxHandshakeAttempts", "max_handshake_attempts"},
	{"RandomTrailers", "random_trailers"},
	{"DisableCookies", "disable_cookies"},
}

var peerParameters = []parameter{
	{"PublicKey", "public_key"},
	{"PresharedKey", "preshared_key"},
	{"Endpoint", "endpoint"},
	{"PersistentKeepalive", "persistent_keepalive_interval"},
}

func loadConfig(configPath string) (*configuration, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	return parseConfig(data)
}

func parseConfig(data []byte) (*configuration, error) {
	cfg, err := ini.LoadSources(ini.LoadOptions{
		Insensitive: true, AllowShadows: true, AllowNonUniqueSections: true,
	}, data)
	if err != nil {
		// The INI parser includes the offending line in some errors.
		return nil, fmt.Errorf("invalid INI syntax")
	}
	for _, section := range cfg.Sections() {
		switch strings.ToLower(section.Name()) {
		case "default":
			if len(section.Keys()) != 0 {
				return nil, fmt.Errorf("config fields must be inside [Interface] or [Peer]")
			}
		case "interface", "peer":
		default:
			return nil, fmt.Errorf("only [Interface] and [Peer] sections are supported")
		}
	}
	interfaces, err := cfg.SectionsByName("Interface")
	if err != nil || len(interfaces) != 1 {
		return nil, fmt.Errorf("exactly one [Interface] is required")
	}
	peers, err := cfg.SectionsByName("Peer")
	if err != nil || len(peers) == 0 {
		return nil, fmt.Errorf("at least one [Peer] is required")
	}
	iface := interfaces[0]
	if err := checkFields(iface, interfaceParameters, "Address", "DNS", "MTU"); err != nil {
		return nil, err
	}
	if err := validateJunk(iface); err != nil {
		return nil, err
	}
	addresses, err := parseAddresses(iface, "Address", true)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("[Interface] Address is required")
	}
	dns, err := parseAddresses(iface, "DNS", false)
	if err != nil {
		return nil, err
	}
	mtu := 1420
	if iface.HasKey("MTU") {
		value, err := configValue(iface, "MTU")
		if err != nil {
			return nil, err
		}
		mtu, err = strconv.Atoi(value)
		if err != nil || mtu <= 0 || mtu > 65535 {
			return nil, fmt.Errorf("MTU must be between 1 and 65535")
		}
	}
	var ipc strings.Builder
	if err := writeParameters(&ipc, iface, interfaceParameters, "PrivateKey"); err != nil {
		return nil, err
	}
	for _, peer := range peers {
		if err := checkFields(peer, peerParameters, "AllowedIPs"); err != nil {
			return nil, err
		}
		if err := writeParameters(&ipc, peer, peerParameters, "PublicKey"); err != nil {
			return nil, err
		}
		allowed := "0.0.0.0/0, ::/0"
		if peer.HasKey("AllowedIPs") {
			allowed, err = configValue(peer, "AllowedIPs")
			if err != nil {
				return nil, err
			}
		}
		for value := range strings.SplitSeq(allowed, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("AllowedIPs must contain valid IP prefixes")
			}
			fmt.Fprintf(&ipc, "allowed_ip=%s\n", prefix.Masked())
		}
	}
	// An empty line terminates the UAPI transaction and commits AWG settings.
	ipc.WriteByte('\n')
	return &configuration{addresses: addresses, dns: dns, mtu: mtu, ipc: ipc.String()}, nil
}

func checkFields(section *ini.Section, parameters []parameter, localFields ...string) error {
	allowed := make(map[string]bool, len(parameters)+len(localFields))
	for _, p := range parameters {
		allowed[strings.ToLower(p.name)] = true
	}
	for _, name := range localFields {
		allowed[strings.ToLower(name)] = true
	}
	for _, key := range section.Keys() {
		if !allowed[strings.ToLower(key.Name())] {
			return fmt.Errorf("unsupported field in [%s]", section.Name())
		}
		if len(key.ValueWithShadows()) > 1 {
			switch strings.ToLower(key.Name()) {
			case "address", "dns", "allowedips":
			default:
				return fmt.Errorf("duplicate field in [%s]", section.Name())
			}
		}
	}
	return nil
}

func configValue(section *ini.Section, name string) (string, error) {
	key, err := section.GetKey(name)
	if err != nil {
		return "", fmt.Errorf("%s is required", name)
	}
	values := key.ValueWithShadows()
	for i, value := range values {
		if strings.HasPrefix(value, "$$") {
			value = value[1:]
		} else if envName, ok := strings.CutPrefix(value, "$"); ok {
			value, ok = os.LookupEnv(envName)
			if !ok {
				return "", fmt.Errorf("%s references an unset environment variable", name)
			}
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return "", fmt.Errorf("%s must be a single line", name)
		}
		values[i] = strings.TrimSpace(value)
	}
	return strings.Join(values, ","), nil
}

func parseAddresses(section *ini.Section, name string, allowPrefix bool) ([]netip.Addr, error) {
	if !section.HasKey(name) {
		return nil, nil
	}
	value, err := configValue(section, name)
	if err != nil {
		return nil, err
	}
	var addresses []netip.Addr
	for item := range strings.SplitSeq(value, ",") {
		item = strings.TrimSpace(item)
		addr, err := netip.ParseAddr(item)
		if err != nil && allowPrefix {
			if prefix, prefixErr := netip.ParsePrefix(item); prefixErr == nil {
				addr, err = prefix.Addr(), nil
			}
		}
		if err != nil || addr.Zone() != "" {
			return nil, fmt.Errorf("%s must contain valid IP addresses", name)
		}
		addresses = append(addresses, addr)
	}
	return addresses, nil
}

func writeParameters(ipc *strings.Builder, section *ini.Section, parameters []parameter, required string) error {
	for _, p := range parameters {
		if !section.HasKey(p.name) && p.name != required {
			continue
		}
		value, err := configValue(section, p.name)
		if err != nil {
			return err
		}
		switch p.uapi {
		case "private_key", "public_key", "preshared_key", "header_protection_key":
			decoded, err := base64.StdEncoding.DecodeString(value)
			if err != nil || len(decoded) != 32 {
				return fmt.Errorf("%s must be a base64-encoded 32-byte key", p.name)
			}
			value = hex.EncodeToString(decoded)
		case "endpoint":
			endpoint, err := net.ResolveUDPAddr("udp", value)
			if err != nil || endpoint.IP == nil || endpoint.Port == 0 {
				return fmt.Errorf("Endpoint must resolve to an IP address with a port between 1 and 65535")
			}
			value = endpoint.String()
		case "random_trailers", "disable_cookies":
			switch strings.ToLower(value) {
			case "on":
				value = "true"
			case "off":
				value = "false"
			}
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("%s must be on/off or true/false", p.name)
			}
			value = strconv.FormatBool(enabled)
		case "i1", "i2", "i3", "i4", "i5":
			if err := validateSignature(value); err != nil {
				return fmt.Errorf("%s contains an invalid packet size", p.name)
			}
		}
		fmt.Fprintf(ipc, "%s=%s\n", p.uapi, value)
	}
	return nil
}

// The upstream engine accepts junk ranges and signature lengths that can panic
// when a handshake is sent. Reject those values before creating the device.
func validateJunk(section *ini.Section) error {
	values := [3]uint64{}
	for i, name := range []string{"Jc", "Jmin", "Jmax"} {
		if !section.HasKey(name) {
			continue
		}
		value, err := configValue(section, name)
		if err != nil {
			return err
		}
		values[i], err = strconv.ParseUint(value, 10, 32)
		if err != nil {
			return fmt.Errorf("%s must be a nonnegative integer", name)
		}
	}
	if values[0] > 128 {
		return fmt.Errorf("Jc must be between 0 and 128")
	}
	if values[1] > values[2] || values[2] > 65507 {
		return fmt.Errorf("junk sizes must satisfy 0 <= Jmin <= Jmax <= 65507")
	}
	return nil
}

func validateSignature(value string) error {
	total := 0
	for {
		_, rest, found := strings.Cut(value, "<")
		if !found {
			return nil
		}
		tag, rest, found := strings.Cut(rest, ">")
		if !found {
			return fmt.Errorf("unterminated tag")
		}
		value = rest
		fields := strings.Fields(tag)
		if len(fields) == 0 {
			continue // The engine validates tag syntax.
		}
		switch fields[0] {
		case "r", "rc", "rd", "dz":
			if len(fields) < 2 {
				return fmt.Errorf("missing size")
			}
			size, err := strconv.ParseUint(fields[1], 10, 16)
			if err != nil {
				return fmt.Errorf("invalid size")
			}
			total += int(size)
		case "b":
			if len(fields) > 1 {
				total += len(strings.TrimPrefix(fields[1], "0x")) / 2
			}
		case "t":
			total += 4
		}
		if total > 65507 {
			return fmt.Errorf("signature exceeds maximum UDP payload")
		}
	}
}
