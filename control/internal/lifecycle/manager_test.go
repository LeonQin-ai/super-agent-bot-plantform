package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

type fakeDriver struct{ starts,stops int; startErr,stopErr error; startGeneration,stopGeneration uint64; startAgentState,stopAgentState model.AgentState }
func(f *fakeDriver)Start(_ context.Context,a model.Agent,r model.Runtime)error{f.starts++;f.startGeneration=a.Generation;f.startAgentState=a.State;return f.startErr}
func(f *fakeDriver)Stop(_ context.Context,a model.Agent,r model.Runtime)error{f.stops++;f.stopGeneration=a.Generation;f.stopAgentState=a.State;return f.stopErr}

func TestWakeIsSingleFlightAndSleepIsGuarded(t *testing.T){
	st,err:=store.Open(filepath.Join(t.TempDir(),"lifecycle.db"));if err!=nil{t.Fatal(err)};defer st.Close()
	ctx:=context.Background();agent,err:=st.CreateAgent(ctx,"tenant","supervisor");if err!=nil{t.Fatal(err)}
	driver:=&fakeDriver{};manager:=New(st,driver)
	first,err:=manager.Wake(ctx,agent.ID);if err!=nil{t.Fatal(err)}
	second,err:=manager.Wake(ctx,agent.ID);if err!=nil{t.Fatal(err)}
	if driver.starts!=1||first.ID!=second.ID||second.State!=model.RuntimeReady{t.Fatalf("wake not single-flight: starts=%d first=%+v second=%+v",driver.starts,first,second)}
	if driver.startGeneration!=first.Generation||driver.startAgentState!=model.AgentWaking{t.Fatalf("driver received stale Agent generation/state: generation=%d state=%s runtime=%+v",driver.startGeneration,driver.startAgentState,first)}
	if _,err=st.CreateTask(ctx,agent.ID,"queued","{}");err!=nil{t.Fatal(err)}
	if _,err=manager.Sleep(ctx,agent.ID);!errors.Is(err,store.ErrConflict){t.Fatalf("sleep with queued task err=%v, want conflict",err)}
	task,err:=st.ClaimNextTask(ctx);if err!=nil{t.Fatal(err)}
	if err=st.SetAgentState(ctx,agent.ID,model.AgentActive);err!=nil{t.Fatal(err)}
	if err=st.CompleteTask(ctx,task.ID,"ok");err!=nil{t.Fatal(err)}
	stopped,err:=manager.Sleep(ctx,agent.ID);if err!=nil{t.Fatal(err)}
	if driver.stops!=1||stopped.State!=model.RuntimeStopped{t.Fatalf("sleep failed: stops=%d runtime=%+v",driver.stops,stopped)}
	if driver.stopGeneration!=stopped.Generation||driver.stopAgentState!=model.AgentSleepPending{t.Fatalf("driver received stale sleep generation/state: generation=%d state=%s runtime=%+v",driver.stopGeneration,driver.stopAgentState,stopped)}
}

func TestWakeFailureCanRetryWithNewGeneration(t *testing.T){
	st,err:=store.Open(filepath.Join(t.TempDir(),"retry.db"));if err!=nil{t.Fatal(err)};defer st.Close()
	ctx:=context.Background();agent,err:=st.CreateAgent(ctx,"tenant","supervisor");if err!=nil{t.Fatal(err)}
	driver:=&fakeDriver{startErr:errors.New("boot failed")};manager:=New(st,driver)
	if _,err=manager.Wake(ctx,agent.ID);err==nil{t.Fatal("expected start failure")}
	driver.startErr=nil
	runtime,err:=manager.Wake(ctx,agent.ID);if err!=nil{t.Fatal(err)}
	if runtime.State!=model.RuntimeReady||runtime.Generation!=2{t.Fatalf("retry did not advance generation: %+v",runtime)}
}

func TestStaleRuntimeCannotBecomeReady(t *testing.T){
	st,err:=store.Open(filepath.Join(t.TempDir(),"stale.db"));if err!=nil{t.Fatal(err)};defer st.Close()
	ctx:=context.Background();agent,err:=st.CreateAgent(ctx,"tenant","supervisor");if err!=nil{t.Fatal(err)}
	old,started,err:=st.BeginWake(ctx,agent.ID);if err!=nil||!started{t.Fatalf("begin wake: %v %v",started,err)}
	if err=st.FailRuntime(ctx,old,"simulated crash");err!=nil{t.Fatal(err)}
	current,started,err:=st.BeginWake(ctx,agent.ID);if err!=nil||!started{t.Fatalf("retry wake: %v %v",started,err)}
	if err=st.MarkRuntimeReady(ctx,old);!errors.Is(err,store.ErrConflict){t.Fatalf("stale generation accepted: %v",err)}
	if err=st.MarkRuntimeReady(ctx,current);err!=nil{t.Fatal(err)}
}

// cancellingStopDriver models a successful stop racing with request cancellation.
type cancellingStopDriver struct {
	fakeDriver
	cancel context.CancelFunc
}

func (d *cancellingStopDriver) Stop(ctx context.Context, a model.Agent, r model.Runtime) error {
	err := d.fakeDriver.Stop(ctx, a, r)
	d.cancel()
	return err
}

func TestSleepPersistsSuccessfulStopAfterRequestCancellation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cancel-sleep.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	agent, err := st.CreateAgent(ctx, "tenant", "supervisor")
	if err != nil {
		t.Fatal(err)
	}
	stopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	driver := &cancellingStopDriver{cancel: cancel}
	manager := New(st, driver)
	if _, err = manager.Wake(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	// The response may observe cancellation, but the durable state must reflect
	// the successful external stop so a retry can finish without stopping twice.
	_, _ = manager.Sleep(stopCtx, agent.ID)
	runtime, err := st.GetRuntime(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.State != model.RuntimeStopped {
		t.Fatalf("runtime state = %s, want STOPPED", runtime.State)
	}
	updated, err := st.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != model.AgentSleeping {
		t.Fatalf("agent state = %s, want SLEEPING", updated.State)
	}
	if _, err = manager.Sleep(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	if driver.stops != 1 {
		t.Fatalf("stop calls = %d, want 1", driver.stops)
	}
	if _, err = manager.Wake(ctx, agent.ID); err != nil {
		t.Fatalf("wake after stopped runtime: %v", err)
	}
}
