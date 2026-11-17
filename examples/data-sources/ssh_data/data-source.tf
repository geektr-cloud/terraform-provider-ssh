data "ssh_conn" "server" {
  host        = "example.internal"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")
  known_hosts = "~/.ssh/known_hosts"
}

data "ssh_data" "inspect" {
  conn = data.ssh_conn.server.conn

  file {
    key  = "config"
    path = "/etc/app/config.yaml"
  }

  command {
    key     = "kernel"
    command = ["uname", "-r"]
  }
}
