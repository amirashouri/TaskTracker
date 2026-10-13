package entities

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type UserRepository struct {
	db Querier
}

func NewUserRepository(db Querier) *UserRepository {
	return &UserRepository{db: db}
}

const userColumns = `id, email, display_name, password_hash, position, avatar_id, status, role, last_seen, created_at`

// scanUser reads one row selected with userColumns. The password hash is part
// of the projection because sign-in needs it; a handler that returns a user
// must still go through toUserResponse, which drops it.
func scanUser(row scanner, u *User) error {
	var lastSeen sql.NullInt64

	if err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &u.Position, &u.AvatarID, &u.Status, &u.Role, &lastSeen, &u.CreatedAt); err != nil {
		return err
	}

	if lastSeen.Valid {
		u.LastSeen = &lastSeen.Int64
	}
	return nil
}

func (r *UserRepository) Create(ctx context.Context, u *User) error {
	u.ID = orNewID(u.ID, NewUserID)
	// Users carry no UpdatedAt and no version: they are never synced, so
	// nothing compare-and-swaps on them.
	if u.CreatedAt == 0 {
		u.CreatedAt = time.Now().UnixMilli()
	}
	if u.Role == "" {
		u.Role = RoleMember
	}

	// Hash password
	hashed, err := bcrypt.GenerateFromPassword([]byte(u.PasswordHash), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hashed)

	query := `INSERT INTO users (` + userColumns + `)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	_, err = r.db.ExecContext(ctx, query,
		u.ID, u.Email, u.DisplayName, u.PasswordHash, u.Position, u.AvatarID, u.Status, u.Role, u.LastSeen, u.CreatedAt)
	return err
}

func (r *UserRepository) GetByID(ctx context.Context, id UserID) (*User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	return oneOrNil(row, scanUser)
}

func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email)
	return oneOrNil(row, scanUser)
}

func (r *UserRepository) Update(ctx context.Context, u *User) error {
	query := `UPDATE users SET email=$1, display_name=$2, position=$3, avatar_id=$4, status=$5, last_seen=$6
	          WHERE id=$7`
	return execOneRow(ctx, r.db, errUserNotFound, query,
		u.Email, u.DisplayName, u.Position, u.AvatarID, u.Status, u.LastSeen, u.ID)
}

func (r *UserRepository) UpdateRole(ctx context.Context, id UserID, role string) error {
	return execOneRow(ctx, r.db, errUserNotFound, `UPDATE users SET role=$1 WHERE id=$2`, role, id)
}

func (r *UserRepository) SetStatus(ctx context.Context, id UserID, status string) error {
	return execOneRow(ctx, r.db, errUserNotFound, `UPDATE users SET status=$1 WHERE id=$2`, status, id)
}

func (r *UserRepository) VerifyPassword(ctx context.Context, email, password string) (*User, error) {
	u, err := r.GetByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	// Update last seen. It is cosmetic, so a write that fails must not fail the
	// sign-in it was recording.
	now := time.Now().UnixMilli()
	u.LastSeen = &now
	_ = r.Update(ctx, u)

	return u, nil
}

func (r *UserRepository) List(ctx context.Context) ([]*User, error) {
	query := `SELECT ` + userColumns + ` FROM users ORDER BY created_at DESC`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return collectRows(rows, scanUser)
}

// Search returns users whose email or display name contains the query,
// case-insensitive, limited to 50 results.
func (r *UserRepository) Search(ctx context.Context, query string) ([]*User, error) {
	pattern := "%" + strings.ToLower(query) + "%"
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+userColumns+`
		FROM users
		WHERE LOWER(email) LIKE $1 OR LOWER(COALESCE(display_name, '')) LIKE $1
		ORDER BY created_at DESC
		LIMIT 50`, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return collectRows(rows, scanUser)
}
