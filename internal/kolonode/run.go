// Package kolonode implements the kolo-node command: a kolo-dht bootstrap server, or a kolo-relay.
package kolonode

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

const (
	// ExitOK is Main's exit code after a clean stop.
	ExitOK = 0
	// ExitError is Main's exit code after a startup failure or an unclean shutdown.
	ExitError = 1
	// ExitUsage is Main's exit code for an invalid command, flag or config.
	ExitUsage = 2
)

// refreshBelow is the table size under which a joined node bootstraps again every bootstrapRefresh: kolo-dht's bucket
// size K, which it does not export.
var refreshBelow = 20

// Limits of the kolo-dht in go.mod that it does not export; TestRequestTimeoutBound checks the timeout against
// kolo-dht's own range.
const (
	// dhtMaxChecks is the most check pings in flight (maxChecks in kolo-dht).
	dhtMaxChecks = 10
	// dhtMaxRequestTimeout is the upper bound of kolo-dht's Config.RequestTimeout.
	dhtMaxRequestTimeout = 10 * time.Second
	// minInflightBootstrap lets all maxAddrs bootstrap pings hold a request slot at once beside kolo-dht's check pings
	// and one maintenance request, so the PING step lasts one RequestTimeout.
	minInflightBootstrap = maxAddrs + dhtMaxChecks + 1
)

// Timings and hooks, variables so tests can shorten or observe them.
var (
	bootstrapTimeout          = 30 * time.Second
	resolveTimeout            = 5 * time.Second
	bootstrapRetry            = time.Minute
	bootstrapRefresh          = 5 * time.Minute
	shutdownTimeout           = 10 * time.Second
	lookupIP         resolver = net.DefaultResolver
)

// Env is where Main writes, and what it calls around the run command.
type Env struct {
	Stdout, Stderr io.Writer
	// SetLogger, if set, receives the run command's logger before the node starts.
	SetLogger func(*slog.Logger)
	// Stopped, if set, is called once the context is done, before the shutdown starts.
	Stopped func()
	// BeforeClose, if set, runs before the node is closed at shutdown; tests use it to stall a shutdown.
	BeforeClose func()
	// BeforeSave, if set, runs with the key file path before a new identity is saved; tests use it to plant a file.
	BeforeSave func(path string)
}

const usage = `usage:
  kolo-node run -identity FILE [flags]       run a kolo-dht bootstrap server
  kolo-node relay -identity FILE -listen ADDR [flags]
                                             run a kolo-relay server
  kolo-node check [-timeout 5s] [-relay HOST:PORT@KEY] [-relay-file FILE] [HOST:PORT...]
                                             check that kolo-dht servers and kolo-relays answer from here
  kolo-node version                          print the version

Run "kolo-node run -h" or "kolo-node relay -h" for their flags.
`

// Main runs the kolo-node command line args (without the program name) and returns the exit code.
func Main(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, usage)
		return ExitUsage
	}
	switch args[0] {
	case "run":
		return run(ctx, args[1:], env)
	case "relay":
		return relayCmd(ctx, args[1:], env)
	case "check":
		return check(ctx, args[1:], env)
	case "version":
		fmt.Fprintln(env.Stdout, "kolo-node", version(), runtime.Version())
		return ExitOK
	case "help", "-h", "-help", "--help":
		fmt.Fprint(env.Stdout, usage)
		return ExitOK
	}
	fmt.Fprintf(env.Stderr, "kolonode: unknown command %s\n", shownKey(args[0]))
	fmt.Fprint(env.Stderr, usage)
	return ExitUsage
}

func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

// fixedSecure refuses inbound kolo-secure connections, which nobody needs from a bootstrap node, and keeps their limits small.
func fixedSecure() *kolosecure.Config {
	return &kolosecure.Config{
		MaxPending: 16,
		Transport: &kolotransport.Config{
			AcceptFunc:             refuseAll,
			MaxConns:               16,
			MaxDialed:              1,
			MaxIncomingStreams:     1,
			MaxConnReceiveWindow:   64 << 10,
			MaxStreamReceiveWindow: 64 << 10,
		},
	}
}

// refuseAll refuses every inbound QUIC attempt after Retry and before any handshake work.
func refuseAll(netip.AddrPort) bool { return false }

