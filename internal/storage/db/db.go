package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite database connection
type DB struct {
	*sql.DB
}

// New creates a new database connection and runs migrations
func New(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable foreign keys and WAL mode for better performance
	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL;"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("setting pragmas: %w", err)
	}

	database := &DB{DB: sqlDB}

	if err := database.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return database, nil
}
