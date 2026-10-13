package entities

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type SprintRepository struct {
	db *sql.DB
}

func NewSprintRepository(db *sql.DB) *SprintRepository {
	return &SprintRepository{db: db}
}

func (r *SprintRepository) Create(ctx context.Context, s *Sprint) error {
	if s.ID == "" {
		s.ID = NewSprintID()
	}
	now := time.Now().UnixMilli()
	if s.CreatedAt == 0 {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	s.Version = uuid.New().String()

	query := `INSERT INTO sprints (id, project_id, name, goal_story_points, goal, start_date, end_date, owner_id, is_finished, finished_at, is_archived, created_at, updated_at, version)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`
	_, err := r.db.ExecContext(ctx, query,
		s.ID, s.ProjectID, s.Name, s.GoalStoryPoints, s.Goal, s.StartDate, s.EndDate,
		s.OwnerID, s.IsFinished, s.FinishedAt, s.IsArchived, s.CreatedAt, s.UpdatedAt, s.Version)
	return err
}

func (r *SprintRepository) GetByID(ctx context.Context, id SprintID) (*Sprint, error) {
	query := `SELECT id, project_id, name, goal_story_points, goal, start_date, end_date, owner_id, is_finished, finished_at, is_archived, created_at, updated_at, version
	          FROM sprints WHERE id = $1`
	row := r.db.QueryRowContext(ctx, query, id)

	var s Sprint
	var ownerID sql.NullString
	var finishedAt sql.NullInt64

	err := row.Scan(&s.ID, &s.ProjectID, &s.Name, &s.GoalStoryPoints, &s.Goal, &s.StartDate, &s.EndDate,
		&ownerID, &s.IsFinished, &finishedAt, &s.IsArchived, &s.CreatedAt, &s.UpdatedAt, &s.Version)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if ownerID.Valid {
		s.OwnerID = (*UserID)(&ownerID.String)
	}
	if finishedAt.Valid {
		s.FinishedAt = &finishedAt.Int64
	}
	return &s, nil
}

func (r *SprintRepository) Update(ctx context.Context, s *Sprint) error {
	previous := s.Version
	s.UpdatedAt = time.Now().UnixMilli()
	s.Version = uuid.New().String()

	query := `UPDATE sprints SET project_id=$1, name=$2, goal_story_points=$3, goal=$4, start_date=$5, end_date=$6, owner_id=$7, is_finished=$8, finished_at=$9, is_archived=$10, updated_at=$11, version=$12
	          WHERE id=$13 AND version=$14`
	result, err := r.db.ExecContext(ctx, query,
		s.ProjectID, s.Name, s.GoalStoryPoints, s.Goal, s.StartDate, s.EndDate,
		s.OwnerID, s.IsFinished, s.FinishedAt, s.IsArchived, s.UpdatedAt, s.Version, s.ID, previous)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("%w: sprint %s", ErrVersionConflict, s.ID)
	}
	return nil
}

func (r *SprintRepository) Delete(ctx context.Context, id SprintID) error {
	query := `DELETE FROM sprints WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

func (r *SprintRepository) ListByProject(ctx context.Context, projectID ProjectID) ([]*Sprint, error) {
	query := `SELECT id, project_id, name, goal_story_points, goal, start_date, end_date, owner_id, is_finished, finished_at, is_archived, created_at, updated_at, version
	          FROM sprints WHERE project_id = $1 ORDER BY start_date DESC`
	rows, err := r.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return r.scanSprints(rows)
}

func (r *SprintRepository) scanSprints(rows *sql.Rows) ([]*Sprint, error) {
	var sprints []*Sprint
	for rows.Next() {
		var s Sprint
		var ownerID sql.NullString
		var finishedAt sql.NullInt64

		err := rows.Scan(&s.ID, &s.ProjectID, &s.Name, &s.GoalStoryPoints, &s.Goal, &s.StartDate, &s.EndDate,
			&ownerID, &s.IsFinished, &finishedAt, &s.IsArchived, &s.CreatedAt, &s.UpdatedAt, &s.Version)
		if err != nil {
			return nil, err
		}

		if ownerID.Valid {
			s.OwnerID = (*UserID)(&ownerID.String)
		}
		if finishedAt.Valid {
			s.FinishedAt = &finishedAt.Int64
		}
		sprints = append(sprints, &s)
	}
	return sprints, nil
}
