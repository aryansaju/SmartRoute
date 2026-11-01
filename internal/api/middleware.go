package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"runtime/debug"

	"github.com/<your-user>/smartroute-core/internal/telemetry"
)

type ctxKey int

const requestIDKey ctxKey = 1

// RequestID returns the id attached to ctx by the middleware.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// middleware assigns a request id, counts requests in flight and turns panics into HTTP 500.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, id))

		s.metrics.Add("smartroute_inflight_requests", "", 1)
		defer s.metrics.Add("smartroute_inflight_requests", "", -1)
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in handler", "request_id", id, "panic", rec, "stack", string(debug.Stack()))
				s.metrics.Add("smartroute_requests_total", telemetry.Labels("outcome", "panic", "stream", "false"), 1)
				writeError(w, http.StatusInternalServerError, "server_error", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

