package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
	"github.com/amirashouri/tasktracker-server/internal/sync"
)

// ProjectHandler serves the admin project screens. Projects are otherwise
// reachable only through the sync feed, where a client sees the projects it is a
// member of; these routes are what let an admin see, edit and archive every
// project on the server, member or not.
type ProjectHandler struct {
	projects *entities.ProjectRepository
	engine   *sync.Engine
}

func NewProjectHandler(projects *entities.ProjectRepository, engine *sync.Engine) *ProjectHandler {
	return &ProjectHandler{projects: projects, engine: engine}
}

// The buckets the projects screen's segmented control switches between. They
// name the archived flag, which is a project's own state -- not the "deleted"
// bucket the members list uses, where the row is gone.
const (
	projectStatusActive   = "active"
	projectStatusArchived = "archived"
)

// List returns the server's projects in one bucket: the active ones by default,
// or the archived ones with ?status=archived. Unlike the sync feed, which a
// client sees only for its own projects, this is every project on the server.
func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = projectStatusActive
	}
	if status != projectStatusActive && status != projectStatusArchived {
		httpx.WriteError(w, http.StatusBadRequest, "status must be 'active' or 'archived'")
		return
	}

	archived := status == projectStatusArchived
	projects, err := h.projects.List(r.Context(), archived)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not list projects")
		return
	}

	// List already drops archived rows when they are not wanted; this keeps the
	// archived bucket to archived rows only, since that call must include them.
	resp := make([]*entities.Project, 0, len(projects))
	for _, p := range projects {
		if p.IsArchived == archived {
			resp = append(resp, p)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// ── Update ──────────────────────────────────────────────────────────

// Update applies an edit to a project the way the sync path does: the row and
// its change-feed entry are written in one transaction, so the project's members
// pull the edit exactly as they would one their own client pushed. Writing the
// row alone would change what the admin sees and nobody else.
//
// The body is the whole project, as the client holds it, rather than a set of
// optional fields: the edit screen is seeded with the project and saves it back,
// and a full object cannot silently drop the members or the archived flag that a
// partial one forgot to mention.
func (h *ProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httpx.WriteError(w, http.StatusBadRequest, "project id is required")
		return
	}

	existing, err := h.projects.GetByID(r.Context(), entities.ProjectID(id))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	if existing == nil {
		httpx.WriteError(w, http.StatusNotFound, "project not found")
		return
	}

	var project entities.Project
	if err := httpx.DecodeJSON(w, r, &project); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// The id and the creation time are not the client's to change; they come from
	// the row. The version the body carries is what the compare-and-swap runs
	// against, and omitting it is last-write-wins, exactly as in a push.
	project.ID = existing.ID
	project.CreatedAt = existing.CreatedAt

	// The change feed carries the payload the way a client sends it: as a JSON
	// *string*, not a nested object. The client's DTO declares `payload` as a
	// String and leaves ignoreUnknownKeys off, so an object here would fail the
	// decode of the whole page on every client rather than be skipped.
	encoded, err := json.Marshal(&project)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not encode project")
		return
	}
	payload, err := json.Marshal(string(encoded))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not encode project")
		return
	}

	actorID, _ := auth.UserFrom(r.Context())
	result := h.engine.ApplyAdminProject(r.Context(), actorID, entities.PushChange{
		Entity:    entities.OutboxEntityProject,
		EntityID:  id,
		Operation: entities.OutboxOperationUpsert,
		Payload:   payload,
		Version:   project.Version,
	})

	switch result.Status {
	case sync.StatusAccepted:
	case sync.StatusConflict:
		httpx.WriteError(w, http.StatusConflict, "the project changed since it was read")
		return
	case sync.StatusRejected:
		httpx.WriteError(w, http.StatusBadRequest, result.Message)
		return
	default:
		httpx.WriteError(w, http.StatusInternalServerError, "could not update project")
		return
	}

	// Re-read rather than echo the request: the row now carries the server's
	// version and updated_at, and those are what the client needs to save again.
	stored, err := h.projects.GetByID(r.Context(), entities.ProjectID(id))
	if err != nil || stored == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not reload project")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, stored)
}
