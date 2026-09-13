# Autorunner - Worker
- Autorunner - It takes respondsibility to create EC2, and register 2 GitHub runner/instance through Terraform. 
We choose deploy seperately worker for Terraform to apply because creating progress and preparing dependencies take many time to complete. 
- AWS does not support to run terraform on lambda directly. So, we will deploy docker image for lambda function with terraform inside.
- To build a Docker image for Lambda, we must follow AWS standards and use their official image. This project uses Node.js to meet the requirements of the Lambda environment.

## How to deploy ?

**Step 1: Provide your AWS token**
- Open `Dockerfile.lambda` to modify following environmental variables:

```shell
ENV AWS_ACCESS_KEY_ID=<your-access-key>
ENV AWS_SECRET_ACCESS_KEY=<your-secret-key>
ENV AWS_REGION=<your-region>
```
> Recommend: You should create new user with limited permissions.

**Step 2: Prepare Docker Registry**
- You can use AWS Elastic Container Registry (AWS ECR) to store docker image.
- Creating new ECR repository.
- Login to your AWS ECR.

```bash
aws ecr get-login-password --region your-region | docker login --username AWS --password-stdin your-account-id.dkr.ecr.your-region.amazonaws.com
```

**Step 3: Build & Push your docker image**
```bash
bash ./Dockerbuild.sh your-account-id.dkr.ecr.your-region.amazonaws.com/your-ecr-repository:latest
```

**Step 4: Provision lambda function with docker image from AWS ECR**
- Go to AWS Lambda function, select Create new function with docker image
- Enter your ECR URI with tag `:latest`. You will get hardly to deploy in next time with sha256 tag name.
