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
	w.ID = NewWorkItemID()
	now := time.Now().UnixMilli()
	w.CreatedAt = now
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
	w.UpdatedAt = time.Now().UnixMilli()
	w.Version = uuid.New().String()

	blockedJSON, _ := json.Marshal(w.BlockedByIDs)
	recurrenceJSON, _ := json.Marshal(w.Recurrence)

	query := `UPDATE work_items SET project_id=$2, key=$3, title=$4, details=$5, status=$6, priority=$7, assignee_id=$8, due_date=$9, recurrence=$10, story_points=$11, blocked_by_ids=$12, sprint_id=$13, rank=$14, updated_at=$15, resolved_at=$16, version=$17
	          WHERE id=$1 AND version=$18`
	result, err := r.db.ExecContext(ctx, query,
		w.ID, w.ProjectID, w.Key, w.Title, w.Details, w.Status, w.Priority,
		w.AssigneeID, w.DueDate, recurrenceJSON, w.StoryPoints, blockedJSON,
		w.SprintID, w.Rank, w.UpdatedAt, w.ResolvedAt, w.Version, w.Version)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("work item not found or version conflict")
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
