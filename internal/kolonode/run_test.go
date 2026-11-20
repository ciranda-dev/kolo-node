package kolonode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

func TestMainCommands(t *testing.T) {
	for _, tc := range []struct {
		args        []string
		code        int
		out, errOut string
	}{
		{nil, ExitUsage, "", "usage:"},
		{[]string{"nope"}, ExitUsage, "", `unknown command "nope"`},
		{[]string{"help"}, ExitOK, "usage:", ""},
		{[]string{"help"}, ExitOK, "[-relay-file FILE]", ""},
		{[]string{"-h"}, ExitOK, "usage:", ""},
		{[]string{"version"}, ExitOK, "kolo-node ", ""},
		{[]string{"run", "-h"}, ExitOK, "", "-identity"},
		{[]string{"check", "-h"}, ExitOK, "", "-timeout"},
	} {
		var out, errOut strings.Builder
		code := Main(t.Context(), tc.args, Env{Stdout: &out, Stderr: &errOut})
		if code != tc.code || !strings.Contains(out.String(), tc.out) || !strings.Contains(errOut.String(), tc.errOut) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", tc.args, code, out.String(), errOut.String())
		}
	}
}

func TestRunLifecycle(t *testing.T) {
	checkGoroutines(t)
	var logger *slog.Logger
	stopped := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := newSink()
	done := make(chan int, 1)
	args := []string{"run", "-listen", "127.0.0.1:0", "-identity", keyPath(t), "-log-format", "json", "-first"}
	go func() {
		done <- Main(ctx, args, Env{Stdout: io.Discard, Stderr: log,
			SetLogger: func(l *slog.Logger) { logger = l },
			Stopped:   func() { close(stopped) },
		})
	}()
	ev := log.wait(t, "started", nil, 10*time.Second)
	if ev["version"] == nil || ev["addr"] == nil {
		t.Fatalf("started = %v", ev)
	}
	cancel()
	if code := <-done; code != ExitOK {
		t.Fatalf("exit %d, want 0; log:\n%s", code, log)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Stopped was not called")
	}
	if logger == nil {
		t.Fatal("SetLogger was not called")
	}
	var msgs []string
	for _, ev := range log.all() {
		msgs = append(msgs, ev["msg"].(string))
	}
	want := "identity,started,network,stopping,stats,stopped"
	if runtime.GOOS != "windows" && os.Geteuid() == 0 { // run warns once, first, when the node runs as root
		want = "running as root is not needed," + want
	}
	if got := strings.Join(msgs, ","); got != want {
		t.Fatalf("events %s, want %s", got, want)
	}
}

func TestIdentityStable(t *testing.T) {
	checkGoroutines(t)
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "node.key")
	args := []string{"run", "-listen", "127.0.0.1:0", "-identity", path, "-log-format", "json", "-first"}

	r := start(t, args...)
	first := r.log.wait(t, "identity", nil, 10*time.Second)
	r.log.wait(t, "started", nil, 10*time.Second)
	if code := r.stop(t); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if first["created"] != true {
		t.Fatalf("first run: %v, want created", first)
	}
	st1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if m := st1.Mode().Perm(); m != 0o600 {
			t.Errorf("key file mode %v, want 0600", m)
		}
		if d, err := os.Stat(dir); err != nil || d.Mode().Perm() != 0o700 {
			t.Errorf("key dir: %v, %v, want 0700", d.Mode(), err)
		}
	}

	r2 := start(t, args...)
	second := r2.log.wait(t, "identity", nil, 10*time.Second)
	r2.log.wait(t, "started", nil, 10*time.Second)
	if code := r2.stop(t); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if second["created"] != false || second["key"] != first["key"] {
		t.Fatalf("second run %v, first %v: want the same key, not created", second, first)
	}
	st2, err := os.Stat(path)
	if err != nil || !os.SameFile(st1, st2) || !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatalf("key file replaced or changed: %v", err)
	}
	seed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	logs := r.log.String() + r2.log.String() + r.out.String() + r2.out.String()
	if strings.Contains(strings.ToLower(logs), hex.EncodeToString(seed)) {
		t.Fatal("the seed appears in the output")
	}
}

