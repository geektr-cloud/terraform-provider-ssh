data "ssh_conn" "server" {
  type        = "linux"
  host        = "example.internal"
  port        = "22"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")

  known_hosts = "~/.ssh/known_hosts"
  envs        = { LANG = "C" }
  timeout     = "5m"
  retry_delay = "5s"
}
