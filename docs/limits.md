# Limits

Every option of `kolo-node run` and `kolo-node check`, with its default, its range and why. Each flag except `-config` is also a key of the config file, with the same name and meaning. The other pages: [guide.md](guide.md), [deploy.md](deploy.md).

| Option | Default | Range | Why |
|---|---|---|---|
| `-config` | none | JSON file of at most 64 KiB, flag only | Bounds what a wrong path (a log, a device) can make the node read |
| `-listen` | `:7447` | IPv4 `host:port` | Unassigned at IANA, above 1024, below Linux's ephemeral range |
| `-identity` | required | key file; created on the first run with mode 0600, its directory with 0700 if missing; never replaced | Not a limit |
| `-bootstrap` | none | comma-separated `host:port` list, repeatable (in the file, an array of strings); with `-bootstrap-file` and `KOLO_BOOTSTRAP`, any number of distinct entries | The operator's own list, so it has no count limit. One answer makes the node ping at most 8 addresses per name and 64 in all |
| `-bootstrap-file` | none | text file of at most 64 KiB, one `host:port` per line, repeatable (in the file, an array of paths) | Bounds what a wrong path can make the node read; about 250 entries of the longest form (a 253-byte name, a colon and a 5-digit port) fit, and comments take the rest |
| `-first` | false | true: no entry nodes; refused with `-bootstrap` or `-bootstrap-file` | Not a limit |
| `KOLO_BOOTSTRAP` | unset | as `-bootstrap`; read only when `-bootstrap`, `-bootstrap-file` (even set to an empty list) and `-first` are all unset | Not a limit |
| `-log-format` | `text` | `text` or `json` | Not a limit |
| `-log-level` | `info` | `info`, `warn` or `error` | Not a limit |
| `-log-addrs` | false | log peer addresses in full instead of their /24 | Not a limit |
| `-stats-interval` | `5m` | 1s to 24h | Bounds the log: 288 lines a day |
| `-dht-max-records` | 65536 | 16 to 1,000,000 | kolo-dht `MaxRecords`; its default and range. A full store takes about 710 bytes of heap per record (kolo-dht `MaxRecords`), 44 MiB at the default |
| `-dht-max-records-per-topic` | 512 | 1 to 65536 | kolo-dht `MaxRecordsPerTopic`; its default and range |
| `-dht-max-records-per-ip` | 256 | 1 to 65536 | kolo-dht `MaxRecordsPerIP`; its default and range |
| `-dht-max-inflight` | 160 | 8 to 4096; at least 75 with `-bootstrap` | kolo-dht `MaxInflight`; its default and range. The 75 lets every bootstrap PING hold a request slot at once, beside kolo-dht's 10 check pings and its maintenance request |
| `-dht-max-relays` | 64 | 1 to 4096 | kolo-dht `MaxRelays`; its default and range |
| `-dht-max-requests-per-ip` | 50 | per second, 1 to 10,000 | kolo-dht `MaxRequestsPerIP`; its default and range |
| `-dht-max-requests-per-prefix` | 200, or `-dht-max-requests-per-ip` if higher | per second, `-dht-max-requests-per-ip` to 100,000 | kolo-dht `MaxRequestsPerPrefix`; its default and range |
| `-dht-max-requests` | 10,000, or the per-prefix limit if higher | per second, the per-prefix limit to 1,000,000 | kolo-dht `MaxRequests`; its default and range |
| `-dht-request-timeout` | `1s` | 100ms to 10s | kolo-dht `RequestTimeout`; its default and range |
| `-dht-punch-timeout` | `45s` | 1s to 2m | kolo-dht `PunchTimeout`; its default and range, which cover a 35s random punch |
| addresses per bootstrap name | 8 | fixed | A name may front several nodes, and one attempt reaches up to 8 of them |
| bootstrap addresses in all | 64 | fixed | kolo-dht's own cap (`maxBoot`), so no address is dropped silently |
| name resolution | 5s per name, in parallel | fixed | Go's resolver waits 5s per query (net/dnsconfig_unix.go) |
| `Bootstrap` deadline | 30s | fixed | Three times the 10s maximum `-dht-request-timeout`, so the PING step always ends inside it |
| retry until joined | 1 minute, doubling up to 5 minutes | fixed | Starts at 1 minute, so a node started before its peers joins soon; the ceiling matches the 5-minute refresh |
| refresh once joined | every 5 minutes while the table holds fewer than 20 servers | fixed | Raised from every 30 minutes below 4 servers; 20 is kolo-dht's bucket size |
| shutdown | 10s, then exit 1 | fixed | A hung close ends in exit 1 and a restart |
| inbound kolo-secure connections | all refused by `AcceptFunc` | fixed | Nobody needs a connection to a bootstrap node |
| `MaxConns` of that listener | 16 | fixed | Bounds connections that get past the refusal |
| `MaxPending` of that listener | 16 | fixed | Raised from 8 to `MaxConns`: pending connections count against `MaxConns`, so more cannot be reached |
| `MaxDialed` of that listener | 1 | fixed | kolo-transport's minimum, since kolo-node never dials |
| `MaxIncomingStreams` of that listener | 1 | fixed | Measured: quic-go retains about 96 heap bytes per byte read on an open stream (kolo-transport README) |
| receive windows of that listener | 64 KiB per stream and per connection | fixed | Measured: about 176 heap bytes per unread byte with 1-byte frames (kolo-transport README), so the worst case is 16 × (2 × 1 × 64 KiB × 96 + 64 KiB × 176), about 368 MiB; the memory sum is under Running on a VPS |
| `relay -listen` | none, required | IPv4 `host:port` | No port is assigned to kolo-relay; the operator picks one |
| `relay -identity` | required | as `run -identity`; a key file of its own | Not a limit |
| `relay -allow-file` | none: an open relay, logged as a `WARN` at start | text file of at most 64 KiB, one hex key per line, at least one key | Bounds what a wrong path can make the relay read, as `-bootstrap-file`; the listed keys are the only ones that may reserve and connect |
| `-relay-max-reservations` | 0: (`-relay-max-conns` - `-relay-max-sessions`) / 2, 56 at the defaults | 1 and up | kolo-relay `MaxReservations`; its default and range. It follows the listener, since a reservation lives on a connection (kolo-relay limits) |
| `-relay-max-sessions` | 16 | 1 to 10000 | kolo-relay `MaxSessions` |
| `-relay-max-sessions-per-peer` | 4, or `-relay-max-sessions` if lower | 1 to `-relay-max-sessions` | kolo-relay `MaxSessionsPerPeer` |
| `-relay-max-sessions-per-target` | 8, or `-relay-max-sessions` if lower | 1 to `-relay-max-sessions` | kolo-relay `MaxSessionsPerTarget` |
| `-relay-reservation-lifetime` | `1h` | 1s to 24h | kolo-relay `ReservationLifetime` |
| `-relay-max-session-duration` | `1h` | 1s to 24h | kolo-relay `MaxSessionDuration` |
| `-relay-max-session-bytes` | 1 GiB | 64 KiB to 1 TiB, each direction | kolo-relay `MaxSessionBytes` |
| `-relay-session-idle-timeout` | `60s` | 1s to 10m | kolo-relay `SessionIdleTimeout` |
| `-relay-connect-timeout` | `15s` | 1s to 60s | kolo-relay `ConnectTimeout` |
| `-relay-control-timeout` | `15s` | 1s to 60s | kolo-relay `ControlTimeout` |
| `-relay-rate-bytes` | 1 MiB per second | 16 KiB to 1 GiB | kolo-relay `RateBytes`, per client connection |
| `-relay-rate-burst` | 256 KiB | 16 KiB to 64 MiB | kolo-relay `RateBurst` |
| `-relay-flush-interval` | `1ms` | 100µs to 100ms | kolo-relay `FlushInterval` |
| `-relay-max-conns` | 128 | 1 to 10000 | The listener's kolo-transport `MaxConns`, kolo-relay's listener default and kolo-transport's range; its `MaxMemory` is raised to (`-relay-max-conns` + 1) × 256 KiB of receive window when that is above kolo-transport's 1 GiB default (about 2.4 GiB at 10000), which kolo-transport requires |
| `-relay-max-conns-per-ip` | 4, or `-relay-max-conns` if lower | 1 to `-relay-max-conns` | The listener's kolo-transport `MaxConnsPerIP`; with either listener flag set, every other listener field keeps kolo-relay's value |
| `-relay-stream-window` | 64 KiB | 64 KiB to 16 MiB, at most `-relay-conn-window` | The listener's kolo-transport `MaxStreamReceiveWindow`, kolo-relay's listener default and kolo-transport's range; a session moves at most this many bytes per round trip in each direction. Worst case about 176 heap bytes per byte of connection window on every connection (kolo-relay limits) |
| `-relay-conn-window` | 256 KiB | 64 KiB to 64 MiB | The listener's kolo-transport `MaxConnReceiveWindow`, kolo-relay's listener default and kolo-transport's range; `MaxMemory` follows it as for `-relay-max-conns`, and (`-relay-max-conns` + 1) × the window may not exceed kolo-transport's 64 GiB `MaxMemory` limit |
| `-relay-congestion` | `reno` | `reno`, `cubic` or `bbr` | The listener's kolo-transport `CongestionControl`; it paces what the relay sends to each client. `bbr` adds up to 5.5 MiB of samples per connection (kolo-transport limits) |
| `check -relay`, `-relay-file`, `KOLO_RELAYS` | none | any number of distinct relays; a file of at most 64 KiB | Same as `-bootstrap` |
| `check -timeout` | `5s` per address | 1s to 1m | Each request waits half of it, so a PING always ends before it |
| `check` addresses | any number | none | Same as `-bootstrap`; each is checked in turn |

A `-dht-` flag left at 0 takes kolo-dht's default (the values above, from
kolo-dht), and kolo-dht checks the ranges. `Config.Server` is
always true. kolo-node is IPv4 only and IPv6 input is refused: an
IPv6 address in `-listen`, `-bootstrap` or `check`, IPv4-mapped forms
included, is a config error (exit 2), and names resolve to IPv4 addresses
only.
