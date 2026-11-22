# Running on a VPS

These steps set up a node on a Linux VPS with systemd, as root (`sudo`).
`203.0.113.10` stands for the VPS's public address.

Build and install the binary, on the VPS or cross-compiled:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o kolo-node ./cmd/kolo-node
sudo install -m 0755 kolo-node /usr/local/bin/kolo-node
```

Create a system user and the config file `/etc/kolo-node/config.json`
(mode 0644; it holds no secret):

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin kolo-node
sudo mkdir -p /etc/kolo-node
```

```json
{
  "listen": ":7447",
  "identity": "/var/lib/kolo-node/identity.key",
  "bootstrap": ["node2.example.org:7447"],
  "log-format": "text"
}
```

List the other bootstrap nodes of your network in `bootstrap`, or put them
in a file named by `bootstrap-file`. The same list on every node is fine: a
node ignores its own address. The first node of a new network has no list
yet: give it `"first": true` instead of `bootstrap`.

Let quic-go have the UDP buffers it asks for, in
`/etc/sysctl.d/90-kolo-node.conf`, then run `sudo sysctl --system`:

```
net.core.rmem_max=7500000
net.core.wmem_max=7500000
```

Open UDP port 7447 in the host firewall, and in the provider's firewall or
security group if it has one. With ufw, allow your SSH port first, since
enabling ufw can drop the SSH session (22 here; for example `sudo ufw allow
2222/tcp` if SSH listens on 2222):

```sh
sudo ufw allow 22/tcp
sudo ufw allow 7447/udp
sudo ufw enable
sudo ufw status verbose
```

With nftables, insert the rule at the top of the input chain of your
ruleset, here a table `inet filter` with a chain `input`, so it comes
before any final drop (`nft add` would append it after one):

```sh
sudo nft insert rule inet filter input meta nfproto ipv4 udp dport 7447 accept
```

To keep it across reboots, add the same `meta nfproto ipv4 udp dport 7447
accept` line to the input chain of your distribution's ruleset in
`/etc/nftables.conf`, before any drop, rather than replacing that ruleset.
If the host has no ruleset yet, this complete `/etc/nftables.conf` is
based on the nftables wiki's server example. Replies to the host's own
traffic, the node's pings included, come in as established. ICMP errors
about its connections, path MTU messages included, come in as related;
conntrack marks any other ICMP error invalid, and it is dropped.
`meta nfproto ipv6 drop` is there because kolo-node is IPv4 only and
IPv6 input is refused, so nothing new may come in over IPv6, SSH included:
connect over IPv4. The rules above it keep the host's own IPv6 working:
neighbor and router discovery, multicast listener queries and DHCPv6
replies, which a cloud's IPv6 address and route depend on.
`flush ruleset` removes every other ruleset on the host, including ones
that other software (such as a container runtime) loaded. Check the file
with `sudo nft -c -f /etc/nftables.conf` before loading it:

```
#!/usr/sbin/nft -f

flush ruleset

table inet filter {
    chain input {
        type filter hook input priority filter; policy drop;
        iif lo accept
        ct state invalid drop
        ct state established,related accept
        icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-advert } accept
        ip6 saddr fe80::/10 icmpv6 type mld-listener-query accept
        ip6 saddr fe80::/10 udp sport 547 udp dport 546 accept
        meta nfproto ipv6 drop
        icmp type echo-request limit rate 5/second accept
        tcp dport 22 accept # your SSH port, if it is not 22
        udp dport 7447 accept
    }
    chain forward {
        type filter hook forward priority filter; policy drop;
    }
    chain output {
        type filter hook output priority filter; policy accept;
    }
}
```

Install the unit `deploy/kolo-node.service` as
`/etc/systemd/system/kolo-node.service`:

```ini
# This unit runs `kolo-node run`, a kolo-dht bootstrap node.
# To run `kolo-node relay` instead, change ExecStart and give it its own key file and UDP port.
# A relay without -allow-file is OPEN: anyone may reserve and use its bandwidth. It logs the
# warning "open relay" at start; pass -allow-file with the keys that may reserve and connect.
[Unit]
Description=Kolo DHT bootstrap node
Documentation=https://github.com/ciranda-dev/kolo-node
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=exec
ExecStart=/usr/local/bin/kolo-node run -config /etc/kolo-node/config.json
Restart=on-failure
RestartSec=5s
RestartPreventExitStatus=2
User=kolo-node
Group=kolo-node
StateDirectory=kolo-node
StateDirectoryMode=0700
UMask=0077
CapabilityBoundingSet=
AmbientCapabilities=
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
PrivateUsers=yes
ProtectSystem=strict
ProtectHome=yes
ProtectClock=yes
ProtectControlGroups=yes
ProtectHostname=yes
ProtectKernelLogs=yes
ProtectKernelModules=yes
ProtectKernelTunables=yes
ProtectProc=invisible
ProcSubset=pid
RestrictAddressFamilies=AF_INET AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RemoveIPC=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM
MemoryMax=512M
Environment=GOMEMLIMIT=480MiB

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now kolo-node
journalctl -u kolo-node -f
```

`StateDirectoryMode=0700` is required: kolo-id refuses a key file in a
directory that others can read. Such a key file error, or a bad config,
exits 2, which the unit does not restart (`RestartPreventExitStatus=2`);
other failures restart after 5s, and `StartLimitIntervalSec=0` keeps
systemd from giving up after a burst of them. The node needs no
capability, since 7447 is above 1024.

In practice the live heap is the record store and little else: about 44
MiB when full (65536 records of about 710 bytes, measured by kolo-dht's
TestHeapPerRecord). The worst case adds about 368 MiB for the refused
listener's 16 connections (see [limits.md](limits.md)), which `AcceptFunc` keeps from
opening, for about 416 MiB. The Go runtime holds about 8% more than the
live heap (stacks, metadata, fragmentation), about 449 MiB. Without a
limit it lets the heap grow to about twice the live heap, which is more
than `MemoryMax`. `GOMEMLIMIT=480MiB` is above 449 MiB, so the runtime does
not collect without pause, and 32 MiB below `MemoryMax`, so it collects
before the kernel kills the node. Raise both together with the record limits.

`RestrictAddressFamilies=AF_INET AF_UNIX` also blocks DNS over IPv6, so on a
host whose nameservers are all IPv6, use IP literals in `-bootstrap` (or
the bootstrap file) or an IPv4 resolver.

Check the port from another machine:

```sh
kolo-node check 203.0.113.10:7447
```

`ok` means a kolo-dht server answered from that address. If it says
`failed`, run `ss -ulpn 'sport = :7447'` on the VPS to see the socket and
`sudo tcpdump -ni any udp port 7447` to see whether the check's packets
arrive.

Give peers the address, `203.0.113.10:7447`, or a DNS name for it, as
their bootstrap list: `Bootstrap` in Go (see [guide.md](guide.md)), `-bootstrap` or
`-bootstrap-file` for another kolo-node.
