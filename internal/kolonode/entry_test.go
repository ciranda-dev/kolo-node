package kolonode

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestScopeTable is the scope table of kolo-dht's TestScope (internal/addrs): scopeOf is a copy of kolo-dht's ScopeOf,
// so when a row changes there, change it here.
func TestScopeTable(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want int
	}{
		{"127.0.0.1", scopeLoopback},
		{"169.254.1.1", scopeLinkLocal},
		{"10.1.2.3", scopePrivate},
		{"192.168.1.1", scopePrivate},
		{"172.16.0.1", scopePrivate},
		{"100.64.0.1", scopePrivate},
		{"203.0.113.7", scopeGlobal},
		{"8.8.8.8", scopeGlobal},
		{"198.18.0.1", scopeGlobal},
		{"0.0.0.0", scopeInvalid},
		{"0.1.2.3", scopeInvalid},
		{"224.0.0.1", scopeInvalid},
		{"240.0.0.1", scopeInvalid},
		{"255.255.255.255", scopeInvalid},
		{"::1", scopeInvalid},
	} {
		if got := scopeOf(netip.MustParseAddr(tc.ip)); got != tc.want {
			t.Errorf("scopeOf(%s) = %d, want %d", tc.ip, got, tc.want)
		}
	}
}

func TestParseEntry(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"203.0.113.10:7447", "203.0.113.10:7447"},
		{"Node1.Example.ORG:65535", "node1.example.org:65535"},
		{"node-1.example.org.:1", "node-1.example.org.:1"},
		{"localhost:7447", "localhost:7447"},
		{"123.example.org:1", "123.example.org:1"},
	} {
		e, err := parseEntry(tc.in)
		if err != nil || e.String() != tc.want {
			t.Errorf("parseEntry(%q) = %v, %v, want %s", tc.in, e, err, tc.want)
		}
	}
	for _, in := range []string{
		"", "203.0.113.10", "203.0.113.10:0", "203.0.113.10:65536", "203.0.113.10:07447", "203.0.113.10:+1",
		"203.0.113.10:http", "2001:db8::1:7447", "[fe80::1%eth0]:7447", ":7447", "-a.example.org:1",
		"a-.example.org:1", "a..example.org:1", "a_b.example.org:1", "a b:1", strings.Repeat("a", 64) + ".org:1",
		strings.Repeat("a.", 127) + "org:1",
		"0.0.0.0:7447", "0.1.2.3:1", "224.0.0.1:1", "255.255.255.255:1", "240.0.0.1:1",
		"1.2.3:7447", "300.1.1.1:7447", "a.example.123:1",
	} {
		if e, err := parseEntry(in); err == nil {
			t.Errorf("parseEntry(%q) = %v, want an error", in, e)
		}
	}
}

func TestParseEntryRefusesIPv6(t *testing.T) {
	for _, in := range []string{"[2001:db8::1]:7447", "[::ffff:203.0.113.10]:7447", "[::1]:7447", "[::]:7447", "[64:ff9b::cb00:710a]:7447", "::ffff:203.0.113.10:7447", "::1:7447"} {
		_, err := parseEntry(in)
		if err == nil || !strings.Contains(err.Error(), "IPv6") || !strings.Contains(err.Error(), "IPv4 only") {
			t.Errorf("parseEntry(%q) = %v, want an error saying IPv6 is refused and v0.1.x is IPv4 only", in, err)
		}
	}
}

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ips, ok := f[host]; ok {
		return ips, nil
	}
	return nil, errors.New("no such host")
}

func entries(t *testing.T, ss ...string) []entry {
	t.Helper()
	out := make([]entry, len(ss))
	for i, s := range ss {
		e, err := parseEntry(s)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = e
	}
	return out
}

func TestResolve(t *testing.T) {
	r := fakeResolver{
		"a.example.org": {netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("203.0.113.2"), netip.MustParseAddr("203.0.113.3")},
	}
	got := resolve(t.Context(), r, entries(t, "a.example.org:7447", "203.0.113.1:7447", "203.0.113.2:7447", "b.example.org:1", "203.0.113.1:7448"), time.Second, nil)
	want := []netip.AddrPort{ // literals first, then the name's new addresses
		netip.MustParseAddrPort("203.0.113.1:7447"),
		netip.MustParseAddrPort("203.0.113.2:7447"),
		netip.MustParseAddrPort("203.0.113.1:7448"),
		netip.MustParseAddrPort("203.0.113.3:7447"),
	}
	if !reflect.DeepEqual(got.addrs, want) || got.dropped != 0 || got.skipped != 0 {
		t.Fatalf("addrs = %v (dropped %d, skipped %d), want %v", got.addrs, got.dropped, got.skipped, want)
	}
	if len(got.failed) != 1 || got.failed["b.example.org:1"] == nil {
		t.Fatalf("failed = %v, want b.example.org:1", got.failed)
	}
}

