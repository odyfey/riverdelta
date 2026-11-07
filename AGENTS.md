# AGENTS.md

This file provides guidance to AI coding agents working with code in this repository.

## What this is

riverdelta is a Go service (`package main` in `app/`, with packages `app/config`, `app/instance` and `app/web`) that serves one River UI for many River databases/schemas, on Postgres or SQLite. It mounts one **unmodified** riverui handler per configured instance under `/<instance>/`, plus a landing page (`/`), `/instances.json` (read by the instance switcher in the UI) and `/healthz`. The README is the user-facing spec for config semantics, connections, health and upgrades; keep it in sync when behavior changes.

## Build

riverui is a plain `go.mod` dependency (`riverqueue.com/riverui`); its proxy module already embeds the built frontend (`dist/`). No Node, no sibling checkout.

```sh
make build                            # go build -ldflags "-X main.revision=... -X .../buildinfo.version=<riverui version>" -o .bin/riverdelta ./app
.bin/riverdelta -config config.yaml   # config.yaml is gitignored; start from config.example.yaml
```

Makefile targets: `make` (= `test build`), `make build`, `make test` (clears test cache, `-race` with coverage summary), `make race` (`-race -timeout=60s`, no coverage), `make lint`, `make version` (prints the revision string `<tag|branch>-<hash>-<UTC timestamp>`, or `latest` outside git), `make clean` (removes `.bin`, `dist`, `coverage.out`). There are no build tags: `make test` and `make lint` include `app/integration_test.go`.

Docker: `docker build -t riverdelta .` (`REVISION` build arg, default `dev`). The `Dockerfile` cross-compiles with `make build REV=$REVISION` (the context has no `.git`). `make build` sets riverui's `internal/buildinfo.version` from `go list -m riverqueue.com/riverui`, since as a dependency riverui would otherwise report riverdelta's module version at `/api/version`.

Releases: a `v*` tag triggers `.github/workflows/release.yml`, which runs GoReleaser (`.goreleaser.yaml`) for images only: builds are skipped, no archives. It builds the multi-arch image from `Dockerfile` (sources passed via `extra_files`, `REVISION` = `<tag>-<short commit>`), pushes to `ghcr.io/odyfey/riverdelta` and creates the GitHub release with a changelog.

The instance switcher lives here, not in riverui: see `app/web/inject.go` / `app/web/static/switcher.js` below.

## Tests and lint

Postgres tests need Docker: `pgtest` starts `postgres:18-alpine` via testcontainers-go on first use and fails (no skip) without Docker. With rootless Docker (Lima/Colima) set `TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/run/user/$(id -u)/docker.sock` so Ryuk can start.

```sh
make test                              # unit + DB tests with -race and coverage
go test -run TestConfigValidate ./...  # single test
make lint                              # golangci-lint, no issue caps
```

