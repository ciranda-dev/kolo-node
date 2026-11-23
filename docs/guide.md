# Guide

How to run, configure and monitor kolo-node. Defaults and bounds are in [limits.md](limits.md); a systemd setup is in [deploy.md](deploy.md).

## Run a node and check it

Run a node on loopback with its key in a new temporary directory, and check it from a second terminal:

```sh
dir=$(mktemp -d)
kolo-node run -listen 127.0.0.1:7447 -identity "$dir/state/node.key" -first
```

```sh
kolo-node check 127.0.0.1:7447
```

`check` prints `ok 127.0.0.1:7447`. Stop the node with Ctrl-C; it logs `stopping`, a last `stats` line and `stopped`, and exits 0. Starting it again with the same `-identity` keeps the same key.

`-first` starts a new network: the node has no entry nodes yet and waits for others. Another kolo-node joins with `-bootstrap`, a comma-separated list of `host:port` entries (IPv4 addresses or DNS names, resolved again at each attempt; see the `bootstrap` event below):

```sh
kolo-node run -listen 127.0.0.1:7448 -identity "$dir/state2/node.key" -bootstrap 127.0.0.1:7447
```

## Public and private networks

A network is the set of kolo-dht servers that know each other, and a node is in the network of the entry nodes it bootstraps from. kolo-node ships no list of entry nodes, and `run` needs one of these:

- `-bootstrap host:port,...`: entry nodes, comma separated; the flag can be repeated.
- `-bootstrap-file path`: a text file of entry nodes, one `host:port` per line. `#` starts a comment, to the end of the line; blank lines are ignored; a UTF-8 BOM and CRLF line ends are accepted. Only a regular file of at most 64 KiB is read: a directory, device or FIFO is refused before it is opened. The flag can be repeated, and it adds up with `-bootstrap`: the `-bootstrap` entries first, then each file's in order, without duplicates.
- `KOLO_BOOTSTRAP=host:port,...`: read only when neither `-bootstrap` nor `-bootstrap-file` is set (on the command line or in the config file, even to an empty list) and `-first` is not set. A set `KOLO_BOOTSTRAP` that is not read logs the warning `KOLO_BOOTSTRAP ignored`.
- `-first`: start a new network, with no entry nodes yet. It cannot be combined with `-bootstrap` or `-bootstrap-file`.

With none of them, or with only empty lists, `run` exits 2: a list left empty by mistake never starts a lone node, and a flag or key set to an empty list never falls back to `KOLO_BOOTSTRAP`. A relative `-bootstrap-file` path, in the config file too, is relative to the working directory of `kolo-node`, not to the config file; systemd units should use absolute paths. Each config array element is one entry: an element with a comma is refused.

An entry is an IPv4 address or a DNS name, with a port from 1 to 65535. IPv6 in any form, IPv4-mapped included, is refused. A bootstrap file:

```text
# entry nodes of our network
203.0.113.10:7447
node2.example.org:7447   # resolved to IPv4 at each attempt
```

A public network is one whose operators publish their entry nodes; members list them. A private network runs its own: start its first node with `-first`, the others with `-bootstrap` (or a file) pointing at it, and give members only these addresses. Run at least two entry nodes on hosts that stay up, with their UDP port open. Two nodes of a private network on loopback:

```sh
dir=$(mktemp -d)
kolo-node run -listen 127.0.0.1:7447 -identity "$dir/a/node.key" -first
kolo-node run -listen 127.0.0.1:7448 -identity "$dir/b/node.key" -bootstrap 127.0.0.1:7447
```

What it is: separation by topology. A kolo-dht server learns other servers only from the servers it asks and from servers that send it requests, so two networks with different entry nodes never see each other's servers, records or peers.

What it is not: access control. The list is not a secret. Anyone who learns one entry address can bootstrap from it, look up topics and announce on them, and a node that lists entry nodes of two networks joins them. Decide who may connect with kolo-secure keys and kolo-swarm's `Allow`, admit members with kolo-invite, and trust only signed records.

## Run a relay

