package entities

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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
	// A created account is active unless the caller says otherwise. Leaving the
	// column empty works too, but an explicit value is what lets "deleted" be the
	// only status that means "gone".
	if u.Status == "" {
		u.Status = StatusActive
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

// CountByRole returns how many accounts hold the role and have not been
// soft-deleted. Deleting the last admin is what it exists to prevent: an
// account that has been deleted cannot manage users, so it must not count.
func (r *UserRepository) CountByRole(ctx context.Context, role string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = $1 AND COALESCE(status, '') <> $2`,
		role, StatusDeleted).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// The three ways a reset code can be refused. They are sentinels because the
// change-password handler maps each to its own wire message, and matching on an
// error string would break the contract the moment the wording is edited.
var (
	ErrResetCodeNotFound = errors.New("reset code not found")
	ErrResetCodeUsed     = errors.New("reset code has already been used")
	ErrResetCodeExpired  = errors.New("reset code has expired")
)

// GeneratePasswordResetCode creates a new time-limited, single-use reset code
// for userID and returns it in the clear. The project owns no mail
// infrastructure (AGENTS.md §7), so the caller — the admin endpoint — is what
// hands the code to the member out of band; this row is the server's only copy.
// Times are epoch milliseconds in both dialects, as everywhere in this schema.
func (r *UserRepository) GeneratePasswordResetCode(ctx context.Context, userID UserID, ttl time.Duration) (string, error) {
	code := uuid.NewString()
	now := time.Now().UnixMilli()

	query := `INSERT INTO password_reset_codes (code, user_id, expires_at, created_at)
	          VALUES ($1, $2, $3, $4)`
	if _, err := r.db.ExecContext(ctx, query, code, userID, now+ttl.Milliseconds(), now); err != nil {
		return "", err
	}
	return code, nil
}

// VerifyResetCode checks that a reset code exists for this user, is unexpired
// and unconsumed — and consumes it in the same call, so one code authorises
// exactly one password change. The consumption is a guarded UPDATE, so two
// concurrent verifications of the same code cannot both win.
func (r *UserRepository) VerifyResetCode(ctx context.Context, userID UserID, code string) (bool, error) {
	var (
		consumed  bool
		expiresAt int64
	)
	query := `SELECT consumed, expires_at FROM password_reset_codes WHERE code = $1 AND user_id = $2`
	err := r.db.QueryRowContext(ctx, query, code, userID).Scan(&consumed, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrResetCodeNotFound
	}
	if err != nil {
		return false, err
	}
	if consumed {
		return false, ErrResetCodeUsed
	}
	if time.Now().UnixMilli() > expiresAt {
		return false, ErrResetCodeExpired
	}

	result, err := r.db.ExecContext(ctx,
		`UPDATE password_reset_codes SET consumed = TRUE
		 WHERE code = $1 AND user_id = $2 AND consumed = FALSE`, code, userID)
	if err != nil {
		return false, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return false, ErrResetCodeUsed
	}
	return true, nil
}

// SetPassword replaces the user's password hash. It is a dedicated write
// because Update deliberately never carries the hash — an ordinary profile
// patch must not be able to overwrite it — and change-password is the only
// caller that may.
func (r *UserRepository) SetPassword(ctx context.Context, id UserID, hash string) error {
	return execOneRow(ctx, r.db, errUserNotFound, `UPDATE users SET password_hash=$1 WHERE id=$2`, hash, id)
}

// List returns every user that has not been soft-deleted, newest first.
func (r *UserRepository) List(ctx context.Context) ([]*User, error) {
	return r.list(ctx, false)
}

// ListDeleted returns only soft-deleted users, newest first. A deleted account
// stays in the table (see StatusDeleted), so it has to be asked for by name
// rather than simply having disappeared from List.
func (r *UserRepository) ListDeleted(ctx context.Context) ([]*User, error) {
	return r.list(ctx, true)
}

func (r *UserRepository) list(ctx context.Context, deleted bool) ([]*User, error) {
	query := `SELECT ` + userColumns + ` FROM users`
	// Only `deleted` is excluded from the live list. An inactive or blank status
	// is still a present account: this story adds the delete bucket, it does not
	// reclassify the labels that already existed. COALESCE covers a row written
	// before the column carried a default, where the status is NULL.
	if deleted {
		query += ` WHERE status = $1`
	} else {
		query += ` WHERE COALESCE(status, '') <> $1`
	}
	query += ` ORDER BY created_at DESC`

	rows, err := r.db.QueryContext(ctx, query, StatusDeleted)
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
