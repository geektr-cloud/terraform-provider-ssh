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

# The provider has no defaults here. A complete JSON override is persisted in
# the resource state, including this explicitly supplied private key.
provider "ssh" {}

resource "ssh_resource" "json_key" {
  conn = jsonencode({
    host        = var.host
    port        = var.port
    user        = var.ssh_user
    private_key = var.private_key
    known_hosts = var.known_hosts
  })

  file {
    path    = var.path
    content = "json-key\n"
    mode    = "0600"
  }
}
