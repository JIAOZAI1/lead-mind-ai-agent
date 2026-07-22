package tenantdb

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
)

const migrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	filename   VARCHAR(255) PRIMARY KEY,
	applied_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

// ApplyMigrations applies every .sql file in fsys (a per-tenant database
// connection) that isn't already recorded in schema_migrations, in
// filename order. This is a deliberately minimal, home-grown runner (no
// golang-migrate or similar dependency) since the migration surface here
// is a handful of files — see PROJECT.md §6.4.
func ApplyMigrations(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	if _, err := db.ExecContext(ctx, migrationsTable); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var filenames []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		filenames = append(filenames, e.Name())
	}
	sort.Strings(filenames)

	for _, filename := range filenames {
		var already string
		err := db.QueryRowContext(ctx, "SELECT filename FROM schema_migrations WHERE filename = ?", filename).Scan(&already)
		if err == nil {
			continue // already applied
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check migration status for %s: %w", filename, err)
		}

		contents, err := fs.ReadFile(fsys, filename)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", filename, err)
		}

		if _, err := db.ExecContext(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply migration %s: %w", filename, err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO schema_migrations (filename) VALUES (?)", filename); err != nil {
			return fmt.Errorf("record migration %s: %w", filename, err)
		}
	}

	return nil
}
