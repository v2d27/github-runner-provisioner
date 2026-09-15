import json
import os
from datetime import datetime
import requests
import numpy as np

VANDUC_RUNNER = "AutoRunner-vanduc" # Must be the same with Terraform
EC2_MAX_INIT_TIME = 120 # seconds
RUNNER_INIT_JOB_TIME = 20 # seconds
ENABLE_CREATE_RUNNER = True
RUNNER_WORKER = "https://6htfyo4y2kv7l724runysejnmq0lzysk.lambda-url.ap-southeast-1.on.aws"

GITHUB_TOKEN = os.getenv("GITHUB_TOKEN")
GITHUB_ORG = os.getenv("GITHUB_ORG")

HISTORY = np.array([["runner-name", "time"]], dtype=str)
CREATING = np.array([["create_node", "need_more_runner", "free_runner_left", "time"]], dtype=str)


def lambda_handler(event, context):
    global HISTORY
    global CREATING

    # Handle header 
    headers = event.get('headers', {})
    authorization = headers.get('authorization')
    if authorization != 'vanduc2708': return response(401, 'Unauthorized! Who you are?')
    number = headers.get('number')
    if not number: return response(404, 'Please provide the number of runner')
    print(f"==> New runner request with header: {authorization}, number runner: {number}")

    # Calc available GitHub runner
    available_runner = 0
    listRunners = getGitHubRunners()
    # print(listRunners)

    if type(listRunners) != list:
        print(f"listRunners error: {listRunners}")
    else:
        for runner in listRunners:
            found = False
            for i in range(1, len(HISTORY)):
                now = datetime.now()
                diff = (now - datetimeToObject(HISTORY[i][1])).total_seconds()
                if HISTORY[i][0] == runner:
                    found = True
                    if diff >= RUNNER_INIT_JOB_TIME:
                        available_runner += 1
                        HISTORY[i][1] = datetimeToString(now)
                        print(f"=> HISTORY[{i}] = [{HISTORY[i][0]}, {HISTORY[i][1]}] => diff_time: {diff} => Select")
                    else:
                        print(f"=> HISTORY[{i}] = [{HISTORY[i][0]}, {HISTORY[i][1]}] => diff_time: {diff} => Skip")
            if not found:
                print(f"==> Add to HISTORY: {runner}")
                HISTORY = np.append(HISTORY, np.array([[runner, datetimeToString(datetime.now())]], dtype=str), axis=0)
                available_runner += 1

        # Clean outdated runner
        mask = np.isin(HISTORY[:, 0], listRunners) # Create mask to keep rows where first element (runner) is in listRunners
        mask[0] = True
        HISTORY = HISTORY[mask] # Apply mask to HISTORY to keep only valid rows
        # print(HISTORY)

    if available_runner == int(number): 
        return response(200, {
            "request": int(number),
            "available": available_runner,
            "conclustion": "use available runner"
        })

    # Check pending list
    count_pending_runner = 0
    if len(CREATING) >= 1:
        for i in range(1, len(CREATING)):
            if int(CREATING[i][2]) > 0:
                diff = (datetime.now() - datetimeToObject(CREATING[i][3])).total_seconds()
                if diff <= EC2_MAX_INIT_TIME:
                    print(f"=> Pending CREATING[{i}] = [{CREATING[i][2]}, {CREATING[i][3]}] => diff_time: {diff} => Select")
                    count_pending_runner += int(CREATING[i][2])
                    CREATING[i][2] = 0
                    if count_pending_runner == int(number): 
                        break
                    elif count_pending_runner > int(number):
                        CREATING[i][2] = count_pending_runner - int(number)
                        break
                else:
                    print(f"=> Pending CREATING[{i}] = [{CREATING[i][2]}, {CREATING[i][3]}] => diff_time: {diff} => Delete")
                    CREATING[i] = ["", "", "", ""]

    # Clean created runner
    mask = CREATING[:, 0] != ""
    CREATING = CREATING[mask]

    if count_pending_runner + available_runner < int(number):
        need_more_runner = int(number) - count_pending_runner - available_runner
        create_node = int(need_more_runner / 2) + int(need_more_runner % 2)
        free_runner_left = create_node * 2 - need_more_runner
        # "create_node", "need_more_runner", "free_runner_left", "time" 
        CREATING = np.append(CREATING, np.array([[create_node, need_more_runner, free_runner_left, datetimeToString(datetime.now())]], dtype=str), axis=0)
        
        for i in range(0, create_node, 1):
            try:
                if ENABLE_CREATE_RUNNER:
                    requests.get(RUNNER_WORKER, timeout=1)
                print(f"==> Created {i+1}/{create_node} node")
            except requests.RequestException as error:
                continue

        return response(200, {
            "request": int(number),
            "pending": count_pending_runner,
            "available": available_runner,
            "create_node": create_node,
            "need_more_runner": need_more_runner,
            "free_runner_left": free_runner_left,
            "conclustion": "creating new runner"
        })
    else: 
        return response(200, {
            "request": int(number),
            "pending": count_pending_runner,
            "available": available_runner,
            "conclustion": "use pending/available runner"
        })

def datetimeToString(objectDateTime):
    return objectDateTime.strftime("%Y-%m-%d %H:%M:%S")

def datetimeToObject(strDateTime):
    return datetime.strptime(strDateTime, "%Y-%m-%d %H:%M:%S")

def response(status_code, body):
    data = {
        "statusCode": status_code,
        "body": json.dumps(body),
        'headers': {
            'Content-Type': 'application/json'
        }
    }
    print(data)
    return data

def getGitHubRunners():
    url = f"https://api.github.com/orgs/{GITHUB_ORG}/actions/runners"

    # Headers with authentication
    headers = {
        "Authorization": f"Bearer {GITHUB_TOKEN}",
        "Accept": "application/vnd.github+json"
    }

    # Send GET request
    response = requests.get(url, headers=headers)
    result = []
    # Check if the request was successful
    if response.status_code == 200:
        runners = response.json()
        for runner in runners.get("runners", []):
            if runner['status'] == "online" and not runner['busy']: # Having runner available
                if VANDUC_RUNNER in runner['name']: 
                    result.append(runner['name'])
            else:
                print(f"Skip {runner['name']}: status={runner['status']}, busy={runner['busy']}")
                
    else:
        return f"Failed to fetch runners. Status code: {response.status_code}, Response: {response.text}"
    return result
