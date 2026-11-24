# 分层 Provider 配置与 JSON Connection Override

## Status

Accepted

## Context

`data.ssh_conn` 将完整 Connection Profile（包括私钥）持久化到每个消费者的 state，支持自包含 destroy 和动态多主机，但扩大凭据暴露面，并使 refresh/destroy 依赖 prior state 中的旧凭据。Provider-only 配置能减少凭据持久化并简化轮换，却不适合所有动态多主机场景，也不能在 provider 配置被删除后独立 destroy。Provider function 不适合网络预检，且无法静态声明 Sensitive 或直接读取已 Configure 的 provider。

## Decision

- Provider 支持全部 Connection Profile 字段，所有字段可选；`Configure` 不联网。
- 删除 `data.ssh_conn`，不再提供统一 SSH 预检。
- `ssh_resource`、`data.ssh_data`、`data.ssh_sensitive_data`、`ephemeral.ssh_data` 的 `conn` 改为可选 Sensitive JSON 字符串。省略、JSON `null` 和 `{}` 表示无覆盖。
- 合并顺序为内置默认值、Provider Connection Defaults、Connection Override。JSON absent 继承，explicit null 清除并回到内置默认值。`envs` 按 key 合并；`jump` 整体替换。Host-key 覆盖自动选择互斥模式。
- JSON 必须是 object 或 null；未知字段、重复 key、数组和标量均拒绝。诊断不回显原始 JSON 或敏感字段。
- 普通消费者只持久化原始 override 和非敏感 Connection Identity。每次生命周期操作使用当前 provider 配置与 prior override；provider 私钥不进入 state，JSON 中显式私钥仍会持久化。
- host/port/user 或 jump 身份变化替换资源；其他连接配置变化不替换。Destroy 需要当前 provider 配置，提前删除时明确失败。
- 提供 `provider::ssh::connection(host, user, port)` 纯函数，返回仅含这三个字段的 JSON；不联网、不处理私钥、不读取 provider。
- 旧 object `conn` 不做自动迁移；配置需改为 `jsonencode()` 或该函数。

## Consequences

Provider 默认凭据不再复制到资源 state，轮换使用当前凭据，静态少量主机配置显著简化。动态多主机仍可通过 JSON override 表达。代价是普通 override 中的私钥仍需持久化，destroy 不再完全自包含，且删除统一预检后配置或认证失败可能在多个资源中并行出现。已明确选择：identity 变化触发替换时 Read/Delete 仍使用当前端点，而不是 prior identity；调用方必须先确认当前端点上的同名路径可安全操作，否则先恢复旧端点并 destroy。所有 surface 共享严格合并和校验逻辑，避免语义分叉。
