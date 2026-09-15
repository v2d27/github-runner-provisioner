import { Context, APIGatewayProxyResult, APIGatewayEvent } from 'aws-lambda';
import { spawn, exec } from 'child_process';


// Function to execute a command and wait for it to complete
function runCommand(command) {
    return new Promise((resolve, reject) => {
      exec(command, (error, stdout, stderr) => {
        if (error) {
          reject(new Error(`Error executing command: ${error.message}`));
          return;
        }
        if (stderr) {
          reject(new Error(`stderr: ${stderr}`));
          return;
        }
        resolve(stdout);
      });
    });
  }

export const handler = async (event: APIGatewayEvent, context: Context): Promise<APIGatewayProxyResult> => {
    // console.log(`Event: ${JSON.stringify(event, null, 2)}`);
    // console.log(`Context: ${JSON.stringify(context, null, 2)}`);
    
    // Run the command
    // exec('cd ./terraform && terraform apply -auto-approve', { detached: true, stdio: 'ignore' });

    let errorMessage = ""
    try {
        const result = await runCommand('sh -c "cd ./terraform && terraform apply -auto-approve -lock=false"');
        console.log(`Terraform outputs: ${result}`);
    } catch (error) {
        console.error(`Error: ${error.message}`);
        errorMessage = error.message
    }

    return {
        statusCode: 200,
        body: JSON.stringify({
            org: 'your-org-name',
            success: `${errorMessage == "" ? true : false}`,
            error_message: `${errorMessage}`
        }),
    };
};