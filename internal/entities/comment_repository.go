package entities

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type CommentRepository struct {
	db Querier
}

func NewCommentRepository(db Querier) *CommentRepository {
	return &CommentRepository{db: db}
}

func (r *CommentRepository) Create(ctx context.Context, c *Comment) error {
	if c.ID == "" {
		c.ID = NewCommentID()
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = time.Now().UnixMilli()
	}
	c.Version = uuid.New().String()

	query := `INSERT INTO comments (id, work_item_id, author_id, body, parent_id, created_at, version)
	          VALUES ($1, $2, $3, $4, $5, $6, $7)`
	_, err := r.db.ExecContext(ctx, query,
		c.ID, c.WorkItemID, c.AuthorID, c.Body, c.ParentID, c.CreatedAt, c.Version)
	return err
}

func (r *CommentRepository) GetByID(ctx context.Context, id CommentID) (*Comment, error) {
	query := `SELECT id, work_item_id, author_id, body, parent_id, created_at, version
	          FROM comments WHERE id = $1`
	row := r.db.QueryRowContext(ctx, query, id)

	var c Comment
	var parentID sql.NullString

	err := row.Scan(&c.ID, &c.WorkItemID, &c.AuthorID, &c.Body, &parentID, &c.CreatedAt, &c.Version)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if parentID.Valid {
		c.ParentID = (*CommentID)(&parentID.String)
	}
	return &c, nil
}

func (r *CommentRepository) Update(ctx context.Context, c *Comment) error {
	previous := c.Version
	c.Version = uuid.New().String()

	query := `UPDATE comments SET work_item_id=$1, author_id=$2, body=$3, parent_id=$4, version=$5
	          WHERE id=$6 AND version=$7`
	result, err := r.db.ExecContext(ctx, query,
		c.WorkItemID, c.AuthorID, c.Body, c.ParentID, c.Version, c.ID, previous)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("%w: comment %s", ErrVersionConflict, c.ID)
	}
	return nil
}

func (r *CommentRepository) Delete(ctx context.Context, id CommentID) error {
	query := `DELETE FROM comments WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

func (r *CommentRepository) ListByWorkItem(ctx context.Context, workItemID WorkItemID) ([]*Comment, error) {
	query := `SELECT id, work_item_id, author_id, body, parent_id, created_at, version
	          FROM comments WHERE work_item_id = $1 ORDER BY created_at`
	rows, err := r.db.QueryContext(ctx, query, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return r.scanComments(rows)
}

func (r *CommentRepository) scanComments(rows *sql.Rows) ([]*Comment, error) {
	var comments []*Comment
	for rows.Next() {
		var c Comment
		var parentID sql.NullString

		err := rows.Scan(&c.ID, &c.WorkItemID, &c.AuthorID, &c.Body, &parentID, &c.CreatedAt, &c.Version)
		if err != nil {
			return nil, err
		}

		if parentID.Valid {
			c.ParentID = (*CommentID)(&parentID.String)
		}
		comments = append(comments, &c)
	}
	return comments, nil
}
