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

data "ssh_conn" "target" {
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
# never reaches plan or state.
ephemeral "ssh_data" "writeonly" {
  conn = data.ssh_conn.target.conn

  file {
    key       = "secret"
    path      = var.secret_file
    sudo      = true
    sensitive = true
  }
}

data "ssh_data" "inspect" {
  conn    = data.ssh_conn.target.conn
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
    key       = "sensitive_seed"
    command   = ["cat", var.seed_file]
    sudo      = true
    sensitive = true
    index     = 3
  }
}

# Managed entirely below /root while connecting as the non-root e2e user. Every
# parent creation, write, chmod, chown, read, and removal needs sudo -n.
resource "ssh_resource" "root_paths" {
  conn  = data.ssh_conn.target.conn
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
    content_wo = ephemeral.ssh_data.writeonly.sensitive_values["secret"]
    mode       = "0600"
  }
}

# A second resource under /tmp uses the ordinary non-sudo path and explicit
# ownership.
resource "ssh_resource" "public_paths" {
  conn  = data.ssh_conn.target.conn
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

output "sensitive_seed_value" {
  value     = data.ssh_data.inspect.sensitive_values["sensitive_seed"]
  sensitive = true
}

output "wo_digest" {
  value = ssh_resource.root_paths.file.1.content_sha256
}
