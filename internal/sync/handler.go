package sync

import (
	"net/http"
	"strconv"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
)

type Handler struct {
	engine *Engine
	jwt    *auth.JWTManager
}

func NewHandler(engine *Engine, jwt *auth.JWTManager) *Handler {
	return &Handler{engine: engine, jwt: jwt}
}

type pushRequest struct {
	Changes []entities.PushChange `json:"changes"`
}

type pushResponse struct {
	Results []entities.PushResult `json:"results"`
}

func (h *Handler) Push(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserFrom(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req pushRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Changes) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "no changes to push")
		return
	}

	results, err := h.engine.Push(r.Context(), userID, req.Changes)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "sync failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pushResponse{Results: results})
}

func (h *Handler) PullProject(w http.ResponseWriter, r *http.Request) {
	h.pull(w, r, ScopeProjects)
}

func (h *Handler) PullWorkItem(w http.ResponseWriter, r *http.Request) {
	h.pull(w, r, ScopeWorkItems)
}

func (h *Handler) PullAttachment(w http.ResponseWriter, r *http.Request) {
	h.pull(w, r, ScopeAttachments)
}

func (h *Handler) pull(w http.ResponseWriter, r *http.Request, scope Scope) {
	userID, ok := auth.UserFrom(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	query := r.URL.Query()

	limit := 0
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = parsed
	}

	page, err := h.engine.Pull(r.Context(), userID, scope, query.Get("cursor"), limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "sync failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}
