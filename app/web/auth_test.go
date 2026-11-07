package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBasicAuthMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("OK"))
		assert.NoError(t, err)
	})
	handler := NewBasicAuth("admin", "secret").middleware(next)

	tests := []struct {
		name       string
		path       string
		username   string
		password   string
		wantStatus int
	}{
		{name: "ValidCredentials", path: "/prod/jobs", username: "admin", password: "secret", wantStatus: http.StatusOK},
		{name: "NoCredentials", path: "/prod/jobs", wantStatus: http.StatusUnauthorized},
		{name: "WrongUsername", path: "/", username: "root", password: "secret", wantStatus: http.StatusUnauthorized},
		{name: "WrongPassword", path: "/instances.json", username: "admin", password: "nope", wantStatus: http.StatusUnauthorized},
		{name: "WrapperHealthOpen", path: "/healthz", wantStatus: http.StatusOK},
		{name: "InstanceHealthOpen", path: "/prod/api/health-checks/minimal", wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil)
			if tt.username != "" {
				req.SetBasicAuth(tt.username, tt.password)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantStatus == http.StatusUnauthorized {
				require.Equal(t, `Basic realm="riverdelta"`, rec.Header().Get("WWW-Authenticate"))
			} else {
				require.Equal(t, "OK", rec.Body.String())
			}
		})
	}
}
