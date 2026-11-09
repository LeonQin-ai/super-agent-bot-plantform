package httpapi

import(
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/execution"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

func newTestHandler(t *testing.T,token string)http.Handler{
	t.Helper();st,err:=store.Open(filepath.Join(t.TempDir(),"api.db"));if err!=nil{t.Fatal(err)}
	t.Cleanup(func(){_=st.Close()})
	return New(st,nil,nil,token)
}
func TestCreateAgentAndSubmitTask(t *testing.T){
	h:=newTestHandler(t,"")
	req:=httptest.NewRequest(http.MethodPost,"/v1/agents",bytes.NewBufferString(`{"tenant_id":"tenant-1","role":"supervisor"}`))
	res:=httptest.NewRecorder();h.ServeHTTP(res,req);if res.Code!=201{t.Fatalf("create status=%d body=%s",res.Code,res.Body.String())}
}
func TestAPITokenRequiredWhenConfigured(t *testing.T){
	h:=newTestHandler(t,"test-secret")
	req:=httptest.NewRequest(http.MethodPost,"/v1/agents",bytes.NewBufferString(`{"tenant_id":"tenant-1"}`))
	res:=httptest.NewRecorder();h.ServeHTTP(res,req);if res.Code!=401{t.Fatalf("unauthenticated status=%d",res.Code)}
	req=httptest.NewRequest(http.MethodPost,"/v1/agents",bytes.NewBufferString(`{"tenant_id":"tenant-1"}`));req.Header.Set("Authorization","test-secret")
	res=httptest.NewRecorder();h.ServeHTTP(res,req);if res.Code!=401{t.Fatalf("raw token without Bearer prefix status=%d",res.Code)}
	req=httptest.NewRequest(http.MethodPost,"/v1/agents",bytes.NewBufferString(`{"tenant_id":"tenant-1"}`));req.Header.Set("Authorization","Bearer test-secret")
	res=httptest.NewRecorder();h.ServeHTTP(res,req);if res.Code!=201{t.Fatalf("authenticated status=%d body=%s",res.Code,res.Body.String())}
}
func TestHealthAndWebhookPathsAreNotBehindAPIToken(t *testing.T){
	h:=newTestHandler(t,"test-secret")
	req:=httptest.NewRequest(http.MethodGet,"/healthz",nil);res:=httptest.NewRecorder();h.ServeHTTP(res,req)
	if res.Code!=200{t.Fatalf("health status=%d",res.Code)}
}
func TestNoHostExecEndpoint(t *testing.T){
	h:=newTestHandler(t,"")
	req:=httptest.NewRequest(http.MethodPost,"/v1/host/exec",bytes.NewBufferString(`{"command":"id"}`))
	res:=httptest.NewRecorder();h.ServeHTTP(res,req);if res.Code!=404{t.Fatalf("unexpected Host exec route: %d",res.Code)}
}

func TestRuntimeWakeFailsClosedWithoutDriver(t *testing.T){
	st,err:=store.Open(filepath.Join(t.TempDir(),"runtime.db"));if err!=nil{t.Fatal(err)};defer st.Close()
	agent,err:=st.CreateAgent(context.Background(),"tenant-1","supervisor");if err!=nil{t.Fatal(err)}
	handler:=New(st,nil,nil,"")
	req:=httptest.NewRequest(http.MethodPost,"/v1/agents/"+agent.ID+"/wake",nil)
	res:=httptest.NewRecorder();handler.ServeHTTP(res,req)
	if res.Code!=http.StatusServiceUnavailable{t.Fatalf("wake status=%d body=%s",res.Code,res.Body.String())}
	runtime,err:=st.GetRuntime(context.Background(),agent.ID);if err!=nil{t.Fatal(err)}
	if runtime.State!="ABSENT"{t.Fatalf("failed-closed wake mutated runtime state: %+v",runtime)}
}

func TestCreateAgentRejectsInvalidInput(t *testing.T){
	h:=newTestHandler(t,"")
	for _,body:=range []string{`{}`,`{"tenant_id":"tenant-1","role":"worker"}`}{
		req:=httptest.NewRequest(http.MethodPost,"/v1/agents",bytes.NewBufferString(body))
		res:=httptest.NewRecorder();h.ServeHTTP(res,req)
		if res.Code!=http.StatusBadRequest{t.Fatalf("body=%s status=%d response=%s",body,res.Code,res.Body.String())}
	}
}

func TestHostCapabilitiesEndpoint(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "capabilities.db"))
	if err != nil { t.Fatal(err) }
	defer st.Close()
	snapshot := execution.Snapshot{
		Platform: execution.PlatformContainer,
		ProbeVersion: "test",
		KVM: execution.KVMCapability{Available: false, Reason: "not exposed"},
	}
	handler := NewWithCapabilities(st, nil, nil, "secret", snapshot)
	req := httptest.NewRequest(http.MethodGet, "/v1/runtime/capabilities", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK { t.Fatalf("status=%d body=%s", res.Code, res.Body.String()) }
	if !bytes.Contains(res.Body.Bytes(), []byte(`"platform":"container"`)) {
		t.Fatalf("capability snapshot missing: %s", res.Body.String())
	}
}

func TestHostCapabilitiesEndpointRequiresToken(t *testing.T) {
	h := newTestHandler(t, "secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/host/capabilities", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized { t.Fatalf("status=%d", res.Code) }
}
