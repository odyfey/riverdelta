package main

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/odyfey/riverdelta/app/config"
	"github.com/odyfey/riverdelta/app/instance"
	"github.com/odyfey/riverdelta/app/pgtest"
	"github.com/odyfey/riverdelta/app/web"
	"github.com/stretchr/testify/require"
)

// openTestInstances opens the instances of databases and closes them when the
// test ends.
func openTestInstances(t *testing.T, databases ...config.Database) []*instance.Instance {
	t.Helper()
	set, err := instance.Open(t.Context(), pgtest.Config(t, databases...), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(set.Close)
	return set.Instances
}

// These tests cover what the fakes in app/web can't: newServerHandler wired
// to real riverui handlers and real databases.

// newTestHandler serves a healthy instance ("prod"), an outdated one
// ("tenant-a", migrated to version 3) and an unreachable one ("offline").
func newTestHandler(t *testing.T, auth *web.BasicAuth) http.Handler {
	t.Helper()
	pool := pgtest.NewPool(t)
	healthy := pgtest.NewSchema(t, pool, 0)
	outdated := pgtest.NewSchema(t, pool, 3)

	instances := openTestInstances(t,
		pgtest.Database(t,
			config.Instance{Name: "prod", Schema: healthy, Label: "Prod", Color: "#dc2626"},
			config.Instance{Name: "tenant-a", Schema: outdated},
		),
		config.Database{Name: "down", URL: pgtest.UnreachableURL, Instances: []config.Instance{{Name: "offline"}}},
	)
	return newServerHandler(instances, testStylesheet(t), auth, slog.New(slog.DiscardHandler))
}

func testStylesheet(t *testing.T) string {
	t.Helper()
	stylesheet, err := riverStylesheet()
	require.NoError(t, err)
	return stylesheet
}

func serveTest(t *testing.T, handler http.Handler, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
	req.Header.Set("Accept", "text/html")
	maps.Copy(req.Header, header)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHandlerRiveruiPage(t *testing.T) {
	handler := newTestHandler(t, nil)

	t.Run("HasPrefixAndSwitcher", func(t *testing.T) {
		rec := serveTest(t, handler, "/prod/jobs?state=running", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
		body := rec.Body.String()
		require.Contains(t, body, `"base":"/prod"`)
		require.Contains(t, body, `"apiUrl":"/prod/api"`)
		require.Contains(t, body, "riverdelta-switcher")
		require.Contains(t, body, "</script></body>")
	})
	t.Run("ConditionalRequestGetsFullPage", func(t *testing.T) {
		header := http.Header{"If-Modified-Since": {time.Now().UTC().Format(http.TimeFormat)}}
		rec := serveTest(t, handler, "/prod/", header)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "riverdelta-switcher")
	})
	t.Run("APIUntouched", func(t *testing.T) {
		rec := serveTest(t, handler, "/prod/api/features", http.Header{"Accept": {"application/json"}})
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
		require.NotContains(t, rec.Body.String(), "riverdelta-switcher")
	})
}

func TestHandlerReportsStatuses(t *testing.T) {
	handler := newTestHandler(t, nil)

	t.Run("InstancesJSON", func(t *testing.T) {
		rec := serveTest(t, handler, "/instances.json", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var got []map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Len(t, got, 3)
		require.Equal(t, map[string]any{"name": "prod", "label": "Prod", "color": "#dc2626", "base": "/prod", "healthy": true}, got[0])
		require.Equal(t, true, got[1]["healthy"])
		require.Contains(t, got[1]["warning"], "river migrations at version 3")
		require.Equal(t, map[string]any{"name": "offline", "label": "offline", "base": "/offline", "healthy": false}, got[2])
	})
	t.Run("Landing", func(t *testing.T) {
		rec := serveTest(t, handler, "/", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		require.Contains(t, body, ">Healthy</span>")
		require.Contains(t, body, ">river migrations at version 3, riverui expects")
		require.Contains(t, body, ">Warning</span>")
		require.Contains(t, body, ">Unreachable</span>")
	})
}

// landingAsset returns the URL in the landing page's attr that follows prefix.
func landingAsset(t *testing.T, body, prefix string) string {
	t.Helper()
	_, rest, found := strings.Cut(body, prefix)
	require.True(t, found, "landing page has no %q", prefix)
	url, _, found := strings.Cut(rest, `"`)
	require.True(t, found)
	return url
}

func TestLandingBorrowsRiveruiAssets(t *testing.T) {
	handler := newTestHandler(t, nil)
	body := serveTest(t, handler, "/", nil).Body.String()
	stylesheetURL := landingAsset(t, body, `<link rel="stylesheet" href="`)

	t.Run("AssetsServed", func(t *testing.T) {
		rec := serveTest(t, handler, stylesheetURL, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Header().Get("Content-Type"), "text/css")

		rec = serveTest(t, handler, landingAsset(t, body, `<link rel="icon" href="`), nil)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Header().Get("Content-Type"), "image/png")
	})

	// Tailwind drops the classes riverui doesn't use, so a riverui upgrade can
	// silently remove one the landing page relies on. The rendered page has
	// every badge variant, because the test handler has every status.
	t.Run("EveryClassExistsInStylesheet", func(t *testing.T) {
		css := serveTest(t, handler, stylesheetURL, nil).Body.String()
		for _, match := range regexp.MustCompile(`class="([^"]*)"`).FindAllStringSubmatch(body, -1) {
			for class := range strings.FieldsSeq(match[1]) {
				selector := "." + strings.NewReplacer(":", `\:`, "/", `\/`, ".", `\.`).Replace(class)
				pattern := regexp.QuoteMeta(selector) + `([^\w\\-]|$)`
				require.Regexp(t, pattern, css, "class %q is not in riverui's stylesheet", class)
			}
		}
	})

	t.Run("StylesheetServedWhenFirstInstanceIsDown", func(t *testing.T) {
		instances := openTestInstances(t, config.Database{Name: "down", URL: pgtest.UnreachableURL, Instances: []config.Instance{{Name: "offline"}}})
		down := newServerHandler(instances, testStylesheet(t), nil, slog.New(slog.DiscardHandler))
		downBody := serveTest(t, down, "/", nil).Body.String()
		rec := serveTest(t, down, landingAsset(t, downBody, `<link rel="stylesheet" href="`), nil)
		require.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestHandlerBasicAuth(t *testing.T) {
	handler := newTestHandler(t, web.NewBasicAuth("admin", "secret"))
	withAuth := http.Header{"Authorization": {"Basic YWRtaW46c2VjcmV0"}} // admin:secret

	tests := []struct {
		name       string
		path       string
		header     http.Header
		wantStatus int
	}{
		{name: "PageRequiresAuth", path: "/prod/jobs", wantStatus: http.StatusUnauthorized},
		{name: "APIRequiresAuth", path: "/prod/api/jobs?state=available", wantStatus: http.StatusUnauthorized},
		{name: "PageWithAuth", path: "/prod/jobs", header: withAuth, wantStatus: http.StatusOK},
		{name: "RiveruiHealthOpen", path: "/prod/api/health-checks/complete", wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantStatus, serveTest(t, handler, tt.path, tt.header).Code)
		})
	}
}
