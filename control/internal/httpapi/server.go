package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/execution"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/lifecycle"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/model"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

type Server struct {
	store store.Store
	manager *lifecycle.Manager
	apiToken string
	capabilities execution.Snapshot
	capabilitySource execution.CapabilitySource
	scheduler *execution.Scheduler
	mux *http.ServeMux
}

func New(st store.Store, manager *lifecycle.Manager, feishuWebhook http.Handler, apiToken string) *Server {
	return NewWithCapabilities(st,manager,feishuWebhook,apiToken,execution.Snapshot{Platform:execution.PlatformUnknown,ProbeVersion:"not-probed"})
}

func NewWithCapabilities(st store.Store, manager *lifecycle.Manager, feishuWebhook http.Handler, apiToken string, capabilities execution.Snapshot, schedulers ...*execution.Scheduler) *Server {
	s:=&Server{store:st,manager:manager,apiToken:apiToken,capabilities:capabilities,mux:http.NewServeMux()}
	if len(schedulers)>0 { s.scheduler=schedulers[0] }
	s.mux.HandleFunc("GET /healthz",s.health)
	s.mux.HandleFunc("GET /v1/runtime/capabilities",s.getCapabilities)
	s.mux.HandleFunc("POST /v1/execution/run",s.runExecution)
	s.mux.HandleFunc("POST /v1/agents",s.createAgent)
	s.mux.HandleFunc("GET /v1/agents/{id}",s.getAgent)
	s.mux.HandleFunc("GET /v1/agents/{id}/runtime",s.getRuntime)
	s.mux.HandleFunc("POST /v1/agents/{id}/wake",s.wakeAgent)
	s.mux.HandleFunc("POST /v1/agents/{id}/sleep",s.sleepAgent)
	s.mux.HandleFunc("POST /v1/agents/{id}/messages",s.submitMessage)
	s.mux.HandleFunc("POST /v1/agents/{id}/executions",s.submitExecution)
	s.mux.HandleFunc("GET /v1/tasks/{id}",s.getTask)
	s.mux.HandleFunc("POST /v1/tasks/{id}/cancel",s.cancelTask)
	if feishuWebhook!=nil { s.mux.Handle("POST /webhooks/feishu",feishuWebhook) }
	return s
}

// NewWithCapabilitySource reads refreshed capabilities for every decision.
func NewWithCapabilitySource(st store.Store, manager *lifecycle.Manager, feishuWebhook http.Handler, apiToken string, source execution.CapabilitySource, scheduler *execution.Scheduler) *Server {
	s := NewWithCapabilities(st, manager, feishuWebhook, apiToken, execution.Snapshot{}, scheduler)
	s.capabilitySource = source
	return s
}

func (s *Server) currentCapabilities() execution.Snapshot {
	if s.capabilitySource != nil { return s.capabilitySource.CurrentSnapshot() }
	return s.capabilities
}

func (s *Server) ServeHTTP(w http.ResponseWriter,r *http.Request) {
	if strings.HasPrefix(r.URL.Path,"/v1/") && s.apiToken!="" {
		authorization:=r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization,"Bearer ") {
			writeError(w,http.StatusUnauthorized,"unauthorized","valid bearer token required")
			return
		}
		provided:=strings.TrimSpace(strings.TrimPrefix(authorization,"Bearer "))
		if len(provided)!=len(s.apiToken)||subtle.ConstantTimeCompare([]byte(provided),[]byte(s.apiToken))!=1 {
			writeError(w,http.StatusUnauthorized,"unauthorized","valid bearer token required")
			return
		}
	}
	s.mux.ServeHTTP(w,r)
}

func(s *Server)health(w http.ResponseWriter,_ *http.Request){writeJSON(w,200,map[string]string{"status":"ok"})}

func(s *Server)getCapabilities(w http.ResponseWriter,_ *http.Request){writeJSON(w,http.StatusOK,s.currentCapabilities())}

