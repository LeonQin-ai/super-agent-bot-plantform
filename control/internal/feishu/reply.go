package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

var messageIDPattern = regexp.MustCompile(`^om_[a-zA-Z0-9_-]{1,96}$`)

type Replier struct {
	AppID, AppSecret string
}

func (r Replier) Reply(ctx context.Context, messageID, taskID, text string) error {
	if !messageIDPattern.MatchString(messageID) {
		return errors.New("invalid Feishu message ID")
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var token struct {
		Code  int    `json:"code"`
		Value string `json:"tenant_access_token"`
	}
	if err := post(ctx, client, "/auth/v3/tenant_access_token/internal", "", map[string]string{
		"app_id": r.AppID, "app_secret": r.AppSecret,
	}, &token); err != nil {
		return err
	}
	if token.Code != 0 || token.Value == "" {
		return fmt.Errorf("Feishu authentication failed (code %d)", token.Code)
	}
	inner, _ := json.Marshal(map[string]string{"text": text})
	var result struct {
		Code *int `json:"code"`
	}
	if err := post(ctx, client, "/im/v1/messages/"+messageID+"/reply", token.Value, map[string]string{
		"msg_type": "text", "content": string(inner), "uuid": taskID,
	}, &result); err != nil {
		return err
	}
	if result.Code == nil {
		return errors.New("Feishu reply response missing status")
	}
	if *result.Code != 0 {
		return fmt.Errorf("Feishu reply failed (code %d)", *result.Code)
	}
	return nil
}

func post(ctx context.Context, client *http.Client, path, token string, body map[string]string, out any) error {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://open.feishu.cn/open-apis"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("Feishu request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Feishu returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out)
}
