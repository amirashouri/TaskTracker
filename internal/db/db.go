package db

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mattn/go-sqlite3"
)

const (
	driverPostgres = "pgx"
	driverSQLite   = "sqlite3"
	sqliteDriver   = "sqlite3_custom"
)

var registerSQLiteDriver sync.Once

type DB struct {
	*sql.DB
	Driver string // driverPostgres or driverSQLite
}

func New(databaseURL string) (*DB, error) {
	driverName, dsn, err := parseURL(databaseURL)
	if err != nil {
		return nil, err
	}

	var conn *sql.DB
	switch driverName {
	case driverPostgres:
		conn, err = sql.Open(driverPostgres, dsn)
	case driverSQLite:
		registerSQLiteDriver.Do(registerCustomSQLite)
		conn, err = sql.Open(sqliteDriver, dsn+"?_fk=1&_journal_mode=WAL&_busy_timeout=5000")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	configurePool(conn, driverName)

	return &DB{DB: conn, Driver: driverName}, nil
}

func parseURL(databaseURL string) (driverName, dsn string, err error) {
	switch {
	case strings.HasPrefix(databaseURL, "postgres://"), strings.HasPrefix(databaseURL, "postgresql://"):
		return driverPostgres, databaseURL, nil
	case strings.HasPrefix(databaseURL, "sqlite://"):
		return driverSQLite, strings.TrimPrefix(databaseURL, "sqlite://"), nil
	default:
		return "", "", fmt.Errorf("unsupported database URL: %s", databaseURL)
	}
}

func registerCustomSQLite() {
	sql.Register(sqliteDriver, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			_, err := conn.Exec("PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;", nil)
			return err
		},
	})
}

func configurePool(conn *sql.DB, driverName string) {
	// SQLite serialises writers, so a single connection avoids SQLITE_BUSY.
	if driverName == driverSQLite {
		conn.SetMaxOpenConns(1)
		conn.SetMaxIdleConns(1)
		return
	}
	conn.SetMaxOpenConns(25)
	conn.SetMaxIdleConns(5)
}

// statements is the Postgres schema. Time columns are epoch milliseconds
// (BIGINT); adjustForSQLite rewrites the Postgres-specific types for SQLite.
var statements = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		email TEXT UNIQUE NOT NULL,
		display_name TEXT,
		password_hash TEXT NOT NULL,
		position TEXT,
		avatar_id TEXT,
		status TEXT,
		last_seen BIGINT,
		created_at BIGINT
	)`,
	`CREATE TABLE IF NOT EXISTS projects (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		key_prefix TEXT NOT NULL,
		name TEXT NOT NULL,
		details TEXT DEFAULT '',
		is_archived BOOLEAN DEFAULT FALSE,
		next_item_number INT DEFAULT 1,
		created_at BIGINT,
		updated_at BIGINT,
		version TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS sprints (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		goal_story_points INT DEFAULT 0,
		goal TEXT,
		start_date BIGINT NOT NULL,
		end_date BIGINT NOT NULL,
		owner_id UUID REFERENCES users(id) ON DELETE SET NULL,
		is_finished BOOLEAN DEFAULT FALSE,
		finished_at BIGINT,
		is_archived BOOLEAN DEFAULT FALSE,
		created_at BIGINT,
		updated_at BIGINT,
		version TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS project_members (
		project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
		user_id UUID REFERENCES users(id) ON DELETE CASCADE,
		role TEXT DEFAULT 'member',
		PRIMARY KEY (project_id, user_id)
	)`,
	`CREATE TABLE IF NOT EXISTS work_items (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
		key TEXT NOT NULL,
		title TEXT NOT NULL,
		details TEXT DEFAULT '',
		status TEXT NOT NULL CHECK (status IN ('backlog','todo','in_progress','in_review','done','closed')),
		priority TEXT NOT NULL CHECK (priority IN ('lowest','low','medium','high','critical')),
		assignee_id UUID REFERENCES users(id) ON DELETE SET NULL,
		due_date BIGINT,
		recurrence JSONB,
		story_points INT,
		blocked_by_ids JSONB,
		sprint_id UUID REFERENCES sprints(id) ON DELETE SET NULL,
		rank INT DEFAULT 0,
		resolved_at BIGINT,
		created_at BIGINT,
		updated_at BIGINT,
		version TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS comments (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		work_item_id UUID REFERENCES work_items(id) ON DELETE CASCADE,
		author_id UUID REFERENCES users(id) ON DELETE CASCADE,
		body TEXT NOT NULL,
		parent_id UUID REFERENCES comments(id) ON DELETE CASCADE,
		created_at BIGINT,
		version TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS sync_log (
		id BIGSERIAL PRIMARY KEY,
		entity TEXT NOT NULL,
		entity_id UUID NOT NULL,
		operation TEXT NOT NULL CHECK (operation IN ('upsert','delete')),
		payload JSONB NOT NULL,
		version TEXT NOT NULL,
		user_id UUID REFERENCES users(id) ON DELETE SET NULL,
		created_at BIGINT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sync_log_cursor ON sync_log (entity, created_at, id)`,
	`CREATE INDEX IF NOT EXISTS idx_work_items_project ON work_items (project_id)`,
	`CREATE INDEX IF NOT EXISTS idx_work_items_status ON work_items (status)`,
	`CREATE INDEX IF NOT EXISTS idx_work_items_sprint ON work_items (sprint_id)`,
	`CREATE INDEX IF NOT EXISTS idx_comments_work_item ON comments (work_item_id)`,
	`CREATE INDEX IF NOT EXISTS idx_sprints_project ON sprints (project_id)`,
}

func Migrate(database *DB) error {
	for i, statement := range migrationsFor(database.Driver) {
		if _, err := database.Exec(statement); err != nil {
			return fmt.Errorf("migration %d failed: %w", i, err)
		}
	}
	return nil
}

func migrationsFor(driverName string) []string {
	if driverName == driverSQLite {
		return adjustForSQLite(statements)
	}
	return statements
}

// adjustForSQLite rewrites the Postgres-specific type names. Referential actions
// (ON DELETE ...) are left alone: SQLite supports them.
func adjustForSQLite(in []string) []string {
	out := make([]string, len(in))
	for i, statement := range in {
		statement = strings.ReplaceAll(statement, "UUID PRIMARY KEY DEFAULT gen_random_uuid()", "TEXT PRIMARY KEY")
		statement = strings.ReplaceAll(statement, "UUID REFERENCES", "TEXT REFERENCES")
		statement = strings.ReplaceAll(statement, "BIGSERIAL PRIMARY KEY", "INTEGER PRIMARY KEY AUTOINCREMENT")
		statement = strings.ReplaceAll(statement, "JSONB", "TEXT")
		out[i] = statement
	}
	return out
}
