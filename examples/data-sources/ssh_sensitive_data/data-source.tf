provider "ssh" {
  host        = "example.internal"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")
  known_hosts = "~/.ssh/known_hosts"
}

data "ssh_sensitive_data" "credential" {
  file {
    key  = "token"
    path = "/run/secrets/api-token"
  }

  command {
    key     = "fingerprint"
    command = ["ssh-keygen", "-lf", "/etc/ssh/ssh_host_ed25519_key.pub"]
  }

  file {
    key  = "raw-token"
    path = "/run/secrets/raw-token"
    trim = false
  }
}
