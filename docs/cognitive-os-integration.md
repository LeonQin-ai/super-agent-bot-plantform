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

There is deliberately no production C11 driver yet. The pinned runtime includes tool and plugin execution paths; launching it as a Host service before capability restriction would violate the Super Agent Bot Plantform trust boundary. Runtime wake/sleep HTTP endpoints therefore return `503 runtime_driver_unavailable` until a reviewed driver is wired in.
