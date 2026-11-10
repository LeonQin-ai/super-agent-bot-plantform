package cognitive

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type modelFunc func(context.Context, []byte) (json.RawMessage, error)

func (f modelFunc) Completion(ctx context.Context, body []byte) (json.RawMessage, error) {
	return f(ctx, body)
}

func TestExchangeReviewsRepeatedRead(t *testing.T) {
	calls := 0
	runner := Runner{model: modelFunc(func(_ context.Context, body []byte) (json.RawMessage, error) {
		calls++
		if calls == 3 {
			if !strings.Contains(string(body), `"tool_choice":{"type":"function","function":{"name":"complete_task"}}`) ||
				strings.Contains(string(body), `"name":"file_read"`) {
				t.Fatalf("review did not select only complete_task: %s", body)
			}
			return json.RawMessage(`{"choices":[{"message":{"content":"file contents"}}]}`), nil
		}
		return json.RawMessage(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"file_read","arguments":"{}"}}]}}]}`), nil
	})}
	request := `{"request":{"tools":[{"function":{"name":"file_read"}},{"function":{"name":"complete_task"}}]}}`
	fromWorker := strings.NewReader(request + "\n" + request + "\n" + `{"answer":"file contents"}` + "\n")
	var toWorker bytes.Buffer
	answer, err := runner.exchange(context.Background(), &toWorker, fromWorker, Frame{Prompt: "Read the file"})
	if err != nil || answer != "file contents" || calls != 3 {
		t.Fatalf("answer %q, calls %d, error %v", answer, calls, err)
	}
}

func TestObservationReviewKeepsArtifactTools(t *testing.T) {
	prompt := "读取两份文件，把原文放到同一个新文件"
	request := `{"tools":[{"function":{"name":"file_read"}},{"function":{"name":"file_write"}},{"function":{"name":"complete_task"}}]}`
	body, ok := reviewObservation([]byte(request), "file_read", artifactWork(prompt))
	if !ok || strings.Contains(string(body), `"name":"file_read"`) ||
		!strings.Contains(string(body), `"name":"file_write"`) || !strings.Contains(string(body), `"tool_choice":"auto"`) {
		t.Fatalf("artifact tool unavailable after repeat: %s", body)
	}
}

func TestRepeatedObservationHandlesBatches(t *testing.T) {
	first := call{Name: "file_read", Arguments: `{"path":"a"}`}
	second := call{Name: "file_read", Arguments: `{"path":"b"}`}
	batch := []call{first, second}
	if repeatedObservation(batch, map[call]bool{first: true}) != "" {
		t.Fatal("new observations must be allowed")
	}
	if repeatedObservation(batch, map[call]bool{first: true, second: true}) != "file_read" {
		t.Fatal("fully repeated observation batch was not detected")
	}
}
