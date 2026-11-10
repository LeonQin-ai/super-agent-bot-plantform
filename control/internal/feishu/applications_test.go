package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

func TestApplicationRoutesIsolateEventsAndQuota(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	registry := filepath.Join(t.TempDir(), "apps.json")
	const config = `[
		{"route":"alpha","tenant":"tenant-a","app_id":"cli_a","app_secret":"secret-a","verification_token":"token-a","encrypt_key":"key-a","allowed_open_ids":["ou_shared"],"pending_limit":1},
		{"route":"beta","tenant":"tenant-b","app_id":"cli_b","app_secret":"secret-b","verification_token":"token-b","encrypt_key":"key-b","allowed_open_ids":["ou_shared"],"pending_limit":1}
	]`
	if err := os.WriteFile(registry, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	apps, err := LoadApplications(st, Config{}, "", registry)
	if err != nil {
		t.Fatal(err)
	}
	post := func(route, id, token, key string, want int) {
		t.Helper()
		body := fmt.Sprintf(`{"schema":"2.0","header":{"event_id":%q,"event_type":"im.message.receive_v1","token":%q},"event":{"sender":{"sender_id":{"open_id":"ou_shared"}},"message":{"message_id":"om_shared","chat_id":"oc_shared","chat_type":"p2p","content":"{\"text\":\"hello\"}"}}}`, id, token)
		req := httptest.NewRequest(http.MethodPost, "/webhooks/feishu/"+route, strings.NewReader(body))
		req.SetPathValue("app", route)
		req.Header.Set("X-Lark-Request-Timestamp", "1")
		req.Header.Set("X-Lark-Request-Nonce", "n")
		digest := sha256.Sum256([]byte("1n" + key + body))
		req.Header.Set("X-Lark-Signature", hex.EncodeToString(digest[:]))
		rec := httptest.NewRecorder()
		apps.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s/%s status=%d want=%d: %s", route, id, rec.Code, want, rec.Body.String())
		}
	}
	post("beta", "event-1", "token-a", "key-a", http.StatusUnauthorized)
	post("alpha", "event-1", "token-a", "key-a", http.StatusOK)
	post("alpha", "event-1", "token-a", "key-a", http.StatusOK)
	post("alpha", "event-2", "token-a", "key-a", http.StatusTooManyRequests)
	post("beta", "event-1", "token-b", "key-b", http.StatusOK)
	ctx := context.Background()
	seen := make(map[string]string)
	for i := 0; i < 2; i++ {
		task, err := st.ClaimNextTask(ctx)
		if err != nil {
			t.Fatal(err)
		}
		agent, err := st.GetAgent(ctx, task.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		appID := map[string]string{"tenant-a": "cli_a", "tenant-b": "cli_b"}[agent.TenantID]
		key := agent.TenantID + "/" + appID
		if appID == "" || apps.repliers[key].AppID != appID || !strings.Contains(task.Payload, key) {
			t.Fatalf("task has incorrect tenant/reply scope: %s", task.Payload)
		}
		seen[agent.TenantID] = agent.WorkspaceID
		if err := st.CompleteTask(ctx, task.ID, "ok"); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen["tenant-a"] == seen["tenant-b"] {
		t.Fatal("application identities shared a workspace")
	}
	post("alpha", "event-2", "token-a", "key-a", http.StatusOK)
	task, err := st.ClaimNextTask(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := st.GetAgent(ctx, task.AgentID)
	if err != nil || agent.WorkspaceID != seen["tenant-a"] {
		t.Fatalf("agent workspace was not reused: %+v, %v", agent, err)
	}
}
