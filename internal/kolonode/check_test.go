package kolonode

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
)

func TestCheck(t *testing.T) {
	checkGoroutines(t)
	_, addr := startNode(t)
	closed := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl := client(t, ctx)
	defer cl.Close()
	clientAddr := cl.Addr().(*net.UDPAddr).AddrPort()

	var out, errOut strings.Builder
	if code := Main(ctx, []string{"check", "-timeout", "2s", addr.String()}, Env{Stdout: &out, Stderr: &errOut}); code != ExitOK {
		t.Fatalf("check of a running node: exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
	if out.String() != "ok "+addr.String()+"\n" {
		t.Fatalf("stdout %q", out.String())
	}

	out.Reset()
	code := Main(ctx, []string{"check", "-timeout", "1s", addr.String(), closed.String(), clientAddr.String()}, Env{Stdout: &out, Stderr: &errOut})
	want := "ok " + addr.String() + "\nfailed " + closed.String() + ": " + noAnswer + "\nfailed " + clientAddr.String() + ": " + noAnswer + "\n"
	if code != ExitError || out.String() != want {
		t.Fatalf("exit %d, stdout %q, want %d and %q", code, out.String(), ExitError, want)
	}

	old := lookupIP
	lookupIP = fakeResolver{"node.example.org": {addr.Addr()}}
	t.Cleanup(func() { lookupIP = old })
	out.Reset()
	name := "node.example.org:" + strings.TrimPrefix(addr.String(), "127.0.0.1:")
	code = Main(ctx, []string{"check", "-timeout", "1s", name, "missing.example.org:1"}, Env{Stdout: &out, Stderr: &errOut})
	want = "ok " + addr.String() + "\nfailed missing.example.org:1: no such host\n"
	if code != ExitError || out.String() != want {
		t.Fatalf("exit %d, stdout %q, want %q", code, out.String(), want)
	}
}

func TestCheckUsage(t *testing.T) {
	many := make([]string, maxEntries+1)
	for i := range many {
		many[i] = netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(i+1)).String()
	}
	for _, args := range [][]string{
		{},
		{"-timeout", "500ms", "127.0.0.1:1"},
		{"-timeout", "2m", "127.0.0.1:1"},
		{"bad"},
		{"-nope", "127.0.0.1:1"},
		many,
	} {
		var out, errOut strings.Builder
		if code := Main(t.Context(), append([]string{"check"}, args...), Env{Stdout: &out, Stderr: &errOut}); code != ExitUsage || out.Len() != 0 {
			t.Errorf("check %v: exit %d, stdout %q, want %d and nothing", args, code, out.String(), ExitUsage)
		}
	}
	for _, a := range []string{"[::1]:7447", "[::ffff:127.0.0.1]:7447"} {
		var out, errOut strings.Builder
		code := Main(t.Context(), []string{"check", a}, Env{Stdout: &out, Stderr: &errOut})
		if code != ExitUsage || out.Len() != 0 || !strings.Contains(errOut.String(), "IPv4 only") {
			t.Errorf("check %s: exit %d, stdout %q, stderr %q, want %d and an IPv4 only error", a, code, out.String(), errOut.String(), ExitUsage)
		}
	}
}

func TestCheckDeadServers(t *testing.T) {
	checkGoroutines(t)
	b, addrB := startNode(t)
	a, addrA := startNode(t, "-bootstrap", addrB.String())
	a.log.wait(t, "bootstrap", func(ev map[string]any) bool { return ev["servers"] == 1.0 }, 10*time.Second)
	if code := b.stop(t); code != ExitOK {
		t.Fatalf("B exit %d", code)
	}
	// A now names a dead server, so check's self-lookup through A runs into the deadline.
	var out, errOut strings.Builder
	code := Main(t.Context(), []string{"check", "-timeout", "1s", addrA.String()}, Env{Stdout: &out, Stderr: &errOut})
	if code != ExitOK || out.String() != "ok "+addrA.String()+"\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q: a node that answered was reported failed", code, out.String(), errOut.String())
	}
}

func TestCheckCapsAddressesPerName(t *testing.T) {
	var many []netip.Addr
	for i := range 12 {
		many = append(many, netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)}))
	}
	setResolver(t, fakeResolver{"many.example.org": many})
	var pinged []netip.AddrPort
	old := pingAddr
	pingAddr = func(_ context.Context, a netip.AddrPort, _ time.Duration) (bool, error) {
		pinged = append(pinged, a)
		return false, nil
	}
	t.Cleanup(func() { pingAddr = old })
	var out, errOut strings.Builder
	Main(t.Context(), []string{"check", "-timeout", "1s", "many.example.org:1"}, Env{Stdout: &out, Stderr: &errOut})
	if len(pinged) != maxPerEntry || strings.Count(out.String(), "\n") != maxPerEntry {
		t.Fatalf("pinged %d, printed:\n%s; want %d", len(pinged), out.String(), maxPerEntry)
	}
}

func TestCheckIgnoresOtherServers(t *testing.T) {
	checkGoroutines(t)
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	other, err := kolodht.New(ctx, "127.0.0.1:0", id, &kolodht.Config{Server: true})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	finished := -1
	checkStarted = func(n *kolodht.Node) {
		client := n.Addr().(*net.UDPAddr).AddrPort()
		go func() {
			defer close(done)
			for ctx.Err() == nil {
				bctx, c := context.WithTimeout(ctx, 200*time.Millisecond)
				other.Bootstrap(bctx, []netip.AddrPort{client}) // pings check's client, which then adds it
				c()
			}
		}()
		for deadline := time.Now().Add(3 * time.Second); n.Stats().Servers == 0; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Error("the other server never entered the client's table")
				return
			}
		}
	}
	checkFinished = func(n *kolodht.Node) { finished = n.Stats().Servers }
	t.Cleanup(func() { checkStarted, checkFinished = nil, nil })

	target := silent.LocalAddr().(*net.UDPAddr).AddrPort()
	var out, errOut strings.Builder
	code := Main(t.Context(), []string{"check", "-timeout", "2s", target.String()}, Env{Stdout: &out, Stderr: &errOut})
	if finished < 1 {
		t.Fatalf("client table had %d servers when ping returned, want the other server in it", finished)
	}
	if code != ExitError || out.String() != "failed "+target.String()+": "+noAnswer+"\n" {
		t.Fatalf("exit %d, stdout %q: an address that never answered was reported ok", code, out.String())
	}
}

func TestAnswered(t *testing.T) {
	live := t.Context()
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		err    error
		parent context.Context
		want   bool
	}{
		{nil, live, true},
		{context.DeadlineExceeded, live, true},
		{fmt.Errorf("lookup: %w", context.DeadlineExceeded), live, true},
		{context.DeadlineExceeded, done, false},
		{context.Canceled, live, false},
		{kolodht.ErrNoNodes, live, false},
		{kolodht.ErrInvalidArgument, live, false},
	} {
		if got := answered(tc.err, tc.parent); got != tc.want {
			t.Errorf("answered(%v, parent done %v) = %v, want %v", tc.err, tc.parent.Err() != nil, got, tc.want)
		}
	}
}
