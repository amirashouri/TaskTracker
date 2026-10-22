package entities

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type ProjectRepository struct {
	db Querier
}

func NewProjectRepository(db Querier) *ProjectRepository {
	return &ProjectRepository{db: db}
}

const projectColumns = `id, key_prefix, name, details, tags, is_archived, next_item_number, created_at, updated_at, version`

// scanProject reads one row selected with projectColumns. MemberIDs is not a
// column: membership lives in project_members and is loaded separately.
func scanProject(row scanner, p *Project) error {
	var tagsJSON []byte
	if err := row.Scan(&p.ID, &p.KeyPrefix, &p.Name, &p.Details, &tagsJSON, &p.IsArchived, &p.NextItemNumber, &p.CreatedAt, &p.UpdatedAt, &p.Version); err != nil {
		return err
	}
	// A malformed blob costs the project its tags, not the row: the rest of the
	// project is still worth returning, the same trade the work-item scan makes
	// for blockers.
	p.Tags = decodeTags(tagsJSON)
	return nil
}

// decodeTags reads the project's tag catalog. A missing or unreadable blob is an
// empty catalog rather than nil, because both the admin API and the sync feed
// re-serve this struct as JSON and a nil slice would marshal as `null` — which
// the client's non-null list cannot decode.
func decodeTags(data []byte) []ProjectTag {
	tags := []ProjectTag{}
	_ = json.Unmarshal(data, &tags)
	if tags == nil {
		tags = []ProjectTag{}
	}
	return tags
}

// scanMember reads the reduced projection GetMembers selects: a membership
// listing has no use for the password hash, so it never leaves the database.
func scanMember(row scanner, u *User) error {
	var lastSeen sql.NullInt64

	if err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Position, &u.AvatarID, &u.Status, &lastSeen, &u.CreatedAt); err != nil {
		return err
	}

	if lastSeen.Valid {
		u.LastSeen = &lastSeen.Int64
	}
	return nil
}

func (r *ProjectRepository) Create(ctx context.Context, p *Project) error {
	p.ID = orNewID(p.ID, NewProjectID)
	p.CreatedAt, p.UpdatedAt, p.Version = createStamp(p.CreatedAt)

	// The row and its member set must land together.
	return inTx(ctx, r.db, func(q Querier) error {
		tagsJSON, _ := json.Marshal(normalisedTags(p.Tags))
		if _, err := q.ExecContext(ctx, `INSERT INTO projects (`+projectColumns+`)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			p.ID, p.KeyPrefix, p.Name, p.Details, tagsJSON, p.IsArchived, p.NextItemNumber, p.CreatedAt, p.UpdatedAt, p.Version); err != nil {
			return err
		}
		return replaceMembers(ctx, q, p.ID, p.MemberIDs)
	})
}

func (r *ProjectRepository) GetByID(ctx context.Context, id ProjectID) (*Project, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = $1`, id)

	p, err := oneOrNil(row, scanProject)
	if err != nil || p == nil {
		return nil, err
	}
	if p.MemberIDs, err = r.memberIDs(ctx, p.ID); err != nil {
		return nil, err
	}
	return p, nil
}

// Update replaces the project row and its member set, and fails with
// ErrVersionConflict when the row changed since the caller read it.
func (r *ProjectRepository) Update(ctx context.Context, p *Project) error {
	previous := p.Version
	p.UpdatedAt = Now()
	p.Version = newVersion()

	return inTx(ctx, r.db, func(q Querier) error {
		// Placeholders ascend in text order: SQLite numbers them by appearance.
		tagsJSON, _ := json.Marshal(normalisedTags(p.Tags))
		if err := execOneRow(ctx, q, conflict("project", p.ID), `UPDATE projects SET key_prefix=$1, name=$2, details=$3, tags=$4, is_archived=$5, next_item_number=$6, updated_at=$7, version=$8
	          WHERE id=$9 AND version=$10`,
			p.KeyPrefix, p.Name, p.Details, tagsJSON, p.IsArchived, p.NextItemNumber, p.UpdatedAt, p.Version, p.ID, previous); err != nil {
			return err
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

	projects, err := collectRows(rows, scanProject)
	if err != nil {
		return nil, err
	}

	// One extra query per project rather than a join: a project has a handful
	// of members, and the join would have to be collapsed back into a list.
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

	return collectRows(rows, scanMember)
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

// normalisedTags guarantees the slice is non-nil, so the JSON the admin API and
// the sync feed re-serve is `[]` rather than `null`.
func normalisedTags(tags []ProjectTag) []ProjectTag {
	if tags == nil {
		return []ProjectTag{}
	}
	return tags
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
