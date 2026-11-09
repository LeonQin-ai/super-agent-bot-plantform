package feishu

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

type Config struct {
	Token, EncryptKey string
	Allowed           map[string]struct{}
	AllowedFile       string
}

type Hook struct {
	s store.Store
	c Config
}

func New(s store.Store, c Config) http.Handler { return &Hook{s: s, c: c} }

type event struct {
	Schema    string `json:"schema"`
	Type      string `json:"type"`
	Token     string `json:"token"`
	Challenge string `json:"challenge"`
	Encrypt   string `json:"encrypt"`
	Header    struct {
		ID    string `json:"event_id"`
		Type  string `json:"event_type"`
		Token string `json:"token"`
	} `json:"header"`
	Event struct {
		Message struct {
			ID       string `json:"message_id"`
			ChatID   string `json:"chat_id"`
			ChatType string `json:"chat_type"`
			Content  string `json:"content"`
		} `json:"message"`
		Sender struct {
			ID struct {
				OpenID string `json:"open_id"`
			} `json:"sender_id"`
		} `json:"sender"`
	} `json:"event"`
}

func (h *Hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.c.Token == "" || h.c.EncryptKey == "" || (len(h.c.Allowed) == 0 && h.c.AllowedFile == "") {
		http.Error(w, "Feishu disabled: configure token, encrypt key and allowlist", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid body", http.StatusRequestEntityTooLarge)
		return
	}
	var v event
	if err := json.Unmarshal(body, &v); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if v.Encrypt != "" {
		plain, err := decrypt(h.c.EncryptKey, v.Encrypt)
		v = event{}
		if err != nil || json.Unmarshal(plain, &v) != nil {
			http.Error(w, "invalid encrypted request", http.StatusUnauthorized)
			return
		}
	}
	// Feishu may send URL verification without signature headers, including
	// when the challenge is encrypted. This branch never dispatches an event.
	if v.Type == "url_verification" {
		if !eq(v.Token, h.c.Token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if v.Challenge == "" {
			http.Error(w, "missing challenge", http.StatusBadRequest)
			return
		}
		out(w, map[string]string{"challenge": v.Challenge})
		return
	}
	if !signature(r, body, h.c.EncryptKey) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	token := v.Header.Token
	if v.Schema == "" && token == "" {
		token = v.Token
	}
	if !eq(token, h.c.Token) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if v.Header.Type != "im.message.receive_v1" {
		out(w, map[string]int{"code": 0})
		return
	}

	openID := v.Event.Sender.ID.OpenID
	if !h.allowed(openID) {
		http.Error(w, "sender forbidden", http.StatusForbidden)
		return
	}
	message := v.Event.Message
	if message.ChatType != "p2p" {
		http.Error(w, "direct messages only", http.StatusForbidden)
		return
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(message.Content), &content); err != nil || strings.TrimSpace(content.Text) == "" {
		http.Error(w, "text message required", http.StatusBadRequest)
		return
	}
	if _, _, err := h.s.IngestFeishuEvent(r.Context(), v.Header.ID, openID, message.ChatID, strings.TrimSpace(content.Text), message.ID); err != nil {
		http.Error(w, "persist event failed", http.StatusInternalServerError)
		return
	}
	out(w, map[string]int{"code": 0})
}

func (h *Hook) allowed(openID string) bool {
	if openID == "" {
		return false
	}
	if _, ok := h.c.Allowed[openID]; ok {
		return true
	}
	if h.c.AllowedFile == "" {
		return false
	}
	content, err := os.ReadFile(h.c.AllowedFile)
	return err == nil && strings.TrimSpace(string(content)) == openID
}

func signature(r *http.Request, body []byte, key string) bool {
	timestamp := r.Header.Get("X-Lark-Request-Timestamp")
	nonce := r.Header.Get("X-Lark-Request-Nonce")
	sign := strings.ToLower(r.Header.Get("X-Lark-Signature"))
	if timestamp == "" || nonce == "" || sign == "" {
		return false
	}
	digest := sha256.Sum256([]byte(timestamp + nonce + key + string(body)))
	return eq(sign, hex.EncodeToString(digest[:]))
}

func decrypt(secret, encoded string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if len(data) < 2*aes.BlockSize || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("invalid ciphertext")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	iv, plaintext := data[:aes.BlockSize], data[aes.BlockSize:]
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, plaintext)
	padding := int(plaintext[len(plaintext)-1])
	if padding < 1 || padding > aes.BlockSize {
		return nil, errors.New("invalid padding")
	}
	for _, b := range plaintext[len(plaintext)-padding:] {
		if int(b) != padding {
			return nil, errors.New("invalid padding")
		}
	}
	return plaintext[:len(plaintext)-padding], nil
}

func eq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func out(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
