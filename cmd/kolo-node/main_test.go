package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestMain runs the test binary as kolo-node when a test re-executes it, so tests drive a real process.
func TestMain(m *testing.M) {
	if os.Getenv("KOLO_NODE_TEST_MAIN") == "1" {
		if os.Getenv("KOLO_NODE_TEST_STALL") == "1" {
			hooks.BeforeClose = func() { select {} }
		}
		main()
	}
	os.Exit(m.Run())
}

// command returns kolo-node args as a re-execution of the test binary.
func command(t *testing.T, env []string, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(exe, args...)
	c.Env = append(append(os.Environ(), "KOLO_NODE_TEST_MAIN=1"), env...)
	c.Dir = t.TempDir()
	return c
}

func TestUsageExitCode(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		out  string
	}{
		{nil, 2, ""},
		{[]string{"nope"}, 2, ""},
		{[]string{"run"}, 2, ""},
		{[]string{"version"}, 0, "kolo-node "},
	} {
		out, err := command(t, nil, tc.args...).Output()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != tc.code || !strings.HasPrefix(string(out), tc.out) {
			t.Errorf("%v: exit %d, stdout %q, want %d and %q", tc.args, code, out, tc.code, tc.out)
		}
	}
}

func TestDocsShowFiles(t *testing.T) {
	unit, err := os.ReadFile("../../deploy/kolo-node.service")
	if err != nil {
		t.Fatal(err)
	}
	deploy := readDoc(t, "../../docs/deploy.md")
	if !strings.Contains(deploy, "```ini\n"+string(unit)+"```\n") {
		t.Fatal("docs/deploy.md does not show deploy/kolo-node.service verbatim")
	}
	for _, line := range []string{
		"flush ruleset\n",
		"table inet filter {\n",
		"ct state invalid drop\n",
		"udp dport 7447 accept\n",
		"chain forward {\n",
		"chain output {\n",
	} {
		if !strings.Contains(deploy, line) {
			t.Errorf("docs/deploy.md's nftables.conf lacks %q", line)
		}
	}
	checkRuleset(t, deploy)
	limit, max := unitBytes(t, string(unit), "Environment=GOMEMLIMIT=", "MiB"), unitBytes(t, string(unit), "MemoryMax=", "M")
	if limit >= max {
		t.Errorf("unit GOMEMLIMIT %d bytes is not below MemoryMax %d bytes", limit, max)
	}
	// docs/deploy.md states the runtime total that GOMEMLIMIT must exceed, in "`GOMEMLIMIT=480MiB` is above 449 MiB".
	m := regexp.MustCompile("`GOMEMLIMIT=([0-9]+)MiB` is above ([0-9]+) MiB").FindStringSubmatch(deploy)
	if m == nil {
		t.Fatal("docs/deploy.md does not state the total GOMEMLIMIT is above")
	}
	prose, _ := strconv.ParseInt(m[1], 10, 64)
	total, _ := strconv.ParseInt(m[2], 10, 64)
	if prose<<20 != limit || limit <= total<<20 {
		t.Errorf("docs/deploy.md says GOMEMLIMIT=%dMiB is above %d MiB; the unit sets %d MiB", prose, total, limit>>20)
	}
	section, noLimit := "", false
	for _, line := range strings.Split(string(unit), "\n") {
		if strings.HasPrefix(line, "[") {
			section = line
		}
		noLimit = noLimit || section == "[Unit]" && line == "StartLimitIntervalSec=0"
		k, v, _ := strings.Cut(line, "=")
		if (k == "StartLimitBurst" || k == "StartLimitIntervalSec") && v != "0" {
			t.Errorf("unit line %q: the unit must not stop restarting after a burst of failures", line)
		}
	}
	if !noLimit {
		t.Error("unit lacks StartLimitIntervalSec=0 in [Unit]: systemd would stop restarting after 5 failures in 10s")
	}
	join, err := os.ReadFile("../../examples/join/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readDoc(t, "../../docs/guide.md"), "```go\n"+string(join)+"```\n") {
		t.Fatal("docs/guide.md does not show examples/join/main.go verbatim")
	}
	if !strings.Contains(string(unit), "ExecStart=/usr/local/bin/kolo-node run -config /etc/kolo-node/config.json\n") ||
		!strings.Contains(string(unit), "StateDirectoryMode=0700\n") || !strings.Contains(string(unit), "RestartPreventExitStatus=2\n") {
		t.Fatal("unit lacks its ExecStart, StateDirectoryMode=0700 or RestartPreventExitStatus=2")
	}
}

