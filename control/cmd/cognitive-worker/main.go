package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/cognitive"
)

const policy = `[
	{"tool":"*","action":"deny"},
	{"tool":"file_read","action":"allow"},
	{"tool":"file_write","action":"allow"},
	{"tool":"file_edit","action":"allow"},
	{"tool":"list_dir","action":"allow"},
	{"tool":"glob","action":"allow"},
	{"tool":"grep","action":"allow"}
]`

func main() {
	encoder := json.NewEncoder(os.Stdout)
	if err := run(encoder); err != nil {
		_ = encoder.Encode(cognitive.Frame{Error: err.Error()})
		os.Exit(1)
	}
}

func run(encoder *json.Encoder) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), cognitive.MaxFrame)
	var request cognitive.Frame
	if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &request) != nil || strings.TrimSpace(request.Prompt) == "" {
		return failure("invalid task input")
	}
	state, err := os.MkdirTemp("/tmp", "cognitive-state-")
	if err != nil {
		return failure("could not initialize runtime state")
	}
	rules := policy
	if request.Briefing {
		rules = `[{"tool":"*","action":"deny"}]`
	}
	if os.WriteFile(filepath.Join(state, "policy.json"), []byte(rules), 0600) != nil {
		return failure("could not initialize tool policy")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return failure("could not start local model relay")
	}
	defer listener.Close()
	var relay sync.Mutex
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err == nil {
			body, err = steerPlanning(body)
		}
		if err != nil {
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		relay.Lock()
		defer relay.Unlock()
		var reply cognitive.Frame
		if encoder.Encode(cognitive.Frame{Request: body}) != nil || !scanner.Scan() ||
			json.Unmarshal(scanner.Bytes(), &reply) != nil || len(reply.Response) == 0 {
			http.Error(w, "model relay failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply.Response)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	config, _ := json.Marshal(map[string]string{
		"llm.provider": "openai", "llm.model": "configured-model",
		"llm.base_url": "http://" + listener.Addr().String() + "/v1", "llm.api_key": "local-relay",
	})
	configPath := filepath.Join(state, "cognitive-os-agent.json")
	if os.WriteFile(configPath, config, 0600) != nil {
		return failure("could not initialize model configuration")
	}
	prompt := "Complete the user request below and answer in the user's language. Work in /workspace. " +
		"For read-only tasks, do not declare output artifacts. Reuse successful observations, " +
		"avoid repeating identical tool calls, and complete the task once you have the requested evidence.\n\n" + request.Prompt
	cmd := exec.CommandContext(ctx, "/usr/local/bin/cognitive-os-agent",
		"--config", configPath, "--state", state, "--workspace", "/workspace", "run", prompt)
	cmd.Env = []string{"HOME=/tmp", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	stdout, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return failure("could not start runtime")
	}
	answer, err := io.ReadAll(io.LimitReader(stdout, (64<<10)+1))
	if err != nil || len(answer) > 64<<10 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return failure("runtime output exceeded its limit")
	}
	waitErr := cmd.Wait()
	relay.Lock()
	defer relay.Unlock()
	text := strings.TrimSpace(string(answer))
	if waitErr != nil || text == "" {
		return encoder.Encode(cognitive.Frame{Error: "runtime failed", Answer: text})
	}
	return encoder.Encode(cognitive.Frame{Answer: text})
}

func steerPlanning(body []byte) ([]byte, error) {
	var request map[string]json.RawMessage
	var messages []map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil || json.Unmarshal(request["messages"], &messages) != nil || len(messages) == 0 {
		return nil, failure("invalid planner request")
	}
	last := messages[len(messages)-1]
	var role, content string
	if json.Unmarshal(last["role"], &role) == nil && role == "user" && json.Unmarshal(last["content"], &content) == nil {
		// The C11 planner repeats the original request after supplying observations.
		// Tell the model to use the preceding evidence to finish the same task.
		last["content"], _ = json.Marshal(content + "\n\n[Task delivery] The request above is the same task, not a new turn. " +
			"Use the earlier tool observations, including their actual returned text. If the user asked for a file and the inputs are already observed, " +
			"write the output now and verify that output before completing. If the request is fully satisfied, call complete_task with the result in answer. " +
			"Do not repeat a successful input read or listing to send the reply.")
		request["messages"], _ = json.Marshal(messages)
	}
	return json.Marshal(request)
}

type failure string

func (f failure) Error() string { return string(f) }
