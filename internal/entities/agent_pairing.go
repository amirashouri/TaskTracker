package entities

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ErrPairingCodeInvalid is the one answer an invalid pairing code gets: used,
// expired, unknown and malformed are deliberately indistinguishable, so a caller
// cannot probe which codes exist or have been spent.
var ErrPairingCodeInvalid = errors.New("pairing code is invalid or expired")

// AgentPairing is a consumed pairing code's facts. The code itself is never
// stored: the table holds only its hash, and this value is what the row tells
// the runner that is being created from it.
type AgentPairing struct {
	ProjectID ProjectID
	CreatedBy UserID
	ExpiresAt Timestamp
	CreatedAt Timestamp
}

// CreatePairingCode stores a short-lived, single-use code for one project.
//
// The caller mints the code, hashes it, and keeps the plaintext only long enough
// to return it once. The repository never sees the value, which is the same
// arrangement RegisterRunner uses for a runner token.
func (r *AgentRepository) CreatePairingCode(ctx context.Context, codeHash string, projectID ProjectID, createdBy UserID, expiresAt Timestamp) error {
	if strings.TrimSpace(codeHash) == "" {
		return errors.New("pairing code hash is required")
	}
	if projectID == "" || createdBy == "" {
		return errors.New("pairing code needs a project and a creator")
	}
	if expiresAt.IsZero() {
		return errors.New("pairing code needs an expiry")
	}

	_, err := r.db.ExecContext(ctx, `INSERT INTO agent_pairing_codes
	          (code_hash, project_id, created_by, expires_at, created_at)
	          VALUES ($1, $2, $3, $4, $5)`,
		codeHash, projectID, createdBy, expiresAt, Now())
	return err
}

// PairRunner spends a pairing code and registers the machine it authorizes in
// one transaction.
//
// The update is the single-use check: it only matches a code that is unused and
// not yet expired, so two agents redeeming one code at the same instant produce
// one runner and one ErrPairingCodeInvalid. The code stays in the table as a
// spent row, which is what makes the reuse visible in an audit rather than
// indistinguishable from a code that never existed.
func (r *AgentRepository) PairRunner(ctx context.Context, codeHash string, now Timestamp, runner *AgentRunner, tokenHash string) (*AgentRunner, error) {
	if strings.TrimSpace(codeHash) == "" {
		return nil, ErrPairingCodeInvalid
	}

	err := inTx(ctx, r.db, func(q Querier) error {
		var pairing AgentPairing
		err := q.QueryRowContext(ctx, `UPDATE agent_pairing_codes
		          SET used_at = $1
		          WHERE code_hash = $2 AND used_at IS NULL AND expires_at > $1
		          RETURNING project_id, created_by, expires_at, created_at`,
			now, codeHash).Scan(&pairing.ProjectID, &pairing.CreatedBy, &pairing.ExpiresAt, &pairing.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPairingCodeInvalid
		}
		if err != nil {
			return err
		}

		// The code owns the identity: the agent does not get to name a project
		// or an owner for itself.
		runner.UserID = pairing.CreatedBy
		projectID := pairing.ProjectID
		runner.ProjectID = &projectID
		return insertRunner(ctx, q, runner, tokenHash)
	})
	if err != nil {
		return nil, err
	}
	return runner, nil
}
