package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/execution"
)

func (s *Server) submitExecution(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		writeError(w, http.StatusServiceUnavailable, "execution_provider_unavailable", "Sandbox Manager Docker provider is not configured")
		return
	}
	var req struct {
		RequestID   string             `json:"request_id"`
		Mode        execution.Mode     `json:"mode"`
		CPUMillis   uint32             `json:"cpu_millis"`
		MemoryBytes uint64             `json:"memory_bytes"`
		PidsLimit   uint64             `json:"pids_limit"`
		Command     execution.ExecSpec `json:"command"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	q := execution.QueuedExecution{Kind: execution.TrustedExecutionKind, Mode: req.Mode, CPUMillis: req.CPUMillis, MemoryBytes: req.MemoryBytes, PidsLimit: req.PidsLimit, Command: req.Command}
	if err := q.Normalize(); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}
	if requestID == "" || len(requestID) > 128 {
		writeError(w, 400, "invalid_request", "request_id or Idempotency-Key must contain 1..128 bytes")
		return
	}
	payload, err := json.Marshal(q)
	if err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	task, err := s.store.CreateTask(r.Context(), r.PathValue("id"), requestID, string(payload))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}
