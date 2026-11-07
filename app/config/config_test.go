package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadConfigExampleIsValid(t *testing.T) {
	t.Setenv("RIVERDELTA_PASSWORD", "secret")
	t.Setenv("SHARED_DATABASE_URL", "postgres://localhost/shared")
	t.Setenv("PROD_DATABASE_URL", "postgres://localhost/prod")

	cfg, err := Load("testdata/config.example.yaml")
	require.NoError(t, err)
	require.Equal(t, ":8080", cfg.Listen)
	instances := cfg.Instances()
	require.Len(t, instances, 3)
	require.Equal(t, Instance{Name: "tenant-a", Schema: "tenant_a", Label: "Tenant A"}, instances[0])
	require.Equal(t, "prod", instances[2].Name)
	require.Equal(t, &BasicAuth{Username: "admin", Password: "secret"}, cfg.BasicAuth)
}

func TestLoadConfigAppliesDefaults(t *testing.T) {
	path := writeConfigFile(t, `
databases:
  main:
    url: postgres://localhost/db
    instances:
    - { name: main }
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, defaultListen, cfg.Listen)
	require.Equal(t, int32(defaultMaxConns), cfg.Databases[0].MaxConns)
	require.Equal(t, "main", cfg.Databases[0].Instances[0].Label)
	require.Nil(t, cfg.BasicAuth)
}

func TestLoadConfigSQLiteDefaultsToOneConn(t *testing.T) {
	path := writeConfigFile(t, `
databases:
  local:
    url: sqlite:/data/app.sqlite3
    instances:
    - { name: local }
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, int32(defaultSQLiteMaxConns), cfg.Databases[0].MaxConns)
}

func TestDatabaseEngine(t *testing.T) {
	tests := []struct {
		url  string
		want Engine
	}{
		{url: "postgres://localhost/db", want: EnginePostgres},
		{url: "postgresql://localhost/db", want: EnginePostgres},
		{url: "host=localhost dbname=db", want: EnginePostgres},
		{url: "sqlite:/data/app.sqlite3", want: EngineSQLite},
		{url: "file:/data/app.sqlite3?mode=ro", want: EngineSQLite},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			require.Equal(t, tt.want, Database{URL: tt.url}.Engine())
		})
	}
}

