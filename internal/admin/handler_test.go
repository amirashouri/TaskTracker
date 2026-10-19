package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/dbtest"
	"github.com/amirashouri/tasktracker-server/internal/entities"
)

// Deleting a member has to end the sessions they already hold, not merely
// refuse their next sign-in. The status check in the auth handler cannot show
// this — it would refuse a deleted account anyway — so the store is read
// directly: only the revoke makes a live token stop working.
func TestDeleteRevokesTheTargetsLiveTokens(t *testing.T) {
	database := dbtest.NewDatabase(t)
	users := entities.NewUserRepository(database.DB)
	tokens := auth.NewTokenStore(database.DB)
	jwtManager := auth.NewJWTManager("00000000000000000000000000000000", time.Hour, time.Hour)
	handler := NewHandler(users, tokens)
	ctx := context.Background()

	caller := &entities.User{Email: "admin@example.com", PasswordHash: "x", Role: entities.RoleAdmin}
	target := &entities.User{Email: "grace@example.com", PasswordHash: "x", Role: entities.RoleMember}
	for _, u := range []*entities.User{caller, target} {
		if err := users.Create(ctx, u); err != nil {
			t.Fatalf("create %s: %v", u.Email, err)
		}
	}

	_, tokenID, err := jwtManager.IssueRefreshWithID(string(target.ID), target.Email, target.Role)
	if err != nil {
		t.Fatalf("issue refresh token: %v", err)
	}
	if err := tokens.Save(ctx, auth.RefreshToken{
		TokenHash: auth.HashTokenID(tokenID),
		FamilyID:  "grace-family",
		UserID:    string(target.ID),
		IssuedAt:  time.Now().UnixMilli(),
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("save refresh token: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/users/"+string(target.ID), nil)
	req.SetPathValue("id", string(target.ID))
	req = req.WithContext(auth.WithUser(context.Background(), string(caller.ID), entities.RoleAdmin))
	rec := httptest.NewRecorder()

	handler.Delete(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}

	stored, err := tokens.Get(ctx, auth.HashTokenID(tokenID))
	if err != nil {
		t.Fatalf("get refresh token: %v", err)
	}
	if stored == nil || !stored.Revoked() {
		t.Error("the deleted member's live refresh token was not revoked")
	}

	after, err := users.GetByID(ctx, target.ID)
	if err != nil {
		t.Fatalf("reload target: %v", err)
	}
	if after.Status != entities.StatusDeleted {
		t.Errorf("target status = %q, want %q", after.Status, entities.StatusDeleted)
	}
}

// The member picker's first load has no query text, so an empty q has to be
// the directory rather than a 400. It also has to be the *live* directory: a
// soft-deleted account is still a real row, but it is not somebody a new
// project should be able to add.
func TestSearchWithNoQueryListsTheLiveDirectory(t *testing.T) {
	database := dbtest.NewDatabase(t)
	users := entities.NewUserRepository(database.DB)
	handler := NewHandler(users, auth.NewTokenStore(database.DB))
	ctx := context.Background()

	live := &entities.User{Email: "live@example.com", PasswordHash: "x", Role: entities.RoleMember}
	gone := &entities.User{Email: "gone@example.com", PasswordHash: "x", Role: entities.RoleMember}
	for _, u := range []*entities.User{live, gone} {
		if err := users.Create(ctx, u); err != nil {
			t.Fatalf("create %s: %v", u.Email, err)
		}
	}
	if err := users.SetStatus(ctx, gone.ID, entities.StatusDeleted); err != nil {
		t.Fatalf("delete %s: %v", gone.Email, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/users/search", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var resp []userResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode directory: %v", err)
	}
	if len(resp) != 1 || resp[0].ID != string(live.ID) {
		t.Errorf("directory = %+v, want only the live member %s", resp, live.ID)
	}
}
