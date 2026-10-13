package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/config"
	"github.com/amirashouri/tasktracker-server/internal/db"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/middleware"
	"github.com/amirashouri/tasktracker-server/internal/sync"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/mattn/go-sqlite3"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
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
	if err := db.Migrate(database.DB); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	// Initialize JWT manager
	jwtManager := auth.NewJWTManager(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)

	// Initialize repositories
	projectRepo := entities.NewProjectRepository(database)
	workItemRepo := entities.NewWorkItemRepository(database)
	sprintRepo := entities.NewSprintRepository(database)
	commentRepo := entities.NewCommentRepository(database)
	userRepo := entities.NewUserRepository(database)

	// Initialize sync engine
	syncEngine := sync.NewEngine(
		database,
		projectRepo,
		workItemRepo,
		sprintRepo,
		commentRepo,
		userRepo,
	)

	// Setup HTTP handlers
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Auth routes
	authHandler := auth.NewHandler(jwtManager, userRepo)
	mux.HandleFunc("POST /v1/auth/signin", authHandler.SignIn)
	mux.HandleFunc("POST /v1/auth/refresh", authHandler.Refresh)

	// Sync routes (protected)
	syncHandler := sync.NewHandler(syncEngine, jwtManager)
	mux.Handle("POST /v1/sync/push", middleware.Auth(jwtManager)(http.HandlerFunc(syncHandler.Push)))
	mux.Handle("GET /v1/sync/changes/project", middleware.Auth(jwtManager)(http.HandlerFunc(syncHandler.PullProject)))
	mux.Handle("GET /v1/sync/changes/workItem", middleware.Auth(jwtManager)(http.HandlerFunc(syncHandler.PullWorkItem)))

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
