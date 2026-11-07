package instance

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/odyfey/riverdelta/app/config"
	"github.com/odyfey/riverdelta/app/pgtest"
	"github.com/odyfey/riverdelta/app/sqlitetest"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	pgtest.Main(m)
}

func openTestSet(t *testing.T, databases ...config.Database) *Set {
	t.Helper()
	set, err := Open(t.Context(), pgtest.Config(t, databases...), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(set.Close)
	return set
}

func TestOpenSharesPoolPerDatabase(t *testing.T) {
	pool := pgtest.NewPool(t)
	schemaA := pgtest.NewSchema(t, pool, 0)
	schemaB := pgtest.NewSchema(t, pool, 0)

	set := openTestSet(t, pgtest.Database(t,
		config.Instance{Name: "tenant-a", Schema: schemaA},
		config.Instance{Name: "tenant-b", Schema: schemaB, Label: "Tenant B", Color: "blue"},
	))

	require.Len(t, set.closers, 1)
	require.Len(t, set.Instances, 2)
	require.Equal(t, "/tenant-a", set.Instances[0].base())
	require.Equal(t, "tenant-a", set.Instances[0].Label)
	require.Equal(t, "Tenant B", set.Instances[1].Label)
	require.Equal(t, "blue", set.Instances[1].Color)
}

func TestInstanceStatus(t *testing.T) {
	pool := pgtest.NewPool(t)
	migrated := pgtest.NewSchema(t, pool, 0)
	unmigrated := pgtest.NewSchema(t, pool, -1)
	outdated := pgtest.NewSchema(t, pool, 3)

	set := openTestSet(t, pgtest.Database(t,
		config.Instance{Name: "migrated", Schema: migrated},
		config.Instance{Name: "unmigrated", Schema: unmigrated},
		config.Instance{Name: "outdated", Schema: outdated},
	))
	require.Greater(t, latestVersion(set.Instances[0].migrator.AllVersions()), 3)

	tests := []struct {
		name        string
		inst        *Instance
		wantWarning string
	}{
		{name: "MigratedSchemaClean", inst: set.Instances[0], wantWarning: ""},
		{name: "UnmigratedSchemaWarns", inst: set.Instances[1], wantWarning: "river migrations at version 0, riverui expects"},
		{name: "OutdatedSchemaWarns", inst: set.Instances[2], wantWarning: "river migrations at version 3, riverui expects"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := tt.inst.status(t.Context())
			require.True(t, status.Healthy)
			if tt.wantWarning == "" {
				require.Empty(t, status.Warning)
				return
			}
			require.Contains(t, status.Warning, tt.wantWarning)
		})
	}
}

func TestOpenUnreachableDatabaseDoesNotBlockOthers(t *testing.T) {
	pool := pgtest.NewPool(t)
	schema := pgtest.NewSchema(t, pool, 0)

	set := openTestSet(t,
		pgtest.Database(t, config.Instance{Name: "up", Schema: schema}),
		config.Database{Name: "down", URL: pgtest.UnreachableURL, Instances: []config.Instance{{Name: "down"}}},
	)

	statuses := StatusAll(t.Context(), set.Instances)
	require.Equal(t, []Status{{Healthy: true}, {Healthy: false}}, statuses)
}

func sqliteDatabase(name, path string) config.Database {
	return config.Database{Name: name, URL: sqlitetest.URL(path), Instances: []config.Instance{{Name: name}}}
}

func TestSQLiteInstanceStatus(t *testing.T) {
	set := openTestSet(t,
		sqliteDatabase("migrated", sqlitetest.NewDatabase(t, 0)),
		sqliteDatabase("unmigrated", sqlitetest.NewDatabase(t, -1)),
		sqliteDatabase("outdated", sqlitetest.NewDatabase(t, 3)),
	)
	require.Len(t, set.closers, 3)
	require.Greater(t, latestVersion(set.Instances[0].migrator.AllVersions()), 3)

	statuses := StatusAll(t.Context(), set.Instances)
	require.Equal(t, Status{Healthy: true}, statuses[0])
	require.True(t, statuses[1].Healthy)
	require.Contains(t, statuses[1].Warning, "river migrations at version 0, riverui expects")
	require.True(t, statuses[2].Healthy)
	require.Contains(t, statuses[2].Warning, "river migrations at version 3, riverui expects")
}

func TestSQLiteMissingFileIsUnhealthyAndNotCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.sqlite3")

	set := openTestSet(t, sqliteDatabase("absent", path))

	require.Equal(t, []Status{{Healthy: false}}, StatusAll(t.Context(), set.Instances))
	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestOpenMixesPostgresAndSQLite(t *testing.T) {
	pool := pgtest.NewPool(t)
	schema := pgtest.NewSchema(t, pool, 0)

	set := openTestSet(t,
		pgtest.Database(t, config.Instance{Name: "pg", Schema: schema}),
		sqliteDatabase("lite", sqlitetest.NewDatabase(t, 0)),
	)

	require.Equal(t, []Status{{Healthy: true}, {Healthy: true}}, StatusAll(t.Context(), set.Instances))
}

func TestSQLiteDSN(t *testing.T) {
	tests := []struct {
		url     string
		want    string
		wantErr string
	}{
		{url: "sqlite:/data/app.sqlite3", want: "file:/data/app.sqlite3?_pragma=busy_timeout%285000%29&mode=rw"},
		{url: "sqlite:///data/app.sqlite3", want: "file:/data/app.sqlite3?_pragma=busy_timeout%285000%29&mode=rw"},
		{url: "sqlite://app.sqlite3", want: "file:app.sqlite3?_pragma=busy_timeout%285000%29&mode=rw"},
		{url: "file:/data/app.sqlite3", want: "file:/data/app.sqlite3?_pragma=busy_timeout%285000%29&mode=rw"},
		{url: "sqlite:app.sqlite3?mode=ro", want: "file:app.sqlite3?_pragma=busy_timeout%285000%29&mode=ro"},
		{url: "sqlite:/a.db?_pragma=busy_timeout(100)", want: "file:/a.db?_pragma=busy_timeout%28100%29&mode=rw"},
		{url: "sqlite:", wantErr: "sqlite url has no file path"},
		{url: "sqlite:/a.db?%zz", wantErr: "can't parse sqlite url query"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			dsn, err := sqliteDSN(tt.url)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, dsn)
		})
	}
}

func TestOpenInvalidURLReturnsError(t *testing.T) {
	cfg := config.Config{
		Databases: []config.Database{{Name: "bad", URL: "postgres://%zz", Instances: []config.Instance{{Name: "bad"}}}},
	}.WithDefaults()

	_, err := Open(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.ErrorContains(t, err, `failed to open database "bad": can't parse url`)
}

func TestLatestVersion(t *testing.T) {
	tests := []struct {
		name       string
		migrations []rivermigrate.Migration
		want       int
	}{
		{name: "Empty", migrations: nil, want: 0},
		{name: "Ordered", migrations: []rivermigrate.Migration{{Version: 1}, {Version: 2}}, want: 2},
		{name: "Unordered", migrations: []rivermigrate.Migration{{Version: 5}, {Version: 2}}, want: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, latestVersion(tt.migrations))
		})
	}
}