DB test helpers are in packages `app/pgtest` and `app/sqlitetest` (importable by any package's tests; `app/web` tests use fakes and need no DB). A package using `pgtest` must have `func TestMain(m *testing.M) { pgtest.Main(m) }` (in `app/main_test.go` and `app/instance/instance_test.go`), which stops the shared container. `pgtest.URL` starts it once per test binary; `pgtest.NewPool`; `pgtest.NewSchema` creates a random `rd_<hex>` schema, migrates River to a target version (0 = all, -1 = none) and drops it on cleanup; `pgtest.Database(t, ...)` is database `"main"` on the test DB; `pgtest.Config` applies defaults and validates; `pgtest.UnreachableURL` is a database that is always down. `sqlitetest.NewDatabase(t, targetVersion)` makes a migrated file in `t.TempDir()` (same version convention), `sqlitetest.URL` turns its path into a config url and `sqlitetest.Open` opens it for inserting test jobs. `app/handler_test.go` tests `newServerHandler` against real riverui and Postgres (only what `app/web`'s fakes can't cover); `config_test.go` reads `app/config/testdata/config.example.yaml`, a copy of the root `config.example.yaml` — keep the two in sync. Tests use testify `require`.

## Architecture

Layout: `app/main.go` is `package main` (startup and wiring only). `app/config` loads the YAML; `app/instance` opens River/riverui per schema and reports status; `app/web` is the HTTP layer and knows nothing about River, pgx or the config file; `app/web/static` holds the embedded `landing.html` and `switcher.js`. Dependencies point one way: `instance` → `config`; `main` → all; `web` → nothing internal. `web` only sees `web.Instance` (name, label, color, handler), `web.Status` and a `web.StatusFunc`; `main` converts `instance.Status` to `web.Status` (same fields) and `config.BasicAuth` to `web.BasicAuth`.

Startup (`app/main.go`): `config.Load` → `instance.Open` → `newServerHandler` (maps instances to `web.Instance` and `instance.StatusAll` to `web.StatusFunc`, then calls `web.NewHandler`) → `serve` (graceful shutdown on SIGINT/SIGTERM).

- **`app/config`** – YAML with unknown fields rejected; instances are nested under `databases.<name>.instances`. `parse` decodes twice (strict into a map, then a `yaml.Node` for key order) because `Node.Decode` drops `KnownFields`; `config.Databases` is an ordered slice and file order is the UI order. `WithDefaults` then `Validate`. `url` and `basic_auth.password` expand `${VAR}` in `expandEnv` (before `withDefaults`); an unset or empty referenced var fails startup. No other field is expanded. Instance names must be `^[a-z0-9-]+$` and must not collide with top-level routes (`api`, `healthz`, `instances.json`) — update that reserved list if you add a route in `app/web/server.go`.
- **Engines** – `config.Database.Engine()` picks SQLite for `sqlite:`/`file:` urls and Postgres for anything else (incl. key/value DSNs). SQLite databases allow exactly one instance and no `schema` (a file is one River database), and `max_conns` defaults to 1.
- **`app/instance`** – one pool per *database*, shared by all instances (schemas) on it: `pgxpool` + `riverpgxv5` for Postgres, `database/sql` with `modernc.org/sqlite` (pure Go, keeps `CGO_ENABLED=0`) + `riversqlite` for SQLite. `newInstance` is generic over `riverdriver.Driver[TTx]` and `Instance.migrator` is a small interface, so the rest is engine-agnostic; riverui picks its own SQLite queries from the driver. `sqliteDSN` adds `mode=rw` (a missing file is unreachable, never created) and a `busy_timeout` pragma unless the url sets them, and never changes the journal mode. `instance.Set` owns and closes pools. Each instance gets an insert-only `river.Client` that is **never started** (no workers, leader election or LISTEN), a `riverui.NewEndpoints` handler with `Prefix: "/<name>"`, and a `rivermigrate.Migrator` for version warnings. An unreachable database must not fail startup (pgxpool and database/sql connect lazily); only invalid config/URLs do.
- **Status** – `instance.StatusAll` checks every instance concurrently under a 2s timeout. Health calls riverui's own `/api/health-checks/complete` in-process via `httptest.NewRecorder` (bypassing auth and logging); the migration warning compares the schema's latest applied River migration with what riverui's River dependency expects.
- **`app/web/server.go`** – middleware order is recovery → slog request log → basic auth → mux. `isHealthPath` (`/healthz` and any `/api/health-checks/`) is both excluded from request logs and exempt from auth.
- **`app/web/auth.go`** – reimplements riverui's `internal/authmiddleware` (not importable from another module) with constant-time comparison.
- **`app/web/inject.go` / `static/switcher.js`** – `injectSwitcher` wraps each instance handler and inlines the embedded `switcher.js` as a `<script>` before the last `</body>` of 200 `text/html` responses (dropping `Content-Length`, `Last-Modified`, `ETag`, so no 304 can serve a page without it). Everything else streams through untouched. The script is plain JS with no build step: it reads `base` from riverui's `script#config__json`, fetches `/instances.json` (`instanceView`) and renders a fixed-position badge in a Shadow DOM. That's the switcher's whole contract with riverui: `</body>`, `#config__json`, and the `dark` class on `<html>` for theming.
- **`app/web/landing.go` / `static/landing.html`** – embedded `html/template` landing page and the `/instances.json` handler, both built from `instanceViews`. The page has no CSS of its own: it copies markup and classes from riverui's `TopNav`, `QueueList` and `Badge`, and links riverui's stylesheet and `apple-icon.png` under the first instance (`icon.svg` is served as `text/plain`, so browsers won't render it). `riverStylesheet` in `app/main.go` finds the hashed stylesheet in `riverui.FrontendIndex`'s `dist/.vite/manifest.json` and fails startup if it can't. An inline script applies riverui's `theme` localStorage key as the `dark` class on `<html>`. Any class used must exist in riverui's compiled CSS, since Tailwind drops unused ones; `TestLandingBorrowsRiveruiAssets` checks this and that the assets are served, so a class that isn't there means restyling that element another way. Every text element needs an explicit colour class, because riverui's `:root` sets near-white text.

Free vs Pro: only `riverui.NewEndpoints` is used, so `/api/features` is all false; switching to Pro means swapping in `riverproui.NewEndpoints` with a riverpro client per instance in `newInstance`.

## Upgrading riverui

Bump `riverqueue.com/riverui` in `go.mod` (Dependabot or `go get`); the README "Upgrading riverui" section has the checklist. Run `go test ./...` after any riverui or River version change; `app/integration_test.go` covers cross-instance isolation.
