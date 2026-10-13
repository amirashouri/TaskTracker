package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// RequestIDHeader is the header a client may supply an id in, and the header
// the server echoes it back in.
const RequestIDHeader = "X-Request-ID"

// maxRequestIDLength bounds a client-supplied id, so a long value cannot be
// used to bloat a log line or a response header.
const maxRequestIDLength = 128

type contextKey struct{ name string }

var requestIDContextKey = &contextKey{"http.request_id"}

// RequestID ensures every request carries an id. It reuses a client-supplied
// X-Request-ID when it is safe to do so and generates one otherwise, puts the
// id on the context for other middleware and handlers to read, and echoes it
// back so a response can be matched to its log line.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := safeRequestID(r.Header.Get(RequestIDHeader))
			if id == "" {
				id = uuid.NewString()
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(withRequestID(r.Context(), id)))
		})
	}
}

// RequestIDFrom returns the request id, or "" when RequestID did not run.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey).(string)
	return id
}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey, id)
}

// safeRequestID rejects anything that could smuggle content into a response
// header or a log line: a client-supplied id must be printable ASCII of a
// bounded length. An empty result means "generate one".
func safeRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxRequestIDLength {
		return ""
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return ""
		}
	}
	return value
}
