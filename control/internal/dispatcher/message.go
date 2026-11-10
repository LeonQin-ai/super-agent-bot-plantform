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
type ScopedMessageReply func(context.Context, string, string, string, string) error
type AgentRunner func(context.Context, string, string, string) (string, error)

// ConfigureMessages must be called before the dispatcher starts.
func (d *Dispatcher) ConfigureMessages(runner MessageRunner, reply MessageReply) {
	d.messages, d.reply = runner, reply
}

func (d *Dispatcher) ConfigureScopedReplies(reply ScopedMessageReply) {
	d.scopedReply = reply
}

// ConfigureAgentRuntime selects a Cognitive OS runtime for API and Feishu tasks.
func (d *Dispatcher) ConfigureAgentRuntime(run AgentRunner) {
	d.agentRuntime = run
}

func (d *Dispatcher) dispatchMessage(parent context.Context, task model.Task) bool {
	var payload struct {
		Source, Content string
		MessageID       string `json:"message_id"`
		FeishuRoute     string `json:"feishu_route"`
	}
	if json.Unmarshal([]byte(task.Payload), &payload) != nil {
		return false
	}
	switch payload.Source {
	case "feishu":
		if d.messages == nil && d.agentRuntime == nil {
			return false
		}
	case "api":
		if d.agentRuntime == nil {
			return false
		}
	default:
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
	var answer string
	var err error
	if d.agentRuntime != nil {
		var agent model.Agent
		agent, err = d.store.GetAgent(ctx, task.AgentID)
		if err == nil {
			answer, err = d.agentRuntime(ctx, task.ID, agent.WorkspaceID, payload.Content)
			if err == nil {
				d.logger.Info("Cognitive OS task completed", "task_id", task.ID, "source", payload.Source)
			}
		}
	} else {
		answer, err = d.messages.Run(ctx, payload.Content, checksum)
	}
	runContextErr := ctx.Err()
	cancel()
	<-watchExited
	persist, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err != nil {
		d.logger.Warn("Agent message task failed", "task_id", task.ID, "source", payload.Source, "error", err)
		answer = "任务执行失败。任务编号 " + task.ID + "。"
		var reported interface{ UserMessage() string }
		if errors.As(err, &reported) && strings.TrimSpace(reported.UserMessage()) != "" {
			answer = "任务未完成。\n" + reported.UserMessage() + "\n任务编号 " + task.ID + "。"
		} else if errors.Is(runContextErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			answer = "任务执行超时。任务编号 " + task.ID + "。"
		}
		err = d.store.FailTask(persist, task.ID, "message_execution_failed", answer)
	} else {
		err = d.store.CompleteTask(persist, task.ID, answer)
	}
	if err != nil {
		if !errors.Is(err, store.ErrConflict) {
			d.logger.Error("persist Agent message result", "task_id", task.ID, "error", err)
		}
		return true
	}
	if payload.Source != "feishu" || payload.MessageID == "" || parent.Err() != nil {
		return true
	}
	replyCtx, replyDone := context.WithTimeout(parent, 22*time.Second)
	defer replyDone()
	switch {
	case payload.FeishuRoute != "" && d.scopedReply != nil:
		err = d.scopedReply(replyCtx, payload.FeishuRoute, payload.MessageID, task.ID, answer)
	case payload.FeishuRoute == "" && d.reply != nil:
		err = d.reply(replyCtx, payload.MessageID, task.ID, answer)
	default:
		return true
	}
	if err != nil {
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
