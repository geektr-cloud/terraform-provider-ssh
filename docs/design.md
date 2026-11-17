# SSH Provider 首版设计

状态：已确认，作为实现与验收契约；`CONTEXT.md` 只维护领域术语。

## 1. 范围与生态

- Provider address：`geektr-cloud/ssh`。
- 首版支持 Terraform >= 1.11 与 OpenTofu >= 1.11；分别测试最低支持版本的最新 patch 和当前稳定版。
- 使用 Plugin Framework v1.19.0、protocol 6、Go 1.26。
- 远端仅支持带 GNU coreutils `install`、`timeout`、`mktemp` 的 Linux；shell 默认 `sh`。
- 首版 surface：`data.ssh_conn`、`ssh_resource`、`data.ssh_data`、`data.ssh_sensitive_data`、`ephemeral.ssh_data`。
- 不实现原方案的 `ephemeral.ssh_connection`、provider function、跨进程连接池或外部 daemon。
- 项目使用 MIT license。

## 2. 连接模型

### 2.1 持久 Connection Profile

```hcl
data "ssh_conn" "myserver" {
  type       = "linux" # 唯一支持值，默认 linux
  host       = var.ssh_host
  port       = "22"
  user       = var.ssh_user
  private_key = file(var.ssh_private_key)

  known_hosts = "~/.ssh/known_hosts"
  envs        = { LANG = "C" }
  timeout     = "5m"
  retry_delay = "5s"
}

resource "ssh_resource" "myres" {
  conn = data.ssh_conn.myserver.conn
  # file / directory ...
}
```

`data.ssh_conn` 必须完成真实 SSH 握手和认证，随后关闭探测连接，才返回 `conn`。这使依赖它的资源不会在配置或认证明显错误时并行产生大量失败连接。

`conn` 是完整、敏感、可持久化的 Connection Profile。每个 resource/data 在 Create/Read/Update/Delete 或读取时自行建立新连接，结束后关闭；不保存 live handle，不使用 UUID map。Delete 只依赖 prior state 中的 `conn`，因此删除配置块时仍能重连。

### 2.2 字段

Connection Profile 包含：

- `type = "linux"`。
- `host`：必填主机名或地址。
- `port = "22"`：字符串，校验 1–65535。
- `user`：必填。
- `private_key`：必填私钥内容，首版不支持 key path、agent、password、keyboard-interactive。
- `known_hosts`：known_hosts 文件路径；支持 `~` 展开，文件必须存在。
- `host_key`：内联 authorized-key 格式的固定主机公钥。
- `insecure_ignore_host_key = false`：显式跳过校验。
- `envs = {}`：command/run 的默认环境变量。
- `timeout = "5m"`：包括 jump 在内的整个拨号与重试预算。
- `retry_delay = "5s"`：临时 TCP/SSH 网络错误的重试间隔。
- `jump`：可选单层 Jump Profile；Jump Profile 含目标连接所需的 host/port/user/private_key/host-key 字段，但不能再含 jump，也不使用 envs。

显式 `known_hosts`、`host_key`、`insecure_ignore_host_key = true` 三种模式互斥；三者都未设置时默认读取 `~/.ssh/known_hosts`。不自动创建文件，不做 TOFU。认证、host key 和配置错误不重试；远端文件和命令操作不自动重试。

目标和 jump 分别校验 host key。jump 仅表示 SSH 跳板，不表示 SOCKS5/HTTP proxy。

### 2.3 连接变更

`ssh_resource` 的 host、port、user 或 jump 目标变化会替换整个资源。private_key、host-key 配置、envs、timeout、retry_delay 变化只更新 state，不重建远端路径。

Read 只能使用 prior state。若旧私钥已失效，需要先使 state 中的凭据更新，或使用 `-refresh=false` 完成轮换；文档必须说明这一限制。

## 3. 安全与持久化

| 值 | 显示 | plan/state |
| --- | --- | --- |
| `data.ssh_conn.private_key` 与输出 `conn` | Sensitive | 持久化，包含私钥 |
| `ssh_resource.conn` / `data.ssh_data.conn` / `data.ssh_sensitive_data.conn` | Sensitive | 持久化，包含私钥 |
| `ephemeral.ssh_data.conn` 及结果 | 按 ephemeral 语义 | 不持久化 |
| `data.ssh_data.values` | 普通 | 持久化 |
| `data.ssh_sensitive_data.values` | Sensitive | 持久化 |
| `ephemeral.ssh_data.values` | Sensitive | 不持久化 |
| 普通 file `content` | 按 schema | 持久化 |
| file `content_wo` | WriteOnly | 原文不持久化 |
| WriteOnly 内容 SHA-256 | 普通 computed | 持久化，不是加密 |

