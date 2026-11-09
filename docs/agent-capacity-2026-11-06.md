# Agent capacity results — 2026-11-06

## Answer for this VM

On the tested **4-vCPU / 3655MiB RAM** VM:

- **128 independent Agent runtimes** passed a 60-second light reasoning workload
  using the built-in Mock LLM: 15,360 tasks, zero errors and zero OOM kills.
- **64 independent Agent runtimes** passed a 120-second real file-tool workload:
  15,360 tasks, zero errors and zero OOM kills. Use this as the initial tested
  operating point for this particular workload, rather than using the short-test
  peak as the deployment target.
- **154 instances were actually started and became ready** during the light-load
  expansion. Expansion then stopped at the configured memory headroom guard.
  This is an observed startup count, not a sustained-load pass or an absolute
  crash/OOM limit.
- 96 file-tool Agents passed a 15-second stage but **failed the subsequent
  60-second check**. 128 file-tool Agents already failed the short stage. A
  successful startup or a short burst is not enough to establish capacity.

The model is mocked, while the C runtime, scheduler, authenticated HTTP API,
state, workspace, reasoning loop and file tool execution are real. These figures
are not real-model inference capacity, nor a measurement of compilation-heavy,
browser or local-LLM workloads.

## Measured results

| Workload | Agents | Load duration | Completed tasks | Errors | Tasks/s | P95 latency | Result |
|---|---:|---:|---:|---:|---:|---:|---|
| Mock reasoning | 128 | 60.27s | 15,360 | 0 | 254.86 | 180.71ms | Pass |
| Real file tool + Mock LLM | 64 | 120.26s | 15,360 | 0 | 127.72 | 274.76ms | Pass |
| Real file tool + Mock LLM | 96 | 60.51s | 9,669 | 3 | 159.80 | 963.43ms | Fail |
| Real file tool + Mock LLM | 128 | 15.56s | 2,533 | 1 | 162.78 | 1069.99ms | Fail |

The offered rate was two tasks/second per Agent, with at most one in-flight task
per Agent. The pass criteria were zero errors/OOM kills, at least 95% of the
offered task count completed, and P95 at most one second. The 96-Agent long run
failed despite P95 being just below one second: it had timeouts and completed
only about 84% of the offered task count. No tested case had an OOM kill.

| Workload / Agents | VM CPU busy | Agent CPU cores | Load-generator CPU cores | Peak Agent cgroup memory | Minimum VM MemAvailable |
|---|---:|---:|---:|---:|---:|
| Light / 128 | 39.49% | 1.018 | 0.338 | 844.79MiB | 737.04MiB |
| File / 64 | 62.26% | 1.853 | 0.344 | 1327.66MiB | 932.71MiB |
| File / 96 | 83.59% | 2.273 | 0.582 | 1202.05MiB | 686.02MiB |
| File / 128 | 94.19% | 2.367 | 0.804 | 968.84MiB | 631.64MiB |

Memory includes anonymous memory, page cache and kernel charges in the container
cgroups. VM headroom also reflects Docker, container shims, the load generator
and existing system services. Do not estimate VM density from the C executable's
RSS or its container limit alone. The file/64 row is a longer run with more state
and task history, so its peak cgroup memory exceeds the shorter file/96 run.

The load generator runs on this VM and its CPU cost is included above. This
makes the results conservative relative to external clients, but the difference
has not been quantified. These tests establish demonstrated operating points,
not a precise universal maximum between 64 and 96 file-tool Agents.

## Runtime and isolation

- OpenCloudOS 9.4, Linux 6.6.117, x86_64; no swap and no `/dev/kvm`.
- Docker 29.7.2, runc and cgroup v2/systemd.
- Cognitive OS revision `6bdfc849bba6d196739372bf18d005076062f068`, matching
  `COGNITIVE_OS_REF`; compiled from the pinned source with GCC, C11, `-O2 -DNDEBUG`.
- Eight scheduler workers per runtime; each instance had a private state root,
  workspace and network namespace, and its own authenticated runtime HTTP API.
- Limits: non-root 65532:65532, read-only rootfs, no capabilities,
  no-new-privileges, `network=none`, private IPC, 128MiB memory/swap ceiling,
  0.5 CPU ceiling, 64 PIDs and bounded local logs. Limits are not reservations.
- Every file-workload task invoked the real tool pipeline and its output file
  was checked for that task's unique marker. Authentication rejection and file
  persistence across a real container restart were also verified.
- Expansion required more than 600MiB reserve plus one container's memory ceiling
  available on the host; runtime sampling stopped load below the reserve.

The successful measurement image was
`sha256:9f88606fd81084cb1dd72fb9c91db0aca8dd040b83245b2da5df9a689837371c`,
containing the native-built pinned C runtime over a preloaded test base. The
tracked capacity Dockerfile provides a reproducible equivalent build recipe.

## Reproduce and inspect

See [agent-capacity-testing.md](agent-capacity-testing.md) and run
`scripts/benchmark-agents.py` with `--workload ping` or `--workload file`.

Remote artifacts are under `/root/light-agent-validation/capacity-20261106/`:

- `measurement/results.json` and `results.csv`: light workload, expansion and
  128-Agent 60-second check. The 154-start count is also confirmed by the 154
  per-Agent directories and the successful run exit; every constructor performed
  authenticated readiness checks and completed a real warmup task.
- `file-workload/results.json`: file/128 short failure and file/96 long failure.
- `file-confirm64/results.json`: 64-Agent 120-second file-tool confirmation.
- `logs/`: source build and benchmark logs.

Local raw results are retained in `.validation/capacity-20261106/` (Git-ignored).
All benchmark containers were removed after each run; each result records an
empty `cleanup_remaining_containers` value. Existing services were not modified.

## Practical limit of this conclusion

Start with at most 64 simultaneously active instances for the tested file-tool
profile, then validate the intended task mix and a longer soak before increasing
it. The 120-second check does not prove hour/day-scale memory stability. Real LLM
quotas, long contexts, streaming, builds, browsers and additional services change
the resource budget. Firecracker MicroVM density was not measured.

This benchmark accesses the real Cognitive OS containers directly. Super Agent Bot Plantform's
production Cognitive OS lifecycle/message driver and Feishu-to-reasoning path
remain separate unfinished integrations; this report does not claim otherwise.
