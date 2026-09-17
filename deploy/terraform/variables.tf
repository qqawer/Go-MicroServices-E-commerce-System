variable "aws_region" {
  type        = string
  description = "AWS region where the instance will be created, e.g. ap-southeast-1."
}

variable "aws_account_id" {
  type        = string
  description = "Expected 12-digit AWS account ID; prevents creating resources in another account."
  validation {
    condition     = can(regex("^[0-9]{12}$", var.aws_account_id))
    error_message = "Supply the 12-digit Account returned by aws sts get-caller-identity."
  }
}

variable "admin_cidr" {
  type        = string
  description = "Your current public IPv4 followed by /32; the only allowed SSH source."
  validation {
    condition     = can(cidrnetmask(var.admin_cidr)) && can(regex("/32$", var.admin_cidr))
    error_message = "Use a single IPv4 /32, not 0.0.0.0/0."
  }
}

variable "ssh_public_key_path" {
  type        = string
  description = "Path to an existing OpenSSH PUBLIC key (.pub). Private keys are never uploaded to AWS or Terraform state."
  validation {
    condition     = can(regex("^(ssh-ed25519|ssh-rsa) ", trimspace(file(pathexpand(var.ssh_public_key_path)))))
    error_message = "Supply an existing OpenSSH .pub file (ed25519 or RSA)."
  }
}

variable "name" {
  type    = string
  default = "grpctest-demo"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,30}$", var.name))
    error_message = "Use 3-31 lowercase letters, digits or hyphens, starting with a letter."
  }
}

variable "instance_type" {
  type        = string
  default     = "t3.large"
  description = "x86 T3 instance; default has 2 vCPU and 8 GiB memory. This creates a paid instance."
  validation {
    condition     = contains(["t3.medium", "t3.large", "t3.xlarge", "t3a.medium", "t3a.large", "t3a.xlarge"], var.instance_type)
    error_message = "This stack supports these x86 T3/T3a sizes; ARM instances require a different build target and AMI."
  }
}

variable "disk_gib" {
  type    = number
  default = 60
  validation {
    condition     = var.disk_gib >= 40 && var.disk_gib <= 1024 && floor(var.disk_gib) == var.disk_gib
    error_message = "Choose a whole number between 40 and 1024 GiB."
  }
}
