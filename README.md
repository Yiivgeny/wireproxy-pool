# wireproxy-pool

**English** | [Русский](README.ru.md)

`wireproxy-pool` is a local SOCKS5 TCP CONNECT proxy with an active/passive pool of independent AmneziaWG 3 backends. Each configuration runs directly through the official `github.com/amnezia-vpn/amneziawg-go/v3` library with its own userspace TUN, network stack, and device. A shared SOCKS5 listener selects a backend above the VPN layer. Standard WireGuard and AmneziaWG 1/2 configurations are also supported.

Module path: `github.com/yiivgeny/wireproxy-pool`.

## Build

Requires Go 1.26 or later. The `github.com/amnezia-vpn/amneziawg-go/v3` dependency is pinned to the official `v3.1.20260828` tag. No neighboring checkouts or local `replace` directives are required.

```sh
go build -o wireproxy-pool ./cmd/wireproxy-pool
```

Real `configs/*.conf` files are excluded from Git. The `configs/example.conf.example` template contains placeholders only; copy it to a `.conf` file and fill in your settings locally.

## AmneziaWG 3 configuration

A configuration must contain exactly one `[Interface]` section and one or more `[Peer]` sections. Section and field names are case-insensitive. Unknown fields and sections produce an error so that VPN parameters cannot be silently ignored. Proxy sections from `wireproxy-awg` and imports through `WGConfig` are not supported: configure the listener and health checks through `wireproxy-pool` command-line flags.

In addition to the standard `PrivateKey`, `Address`, `DNS`, `MTU`, `ListenPort`, and peer fields, the following parameters are supported:

| Section | Fields | Format |
| --- | --- | --- |
| Interface | `Jc`, `Jmin`, `Jmax`, `S1`–`S4` | Nonnegative integers |
| Interface | `H1`–`H4` | A number or a range such as `100-199`; ranges must not overlap |
| Interface | `I1`–`I5` | Custom signature packet specifications |
| Interface | `HeaderProtectionKey` | The server's header protection key: 32 bytes encoded as base64 |
| Interface | `ContentPaddingAddition` | A number or a range |
| Interface | `RekeyAfterTime`, `RekeyTimeout`, `RejectAfterTime`, `KeepaliveTimeout` | Seconds: a number or a range |
| Interface | `MaxHandshakeAttempts` | A number or a range |
| Interface | `RandomTrailers`, `DisableCookies` | `on`/`off` or `true`/`false` (AWG 3.1) |
| Peer | `PersistentKeepalive` | Seconds: a number or a range such as `22-30` |