func(s *Server)createAgent(w http.ResponseWriter,r *http.Request){
	var req struct{TenantID string `json:"tenant_id"`;Role string `json:"role"`}
	if err:=decodeJSON(w,r,&req);err!=nil{writeError(w,400,"invalid_request",err.Error());return}
	tenantID,role:=strings.TrimSpace(req.TenantID),strings.TrimSpace(req.Role)
	if tenantID==""{writeError(w,400,"invalid_request","tenant_id is required");return}
	if role!=""&&role!="supervisor"{writeError(w,400,"invalid_request","only supervisor identities may be created through this API");return}
	agent,err:=s.store.CreateAgent(r.Context(),tenantID,role)
	if err!=nil{writeStoreError(w,err);return}
	writeJSON(w,201,agent)
}
func(s *Server)getAgent(w http.ResponseWriter,r *http.Request){
	agent,err:=s.store.GetAgent(r.Context(),r.PathValue("id"));if err!=nil{writeStoreError(w,err);return}
	writeJSON(w,200,agent)
}
func(s *Server)getRuntime(w http.ResponseWriter,r *http.Request){
	runtime,err:=s.store.GetRuntime(r.Context(),r.PathValue("id"));if err!=nil{writeStoreError(w,err);return}
	writeJSON(w,200,runtime)
}
func(s *Server)wakeAgent(w http.ResponseWriter,r *http.Request){
	if s.manager==nil{writeError(w,503,"runtime_driver_unavailable","no trusted SuperAgent runtime driver is configured");return}
	runtime,err:=s.manager.Wake(r.Context(),r.PathValue("id"));if err!=nil{writeLifecycleError(w,err);return}
	status:=http.StatusOK
	if runtime.State==model.RuntimeStarting{status=http.StatusAccepted}
	writeJSON(w,status,runtime)
}
func(s *Server)sleepAgent(w http.ResponseWriter,r *http.Request){
	if s.manager==nil{writeError(w,503,"runtime_driver_unavailable","no trusted SuperAgent runtime driver is configured");return}
	runtime,err:=s.manager.Sleep(r.Context(),r.PathValue("id"));if err!=nil{writeLifecycleError(w,err);return}
	status:=http.StatusOK
	if runtime.State==model.RuntimeStopping{status=http.StatusAccepted}
	writeJSON(w,status,runtime)
}
func(s *Server)submitMessage(w http.ResponseWriter,r *http.Request){
	var req struct{RequestID string `json:"request_id"`;Content string `json:"content"`}
	if err:=decodeJSON(w,r,&req);err!=nil{writeError(w,400,"invalid_request",err.Error());return}
	req.Content=strings.TrimSpace(req.Content);if req.Content==""{writeError(w,400,"invalid_request","content is required");return}
	if req.RequestID==""{req.RequestID=r.Header.Get("Idempotency-Key")}
	payload,_:=json.Marshal(map[string]string{"source":"api","content":req.Content})
	task,err:=s.store.CreateTask(r.Context(),r.PathValue("id"),req.RequestID,string(payload))
	if err!=nil{writeStoreError(w,err);return}
	writeJSON(w,202,task)
}
func(s *Server)getTask(w http.ResponseWriter,r *http.Request){task,err:=s.store.GetTask(r.Context(),r.PathValue("id"));if err!=nil{writeStoreError(w,err);return};writeJSON(w,200,task)}
func(s *Server)cancelTask(w http.ResponseWriter,r *http.Request){task,err:=s.store.CancelTask(r.Context(),r.PathValue("id"));if err!=nil{writeStoreError(w,err);return};writeJSON(w,200,task)}