func TestNothingWrittenOnStartError(t *testing.T) {
	checkGoroutines(t)
	busy, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "node.key")
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"-dht-max-records", "1"}, ExitUsage},
		{[]string{"-dht-max-requests-per-ip", "100", "-dht-max-requests-per-prefix", "50"}, ExitUsage},
		{[]string{"-listen", busy.LocalAddr().String()}, ExitError},
	} {
		log := newSink()
		args := append([]string{"run", "-listen", "127.0.0.1:0", "-identity", path, "-log-format", "json", "-first"}, tc.args...)
		if code := Main(t.Context(), args, Env{Stdout: io.Discard, Stderr: log}); code != tc.code {
			t.Errorf("%v: exit %d, want %d; log:\n%s", tc.args, code, tc.code, log)
		}
		log.wait(t, "start failed", nil, time.Second)
		if _, err := os.Lstat(filepath.Join(dir, "sub")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v: something was written: %v", tc.args, err)
		}
	}
	log := newSink()
	Main(t.Context(), []string{"run", "-listen", "127.0.0.1:0", "-identity", path, "-log-format", "json", "-first", "-dht-max-records", "1"}, Env{Stdout: io.Discard, Stderr: log})
	if ev := log.wait(t, "start failed", nil, time.Second); !strings.Contains(ev["err"].(string), "MaxRecords") {
		t.Errorf("error %v does not name the field", ev["err"])
	}
}

func TestInsecureKeyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kolo-id checks modes on unix only")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := newSink()
	code := Main(t.Context(), []string{"run", "-listen", "127.0.0.1:0", "-identity", filepath.Join(dir, "node.key"), "-log-format", "json", "-first"}, Env{Stdout: io.Discard, Stderr: log})
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d (a key file problem that a restart cannot fix); log:\n%s", code, ExitUsage, log)
	}
	ev := log.wait(t, "identity failed", nil, time.Second)
	if !strings.Contains(ev["err"].(string), "accessible to others") {
		t.Fatalf("error %v, want kolo-id's insecure key file error", ev["err"])
	}
}