To enable AWG 3 header protection, configure the same `HeaderProtectionKey` on the client and server, and set every `S1`–`S4` value to at least 12. Updating the client library cannot supply the key or the parameters that must match the server: use a configuration exported by the server. Configurations without `HeaderProtectionKey` retain compatibility with earlier protocol versions. See the [official AmneziaWG configuration documentation](https://github.com/amnezia-vpn/amneziawg-go/blob/v3.1.20260828/README.md#configuration).

A value starting with `$NAME` reads the named environment variable; a leading `$$` escapes a literal `$`. Repeated `Address`, `DNS`, and `AllowedIPs` fields are combined. The default MTU is 1420. If `AllowedIPs` is omitted, it defaults to `0.0.0.0/0` and `::/0`. `Address` is required. Invalid configurations fail startup without logging keys or field values.

The parser limits `Jc` to 0–128, checks `0 <= Jmin <= Jmax <= 65507`, and validates signature packet sizes to prevent invalid values from triggering panics or excessive memory allocation inside the VPN engine.

TUN initialization works around an [upstream first-packet bug with nonzero S4](https://github.com/amnezia-vpn/amneziawg-go/issues/168): it suppresses the automatic `EventUp` and returns zero packets from the first read after configuration, allowing the device to refresh its transport padding before reading application traffic.

## Run

Two explicit files:

```sh
./wireproxy-pool configs/awg1.conf configs/awg2.conf
```

A quoted glob expanded by the application:

```sh
./wireproxy-pool 'configs/*.conf'
```

A combination of files and multiple patterns:

```sh
./wireproxy-pool configs/awg1.conf 'configs/fallback-*.conf' '/etc/wireproxy/extra-?.conf'
```

Flags must precede the first configuration path:

```sh
./wireproxy-pool \
  --socks-listen 127.0.0.1:1080 \
  --metrics-listen 127.0.0.1:9090 \
  --log-level debug \
  --health-target one.one.one.one:443 \
  --health-interval 15s \
  --health-timeout 5s \
  --connect-timeout 10s \
  --failure-threshold 3 \
  --success-threshold 2 \
  'configs/*.conf'
```

Every pattern must match at least one regular file. Invalid patterns, missing files, and an empty result set fail startup with an explanatory error. Arguments retain their input order, matches within each pattern are sorted, and duplicate files are removed by canonical absolute path, preserving the first occurrence. The first resulting configuration is therefore the primary backend.

### supervisord

Homebrew `supervisord` can keep the application running. Machine-specific settings in the local `supervisor/` directory are not included in the repository. Set the working directory to the project root and the command to `go run ./cmd/wireproxy-pool configs/*.conf`, using the full path to Go. The application expands the `configs/*.conf` pattern.

```sh
supervisorctl status wireproxy-pool
supervisorctl restart wireproxy-pool
```

## Endpoints

- SOCKS5: `127.0.0.1:1080` by default. Supports TCP CONNECT without authentication; BIND and UDP ASSOCIATE are disabled.
- Prometheus: `http://127.0.0.1:9090/metrics` by default.

Both listeners bind to loopback by default. If you expose SOCKS5 on an external interface, add external access controls: built-in authentication is not currently available.

### Connection debug logging

The default log level is `--log-level info`. At `debug` level, each successful SOCKS CONNECT logs the client, requested destination, resolved endpoint actually used, and selected backend:

```text
DEBUG SOCKS CONNECT client="127.0.0.1:54321" requested_endpoint="example.com:443" selected_endpoint="203.0.113.10:443" backend="awg2-53034a35"
```

The log entry identifies the backend that actually established the connection, including any fallback. These entries are not emitted at `info` level.

## Health checks and failover

Health checks verify useful connectivity through each individual backend. Hostnames are resolved with that backend's resolver, and the TCP connection to `--health-target` is established through the same userspace network stack. A successful handshake alone is not sufficient.

Availability rules:

1. A backend is unavailable until its first health check. Initial checks run before the listeners begin serving; the first success immediately makes the backend available, while the first failure leaves it unavailable.
2. An available backend becomes unavailable after `--failure-threshold` consecutive failed health checks (default: 3).
3. An unavailable backend recovers after `--success-threshold` consecutive successful checks (default: 2). These thresholds reduce flapping.
4. The active backend is the first available backend in the stable configuration order. When the primary recovers, it receives new connections again.
5. Destination-specific failures, including connection refusal and NXDOMAIN, do not change backend health. They apply only to the current request and can trigger an attempt through the next available backend.

For each SOCKS request, DNS resolution and the first dial attempt use the same selected backend. On fallback, the original hostname is resolved again through the next backend. `--connect-timeout` applies separately to each backend attempt; `--health-timeout` applies separately to each backend probe.

If a VPN configuration includes `DNS`, lookups use the DNS servers of that backend's network stack. If `DNS` is omitted, the system resolver is used; TCP connections still go through the selected userspace network stack. Specify DNS servers in every configuration to keep DNS inside each tunnel.

Failover applies to new connections and connection attempts. Existing TCP sessions do not migrate between backends.

## Metrics

All per-backend gauges have stable `backend` and `config` labels. The backend identifier combines the filename with a short hash of its absolute path; configuration contents and keys are not used to construct labels.

- `wireproxy_backend_up` — current stable availability state, `0` or `1`.
- `wireproxy_backend_active` — whether this backend is the first available backend, `0` or `1`.
- `wireproxy_backend_last_check_timestamp_seconds` — completion time of the latest health check.
- `wireproxy_backend_last_check_success` — result of the latest health check, `0` or `1`.
- `wireproxy_backend_last_check_duration_seconds` — duration of the latest health check.
- `wireproxy_backend_consecutive_failures` — consecutive failed health checks.
- `wireproxy_backend_failovers_total{from_backend,to_backend}` — transitions between backends during request attempts.
- `wireproxy_backend_dial_errors_total{backend,stage}` — errors at the `resolve`, `connect`, and `no_backend` stages.

Private, public, and preshared keys and configuration contents are not exposed in logs or metrics. The `config` label contains only the absolute configuration path.

## Shutdown and checks

SIGINT/SIGTERM stops health-check goroutines, closes the SOCKS listener and active client connections, gracefully shuts down the metrics HTTP server, and closes all successfully created devices. A partial startup failure also closes any backends that have already started.

Local checks require no external network access. Integration tests create pairs of userspace devices over loopback UDP and verify handshakes, DNS, and TCP echo for WireGuard, AWG 2, AWG 3 with header protection and content padding, and AWG 3.1 with `RandomTrailers` and `DisableCookies`:

```sh
go test ./...
go test -race ./...
go vet ./...
```
