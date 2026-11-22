package kolonode

import (
	"encoding/json"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

// encodeConfig returns o's file settings as a config file.
func encodeConfig(t *testing.T, o *options) []byte {
	t.Helper()
	var cur options
	fs := runFlags(&cur, io.Discard)
	cur = *o // the flags point into cur, so Get now reads o's values
	m := map[string]any{}
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name == "config" || f.Name == "bootstrap" {
			return
		}
		switch v := f.Value.(flag.Getter).Get().(type) {
		case []string:
			if v != nil {
				m[f.Name] = v
			}
		case time.Duration:
			m[f.Name] = v.String()
		default:
			m[f.Name] = v
		}
	})
	if o.bootstrap != nil {
		m["bootstrap"] = o.bootstrap
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func FuzzConfig(f *testing.F) {
	f.Add([]byte(`{"listen": ":7447", "identity": "/k", "bootstrap": ["a.example.org:1"], "log-addrs": true, "stats-interval": "1m", "dht-max-records": 10}`))
	f.Add([]byte(`{"bootstrap": [], "dht-punch-timeout": "1h"}`))
	f.Add([]byte(`{"listen": ":1", "listen": ":2"}`))
	f.Add([]byte(`{"dht-max-records": 1e3} {}`))
	f.Add([]byte(`[{"a": [1, {"b": null}]}]`))
	f.Fuzz(func(t *testing.T, b []byte) {
		var o options
		fs := runFlags(&o, io.Discard)
		if decodeConfig(fs, b) != nil {
			return
		}
		var again options
		fs2 := runFlags(&again, io.Discard)
		enc := encodeConfig(t, &o)
		if err := decodeConfig(fs2, enc); err != nil {
			t.Fatalf("re-decoding %s: %v", enc, err)
		}
		if !reflect.DeepEqual(o, again) {
			t.Fatalf("round trip of %q: %+v, then %+v", b, o, again)
		}
	})
}

func FuzzBootstrapEntry(f *testing.F) {
	for _, s := range []string{"203.0.113.10:7447", "[2001:db8::1]:1", "[::ffff:203.0.113.10]:1", "node1.example.org:65535", "[fe80::1%eth0]:1", "a..b:1", "x:0", ":"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		e, err := parseEntry(s)
		if strings.Contains(s, ",") && (err == nil || err.Error() != "want host:port") {
			t.Fatalf("parseEntry(%q) = %v, want host:port", s, err)
		}
		if err != nil {
			return
		}
		if e.port == 0 || e.host == "" {
			t.Fatalf("parseEntry(%q) = %+v", s, e)
		}
		if e.ip.IsValid() && !e.ip.Is4() {
			t.Fatalf("parseEntry(%q) = %+v, an address that is not IPv4", s, e)
		}
		again, err := parseEntry(e.String())
		if err != nil || again != e {
			t.Fatalf("parseEntry(%q) = %+v; reparsing %q: %+v, %v", s, e, e.String(), again, err)
		}
	})
}

func FuzzRelayEntry(f *testing.F) {
	k := strings.Repeat("ab", 32)
	for _, s := range []string{"127.0.0.1:7448@" + k, "relay.example.org:1@" + strings.ToUpper(k), "[::1]:1@" + k, "a@b@c", "127.0.0.1:0@" + k, "@"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, err := parseRelayEntry(s)
		if err != nil {
			return
		}
		again, err := parseRelayEntry(r.String())
		if err != nil || again.String() != r.String() {
			t.Fatalf("%q parses again as %v, %v", r, again, err)
		}
		if r.addr.ip.IsValid() && !r.addr.ip.Is4() {
			t.Fatalf("IPv6 accepted: %q", s)
		}
	})
}

func FuzzRelayConfig(f *testing.F) {
	f.Add([]byte(`{"listen": "127.0.0.1:7448", "identity": "/k", "relay-max-session-bytes": 1099511627776, "relay-flush-interval": "2ms"}`))
	f.Add([]byte(`{"relay-rate-bytes": 1e3}`))
	f.Add([]byte(`{"relay-stream-window": 1048576, "relay-conn-window": 4194304, "relay-congestion": "bbr"}`))
	f.Add([]byte(`{"allow-file": null}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		var o relayOptions
		decodeConfig(relayFlags(&o, io.Discard), b)
	})
}
