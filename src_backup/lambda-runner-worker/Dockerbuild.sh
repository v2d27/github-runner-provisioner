#!/bin/bash

AWS_ECR_URI=$1

docker build --platform linux/amd64 -f ./Dockerfile.lambda  -t docker-image:test .

docker tag docker-image:test $AWS_ECR_URI

docker push $AWS_ECR_URI
