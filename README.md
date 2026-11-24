# Terraform/OpenTofu SSH Provider

Manage remote files and directories over SSH, and read files or run commands on Linux hosts.

Provider address: `geektr-cloud/ssh`

## Surfaces

| Surface | Purpose |
| --- | --- |
| `provider "ssh"` | Optional Provider Connection Defaults shared by every consumer |
| `resource.ssh_resource` | Manage independent remote files and directories |
| `data.ssh_data` | Read files, run commands or scripts, persist ordinary results |
| `data.ssh_sensitive_data` | Same operation model with all results marked sensitive; still persisted |
| `ephemeral.ssh_data` | Same operation model, no persistence of results or connection configuration |
| `provider::ssh::connection` | Deterministic JSON override helper for a dynamic target |

No connection probe, connection pool, process-global map, or external daemon is implemented. Every Terraform lifecycle operation opens its own SSH connection and closes it afterwards.

## Requirements

- Terraform >= 1.11 or OpenTofu >= 1.11. `ssh_resource.file.content_wo` uses write-only attributes and provider functions require this baseline.
- Remote Linux with GNU coreutils `install`, `timeout`, `mktemp`, and a POSIX `sh`.
- Go 1.26.0 to build. The provider uses Terraform Plugin Framework v1.19.0.

Verified in this repository:

| Tool | Versions |
| --- | --- |
| Terraform | 1.11.4, 1.17.2 |
| OpenTofu | 1.11.14, 1.13.9 |
| Go | 1.26.0 toolchain building a `go 1.26.0` module; also verified with newer toolchains |

The CLI matrix runs against real OpenSSH with apply, drift repair, an empty second plan, write-only/ephemeral data, layered provider defaults, JSON overrides, the connection function, and destroy.

## Installation

```hcl
terraform {
  required_providers {
    ssh = {
      source  = "geektr-cloud/ssh"
      version = "~> 0.2"
    }
  }
}
```

## Layered connection configuration

Connection settings live in two layers:

1. Provider Connection Defaults (`provider "ssh"`), all fields optional.
2. Connection Override (`conn` on a resource, data source, or ephemeral resource), a Sensitive JSON object string.

Built-in defaults fill whatever remains.

```hcl
provider "ssh" {
  type        = "linux" # only supported value
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
  # Optional JSON override. Omit it to inherit everything above.
  conn = jsonencode({
    user = "deploy"
  })

  file {
    path    = "/etc/myapp/config.yaml"
    content = "listen: 0.0.0.0:8080\n"
    mode    = "0644"
  }
}
```

`provider.Configure` performs no network I/O. It stores the defaults and performs configuration-only checks (supported type, port range, duration syntax, environment variable names, mutually exclusive host key modes).

### Merge rules

| Situation | Result |
| --- | --- |
| `conn` omitted, JSON `null`, or `{}` | No override; provider defaults unchanged |
| Field absent from JSON | Inherit provider value |
| Field explicitly `null` in JSON | Clear provider value and use the built-in default |
| `envs` | Merge by key; same key wins; `null` clears all; `{}` adds nothing |
| `jump` | Replace the whole jump profile; `null` removes it; no internal merge |
| Explicit `known_hosts` or `host_key` | Select that host key mode and clear the other provider modes |
| Explicit `insecure_ignore_host_key = true` | Select insecure mode and clear known_hosts/host_key |
| Explicit `insecure_ignore_host_key = false` | Do not clear a provider-fixed host key mode |
| No host key mode selected | `~/.ssh/known_hosts` |
| Missing host, user, or private_key after merging | Error before dialing |

Built-in defaults: `type = "linux"`, `port = "22"`, `timeout = "5m"`, `retry_delay = "5s"`, `envs = {}`, `insecure_ignore_host_key = false`.

`jump` is a single SSH hop, not a SOCKS5 or HTTP proxy. A jump profile contains host, port, user, private key, and host key settings. It cannot contain another jump, `envs`, `timeout`, or `retry_delay`.

