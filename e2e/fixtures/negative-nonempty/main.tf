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

provider "ssh" {
  host        = var.host
  port        = var.port
  user        = var.ssh_user
  private_key = var.private_key
  known_hosts = var.known_hosts
  timeout     = "2m"
  retry_delay = "1s"
}

resource "ssh_resource" "nonempty" {
  directory {
    path        = var.path
    mode        = "0755"
    auto_import = true
  }
}
