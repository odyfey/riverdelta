package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testViews(*http.Request) []instanceView {
	return instanceViews(
		[]Instance{
			{Name: "prod", Label: "Prod", Color: "#dc2626"},
			{Name: "tenant-a", Label: "tenant-a"},
			{Name: "offline", Label: "offline"},
		},
		[]Status{
			{Healthy: true},
			{Healthy: true, Warning: "river migrations at version 3, riverui expects 8"},
			{},
		},
	)
}

func TestHandleInstancesJSONShape(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/instances.json", nil)
	handleInstancesJSON(testViews, slog.New(slog.DiscardHandler)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 3)
	require.Equal(t, map[string]any{"name": "prod", "label": "Prod", "color": "#dc2626", "base": "/prod", "healthy": true}, got[0])
	require.Equal(t, "river migrations at version 3, riverui expects 8", got[1]["warning"])
	require.NotContains(t, got[1], "color")
	require.Equal(t, map[string]any{"name": "offline", "label": "offline", "base": "/offline", "healthy": false}, got[2])
}

func TestHandleLandingRendersInstances(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	handleLanding(testViews, "/prod/assets/main.css", "/prod/apple-icon.png", slog.New(slog.DiscardHandler)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	body := rec.Body.String()
	require.Contains(t, body, `<link rel="stylesheet" href="/prod/assets/main.css" />`)
	require.Contains(t, body, `<link rel="icon" href="/prod/apple-icon.png" type="image/png" />`)
	require.Contains(t, body, `style="background-color: #dc2626">P</span>`)
	require.Contains(t, body, `style="background-color: #64748b">t</span>`)
	require.Contains(t, body, `href="/prod/">Prod</a>`)
	require.Contains(t, body, `href="/tenant-a/">tenant-a</a>`)
	require.Contains(t, body, `href="/offline/">offline</a>`)
	require.Contains(t, body, `>river migrations at version 3, riverui expects`)
	require.Equal(t, 1, strings.Count(body, ">Healthy</span>"))
	require.Equal(t, 1, strings.Count(body, ">Warning</span>"))
	require.Equal(t, 1, strings.Count(body, ">Unreachable</span>"))
}

func TestInitial(t *testing.T) {
	require.Equal(t, "P", initial("Prod"))
	require.Equal(t, "Я", initial("Яндекс"))
	require.Empty(t, initial(""))
}

func TestHandleHealthzReturnsOK(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	handleHealthz(slog.New(slog.DiscardHandler)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "ok\n", rec.Body.String())
}