低熵 WriteOnly 内容的 SHA-256 可能被离线猜测。私钥、`data.ssh_data.values` 和 `data.ssh_sensitive_data.values` 进入 state 是当前明确接受的安全边界；文档示例附近必须有警告，不能把 Sensitive 描述成加密。

## 4. `ssh_resource`

### 4.1 结构

```hcl
resource "ssh_resource" "myres" {
  conn  = data.ssh_conn.myserver.conn
  umask = "022"

  # 顶层 owner/uid/group/gid 是路径块默认值。
  # owner 与 uid 互斥；group 与 gid 互斥。

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
    # content_sha256 为 computed。
  }
}
```

一个资源可管理多个独立 file/directory 子项，path 跨两类块唯一。允许同一资源内父子路径；不同资源之间的重叠所有权不支持。

### 4.2 元数据

- 顶层 `umask = "022"`；路径块可覆盖。
- file 默认 mode 为 `0666 & ^umask`，通常 0644；directory 和自动父目录默认 `0777 & ^umask`，通常 0755。
- 显式 mode 优先，使用八进制字符串。
- 顶层 owner/uid/group/gid 提供默认值。路径块显式设置 owner 或 uid 时整体覆盖顶层 owner/uid；group/gid 同理。显式性以配置为准，不从 plan 中的 computed unknown 推断。
- owner/group 未设置时使用执行连接的有效用户/组；sudo 后通常为 root。
- 路径块支持 `auto_import = false`。

### 4.3 内容与 WriteOnly

file 的 `content` 和 `content_wo` 必须二选一，空字符串有效。普通 content 精确检测内容漂移；content_wo 原文只从 Config 读取，state 只保存 `content_sha256`。

`file` 保留用户要求的 block 语法；在 resource-level `ModifyPlan` 中处理嵌套 WriteOnly：

- 从 `req.Config` 计算期望 SHA-256，不能把 content_wo 写回 plan。
- Read 从远端内容重算 `content_sha256`，使远端漂移与期望 hash 产生 diff。
- unknown 内容产生 unknown hash；destroy 时 Plan/Config 为 null，必须跳过。
- content_wo 必须在 plan/apply 间稳定。字面量、普通变量和稳定 ephemeral variable 可用；每 phase 变化的 ephemeral resource 不受支持，不增加 version/双 hash 状态机。

### 4.4 所有权与 CRUD

- path 必须是清理后的绝对路径，不能为 `/`；最终目标不能是 symlink。允许普通父路径中的 symlink。
- 未管理目标已存在时，默认报错；`auto_import = true` 接管精确路径并收敛到配置，不备份旧内容，不递归接管目录子项。类型不符始终报错。
- 自动创建的祖先目录不属于资源，destroy 后保留。
- Create/Update 的 upsert 按 path 字符串长度从短到长；同长度使用稳定实现顺序，但只承诺父先子后。
- Update 先根据 prior state 删除已移除或类型改变的子项，再执行 upsert。多路径写入不是事务，失败时保存已成功子项的 state。
- file 使用同目录临时文件，先以 0600 创建并在任何内容写入前收紧权限，最终 mode 与属主在原子 rename 前设置；owner/group 名称必须在目标内容写入前解析完成。
- 新建目录后 chmod/chown 失败时 best-effort 删除该新目录。
- directory 只收敛目录本身，不递归修改子项。
- Read 对缺失子项从 state 移除，让后续 plan 重建；忽略未管理子项。
- Delete 依据 prior state，按 path 长度从长到短；文件只删精确路径，目录用 `rmdir`，缺失视为成功，非空目录失败。绝不递归删除。
- 普通操作权限不足时，resource 使用 `sudo -n` 重试同一操作；其他错误不提权。权限分类依据 SFTP 状态或可靠的远端权限检查，不解析任意错误文本。

不支持额外 `ImportState`；首版导入机制就是路径块级 `auto_import`。

## 5. `data.ssh_data`、`data.ssh_sensitive_data` 与 `ephemeral.ssh_data`

三种 surface 使用相同的 conn/sudo/timeout/file/command/run 配置和执行语义。普通 data 持久化 conn 与结果；敏感 data 持久化 conn 与结果，但整个结果 map 静态 Sensitive；ephemeral 不持久化结果。

```hcl
data "ssh_data" "mydata" {
  conn = data.ssh_conn.myserver.conn
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

data "ssh_sensitive_data" "secret" {
  conn = data.ssh_conn.myserver.conn

  file {
    key  = "key4"
    path = "/run/secrets/token"
  }
}
```

公共字段：`key` 必填且全局唯一；`index = 0` 且非负；`sudo` 为三态覆盖；`timeout = "5m"`。三种 surface 都没有块级 `sensitive` 参数，也没有 `sensitive_values`。

