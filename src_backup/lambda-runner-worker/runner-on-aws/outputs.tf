output "runner_name" {
  value = local.name
}

output "key_pair" {
  value = local.key_pair
}

output "runner_ip" {
  value = aws_instance.this.public_ip
}