func client(t *testing.T, ctx context.Context) *kolodht.Node {
	t.Helper()
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	n, err := kolodht.New(ctx, "127.0.0.1:0", id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPeersThroughNode(t *testing.T) {
	checkGoroutines(t)
	r, addr := startNode(t, "-stats-interval", "1s")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice, bob := client(t, ctx), client(t, ctx)
	defer alice.Close()
	defer bob.Close()
	for _, n := range []*kolodht.Node{alice, bob} {
		if err := n.Bootstrap(ctx, []netip.AddrPort{addr}); err != nil {
			t.Fatalf("Bootstrap from kolo-node: %v", err)
		}
	}
	topic := kolodht.Topic(sha256.Sum256([]byte("kolo-node test topic")))
	if err := bob.Announce(ctx, topic); err != nil {
		t.Fatal(err)
	}
	r.log.wait(t, "stats", func(ev map[string]any) bool { return ev["records"] == 1.0 }, 10*time.Second)
	go func() {
		c, err := bob.Accept(ctx)
		if err != nil {
			return
		}
		s, err := c.AcceptStream(ctx)
		if err != nil {
			return
		}
		io.Copy(s, s)
		s.Close()
		<-c.Context().Done() // closed by Alice or by bob.Close, after the echo is delivered
	}()
	peers, err := alice.Lookup(ctx, topic)
	if err != nil || len(peers) != 1 {
		t.Fatalf("Lookup: %d records, %v", len(peers), err)
	}
	c, err := alice.Connect(ctx, peers[0])
	if err != nil {
		t.Fatalf("Connect through kolo-node: %v", err)
	}
	defer c.Close()
	s, err := c.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("hello kolo")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reply, err := io.ReadAll(s)
	if err != nil || string(reply) != "hello kolo" {
		t.Fatalf("echo %q, %v", reply, err)
	}
}

func TestInboundConnectionsRefused(t *testing.T) {
	checkGoroutines(t)
	r, addr := startNode(t, "-stats-interval", "1s")
	key, err := hex.DecodeString(r.log.wait(t, "identity", nil, time.Second)["key"].(string))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	c, err := kolosecure.Dial(ctx, addr.String(), id, key, nil)
	if err == nil {
		c.Close()
		t.Fatal("Dial to kolo-node completed a handshake")
	}
	if !errors.Is(err, kolotransport.ErrRefused) {
		t.Fatalf("Dial: %v, want %v (refused before the handshake)", err, kolotransport.ErrRefused)
	}
	dialed := time.Now()
	r.log.wait(t, "stats", func(ev map[string]any) bool {
		at, err := time.Parse(time.RFC3339Nano, ev["time"].(string))
		return err == nil && at.After(dialed)
	}, 10*time.Second)
	for _, ev := range r.log.all() {
		if ev["msg"] == "stats" && ev["closed"] != 0.0 {
			t.Fatalf("stats %v: a connection reached Accept", ev)
		}
	}
}

func TestTwoNodes(t *testing.T) {
	checkGoroutines(t)
	a, addrA := startNode(t, "-stats-interval", "1s")
	hasServer := func(ev map[string]any) bool { return ev["servers"] == 1.0 }
	// Both nodes share 127.0.0.1, so they can draw the same r (1 in 8) and so the same ID; kolo-dht then moves
	// B's r, and A learns B only from B's next request. A new B draws a new r.
	found := false
	for range 5 {
		b, _ := startNode(t, "-stats-interval", "1s", "-bootstrap", addrA.String())
		ev := b.log.wait(t, "bootstrap", nil, 10*time.Second)
		if ev["servers"] != 1.0 {
			t.Fatalf("B bootstrap event %v, want servers=1", ev)
		}
		if got := ev["addrs"].([]any); len(got) != 1 || got[0] != "127.0.0.0/24" {
			t.Fatalf("B logged addrs %v, want only the /24", got)
		}
		if strings.Contains(b.log.String(), addrA.String()) {
			t.Fatalf("B's log shows A's full address by default:\n%s", b.log)
		}
		if found = a.log.tryWait("stats", hasServer, 3*time.Second); found {
			break
		}
		b.stop(t)
	}
	if !found {
		t.Fatalf("A never added B; log:\n%s", a.log)
	}

	c, _ := startNode(t, "-log-addrs", "-bootstrap", addrA.String())
	ev := c.log.wait(t, "bootstrap", nil, 10*time.Second)
	if got := ev["addrs"].([]any); len(got) != 1 || got[0] != addrA.String() {
		t.Fatalf("with -log-addrs: addrs %v, want %s", got, addrA)
	}
}

func TestBootstrapSelf(t *testing.T) {
	checkGoroutines(t)
	self := freePort(t)
	r := start(t, "run", "-listen", self.String(), "-identity", keyPath(t), "-log-format", "json", "-bootstrap", self.String())
	r.log.wait(t, "bootstrap failed", nil, 10*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n := client(t, ctx)
	defer n.Close()
	if err := n.Bootstrap(ctx, []netip.AddrPort{self}); err != nil {
		t.Fatalf("the node stopped serving: %v", err)
	}
}

func TestBootstrapResolve(t *testing.T) {
	checkGoroutines(t)
	_, addrA := startNode(t)
	old := lookupIP
	lookupIP = fakeResolver{"a.example.org": {addrA.Addr()}}
	t.Cleanup(func() { lookupIP = old })
	b, _ := startNode(t, "-bootstrap", "a.example.org:"+strconv.Itoa(int(addrA.Port()))+",missing.example.org:1")
	b.log.wait(t, "resolve failed", func(ev map[string]any) bool { return ev["entry"] == "missing.example.org:1" }, 10*time.Second)
	b.log.wait(t, "bootstrap", func(ev map[string]any) bool { return ev["servers"] == 1.0 }, 10*time.Second)
}

func TestShutdownTimeout(t *testing.T) {
	checkGoroutines(t)
	release := make(chan struct{})
	setDuration(t, &shutdownTimeout, 200*time.Millisecond)
	r, _ := startNodeEnv(t, Env{BeforeClose: func() { <-release }})
	t.Cleanup(func() { close(release) }) // runs before checkGoroutines
	if code := r.stop(t); code != ExitError {
		t.Fatalf("exit %d, want %d", code, ExitError)
	}
	r.log.wait(t, "shutdown timed out", nil, time.Second)
}

// stubResolver answers from a function, for resolvers that block or change between calls.
type stubResolver func(ctx context.Context, host string) ([]netip.Addr, error)

func (f stubResolver) LookupNetIP(ctx context.Context, _, host string) ([]netip.Addr, error) {
	return f(ctx, host)
}

func setResolver(t *testing.T, r resolver) {
	t.Helper()
	old := lookupIP
	lookupIP = r
	t.Cleanup(func() { lookupIP = old })
}

func setDuration(t *testing.T, p *time.Duration, d time.Duration) {
	t.Helper()
	old := *p
	*p = d
	t.Cleanup(func() { *p = old })
}

func TestBootstrapSlowName(t *testing.T) {
	checkGoroutines(t)
	_, addrA := startNode(t)
	setDuration(t, &resolveTimeout, 500*time.Millisecond)
	setDuration(t, &bootstrapTimeout, 500*time.Millisecond)
	setResolver(t, stubResolver(func(ctx context.Context, host string) ([]netip.Addr, error) {
		<-ctx.Done() // slow.example.org never answers
		return nil, ctx.Err()
	}))
	b, _ := startNode(t, "-bootstrap", "slow.example.org:1,"+addrA.String())
	b.log.wait(t, "resolve failed", nil, 5*time.Second)
	b.log.wait(t, "bootstrap", func(ev map[string]any) bool { return ev["servers"] == 1.0 }, 10*time.Second)
}

func TestBootstrapRetry(t *testing.T) {
	checkGoroutines(t)
	_, addrA := startNode(t)
	setDuration(t, &bootstrapRetry, 200*time.Millisecond)
	var mu sync.Mutex
	calls := 0
	setResolver(t, stubResolver(func(_ context.Context, host string) ([]netip.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		if calls++; calls == 1 {
			return nil, errors.New("temporary failure")
		}
		return []netip.Addr{addrA.Addr()}, nil
	}))
	b, _ := startNode(t, "-bootstrap", "a.example.org:"+strconv.Itoa(int(addrA.Port())))
	b.log.wait(t, "resolve failed", nil, 5*time.Second)
	b.log.wait(t, "bootstrap", func(ev map[string]any) bool { return ev["servers"] == 1.0 }, 10*time.Second)
}

func TestBootstrapScope(t *testing.T) {
	checkGoroutines(t)
	_, addrA := startNode(t)
	port := strconv.Itoa(int(addrA.Port()))
	setResolver(t, fakeResolver{"mixed.example.org": {
		netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("169.254.1.1"), addrA.Addr(),
	}})
	b, _ := startNode(t, "-bootstrap", "mixed.example.org:"+port)
	if ev := b.log.wait(t, "bootstrap addresses skipped", nil, 5*time.Second); ev["skipped"] != 2.0 {
		t.Fatalf("skipped event %v, want 2", ev)
	}
	b.log.wait(t, "bootstrap", func(ev map[string]any) bool { return ev["servers"] == 1.0 }, 10*time.Second)
	if strings.Contains(b.log.String(), "10.0.0.1") || strings.Contains(b.log.String(), "169.254") {
		t.Fatalf("skipped addresses were logged:\n%s", b.log)
	}
}

// countingResolver fails every lookup and counts them.
type countingResolver struct {
	mu sync.Mutex
	n  int
}

func (c *countingResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return nil, errors.New("no such host")
}

func (c *countingResolver) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func TestBootstrapRetryIgnoresOtherServers(t *testing.T) {
	checkGoroutines(t)
	setDuration(t, &bootstrapRetry, 200*time.Millisecond)
	setDuration(t, &bootstrapRefresh, 400*time.Millisecond) // caps the backoff
	cr := &countingResolver{}
	setResolver(t, cr)
	n, addrN := startNode(t, "-stats-interval", "1s", "-bootstrap", "boot.example.org:1")
	// Another server bootstraps to N and so enters its table; N has still not reached its own bootstrap list.
	joined := false
	for range 5 {
		m, _ := startNode(t, "-bootstrap", addrN.String())
		m.log.wait(t, "bootstrap", nil, 10*time.Second)
		if joined = n.log.tryWait("stats", func(ev map[string]any) bool { return ev["servers"] == 1.0 }, 3*time.Second); joined {
			break
		}
		m.stop(t)
	}
	if !joined {
		t.Fatal("N never added the other server")
	}
	before := cr.count()
	deadline := time.Now().Add(5 * time.Second)
	for cr.count() < before+3 {
		if time.Now().After(deadline) {
			t.Fatalf("resolver calls stayed at %d after another server joined: the retry loop stopped", cr.count())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestBootstrapRefresh(t *testing.T) {
	checkGoroutines(t)
	setDuration(t, &bootstrapRefresh, 200*time.Millisecond)
	_, addrA := startNode(t)
	b, _ := startNode(t, "-bootstrap", addrA.String())
	b.log.wait(t, "bootstrap", nil, 10*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		count := 0
		for _, ev := range b.log.all() {
			if ev["msg"] == "bootstrap" {
				count++
			}
		}
		if count >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d bootstrap events, want a refresh while the table is small", count)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, ev := range b.log.all() {
		if ev["retry"] != nil {
			t.Fatalf("event %v announces a retry after a successful bootstrap", ev)
		}
	}
}

func TestStartedLogsSetLimits(t *testing.T) {
	checkGoroutines(t)
	r, _ := startNode(t, "-dht-max-requests-per-ip", "5000", "-dht-punch-timeout", "1m")
	ev := r.log.wait(t, "started", nil, time.Second)
	if ev["dht-max-requests-per-ip"] != 5000.0 || ev["dht-punch-timeout"] != "1m0s" {
		t.Fatalf("started %v, want dht-max-requests-per-ip 5000 and dht-punch-timeout 1m0s", ev)
	}
	// kolo-dht derives the others; logging them would copy its defaults.
	for _, k := range []string{"dht-max-requests-per-prefix", "dht-max-requests", "dht-max-records"} {
		if _, ok := ev[k]; ok {
			t.Errorf("started %v logs %s, which was not set", ev, k)
		}
	}
}

func TestKeyPlantedBeforeSave(t *testing.T) {
	checkGoroutines(t)
	path := keyPath(t)
	var planted *koloid.Identity
	beforeSave := func(p string) {
		id, err := koloid.Generate()
		if err != nil {
			t.Error(err)
			return
		}
		if err := id.SaveNew(p); err != nil {
			t.Error(err)
		}
		planted = id
	}
	log := newSink()
	code := Main(t.Context(), []string{"run", "-listen", "127.0.0.1:0", "-identity", path, "-log-format", "json", "-first"}, Env{Stdout: io.Discard, Stderr: log, BeforeSave: beforeSave})
	if code != ExitError {
		t.Fatalf("exit %d, want %d; log:\n%s", code, ExitError, log)
	}
	ev := log.wait(t, "identity failed", nil, time.Second)
	if !strings.Contains(ev["err"].(string), fs.ErrExist.Error()) {
		t.Fatalf("error %v, want one wrapping fs.ErrExist", ev["err"])
	}
	got, err := koloid.Load(path)
	if err != nil || planted == nil || !got.PublicKey().Equal(planted.PublicKey()) {
		t.Fatalf("the planted key was replaced: %v", err)
	}
}

func TestBootstrapLookupCutShortJoins(t *testing.T) {
	checkGoroutines(t)
	// Set before any node starts: their bootstrap loops read these.
	setDuration(t, &bootstrapTimeout, time.Second)
	setDuration(t, &bootstrapRetry, 200*time.Millisecond)
	b, addrB := startNode(t)
	a, addrA := startNode(t, "-bootstrap", addrB.String())
	a.log.wait(t, "bootstrap", nil, 10*time.Second)
	if code := b.stop(t); code != ExitOK {
		t.Fatalf("B exit %d", code)
	}
	// Hold B's port with a socket that never answers, so no other node can take it and answer in B's place.
	silent, err := net.ListenPacket("udp", addrB.String())
	if err != nil {
		t.Fatalf("could not hold B's port: %v", err)
	}
	defer silent.Close()
	// A now names a silent server; C's lookup through A waits 2s for it, past C's 1s bootstrap timeout. C and B share
	// 127.0.0.1, so C draws B's ID (r) 1 time in 8 and then skips B as itself; a new C draws a new r.
	var c *running
	for range 5 {
		c, _ = startNode(t, "-dht-request-timeout", "2s", "-bootstrap", addrA.String())
		if ev := c.log.wait(t, "bootstrap", nil, 5*time.Second); ev["lookup"] == "cut short" {
			break
		}
		c.stop(t)
		c = nil
	}
	if c == nil {
		t.Fatalf("no bootstrap event with lookup=cut short (B was %s)\nA:\n%s", addrB, a.log)
	}
	time.Sleep(3 * time.Second)
	for _, ev := range c.log.all() {
		if ev["msg"] == "bootstrap failed" || ev["retry"] != nil {
			t.Fatalf("event %v: an answered bootstrap was retried", ev)
		}
	}
}

func TestBootstrapBackoff(t *testing.T) {
	checkGoroutines(t)
	setDuration(t, &bootstrapRetry, 50*time.Millisecond)
	setDuration(t, &bootstrapRefresh, 400*time.Millisecond)
	self := freePort(t)
	r := start(t, "run", "-listen", self.String(), "-identity", keyPath(t), "-log-format", "json", "-bootstrap", self.String())
	r.log.wait(t, "started", nil, 5*time.Second)
	time.Sleep(3 * time.Second)
	var retries []string
	for _, ev := range r.log.all() {
		if ev["msg"] == "bootstrap failed" {
			retries = append(retries, ev["retry"].(string))
		}
	}
	if len(retries) < 4 || len(retries) > 14 {
		t.Fatalf("%d failures in 3s (retries %v), want about 8 with backoff from 50ms to 400ms", len(retries), retries)
	}
	if retries[0] != "50ms" || retries[1] != "100ms" || retries[len(retries)-1] != "400ms" {
		t.Fatalf("retries %v, want 50ms, 100ms, ... doubling up to 400ms", retries)
	}
}

func TestBootstrapNoRefreshWhenTableFull(t *testing.T) {
	checkGoroutines(t)
	setDuration(t, &bootstrapRefresh, 200*time.Millisecond)
	old := refreshBelow
	refreshBelow = 1 // stands for a table of refreshBelow servers, which nodes sharing 127.0.0.1 cannot reliably build
	t.Cleanup(func() { refreshBelow = old })
	_, addrA := startNode(t)
	b, _ := startNode(t, "-bootstrap", addrA.String())
	b.log.wait(t, "bootstrap", func(ev map[string]any) bool { return ev["servers"] == 1.0 }, 10*time.Second)
	time.Sleep(time.Second)
	n := 0
	for _, ev := range b.log.all() {
		if ev["msg"] == "bootstrap" || ev["msg"] == "bootstrap failed" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d bootstrap events, want 1: a table of refreshBelow servers needs no refresh", n)
	}
}

func TestBootstrapStaysJoined(t *testing.T) {
	checkGoroutines(t)
	setDuration(t, &bootstrapRetry, 100*time.Millisecond)
	setDuration(t, &bootstrapRefresh, 300*time.Millisecond)
	old := refreshBelow
	refreshBelow = 2
	t.Cleanup(func() { refreshBelow = old })
	a, addrA := startNode(t)
	b, addrB := startNode(t, "-stats-interval", "1s", "-dht-request-timeout", "100ms", "-bootstrap", addrA.String())
	b.log.wait(t, "bootstrap", func(ev map[string]any) bool { return ev["servers"] == 1.0 }, 10*time.Second)
	if code := a.stop(t); code != ExitOK {
		t.Fatalf("A exit %d", code)
	}
	silent, err := net.ListenPacket("udp", addrA.String())
	if err != nil {
		t.Fatalf("could not hold A's port: %v", err)
	}
	defer silent.Close()
	// A failed refresh is retried after bootstrapRetry, then backs off.
	b.log.wait(t, "bootstrap failed", func(ev map[string]any) bool { return ev["retry"] == "100ms" }, 5*time.Second)
	b.log.wait(t, "bootstrap failed", func(ev map[string]any) bool { return ev["retry"] == "200ms" }, 5*time.Second)
	// Another server enters B's table, which then holds refreshBelow servers: B stops trying.
	joined := false
	for range 5 {
		c, _ := startNode(t, "-bootstrap", addrB.String())
		c.log.wait(t, "bootstrap", nil, 10*time.Second)
		if joined = b.log.tryWait("stats", func(ev map[string]any) bool { return ev["servers"] == 2.0 }, 3*time.Second); joined {
			break
		}
		c.stop(t)
	}
	if !joined {
		t.Fatal("B never added the other server")
	}
	failures := func() int {
		n := 0
		for _, ev := range b.log.all() {
			if ev["msg"] == "bootstrap failed" {
				n++
			}
		}
		return n
	}
	before := failures()
	time.Sleep(2 * time.Second)
	if n := failures() - before; n > 1 {
		t.Fatalf("%d failed bootstraps in 2s with refreshBelow servers in the table, want at most the one under way", n)
	}
}

// TestBootstrapPingsEveryAddress checks that with minInflightBootstrap slots, Bootstrap pings all maxAddrs addresses at
// once while dhtMaxChecks check pings are in flight.
func TestBootstrapPingsEveryAddress(t *testing.T) {
	checkGoroutines(t)
	listen := func() net.PacketConn {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pc.Close() })
		return pc
	}
	addrOf := func(pc net.PacketConn) netip.AddrPort { return pc.LocalAddr().(*net.UDPAddr).AddrPort() }
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	const timeout = dhtMaxRequestTimeout // check pings last one RequestTimeout, and 10 take 9s to start
	cfg := &kolodht.Config{Server: true, MaxInflight: minInflightBootstrap, RequestTimeout: timeout, Secure: fixedSecure()}
	node, err := kolodht.New(t.Context(), "127.0.0.1:0", id, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	nodeAddr := node.Addr().(*net.UDPAddr)

	// serverPing returns a PING from a new kolo-dht server, which replayed from a socket that never answers makes the
	// node send that socket a check ping.
	serverPing := func() []byte {
		trap := listen()
		sid, err := koloid.Generate()
		if err != nil {
			t.Fatal(err)
		}
		srv, err := kolodht.New(t.Context(), "127.0.0.1:0", sid, &kolodht.Config{Server: true, Secure: fixedSecure()})
		if err != nil {
			t.Fatal(err)
		}
		defer srv.Close()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { srv.Bootstrap(ctx, []netip.AddrPort{addrOf(trap)}); close(done) }()
		defer func() { cancel(); <-done }()
		buf := make([]byte, 2048)
		trap.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := trap.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		return buf[:n]
	}
	// checked replays ping from a new socket and reports whether the node sent it a check ping after its reply.
	checked := func(ping []byte) bool {
		pc := listen()
		if _, err := pc.WriteTo(ping, nodeAddr); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2048)
		pc.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		for range 2 {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return false
			}
		}
		return true
	}
	// The node refuses a server with its own ID, an r in 8 chance on one IP: take another server's PING then.
	ping := serverPing()
	first := time.Now()
	for tries := 0; !checked(ping); tries++ {
		if tries == 8 {
			t.Fatal("no check ping from the node")
		}
		time.Sleep(time.Second) // the next window of kolo-dht's one-a-second budget
		ping, first = serverPing(), time.Now()
	}
	// One check ping per fixed one-second window of kolo-dht's budget, scheduled from the first so delays do not add up.
	// A ping that lands in the window of the one before is refused without being counted: ask again.
	const step = 1010 * time.Millisecond
	for i := 1; i < dhtMaxChecks; i++ {
		due := first.Add(time.Duration(i) * step)
		time.Sleep(time.Until(due))
		for !checked(ping) {
			if time.Now().After(due.Add(step)) {
				t.Fatalf("check ping %d of %d not sent", i+1, dhtMaxChecks)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	socks := make([]net.PacketConn, maxAddrs)
	addrs := make([]netip.AddrPort, maxAddrs)
	for i := range socks {
		socks[i] = listen()
		addrs[i] = addrOf(socks[i])
	}
	// The first check ping, sent after first, ends no earlier than first+timeout and frees a slot then: every ping
	// must arrive before that, or a slot freed by it could have carried one.
	expiry := first.Add(timeout)
	start := time.Now()
	if !start.Before(expiry) {
		t.Fatalf("the %d check pings took %v to start, so the first may have ended before the pings are in", dhtMaxChecks, start.Sub(first))
	}
	done := make(chan error, 1)
	go func() { done <- node.Bootstrap(t.Context(), addrs) }()
	buf := make([]byte, 2048)
	for i, pc := range socks {
		pc.SetReadDeadline(expiry)
		if _, _, err := pc.ReadFrom(buf); err != nil {
			t.Errorf("address %d of %d got no ping while the %d check pings were in flight: %v", i+1, maxAddrs, dhtMaxChecks, err)
		}
	}
	if err := <-done; !errors.Is(err, kolodht.ErrNoNodes) {
		t.Fatalf("Bootstrap = %v, want ErrNoNodes", err)
	}
}

func TestRequestTimeoutBound(t *testing.T) {
	checkGoroutines(t)
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		d  time.Duration
		ok bool
	}{{dhtMaxRequestTimeout, true}, {dhtMaxRequestTimeout + time.Millisecond, false}} {
		node, err := kolodht.New(t.Context(), "127.0.0.1:0", id, &kolodht.Config{RequestTimeout: tc.d, Secure: fixedSecure()})
		if err == nil {
			node.Close()
		}
		if tc.ok && err != nil || !tc.ok && !errors.Is(err, kolodht.ErrInvalidConfig) {
			t.Errorf("RequestTimeout %v: %v, want accepted %v: dhtMaxRequestTimeout must be kolo-dht's bound", tc.d, err, tc.ok)
		}
	}
}

func TestPunchTimeoutBound(t *testing.T) {
	checkGoroutines(t)
	const minPunchTimeout, maxPunchTimeout = time.Second, 2 * time.Minute // the README's -dht-punch-timeout range
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		d  time.Duration
		ok bool
	}{
		{minPunchTimeout, true}, {minPunchTimeout - time.Millisecond, false},
		{maxPunchTimeout, true}, {maxPunchTimeout + time.Millisecond, false},
	} {
		node, err := kolodht.New(t.Context(), "127.0.0.1:0", id, &kolodht.Config{PunchTimeout: tc.d, Secure: fixedSecure()})
		if err == nil {
			node.Close()
		}
		if tc.ok && err != nil || !tc.ok && !errors.Is(err, kolodht.ErrInvalidConfig) {
			t.Errorf("PunchTimeout %v: %v, want accepted %v: the README must give kolo-dht's bound", tc.d, err, tc.ok)
		}
	}
}
