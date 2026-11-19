# v0.1.0

## 中文

- 新增内置 Agent 管理看板，支持创建 Agent、查看状态和最近任务、发送会话。
- 支持向所有现有 Agent 批量发送消息，以及创建、查看和删除持久化定时任务。
- 接入真实 light-agent C11 Worker，保留隔离工作区、原生记忆和近期会话上下文。
- 支持飞书接入、新闻源抓取、职位查询及地区追问。
- 更新中英文 README，补充看板访问、SSH 转发和运行配置说明。

看板地址为 `/dashboard/`，管理接口使用 `LIGHT_AGENT_API_TOKEN`。执行任务需要配置 Worker 和模型。Linux 远程服务默认通过 SSH 转发访问。

定时任务作用于触发时的全部 Agent。停机期间错过的多次执行合并补跑一次；删除规则不会取消已入队任务。当前任务执行并发受现有调度器限制，本版本不承诺百万 Agent 同时执行。

## English

- Add an embedded dashboard for Agent creation, status, recent tasks and messaging.
- Add fleet broadcasts and persistent interval schedules with creation, listing and deletion.
- Integrate real light-agent C11 workers with isolated workspaces, native memory and recent conversation context.
- Support Feishu, news feeds, job listings and location follow-ups.
- Update English and Chinese setup documentation, including dashboard access and SSH forwarding.

Open `/dashboard/` and authenticate with `LIGHT_AGENT_API_TOKEN`. Task execution requires a configured worker and model. Remote Linux services are accessed through SSH forwarding by default.

Schedules target all Agents present when they run. Missed occurrences coalesce into one catch-up batch; deleting a schedule does not cancel queued tasks. Execution concurrency remains bounded by the existing dispatcher; this release makes no million-concurrent-Agent claim.
