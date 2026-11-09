# Host Capability Manager

## Purpose

Super Agent Bot Plantform must distinguish where it is deployed and what the Linux node can actually execute. Do not infer execution support from labels such as "physical server" or "VM" alone. Platform type and runtime capabilities are separate facts.

A physical server may have KVM disabled in firmware, missing kernel modules or no accessible `/dev/kvm`. A VM may expose nested virtualization and a usable KVM device. Therefore, the scheduler must use verified capability probes, not a single platform heuristic.

## Two independent dimensions

### Deployment platform

- `bare_metal`: platform detection indicates physical hardware.
- `virtual_machine`: DMI/hypervisor/systemd detection indicates a guest VM.
- `container`: Super Agent Bot Plantform itself runs inside a container.
- `unknown`: evidence is absent or conflicting.

### Execution capabilities

Probe each independently and record evidence:
- `docker_oci`: Docker Engine/compatible OCI daemon reachable by the trusted Host Sandbox Manager.
- `kvm`: `/dev/kvm` can be opened and `KVM_GET_API_VERSION` returns the expected API version; device existence alone is insufficient.
- `firecracker`: supported architecture, usable KVM, required kernel features, Firecracker/Jailer binaries and permissions, cgroup v2 and networking prerequisites.
- `gvisor_runsc`: runsc installed, registered in Docker/containerd, and a disposable preflight container starts using the requested platform.
- `wasm_wasmtime` / `wasm_wasmedge`: runtime installed and a version-pinned smoke test passes.
- `cgroup_v2`: unified hierarchy mounted and required controllers delegated.
- `network_isolation`: required firewall/network namespace primitives and policy enforcement are available.
- `resource_limits`: CPU, memory, pids and I/O controllers available for the selected backend.

## Linux discovery

Use multiple signals for platform classification:
- `systemd-detect-virt --vm` and `systemd-detect-virt --container`.
- `/sys/class/dmi/id/sys_vendor`, `product_name`, `product_version` where readable.
- `/sys/hypervisor/type` and architecture-specific hypervisor indicators.
- `virt-what` when installed.

Do not fail deployment merely because platform classification is `unknown`; use the verified runtime capabilities to make safe scheduling decisions.

KVM probe:
1. Check `/dev/kvm` exists and is a character device.
2. Open it with the same UID/GID used by the Host Sandbox Manager.
3. Call ioctl `KVM_GET_API_VERSION` (request 0xAE00); require the expected API version (12).
4. Record the error if access is denied, module unavailable or ioctl fails.

Firecracker probe must be stronger than KVM detection. Validate architecture, binary versions, Jailer availability, cgroup v2, tap/vsock/network setup and a disposable boot test in an isolated preflight environment. A positive KVM probe alone does not mean Firecracker is production-ready.

Docker probe must use the daemon endpoint configured for the trusted manager. Never mount the Docker socket into SuperAgent or a Worker just to make detection easier.

gVisor probe must verify that Docker reports the configured `runsc` runtime and start a disposable container with `--runtime=runsc`. Do not infer it from the `runsc` binary alone.

WASM probe must execute a known minimal WASI module under the configured runtime, and verify fuel, memory and timeout controls.

## Scheduler policy

| Deployment / capability | Docker/runc | Firecracker MicroVM | WASM | gVisor |
|---|---|---|---|---|
| Physical + Firecracker preflight passed | Trusted internal work | Local external/untrusted work | Constrained extensions | Optional |
| Physical + no usable KVM | Trusted internal work | Remote KVM pool or reject/defer | Constrained extensions | Explicitly approved alternative |
| VM + nested KVM preflight passed | Trusted internal work | Local MicroVM if full preflight passes | Constrained extensions | Optional |
| VM + no nested KVM | Trusted internal work | Remote KVM pool or reject/defer | Constrained extensions | Explicitly approved alternative |
| Container deployment | Only if trusted manager has a separately secured daemon | Usually remote; do not assume nested KVM | Constrained extensions | Only after runtime/platform preflight |

A task's trust policy is authoritative:
- `trusted_internal` may use hardened runc.
- `external_untrusted` requires Firecracker or an explicitly approved alternative policy. If Firecracker is required and unavailable locally, route to a remote KVM worker pool or fail closed.
- `constrained_extension` may use WASM with narrow imports and enforced fuel/memory/time limits.

Do not silently downgrade MicroVM-required tasks to runc or gVisor. If gVisor is allowed as an alternative, record that decision and its policy version in the audit trail.

## Capability record

Persist a time-stamped, versioned snapshot per execution node:

