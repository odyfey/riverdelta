package web

import (
	"bytes"
	_ "embed"
	"net/http"
	"strconv"
	"strings"
)

//go:embed static/switcher.js
var switcherJS string

// switcherTag is inlined so the switcher needs no route of its own.
var switcherTag = []byte("<script>" + switcherJS + "</script>")

var bodyClose = []byte("</body>")

// injectSwitcher adds the instance switcher to riverui's HTML pages. Only 200
// text/html responses are buffered; API responses and assets pass through.
func injectSwitcher(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		iw := &injectWriter{ResponseWriter: w}
		next.ServeHTTP(iw, r)
		iw.finish(r.Method == http.MethodHead)
	})
}

type injectWriter struct {
	http.ResponseWriter
	decided bool
	buf     *bytes.Buffer // non-nil while buffering an HTML page
}

func (w *injectWriter) WriteHeader(code int) {
	if w.decided {
		return
	}
	w.decided = true
	if code == http.StatusOK && strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		// riverui serves index.html via http.ServeContent; its length and
		// validators describe the page without the script.
		h := w.Header()
		h.Del("Content-Length")
		h.Del("Last-Modified")
		h.Del("ETag")
		w.buf = &bytes.Buffer{}
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *injectWriter) Write(p []byte) (int, error) {
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	if w.buf != nil {
		return w.buf.Write(p) //nolint:wrapcheck // bytes.Buffer never fails
	}
	return w.ResponseWriter.Write(p) //nolint:wrapcheck // pass-through writer
}

func (w *injectWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *injectWriter) finish(head bool) {
	if w.buf == nil {
		return
	}
	if head {
		w.ResponseWriter.WriteHeader(http.StatusOK)
		return
	}
	body := insertBeforeBodyClose(w.buf.Bytes())
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.ResponseWriter.WriteHeader(http.StatusOK)
	_, _ = w.ResponseWriter.Write(body)
}

// insertBeforeBodyClose returns page unchanged if it has no "</body>".
func insertBeforeBodyClose(page []byte) []byte {
	idx := bytes.LastIndex(page, bodyClose)
	if idx < 0 {
		return page
	}
	out := make([]byte, 0, len(page)+len(switcherTag))
	out = append(out, page[:idx]...)
	out = append(out, switcherTag...)
	return append(out, page[idx:]...)
}
