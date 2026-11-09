# Local Degraded Sandbox (no remote Worker Pool)

## Goal

Super Agent Bot Plantform must remain usable on a standalone physical host or VM even when no remote KVM-capable Worker Pool exists. This does not mean every task can run locally. The platform exposes only the isolation boundaries it can actually provide and refuses tasks whose mandatory boundary is unavailable.

This is a local execution policy, not a claim that Docker or gVisor is equivalent to a hardware MicroVM.

## Operating modes

### Strict mode

Use when the tenant or operator requires a specific security boundary.

- Trusted internal tasks may use hardened Docker/runc.
- Constrained WASM extensions may use the configured WASM runtime.
- Tasks requiring Firecracker must use local Firecracker after full KVM preflight.
- If Firecracker is unavailable and there is no remote pool, reject/defer the task.
- No automatic downgrade to gVisor or runc.

### Compatible mode (recommended standalone default)

Maximize local task availability without hiding the weaker boundary.

- Trusted internal tools and approved scripts: Docker/runc.
- Constrained plugins, parsers, transforms and compute functions: WASM.
- Arbitrary Linux commands or external code: Docker/gVisor only when the administrator explicitly enables gVisor for that risk tier and accepts its security/compatibility profile.
- If the task needs a full Linux environment and its policy requires a MicroVM, reject/defer it when local KVM is unavailable.
- Never silently use runc for external code.

### Availability mode

Use only where the operator deliberately prioritizes availability and has approved a per-task downgrade policy.

- The Policy Engine may select a pre-approved alternate boundary (for example, gVisor instead of MicroVM) only when the task's immutable policy explicitly lists that alternate.
- Require administrator approval for each downgrade or a narrowly scoped, versioned policy approval.
- Emit an audit event with requested boundary, actual boundary, reason, approving identity, policy version and node capability snapshot.
- Do not allow a model prompt, plugin manifest or task-supplied field to authorize its own downgrade.
- Never downgrade a MicroVM-mandatory or explicitly high-risk task to runc.
- WASM is not a generic fallback for arbitrary Linux tasks; the task must already be a compatible WASI module.

## Local backend matrix

| Task class | No-KVM local backend | Decision |
|---|---|---|
| Trusted internal scripts/services | Hardened Docker/runc | Allow under T1 policy |
| Reviewed lightweight plugins | WASM with narrow imports | Allow under T3 policy |
| External code that is explicitly approved for gVisor | Docker/gVisor runsc | Allow only with explicit alternate-boundary policy |
| External arbitrary Linux code requiring independent Guest kernel | None | Reject/defer; do not run locally |
| Task requiring Firecracker | None | Reject/defer; no remote pool means no compliant executor |
| WASM-compatible computation | Wasmtime/WasmEdge | Allow with fuel, memory and wall-clock limits |

gVisor is a userspace-kernel sandbox and can reduce direct exposure to the Host kernel, but it is not a hardware MicroVM and does not provide an independent Guest kernel. Its availability and compatibility depend on the selected platform and host kernel. Run a real preflight with the exact Docker runtime and workload profile.

## 4-vCPU / 8-GiB standalone VM profile

Start conservatively:
- Control Plane: 0.5 GiB memory and up to 1 vCPU.
- Cognitive OS SuperAgent: 1-1.5 GiB memory; no Docker socket or Host shell.
- Rust Sandbox Manager: 0.1-0.2 GiB memory, trusted Host service.
- Reserve at least 1 GiB for OS burst, page cache and daemon overhead.
- Start with one 1-vCPU/1-GiB Worker slot. Add a second only after load tests show safe RSS, CPU throttling and OOM headroom.
- Compile-heavy tasks should use one larger slot (for example 2 vCPU and 2-3 GiB) rather than multiple concurrent Workers.

These are starting limits, not measured capacity guarantees. Measure P95/P99 RSS, CPU throttling, task latency, OOM kills and daemon overhead on the target VM.

## Configuration example

The following is a policy contract. It must be wired to validated configuration structs and runtime preflight before it is treated as an active implementation setting.

```yaml
execution:
  mode: compatible # strict | compatible | availability

  local_backends:
    docker:
      enabled: true
      runtime: runc
    gvisor:
      enabled: true
      runtime: runsc
      allowed_for_tiers: [] # explicitly add approved risk tiers
    wasm:
      enabled: true
      runtime: wasmtime
      fuel_limit: 10000000
      memory_limit: 128MiB
      wall_timeout: 10s
    firecracker:
      enabled: auto # only after KVM + Firecracker preflight

  remote_workers:
    enabled: false

  fallback:
    require_explicit_policy: true
    require_admin_approval: true
    allow_runc_for_external_code: false
    allow_gvisor_for_external_code: false
    audit_every_downgrade: true
    fail_closed_when_no_compliant_backend: true
```

For a no-KVM VM with no remote pool, the expected result is:
- Docker/runc for trusted internal tasks.
- WASM for compatible constrained extensions.
- gVisor only for tasks explicitly approved for that alternate boundary.
- Firecracker-required work is rejected/deferred with a structured `no_compliant_executor` reason.

## API and audit semantics

When no compliant backend exists, the scheduler should return a stable policy error such as:

```json
{
  "code": "no_compliant_executor",
  "requested_boundary": "microvm",
  "local_capabilities": ["docker_oci", "wasm_wasmtime", "gvisor_runsc"],
  "remote_worker_pool_available": false,
  "action": "rejected"
}
```

The task must not be marked as executed, and no container should be created as a side effect.

For an approved downgrade, record:
- original required boundary and selected boundary;
- policy ID/version and downgrade rule;
- administrator/tenant approval reference;
- platform and capability snapshot IDs;
- selected runtime and immutable image digest;
- resource/network/filesystem capability grants.

## Security invariants

1. No Host shell or generic Host exec API is exposed to Agents.
2. Docker socket is accessible only to the trusted Host Sandbox Manager, never to SuperAgent or Worker containers.
3. Every Worker receives a dedicated task workspace; no arbitrary Host rootfs bind mounts.
4. All container workers run non-root, drop all capabilities, enable no-new-privileges, use read-only rootfs, seccomp plus AppArmor/SELinux where available, cgroup limits and default-deny networking.
5. gVisor selection is explicit and verified; no silent runsc-to-runc fallback.
6. WASM receives only declared imports/capabilities and has fuel, memory and wall-clock limits.
7. A MicroVM-required task never runs in runc, gVisor or WASM unless a separate, explicit policy has reclassified the task before admission.
8. If no compliant local backend and no remote executor exist, fail closed and explain the limitation.

## Acceptance tests

- No-KVM + no remote pool + MicroVM-required task returns `no_compliant_executor`; no container is created.
- Compatible mode runs trusted internal tasks in runc and WASI-compatible plugins in WASM.
- gVisor is used only when the task policy explicitly allows it and the runtime preflight passes.
- Removing runsc after startup makes gVisor unavailable; scheduler does not fall back to runc.
- Availability-mode downgrade requires a valid administrator approval/policy reference and creates an immutable audit event.
- Agent prompts cannot change `required_boundary` or grant fallback permissions.
- Docker daemon outage or capability loss causes tasks to fail/retry safely without running commands on Host.
