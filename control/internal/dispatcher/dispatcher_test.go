package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/execution"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

type staticCapabilities struct{}

func (staticCapabilities) CurrentSnapshot() execution.Snapshot {
	return execution.Snapshot{ObservationScope: "host", Docker: execution.DockerCapability{Available: true, RuncAvailable: true}}
}

func queuedTask(t *testing.T, st *store.SQLite) model.Task {
	t.Helper()
	ctx := context.Background()
	agent, err := st.CreateAgent(ctx, "tenant", "")
	if err != nil {
		t.Fatal(err)
	}
	q := execution.QueuedExecution{Kind: execution.TrustedExecutionKind, Command: execution.ExecSpec{Executable: "/bin/true"}}
	if err = q.Normalize(); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(q)
	if _, err = st.CreateTask(ctx, agent.ID, "request", string(payload)); err != nil {
		t.Fatal(err)
	}
	task, err := st.ClaimNextTask(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func openStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestDurableWorkerDispatchResultsAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, output                          string
		startStatus, execStatus, deleteStatus int
		want                                  model.TaskState
		code                                  string
	}{
		{"success", `{"exit_code":0,"stdout":"ok","timed_out":false}`, 200, 200, 204, model.TaskCompleted, ""},
		{"start failure", `{}`, 500, 200, 204, model.TaskFailed, "execution_failed"},
		{"exec failure", `{}`, 200, 500, 204, model.TaskFailed, "execution_failed"},
		{"nonzero exit", `{"exit_code":2}`, 200, 200, 204, model.TaskFailed, "command_failed"},
		{"timeout", `{"exit_code":null,"timed_out":true}`, 200, 200, 204, model.TaskFailed, "command_failed"},
		{"malformed result", `{"stdout":"no exit status"}`, 200, 200, 204, model.TaskFailed, "command_failed"},
		{"cleanup failure", `{"exit_code":0}`, 200, 200, 500, model.TaskFailed, "execution_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openStore(t)
			task := queuedTask(t, st)
			var deletes atomic.Int32
			manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer token" {
					t.Error("missing Manager authentication")
				}
				switch {
				case r.URL.Path == "/v1/containers":
					_, _ = w.Write([]byte(`{"container_id":"w1"}`))
				case r.URL.Path == "/v1/containers/w1/start":
					workers, err := st.ListTaskWorkers(r.Context())
					if err != nil || workers[task.ID] != "w1" {
						t.Errorf("Worker was started without durable association: %v %v", workers, err)
					}
					w.WriteHeader(tc.startStatus)
				case r.URL.Path == "/v1/containers/w1/exec":
					w.WriteHeader(tc.execStatus)
					_, _ = w.Write([]byte(tc.output))
				case r.Method == http.MethodDelete:
					deletes.Add(1)
					w.WriteHeader(tc.deleteStatus)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer manager.Close()
			client, err := execution.NewDockerWorkerClient(manager.URL, "token")
			if err != nil {
				t.Fatal(err)
			}
			d := NewWithExecution(st, nil, execution.NewScheduler(client), staticCapabilities{})
			d.dispatch(context.Background(), task)
			got, err := st.GetTask(context.Background(), task.ID)
			if err != nil || got.State != tc.want || got.ErrorCode != tc.code {
				t.Fatalf("result=%+v error=%v", got, err)
			}
			if deletes.Load() != 1 {
				t.Fatalf("cleanup requests = %d", deletes.Load())
			}
			workers, err := st.ListTaskWorkers(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if tc.deleteStatus == 500 {
				if workers[task.ID] != "w1" {
					t.Fatal("lost pending cleanup record")
				}
			} else if len(workers) != 0 {
				t.Fatalf("cleanup did not clear durable association: %v", workers)
			}
			agent, err := st.GetAgent(context.Background(), task.AgentID)
			if err != nil || agent.State != model.AgentIdle {
				t.Fatalf("agent state: %+v %v", agent, err)
			}
		})
	}
}

