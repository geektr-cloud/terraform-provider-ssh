# Changelog

All notable changes to this provider are documented here.

## 0.2.0 (2026-11-25)

### Added

- Provider-level Connection Defaults. Every Connection Profile field is optional in the `provider "ssh"` block. `Configure` performs no network I/O.
- Optional Sensitive JSON string `conn` on `ssh_resource`, `data.ssh_data`, `data.ssh_sensitive_data`, and `ephemeral.ssh_data`. Built-in defaults, provider defaults, and the override merge in that order. Explicit JSON `null` clears a provider value; `envs` merge by key; `jump` replaces the whole profile.
- Strict JSON validation for `conn`: object or `null` only, no unknown fields, duplicate keys at any level, arrays, scalars, or wrong types. Diagnostics never echo raw JSON or secret values.
- `ssh_resource.connection_identity`: a non-sensitive canonical target/jump identity. Identity changes replace the resource; credential, host-key, env, and timeout changes do not.
- Provider function `provider::ssh::connection(host, user, port)`, returning only `{"host","user","port"}`. It is deterministic, performs no network access, and does not read provider configuration.
- Tests covering provider defaults, partial configuration, JSON absent/null/empty/errors, env/jump/host-key merging, credential rotation, identity replacement, destroy with prior overrides, and state credential boundaries.

### Changed

- Consumers no longer persist the provider-level private key. State keeps the raw JSON override and the connection identity, and every lifecycle operation re-resolves with the current provider defaults.
- `ssh_resource.Read` and `Delete` use the current provider defaults plus the prior override. Destroy fails explicitly if the provider configuration was removed or is incomplete. When identity changes trigger replacement, Read/Delete still use the current endpoint; ensure same-named paths there are safe to operate on, or restore the old endpoint and destroy first.
- Sensitive and ordinary data sources and the ephemeral resource keep their existing single `values` map and `trim` behavior.

### Removed

- `data.ssh_conn` and the unified connection preflight. Remove its instances from state.
- The object-valued `conn` argument. This is a breaking change with no state upgrade.

### Migration

- Move shared connection settings into `provider "ssh"`, or replace `conn = data.ssh_conn.X.conn` with a JSON override.
- Wrap object overrides in `jsonencode()`, or use `provider::ssh::connection(host, user, port)` and let the provider supply credentials and host-key settings.
- Remove `data.ssh_conn` from state and refresh. Managed paths keep their remote objects, but the resource `conn` attribute type changed, so re-apply or perform an explicit state migration.
- Audit where private keys are stored: provider-level keys no longer enter consumer state; keys inside `conn` still do.

## 0.1.0 (2026-11-18)

Initial release.

### Added

- `data.ssh_conn`: real SSH probe returning a sensitive, persistable Connection Profile.
- `ssh_resource`: managed files and directories with atomic file writes, per-path modes and ownership, smart `sudo -n` escalation, `auto_import`, and non-recursive destroy.
- Write-only `file.content_wo` with persisted `content_sha256` digest.
- `data.ssh_data` and `ephemeral.ssh_data`: file reads, argv commands, and script runs through shell stdin without remote temporary files or script text in process arguments, with keyed results.
- `data.ssh_sensitive_data`: same connection and operation model as `data.ssh_data`, with the whole `values` map statically Sensitive and remote stderr suppressed in every diagnostic. It is an ordinary data source: results are still persisted in plan and state, and Sensitive is display redaction, not encryption.
- `trim` on every `file`, `command`, and `run` block of `data.ssh_data`, `data.ssh_sensitive_data`, and `ephemeral.ssh_data`. It is optional and defaults to `true`; results are validated as UTF-8 and then trimmed with `strings.TrimSpace`. `trim = false` preserves exact output, including trailing newlines.
- Strict host verification through known_hosts or fixed host keys, with an explicit insecure override.
- Optional single jump host, connection retries for transient network failures, and bounded operation timeouts.
- Per-item output limit of 512 KiB, configurable only with `SSH_PROVIDER_MAX_OUTPUT_SIZE`.
- Documentation, examples, unit tests, in-process acceptance tests, and GoReleaser configuration.
- Manual Docker-based Terraform/OpenTofu end-to-end tests under `e2e/`, intentionally excluded from CI.
- Build baseline raised to Go 1.26.0 with updated `golang.org/x/crypto`, `golang.org/x/net`, `golang.org/x/text`, and `google.golang.org/grpc` dependencies.
- `content_wo` plan/apply stability check: a changed write-only value between plan and apply is rejected before any remote write.

### Changed

- `data.ssh_data` now writes every operation result to `values`. Block-level `sensitive` arguments and the `sensitive_values` map were removed; diagnostics include bounded remote stderr for every operation.
- `ephemeral.ssh_data` now writes every result to `values`, which is statically Sensitive and never persisted. Block-level `sensitive` and `sensitive_values` were removed, and all error diagnostics suppress remote stderr.
- Results are now trimmed by default. Configurations that depend on leading or trailing whitespace, including a trailing newline, must set `trim = false`. The raw output limit is unchanged: it is applied before trimming.
- Internal operation model and `Execute` use a single result map and a caller-supplied sensitive flag for diagnostics only.

### Migration

- Move operations whose results need display redaction from `data.ssh_data` to `data.ssh_sensitive_data` and remove their `sensitive` arguments.
- Replace every `.sensitive_values["key"]` reference with `.values["key"]`.
- Add `trim = false` to every `file`, `command`, or `run` block that must keep its exact output, especially a trailing newline. The new default is `true`.
- Data source and ephemeral schemas cannot be versioned. Update the configuration and refresh state; there is no automatic state upgrade.

### Known limitations

- The private key and ordinary data-source results are persisted in state. Sensitive is display redaction, not encryption.
- Refresh and destroy use the credential in prior state. See README for the rotation procedure.
- Remote import is block-level `auto_import` only; no resource `ImportState`.
- Linux with GNU coreutils is required. Other Unix variants, BusyBox, and Windows are unsupported.
