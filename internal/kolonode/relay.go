package kolonode

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	koloid "github.com/ciranda-dev/kolo-id"
	kolorelay "github.com/ciranda-dev/kolo-relay"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

// kolo-relay's listener when ServerConfig.Secure is nil (relaySecure in kolo-relay, unexported; datagrams on). Setting -relay-max-conns or
// -relay-max-conns-per-ip, -relay-stream-window, -relay-conn-window or -relay-congestion needs a non-nil Secure, which kolo-relay uses as given, so the command sets all of them.
const (
	relayMaxConns          = 128
	relayMaxConnsPerIP     = 4
	relayMaxDialed         = 1
	relayMaxStreams        = 2
	relayStreamWindow      = 64 << 10
	relayConnWindow        = 256 << 10
	relayMinWindow         = 64 << 10 // kolo-transport's smallest window
	relayMaxStreamWindow   = 16 << 20
	relayMaxConnWindow     = 64 << 20
	relayMaxMemory         = 64 << 30
	relayDefault           = "; 0 means the kolo-relay default"
	relayListenerDefault   = "; 0 means %d, kolo-relay's listener default"
	relayWindowDefault     = "; 0 means %d, kolo-relay's listener default, and each byte costs about 176 bytes of heap per connection"
	allowFileWhat          = "-allow-file"
	openRelayWarning       = "open relay"
	openRelayWarningReason = "ANYONE may reserve and use this relay's bandwidth (-open), and 16 addresses fill the default 64 reservations; pass -allow-file instead to allow only listed keys"
)

// relayOptions holds the settings of the relay command.
type relayOptions struct {
	config    string
	listen    string
	identity  string
	allowFile string
	open      bool
	logFormat string
	logLevel  string

	srv           kolorelay.ServerConfig
	maxConns      int
	maxConnsPerIP int
	streamWindow  int64
	connWindow    int64
	congestion    string

	allowed map[[ed25519.PublicKeySize]byte]bool // from allowFile, set by loadAllow
}

// relayFlags defines the relay flags on a new FlagSet bound to o, setting o to the defaults.
func relayFlags(o *relayOptions, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("kolo-node relay", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.config, "config", "", "JSON config `file`; flags set on the command line override it")
	fs.StringVar(&o.listen, "listen", "", "IPv4 UDP `address` to listen on (required)")
	fs.StringVar(&o.identity, "identity", "", "key `file` of the relay, created on the first run (required)")
	fs.StringVar(&o.allowFile, "allow-file", "", "`file` of the hex keys that may reserve and connect, one per line; required unless -open is given")
	fs.BoolVar(&o.open, "open", false, "run an open relay: any key may reserve and connect, at the operator's cost; required when -allow-file is not given")
	fs.StringVar(&o.logFormat, "log-format", "text", "log `format`: text or json")
	fs.StringVar(&o.logLevel, "log-level", "info", "log `level`: info, warn or error")
	s := &o.srv
	fs.IntVar(&s.MaxReservations, "relay-max-reservations", 0, "kolorelay.ServerConfig.MaxReservations"+relayDefault)
	fs.IntVar(&s.MaxSessions, "relay-max-sessions", 0, "kolorelay.ServerConfig.MaxSessions"+relayDefault)
	fs.IntVar(&s.MaxSessionsPerPeer, "relay-max-sessions-per-peer", 0, "kolorelay.ServerConfig.MaxSessionsPerPeer"+relayDefault)
	fs.IntVar(&s.MaxSessionsPerTarget, "relay-max-sessions-per-target", 0, "kolorelay.ServerConfig.MaxSessionsPerTarget"+relayDefault)
	fs.DurationVar(&s.ReservationLifetime, "relay-reservation-lifetime", 0, "kolorelay.ServerConfig.ReservationLifetime"+relayDefault)
	fs.DurationVar(&s.MaxSessionDuration, "relay-max-session-duration", 0, "kolorelay.ServerConfig.MaxSessionDuration"+relayDefault)
	fs.Int64Var(&s.MaxSessionBytes, "relay-max-session-bytes", 0, "kolorelay.ServerConfig.MaxSessionBytes"+relayDefault)
	fs.DurationVar(&s.SessionIdleTimeout, "relay-session-idle-timeout", 0, "kolorelay.ServerConfig.SessionIdleTimeout"+relayDefault)
	fs.DurationVar(&s.ConnectTimeout, "relay-connect-timeout", 0, "kolorelay.ServerConfig.ConnectTimeout"+relayDefault)
	fs.DurationVar(&s.ControlTimeout, "relay-control-timeout", 0, "kolorelay.ServerConfig.ControlTimeout"+relayDefault)
	fs.Int64Var(&s.RateBytes, "relay-rate-bytes", 0, "kolorelay.ServerConfig.RateBytes, per second"+relayDefault)
	fs.Int64Var(&s.RateBurst, "relay-rate-burst", 0, "kolorelay.ServerConfig.RateBurst"+relayDefault)
	fs.DurationVar(&s.FlushInterval, "relay-flush-interval", 0, "kolorelay.ServerConfig.FlushInterval"+relayDefault)
	fs.IntVar(&o.maxConns, "relay-max-conns", 0, "the listener's kolo-transport MaxConns"+fmt.Sprintf(relayListenerDefault, relayMaxConns))
	fs.IntVar(&o.maxConnsPerIP, "relay-max-conns-per-ip", 0, "the listener's kolo-transport MaxConnsPerIP"+fmt.Sprintf(relayListenerDefault, relayMaxConnsPerIP))
	fs.Int64Var(&o.streamWindow, "relay-stream-window", 0, "the listener's kolo-transport MaxStreamReceiveWindow, in bytes, 64 KiB to 16 MiB"+fmt.Sprintf(relayWindowDefault, relayStreamWindow))
	fs.Int64Var(&o.connWindow, "relay-conn-window", 0, "the listener's kolo-transport MaxConnReceiveWindow, in bytes, 64 KiB to 64 MiB and at least the stream window"+fmt.Sprintf(relayWindowDefault, relayConnWindow))
	fs.StringVar(&o.congestion, "relay-congestion", "", "the listener's kolo-transport CongestionControl: reno, cubic or bbr; empty means reno, kolo-relay's listener default")
	return fs
}

