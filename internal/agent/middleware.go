// Package agent serves the runner side of agent runs: the API a `tt agent run`
// worker calls to claim a dispatch, report progress, and finish.
//
// The app-facing half of the same feature (dispatching a work item, reading a
// run's progress, cancelling, registering a machine) is a separate surface with
// a different credential. Nothing here authenticates a user JWT, and a runner
// token is refused everywhere a user JWT is expected.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
)

// tokenPrefix marks an opaque runner token.
//
// It exists so a leaked value is recognisable in a log, and so a user JWT
// presented at a runner route is refused without a database lookup. A runner
// token is not a JWT: it is a random secret stored only as a hash, which is what
// makes revocation immediate rather than "when the token expires".
const tokenPrefix = "ttag_"

// HashRunnerToken is the one definition of how a runner token is stored and
// looked up, so registration and authentication cannot disagree.
//
// A single SHA-256 is right here where a password would need bcrypt: the token is
// 256 bits of randomness, not a value a person chose, so there is nothing to
// brute-force and no work factor worth paying on every request.
func HashRunnerToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type runnerContextKey struct{}

// WithRunner puts the authenticated runner on a request context.
func WithRunner(ctx context.Context, runner *entities.AgentRunner) context.Context {
	return context.WithValue(ctx, runnerContextKey{}, runner)
}

// RunnerFrom returns the runner the request authenticated as.
func RunnerFrom(ctx context.Context) (*entities.AgentRunner, bool) {
	runner, ok := ctx.Value(runnerContextKey{}).(*entities.AgentRunner)
	return runner, ok && runner != nil
}

// Authenticate resolves an `Authorization: Bearer ttag_…` header to a live
// runner and rejects everything else.
//
// The 401 is deliberately the same for a missing token, a malformed one, and an
// unknown or revoked one: telling them apart would let a caller probe which
// tokens exist.
func (h *Handler) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || !strings.HasPrefix(token, tokenPrefix) {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}

		runner, err := h.repo.RunnerByTokenHash(r.Context(), HashRunnerToken(token))
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "authentication failed")
			return
		}
		if runner == nil {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}

		next.ServeHTTP(w, r.WithContext(WithRunner(r.Context(), runner)))
	})
}

// bearerToken reads the credential out of an Authorization header. The scheme is
// matched case-sensitively because this server only ever issues `Bearer`, and a
// lenient match is another way for two components to disagree about one header.
func bearerToken(header string) (string, bool) {
	const scheme = "Bearer "
	if !strings.HasPrefix(header, scheme) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, scheme))
	return token, token != ""
}
