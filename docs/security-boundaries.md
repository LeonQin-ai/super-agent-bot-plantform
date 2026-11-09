# Security boundaries

- No host.exec, host.shell, host.python, system(), popen() or generic Host process API is exposed to an Agent.
- Shell, scripts, compilers, tests, Git hooks, MCP stdio and third-party plugins execute only inside Worker Guest.
- Missing or unhealthy Guest Executor means fail closed. Never fall back to Host.
- Worker creation is mediated by Agent Manager admission control.
- Guest cannot mount Host rootfs or access /dev/kvm, Docker socket, Firecracker API socket or Host management UDS.
- Non-loopback HTTP binding requires LIGHT_AGENT_API_TOKEN; /v1 routes require Bearer authentication when configured.

## Host File Broker
Bind each capability to tenant, Agent, Worker and immutable authorized root ID. Resolve paths relative to a pre-opened directory FD. Prefer openat2 with RESOLVE_BENEATH and RESOLVE_NO_MAGICLINKS. Reject traversal, magic links, unauthorized symlinks, device nodes and paths outside the authorized root. Source is read-only; mutations go to a private overlay. Enforce file-size, directory-depth, concurrency, byte and rate limits. Audit operation metadata, never file contents or secrets.

## Guest executor
Use a structured ExecuteRequest with executable, argv, working directory, timeout and resource limits; never accept a Host shell command string. Inside Guest validate inputs; create task cgroup v2; set CPU/memory/PID/IO limits; apply namespaces; drop UID/GID and capabilities; set NoNewPrivileges and a tested seccomp profile; execute with execve/execveat; bound output; cancel the entire task cgroup. Host cgroups separately constrain each Firecracker VMM.

## C11 SuperAgent
Cognitive OS currently contains built-in tools, plugin runtime and execution paths. Do not launch it as an unrestricted Host process and do not expose its task API to untrusted callers. The production adapter must use a dedicated state root and restricted tool registry, authenticate its local API, audit every capability, and prove that user code cannot execute on the Host. Until then, Super Agent Bot Plantform keeps Runtime Driver unconfigured and wake/sleep endpoints fail closed.

## Feishu ingress
Verify request signature and verification token. Decrypt encrypted event payloads before parsing. Deduplicate by event ID and persist event/task atomically before ACK. Initial implementation accepts direct messages only and requires explicit sender Open ID allowlist. Group messages are rejected. Never place App Secret, encryption key or API credentials in logs, Agent Memory, Guest images or normal files.

Do not promise exactly-once command execution. Use stable task/execution IDs, query execution state before retry, and never automatically retry non-idempotent operations.
