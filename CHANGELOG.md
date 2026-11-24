# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **Breaking:** `-relay-max-conns` no longer stops at 10000. Any value of 1 or more is accepted when (`-relay-max-conns` + 1) × (the connection window + 240 KiB of datagram queues) fits kolo-transport's 64 GiB `MaxMemory` limit (135299 connections at 256 KiB); 10001 used to exit with code 2 and now starts the relay. A combination above the limit still exits with code 2, also when the product would overflow. `-relay-max-sessions` keeps kolo-relay's range of 1 to 10000. Needs the kolo-transport release that carries the change.
- **Breaking:** the default of `-relay-max-reservations` (0) is now kolo-relay's derived default, (`-relay-max-conns` - `-relay-max-sessions`) / 2, at least 1: 56 at the defaults instead of 64, more with a bigger `-relay-max-conns` and fewer with a smaller one. `-relay-max-sessions` 0 now means 16, or a quarter of `-relay-max-conns` if lower (at least 1), so a small listener keeps reservations. Any larger value is accepted (kolo-relay's maximum of 100000 is gone). Needs the kolo-relay release that carries the change.
- **Breaking:** `-relay-max-conns` is no longer held to kolo-transport's 64 GiB `MaxMemory` limit, which is gone: 135300 connections at 256 KiB, or 10000 with a 64 MiB connection window, used to exit with code 2 and now start the relay. Only a combination whose (`-relay-max-conns` + 1) × (the connection window + 240 KiB of datagram queues) does not fit an `int64` exits with code 2.
- **Breaking:** `-relay-max-sessions` is at most `-relay-max-conns` (kolo-relay's `MaxSessions` follows the listener's `MaxConns`) instead of 10000: 129 at the default 128 connections now exits with code 2, and 10001 starts with `-relay-max-conns` 20000. `-relay-max-session-bytes` has no maximum (it was 1 TiB).
- **Breaking:** `-bootstrap`, `-bootstrap-file`, `KOLO_BOOTSTRAP`, `check` addresses and `check -relay`, `-relay-file` and `KOLO_RELAYS` no longer stop at 16 entries; a list is the operator's own input. A name still yields at most 8 addresses and 64 addresses in all.
- **Breaking:** with `-allow-file`, unlisted keys can no longer connect through the relay. `relay -allow-file` now sets kolo-relay's `AllowConnect` as well as `AllowReserve`, so only the listed keys may reserve and only the listed keys may connect; another key that asks to reach a listed key gets the answer of a key with no reservation (`kolorelay.ErrUnavailable`). Without `-allow-file` the relay is still open and logs the `open relay` warning; list the keys that must reach a member through the relay.

### Added

- `relay`: the `started` event carries `max_reservations`, the reservation limit in force, whether set with `-relay-max-reservations` or derived from `-relay-max-conns` and `-relay-max-sessions`.
- `relay`: the listener the `-relay-max-conns`, `-relay-max-conns-per-ip`, window and congestion flags build offers QUIC datagrams, as kolo-relay's default listener does, so clients that opt in (`Transport.Datagrams` in kolo-relay's `Config.Relay`) carry relayed packets as datagrams; the relay offers them, clients use them only when they ask.
- `relay`: `-relay-stream-window`, `-relay-conn-window` and `-relay-congestion` (and the same config file keys) set the listener's receive windows and congestion controller. 0 and empty keep kolo-relay's 64 KiB, 256 KiB and Reno; a value out of range, or a stream window above the connection window, exits with code 2 and names the flag.

## [0.1.0] - 2026-11-20

### Added

- First release of kolo-node.
