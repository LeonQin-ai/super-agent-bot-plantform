package feishu

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

func TestURLVerification(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "feishu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	handler := New(st, Config{Token: "verify-token", EncryptKey: "encrypt-key", Allowed: map[string]struct{}{"ou_allowed": {}}})
	req := httptest.NewRequest(http.MethodPost, "/webhooks/feishu", strings.NewReader(`{"type":"url_verification","token":"verify-token","challenge":"challenge-123"}`))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "challenge-123") {
		t.Fatalf("unexpected response: status=%d body=%s", res.Code, res.Body.String())
	}
}
func TestWebhookDisabledWithoutAllowlist(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "feishu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	handler := New(st, Config{Token: "token", EncryptKey: "key"})
	req := httptest.NewRequest(http.MethodPost, "/webhooks/feishu", strings.NewReader("{}"))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", res.Code)
	}
}

func TestDecryptFeishuReferenceVector(t *testing.T) {
	plain, err := decrypt("test key", "P37w+VZImNgPEO1RBhJ6RtKl7n6zymIbEG1pReEzghk=")
	if err != nil || string(plain) != "hello world" {
		t.Fatalf("decrypt reference vector = %q, %v", plain, err)
	}
}

func TestEncryptedChallengeAndV2Delivery(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "feishu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const key = "fixture-encrypt-key"
	handler := New(st, Config{Token: "verify-token", EncryptKey: key, Allowed: map[string]struct{}{"ou_allowed": {}}})
	post := func(payload string, signed bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		// Use a prefixed IV that differs from the key, as Feishu does.
		secret := sha256.Sum256([]byte(key))
		block, err := aes.NewCipher(secret[:])
		if err != nil {
			t.Fatal(err)
		}
		iv := []byte("0123456789abcdef")
		padding := aes.BlockSize - len(payload)%aes.BlockSize
		plain := append([]byte(payload), bytes.Repeat([]byte{byte(padding)}, padding)...)
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(plain, plain)
		body, err := json.Marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(append(iv, plain...))})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/webhooks/feishu", bytes.NewReader(body))
		if signed {
			const ts, nonce = "1794211200", "fixture-nonce"
			digest := sha256.Sum256([]byte(ts + nonce + key + string(body)))
			req.Header.Set("X-Lark-Request-Timestamp", ts)
			req.Header.Set("X-Lark-Request-Nonce", nonce)
			req.Header.Set("X-Lark-Signature", hex.EncodeToString(digest[:]))
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("status=%d, want %d. Body: %s", res.Code, want, res.Body.String())
		}
		return res
	}
	challenge := post(`{"type":"url_verification","token":"verify-token","challenge":"encrypted-challenge"}`, false, http.StatusOK)
	var echoed map[string]string
	if err := json.Unmarshal(challenge.Body.Bytes(), &echoed); err != nil || echoed["challenge"] != "encrypted-challenge" {
		t.Fatalf("unexpected challenge response %s", challenge.Body.String())
	}
	post(`{"type":"url_verification","token":"wrong","challenge":"encrypted-challenge"}`, false, http.StatusUnauthorized)
	const delivery = `{"schema":"2.0","header":{"event_id":"event-1","event_type":"im.message.receive_v1","token":"verify-token"},"event":{"sender":{"sender_id":{"open_id":"ou_allowed"}},"message":{"chat_id":"oc_test","chat_type":"p2p","message_type":"text","content":"{\"text\":\"飞书联调\"}"}}}`
	post(delivery, false, http.StatusUnauthorized)
	missingHeaderToken := strings.Replace(delivery, `,"token":"verify-token"}`, `}`, 1)
	post(`{"token":"verify-token",`+missingHeaderToken[1:], true, http.StatusUnauthorized)
	post(delivery, true, http.StatusOK)
	post(delivery, true, http.StatusOK)
	task, err := st.ClaimNextTask(context.Background())
	if err != nil || task.RequestID != "event-1" || !strings.Contains(task.Payload, "飞书联调") {
		t.Fatalf("unexpected persisted task %+v, %v", task, err)
	}
	if _, err := st.ClaimNextTask(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("retry created another task: %v", err)
	}
}
