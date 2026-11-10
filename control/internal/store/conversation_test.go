package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
)

func TestRecentMessageTurnsPersistAndStayWithAgent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conversation.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	makeAgent := func(tenant string) model.Agent {
		t.Helper()
		agent, err := st.CreateAgent(ctx, tenant, "")
		if err != nil {
			t.Fatal(err)
		}
		return agent
	}
	complete := func(agent model.Agent, id, payload, answer string) {
		t.Helper()
		task, err := st.CreateTask(ctx, agent.ID, id, payload)
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := st.ClaimNextTask(ctx)
		if err != nil || claimed.ID != task.ID {
			t.Fatalf("claim %s: %v", task.ID, err)
		}
		if err := st.CompleteTask(ctx, task.ID, answer); err != nil {
			t.Fatal(err)
		}
	}
	agent := makeAgent("tenant-a")
	other := makeAgent("tenant-b")
	complete(agent, "history", `{"source":"api","content":"search virtualization jobs"}`, "matching jobs")
	complete(agent, "execution", `{"kind":"trusted_execution"}`, "private stdout")
	complete(other, "private", `{"source":"api","content":"private other tenant"}`, "other")
	current, err := st.CreateTask(ctx, agent.ID, "followup", `{"source":"api","content":"Hangzhou?"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	turns, err := st.RecentMessageTurns(ctx, current)
	if err != nil || len(turns) != 1 || turns[0].User != "search virtualization jobs" || turns[0].Assistant != "matching jobs" {
		t.Fatalf("unexpected conversation: %+v %v", turns, err)
	}
}
