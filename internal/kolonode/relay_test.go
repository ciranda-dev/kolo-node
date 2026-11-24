package kolonode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	koloid "github.com/ciranda-dev/kolo-id"
	kolorelay "github.com/ciranda-dev/kolo-relay"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

func TestRelayFlags(t *testing.T) {
	p := writeConfig(t, `{"listen": "127.0.0.1:0", "identity": "/k", "relay-max-sessions": 3, "relay-max-session-bytes": 1099511627776, "relay-reservation-lifetime": "2h"}`)
	o, err := parseRelay([]string{"-config", p,
		"-relay-max-reservations", "100", "-relay-max-sessions", "40", "-relay-max-sessions-per-peer", "5",
		"-relay-max-sessions-per-target", "6", "-relay-max-session-duration", "3h", "-relay-session-idle-timeout", "2m",
		"-relay-connect-timeout", "20s", "-relay-control-timeout", "30s", "-relay-rate-bytes", "2097152",
		"-relay-rate-burst", "524288", "-relay-flush-interval", "2ms", "-relay-max-conns", "300", "-relay-max-conns-per-ip", "12",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	c := o.serverConfig()
	if c.MaxReservations != 100 || c.MaxSessions != 40 || c.MaxSessionsPerPeer != 5 || c.MaxSessionsPerTarget != 6 ||
		c.ReservationLifetime != 2*time.Hour || c.MaxSessionDuration != 3*time.Hour || c.MaxSessionBytes != 1<<40 ||
		c.SessionIdleTimeout != 2*time.Minute || c.ConnectTimeout != 20*time.Second || c.ControlTimeout != 30*time.Second ||
		c.RateBytes != 2<<20 || c.RateBurst != 512<<10 || c.FlushInterval != 2*time.Millisecond {
		t.Fatalf("server config %+v", c)
	}
	tc := c.Secure.Transport
	if tc.MaxConns != 300 || tc.MaxConnsPerIP != 12 || tc.MaxDialed != relayMaxDialed || tc.MaxIncomingStreams != relayMaxStreams ||
		tc.MaxStreamReceiveWindow != relayStreamWindow || tc.MaxConnReceiveWindow != relayConnWindow || c.Secure.MaxPending != 0 ||
		tc.MaxMemory != kolotransport.DefaultMaxMemory || !tc.Datagrams {
		t.Fatalf("listener %+v", tc)
	}
	o, _ = parseRelay([]string{"-listen", "127.0.0.1:0", "-identity", "/k", "-relay-max-conns", "10000"}, io.Discard)
	if tc := o.serverConfig().Secure.Transport; tc.MaxMemory != (10000+relayMaxDialed)*relayConnWindow {
		t.Fatalf("MaxMemory %d for 10000 connections", tc.MaxMemory)
	}
	o, err = parseRelay([]string{"-listen", "127.0.0.1:0", "-identity", "/k"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c := o.serverConfig(); c.Secure != nil || c.AllowReserve != nil {
		t.Fatal("defaults set a listener or an allow list")
	}
	o, _ = parseRelay([]string{"-listen", "127.0.0.1:0", "-identity", "/k", "-relay-max-conns", "2"}, io.Discard)
	if tc := o.serverConfig().Secure.Transport; tc.MaxConns != 2 || tc.MaxConnsPerIP != 2 {
		t.Fatalf("per-address default above MaxConns: %+v", tc)
	}
	o, _ = parseRelay([]string{"-listen", "127.0.0.1:0", "-identity", "/k", "-relay-max-conns-per-ip", "9"}, io.Discard)
	if tc := o.serverConfig().Secure.Transport; tc.MaxConns != relayMaxConns || tc.MaxConnsPerIP != 9 {
		t.Fatalf("listener with only the per-address limit: %+v", tc)
	}
}

func TestRelayUsage(t *testing.T) {
	for _, args := range [][]string{
		{"-identity", "/k"},
		{"-listen", "127.0.0.1:0"},
		{"-listen", "[::1]:7448", "-identity", "/k"},
		{"-listen", "[::ffff:127.0.0.1]:7448", "-identity", "/k"},
		{"-listen", "127.0.0.1:0", "-identity", "/k", "-log-format", "xml"},
		{"-listen", "127.0.0.1:0", "-identity", "/k", "-relay-max-conns", "-1"},
		{"-listen", "127.0.0.1:0", "-identity", "/k", "extra"},
		{"-listen", "127.0.0.1:0", "-identity", "/k", "-nope"},
	} {
		var errOut strings.Builder
		if code := Main(t.Context(), append([]string{"relay"}, args...), Env{Stdout: io.Discard, Stderr: &errOut}); code != ExitUsage {
			t.Errorf("relay %v: exit %d, stderr %q", args, code, errOut.String())
		}
	}
}

func TestRelayRanges(t *testing.T) {
	for _, bad := range [][]string{
		{"-relay-max-reservations", "100001"},
		{"-relay-max-sessions", "10001"},
		{"-relay-max-sessions-per-peer", "17"},
		{"-relay-max-sessions-per-target", "17"},
		{"-relay-reservation-lifetime", "500ms"},
		{"-relay-reservation-lifetime", "25h"},
		{"-relay-max-session-duration", "25h"},
		{"-relay-max-session-bytes", "1024"},
		{"-relay-session-idle-timeout", "11m"},
		{"-relay-connect-timeout", "2m"},
		{"-relay-control-timeout", "500ms"},
		{"-relay-rate-bytes", "1024"},
		{"-relay-rate-burst", "1024"},
		{"-relay-flush-interval", "1s"},
		{"-relay-max-conns", "10001"},
		{"-relay-max-conns", "8", "-relay-max-conns-per-ip", "9"},
	} {
		key := keyPath(t)
		var errOut strings.Builder
		args := append([]string{"relay", "-listen", "127.0.0.1:0", "-identity", key}, bad...)
		if code := Main(t.Context(), args, Env{Stdout: io.Discard, Stderr: &errOut}); code != ExitUsage {
			t.Errorf("%v: exit %d, stderr %q", bad, code, errOut.String())
		}
		if _, err := os.Stat(key); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v: a key file was written for a refused config", bad)
		}
	}
}

func TestRelayMaxConnsRange(t *testing.T) {
	for _, n := range []string{"4096", "10000"} {
		t.Run(n, func(t *testing.T) {
			checkGoroutines(t)
			r, addr, key := startRelayCmd(t, "-relay-max-conns", n)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			res, err := kolorelay.Reserve(ctx, addr, key, newID(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			res.Close()
			if code := r.stop(t); code != ExitOK {
				t.Fatalf("exit %d", code)
			}
		})
	}
}

func relayOpts(t *testing.T, args ...string) relayOptions {
	t.Helper()
	o, err := parseRelay(append([]string{"-listen", "127.0.0.1:0", "-identity", "/k"}, args...), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestRelayListenerFlags(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		name         string
		args         []string
		conns        int
		stream, conn int64
		cc           string
	}{
		{"stream window", []string{"-relay-stream-window", "262144"}, relayMaxConns, 256 << 10, relayConnWindow, ""},
		{"conn window", []string{"-relay-conn-window", "2097152"}, relayMaxConns, relayStreamWindow, 2 * mib, ""},
		{"congestion", []string{"-relay-congestion", "bbr"}, relayMaxConns, relayStreamWindow, relayConnWindow, "bbr"},
		{"both windows", []string{"-relay-stream-window", "1048576", "-relay-conn-window", "4194304"}, relayMaxConns, mib, 4 * mib, ""},
		{"equal windows", []string{"-relay-stream-window", "65536", "-relay-conn-window", "65536"}, relayMaxConns, 64 << 10, 64 << 10, ""},
		{"all", []string{"-relay-stream-window", "1048576", "-relay-conn-window", "4194304", "-relay-congestion", "cubic", "-relay-max-conns", "8", "-relay-max-conns-per-ip", "2"}, 8, mib, 4 * mib, "cubic"},
		{"top of the range", []string{"-relay-stream-window", "16777216", "-relay-conn-window", "67108864", "-relay-max-conns", "100"}, 100, 16 * mib, 64 * mib, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := relayOpts(t, tc.args...)
			c := o.serverConfig()
			if c.Secure == nil {
				t.Fatal("no listener config")
			}
			tr := c.Secure.Transport
			if tr.MaxStreamReceiveWindow != tc.stream || tr.MaxConnReceiveWindow != tc.conn || tr.CongestionControl != tc.cc || tr.MaxConns != tc.conns {
				t.Fatalf("listener %+v", tr)
			}
			if want := max(kolotransport.DefaultMaxMemory, int64(tc.conns+relayMaxDialed)*tc.conn); tr.MaxMemory != want {
				t.Fatalf("MaxMemory %d, want %d", tr.MaxMemory, want)
			}
			if tr.MaxDialed != relayMaxDialed || tr.MaxIncomingStreams != relayMaxStreams {
				t.Fatalf("listener %+v", tr)
			}
		})
	}
	o := relayOpts(t, "-relay-stream-window", "0", "-relay-conn-window", "0", "-relay-congestion", "")
	if o.serverConfig().Secure != nil || len(o.limitAttrs()) != 0 {
		t.Fatal("zero values set a listener or a log attribute")
	}
}

func TestRelayListenerConfigFile(t *testing.T) {
	p := writeConfig(t, `{"listen": "127.0.0.1:0", "identity": "/k", "relay-stream-window": 1048576, "relay-conn-window": 4194304, "relay-congestion": "bbr"}`)
	o, err := parseRelay([]string{"-config", p}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	tr := o.serverConfig().Secure.Transport
	if tr.MaxStreamReceiveWindow != 1<<20 || tr.MaxConnReceiveWindow != 4<<20 || tr.CongestionControl != "bbr" || !tr.Datagrams {
		t.Fatalf("listener %+v", tr)
	}
	o, err = parseRelay([]string{"-config", p, "-relay-congestion", "cubic", "-relay-conn-window", "2097152"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if tr := o.serverConfig().Secure.Transport; tr.CongestionControl != "cubic" || tr.MaxConnReceiveWindow != 2<<20 || tr.MaxStreamReceiveWindow != 1<<20 {
		t.Fatalf("flags do not override the file: %+v", tr)
	}
	for _, bad := range []string{
		`{"relay-congestion": 3}`, `{"relay-stream-window": "1MiB"}`, `{"relay-conn-window": null}`, `{"relay-conn-window": 1.5}`,
	} {
		if _, err := parseRelay([]string{"-config", writeConfig(t, bad)}, io.Discard); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestRelayListenerRefused(t *testing.T) {
	for _, tc := range []struct {
		flag string // the flag the error must name
		args []string
		leak string // a value the error must not show
	}{
		{"-relay-stream-window", []string{"-relay-stream-window", "-4242"}, "4242"},
		{"-relay-conn-window", []string{"-relay-conn-window", "-4242"}, "4242"},
		{"-relay-stream-window", []string{"-relay-stream-window", "42421"}, "42421"},
		{"-relay-stream-window", []string{"-relay-stream-window", "16777217"}, "16777217"},
		{"-relay-conn-window", []string{"-relay-conn-window", "42421"}, "42421"},
		{"-relay-conn-window", []string{"-relay-conn-window", "67108865"}, "67108865"},
		{"-relay-conn-window", []string{"-relay-stream-window", "524288"}, "524288"},
		{"-relay-conn-window", []string{"-relay-stream-window", "1048576", "-relay-conn-window", "524288"}, "524288"},
		{"-relay-congestion", []string{"-relay-congestion", "vegas7"}, "vegas7"},
		{"-relay-congestion", []string{"-relay-congestion", "BBR"}, "BBR"},
		{"-relay-conn-window", []string{"-relay-max-conns", "10000", "-relay-conn-window", "67108864"}, "67108864"},
	} {
		_, err := parseRelay(append([]string{"-listen", "127.0.0.1:0", "-identity", "/k"}, tc.args...), io.Discard)
		var ue usageError
		if !errors.As(err, &ue) {
			t.Errorf("%v: %v, want a usage error", tc.args, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.flag) || strings.Contains(err.Error(), tc.leak) {
			t.Errorf("%v: error %q must name %s and not show %s", tc.args, err, tc.flag, tc.leak)
		}
		var errOut strings.Builder
		key := keyPath(t)
		args := append([]string{"relay", "-listen", "127.0.0.1:0", "-identity", key}, tc.args...)
		if code := Main(t.Context(), args, Env{Stdout: io.Discard, Stderr: &errOut}); code != ExitUsage {
			t.Errorf("%v: exit %d, stderr %q", tc.args, code, errOut.String())
		}
		if _, err := os.Stat(key); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v: a key file was written for a refused config", tc.args)
		}
	}
}

func TestRelayListenerStarts(t *testing.T) {
	checkGoroutines(t)
	r, addr, key := startRelayCmd(t, "-relay-stream-window", "1048576", "-relay-conn-window", "4194304", "-relay-congestion", "bbr")
	ev := r.log.wait(t, "started", nil, 5*time.Second)
	if ev["relay-stream-window"] != float64(1<<20) || ev["relay-conn-window"] != float64(4<<20) || ev["relay-congestion"] != "bbr" {
		t.Fatalf("started event %v", ev)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := kolorelay.Reserve(ctx, addr, key, newID(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Close()
	if code := r.stop(t); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
}

func TestRelayAllowFile(t *testing.T) {
	k1, k2 := newID(t).PublicKey(), newID(t).PublicKey()
	p := writeConfig(t, "\ufeff# members\r\n"+hex.EncodeToString(k1)+"\r\n\n  "+strings.ToUpper(hex.EncodeToString(k2))+"  # second\n")
	o, err := parseRelay([]string{"-listen", "127.0.0.1:0", "-identity", "/k", "-allow-file", p}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	allow := o.serverConfig().AllowReserve
	if !allow(k1) || !allow(k2) || allow(newID(t).PublicKey()) || allow(k1[:31]) {
		t.Fatal("allow list")
	}
	for _, tc := range []struct{ content, want string }{
		{"# none\n\n", "holds no key"},
		{hex.EncodeToString(k1) + "\nsecret-token\n", "line 2"},
		{hex.EncodeToString(k1)[:62] + "\n", "line 1"},
	} {
		_, err := parseRelay([]string{"-listen", "127.0.0.1:0", "-identity", "/k", "-allow-file", writeConfig(t, tc.content)}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret") {
			t.Errorf("allow file %q: %v, want %s", tc.content, err, tc.want)
		}
	}
	line := hex.EncodeToString(k1) + "\n"
	fits := line + strings.Repeat("#", maxConfigSize-len(line))
	if _, err := parseRelay([]string{"-listen", "127.0.0.1:0", "-identity", "/k", "-allow-file", writeConfig(t, fits)}, io.Discard); err != nil {
		t.Fatalf("an allow file of %d bytes: %v", maxConfigSize, err)
	}
	if _, err := parseRelay([]string{"-listen", "127.0.0.1:0", "-identity", "/k", "-allow-file", writeConfig(t, fits+"#")}, io.Discard); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("an allow file over the limit: %v", err)
	}
	dir := t.TempDir()
	if _, err := parseRelay([]string{"-listen", "127.0.0.1:0", "-identity", "/k", "-allow-file", dir}, io.Discard); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a directory: %v", err)
	}
	if _, err := parseRelay([]string{"-listen", "127.0.0.1:0", "-identity", "/k", "-allow-file", filepath.Join(dir, "missing")}, io.Discard); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing file: %v", err)
	}
}

func newID(t *testing.T) *koloid.Identity {
	t.Helper()
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// startRelayCmd runs the relay command on loopback with JSON logs and returns its address and key once started.
func startRelayCmd(t *testing.T, args ...string) (*running, string, ed25519.PublicKey) {
	t.Helper()
	r := start(t, append([]string{"relay", "-listen", "127.0.0.1:0", "-identity", keyPath(t), "-log-format", "json"}, args...)...)
	idEv := r.log.wait(t, "identity", nil, 10*time.Second)
	ev := r.log.wait(t, "started", nil, 10*time.Second)
	key, err := hex.DecodeString(idEv["key"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return r, ev["addr"].(string), key
}

func TestRelayServes(t *testing.T) {
	checkGoroutines(t)
	r, addr, key := startRelayCmd(t)
	if !r.log.tryWait(openRelayWarning, nil, 5*time.Second) {
		t.Fatal("no open relay warning without -allow-file")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tid, iid := newID(t), newID(t)
	res, err := kolorelay.Reserve(ctx, addr, key, tid, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	done := make(chan error, 1)
	go func() {
		c, err := res.Accept(ctx)
		if err != nil {
			done <- err
			return
		}
		st, err := c.AcceptStream(ctx)
		if err == nil {
			_, err = io.Copy(st, st)
			st.Close()
		}
		done <- err
	}()
	c, err := kolorelay.Dial(ctx, addr, key, iid, tid.PublicKey(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st, err := c.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.Write([]byte("hello relay"))
	st.Close()
	if got, err := io.ReadAll(st); err != nil || string(got) != "hello relay" {
		t.Fatalf("echo through the relay command: %q, %v", got, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	c.Close()
	res.Close()
	if code := r.stop(t); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	r.log.wait(t, "stopped", nil, 5*time.Second)
}

// Without -allow-file the relay is open and says so, as a warning, at start; with it, no warning but the allow event.
func TestRelayOpenWarning(t *testing.T) {
	checkGoroutines(t)
	r, _, _ := startRelayCmd(t)
	ev := r.log.wait(t, openRelayWarning, nil, 5*time.Second)
	reason, _ := ev["reason"].(string)
	if ev["level"] != "WARN" || !strings.Contains(reason, "ANYONE") || !strings.Contains(reason, "-allow-file") {
		t.Fatalf("open relay event %v: want a WARN that says anyone may reserve and names -allow-file", ev)
	}
	if r.log.tryWait("allow", nil, 0) {
		t.Fatal("an open relay logged the allow event")
	}
	member := newID(t)
	a, _, _ := startRelayCmd(t, "-allow-file", writeConfig(t, hex.EncodeToString(member.PublicKey())+"\n"))
	a.log.wait(t, "allow", nil, 5*time.Second)
	if a.log.tryWait(openRelayWarning, nil, 0) {
		t.Fatal("a relay with -allow-file warned that it is open")
	}
}

func TestRelayAllowReserve(t *testing.T) {
	checkGoroutines(t)
	member := newID(t)
	_, addr, key := startRelayCmd(t, "-allow-file", writeConfig(t, hex.EncodeToString(member.PublicKey())+"\n"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := kolorelay.Reserve(ctx, addr, key, member, nil)
	if err != nil {
		t.Fatalf("a listed key: %v", err)
	}
	res.Close()
	if _, err := kolorelay.Reserve(ctx, addr, key, newID(t), nil); !errors.Is(err, kolorelay.ErrNotAllowed) {
		t.Fatalf("an unlisted key: %v, want ErrNotAllowed", err)
	}
}

func TestRelayIdentityStable(t *testing.T) {
	checkGoroutines(t)
	path := keyPath(t)
	var keys []string
	for range 2 {
		r := start(t, "relay", "-listen", "127.0.0.1:0", "-identity", path, "-log-format", "json")
		ev := r.log.wait(t, "identity", nil, 10*time.Second)
		keys = append(keys, ev["key"].(string))
		if code := r.stop(t); code != ExitOK {
			t.Fatalf("exit %d", code)
		}
	}
	if keys[0] != keys[1] {
		t.Fatal("the relay key changed between runs")
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v", err)
	}
}

func relayArg(addr string, key ed25519.PublicKey) string { return addr + "@" + hex.EncodeToString(key) }

func TestCheckRelayLists(t *testing.T) {
	t.Setenv(envRelays, "")
	k := func(i int) string { return hex.EncodeToString(bytes.Repeat([]byte{byte(i + 1)}, 32)) }
	entry := func(i int) string { return fmt.Sprintf("127.0.0.1:%d@%s", i+1, k(i)) }
	got, err := loadRelays(splitList(entry(0)+" , "+entry(1)), nil, true)
	if err != nil || len(got) != 2 {
		t.Fatalf("comma list: %v, %v", got, err)
	}
	file := writeConfig(t, "# relays\r\n"+entry(2)+"\n"+entry(0)+"  # again\n")
	got, err = loadRelays([]string{entry(0), entry(1)}, []string{file}, true)
	if err != nil || len(got) != 3 || got[2].String() != entry(2) {
		t.Fatalf("flags then file without duplicates: %v, %v", got, err)
	}
	var many []string
	for i := range maxEntries + 1 {
		many = append(many, entry(i))
	}
	if got, err := loadRelays(many[:maxEntries], nil, true); err != nil || len(got) != maxEntries {
		t.Fatalf("%d relays: %v", maxEntries, err)
	}
	if _, err := loadRelays(many[:8], []string{writeConfig(t, strings.Join(many[8:], "\n"))}, true); err == nil {
		t.Fatalf("%d relays over flags and a file were accepted", maxEntries+1)
	}
	for _, bad := range []string{
		"127.0.0.1:1", "127.0.0.1:1@" + k(0)[:62], "[::1]:1@" + k(0), "[::ffff:127.0.0.1]:1@" + k(0), "127.0.0.1:0@" + k(0),
		entry(0) + ",127.0.0.1:9@" + k(0), entry(0) + ",127.0.0.1:1@" + k(1),
	} {
		if _, err := loadRelays(splitList(bad), nil, true); err == nil {
			t.Errorf("%q accepted", bad)
		} else if strings.Contains(err.Error(), k(0)[:16]) {
			t.Errorf("error %q shows a key", err)
		}
	}
	for bad, want := range map[string]string{
		entry(0) + ",127.0.0.1:9@" + k(0): "the key of an earlier relay with another address",
		entry(0) + ",127.0.0.1:1@" + k(1): "the address of an earlier relay with another key",
	} {
		if _, err := loadRelays(splitList(bad), nil, true); err == nil || !strings.Contains(err.Error(), "entry 2: "+want) {
			t.Errorf("conflict %q: %v", bad, err)
		}
	}
	t.Setenv(envRelays, entry(5))
	if got, _ := loadRelays(nil, nil, false); len(got) != 1 || got[0].String() != entry(5) {
		t.Fatalf("%s: %v", envRelays, got)
	}
	if got, _ := loadRelays(nil, nil, true); len(got) != 0 {
		t.Fatalf("%s read although a flag was set to an empty list: %v", envRelays, got)
	}
	var out, errOut strings.Builder
	if code := Main(t.Context(), []string{"check", "-relay", ""}, Env{Stdout: &out, Stderr: &errOut}); code != ExitUsage || out.Len() != 0 {
		t.Fatalf("check -relay '' with %s set: exit %d, stdout %q", envRelays, code, out.String())
	}
}

func TestCheckRelays(t *testing.T) {
	checkGoroutines(t)
	t.Setenv(envRelays, "")
	var reserves atomic.Int32
	id := newID(t)
	srv, err := kolorelay.Listen(context.Background(), "127.0.0.1:0", id, &kolorelay.ServerConfig{AllowReserve: func(ed25519.PublicKey) bool {
		reserves.Add(1)
		return true
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	addr := srv.Addr().String()
	good := relayArg(addr, id.PublicKey())
	other, err := kolorelay.Listen(context.Background(), "127.0.0.1:0", newID(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	wrong := relayArg(other.Addr().String(), newID(t).PublicKey())
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	dead := relayArg(pc.LocalAddr().String(), newID(t).PublicKey())

	var out, errOut strings.Builder
	if code := Main(t.Context(), []string{"check", "-timeout", "10s", "-relay", good}, Env{Stdout: &out, Stderr: &errOut}); code != ExitOK || out.String() != "ok relay "+good+"\n" {
		t.Fatalf("check of a relay: exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
	t.Setenv(envRelays, good)
	out.Reset()
	if code := Main(t.Context(), []string{"check", "-timeout", "10s"}, Env{Stdout: &out, Stderr: &errOut}); code != ExitOK || out.String() != "ok relay "+good+"\n" {
		t.Fatalf("check of %s: exit %d, stdout %q", envRelays, code, out.String())
	}
	t.Setenv(envRelays, "")
	for _, bad := range []string{wrong, dead} {
		out.Reset()
		code := Main(t.Context(), []string{"check", "-timeout", "1s", "-relay-file", writeConfig(t, good+"\n"+bad+"\n")}, Env{Stdout: &out, Stderr: &errOut})
		if code != ExitError || !strings.HasPrefix(out.String(), "ok relay "+good+"\nfailed relay "+bad+": ") {
			t.Errorf("check of a bad relay: exit %d, stdout %q", code, out.String())
		}
	}
	if n := reserves.Load(); n != 0 {
		t.Fatalf("check reserved %d times", n)
	}
	_, node := startNode(t)
	out.Reset()
	if code := Main(t.Context(), []string{"check", "-timeout", "5s", "-relay", good, node.String()}, Env{Stdout: &out, Stderr: &errOut}); code != ExitOK || out.String() != "ok "+node.String()+"\nok relay "+good+"\n" {
		t.Fatalf("check of a node and a relay: exit %d, stdout %q", code, out.String())
	}
}
