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
	// DriverPostgres and DriverSQLite name the drivers this package supports.
	DriverPostgres = "pgx"
	DriverSQLite   = "sqlite3"

	sqliteDriver = "sqlite3_custom"
)

var registerSQLiteDriver sync.Once

// DB is a connection pool plus the driver it was opened with, so callers can
// branch on the dialect without probing the server.
type DB struct {
	*sql.DB
	Driver string // DriverPostgres or DriverSQLite
}

func New(databaseURL string) (*DB, error) {
	driverName, dsn, err := parseURL(databaseURL)
	if err != nil {
		return nil, err
	}

	var conn *sql.DB
	switch driverName {
	case DriverPostgres:
		conn, err = sql.Open(DriverPostgres, dsn)
	case DriverSQLite:
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
		return DriverPostgres, databaseURL, nil
	case strings.HasPrefix(databaseURL, "sqlite://"):
		return DriverSQLite, strings.TrimPrefix(databaseURL, "sqlite://"), nil
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
	if driverName == DriverSQLite {
		conn.SetMaxOpenConns(1)
		conn.SetMaxIdleConns(1)
		return
	}
	conn.SetMaxOpenConns(25)
	conn.SetMaxIdleConns(5)
}
