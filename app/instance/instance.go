// Package instance opens one riverui handler per configured River schema
// (Postgres) or file (SQLite) and reports its health.
package instance

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/odyfey/riverdelta/app/config"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivermigrate"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver, pure Go
	"riverqueue.com/riverui"
)

const (
	statusTimeout = 2 * time.Second
	// sqliteBusyTimeout waits out the River app's write locks instead of
	// failing with SQLITE_BUSY
	sqliteBusyTimeout = "busy_timeout(5000)"
)

// Instance is one River schema served by its own unmodified riverui handler
// under "/<Name>/".
type Instance struct {
	Name     string
	Label    string
	Color    string
	Handler  http.Handler
	migrator migrator
}

// migrator is the part of *rivermigrate.Migrator the status needs; it's
// independent of the driver's transaction type.
type migrator interface {
	AllVersions() []rivermigrate.Migration
	ExistingVersions(ctx context.Context) ([]rivermigrate.Migration, error)
}

type Status struct {
	Healthy bool
	Warning string
}

// Set owns the pools so they're closed once, however many schemas share
// them.
type Set struct {
	Instances []*Instance
	closers   []func()
}

// Open never fails on an unreachable database: both pgxpool and
// database/sql connect lazily, so the instance stays mounted, reports
// unhealthy, and recovers when the database comes back.
func Open(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Set, error) {
	set := &Set{}
	for _, db := range cfg.Databases {
		var err error
		switch db.Engine() {
		case config.EngineSQLite:
			err = set.openSQLite(ctx, db, logger)
		default:
			err = set.openPostgres(ctx, db, logger)
		}
		if err != nil {
			set.Close()
			return nil, err
		}
	}
	return set, nil
}

func (s *Set) Close() {
	for _, closeFn := range s.closers {
		closeFn()
	}
}

func (s *Set) openPostgres(ctx context.Context, db config.Database, logger *slog.Logger) error {
	// pgx redacts the password from parse errors
	poolConfig, err := pgxpool.ParseConfig(db.URL)
	if err != nil {
		return fmt.Errorf("failed to open database %q: can't parse url: %w", db.Name, err)
	}
	poolConfig.MaxConns = db.MaxConns
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("failed to open database %q: failed to create pool: %w", db.Name, err)
	}
	s.closers = append(s.closers, pool.Close)
	if err := pool.Ping(ctx); err != nil {
		logger.ErrorContext(ctx, "database is unreachable", slog.String("database", db.Name), slog.Any("err", err))
	}
	return addInstances(ctx, s, db, riverpgxv5.New(pool), logger)
}

// openSQLite never creates the file: a missing one is an unreachable
// database, not an empty River schema.
func (s *Set) openSQLite(ctx context.Context, db config.Database, logger *slog.Logger) error {
	dsn, err := sqliteDSN(db.URL)
	if err != nil {
		return fmt.Errorf("failed to open database %q: %w", db.Name, err)
	}
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("failed to open database %q: %w", db.Name, err)
	}
	pool.SetMaxOpenConns(int(db.MaxConns))
	s.closers = append(s.closers, func() {
		if err := pool.Close(); err != nil {
			logger.Error("failed to close database", slog.String("database", db.Name), slog.Any("err", err))
		}
	})
	if err := pool.PingContext(ctx); err != nil {
		logger.ErrorContext(ctx, "database is unreachable", slog.String("database", db.Name), slog.Any("err", err))
	}
	return addInstances(ctx, s, db, riversqlite.New(pool), logger)
}

// sqliteDSN turns "sqlite:<path>" (or "sqlite://<path>", as the river CLI
// takes it) into a SQLite "file:" URI and opens it read-write without create
// unless the url sets mode. It doesn't touch the journal mode: that's
// persistent and belongs to the River app.
func sqliteDSN(rawURL string) (string, error) {
	rest := rawURL
	for _, prefix := range []string{"sqlite://", "sqlite:", "file:"} {
		if after, ok := strings.CutPrefix(rawURL, prefix); ok {
			rest = after
			break
		}
	}
	path, rawQuery, _ := strings.Cut(rest, "?")
	if path == "" {
		return "", fmt.Errorf("sqlite url has no file path")
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("can't parse sqlite url query: %w", err)
	}
	if !query.Has("mode") {
		query.Set("mode", "rw")
	}
	if !slices.ContainsFunc(query["_pragma"], func(p string) bool { return strings.HasPrefix(p, "busy_timeout") }) {
		query.Add("_pragma", sqliteBusyTimeout)
	}
	return "file:" + path + "?" + query.Encode(), nil
}

