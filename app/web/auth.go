package web

import (
	"crypto/subtle"
	"net/http"
)

// BasicAuth reimplements riverui's internal/authmiddleware, which other
// modules can't import.
type BasicAuth struct {
	username string
	password string
}

func NewBasicAuth(username, password string) *BasicAuth {
	return &BasicAuth{username: username, password: password}
}

// middleware lets health checks through unauthenticated so probes don't
// need credentials.
func (a *BasicAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthPath(r.URL.Path) || a.authorized(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="riverdelta"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

func (a *BasicAuth) authorized(r *http.Request) bool {
	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}
	usernameOK := subtle.ConstantTimeCompare([]byte(username), []byte(a.username)) == 1
	passwordOK := subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) == 1
	return usernameOK && passwordOK
}
