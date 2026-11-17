terraform {
  required_providers {
    ssh = {
      source = "geektr-cloud/ssh"
    }
  }
}

provider "ssh" {}
