package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// resetCodeRequest mints a code the way the route does: the member is named by
// the path, and the response hands the code to the caller — there is no mail
// infrastructure, so this reply *is* the delivery (AGENTS.md §7).
func resetCodeRequest(t *testing.T, handler *Handler, userID string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/users/"+userID+"/password-reset-code", nil)
	req.SetPathValue("id", userID)
	rec := httptest.NewRecorder()
	handler.PasswordResetCode(rec, req)
	return rec
}

func TestPasswordResetCodeReturnsAFreshUsableCode(t *testing.T) {
	database := dbtest.NewDatabase(t)
	users := entities.NewUserRepository(database.DB)
	handler := NewHandler(users, auth.NewTokenStore(database.DB))
	ctx := context.Background()

	member := &entities.User{Email: "grace@example.com", PasswordHash: "x", Role: entities.RoleMember}
	if err := users.Create(ctx, member); err != nil {
		t.Fatalf("create member: %v", err)
	}

	rec := resetCodeRequest(t, handler, string(member.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("reset code = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	// The key set is the client's DTO contract: no sent flag, no password — the
	// code itself and when it dies, and nothing the client does not declare.
	var resp struct {
		Code      string `json:"code"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode reset code: %v", err)
	}
	if resp.Code == "" {
		t.Fatal("the response carries no code, so the admin has nothing to hand over")
	}
	if resp.ExpiresAt <= time.Now().UnixMilli() {
		t.Errorf("expires_at = %d, want a future epoch-millisecond instant", resp.ExpiresAt)
	}

	// The code the member receives is the one that verifies against the row.
	ok, err := users.VerifyResetCode(ctx, member.ID, resp.Code)
	if err != nil || !ok {
		t.Errorf("VerifyResetCode = %v, %v; want the minted code to verify", ok, err)
	}

	// Two mints are two different codes: nothing is reused across requests.
	second := resetCodeRequest(t, handler, string(member.ID))
	if second.Code == http.StatusCreated {
		var again struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(second.Body.Bytes(), &again); err == nil && again.Code == resp.Code {
			t.Error("a second mint returned the same code")
		}
	}
}

func TestPasswordResetCodeRefusesADeletedMember(t *testing.T) {
	database := dbtest.NewDatabase(t)
	users := entities.NewUserRepository(database.DB)
	handler := NewHandler(users, auth.NewTokenStore(database.DB))
	ctx := context.Background()

	gone := &entities.User{Email: "gone@example.com", PasswordHash: "x", Role: entities.RoleMember}
	if err := users.Create(ctx, gone); err != nil {
		t.Fatalf("create member: %v", err)
	}
	if err := users.SetStatus(ctx, gone.ID, entities.StatusDeleted); err != nil {
		t.Fatalf("delete member: %v", err)
	}

	rec := resetCodeRequest(t, handler, string(gone.ID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reset code for a deleted member = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "user is deleted") {
		t.Errorf("body = %s, want it to say the user is deleted", rec.Body.String())
	}
}

func TestPasswordResetCodeAnswersUserNotFound(t *testing.T) {
	database := dbtest.NewDatabase(t)
	users := entities.NewUserRepository(database.DB)
	handler := NewHandler(users, auth.NewTokenStore(database.DB))

	rec := resetCodeRequest(t, handler, string(entities.NewUserID()))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reset code for an unknown member = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "user not found") {
		t.Errorf("body = %s, want it to say the user was not found", rec.Body.String())
	}
}
