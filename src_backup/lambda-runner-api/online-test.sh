#!/bin/bash

API_GATEWAY_URL="https://<your_id>.execute-api.ap-southeast-1.amazonaws.com/runner"

# for i in {1..3}:
# do
    curl -X POST $API_GATEWAY_URL/request \
        -H "Authorization: vanduc2708" \
        -H "Number: 1"
# done
