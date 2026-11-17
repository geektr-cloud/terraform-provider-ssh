# Changelog

All notable changes to this provider are documented here.

## 0.1.0 (2026-11-17)

Initial release.

### Added

- `data.ssh_conn`: real SSH probe returning a sensitive, persistable Connection Profile.
- `ssh_resource`: managed files and directories with atomic file writes, per-path modes and ownership, smart `sudo -n` escalation, `auto_import`, and non-recursive destroy.
- Write-only `file.content_wo` with persisted `content_sha256` digest.
- `data.ssh_data` and `ephemeral.ssh_data`: file reads, argv commands, and script runs through shell stdin without remote temporary files or script text in process arguments, with keyed results.
- Strict host verification through known_hosts or fixed host keys, with an explicit insecure override.
- Optional single jump host, connection retries for transient network failures, and bounded operation timeouts.
- Per-item output limit of 512 KiB, configurable only with `SSH_PROVIDER_MAX_OUTPUT_SIZE`.
- Documentation, examples, unit tests, in-process acceptance tests, and GoReleaser configuration.
- Manual Docker-based Terraform/OpenTofu end-to-end tests under `e2e/`, intentionally excluded from CI.
- Build baseline raised to Go 1.26.0 with updated `golang.org/x/crypto`, `golang.org/x/net`, `golang.org/x/text`, and `google.golang.org/grpc` dependencies.
- `content_wo` plan/apply stability check: a changed write-only value between plan and apply is rejected before any remote write.

### Known limitations

- The private key and ordinary data-source results are persisted in state. Sensitive is display redaction, not encryption.
- Refresh and destroy use the credential in prior state. See README for the rotation procedure.
- Remote import is block-level `auto_import` only; no resource `ImportState`.
- Linux with GNU coreutils is required. Other Unix variants, BusyBox, and Windows are unsupported.
