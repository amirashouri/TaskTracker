package entities

import (
	"context"
	"database/sql"
	"fmt"
)

type EpicRepository struct {
	db Querier
}

func NewEpicRepository(db Querier) *EpicRepository {
	return &EpicRepository{db: db}
}

const epicColumns = `id, title, details, due_date, is_archived, owner_id, created_at, updated_at, version`

func scanEpic(row scanner, e *Epic) error {
	var dueDate sql.NullInt64
	var ownerID sql.NullString
	if err := row.Scan(&e.ID, &e.Title, &e.Details, &dueDate, &e.IsArchived, &ownerID,
		&e.CreatedAt, &e.UpdatedAt, &e.Version); err != nil {
		return err
	}
	e.DueDate = timestampOrNil(dueDate)
	e.OwnerID = nil
	if ownerID.Valid {
		id := UserID(ownerID.String)
		e.OwnerID = &id
	}
	return nil
}

// ensureEpicOwner gives a new epic with no owner the bootstrap admin. The
// repository owns this default rather than the client: an offline device may
// push a payload written before the field existed, and the document it serves
// back must still name one owner.
func ensureEpicOwner(ctx context.Context, q Querier, e *Epic) error {
	if e.OwnerID != nil {
		return nil
	}
	admin, err := NewUserRepository(q).FirstByRole(ctx, RoleAdmin)
	if err != nil {
		return err
	}
	if admin != nil {
		e.OwnerID = &admin.ID
	}
	return nil
}

// preserveEpicOwner keeps the stored owner when an update payload omits the
// field. Falling back to the admin here would silently reassign an epic every
// time an older client saved it; a row with no stored owner still gets the
// default.
func preserveEpicOwner(ctx context.Context, q Querier, e *Epic) error {
	if e.OwnerID != nil {
		return nil
	}
	var owner sql.NullString
	err := q.QueryRowContext(ctx, `SELECT owner_id FROM epics WHERE id = $1`, e.ID).Scan(&owner)
	if err == sql.ErrNoRows {
		return ensureEpicOwner(ctx, q, e)
	}
	if err != nil {
		return err
	}
	if owner.Valid {
		id := UserID(owner.String)
		e.OwnerID = &id
		return nil
	}
	return ensureEpicOwner(ctx, q, e)
}

// EpicLinkError reports a work item naming an epic that does not list the
// item's project. The link is refused for good: re-sending the same payload
// cannot make the epic span a project it does not name, so the engine answers
// `rejected` rather than spending the client's retry budget.
type EpicLinkError struct {
	EpicID    EpicID
	ProjectID ProjectID
}

func (e *EpicLinkError) Error() string {
	return fmt.Sprintf("epic %s does not include project %s", e.EpicID, e.ProjectID)
}

func (r *EpicRepository) Create(ctx context.Context, e *Epic) error {
	e.ID = orNewID(e.ID, NewEpicID)
	e.CreatedAt, e.UpdatedAt, e.Version = createStamp(e.CreatedAt)

	return inTx(ctx, r.db, func(q Querier) error {
		if err := ensureEpicOwner(ctx, q, e); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx,
			`INSERT INTO epics (`+epicColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			e.ID, e.Title, e.Details, e.DueDate, e.IsArchived, e.OwnerID,
			e.CreatedAt, e.UpdatedAt, e.Version); err != nil {
			return err
		}
		return replaceEpicProjects(ctx, q, e.ID, e.ProjectIDs)
	})
}

func (r *EpicRepository) GetByID(ctx context.Context, id EpicID) (*Epic, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+epicColumns+` FROM epics WHERE id = $1`, id)
	epic, err := oneOrNil(row, scanEpic)
	if err != nil || epic == nil {
		return epic, err
	}
	epic.ProjectIDs, err = epicProjectIDs(ctx, r.db, epic.ID)
	return epic, err
}

// Update replaces the project set wholesale: the client sends the whole
// document, and a link the payload no longer names must not survive.
func (r *EpicRepository) Update(ctx context.Context, e *Epic) error {
	previous := e.Version
	e.UpdatedAt = Now()
	e.Version = newVersion()

	return inTx(ctx, r.db, func(q Querier) error {
		if err := preserveEpicOwner(ctx, q, e); err != nil {
			return err
		}
		if err := execOneRow(ctx, q, conflict("epic", e.ID),
			`UPDATE epics SET title=$1, details=$2, due_date=$3, is_archived=$4,
			        owner_id=$5, updated_at=$6, version=$7
			  WHERE id=$8 AND version=$9`,
			e.Title, e.Details, e.DueDate, e.IsArchived, e.OwnerID,
			e.UpdatedAt, e.Version, e.ID, previous); err != nil {
			return err
		}
		return replaceEpicProjects(ctx, q, e.ID, e.ProjectIDs)
	})
}

func (r *EpicRepository) Delete(ctx context.Context, id EpicID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM epics WHERE id = $1`, id)
	return err
}

// LinkState reports whether the epic exists and whether it lists projectID.
// The two answers are distinct on purpose: a missing epic is a parent that may
// still arrive (the foreign key keeps the change `retry_later`), while an epic
// that exists without the project is a permanent refusal.
func (r *EpicRepository) LinkState(ctx context.Context, epicID EpicID, projectID ProjectID) (exists, lists bool, err error) {
	err = r.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM epics WHERE id = $1),
		       EXISTS (SELECT 1 FROM epic_projects WHERE epic_id = $1 AND project_id = $2)`,
		epicID, projectID).Scan(&exists, &lists)
	return exists, lists, err
}

// replaceEpicProjects rewrites the epic's project links in the caller's
// transaction, numbering them by their position in the payload.
func replaceEpicProjects(ctx context.Context, q Querier, epicID EpicID, projectIDs []ProjectID) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM epic_projects WHERE epic_id = $1`, epicID); err != nil {
		return err
	}

	seen := make(map[ProjectID]struct{}, len(projectIDs))
	position := 0
	for _, projectID := range projectIDs {
		if _, duplicate := seen[projectID]; duplicate {
			continue
		}
		seen[projectID] = struct{}{}
		if _, err := q.ExecContext(ctx,
			`INSERT INTO epic_projects (epic_id, project_id, position) VALUES ($1, $2, $3)`,
			epicID, projectID, position); err != nil {
			return err
		}
		position++
	}
	return nil
}

func epicProjectIDs(ctx context.Context, q Querier, epicID EpicID) ([]ProjectID, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT project_id FROM epic_projects WHERE epic_id = $1 ORDER BY position, project_id`, epicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []ProjectID{}
	for rows.Next() {
		var projectID ProjectID
		if err := rows.Scan(&projectID); err != nil {
			return nil, err
		}
		ids = append(ids, projectID)
	}
	return ids, rows.Err()
}
