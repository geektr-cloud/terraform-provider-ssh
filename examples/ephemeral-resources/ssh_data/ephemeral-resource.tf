provider "ssh" {
  host        = "example.internal"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")
  known_hosts = "~/.ssh/known_hosts"
}

ephemeral "ssh_data" "secret" {
  file {
    key  = "token"
    path = "/run/secrets/token"
  }
}
