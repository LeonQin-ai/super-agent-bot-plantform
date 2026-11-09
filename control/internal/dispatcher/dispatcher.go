package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/execution"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

type Dispatcher struct {
	store        store.Store
	logger       *slog.Logger
	scheduler    *execution.Scheduler
	capabilities execution.CapabilitySource
	messages     MessageRunner
	reply        MessageReply
}

func New(st store.Store, logger *slog.Logger) *Dispatcher {
	return NewWithExecution(st, logger, nil, nil)
}

func NewWithExecution(st store.Store, logger *slog.Logger, scheduler *execution.Scheduler, source execution.CapabilitySource) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{store: st, logger: logger, scheduler: scheduler, capabilities: source}
}

// Recover runs before admission on a single-owner database. Destroy recorded
// Workers before marking interrupted tasks failed; never replay uncertain work.
func (d *Dispatcher) Recover(ctx context.Context) error {
	if err := d.cleanupWorkers(ctx); err != nil {
		return err
	}
	return d.store.InterruptRunningTasks(ctx)
}

func (d *Dispatcher) cleanupWorkers(ctx context.Context) error {
	workers, err := d.store.ListTaskWorkers(ctx)
	if err != nil {
		return err
	}
	for taskID, workerID := range workers {
		cleanupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = d.scheduler.CleanupWorker(cleanupCtx, workerID)
		cancel()
		if err != nil {
			return fmt.Errorf("reconcile Worker for task %s: %w", taskID, err)
		}
		if err = d.store.ClearTaskWorker(ctx, taskID); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		// Retry unresolved cleanup before claiming another task. A failed delete
		// must not allow an unlimited accumulation of live Workers.
		if err := d.cleanupWorkers(ctx); err != nil {
			if ctx.Err() == nil {
				d.logger.Error("pending Worker cleanup blocks dispatch", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			continue
		}
		task, err := d.store.ClaimNextTask(ctx)
		if err == nil {
			d.dispatch(ctx, task)
			continue
		}
		if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			d.logger.Error("claim queued task", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) fail(task model.Task, code, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.store.FailTask(ctx, task.ID, code, message); err != nil && !errors.Is(err, store.ErrConflict) {
		d.logger.Error("persist task failure", "task_id", task.ID, "error", err)
	}
}

func (d *Dispatcher) dispatch(parent context.Context, task model.Task) {
	var q execution.QueuedExecution
	if err := json.Unmarshal([]byte(task.Payload), &q); err != nil {
		d.fail(task, "invalid_task_payload", "Task payload is not valid JSON")
		return
	}
	if q.Kind != execution.TrustedExecutionKind {
		if q.Kind == "" && d.messages != nil && d.dispatchMessage(parent, task) {
			return
		}
		d.fail(task, "runtime_driver_unavailable", "No restricted Cognitive OS message driver is configured")
		return
	}
	if err := q.Normalize(); err != nil {
		d.fail(task, "invalid_execution_request", err.Error())
		return
	}
	if d.scheduler == nil || d.capabilities == nil {
		d.fail(task, "execution_provider_unavailable", "Docker Worker provider is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(parent, 6*time.Minute)
	defer cancel()
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := d.store.GetTask(ctx, task.ID)
				if err != nil || current.State != model.TaskRunning {
					cancel()
					return
				}
			}
		}
	}()
	result, err := d.scheduler.Run(ctx, execution.RunRequest{
		Mode: q.Mode, Policy: execution.TaskPolicy{Tier: execution.TierTrustedInternal}, Host: d.capabilities.CurrentSnapshot(),
		Worker: execution.WorkerSpec{WorkerID: task.ID, WorkspaceID: task.ID, CPUMillis: q.CPUMillis, MemoryBytes: q.MemoryBytes, PidsLimit: q.PidsLimit}, Command: q.Command,
		OnWorkerCreated: func(ctx context.Context, w execution.WorkerRecord) error {
			return d.store.TrackTaskWorker(ctx, task.ID, w.ContainerID)
		},
		OnWorkerCleaned: func(ctx context.Context) error { return d.store.ClearTaskWorker(ctx, task.ID) },
	})
	close(watchDone)
	<-watchExited
	if err != nil {
		d.fail(task, "execution_failed", err.Error())
		return
	}
	var commandResult struct {
		ExitCode *int `json:"exit_code"`
		TimedOut bool `json:"timed_out"`
	}
	if err = json.Unmarshal(result.Result, &commandResult); err != nil || commandResult.ExitCode == nil || commandResult.TimedOut || *commandResult.ExitCode != 0 {
		d.fail(task, "command_failed", string(result.Result))
		return
	}
	payload, err := json.Marshal(result)
	if err != nil {
		d.fail(task, "invalid_execution_result", err.Error())
		return
	}
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer persistCancel()
	if err = d.store.CompleteTask(persistCtx, task.ID, string(payload)); err != nil && !errors.Is(err, store.ErrConflict) {
		d.logger.Error("persist execution result", "task_id", task.ID, "error", err)
	}
}
