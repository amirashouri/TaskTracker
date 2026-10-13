package db

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationsDir = "migrations"

const (
	suffixPostgres = ".postgres.sql"
	suffixSQLite   = ".sqlite.sql"
)

// migration is one version's SQL for the driver being migrated.
type migration struct {
	version string
	sql     string
}

// Migrate applies every migration the database has not recorded yet, in version
// order, each version in its own transaction. It is safe to run on every start,
// and it records versions so an existing database upgrades in place.
func Migrate(database *DB) error {
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at BIGINT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(database)
	if err != nil {
		return err
	}

	migrations, err := loadMigrations(database.Driver)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(database, m); err != nil {
			return fmt.Errorf("migration %s: %w", m.version, err)
		}
	}
	return nil
}

func appliedVersions(database *DB) (map[string]bool, error) {
	rows, err := database.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

func applyMigration(database *DB, m migration) error {
	statements, err := splitStatements(m.sql)
	if err != nil {
		return err
	}

	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("statement %d: %w", i+1, err)
		}
	}

	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES ($1, $2)`,
		m.version, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// loadMigrations returns the versions for one driver, oldest first. Versions
// must be unique: two files may not declare the same number.
func loadMigrations(driver string) ([]migration, error) {
	suffix, err := migrationSuffix(driver)
	if err != nil {
		return nil, err
	}

	paths, err := fs.Glob(migrationFiles, migrationsDir+"/*"+suffix)
	if err != nil {
		return nil, err
	}

	migrations := make([]migration, 0, len(paths))
	for _, path := range paths {
		version, err := versionFromName(filepath.Base(path))
		if err != nil {
			return nil, err
		}
		body, err := migrationFiles.ReadFile(path)
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration{version: version, sql: string(body)})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	for i := 1; i < len(migrations); i++ {
		if migrations[i].version == migrations[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %s", migrations[i].version)
		}
	}
	return migrations, nil
}

func migrationSuffix(driver string) (string, error) {
	switch driver {
	case DriverPostgres:
		return suffixPostgres, nil
	case DriverSQLite:
		return suffixSQLite, nil
	default:
		return "", fmt.Errorf("unsupported database driver %q", driver)
	}
}

// versionFromName reads the version from a file named NNNN_name.<driver>.sql.
func versionFromName(name string) (string, error) {
	version, _, found := strings.Cut(name, "_")
	if !found {
		return "", fmt.Errorf("migration %s: expected NNNN_name.<driver>.sql", name)
	}
	if _, err := strconv.Atoi(version); err != nil {
		return "", fmt.Errorf("migration %s: version %q is not a number", name, version)
	}
	return version, nil
}

// splitStatements cuts a migration file into statements on top-level semicolons.
// Semicolons inside single-quoted strings and line comments do not split, so a
// value may contain one.
func splitStatements(sql string) ([]string, error) {
	var (
		statements []string
		current    strings.Builder
		inString   bool
		inComment  bool
	)

	for i := 0; i < len(sql); i++ {
		c := sql[i]

		if inComment {
			if c == '\n' {
				inComment = false
				current.WriteByte(c)
			}
			continue
		}

		if inString {
			current.WriteByte(c)
			if c == '\'' {
				// A doubled quote escapes a quote inside the string.
				if i+1 < len(sql) && sql[i+1] == '\'' {
					current.WriteByte(sql[i+1])
					i++
					continue
				}
				inString = false
			}
			continue
		}

		switch {
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			inComment = true
			i++
		case c == '\'':
			inString = true
			current.WriteByte(c)
		case c == ';':
			if statement := strings.TrimSpace(current.String()); statement != "" {
				statements = append(statements, statement)
			}
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}

	if inString {
		return nil, errors.New("unterminated string literal")
	}
	if statement := strings.TrimSpace(current.String()); statement != "" {
		statements = append(statements, statement)
	}
	return statements, nil
}
