# 以持久 Connection Profile 替代进程内连接句柄

## Status

Superseded by [ADR 0002](0002-layered-connection-profiles.md). The historical decision below is kept for context; `data.ssh_conn` no longer exists.

原始设计让 managed resource 引用 ephemeral connection 的 UUID，但 Terraform/OpenTofu 不允许 ephemeral 值进入普通持久参数；WriteOnly 又无法用于 Read/Delete。源码与原型进一步确认 function、data source 和 package 全局 map 都不能跨 graph walk、provider 进程或 destroy 保留连接，稳定 hash 也无法反推出重连凭据。首版改为 `data.ssh_conn` 在真实 SSH 冒烟成功后返回完整敏感 `conn` 对象，消费者将该对象持久化并在每个生命周期操作中独立重连；删除 `ephemeral.ssh_connection`，不为 managed resource 使用连接 map。

## Consequences

Connection Profile 让 orphan destroy 和后续 CLI run 能仅凭 prior state 重连，同时保留 data 冒烟失败阻断并行资源连接的优点。代价是私钥会进入 data 及消费者的 plan/state，Sensitive 只影响显示；旧私钥失效时 Read 仍使用 prior state，需要按文档轮换或跳过 refresh。只传 opaque id 的模型需要外部 daemon，首版明确不采用。
