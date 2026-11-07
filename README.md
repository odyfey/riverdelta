# riverdelta

Run a single River UI across multiple River queue databases and schemas, on Postgres or SQLite.

Normally every River database or schema needs its own [riverui](https://github.com/riverqueue/riverui) deployment ([riverui#3](https://github.com/riverqueue/riverui/issues/3)). riverdelta is a small Go service that hosts one unmodified riverui handler per instance, each under its own path prefix:

```
/                  landing page: every instance with its health status
/instances.json    instance list, read by the switcher in the UI
/healthz           liveness of riverdelta itself
/<instance>/...    riverui for that database/schema (UI and /api)
```

Each instance uses an insert-only `river.Client`: it is never started, so it runs no workers, takes no part in leader election and opens no LISTEN connection. The UI is stock riverui (`riverqueue.com/riverui`, a normal Go dependency). riverdelta adds one `<script>` before `</body>` in each riverui HTML page. The script draws an instance switcher badge in the bottom-right corner: it fetches `/instances.json` and links to the same page and filters in every other instance. API responses and assets are passed through untouched, and if the script fails, the page is plain riverui.

## Configuration

```sh
riverdelta -config config.yaml
```

See [`config.example.yaml`](config.example.yaml):

```yaml
listen: ":8080"
basic_auth: { username: admin, password: "${RIVERDELTA_PASSWORD}" } # optional
databases:
  shared:
    url: ${SHARED_DATABASE_URL}
    max_conns: 3
    instances:
    - { name: tenant-a, schema: tenant_a, label: "Tenant A" }
  prod:
    url: ${PROD_DATABASE_URL}
    instances:
    - { name: prod, schema: "", label: "Prod", color: red }
  local:
    url: sqlite:/data/app.sqlite3
    instances:
    - { name: local, label: "Local" }
```

- `url` and `basic_auth.password` expand `${VAR}` from the environment (only that form; a bare `$` stays literal). Keep secrets in env vars and only `${VAR}` references in the file, so it holds no credentials and is safe to commit or mount as a ConfigMap. Literal values work, but only for local development.
- `url` picks the engine: `sqlite:<path>`, `sqlite://<path>` (the river CLI's form) or `file:<path>` is SQLite (see [SQLite](#sqlite)), anything else is Postgres (`postgres://`, `postgresql://` or a key/value DSN, as parsed by pgx).
- Each database gets one connection pool, shared by every instance (schema) listed under it, and must list at least one instance.
- Instances appear in the switcher and on the landing page in file order: databases as listed, then each database's instances.
- `instances[].name` is the URL segment: `^[a-z0-9-]+$`, unique across all databases, and not `api`, `healthz` or `instances.json`. `schema` empty means the connection's `search_path`. `label` defaults to the name. `color` is a CSS color name or hex value.
- Startup fails on an invalid config or a referenced env var that is unset or empty. An unreachable database only logs an error: its instances stay mounted, show as unhealthy and recover when the database comes back.
- With `basic_auth`, everything except `/healthz` and `/<instance>/api/health-checks/*` needs credentials.

### Connections

Each pool opens at most `max_conns` connections (default 3), so riverdelta uses at most the sum of `max_conns` over all databases. That doesn't depend on the number of schemas. Each riverui handler also refreshes job counts in the background about every 10 seconds, so a database with N schemas sees N small queries per refresh. That is fine for tens of instances.

When going through PgBouncer in transaction mode, use a URL that disables prepared statement caching (for example `default_query_exec_mode=exec`), because pgx caches prepared statements per connection.

### SQLite

A SQLite file is one River database, so a SQLite entry has exactly one instance and no `schema`. riverdelta uses the pure-Go `modernc.org/sqlite` driver, so the image still needs no cgo.

- The file is opened read-write without create (`mode=rw`). A missing file is an unreachable database: the instance shows as unhealthy and recovers once the file exists, and riverdelta never creates an empty one. Set `mode` in the url to override, e.g. `sqlite:/data/app.sqlite3?mode=ro` for a read-only view where cancel, retry and delete fail.
- `max_conns` defaults to 1, since SQLite runs one write at a time. `_pragma=busy_timeout(5000)` is added unless the url sets a busy timeout, so riverdelta waits out the River app's locks and avoids `SQLITE_BUSY`. Other `_pragma` values in the url are passed through. The journal mode is left alone, because it is a persistent property of the River app's file.
- In Docker, mount the file's directory, not just the file: SQLite creates `-wal`/`-shm`/`-journal` files next to it, and the container runs as `nobody`, which needs write access to both.

### Health and migrations

The landing page and `/instances.json` check every instance concurrently, with a 2 second timeout:

- `healthy` runs riverui's own `complete` health check (a `select 1` on the instance's pool).
- `warning` is set when the schema's (or SQLite file's) River migration version differs from the one riverui's River dependency expects. Older schemas can make some screens fail. Run `river migrate-up --schema <schema>` (or `--database-url sqlite://<path>` for SQLite, the same url riverdelta accepts), or upgrade riverdelta when the schema is newer.