func TestLoadConfigExpandsEnv(t *testing.T) {
	t.Setenv("RD_TEST_USER", "river")
	t.Setenv("RD_TEST_PASSWORD", "pa$word")
	path := writeConfigFile(t, `
basic_auth: { username: admin, password: "lit$eral" }
databases:
  main:
    url: postgres://${RD_TEST_USER}:${RD_TEST_PASSWORD}@localhost/db
    instances:
    - { name: main }
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "postgres://river:pa$word@localhost/db", cfg.Databases[0].URL)
	require.Equal(t, &BasicAuth{Username: "admin", Password: "lit$eral"}, cfg.BasicAuth)
}

func TestLoadConfigEmptyEnvReturnsError(t *testing.T) {
	t.Setenv("RD_TEST_EMPTY", "")
	path := writeConfigFile(t, `
basic_auth: { username: admin, password: "${RD_TEST_EMPTY}" }
databases:
  main:
    url: ${RD_TEST_UNSET}
    instances:
    - { name: main }
`)

	_, err := Load(path)
	require.ErrorContains(t, err, "basic_auth: password: env RD_TEST_EMPTY is empty")
	require.ErrorContains(t, err, `database "main": url: env RD_TEST_UNSET is empty`)
}

func TestLoadConfigKeepsFileOrder(t *testing.T) {
	path := writeConfigFile(t, `
databases:
  zeta:
    url: postgres://localhost/db
    instances:
    - { name: zeta-2 }
    - { name: zeta-1 }
  alpha:
    url: postgres://localhost/db
    instances:
    - { name: alpha-1 }
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	instances := cfg.Instances()
	names := make([]string, 0, len(instances))
	for _, inst := range instances {
		names = append(names, inst.Name)
	}
	require.Equal(t, []string{"zeta-2", "zeta-1", "alpha-1"}, names)
	require.Equal(t, "zeta", cfg.Databases[0].Name)
}

func TestLoadConfigUnknownFieldReturnsError(t *testing.T) {
	path := writeConfigFile(t, `
databases:
  main:
    instances:
    - { name: main, shcema: typo }
`)

	_, err := Load(path)
	require.ErrorContains(t, err, "field shcema not found")
}

func TestLoadConfigMissingFileReturnsError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestConfigValidate(t *testing.T) {
	valid := func() Config {
		return Config{
			Listen:    ":8080",
			BasicAuth: &BasicAuth{Username: "admin", Password: "secret"},
			Databases: []Database{
				{Name: "main", URL: "postgres://localhost/db", MaxConns: 3, Instances: []Instance{
					{Name: "prod", Color: "red"},
					{Name: "tenant-a", Schema: "tenant_a", Color: "#2563eb"},
				}},
				{Name: "other", URL: "postgres://localhost/db", MaxConns: 3, Instances: []Instance{
					{Name: "tenant-b"},
				}},
				{Name: "local", URL: "sqlite:/data/app.sqlite3", MaxConns: 1, Instances: []Instance{
					{Name: "local"},
				}},
			},
		}
	}
	// mainDB and otherDB are the two databases of the valid config
	mainDB := func(c *Config) *Database { return &c.Databases[0] }
	otherDB := func(c *Config) *Database { return &c.Databases[1] }
	sqliteDB := func(c *Config) *Database { return &c.Databases[2] }

	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{name: "Valid", mutate: func(*Config) {}},
		{
			name:    "NoInstances",
			mutate:  func(c *Config) { c.Databases = nil },
			wantErr: "at least one instance is required",
		},
		{
			name:    "DatabaseWithoutInstances",
			mutate:  func(c *Config) { otherDB(c).Instances = nil },
			wantErr: `database "other": at least one instance is required`,
		},
		{
			name:    "DuplicateName",
			mutate:  func(c *Config) { mainDB(c).Instances[1].Name = "prod" },
			wantErr: `instance "prod": duplicate name`,
		},
		{
			name:    "DuplicateNameAcrossDatabases",
			mutate:  func(c *Config) { otherDB(c).Instances[0].Name = "prod" },
			wantErr: `instance "prod": duplicate name`,
		},
		{
			name:    "ReservedName",
			mutate:  func(c *Config) { mainDB(c).Instances[0].Name = "api" },
			wantErr: `instance "api": name is reserved`,
		},
		{
			name:    "NameNotURLSafe",
			mutate:  func(c *Config) { mainDB(c).Instances[0].Name = "Prod/1" },
			wantErr: `instance "Prod/1": name must match`,
		},
		{
			name:    "InvalidSchema",
			mutate:  func(c *Config) { mainDB(c).Instances[1].Schema = "tenant-a" },
			wantErr: `instance "tenant-a": schema must match`,
		},
		{
			name:    "InvalidColor",
			mutate:  func(c *Config) { mainDB(c).Instances[0].Color = "red;background:url(x)" },
			wantErr: `instance "prod": color must be a css color name or hex value`,
		},
		{
			name:    "MissingURL",
			mutate:  func(c *Config) { mainDB(c).URL = "" },
			wantErr: `database "main": url is required`,
		},
		{
			name:    "NegativeMaxConns",
			mutate:  func(c *Config) { mainDB(c).MaxConns = -1 },
			wantErr: `database "main": max_conns must be positive`,
		},
		{
			name:    "SQLiteSecondInstance",
			mutate:  func(c *Config) { sqliteDB(c).Instances = append(sqliteDB(c).Instances, Instance{Name: "local-2"}) },
			wantErr: `database "local": sqlite allows exactly one instance per file`,
		},
		{
			name:    "SQLiteSchema",
			mutate:  func(c *Config) { sqliteDB(c).Instances[0].Schema = "tenant_a" },
			wantErr: `instance "local": schema is not supported for sqlite`,
		},
		{
			name:    "BasicAuthMissingUsername",
			mutate:  func(c *Config) { c.BasicAuth.Username = "" },
			wantErr: "basic_auth: username is required",
		},
		{
			name:    "BasicAuthMissingPassword",
			mutate:  func(c *Config) { c.BasicAuth.Password = "" },
			wantErr: "basic_auth: password is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
