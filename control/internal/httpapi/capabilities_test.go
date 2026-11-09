package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/execution"
)

func TestLiveCapabilitiesDisableAndRecoverExecution(t *testing.T) {
	var creates atomic.Int32
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer manager-token" {
			t.Error("missing manager authentication")
		}
		switch r.URL.Path {
		case "/v1/containers":
			creates.Add(1)
			_, _ = w.Write([]byte(`{"container_id":"worker1","state":"CREATED"}`))
		case "/v1/containers/worker1/exec":
			_, _ = w.Write([]byte(`{"stdout":"ok"}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer manager.Close()
	client, err := execution.NewDockerWorkerClient(manager.URL, "manager-token")
	if err != nil {
		t.Fatal(err)
	}
	fail := false
	registry := execution.NewCapabilityRegistry(time.Minute, func(context.Context) (execution.Snapshot, error) {
		if fail {
			return execution.Snapshot{}, errors.New("manager unavailable")
		}
		return execution.Snapshot{ObservationScope: "host", Docker: execution.DockerCapability{Available: true, RuncAvailable: true}}, nil
	})
	handler := NewWithCapabilitySource(nil, nil, nil, "api-token", registry, execution.NewScheduler(client))
	run := func(want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/execution/run", bytes.NewBufferString(`{"trust_tier":"trusted_internal","task_id":"task1","executable":"/bin/true"}`))
		req.Header.Set("Authorization", "Bearer api-token")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("run status=%d want=%d body=%s", res.Code, want, res.Body.String())
		}
	}
	run(http.StatusUnprocessableEntity)
	if err = registry.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	run(http.StatusOK)
	fail = true
	if err = registry.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh failure")
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/runtime/capabilities", nil)
	req.Header.Set("Authorization", "Bearer api-token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if !bytes.Contains(res.Body.Bytes(), []byte(`"observation_scope":"unavailable"`)) {
		t.Fatalf("stale capability API: %s", res.Body.String())
	}
	run(http.StatusUnprocessableEntity)
	if creates.Load() != 1 {
		t.Fatalf("unavailable capabilities created a Worker: %d", creates.Load())
	}
	fail = false
	if err = registry.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	run(http.StatusOK)
	if creates.Load() != 2 {
		t.Fatalf("recovery did not create a Worker: %d", creates.Load())
	}
}
