package entities

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type WorkItemRepository struct {
	db Querier
}

func NewWorkItemRepository(db Querier) *WorkItemRepository {
	return &WorkItemRepository{db: db}
}

const workItemColumns = `id, project_id, key, title, details, status, priority, assignee_id, due_date, recurrence, story_points, blocked_by_ids, sprint_id, rank, created_at, updated_at, resolved_at, version`

// scanWorkItem reads one row selected with workItemColumns. Both GetByID and
// the list queries scan through it, so the column order and the nullable
// conversions are stated once.
func scanWorkItem(row scanner, w *WorkItem) error {
	var blockedJSON, recurrenceJSON []byte
	var assigneeID, sprintID sql.NullString
	var dueDate, resolvedAt, storyPoints sql.NullInt64

	if err := row.Scan(&w.ID, &w.ProjectID, &w.Key, &w.Title, &w.Details, &w.Status, &w.Priority,
		&assigneeID, &dueDate, &recurrenceJSON, &storyPoints, &blockedJSON, &sprintID, &w.Rank,
		&w.CreatedAt, &w.UpdatedAt, &resolvedAt, &w.Version); err != nil {
		return err
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

	// The blobs are written by this package, so a row that fails to decode was
	// edited by hand; the rest of it is still worth returning.
	_ = json.Unmarshal(blockedJSON, &w.BlockedByIDs)
	_ = json.Unmarshal(recurrenceJSON, &w.Recurrence)

	return nil
}

func (r *WorkItemRepository) Create(ctx context.Context, w *WorkItem) error {
	w.ID = orNewID(w.ID, NewWorkItemID)
	w.CreatedAt, w.UpdatedAt, w.Version = createStamp(w.CreatedAt)

	blockedJSON, _ := json.Marshal(w.BlockedByIDs)
	recurrenceJSON, _ := json.Marshal(w.Recurrence)

	query := `INSERT INTO work_items (` + workItemColumns + `)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`
	_, err := r.db.ExecContext(ctx, query,
		w.ID, w.ProjectID, w.Key, w.Title, w.Details, w.Status, w.Priority,
		w.AssigneeID, w.DueDate, recurrenceJSON, w.StoryPoints, blockedJSON,
		w.SprintID, w.Rank, w.CreatedAt, w.UpdatedAt, w.ResolvedAt, w.Version)
	return err
}

func (r *WorkItemRepository) GetByID(ctx context.Context, id WorkItemID) (*WorkItem, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+workItemColumns+` FROM work_items WHERE id = $1`, id)
	return oneOrNil(row, scanWorkItem)
}

func (r *WorkItemRepository) Update(ctx context.Context, w *WorkItem) error {
	previous := w.Version
	w.UpdatedAt = time.Now().UnixMilli()
	w.Version = newVersion()

	blockedJSON, _ := json.Marshal(w.BlockedByIDs)
	recurrenceJSON, _ := json.Marshal(w.Recurrence)

	query := `UPDATE work_items SET project_id=$1, key=$2, title=$3, details=$4, status=$5, priority=$6, assignee_id=$7, due_date=$8, recurrence=$9, story_points=$10, blocked_by_ids=$11, sprint_id=$12, rank=$13, updated_at=$14, resolved_at=$15, version=$16
	          WHERE id=$17 AND version=$18`
	return execOneRow(ctx, r.db, conflict("work item", w.ID), query,
		w.ProjectID, w.Key, w.Title, w.Details, w.Status, w.Priority,
		w.AssigneeID, w.DueDate, recurrenceJSON, w.StoryPoints, blockedJSON,
		w.SprintID, w.Rank, w.UpdatedAt, w.ResolvedAt, w.Version, w.ID, previous)
}

func (r *WorkItemRepository) Delete(ctx context.Context, id WorkItemID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM work_items WHERE id = $1`, id)
	return err
}

func (r *WorkItemRepository) ListByProject(ctx context.Context, projectID ProjectID) ([]*WorkItem, error) {
	query := `SELECT ` + workItemColumns + ` FROM work_items WHERE project_id = $1 ORDER BY rank, priority DESC, created_at`
	rows, err := r.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return collectRows(rows, scanWorkItem)
}

func (r *WorkItemRepository) ListBySprint(ctx context.Context, sprintID SprintID) ([]*WorkItem, error) {
	query := `SELECT ` + workItemColumns + ` FROM work_items WHERE sprint_id = $1 ORDER BY rank, priority DESC, created_at`
	rows, err := r.db.QueryContext(ctx, query, sprintID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return collectRows(rows, scanWorkItem)
}
