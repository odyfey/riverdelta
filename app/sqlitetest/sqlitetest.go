// Package sqlitetest has the SQLite helpers shared by tests: River
// databases in temporary files, no external service needed.
package sqlitetest

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// Open opens the file at path, as River apps under test would.
func Open(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	// one connection, as River recommends for SQLite
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// NewDatabase creates a SQLite file migrated up to targetVersion (0 for all
// migrations, -1 for none) and returns its path.
func NewDatabase(t *testing.T, targetVersion int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "river.sqlite3")
	db := Open(t, path)
	// creates the file even when no migration runs
	require.NoError(t, db.PingContext(t.Context()))
	if targetVersion < 0 {
		return path
	}
	migrator, err := rivermigrate.New(riversqlite.New(db), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(t.Context(), rivermigrate.DirectionUp, &rivermigrate.MigrateOpts{TargetVersion: targetVersion})
	require.NoError(t, err)
	return path
}

// URL is the config url of the file at path.
func URL(path string) string {
	return "sqlite:" + path
}
