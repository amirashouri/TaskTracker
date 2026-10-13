package entities

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type UserRepository struct {
	db *sql.DB
}

func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) Create(ctx context.Context, u *User) error {
	u.ID = NewUserID()
	now := time.Now().UnixMilli()
	u.CreatedAt = now

	// Hash password
	hashed, err := bcrypt.GenerateFromPassword([]byte(u.PasswordHash), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hashed)

	query := `INSERT INTO users (id, email, display_name, password_hash, position, avatar_id, status, last_seen, created_at)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	_, err = r.db.ExecContext(ctx, query,
		u.ID, u.Email, u.DisplayName, u.PasswordHash, u.Position, u.AvatarID, u.Status, u.LastSeen, u.CreatedAt)
	return err
}

func (r *UserRepository) GetByID(ctx context.Context, id UserID) (*User, error) {
	query := `SELECT id, email, display_name, password_hash, position, avatar_id, status, last_seen, created_at
	          FROM users WHERE id = $1`
	row := r.db.QueryRowContext(ctx, query, id)

	var u User
	var lastSeen sql.NullInt64

	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &u.Position, &u.AvatarID, &u.Status, &lastSeen, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if lastSeen.Valid {
		u.LastSeen = &lastSeen.Int64
	}
	return &u, nil
}

func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*User, error) {
	query := `SELECT id, email, display_name, password_hash, position, avatar_id, status, last_seen, created_at
	          FROM users WHERE email = $1`
	row := r.db.QueryRowContext(ctx, query, email)

	var u User
	var lastSeen sql.NullInt64

	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &u.Position, &u.AvatarID, &u.Status, &lastSeen, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if lastSeen.Valid {
		u.LastSeen = &lastSeen.Int64
	}
	return &u, nil
}

func (r *UserRepository) Update(ctx context.Context, u *User) error {
	query := `UPDATE users SET email=$2, display_name=$3, position=$4, avatar_id=$5, status=$6, last_seen=$7
	          WHERE id=$1`
	result, err := r.db.ExecContext(ctx, query,
		u.ID, u.Email, u.DisplayName, u.Position, u.AvatarID, u.Status, u.LastSeen)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
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

	// Update last seen
	now := time.Now().UnixMilli()
	u.LastSeen = &now
	r.Update(ctx, u)

	return u, nil
}

func (r *UserRepository) List(ctx context.Context) ([]*User, error) {
	query := `SELECT id, email, display_name, password_hash, position, avatar_id, status, last_seen, created_at
	          FROM users ORDER BY created_at DESC`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		var u User
		var lastSeen sql.NullInt64
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &u.Position, &u.AvatarID, &u.Status, &lastSeen, &u.CreatedAt); err != nil {
			return nil, err
		}
		if lastSeen.Valid {
			u.LastSeen = &lastSeen.Int64
		}
		users = append(users, &u)
	}
	return users, nil
}
