package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testPage = "<html><body><div id=\"root\"></div></body></html>"

func serveInjected(t *testing.T, method string, inner http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, "/prod/jobs", http.NoBody)
	rec := httptest.NewRecorder()
	injectSwitcher(inner).ServeHTTP(rec, req)
	return rec
}

func serveContent(contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "index.html", time.Unix(1_700_000_000, 0), strings.NewReader(body))
	}
}

func TestInjectSwitcherHTML(t *testing.T) {
	res := serveInjected(t, http.MethodGet, serveContent("text/html; charset=utf-8", testPage))
	body := res.Body.String()

	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, "<html><body><div id=\"root\"></div>"+string(switcherTag)+"</body></html>", body)
	require.Equal(t, strconv.Itoa(len(body)), res.Header().Get("Content-Length"))
	require.Empty(t, res.Header().Get("Last-Modified"))
	require.Empty(t, res.Header().Get("ETag"))
}

func TestInjectSwitcherHEAD(t *testing.T) {
	res := serveInjected(t, http.MethodHead, serveContent("text/html; charset=utf-8", testPage))

	require.Equal(t, http.StatusOK, res.Code)
	require.Empty(t, res.Body.String())
	require.Empty(t, res.Header().Get("Content-Length"))
}

func TestInjectSwitcherPassesThrough(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{name: "JSON", status: http.StatusOK, contentType: "application/json", body: `{"body":"</body>"}`},
		{name: "JavaScript", status: http.StatusOK, contentType: "text/javascript", body: `"</body>"`},
		{name: "HTMLNotFound", status: http.StatusNotFound, contentType: "text/html", body: testPage},
		{name: "HTMLWithoutBodyClose", status: http.StatusOK, contentType: "text/html", body: "<p>hi</p>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := serveInjected(t, http.MethodGet, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			require.Equal(t, tt.status, res.Code)
			require.Equal(t, tt.body, res.Body.String())
		})
	}
}

func TestInjectSwitcherKeepsAssetValidators(t *testing.T) {
	res := serveInjected(t, http.MethodGet, serveContent("text/css", "body{}"))

	require.Equal(t, "body{}", res.Body.String())
	require.Equal(t, "6", res.Header().Get("Content-Length"))
	require.NotEmpty(t, res.Header().Get("Last-Modified"))
	require.Equal(t, `"v1"`, res.Header().Get("ETag"))
}
