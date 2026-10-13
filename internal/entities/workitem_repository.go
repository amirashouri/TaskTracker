package entities

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type WorkItemRepository struct {
	db *sql.DB
}

func NewWorkItemRepository(db *sql.DB) *WorkItemRepository {
	return &WorkItemRepository{db: db}
}

func (r *WorkItemRepository) Create(ctx context.Context, w *WorkItem) error {
	if w.ID == "" {
		w.ID = NewWorkItemID()
	}
	now := time.Now().UnixMilli()
	if w.CreatedAt == 0 {
		w.CreatedAt = now
	}
	w.UpdatedAt = now
	w.Version = uuid.New().String()

	blockedJSON, _ := json.Marshal(w.BlockedByIDs)
	recurrenceJSON, _ := json.Marshal(w.Recurrence)

	query := `INSERT INTO work_items (id, project_id, key, title, details, status, priority, assignee_id, due_date, recurrence, story_points, blocked_by_ids, sprint_id, rank, created_at, updated_at, resolved_at, version)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`
	_, err := r.db.ExecContext(ctx, query,
		w.ID, w.ProjectID, w.Key, w.Title, w.Details, w.Status, w.Priority,
		w.AssigneeID, w.DueDate, recurrenceJSON, w.StoryPoints, blockedJSON,
		w.SprintID, w.Rank, w.CreatedAt, w.UpdatedAt, w.ResolvedAt, w.Version)
	return err
}

func (r *WorkItemRepository) GetByID(ctx context.Context, id WorkItemID) (*WorkItem, error) {
	query := `SELECT id, project_id, key, title, details, status, priority, assignee_id, due_date, recurrence, story_points, blocked_by_ids, sprint_id, rank, created_at, updated_at, resolved_at, version
	          FROM work_items WHERE id = $1`
	row := r.db.QueryRowContext(ctx, query, id)

	var w WorkItem
	var blockedJSON, recurrenceJSON []byte
	var assigneeID, sprintID sql.NullString
	var dueDate, resolvedAt sql.NullInt64
	var storyPoints sql.NullInt64

	err := row.Scan(&w.ID, &w.ProjectID, &w.Key, &w.Title, &w.Details, &w.Status, &w.Priority,
		&assigneeID, &dueDate, &recurrenceJSON, &storyPoints, &blockedJSON, &sprintID, &w.Rank,
		&w.CreatedAt, &w.UpdatedAt, &resolvedAt, &w.Version)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if assigneeID.Valid {
		w.AssigneeID = (*UserID)(&assigneeID.String)
	}
	if sprintID.Valid {
		w.SprintID = (*SprintID)(&sprintID.String)
	}
	if dueDate.Valid {
		w.DueDate = &dueDate.Int64
	}
	if resolvedAt.Valid {
		w.ResolvedAt = &resolvedAt.Int64
	}
	if storyPoints.Valid {
		w.StoryPoints = new(int)
		*w.StoryPoints = int(storyPoints.Int64)
	}
	json.Unmarshal(blockedJSON, &w.BlockedByIDs)
	json.Unmarshal(recurrenceJSON, &w.Recurrence)

	return &w, nil
}

func (r *WorkItemRepository) Update(ctx context.Context, w *WorkItem) error {
	previous := w.Version
	w.UpdatedAt = time.Now().UnixMilli()
	w.Version = uuid.New().String()

	blockedJSON, _ := json.Marshal(w.BlockedByIDs)
	recurrenceJSON, _ := json.Marshal(w.Recurrence)

	query := `UPDATE work_items SET project_id=$1, key=$2, title=$3, details=$4, status=$5, priority=$6, assignee_id=$7, due_date=$8, recurrence=$9, story_points=$10, blocked_by_ids=$11, sprint_id=$12, rank=$13, updated_at=$14, resolved_at=$15, version=$16
	          WHERE id=$17 AND version=$18`
	result, err := r.db.ExecContext(ctx, query,
		w.ProjectID, w.Key, w.Title, w.Details, w.Status, w.Priority,
		w.AssigneeID, w.DueDate, recurrenceJSON, w.StoryPoints, blockedJSON,
		w.SprintID, w.Rank, w.UpdatedAt, w.ResolvedAt, w.Version, w.ID, previous)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("%w: work item %s", ErrVersionConflict, w.ID)
	}
	return nil
}

func (r *WorkItemRepository) Delete(ctx context.Context, id WorkItemID) error {
	query := `DELETE FROM work_items WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

func (r *WorkItemRepository) ListByProject(ctx context.Context, projectID ProjectID) ([]*WorkItem, error) {
	query := `SELECT id, project_id, key, title, details, status, priority, assignee_id, due_date, recurrence, story_points, blocked_by_ids, sprint_id, rank, created_at, updated_at, resolved_at, version
	          FROM work_items WHERE project_id = $1 ORDER BY rank, priority DESC, created_at`
	rows, err := r.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return r.scanWorkItems(rows)
}

func (r *WorkItemRepository) ListBySprint(ctx context.Context, sprintID SprintID) ([]*WorkItem, error) {
	query := `SELECT id, project_id, key, title, details, status, priority, assignee_id, due_date, recurrence, story_points, blocked_by_ids, sprint_id, rank, created_at, updated_at, resolved_at, version
	          FROM work_items WHERE sprint_id = $1 ORDER BY rank, priority DESC, created_at`
	rows, err := r.db.QueryContext(ctx, query, sprintID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return r.scanWorkItems(rows)
}

func (r *WorkItemRepository) scanWorkItems(rows *sql.Rows) ([]*WorkItem, error) {
	var items []*WorkItem
	for rows.Next() {
		var w WorkItem
		var blockedJSON, recurrenceJSON []byte
		var assigneeID, sprintID sql.NullString
		var dueDate, resolvedAt sql.NullInt64
		var storyPoints sql.NullInt64

		err := rows.Scan(&w.ID, &w.ProjectID, &w.Key, &w.Title, &w.Details, &w.Status, &w.Priority,
			&assigneeID, &dueDate, &recurrenceJSON, &storyPoints, &blockedJSON, &sprintID, &w.Rank,
			&w.CreatedAt, &w.UpdatedAt, &resolvedAt, &w.Version)
		if err != nil {
			return nil, err
		}

		if assigneeID.Valid {
			w.AssigneeID = (*UserID)(&assigneeID.String)
		}
		if sprintID.Valid {
			w.SprintID = (*SprintID)(&sprintID.String)
		}
		if dueDate.Valid {
			w.DueDate = &dueDate.Int64
		}
		if resolvedAt.Valid {
			w.ResolvedAt = &resolvedAt.Int64
		}
		if storyPoints.Valid {
			w.StoryPoints = new(int)
			*w.StoryPoints = int(storyPoints.Int64)
		}
		json.Unmarshal(blockedJSON, &w.BlockedByIDs)
		json.Unmarshal(recurrenceJSON, &w.Recurrence)

		items = append(items, &w)
	}
	return items, nil
}
