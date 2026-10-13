package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
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
		log.Fatalf("Invalid configuration: %v", err)
	}

	// Initialize database
	database, err := db.New(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := db.Migrate(database); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
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
	adminHandler := admin.NewHandler(userRepo)

	// Setup HTTP handlers
	mux := http.NewServeMux()

	// Health checks
	mux.HandleFunc("/healthz", healthz())
	mux.HandleFunc("/readyz", readyz(database.DB))

	// Auth routes
	noStore := middleware.NoStore()
	mux.Handle("POST /v1/auth/login", noStore(http.HandlerFunc(authHandler.SignIn)))
	mux.Handle("POST /v1/auth/refresh", noStore(http.HandlerFunc(authHandler.Refresh)))
	mux.Handle("POST /v1/auth/signout", noStore(http.HandlerFunc(authHandler.SignOut)))

	// Sync routes (protected)
	syncHandler := sync.NewHandler(syncEngine, jwtManager)
	mux.Handle("POST /v1/sync/push", middleware.Auth(jwtManager)(http.HandlerFunc(syncHandler.Push)))
	mux.Handle("GET /v1/sync/changes/project", middleware.Auth(jwtManager)(http.HandlerFunc(syncHandler.PullProject)))
	mux.Handle("GET /v1/sync/changes/workItem", middleware.Auth(jwtManager)(http.HandlerFunc(syncHandler.PullWorkItem)))

	// Admin routes (admin only)
	adminMW := func(handler http.HandlerFunc) http.Handler {
		return middleware.Chain(handler, middleware.Auth(jwtManager), middleware.AdminOnly())
	}
	mux.Handle("POST /v1/admin/users", adminMW(http.HandlerFunc(adminHandler.Create)))
	mux.Handle("GET /v1/admin/users", adminMW(http.HandlerFunc(adminHandler.List)))
	mux.Handle("PATCH /v1/admin/users/{id}", adminMW(http.HandlerFunc(adminHandler.Update)))
	mux.Handle("DELETE /v1/admin/users/{id}", adminMW(http.HandlerFunc(adminHandler.Deactivate)))

	// Directory search (any authenticated user)
	mux.Handle("GET /v1/users/search", middleware.Auth(jwtManager)(http.HandlerFunc(adminHandler.Search)))

	// Apply middleware chain
	handler := middleware.Chain(
		mux,
		middleware.SecurityHeaders(),
		middleware.Logging,
		middleware.CORS(cfg.CORSOrigins),
		middleware.Recover,
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
		log.Printf("Server starting on port %s", cfg.ServerPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
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
				log.Printf("prune change feed: %v", err)
				continue
			}
			if pruned > 0 {
				log.Printf("pruned %d change feed rows older than %s", pruned, cutoff.Format(time.RFC3339))
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
		log.Fatalf("Invalid configuration: %v", err)
	}

	database, err := db.New(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	userRepo := entities.NewUserRepository(database.DB)

	if existing, _ := userRepo.GetByEmail(context.Background(), *email); existing != nil {
		log.Fatalf("email %s is already registered", *email)
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
		log.Fatalf("Failed to create user: %v", err)
	}

	fmt.Printf("Created user %s (id=%s, role=%s)\n", user.Email, user.ID, user.Role)
}
