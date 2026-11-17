data "ssh_conn" "server" {
  host        = "example.internal"
  port        = "22"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")
  known_hosts = "~/.ssh/known_hosts"
}

resource "ssh_resource" "app" {
  conn = data.ssh_conn.server.conn

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
