# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- `-relay-max-reservations` keeps its meaning with default 0, which now means kolo-relay's derived default, (`-relay-max-conns` - `-relay-max-sessions`) / 2, 56 at the defaults instead of 64, and any larger value is accepted (kolo-relay's maximum of 100000 is gone). Needs the kolo-relay release that carries the change.
- `-bootstrap`, `-bootstrap-file`, `KOLO_BOOTSTRAP`, `check` addresses and `check -relay`, `-relay-file` and `KOLO_RELAYS` no longer stop at 16 entries; a list is the operator's own input. A name still yields at most 8 addresses and 64 addresses in all.

- Breaking: with `-allow-file`, unlisted keys can no longer connect through the relay. `relay -allow-file` now sets kolo-relay's `AllowConnect` as well as `AllowReserve`, so only the listed keys may reserve and only the listed keys may connect; another key that asks to reach a listed key gets the answer of a key with no reservation (`kolorelay.ErrUnavailable`). Without `-allow-file` the relay is still open and logs the `open relay` warning; list the keys that must reach a member through the relay.

### Added

- `relay`: the listener the `-relay-max-conns`, `-relay-max-conns-per-ip`, window and congestion flags build offers QUIC datagrams, as kolo-relay's default listener does, so clients that opt in (`Transport.Datagrams` in kolo-relay's `Config.Relay`) carry relayed packets as datagrams; the relay offers them, clients use them only when they ask.
- `relay`: `-relay-stream-window`, `-relay-conn-window` and `-relay-congestion` (and the same config file keys) set the listener's receive windows and congestion controller. 0 and empty keep kolo-relay's 64 KiB, 256 KiB and Reno; a value out of range, or a stream window above the connection window, exits with code 2 and names the flag.

## [0.1.0] - 2026-11-20

### Added

- First release of kolo-node.
