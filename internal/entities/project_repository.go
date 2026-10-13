package entities

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ProjectRepository struct {
	db *sql.DB
}

func NewProjectRepository(db *sql.DB) *ProjectRepository {
	return &ProjectRepository{db: db}
}

func (r *ProjectRepository) Create(ctx context.Context, p *Project) error {
	p.ID = NewProjectID()
	now := time.Now().UnixMilli()
	p.CreatedAt = now
	p.UpdatedAt = now
	p.Version = uuid.New().String()

	membersJSON, _ := json.Marshal(p.MemberIDs)

	query := `INSERT INTO projects (id, key_prefix, name, details, member_ids, is_archived, next_item_number, created_at, updated_at, version)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	_, err := r.db.ExecContext(ctx, query,
		p.ID, p.KeyPrefix, p.Name, p.Details, membersJSON, p.IsArchived, p.NextItemNumber, p.CreatedAt, p.UpdatedAt, p.Version)
	return err
}

func (r *ProjectRepository) GetByID(ctx context.Context, id ProjectID) (*Project, error) {
	query := `SELECT id, key_prefix, name, details, member_ids, is_archived, next_item_number, created_at, updated_at, version
	          FROM projects WHERE id = $1`
	row := r.db.QueryRowContext(ctx, query, id)

	var p Project
	var membersJSON []byte
	err := row.Scan(&p.ID, &p.KeyPrefix, &p.Name, &p.Details, &membersJSON, &p.IsArchived, &p.NextItemNumber, &p.CreatedAt, &p.UpdatedAt, &p.Version)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal(membersJSON, &p.MemberIDs)
	return &p, nil
}

func (r *ProjectRepository) Update(ctx context.Context, p *Project) error {
	p.UpdatedAt = time.Now().UnixMilli()
	p.Version = uuid.New().String()

	membersJSON, _ := json.Marshal(p.MemberIDs)

	query := `UPDATE projects SET key_prefix=$2, name=$3, details=$4, member_ids=$5, is_archived=$6, next_item_number=$7, updated_at=$8, version=$9
	          WHERE id=$1 AND version=$10`
	result, err := r.db.ExecContext(ctx, query,
		p.ID, p.KeyPrefix, p.Name, p.Details, membersJSON, p.IsArchived, p.NextItemNumber, p.UpdatedAt, p.Version, p.Version)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("project not found or version conflict")
	}
	return nil
}

func (r *ProjectRepository) Delete(ctx context.Context, id ProjectID) error {
	query := `DELETE FROM projects WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

func (r *ProjectRepository) List(ctx context.Context, includeArchived bool) ([]*Project, error) {
	query := `SELECT id, key_prefix, name, details, member_ids, is_archived, next_item_number, created_at, updated_at, version
	          FROM projects`
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
		var membersJSON []byte
		if err := rows.Scan(&p.ID, &p.KeyPrefix, &p.Name, &p.Details, &membersJSON, &p.IsArchived, &p.NextItemNumber, &p.CreatedAt, &p.UpdatedAt, &p.Version); err != nil {
			return nil, err
		}
		json.Unmarshal(membersJSON, &p.MemberIDs)
		projects = append(projects, &p)
	}
	return projects, nil
}

func (r *ProjectRepository) AddMember(ctx context.Context, projectID ProjectID, userID UserID, role string) error {
	query := `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)
	          ON CONFLICT (project_id, user_id) DO UPDATE SET role = $3`
	_, err := r.db.ExecContext(ctx, query, projectID, userID, role)
	return err
}

func (r *ProjectRepository) RemoveMember(ctx context.Context, projectID ProjectID, userID UserID) error {
	query := `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`
	_, err := r.db.ExecContext(ctx, query, projectID, userID)
	return err
}

func (r *ProjectRepository) GetMembers(ctx context.Context, projectID ProjectID) ([]*User, error) {
	query := `SELECT u.id, u.email, u.display_name, u.position, u.avatar_id, u.status, u.last_seen, u.created_at
	          FROM users u
	          JOIN project_members pm ON u.id = pm.user_id
	          WHERE pm.project_id = $1`
	rows, err := r.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Position, &u.AvatarID, &u.Status, &u.LastSeen, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, &u)
	}
	return users, nil
}
