package httpapi

import (
	"embed"
	"net/http"
	"strings"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

//go:embed dashboard/*
var dashboard embed.FS

func (s *Server) EnableDashboardRuntime() { s.dashboardRuntime = true }

func (s *Server) registerDashboard() {
	assets := http.FileServer(http.FS(dashboard))
	s.mux.Handle("GET /dashboard/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		assets.ServeHTTP(w, r)
	}))
	s.mux.HandleFunc("GET /v1/agents", func(w http.ResponseWriter, r *http.Request) {
		agents, err := s.store.Agents(r.Context(), r.URL.Query().Get("after"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"agents": agents, "runtime_configured": s.dashboardRuntime})
	})
	s.mux.HandleFunc("GET /v1/agents/{id}/tasks", func(w http.ResponseWriter, r *http.Request) {
		tasks, err := s.store.Tasks(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, 200, tasks)
	})
	s.mux.HandleFunc("POST /v1/messages/broadcast", s.broadcast)
	s.mux.HandleFunc("GET /v1/schedules", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.store.Schedules(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, 200, items)
	})
	s.mux.HandleFunc("POST /v1/schedules", s.addSchedule)
	s.mux.HandleFunc("DELETE /v1/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.RemoveSchedule(r.Context(), r.PathValue("id")); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(204)
	})
}

func (s *Server) broadcast(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content   string `json:"content"`
		RequestID string `json:"request_id"`
	}
	if !s.dashboardRuntime {
		writeError(w, 503, "runtime_driver_unavailable", "light-agent runtime is not configured")
		return
	}
	if err := decodeJSON(w, r, &body); err != nil || !validMessage(body.Content) || strings.TrimSpace(body.RequestID) == "" || len(body.RequestID) > 100 {
		writeError(w, 400, "invalid_request", "content and request_id are required")
		return
	}
	count, err := s.store.Broadcast(r.Context(), body.RequestID, strings.TrimSpace(body.Content))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 202, map[string]int64{"queued": count})
}

func (s *Server) addSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content  string    `json:"content"`
		Minutes  int64     `json:"minutes"`
		FirstRun time.Time `json:"first_run"`
	}
	if !s.dashboardRuntime {
		writeError(w, 503, "runtime_driver_unavailable", "light-agent runtime is not configured")
		return
	}
	if err := decodeJSON(w, r, &body); err != nil || !validMessage(body.Content) || body.Minutes < 1 || body.Minutes > 525600 || !body.FirstRun.After(time.Now()) {
		writeError(w, 400, "invalid_request", "provide a message, a future first run and an interval of 1–525600 minutes")
		return
	}
	item, err := s.store.AddSchedule(r.Context(), store.Schedule{Content: strings.TrimSpace(body.Content), Minutes: body.Minutes, NextRun: body.FirstRun.Unix()})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 201, item)
}

func validMessage(content string) bool {
	return strings.TrimSpace(content) != "" && len(content) <= 16<<10
}
