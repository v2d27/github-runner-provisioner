#!/bin/bash

# syntax \$\{...\} => is used by Terraform
# it is not available here for bash shell

echo "${INSTANCE_NAME}" > instance_name.txt
echo "STARTUP_DIR=$(pwd)
STARTUP_USER=$(whoami)" > startup.txt
echo "false" > /shutdowncheck.txt

# Change timezone
sudo timedatectl set-timezone Asia/Ho_Chi_Minh

# CronJob self-destroy => run each 2 minutes
echo '#!/bin/bash
echo "$(date) self-destroy is called through cronjob" >> /logs.txt

# Env variables
GITHUB_PAT=$(jq -r '.GITHUB_PAT' /self-destroy.json)
ORG_URI_NAME=$(jq -r '.ORG_URI_NAME' /self-destroy.json)
RUNNER_NAME1=$(jq -r '.RUNNER_NAME1' /self-destroy.json)
RUNNER_NAME2=$(jq -r '.RUNNER_NAME2' /self-destroy.json)

# Fetching data from github
JSONFILE="./runner.json"
curl -H "Authorization: Bearer $GITHUB_PAT" -H "Accept: application/vnd.github.v3+json" "https://api.github.com/orgs/$ORG_URI_NAME/actions/runners" -o $JSONFILE

if [ ! -f "$JSONFILE" ]; then
    echo "File $JSONFILE does not exist." >> /logs.txt
    exit 1
fi

SHUTDOWN_FIRSTCHECK_ISOKEY="$(cat /shutdowncheck.txt)"

status1=$(jq --arg rname "$RUNNER_NAME1" -r ".runners[] | select(.name == \$rname) | .status" $JSONFILE)
status2=$(jq --arg rname "$RUNNER_NAME2" -r ".runners[] | select(.name == \$rname) | .status" $JSONFILE)
busy1=$(jq --arg rname "$RUNNER_NAME1" -r ".runners[] | select(.name == \$rname) | .busy" $JSONFILE)
busy2=$(jq --arg rname "$RUNNER_NAME2" -r ".runners[] | select(.name == \$rname) | .busy" $JSONFILE)

if [ "$status1" == "$status2" ] && [ "$status1" == "online" ] && [ "$busy1" == "$busy2" ] && [ "$busy1" == "false" ]; then
    echo "All status are the same: status1=$status1, status2=$status2, busy1=$busy1, busy2=$busy2" >> /logs.txt
    if [ "$SHUTDOWN_FIRSTCHECK_ISOKEY" == "false" ]; then
        echo "true" > /shutdowncheck.txt
        echo "SHUTDOWN_FIRSTCHECK_ISOKEY=true > /shutdowncheck.txt" >> /logs.txt
    else
        id1=$(jq --arg rname "$RUNNER_NAME1" -r ".runners[] | select(.name == \$rname) | .id" $JSONFILE)
        id2=$(jq --arg rname "$RUNNER_NAME2" -r ".runners[] | select(.name == \$rname) | .id" $JSONFILE)
        echo "Deleting $RUNNER_NAME1" >> /logs.txt
        curl -X DELETE  -H "Authorization: Bearer $GITHUB_PAT" \
                        -H "Accept: application/vnd.github+json" \
                        "https://api.github.com/orgs/$ORG_URI_NAME/actions/runners/$id1"
        sleep 2
        echo "Deleting $RUNNER_NAME2" >> /logs.txt
        curl -X DELETE  -H "Authorization: Bearer $GITHUB_PAT" \
                        -H "Accept: application/vnd.github+json" \
                        "https://api.github.com/orgs/$ORG_URI_NAME/actions/runners/$id2"
        sleep 2
        sudo shutdown now
    fi
else
    echo "Prevent shutdown. All status are different: status1=$status1, status2=$status2, busy1=$busy1, busy2=$busy2" >> /logs.txt
    echo "false" > /shutdowncheck.txt
    sudo shutdown -c
fi
cat /logs.txt' > self-destroy.sh
chmod +x self-destroy.sh
echo "*/2 * * * * bash /self-destroy.sh >> /self-destroy.log 2>&1" > cronjobdestroy
crontab -u root cronjobdestroy
crontab -l


# Variables
GITHUB_PAT="your-github-PAT"
ORG_URI_NAME="your-org-name"
RUNNER_DIR="actions-runner"
RUNNER_VERSION="2.285.0"
RUNNER_OS="linux"
RUNNER_ARCH="x64"
RUNNER_PACKAGE="actions-runner.tar.gz"
RUNNER_NAME1="${INSTANCE_NAME}-$(openssl rand -hex 4)"
RUNNER_NAME2="${INSTANCE_NAME}-$(openssl rand -hex 4)"
RUNNER_LABELS="my-aws-runner,vanduc-aws-runner"

# Init variable for first time startup
echo "{
    \"GITHUB_PAT\":\"$GITHUB_PAT\",
    \"ORG_URI_NAME\":\"$ORG_URI_NAME\",
    \"RUNNER_NAME1\":\"$RUNNER_NAME1\",
    \"RUNNER_NAME2\":\"$RUNNER_NAME2\"
}" > self-destroy.json

# Expand RAM from 3.72Gb to 8GB
sudo fallocate -l 4G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
sudo swapon --show

