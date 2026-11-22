# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Breaking: `relay` refuses to start (exit 2) unless it is given `-allow-file` or the new `-open` (config key `open`). `-open` runs a relay that any key may reserve on and connect through, and logs the `open relay` warning as before; the two cannot be used together. Add `-open` to keep a relay that was started without `-allow-file`.
- Breaking: with `-allow-file`, only the listed keys may connect through the relay, as only they may reserve; another key that asks to reach a listed key gets the answer of a key with no reservation (`kolorelay.ErrUnavailable`).

### Added

- `relay`: the listener the `-relay-max-conns`, `-relay-max-conns-per-ip`, window and congestion flags build offers QUIC datagrams, as kolo-relay's default listener does, so clients that opt in (`Transport.Datagrams` in kolo-relay's `Config.Relay`) carry relayed packets as datagrams; the relay offers them, clients use them only when they ask.
- `relay`: `-relay-stream-window`, `-relay-conn-window` and `-relay-congestion` (and the same config file keys) set the listener's receive windows and congestion controller. 0 and empty keep kolo-relay's 64 KiB, 256 KiB and Reno; a value out of range, or a stream window above the connection window, exits with code 2 and names the flag.

## [0.1.0] - 2026-11-20

### Added

- First release of kolo-node.
