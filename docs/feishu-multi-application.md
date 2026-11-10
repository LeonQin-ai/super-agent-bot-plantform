# Multi-application Feishu on one Platform

Set `FEISHU_APPLICATIONS_FILE` to a private JSON file. Each entry maps a webhook
route to a tenant and a Feishu app. The existing `/webhooks/feishu` route remains
available for the original application and keeps its existing Agent bindings.
Keep `LIGHT_AGENT_API_TOKEN` restricted to Platform operators because it grants
access to the administrative API across tenants.

```json
[
  {
    "route": "team-a",
    "tenant": "tenant-a",
    "app_id": "<Feishu application ID>",
    "app_secret": "<Feishu application secret>",
    "verification_token": "<unique relay token>",
    "encrypt_key": "<unique relay signing key>",
    "allowed_open_ids": ["<authorized Open ID>"],
    "pending_limit": 16
  }
]
```

Run one bridge process per registered app with its existing environment settings
and `FEISHU_APP_ROUTE=team-a`. The bridge signs requests to
`/webhooks/feishu/team-a`. Configure the same Open ID allowlist in both the
bridge and the application registry. Each app needs its own route, app ID and
verification credentials. Applications in the same tenant must set the same
pending limit.

The control plane resolves an Agent using tenant, app and Open ID, and scopes
event deduplication to the same tenant and app. It persists the original
application's reply identity with the task. The tenant pending limit counts
queued and running tasks, including API tasks assigned to the tenant. The
bridge receives HTTP 429 for new events beyond that limit, while an already
recorded retry is acknowledged without creating another task.

## Deployment and capacity milestones

The current deployment has one SQLite owner and one task dispatcher. An Agent
has a persistent identity, workspace, and native light-agent memory directory.
Each Cognitive OS task starts a disposable Worker and can recall recent
completed exchanges from SQLite. The remaining issue work includes full
session checkpoint/restore, separate Runtime and
Worker quotas, durable reply retries, remote node leases and cross-Platform
failover. Validate those paths with separate Platform hosts and at least one
virtual and one physical execution node.

For capacity planning, 6 billion registered identities across 4,096 logical
shards would average about 1.46 million identities per shard. At an assumed
1 KiB per identity and binding, that is about 6.14 TB raw or 18.43 TB with
three copies, before indexing, workspace data and event history. These are
planning assumptions. Measure actual storage with a representative dataset.

At an illustrative 1% daily active rate and two events per active user, mean
ingress is about 1,389 events/s. A 10× peak is about 13,889 events/s, and a
20-second average active task would imply roughly 278,000 simultaneous tasks
at that peak. Measure identity routing and event throughput separately from
model/Worker concurrency. Start with a shard and replayed events, then add
physical and virtual execution nodes, test node loss and shard reassignment,
and record queue delay, duplicate execution and tenant isolation at every step.
