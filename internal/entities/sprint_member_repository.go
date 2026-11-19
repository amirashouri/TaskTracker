package entities

import (
	"context"
)

type SprintMemberRepository struct {
	db Querier
}

func NewSprintMemberRepository(db Querier) *SprintMemberRepository {
	return &SprintMemberRepository{db: db}
}

const sprintMemberColumns = `id, project_id, sprint_id, user_id, available_story_points, created_at, updated_at, version`

func scanSprintMember(row scanner, m *SprintMember) error {
	return row.Scan(&m.ID, &m.ProjectID, &m.SprintID, &m.UserID, &m.AvailableStoryPoints,
		&m.CreatedAt, &m.UpdatedAt, &m.Version)
}

func (r *SprintMemberRepository) Create(ctx context.Context, m *SprintMember) error {
	if m.ID == "" {
		m.ID = SprintMemberID(string(m.SprintID) + ":" + string(m.UserID))
	}
	if m.AvailableStoryPoints < 0 {
		m.AvailableStoryPoints = 0
	}
	m.CreatedAt, m.UpdatedAt, m.Version = createStamp(m.CreatedAt)

	query := `INSERT INTO sprint_members (` + sprintMemberColumns + `)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	_, err := r.db.ExecContext(ctx, query,
		m.ID, m.ProjectID, m.SprintID, m.UserID, m.AvailableStoryPoints,
		m.CreatedAt, m.UpdatedAt, m.Version)
	return err
}

func (r *SprintMemberRepository) GetByID(ctx context.Context, id SprintMemberID) (*SprintMember, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+sprintMemberColumns+` FROM sprint_members WHERE id = $1`, id)
	return oneOrNil(row, scanSprintMember)
}

func (r *SprintMemberRepository) BySprint(ctx context.Context, sprintID SprintID) ([]*SprintMember, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+sprintMemberColumns+` FROM sprint_members WHERE sprint_id = $1 ORDER BY id`, sprintID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectRows(rows, scanSprintMember)
}

func (r *SprintMemberRepository) Update(ctx context.Context, m *SprintMember) error {
	previous := m.Version
	m.UpdatedAt = Now()
	m.Version = newVersion()
	if m.AvailableStoryPoints < 0 {
		m.AvailableStoryPoints = 0
	}

	query := `UPDATE sprint_members SET project_id=$1, sprint_id=$2, user_id=$3,
	          available_story_points=$4, updated_at=$5, version=$6
	          WHERE id=$7 AND version=$8`
	return execOneRow(ctx, r.db, conflict("sprint member", string(m.ID)), query,
		m.ProjectID, m.SprintID, m.UserID, m.AvailableStoryPoints, m.UpdatedAt, m.Version,
		m.ID, previous)
}

func (r *SprintMemberRepository) Delete(ctx context.Context, id SprintMemberID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sprint_members WHERE id = $1`, id)
	return err
}
