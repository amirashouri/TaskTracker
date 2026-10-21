package profile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/dbtest"
	"github.com/amirashouri/tasktracker-server/internal/entities"
)

func newTestHandler(t *testing.T) (*Handler, *entities.UserRepository, *entities.User) {
	t.Helper()

	database := dbtest.NewDatabase(t)
	users := entities.NewUserRepository(database.DB)
	user := &entities.User{
		Email:        "ada@example.com",
		PasswordHash: "x",
		DisplayName:  "Ada",
		Position:     "Designer",
		Role:         entities.RoleMember,
	}
	if err := users.Create(context.Background(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return NewHandler(users), users, user
}

// call invokes one handler with the caller already on the context, which is
// what middleware.Auth does in production.
func call(handler http.HandlerFunc, method, userID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/v1/me", strings.NewReader(body))
	if userID != "" {
		req = req.WithContext(auth.WithUser(req.Context(), userID, entities.RoleMember))
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestGetReturnsOnlyTheCallersProfile(t *testing.T) {
	handler, users, ada := newTestHandler(t)

	grace := &entities.User{Email: "grace@example.com", PasswordHash: "x", DisplayName: "Grace"}
	if err := users.Create(context.Background(), grace); err != nil {
		t.Fatalf("create grace: %v", err)
	}

	for _, want := range []*entities.User{ada, grace} {
		rec := call(handler.Get, http.MethodGet, string(want.ID), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET as %s = %d, body %s", want.Email, rec.Code, rec.Body.String())
		}

		var got profileResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode profile: %v", err)
		}
		if got.ID != string(want.ID) || got.Email != want.Email {
			t.Errorf("GET as %s returned %s (%s), want the caller's own row", want.Email, got.ID, got.Email)
		}
		if got.Presence != entities.PresenceActive {
			t.Errorf("presence = %q, want the column default %q", got.Presence, entities.PresenceActive)
		}
	}
}

func TestPatchWritesOnlyTheFieldsItCarries(t *testing.T) {
	handler, users, ada := newTestHandler(t)

	rec := call(handler.Update, http.MethodPatch, string(ada.ID), `{"display_name":"Ada Lovelace"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, body %s", rec.Code, rec.Body.String())
	}

	got, err := users.GetProfile(context.Background(), ada.ID)
	if err != nil || got == nil {
		t.Fatalf("read back profile: %v", err)
	}
	if got.DisplayName != "Ada Lovelace" {
		t.Errorf("display_name = %q, want the patched value", got.DisplayName)
	}
	// The request carried no position, so the admin-created one has to survive;
	// this is what stops an avatar edit from erasing an admin's free-text title.
	if got.Position != "Designer" {
		t.Errorf("position = %q, want the untouched %q", got.Position, "Designer")
	}
	if got.Presence != entities.PresenceActive || got.PresenceNote != "" {
		t.Errorf("presence = %q/%q, want active/empty", got.Presence, got.PresenceNote)
	}

	// status is not part of the profile projection; read the account row to prove
	// the presence/name write did not touch the lifecycle.
	full, err := users.GetByID(context.Background(), ada.ID)
	if err != nil || full == nil {
		t.Fatalf("read back user: %v", err)
	}
	if full.Status != entities.StatusActive {
		t.Errorf("status = %q, want the account lifecycle untouched", full.Status)
	}
}

func TestPatchCanClearTheNoteButNotEmptyPresence(t *testing.T) {
	handler, users, ada := newTestHandler(t)

	if rec := call(handler.Update, http.MethodPatch, string(ada.ID), `{"presence":"away","presence_note":"At the dentist"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed presence = %d, body %s", rec.Code, rec.Body.String())
	}

	if rec := call(handler.Update, http.MethodPatch, string(ada.ID), `{"presence_note":""}`); rec.Code != http.StatusOK {
		t.Fatalf("clear note = %d, body %s", rec.Code, rec.Body.String())
	}
	got, _ := users.GetProfile(context.Background(), ada.ID)
	if got.PresenceNote != "" {
		t.Errorf("presence_note = %q, want cleared", got.PresenceNote)
	}
	if got.Presence != entities.PresenceAway {
		t.Errorf("presence = %q, want %q still in force", got.Presence, entities.PresenceAway)
	}

	if rec := call(handler.Update, http.MethodPatch, string(ada.ID), `{"presence":""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty presence = %d, want 400", rec.Code)
	}
}

func TestPatchLeavesAnInactiveAccountInactive(t *testing.T) {
	handler, users, ada := newTestHandler(t)

	if err := users.SetStatus(context.Background(), ada.ID, entities.StatusInactive); err != nil {
		t.Fatalf("inactivate: %v", err)
	}

	if rec := call(handler.Update, http.MethodPatch, string(ada.ID), `{"presence":"busy"}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, body %s", rec.Code, rec.Body.String())
	}

	got, err := users.GetByID(context.Background(), ada.ID)
	if err != nil || got == nil {
		t.Fatalf("read back user: %v", err)
	}
	if got.Status != entities.StatusInactive {
		t.Errorf("status = %q after a presence write, want %q — presence is not account state", got.Status, entities.StatusInactive)
	}
}

func TestPatchRejectsInvalidFields(t *testing.T) {
	handler, users, ada := newTestHandler(t)

	cases := []struct {
		name string
		body string
	}{
		{"blank display name", `{"display_name":"   "}`},
		{"overlong display name", `{"display_name":"` + strings.Repeat("a", 121) + `"}`},
		{"overlong position", `{"position":"` + strings.Repeat("a", 121) + `"}`},
		{"two digit avatar", `{"avatar_id":"10"}`},
		{"non digit avatar", `{"avatar_id":"x"}`},
		{"unknown presence", `{"presence":"sleeping"}`},
		{"overlong note", `{"presence_note":"` + strings.Repeat("a", 281) + `"}`},
		{"empty patch", `{}`},
		{"field that is not self-owned", `{"email":"other@example.com"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(handler.Update, http.MethodPatch, string(ada.ID), tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d, want 400 (body %s)", tc.name, rec.Code, rec.Body.String())
			}
		})
	}

	got, err := users.GetProfile(context.Background(), ada.ID)
	if err != nil || got == nil {
		t.Fatalf("read back profile: %v", err)
	}
	if got.DisplayName != "Ada" || got.Position != "Designer" || got.Presence != entities.PresenceActive {
		t.Errorf("a rejected patch changed the row: %+v", got)
	}
}

func TestHandlersRequireAuthentication(t *testing.T) {
	handler, _, _ := newTestHandler(t)

	if rec := call(handler.Get, http.MethodGet, "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET = %d, want 401", rec.Code)
	}
	if rec := call(handler.Update, http.MethodPatch, "", `{"presence":"away"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated PATCH = %d, want 401", rec.Code)
	}
}
