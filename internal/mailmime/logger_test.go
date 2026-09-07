package mailmime

import (
	"io"
	"log/slog"
)

// discardLogger keeps the parser's warnings out of the test output; the
// assertions cover the behaviour, not the logging.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
