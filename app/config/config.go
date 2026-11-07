// Package config loads and validates the riverdelta YAML config.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	defaultListen         = ":8080"
	defaultMaxConns       = 3
	defaultSQLiteMaxConns = 1
)

// Engine is the database a url points at.
type Engine string

// Engines riverdelta opens; see Database.Engine.
const (
	EnginePostgres Engine = "postgres"
	EngineSQLite   Engine = "sqlite"
)

var (
	instanceNameRE = regexp.MustCompile(`^[a-z0-9-]+$`)
	// schemaNameRE mirrors the rule river.Config applies to Schema.
	schemaNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	colorRE      = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-z]+)$`)
	// reservedNames would shadow the wrapper's own routes under "/".
	reservedNames = []string{"api", "healthz", "instances.json"}
	// envRefRE is the only expanded syntax, so a bare "$" in a literal
	// password stays as is.
	envRefRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// Config keeps databases, and the instances nested in them, in file order:
// that's the order of the landing page and the switcher.
type Config struct {
	Listen    string
	BasicAuth *BasicAuth
	Databases []Database
}

// fileConfig is the strictly decoded file; its map loses the database order,
// which Load restores from the yaml node.
type fileConfig struct {
	Listen    string              `yaml:"listen"`
	BasicAuth *BasicAuth          `yaml:"basic_auth"`
	Databases map[string]Database `yaml:"databases"`
}

type BasicAuth struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type Database struct {
	Name      string     `yaml:"-"`
	URL       string     `yaml:"url"`
	MaxConns  int32      `yaml:"max_conns"`
	Instances []Instance `yaml:"instances"`
}

// Engine is SQLite for "sqlite:" and "file:" urls and Postgres for anything
// else, so key/value DSNs keep working.
func (d Database) Engine() Engine {
	if strings.HasPrefix(d.URL, "sqlite:") || strings.HasPrefix(d.URL, "file:") {
		return EngineSQLite
	}
	return EnginePostgres
}

type Instance struct {
	Name   string `yaml:"name"`
	Schema string `yaml:"schema"`
	Label  string `yaml:"label"`
	Color  string `yaml:"color"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is the operator-supplied -config flag
	if err != nil {
		return Config{}, fmt.Errorf("failed to read config: %w", err)
	}

	cfg, err := parse(raw)
	if err != nil {
		return Config{}, fmt.Errorf("can't parse config %s: %w", path, err)
	}

	cfg, err = cfg.expandEnv()
	if err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// parse decodes twice: strictly into fileConfig, then into a yaml node
// for the database order. A custom UnmarshalYAML would keep the order in one
// pass, but yaml.Node.Decode drops KnownFields.
func parse(raw []byte) (Config, error) {
	var file fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil {
		return Config{}, fmt.Errorf("decode: %w", err)
	}

	var order struct {
		Databases yaml.Node `yaml:"databases"`
	}
	if err := yaml.Unmarshal(raw, &order); err != nil {
		return Config{}, fmt.Errorf("decode database order: %w", err)
	}

	cfg := Config{Listen: file.Listen, BasicAuth: file.BasicAuth}
	// a mapping node's Content alternates keys and values
	keys := order.Databases.Content
	for i := 0; i < len(keys); i += 2 {
		db := file.Databases[keys[i].Value]
		db.Name = keys[i].Value
		cfg.Databases = append(cfg.Databases, db)
	}
	return cfg, nil
}

// expandEnv resolves "${VAR}" in the secret-bearing fields, url and
// password, so the file can live in git or a ConfigMap. A referenced env var
// that is unset or empty fails startup instead of the first request.
func (c Config) expandEnv() (Config, error) {
	var errs []error
	if c.BasicAuth != nil {
		auth := *c.BasicAuth
		password, err := expandEnv(auth.Password)
		if err != nil {
			errs = append(errs, fmt.Errorf("basic_auth: password: %w", err))
		}
		auth.Password = password
		c.BasicAuth = &auth
	}

	databases := slices.Clone(c.Databases)
	for i := range databases {
		url, err := expandEnv(databases[i].URL)
		if err != nil {
			errs = append(errs, fmt.Errorf("database %q: url: %w", databases[i].Name, err))
		}
		databases[i].URL = url
	}
	c.Databases = databases
	return c, errors.Join(errs...)
}

