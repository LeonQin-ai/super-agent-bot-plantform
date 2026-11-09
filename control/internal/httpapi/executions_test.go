package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/dispatcher"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/execution"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

func TestQueuedExecutionAdmissionAndDispatch(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	agent, err := st.CreateAgent(context.Background(), "tenant", "")
	if err != nil {
		t.Fatal(err)
	}
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/containers":
			_, _ = w.Write([]byte(`{"container_id":"w1"}`))
		case "/v1/containers/w1/exec":
			_, _ = w.Write([]byte(`{"exit_code":0,"stdout":"durable-ok","timed_out":false}`))
		default:
			w.WriteHeader(204)
		}
	}))
	defer manager.Close()
	client, err := execution.NewDockerWorkerClient(manager.URL, "manager-token")
	if err != nil {
		t.Fatal(err)
	}
	registry := execution.NewPersistentCapabilityRegistry(time.Minute, func(context.Context) (execution.Snapshot, error) {
		return execution.Snapshot{ObservationScope: "host", Docker: execution.DockerCapability{Available: true, RuncAvailable: true}}, nil
	}, st)
	if err = registry.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	scheduler := execution.NewScheduler(client)
	handler := NewWithCapabilitySource(st, nil, nil, "api-token", registry, scheduler)
	post := func(body, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/agents/"+agent.ID+"/executions", bytes.NewBufferString(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	body := `{"request_id":"queue-1","command":{"executable":"/bin/echo","argv":["durable-ok"]}}`
	if res := post(body, ""); res.Code != 401 {
		t.Fatalf("unauthenticated admission: %d", res.Code)
	}
	res := post(body, "api-token")
	if res.Code != 202 {
		t.Fatalf("admission: %d %s", res.Code, res.Body.String())
	}
	var task model.Task
	if err = json.Unmarshal(res.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if task.State != model.TaskQueued {
		t.Fatalf("admission was not durable: %+v", task)
	}
	repeated := post(body, "api-token")
	var duplicate model.Task
	if err = json.Unmarshal(repeated.Body.Bytes(), &duplicate); err != nil || duplicate.ID != task.ID {
		t.Fatalf("idempotent admission: %s %v", repeated.Body.String(), err)
	}
	if res = post(`{"request_id":"queue-1","command":{"executable":"/bin/false"}}`, "api-token"); res.Code != 409 {
		t.Fatalf("conflicting retry status=%d", res.Code)
	}
	for _, invalid := range []string{
		`{"command":{"executable":"/bin/true"}}`,
		`{"request_id":"bad","command":{"executable":"/bin/true","network":true}}`,
		`{"request_id":"bad","command":{"executable":"relative"}}`,
		`{"request_id":"bad","trust_tier":"external_untrusted","command":{"executable":"/bin/true"}}`,
		`{"request_id":"bad","command":{"executable":"/bin/true","cwd":"../host"}}`,
	} {
		if res = post(invalid, "api-token"); res.Code != 400 {
			t.Fatalf("invalid admission accepted: %s -> %d %s", invalid, res.Code, res.Body.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	d := dispatcher.NewWithExecution(st, nil, scheduler, registry)
	if err = d.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { d.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		req := httptest.NewRequest(http.MethodGet, "/v1/tasks/"+task.ID, nil)
		req.Header.Set("Authorization", "Bearer api-token")
		res = httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var current model.Task
		if err = json.Unmarshal(res.Body.Bytes(), &current); err != nil {
			t.Fatal(err)
		}
		if current.State == model.TaskCompleted {
			if !bytes.Contains([]byte(current.Result), []byte("durable-ok")) {
				t.Fatalf("lost execution output: %s", current.Result)
			}
			break
		}
		if current.State == model.TaskFailed {
			t.Fatalf("queued execution failed: %+v", current)
		}
		select {
		case <-deadline:
			t.Fatal("queued execution never completed")
		case <-ticker.C:
		}
	}
}