// networkResolver answers "ip4" lookups with 16-byte IPv4-mapped values and any other network with an IPv6 address too.
type networkResolver struct {
	mu       sync.Mutex
	networks []string
}

func (r *networkResolver) LookupNetIP(_ context.Context, network, _ string) ([]netip.Addr, error) {
	r.mu.Lock()
	r.networks = append(r.networks, network)
	r.mu.Unlock()
	ips := []netip.Addr{netip.MustParseAddr("::ffff:203.0.113.5")}
	if network != "ip4" {
		ips = append(ips, netip.MustParseAddr("2001:db8::5"))
	}
	return ips, nil
}

func TestResolveIPv4Only(t *testing.T) {
	r := &networkResolver{}
	got := resolve(t.Context(), r, entries(t, "a.example.org:1"), time.Second, nil)
	if !reflect.DeepEqual(r.networks, []string{"ip4"}) {
		t.Fatalf("looked up %v, want only \"ip4\"", r.networks)
	}
	if want := []netip.AddrPort{netip.MustParseAddrPort("203.0.113.5:1")}; !reflect.DeepEqual(got.addrs, want) {
		t.Fatalf("addrs = %v, want %v (unmapped)", got.addrs, want)
	}
	v6 := fakeResolver{"a.example.org": {netip.MustParseAddr("2001:db8::5"), netip.MustParseAddr("203.0.113.6")}}
	got = resolve(t.Context(), v6, entries(t, "a.example.org:1"), time.Second, nil)
	if want := []netip.AddrPort{netip.MustParseAddrPort("203.0.113.6:1")}; !reflect.DeepEqual(got.addrs, want) || got.skipped != 1 {
		t.Fatalf("addrs = %v (skipped %d), want %v and the IPv6 answer skipped", got.addrs, got.skipped, want)
	}
}

func TestResolveCaps(t *testing.T) {
	r := fakeResolver{}
	var names []string
	for n := range 10 {
		var ips []netip.Addr
		for i := range 12 {
			ips = append(ips, netip.AddrFrom4([4]byte{203, 0, byte(n), byte(i + 1)}))
		}
		name := "n" + strconv.Itoa(n) + ".example.org"
		r[name] = ips
		names = append(names, name+":1")
	}
	got := resolve(t.Context(), r, entries(t, names[:1]...), time.Second, nil)
	if len(got.addrs) != maxPerEntry || got.dropped != 12-maxPerEntry {
		t.Fatalf("one name: %d addresses, %d dropped, want %d and %d", len(got.addrs), got.dropped, maxPerEntry, 12-maxPerEntry)
	}
	got = resolve(t.Context(), r, entries(t, names...), time.Second, nil)
	if len(got.addrs) != maxAddrs || got.dropped != 10*12-maxAddrs {
		t.Fatalf("ten names: %d addresses, %d dropped, want %d and %d", len(got.addrs), got.dropped, maxAddrs, 10*12-maxAddrs)
	}
}

