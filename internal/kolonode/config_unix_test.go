//go:build unix

package kolonode

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// parseWithin runs parseRun with a deadline, since a FIFO could block it.
func parseWithin(t *testing.T, args []string, d time.Duration) (options, error) {
	t.Helper()
	type result struct {
		o   options
		err error
	}
	ch := make(chan result, 1)
	go func() {
		o, err := parseRun(args, io.Discard)
		ch <- result{o, err}
	}()
	select {
	case r := <-ch:
		return r.o, r.err
	case <-time.After(d):
		t.Fatalf("parseRun(%v) blocked", args)
		return options{}, nil
	}
}

func TestConfigFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := parseWithin(t, []string{"-config", p}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error %v, want not a regular file", err)
	}
}

func TestConfigSwappedAfterOpen(t *testing.T) {
	p := writeConfig(t, `{"identity": "/k", "listen": "127.0.0.1:9", "first": true}`)
	fileOpened = func(path string) {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { fileOpened = nil })
	o, err := parseWithin(t, []string{"-config", p}, time.Second)
	if err != nil || o.listen != "127.0.0.1:9" {
		t.Fatalf("parseRun = %+v, %v, want the file that was opened", o, err)
	}
}

func TestConfigDeviceNotOpened(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.Symlink("/dev/zero", p); err != nil {
		t.Fatal(err)
	}
	opened := false
	fileOpened = func(string) { opened = true }
	t.Cleanup(func() { fileOpened = nil })
	_, err := parseWithin(t, []string{"-config", p}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") || opened {
		t.Fatalf("error %v, opened %v: want a device refused before it is opened", err, opened)
	}
}

func TestBootstrapFileSpecial(t *testing.T) {
	t.Setenv(envBootstrap, "")
	opened := false
	fileOpened = func(string) { opened = true }
	t.Cleanup(func() { fileOpened = nil })
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	dev := filepath.Join(dir, "zero")
	if err := os.Symlink("/dev/zero", dev); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fifo, dev} {
		_, err := parseWithin(t, []string{"-identity", "/k", "-bootstrap-file", p}, time.Second)
		if err == nil || !strings.Contains(err.Error(), "not a regular file") || opened {
			t.Errorf("%s: %v, opened %v; want refused before it is opened", p, err, opened)
		}
	}
}

func TestBootstrapFileSwappedAfterOpen(t *testing.T) {
	t.Setenv(envBootstrap, "")
	p := writeConfig(t, "203.0.113.1:7447\n")
	fileOpened = func(path string) {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { fileOpened = nil })
	o, err := parseWithin(t, []string{"-identity", "/k", "-bootstrap-file", p}, time.Second)
	if err != nil || len(o.nodes) != 1 {
		t.Fatalf("parseRun = %v, %v, want the file that was opened", o.nodes, err)
	}
}

func TestBootstrapFileSwappedBeforeOpen(t *testing.T) {
	t.Setenv(envBootstrap, "")
	p := writeConfig(t, "203.0.113.1:7447\n")
	fileChecked = func(path string) {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { fileChecked = nil })
	_, err := parseWithin(t, []string{"-identity", "/k", "-bootstrap-file", p}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error %v, want the FIFO swapped in after the path check refused", err)
	}
}
