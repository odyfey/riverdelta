// Package web serves the riverui instances, the landing page and
// "/instances.json", and injects the instance switcher into riverui pages.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	sloghttp "github.com/samber/slog-http"
)

// Instance is one riverui handler, mounted under "/<Name>/".
type Instance struct {
	Name    string
	Label   string
	Color   string
	Handler http.Handler
}

func (i Instance) Base() string {
	return "/" + i.Name
}

// Status is the health of one instance as shown on the landing page and in
// the switcher.
type Status struct {
	Healthy bool
	Warning string
}

// StatusFunc returns one Status per instance, in the same order.
type StatusFunc func(ctx context.Context) []Status

// NewHandler wires recovery → request log → basic auth → mux. auth may be nil
// to serve without authentication. The landing page borrows the stylesheet and
// icon of the first instance: stylesheet is the path of that stylesheet
// relative to an instance's base. instances must not be empty.
func NewHandler(instances []Instance, status StatusFunc, stylesheet string, auth *BasicAuth, logger *slog.Logger) http.Handler {
	views := func(r *http.Request) []instanceView {
		return instanceViews(instances, status(r.Context()))
	}
	assetBase := instances[0].Base() + "/"

	mux := http.NewServeMux()
	mux.Handle("GET /{$}", handleLanding(views, assetBase+stylesheet, assetBase+"apple-icon.png", logger))
	mux.Handle("GET /instances.json", handleInstancesJSON(views, logger))
	mux.Handle("GET /healthz", handleHealthz(logger))
	for _, inst := range instances {
		mux.Handle(inst.Base()+"/", injectSwitcher(inst.Handler))
	}

	var handler http.Handler = mux
	if auth != nil {
		handler = auth.middleware(handler)
	}
	handler = sloghttp.NewWithConfig(logger, sloghttp.Config{
		Filters: []sloghttp.Filter{sloghttp.Ignore(isHealthRequest)},
	})(handler)
	return sloghttp.Recovery(handler)
}

func isHealthRequest(_ sloghttp.WrapResponseWriter, r *http.Request) bool {
	return isHealthPath(r.URL.Path)
}

// isHealthPath matches the wrapper's own probe and every instance's riverui
// health checks.
func isHealthPath(path string) bool {
	return path == "/healthz" || strings.Contains(path, "/api/health-checks/")
}