func expandEnv(s string) (string, error) {
	var errs []error
	expanded := envRefRE.ReplaceAllStringFunc(s, func(ref string) string {
		name := envRefRE.FindStringSubmatch(ref)[1]
		value := os.Getenv(name)
		if value == "" {
			errs = append(errs, fmt.Errorf("env %s is empty", name))
		}
		return value
	})
	return expanded, errors.Join(errs...)
}

func (c Config) WithDefaults() Config {
	if c.Listen == "" {
		c.Listen = defaultListen
	}

	databases := slices.Clone(c.Databases)
	for i := range databases {
		if databases[i].MaxConns == 0 {
			databases[i].MaxConns = defaultMaxConns
			// SQLite runs one operation at a time; more connections only
			// trade waiting for SQLITE_BUSY
			if databases[i].Engine() == EngineSQLite {
				databases[i].MaxConns = defaultSQLiteMaxConns
			}
		}
		instances := slices.Clone(databases[i].Instances)
		for j := range instances {
			if instances[j].Label == "" {
				instances[j].Label = instances[j].Name
			}
		}
		databases[i].Instances = instances
	}
	c.Databases = databases
	return c
}

// Instances flattens the instances of all databases in config order.
func (c Config) Instances() []Instance {
	var instances []Instance
	for _, db := range c.Databases {
		instances = append(instances, db.Instances...)
	}
	return instances
}

func (c Config) Validate() error {
	var errs []error
	instances := c.Instances()
	if len(instances) == 0 {
		errs = append(errs, errors.New("at least one instance is required"))
	}
	if c.BasicAuth != nil {
		errs = append(errs, c.BasicAuth.validate())
	}
	for _, db := range c.Databases {
		errs = append(errs, db.validate())
	}

	// names are unique across databases: they share the "/<name>/" namespace
	seen := make(map[string]bool, len(instances))
	for _, inst := range instances {
		if seen[inst.Name] {
			errs = append(errs, fmt.Errorf("instance %q: duplicate name", inst.Name))
		}
		seen[inst.Name] = true
	}
	return errors.Join(errs...)
}

func (b BasicAuth) validate() error {
	var errs []error
	if b.Username == "" {
		errs = append(errs, errors.New("basic_auth: username is required"))
	}
	if b.Password == "" {
		errs = append(errs, errors.New("basic_auth: password is required"))
	}
	return errors.Join(errs...)
}

func (d Database) validate() error {
	name := d.Name
	var errs []error
	if d.URL == "" {
		errs = append(errs, fmt.Errorf("database %q: url is required", name))
	}
	if d.MaxConns < 0 {
		errs = append(errs, fmt.Errorf("database %q: max_conns must be positive", name))
	}
	if len(d.Instances) == 0 {
		errs = append(errs, fmt.Errorf("database %q: at least one instance is required", name))
	}
	for _, inst := range d.Instances {
		errs = append(errs, inst.validate())
	}
	// a SQLite file is one River database: there are no schemas to split it
	if d.Engine() == EngineSQLite {
		if len(d.Instances) > 1 {
			errs = append(errs, fmt.Errorf("database %q: sqlite allows exactly one instance per file", name))
		}
		for _, inst := range d.Instances {
			if inst.Schema != "" {
				errs = append(errs, fmt.Errorf("instance %q: schema is not supported for sqlite", inst.Name))
			}
		}
	}
	return errors.Join(errs...)
}

func (i Instance) validate() error {
	var errs []error
	if !instanceNameRE.MatchString(i.Name) {
		errs = append(errs, fmt.Errorf("instance %q: name must match %s", i.Name, instanceNameRE))
	}
	if slices.Contains(reservedNames, i.Name) {
		errs = append(errs, fmt.Errorf("instance %q: name is reserved", i.Name))
	}
	if i.Schema != "" && !schemaNameRE.MatchString(i.Schema) {
		errs = append(errs, fmt.Errorf("instance %q: schema must match %s", i.Name, schemaNameRE))
	}
	if i.Color != "" && !colorRE.MatchString(i.Color) {
		errs = append(errs, fmt.Errorf("instance %q: color must be a css color name or hex value", i.Name))
	}
	return errors.Join(errs...)
}
