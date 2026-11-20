package kolonode

import (
	"io"
	"log/slog"
	"net/netip"
)

func newLogger(w io.Writer, format, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	switch level {
	case "warn":
		opts.Level = slog.LevelWarn
	case "error":
		opts.Level = slog.LevelError
	}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// showAddr returns a peer address for a log: in full if show, otherwise its /24 without the port.
func showAddr(a netip.AddrPort, show bool) string {
	if show {
		return a.String()
	}
	return netip.PrefixFrom(a.Addr(), 24).Masked().String()
}

func showAddrs(as []netip.AddrPort, show bool) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = showAddr(a, show)
	}
	return out
}
