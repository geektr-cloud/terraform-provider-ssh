provider "ssh" {
  host        = "example.internal"
  port        = "22"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")
  known_hosts = "~/.ssh/known_hosts"
}

resource "ssh_resource" "app" {
  # Optional JSON override. It may supply only the fields that differ from the
  # provider defaults, or a complete profile with its own private_key.
  conn = jsonencode({
    user = "deploy"
  })

  directory {
    path = "/opt/app"
    mode = "0755"
  }

  file {
    path    = "/opt/app/config.yaml"
    content = "listen: 0.0.0.0:8080\n"
    mode    = "0644"
  }
}

resource "ssh_resource" "dynamic" {
  # Deterministic helper for a dynamic target: no network, no provider read,
  # no key material. Remaining fields inherit the provider defaults.
  conn = provider::ssh::connection("other.internal", "deploy", "22")

  file {
    path    = "/opt/app/marker"
    content = "managed\n"
    mode    = "0644"
  }
}