func run(ctx context.Context, args []string, env Env) int {
	o, err := parseRun(args, env.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return ExitOK
	}
	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(env.Stderr, err)
		return ExitUsage
	}
	if err != nil {
		return ExitUsage // the flag package printed it
	}
	log := newLogger(env.Stderr, o.logFormat, o.logLevel)
	if env.SetLogger != nil {
		env.SetLogger(log)
	}
	warnRoot(log)

	id, created, err := loadIdentity(o.identity)
	if err != nil {
		log.Error("identity failed", "err", err)
		return identityExit(err)
	}
	node, err := kolodht.New(ctx, o.listen, id, o.dhtConfig())
	if err != nil {
		log.Error("start failed", "err", err)
		if errors.Is(err, kolodht.ErrInvalidConfig) {
			return ExitUsage
		}
		return ExitError
	}
	if created {
		// Saved only once the node is up, so a config or listen error leaves nothing on disk.
		if env.BeforeSave != nil {
			env.BeforeSave(o.identity)
		}
		if err := id.SaveNew(o.identity); err != nil && !errors.Is(err, koloid.ErrSavedNotSynced) {
			node.Close()
			log.Error("identity failed", "err", err)
			return identityExit(err)
		} else if err != nil {
			log.Warn("identity saved but its directory was not synced", "err", err)
		}
	}
	log.Info("identity", "key", id.PublicKeyString(), "created", created)
	log.Info("started", append([]any{"version", version(), "addr", node.Addr().String()}, o.limitAttrs()...)...)
	if o.first {
		log.Info("network", "mode", "first", "entries", 0)
	} else {
		log.Info("network", "mode", "join", "entries", len(o.nodes), "from", strings.Join(o.from, ","))
	}
	if o.envIgnored != "" {
		log.Warn(envBootstrap+" ignored", "because", o.envIgnored)
	}

	s := &server{node: node, log: log, o: o, last: time.Now()}
	var wg sync.WaitGroup
	wg.Go(func() { s.closeInbound(ctx) })
	wg.Go(func() { s.bootstrap(ctx) })
	wg.Go(func() { s.statsLoop(ctx) })

	<-ctx.Done()
	if env.Stopped != nil {
		env.Stopped()
	}
	log.Info("stopping", "cause", context.Cause(ctx).Error())
	done := make(chan error, 1)
	go func() {
		wg.Wait()
		s.logStats()
		if env.BeforeClose != nil {
			env.BeforeClose()
		}
		done <- node.Close()
	}()
	t := time.NewTimer(shutdownTimeout)
	defer t.Stop()
	select {
	case err := <-done:
		if err != nil {
			log.Error("close failed", "err", err)
			return ExitError
		}
		log.Info("stopped")
		return ExitOK
	case <-t.C:
		log.Error("shutdown timed out", "after", shutdownTimeout.String())
		return ExitError
	}
}

// warnRoot logs that root is not needed, when the command runs as root.
func warnRoot(log *slog.Logger) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		log.Warn("running as root is not needed")
	}
}

// identityExit is ExitUsage for a key file problem that a restart cannot fix, and ExitError otherwise.
func identityExit(err error) int {
	if errors.Is(err, koloid.ErrInsecureKeyFile) || errors.Is(err, koloid.ErrInvalidKeyFile) {
		return ExitUsage
	}
	return ExitError
}

// loadIdentity loads the key file, or generates a new identity that the caller saves with SaveNew if created.
func loadIdentity(path string) (id *koloid.Identity, created bool, err error) {
	id, err = koloid.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		id, err = koloid.Generate()
		return id, err == nil, err
	}
	return id, false, err
}

func (o *options) limitAttrs() []any {
	var out []any
	for _, l := range []struct {
		name string
		v    int64
	}{
		{"dht-max-records", int64(o.maxRecords)},
		{"dht-max-records-per-topic", int64(o.maxRecordsPerTopic)},
		{"dht-max-records-per-ip", int64(o.maxRecordsPerIP)},
		{"dht-max-inflight", int64(o.maxInflight)},
		{"dht-max-relays", int64(o.maxRelays)},
		{"dht-max-requests-per-ip", int64(o.maxRequestsPerIP)},
		{"dht-max-requests-per-prefix", int64(o.maxRequestsPerPrefix)},
		{"dht-max-requests", int64(o.maxRequests)},
	} {
		if l.v != 0 {
			out = append(out, l.name, l.v)
		}
	}
	if o.requestTimeout != 0 {
		out = append(out, "dht-request-timeout", o.requestTimeout.String())
	}
	if o.punchTimeout != 0 {
		out = append(out, "dht-punch-timeout", o.punchTimeout.String())
	}
	return out
}

