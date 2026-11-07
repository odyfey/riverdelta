// Package pgtest has the Postgres helpers shared by tests that need a real
// database. The database is a testcontainers Postgres started on first use,
// so these tests need Docker; a package using them calls Main from TestMain
// to stop the container.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/odyfey/riverdelta/app/config"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// UnreachableURL fails fast: nothing listens on port 1.
const UnreachableURL = "postgres://riverdelta@127.0.0.1:1/down?connect_timeout=1"

const image = "postgres:18-alpine"

// the container is shared by every test of the package binary
var (
	startOnce sync.Once
	container *postgres.PostgresContainer
	url       string
	startErr  error
)

// Main runs the package's tests and then stops the container if one was
// started; Ryuk removes it if the binary dies first.
func Main(m *testing.M) {
	code := m.Run()
	if container != nil {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "pgtest: failed to terminate postgres container: %v\n", err)
		}
	}
	os.Exit(code)
}

// URL points at the shared Postgres container, starting it on first use.
func URL(t *testing.T) string {
	t.Helper()
	startOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		container, startErr = postgres.Run(ctx, image,
			postgres.WithDatabase("riverdelta_test"),
			postgres.WithUsername("postgres"),
			postgres.WithPassword("postgres"),
			postgres.BasicWaitStrategies(),
		)
		if startErr != nil {
			return
		}
		url, startErr = container.ConnectionString(ctx, "sslmode=disable")
	})
	require.NoError(t, startErr, "failed to start %s with testcontainers (is Docker running?)", image)
	return url
}

func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), URL(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Ping(t.Context()))
	return pool
}

// NewSchema creates a uniquely named schema and migrates River into it up to
// targetVersion (0 for all migrations, -1 for none).
func NewSchema(t *testing.T, pool *pgxpool.Pool, targetVersion int) string {
	t.Helper()
	suffix := make([]byte, 6)
	_, err := rand.Read(suffix)
	require.NoError(t, err)
	schema := "rd_" + hex.EncodeToString(suffix)

	_, err = pool.Exec(t.Context(), "create schema "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, dropErr := pool.Exec(ctx, "drop schema "+schema+" cascade")
		require.NoError(t, dropErr)
	})

	if targetVersion < 0 {
		return schema
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Schema: schema})
	require.NoError(t, err)
	_, err = migrator.Migrate(t.Context(), rivermigrate.DirectionUp, &rivermigrate.MigrateOpts{TargetVersion: targetVersion})
	require.NoError(t, err)
	return schema
}

// Database is the database "main" on the test database URL.
func Database(t *testing.T, instances ...config.Instance) config.Database {
	t.Helper()
	return config.Database{Name: "main", URL: URL(t), Instances: instances}
}

// Config is a validated config with defaults for the given databases.
func Config(t *testing.T, databases ...config.Database) config.Config {
	t.Helper()
	cfg := config.Config{Databases: databases}.WithDefaults()
	require.NoError(t, cfg.Validate())
	return cfg
}
