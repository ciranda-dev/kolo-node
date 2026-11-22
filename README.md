# kolo-node

A bootstrap server for the kolo-dht network: the address new Kolo peers join through.

[![status: experimental](https://img.shields.io/badge/status-experimental-orange)](#status)
[![license: MIT OR Apache-2.0](https://img.shields.io/badge/license-MIT%20OR%20Apache--2.0-blue)](#license)
[![go: 1.26+](https://img.shields.io/badge/go-1.26%2B-00ADD8)](go.mod)

## What it is

kolo-node runs a [kolo-dht](https://github.com/ciranda-dev/kolo-dht) server on one UDP port. It answers DHT queries, stores signed topic announcements and relays connect requests so peers can punch through their NATs. It carries no application data. `kolo-node relay` runs a [kolo-relay](https://github.com/ciranda-dev/kolo-relay) instead, for peers that cannot connect directly; it copies their end-to-end encrypted traffic.

It keeps its [kolo-id](https://github.com/ciranda-dev/kolo-id) key in a file, shuts down cleanly on SIGINT and SIGTERM, and logs a few structured events with slog. Run a few of them on different hosts and give their addresses to peers. The command is Go only, with no cgo; `CGO_ENABLED=0` gives a static binary.

## Install

```sh
go install github.com/ciranda-dev/kolo-node/cmd/kolo-node@latest
```

## Quick start

Start a new network on loopback: its first node, with `-first` (no entry nodes yet) and its key in a new temporary directory:

```sh
dir=$(mktemp -d)
kolo-node run -listen 127.0.0.1:7447 -identity "$dir/state/node.key" -first
```

Check it from a second terminal:

```sh
kolo-node check 127.0.0.1:7447
```

`check` prints `ok 127.0.0.1:7447`. Stop the node with Ctrl-C; it logs `stopping`, a last `stats` line and `stopped`, and exits 0. Starting it again with the same `-identity` keeps the same key.

A peer bootstraps from the node by passing its address to `(*kolodht.Node).Bootstrap`. [examples/join](examples/join) does that with the addresses given as arguments:

```sh
go run ./examples/join 127.0.0.1:7447
```

```text
joined: 1 servers in the routing table
```

Another kolo-node joins with `-bootstrap`, a list of `host:port` entries (IPv4 addresses or DNS names), with `-bootstrap-file`, a file of one entry per line, or with both:

```sh
kolo-node run -listen 127.0.0.1:7448 -identity "$dir/state2/node.key" -bootstrap 127.0.0.1:7447
```

Without `-bootstrap`, `-bootstrap-file`, the `KOLO_BOOTSTRAP` environment variable or `-first`, `run` exits 2, so a list left empty by mistake never starts a lone node. kolo-node never uses the built-in entry nodes of kolo-dht: give it `-bootstrap`, `-bootstrap-file` or `-first`. It ships no list of public nodes; see [Public and private networks](docs/guide.md#public-and-private-networks).

## Commands

```
kolo-node run -identity FILE [flags]                 run a kolo-dht bootstrap server
kolo-node relay -identity FILE -listen ADDR [flags]  run a kolo-relay server
kolo-node check [-timeout 5s] [-relay LIST] [-relay-file FILE] [HOST:PORT...]
                                                     check that kolo-dht servers and kolo-relays answer from here
kolo-node version                                    print the module and Go versions
```

| Flag | Default | What it does |
|---|---|---|
| `-config` | none | Read the options below from a JSON file |
| `-listen` | `:7447` | IPv4 UDP address to listen on |
| `-identity` | required | Key file; created with mode 0600 on the first run and never replaced |
| `-bootstrap` | none | Entry nodes to join, comma separated, repeatable |
| `-bootstrap-file` | none | File of entry nodes, one `host:port` per line, `#` comments; repeatable |
| `-first` | false | Start a new network: no entry nodes yet |
| `-log-format`, `-log-level` | `text`, `info` | `text` or `json`; `info`, `warn` or `error` |
| `-log-addrs` | false | Log peer addresses in full instead of their /24 |
| `-stats-interval` | `5m` | How often the `stats` event is logged |
| `-dht-*` | kolo-dht defaults | The record, request and timeout limits of the DHT server |

`-bootstrap` and `-bootstrap-file` add up, without duplicates, to any number of entries. `KOLO_BOOTSTRAP` (comma separated) is read only when neither is set and `-first` is not set.

`relay` takes `-config`, `-listen` and `-identity` (its own key file), both required, `-allow-file` (the hex keys that may reserve and connect, one per line), `-log-format`, `-log-level`, one `-relay-*` flag per kolo-relay `ServerConfig` limit and the listener's `-relay-max-conns`, `-relay-max-conns-per-ip`, `-relay-stream-window`, `-relay-conn-window` and `-relay-congestion`. The windows are in bytes (64 KiB to 16 MiB per stream, 64 KiB to 64 MiB per connection, the stream window at most the connection window); 0 keeps kolo-relay's 64 KiB and 256 KiB. A session moves at most one stream window per round trip in each direction, so a longer path needs a bigger window. The price is memory: in the worst case each byte of connection window costs about 176 bytes of heap on every connection, so a 1 MiB window is about 176 MiB per connection and `-relay-max-conns` times that in all; size both together (kolo-relay's limits page has the arithmetic). The listener offers QUIC datagrams in every case, as kolo-relay's default does, so relayed packets travel as datagrams when the clients opt in with `Transport.Datagrams` in kolo-relay's `Config.Relay`. `-relay-congestion` is `reno` (the default), `cubic` or `bbr`; `bbr` keeps a long, lossy path full where the others slow down on loss, at the cost of up to 5.5 MiB more per connection and a standing queue on the path. Without `-allow-file` the relay is open: anyone may reserve and connect, at the operator's cost, and 14 addresses can fill its 56 default reservations. It logs a `WARN` event `open relay` at start that says so. With `-allow-file`, only the listed keys may reserve and only the listed keys may connect through the relay; another key that asks to reach a listed key gets the answer of a key with no reservation (`kolorelay.ErrUnavailable`), so a key that must reach a member through the relay has to be listed. A listed member's own policy (kolo-relay `Config.AllowFunc`, kolo-swarm `Config.Allow`) still decides whether a session from another listed key starts. `check` takes relays as `host:port@key` from `-relay` (comma separated, repeatable), `-relay-file` (one per line) or `KOLO_RELAYS`, and asks each for a random key without reserving anything.

Exit codes: 0 after a clean stop, 1 if the identity or the socket cannot be set up or the shutdown fails, 2 for an invalid command, flag, config file or key file.

## Network

kolo-node is IPv4 only: an IPv6 address in `-listen`, `-bootstrap`, `-bootstrap-file`, `KOLO_BOOTSTRAP`, `-relay`, `-relay-file`, `KOLO_RELAYS` or `check` is refused with exit 2. A systemd unit and the steps for a Linux VPS are in [docs/deploy.md](docs/deploy.md). Open the UDP port in the host firewall and in the provider's firewall.

## More

- [Guide](docs/guide.md): running a node, public and private networks, running a relay, the config file, `check`, log events, exit codes, design notes and references.
- [Limits](docs/limits.md): every flag with its default, range and reason.
- [Running on a VPS](docs/deploy.md): systemd unit, firewall rules and sizing.
- [Security policy](SECURITY.md).

## Status

Experimental. The API may change before v1.0.0.

## License

Licensed under either of the [Apache License, Version 2.0](LICENSE) or the [MIT license](LICENSE-MIT), at your option.
