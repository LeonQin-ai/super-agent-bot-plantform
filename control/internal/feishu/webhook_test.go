package feishu

import(
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

func TestURLVerification(t *testing.T){
	st,err:=store.Open(filepath.Join(t.TempDir(),"feishu.db"));if err!=nil{t.Fatal(err)};defer st.Close()
	handler:=New(st,Config{Token:"verify-token",EncryptKey:"encrypt-key",Allowed:map[string]struct{}{"ou_allowed":{}}})
	req:=httptest.NewRequest(http.MethodPost,"/webhooks/feishu",strings.NewReader(`{"type":"url_verification","token":"verify-token","challenge":"challenge-123"}`))
	res:=httptest.NewRecorder();handler.ServeHTTP(res,req)
	if res.Code!=http.StatusOK||!strings.Contains(res.Body.String(),"challenge-123"){t.Fatalf("unexpected response: status=%d body=%s",res.Code,res.Body.String())}
}
func TestWebhookDisabledWithoutAllowlist(t *testing.T){
	st,err:=store.Open(filepath.Join(t.TempDir(),"feishu.db"));if err!=nil{t.Fatal(err)};defer st.Close()
	handler:=New(st,Config{Token:"token",EncryptKey:"key"})
	req:=httptest.NewRequest(http.MethodPost,"/webhooks/feishu",strings.NewReader("{}"))
	res:=httptest.NewRecorder();handler.ServeHTTP(res,req)
	if res.Code!=http.StatusServiceUnavailable{t.Fatalf("status=%d, want 503",res.Code)}
}
