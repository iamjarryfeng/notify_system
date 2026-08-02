package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/jmoiron/sqlx"
)

const migrationNamespace = "notification_service"

// migrationTable is the per-service migration tracking table. Each service uses
// its own table to avoid concurrent CREATE TABLE collisions on a shared database.
const migrationTable = "schema_migrations_notification_service"

// RunMigrations reads all .up.sql files from the given directory and executes them
// in sorted order against the database. All DDL uses IF NOT EXISTS, so re-runs are safe.
func RunMigrations(db *sqlx.DB, dir string) error {
	pattern := filepath.Join(dir, "*.up.sql")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob migrations in %s: %w", dir, err)
	}

	if len(files) == 0 {
		slog.Warn("no migration files found", "dir", dir)
		return nil
	}

	if err := ensureMigrationTable(db); err != nil {
		return err
	}

	sort.Strings(files)

	for _, f := range files {
		filename := filepath.Base(f)
		applied, err := migrationApplied(db, filename)
		if err != nil {
			return err
		}
		if applied {
			slog.Info("migration skipped", "file", filename)
			continue
		}

		content, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", filename, err)
		}

		tx, err := db.BeginTxx(context.Background(), nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", filename, err)
		}

		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("execute migration %s: %w", filename, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO %s (service_name, filename) VALUES ($1, $2)`, migrationTable), migrationNamespace, filename); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", filename, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", filename, err)
		}

		slog.Info("migration applied", "file", filename)
	}

	return nil
}

func ensureMigrationTable(db *sqlx.DB) error {
	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			service_name TEXT NOT NULL,
			filename TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (service_name, filename)
		)`, migrationTable))
	if err != nil {
		return fmt.Errorf("ensure %s table: %w", migrationTable, err)
	}
	return nil
}

func migrationApplied(db *sqlx.DB, filename string) (bool, error) {
	var applied bool
	err := db.QueryRow(fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE service_name = $1 AND filename = $2)`, migrationTable), migrationNamespace, filename).Scan(&applied)
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("check migration %s: %w", filename, err)
	}
	return applied, nil
}