```json
{
  "node_id": "worker-node-01",
  "platform": "virtual_machine",
  "platform_evidence": ["systemd-detect-virt=kvm", "dmi=QEMU"],
  "capabilities": {
    "docker_oci": {"available": true, "version": "pinned"},
    "kvm": {"available": false, "reason": "KVM_GET_API_VERSION: ENOENT"},
    "firecracker": {"available": false, "reason": "kvm_unavailable"},
    "gvisor_runsc": {"available": true, "preflight": "passed"},
    "wasm_wasmtime": {"available": true, "preflight": "passed"},
    "cgroup_v2": {"available": true}
  },
  "probed_at": "RFC3339 timestamp",
  "probe_version": "1"
}
```

The example is illustrative; production records must use observed versions and errors, not these sample values.

## Lifecycle and scheduling

1. Probe at daemon startup and register the node.
2. Re-probe on daemon restart and after runtime/configuration changes.
3. Perform lightweight periodic health checks; run expensive Firecracker/gVisor preflight only at startup, on change, or on operator request.
4. Mark a capability unavailable immediately after repeated runtime failures; do not continue scheduling against stale positive state.
5. The Scheduler filters nodes by required boundary, capabilities, resource headroom, tenant policy and locality.
6. Remote MicroVM workers register through the same node capability contract.
7. Every task audit event records selected node, platform class, backend, runtime/image digest, capability snapshot version and policy decision.

## Deployment profiles

### Physical/KVM-capable worker

- Go Control Plane and trusted SuperAgent may run as services/containers.
- Docker/runc handles approved internal tasks.
- Firecracker handles arbitrary external code after full preflight.
- WASM handles constrained plugins.
- gVisor is optional.

### VM without nested virtualization

- Go Control Plane and trusted SuperAgent run in Docker.
- Docker/runc handles approved internal tasks.
- WASM handles constrained plugins.
- Firecracker-required tasks route to remote KVM-capable physical workers or are rejected/deferred.
- gVisor is an optional, explicitly approved weaker isolation profile after preflight.

### VM with nested virtualization

Treat it as VM platform class, but if KVM and full Firecracker preflight pass, local MicroVM scheduling is allowed. This prevents the incorrect assumption that all VMs lack MicroVM support.

## Acceptance tests

- Bare metal with KVM disabled is classified as bare metal but reports Firecracker unavailable.
- VM without nested virtualization reports VM and KVM unavailable.
- VM with nested virtualization and accessible KVM may pass Firecracker preflight.
- A container with no delegated KVM cannot claim Firecracker support merely because the underlying physical host has KVM.
- Missing Docker, runsc, WASM runtime or cgroup controllers is reported independently.
- A stale capability snapshot is invalidated after runtime/device loss.
- MicroVM-required tasks are remotely routed or fail closed; never downgraded silently.


## Implemented Host API and Control Plane integration

The Rust Host Sandbox Manager exposes an authenticated `GET /v1/capabilities` endpoint. It reports:
- platform class as observed by the Sandbox Manager process;
- KVM availability only after opening `/dev/kvm` and successfully calling `KVM_GET_API_VERSION`;
- Docker daemon/runtime information from `docker info`;
- presence/version of Firecracker, Jailer, runsc and Wasmtime/WasmEdge;
- cgroup v2 status.

The snapshot deliberately distinguishes `available` from `ready`. Finding a binary or registered runtime is not a successful security preflight. Firecracker remains not-ready until a disposable Guest boot test passes. gVisor remains not-ready until a disposable container actually starts with `runsc`. WASM remains not-ready until WASI capability restrictions and fuel/memory/time limits are verified.

The Go Control Plane can fetch the authoritative Host snapshot using:
- `LIGHT_AGENT_SANDBOX_MANAGER_URL`
- `LIGHT_AGENT_SANDBOX_MANAGER_TOKEN`

If this URL is unset, Go reports only its own process-visible capability scope. If Go runs in Docker, that scope is `container`, not the underlying physical host/VM. With a configured URL, the Control Plane refreshes the authenticated Host snapshot every 30 seconds. A failed fetch immediately reports `unavailable`, and snapshots expire after 90 seconds without a successful refresh. A later successful probe restores readiness without a restart. The policy resolver rejects all non-host snapshots for host backend scheduling. See [capability-refresh.md](capability-refresh.md) for lifecycle and validation details.

When the Control Plane runs in Docker, the Host Sandbox Manager must be reachable over a protected interface. If binding it beyond loopback, use a long random token (32+ characters) and restrict ingress with the Host firewall to the Control Plane network. Never mount the Docker socket into the Control Plane or Agent containers.

Example:

```bash
curl -H "Authorization: Bearer $LIGHT_AGENT_SANDBOX_MANAGER_TOKEN" \
  http://127.0.0.1:9090/v1/capabilities
```
