package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
)

// Middleware wraps a handler with a cross-cutting concern.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so the first listed is the outermost.
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Logging records method, path, status and duration for every request. A
// request that ended in a server error is logged at error level, so a filter
// on level finds real failures without hiding the rest.
func Logging(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)

			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"duration_ms", time.Since(start).Milliseconds(),
			}
			if recorder.status >= http.StatusInternalServerError {
				logger.Error("request failed", attrs...)
				return
			}
			logger.Info("request", attrs...)
		})
	}
}

// Recover turns a panic into a 500 instead of dropping the connection, and
// records the stack at error level.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Error("panic serving request",
						"method", r.Method,
						"path", r.URL.Path,
						"panic", recovered,
						"stack", string(debug.Stack()))
					httpx.WriteError(w, http.StatusInternalServerError, "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// CORS answers preflight requests and reflects an allowed Origin. Mobile
// clients do not send Origin, so an empty allow-list simply disables the
// headers.
func CORS(origins []string) Middleware {
	allowed := make(map[string]struct{}, len(origins))
	wildcard := false
	for _, origin := range origins {
		allowed[origin] = struct{}{}
		if origin == "*" {
			wildcard = true
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				if _, ok := allowed[origin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Add("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
					w.Header().Set("Access-Control-Max-Age", "600")
				} else if wildcard {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				}
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets the response headers that are safe for every endpoint.
// nosniff stops a browser from guessing a content type and executing a JSON
// body as script.
func SecurityHeaders() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		})
	}
}

// NoStore keeps a response out of every cache. Auth responses carry tokens, so
// neither a shared proxy nor the client's HTTP cache may retain them.
func NoStore() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	}
}

// Auth requires a valid access token and puts the user id and role on the context.
func Auth(jwt *auth.JWTManager) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r.Header.Get("Authorization"))
			if !ok {
				httpx.WriteError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			claims, err := jwt.Parse(token)
			if err != nil {
				httpx.WriteError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			if claims.TokenType != auth.TokenTypeAccess {
				httpx.WriteError(w, http.StatusUnauthorized, "access token required")
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), claims.UserID, claims.Role)))
		})
	}
}

// AdminOnly rejects requests from non-admin users. Must be chained after Auth,
// which sets the role on the context.
func AdminOnly() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, ok := auth.RoleFrom(r.Context())
			if !ok || role != entities.RoleAdmin {
				httpx.WriteError(w, http.StatusForbidden, "admin access required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}
