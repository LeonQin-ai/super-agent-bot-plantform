package cognitive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const MaxFrame = 2 << 20
const maxModelRequests = 24

type taskError struct {
	reason, detail string
}

func (e taskError) Error() string       { return e.reason }
func (e taskError) UserMessage() string { return e.detail }

type Frame struct {
	Prompt   string          `json:"prompt,omitempty"`
	Briefing bool            `json:"briefing,omitempty"`
	Request  json.RawMessage `json:"request,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
	Answer   string          `json:"answer,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type Model interface {
	Completion(context.Context, []byte) (json.RawMessage, error)
}

type Runner struct {
	image, workspaces string
	model             Model
}

var imageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var taskID = regexp.MustCompile(`^tsk_[a-z0-9]{1,64}$`)
var workspaceID = regexp.MustCompile(`^wsp_[a-z0-9]{1,64}$`)

func New(image, workspaces string, model Model) (*Runner, error) {
	if !imageID.MatchString(image) || !filepath.IsAbs(workspaces) || strings.Contains(workspaces, ",") || model == nil {
		return nil, errors.New("Cognitive OS requires a pinned local image ID, absolute workspace root and model client")
	}
	return &Runner{image: image, workspaces: workspaces, model: model}, nil
}

func (r *Runner) Run(ctx context.Context, task, workspace, prompt string) (string, error) {
	if !workspaceID.MatchString(workspace) {
		return "", errors.New("invalid Cognitive OS task")
	}
	dir, err := r.workspace(workspace)
	if err != nil {
		return "", err
	}
	return r.run(ctx, task, []string{"--mount", "type=bind,src=" + dir + ",dst=/workspace"}, Frame{Prompt: prompt})
}

func (r *Runner) RunBriefing(ctx context.Context, task, prompt string) (string, error) {
	return r.run(ctx, task, []string{"--tmpfs", "/workspace:rw,nosuid,nodev,noexec,size=4m,mode=1777"}, Frame{Prompt: prompt, Briefing: true})
}

func (r *Runner) run(ctx context.Context, task string, workspaceOptions []string, input Frame) (string, error) {
	if !taskID.MatchString(task) || len(input.Prompt) > 16<<10 {
		return "", errors.New("invalid Cognitive OS task")
	}
	name := "sabp-cognitive-" + task
	args := []string{"run", "--rm", "-i", "--pull=never",
		"--name", name, "--label", "com.light-agent.cognitive=true", "--network", "none",
		"--user", "65532:65532", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--memory", "512m", "--memory-swap", "512m", "--cpus", "1", "--pids-limit", "64",
		"--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m"}
	args = append(args, workspaceOptions...)
	args = append(args, "--workdir", "/workspace", "--entrypoint", "/usr/local/bin/sabp-cognitive-worker", r.image)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err = cmd.Start(); err != nil {
		return "", errors.New("could not start Cognitive OS Worker")
	}
	answer, runErr := r.exchange(ctx, in, out, input)
	_ = in.Close()
	if runErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if waitErr != nil || runErr != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
		if runErr != nil {
			return "", runErr
		}
		return "", errors.New("Cognitive OS Worker exited unsuccessfully")
	}
	return answer, nil
}

func (r *Runner) workspace(id string) (string, error) {
	if err := os.MkdirAll(r.workspaces, 0o700); err != nil {
		return "", err
	}
	dir := filepath.Join(r.workspaces, id)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return "", errors.New("invalid Cognitive OS workspace")
	}
	if err = os.Chown(dir, 65532, 65532); err != nil {
		return "", err
	}
	return dir, nil
}

