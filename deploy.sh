#!/usr/bin/env bash
# Build and deploy with Serverless Framework.
#
# 시크릿은 .env 파일에서 읽는다 (.gitignore로 커밋 제외됨).
# .env가 없으면 현재 셸의 환경변수를 그대로 쓴다 (CI 등).
set -euo pipefail
cd "$(dirname "$0")"

if [ -f .env ]; then
  set -a
  # shellcheck source=/dev/null
  source .env
  set +a
  echo "loaded .env"
fi

: "${SLACK_SIGNING_SECRET:?빠짐 — .env에 SLACK_SIGNING_SECRET을 넣어줘 (Basic Information → Signing Secret)}"
: "${SLACK_BOT_TOKEN:?빠짐 — .env에 SLACK_BOT_TOKEN을 넣어줘 (OAuth & Permissions → xoxb-...)}"
export GITHUB_TOKEN="${GITHUB_TOKEN:-}"

./build.sh
npx serverless deploy "$@"
SH
chmod +x deploy.sh build.sh