`kolo-node relay` runs a [kolo-relay](https://github.com/ciranda-dev/kolo-relay): peers that cannot connect directly, such as two computers behind firewalls that let only outgoing traffic through, reserve on it and meet through it. Each side runs its own kolo-secure connection end to end through the relay, so it copies bytes it cannot read. kolo-swarm uses it when its `Config.Relays` lists it. A relay has its own key file and its own UDP port; one process is one relay, and an operator runs several relays as several processes. On loopback, with its key in a new temporary directory:

```sh
dir=$(mktemp -d)
kolo-node relay -listen 127.0.0.1:7448 -identity "$dir/relay/relay.key"
```

It logs `identity` with its `key` and `started` with its `addr`, as `run` does; peers list it as `host:port@key`, for example `203.0.113.20:7448@<key>` with the logged key. Check it from a second terminal with `kolo-node check -relay 127.0.0.1:7448@<key>`, which prints `ok relay 127.0.0.1:7448@<key>`.

Without `-allow-file` the relay is open: anyone may reserve and connect, at the operator's cost, and 14 addresses can fill its 56 default reservations at 4 connections per address. It logs a `WARN` event `open relay` at start, whose `reason` says that anyone may reserve and names `-allow-file`; a relay that is not meant for everyone must be given `-allow-file`. With `-allow-file path`, only the keys in the file may reserve and only the keys in the file may connect through the relay: one key per line as 64 hex characters, `#` comments, blank lines ignored, a UTF-8 BOM and CRLF accepted, a regular file of at most 64 KiB, read at start. Another key that asks to reach a listed key gets the answer of a key with no reservation (`kolorelay.ErrUnavailable`), so a key that must reach a member through the relay has to be listed. A listed member's own policy (kolo-relay `Config.AllowFunc`, kolo-swarm `Config.Allow`) still decides whether a session from another listed key starts. A file with no key is refused. The limits of kolo-relay's `ServerConfig` are flags (`-relay-max-reservations` and so on, see [limits.md](limits.md)); 0 keeps kolo-relay's default, and kolo-relay checks the ranges (exit 2). `-relay-max-conns`, `-relay-max-conns-per-ip`, `-relay-stream-window`, `-relay-conn-window` and `-relay-congestion` set the listener's limits; with any of them set, the command sets every listener field to kolo-relay's own values (`MaxDialed` 1, `MaxIncomingStreams` 2, windows 64 KiB and 256 KiB, Reno, datagrams on) except the ones given, and raises `MaxMemory` above its 1 GiB default when (`-relay-max-conns` + 1) × (the connection window + 240 KiB of datagram queues) needs more, about 4.7 GiB of receive window and datagram queues at 10001 connections and 256 KiB. `-relay-max-conns` has no maximum of its own: it must be at least 1, and (`-relay-max-conns` + 1) × (the connection window + 240 KiB of datagram queues) may not exceed kolo-transport's 64 GiB `MaxMemory` limit (135299 connections at 256 KiB), which exits with code 2. A session moves at most one stream window per round trip in each direction, and each byte of connection window costs about 176 bytes of heap on every connection in the worst case (kolo-relay's limits page), so a 1 MiB connection window is about 176 MiB per connection and `-relay-max-conns` times that in all: raise the windows only as far as the path needs and lower `-relay-max-conns` to match. `bbr` keeps a long, lossy path full where `reno` and `cubic` slow down on loss; it adds up to 5.5 MiB per connection and holds a standing queue. A value outside the ranges, or a stream window above the connection window, exits with code 2 and names the flag. Every client behind one address counts against `-relay-max-conns-per-ip`: a member needs one connection for its reservation, a second for a while after each lifetime end, and one per relayed partner it starts, so size it as kolo-relay's limits page says ("Sizing a relay for a swarm"). The relay logs no peer address and no key of its clients.

## Join from Go

A peer bootstraps from the node by passing its address to `(*kolodht.Node).Bootstrap`. This program takes the addresses as arguments, such as `go run ./examples/join 127.0.0.1:7447`. It listens on `127.0.0.1:0` for local runs; to reach a node on a VPS, give it `-listen :0`, as in `go run ./examples/join -listen :0 203.0.113.10:7447`:

```go
// Command join starts a kolo-dht client and bootstraps it from the kolo-node servers given as arguments.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "UDP address of this client; \":0\" to reach servers on other hosts")
	flag.Parse()
	if flag.NArg() == 0 {
		log.Fatal("usage: join [-listen ADDR] IP:PORT...")
	}
	var servers []netip.AddrPort
	for _, a := range flag.Args() {
		ap, err := netip.ParseAddrPort(a)
		if err != nil {
			log.Fatal(err)
		}
		servers = append(servers, ap)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	id, err := koloid.Generate()
	if err != nil {
		log.Fatal(err)
	}
	n, err := kolodht.New(ctx, *listen, id, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer n.Close()
	if err := n.Bootstrap(ctx, servers); err != nil {
		log.Fatal(err)
	}
	fmt.Println("joined:", n.Stats().Servers, "servers in the routing table")
}
```

Give peers the address of your nodes, or a DNS name for it, as their bootstrap list: `Bootstrap` or kolo-swarm's `Config.BootstrapNodes` in Go, `-bootstrap` or `-bootstrap-file` for another kolo-node.

## Command line

kolo-node has no importable package. Its interface is the command line, the config file, the log events and the exit codes.

