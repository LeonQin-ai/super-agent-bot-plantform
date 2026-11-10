# Cognitive OS integration contract

This document records the adapter boundary for the pinned Cognitive OS revision in `COGNITIVE_OS_REF`.

## What the pinned C11 runtime exposes

The public umbrella header `cognitive-os-agent.h` exposes:
- `init(runtime_ctx *, const config *)` to assemble the runtime.
- `run(runtime_ctx *, prompt, answer)` and `orchestrate(...)` for synchronous reasoning.
- `serve(runtime_ctx *)` / `stop(runtime_ctx *)` for the HTTP API lifecycle.
- `runtime_shutdown(runtime_ctx *)` for orderly teardown.
- `state_export` / `state_import` for process-state transfer.
- `runtime_ctx` contains scheduler, Memory, state store, task journal, flow store, tool registry, plugin registry and MCP manager.

The REST API documents `POST /v1/tasks` with a `prompt`, `GET /v1/tasks/{id}`, and status/observability endpoints. The C11 API is not itself a Firecracker lifecycle API.

## Adapter rules

1. The Go Control Plane owns durable Agent identity, generation fencing, task admission and lifecycle state.
2. A C11 SuperAgent instance receives a dedicated state root and workspace per Agent/tenant boundary. Never share mutable state roots between tenants.
3. Persist Memory, Session, task journal and flow state before graceful shutdown. A process restart must restore and reconcile state before accepting work.
4. Authenticate the local C11 HTTP API. The pinned C11 server allows open access when no auth key is configured; that default is unacceptable for a production adapter.
5. The Host SuperAgent must use a restricted capability profile. Do not register Host shell/process execution, arbitrary filesystem writes, unrestricted MCP stdio or third-party plugin execution.
6. Worker command execution, build/test scripts and MCP stdio belong in Worker MicroVM Guests only. The C11 Host adapter may submit structured orchestration requests to Agent Manager and File Broker, not spawn user commands.
7. Use runtime generation as a fencing token on every adapter request and callback. Reject callbacks from a previous generation.
8. Shutdown is a quiesce protocol, not just SIGTERM: stop admission, drain active reasoning/tool callbacks, persist state, stop the C11 context, then mark Runtime STOPPED.

## Current status

Super Agent Bot Plantform implements the Go-side Runtime record, single-flight wake reservation, generation fencing, sleep admission checks and an injectable `lifecycle.Driver` interface.

The optional Cognitive OS adapter runs one C11 process per API or Feishu message in a disposable
Docker Worker. The Agent's private workspace persists across messages. Runtime
state lives in a temporary filesystem for the duration of the task. The C11
policy allows file reads, writes, edits, directory listings, globbing and text
search. Docker disables networking. The worker relays model requests over
stdio to the control plane, which uses the configured model endpoint and key.

On Linux, first build the pinned Cognitive OS base image with
[`Dockerfile.agent-capacity`](../deploy/Dockerfile.agent-capacity), using a
`cognitive-os-agent` binary built from `COGNITIVE_OS_REF`. Then build the
message Worker image:

```bash
build_dir=$(mktemp -d)
CGO_ENABLED=0 go build -o "$build_dir/sabp-cognitive-worker" ./control/cmd/cognitive-worker
docker build --build-arg COGNITIVE_BASE_IMAGE=light-agent/cognitive-capacity-recipe:6bdfc849 \
  -f deploy/Dockerfile.cognitive-worker -t light-agent/cognitive-feishu:6bdfc849 "$build_dir"
docker image inspect light-agent/cognitive-feishu:6bdfc849 --format '{{.Id}}'
```

Set `LIGHT_AGENT_COGNITIVE_IMAGE` to that local image ID and
`LIGHT_AGENT_COGNITIVE_WORKSPACES` to an absolute private directory for Agent
workspaces. Use the existing [Feishu and model settings](../README.md#connect-a-feishu-bot-and-model-on-linux).
The control plane must be able to run Docker and create directories owned by
the Worker UID. Restart it, then ask the bot to write a file and read it back
in a separate message. API clients can also create an Agent with `POST /v1/agents`,
submit a task with `POST /v1/agents/{id}/messages` using
`{"request_id":"write-notes","content":"Create notes.txt in the workspace"}`,
and poll `GET /v1/tasks/{id}` with the returned task ID. API tasks return their
results through the task endpoint.

Runtime wake/sleep HTTP endpoints still return `503 runtime_driver_unavailable`.
The per-message adapter does not implement the production lifecycle
driver above.
