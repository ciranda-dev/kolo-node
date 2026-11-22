package kolonode

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigFileSetsEveryKey(t *testing.T) {
	nodes := writeConfig(t, "c.example.org:7447\n")
	p := writeConfig(t, `{
		"listen": "127.0.0.1:9000",
		"identity": "/x/node.key",
		"bootstrap": ["a.example.org:7447", "203.0.113.20:7447"],
		"bootstrap-file": [`+strconv.Quote(nodes)+`],
		"first": false,
		"log-format": "json",
		"log-level": "warn",
		"log-addrs": true,
		"stats-interval": "30s",
		"dht-max-records": 100,
		"dht-max-records-per-topic": 2,
		"dht-max-records-per-ip": 3,
		"dht-max-inflight": 90,
		"dht-max-relays": 5,
		"dht-max-requests-per-ip": 6,
		"dht-max-requests-per-prefix": 7,
		"dht-max-requests": 8,
		"dht-request-timeout": "2s",
		"dht-punch-timeout": "3s"
	}`)
	o, err := parseRun([]string{"-config", p}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := options{
		config: p, listen: "127.0.0.1:9000", identity: "/x/node.key",
		bootstrap: []string{"a.example.org:7447", "203.0.113.20:7447"}, bootFiles: []string{nodes},
		bootSet: true, bootFilesSet: true,
		nodes:     entries(t, "a.example.org:7447", "203.0.113.20:7447", "c.example.org:7447"),
		from:      []string{"-bootstrap", "-bootstrap-file"},
		logFormat: "json", logLevel: "warn", logAddrs: true, statsInterval: 30 * time.Second,
		maxRecords: 100, maxRecordsPerTopic: 2, maxRecordsPerIP: 3, maxInflight: 90, maxRelays: 5,
		maxRequestsPerIP: 6, maxRequestsPerPrefix: 7, maxRequests: 8,
		requestTimeout: 2 * time.Second, punchTimeout: 3 * time.Second,
	}
	if !reflect.DeepEqual(o, want) {
		t.Fatalf("options = %+v, want %+v", o, want)
	}
}

func TestConfigEveryFlagIsAKey(t *testing.T) {
	var o options
	fs := runFlags(&o, io.Discard)
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name == "config" {
			return
		}
		if _, ok := f.Value.(flag.Getter); !ok {
			t.Errorf("flag %s has no Get", f.Name)
		}
	})
}

func TestConfigRejects(t *testing.T) {
	for _, tc := range []struct{ name, json, want string }{
		{"unknown", `{"identity": "/k", "nope": 1}`, `key "nope": unknown`},
		{"wrong case", `{"identity": "/k", "Listen": ":1"}`, `key "Listen": unknown`},
		{"config key", `{"identity": "/k", "config": "/x"}`, `key "config": unknown`},
		{"repeated", `{"identity": "/k", "listen": ":1", "listen": ":2"}`, `key "listen": repeated`},
		{"repeated escaped", `{"identity": "/k", "listen": ":1", "list\u0065n": ":2"}`, `key "listen": repeated`},
		{"repeated escaped first", `{"identity": "/k", "\u006cisten": ":1", "listen": ":2"}`, `key "listen": repeated`},
		{"escaped wrong case", `{"identity": "/k", "\u004cisten": ":1"}`, `key "Listen": unknown`},
		{"null", `{"identity": null}`, `key "identity": null`},
		{"float", `{"identity": "/k", "dht-max-records": 1.5}`, "want an integer"},
		{"exponent", `{"identity": "/k", "dht-max-records": 1e3}`, "want an integer"},
		{"int as string", `{"identity": "/k", "dht-max-records": "SECRETVALUE"}`, "want an integer"},
		{"string as array", `{"identity": ["SECRETVALUE"]}`, "want a string"},
		{"bool as string", `{"identity": "/k", "log-addrs": "SECRETVALUE"}`, "want true or false"},
		{"duration as number", `{"identity": "/k", "stats-interval": 5}`, "want a duration"},
		{"bad duration", `{"identity": "/k", "stats-interval": "SECRETVALUE"}`, "want a duration"},
		{"list of numbers", `{"identity": "/k", "bootstrap": [1]}`, "want an array of strings"},
		{"trailing object", `{"identity": "/k"} {}`, "data after the JSON object"},
		{"trailing junk", `{"identity": "/k"} x`, "data after the JSON object"},
		{"array", `[]`, "want a JSON object"},
		{"empty", ``, "unexpected end of JSON"},
		{"truncated", `{"identity": "/k"`, "invalid JSON"},
		{"syntax", `{"identity" "/k"}`, "invalid JSON"},
		{"stats range", `{"identity": "/k", "stats-interval": "100ms"}`, "-stats-interval"},
		{"bad entry", `{"identity": "/k", "bootstrap": ["SECRETVALUE"]}`, "-bootstrap entry 1"},
		{"comma in an entry", `{"identity": "/k", "bootstrap": ["a.example.org:1,b.example.org:2"]}`, "-bootstrap entry 1: want host:port"},
		{"long key", `{"` + strings.Repeat("k", 1000) + `": 1}`, `...": unknown`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseRun([]string{"-config", writeConfig(t, tc.json)}, io.Discard)
			var ue usageError
			if !errors.As(err, &ue) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want a usage error containing %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "SECRETVALUE") || len(err.Error()) > 300 {
				t.Fatalf("error %q shows the value or is too long", err)
			}
		})
	}
}

