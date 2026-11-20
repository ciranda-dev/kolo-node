package kolonode

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// check's client listens on loopback in tests, not on every interface.
func init() { checkListen = "127.0.0.1:0" }

// checkGoroutines fails the test if goroutines started during it outlive its cleanups.
func checkGoroutines(t *testing.T) {
	t.Helper()
	before := goroutines()
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			var leaked []string
			for id, stack := range goroutines() {
				if _, ok := before[id]; !ok {
					leaked = append(leaked, stack)
				}
			}
			if len(leaked) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("%d goroutines leaked:\n%s", len(leaked), strings.Join(leaked, "\n\n"))
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func goroutines() map[string]string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	out := make(map[string]string)
	for _, stack := range strings.Split(string(buf), "\n\n") {
		if id, _, ok := strings.Cut(strings.TrimPrefix(stack, "goroutine "), " "); ok {
			out[id] = stack
		}
	}
	return out
}

// sink collects JSON log lines; slog writes each record with one Write.
type sink struct {
	mu      sync.Mutex
	raw     bytes.Buffer
	events  []map[string]any
	changed chan struct{}
}

func newSink() *sink { return &sink{changed: make(chan struct{})} }

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw.Write(p)
	for line := range strings.SplitSeq(strings.TrimSpace(string(p)), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil {
			s.events = append(s.events, ev)
		}
	}
	close(s.changed)
	s.changed = make(chan struct{})
	return len(p), nil
}

func (s *sink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.raw.String()
}

func (s *sink) all() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.events...)
}

// tryWait is wait without failing: it reports whether such an event came within d.
func (s *sink) tryWait(msg string, ok func(map[string]any) bool, d time.Duration) bool {
	deadline := time.After(d)
	for {
		s.mu.Lock()
		for _, ev := range s.events {
			if ev["msg"] == msg && (ok == nil || ok(ev)) {
				s.mu.Unlock()
				return true
			}
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-deadline:
			return false
		}
	}
}

// wait returns the first event with msg for which ok returns true (nil ok accepts any), or fails after d.
func (s *sink) wait(t *testing.T, msg string, ok func(map[string]any) bool, d time.Duration) map[string]any {
	t.Helper()
	deadline := time.After(d)
	for {
		s.mu.Lock()
		for _, ev := range s.events {
			if ev["msg"] == msg && (ok == nil || ok(ev)) {
				s.mu.Unlock()
				return ev
			}
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("no %q event within %v; log:\n%s", msg, d, s.String())
			return nil
		}
	}
}

// lockedBuffer is a bytes.Buffer safe for concurrent use.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type running struct {
	log    *sink
	out    *lockedBuffer
	cancel context.CancelFunc
	done   chan int
}

// start runs Main with args in the background; a cleanup stops it.
func start(t *testing.T, args ...string) *running { return startEnv(t, Env{}, args...) }

// startEnv is start with the hooks of env; its Stdout and Stderr are replaced.
func startEnv(t *testing.T, env Env, args ...string) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{log: newSink(), out: &lockedBuffer{}, cancel: cancel, done: make(chan int, 1)}
	env.Stdout, env.Stderr = r.out, r.log
	go func() { r.done <- Main(ctx, args, env) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(20 * time.Second):
			t.Error("Main did not return")
		}
	})
	return r
}

// stop cancels Main and returns its exit code.
func (r *running) stop(t *testing.T) int {
	t.Helper()
	r.cancel()
	select {
	case code := <-r.done:
		r.done <- code
		return code
	case <-time.After(20 * time.Second):
		t.Fatal("Main did not return")
		return -1
	}
}

// startNode runs a node on loopback with JSON logs and returns it with its address once started.
func startNode(t *testing.T, args ...string) (*running, netip.AddrPort) {
	t.Helper()
	return startNodeEnv(t, Env{}, args...)
}

// startNodeEnv is startNode with the hooks of env.
func startNodeEnv(t *testing.T, env Env, args ...string) (*running, netip.AddrPort) {
	t.Helper()
	base := []string{"run", "-listen", "127.0.0.1:0", "-identity", keyPath(t), "-log-format", "json"}
	if !slices.Contains(args, "-bootstrap") && !slices.Contains(args, "-bootstrap-file") {
		base = append(base, "-first")
	}
	r := startEnv(t, env, append(base, args...)...)
	ev := r.log.wait(t, "started", nil, 10*time.Second)
	return r, netip.MustParseAddrPort(ev["addr"].(string))
}

// keyPath returns a key file path in a directory that does not exist yet, so SaveNew creates it with mode 0700.
func keyPath(t *testing.T) string { return filepath.Join(t.TempDir(), "state", "node.key") }

// freePort returns a loopback UDP address that was free a moment ago.
func freePort(t *testing.T) netip.AddrPort {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).AddrPort()
}