```
kolo-node run -identity FILE [flags]                 run a kolo-dht bootstrap server
kolo-node relay -identity FILE -listen ADDR [flags]  run a kolo-relay server
kolo-node check [-timeout 5s] [-relay LIST] [-relay-file FILE] [HOST:PORT...]
                                                     check that kolo-dht servers and kolo-relays answer from here
kolo-node version                                    print the module and Go versions
```

Precedence: defaults, then the config file, then the flags given on the command line. A flag replaces the file's value; `-bootstrap` and `-bootstrap-file` each replace the whole list of their key (`bootstrap` and `bootstrap-file` are arrays of strings in the file). `KOLO_BOOTSTRAP` comes last, only when neither list is set and `-first` is not.

The config file is one JSON object of at most 64 KiB. Keys match exactly and once; an unknown, repeated or differently cased key, `null`, a number that is not an integer, or anything after the object is an error. Durations are strings such as `"30s"`.

## Check

`check` resolves each argument (a DNS name to at most 8 addresses) and, for
each address, starts a new kolo-dht client with a key held only in memory
that refuses inbound connections, and calls `Bootstrap`: one PING to the
address, then, if it answered, a lookup of the client's own ID through the
servers that address names (kolo-dht asks at most 100 per lookup, only
public ones or ones in the scope of the address that named them). Each
request waits at most half of `-timeout` (1s to 1m), which bounds each
address. It prints `ok ADDRESS` if `Bootstrap` succeeded, or if the address
answered the PING and only the lookup after it ran out of time (the PING
ends within half the timeout, so the deadline is reached only after an
answer); otherwise `failed ADDRESS: reason`. Other servers that ping the
client do not count. An argument that is not `host:port`, or a literal
address that cannot be used (unspecified, multicast, reserved), exits 2.

`check` also checks kolo-relays, given as `host:port@key` (the key as 64
hex characters) with `-relay` (comma separated, repeatable), `-relay-file`
(one per line, `#` comments, repeatable, a regular file of at most 64 KiB)
or `KOLO_RELAYS` (comma separated), read only when neither flag is set,
even to an empty list. They add up without duplicates; one
key at two addresses or one address with two keys is refused (exit 2).
For each address of each relay, `check` dials it with a new key held only
in memory and asks for another new random key: the relay's "unavailable"
answer proves the address, the relay's key (another key is refused) and
the protocol, and nothing is reserved. It prints `ok relay ADDRESS@KEY`
or `failed relay ADDRESS@KEY: reason`, after the DHT servers, and exits 0
only if every server and relay answered.

## Log events

Log events (slog, to stderr), with their attributes:

