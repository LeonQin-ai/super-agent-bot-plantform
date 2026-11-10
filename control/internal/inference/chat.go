package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	endpoint, model, key string
	http                 *http.Client
}

func New(base, model, key string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid model API base URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("model API requires HTTPS or loopback HTTP")
	}
	if strings.TrimSpace(model) == "" || strings.TrimSpace(key) == "" {
		return nil, errors.New("model name and API key are required")
	}
	return &Client{endpoint: u.String() + "/chat/completions", model: model, key: key, http: &http.Client{
		Timeout:       75 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

var checksumTool = json.RawMessage(`{"type":"function","function":{"name":"sha256","description":"Compute the exact SHA-256 digest of UTF-8 text in the isolated Linux Worker. Use this for checksum requests.","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}}}`)

func (c *Client) Run(ctx context.Context, prompt string, checksum func(context.Context, string) (string, error)) (string, error) {
	if len(prompt) > 16<<10 {
		return "", errors.New("message exceeds 16 KiB")
	}
	messages := []message{
		{Role: "system", Content: "You are a light-agent in a private Feishu chat. Solve the current task and reply in the user's language. Only claim to have run commands or accessed data when a tool result verifies it."},
		{Role: "user", Content: prompt},
	}
	for round := 0; round < 3; round++ {
		body := map[string]any{"model": c.model, "messages": messages, "max_tokens": 3072, "thinking": map[string]string{"type": "disabled"}}
		if checksum != nil && round < 2 {
			body["tools"] = []json.RawMessage{checksumTool}
		}
		result, err := c.complete(ctx, body)
		if err != nil {
			return "", err
		}
		if len(result.ToolCalls) == 0 && strings.TrimSpace(result.Content) != "" {
			return result.Content, nil
		}
		if checksum == nil || len(result.ToolCalls) != 1 || round == 2 {
			break
		}
		call := result.ToolCalls[0]
		var args struct {
			Text *string `json:"text"`
		}
		if call.ID == "" || call.Type != "function" || call.Function.Name != "sha256" ||
			json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args.Text == nil || len(*args.Text) > 4096 || strings.ContainsRune(*args.Text, 0) {
			return "", errors.New("invalid sandbox tool request")
		}
		digest, err := checksum(ctx, *args.Text)
		if err != nil {
			return "", err
		}
		messages = append(messages, result, message{Role: "tool", Content: digest, ToolCallID: call.ID})
	}
	return "", errors.New("model returned no final answer")
}

func (c *Client) complete(ctx context.Context, body map[string]any) (message, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return message{}, err
	}
	response, err := c.Completion(ctx, data)
	if err != nil {
		return message{}, err
	}
	var result struct {
		Choices []struct {
			Message message `json:"message"`
		} `json:"choices"`
	}
	if err = json.Unmarshal(response, &result); err != nil || len(result.Choices) == 0 {
		return message{}, errors.New("invalid model API response")
	}
	return result.Choices[0].Message, nil
}

// Completion sends a bounded request to the operator-configured model.
func (c *Client) Completion(ctx context.Context, data []byte) (json.RawMessage, error) {
	var request map[string]json.RawMessage
	if err := json.Unmarshal(data, &request); err != nil || request == nil {
		return nil, errors.New("invalid model API request")
	}
	request["model"], _ = json.Marshal(c.model)
	request["thinking"] = json.RawMessage(`{"type":"disabled"}`)
	delete(request, "stream")
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("model API request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model API returned HTTP %d", resp.StatusCode)
	}
	result, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(result) > 1<<20 || !json.Valid(result) {
		return nil, errors.New("invalid model API response")
	}
	return result, nil
}
