resource "random_string" "this" {
  length  = 6
  special = false
  upper   = false
  lower   = true
}

locals {
  vpc_id = data.aws_subnet.this.vpc_id
  subnet_id = data.aws_subnet.this.id
  name = "AutoRunner-vanduc-${random_string.this.result}"
  security_group_ids = ["sg-0bd78f993009d8c44"]
  key_pair = "github-action-runner"
  tags = {
    Name = local.name
    Type = "SPOT runner"
  }
}

# Define a Spot EC2 instance
resource "aws_instance" "this" {
  ami           = "ami-06650ca7ed78ff6fa"  # ubuntu server 24.04 LTS   
  instance_type = "m5.xlarge"              # Specify the EC2 instance type

  # Instance market option for Spot Instances
  instance_market_options {
    market_type = "spot"  # Set the market type to "spot"
    # spot_options {
    #   max_price = 0.0031
    # }
  }

  root_block_device {
    volume_size = 30 # 30GB
  }

  # Other common parameters
  subnet_id               = local.subnet_id
  vpc_security_group_ids  = local.security_group_ids
  key_name                = local.key_pair
  iam_instance_profile    = length(data.aws_iam_instance_profile.this.id) == 0 ? aws_iam_instance_profile.this[0].name : data.aws_iam_instance_profile.this.name

  # Entrypoint
  user_data = templatefile("entrypoint.sh", {
    INSTANCE_NAME = local.name
  })

  tags = local.tags

  lifecycle {
    create_before_destroy = true
    prevent_destroy       = true
  }
}



