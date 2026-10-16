package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/admin"
	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/config"
	"github.com/amirashouri/tasktracker-server/internal/db"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
	"github.com/amirashouri/tasktracker-server/internal/middleware"
	"github.com/amirashouri/tasktracker-server/internal/ratelimit"
	"github.com/amirashouri/tasktracker-server/internal/sync"
)

// readyzTimeout bounds the readiness ping, so a hung database cannot make the
// probe hang with it.
const readyzTimeout = 2 * time.Second

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "user-create":
			createUserCmd(os.Args[2:])
			return
		case "serve":
			if len(os.Args) > 2 {
				os.Args = append(os.Args[:1], os.Args[2:]...)
			}
		}
	}
	runServer()
}

func runServer() {
	cfg, err := config.Load()
	if err != nil {
		fatal("invalid configuration", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	slog.SetDefault(logger)

	// Initialize database
	database, err := db.New(cfg.DatabaseURL)
	if err != nil {
		fatal("failed to connect to database", err)
	}
	defer database.Close()

	// Run migrations
	if err := db.Migrate(database); err != nil {
		fatal("failed to run migrations", err)
	}

	// Initialize JWT manager
	jwtManager := auth.NewJWTManager(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)

	// Initialize repositories
	userRepo := entities.NewUserRepository(database.DB)

	// Initialize sync engine
	syncEngine := sync.NewEngine(database.DB)

	// Initialize auth handler with token store
	tokenStore := auth.NewTokenStore(database.DB)
	limiter := ratelimit.New(cfg.SignInMaxAttempts, cfg.SignInWindow)
	authHandler := auth.NewHandler(jwtManager, userRepo, tokenStore, limiter)

	// Initialize admin handler
	adminHandler := admin.NewHandler(userRepo, tokenStore)

	// Initialize sync handler
	syncHandler := sync.NewHandler(syncEngine, jwtManager)

	// Setup HTTP handlers
	mux := mount(authHandler, adminHandler, syncHandler, jwtManager, database.DB)

	// Apply middleware chain
	handler := middleware.Chain(
		mux,
		middleware.SecurityHeaders(),
		middleware.RequestID(),
		middleware.Logging(logger),
		middleware.CORS(cfg.CORSOrigins),
		middleware.Recover(logger),
	)

	// Retain the change feed for the configured window. Pruning is what makes a
	// client's cursor able to go stale, which Pull reports as ResyncRequired.
	pruneCtx, stopPruning := context.WithCancel(context.Background())
	defer stopPruning()
	if cfg.SyncLogRetention > 0 {
		go pruneChangeFeed(pruneCtx, syncEngine, cfg.SyncLogRetention)
	}

	server := &http.Server{
		Addr:         ":" + cfg.ServerPort,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		logger.Info("server listening", "port", cfg.ServerPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal("server failed", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		fatal("server forced to shutdown", err)
	}

	logger.Info("server exited")
}

// fatal reports a startup failure and exits. slog has no Fatal, and a failure
// to start must not be swallowed.
func fatal(message string, err error) {
	slog.Error(message, "error", err)
	os.Exit(1)
}

// mount registers every route and returns the multiplexer. It is separate from
// runServer so a test can ask whether a path the client actually calls resolves:
// a route table is exactly the kind of thing that looks correct and answers 404.
func mount(
	authHandler *auth.Handler,
	adminHandler *admin.Handler,
	syncHandler *sync.Handler,
	jwtManager *auth.JWTManager,
	database *sql.DB,
) *http.ServeMux {
	mux := http.NewServeMux()

	// Health checks
	mux.HandleFunc("/healthz", healthz())
	mux.HandleFunc("/readyz", readyz(database))

	// Auth routes
	noStore := middleware.NoStore()
	mux.Handle("POST /v1/auth/login", noStore(http.HandlerFunc(authHandler.SignIn)))
	mux.Handle("POST /v1/auth/refresh", noStore(http.HandlerFunc(authHandler.Refresh)))
	mux.Handle("POST /v1/auth/signout", noStore(http.HandlerFunc(authHandler.SignOut)))

	// Sync routes (protected)
	mux.Handle("POST /v1/sync/push", middleware.Auth(jwtManager)(http.HandlerFunc(syncHandler.Push)))

	// A scope is not an entity type. The `workItems` stream also carries project
	// changes, because renaming a project has to reach a client that never asked
	// for projects — so the client names its scopes in the plural, and these are
	// the paths it actually calls.
	pullProjects := middleware.Auth(jwtManager)(http.HandlerFunc(syncHandler.PullProject))
	pullWorkItems := middleware.Auth(jwtManager)(http.HandlerFunc(syncHandler.PullWorkItem))
	mux.Handle("GET /v1/sync/changes/projects", pullProjects)
	mux.Handle("GET /v1/sync/changes/workItems", pullWorkItems)

	// The singular spellings this server shipped first, kept as aliases so a
	// build already pointing at them does not start seeing 404s.
	mux.Handle("GET /v1/sync/changes/project", pullProjects)
	mux.Handle("GET /v1/sync/changes/workItem", pullWorkItems)

	// Admin routes (admin only)
	adminMW := func(handler http.HandlerFunc) http.Handler {
		return middleware.Chain(handler, middleware.Auth(jwtManager), middleware.AdminOnly())
	}
	mux.Handle("POST /v1/admin/users", adminMW(http.HandlerFunc(adminHandler.Create)))
	mux.Handle("GET /v1/admin/users", adminMW(http.HandlerFunc(adminHandler.List)))
	mux.Handle("PATCH /v1/admin/users/{id}", adminMW(http.HandlerFunc(adminHandler.Update)))
	mux.Handle("DELETE /v1/admin/users/{id}", adminMW(http.HandlerFunc(adminHandler.Delete)))

	// Directory search (any authenticated user)
	mux.Handle("GET /v1/users/search", middleware.Auth(jwtManager)(http.HandlerFunc(adminHandler.Search)))

	return mux
}

// healthz is the liveness probe: the process is up. It deliberately does not
// touch the database, so a database outage does not get the process restarted.
func healthz() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
}

// readyz is the readiness probe: the process can serve, which means the
// database answers. A failure returns 503 so a load balancer stops sending
// traffic without killing the process.
func readyz(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyzTimeout)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			httpx.WriteError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
}

// pruneChangeFeed deletes change-feed rows older than retention once an hour
// until ctx is cancelled. The interval is deliberately not configurable: the
// window is what an operator cares about, and an hourly sweep bounds the
// growth between restarts.
func pruneChangeFeed(ctx context.Context, engine *sync.Engine, retention time.Duration) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-retention)
			pruned, err := engine.Prune(ctx, cutoff)
			if err != nil {
				slog.Error("prune change feed", "error", err)
				continue
			}
			if pruned > 0 {
				slog.Info("pruned change feed", "rows", pruned, "older_than", cutoff.Format(time.RFC3339))
			}
		}
	}
}