// parseRelay applies the defaults, then the config file, then the flags set in args.
func parseRelay(args []string, out io.Writer) (relayOptions, error) {
	var probe relayOptions
	pfs := relayFlags(&probe, out)
	if err := pfs.Parse(args); err != nil {
		return relayOptions{}, err
	}
	if pfs.NArg() > 0 {
		return relayOptions{}, usageError{errors.New("kolonode: relay takes no arguments, only flags")}
	}
	var o relayOptions
	fs := relayFlags(&o, io.Discard)
	if probe.config != "" {
		b, err := readFile(probe.config, "config")
		if err != nil {
			return relayOptions{}, usageError{err}
		}
		if err := decodeConfig(fs, b); err != nil {
			return relayOptions{}, usageError{fmt.Errorf("kolonode: config %s: %w", probe.config, err)}
		}
	}
	if err := fs.Parse(args); err != nil {
		return relayOptions{}, err
	}
	if err := o.validate(); err != nil {
		return relayOptions{}, usageError{err}
	}
	if err := o.loadAllow(); err != nil {
		return relayOptions{}, usageError{err}
	}
	return o, nil
}

func (o *relayOptions) validate() error {
	switch {
	case o.identity == "":
		return errors.New("kolonode: -identity is required")
	case o.listen == "":
		return errors.New("kolonode: -listen is required")
	case listenIPv6(o.listen):
		return fmt.Errorf("kolonode: -listen: %w", errIPv6)
	case o.logFormat != "text" && o.logFormat != "json":
		return errors.New("kolonode: -log-format must be text or json")
	case o.logLevel != "info" && o.logLevel != "warn" && o.logLevel != "error":
		return errors.New("kolonode: -log-level must be info, warn or error")
	case o.maxConns < 0 || o.maxConnsPerIP < 0:
		return errors.New("kolonode: -relay-max-conns and -relay-max-conns-per-ip must not be negative")
	case o.streamWindow != 0 && (o.streamWindow < relayMinWindow || o.streamWindow > relayMaxStreamWindow):
		return errors.New("kolonode: -relay-stream-window must be 0 or between 64 KiB and 16 MiB")
	case o.connWindow != 0 && (o.connWindow < relayMinWindow || o.connWindow > relayMaxConnWindow):
		return errors.New("kolonode: -relay-conn-window must be 0 or between 64 KiB and 64 MiB")
	case o.congestion != "" && o.congestion != "reno" && o.congestion != "cubic" && o.congestion != "bbr":
		return errors.New("kolonode: -relay-congestion must be reno, cubic or bbr")
	}
	stream, conn := o.windows()
	if stream > conn {
		return errors.New("kolonode: -relay-conn-window must be at least -relay-stream-window")
	}
	if conns, _ := o.conns(); int64(conns+relayMaxDialed)*conn > relayMaxMemory {
		return errors.New("kolonode: -relay-max-conns and -relay-conn-window need more receive window than kolo-transport's 64 GiB limit")
	}
	switch {
	case o.open && o.allowFile != "":
		return errors.New("kolonode: -open and -allow-file cannot be used together")
	case !o.open && o.allowFile == "":
		return errors.New("kolonode: -allow-file is required; pass -open instead to let any key reserve and connect")
	}
	return nil
}

