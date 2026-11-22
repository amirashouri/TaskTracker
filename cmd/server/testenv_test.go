package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/admin"
	"github.com/amirashouri/tasktracker-server/internal/agent"
	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/db"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/me"
	"github.com/amirashouri/tasktracker-server/internal/profile"
	"github.com/amirashouri/tasktracker-server/internal/ratelimit"
	"github.com/amirashouri/tasktracker-server/internal/sync"
)

// mountTestMux builds the route table the way the composition root does, against
// the caller's database. The limiter and JWT manager stay with the caller so a
// test can choose its own sign-in budget and session lifetime.
func mountTestMux(t *testing.T, database *db.DB, limiter *ratelimit.Limiter, jwtManager *auth.JWTManager) *http.ServeMux {
	t.Helper()

	users := entities.NewUserRepository(database.DB)
	tokens := auth.NewTokenStore(database.DB)

	return mount(
		auth.NewHandler(jwtManager, users, tokens, limiter, nil),
		admin.NewHandler(users, tokens),
		profile.NewHandler(users),
		me.NewHandler(users),
		admin.NewProjectHandler(
			entities.NewProjectRepository(database.DB),
			entities.NewWorkItemRepository(database.DB),
			sync.NewEngine(database.DB, ""),
		),
		sync.NewHandler(sync.NewEngine(database.DB, ""), jwtManager),
		agent.NewHandler(entities.NewAgentRepository(database), time.Minute),
		agent.NewAppHandler(
			entities.NewAgentRepository(database),
			entities.NewWorkItemRepository(database.DB),
			entities.NewProjectRepository(database.DB),
			entities.NewCommentRepository(database.DB),
			users,
			sync.NewEngine(database.DB, ""),
		),
		jwtManager,
		database.DB,
		sync.NewEngine(database.DB, ""),
		"",
		int64(25*1024*1024),
		30*time.Second,
	)
}
