package db

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/mattn/go-sqlite3"
)

type DB struct {
	*sql.DB
	Driver string // "postgres" or "sqlite"
}

func New(databaseURL string) (*DB, error) {
	var driverName string
	var dsn string

	if strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://") {
		driverName = "pgx"
		dsn = databaseURL
	} else if strings.HasPrefix(databaseURL, "sqlite://") {
		driverName = "sqlite3"
		dsn = strings.TrimPrefix(databaseURL, "sqlite://")
	} else {
		return nil, fmt.Errorf("unsupported database URL: %s", databaseURL)
	}

	var db *sql.DB
	var err error

	if driverName == "pgx" {
		db, err = sql.Open("pgx", dsn)
	} else {
		// Register sqlite3 driver with custom options
		sql.Register("sqlite3_custom", &sqlite3.SQLiteDriver{
			ConnectHook: func(conn *sqlite3.SQLiteConn) error {
				// Enable foreign keys and WAL mode
				_, err := conn.Exec("PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL;", nil)
				return err
			},
		})
		db, err = sql.Open("sqlite3_custom", dsn+"?_fk=1&_journal_mode=WAL")
	}

	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)

	return &DB{
		DB:     db,
		Driver: driverName,
	}, nil
}

func Migrate(db *sql.DB) error {
	migrations := []string{
		// Users table
		`CREATE TABLE IF NOT EXISTS users (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			email TEXT UNIQUE NOT NULL,
			display_name TEXT,
			password_hash TEXT NOT NULL,
			position TEXT,
			avatar_id TEXT,
			status TEXT,
			last_seen TIMESTAMP,
			created_at TIMESTAMP DEFAULT NOW()
		)`,
		// Projects table
		`CREATE TABLE IF NOT EXISTS projects (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			key_prefix TEXT NOT NULL,
			name TEXT NOT NULL,
			details TEXT DEFAULT '',
			is_archived BOOLEAN DEFAULT FALSE,
			next_item_number INT DEFAULT 1,
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW()
		)`,
		// Project members
		`CREATE TABLE IF NOT EXISTS project_members (
			project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
			user_id UUID REFERENCES users(id) ON DELETE CASCADE,
			role TEXT DEFAULT 'member',
			PRIMARY KEY (project_id, user_id)
		)`,
		// Work items
		`CREATE TABLE IF NOT EXISTS work_items (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
			key TEXT NOT NULL,
			title TEXT NOT NULL,
			details TEXT DEFAULT '',
			status TEXT NOT NULL CHECK (status IN ('backlog','todo','in_progress','in_review','done','closed')),
			priority TEXT NOT NULL CHECK (priority IN ('lowest','low','medium','high','critical')),
			assignee_id UUID REFERENCES users(id) ON DELETE SET NULL,
			due_date TIMESTAMP,
			recurrence_type TEXT,
			recurrence_day INT,
			recurrence_until TIMESTAMP,
			story_points INT,
			blocked_by_ids UUID[],
			sprint_id UUID REFERENCES sprints(id) ON DELETE SET NULL,
			rank INT DEFAULT 0,
			resolved_at TIMESTAMP,
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW()
		)`,
		// Sprints
		`CREATE TABLE IF NOT EXISTS sprints (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			goal_story_points INT DEFAULT 0,
			goal TEXT,
			start_date TIMESTAMP NOT NULL,
			end_date TIMESTAMP NOT NULL,
			owner_id UUID REFERENCES users(id) ON DELETE SET NULL,
			is_finished BOOLEAN DEFAULT FALSE,
			finished_at TIMESTAMP,
			is_archived BOOLEAN DEFAULT FALSE,
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW()
		)`,
		// Comments
		`CREATE TABLE IF NOT EXISTS comments (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			work_item_id UUID REFERENCES work_items(id) ON DELETE CASCADE,
			author_id UUID REFERENCES users(id) ON DELETE CASCADE,
			body TEXT NOT NULL,
			parent_id UUID REFERENCES comments(id) ON DELETE CASCADE,
			created_at TIMESTAMP DEFAULT NOW()
		)`,
		// Sync log
		`CREATE TABLE IF NOT EXISTS sync_log (
			id BIGSERIAL PRIMARY KEY,
			entity TEXT NOT NULL,
			entity_id UUID NOT NULL,
			operation TEXT NOT NULL CHECK (operation IN ('upsert','delete')),
			payload JSONB NOT NULL,
			version TEXT NOT NULL,
			user_id UUID REFERENCES users(id) ON DELETE SET NULL,
			created_at TIMESTAMP DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_log_cursor ON sync_log (entity, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_work_items_project ON work_items (project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_work_items_status ON work_items (status)`,
		`CREATE INDEX IF NOT EXISTS idx_work_items_sprint ON work_items (sprint_id)`,
		`CREATE INDEX IF NOT EXISTS idx_comments_work_item ON comments (work_item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sprints_project ON sprints (project_id)`,
	}

	// Adjust for SQLite
	if isSQLite(db) {
		migrations = adjustForSQLite(migrations)
	}

	for i, migration := range migrations {
		if _, err := db.Exec(migration); err != nil {
			return fmt.Errorf("migration %d failed: %w", i, err)
		}
	}
	return nil
}

func isSQLite(db *sql.DB) bool {
	var driver string
	err := db.QueryRow("SELECT 'sqlite'").Scan(&driver)
	return err == nil || strings.Contains(fmt.Sprintf("%T", db.Driver()), "sqlite")
}

func adjustForSQLite(migrations []string) []string {
	result := make([]string, len(migrations))
	for i, m := range migrations {
		m = strings.ReplaceAll(m, "UUID PRIMARY KEY DEFAULT gen_random_uuid()", "TEXT PRIMARY KEY")
		m = strings.ReplaceAll(m, "UUID REFERENCES", "TEXT REFERENCES")
		m = strings.ReplaceAll(m, "BIGSERIAL PRIMARY KEY", "INTEGER PRIMARY KEY AUTOINCREMENT")
		m = strings.ReplaceAll(m, "JSONB", "TEXT")
		m = strings.ReplaceAll(m, "UUID[]", "TEXT") // SQLite doesn't have arrays
		m = strings.ReplaceAll(m, "DEFAULT NOW()", "DEFAULT CURRENT_TIMESTAMP")
		m = strings.ReplaceAll(m, "ON DELETE CASCADE", "")
		m = strings.ReplaceAll(m, "ON DELETE SET NULL", "")
		result[i] = m
	}
	return result
}

// For pgx driver
func init() {
	_ = stdlib.GetDefaultDriver()
}
