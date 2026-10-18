package entities

import (
	"context"
	"database/sql"
)

type AttachmentRepository struct {
	db Querier
}

func NewAttachmentRepository(db Querier) *AttachmentRepository {
	return &AttachmentRepository{db: db}
}

const attachmentColumns = `id, work_item_id, project_id, file_name, mime_type, size_bytes, blob_ref, created_by, created_at, version`

// scanAttachment reads one row selected with attachmentColumns.
func scanAttachment(row scanner, a *Attachment) error {
	var (
		mimeType  sql.NullString
		blobRef   sql.NullString
		createdBy sql.NullString
	)
	if err := row.Scan(
		&a.ID, &a.WorkItemID, &a.ProjectID, &a.FileName, &mimeType, &a.SizeBytes, &blobRef, &createdBy, &a.CreatedAt, &a.Version,
	); err != nil {
		return err
	}
	if mimeType.Valid {
		a.MimeType = mimeType.String
	}
	if blobRef.Valid {
		a.BlobRef = blobRef.String
	}
	if createdBy.Valid {
		a.CreatedBy = UserID(createdBy.String)
	}
	return nil
}

// attachmentNullString stores a nullable text column: an empty value is NULL,
// not an empty string, so "unknown" and "blank" stay distinct on read.
func attachmentNullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// attachmentCreatedBy stores the nullable FK to users: an empty id is NULL so it
// does not violate the foreign key (a zero UUID is not a real user).
func attachmentCreatedBy(id UserID) any {
	if id == "" {
		return nil
	}
	return id
}

func (r *AttachmentRepository) Create(ctx context.Context, a *Attachment) error {
	a.ID = orNewID(a.ID, NewAttachmentID)
	// An attachment is authored on a device and never edited in place through the
	// sync engine once its blob is uploaded — the upload only sets blob_ref (see
	// SetBlobRef), so Create stamps the row once.
	a.CreatedAt, _, a.Version = createStamp(a.CreatedAt)

	query := `INSERT INTO attachments (` + attachmentColumns + `)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	_, err := r.db.ExecContext(ctx, query,
		a.ID, a.WorkItemID, a.ProjectID, a.FileName,
		attachmentNullString(a.MimeType), a.SizeBytes, attachmentNullString(a.BlobRef),
		attachmentCreatedBy(a.CreatedBy), a.CreatedAt, a.Version)
	return err
}

func (r *AttachmentRepository) GetByID(ctx context.Context, id AttachmentID) (*Attachment, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+attachmentColumns+` FROM attachments WHERE id = $1`, id)
	return oneOrNil(row, scanAttachment)
}

// Update overwrites the row under a version compare-and-swap, mirroring the
// other entities. It is the path the sync engine takes when a re-pushed
// attachment carries a newer version; SetBlobRef is the lighter path the file
// API uses after an upload, and deliberately does not bump version.
func (r *AttachmentRepository) Update(ctx context.Context, a *Attachment) error {
	previous := a.Version
	a.Version = newVersion()

	query := `UPDATE attachments SET work_item_id=$1, project_id=$2, file_name=$3,
	          mime_type=$4, size_bytes=$5, blob_ref=$6, created_by=$7, created_at=$8, version=$9
	          WHERE id=$10 AND version=$11`
	return execOneRow(ctx, r.db, conflict("attachment", a.ID), query,
		a.WorkItemID, a.ProjectID, a.FileName, attachmentNullString(a.MimeType), a.SizeBytes,
		attachmentNullString(a.BlobRef), attachmentCreatedBy(a.CreatedBy), a.CreatedAt, a.Version,
		a.ID, previous)
}

func (r *AttachmentRepository) Delete(ctx context.Context, id AttachmentID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM attachments WHERE id = $1`, id)
	return err
}

func (r *AttachmentRepository) ListByWorkItem(ctx context.Context, workItemID WorkItemID) ([]*Attachment, error) {
	query := `SELECT ` + attachmentColumns + ` FROM attachments WHERE work_item_id = $1 ORDER BY created_at, id`
	rows, err := r.db.QueryContext(ctx, query, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectRows(rows, scanAttachment)
}

// ListByProject backs server-side cleanup (a deleted project drops its
// attachments' rows, and the caller removes their blobs) without a join.
func (r *AttachmentRepository) ListByProject(ctx context.Context, projectID ProjectID) ([]*Attachment, error) {
	query := `SELECT ` + attachmentColumns + ` FROM attachments WHERE project_id = $1 ORDER BY created_at, id`
	rows, err := r.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectRows(rows, scanAttachment)
}

// SetBlobRef links a row to its uploaded bytes. It does NOT bump version: an
// upload is not an entity edit, and bumping would re-broadcast the attachment to
// every member as if its metadata had changed.
func (r *AttachmentRepository) SetBlobRef(ctx context.Context, id AttachmentID, blobRef string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE attachments SET blob_ref=$1 WHERE id=$2`, blobRef, id)
	return err
}
