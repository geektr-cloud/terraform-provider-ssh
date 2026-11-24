# SSH Provider

通过 SSH 管理远端文件与目录，并提供一次性远端读取和命令执行结果。核心边界是只拥有显式声明或显式接管的远端路径。

## Language

### Remote ownership

**Managed Path Collection**:
一个 `ssh_resource` 管理的独立 Managed Path 集合；同一集合内路径唯一，移除路径即解除管理并删除该路径，改变路径代表不同的管理身份。
_Avoid_: monolithic remote resource, ordered path list

**Managed Path**:
由配置显式声明，或由 Existing Path Adoption 显式接管的单个远端绝对文件或目录路径；其他远端对象不属于它，最终路径不能是符号链接。
_Avoid_: remote object, recursive ownership

**Existing Path Adoption**:
启用 `auto_import` 后，将 apply 前已经存在的精确目标路径纳入管理；未启用时，该路径视为冲突。
_Avoid_: automatic recursive import

**Unmanaged Parent Directory**:
为到达 Managed Path 而创建或经过的祖先目录；它不因被经过或自动创建而纳入管理。
_Avoid_: owned parent, implicit directory resource

**Managed Directory**:
只拥有精确目录路径本身的 Managed Path；其他 actor 创建的子项不会被接管，非空目录不能被销毁。
_Avoid_: directory tree, recursive directory

**Managed File**:
内容恰好由普通 `content` 或 WriteOnly `content_wo` 之一决定的 Managed Path；空字符串是有效的空文件内容。
_Avoid_: metadata-only file, dual content source

### Results and execution

**Result Set**:
一次 data 或 ephemeral `ssh_data` 求值产生的 keyed UTF-8 文本集合；所有结果统一写入 `values`，key 在全部操作中唯一。
_Avoid_: dynamically sensitive map elements, trimmed output, partitioned result maps

**Result Trimming**:
对每个 file/command/run 结果在 UTF-8 校验后应用的默认前后空白去除；`trim = true` 为默认值，`trim = false` 保留精确输出与结尾换行。
_Avoid_: byte normalization, newline stripping by default

**Sensitive Data Source**:
操作模型与 `data.ssh_data` 相同，但整个 `values` 静态 Sensitive 且所有操作抑制远端 stderr 的普通 data source；Sensitive 只是显示保护，结果仍会持久化。
_Avoid_: encrypted data source, ephemeral data source

**Sensitive Result**:
由 `data.ssh_sensitive_data` 或 `ephemeral.ssh_data` 产生的结果；Sensitive 控制显示，普通 data source 仍会持久化它，只有 ephemeral surface 不持久化。
_Avoid_: encrypted state value, individually sensitive map element

**Secret Content Digest**:
WriteOnly 文件内容的 SHA-256 摘要，作为持久化的内容身份用于发现配置变化与远端漂移；它不是加密，低熵内容可能遭到离线猜测。
_Avoid_: encrypted content, plaintext content state

**Command**:
由非空 argv 表达的远端程序调用；参数保持边界，不将 argv 解释为用户 shell 表达式。
_Avoid_: shell string, joined arguments

**Script Run**:
由指定 shell 通过标准输入接收并执行的脚本操作；不创建远端临时脚本文件，也不把脚本正文放入远端进程参数。
_Avoid_: command argv, inline shell concatenation, temporary remote script

### Privilege and identity

**Connection Profile**:
独立建立 SSH 连接所需的完整配置，包括目标身份、认证、主机校验、跳板与超时；它不包含 live connection handle，各生命周期操作据此独立重连。
_Avoid_: connection UUID, live connection handle, process-global connection

**Layered Connection Profile**:
由 Provider Connection Defaults 与消费者的 Connection Override 合并形成的 Connection Profile；覆盖只作用于本次消费者，不修改其他消费者的默认配置。
_Avoid_: provider-only connection, duplicated full profile

**Provider Connection Defaults**:
provider 持有的部分或完整 Connection Profile，供未覆盖对应字段的消费者共享；凭据默认值不属于资源持久身份。
_Avoid_: global live connection, resource-stored provider credential

**Connection Override**:
消费者针对一次连接提供的部分 Connection Profile；未出现的字段继承 Provider Connection Defaults，显式空值清除对应默认值并回到内置默认行为。
_Avoid_: replacement profile, implicit reset

**Connection Identity**:
标识目标 SSH 端点的非敏感部分，包括目标 host/port/user 与 Jump Host 的 host/port/user；它决定资源是否指向同一远端，不包含私钥、host-key 配置、环境变量或超时。
_Avoid_: credential fingerprint, full connection profile

**Jump Host**:
Connection Profile 中用于到达目标主机的单层 SSH 跳板；它不是 SOCKS5/HTTP proxy，且自身不能再包含 Jump Host。
_Avoid_: proxy, recursive jump chain

**Target Host**:
提供 GNU coreutils 的 `install`、`timeout`、`mktemp` 及指定 shell 的 Linux 主机；其他 Unix、BusyBox 或 Windows 不在支持范围内。
_Avoid_: arbitrary Linux, POSIX-compatible host

**Inline Private Key Authentication**:
仅使用配置中的私钥内容进行 SSH 认证；Provider Connection Defaults 中的私钥不进入资源持久身份，普通消费者在 Connection Override 中显式提供的私钥仍会随该覆盖配置持久化。
_Avoid_: private key path, agent authentication, password authentication

**Smart Escalation**:
`ssh_resource` 先以普通身份操作，仅在权限不足时改用无交互 sudo 完成同一操作。
_Avoid_: automatic escalation, sudo always

**Explicit Escalation**:
`ssh_data` 由整体或单个操作的 `sudo = true` 明确选择无交互 sudo，不因权限错误自动切换身份。
_Avoid_: implicit sudo

**Strict Host Verification**:
连接默认必须依据 known_hosts 或固定主机密钥确认 SSH 服务端身份；只有显式的不安全选项才允许跳过。
_Avoid_: accept any host key, TOFU by default
