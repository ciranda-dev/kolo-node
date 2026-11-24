package kolonode

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBootstrapFile(t *testing.T) {
	t.Setenv(envBootstrap, "")
	a := writeConfig(t, "\ufeff# entry nodes of our network\r\n"+
		"203.0.113.1:7447\r\n"+
		"\r\n"+
		"   # indented comment\n"+
		"Seed.Example.org:7447   # trailing comment\n"+
		"203.0.113.1:7447\n"+
		"\t203.0.113.2:1, 203.0.113.3:2\n"+
		"203.0.113.4:3")
	b := writeConfig(t, "203.0.113.4:3\n203.0.113.9:9\n")
	o, err := parseRun([]string{"-identity", "/k", "-bootstrap-file", a, "-bootstrap", "203.0.113.2:1,203.0.113.8:8", "-bootstrap-file", b, "-bootstrap", "203.0.113.1:7447"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := entries(t, "203.0.113.2:1", "203.0.113.8:8", "203.0.113.1:7447", "seed.example.org:7447", "203.0.113.3:2", "203.0.113.4:3", "203.0.113.9:9")
	if !reflect.DeepEqual(o.nodes, want) {
		t.Fatalf("nodes = %v, want %v", o.nodes, want)
	}
	if want := []string{"-bootstrap", "-bootstrap-file"}; !reflect.DeepEqual(o.from, want) {
		t.Fatalf("from = %v, want %v", o.from, want)
	}
	// The config file's lists are replaced by the flags, like every key.
	p := writeConfig(t, `{"identity": "/k", "bootstrap-file": [`+fmt.Sprintf("%q", b)+`]}`)
	if o, err = parseRun([]string{"-config", p}, io.Discard); err != nil || !reflect.DeepEqual(o.nodes, entries(t, "203.0.113.4:3", "203.0.113.9:9")) {
		t.Fatalf("from the config file: %v, %v", o.nodes, err)
	}
	if o, err = parseRun([]string{"-config", p, "-bootstrap-file", a}, io.Discard); err != nil || len(o.nodes) != 5 {
		t.Fatalf("flag over the config file: %v, %v", o.nodes, err)
	}
}

func TestBootstrapEntriesHaveNoCount(t *testing.T) {
	t.Setenv(envBootstrap, "")
	var flagList, fileList []string
	for i := range 100 {
		e := fmt.Sprintf("203.0.113.%d:7447", i+1)
		if i < 60 {
			flagList = append(flagList, e)
		}
		if i >= 50 {
			fileList = append(fileList, e)
		}
	}
	args := []string{"-identity", "/k", "-bootstrap", strings.Join(flagList, ","), "-bootstrap-file", writeConfig(t, strings.Join(fileList, "\n"))}
	if o, err := parseRun(args, io.Discard); err != nil || len(o.nodes) != 100 {
		t.Fatalf("100 distinct entries over a flag and a file: %d entries, %v", len(o.nodes), err)
	}
}

func TestBootstrapFileRefuses(t *testing.T) {
	t.Setenv(envBootstrap, "")
	dir := t.TempDir()
	fits := "203.0.113.1:1\n" + strings.Repeat("#", maxConfigSize-len("203.0.113.1:1\n"))
	if o, err := parseRun([]string{"-identity", "/k", "-bootstrap-file", writeConfig(t, fits)}, io.Discard); err != nil || len(o.nodes) != 1 {
		t.Fatalf("a file of exactly %d bytes: %v, %v", maxConfigSize, o.nodes, err)
	}
	for name, tc := range map[string]struct{ content, path, want string }{
		"IPv6":        {content: "203.0.113.1:1\n[2001:db8::1]:7447\n", want: "line 2: an IPv6 address is refused"},
		"mapped IPv6": {content: "\n\n::ffff:203.0.113.1:7447\n", want: "line 3: an IPv6 address is refused"},
		"bad port":    {content: "# c\n203.0.113.1:0\n", want: "line 2: port"},
		"two a line":  {content: "203.0.113.1:1 203.0.113.2:2\n", want: "line 1"},
		"content":     {content: "SECRETVALUE\n", want: "line 1"},
		"size":        {content: fits + "#", want: "larger than 65536 bytes"},
		"directory":   {path: dir, want: "-bootstrap-file " + dir + ": not a regular file"},
		"missing":     {path: filepath.Join(dir, "none.txt"), want: filepath.Join(dir, "none.txt")},
	} {
		p := tc.path
		if p == "" {
			p = writeConfig(t, tc.content)
		}
		_, err := parseRun([]string{"-identity", "/k", "-bootstrap-file", p}, io.Discard)
		var ue usageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want a usage error containing %q", name, err, tc.want)
		}
		if err != nil && (strings.Contains(err.Error(), "SECRETVALUE") || !strings.HasPrefix(err.Error(), "kolonode: ")) {
			t.Errorf("%s: error %q shows the file's content", name, err)
		}
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 { // root reads any file; chmod 0 does not stop reads on Windows
		p := writeConfig(t, "SECRETVALUE:1\n")
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		_, err := parseRun([]string{"-identity", "/k", "-bootstrap-file", p}, io.Discard)
		if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), p) || strings.Contains(err.Error(), "SECRETVALUE") {
			t.Errorf("unreadable file: %v", err)
		}
	}
}

