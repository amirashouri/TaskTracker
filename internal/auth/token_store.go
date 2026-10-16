package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrTokenReused reports that a refresh token was presented after it had already
// been rotated away. Rotate revokes the whole token family before returning it,
// so a stolen token cannot outlive its detection.
var ErrTokenReused = errors.New("refresh token reused")

// RefreshToken is one stored refresh token. Only the hash of the token's JWT id
// is kept; the token itself never reaches the database.
type RefreshToken struct {
	TokenHash string
	FamilyID  string
	UserID    string
	IssuedAt  int64
	ExpiresAt int64
	RevokedAt *int64
}

// Revoked reports whether the token has been rotated away or signed out.
func (t *RefreshToken) Revoked() bool { return t.RevokedAt != nil }

// Expired reports whether the token's own expiry has passed.
func (t *RefreshToken) Expired(now time.Time) bool { return now.UnixMilli() >= t.ExpiresAt }

// TokenStore keeps refresh tokens so they can be rotated and revoked. It is the
// state that makes a stateless JWT revocable.
type TokenStore struct {
	db *sql.DB
}

func NewTokenStore(db *sql.DB) *TokenStore {
	return &TokenStore{db: db}
}

// HashTokenID hashes a refresh token's JWT id for storage and lookup. The id is
// random and single-use, so hashing it is what keeps a database read from
// yielding a replayable token.
func HashTokenID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// Save records a freshly issued token.
func (s *TokenStore) Save(ctx context.Context, token RefreshToken) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO refresh_tokens (token_hash, family_id, user_id, issued_at, expires_at, revoked_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		token.TokenHash, token.FamilyID, token.UserID, token.IssuedAt, token.ExpiresAt, token.RevokedAt)
	if err != nil {
		return fmt.Errorf("save refresh token: %w", err)
	}
	return nil
}

// Get returns the stored token, or nil when it was never issued.
func (s *TokenStore) Get(ctx context.Context, tokenHash string) (*RefreshToken, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT token_hash, family_id, user_id, issued_at, expires_at, revoked_at
		FROM refresh_tokens WHERE token_hash = $1`, tokenHash)

	var token RefreshToken
	var revokedAt sql.NullInt64
	err := row.Scan(&token.TokenHash, &token.FamilyID, &token.UserID, &token.IssuedAt, &token.ExpiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get refresh token: %w", err)
	}
	if revokedAt.Valid {
		token.RevokedAt = &revokedAt.Int64
	}
	return &token, nil
}

// Rotate revokes the presented token and records its replacement in one
// transaction. If the presented token is already revoked it instead revokes the
// whole family and returns ErrTokenReused: the conditional update is a
// compare-and-swap, so two concurrent uses of one token cannot both succeed.
func (s *TokenStore) Rotate(ctx context.Context, presentedHash string, next RefreshToken) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rotate: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = $1 WHERE token_hash = $2 AND revoked_at IS NULL`,
		next.IssuedAt, presentedHash)
	if err != nil {
		return fmt.Errorf("revoke presented refresh token: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		if _, err := revokeFamilyTx(ctx, tx, next.FamilyID, next.IssuedAt); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrTokenReused
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO refresh_tokens (token_hash, family_id, user_id, issued_at, expires_at, revoked_at)
		VALUES ($1, $2, $3, $4, $5, NULL)`,
		next.TokenHash, next.FamilyID, next.UserID, next.IssuedAt, next.ExpiresAt); err != nil {
		return fmt.Errorf("save rotated refresh token: %w", err)
	}
	return tx.Commit()
}

// RevokeFamily revokes every live token in a family. A family is one sign-in, so
// this ends a session: sign-out calls it, and so does reuse detection.
func (s *TokenStore) RevokeFamily(ctx context.Context, familyID string, at time.Time) error {
	_, err := revokeFamilyTx(ctx, s.db, familyID, at.UnixMilli())
	return err
}

// RevokeAllForUser revokes every live refresh token the user holds, across all
// of their families.
//
// Deleting a member calls this: revocation is what makes the deletion take
// effect for someone who is already signed in, since their access token is
// stateless and expires only on its own TTL. Rows are marked revoked rather than
// deleted, so the reuse detection that reads them keeps working.
func (s *TokenStore) RevokeAllForUser(ctx context.Context, userID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = $1 WHERE user_id = $2 AND revoked_at IS NULL`,
		at.UnixMilli(), userID)
	if err != nil {
		return fmt.Errorf("revoke refresh tokens for user: %w", err)
	}
	return nil
}

// revokeFamilyTx works on both a pool and a transaction, so Rotate can revoke a
// family inside the transaction that detected the reuse.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func revokeFamilyTx(ctx context.Context, db execer, familyID string, revokedAt int64) (sql.Result, error) {
	result, err := db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = $1 WHERE family_id = $2 AND revoked_at IS NULL`,
		revokedAt, familyID)
	if err != nil {
		return nil, fmt.Errorf("revoke refresh token family: %w", err)
	}
	return result, nil
}
