package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/execution"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
)

func TestCapabilityJournalSurvivesRestartWithoutRestoringAuthorization(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	host := execution.Snapshot{ObservationScope: "host", Docker: execution.DockerCapability{Available: true, RuncAvailable: true}}
	registry := execution.NewPersistentCapabilityRegistry(time.Minute, func(context.Context) (execution.Snapshot, error) { return host, nil }, st)
	if err = registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	first := registry.CurrentSnapshot().RegistryVersion
	if first == 0 {
		t.Fatal("observation has no durable version")
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	latest, err := st.LatestCapabilitySnapshot(ctx)
	if err != nil || latest.RegistryVersion != first || latest.ObservationScope != "host" {
		t.Fatalf("lost journal: %+v %v", latest, err)
	}
	restarted := execution.NewPersistentCapabilityRegistry(time.Minute, func(context.Context) (execution.Snapshot, error) { return host, nil }, st)
	if restarted.CurrentSnapshot().ObservationScope == "host" {
		t.Fatal("persisted history authorized execution after restart")
	}
	if err = restarted.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if restarted.CurrentSnapshot().RegistryVersion <= first {
		t.Fatal("observation version did not increase after restart")
	}
	// A cancelled refresh persists a negative observation even though its probe
	// context can no longer be used for database writes.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err = restarted.Refresh(cancelCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled refresh: %v", err)
	}
	latest, err = st.LatestCapabilitySnapshot(ctx)
	if err != nil || latest.ObservationScope != "unavailable" {
		t.Fatalf("negative observation not persisted: %+v %v", latest, err)
	}
}

func TestRunningCancellationAndStartupReconciliation(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	agent, err := st.CreateAgent(ctx, "tenant", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.CreateTask(ctx, agent.ID, "first", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimNextTask(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.TrackTaskWorker(ctx, first.ID, "worker1"); err != nil {
		t.Fatal(err)
	}
	if err = st.InterruptRunningTasks(ctx); err == nil {
		t.Fatal("reconciled before Worker cleanup")
	}
	if _, err = st.CancelTask(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.CompleteTask(ctx, first.ID, "late response"); !errors.Is(err, ErrConflict) {
		t.Fatalf("late response overwrote cancellation: %v", err)
	}
	if err = st.FailTask(ctx, first.ID, "late_failure", "late error"); !errors.Is(err, ErrConflict) {
		t.Fatalf("failure overwrote cancellation: %v", err)
	}
	if err = st.TrackTaskWorker(ctx, first.ID, "worker2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("started cancelled task: %v", err)
	}
	if err = st.ClearTaskWorker(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateTask(ctx, agent.ID, "second", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimNextTask(ctx); err != nil {
		t.Fatal(err)
	}
	queued, err := st.CreateTask(ctx, agent.ID, "queued", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.InterruptRunningTasks(ctx); err != nil {
		t.Fatal(err)
	}
	interrupted, err := st.GetTask(ctx, second.ID)
	if err != nil || interrupted.State != model.TaskFailed || interrupted.ErrorCode != "execution_interrupted" {
		t.Fatalf("interrupted task: %+v %v", interrupted, err)
	}
	stillQueued, err := st.GetTask(ctx, queued.ID)
	if err != nil || stillQueued.State != model.TaskQueued {
		t.Fatalf("queued task was changed: %+v %v", stillQueued, err)
	}
	updated, err := st.GetAgent(ctx, agent.ID)
	if err != nil || updated.State != model.AgentIdle {
		t.Fatalf("agent state not reconciled: %+v %v", updated, err)
	}
}