func addInstances[TTx any](ctx context.Context, s *Set, db config.Database, driver riverdriver.Driver[TTx], logger *slog.Logger) error {
	for _, ic := range db.Instances {
		inst, err := newInstance(ctx, ic, driver, logger)
		if err != nil {
			return fmt.Errorf("failed to create instance %q: %w", ic.Name, err)
		}
		s.Instances = append(s.Instances, inst)
	}
	return nil
}

// newInstance builds an insert-only client: without Start there are no
// workers, no leader election and no LISTEN connection.
func newInstance[TTx any](ctx context.Context, ic config.Instance, driver riverdriver.Driver[TTx], logger *slog.Logger) (*Instance, error) {
	logger = logger.With(slog.String("instance", ic.Name))

	client, err := river.NewClient(driver, &river.Config{Logger: logger, Schema: ic.Schema})
	if err != nil {
		return nil, fmt.Errorf("failed to create river client: %w", err)
	}

	handler, err := riverui.NewHandler(&riverui.HandlerOpts{
		Endpoints: riverui.NewEndpoints(client, nil),
		Logger:    logger,
		Prefix:    "/" + ic.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create riverui handler: %w", err)
	}
	if err = handler.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start riverui handler: %w", err)
	}

	migrator, err := rivermigrate.New(driver, &rivermigrate.Config{Logger: logger, Schema: ic.Schema})
	if err != nil {
		return nil, fmt.Errorf("failed to create migrator: %w", err)
	}

	return &Instance{
		Name:     ic.Name,
		Label:    ic.Label,
		Color:    ic.Color,
		Handler:  handler,
		migrator: migrator,
	}, nil
}

func (i *Instance) base() string {
	return "/" + i.Name
}

func (i *Instance) status(ctx context.Context) Status {
	if !i.healthy(ctx) {
		return Status{}
	}
	return Status{Healthy: true, Warning: i.migrationWarning(ctx)}
}

// healthy goes through riverui's own "complete" check in-process: "minimal"
// never touches the database, and calling the handler directly skips the
// wrapper's auth and request log.
func (i *Instance) healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.base()+"/api/health-checks/complete", http.NoBody)
	if err != nil {
		return false
	}
	rec := httptest.NewRecorder()
	i.Handler.ServeHTTP(rec, req)
	return rec.Code == http.StatusOK
}

// migrationWarning flags schemas whose River migration version differs from
// the one riverui's River dependency expects, since its queries may fail
// there.
func (i *Instance) migrationWarning(ctx context.Context) string {
	existing, err := i.migrator.ExistingVersions(ctx)
	if err != nil {
		return fmt.Sprintf("can't read river migrations: %v", err)
	}
	want := latestVersion(i.migrator.AllVersions())
	have := latestVersion(existing)
	switch {
	case have < want:
		return fmt.Sprintf("river migrations at version %d, riverui expects %d: run river migrate-up", have, want)
	case have > want:
		return fmt.Sprintf("river migrations at version %d are newer than riverui knows (%d): upgrade riverdelta", have, want)
	default:
		return ""
	}
}

func latestVersion(migrations []rivermigrate.Migration) int {
	if len(migrations) == 0 {
		return 0
	}
	return slices.MaxFunc(migrations, func(a, b rivermigrate.Migration) int { return a.Version - b.Version }).Version
}

// StatusAll checks instances concurrently so one slow database doesn't
// add up to N timeouts.
func StatusAll(ctx context.Context, instances []*Instance) []Status {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()

	statuses := make([]Status, len(instances))
	var wg sync.WaitGroup
	for idx, inst := range instances {
		wg.Go(func() {
			statuses[idx] = inst.status(ctx)
		})
	}
	wg.Wait()
	return statuses
}
