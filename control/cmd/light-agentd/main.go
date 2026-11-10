package main

import(
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/config"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/cognitive"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/dispatcher"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/execution"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/feishu"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/httpapi"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/inference"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/jobs"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/news"
	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store"
)

func main(){
	cfg:=config.Load()
	logger:=slog.New(slog.NewJSONHandler(os.Stdout,&slog.HandlerOptions{Level:parseLevel(cfg.LogLevel)}))
	if !isLoopbackBind(cfg.ListenAddr)&&cfg.APIAuthToken==""{
		logger.Error("refusing non-loopback bind without LIGHT_AGENT_API_TOKEN","addr",cfg.ListenAddr)
		os.Exit(2)
	}
	st,err:=store.Open(cfg.DBPath);if err!=nil{logger.Error("open state store","error",err);os.Exit(1)}
	defer st.Close()
	ctx,stop:=signal.NotifyContext(context.Background(),syscall.SIGINT,syscall.SIGTERM);defer stop()
	appRegistry := os.Getenv("FEISHU_APPLICATIONS_FILE")
	if appRegistry != "" && cfg.APIAuthToken == "" {
		logger.Error("multi-application Feishu requires the administrative API token")
		os.Exit(2)
	}
	webhook, err := feishu.LoadApplications(st,feishu.Config{Token:cfg.FeishuVerificationToken,EncryptKey:cfg.FeishuEncryptKey,Allowed:cfg.FeishuAllowedOpenIDs,AllowedFile:os.Getenv("FEISHU_ALLOWED_OPEN_ID_FILE")},os.Getenv("FEISHU_APP_ID"),appRegistry)
	if err != nil {
		logger.Error("invalid Feishu application registry", "error", err)
		os.Exit(2)
	}
	probeCtx,probeCancel:=context.WithTimeout(ctx,3*time.Second)
	capabilities:=execution.Detect(probeCtx,execution.ProbeOptions{DockerSocket:cfg.DockerSocket,WASMRuntime:cfg.WASMRuntime})
	var capabilityRegistry *execution.CapabilityRegistry
if cfg.SandboxManagerURL != "" {
		capabilityRegistry = execution.NewPersistentCapabilityRegistry(90*time.Second, func(fetchCtx context.Context) (execution.Snapshot, error) {
			return execution.FetchHostCapabilities(fetchCtx, cfg.SandboxManagerURL, cfg.SandboxManagerToken)
		}, st)
		hostProbeCtx, hostProbeCancel := context.WithTimeout(ctx,3*time.Second)
		fetchErr := capabilityRegistry.Refresh(hostProbeCtx)
		hostProbeCancel()
		if fetchErr != nil {
			logger.Error("Host Sandbox Manager capability probe failed; execution remains unavailable", "error", fetchErr)
		}
		capabilities = capabilityRegistry.CurrentSnapshot()
		go capabilityRegistry.Run(ctx, 30*time.Second, 3*time.Second, func(err error) {
			logger.Error("Host capability refresh failed; execution disabled until recovery", "error", err)
		})
	}
	probeCancel()
	logger.Info("execution capabilities detected","platform",capabilities.Platform,"observation_scope",capabilities.ObservationScope,"kvm",capabilities.KVM.Available,"docker",capabilities.Docker.Available,"firecracker_ready",capabilities.Firecracker.Ready,"gvisor_ready",capabilities.GVisor.Ready,"wasm_ready",capabilities.WASM.Ready)
	// Runtime endpoints remain fail-closed until a restricted C11 SuperAgent
	// driver is configured. User workloads are never executed by this process.
	var scheduler *execution.Scheduler
	if cfg.SandboxManagerURL!="" && cfg.SandboxManagerToken!="" {
		client,clientErr:=execution.NewDockerWorkerClient(cfg.SandboxManagerURL,cfg.SandboxManagerToken)
		if clientErr!=nil { logger.Error("Docker Worker client configuration rejected","error",clientErr) } else { scheduler=execution.NewScheduler(client) }
	}
	api:=httpapi.NewWithCapabilities(st,nil,webhook,cfg.APIAuthToken,capabilities,scheduler)
	if capabilityRegistry != nil { api=httpapi.NewWithCapabilitySource(st,nil,webhook,cfg.APIAuthToken,capabilityRegistry,scheduler) }
	taskDispatcher:=dispatcher.NewWithExecution(st,logger,scheduler,capabilityRegistry)
	taskDispatcher.ConfigureScopedReplies(webhook.Reply)
	modelBase, modelName, modelKey := os.Getenv("LIGHT_AGENT_MODEL_BASE_URL"), os.Getenv("LIGHT_AGENT_MODEL"), os.Getenv("LIGHT_AGENT_MODEL_API_KEY")
	cognitiveImage := os.Getenv("LIGHT_AGENT_COGNITIVE_IMAGE")
	if modelBase != "" || modelName != "" || modelKey != "" || cognitiveImage != "" {
		modelClient, err := inference.New(modelBase, modelName, modelKey)
		if err != nil {
			logger.Error("invalid model configuration", "error", err)
			os.Exit(2)
		}
		replier := feishu.Replier{AppID: os.Getenv("FEISHU_APP_ID"), AppSecret: os.Getenv("FEISHU_APP_SECRET")}
		var reply dispatcher.MessageReply
		if replier.AppID != "" && replier.AppSecret != "" {
			reply = replier.Reply
		}
		taskDispatcher.ConfigureMessages(modelClient, reply)
		if cognitiveImage != "" {
			runtime, err := cognitive.New(cognitiveImage, os.Getenv("LIGHT_AGENT_COGNITIVE_WORKSPACES"), modelClient)
			if err != nil {
				logger.Error("invalid Cognitive OS configuration", "error", err)
				os.Exit(2)
			}
			taskDispatcher.ConfigureAgentRuntime(jobs.Wrap(news.Wrap(runtime.Run, runtime.RunBriefing)))
		}
	}
	if err:=taskDispatcher.Recover(ctx);err!=nil { logger.Error("task reconciliation failed; refusing admission","error",err);os.Exit(1) }
	dispatchDone:=make(chan struct{})
	go func(){defer close(dispatchDone);taskDispatcher.Run(ctx)}()
	server:=httpapi.HTTPServer(cfg.ListenAddr,api)
	go func(){logger.Info("light-agent control plane listening","addr",cfg.ListenAddr,"db",cfg.DBPath);if err:=server.ListenAndServe();err!=nil&&!errors.Is(err,http.ErrServerClosed){logger.Error("http server failed","error",err);stop()}}()
	<-ctx.Done()
	shutdownCtx,cancel:=context.WithTimeout(context.Background(),30*time.Second);defer cancel()
	if err:=server.Shutdown(shutdownCtx);err!=nil{logger.Error("http server shutdown","error",err)}
	select { case <-dispatchDone: case <-shutdownCtx.Done(): logger.Error("task dispatcher shutdown timed out; recovery will clean recorded Workers") }
	logger.Info("light-agent stopped")
}

func isLoopbackBind(addr string)bool{
	host,_,err:=net.SplitHostPort(addr);if err!=nil{return false}
	if host=="localhost"{return true}
	ip:=net.ParseIP(strings.Trim(host,"[]"))
	return ip!=nil&&ip.IsLoopback()
}
func parseLevel(v string)slog.Level{switch v{case "debug":return slog.LevelDebug;case "warn":return slog.LevelWarn;case "error":return slog.LevelError;default:return slog.LevelInfo}}
