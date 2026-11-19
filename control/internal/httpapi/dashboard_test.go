package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

func TestDashboardBroadcastAndSchedule(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server := New(st, nil, nil, "admin")
	server.EnableDashboardRuntime()
	call := func(method, path, body, token string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	if body := call(http.MethodGet, "/dashboard/", "", "", 200); !strings.Contains(body, "Agent 控制台") {
		t.Fatal(body)
	}
	call("GET", "/v1/agents", "", "", 401)
	call("POST", "/v1/agents", `{"tenant_id":"alpha"}`, "admin", 201)
	call("POST", "/v1/agents", `{"tenant_id":"beta"}`, "admin", 201)
	if body := call("GET", "/v1/agents", "", "admin", 200); !strings.Contains(body, `"runtime_configured":true`) {
		t.Fatal(body)
	}
	const broadcast = `{"request_id":"test","content":"hello fleet"}`
	if body := call("POST", "/v1/messages/broadcast", broadcast, "admin", 202); !strings.Contains(body, `"queued":2`) {
		t.Fatal(body)
	}
	if body := call("POST", "/v1/messages/broadcast", broadcast, "admin", 202); !strings.Contains(body, `"queued":0`) {
		t.Fatal(body)
	}
	start := time.Now().UTC().Truncate(time.Second).Add(time.Minute)
	call("POST", "/v1/schedules", `{"content":"scheduled fleet","minutes":1,"first_run":"`+start.Format(time.RFC3339)+`"}`, "admin", 201)
	ctx := context.Background()
	if _, err := st.CreateAgent(ctx, "late", "supervisor"); err != nil {
		t.Fatal(err)
	}
	now := start.Add(5 * time.Minute)
	for range 2 {
		if err := st.QueueDueSchedule(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	items, err := st.Schedules(ctx)
	if err != nil || len(items) != 1 || items[0].NextRun != now.Add(time.Minute).Unix() {
		t.Fatalf("unexpected schedules: %+v %v", items, err)
	}
	agents, err := st.Agents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range agents {
		tasks, err := st.Tasks(ctx, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, task := range tasks {
			if strings.Contains(task.Payload, "scheduled fleet") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("agent %s has %d scheduled tasks", agent.ID, count)
		}
	}
	call("DELETE", "/v1/schedules/"+items[0].ID, "", "admin", 204)
}
