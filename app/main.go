// Command riverdelta serves one River UI for many River databases and
// schemas, each under its own "/<instance>/" prefix.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"riverqueue.com/riverui"

	"github.com/odyfey/riverdelta/app/config"
	"github.com/odyfey/riverdelta/app/instance"
	"github.com/odyfey/riverdelta/app/web"
)

// revision is set at build time via -ldflags "-X main.revision=...".
var revision = "unknown"

const (
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	stylesheet, err := riverStylesheet()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	set, err := instance.Open(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer set.Close()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           newServerHandler(set.Instances, stylesheet, basicAuth(cfg), logger),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	logger.InfoContext(ctx, "starting server", slog.String("revision", revision), slog.String("addr", cfg.Listen), slog.Int("instances", len(set.Instances)))
	return serve(ctx, srv)
}

// riverStylesheet returns the path of riverui's hashed stylesheet relative to
// an instance's base, as listed in its frontend's build manifest.
func riverStylesheet() (string, error) {
	data, err := riverui.FrontendIndex.ReadFile("dist/.vite/manifest.json")
	if err != nil {
		return "", fmt.Errorf("read riverui manifest: %w", err)
	}
	var manifest map[string]struct {
		CSS []string `json:"css"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("parse riverui manifest: %w", err)
	}
	css := manifest["src/main.tsx"].CSS
	if len(css) == 0 {
		return "", errors.New("riverui manifest lists no stylesheet")
	}
	return css[0], nil
}

// newServerHandler serves instances through package web.
func newServerHandler(instances []*instance.Instance, stylesheet string, auth *web.BasicAuth, logger *slog.Logger) http.Handler {
	webInstances := make([]web.Instance, len(instances))
	for idx, inst := range instances {
		webInstances[idx] = web.Instance{Name: inst.Name, Label: inst.Label, Color: inst.Color, Handler: inst.Handler}
	}
	status := func(ctx context.Context) []web.Status {
		statuses := instance.StatusAll(ctx, instances)
		webStatuses := make([]web.Status, len(statuses))
		for idx, st := range statuses {
			webStatuses[idx] = web.Status(st)
		}
		return webStatuses
	}
	return web.NewHandler(webInstances, status, stylesheet, auth, logger)
}

// basicAuth is nil when the config has no basic_auth.
func basicAuth(cfg config.Config) *web.BasicAuth {
	if cfg.BasicAuth == nil {
		return nil
	}
	return web.NewBasicAuth(cfg.BasicAuth.Username, cfg.BasicAuth.Password)
}

// serve runs srv until ctx is done, then drains in-flight requests.
func serve(ctx context.Context, srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("failed to serve http: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("failed to shut down http server: %w", err)
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("failed to serve http: %w", err)
	}
	return nil
}
