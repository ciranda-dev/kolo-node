package kolonode

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	kolodht "github.com/ciranda-dev/kolo-dht"
)

const (
	maxConfigSize  = 64 << 10
	maxEntries     = 16
	minStats       = time.Second
	maxStats       = 24 * time.Hour
	defaultListen  = ":7447"
	defaultStats   = 5 * time.Minute
	maxShownKeyLen = 64
)

// envBootstrap is the environment variable read for entry nodes when no -bootstrap, -bootstrap-file or -first is set.
const envBootstrap = "KOLO_BOOTSTRAP"

// fileChecked and fileOpened, if set, run once the config file or a bootstrap file passed its path check and once it is
// open; tests swap the path then.
var fileChecked, fileOpened func(path string)

// options holds the settings of the run command.
type options struct {
	config        string
	listen        string
	identity      string
	bootstrap     []string
	bootFiles     []string
	bootSet       bool // -bootstrap or its key was set, even to nothing
	bootFilesSet  bool
	first         bool
	logFormat     string
	logLevel      string
	logAddrs      bool
	statsInterval time.Duration

	maxRecords           int
	maxRecordsPerTopic   int
	maxRecordsPerIP      int
	maxInflight          int
	maxRelays            int
	maxRequestsPerIP     int
	maxRequestsPerPrefix int
	maxRequests          int
	requestTimeout       time.Duration
	punchTimeout         time.Duration

	nodes      []entry  // the entry nodes, set by loadNodes
	from       []string // where they came from
	envIgnored string   // what made loadNodes ignore a set KOLO_BOOTSTRAP
}

// listValue is a repeatable list flag, comma-separated if split; the first Set replaces any value from the config file.
type listValue struct {
	p       *[]string
	set     *bool
	split   bool
	cleared bool
}

func (l *listValue) String() string {
	if l == nil || l.p == nil {
		return ""
	}
	return strings.Join(*l.p, ",")
}

func (l *listValue) Set(s string) error {
	*l.set = true
	if !l.cleared {
		*l.p = nil
		l.cleared = true
	}
	if l.split {
		*l.p = append(*l.p, splitList(s)...)
	} else if s != "" {
		*l.p = append(*l.p, s)
	}
	return nil
}

