package me

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

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
		PasswordHash: "correct horse battery",
		DisplayName:  "Ada",
		Role:         entities.RoleMember,
	}
	if err := users.Create(context.Background(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return NewHandler(users), users, user
}

// call invokes ChangePassword with the caller already on the context, which is
// what middleware.Auth does in production. An empty userID is the unauthenticated
// request.
func call(handler http.HandlerFunc, userID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/me/password", strings.NewReader(body))
	if userID != "" {
		req = req.WithContext(auth.WithUser(req.Context(), userID, entities.RoleMember))
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, want, rec.Body.String())
	}
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("body = %s, want it to say %q", rec.Body.String(), want)
	}
}

// The key set is the wire contract: the client's DTO has ignoreUnknownKeys off,
// so a renamed or added key is a decoding failure in every installed build.
func wantExactKeys(t *testing.T, rec *httptest.ResponseRecorder, want []string) {
	t.Helper()

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &fields); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(fields) != len(want) {
		t.Errorf("keys = %v, want %v", keys(fields), want)
		return
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Errorf("response carries no %q; it has %v", key, keys(fields))
		}
	}
}

func keys(fields map[string]json.RawMessage) []string {
	out := make([]string, 0, len(fields))
	for key := range fields {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func TestChangePasswordRefusesAWrongCredential(t *testing.T) {
	handler, users, ada := newTestHandler(t)

	rec := call(handler.ChangePassword, string(ada.ID),
		`{"old_password_or_code":"not-the-password","new_password":"brand new secret"}`)
	wantStatus(t, rec, http.StatusBadRequest)
	wantError(t, rec, "old password does not match")

	if _, err := users.VerifyPassword(context.Background(), ada.Email, "correct horse battery"); err != nil {
		t.Errorf("a refused change altered the password: %v", err)
	}
}

// A credential that is merely wrong answers as a wrong password, never as a
// code error: the code-shaped answer would confirm that some guess names a real
// code.
func TestChangePasswordWithAnUnknownCodeAnswersAsAWrongPassword(t *testing.T) {
	handler, _, ada := newTestHandler(t)

	rec := call(handler.ChangePassword, string(ada.ID),
		`{"old_password_or_code":"00000000-0000-0000-0000-000000000000","new_password":"brand new secret"}`)
	wantStatus(t, rec, http.StatusBadRequest)
	wantError(t, rec, "old password does not match")
}

func TestChangePasswordRefusesAShortNewPassword(t *testing.T) {
	handler, users, ada := newTestHandler(t)

	rec := call(handler.ChangePassword, string(ada.ID),
		`{"old_password_or_code":"correct horse battery","new_password":"short"}`)
	wantStatus(t, rec, http.StatusBadRequest)
	wantError(t, rec, "new password does not meet policy")

	if _, err := users.VerifyPassword(context.Background(), ada.Email, "correct horse battery"); err != nil {
		t.Errorf("a policy refusal altered the password: %v", err)
	}
}

func TestChangePasswordWithTheOldPassword(t *testing.T) {
	handler, users, ada := newTestHandler(t)

	rec := call(handler.ChangePassword, string(ada.ID),
		`{"old_password_or_code":"correct horse battery","new_password":"brand new secret"}`)
	wantStatus(t, rec, http.StatusOK)
	wantExactKeys(t, rec, []string{"id", "email", "display_name", "password_hash"})

	if _, err := users.VerifyPassword(context.Background(), ada.Email, "brand new secret"); err != nil {
		t.Errorf("the new password does not verify: %v", err)
	}
	if _, err := users.VerifyPassword(context.Background(), ada.Email, "correct horse battery"); err == nil {
		t.Error("the old password still verifies after the change")
	}
}

func TestChangePasswordWithAResetCodeConsumesIt(t *testing.T) {
	handler, users, ada := newTestHandler(t)
	ctx := context.Background()

	code, err := users.GeneratePasswordResetCode(ctx, ada.ID, time.Hour)
	if err != nil {
		t.Fatalf("generate reset code: %v", err)
	}

	rec := call(handler.ChangePassword, string(ada.ID),
		`{"old_password_or_code":"`+code+`","new_password":"brand new secret"}`)
	wantStatus(t, rec, http.StatusOK)
	wantExactKeys(t, rec, []string{"id", "email", "display_name", "password_hash"})

	if _, err := users.VerifyPassword(ctx, ada.Email, "brand new secret"); err != nil {
		t.Errorf("the new password does not verify: %v", err)
	}

	// Single use: the same code must not authorise a second change.
	rec = call(handler.ChangePassword, string(ada.ID),
		`{"old_password_or_code":"`+code+`","new_password":"another secret 1"}`)
	wantStatus(t, rec, http.StatusBadRequest)
	wantError(t, rec, "reset code is invalid or expired")

	if _, err := users.VerifyPassword(ctx, ada.Email, "brand new secret"); err != nil {
		t.Errorf("the second, refused change altered the password: %v", err)
	}
}

func TestChangePasswordWithAnExpiredCodeSaysSo(t *testing.T) {
	handler, users, ada := newTestHandler(t)

	code, err := users.GeneratePasswordResetCode(context.Background(), ada.ID, -time.Hour)
	if err != nil {
		t.Fatalf("generate reset code: %v", err)
	}

	rec := call(handler.ChangePassword, string(ada.ID),
		`{"old_password_or_code":"`+code+`","new_password":"brand new secret"}`)
	wantStatus(t, rec, http.StatusBadRequest)
	wantError(t, rec, "reset code is invalid or expired")
}

func TestChangePasswordWithoutACallerIsUnauthorized(t *testing.T) {
	handler, _, _ := newTestHandler(t)

	rec := call(handler.ChangePassword, "",
		`{"old_password_or_code":"whatever","new_password":"brand new secret"}`)
	wantStatus(t, rec, http.StatusUnauthorized)
}
