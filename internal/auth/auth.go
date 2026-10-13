package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
	"github.com/amirashouri/tasktracker-server/internal/ratelimit"
)

// Token types carried in the "typ" claim so a refresh token cannot be replayed
// as an access token.
const (
	TokenTypeAccess  = "access"
	TokenTypeRefresh = "refresh"
)

// Claims is the signed payload of both token types.
type Claims struct {
	UserID    string `json:"uid"`
	Email     string `json:"email,omitempty"`
	Role      string `json:"role,omitempty"`
	TokenType string `json:"typ"`
	jwt.RegisteredClaims
}

type JWTManager struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
}

func NewJWTManager(secret string, accessTTL, refreshTTL time.Duration) *JWTManager {
	return &JWTManager{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		now:        time.Now,
	}
}

func (m *JWTManager) AccessTTL() time.Duration { return m.accessTTL }

func (m *JWTManager) RefreshTTL() time.Duration { return m.refreshTTL }

func (m *JWTManager) IssueAccess(userID, email, role string) (string, error) {
	token, _, err := m.issue(userID, email, role, TokenTypeAccess, m.accessTTL)
	return token, err
}

func (m *JWTManager) IssueRefresh(userID, email, role string) (string, error) {
	token, _, err := m.issue(userID, email, role, TokenTypeRefresh, m.refreshTTL)
	return token, err
}

// IssueRefreshWithID also returns the token's JWT id, which is what a refresh
// token is recorded and looked up by. Returning it avoids parsing the token back
// out just to learn its id.
func (m *JWTManager) IssueRefreshWithID(userID, email, role string) (token, id string, err error) {
	return m.issue(userID, email, role, TokenTypeRefresh, m.refreshTTL)
}

func (m *JWTManager) issue(userID, email, role, tokenType string, ttl time.Duration) (string, string, error) {
	now := m.now()
	id := uuid.NewString()
	claims := Claims{
		UserID:    userID,
		Email:     email,
		Role:      role,
		TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        id,
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", "", err
	}
	return token, id, nil
}

// Parse verifies the signature and expiry and returns the claims.
func (m *JWTManager) Parse(tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %q", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

type contextKey struct{ name string }

var (
	userContextKey = &contextKey{"auth.user"}
	roleContextKey = &contextKey{"auth.role"}
)

// WithUser stores the authenticated user id and role on the request context.
func WithUser(ctx context.Context, userID, role string) context.Context {
	ctx = context.WithValue(ctx, userContextKey, userID)
	return context.WithValue(ctx, roleContextKey, role)
}

// UserFrom returns the authenticated user id, if the request was authenticated.
func UserFrom(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userContextKey).(string)
	return userID, ok && userID != ""
}

// RoleFrom returns the authenticated user's role, if the request was authenticated.
func RoleFrom(ctx context.Context) (string, bool) {
	role, ok := ctx.Value(roleContextKey).(string)
	return role, ok
}

type Handler struct {
	jwt     *JWTManager
	users   *entities.UserRepository
	tokens  *TokenStore
	limiter *ratelimit.Limiter
}

func NewHandler(jwt *JWTManager, users *entities.UserRepository, tokens *TokenStore, limiter *ratelimit.Limiter) *Handler {
	return &Handler{jwt: jwt, users: users, tokens: tokens, limiter: limiter}
}

type signInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id,omitempty"`
	Email        string `json:"email,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
}

func (h *Handler) SignIn(w http.ResponseWriter, r *http.Request) {
	var req signInRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		httpx.WriteError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	// Throttle before the password is verified, so guessing is limited rather
	// than merely slowed by bcrypt.
	if retryAfter, ok := h.allowSignIn(r, email); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
		httpx.WriteError(w, http.StatusTooManyRequests, "too many sign-in attempts")
		return
	}

	user, err := h.users.VerifyPassword(r.Context(), email, req.Password)
	if err != nil || user == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// Clear the account's counter on success so a few typos do not lock a user
	// out. The address counter is left alone, so spraying stays limited.
	h.limiter.Reset("email:" + email)

	// A sign-in starts a new token family; every rotation descends from this id,
	// so the whole session can be revoked at once.
	if err := h.writeTokens(w, r, user, uuid.NewString()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not issue token")
	}
}

// allowSignIn checks the client address and the account being tried. Keying on
// both stops one address spraying many accounts and one account being sprayed
// from many addresses.
func (h *Handler) allowSignIn(r *http.Request, email string) (time.Duration, bool) {
	if h.limiter == nil {
		return 0, true
	}
	if allowed, retryAfter := h.limiter.Allow("ip:" + clientIP(r)); !allowed {
		return retryAfter, false
	}
	if allowed, retryAfter := h.limiter.Allow("email:" + email); !allowed {
		return retryAfter, false
	}
	return 0, true
}

// clientIP is the peer address. A deployment behind a proxy must be trusted to
// rewrite it; X-Forwarded-For is deliberately not read here.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// retryAfterSeconds rounds up, so the header never tells a client to retry
// before the window has actually reset.
func retryAfterSeconds(d time.Duration) int {
	seconds := int((d + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

// Refresh rotates a refresh token: the presented token is revoked and a new one
// is issued in the same family. A token that was already revoked is treated as
// theft, and its whole family is revoked.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil || req.RefreshToken == "" {
		httpx.WriteError(w, http.StatusBadRequest, "refreshToken is required")
		return
	}

	presented, claims, ok := h.storedRefreshToken(r, req.RefreshToken)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	if presented.Revoked() {
		// The token was rotated away, signed out, or reused: the family is
		// compromised, so end all of it.
		if err := h.tokens.RevokeFamily(r.Context(), presented.FamilyID, h.jwt.now()); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "could not revoke refresh token")
			return
		}
		httpx.WriteError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	user, err := h.users.GetByID(r.Context(), entities.UserID(claims.UserID))
	if err != nil || user == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	record, refresh, err := h.newRefreshToken(user, presented.FamilyID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not issue token")
		return
	}

	err = h.tokens.Rotate(r.Context(), presented.TokenHash, record)
	if errors.Is(err, ErrTokenReused) {
		// Another request rotated this token first. The race is the same signal
		// as a reuse, and Rotate has already revoked the family.
		httpx.WriteError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not rotate refresh token")
		return
	}

	h.writePair(w, user, refresh)
}

// SignOut revokes a refresh token's whole family, so signing out ends the
// session rather than only the token that happened to be presented. It is
// idempotent and never reveals whether the token was valid; an access token
// stays signed until it expires, which is what its short lifetime is for.
func (h *Handler) SignOut(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil || req.RefreshToken == "" {
		httpx.WriteError(w, http.StatusBadRequest, "refreshToken is required")
		return
	}

	if presented, _, ok := h.storedRefreshToken(r, req.RefreshToken); ok {
		if err := h.tokens.RevokeFamily(r.Context(), presented.FamilyID, h.jwt.now()); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "could not revoke refresh token")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// storedRefreshToken verifies a presented token and loads the record it refers
// to. It reports false for anything that cannot be used: a bad signature, an
// access token, an unknown or expired token, or a record owned by someone else.
func (h *Handler) storedRefreshToken(r *http.Request, token string) (*RefreshToken, *Claims, bool) {
	claims, err := h.jwt.Parse(token)
	if err != nil || claims.TokenType != TokenTypeRefresh || claims.ID == "" {
		return nil, nil, false
	}

	stored, err := h.tokens.Get(r.Context(), HashTokenID(claims.ID))
	if err != nil || stored == nil || stored.UserID != claims.UserID {
		return nil, nil, false
	}
	if stored.Expired(h.jwt.now()) {
		return nil, nil, false
	}
	return stored, claims, true
}

// newRefreshToken issues a refresh token and the record that stores it. Only the
// hash of the token's id is kept, so the token itself never reaches the database.
func (h *Handler) newRefreshToken(user *entities.User, familyID string) (RefreshToken, string, error) {
	refresh, id, err := h.jwt.IssueRefreshWithID(string(user.ID), user.Email, user.Role)
	if err != nil {
		return RefreshToken{}, "", err
	}
	now := h.jwt.now()
	return RefreshToken{
		TokenHash: HashTokenID(id),
		FamilyID:  familyID,
		UserID:    string(user.ID),
		IssuedAt:  now.UnixMilli(),
		ExpiresAt: now.Add(h.jwt.RefreshTTL()).UnixMilli(),
	}, refresh, nil
}

// writeTokens issues the first token pair of a session and stores its refresh
// token.
func (h *Handler) writeTokens(w http.ResponseWriter, r *http.Request, user *entities.User, familyID string) error {
	record, refresh, err := h.newRefreshToken(user, familyID)
	if err != nil {
		return err
	}
	if err := h.tokens.Save(r.Context(), record); err != nil {
		return err
	}
	h.writePair(w, user, refresh)
	return nil
}

func (h *Handler) writePair(w http.ResponseWriter, user *entities.User, refresh string) {
	access, err := h.jwt.IssueAccess(string(user.ID), user.Email, user.Role)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		UserID:       string(user.ID),
		Email:        user.Email,
		DisplayName:  user.DisplayName,
		TokenType:    "Bearer",
		ExpiresIn:    int64(h.jwt.AccessTTL().Seconds()),
	})
}
