// Command kolo-node runs a kolo-dht bootstrap server or a kolo-relay; see the README for its commands, flags and config file.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ciranda-dev/kolo-node/internal/kolonode"
)

// hooks are the Env hooks of main, set only by tests that re-execute this binary.
var hooks kolonode.Env

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	env := hooks
	env.Stdout, env.Stderr = os.Stdout, os.Stderr
	env.SetLogger = func(l *slog.Logger) {
		slog.SetDefault(l)
		slog.SetLogLoggerLevel(slog.LevelWarn)
	}
	env.Stopped = stop // a second signal then gets Go's default behavior and ends the process
	os.Exit(kolonode.Main(ctx, os.Args[1:], env))
}
