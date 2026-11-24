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

variable "root_base" {
  type = string
}

variable "public_base" {
  type = string
}

variable "seed_file" {
  type = string
}

variable "secret_file" {
  type = string
}

variable "run_token" {
  type = string
}

# Provider Connection Defaults: consumers below inherit these unless they
# provide a JSON override.
provider "ssh" {
  type        = "linux"
  host        = var.host
  port        = var.port
  user        = var.ssh_user
  private_key = var.private_key
  known_hosts = var.known_hosts
  envs        = { LANG = "C" }
  timeout     = "2m"
  retry_delay = "1s"
}

# The write-only input is produced by an ephemeral resource, so its plaintext
# never reaches plan or state. No conn override: every field is inherited.
ephemeral "ssh_data" "writeonly" {
  file {
    key  = "secret"
    path = var.secret_file
    sudo = true
  }
}

data "ssh_data" "inspect" {
  timeout = "2m"

  file {
    key  = "seed"
    path = var.seed_file
    sudo = true
  }

  command {
    key     = "uname"
    command = ["uname", "-s"]
    index   = 1
  }

  run {
    key    = "run"
    script = "printf '%s' \"$RUN_TOKEN\""
    shell  = "sh"
    envs   = { RUN_TOKEN = var.run_token }
    index  = 2
  }

  command {
    key     = "padded"
    command = ["printf", "  padded  "]
    index   = 4
  }

  command {
    key     = "keep_newline"
    command = ["printf", "keep-newline\n"]
    trim    = false
    index   = 5
  }
}

# A partial JSON override: host and user are explicit, everything else inherits.
data "ssh_data" "override" {
  conn = jsonencode({
    host = var.host
    user = var.ssh_user
  })

  command {
    key     = "override"
    command = ["printf", "%s", "json-override"]
  }
}

# Provider function helper: no network, no provider read, no key material.
data "ssh_data" "function" {
  conn = provider::ssh::connection(var.host, var.ssh_user, var.port)

  command {
    key     = "function"
    command = ["printf", "%s", "function-value"]
  }
}

# Results from this surface are marked sensitive and their error details
# suppress remote stderr. The data source still persists the results in state.
data "ssh_sensitive_data" "inspect_secret" {
  command {
    key     = "sensitive_seed"
    command = ["cat", var.seed_file]
    sudo    = true
    index   = 3
  }

  command {
    key     = "sensitive_padded"
    command = ["printf", "  secret-padded  "]
    index   = 4
  }
}

# Managed entirely below /root while connecting as the non-root e2e user. Every
# parent creation, write, chmod, chown, read, and removal needs sudo -n.
resource "ssh_resource" "root_paths" {
  umask = "022"

  directory {
    path = "${var.root_base}/dir"
    mode = "0750"
  }

  file {
    path    = "${var.root_base}/dir/plain.txt"
    content = "plain-content\n"
    mode    = "0640"
  }

  file {
    path       = "${var.root_base}/dir/secret.txt"
    content_wo = ephemeral.ssh_data.writeonly.values["secret"]
    mode       = "0600"
  }
}

# A second resource under /tmp uses the ordinary non-sudo path and explicit
# ownership. It also uses a JSON override while inheriting the private key.
resource "ssh_resource" "public_paths" {
  conn = jsonencode({
    user = var.ssh_user
  })

  owner = "e2e"
  group = "e2e"
  umask = "022"

  directory {
    path = "${var.public_base}/dir"
    mode = "0755"
  }

  file {
    path    = "${var.public_base}/dir/hello.txt"
    content = "hello-world\n"
    mode    = "0644"
  }
}

output "seed_value" {
  value = data.ssh_data.inspect.values["seed"]
}

output "uname_value" {
  value = data.ssh_data.inspect.values["uname"]
}

output "run_value" {
  value = data.ssh_data.inspect.values["run"]
}

output "padded_value" {
  value = data.ssh_data.inspect.values["padded"]
}

output "keep_newline_value" {
  value = data.ssh_data.inspect.values["keep_newline"]
}

output "override_value" {
  value = data.ssh_data.override.values["override"]
}

output "function_value" {
  value = data.ssh_data.function.values["function"]
}

output "sensitive_seed_value" {
  value     = data.ssh_sensitive_data.inspect_secret.values["sensitive_seed"]
  sensitive = true
}

output "sensitive_padded_value" {
  value     = data.ssh_sensitive_data.inspect_secret.values["sensitive_padded"]
  sensitive = true
}

output "wo_digest" {
  value = ssh_resource.root_paths.file.1.content_sha256
}