func (r *Runner) exchange(ctx context.Context, in io.Writer, out io.Reader, input Frame) (string, error) {
	encoder := json.NewEncoder(in)
	if err := encoder.Encode(input); err != nil {
		return "", errors.New("could not submit task to Cognitive OS Worker")
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), MaxFrame)
	observations := make(map[call]bool)
	artifactRequested := artifactWork(input.Prompt)
	wroteArtifact := false
	for requests := 0; scanner.Scan(); {
		var frame Frame
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			break
		}
		switch {
		case frame.Error != "":
			detail := []rune(strings.TrimSpace(frame.Answer))
			if len(detail) > 1200 {
				detail = append(detail[:1200], '…')
			}
			return "", taskError{reason: "Cognitive OS Worker reported " + frame.Error, detail: string(detail)}
		case strings.TrimSpace(frame.Answer) != "":
			return frame.Answer, nil
		case len(frame.Request) > 0:
			if requests >= maxModelRequests {
				return "", taskError{reason: "Cognitive OS model request budget exhausted",
					detail: "任务达到了模型调用上限。可以把它拆成几个步骤分别执行。"}
			}
			requests++
			reply, err := r.model.Completion(ctx, frame.Request)
			if err != nil {
				return "", err
			}
			current := modelCalls(reply)
			if name := repeatedObservation(current, observations); name != "" && requests < maxModelRequests {
				request, filtered := reviewObservation(frame.Request, name, artifactRequested && !wroteArtifact)
				if filtered {
					requests++
					reply, err = r.model.Completion(ctx, request)
					if err != nil {
						return "", err
					}
					current = modelCalls(reply)
				}
			}
			for _, action := range current {
				switch action.Name {
				case "file_read", "list_dir", "glob", "grep":
					observations[action] = true
				case "file_write", "file_edit":
					// A write can make a later verification read necessary.
					wroteArtifact = true
					clear(observations)
				}
			}
			if err := encoder.Encode(Frame{Response: reply}); err != nil {
				return "", errors.New("could not relay model response")
			}
		default:
			return "", errors.New("invalid Cognitive OS Worker frame")
		}
	}
	return "", errors.New("Cognitive OS Worker returned no result")
}

type call struct {
	Name, Arguments string
}

func modelCalls(body []byte) []call {
	var result struct {
		Choices []struct {
			Message struct {
				Calls []struct{ Function call } `json:"tool_calls"`
			}
		}
	}
	if json.Unmarshal(body, &result) != nil || len(result.Choices) == 0 {
		return nil
	}
	actions := result.Choices[0].Message.Calls
	calls := make([]call, 0, len(actions))
	for _, action := range actions {
		function := action.Function
		var args map[string]json.RawMessage
		if json.Unmarshal([]byte(function.Arguments), &args) == nil {
			canonical, _ := json.Marshal(args)
			function.Arguments = string(canonical)
		}
		calls = append(calls, function)
	}
	return calls
}

func repeatedObservation(calls []call, observed map[call]bool) string {
	if len(calls) == 0 {
		return ""
	}
	for _, action := range calls {
		if !observed[action] {
			return ""
		}
	}
	return calls[0].Name
}

func artifactWork(prompt string) bool {
	prompt = strings.ToLower(prompt)
	for _, action := range []string{"写", "生成", "创建", "保存", "编辑", "修改", "新文件", "输出到", "write", "create", "save", "edit", "append", "generate"} {
		if strings.Contains(prompt, action) {
			return true
		}
	}
	return false
}

func reviewObservation(body []byte, name string, artifactRequested bool) ([]byte, bool) {
	var request map[string]json.RawMessage
	var tools []json.RawMessage
	if json.Unmarshal(body, &request) != nil || json.Unmarshal(request["tools"], &tools) != nil {
		return nil, false
	}
	if artifactRequested {
		// Pause the repeated observation for one model turn. Keep write tools
		// available so the planner can produce the requested artifact.
		filtered := make([]json.RawMessage, 0, len(tools))
		for _, tool := range tools {
			var spec struct{ Function call }
			if json.Unmarshal(tool, &spec) == nil && spec.Function.Name != name {
				filtered = append(filtered, tool)
			}
		}
		if len(filtered) == 0 || len(filtered) == len(tools) {
			return nil, false
		}
		request["tools"], _ = json.Marshal(filtered)
		request["tool_choice"] = json.RawMessage(`"auto"`)
		result, err := json.Marshal(request)
		return result, err == nil
	}
	// The runtime reviews the model's completion against its observed results.
	for _, tool := range tools {
		var spec struct{ Function call }
		if json.Unmarshal(tool, &spec) == nil && spec.Function.Name == "complete_task" {
			request["tools"], _ = json.Marshal([]json.RawMessage{tool})
			request["tool_choice"] = json.RawMessage(`{"type":"function","function":{"name":"complete_task"}}`)
			result, err := json.Marshal(request)
			return result, err == nil
		}
	}
	return nil, false
}
