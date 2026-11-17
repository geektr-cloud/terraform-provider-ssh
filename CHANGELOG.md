# Changelog

All notable changes to this provider are documented here.

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
