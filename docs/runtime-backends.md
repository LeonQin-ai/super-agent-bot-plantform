# Risk-tiered execution: Container, MicroVM and WASM

## Platform classification and capability detection

Do not classify all virtual machines as lacking MicroVM support. A physical host may lack KVM because firmware/modules/permissions are unavailable, while a VM may expose nested KVM. Probe the actual runtime capabilities independently of platform class. See [host-capability.md](host-capability.md) for exact Linux detection and scheduling rules.

## Decision for hosts without nested virtualization

A 4-vCPU/8-GiB VM without usable `/dev/kvm` cannot run Firecracker. Do not try to emulate KVM or silently downgrade a requested MicroVM. Super Agent Bot Plantform must support OCI/Docker execution as a first-class backend and retain Firecracker as an optional backend for hosts that have KVM.

The word "Sandbox" is a security profile, not a synonym for Docker. A plain Docker container uses namespaces/cgroups and shares the host kernel. It is useful for packaging and ordinary isolation, but should not be described as a VM-strength boundary.

## Risk tiers and execution backends

| Risk tier | Execution boundary | KVM required | Intended use |
|---|---|---:|---|---|
| Trusted internal work | Docker Engine + runc | No | Approved internal tools and services; shared Host kernel |
| External/untrusted code | Firecracker + Jailer | Yes | Independent Guest kernel; preferred strong boundary for arbitrary code |
| Lightweight extensions | WASM runtime (Wasmtime/WasmEdge) | No | Capability-scoped plugins, transforms and compute functions |
| Untrusted work without KVM | Docker Engine + gVisor `runsc` | No for the standard ptrace platform | Compatibility fallback only when policy explicitly permits; not equivalent to MicroVM |

Backend selection is a security-policy decision, not a performance-only preference. A MicroVM-required task must fail closed or be routed to a remote KVM worker pool when local KVM is unavailable. It must never silently downgrade to runc or runsc. gVisor may be selected only when the task policy explicitly allows that weaker boundary. WASM is appropriate only when the task fits the WASI/capability model; it is not a drop-in replacement for arbitrary Linux processes.

gVisor is still not a hardware VM: it shares the host kernel for the Sentry's host-facing operations and has compatibility/performance trade-offs. For high-risk multi-tenant workloads, use Firecracker on a KVM-capable host or a separate worker host.

## Super Agent Bot Plantform component placement

- Go Control Plane: Docker service; owns Agent identity, durable task queue, Feishu/API ingress, quotas and policy.
- Cognitive OS SuperAgent: long-lived service/container; can plan, manage Agent identities and request approved capabilities. It has no Host shell, Docker socket or arbitrary Host filesystem access.
- Rust Sandbox Manager: trusted Host-side daemon. It alone talks to Docker Engine or Firecracker. Expose only an authenticated loopback/mTLS API; never expose the Docker socket to Agents.
- Worker: one ephemeral runc container for approved internal work; one Firecracker Guest for external arbitrary code; one WASM instance for constrained plugins/functions. gVisor is an explicitly approved alternative for hosts without KVM.
- Rust cos-execd: runs inside the Worker image and accepts structured executable + argv requests. Never accept an arbitrary Host shell command string.
- Workspace Broker: only grants a per-task workspace capability. Source is read-only by default; writes go to a private overlay and are committed after validation.

The SuperAgent may run in a normal Docker service while all untrusted command/build/test/MCP execution runs in a Worker Sandbox. This preserves the desired Host-resident orchestrator behavior without giving it Host command execution.

## Required container hardening

Every Worker container, including plain Docker mode, must use:
- non-root UID/GID; `--cap-drop=ALL`; `no-new-privileges`;
- read-only root filesystem; bounded tmpfs for `/tmp`;
- per-task CPU, memory and PID limits; place the workspace base on a quota-controlled filesystem until per-workspace disk quotas are integrated;
- default-deny network; explicit egress policy only when required;
- seccomp and AppArmor/SELinux policy; user namespace remapping where supported;
- no Host PID/network namespace, no privileged mode, no device passthrough;
- no Docker socket, Host rootfs, KVM device, sandbox manager socket or cloud credentials;
- ephemeral container and writable layer; destroy after task completion.

In `sandbox` mode add Docker runtime `runsc` (gVisor) and pin the runtime/image versions. Verify the installed runtime before accepting jobs.

Do not bind-mount an arbitrary Host directory. The broker should create a dedicated workspace directory for the task and mount only that path. Avoid mounting the same writable workspace into concurrent Workers unless an explicit locking/transaction policy exists.

## Configuration contract

The intended configuration is:
- `LIGHT_AGENT_EXECUTION_BACKEND=docker|microvm|wasm|gvisor`
- `LIGHT_AGENT_DOCKER_SOCKET=unix:///var/run/docker.sock`
- `LIGHT_AGENT_DOCKER_RUNTIME=runc|runsc`
- `LIGHT_AGENT_SANDBOX_REQUIRE_GVISOR=true`
- `LIGHT_AGENT_WASM_RUNTIME=wasmtime|wasmedge`
- `LIGHT_AGENT_WASM_FUEL_LIMIT=<integer>`
- `LIGHT_AGENT_WASM_MEMORY_LIMIT=128MiB`
- `LIGHT_AGENT_WASM_ALLOWED_CAPABILITIES=clock,random,workspace-read`
- `LIGHT_AGENT_WORKER_IMAGE=<immutable image digest>`
- `LIGHT_AGENT_WORKER_CPU_LIMIT=1.0`
- `LIGHT_AGENT_WORKER_MEMORY_LIMIT=1g`
- `LIGHT_AGENT_WORKER_PIDS_LIMIT=128`
- `LIGHT_AGENT_WORKER_NETWORK=none`

These settings define the target interface. The Docker/runsc provider and Go lifecycle adapter must be implemented and tested before claiming the backend is production-ready. Current `cos-sandboxd` is the Firecracker/KVM provider; it is not yet a Docker provider.

## 4 vCPU / 8 GiB VM starting profile

Reserve approximately 1.5-2 GiB for the OS, Docker daemon, Control Plane and SuperAgent. Leave at least 1 GiB for burst and filesystem cache. Start with one Worker at 1 vCPU / 1 GiB; increase to two concurrent Workers only after measuring peak RSS, CPU throttling, task latency and OOM behavior. For compile-heavy jobs use one Worker with 2 vCPU / 2-3 GiB rather than oversubscribing memory.

The number of logical Agent identities can be much larger than the number of running Worker containers. Agent identity, Memory and task journal are persistent data; containers are disposable execution slots.

## Acceptance tests

1. Docker mode creates, executes, cancels and destroys an OCI Worker without KVM.
2. gVisor mode refuses startup if `runsc` is absent and proves the created container uses `runsc`.
3. MicroVM-required tasks are never downgraded to containers; unavailable local KVM causes fail-closed or explicit remote-worker routing.
4. WASM plugins cannot access undeclared filesystem, network or host capabilities; fuel and memory limits are enforced.
5. Worker has no Docker socket, /dev/kvm, Host PID namespace or Host rootfs.
6. CPU/memory/PID limits are observable and enforced.
7. Network is denied by default; only explicit allowlisted egress works.
8. Container exit, daemon restart and API timeout reconcile state without duplicate execution.
9. Workspace traversal, symlink, reserved metadata and concurrent-commit tests pass.
10. Firecracker mode continues to require KVM and remains an optional backend.
