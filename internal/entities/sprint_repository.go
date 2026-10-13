package entities

import (
	"context"
	"database/sql"
)

type SprintRepository struct {
	db Querier
}

func NewSprintRepository(db Querier) *SprintRepository {
	return &SprintRepository{db: db}
}

const sprintColumns = `id, project_id, name, goal_story_points, goal, start_date, end_date, owner_id, is_finished, finished_at, is_archived, created_at, updated_at, version`

// scanSprint reads one row selected with sprintColumns.
func scanSprint(row scanner, s *Sprint) error {
	var ownerID sql.NullString
	var finishedAt sql.NullInt64

	if err := row.Scan(&s.ID, &s.ProjectID, &s.Name, &s.GoalStoryPoints, &s.Goal, &s.StartDate, &s.EndDate,
		&ownerID, &s.IsFinished, &finishedAt, &s.IsArchived, &s.CreatedAt, &s.UpdatedAt, &s.Version); err != nil {
		return err
	}

	if ownerID.Valid {
		s.OwnerID = (*UserID)(&ownerID.String)
	}
	s.FinishedAt = timestampOrNil(finishedAt)
	return nil
}

func (r *SprintRepository) Create(ctx context.Context, s *Sprint) error {
	s.ID = orNewID(s.ID, NewSprintID)
	s.CreatedAt, s.UpdatedAt, s.Version = createStamp(s.CreatedAt)

	query := `INSERT INTO sprints (` + sprintColumns + `)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`
	_, err := r.db.ExecContext(ctx, query,
		s.ID, s.ProjectID, s.Name, s.GoalStoryPoints, s.Goal, s.StartDate, s.EndDate,
		s.OwnerID, s.IsFinished, s.FinishedAt, s.IsArchived, s.CreatedAt, s.UpdatedAt, s.Version)
	return err
}

func (r *SprintRepository) GetByID(ctx context.Context, id SprintID) (*Sprint, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+sprintColumns+` FROM sprints WHERE id = $1`, id)
	return oneOrNil(row, scanSprint)
}

func (r *SprintRepository) Update(ctx context.Context, s *Sprint) error {
	previous := s.Version
	s.UpdatedAt = Now()
	s.Version = newVersion()

	query := `UPDATE sprints SET project_id=$1, name=$2, goal_story_points=$3, goal=$4, start_date=$5, end_date=$6, owner_id=$7, is_finished=$8, finished_at=$9, is_archived=$10, updated_at=$11, version=$12
	          WHERE id=$13 AND version=$14`
	return execOneRow(ctx, r.db, conflict("sprint", s.ID), query,
		s.ProjectID, s.Name, s.GoalStoryPoints, s.Goal, s.StartDate, s.EndDate,
		s.OwnerID, s.IsFinished, s.FinishedAt, s.IsArchived, s.UpdatedAt, s.Version, s.ID, previous)
}

func (r *SprintRepository) Delete(ctx context.Context, id SprintID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sprints WHERE id = $1`, id)
	return err
}

func (r *SprintRepository) ListByProject(ctx context.Context, projectID ProjectID) ([]*Sprint, error) {
	query := `SELECT ` + sprintColumns + ` FROM sprints WHERE project_id = $1 ORDER BY start_date DESC`
	rows, err := r.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return collectRows(rows, scanSprint)
}
