# Independent Agent capacity testing

`scripts/benchmark-agents.py` runs real Cognitive OS runtime processes in separate
hardened Docker/runc containers, with one private state root and workspace per
instance. Only the LLM is mocked using the runtime's built-in deterministic
provider. This is not a count of idle `sleep` containers or database identities.

This benchmark talks directly to the pinned Cognitive OS HTTP API. It does not
claim that Super Agent Bot Plantform's still-pending production reasoning driver or Feishu
orchestration integration is complete.

## Reproduce on a Linux Docker host

Compile `COGNITIVE_OS_REF` revision
`6bdfc849bba6d196739372bf18d005076062f068`, then build the image with the directory
containing the resulting binary as the build context:

```bash
make -C cognitive-os-src/backend build/cognitive-os-agent \
  CFLAGS='-std=c11 -O2 -DNDEBUG -Iinclude -Ithird_party/cJSON -Ithird_party/wasm3'
docker build -f deploy/Dockerfile.agent-capacity \
  -t light-agent/cognitive-capacity:6bdfc849 cognitive-os-src/backend/build
image="$(docker image inspect light-agent/cognitive-capacity:6bdfc849 --format '{{.Id}}')"
sudo python3 scripts/benchmark-agents.py \
  --image "$image" --work-dir /tmp/light-agent-capacity-unique \
  --levels 1,4,8,16,32,64,96,128,192,256 \
  --duration 15 --soak 60 --period 0.5
```

Use `CAPACITY_BASE_IMAGE` to select a trusted preloaded base when building offline.
The image must carry the pinned revision label; tags are resolved to an immutable
image ID before testing. The work directory must not already exist.

The default `ping` workload runs the actual reasoning/task pipeline and verifies
that each result contains its unique input marker. `--workload file` instead
requests a real `file_write` tool call for every task and checks the resulting
file's content after completion. A baseline also verifies bearer authentication,
real file-tool execution and workspace persistence across container restart.

## Profile and measurement

- Non-root UID/GID 65532, read-only rootfs, no capabilities, no-new-privileges,
  private IPC and `network=none`.
- Default limits: 128MiB memory/swap ceiling, 0.5 CPU ceiling, 64 PIDs and 1024
  file descriptors. CPU and memory ceilings are not resource reservations.
- Eight scheduler workers per runtime, matching the pinned runtime default.
- Default load: two submitted tasks per second per Agent, with at most one
  in-flight task per Agent. Instances are retained across increasing stages,
  preserving accumulated state and task history.
- The operator load generator opens sockets inside each Agent's network namespace
  and restores its own namespace immediately. No ports are published and no
  Docker socket or host credentials are mounted into the Agent.
- Agent memory and CPU come from cgroup v2 counters. VM `MemAvailable` and CPU
  utilization are measured separately, including Docker/shim and benchmark
  overhead. Aggregate cgroup usage alone understates total VM capacity cost.
- The load generator's CPU consumption is reported separately so client-side
  saturation can be distinguished from Agent saturation.

The default pass criterion is zero errors/OOM kills, at least 95% of the offered
task count completed, and P95 task latency no greater than one second. Expansion
stops before available host memory falls below the 600MiB reserve plus one
container's memory ceiling. Runtime sampling aborts load if available memory drops
below the reserve or a Worker has an OOM kill. The harness cleans only containers
carrying its unique run label, including containers from ambiguous Create replies.

Results are written to `results.json` and `results.csv`. `largest_stable_agents`
describes the staged short test; inspect the `soak` row and `soak_passed` separately
for the longer check. Hitting the reserve or configured test ceiling does not
establish the machine's absolute crash/OOM limit.

These results do not measure remote LLM rate limits, token streaming/large-context
memory, local model inference, compilation-heavy Worker tasks, or Firecracker
MicroVM density. A production sizing decision needs the intended workload and a
longer soak than this short capacity probe.
