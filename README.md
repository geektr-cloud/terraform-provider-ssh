# Terraform/OpenTofu SSH Provider

Manage remote files and directories over SSH, and read files or run commands on Linux hosts.

Provider address: `geektr-cloud/ssh`

## Surfaces

| Surface | Purpose |
| --- | --- |
| `data.ssh_conn` | Probe a connection and return a sensitive, persistable Connection Profile |
| `resource.ssh_resource` | Manage independent remote files and directories |
| `data.ssh_data` | Read files, run commands or scripts, persist results |
| `ephemeral.ssh_data` | Same operation model, no persistence of results or profile |

No provider function, connection pool, process-global map, or external daemon is implemented. Every Terraform lifecycle operation opens its own SSH connection and closes it afterwards.

## Requirements

- Terraform >= 1.11 or OpenTofu >= 1.11. `ssh_resource.file.content_wo` uses write-only attributes.
- Remote Linux with GNU coreutils `install`, `timeout`, `mktemp`, and a POSIX `sh`.
- Go 1.26.0 to build. The provider uses Terraform Plugin Framework v1.19.0.

Verified in this repository:

| Tool | Versions |
| --- | --- |
| Terraform | 1.11.4, 1.17.2 |
| OpenTofu | 1.11.14, 1.13.9 |
| Go | 1.26.0 toolchain building a `go 1.26.0` module; also verified with newer toolchains |

The CLI matrix ran against real OpenSSH with apply, drift repair, an empty second plan, write-only/ephemeral data, and destroy.

## Installation

```hcl
terraform {
  required_providers {
    ssh = {
      source  = "geektr-cloud/ssh"
      version = "~> 0.1"
    }
  }
}
```

## Connection Profile

```hcl
data "ssh_conn" "server" {
  type        = "linux" # only supported value
  host        = var.ssh_host
  port        = "22"
  user        = var.ssh_user
  private_key = file(var.ssh_private_key)

  known_hosts = "~/.ssh/known_hosts"
  envs        = { LANG = "C" }
  timeout     = "5m"
  retry_delay = "5s"
}

resource "ssh_resource" "config" {
  conn = data.ssh_conn.server.conn
  # ...
}
```

`data.ssh_conn` completes a real SSH handshake and authentication, then closes the probe connection before returning `conn`. A configuration or credential error therefore blocks dependent resources from producing many parallel failures.

`conn` contains the full profile, including the private key. Consumers persist it and reconnect for every operation. Destroy works from prior state alone, including after the configuration that produced it is removed.

Host verification modes are mutually exclusive:

- `known_hosts` (path, `~` expanded, file must exist)
- `host_key` (inline authorized-key)
- `insecure_ignore_host_key = true`

When all are omitted, `~/.ssh/known_hosts` is used. TOFU is not implemented. Authentication, host key, and configuration errors are not retried. Transient TCP/SSH network errors retry until the `timeout` budget is exhausted. Remote file and command operations do not retry automatically.

A single jump host can be configured with a nested `jump` object. The jump target is an SSH hop, not a SOCKS5 or HTTP proxy. It cannot contain another jump.

Encrypted private keys are supported through the `SSH_PROVIDER_PRIVATE_KEY_PASSPHRASE` environment variable. Passphrase material is never persisted by the provider.

## Security and state

| Value | Display | Plan/state |
| --- | --- | --- |
| `data.ssh_conn.private_key`, `data.ssh_conn.conn` | Sensitive | Persisted, contains private key |
| `ssh_resource.conn`, `data.ssh_data.conn` | Sensitive | Persisted, contains private key |
| `ephemeral.ssh_data.conn` and results | Ephemeral | Not persisted |
| `data.ssh_data.values` | Normal | Persisted |
| `data.ssh_data.sensitive_values` | Sensitive | Persisted |
| `file.content` | Normal | Persisted |
| `file.content_wo` | Write-only | Plaintext not persisted |
| `file.content_sha256` | Normal computed | Persisted digest |

**Sensitive is not encryption.** It only redacts CLI display. Plan files and state still contain the private key and ordinary data-source results. Use encrypted remote state, restricted access, and short-lived keys.

`content_wo` is never written to plan or state. Only its SHA-256 digest is persisted as `content_sha256`. The digest is a content identity, not a secret: low-entropy values can be guessed offline. `content_wo` must be stable between plan and apply. Literals, ordinary variables, and stable ephemeral variables work. Ephemeral resources that change on every phase are not supported.

### Key rotation limitation

Refresh uses the private key stored in prior state. If that key has been revoked, `terraform plan`, `terraform refresh`, and `terraform destroy` cannot authenticate. To rotate:

```sh
terraform apply -refresh=false
```

or update the state credential through a controlled state migration before refreshing. The same limitation applies to destroy: if the stored credential is invalid, restore a valid credential in state first.

## `ssh_resource`

```hcl
resource "ssh_resource" "app" {
  conn  = data.ssh_conn.server.conn
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

## `data.ssh_data` and `ephemeral.ssh_data`

```hcl
data "ssh_data" "inspect" {
  conn    = data.ssh_conn.server.conn
  sudo    = false
  timeout = "5m"

  file {
    key       = "config"
    path      = "/etc/app/config"
    sensitive = true
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
}
```

- Keys are required and globally unique across all operations. `index` defaults to 0 and must be non-negative.
- Operations run by ascending index. Same-index operations use a deterministic provider order. First failure aborts the whole operation and returns no partial maps.
- `command` is a non-empty argv. Arguments are shell-quoted one by one; shell operators inside an argument are not interpreted.
- `run` feeds the script body to the configured interpreter over stdin (`<shell> -s`), so no script file is written to the remote filesystem and the script does not appear in the remote process argv.
- `command` uses `conn.envs`; `run` merges `conn.envs` with block `envs` (block wins). File reads do not use envs.
- Sudo is explicit for `ssh_data`: set it on the resource or per operation. It never escalates implicitly.
- File contents and stdout must be valid UTF-8. Trailing newlines are preserved.
- Results go to `values` or `sensitive_values` according to `sensitive`. Empty result sets are empty maps, not null.
- Per-item output is capped at 512 KiB (524288 bytes). The same cap applies to ordinary `ssh_resource.file.content` writes and reads; a larger value fails apply or refresh. Set `SSH_PROVIDER_MAX_OUTPUT_SIZE` to a positive byte count to change it. There is no HCL option and no unlimited mode. Write-only content is not subject to this cap, and its digest is computed as a stream.
- Sensitive operations suppress remote stderr in diagnostics.

`ephemeral.ssh_data` has the same operation model. Its results and profile are not stored in plan or state. It is suitable for write-only inputs such as `content_wo`.

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

Manual Docker-based end-to-end tests run real Terraform/OpenTofu CLIs against OpenSSH, including non-root sudo and negative lifecycle scenarios:

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
