# SSH Provider 设计

状态：已确认，作为实现与验收契约；`CONTEXT.md` 只维护领域术语。连接层由 [ADR 0002](adr/0002-layered-connection-profiles.md) 取代 [ADR 0001](adr/0001-connection-profiles.md)。

## 1. 范围与生态

- Provider address：`geektr-cloud/ssh`。
- 支持 Terraform >= 1.11 与 OpenTofu >= 1.11；分别测试最低支持版本的最新 patch 和当前稳定版。
- 使用 Plugin Framework v1.19.0、protocol 6、Go 1.26。
- 远端仅支持带 GNU coreutils `install`、`timeout`、`mktemp` 的 Linux；shell 默认 `sh`。
- surface：`provider "ssh"` Connection Defaults、`ssh_resource`、`data.ssh_data`、`data.ssh_sensitive_data`、`ephemeral.ssh_data`、provider function `provider::ssh::connection`。
- 不实现 `data.ssh_conn`、统一连接预检、跨进程连接池或外部 daemon。
- 项目使用 MIT license。

## 2. 连接模型

### 2.1 分层 Connection Profile

Provider block 持有 Provider Connection Defaults；每个消费者可提供 Connection Override。合并顺序固定为：

```
内置默认值 ← Provider Connection Defaults ← Connection Override
```

- Provider 字段全部可选，可完整或部分配置。`Configure` 不联网，只保存 defaults 并做结构校验（type、port、duration、env 名称、host-key 模式互斥）。
- 消费者 `conn` 是 Optional + Sensitive JSON string。省略、JSON `null`、`{}` 都表示无覆盖。
- JSON 字段 absent 继承 provider；explicit `null` 清除 provider 值并回到内置默认；显式值覆盖。
- `envs` 按 key 合并，同名覆盖；`envs = null` 清空；`envs = {}` 不新增也不删除。`jump` 整体替换，`jump = null` 清除，不做内部合并。
- Host-key：override 显式 `known_hosts` 或 `host_key` 时选择该模式并清除 provider 的其他模式；`insecure_ignore_host_key = true` 同理；`insecure_ignore_host_key = false` 不清除 provider 已固定的模式；explicit `null` 按字段清除。最终三种模式都未选择时使用 `~/.ssh/known_hosts`。
- 内置默认：`type = "linux"`、`port = "22"`、`timeout = "5m"`、`retry_delay = "5s"`、`envs = {}`、`insecure_ignore_host_key = false`。最终缺少 host、user 或 private_key 时，在拨号前报错。
- 密码、key path、agent、keyboard-interactive 仍不支持。

一个消费者示例：

```hcl
provider "ssh" {
  host        = "example.internal"
  port        = "22"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")
  known_hosts = "~/.ssh/known_hosts"
  envs        = { LANG = "C" }
  timeout     = "5m"
  retry_delay = "5s"
}

resource "ssh_resource" "config" {
  conn = jsonencode({ user = "deploy" })
  # file / directory ...
}
```

### 2.2 JSON 严格性

- 顶层只能是 object 或 `null`；数组和标量拒绝。
- 所有层级拒绝未知字段、重复 key 和错误类型，包括 `jump` 内部与 `envs` key。
- `jump` 内不能再嵌套 `jump`、`envs`、`timeout`、`retry_delay` 或 `type`。
- 诊断只包含字段路径和错误类型，绝不回显原始 JSON、private_key 或敏感值。
- unknown conn 保留到实际执行；执行前必须是 known。

### 2.3 字段

Connection Profile 包含：

- `type = "linux"`。
- `host`：主机名或地址。
- `port = "22"`：字符串，校验 1–65535。
- `user`。
- `private_key`：内联私钥内容。
- `known_hosts`：known_hosts 文件路径；支持 `~` 展开，文件必须在拨号时存在。
- `host_key`：内联 authorized-key 格式的固定主机公钥。
- `insecure_ignore_host_key = false`：显式跳过校验。
- `envs = {}`：command/run 的默认环境变量。
- `timeout = "5m"`：包括 jump 在内的整个拨号与重试预算。
- `retry_delay = "5s"`：临时 TCP/SSH 网络错误的重试间隔。
- `jump`：可选单层 Jump Profile；含 host/port/user/private_key/host-key 字段，不能嵌套 jump，也不使用 envs。

目标和 jump 分别校验 host key。jump 仅表示 SSH 跳板，不表示 SOCKS5/HTTP proxy。

### 2.4 持久化与连接生命周期

