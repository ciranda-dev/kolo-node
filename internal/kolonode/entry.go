package kolonode

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxAddrs    = 64
	maxPerEntry = 8
)

type entry struct {
	host string
	ip   netip.Addr // valid when host is an IP literal
	port uint16
}

func (e entry) String() string { return net.JoinHostPort(e.host, strconv.Itoa(int(e.port))) }

// errIPv6 is the error for an IPv6 address in the config, IPv4-mapped forms included.
var errIPv6 = errors.New("an IPv6 address is refused: kolo-node v0.1.x is IPv4 only")

// parseEntry parses "host:port" with an IPv4 literal or a DNS name, and a port from 1 to 65535.
func parseEntry(s string) (entry, error) {
	if strings.Contains(s, ",") {
		return entry{}, errors.New("want host:port")
	}
	if strings.Count(s, ":") > 1 {
		return entry{}, errIPv6
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return entry{}, errors.New("want host:port")
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil || p == 0 || strings.HasPrefix(port, "0") {
		return entry{}, errors.New("port must be a number from 1 to 65535")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !ip.Is4() {
			return entry{}, errIPv6
		}
		if scopeOf(ip) == scopeInvalid {
			return entry{}, errors.New("the address cannot be used (unspecified, multicast or reserved)")
		}
		return entry{host: ip.String(), ip: ip, port: uint16(p)}, nil
	}
	if !validName(host) {
		return entry{}, errors.New("host must be an IP address or a DNS name")
	}
	return entry{host: strings.ToLower(host), port: uint16(p)}, nil
}

// validName reports whether s is a DNS name of letters, digits and hyphens with an optional final dot, whose last
// label is not all digits (RFC 1123 section 2.1), so a mistyped IPv4 address is not taken for a name.
func validName(s string) bool {
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 {
		return false
	}
	digits := false
	for label := range strings.SplitSeq(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		digits = true
		for i := 0; i < len(label); i++ {
			switch c := label[i]; {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
				digits = false
			default:
				return false
			}
		}
	}
	return !digits
}

// resolver is the part of *net.Resolver used to resolve bootstrap names.
type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type resolved struct {
	addrs   []netip.AddrPort
	failed  map[string]error // by entry
	dropped int              // distinct addresses cut by maxPerEntry or maxAddrs and kept by no entry
	skipped int              // name addresses that keep refused
}

// resolve returns the distinct IPv4 addresses of entries, at most maxPerEntry per name and maxAddrs in all, resolving
// names in parallel within timeout each. A name's address is kept only if keep (when not nil) accepts it, so a DNS
// answer cannot point the node into a network it was not told about.
func resolve(ctx context.Context, r resolver, entries []entry, timeout time.Duration, keep func(netip.Addr) bool) resolved {
	type answer struct {
		ips []netip.Addr
		err error
	}
	answers := make([]answer, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		if e.ip.IsValid() {
			answers[i].ips = []netip.Addr{e.ip}
			continue
		}
		wg.Go(func() {
			rctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			answers[i].ips, answers[i].err = r.LookupNetIP(rctx, "ip4", e.host)
		})
	}
	wg.Wait()

	out := resolved{failed: map[string]error{}}
	seen := map[netip.AddrPort]bool{}
	// An address cut by a cap stays unseen, so another entry can still bring it.
	cut := map[netip.AddrPort]bool{}
	// Literals first, so DNS answers cannot push a configured address out of the maxAddrs cap.
	for _, literals := range []bool{true, false} {
		for i, e := range entries {
			if e.ip.IsValid() != literals {
				continue
			}
			if answers[i].err != nil {
				out.failed[e.String()] = answers[i].err
				continue
			}
			n := 0
			for _, ip := range answers[i].ips {
				ap := netip.AddrPortFrom(ip.Unmap(), e.port)
				if seen[ap] {
					continue
				}
				switch {
				case !literals && (!ap.Addr().Is4() || keep != nil && !keep(ap.Addr())):
					seen[ap] = true
					out.skipped++
				case n >= maxPerEntry || len(out.addrs) >= maxAddrs:
					cut[ap] = true
				default:
					seen[ap] = true
					delete(cut, ap)
					out.addrs = append(out.addrs, ap)
					n++
				}
			}
		}
	}
	out.dropped = len(cut)
	return out
}

// Address scopes, from the narrowest, as kolo-dht defines them (its internal/addrs package).
const (
	scopeInvalid = iota
	scopeLoopback
	scopeLinkLocal
	scopePrivate
	scopeGlobal
)

var (
	cgnat       = netip.MustParsePrefix("100.64.0.0/10")
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	reserved    = netip.MustParsePrefix("240.0.0.0/4")
)

// scopeOf is kolo-dht's scope: anything but a unicast IPv4 address outside 0.0.0.0/8 and 240.0.0.0/4 is invalid.
func scopeOf(ip netip.Addr) int {
	ip = ip.Unmap()
	switch {
	case !ip.Is4(), ip.IsUnspecified(), ip.IsMulticast(), thisNetwork.Contains(ip), reserved.Contains(ip):
		return scopeInvalid
	case ip.IsLoopback():
		return scopeLoopback
	case ip.IsLinkLocalUnicast():
		return scopeLinkLocal
	case ip.IsPrivate(), cgnat.Contains(ip):
		return scopePrivate
	}
	return scopeGlobal
}

// reachableFrom returns kolo-dht's rule for addresses learned from others: global, or in the scope of local.
// An unspecified local address has no scope, so only global addresses pass.
func reachableFrom(local netip.Addr) func(netip.Addr) bool {
	ls := scopeOf(local)
	return func(ip netip.Addr) bool {
		s := scopeOf(ip)
		return s != scopeInvalid && (s == scopeGlobal || s == ls)
	}
}