func TestResolveParallel(t *testing.T) {
	const n = 4
	var mu sync.Mutex
	arrived := 0
	all := make(chan struct{})
	// A barrier: each lookup waits until all n have started, so a serial resolve cannot get past the first one.
	r := stubResolver(func(ctx context.Context, host string) ([]netip.Addr, error) {
		mu.Lock()
		if arrived++; arrived == n {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
			return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
		case <-time.After(2 * time.Second):
			return nil, errors.New("not all lookups started")
		}
	})
	got := resolve(t.Context(), r, entries(t, "a.example.org:1", "b.example.org:1", "c.example.org:1", "d.example.org:1"), 5*time.Second, nil)
	mu.Lock()
	defer mu.Unlock()
	if arrived != n || len(got.failed) != 0 {
		t.Fatalf("%d lookups made, %d failed waiting for the others: names are not resolved in parallel", arrived, len(got.failed))
	}

	slow := stubResolver(func(ctx context.Context, host string) ([]netip.Addr, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	start := time.Now()
	got = resolve(t.Context(), slow, entries(t, "slow.example.org:1"), 300*time.Millisecond, nil)
	if d := time.Since(start); d > 2*time.Second || len(got.failed) != 1 {
		t.Fatalf("a name that never answers took %v (failed %v), want its own timeout", d, got.failed)
	}
}

func TestResolveLiteralsFirst(t *testing.T) {
	r := fakeResolver{}
	var list []string
	for n := range 8 {
		var ips []netip.Addr
		for i := range 8 {
			ips = append(ips, netip.AddrFrom4([4]byte{203, 0, byte(n), byte(i + 1)}))
		}
		name := "n" + strconv.Itoa(n) + ".example.org"
		r[name] = ips
		list = append(list, name+":1")
	}
	list = append(list, "198.51.100.1:1")
	got := resolve(t.Context(), r, entries(t, list...), time.Second, nil)
	if len(got.addrs) != maxAddrs || !slices.Contains(got.addrs, netip.MustParseAddrPort("198.51.100.1:1")) {
		t.Fatalf("%d addresses, literal kept: %v; want %d with the literal", len(got.addrs), slices.Contains(got.addrs, netip.MustParseAddrPort("198.51.100.1:1")), maxAddrs)
	}
	if got.dropped != 1 {
		t.Fatalf("dropped %d, want 1", got.dropped)
	}

	twice := fakeResolver{"a.example.org": {netip.MustParseAddr("10.0.0.1")}, "b.example.org": {netip.MustParseAddr("10.0.0.1")}}
	got = resolve(t.Context(), twice, entries(t, "a.example.org:1", "b.example.org:1"), time.Second, reachableFrom(netip.IPv4Unspecified()))
	if got.skipped != 1 {
		t.Fatalf("skipped %d, want the same address counted once", got.skipped)
	}
}

func TestResolveScope(t *testing.T) {
	ips := []netip.Addr{
		netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("169.254.1.1"),
		netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("0.0.0.1"), netip.MustParseAddr("224.0.0.1"),
		netip.MustParseAddr("192.168.1.1"), netip.MustParseAddr("203.0.113.1"),
	}
	for _, tc := range []struct {
		local string
		want  []string
	}{
		{"0.0.0.0", []string{"203.0.113.1:1"}},
		{"127.0.0.1", []string{"127.0.0.1:1", "203.0.113.1:1"}},
		{"192.168.1.10", []string{"10.0.0.1:1", "100.64.0.1:1", "192.168.1.1:1", "203.0.113.1:1"}},
		{"203.0.113.50", []string{"203.0.113.1:1"}},
	} {
		got := resolve(t.Context(), fakeResolver{"x.example.org": ips}, entries(t, "x.example.org:1", "10.0.0.2:1"), time.Second, reachableFrom(netip.MustParseAddr(tc.local)))
		var s []string
		for _, a := range got.addrs {
			if a.Addr() != netip.MustParseAddr("10.0.0.2") {
				s = append(s, a.String())
			}
		}
		if !reflect.DeepEqual(s, tc.want) || got.skipped != len(ips)-len(tc.want) {
			t.Errorf("local %s: kept %v (skipped %d), want %v", tc.local, s, got.skipped, tc.want)
		}
		if len(got.addrs) != len(s)+1 {
			t.Errorf("local %s: the literal 10.0.0.2 was not kept", tc.local)
		}
	}
}

func TestShowAddr(t *testing.T) {
	for _, tc := range []struct{ in, hidden string }{
		{"203.0.113.77:7447", "203.0.113.0/24"},
		{"198.51.100.255:1", "198.51.100.0/24"},
	} {
		a := netip.MustParseAddrPort(tc.in)
		if got := showAddr(a, false); got != tc.hidden {
			t.Errorf("showAddr(%s, false) = %s, want %s", a, got, tc.hidden)
		}
		if got := showAddr(a, true); got != a.String() {
			t.Errorf("showAddr(%s, true) = %s", a, got)
		}
	}
	if got := fmt.Sprint(showAddrs([]netip.AddrPort{netip.MustParseAddrPort("198.51.100.9:1")}, false)); got != "[198.51.100.0/24]" {
		t.Errorf("showAddrs = %s", got)
	}
}

func TestResolveCapDoesNotHideAddress(t *testing.T) {
	var nine []netip.Addr
	for i := range 9 {
		nine = append(nine, netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)}))
	}
	r := fakeResolver{"a.example.org": nine, "b.example.org": {nine[8]}}
	got := resolve(t.Context(), r, entries(t, "a.example.org:1", "b.example.org:1"), time.Second, nil)
	if !slices.Contains(got.addrs, netip.AddrPortFrom(nine[8], 1)) || len(got.addrs) != 9 {
		t.Fatalf("addrs %v: the address a's cap cut must still come from b", got.addrs)
	}
	if got.dropped != 0 {
		t.Fatalf("dropped %d, want 0: b kept the address a's cap cut", got.dropped)
	}
	r = fakeResolver{"a.example.org": append(nine, nine[8], nine[8])}
	got = resolve(t.Context(), r, entries(t, "a.example.org:1"), time.Second, nil)
	if len(got.addrs) != maxPerEntry || got.dropped != 1 {
		t.Fatalf("%d addresses, %d dropped, want %d and 1: a repeated address is dropped once", len(got.addrs), got.dropped, maxPerEntry)
	}
}
