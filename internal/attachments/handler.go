package attachments

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
	"github.com/amirashouri/tasktracker-server/internal/sync"
)

// Handler serves attachment blobs. It reuses the sync engine's IsMember so blob
// access is scoped to the same project membership the change feed uses — one
// query, no chance the two checks disagree.
type Handler struct {
	engine   *sync.Engine
	db       *sql.DB
	blobs    *BlobStore
	maxBytes int64
	deadline time.Duration
}

func NewHandler(engine *sync.Engine, db *sql.DB, blobs *BlobStore, maxBytes int64, deadline time.Duration) *Handler {
	return &Handler{engine: engine, db: db, blobs: blobs, maxBytes: maxBytes, deadline: deadline}
}

func (h *Handler) caller(r *http.Request) (string, bool) {
	return auth.UserFrom(r.Context())
}

// membership checks the caller belongs to the project named in the path, and
// that the attachment row exists and belongs to that same project and work item.
// A mismatch is reported as 404 (not 403) so a caller cannot probe whether a
// project or attachment exists.
func (h *Handler) authorize(ctx context.Context, userID, projectID, workItemID, attachmentID string) (*entities.Attachment, bool) {
	member, err := h.engine.IsMember(ctx, userID, projectID)
	if err != nil || !member {
		return nil, false
	}
	row, err := entities.NewAttachmentRepository(h.db).GetByID(ctx, entities.AttachmentID(attachmentID))
	if err != nil || row == nil {
		return nil, false
	}
	if string(row.ProjectID) != projectID || string(row.WorkItemID) != workItemID {
		return nil, false
	}
	return row, true
}

// Upload streams one attachment's bytes. The metadata row must already exist
// (it arrives through the sync feed); only then may the blob be stored. The body
// is capped at maxBytes so a 25 MiB photo cannot exhaust memory.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	// Set per-request read/write deadline for this blob route.
	// This allows large uploads over slow links while keeping the 15s global
	// timeout for the JSON API. If the connection doesn't support deadlines
	// (e.g., in tests), we log and continue with the global timeout.
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Now().Add(h.deadline)); err != nil {
		slog.Debug("could not set read deadline for attachment upload", "error", err)
	}
	if err := rc.SetWriteDeadline(time.Now().Add(h.deadline)); err != nil {
		slog.Debug("could not set write deadline for attachment upload", "error", err)
	}

	userID, ok := h.caller(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	projectID := r.PathValue("projectId")
	workItemID := r.PathValue("workItemId")
	attachmentID := r.PathValue("attachmentId")
	if projectID == "" || workItemID == "" || attachmentID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing path parameters")
		return
	}

	if _, authorized := h.authorize(r.Context(), userID, projectID, workItemID, attachmentID); !authorized {
		httpx.WriteError(w, http.StatusNotFound, "attachment not found")
		return
	}

	// Cap the read before touching memory. MaxBytesReader fails past the limit
	// with a *http.MaxBytesError, which we map to 413.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "that file is larger than the server allows")
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, "could not read the upload")
		return
	}

	if err := h.blobs.Write(attachmentID, data); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not store the attachment")
		return
	}
	// Link the row to its bytes. This does not bump the row version, so it does
	// not rebroadcast the attachment through the change feed.
	if err := entities.NewAttachmentRepository(h.db).SetBlobRef(r.Context(), entities.AttachmentID(attachmentID), attachmentID); err != nil {
		// The blob is now orphaned; a later re-upload overwrites it. Best-effort.
		httpx.WriteError(w, http.StatusInternalServerError, "could not record the attachment")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "stored"})
}

// Download streams one attachment's bytes to a member, honouring Range requests.
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	// Set per-request read/write deadline for this blob route.
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Now().Add(h.deadline)); err != nil {
		slog.Debug("could not set read deadline for attachment download", "error", err)
	}
	if err := rc.SetWriteDeadline(time.Now().Add(h.deadline)); err != nil {
		slog.Debug("could not set write deadline for attachment download", "error", err)
	}

	userID, ok := h.caller(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	projectID := r.PathValue("projectId")
	workItemID := r.PathValue("workItemId")
	attachmentID := r.PathValue("attachmentId")
	if projectID == "" || workItemID == "" || attachmentID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing path parameters")
		return
	}

	row, authorized := h.authorize(r.Context(), userID, projectID, workItemID, attachmentID)
	if !authorized {
		httpx.WriteError(w, http.StatusNotFound, "attachment not found")
		return
	}
	if row.BlobRef == "" {
		httpx.WriteError(w, http.StatusNotFound, "attachment has not been uploaded yet")
		return
	}

	f, err := h.blobs.Open(attachmentID)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "attachment has not been uploaded yet")
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not read the attachment")
		return
	}
	// ServeContent handles Range, Content-Length and 206 for us. Pre-set the
	// stored mime type so the caller opens the file correctly; ServeContent
	// leaves a header that is already present alone.
	if row.MimeType != "" {
		w.Header().Set("Content-Type", row.MimeType)
	}
	if row.FileName != "" {
		w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+sanitizeDisposition(row.FileName))
	}
	http.ServeContent(w, r, row.FileName, info.ModTime(), f)
}

// sanitizeDisposition strips characters that would break the header; the value
// is wrapped in UTF-8” so non-ASCII names survive. A hostile name is a display
// question, not a header-injection one.
func sanitizeDisposition(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch r {
		case '"', '\\', '\n', '\r', ';':
			continue
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
