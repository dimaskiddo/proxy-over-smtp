package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// newLogger builds the event-stream logger. When file is set, output is teed to it.
func newLogger(level, format, file string, stdout io.Writer) (*slog.Logger, io.Closer, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, nil, fmt.Errorf("invalid log level %q: want debug, info, warn or error", level)
	}

	// The no-op closer lets PersistentPostRunE close unconditionally.
	w, closer := stdout, io.Closer(io.NopCloser(nil))
	if file != "" {
		f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file: %w", err)
		}

		w, closer = io.MultiWriter(stdout, f), f
	}

	opts := &slog.HandlerOptions{Level: lvl}

	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), closer, nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), closer, nil
	default:
		if file != "" {
			closer.Close()
		}

		return nil, nil, fmt.Errorf("invalid log format %q: want text or json", format)
	}
}
