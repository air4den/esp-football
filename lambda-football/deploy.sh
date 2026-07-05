#!/bin/bash
set -e

echo "Building..."
GOOS=linux GOARCH=arm64 go build -tags lambda.norpc -o bootstrap .
zip function.zip bootstrap

echo "Deploying..."
aws lambda update-function-code \
  --function-name esp-football \
  --zip-file fileb://function.zip \
  --region us-east-2

rm bootstrap function.zip
echo "Done!"
