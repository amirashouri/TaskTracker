package main

import (
	"context"
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
	"github.com/amirashouri/tasktracker-server/internal/middleware"
	"github.com/amirashouri/tasktracker-server/internal/sync"
)

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
	projectRepo := entities.NewProjectRepository(database.DB)
	workItemRepo := entities.NewWorkItemRepository(database.DB)
	sprintRepo := entities.NewSprintRepository(database.DB)
	commentRepo := entities.NewCommentRepository(database.DB)
	userRepo := entities.NewUserRepository(database.DB)

	// Initialize sync engine
	syncEngine := sync.NewEngine(
		database.DB,
		projectRepo,
		workItemRepo,
		sprintRepo,
		commentRepo,
		userRepo,
	)

	// Initialize auth handler with token store
	tokenStore := auth.NewTokenStore(database.DB)
	authHandler := auth.NewHandler(jwtManager, userRepo, tokenStore)

	// Initialize admin handler
	adminHandler := admin.NewHandler(userRepo)

	// Setup HTTP handlers
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Auth routes
	mux.HandleFunc("POST /v1/auth/login", authHandler.SignIn)
	mux.HandleFunc("POST /v1/auth/refresh", authHandler.Refresh)
	mux.HandleFunc("POST /v1/auth/signout", authHandler.SignOut)

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
		middleware.Logging,
		middleware.CORS(cfg.CORSOrigins),
		middleware.Recover,
	)

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
