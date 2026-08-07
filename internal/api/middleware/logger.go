package middleware

import (
	"context"
	"log/slog"
)

// loggerKey types the context key holding the per-request logger.
type loggerKey struct{}

// withLogger stores lg on ctx. Unexported: the only thing allowed to decide what a request's logger
// carries is RequestID, so that `request_id` is present on every line rather than on the lines
// somebody remembered.
func withLogger(ctx context.Context, lg *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, lg)
}

// Logger returns the request-scoped logger, falling back to slog.Default outside a request.
//
// The fallback is what makes this safe to call unconditionally from a handler, a service, or a test
// that never mounted the middleware. It degrades to a log line without `request_id` — which is the
// same line you would have had anyway — rather than to a nil dereference on the unhappy path, where
// logging matters most.
func Logger(ctx context.Context) *slog.Logger {
	if lg, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && lg != nil {
		return lg
	}

	return slog.Default()
}