- `identity`: `key` (public key, hex), `created`.
- `started`: `version`, `addr` (the node's own socket), and the limits that
  were set.
- `network`: `mode` (`first` for a new network, `join` otherwise),
  `entries` (the entry nodes listed) and, with `join`, `from` (`-bootstrap`,
  `-bootstrap-file` or `KOLO_BOOTSTRAP`). The warning `KOLO_BOOTSTRAP
  ignored` (`because`: `-first`, or `-bootstrap or -bootstrap-file`)
  follows when that variable is set but not read.
- `bootstrap`: `addrs`, `servers` (routing table size). Warnings when it
  did not work: `resolve failed`, `bootstrap addresses skipped` (`skipped`:
  how many addresses a name resolved to outside the scope of the listen
  address, such as private ones for a public node; only literal addresses
  may be private or loopback), `too many bootstrap addresses` and
  `bootstrap failed` (with `retry`, the wait before the next attempt).
  Names are resolved in parallel, each within 5s, and `Bootstrap` then gets
  30s of its own. The node has joined once a listed address answered its
  PING as a server; if the lookup after that answer runs out of time,
  `bootstrap` says `lookup=cut short` and it still counts. Every PING gets
  a request slot at once (hence at least 75 for `-dht-max-inflight`) and
  waits at most 10s, so the deadline is only reached after an answer. Until
  it has joined, the node tries again after 1 minute, doubling the wait
  after each failure up to 5 minutes; other servers entering its table do
  not count. Once joined, it stays joined: every 5 minutes, if its table
  holds fewer than 20 servers, it resolves and bootstraps again, which also
  follows DNS changes, and a failed refresh is tried again with the same
  backoff, each time only while the table is still that small. It keeps
  serving meanwhile. Literal addresses are kept before any name's, and a
  literal that cannot be used (unspecified, multicast, reserved) is a
  config error.
- `stats`, every `-stats-interval` and at shutdown: `servers`, `records`,
  and since the previous one `requests` (within the rate limits),
  `limited` (dropped by them), `closed` (inbound connections that got past
  the refusal and were closed; normally 0), `since`.
- `stopping` (`cause`), `stopped`; errors: `identity failed`, `start
  failed`, `close failed`, `shutdown timed out`.
- `relay` logs `identity`, `started` (with the limits that were set),
  `allow` (`keys`) or the warning `open relay` (`reason`), `stopping`,
  `stopped` and the same errors. kolo-relay has no counters, so there is
  no `stats` event.
- quic-go's warning when the OS refuses the UDP buffer size it asks for.

Logs never contain the seed, packet contents, topics, record keys, or a
peer address beyond its /24 unless `-log-addrs` is set. Nothing is
logged per packet.

## Exit codes

Exit codes: 0 after SIGINT or SIGTERM once everything is closed; 1 if the
identity or the socket cannot be set up, or the shutdown fails or takes over
10s; 2 for an invalid command, flag, config file or kolo-dht limit, and for
a key file or key directory that kolo-id refuses (wrong size, not a regular
file, or open to others). A second signal during the shutdown ends the
process at once.

## Design

- `run` is a kolo-dht server with `Server: true` and nothing else: no
  relay of application data, no announcements of its own, no outbound
  connections. Relaying is the separate `relay` command, with its own key
  and port, so a bootstrap node never carries application traffic unless
  its operator starts one.
- Nobody needs a kolo-secure connection to a bootstrap node, so
  kolo-transport's `AcceptFunc` refuses every attempt after Retry, before
  any handshake work; any connection that still reached `Accept` would be
  closed at once, with kolo-secure and kolo-transport capped at 16 pending
  and 16 connections, one stream and 64 KiB windows.
- The key file is created with `SaveNew` only after the socket is open, so
  a config or listen error leaves nothing on disk; an existing file is
  never replaced. kolo-dht derives a server's DHT ID from its IP and
  a random choice made at each start, and its PINGs are not authenticated,
  so the stable key identifies the operator's node but no peer checks it.
- Config file in JSON with the standard library, decoded strictly token by
  token (encoding/json v1 alone would accept repeated keys and any case);
  flags override it, like most daemons. Its path must name a regular file
  before it is opened, so a device is never opened; it is then opened
  without blocking and checked again through the open file, so a FIFO
  swapped in between cannot stall the start.
- A DNS name in `-bootstrap` only leads to public addresses, or to ones in
  the scope of the listen address (kolo-dht's rule for addresses learned
  from other nodes), so whoever controls the name cannot point the node
  into a private network; literal addresses are used as configured.
- IPv4 only, like kolo-dht.
- Logs are events, not traces: their number does not grow with traffic, so
  a flood cannot fill the disk. Bootstrap peers' addresses are cut to the
  /24 that kolo-dht's limits group them by.
- No HTTP endpoint: health is the `stats` event, and reachability is tested
  from outside with `check`, which uses the real UDP path; a local health
  URL would pass while the UDP port is blocked.
- `check` exists because a UDP port cannot be probed with `nc` or a
  browser: only a reply from a kolo-dht server proves the path.
- Port 7447: unassigned at IANA, above 1024 and below Linux's ephemeral
  range.
- The systemd unit runs as a static system user rather than
  `DynamicUser=`: systemd.exec reaches a dynamic user's state directory
  through a symlink, and kolo-id refuses a key directory that is a
  symlink.

## References

- [RFC 1123, Requirements for Internet Hosts - Application and Support](https://www.rfc-editor.org/info/rfc1123) (Internet Standard, October 1989).
  Takes: the host name syntax check (section 2.1).

## Operations links

- [kolo-dht](https://github.com/ciranda-dev/kolo-dht), [kolo-id](https://github.com/ciranda-dev/kolo-id)
- [systemd.exec(5)](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)
- [os/signal](https://pkg.go.dev/os/signal), [log/slog](https://pkg.go.dev/log/slog), [encoding/json](https://pkg.go.dev/encoding/json)
- [ufw(8)](https://manpages.ubuntu.com/manpages/noble/en/man8/ufw.8.html), [nftables quick reference](https://wiki.nftables.org/wiki-nftables/index.php/Quick_reference-nftables_in_10_minutes), [nftables server example](https://wiki.nftables.org/wiki-nftables/index.php/Simple_ruleset_for_a_server), [nft(8)](https://www.netfilter.org/projects/nftables/manpage.html)
- Linux conntrack for ICMP errors and neighbor discovery: [nf_conntrack_proto_icmp.c](https://github.com/torvalds/linux/blob/master/net/netfilter/nf_conntrack_proto_icmp.c), [nf_conntrack_proto_icmpv6.c](https://github.com/torvalds/linux/blob/master/net/netfilter/nf_conntrack_proto_icmpv6.c), [nft_ct.c](https://github.com/torvalds/linux/blob/master/net/netfilter/nft_ct.c)
- [IANA service name and port number registry](https://www.iana.org/assignments/service-names-port-numbers/service-names-port-numbers.xhtml)

