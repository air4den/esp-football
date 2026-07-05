# esp-football

A live football scoreboard for an ESP32 display. The device polls an AWS Lambda function that fetches live match data from the ESPN API for a configured team. 

## Lambda Function (`lambda-football/`)

A Go program that queries the ESPN API and returns a JSON payload with the current match state for the configured team. It is deployed as an AWS Lambda function with a public function URL.

### Run locally

```bash
cd lambda-football
go run -tags local .
```

### Deploy manually

Build for Lambda's ARM64 runtime and zip the binary:

```bash
GOOS=linux GOARCH=arm64 go build -tags lambda.norpc -o bootstrap .
zip function.zip bootstrap

aws lambda update-function-code \
  --function-name esp-football \
  --zip-file fileb://function.zip \
  --region us-east-2
```

### Deploy with the script

```bash
cd lambda-football
./deploy.sh
```

Builds, zips, deploys, and cleans up in one step. Requires the AWS CLI to be configured.

### GitHub Action

Pushing to `main` automatically builds and deploys the Lambda function via `.github/workflows/deploy.yml`.

This requires two repository secrets to be set in GitHub:

- `AWS_ACCESS_KEY_ID`
- `AWS_SECRET_ACCESS_KEY`

The IAM user behind these credentials needs `lambda:UpdateFunctionCode` permission on the `esp-football` function.
