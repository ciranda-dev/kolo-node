package kolonode

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
	kolorelay "github.com/ciranda-dev/kolo-relay"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

const (
	noAnswer            = "no kolo-dht server answered"
	defaultCheckTimeout = 5 * time.Second
)

// check pings each HOST:PORT from a fresh client node and asks each listed relay for a random key, and exits 0 if
// every address answered as a kolo-dht server and every relay as a kolo-relay.
func check(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("kolo-node check", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	timeout := fs.Duration("timeout", defaultCheckTimeout, "time allowed per address, 1s to 60s")
	var relays, relayFiles []string
	var relaySet bool
	fs.Var(&listValue{p: &relays, set: &relaySet, split: true}, "relay", "comma-separated `host:port@key` list of kolo-relays, repeatable")
	fs.Var(&listValue{p: &relayFiles, set: &relaySet}, "relay-file", "`file` of kolo-relays, one host:port@key per line, repeatable")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: kolo-node check [-timeout 5s] [-relay HOST:PORT@KEY] [-relay-file FILE] [HOST:PORT...]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		return ExitUsage
	}
	if *timeout < time.Second || *timeout > time.Minute {
		fmt.Fprintln(env.Stderr, "kolonode: -timeout must be between 1s and 1m")
		return ExitUsage
	}
	rs, err := loadRelays(relays, relayFiles, relaySet)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitUsage
	}
	if fs.NArg() == 0 && len(rs) == 0 {
		fmt.Fprintf(env.Stderr, "kolonode: check takes addresses, or relays from -relay, -relay-file or %s\n", envRelays)
		return ExitUsage
	}
	entries := make([]entry, fs.NArg())
	for i, a := range fs.Args() {
		e, err := parseEntry(a)
		if err != nil {
			fmt.Fprintf(env.Stderr, "kolonode: address %d: %v\n", i+1, err)
			return ExitUsage
		}
		entries[i] = e
	}

	code := ExitOK
	for _, e := range entries {
		r := resolve(ctx, lookupIP, []entry{e}, *timeout, nil)
		if err := r.failed[e.String()]; err != nil {
			fmt.Fprintf(env.Stdout, "failed %s: %v\n", e, err)
			code = ExitError
			continue
		}
		if r.dropped > 0 {
			fmt.Fprintf(env.Stderr, "kolonode: %s: only its first %d addresses are checked\n", e, maxPerEntry)
		}
		for _, a := range r.addrs {
			ok, err := pingAddr(ctx, a, *timeout)
			switch {
			case err != nil:
				fmt.Fprintln(env.Stderr, err)
				return ExitError
			case ok:
				fmt.Fprintf(env.Stdout, "ok %s\n", a)
			default:
				fmt.Fprintf(env.Stdout, "failed %s: %s\n", a, noAnswer)
				code = ExitError
			}
		}
		if ctx.Err() != nil {
			return ExitError
		}
	}
	for _, r := range rs {
		res := resolve(ctx, lookupIP, []entry{r.addr}, *timeout, nil)
		if err := res.failed[r.addr.String()]; err != nil {
			fmt.Fprintf(env.Stdout, "failed relay %s: %v\n", r, err)
			code = ExitError
			continue
		}
		for _, a := range res.addrs {
			at := relayEntry{addr: entry{host: a.Addr().String(), ip: a.Addr(), port: a.Port()}, key: r.key}
			if err := pingRelayAddr(ctx, a, r.key, *timeout); err != nil {
				fmt.Fprintf(env.Stdout, "failed relay %s: %v\n", at, err)
				code = ExitError
			} else {
				fmt.Fprintf(env.Stdout, "ok relay %s\n", at)
			}
		}
		if ctx.Err() != nil {
			return ExitError
		}
	}
	return code
}

// pingRelayAddr is pingRelay, a variable so tests can replace it.
var pingRelayAddr = pingRelay

// pingRelay asks the relay at a, whose key is key, for a new random key with a new identity: ErrUnavailable proves
// the address, the key (kolo-secure refuses another) and the protocol, and reserves nothing.
func pingRelay(ctx context.Context, a netip.AddrPort, key ed25519.PublicKey, timeout time.Duration) error {
	id, err := koloid.Generate()
	if err != nil {
		return err
	}
	target, err := koloid.Generate()
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := kolorelay.Dial(dctx, a.String(), key, id, target.PublicKey(), nil)
	switch {
	case err == nil:
		c.Close()
		return nil
	case errors.Is(err, kolorelay.ErrUnavailable):
		return nil
	case dctx.Err() != nil && ctx.Err() == nil:
		return errors.New("no kolo-relay answered")
	}
	return err
}

// Variables so tests can keep check's client on loopback and replace its pings.
var (
	checkListen   = ":0"
	pingAddr      = ping
	checkStarted  func(*kolodht.Node) // runs once check's client is up
	checkFinished func(*kolodht.Node) // runs once its Bootstrap returned
)

// ping reports whether a kolo-dht server answered from a, using a new client that refuses inbound connections. Each
// request waits half of timeout, so the deadline is only reached after an answer. The routing table is no evidence:
// any server that pings the client enters it.
func ping(ctx context.Context, a netip.AddrPort, timeout time.Duration) (bool, error) {
	id, err := koloid.Generate()
	if err != nil {
		return false, err
	}
	cfg := &kolodht.Config{
		RequestTimeout: min(timeout/2, dhtMaxRequestTimeout),
		Secure:         &kolosecure.Config{Transport: &kolotransport.Config{AcceptFunc: refuseAll}},
	}
	node, err := kolodht.New(ctx, checkListen, id, cfg)
	if err != nil {
		return false, err
	}
	defer node.Close()
	if checkStarted != nil {
		checkStarted(node)
	}
	bctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err = node.Bootstrap(bctx, []netip.AddrPort{a})
	if checkFinished != nil {
		checkFinished(node)
	}
	return answered(err, ctx), nil
}

// answered reads Bootstrap's result: nil, or the deadline reached in the lookup after a's answer (the parent context
// still running) means a answered; ErrNoNodes and anything else mean it did not.
func answered(err error, parent context.Context) bool {
	return err == nil || errors.Is(err, context.DeadlineExceeded) && parent.Err() == nil
}
