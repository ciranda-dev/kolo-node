//go:build unix

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// node starts kolo-node run on loopback with JSON logs and returns it with a channel of its log events.
func node(t *testing.T, env ...string) (*exec.Cmd, <-chan map[string]any) {
	t.Helper()
	c := command(t, env, "run", "-listen", "127.0.0.1:0", "-identity", filepath.Join(t.TempDir(), "state", "node.key"), "-log-format", "json", "-first")
	stderr, err := c.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	events := make(chan map[string]any, 64)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			var ev map[string]any
			if json.Unmarshal(sc.Bytes(), &ev) == nil {
				events <- ev
			}
		}
		io.Copy(io.Discard, stderr)
	}()
	return c, events
}

func waitEvent(t *testing.T, events <-chan map[string]any, msg string) map[string]any {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("log ended before %q", msg)
			}
			if ev["msg"] == msg {
				return ev
			}
		case <-timeout:
			t.Fatalf("no %q event", msg)
		}
	}
}

// wait returns the process's exit error, or fails after 15s.
func wait(t *testing.T, c *exec.Cmd) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("kolo-node did not exit")
		return nil
	}
}

func TestSignalStopsCleanly(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			c, events := node(t)
			waitEvent(t, events, "started")
			if err := c.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			waitEvent(t, events, "stopping")
			waitEvent(t, events, "stopped")
			if err := wait(t, c); err != nil {
				t.Fatalf("exit: %v, want 0", err)
			}
		})
	}
}

func TestSecondSignalKills(t *testing.T) {
	c, events := node(t, "KOLO_NODE_TEST_STALL=1")
	waitEvent(t, events, "started")
	if err := c.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, events, "stopping")
	if err := c.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := wait(t, c)
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("exit: %v, want killed by SIGTERM", err)
	}
	ws := ee.Sys().(syscall.WaitStatus)
	if !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Fatalf("exit status %v, want killed by SIGTERM", ws)
	}
}
