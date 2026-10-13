// Package database is the SQLite database of the portal. The tables are defined by the migrations, the queries are in
// queries and compiled to the schema package by sqlc (make schema).
package database

import (
	"database/sql"
	"embed"
	"path/filepath"

	migrate "github.com/rubenv/sql-migrate"
	"github.com/valentin-kaiser/go-core/apperror"
	"github.com/valentin-kaiser/go-core/logging/log"
	_ "modernc.org/sqlite" // sqlite driver without cgo
)

//go:embed migrations
var migrations embed.FS

// Open opens portal.db in dir and applies the pending migrations.
func Open(dir string) (*sql.DB, error) {
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "portal.db")) +
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, apperror.NewError("failed to open the portal database").AddError(err)
	}
	db.SetMaxOpenConns(1)
	if err := Migrate(db); err != nil {
		_ = db.Close()
		return nil, apperror.Wrap(err)
	}
	return db, nil
}

// Migrate applies all pending migrations, it does nothing when the database is up to date.
func Migrate(db *sql.DB) error {
	src := &migrate.EmbedFileSystemMigrationSource{FileSystem: migrations, Root: "migrations"}
	n, err := migrate.Exec(db, "sqlite3", src, migrate.Up)
	if err != nil {
		return apperror.NewError("failed to run migrations").AddError(err)
	}
	if n > 0 {
		log.Info().Msgf("applied %d migration(s) to the portal database", n)
	}
	return nil
}
