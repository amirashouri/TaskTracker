// Package me serves the signed-in user's own password endpoint. The caller's
// profile lives in internal/profile — PATCH /v1/me is the profile write the
// client already decodes — so the password change takes a route of its own:
// it is an action with its own request body, not a field patch.
package me

import (
	"errors"
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
)

// minPasswordLength is the policy a self-service change has to meet. It
// matches the length the CLI already enforces for a bootstrapped admin, so one
// number is the project's whole password policy.
const minPasswordLength = 8

// Handler answers POST /v1/me/password for the caller named by the auth
// middleware.
type Handler struct {
	users *entities.UserRepository
}

func NewHandler(users *entities.UserRepository) *Handler {
	return &Handler{users: users}
}

type changePasswordRequest struct {
	OldPasswordOrCode string `json:"old_password_or_code"`
	NewPassword       string `json:"new_password"`
}

type changePasswordResponse struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"display_name"`
	PasswordHash string `json:"password_hash"`
}

// ChangePassword sets a new password for the caller after verifying identity
// with either their current password or a valid, unconsumed reset code — the
// two credentials the client's change sheet offers. A code that verifies is
// consumed here, so one code authorises exactly one change.
//
// The response carries password_hash like no other /v1/me shape does: the
// client's DTO for this endpoint declares it, and specs/t8.1-change-password.md
// records that as the deliberate exception to the projection rule.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFrom(r.Context())
	if !ok || userID == "" {
		httpx.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	user, err := h.users.GetByID(r.Context(), entities.UserID(userID))
	if err != nil || user == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req changePasswordRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Neither field is trimmed: a password may legitimately start or end with a
	// space, and silently altering it is the bug cmd/tt/prompt.go already warns
	// about for the CLI's own secret reads.
	if req.OldPasswordOrCode == "" || req.NewPassword == "" {
		httpx.WriteError(w, http.StatusBadRequest, "old_password_or_code and new_password are required")
		return
	}
	if len(req.NewPassword) < minPasswordLength {
		httpx.WriteError(w, http.StatusBadRequest, "new password does not meet policy")
		return
	}

	// Identity, password first: verifying as a password consumes nothing, so a
	// member who merely remembers their password can never burn a code they did
	// not need. Only when the password fails is the credential read as a code.
	//
	// A credential that is not a code this user holds answers as a wrong
	// password — no oracle for guessing other members' codes. A code that does
	// exist but is spent or expired says so, which is the answer the forgot-
	// password flow has to surface.
	credential := req.OldPasswordOrCode
	if _, err := h.users.VerifyPassword(r.Context(), user.Email, credential); err == nil {
		// Identity verified.
	} else {
		var identityError string
		switch verified, codeErr := h.users.VerifyResetCode(r.Context(), user.ID, credential); {
		case verified:
			// Identity verified; the code is consumed by this call.
		case errors.Is(codeErr, entities.ErrResetCodeNotFound):
			identityError = "old password does not match"
		default:
			identityError = "reset code is invalid or expired"
		}
		if identityError != "" {
			httpx.WriteError(w, http.StatusBadRequest, identityError)
			return
		}
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := h.users.SetPassword(r.Context(), user.ID, string(hashed)); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not set password")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, changePasswordResponse{
		ID:           string(user.ID),
		Email:        user.Email,
		DisplayName:  user.DisplayName,
		PasswordHash: string(hashed),
	})
}
