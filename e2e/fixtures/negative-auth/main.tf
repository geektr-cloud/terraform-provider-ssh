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

# private_key is deliberately unauthorized. The resource fails authentication
# directly; there is no unified connection probe any more.
provider "ssh" {
  host        = var.host
  port        = var.port
  user        = var.ssh_user
  private_key = var.private_key
  known_hosts = var.known_hosts
  timeout     = "20s"
  retry_delay = "1s"
}

resource "ssh_resource" "unauthorized" {
  file {
    path    = var.path
    content = "must-not-exist"
    mode    = "0644"
  }
}
