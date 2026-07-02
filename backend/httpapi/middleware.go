package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

// requestIDHeader is set on every response so clients and logs can correlate.
const requestIDHeader = "X-Request-Id"

// wrap applies the shared middleware chain: request id, panic recovery,
// request logging.
func wrap(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set(requestIDHeader, id)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				if logger != nil {
					logger.Error("panic", "requestId", id, "method", r.Method, "path", r.URL.Path, "panic", rec)
				}
				if !sw.wrote {
					writeError(sw, http.StatusInternalServerError, "internal error")
				}
				return
			}
			if logger != nil {
				logger.Info("request", "requestId", id, "method", r.Method, "path", r.URL.Path,
					"status", sw.status, "duration", time.Since(start))
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// statusWriter records the response status and whether a body write started,
// so recovery knows if it can still emit an error response.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(p)
}