### Strict JSON

`conn` must be a JSON object or `null`. Arrays, scalars, unknown fields, duplicate keys at any level (including `jump` and `envs`), and wrong value types are rejected. Diagnostics report only the field path and error type. They never include the raw JSON, a private key, or any secret value.

Unknown `conn` values are deferred to execution and must be known before any dial.

### Provider function

```hcl
resource "ssh_resource" "dynamic" {
  conn = provider::ssh::connection(var.target_host, "deploy", var.target_port)

  file {
    path    = "/opt/app/marker"
    content = "managed\n"
    mode    = "0644"
  }
}
```

`provider::ssh::connection(host, user, port)` returns exactly `{"host":...,"user":...,"port":...}`. It is deterministic, performs no network access, does not read provider configuration, and never accepts key material.

### Connection identity and replacement

`ssh_resource.connection_identity` is a non-sensitive canonical encoding of the target and jump host/port/user. A change replaces the resource. Credential, host-key, `envs`, `timeout`, and `retry_delay` changes update in place.

Read reconnects with the current provider defaults plus the prior JSON override and preserves the prior identity so the next plan can detect provider default changes. If an identity field is unknown during plan, the provider skips the comparison rather than treating unknown as an empty string.

Destroy also uses the current provider defaults plus the prior override. Destroying after removing the provider block, or with defaults that no longer supply missing fields, fails explicitly. When an identity change triggers replacement, Read and Delete still use the **current** endpoint, not the prior `connection_identity`. Before replacing an endpoint, ensure that paths with the same names on the new endpoint are safe to refresh and delete; otherwise restore the old endpoint and destroy first.

## Security and state

| Value | Display | Plan/state |
| --- | --- | --- |
| Provider `private_key` and other provider defaults | Sensitive where applicable | Not copied into consumer state |
| Consumer `conn` JSON | Sensitive | Persisted by ordinary resource/data sources |
| Private key written inside `conn` | Sensitive | Persisted with that override |
| `ssh_resource.connection_identity` | Normal computed | Persisted |
| `ephemeral.ssh_data` configuration and results | Ephemeral | Not persisted |
| `data.ssh_data.values` | Normal | Persisted |
| `data.ssh_sensitive_data.values` | Sensitive | Persisted |
| `ephemeral.ssh_data.values` | Sensitive | Not persisted |
| `file.content` | Normal | Persisted |
| `file.content_wo` | Write-only | Plaintext not persisted |
| `file.content_sha256` | Normal computed | Persisted digest |

**Sensitive is not encryption.** It only redacts CLI display. Plan files and state still contain the `conn` JSON (including any private key written there), `data.ssh_sensitive_data.values`, and ordinary data-source results. Use encrypted remote state, restricted access, and short-lived keys.

Provider-level private keys are not written to resource or data source state. A private key placed in a consumer's `conn` JSON is written to that consumer's state and plan. Prefer provider defaults for shared credentials.

`content_wo` is never written to plan or state. Only its SHA-256 digest is persisted as `content_sha256`. The digest is a content identity, not a secret: low-entropy values can be guessed offline. `content_wo` must be stable between plan and apply. Literals, ordinary variables, and stable ephemeral variables work. Ephemeral resources that change on every phase are not supported.

Encrypted private keys are supported through the `SSH_PROVIDER_PRIVATE_KEY_PASSPHRASE` environment variable. Passphrase material is never persisted by the provider.

### Credential rotation

Read and destroy use the current provider defaults plus the prior override. Rotating the provider-level private key therefore affects the next refresh without rewriting resource state. If a private key that is persisted inside `conn` is revoked, refresh and destroy can no longer authenticate. Update that override (for example with `terraform apply -refresh=false`) or perform a controlled state update before refreshing.

## `ssh_resource`

