package entities

import (
	"context"
	"database/sql"
	"fmt"
)

type SprintRepository struct {
	db Querier
}

func NewSprintRepository(db Querier) *SprintRepository {
	return &SprintRepository{db: db}
}

const sprintColumns = `id, project_id, name, goal_story_points, goal, start_date, end_date, owner_id, is_finished, finished_at, is_archived, is_locked, created_at, updated_at, version`

// scanSprint reads one row selected with sprintColumns.
func scanSprint(row scanner, s *Sprint) error {
	var ownerID sql.NullString
	var finishedAt sql.NullInt64

	if err := row.Scan(&s.ID, &s.ProjectID, &s.Name, &s.GoalStoryPoints, &s.Goal, &s.StartDate, &s.EndDate,
		&ownerID, &s.IsFinished, &finishedAt, &s.IsArchived, &s.IsLocked, &s.CreatedAt, &s.UpdatedAt, &s.Version); err != nil {
		return err
	}

	if ownerID.Valid {
		s.OwnerID = (*UserID)(&ownerID.String)
	}
	s.FinishedAt = timestampOrNil(finishedAt)
	return nil
}

// SprintWindowOverlapError is the refusal Create and Update return when a
// sprint's window shares an instant with an unfinished sibling in the same
// project.
//
// The sentence is the client store's own refusal, word for word: a push this
// server turns down becomes a dead-lettered outbox row on the device that sent
// it, and the verdict's message is the only place that device can read why. The
// tail changes with the operation because an edit is not a new sprint.
type SprintWindowOverlapError struct {
	// Blocker is the name of the sprint whose window is in the way.
	Blocker string
	// Updating says the refused write was an edit rather than a create.
	Updating bool
}

func (e *SprintWindowOverlapError) Error() string {
	remedy := "starting another"
	if e.Updating {
		remedy = "saving this sprint"
	}
	return fmt.Sprintf("'%s' overlaps this window. Finish it before %s.", e.Blocker, remedy)
}

// refusesOverlap is the scheduling rule Create and Update share, and it mirrors
// the client's `sprintBlockingOverlap` rather than re-inventing it: two
// unfinished windows may not share an instant, both endpoints are inclusive, and
// a finished sprint is history rather than a timetable.
//
// It has to run here because the store cannot see another device. A device that
// is offline when a sibling is created passes its own store check — it has no
// row to find — and would otherwise push two open sprints into one project, at
// which point every device pulls the conflict and no layer reconciles it.
//
// The read joins the caller's transaction, so a push that arrives after the
// first device's sprint is committed sees it and is refused. Two pushes that are
// genuinely concurrent can still interleave under Postgres READ COMMITTED; the
// case worth covering is the sequential offline create, which this does.
func (r *SprintRepository) refusesOverlap(ctx context.Context, s *Sprint, updating bool) error {
	// A finished sprint is being written as history: its window may sit over an
	// open one, and refusing it would dead-letter an old sprint on a fresh
	// device. The client's rule draws the same line.
	if s.IsFinished {
		return nil
	}

	siblings, err := r.ListByProject(ctx, s.ProjectID)
	if err != nil {
		return err
	}
	for _, sibling := range siblings {
		if sibling.ID == s.ID || sibling.IsFinished {
			continue
		}
		if s.Overlaps(sibling) {
			return &SprintWindowOverlapError{Blocker: sibling.Name, Updating: updating}
		}
	}
	return nil
}

func (r *SprintRepository) Create(ctx context.Context, s *Sprint) error {
	s.ID = orNewID(s.ID, NewSprintID)
	if err := r.refusesOverlap(ctx, s, false); err != nil {
		return err
	}
	s.CreatedAt, s.UpdatedAt, s.Version = createStamp(s.CreatedAt)

	query := `INSERT INTO sprints (` + sprintColumns + `)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`
	_, err := r.db.ExecContext(ctx, query,
		s.ID, s.ProjectID, s.Name, s.GoalStoryPoints, s.Goal, s.StartDate, s.EndDate,
		s.OwnerID, s.IsFinished, s.FinishedAt, s.IsArchived, s.IsLocked, s.CreatedAt, s.UpdatedAt, s.Version)
	return err
}

func (r *SprintRepository) GetByID(ctx context.Context, id SprintID) (*Sprint, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+sprintColumns+` FROM sprints WHERE id = $1`, id)
	return oneOrNil(row, scanSprint)
}

func (r *SprintRepository) Update(ctx context.Context, s *Sprint) error {
	if err := r.refusesOverlap(ctx, s, true); err != nil {
		return err
	}
	previous := s.Version
	s.UpdatedAt = Now()
	s.Version = newVersion()

	query := `UPDATE sprints SET project_id=$1, name=$2, goal_story_points=$3, goal=$4, start_date=$5, end_date=$6, owner_id=$7, is_finished=$8, finished_at=$9, is_archived=$10, is_locked=$11, updated_at=$12, version=$13
	          WHERE id=$14 AND version=$15`
	return execOneRow(ctx, r.db, conflict("sprint", s.ID), query,
		s.ProjectID, s.Name, s.GoalStoryPoints, s.Goal, s.StartDate, s.EndDate,
		s.OwnerID, s.IsFinished, s.FinishedAt, s.IsArchived, s.IsLocked, s.UpdatedAt, s.Version, s.ID, previous)
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
