#!/usr/bin/env bash
# Cross-compile the Lambda binary (provided.al2023, arm64).
#   -trimpath         빌드 머신의 절대경로를 바이너리에서 제거 (재현성 + 경로 노출 방지)
#   -ldflags="-s -w"  심볼·디버그 정보 제거 → 8.5MB → 5.7MB (업로드·콜드스타트 이득)
set -euo pipefail
cd "$(dirname "$0")"

GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -tags lambda.norpc -trimpath -ldflags="-s -w" -o bootstrap ./cmd/bot

echo "built ./bootstrap ($(du -h bootstrap | cut -f1))"
