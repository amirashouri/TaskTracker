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

// Get returns one project, member or not. It is the read half of the admin edit
// screen and returns the identical shape List and Update do, because the client
// decodes all three into one DTO: a seed that carried a different key set would
// build a screen whose save Update then refuses.
func (h *ProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httpx.WriteError(w, http.StatusBadRequest, "project id is required")
		return
	}

	project, err := h.projects.GetByID(r.Context(), entities.ProjectID(id))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	if project == nil {
		httpx.WriteError(w, http.StatusNotFound, "project not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, project)
}

// ── Create ──────────────────────────────────────────────────────────

// createProjectRequest is what the create screen sends: the fields its form
// collects, plus the id it minted. The id is the client's, as it is on the sync
// path, which is what lets the caller name the row a lost response left behind
// instead of sending the same project again under a second id.
//
// `isArchived`, `nextItemNumber` and the stamps are not in the body: a project
// being created has no archived state and no items, so a client has nothing to
// say about them, and the create's own default is the only correct one.
type createProjectRequest struct {
	ID        string   `json:"id"`
	KeyPrefix string   `json:"keyPrefix"`
	Name      string   `json:"name"`
	Details   string   `json:"details"`
	MemberIDs []string `json:"memberIds"`
}

// Create writes a project and its change-feed entry in one transaction, so the
// members the body names pull it exactly as they would a project one of their
// own clients pushed. It is that push, authorized by the admin route rather than
// by membership: an admin creating a project for other people is not on its
// member list, which is the one check `ApplyAdminProject` skips for the same
// reason Update uses it.
func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(req.Name)
	keyPrefix := strings.TrimSpace(req.KeyPrefix)
	if name == "" || keyPrefix == "" {
		httpx.WriteError(w, http.StatusBadRequest, "name and keyPrefix are required")
		return
	}

	members := make([]entities.UserID, 0, len(req.MemberIDs))
	for _, id := range req.MemberIDs {
		if id = strings.TrimSpace(id); id != "" {
			members = append(members, entities.UserID(id))
		}
	}

	project := &entities.Project{
		ID:        entities.ProjectID(strings.TrimSpace(req.ID)),
		KeyPrefix: keyPrefix,
		Name:      name,
		Details:   req.Details,
		MemberIDs: members,
		// The first key a new project issues is `-1`, which is the column's
		// default as well -- but an insert names the column, so a zero here would
		// start the project at `-0` and never consult that default.
		NextItemNumber: 1,
	}
	if project.ID == "" {
		project.ID = entities.NewProjectID()
	}

	// An id the server already holds is a request it has already applied, not a
	// replace. Overwriting would also write this body's `nextItemNumber` over a
	// row that has issued keys since, handing a number out twice.
	existing, err := h.projects.GetByID(r.Context(), project.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	if existing != nil {
		httpx.WriteError(w, http.StatusConflict, "project already exists")
		return
	}

	// The change feed carries the payload the way a client sends it: as a JSON
	// *string*, not a nested object. Update spells out why -- the client's DTO
	// declares `payload` as a String, so an object here would fail the decode of
	// the whole page on every client rather than be skipped.
	//
	// Version travels on the envelope (PushChange.Version → RemoteChange.Version),
	// not inside the project payload: the client's Project DTO does not declare it,
	// so including it breaks decoding on every pull. Strip it before marshaling.
	feed := *project
	feed.Version = ""
	encoded, err := json.Marshal(&feed)
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
		EntityID:  string(project.ID),
		Operation: entities.OutboxOperationUpsert,
		Payload:   payload,
	})

	switch result.Status {
	case sync.StatusAccepted:
	case sync.StatusRejected:
		httpx.WriteError(w, http.StatusBadRequest, result.Message)
		return
	default:
		httpx.WriteError(w, http.StatusInternalServerError, "could not create project")
		return
	}

	// Re-read rather than echo the request: the row now carries the stamps and
	// the version the client holds before its first edit.
	stored, err := h.projects.GetByID(r.Context(), project.ID)
	if err != nil || stored == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not reload project")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, stored)
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
	//
	// Version travels on the envelope (PushChange.Version → RemoteChange.Version),
	// not inside the project payload: the client's Project DTO does not declare it,
	// so including it breaks decoding on every pull. Strip it before marshaling.
	feed := project
	feed.Version = ""
	encoded, err := json.Marshal(&feed)
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
