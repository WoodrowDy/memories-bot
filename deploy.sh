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

# 아래 둘은 없어도 배포된다 — 일부러 강제하지 않는다.
# ANTHROPIC_API_KEY가 비면 자연어 브레인이 꺼지고 1차 키워드 검색으로 동작한다.
export ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}"
if [ -z "$ANTHROPIC_API_KEY" ]; then
  echo "note: ANTHROPIC_API_KEY 없음 → 키워드 검색 모드로 배포됨 (자연어 브레인 off)"
fi

# 깨진 코드를 슬랙에 붙은 채로 배포하지 않도록 게이트
go vet ./...
go test ./...

./build.sh
npx serverless deploy "$@"