// createUserCmd is the one-shot bootstrap command for creating the first user
// (or any user) without an HTTP endpoint. It connects directly to the database
// named by DATABASE_URL.
func createUserCmd(args []string) {
	fs := flag.NewFlagSet("user-create", flag.ExitOnError)
	email := fs.String("email", "", "user email (required)")
	password := fs.String("password", "", "user password (required)")
	displayName := fs.String("display-name", "", "display name")
	position := fs.String("position", "", "job position")
	isAdmin := fs.Bool("admin", false, "create as admin")
	fs.Parse(args)

	*email = strings.TrimSpace(*email)
	if *email == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "email and password are required")
		fs.Usage()
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		cliFatal("invalid configuration: %v", err)
	}

	database, err := db.New(cfg.DatabaseURL)
	if err != nil {
		cliFatal("failed to connect to database: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		cliFatal("failed to run migrations: %v", err)
	}

	userRepo := entities.NewUserRepository(database.DB)

	if existing, _ := userRepo.GetByEmail(context.Background(), *email); existing != nil {
		cliFatal("email %s is already registered", *email)
	}

	role := entities.RoleMember
	if *isAdmin {
		role = entities.RoleAdmin
	}

	user := &entities.User{
		Email:        *email,
		PasswordHash: *password,
		DisplayName:  *displayName,
		Position:     *position,
		Role:         role,
	}

	if err := userRepo.Create(context.Background(), user); err != nil {
		cliFatal("failed to create user: %v", err)
	}

	fmt.Printf("Created user %s (id=%s, role=%s)\n", user.Email, user.ID, user.Role)
}

// cliFatal reports a subcommand failure and exits. A subcommand is a
// command-line tool, so its errors are plain text on stderr rather than
// structured log lines.
func cliFatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