| 值 | 显示 | plan/state |
| --- | --- | --- |
| Provider `private_key` | Sensitive | 不属于任何资源身份，不复制到消费者 state |
| 消费者 `conn` JSON | Sensitive | 普通 resource/data 持久化原始 override |
| JSON 内显式 `private_key` | Sensitive | 随 `conn` 持久化 |
| `ssh_resource.connection_identity` | 普通 computed | 持久化 |
| `ephemeral.ssh_data` 的 conn 与结果 | Ephemeral | 不持久化 |
| `data.ssh_data.values` | 普通 | 持久化 |
| `data.ssh_sensitive_data.values` | Sensitive | 持久化 |
| `ephemeral.ssh_data.values` | Sensitive | 不持久化 |
| 普通 file `content` | 按 schema | 持久化 |
| file `content_wo` | WriteOnly | 原文不持久化 |
| WriteOnly 内容 SHA-256 | 普通 computed | 持久化，不是加密 |

普通消费者每次 Create/Read/Update/Delete 都用“当前 provider defaults + prior/当前 JSON override”重新解析并独立拨号。Read 不使用 provider 合并结果覆盖 state 中的 conn。Destroy 需要当前 provider 配置；provider 被提前删除或最终配置不完整时明确失败。

### 2.5 Connection Identity 与替换

`connection_identity` 是目标与 jump 的 host/port/user 规范 JSON 编码，不含私钥、host-key 配置、envs 或超时。

- Create/Update 写入当前 identity。
- Read 保留 prior identity，使后续 plan 能发现 provider defaults 变化。
- ModifyPlan 用当前 provider + config `conn` 解析 identity，与 prior identity 比较；变化设置 `RequiresReplace`。
- identity 任一必需字段 unknown 时保守跳过比较，绝不把 unknown 当空字符串。
- 私钥、host-key 配置、envs、timeout、retry_delay 变化不替换资源。
- 已确认：identity 变化触发替换时，Read/Delete 仍使用当前 provider + override 指向的端点，不回到 prior identity。替换前必须确认当前端点上的同名路径可安全读取和删除；否则先恢复旧端点并 destroy。

### 2.6 Provider function

`provider::ssh::connection(host, user, port)`：

- 三个 `StringParameter`，返回 String。
- JSON 仅含 `{host,user,port}`。
- 纯确定性、不联网、不读取 provider、不接收私钥。

## 3. `ssh_resource`

### 3.1 结构

```hcl
resource "ssh_resource" "myres" {
  conn  = jsonencode({ user = "deploy" })
  umask = "022"

  directory {
    path        = "/tmp/example"
    mode        = "0750"
    auto_import = false
  }

  file {
    path    = "/tmp/example/hello.sh"
    content = "echo Hello world\n"
    mode    = "0700"
  }

  file {
    path       = "/tmp/example/secret"
    content_wo = var.stable_ephemeral_secret
  }
}
```

一个资源可管理多个独立 file/directory 子项，path 跨两类块唯一。允许同一资源内父子路径；不同资源之间的重叠所有权不支持。

### 3.2 元数据

- 顶层 `umask = "022"`；路径块可覆盖。
- file 默认 mode 为 `0666 & ^umask`；directory 和自动父目录默认 `0777 & ^umask`。
- 显式 mode 优先，使用八进制字符串。
- 顶层 owner/uid/group/gid 提供默认值。路径块显式设置 owner 或 uid 时整体覆盖顶层 owner/uid；group/gid 同理。
- owner/group 未设置时使用执行连接的有效用户/组；sudo 后通常为 root。
- 路径块支持 `auto_import = false`。

### 3.3 内容与 WriteOnly

file 的 `content` 和 `content_wo` 必须二选一，空字符串有效。普通 content 精确检测内容漂移；content_wo 原文只从 Config 读取，state 只保存 `content_sha256`。

resource-level `ModifyPlan` 处理嵌套 WriteOnly：

- 从 `req.Config` 计算期望 SHA-256，不把 content_wo 写回 plan。
- Read 从远端内容重算 `content_sha256`，使远端漂移产生 diff。
- unknown 内容产生 unknown hash；destroy 时 Plan/Config 为 null，跳过。
- content_wo 必须在 plan/apply 间稳定。

### 3.4 所有权与 CRUD

