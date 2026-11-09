package lifecycle

import (
	"context"
	"errors"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

var ErrDriverUnavailable = errors.New("runtime driver is not configured")

// Driver owns only the trusted SuperAgent runtime lifecycle. It must not expose
// a generic host shell or execute user workloads; those belong in Worker Guests.
type Driver interface {
	Start(context.Context, model.Agent, model.Runtime) error
	Stop(context.Context, model.Agent, model.Runtime) error
}

type Manager struct { store store.Store; driver Driver }

func New(st store.Store, driver Driver) *Manager { return &Manager{store:st,driver:driver} }

func (m *Manager) Wake(ctx context.Context, agentID string) (model.Runtime,error) {
	if m==nil || m.driver==nil { return model.Runtime{},ErrDriverUnavailable }
	agent,err:=m.store.GetAgent(ctx,agentID);if err!=nil{return model.Runtime{},err}
	runtime,started,err:=m.store.BeginWake(ctx,agentID);if err!=nil{return model.Runtime{},err}
	if !started { return runtime,nil }
	// BeginWake has already committed the new fencing generation. Pass that
	// generation to the driver, not the stale Agent snapshot read beforehand.
	agent.Generation=runtime.Generation
	agent.State=model.AgentWaking
	if err=m.driver.Start(ctx,agent,runtime);err!=nil {
		_ = m.store.FailRuntime(context.WithoutCancel(ctx),runtime,err.Error())
		return model.Runtime{},err
	}
	if err=m.store.MarkRuntimeReady(ctx,runtime);err!=nil {
		// If the caller timed out, persist the failed reservation when it still
		// belongs to this generation. A stale generation cannot alter newer state.
		_ = m.store.FailRuntime(context.WithoutCancel(ctx),runtime,err.Error())
		// A late-starting old runtime must not leak after a newer wake.
		_ = m.driver.Stop(context.WithoutCancel(ctx),agent,runtime)
		return model.Runtime{},err
	}
	return m.store.GetRuntime(ctx,agentID)
}

func (m *Manager) Sleep(ctx context.Context,agentID string)(model.Runtime,error) {
	if m==nil || m.driver==nil { return model.Runtime{},ErrDriverUnavailable }
	agent,err:=m.store.GetAgent(ctx,agentID);if err!=nil{return model.Runtime{},err}
	runtime,stopping,err:=m.store.BeginSleep(ctx,agentID);if err!=nil{return model.Runtime{},err}
	if !stopping { return runtime,nil }
	agent.Generation=runtime.Generation
	agent.State=model.AgentSleepPending
	if err=m.driver.Stop(ctx,agent,runtime);err!=nil {
		_ = m.store.FailRuntime(context.WithoutCancel(ctx),runtime,err.Error())
		return model.Runtime{},err
	}
	// Stop has completed its external side effect. Persist it even if the
	// request was cancelled, otherwise future sleep calls stay in STOPPING.
	if err=m.store.MarkRuntimeStopped(context.WithoutCancel(ctx),runtime);err!=nil{return model.Runtime{},err}
	return m.store.GetRuntime(ctx,agentID)
}