func TestConfigFiles(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, []byte(`{"identity": "/k", "listen": "`+strings.Repeat(" ", maxConfigSize)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	limit := filepath.Join(dir, "limit.json")
	body := `{"identity": "/k", "first": true}`
	if err := os.WriteFile(limit, []byte(body+strings.Repeat(" ", maxConfigSize-len(body))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseRun([]string{"-config", limit}, io.Discard); err != nil {
		t.Fatalf("a file of exactly %d bytes: %v", maxConfigSize, err)
	}
	for name, tc := range map[string]struct{ path, want string }{
		"too large": {big, "larger than"},
		"directory": {dir, "not a regular file"},
		"missing":   {filepath.Join(dir, "none.json"), "kolonode: config"},
	} {
		_, err := parseRun([]string{"-config", tc.path}, io.Discard)
		var ue usageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want a usage error containing %q", name, err, tc.want)
		}
	}
}

func TestPrecedence(t *testing.T) {
	p := writeConfig(t, `{"identity": "/k", "listen": "127.0.0.1:9000", "bootstrap": ["a.example.org:1", "b.example.org:2"], "dht-max-records": 100}`)
	o, err := parseRun([]string{"-bootstrap", "c.example.org:3", "-config", p, "-bootstrap", "d.example.org:4,e.example.org:5", "-dht-max-records", "200"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.listen != "127.0.0.1:9000" || o.maxRecords != 200 || o.statsInterval != defaultStats || o.logFormat != "text" {
		t.Fatalf("options = %+v", o)
	}
	if want := []string{"c.example.org:3", "d.example.org:4", "e.example.org:5"}; !reflect.DeepEqual(o.bootstrap, want) {
		t.Fatalf("bootstrap = %v, want %v (flags replace the file list)", o.bootstrap, want)
	}
	o, err = parseRun([]string{"-identity", "/k", "-first"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.listen != defaultListen || o.bootstrap != nil || o.nodes != nil || o.logLevel != "info" || o.logAddrs {
		t.Fatalf("defaults = %+v", o)
	}
}

func TestFlagErrors(t *testing.T) {
	t.Setenv(envBootstrap, "")
	empty := writeConfig(t, "# no entries yet\n")
	for _, tc := range []struct {
		args  []string
		usage bool // a usageError; otherwise the flag package reported it
		want  string
	}{
		{[]string{"-identity", "/k", "-nope"}, false, ""},
		{[]string{"-identity", "/k", "extra"}, true, "no arguments"},
		{[]string{}, true, "-identity is required"},
		{[]string{"-identity", "/k", "-listen", ""}, true, "-listen"},
		{[]string{"-identity", "/k", "-log-format", "xml"}, true, "-log-format"},
		{[]string{"-identity", "/k", "-log-level", "debug"}, true, "-log-level"},
		{[]string{"-identity", "/k", "-stats-interval", "25h"}, true, "-stats-interval"},
		{[]string{"-identity", "/k"}, true, "no entry nodes"},
		{[]string{"-identity", "/k", "-bootstrap", " , "}, true, "no entry nodes"},
		{[]string{"-identity", "/k", "-bootstrap-file", empty}, true, "no entry nodes"},
		{[]string{"-identity", "/k", "-first", "-bootstrap", "203.0.113.1:1"}, true, "-first starts a new network"},
		{[]string{"-identity", "/k", "-first", "-bootstrap-file", empty}, true, "-first starts a new network"},
		{[]string{"-identity", "/k", "-bootstrap", "a.example.org:1,bad"}, true, "entry 2"},
		{[]string{"-identity", "/k", "-bootstrap", "0.0.0.0:7447"}, true, "entry 1"},
		{[]string{"-identity", "/k", "-bootstrap", "203.0.113.1:1", "-dht-max-inflight", "72"}, true, "-dht-max-inflight"},
	} {
		_, err := parseRun(tc.args, io.Discard)
		var ue usageError
		if err == nil || errors.As(err, &ue) != tc.usage || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error = %v, want usage %v containing %q", tc.args, err, tc.usage, tc.want)
		}
		if code := Main(t.Context(), append([]string{"run"}, tc.args...), Env{Stdout: io.Discard, Stderr: io.Discard}); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", tc.args, code, ExitUsage)
		}
	}
}

func TestDHTConfig(t *testing.T) {
	o, err := parseRun([]string{"-identity", "/k", "-first"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	c := o.dhtConfig()
	if !c.Server || c.Secure == nil || c.Secure.Transport == nil {
		t.Fatalf("config = %+v, want a server with the fixed kolo-secure config", c)
	}
	c.Secure = nil
	if !reflect.DeepEqual(*c, kolodht.Config{Server: true}) {
		t.Fatalf("unset limits = %+v, want zero (kolo-dht defaults)", *c)
	}
	o, err = parseRun([]string{"-identity", "/k", "-first",
		"-dht-max-records", "1", "-dht-max-records-per-topic", "2", "-dht-max-records-per-ip", "3",
		"-dht-max-inflight", "4", "-dht-max-relays", "5", "-dht-max-requests-per-ip", "6",
		"-dht-max-requests-per-prefix", "7", "-dht-max-requests", "8",
		"-dht-request-timeout", "9s", "-dht-punch-timeout", "10s"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	c = o.dhtConfig()
	c.Secure = nil
	want := kolodht.Config{Server: true, MaxRecords: 1, MaxRecordsPerTopic: 2, MaxRecordsPerIP: 3, MaxInflight: 4,
		MaxRelays: 5, MaxRequestsPerIP: 6, MaxRequestsPerPrefix: 7, MaxRequests: 8,
		RequestTimeout: 9 * time.Second, PunchTimeout: 10 * time.Second}
	if !reflect.DeepEqual(*c, want) {
		t.Fatalf("config = %+v, want %+v", *c, want)
	}
}

func TestInflightWithBootstrap(t *testing.T) {
	for _, args := range [][]string{
		{"-identity", "/k", "-bootstrap", "203.0.113.1:1", "-dht-max-inflight", "75"},
		{"-identity", "/k", "-bootstrap", "203.0.113.1:1"},
		{"-identity", "/k", "-first", "-dht-max-inflight", "8"},
	} {
		if _, err := parseRun(args, io.Discard); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	if _, err := parseRun([]string{"-identity", "/k", "-bootstrap", "203.0.113.1:1", "-dht-max-inflight", "74"}, io.Discard); err == nil {
		t.Error("-dht-max-inflight 74 with -bootstrap: accepted, want an error (64 pings, 10 check pings and 1 maintenance request)")
	}
	// kolo-dht pings every kept address, and the PING step, one RequestTimeout (TestBootstrapPingsEveryAddress),
	// lasts a third of the bootstrap timeout at most: a deadline reached in Bootstrap then falls in the lookup after an answer.
	const dhtMaxBoot = 64 // kolo-dht maxBoot, the most addresses Bootstrap pings
	if maxAddrs > dhtMaxBoot || bootstrapTimeout < 3*dhtMaxRequestTimeout {
		t.Fatalf("maxAddrs %d, bootstrapTimeout %v: kolo-dht may skip addresses or the PING step may not end inside the bootstrap timeout", maxAddrs, bootstrapTimeout)
	}
}

func TestRefusesIPv6(t *testing.T) {
	for _, args := range [][]string{
		{"-identity", "/k", "-listen", "[::]:7447"},
		{"-identity", "/k", "-listen", "[::1]:0"},
		{"-identity", "/k", "-listen", "[::ffff:127.0.0.1]:7447"},
		{"-identity", "/k", "-listen", "::1:7447"},
		{"-identity", "/k", "-listen", "[::1]"},
		{"-identity", "/k", "-bootstrap", "[2001:db8::1]:7447"},
		{"-identity", "/k", "-bootstrap", "203.0.113.1:1,[::ffff:203.0.113.2]:7447"},
		{"-config", writeConfig(t, `{"identity": "/k", "listen": "[::ffff:0.0.0.0]:7447"}`)},
		{"-config", writeConfig(t, `{"identity": "/k", "bootstrap": ["[::ffff:203.0.113.2]:7447"]}`)},
		{"-identity", "/k", "-bootstrap-file", writeConfig(t, "203.0.113.1:1\n::ffff:203.0.113.2:7447\n")},
	} {
		_, err := parseRun(args, io.Discard)
		var ue usageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), "IPv6") || !strings.Contains(err.Error(), "IPv4 only") {
			t.Errorf("%v: %v, want a usage error saying IPv6 is refused and kolo-node is IPv4 only", args, err)
		}
	}
	for _, listen := range []string{":7447", "127.0.0.1:0", "0.0.0.0:7447", "localhost:7447"} {
		if _, err := parseRun([]string{"-identity", "/k", "-first", "-listen", listen}, io.Discard); err != nil {
			t.Errorf("-listen %s: %v", listen, err)
		}
	}
}

func TestDefaults(t *testing.T) {
	if bootstrapRefresh != 5*time.Minute || refreshBelow != 20 || bootstrapRetry != time.Minute {
		t.Errorf("bootstrapRefresh %v, refreshBelow %d, bootstrapRetry %v, want 5m, 20 and 1m", bootstrapRefresh, refreshBelow, bootstrapRetry)
	}
	if resolveTimeout != 5*time.Second || bootstrapTimeout != 30*time.Second || shutdownTimeout != 10*time.Second {
		t.Errorf("resolveTimeout %v, bootstrapTimeout %v, shutdownTimeout %v, want 5s, 30s and 10s", resolveTimeout, bootstrapTimeout, shutdownTimeout)
	}
	s := fixedSecure()
	tc := s.Transport
	if s.MaxPending != 16 || tc.MaxConns != 16 || tc.MaxDialed != 1 || tc.MaxIncomingStreams != 1 ||
		tc.MaxConnReceiveWindow != 64<<10 || tc.MaxStreamReceiveWindow != 64<<10 || tc.AcceptFunc == nil {
		t.Errorf("fixedSecure = %+v, transport %+v, want MaxPending 16, MaxConns 16, MaxDialed 1, 1 stream, 64 KiB windows, AcceptFunc set", s, tc)
	}
}
