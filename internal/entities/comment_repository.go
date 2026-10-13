package entities

import (
	"context"
	"database/sql"
)

type CommentRepository struct {
	db Querier
}

func NewCommentRepository(db Querier) *CommentRepository {
	return &CommentRepository{db: db}
}

const commentColumns = `id, work_item_id, author_id, body, parent_id, created_at, version`

// scanComment reads one row selected with commentColumns.
func scanComment(row scanner, c *Comment) error {
	var parentID sql.NullString

	if err := row.Scan(&c.ID, &c.WorkItemID, &c.AuthorID, &c.Body, &parentID, &c.CreatedAt, &c.Version); err != nil {
		return err
	}

	if parentID.Valid {
		c.ParentID = (*CommentID)(&parentID.String)
	}
	return nil
}

func (r *CommentRepository) Create(ctx context.Context, c *Comment) error {
	c.ID = orNewID(c.ID, NewCommentID)
	// A comment is written once and never edited, so it carries no UpdatedAt.
	c.CreatedAt, _, c.Version = createStamp(c.CreatedAt)

	query := `INSERT INTO comments (` + commentColumns + `)
	          VALUES ($1, $2, $3, $4, $5, $6, $7)`
	_, err := r.db.ExecContext(ctx, query,
		c.ID, c.WorkItemID, c.AuthorID, c.Body, c.ParentID, c.CreatedAt, c.Version)
	return err
}

func (r *CommentRepository) GetByID(ctx context.Context, id CommentID) (*Comment, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+commentColumns+` FROM comments WHERE id = $1`, id)
	return oneOrNil(row, scanComment)
}

func (r *CommentRepository) Update(ctx context.Context, c *Comment) error {
	previous := c.Version
	c.Version = newVersion()

	query := `UPDATE comments SET work_item_id=$1, author_id=$2, body=$3, parent_id=$4, version=$5
	          WHERE id=$6 AND version=$7`
	return execOneRow(ctx, r.db, conflict("comment", c.ID), query,
		c.WorkItemID, c.AuthorID, c.Body, c.ParentID, c.Version, c.ID, previous)
}

func (r *CommentRepository) Delete(ctx context.Context, id CommentID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM comments WHERE id = $1`, id)
	return err
}

func (r *CommentRepository) ListByWorkItem(ctx context.Context, workItemID WorkItemID) ([]*Comment, error) {
	query := `SELECT ` + commentColumns + ` FROM comments WHERE work_item_id = $1 ORDER BY created_at`
	rows, err := r.db.QueryContext(ctx, query, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return collectRows(rows, scanComment)
}