# Creating 2 user for each github runner
sudo useradd -m -d /home/user1 -s /bin/bash -p $(openssl passwd -1 "1234") user1
sudo useradd -m -d /home/user2 -s /bin/bash -p $(openssl passwd -1 "1234") user2
RUNNER_DIR1="/home/user1/actions-runner"
RUNNER_DIR2="/home/user2/actions-runner"
sudo -u user1 mkdir $RUNNER_DIR1
sudo -u user2 mkdir $RUNNER_DIR2

# # Install AWS CLI
# echo '#!/bin/bash
# curl "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "awscliv2.zip"
# sudo apt install unzip -y
# unzip awscliv2.zip
# sudo bash ./aws/install' > /install-aws-cli.sh
# sudo nohup bash /install-aws-cli.sh > /dev/null 2>&1 &

# Install latest Docker
echo '#!/bin/bash
sudo apt install -y apt-transport-https ca-certificates curl software-properties-common
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt update -y
sudo apt install docker-ce -y
sudo systemctl start docker
sudo systemctl enable docker
sudo curl -L "https://github.com/docker/compose/releases/latest/download/docker-compose-$(uname -s)-$(uname -m)" -o /usr/local/bin/docker-compose
sudo chmod +x /usr/local/bin/docker-compose
docker -v
docker-compose -v
sudo usermod -aG docker user1
sudo usermod -aG docker user2
sudo newgrp docker' > /install-docker.sh
bash /install-docker.sh

# Obtain registration token
REG_TOKEN1=$(curl -X POST -H "Authorization: Bearer $GITHUB_PAT" -H "Accept: application/vnd.github.v3+json" \
    https://api.github.com/orgs/$ORG_URI_NAME/actions/runners/registration-token | jq -r .token )

sleep 1

REG_TOKEN2=$(curl -X POST -H "Authorization: Bearer $GITHUB_PAT" -H "Accept: application/vnd.github.v3+json" \
    https://api.github.com/orgs/$ORG_URI_NAME/actions/runners/registration-token | jq -r .token )


# Download runner package
sudo curl -o $RUNNER_PACKAGE -L https://github.com/actions/runner/releases/download/v2.321.0/actions-runner-linux-x64-2.321.0.tar.gz
sudo cp $RUNNER_PACKAGE $RUNNER_DIR1
sudo chown user1:user1 $RUNNER_DIR1/$RUNNER_PACKAGE
sudo cp $RUNNER_PACKAGE $RUNNER_DIR2
sudo chown user2:user2 $RUNNER_DIR2/$RUNNER_PACKAGE

# Extract package
sudo sudo -u user1 tar xzf "$RUNNER_DIR1/$RUNNER_PACKAGE" -C $RUNNER_DIR1
sudo sudo -u user2 tar xzf "$RUNNER_DIR2/$RUNNER_PACKAGE" -C $RUNNER_DIR2

# Configure runner
sudo sudo -u user1 bash $RUNNER_DIR1/config.sh --url https://github.com/$ORG_URI_NAME --token $REG_TOKEN1 --name $RUNNER_NAME1 --labels $RUNNER_LABELS --unattended
sudo sudo -u user2 bash $RUNNER_DIR2/config.sh --url https://github.com/$ORG_URI_NAME --token $REG_TOKEN2 --name $RUNNER_NAME2 --labels $RUNNER_LABELS --unattended

# Register Runner 1 service
echo "[Unit]
Description=GitHub Actions Runner 1
After=network.target

[Service]
User=user1
WorkingDirectory=/home/user1/
ExecStart=$RUNNER_DIR1/run.sh
Restart=always

[Install]
WantedBy=multi-user.target" > /etc/systemd/system/github-runner-1.service

# Register Runner 2 service
echo "[Unit]
Description=GitHub Actions Runner 2
After=network.target

[Service]
User=user2
WorkingDirectory=/home/user2/
ExecStart=$RUNNER_DIR2/run.sh
Restart=always

[Install]
WantedBy=multi-user.target" > /etc/systemd/system/github-runner-2.service

# Start runner
sudo systemctl start github-runner-1
sudo systemctl start github-runner-2
sudo systemctl enable github-runner-1
sudo systemctl enable github-runner-2

# Wait starting
# echo '#!/bin/bash
# # Wait docker installed
# check_docker_installed() {
#   if docker -v &>/dev/null; then
#     echo "Docker is installed."
#     return 0
#   else
#     echo "Docker is not installed."
#     return 1
#   fi
# }
# check_aws_cli_installed() {
#   if aws --version &>/dev/null; then
#     echo "AWS CLI is installed."
#     return 0
#   else
#     echo "AWS CLI is not installed."
#     return 1
#   fi
# }
# # Loop to check both installations
# while true; do
#   docker_installed=false
#   aws_installed=false
#   # Check Docker
#   check_docker_installed
#   if [ $? -eq 0 ]; then
#     docker_installed=true
#   fi
#   # Check AWS CLI
#   check_aws_cli_installed
#   if [ $? -eq 0 ]; then
#     aws_installed=true
#   fi
#   # Exit loop if both are installed
#   if $docker_installed && $aws_installed; then
#     echo "Both Docker and AWS CLI are installed. Exiting loop."
#     break
#   fi
#   echo "Retrying in 5 seconds..."
#   sleep 5
# done
# sudo systemctl start github-runner-1
# sudo systemctl start github-runner-2
# sudo systemctl enable github-runner-1
# sudo systemctl enable github-runner-2' > /wait-starting.sh
# bash /wait-starting.sh


