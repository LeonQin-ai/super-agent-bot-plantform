package store

import (
	"context"
	"errors"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("state conflict")
)

type Store interface {
	CreateAgent(context.Context, string, string) (model.Agent, error)
	GetAgent(context.Context, string) (model.Agent, error)
	CreateTask(context.Context, string, string, string) (model.Task, error)
	GetTask(context.Context, string) (model.Task, error)
	CancelTask(context.Context, string) (model.Task, error)
	ClaimNextTask(context.Context) (model.Task, error)
	CompleteTask(context.Context, string, string) error
	FailTask(context.Context, string, string, string) error
	InterruptRunningTasks(context.Context) error
	TrackTaskWorker(context.Context, string, string) error
	ListTaskWorkers(context.Context) (map[string]string, error)
	ClearTaskWorker(context.Context, string) error
	SetAgentState(context.Context, string, model.AgentState) error
	IngestFeishuEvent(context.Context, string, string, string, string) (model.Task, bool, error)
	GetRuntime(context.Context, string) (model.Runtime, error)
	BeginWake(context.Context, string) (model.Runtime, bool, error)
	MarkRuntimeReady(context.Context, model.Runtime) error
	BeginSleep(context.Context, string) (model.Runtime, bool, error)
	MarkRuntimeStopped(context.Context, model.Runtime) error
	FailRuntime(context.Context, model.Runtime, string) error
	Close() error
}