// windows returns the listener's stream and connection windows, with kolo-relay's defaults for 0.
func (o *relayOptions) windows() (stream, conn int64) {
	stream, conn = o.streamWindow, o.connWindow
	if stream == 0 {
		stream = relayStreamWindow
	}
	if conn == 0 {
		conn = relayConnWindow
	}
	return stream, conn
}

// conns returns the listener's MaxConns and MaxConnsPerIP, with kolo-relay's defaults for 0.
func (o *relayOptions) conns() (conns, perIP int) {
	conns, perIP = o.maxConns, o.maxConnsPerIP
	if conns == 0 {
		conns = relayMaxConns
	}
	if perIP == 0 {
		perIP = min(relayMaxConnsPerIP, conns)
	}
	return conns, perIP
}

// loadAllow reads -allow-file under the -bootstrap-file rules: one hex key per line, "#" comments, blank lines
// ignored, a UTF-8 BOM and CRLF accepted, a regular file of at most 64 KiB, and at least one key.
func (o *relayOptions) loadAllow() error {
	if o.allowFile == "" {
		return nil
	}
	b, err := readFile(o.allowFile, allowFileWhat)
	if err != nil {
		return err
	}
	o.allowed = map[[ed25519.PublicKeySize]byte]bool{}
	err = fileEntries(b, func(line int, s string) error {
		k, err := hex.DecodeString(s)
		if err != nil || len(k) != ed25519.PublicKeySize {
			return fmt.Errorf("kolonode: %s %s line %d: want a key of %d hex characters", allowFileWhat, o.allowFile, line, 2*ed25519.PublicKeySize)
		}
		o.allowed[[ed25519.PublicKeySize]byte(k)] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(o.allowed) == 0 {
		return fmt.Errorf("kolonode: %s %s holds no key, so nobody could reserve", allowFileWhat, o.allowFile)
	}
	return nil
}

// serverConfig returns the kolo-relay config of o; zero fields take kolo-relay's defaults.
func (o *relayOptions) serverConfig() *kolorelay.ServerConfig {
	c := o.srv
	if o.maxConns != 0 || o.maxConnsPerIP != 0 || o.streamWindow != 0 || o.connWindow != 0 || o.congestion != "" {
		conns, perIP := o.conns()
		stream, conn := o.windows()
		c.Secure = &kolosecure.Config{Transport: &kolotransport.Config{
			MaxConns:               conns,
			MaxConnsPerIP:          perIP,
			MaxDialed:              relayMaxDialed,
			MaxIncomingStreams:     relayMaxStreams,
			MaxStreamReceiveWindow: stream,
			MaxConnReceiveWindow:   conn,
			CongestionControl:      o.congestion,
			// kolo-relay's listener offers datagrams; a client that opts in keeps its losses instead of waiting on a stream.
			Datagrams: true,
			// kolo-transport refuses less than (MaxConns + MaxDialed) × MaxConnReceiveWindow; kolo-swarm sizes it the same way.
			MaxMemory: max(kolotransport.DefaultMaxMemory, int64(conns+relayMaxDialed)*conn),
		}}
	}
	if o.allowed != nil {
		allowed := o.allowed
		listed := func(k ed25519.PublicKey) bool {
			return len(k) == ed25519.PublicKeySize && allowed[[ed25519.PublicKeySize]byte(k)]
		}
		c.AllowReserve = listed
	}
	return &c
}

func (o *relayOptions) limitAttrs() []any {
	var out []any
	for _, l := range []struct {
		name string
		v    int64
	}{
		{"relay-max-reservations", int64(o.srv.MaxReservations)},
		{"relay-max-sessions", int64(o.srv.MaxSessions)},
		{"relay-max-sessions-per-peer", int64(o.srv.MaxSessionsPerPeer)},
		{"relay-max-sessions-per-target", int64(o.srv.MaxSessionsPerTarget)},
		{"relay-max-session-bytes", o.srv.MaxSessionBytes},
		{"relay-rate-bytes", o.srv.RateBytes},
		{"relay-rate-burst", o.srv.RateBurst},
		{"relay-max-conns", int64(o.maxConns)},
		{"relay-max-conns-per-ip", int64(o.maxConnsPerIP)},
		{"relay-stream-window", o.streamWindow},
		{"relay-conn-window", o.connWindow},
	} {
		if l.v != 0 {
			out = append(out, l.name, l.v)
		}
	}
	if o.congestion != "" {
		out = append(out, "relay-congestion", o.congestion)
	}
	for _, l := range []struct {
		name string
		v    time.Duration
	}{
		{"relay-reservation-lifetime", o.srv.ReservationLifetime},
		{"relay-max-session-duration", o.srv.MaxSessionDuration},
		{"relay-session-idle-timeout", o.srv.SessionIdleTimeout},
		{"relay-connect-timeout", o.srv.ConnectTimeout},
		{"relay-control-timeout", o.srv.ControlTimeout},
		{"relay-flush-interval", o.srv.FlushInterval},
	} {
		if l.v != 0 {
			out = append(out, l.name, l.v.String())
		}
	}
	return out
}

// invalidConfig reports whether err is a config error of kolo-relay or of the listener it starts.
func invalidConfig(err error) bool {
	return errors.Is(err, kolorelay.ErrInvalidConfig) || errors.Is(err, kolosecure.ErrInvalidConfig) || errors.Is(err, kolotransport.ErrInvalidConfig)
}

// relayCmd runs a kolo-relay server until ctx ends.
func relayCmd(ctx context.Context, args []string, env Env) int {
	o, err := parseRelay(args, env.Stderr)
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
	srv, err := kolorelay.Listen(ctx, o.listen, id, o.serverConfig())
	if err != nil {
		log.Error("start failed", "err", err)
		if invalidConfig(err) || errors.Is(err, kolorelay.ErrInvalidArgument) {
			return ExitUsage
		}
		return ExitError
	}
	if created {
		if env.BeforeSave != nil {
			env.BeforeSave(o.identity)
		}
		if err := id.SaveNew(o.identity); err != nil && !errors.Is(err, koloid.ErrSavedNotSynced) {
			srv.Close()
			log.Error("identity failed", "err", err)
			return identityExit(err)
		} else if err != nil {
			log.Warn("identity saved but its directory was not synced", "err", err)
		}
	}
	log.Info("identity", "key", id.PublicKeyString(), "created", created)
	log.Info("started", append([]any{"version", version(), "addr", srv.Addr().String()}, o.limitAttrs()...)...)
	if o.open {
		log.Warn(openRelayWarning, "reason", openRelayWarningReason)
	} else {
		log.Info("allow", "keys", len(o.allowed))
	}

	<-ctx.Done()
	if env.Stopped != nil {
		env.Stopped()
	}
	log.Info("stopping", "cause", context.Cause(ctx).Error())
	return closeWithin(log, func() error {
		if env.BeforeClose != nil {
			env.BeforeClose()
		}
		return srv.Close()
	})
}

// closeWithin runs closeFn, logs stopped or the failure, and gives up after shutdownTimeout.
func closeWithin(log *slog.Logger, closeFn func() error) int {
	done := make(chan error, 1)
	go func() { done <- closeFn() }()
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
