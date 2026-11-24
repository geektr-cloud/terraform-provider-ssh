terraform {
  required_providers {
    ssh = {
      source = "geektr-cloud/ssh"
    }
  }
}

# Provider Connection Defaults are optional and may be partial. Consumers
# inherit every field they do not override.
provider "ssh" {
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
  file {
    path    = "/etc/myapp/config.yaml"
    content = "listen: 0.0.0.0:8080\n"
    mode    = "0644"
  }
}