- path 必须是清理后的绝对路径，不能为 `/`；最终目标不能是 symlink。普通父路径中的 symlink 允许。
- 未管理目标已存在时默认报错；`auto_import = true` 接管精确路径并收敛，不递归。
- 自动创建的祖先目录不属于资源，destroy 后保留。
- Create/Update 的 upsert 按路径长度从短到长；Update 先删除已移除或类型改变的子项。
- file 使用同目录临时文件，0600 创建，最终 mode 与属主在原子 rename 前设置。
- 新建目录 chmod/chown 失败时 best-effort 删除。
- directory 只收敛目录本身。
- Read 对缺失子项从 state 移除；忽略未管理子项。
- Delete 依据 prior state，按路径长度从长到短；文件只删精确路径，目录用 `rmdir`，非空目录失败。
- 普通操作权限不足时使用 `sudo -n` 重试同一操作。

不支持额外 `ImportState`；导入机制是路径块级 `auto_import`。

## 4. `data.ssh_data`、`data.ssh_sensitive_data` 与 `ephemeral.ssh_data`

三种 surface 使用相同的 conn/sudo/timeout/file/command/run 配置和执行语义。`conn` 均为 Optional Sensitive JSON string，可省略以完全继承 provider defaults。

```hcl
data "ssh_data" "mydata" {
  conn = jsonencode({ user = "auditor" })
  sudo = false

  file {
    key  = "key1"
    path = "/etc/someconfig"
  }

  command {
    key     = "key2"
    index   = 2
    command = ["printf", "%s", "hello"]
  }

  run {
    key    = "key3"
    script = "echo 'hello'\n"
    shell  = "sh"
    envs   = { KEY = "value" }
  }
}
```

公共字段：`key` 必填且全局唯一；`index = 0` 且非负；`sudo` 为三态覆盖；`timeout = "5m"`。三种 surface 都没有块级 `sensitive` 参数，也没有 `sensitive_values`。

- file：`path` 必填。
- command：非空 argv，provider 安全 shell-quote。
- run：`script` 必填，`shell = "sh"`，`envs` 覆盖合并后 envs。脚本通过 stdin 交给 `<shell> -s`。
- command 使用合并后的 envs；run 在此基础上叠加块级 envs；file 不使用 envs。
- 顶层 sudo 默认 false；操作未设置时继承。
- 所有配置错误必须在任何远端操作前一次性报告。按 index 升序执行。首错使整个操作失败，不返回部分 map。
- file 返回完整内容，command/run 成功时返回完整 stdout；保留结尾换行，拒绝非 UTF-8。
- 所有结果统一写入 `values`；空结果返回非 null 空 map。
- 每个块都有 `trim`，Optional，默认 true。
- `data.ssh_sensitive_data` 与 `ephemeral.ssh_data` 抑制远端 stderr 和错误详情；`data.ssh_data` 包含有界 stderr。
- 单项输出默认最多 512 KiB，只能通过 `SSH_PROVIDER_MAX_OUTPUT_SIZE` 修改。

## 5. 实现边界

- Provider `Configure` 不做网络 I/O，只保存分层 defaults。
- 不使用 package 级可变连接 map；每个操作显式创建并关闭 client。
- 严格 JSON 解析、合并、身份计算和结构校验集中在单一共享 resolver；四种消费者不得各自实现。
- SSH 细节封装在内部 client；remote file/directory 语义与 Terraform schema 分离。
- 所有操作接收 context；限制响应和输出大小；诊断不得包含私钥、secret 内容或敏感 stderr。
- null 与 empty 严格区分；ValidateConfig 和 ModifyPlan 跳过 unknown，写入前再次拒绝非法 unknown/null。

## 6. 验收

1. `gofmt`、`go vet`、unit tests、`go test -race`、build、`go mod verify`、lint。
2. Schema/unit：provider 全量/部分配置、JSON absent/null/空值/错误类型/未知字段/重复 key（含 jump）、env/jump/host-key 合并、identity 与替换、WriteOnly、path、mode/umask、index/key、输出上限。
3. Hermetic SSH/SFTP：host key、认证失败不重试、临时网络重试、jump、命令超时、SFTP 解阻塞、响应上限、secret 日志扫描。
4. GNU Linux 真实 CLI acceptance：provider defaults、JSON override、function、CRUD、auto_import、嵌套路径、漂移修复、第二次 apply 无变化、destroy 只删除 owned paths、非空目录保护、WriteOnly 稳定值、provider 私钥不进入 state、JSON 私钥进入 state、credential 轮换与 identity 替换。
5. Terraform 与 OpenTofu 分别跑最低版本最新 patch 和当前稳定版；文档只声称实际通过的矩阵。
6. dev_overrides 与 filesystem mirror 分别验证；GoReleaser 构建、checksum 和签名在 release 时验证。