- file：`path` 必填。
- command：`command` 为非空 argv，provider 安全 shell-quote，不解释用户 shell 运算符。
- run：`script` 必填，`shell = "sh"`，`envs` 覆盖 conn 中的同名值。脚本正文通过 stdin 交给 `<shell> -s` 执行，不写远端临时文件，也不出现在远端进程 argv 中；连接关闭或超时不会遗留脚本。
- command 使用 conn envs；run 使用合并后的 envs；file 操作不使用 envs。不依赖 sshd AcceptEnv。
- 顶层 sudo 默认 false；操作未设置时继承，显式 true/false 覆盖。所有 sudo 都使用 `sudo -n`，不接收密码。需要环境时显式传递允许的变量，不依赖 `sudo -E`。
- 所有配置错误必须在任何远端操作前一次性报告。按 index 从小到大执行，同 index 不承诺顺序；实现可顺序遍历，不刻意 shuffle。首错使整个操作失败，不返回部分 map，不回滚已产生的远端副作用。
- file 返回完整内容，command/run 成功时返回完整 stdout；保留结尾换行，拒绝非 UTF-8。
- 所有结果统一写入 `values`；空结果返回非 null 空 map。`data.ssh_sensitive_data.values` 与 `ephemeral.ssh_data.values` 的 Sensitive 是静态 schema 属性，不是配置参数。
- 每个 file/command/run 块都有 `trim`，Optional，默认 true。schema 使用 Optional+Computed，读取时把省略值规范化为 true 写回 plan/state，使该默认值稳定且不依赖结果 map。执行时先校验完整输出的 UTF-8，再对完整结果应用 `strings.TrimSpace`；`trim = false` 精确保留前后空白和结尾换行；仅含空白的结果变为空串；输出上限仍按 trim 前的原始字节计算。未知 `trim` 在任何远端操作前报错。
- `data.ssh_sensitive_data` 是普通 data source：Sensitive 只影响 CLI 显示，结果仍进入 plan/state。它和 `ephemeral.ssh_data` 的所有操作都抑制远端 stderr 和错误详情；`data.ssh_data` 的诊断包含有界、面向排错的 stderr。
- 内部 `Execute` 接收调用方固定的 sensitive bool 仅用于诊断，返回单一结果 map；不存在按块分流的结果 map。
- 单项 file/stdout 默认最多 512 KiB（524288 bytes）。只允许通过 `SSH_PROVIDER_MAX_OUTPUT_SIZE` 环境变量修改，不提供 HCL 参数，不允许无限制。`ssh_resource` 普通 `content` 的写入和 refresh 读取也使用同一上限；WriteOnly 内容不受该上限约束，摘要以流式计算。
- command/run 使用远端 `timeout` 加本地 session 清理；SFTP 卡顿时关闭连接解阻塞。超时使当前项失败。
- 迁移：旧块级 `sensitive = true` 的操作改放到 `data.ssh_sensitive_data`（ephemeral 则删除该参数），旧 `.sensitive_values["key"]` 改为 `.values["key"]`。`trim` 默认 true 会改变既有输出；依赖原始前后空白或结尾换行的配置必须显式 `trim = false`。data source 与 ephemeral schema 不能版本化，没有自动 state 升级，必须 refresh。

## 6. 实现边界

- Provider `Configure` 不做网络 I/O；provider 本身无凭据配置。
- 不使用 package 级可变连接 map；每个操作显式创建并关闭 client。
- 参考 goph 的 API 和实现，但首版优先直接使用 `x/crypto/ssh` 与 `pkg/sftp`，自行实现 host key、jump deadline、session 清理、SFTP 超时和命令 quoting，避免继承 goph v2 已核实的 context/jump 生命周期缺口。
- SSH 细节封装在内部 client；remote file/directory 语义与 Terraform schema 分离。
- 所有操作接收 context；限制响应和输出大小；诊断不得包含私钥、secret 内容或敏感 stderr。
- null 与 empty 严格区分；ValidateConfig 跳过 unknown，写入前再次拒绝非法 unknown/null。

## 7. 验收

1. `gofmt`、`go vet`、unit tests、`go test -race`、build、`go mod tidy`/`go mod verify`。
2. Schema/unit：字段互斥、unknown/null、path、mode/umask、index/key、内容二选一、hash plan、输出上限。
3. Hermetic SSH/SFTP：host key、认证失败不重试、临时网络重试、jump、命令超时、SFTP 解阻塞、响应上限、secret 日志扫描。
4. GNU Linux 真实 CLI acceptance：data 冒烟失败阻断资源、CRUD、auto_import、嵌套路径、漂移修复、第二次 apply 无变化、destroy 只删除 owned paths、非空目录保护、WriteOnly 稳定值。
5. Terraform 与 OpenTofu 分别跑最低版本最新 patch 和当前稳定版；文档只声称实际通过的矩阵。
6. dev_overrides 与 filesystem mirror 分别验证；GoReleaser 构建、checksum 和签名在 release 时验证。