```hcl
resource "ssh_resource" "app" {
  umask = "022"

  directory {
    path        = "/opt/app"
    mode        = "0750"
    auto_import = false
  }

  file {
    path    = "/opt/app/config.yaml"
    content = "listen: 0.0.0.0:8080\n"
    mode    = "0644"
  }

  file {
    path       = "/opt/app/secret"
    content_wo = var.stable_secret
    mode       = "0600"
  }
}
```

Ownership and content:

- Top-level `owner`/`uid` and `group`/`gid` are defaults for path blocks. `owner` conflicts with `uid`; `group` conflicts with `gid`.
- File default mode is `0666 &^ umask`; directory default is `0777 &^ umask`.
- Files are written through a same-directory temporary file. The temporary file is restricted to `0600` before any content is written, then mode and ownership are applied before an atomic rename. Owner/group names are resolved before the target is touched.
- Directory creation is not atomic. If mode or ownership fails on a newly created directory, the provider attempts to remove it.
- Ordinary `file.content` reads are also capped by `SSH_PROVIDER_MAX_OUTPUT_SIZE`; a larger managed file fails refresh instead of being loaded without a bound. Write-only digest reads stream the file.
- Directories converge only the directory itself, never children.
- Unmanaged existing targets fail by default. `auto_import = true` adopts the exact path and converges it. Children are not adopted. Type mismatches always fail.
- Ancestor directories created to reach a managed path are unmanaged and survive destroy.
- Missing paths are removed from state on refresh and recreated on the next plan.
- Destroy removes files exactly and directories with `rmdir`. Non-empty directories fail; the provider never removes recursively.
- Path must be a cleaned absolute path, not `/`. The final target must not be a symlink.
- Apply is not transactional. If a later path fails, successfully written paths remain in state.
- Import is intentionally not implemented. Use block-level `auto_import` for adoption.

When a normal operation fails with a permission error, the resource retries the same operation with `sudo -n`. Other errors do not trigger escalation. All sudo use is non-interactive.

## `data.ssh_data`, `data.ssh_sensitive_data`, and `ephemeral.ssh_data`

```hcl
data "ssh_data" "inspect" {
  conn    = jsonencode({ user = "auditor" })
  sudo    = false
  timeout = "5m"

  file {
    key  = "config"
    path = "/etc/app/config"
  }

  command {
    key     = "kernel"
    command = ["uname", "-r"]
  }

  run {
    key    = "services"
    script = "systemctl list-units --type=service --no-legend\n"
    shell  = "sh"
    envs   = { LANG = "C" }
  }

  file {
    key  = "raw"
    path = "/etc/app/raw.txt"
    trim = false # preserve exact leading and trailing whitespace
  }
}

data "ssh_sensitive_data" "credential" {
  file {
    key  = "token"
    path = "/run/secrets/api-token"
  }
}
```

All three surfaces use the same connection, sudo, timeout, and operation model. They differ only in where results go and how diagnostics are reported:

| Surface | Result map | Persisted | Diagnostics |
| --- | --- | --- | --- |
| `data.ssh_data` | `values`, ordinary | Yes | Bounded remote stderr included |
| `data.ssh_sensitive_data` | `values`, Sensitive | Yes | Remote stderr suppressed |
| `ephemeral.ssh_data` | `values`, Sensitive | No | Remote stderr suppressed |