## Building

A plain Go build; the riverui module from the Go proxy already contains its built frontend.

```sh
make build                                    # .bin/riverdelta
go install github.com/odyfey/riverdelta/app@latest   # installs the binary as "app"
```

`make build`, the Dockerfile and releases set riverui's reported version (`/api/version`) from `go.mod`; a plain `go build` or `go install` reports riverdelta's version there instead.

```sh
docker build --build-arg REVISION=$(git rev-parse --short HEAD) -t riverdelta .

docker run -p 8080:8080 \
  -v $PWD/config.yaml:/etc/riverdelta/config.yaml:ro \
  -e PROD_DATABASE_URL=... riverdelta
```

Or with Docker Compose: copy `config.example.yaml` to `config.yaml`, put the `${VAR}` values it references in `.env` (gitignored), and run `docker compose up -d --build`. `compose.yaml` mounts `config.yaml`, publishes port 8080 (`RIVERDELTA_PORT` to change it) and health-checks `/healthz`.

Pushing a `v*` tag runs GoReleaser (`.goreleaser.yaml`), which publishes a linux amd64/arm64 image to `ghcr.io/odyfey/riverdelta` tagged with the version (and `latest` for non-prereleases) and a GitHub release with the changelog.

## Testing

Database tests need Docker: the first test that needs Postgres starts a `postgres:18-alpine` container with [testcontainers-go](https://golang.testcontainers.org/), and it is removed when the package's tests finish. Without Docker those tests fail. SQLite tests use temporary files and need nothing.

```sh
go test ./...                     # unit, database and end-to-end isolation tests
golangci-lint run ./...
```

With rootless Docker (Lima, Colima, rootless dockerd), testcontainers' Ryuk cleanup container can't mount the host socket path. Point it at the socket inside the VM:

```sh
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/run/user/$(id -u)/docker.sock
```

Tests create River schemas with random names and drop them afterwards. The integration tests migrate two schemas, insert different jobs in each, and check that listing, job details, cancel, retry, delete, and queue pause/resume/update in one instance never touch the other, even when the job IDs are the same. A SQLite instance next to a Postgres one gets the same job checks through riverui's SQLite queries.

## Free and Pro

riverdelta depends only on `riverqueue.com/riverui`, not on `riverpro` or `riverproui`. Pro screens are gated by `/api/features`, which is all `false` with `riverui.NewEndpoints`: nav items stay hidden and direct URLs show the upsell page. To move to Pro later, swap in `riverproui.NewEndpoints` with a riverpro client per instance in [`app/instance/instance.go`](app/instance/instance.go).

## Upgrading riverui

riverui is a normal `go.mod` dependency, and Dependabot proposes new versions.

1. Read riverui's `CHANGELOG.md` for features moving to Pro and for migration requirements.
2. `go get riverqueue.com/riverui@<version> && go mod tidy`, then run `go test ./...`.
3. The switcher assumes only that riverui pages have a `</body>` and a `script#config__json` with `base`. `TestInjectSwitcherRiverui` checks the injection against the real handler.
4. The landing page has no styles of its own: it uses riverui's stylesheet and icon, served from the first instance. Tailwind drops classes riverui stops using, so `TestLandingBorrowsRiveruiAssets` fails if the page uses one that is gone.
5. Keep the River version used by your services compatible with riverui's (the landing page warns about migration mismatches).

## Known limitations

- riverui keeps user settings in `localStorage`, which is per origin, so all instances share them. Separate settings would need one hostname per instance (choosing the handler by `Host`), which riverdelta doesn't do yet.
- Instance-level access control is all or nothing: one basic auth user for every instance.

## License

[Mozilla Public License 2.0](LICENSE), the same as [River](https://github.com/riverqueue/river) and [riverui](https://github.com/riverqueue/riverui).