// fileEntries calls f with each entry of a bootstrap file and its line number: one host:port per line, "#" starts a
// comment, blank lines are ignored, and a UTF-8 BOM and CRLF line ends are accepted. It stops at f's first error.
func fileEntries(b []byte, f func(line int, entry string) error) error {
	n := 0
	for line := range strings.Lines(strings.TrimPrefix(string(b), "\ufeff")) {
		n++
		line, _, _ = strings.Cut(line, "#")
		for _, e := range splitList(line) {
			if err := f(n, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// splitList returns the entries of a comma-separated list, trimmed, without empty ones.
func splitList(s string) []string {
	var out []string
	for e := range strings.SplitSeq(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

func (l *listValue) Get() any { return *l.p }

const dhtDefault = "; 0 means the kolo-dht default"

// runFlags defines the run flags on a new FlagSet bound to o, setting o to the defaults.
func runFlags(o *options, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("kolo-node run", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.config, "config", "", "JSON config `file`; flags set on the command line override it")
	fs.StringVar(&o.listen, "listen", defaultListen, "IPv4 UDP `address` to listen on")
	fs.StringVar(&o.identity, "identity", "", "key `file`, created on the first run (required)")
	fs.Var(&listValue{p: &o.bootstrap, set: &o.bootSet, split: true}, "bootstrap", "comma-separated `host:port` list of entry nodes, repeatable")
	fs.Var(&listValue{p: &o.bootFiles, set: &o.bootFilesSet}, "bootstrap-file", "`file` of entry nodes, one host:port per line, repeatable")
	fs.BoolVar(&o.first, "first", false, "start a new network: no entry nodes yet")
	fs.StringVar(&o.logFormat, "log-format", "text", "log `format`: text or json")
	fs.StringVar(&o.logLevel, "log-level", "info", "log `level`: info, warn or error")
	fs.BoolVar(&o.logAddrs, "log-addrs", false, "log full peer addresses instead of their /24")
	fs.DurationVar(&o.statsInterval, "stats-interval", defaultStats, "interval between stats events, 1s to 24h")
	fs.IntVar(&o.maxRecords, "dht-max-records", 0, "kolodht.Config.MaxRecords"+dhtDefault)
	fs.IntVar(&o.maxRecordsPerTopic, "dht-max-records-per-topic", 0, "kolodht.Config.MaxRecordsPerTopic"+dhtDefault)
	fs.IntVar(&o.maxRecordsPerIP, "dht-max-records-per-ip", 0, "kolodht.Config.MaxRecordsPerIP"+dhtDefault)
	fs.IntVar(&o.maxInflight, "dht-max-inflight", 0, "kolodht.Config.MaxInflight"+dhtDefault)
	fs.IntVar(&o.maxRelays, "dht-max-relays", 0, "kolodht.Config.MaxRelays"+dhtDefault)
	fs.IntVar(&o.maxRequestsPerIP, "dht-max-requests-per-ip", 0, "kolodht.Config.MaxRequestsPerIP"+dhtDefault)
	fs.IntVar(&o.maxRequestsPerPrefix, "dht-max-requests-per-prefix", 0, "kolodht.Config.MaxRequestsPerPrefix"+dhtDefault)
	fs.IntVar(&o.maxRequests, "dht-max-requests", 0, "kolodht.Config.MaxRequests"+dhtDefault)
	fs.DurationVar(&o.requestTimeout, "dht-request-timeout", 0, "kolodht.Config.RequestTimeout"+dhtDefault)
	fs.DurationVar(&o.punchTimeout, "dht-punch-timeout", 0, "kolodht.Config.PunchTimeout"+dhtDefault)
	return fs
}

// usageError is a flag or config error; the run command exits with code 2.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// parseRun applies the defaults, then the config file, then the flags set in args.
func parseRun(args []string, out io.Writer) (options, error) {
	var probe options
	pfs := runFlags(&probe, out)
	if err := pfs.Parse(args); err != nil {
		return options{}, err
	}
	if pfs.NArg() > 0 {
		return options{}, usageError{errors.New("kolonode: run takes no arguments, only flags")}
	}
	var o options
	fs := runFlags(&o, io.Discard)
	if probe.config != "" {
		b, err := readFile(probe.config, "config")
		if err != nil {
			return options{}, usageError{err}
		}
		if err := decodeConfig(fs, b); err != nil {
			return options{}, usageError{fmt.Errorf("kolonode: config %s: %w", probe.config, err)}
		}
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if err := o.validate(); err != nil {
		return options{}, usageError{err}
	}
	if err := o.loadNodes(); err != nil {
		return options{}, usageError{err}
	}
	return o, nil
}

// loadNodes sets o.nodes to the -bootstrap entries, then each -bootstrap-file's in order, without duplicates, or else
// to KOLO_BOOTSTRAP. Only -first starts a node without entry nodes, so a list left empty by mistake fails, and a flag
// or key set to an empty list never falls back to KOLO_BOOTSTRAP.
func (o *options) loadNodes() error {
	given := o.bootSet || o.bootFilesSet
	if o.first && given {
		return errors.New("kolonode: -first starts a new network, so it takes no -bootstrap or -bootstrap-file")
	}
	seen := map[string]bool{}
	add := func(s string) (bool, error) {
		e, err := parseEntry(s)
		if err != nil {
			return false, err
		}
		if !seen[e.String()] {
			seen[e.String()] = true
			o.nodes = append(o.nodes, e)
		}
		return len(o.nodes) > maxEntries, nil
	}
	tooMany := fmt.Errorf("kolonode: more than %d bootstrap entries in all", maxEntries)
	for i, s := range o.bootstrap {
		if over, err := add(s); err != nil {
			return fmt.Errorf("kolonode: -bootstrap entry %d: %w", i+1, err)
		} else if over {
			return tooMany
		}
	}
	if len(o.bootstrap) > 0 {
		o.from = append(o.from, "-bootstrap")
	}
	for _, p := range o.bootFiles {
		b, err := readFile(p, "-bootstrap-file")
		if err != nil {
			return err
		}
		err = fileEntries(b, func(line int, s string) error {
			if over, err := add(s); err != nil {
				return fmt.Errorf("kolonode: -bootstrap-file %s line %d: %w", p, line, err)
			} else if over {
				return tooMany
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(o.bootFiles) > 0 {
		o.from = append(o.from, "-bootstrap-file")
	}
	switch env := os.Getenv(envBootstrap) != ""; {
	case env && o.first:
		o.envIgnored = "-first"
	case env && given:
		o.envIgnored = "-bootstrap or -bootstrap-file"
	}
	if !given && !o.first {
		for i, s := range splitList(os.Getenv(envBootstrap)) {
			if over, err := add(s); err != nil {
				return fmt.Errorf("kolonode: %s entry %d: %w", envBootstrap, i+1, err)
			} else if over {
				return tooMany
			}
		}
		if len(o.nodes) > 0 {
			o.from = append(o.from, envBootstrap)
		}
	}
	switch {
	case !o.first && len(o.nodes) == 0:
		return fmt.Errorf("kolonode: no entry nodes: give -bootstrap, -bootstrap-file or %s, or -first to start a new network", envBootstrap)
	case len(o.nodes) > 0 && o.maxInflight != 0 && o.maxInflight < minInflightBootstrap:
		return fmt.Errorf("kolonode: -dht-max-inflight must be at least %d with entry nodes, so every bootstrap ping gets a request slot", minInflightBootstrap)
	}
	return nil
}

// readFile reads the regular file of the config or a bootstrap file (what names it in errors), of at most maxConfigSize
// bytes. It refuses non-regular files before opening, so a device or FIFO is never opened, and checks the opened file
// again against a swap.
func readFile(path, what string) ([]byte, error) {
	if st, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("kolonode: %s: %w", what, err)
	} else if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("kolonode: %s %s: not a regular file", what, path)
	}
	if fileChecked != nil {
		fileChecked(path)
	}
	f, err := openFile(path)
	if err != nil {
		return nil, fmt.Errorf("kolonode: %s: %w", what, err)
	}
	defer f.Close()
	if fileOpened != nil {
		fileOpened(path)
	}
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("kolonode: %s: %w", what, err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("kolonode: %s %s: not a regular file", what, path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return nil, fmt.Errorf("kolonode: %s: %w", what, err)
	}
	if len(b) > maxConfigSize {
		return nil, fmt.Errorf("kolonode: %s %s: larger than %d bytes", what, path, maxConfigSize)
	}
	return b, nil
}

// shownKey quotes a config key for an error, cut to a bounded length.
func shownKey(k string) string {
	if utf8.RuneCountInString(k) > maxShownKeyLen {
		k = string([]rune(k)[:maxShownKeyLen]) + "..."
	}
	return strconv.Quote(k)
}

// decodeConfig sets the flags of fs from a JSON object whose keys are their names, except config.
// Keys match exactly and once; null, unknown keys, wrong types and trailing data are errors.
func decodeConfig(fs *flag.FlagSet, b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return syntaxError(err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("want a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return syntaxError(err)
		}
		key := tok.(string)
		fl := fs.Lookup(key)
		if fl == nil || key == "config" {
			return fmt.Errorf("key %s: unknown", shownKey(key))
		}
		if seen[key] {
			return fmt.Errorf("key %s: repeated", shownKey(key))
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return syntaxError(err)
		}
		if err := setFromJSON(fl, raw); err != nil {
			return fmt.Errorf("key %s: %w", shownKey(key), err)
		}
	}
	if _, err := dec.Token(); err != nil {
		return syntaxError(err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the JSON object")
	}
	return nil
}

func syntaxError(err error) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return fmt.Errorf("invalid JSON at offset %d", se.Offset)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("unexpected end of JSON")
	}
	return errors.New("invalid JSON")
}

func setFromJSON(fl *flag.Flag, raw json.RawMessage) error {
	if string(raw) == "null" {
		return errors.New("null is not a value")
	}
	switch fl.Value.(flag.Getter).Get().(type) {
	case []string:
		var v []string
		if json.Unmarshal(raw, &v) != nil {
			return errors.New("want an array of strings")
		}
		if v == nil {
			v = []string{}
		}
		lv := fl.Value.(*listValue)
		*lv.p, *lv.set = v, true
		return nil
	case string:
		var v string
		if json.Unmarshal(raw, &v) != nil {
			return errors.New("want a string")
		}
		return fl.Value.Set(v)
	case bool:
		var v bool
		if json.Unmarshal(raw, &v) != nil {
			return errors.New("want true or false")
		}
		return fl.Value.Set(strconv.FormatBool(v))
	case int:
		var v int
		if json.Unmarshal(raw, &v) != nil {
			return errors.New("want an integer")
		}
		return fl.Value.Set(strconv.Itoa(v))
	case int64:
		var v int64
		if json.Unmarshal(raw, &v) != nil {
			return errors.New("want an integer")
		}
		return fl.Value.Set(strconv.FormatInt(v, 10))
	case time.Duration:
		var v string
		if json.Unmarshal(raw, &v) != nil || fl.Value.Set(v) != nil {
			return errors.New("want a duration string such as \"5m\"")
		}
		return nil
	}
	return errors.New("unsupported key")
}

func (o *options) validate() error {
	switch {
	case o.identity == "":
		return errors.New("kolonode: -identity is required")
	case o.listen == "":
		return errors.New("kolonode: -listen is empty")
	case listenIPv6(o.listen):
		return fmt.Errorf("kolonode: -listen: %w", errIPv6)
	case o.logFormat != "text" && o.logFormat != "json":
		return errors.New("kolonode: -log-format must be text or json")
	case o.logLevel != "info" && o.logLevel != "warn" && o.logLevel != "error":
		return errors.New("kolonode: -log-level must be info, warn or error")
	case o.statsInterval < minStats || o.statsInterval > maxStats:
		return errors.New("kolonode: -stats-interval must be between 1s and 24h")
	}
	return nil
}

// listenIPv6 reports whether addr holds an IPv6 form: an IPv4 host:port has one colon, any IPv6 address two or more.
func listenIPv6(addr string) bool { return strings.Count(addr, ":") > 1 }

// dhtConfig returns the kolo-dht config of a server with o's limits; zero fields take kolo-dht's defaults.
func (o *options) dhtConfig() *kolodht.Config {
	return &kolodht.Config{
		Secure:               fixedSecure(),
		Server:               true,
		MaxRecords:           o.maxRecords,
		MaxRecordsPerTopic:   o.maxRecordsPerTopic,
		MaxRecordsPerIP:      o.maxRecordsPerIP,
		MaxInflight:          o.maxInflight,
		MaxRelays:            o.maxRelays,
		MaxRequestsPerIP:     o.maxRequestsPerIP,
		MaxRequestsPerPrefix: o.maxRequestsPerPrefix,
		MaxRequests:          o.maxRequests,
		RequestTimeout:       o.requestTimeout,
		PunchTimeout:         o.punchTimeout,
	}
}
