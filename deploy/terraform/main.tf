data "aws_availability_zones" "available" {
  state = "available"
}

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical, commercial AWS regions
  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }
  filter {
    name   = "architecture"
    values = ["x86_64"]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
  filter {
    name   = "root-device-type"
    values = ["ebs"]
  }
}

locals {
  # Explicit allowlist: never upload runtime/, .env, state or credentials.
  bootstrap_files = [
    "compose.yaml", "compose.apps.yaml",
    "mysql/01-databases.sql",
    "scripts/install-docker.sh", "scripts/init-env.py", "scripts/stack.sh",
    "scripts/configure-nacos.py", "scripts/release.py",
    "scripts/capacity.py", "scripts/image_retention.py", "scripts/prepare-nacos-logging.py",
    "rocketmq/broker.conf", "rocketmq/topics.sh",
    "jenkins/Dockerfile", "jenkins/plugins.txt", "jenkins/init.groovy", "jenkins/Jenkinsfile",
  ]
  cloud_config = {
    write_files = [for relative_path in local.bootstrap_files : {
      path        = "/opt/grpctest/deploy/${relative_path}"
      owner       = "root:root"
      permissions = "0644"
      content     = file("${path.module}/../${relative_path}")
    }]
    runcmd = [["bash", "-c", <<-SCRIPT
      set -euo pipefail
      umask 077
      cd /opt/grpctest/deploy
      bash scripts/install-docker.sh
      python3 scripts/init-env.py
      bash scripts/stack.sh check
      bash scripts/stack.sh up
      docker compose --env-file .env -f compose.yaml exec -T mqbroker sh -s < rocketmq/topics.sh
      python3 scripts/configure-nacos.py --apply
      install -d /var/lib/grpctest
      touch /var/lib/grpctest/infra-ready
    SCRIPT
    ]]
  }
  # cloud-init accepts gzip; credentials are generated on the server, not in user data.
  user_data_base64 = base64gzip("#cloud-config\n${yamlencode(local.cloud_config)}")
}

resource "aws_vpc" "main" {
  cidr_block           = "10.42.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = var.name }
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
}

resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.main.id
  cidr_block              = "10.42.1.0/24"
  availability_zone       = data.aws_availability_zones.available.names[0]
  map_public_ip_on_launch = true
  tags                    = { Name = "${var.name}-public" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

resource "aws_security_group" "main" {
  name_prefix = "${var.name}-"
  description = "Single-host learning environment; SSH only from the specified administrator IP"
  vpc_id      = aws_vpc.main.id
  ingress {
    description = "Administrator SSH"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [var.admin_cidr]
  }
  egress {
    description = "Package and image downloads"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_key_pair" "main" {
  key_name_prefix = "${var.name}-"
  public_key      = trimspace(file(pathexpand(var.ssh_public_key_path)))
}

resource "aws_instance" "main" {
  ami                         = data.aws_ami.ubuntu.id
  instance_type               = var.instance_type
  subnet_id                   = aws_subnet.public.id
  associate_public_ip_address = true
  vpc_security_group_ids      = [aws_security_group.main.id]
  key_name                    = aws_key_pair.main.key_name
  user_data_base64            = local.user_data_base64
  disable_api_termination     = true

  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
  }
  credit_specification {
    cpu_credits = "standard"
  }
  root_block_device {
    volume_type           = "gp3"
    volume_size           = var.disk_gib
    encrypted             = true
    delete_on_termination = false
    tags                  = { Name = "${var.name}-data" }
  }

  # Ensure package downloads can reach the internet during first boot.
  depends_on = [aws_route_table_association.public]
  tags       = { Name = var.name }

  lifecycle {
    prevent_destroy = true
    # Do not replace the database host when Canonical releases another AMI or
    # when local deployment scripts change. user data is for the first boot only.
    ignore_changes = [ami, user_data_base64]
    precondition {
      # AWS accepts at most 16 KiB raw (here gzip-compressed) EC2 user data.
      condition     = length(local.user_data_base64) <= 21844
      error_message = "Compressed bootstrap exceeds the EC2 user-data limit; reduce bundled files."
    }
  }
}
