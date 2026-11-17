# End-to-end tests

Manual Docker-based end-to-end tests for `geektr-cloud/ssh`. They run a real
Terraform and/or OpenTofu CLI against a real OpenSSH server in a container.

**These tests are not part of any CI workflow.** Nothing under `.github/`
references this directory.

## What is covered

`fixtures/basic` applies a full configuration and then verifies remote state:

- `data.ssh_conn` connection probe.
- `ssh_resource` managed directory, ordinary `content` file, and write-only
  `content_wo` file whose value comes from `ephemeral.ssh_data`.
- `data.ssh_data` file read, `command` argv, `run` script over stdin, and
  `sensitive_values`.
- A managed path under `/root` while connecting as the non-root `e2e` user, so
  parent creation, writes, chmod, chown, refreshes, and removals exercise
  `sudo -n`.
- Remote content and permission assertions.
- A second plan with `-detailed-exitcode` returning 0.
- Remote drift on both a plain file and the write-only file, a plan returning
  2, and an apply that repairs both.
- Destroy and post-destroy absence checks.
- State and plan inspection: the write-only plaintext must not appear.
- Log inspection: no private key material and no write-only plaintext.

Negative fixtures cover:

- A pre-existing path is rejected by default and its old content/mode stay
  unchanged.
- `auto_import = true` adopts an existing path and later destroys it.
- Destroying a non-empty managed directory fails and leaves the unmanaged
  child intact.
- An unauthorized key fails authentication before any resource is created.
- An invalid `owner` fails before the target is modified, leaving content and
  mode intact.

## Requirements

- Docker daemon reachable by the current user.
- Go toolchain able to build this module.
- `terraform` and/or `tofu` on `PATH`, or explicit paths.
- `ssh`, `ssh-keygen`, and `ssh-keyscan` on the host.
- Network access for `docker build` (apt packages) and, if needed, Go modules.

The container image is Ubuntu 24.04 with `openssh-server`, `sudo`, and
`coreutils`. It contains no private key. `run.sh` generates an ed25519 key pair
for every run, copies only the public key into `authorized_keys`, fixes
ownership and modes, and uses a dynamically assigned `127.0.0.1` port. Only
public key authentication is enabled.

## Run

From the repository root:

```sh
./e2e/run.sh
```

The script builds the provider from the current working tree into a temporary
directory and generates a `dev_overrides` CLI configuration. It does not read
the provider from a registry. Every CLI invocation uses that override.

Run only one CLI:

```sh
E2E_TERRAFORM=/usr/local/bin/terraform ./e2e/run.sh
E2E_TOFU=/usr/local/bin/tofu ./e2e/run.sh
```

Setting either variable, even to an empty value, disables PATH discovery for
both tools. Only explicitly named, non-empty values run. To auto-discover both
CLIs on `PATH` again, unset both variables. If both are set but empty, the
script fails instead of silently falling back to `PATH`.

Run a version matrix with downloaded binaries:

```sh
E2E_TERRAFORM=/path/to/terraform-1.11.4/terraform \
E2E_TOFU=/path/to/tofu-1.11.14/tofu \
./e2e/run.sh
```

If neither `E2E_TERRAFORM`/`E2E_TOFU` nor a CLI on `PATH` is found, the script
fails with an explicit error. When both are available, they run sequentially,
each with its own working directory, state, plan files, and remote paths.

Environment variables:

| Variable | Meaning |
| --- | --- |
| `E2E_TERRAFORM` | Terraform binary to test. |
| `E2E_TOFU` | OpenTofu binary to test. |
| `E2E_APT_PROXY` | Proxy for the Docker build's apt traffic. Defaults to `HTTPS_PROXY`, then `https_proxy`. |
| `E2E_IMAGE` | Image repository prefix. Default `tf-ssh-e2e`; the tag is the run session. |
| `E2E_KEEP_ARTIFACTS` | `1` keeps logs, plans, state, and keys after a successful run. Failures are always kept. |

### Proxy

The suite makes foreign network requests during `docker build` (apt) and
possibly during `go build` (module download). Set the proxy for your
environment before running; nothing site-specific is hard-coded:

```sh
export HTTPS_PROXY=http://proxy.example:3128
export HTTP_PROXY=http://proxy.example:3128
export E2E_APT_PROXY=http://proxy.example:3128
./e2e/run.sh
```

`E2E_APT_PROXY` is passed as a Docker build argument and written only inside
the build layer's apt configuration, then removed. If it is unset and no
`HTTPS_PROXY`/`https_proxy` exists, apt runs without a proxy.

Docker image pulls are handled by the Docker daemon, not by this script. If the
daemon needs a proxy, configure it in the daemon or pre-pull
`ubuntu:24.04`.

## Artifacts and cleanup

The script creates one uniquely named container and one temporary directory.
Its `EXIT` trap removes only that container (after checking the
`tf-ssh-e2e=1` label) and that temporary directory. Failed runs keep the
temporary directory and print its path. Successful runs delete it unless
`E2E_KEEP_ARTIFACTS=1`.

Kept artifacts include CLI logs, plan files, state, `e2e.tfvars`, and the
generated private keys. Treat them as sensitive: the provider persists the
connection profile, including the private key, in state by design.

The image is left in the local Docker cache.

## Why not CI

This suite needs a Docker daemon, dynamically published ports, host SSH
clients, and foreign package/module downloads. Those requirements are heavier
and less hermetic than the existing unit and in-process acceptance tests. It
is a manual release check, so no workflow was added. Ordinary Go acceptance
tests are unchanged and still run only with `TF_ACC`, as documented in the
main README.