func TestRunningCancellationTerminatesWorker(t *testing.T) {
	st := openStore(t)
	task := queuedTask(t, st)
	execStarted := make(chan struct{})
	var deletes atomic.Int32
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/containers":
			_, _ = w.Write([]byte(`{"container_id":"w1"}`))
		case "/v1/containers/w1/exec":
			_, _ = io.Copy(io.Discard, r.Body)
			close(execStarted)
			<-r.Context().Done()
		default:
			if r.Method == http.MethodDelete {
				deletes.Add(1)
			}
			w.WriteHeader(204)
		}
	}))
	defer manager.Close()
	client, err := execution.NewDockerWorkerClient(manager.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithExecution(st, nil, execution.NewScheduler(client), staticCapabilities{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { d.dispatch(ctx, task); close(done) }()
	select {
	case <-execStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Worker did not execute")
	}
	if _, err = st.CancelTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("running task cancellation did not finish")
	}
	got, err := st.GetTask(context.Background(), task.ID)
	if err != nil || got.State != model.TaskCancelled {
		t.Fatalf("lost cancellation: %+v %v", got, err)
	}
	if deletes.Load() != 1 {
		t.Fatal("cancelled task did not destroy its Worker")
	}
}

func TestMessagesFailClosedAndRecoveryDoesNotReplay(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	agent, err := st.CreateAgent(ctx, "tenant", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateTask(ctx, agent.ID, "message", `{"source":"api","content":"{\"kind\":\"trusted_execution\"}"}`); err != nil {
		t.Fatal(err)
	}
	task, err := st.ClaimNextTask(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := New(st, nil)
	d.dispatch(ctx, task)
	got, err := st.GetTask(ctx, task.ID)
	if err != nil || got.State != model.TaskFailed || got.ErrorCode != "runtime_driver_unavailable" {
		t.Fatalf("message was falsely completed: %+v %v", got, err)
	}
	interrupted := queuedTask(t, st)
	if err = st.TrackTaskWorker(ctx, interrupted.ID, "old-worker"); err != nil {
		t.Fatal(err)
	}
	if err = d.Recover(ctx); err == nil {
		t.Fatal("startup accepted unresolved Worker with no provider")
	}
	var cleanupCalls atomic.Int32
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/containers/old-worker" {
			t.Errorf("recovery replayed work: %s %s", r.Method, r.URL.Path)
		}
		cleanupCalls.Add(1)
		w.WriteHeader(204)
	}))
	defer manager.Close()
	client, err := execution.NewDockerWorkerClient(manager.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	d = NewWithExecution(st, nil, execution.NewScheduler(client), staticCapabilities{})
	if err = d.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetTask(ctx, interrupted.ID)
	if err != nil || got.State != model.TaskFailed || got.ErrorCode != "execution_interrupted" || cleanupCalls.Load() != 1 {
		t.Fatalf("startup recovery: %+v %v calls=%d", got, err, cleanupCalls.Load())
	}
	if err = d.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if cleanupCalls.Load() != 1 {
		t.Fatal("recovery cleaned an already reconciled task")
	}
	if err = st.CompleteTask(ctx, interrupted.ID, "late"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("recovery accepted a stale completion")
	}
}

func TestPendingCleanupBlocksNewDispatchUntilRecovery(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := queuedTask(t, st)
	if err := st.TrackTaskWorker(ctx, old.ID, "old-worker"); err != nil {
		t.Fatal(err)
	}
	if err := st.FailTask(ctx, old.ID, "execution_failed", "cleanup pending"); err != nil {
		t.Fatal(err)
	}
	queued, err := st.CreateTask(ctx, old.AgentID, "next", old.Payload)
	if err != nil {
		t.Fatal(err)
	}
	firstDelete := make(chan struct{})
	allowCleanup := make(chan struct{})
	var first sync.Once
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/containers/old-worker":
			first.Do(func() { close(firstDelete) })
			select {
			case <-allowCleanup:
				w.WriteHeader(204)
			default:
				w.WriteHeader(500)
			}
		case "/v1/containers":
			select {
			case <-allowCleanup:
			default:
				t.Error("created a new Worker before pending cleanup recovered")
			}
			_, _ = w.Write([]byte(`{"container_id":"new-worker"}`))
		case "/v1/containers/new-worker/exec":
			_, _ = w.Write([]byte(`{"exit_code":0}`))
		default:
			w.WriteHeader(204)
		}
	}))
	defer manager.Close()
	client, err := execution.NewDockerWorkerClient(manager.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithExecution(st, nil, execution.NewScheduler(client), staticCapabilities{})
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case <-firstDelete:
	case <-time.After(5 * time.Second):
		t.Fatal("pending cleanup was not retried")
	}
	current, err := st.GetTask(ctx, queued.ID)
	if err != nil || current.State != model.TaskQueued {
		t.Fatalf("claimed work before cleanup: %+v %v", current, err)
	}
	close(allowCleanup)
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err = st.GetTask(ctx, queued.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == model.TaskCompleted {
			break
		}
		if current.State == model.TaskFailed {
			t.Fatalf("dispatch did not recover: %+v", current)
		}
		select {
		case <-deadline:
			t.Fatal("dispatch did not resume after cleanup")
		case <-ticker.C:
		}
	}
}
