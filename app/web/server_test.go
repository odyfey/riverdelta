package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestHandler serves fake instances whose handlers echo the request path.
func newTestHandler(statuses []Status, auth *BasicAuth, instances ...Instance) http.Handler {
	for idx := range instances {
		instances[idx].Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<html><body>"+r.URL.Path+"</body></html>")
		})
	}
	status := func(context.Context) []Status { return statuses }
	return NewHandler(instances, status, "assets/main.css", auth, slog.New(slog.DiscardHandler))
}

func serveTest(t *testing.T, handler http.Handler, path string, withAuth bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
	if withAuth {
		req.SetBasicAuth("admin", "secret")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestNewHandlerRoutes(t *testing.T) {
	handler := newTestHandler([]Status{{Healthy: true}}, nil, Instance{Name: "prod", Label: "Prod"})

	t.Run("Landing", func(t *testing.T) {
		rec := serveTest(t, handler, "/", false)
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		require.Contains(t, body, `href="/prod/">Prod</a>`)
		require.Contains(t, body, `<link rel="stylesheet" href="/prod/assets/main.css" />`)
		require.Contains(t, body, `<img alt="" class="size-7" src="/prod/apple-icon.png" />`)
	})
	t.Run("InstancesJSON", func(t *testing.T) {
		rec := serveTest(t, handler, "/instances.json", false)
		require.JSONEq(t, `[{"name":"prod","label":"Prod","base":"/prod","healthy":true}]`, rec.Body.String())
	})
	t.Run("InstancePageGetsSwitcher", func(t *testing.T) {
		rec := serveTest(t, handler, "/prod/jobs", false)
		require.Equal(t, "<html><body>/prod/jobs"+string(switcherTag)+"</body></html>", rec.Body.String())
	})
	t.Run("InstanceRootRedirects", func(t *testing.T) {
		rec := serveTest(t, handler, "/prod", false)
		require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
		require.Equal(t, "/prod/", rec.Header().Get("Location"))
	})
	t.Run("UnknownPathNotFound", func(t *testing.T) {
		rec := serveTest(t, handler, "/staging/jobs", false)
		require.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestNewHandlerBasicAuth(t *testing.T) {
	handler := newTestHandler([]Status{{Healthy: true}}, NewBasicAuth("admin", "secret"), Instance{Name: "prod"})

	require.Equal(t, http.StatusUnauthorized, serveTest(t, handler, "/prod/jobs", false).Code)
	require.Equal(t, http.StatusOK, serveTest(t, handler, "/prod/jobs", true).Code)
	require.Equal(t, http.StatusOK, serveTest(t, handler, "/healthz", false).Code)
}

func TestIsHealthPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/healthz", want: true},
		{path: "/prod/api/health-checks/minimal", want: true},
		{path: "/prod/api/health-checks/complete", want: true},
		{path: "/prod/healthz", want: false},
		{path: "/prod/api/jobs", want: false},
		{path: "/", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			require.Equal(t, tt.want, isHealthPath(tt.path))
		})
	}
}
