package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
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

func (m *JWTManager) IssueAccess(userID, email string) (string, error) {
	return m.issue(userID, email, TokenTypeAccess, m.accessTTL)
}

func (m *JWTManager) IssueRefresh(userID, email string) (string, error) {
	return m.issue(userID, email, TokenTypeRefresh, m.refreshTTL)
}

func (m *JWTManager) issue(userID, email, tokenType string, ttl time.Duration) (string, error) {
	now := m.now()
	claims := Claims{
		UserID:    userID,
		Email:     email,
		TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        uuid.NewString(),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
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

var userContextKey = &contextKey{"auth.user"}

// WithUser stores the authenticated user id on the request context.
func WithUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userContextKey, userID)
}

// UserFrom returns the authenticated user id, if the request was authenticated.
func UserFrom(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userContextKey).(string)
	return userID, ok && userID != ""
}

type Handler struct {
	jwt   *JWTManager
	users *entities.UserRepository
}

func NewHandler(jwt *JWTManager, users *entities.UserRepository) *Handler {
	return &Handler{jwt: jwt, users: users}
}

type signInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type tokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int64  `json:"expiresIn"`
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

	user, err := h.users.VerifyPassword(r.Context(), email, req.Password)
	if err != nil || user == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	h.writeTokens(w, user)
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil || req.RefreshToken == "" {
		httpx.WriteError(w, http.StatusBadRequest, "refreshToken is required")
		return
	}

	claims, err := h.jwt.Parse(req.RefreshToken)
	if err != nil || claims.TokenType != TokenTypeRefresh {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	user, err := h.users.GetByID(r.Context(), entities.UserID(claims.UserID))
	if err != nil || user == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}
	h.writeTokens(w, user)
}

func (h *Handler) writeTokens(w http.ResponseWriter, user *entities.User) {
	access, err := h.jwt.IssueAccess(string(user.ID), user.Email)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	refresh, err := h.jwt.IssueRefresh(string(user.ID), user.Email)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int64(h.jwt.AccessTTL().Seconds()),
	})
}