func decodeJSON(w http.ResponseWriter,r *http.Request,dst any)error{
	r.Body=http.MaxBytesReader(w,r.Body,1<<20);dec:=json.NewDecoder(r.Body);dec.DisallowUnknownFields()
	if err:=dec.Decode(dst);err!=nil{return err}
	if err:=dec.Decode(&struct{}{});!errors.Is(err,io.EOF){return errors.New("request must contain exactly one JSON object")}
	return nil
}
func writeStoreError(w http.ResponseWriter,err error){
	switch{case errors.Is(err,store.ErrNotFound):writeError(w,404,"not_found","resource not found")
	case errors.Is(err,store.ErrConflict):writeError(w,409,"state_conflict","resource state changed")
	default:writeError(w,500,"internal_error","internal storage error")}
}
func writeLifecycleError(w http.ResponseWriter,err error){
	if errors.Is(err,lifecycle.ErrDriverUnavailable){writeError(w,503,"runtime_driver_unavailable",err.Error());return}
	writeStoreError(w,err)
}
func writeError(w http.ResponseWriter,status int,code,message string){writeJSON(w,status,map[string]string{"error":code,"message":message})}
func writeJSON(w http.ResponseWriter,status int,value any){w.Header().Set("Content-Type","application/json");w.WriteHeader(status);_=json.NewEncoder(w).Encode(value)}
func HTTPServer(addr string,handler http.Handler)*http.Server{return &http.Server{Addr:addr,Handler:handler,ReadHeaderTimeout:5*time.Second,ReadTimeout:15*time.Second,WriteTimeout:6*time.Minute,IdleTimeout:60*time.Second}}

func(s *Server)runExecution(w http.ResponseWriter,r *http.Request){
 if s.scheduler==nil { writeError(w,http.StatusServiceUnavailable,"execution_provider_unavailable","Sandbox Manager Docker provider is not configured");return }
 var req struct {
  Mode execution.Mode `json:"mode"`
  Tier execution.TrustTier `json:"trust_tier"`
  TaskID string `json:"task_id"`
  CPU uint32 `json:"cpu_millis"`
  Memory uint64 `json:"memory_bytes"`
  PIDs uint64 `json:"pids_limit"`
  Executable string `json:"executable"`
  Argv []string `json:"argv"`
  CWD string `json:"cwd"`
  Timeout uint64 `json:"timeout_ms"`
  Output uint64 `json:"max_output_bytes"`
 }
 if err:=decodeJSON(w,r,&req);err!=nil {writeError(w,400,"invalid_request",err.Error());return}
 if req.TaskID=="" || len(req.TaskID)>96 {writeError(w,400,"invalid_request","task_id must be 1..96 characters");return}
 for _,ch:=range req.TaskID { if !(ch>='a'&&ch<='z'||ch>='A'&&ch<='Z'||ch>='0'&&ch<='9'||ch=='-'||ch=='_') {writeError(w,400,"invalid_request","task_id may contain only ASCII letters, digits, '-' and '_'");return} }
 if req.Tier!=execution.TierTrustedInternal {writeError(w,403,"trust_tier_not_admitted","this synchronous Docker endpoint accepts only trusted_internal work; external_untrusted work requires a MicroVM");return}
 if req.Executable=="" {writeError(w,400,"invalid_request","absolute executable is required");return}
 if req.Mode=="" {req.Mode=execution.ModeStrict}
 if req.CPU==0 {req.CPU=1000}; if req.Memory==0 {req.Memory=1024*1024*1024}; if req.PIDs==0 {req.PIDs=128}; if req.Timeout==0 {req.Timeout=15000}; if req.Output==0 {req.Output=1024*1024}
 result,err:=s.scheduler.Run(r.Context(),execution.RunRequest{
  Mode:req.Mode,Policy:execution.TaskPolicy{Tier:req.Tier},Host:s.currentCapabilities(),
  Worker:execution.WorkerSpec{WorkerID:"api-"+req.TaskID,WorkspaceID:req.TaskID,CPUMillis:req.CPU,MemoryBytes:req.Memory,PidsLimit:req.PIDs},
  Command:execution.ExecSpec{Executable:req.Executable,Argv:req.Argv,CWD:req.CWD,TimeoutMS:req.Timeout,MemoryBytes:req.Memory,MaxOutputBytes:req.Output,Network:false},
 })
 if err!=nil {writeError(w,422,"execution_rejected",err.Error());return}
 writeJSON(w,http.StatusOK,result)
}
