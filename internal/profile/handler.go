// Package profile serves the signed-in user's own account profile.
//
// It is separate from package admin because the permission is the point:
// admin manages other people's accounts, while these two routes only ever
// touch the caller's row. Putting them behind the admin middleware would make
// a member's own display name an administrative act.
package profile

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
)

// Limits shared with the client's editors. They are part of the wire contract:
// a value inside them is accepted, one outside is a 400, and the client caps
// its own input at the same numbers.
const (
	maxDisplayNameRunes  = 120
	maxPositionRunes     = 120
	maxPresenceNoteRunes = 280
)

type Handler struct {
	users *entities.UserRepository
}

func NewHandler(users *entities.UserRepository) *Handler {
	return &Handler{users: users}
}

// profileResponse is intentionally smaller than the admin userResponse: the
// client has no use for the account timestamps or role here, and every key it
// does not declare would be a strict-decoding failure.
type profileResponse struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"display_name,omitempty"`
	Position     string `json:"position,omitempty"`
	AvatarID     string `json:"avatar_id,omitempty"`
	Presence     string `json:"presence,omitempty"`
	PresenceNote string `json:"presence_note,omitempty"`
}

func toProfileResponse(u *entities.User) profileResponse {
	return profileResponse{
		ID:           string(u.ID),
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Position:     u.Position,
		AvatarID:     u.AvatarID,
		Presence:     u.Presence,
		PresenceNote: u.PresenceNote,
	}
}

type updateProfileRequest struct {
	DisplayName  *string `json:"display_name,omitempty"`
	Position     *string `json:"position,omitempty"`
	AvatarID     *string `json:"avatar_id,omitempty"`
	Presence     *string `json:"presence,omitempty"`
	PresenceNote *string `json:"presence_note,omitempty"`
}

// Get answers GET /v1/me.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.UserFrom(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	user, err := h.users.GetProfile(r.Context(), entities.UserID(id))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not load profile")
		return
	}
	if user == nil {
		httpx.WriteError(w, http.StatusNotFound, "user not found")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, toProfileResponse(user))
}

// Update answers PATCH /v1/me. Only the keys the caller sent are written; an
// absent key is not an empty value.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.UserFrom(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req updateProfileRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	patch, message := buildPatch(req)
	if message != "" {
		httpx.WriteError(w, http.StatusBadRequest, message)
		return
	}

	userID := entities.UserID(id)
	if err := h.users.UpdateProfile(r.Context(), userID, patch); err != nil {
		// The token may outlive the row (an account deleted mid-session). Re-read
		// to tell that apart from a storage failure without exporting the
		// repository's not-found sentinel for one caller.
		if user, getErr := h.users.GetProfile(r.Context(), userID); getErr == nil && user == nil {
			httpx.WriteError(w, http.StatusNotFound, "user not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "could not update profile")
		return
	}

	user, err := h.users.GetProfile(r.Context(), userID)
	if err != nil || user == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not load profile")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, toProfileResponse(user))
}

// buildPatch validates the request and returns either a patch or the one
// sentence explaining the first field that failed.
func buildPatch(req updateProfileRequest) (entities.ProfilePatch, string) {
	var patch entities.ProfilePatch

	if req.DisplayName != nil {
		name := strings.TrimSpace(*req.DisplayName)
		if name == "" {
			return patch, "display name is required"
		}
		if utf8.RuneCountInString(name) > maxDisplayNameRunes {
			return patch, "display name is too long"
		}
		patch.DisplayName = &name
	}

	if req.Position != nil {
		position := strings.TrimSpace(*req.Position)
		if utf8.RuneCountInString(position) > maxPositionRunes {
			return patch, "position is too long"
		}
		patch.Position = &position
	}

	if req.AvatarID != nil {
		avatar := strings.TrimSpace(*req.AvatarID)
		if avatar != "" && (len(avatar) != 1 || avatar[0] < '0' || avatar[0] > '9') {
			return patch, "avatar id must be a single digit"
		}
		patch.AvatarID = &avatar
	}

	if req.Presence != nil {
		presence := strings.TrimSpace(*req.Presence)
		if !entities.IsValidPresence(presence) {
			return patch, "presence must be active, away, be_right_back or busy"
		}
		patch.Presence = &presence
	}

	if req.PresenceNote != nil {
		note := strings.TrimSpace(*req.PresenceNote)
		if utf8.RuneCountInString(note) > maxPresenceNoteRunes {
			return patch, "presence note is too long"
		}
		patch.PresenceNote = &note
	}

	if patch.Empty() {
		return patch, "no fields to update"
	}

	return patch, ""
}
