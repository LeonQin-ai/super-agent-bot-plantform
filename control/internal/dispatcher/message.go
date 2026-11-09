package dispatcher

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/execution"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

type MessageRunner interface {
	Run(context.Context, string, func(context.Context, string) (string, error)) (string, error)
}

type MessageReply func(context.Context, string, string, string) error

// ConfigureMessages must be called before the dispatcher starts.
func (d *Dispatcher) ConfigureMessages(runner MessageRunner, reply MessageReply) {
	d.messages, d.reply = runner, reply
}

func (d *Dispatcher) dispatchMessage(parent context.Context, task model.Task) bool {
	var payload struct {
		Source, Content string
		MessageID       string `json:"message_id"`
	}
	if json.Unmarshal([]byte(task.Payload), &payload) != nil || payload.Source != "feishu" {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Minute)
	defer cancel()
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
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
	var checksum func(context.Context, string) (string, error)
	if d.scheduler != nil && d.capabilities != nil {
		checksum = func(ctx context.Context, text string) (string, error) {
			return d.checksum(ctx, task, text)
		}
	}
	answer, err := d.messages.Run(ctx, payload.Content, checksum)
	cancel()
	<-watchExited
	persist, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err != nil {
		d.logger.Warn("Feishu task failed", "task_id", task.ID, "error", err)
		answer = "任务执行失败。请稍后再试。"
		err = d.store.FailTask(persist, task.ID, "message_execution_failed", answer)
	} else {
		err = d.store.CompleteTask(persist, task.ID, answer)
	}
	if err != nil {
		if !errors.Is(err, store.ErrConflict) {
			d.logger.Error("persist Feishu task result", "task_id", task.ID, "error", err)
		}
		return true
	}
	if d.reply == nil || payload.MessageID == "" || parent.Err() != nil {
		return true
	}
	replyCtx, replyDone := context.WithTimeout(parent, 22*time.Second)
	defer replyDone()
	if err := d.reply(replyCtx, payload.MessageID, task.ID, answer); err != nil {
		d.logger.Warn("Feishu task reply failed", "task_id", task.ID, "error", err)
	} else {
		d.logger.Info("Feishu task reply delivered", "task_id", task.ID)
	}
	return true
}

func (d *Dispatcher) checksum(ctx context.Context, task model.Task, text string) (string, error) {
	agent, err := d.store.GetAgent(ctx, task.AgentID)
	if err != nil {
		return "", errors.New("sandbox Agent unavailable")
	}
	// The program is fixed. The model supplies only the data argument.
	result, err := d.scheduler.Run(ctx, execution.RunRequest{
		Mode: execution.ModeStrict, Policy: execution.TaskPolicy{Tier: execution.TierTrustedInternal},
		Host:   d.capabilities.CurrentSnapshot(),
		Worker: execution.WorkerSpec{WorkerID: task.ID, WorkspaceID: agent.WorkspaceID, CPUMillis: 250, MemoryBytes: 128 << 20, PidsLimit: 16},
		Command: execution.ExecSpec{Executable: "/bin/sh", Argv: []string{"-c", `printf '%s' "$1" | /usr/bin/sha256sum`, "sha256", text},
			CWD: ".", TimeoutMS: 5000, MemoryBytes: 64 << 20, MaxOutputBytes: 1024},
		OnWorkerCreated: func(ctx context.Context, w execution.WorkerRecord) error {
			return d.store.TrackTaskWorker(ctx, task.ID, w.ContainerID)
		},
		OnWorkerCleaned: func(ctx context.Context) error { return d.store.ClearTaskWorker(ctx, task.ID) },
	})
	if err != nil {
		return "", errors.New("sandbox checksum execution failed")
	}
	var output struct {
		ExitCode *int   `json:"exit_code"`
		TimedOut bool   `json:"timed_out"`
		Stdout   string `json:"stdout"`
	}
	if json.Unmarshal(result.Result, &output) != nil || output.ExitCode == nil || *output.ExitCode != 0 || output.TimedOut {
		return "", errors.New("sandbox checksum command failed")
	}
	fields := strings.Fields(output.Stdout)
	if len(fields) == 0 || len(fields[0]) != 64 {
		return "", errors.New("invalid checksum output")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", errors.New("invalid checksum output")
	}
	d.logger.Info("Feishu sandbox checksum completed", "task_id", task.ID)
	return fields[0], nil
}
