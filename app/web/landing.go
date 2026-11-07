package web

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
)

//go:embed static/landing.html
var landingHTML string

var landingTemplate = template.Must(template.New("landing").Funcs(template.FuncMap{"initial": initial}).Parse(landingHTML))

// landingData is what static/landing.html renders.
type landingData struct {
	Stylesheet string
	Icon       string
	Instances  []instanceView
}

// initial returns the first character of label, for the instance mark.
func initial(label string) string {
	for _, r := range label {
		return string(r)
	}
	return ""
}

// instanceView is the "/instances.json" entry the switcher reads.
type instanceView struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Color   string `json:"color,omitempty"`
	Base    string `json:"base"`
	Healthy bool   `json:"healthy"`
	Warning string `json:"warning,omitempty"`
}

func instanceViews(instances []Instance, statuses []Status) []instanceView {
	views := make([]instanceView, len(instances))
	for idx, inst := range instances {
		views[idx] = instanceView{
			Name:    inst.Name,
			Label:   inst.Label,
			Color:   inst.Color,
			Base:    inst.Base(),
			Healthy: statuses[idx].Healthy,
			Warning: statuses[idx].Warning,
		}
	}
	return views
}

func handleInstancesJSON(views func(*http.Request) []instanceView, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := json.Marshal(views(r))
		if err != nil {
			logger.ErrorContext(r.Context(), "failed to encode instances", slog.Any("err", err))
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		writeBody(w, r, logger, body)
	}
}

func handleLanding(views func(*http.Request) []instanceView, stylesheet, icon string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := landingData{Stylesheet: stylesheet, Icon: icon, Instances: views(r)}
		var buf bytes.Buffer
		if err := landingTemplate.Execute(&buf, data); err != nil {
			logger.ErrorContext(r.Context(), "failed to render landing page", slog.Any("err", err))
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		writeBody(w, r, logger, buf.Bytes())
	}
}

func handleHealthz(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writeBody(w, r, logger, []byte("ok\n"))
	}
}

func writeBody(w http.ResponseWriter, r *http.Request, logger *slog.Logger, body []byte) {
	if _, err := w.Write(body); err != nil {
		logger.WarnContext(r.Context(), "failed to write response", slog.Any("err", err))
	}
}