- Keys are required and globally unique across all operations. `index` defaults to 0 and must be non-negative.
- Operations run by ascending index. Same-index operations use a deterministic provider order. First failure aborts the whole operation and returns no partial map.
- `command` is a non-empty argv. Arguments are shell-quoted one by one; shell operators inside an argument are not interpreted.
- `run` feeds the script body to the configured interpreter over stdin (`<shell> -s`), so no script file is written to the remote filesystem and the script does not appear in the remote process argv.
- `command` uses the merged connection envs; `run` merges those with block `envs` (block wins). File reads do not use envs.
- Sudo is explicit: set it on the data source or per operation. It never escalates implicitly.
- File contents and stdout must be valid UTF-8. Trailing newlines are preserved.
- Every operation writes to `values`. There is no block-level `sensitive` argument and no `sensitive_values` map. Empty result sets are empty maps, not null.
- Every `file`, `command`, and `run` block accepts `trim`, which defaults to `true`. Results are validated as UTF-8 first, then `strings.TrimSpace` removes leading and trailing Unicode whitespace. Set `trim = false` to preserve exact output, including trailing newlines. A whitespace-only result becomes an empty string. `trim` does not change the raw-output size limit.
- Per-item output is capped at 512 KiB (524288 bytes). The same cap applies to ordinary `ssh_resource.file.content` writes and reads; a larger value fails apply or refresh. Set `SSH_PROVIDER_MAX_OUTPUT_SIZE` to a positive byte count to change it. There is no HCL option and no unlimited mode. Write-only content is not subject to this cap, and its digest is computed as a stream.

`data.ssh_sensitive_data` marks the whole `values` map Sensitive for CLI display. It is still an ordinary data source: results are written to plan and state. Sensitive is not encryption. Put operations with sensitive results here instead of `data.ssh_data`.

`ephemeral.ssh_data` has the same operation model. Its results and connection configuration are not stored in plan or state. It is suitable for write-only inputs such as `content_wo`.

## Migration from 0.1

`data.ssh_conn` and object-valued `conn` were removed. There is no automatic state upgrade.

1. Move connection settings into the `provider "ssh"` block, or replace `conn = data.ssh_conn.X.conn` with a JSON override.
2. Wrap object overrides in `jsonencode()`, or use `provider::ssh::connection(host, user, port)` plus provider defaults:

   ```hcl
   # Before
   conn = {
     host        = "example.internal"
     port        = "22"
     user        = "deploy"
     private_key = var.private_key
   }

   # After
   conn = jsonencode({
     host        = "example.internal"
     port        = "22"
     user        = "deploy"
   })
   ```

3. Run `terraform state rm` for the removed `data.ssh_conn` instances, then refresh. Managed resources keep their paths; resource state from 0.1 cannot be reused because the `conn` attribute type changed, so re-apply or perform an explicit state migration.
4. Replace references to `data.ssh_conn.X.conn` with either the new `conn` JSON string or `null`.
5. Review where private keys are stored. Provider-level keys are no longer copied into consumer state; keys inside `conn` still are.
6. Data source and ephemeral schemas cannot be versioned. Update configuration and refresh.

The previous migration from block-level `sensitive` to `data.ssh_sensitive_data`, `.values`, and `trim` remains in effect.

## Output limit

```sh
export SSH_PROVIDER_MAX_OUTPUT_SIZE=1048576
```

The value is read at operation time. Invalid or non-positive values fail the operation before any remote action.

## Development

```sh
gofmt -l .
go vet ./...
go test -race ./...
go build ./...
go mod verify
```

Acceptance tests use an in-process SSH server and run only with `TF_ACC`:

```sh
TF_ACC=1 TF_ACC_TERRAFORM_PATH="$(command -v terraform)" go test ./internal/provider -run TestAcc -v
```

The harness in `terraform-plugin-testing` v1.14.1 is Terraform-specific. OpenTofu is verified through the CLI with the same provider build and a `dev_overrides` configuration; the repository does not claim an automated OpenTofu acceptance matrix until CI adds it.

Manual Docker-based end-to-end tests run real Terraform/OpenTofu CLIs against OpenSSH, including non-root sudo, provider defaults, JSON overrides, the connection function, and negative lifecycle scenarios:

```sh
./e2e/run.sh
```

See [e2e/README.md](e2e/README.md) for version selection, proxies, coverage, and artifacts. This suite is intentionally not part of CI.

Generate provider documentation:

```sh
go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0 generate --provider-name ssh
```

## License

MIT. See [LICENSE](LICENSE).
