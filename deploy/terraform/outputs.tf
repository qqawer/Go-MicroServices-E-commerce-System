output "instance_id" {
  value = aws_instance.main.id
}

output "public_ip" {
  value = aws_instance.main.public_ip
}

output "security_group_id" {
  value = aws_security_group.main.id
}

output "ssh_destination" {
  value = "ubuntu@${aws_instance.main.public_ip}"
}

output "next_step" {
  value = "Connect over SSH using your private key, then run: sudo cloud-init status --wait && sudo test -f /var/lib/grpctest/infra-ready"
}
