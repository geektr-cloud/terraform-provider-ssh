terraform {
  required_version = ">= 1.11"

  required_providers {
    ssh = {
      source = "geektr-cloud/ssh"
    }
  }
}

variable "host" {
  type = string
}

variable "port" {
  type = string
}

variable "ssh_user" {
  type = string
}

variable "private_key" {
  type      = string
  sensitive = true
}

variable "known_hosts" {
  type = string
}

variable "path" {
  type = string
}

variable "bad_owner" {
  type = string
}

data "ssh_conn" "target" {
  host        = var.host
  port        = var.port
  user        = var.ssh_user
  private_key = var.private_key
  known_hosts = var.known_hosts
  timeout     = "2m"
  retry_delay = "1s"
}

# Ownership resolution happens before the target is touched. A missing owner
# must leave the adopted file's old content and mode intact.
resource "ssh_resource" "atomic_owner" {
  conn  = data.ssh_conn.target.conn
  owner = var.bad_owner

  file {
    path        = var.path
    content     = "clobber-content"
    mode        = "0600"
    auto_import = true
  }
}
