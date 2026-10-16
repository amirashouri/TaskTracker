// Package admin handles user management endpoints that require admin access.
// There is no self-signup: the admin creates users, and the first admin is
// bootstrapped via the CLI subcommand.
package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
)

type Handler struct {
	users  *entities.UserRepository
	tokens *auth.TokenStore
}

func NewHandler(users *entities.UserRepository, tokens *auth.TokenStore) *Handler {
	return &Handler{users: users, tokens: tokens}
}

// ── Create ──────────────────────────────────────────────────────────

type createUserRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name,omitempty"`
	Position    string `json:"position,omitempty"`
	Role        string `json:"role,omitempty"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		httpx.WriteError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = entities.RoleMember
	}
	if role != entities.RoleAdmin && role != entities.RoleMember {
		httpx.WriteError(w, http.StatusBadRequest, "role must be 'admin' or 'member'")
		return
	}

	// Reject duplicates early so the error message is actionable.
	if existing, _ := h.users.GetByEmail(r.Context(), email); existing != nil {
		httpx.WriteError(w, http.StatusConflict, "email already registered")
		return
	}

	user := &entities.User{
		Email:        email,
		PasswordHash: req.Password, // Create hashes it
		DisplayName:  req.DisplayName,
		Position:     req.Position,
		Role:         role,
	}

	if err := h.users.Create(r.Context(), user); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not create user")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, toUserResponse(user))
}

// ── List ────────────────────────────────────────────────────────────

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	users, err := h.users.List(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not list users")
		return
	}

	resp := make([]userResponse, 0, len(users))
	for _, u := range users {
		resp = append(resp, toUserResponse(u))
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// ── Update ──────────────────────────────────────────────────────────

type updateUserRequest struct {
	DisplayName *string `json:"display_name,omitempty"`
	Position    *string `json:"position,omitempty"`
	Role        *string `json:"role,omitempty"`
	Status      *string `json:"status,omitempty"`
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httpx.WriteError(w, http.StatusBadRequest, "user id is required")
		return
	}

	var req updateUserRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	user, err := h.users.GetByID(r.Context(), entities.UserID(id))
	if err != nil || user == nil {
		httpx.WriteError(w, http.StatusNotFound, "user not found")
		return
	}

	if req.DisplayName != nil {
		user.DisplayName = *req.DisplayName
	}
	if req.Position != nil {
		user.Position = *req.Position
	}
	if req.Status != nil {
		user.Status = *req.Status
	}

	if err := h.users.Update(r.Context(), user); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not update user")
		return
	}

	// Role is updated separately so it does not require a full user update.
	if req.Role != nil {
		role := strings.TrimSpace(*req.Role)
		if role != entities.RoleAdmin && role != entities.RoleMember {
			httpx.WriteError(w, http.StatusBadRequest, "role must be 'admin' or 'member'")
			return
		}
		if err := h.users.UpdateRole(r.Context(), entities.UserID(id), role); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "could not update role")
			return
		}
		user.Role = role
	}

	httpx.WriteJSON(w, http.StatusOK, toUserResponse(user))
}

// ── Delete ──────────────────────────────────────────────────────────

// Delete removes a member without removing the row. The account is marked
// deleted, which is what refuses its next sign-in and refresh, and every live
// refresh token is revoked so sessions that are already open stop rotating.
// The row itself stays: sync_log and project_members still reference it, and a
// reactivate has to be able to find it.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httpx.WriteError(w, http.StatusBadRequest, "user id is required")
		return
	}

	if callerID, ok := auth.UserFrom(r.Context()); ok && callerID == id {
		httpx.WriteError(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}

	user, err := h.users.GetByID(r.Context(), entities.UserID(id))
	if err != nil || user == nil {
		httpx.WriteError(w, http.StatusNotFound, "user not found")
		return
	}

	// Deleting the only admin would leave nobody able to manage users. The caller
	// is an admin by claim, but a claim can outlive the role it names, so the
	// count is what decides — not the caller's own role.
	if user.Role == entities.RoleAdmin {
		admins, err := h.users.CountByRole(r.Context(), entities.RoleAdmin)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "could not count admins")
			return
		}
		if admins <= 1 {
			httpx.WriteError(w, http.StatusConflict, "cannot delete the last admin")
			return
		}
	}

	// Revoke before flipping the status. The other order leaves a window in which
	// a refresh in flight is still accepted and mints a token the revoke never
	// sees; this order makes that refresh hit a revoked token and be treated as
	// reuse, which ends the family instead.
	if err := h.tokens.RevokeAllForUser(r.Context(), id, time.Now()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not revoke user sessions")
		return
	}

	if err := h.users.SetStatus(r.Context(), entities.UserID(id), entities.StatusDeleted); err != nil {
		httpx.WriteError(w, http.StatusNotFound, "user not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Directory search (any authenticated user) ───────────────────────

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		httpx.WriteError(w, http.StatusBadRequest, "query parameter 'q' is required")
		return
	}

	users, err := h.users.Search(r.Context(), q)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "search failed")
		return
	}

	resp := make([]userResponse, 0, len(users))
	for _, u := range users {
		resp = append(resp, toUserResponse(u))
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// ── Response shape ──────────────────────────────────────────────────

type userResponse struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Position    string `json:"position,omitempty"`
	AvatarID    string `json:"avatar_id,omitempty"`
	Status      string `json:"status,omitempty"`
	Role        string `json:"role,omitempty"`
	LastSeen    *int64 `json:"last_seen,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

func toUserResponse(u *entities.User) userResponse {
	return userResponse{
		ID:          string(u.ID),
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Position:    u.Position,
		AvatarID:    u.AvatarID,
		Status:      u.Status,
		Role:        u.Role,
		LastSeen:    u.LastSeen,
		CreatedAt:   u.CreatedAt,
	}
}
