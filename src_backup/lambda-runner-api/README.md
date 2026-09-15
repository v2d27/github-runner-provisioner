# Autorunner - API
- Autorunner - API, 

## How to deploy ?

**Step 1: Provide GitHub personal access token with runner access privilege**
- Open `variables.tf`, change default value of two variables below:

```terraform
variable "github-token" {
    type = string
    default = "your-github-access-token"
    description = "Your PAT with runner access permissions"
}


variable "github-org" {
    type = string
    default = "your-github-org"
    description = "Your GitHub organization"
}
```
- Optional: You can change region variable to your working region.

**Step 2: Deploy to AWS**
```bash
terraform init
terraform apply --auto-approve
```

## How to use ?
- Go to AWS Lambda, select `autorunner` function
- Select Config tab, and copy lambda function gateway URL
- Change `API_GATEWAY_URL` to your lambda function gateway URL

```bash
API_GATEWAY_URL="https://<your_id>.execute-api.ap-southeast-1.amazonaws.com/runner"

curl -X POST $API_GATEWAY_URL/request \
    -H "Authorization: vanduc2708" \
    -H "Number: 2"
```

- Example some results:
```json
{"request": 2, "pending": 0, "available": 0, "create_node": 1, "need_more_runner": 2, "free_runner_left": 0, "conclustion": "creating new runner"}
{"request": 3, "pending": 1, "available": 0, "create_node": 1, "need_more_runner": 2, "free_runner_left": 0, "conclustion": "creating new runner"}
{"request": 3, "pending": 0, "available": 0, "create_node": 2, "need_more_runner": 3, "free_runner_left": 1, "conclustion": "creating new runner"}
```
