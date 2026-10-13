package entities

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ProjectRepository struct {
	db Querier
}

func NewProjectRepository(db Querier) *ProjectRepository {
	return &ProjectRepository{db: db}
}

const projectColumns = `id, key_prefix, name, details, is_archived, next_item_number, created_at, updated_at, version`

func (r *ProjectRepository) Create(ctx context.Context, p *Project) error {
	if p.ID == "" {
		p.ID = NewProjectID()
	}
	now := time.Now().UnixMilli()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	p.Version = uuid.New().String()

	// The row and its member set must land together.
	return inTx(ctx, r.db, func(q Querier) error {
		if _, err := q.ExecContext(ctx, `INSERT INTO projects (id, key_prefix, name, details, is_archived, next_item_number, created_at, updated_at, version)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			p.ID, p.KeyPrefix, p.Name, p.Details, p.IsArchived, p.NextItemNumber, p.CreatedAt, p.UpdatedAt, p.Version); err != nil {
			return err
		}
		return replaceMembers(ctx, q, p.ID, p.MemberIDs)
	})
}

func (r *ProjectRepository) GetByID(ctx context.Context, id ProjectID) (*Project, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = $1`, id)

	var p Project
	err := row.Scan(&p.ID, &p.KeyPrefix, &p.Name, &p.Details, &p.IsArchived, &p.NextItemNumber, &p.CreatedAt, &p.UpdatedAt, &p.Version)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.MemberIDs, err = r.memberIDs(ctx, p.ID); err != nil {
		return nil, err
	}
	return &p, nil
}

// Update replaces the project row and its member set, and fails with
// ErrVersionConflict when the row changed since the caller read it.
func (r *ProjectRepository) Update(ctx context.Context, p *Project) error {
	previous := p.Version
	p.UpdatedAt = time.Now().UnixMilli()
	p.Version = uuid.New().String()

	return inTx(ctx, r.db, func(q Querier) error {
		// Placeholders ascend in text order: SQLite numbers them by appearance.
		result, err := q.ExecContext(ctx, `UPDATE projects SET key_prefix=$1, name=$2, details=$3, is_archived=$4, next_item_number=$5, updated_at=$6, version=$7
	          WHERE id=$8 AND version=$9`,
			p.KeyPrefix, p.Name, p.Details, p.IsArchived, p.NextItemNumber, p.UpdatedAt, p.Version, p.ID, previous)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return fmt.Errorf("%w: project %s", ErrVersionConflict, p.ID)
		}
		return replaceMembers(ctx, q, p.ID, p.MemberIDs)
	})
}

func (r *ProjectRepository) Delete(ctx context.Context, id ProjectID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, id)
	return err
}

// AllocateKey reserves the next work-item number for a project and returns the
// formatted key. One statement both reads and increments the counter, so two
// concurrent callers cannot be handed the same number.
func (r *ProjectRepository) AllocateKey(ctx context.Context, projectID ProjectID) (string, error) {
	var prefix string
	var number int
	err := r.db.QueryRowContext(ctx, `
		UPDATE projects SET next_item_number = next_item_number + 1
		 WHERE id = $1
		 RETURNING key_prefix, next_item_number - 1`, projectID).Scan(&prefix, &number)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("project %s not found", projectID)
	}
	if err != nil {
		return "", err
	}
	return (&Project{KeyPrefix: prefix}).Key(number), nil
}

func (r *ProjectRepository) List(ctx context.Context, includeArchived bool) ([]*Project, error) {
	query := `SELECT ` + projectColumns + ` FROM projects`
	if !includeArchived {
		query += ` WHERE is_archived = FALSE`
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []*Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.KeyPrefix, &p.Name, &p.Details, &p.IsArchived, &p.NextItemNumber, &p.CreatedAt, &p.UpdatedAt, &p.Version); err != nil {
			return nil, err
		}
		projects = append(projects, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, project := range projects {
		if project.MemberIDs, err = r.memberIDs(ctx, project.ID); err != nil {
			return nil, err
		}
	}
	return projects, nil
}

func (r *ProjectRepository) AddMember(ctx context.Context, projectID ProjectID, userID UserID, role string) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)
	          ON CONFLICT (project_id, user_id) DO UPDATE SET role = $3`,
		projectID, userID, role)
	return err
}

func (r *ProjectRepository) RemoveMember(ctx context.Context, projectID ProjectID, userID UserID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	return err
}

func (r *ProjectRepository) GetMembers(ctx context.Context, projectID ProjectID) ([]*User, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT u.id, u.email, u.display_name, u.position, u.avatar_id, u.status, u.last_seen, u.created_at
	          FROM users u
	          JOIN project_members pm ON u.id = pm.user_id
	          WHERE pm.project_id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		var u User
		var lastSeen sql.NullInt64
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Position, &u.AvatarID, &u.Status, &lastSeen, &u.CreatedAt); err != nil {
			return nil, err
		}
		if lastSeen.Valid {
			u.LastSeen = &lastSeen.Int64
		}
		users = append(users, &u)
	}
	return users, rows.Err()
}

func (r *ProjectRepository) memberIDs(ctx context.Context, projectID ProjectID) ([]UserID, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT user_id FROM project_members WHERE project_id = $1 ORDER BY user_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []UserID
	for rows.Next() {
		var id UserID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// replaceMembers makes project_members the single writable store for membership.
func replaceMembers(ctx context.Context, exec Querier, projectID ProjectID, members []UserID) error {
	if _, err := exec.ExecContext(ctx, `DELETE FROM project_members WHERE project_id = $1`, projectID); err != nil {
		return err
	}

	seen := make(map[UserID]struct{}, len(members))
	for _, userID := range members {
		if userID == "" {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		if _, err := exec.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id) VALUES ($1, $2)`, projectID, userID); err != nil {
			return err
		}
	}
	return nil
}