// readDoc returns the docs page at path, failing the test when it cannot be read.
func readDoc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("docs page %s: %v", path, err)
	}
	return string(b)
}

// checkRuleset checks the input chain of docs/deploy.md. Before the IPv6 drop come only loopback, the ct state rules and what
// the host's own IPv6 needs (neighbor and router discovery, MLD queries, DHCPv6 replies); every IPv4 accept comes after.
func checkRuleset(t *testing.T, doc string) {
	t.Helper()
	start := strings.Index(doc, "    chain input {\n        type filter hook input priority filter; policy drop;\n")
	end := strings.Index(doc, "    chain forward {\n")
	if start < 0 || end < start {
		t.Fatal("docs/deploy.md lacks the nftables input chain")
	}
	var got []string
	for _, line := range strings.Split(doc[start:end], "\n")[2:] {
		if r := strings.TrimSpace(line); r != "" && r != "}" {
			got = append(got, r)
		}
	}
	want := []string{
		"iif lo accept",
		"ct state invalid drop",
		"ct state established,related accept",
		"icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-advert } accept",
		"ip6 saddr fe80::/10 icmpv6 type mld-listener-query accept",
		"ip6 saddr fe80::/10 udp sport 547 udp dport 546 accept",
		"meta nfproto ipv6 drop",
	}
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Errorf("input chain starts %q, want %q", got, want)
	}
	for _, r := range []string{"tcp dport 22 accept # your SSH port, if it is not 22", "udp dport 7447 accept"} {
		if !slices.Contains(got, r) {
			t.Errorf("input chain lacks %q", r)
		}
	}
	for _, r := range got {
		// conntrack marks an ICMP error about a known connection related and any other invalid, so these never match.
		if strings.Contains(r, "destination-unreachable") || strings.Contains(r, "packet-too-big") || strings.Contains(r, "time-exceeded") {
			t.Errorf("input chain rule %q matches ICMP errors, which the ct state rules above it already decide", r)
		}
	}
}

// unitBytes returns the size in bytes after key in unit, a number followed by unit suffix (MiB or systemd's M, both 2^20).
func unitBytes(t *testing.T, unit, key, suffix string) int64 {
	t.Helper()
	_, rest, ok := strings.Cut(unit, "\n"+key)
	v, _, _ := strings.Cut(rest, "\n")
	n, err := strconv.ParseInt(strings.TrimSuffix(v, suffix), 10, 64)
	if !ok || !strings.HasSuffix(v, suffix) || err != nil {
		t.Fatalf("unit has no %s<number>%s line", key, suffix)
	}
	return n << 20
}

// TestSystemdVerify runs systemd-analyze verify on the unit, only when KOLO_SYSTEMD_VERIFY=1 (a Linux host with systemd).
func TestSystemdVerify(t *testing.T) {
	if os.Getenv("KOLO_SYSTEMD_VERIFY") != "1" {
		t.Skip("set KOLO_SYSTEMD_VERIFY=1 on a systemd host to run it")
	}
	tool, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is not in PATH")
	}
	unit, err := os.ReadFile("../../deploy/kolo-node.service")
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "kolo-node.service")
	if err := os.WriteFile(p, []byte(strings.Replace(string(unit), "/usr/local/bin/kolo-node", exe, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(tool, "verify", p).CombinedOutput(); err != nil {
		t.Fatalf("systemd-analyze verify: %v\n%s", err, out)
	}
}
