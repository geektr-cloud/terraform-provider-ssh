provider "ssh" {
  host        = "example.internal"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")
  known_hosts = "~/.ssh/known_hosts"
}

data "ssh_data" "inspect" {
  # Omitting conn inherits every provider default. A JSON override can change
  # only the fields that differ for this consumer.
  conn = jsonencode({
    user = "auditor"
  })

  file {
    key  = "config"
    path = "/etc/app/config.yaml"
  }

  command {
    key     = "kernel"
    command = ["uname", "-r"]
  }

  file {
    key  = "raw"
    path = "/etc/app/raw.txt"
    trim = false # preserve exact bytes, including trailing newlines
  }
}
