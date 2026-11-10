package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

type Applications struct {
	legacy   http.Handler
	hooks    map[string]http.Handler
	repliers map[string]Replier
}

type registration struct {
	Route        string   `json:"route"`
	Tenant       string   `json:"tenant"`
	AppID        string   `json:"app_id"`
	AppSecret    string   `json:"app_secret"`
	Token        string   `json:"verification_token"`
	EncryptKey   string   `json:"encrypt_key"`
	Allowed      []string `json:"allowed_open_ids"`
	PendingLimit int      `json:"pending_limit"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func LoadApplications(st store.Store, legacy Config, legacyAppID, path string) (*Applications, error) {
	apps := &Applications{legacy: New(st, legacy), hooks: make(map[string]http.Handler), repliers: make(map[string]Replier)}
	if path == "" {
		return apps, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var rows []registration
	if err := decoder.Decode(&rows); err != nil || len(rows) == 0 || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid Feishu application registry")
	}
	appIDs := map[string]bool{legacyAppID: true}
	tokens := map[string]bool{legacy.Token: true}
	keys := map[string]bool{legacy.EncryptKey: true}
	limits := make(map[string]int)
	for _, row := range rows {
		if !identifier.MatchString(row.Route) || !identifier.MatchString(row.Tenant) || !identifier.MatchString(row.AppID) ||
			strings.TrimSpace(row.AppSecret) == "" || strings.TrimSpace(row.Token) == "" || strings.TrimSpace(row.EncryptKey) == "" ||
			len(row.Allowed) == 0 || row.PendingLimit < 1 {
			return nil, errors.New("incomplete Feishu application registration")
		}
		if apps.hooks[row.Route] != nil || appIDs[row.AppID] || tokens[row.Token] || keys[row.EncryptKey] {
			return nil, errors.New("duplicate Feishu route, application or verification credentials")
		}
		if limit, ok := limits[row.Tenant]; ok && limit != row.PendingLimit {
			return nil, errors.New("Feishu applications in a tenant must share the same pending limit")
		}
		appIDs[row.AppID], tokens[row.Token], keys[row.EncryptKey], limits[row.Tenant] = true, true, true, row.PendingLimit
		allowed := make(map[string]struct{}, len(row.Allowed))
		for _, id := range row.Allowed {
			if !identifier.MatchString(id) || !strings.HasPrefix(id, "ou_") {
				return nil, errors.New("invalid Feishu Open ID")
			}
			allowed[id] = struct{}{}
		}
		scope := store.FeishuAppScope{Tenant: row.Tenant, AppID: row.AppID, PendingLimit: row.PendingLimit}
		apps.hooks[row.Route] = &Hook{s: st, scope: scope, c: Config{Token: row.Token, EncryptKey: row.EncryptKey, Allowed: allowed}}
		apps.repliers[scope.ReplyKey()] = Replier{AppID: row.AppID, AppSecret: row.AppSecret}
	}
	return apps, nil
}

func (a *Applications) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := r.PathValue("app")
	if route == "" {
		a.legacy.ServeHTTP(w, r)
		return
	}
	hook := a.hooks[route]
	if hook == nil {
		http.NotFound(w, r)
		return
	}
	hook.ServeHTTP(w, r)
}

func (a *Applications) Reply(ctx context.Context, key, messageID, taskID, answer string) error {
	replier, ok := a.repliers[key]
	if !ok {
		return errors.New("Feishu application registration unavailable")
	}
	return replier.Reply(ctx, messageID, taskID, answer)
}