func TestBootstrapEnv(t *testing.T) {
	t.Setenv(envBootstrap, " seed.example.org:7447, 203.0.113.5:2,seed.example.org:7447")
	o, err := parseRun([]string{"-identity", "/k"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if want := entries(t, "seed.example.org:7447", "203.0.113.5:2"); !reflect.DeepEqual(o.nodes, want) || !reflect.DeepEqual(o.from, []string{envBootstrap}) {
		t.Fatalf("nodes %v from %v, want %v from %s", o.nodes, o.from, want, envBootstrap)
	}
	// Flags override the environment, and -first ignores it.
	for _, args := range [][]string{
		{"-identity", "/k", "-bootstrap", "203.0.113.9:9"},
		{"-identity", "/k", "-bootstrap-file", writeConfig(t, "203.0.113.9:9\n")},
		{"-config", writeConfig(t, `{"identity": "/k", "bootstrap": ["203.0.113.9:9"]}`)},
	} {
		o, err := parseRun(args, io.Discard)
		if err != nil || !reflect.DeepEqual(o.nodes, entries(t, "203.0.113.9:9")) {
			t.Errorf("%v: nodes %v, %v; want only the flag's", args, o.nodes, err)
		}
	}
	if o, err := parseRun([]string{"-identity", "/k", "-first"}, io.Discard); err != nil || o.nodes != nil || o.envIgnored != "-first" {
		t.Errorf("-first: nodes %v, %v, ignored by %q; want none, ignored by -first", o.nodes, err, o.envIgnored)
	}
	if o, err := parseRun([]string{"-identity", "/k", "-bootstrap", "203.0.113.9:9"}, io.Discard); err != nil || o.envIgnored == "" {
		t.Errorf("-bootstrap: %v, ignored by %q; want the environment reported ignored", err, o.envIgnored)
	}
	// A flag or key set to an empty list is a mistake, not a request for the environment's list.
	for _, args := range [][]string{
		{"-identity", "/k", "-bootstrap", ""},
		{"-identity", "/k", "-bootstrap", " , "},
		{"-identity", "/k", "-bootstrap-file", ""},
		{"-identity", "/k", "-bootstrap-file", writeConfig(t, "# none yet\n")},
		{"-config", writeConfig(t, `{"identity": "/k", "bootstrap": []}`)},
		{"-config", writeConfig(t, `{"identity": "/k", "bootstrap-file": []}`)},
	} {
		_, err := parseRun(args, io.Discard)
		var ue usageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), "no entry nodes") {
			t.Errorf("%v: %v, want no entry nodes despite %s", args, err, envBootstrap)
		}
	}
	for env, want := range map[string]string{
		"203.0.113.1:1,bad":         envBootstrap + " entry 2",
		"[::ffff:203.0.113.1]:7447": "IPv4 only",
		"203.0.113.1:1, ::1:7447":   "IPv4 only",
		" , ":                       "no entry nodes",
		"x.example:1,SECRETVALUE":   envBootstrap + " entry 2",
	} {
		t.Setenv(envBootstrap, env)
		_, err := parseRun([]string{"-identity", "/k"}, io.Discard)
		var ue usageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "SECRETVALUE") {
			t.Errorf("%s=%q: %v, want a usage error containing %q", envBootstrap, env, err, want)
		}
	}
}

func TestFirstNodeAndJoinFromFile(t *testing.T) {
	checkGoroutines(t)
	a, addrA := startNode(t)
	if ev := a.log.wait(t, "network", nil, 5*time.Second); ev["mode"] != "first" || ev["entries"] != 0.0 {
		t.Fatalf("first node network event %v", ev)
	}
	nodes := writeConfig(t, "# our network\n"+addrA.String()+"\n")
	b, _ := startNode(t, "-bootstrap-file", nodes)
	if ev := b.log.wait(t, "network", nil, 5*time.Second); ev["mode"] != "join" || ev["entries"] != 1.0 || ev["from"] != "-bootstrap-file" {
		t.Fatalf("joining node network event %v", ev)
	}
	b.log.wait(t, "bootstrap", func(ev map[string]any) bool { return ev["servers"] == 1.0 }, 10*time.Second)
}

func TestEnvIgnoredWarns(t *testing.T) {
	checkGoroutines(t)
	t.Setenv(envBootstrap, "203.0.113.9:9")
	r, _ := startNode(t)
	if ev := r.log.wait(t, envBootstrap+" ignored", nil, 5*time.Second); ev["level"] != "WARN" || ev["because"] != "-first" {
		t.Fatalf("warning %v", ev)
	}
}

func FuzzBootstrapFile(f *testing.F) {
	for _, s := range []string{"", "\ufeff203.0.113.1:7447\r\n# c\n", "a.example:1 # x\n\nb.example:2,c.example:3", "[::1]:1\n", "#\n#\n", "\r\r\n,,#,\n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		last := 0
		lines := strings.Count(string(b), "\n") + 1
		err := fileEntries(b, func(line int, e string) error {
			if line < last || line > lines || e == "" || strings.ContainsAny(e, "#,\n") || e != strings.TrimSpace(e) {
				t.Fatalf("entry %q on line %d after line %d", e, line, last)
			}
			last = line
			if en, err := parseEntry(e); err == nil {
				if again, err := parseEntry(en.String()); err != nil || again != en {
					t.Fatalf("parseEntry(%q) = %v; again %v, %v", e, en, again, err)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