// server is a running node and what its stats events count.
type server struct {
	node   *kolodht.Node
	log    *slog.Logger
	o      options
	closed atomic.Uint64

	// only the stats loop and, after it ends, the shutdown use these
	last                          time.Time
	requests, limited, closedSeen uint64
}

func (s *server) closeInbound(ctx context.Context) {
	for {
		c, err := s.node.Accept(ctx)
		if err != nil {
			return
		}
		c.CloseWithError(0, "")
		s.closed.Add(1)
	}
}

// bootstrap retries until a listed address answers, waiting bootstrapRetry and doubling up to bootstrapRefresh. Once
// joined, it bootstraps again every bootstrapRefresh while the table holds fewer than refreshBelow servers. The table
// size alone never counts as joined, since any server can enter it by pinging the node.
func (s *server) bootstrap(ctx context.Context) {
	entries := s.o.nodes
	if len(entries) == 0 {
		return
	}
	keep := reachableFrom(s.node.Addr().(*net.UDPAddr).AddrPort().Addr())
	ceiling := max(bootstrapRefresh, bootstrapRetry)
	joined, backoff := false, bootstrapRetry
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if joined && s.node.Stats().Servers >= refreshBelow {
			t.Reset(bootstrapRefresh)
			continue
		}
		ok := s.bootstrapOnce(ctx, entries, keep, backoff)
		if ctx.Err() != nil {
			return
		}
		if !ok {
			t.Reset(backoff)
			backoff = min(2*backoff, ceiling)
			continue
		}
		joined, backoff = true, bootstrapRetry
		t.Reset(bootstrapRefresh)
	}
}

// bootstrapOnce reports whether a listed address answered; a failure is logged with retry, the caller's next wait.
func (s *server) bootstrapOnce(ctx context.Context, entries []entry, keep func(netip.Addr) bool, retry time.Duration) bool {
	r := resolve(ctx, lookupIP, entries, resolveTimeout, keep)
	if ctx.Err() != nil {
		return false
	}
	for name, err := range r.failed {
		s.log.Warn("resolve failed", "entry", name, "err", err)
	}
	if r.skipped > 0 {
		s.log.Warn("bootstrap addresses skipped", "skipped", r.skipped, "reason", "a name resolved outside the scope of the listen address")
	}
	if r.dropped > 0 {
		s.log.Warn("too many bootstrap addresses", "kept", len(r.addrs), "dropped", r.dropped)
	}
	if len(r.addrs) == 0 {
		s.log.Warn("bootstrap failed", "err", "no address", "retry", retry.String())
		return false
	}
	addrs := showAddrs(r.addrs, s.o.logAddrs)
	// A context of its own, since Bootstrap gives up at once on one that resolution used up. Pings last at most a third of
	// bootstrapTimeout, so this deadline is only reached in the lookup after an answer.
	bctx, cancel := context.WithTimeout(ctx, bootstrapTimeout)
	defer cancel()
	err := s.node.Bootstrap(bctx, r.addrs)
	switch {
	case ctx.Err() != nil:
		return false
	case !answered(err, ctx):
		s.log.Warn("bootstrap failed", "addrs", addrs, "err", err, "retry", retry.String())
		return false
	case err != nil:
		s.log.Info("bootstrap", "addrs", addrs, "servers", s.node.Stats().Servers, "lookup", "cut short")
	default:
		s.log.Info("bootstrap", "addrs", addrs, "servers", s.node.Stats().Servers)
	}
	return true
}

func (s *server) statsLoop(ctx context.Context) {
	t := time.NewTicker(s.o.statsInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.logStats()
		}
	}
}

func (s *server) logStats() {
	st := s.node.Stats()
	closed := s.closed.Load()
	now := time.Now()
	s.log.Info("stats",
		"servers", st.Servers,
		"records", st.Records,
		"requests", st.Requests-s.requests,
		"limited", st.Limited-s.limited,
		"closed", closed-s.closedSeen,
		"since", now.Sub(s.last).Round(time.Second).String(),
	)
	s.requests, s.limited, s.closedSeen, s.last = st.Requests, st.Limited, closed, now
}
