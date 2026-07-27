#!/usr/bin/env bash
# Cross-compile the Lambda binary (provided.al2023, arm64).
set -euo pipefail
cd "$(dirname "$0")"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -o bootstrap ./cmd/bot
echo "built ./bootstrap"
