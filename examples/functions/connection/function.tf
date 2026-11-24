# Returns only {"host","user","port"}. It is deterministic, performs no
# network access, and does not read provider configuration.
output "connection" {
  value = provider::ssh::connection("example.internal", "deploy", "22")
}
